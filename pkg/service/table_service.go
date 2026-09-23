// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/retry"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TableServicer manages the catalog of user-defined relational tables: the
// tables CREATE TABLE produces, as distinct from collections and from the
// fixed engine tables. See the doc comment on TableDef (table_types.go) for
// why collections are not reused for this.
type TableServicer interface {
	// CreateTable creates a new table in the given database. The table's
	// TableID is allocated from the database's TableID sequence and is
	// immutable for the table's lifetime; row and index keys are composed
	// from it rather than from Name.
	CreateTable(ctx context.Context, req *CreateTableRequest) (*TableDef, error)

	// DropTable removes a table's catalog entry, its generation counter and
	// its sequences, and publishes a ReclaimJob for its rows and index
	// entries, in one transaction; see TableService.DropTable.
	DropTable(ctx context.Context, req *DropTableRequest) error

	// RenameTable changes a table's name without moving any data: row and
	// index keys are composed from the table's immutable TableID, never
	// from its name, so a rename is a pure catalog compare-and-swap.
	RenameTable(ctx context.Context, req *RenameTableRequest) error

	// TruncateTable atomically advances a table's generation and, unless
	// resetSequence is false (PostgreSQL's TRUNCATE does not touch a
	// sequence), resets its AUTO_INCREMENT counter to zero -- both in ONE
	// Raft transaction, so neither ever applies without the other. It also
	// publishes a ReclaimJob naming the old generation's now-orphaned row
	// and index key prefixes, in the SAME transaction, so the background
	// ReclaimSweeper can delete them once no reader can still be using them
	// (see ReclaimQuiescence).
	// See TableDataPrefix/TableIndexPrefix for what "generation" changes,
	// and go-qrdb#547 for why this makes TRUNCATE TABLE O(1) rather than a
	// scan of every row. Returns the table's new definition, with
	// Generation already updated.
	TruncateTable(ctx context.Context, database, name string, resetSequence bool) (*TableDef, error)

	// GetTable returns a table's current definition. It performs a fresh
	// linearizable read on every call -- see the doc comment on
	// TableService for why no cache is kept.
	GetTable(ctx context.Context, database, name string) (*TableDef, error)

	// ListTables returns the names of every table in the given database.
	ListTables(ctx context.Context, database string) ([]string, error)

	// AlterTable applies one or more column changes to a table. Adding or
	// renaming a column is metadata-only and returns the updated
	// definition. Dropping or modifying a column requires re-encoding
	// existing rows; AlterTable journals that as a pending rewrite and
	// returns TableRewriteRequiredError rather than performing it, since
	// the rewrite executor is a later phase.
	AlterTable(ctx context.Context, req *AlterTableRequest) (*TableDef, error)

	// CreateIndex adds a secondary index to a table's catalog entry.
	CreateIndex(ctx context.Context, req *CreateTableIndexRequest) error

	// DropIndex removes a secondary index from a table's catalog entry and
	// publishes a ReclaimJob for its entries, in one transaction; see
	// TableService.DropIndex.
	DropIndex(ctx context.Context, database, table, index string) error

	// RenameIndex renames a secondary index. Entries are keyed by the
	// index's incarnation, never its name, so this is a pure catalog change;
	// see TableService.RenameIndex.
	RenameIndex(ctx context.Context, req *RenameTableIndexRequest) error

	// AddForeignKey appends a foreign key constraint to a table's catalog
	// entry. A foreign key name is unique across the whole database in
	// MySQL, not merely across one table, so this also rejects a name
	// already declared on any other table in the same database.
	AddForeignKey(ctx context.Context, req *CreateTableForeignKeyRequest) error

	// DropForeignKey removes a foreign key constraint from a table's
	// catalog entry.
	DropForeignKey(ctx context.Context, database, table, name string) error

	// UpdateForeignKey replaces an existing foreign key constraint in
	// place, preserving its position in the table's constraint list. The
	// replacement may carry a different name, which is how a constraint is
	// renamed, and is subject to the same database-wide name uniqueness
	// rule AddForeignKey applies.
	UpdateForeignKey(ctx context.Context, req *UpdateTableForeignKeyRequest) error

	// ReferencingForeignKeys returns every foreign key in database that
	// names parent as its parent table, together with the table declaring
	// it. This is the reverse of GetTable's declared-constraint list, which
	// the catalog is keyed for; answering it requires reading every table
	// in the database (see the implementation for how that is done in a
	// single scan).
	ReferencingForeignKeys(ctx context.Context, database, parent string) ([]TableForeignKey, error)

	// RewriteJournal returns the in-flight DDL rewrite journal for a table
	// together with the table's still-live (pre-rewrite) definition -- the
	// one its currently-stored rows still physically match, since the
	// opening compare-and-swap that wrote the journal deliberately leaves
	// the live catalog record untouched (see AlterTable). A caller (the
	// rewrite executor) uses the two together to resume an interrupted
	// rewrite: decode existing rows against current, skip any row already
	// re-encoded at journal.Target.SchemaVersion, re-encode the rest against
	// journal.Target, and finish with CompleteRewrite. Returns
	// TableNoRewriteInProgressError when no journal is present.
	RewriteJournal(ctx context.Context, database, name string) (journal *TableDDLJournal, current *TableDef, err error)

	// CompleteRewrite finalizes an in-flight rewrite: atomically replaces
	// the live catalog definition with the journaled target definition and
	// deletes the journal, in a single compare-and-swap keyed off the
	// journal's own revision so a racing completion (e.g. two nodes both
	// resuming the same interrupted rewrite) can only succeed once. Returns
	// TableNoRewriteInProgressError when no journal is present -- including
	// when a racing caller already completed it -- so callers can treat
	// that outcome as success rather than failure.
	CompleteRewrite(ctx context.Context, database, name string) (*TableDef, error)
}

// tableKVService is the narrow KV interface TableService needs for
// operations that do not require MVCC revision metadata: claiming a new
// table's metadata key exactly once (PutIfAbsent), reading the current
// metadata or DDL-journal value with a plain linearizable read (Get), and
// enumerating a database's tables (List).
type tableKVService interface {
	PutIfAbsent(ctx context.Context, key string, value []byte) (stored bool, err error)
	Get(ctx context.Context, key string) ([]byte, error)
	List(ctx context.Context, prefix string) ([]string, error)

	// Scan reads every key/value under prefix in one call. It backs the
	// database-wide catalog reads foreign keys need -- reverse lookup by
	// parent table, and database-wide constraint name uniqueness -- which
	// would otherwise cost one round trip per table in the database.
	Scan(ctx context.Context, prefix string) (map[string][]byte, error)
}

// tableMVCCService is the narrow MVCC read interface TableService needs to
// observe a catalog record's ModRevision before proposing a
// compare-and-swap mutation against it.
type tableMVCCService interface {
	GetRevision(ctx context.Context, key string) (*KeyValue, error)
}

// TableService implements TableServicer backed by a KVServicer/MVCCKVServicer
// pair (for reads) and a TransactionServicer (for atomic catalog mutation),
// with TableID allocation delegated to a SequenceServicer.
//
// No caching. GetTable and ListTables read the catalog on every call rather
// than serving from an in-memory cache. KVServicer.Get and
// MVCCKVServicer.GetRevision are linearizable reads through Raft, so a
// CREATE TABLE committed on the leader is immediately visible to the very
// next GetTable issued against any follower -- the same property that
// already makes collections coherent without a cache. A cache in front of
// this service is a deliberate later optimization, not a correctness
// requirement, and would need its own invalidation story keyed off
// tableCatalogEpochKey (bumped by every mutation in this file for exactly
// that purpose).
type TableService struct {
	kv   tableKVService
	mvcc tableMVCCService
	txn  TransactionServicer
	seq  SequenceServicer

	// reclaimIndex configures where reclaim-publishing operations
	// (TruncateTable, DropIndex, DropTable) write their ReclaimIndexEntry. nil means
	// they refuse rather than publish a job the sweeper could never find:
	// see WithReclaimIndex.
	reclaimIndex *tableReclaimIndex
}

// tableReclaimIndex holds what a reclaim-publishing operation needs to
// publish a job's index entry -- see WithReclaimIndex.
type tableReclaimIndex struct {
	// txn proposes against the SYSTEM store, un-namespaced: the one place
	// ReclaimSweeper lists. It is never this TableService's own txn, which
	// may be namespaced or a different store altogether.
	txn TransactionServicer

	// placement and origin are recorded on every entry so the sweeper knows
	// which node owns the job (see ReclaimStoreResolver.Owns).
	placement StorePlacement
	origin    string
}

// TableServiceOption configures optional TableService behavior not every
// composition needs -- currently only WithReclaimIndex.
type TableServiceOption func(*TableService)

// WithReclaimIndex configures reclaim-publishing operations to write their
// ReclaimIndexEntry through systemTxn -- a TransactionServicer on the
// un-namespaced system store -- recording placement (where THIS
// TableService's own store lives) and origin (this node's identity). See
// ReclaimIndexEntry for why the entry is always its own, earlier
// transaction. The composer wiring TableService (pkg/server's registry) is
// the only caller that knows all three.
func WithReclaimIndex(systemTxn TransactionServicer, placement StorePlacement, origin string) TableServiceOption {
	return func(s *TableService) {
		s.reclaimIndex = &tableReclaimIndex{txn: systemTxn, placement: placement, origin: origin}
	}
}

// Compile-time interface check.
var _ TableServicer = (*TableService)(nil)

// NewTableService creates a new TableServicer backed by the given KV read
// interface, MVCC read interface, TransactionServicer, and SequenceServicer.
func NewTableService(kv tableKVService, mvcc tableMVCCService, txn TransactionServicer, seq SequenceServicer, opts ...TableServiceOption) *TableService {
	s := &TableService{kv: kv, mvcc: mvcc, txn: txn, seq: seq}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// CreateTable creates a new table. TableID is allocated first from the
// database's tableIDSequenceName sequence, then the definition is claimed
// with a single PutIfAbsent -- the only write with a defined outcome under
// concurrency (see KVServicer.PutIfAbsent), so two concurrent CREATE TABLE
// statements for the same name can never both believe they created the
// table. A losing PutIfAbsent leaves the just-allocated TableID unused,
// which is an acceptable gap in the TableID sequence (see SequenceServicer).
func (s *TableService) CreateTable(ctx context.Context, req *CreateTableRequest) (*TableDef, error) {
	if err := validateCreateTableRequest(req); err != nil {
		return nil, err
	}

	tableID, err := s.seq.Allocate(ctx, req.Database, tableIDSequenceName, 1)
	if err != nil {
		return nil, err
	}

	indexes := append([]TableIndex(nil), req.Indexes...)
	for i := range indexes {
		indexes[i].Incarnation = uint64(i) + 1
	}

	now := time.Now().UTC()
	def := &TableDef{
		Layout:            TableLayoutCurrent,
		IndexIncarnations: uint64(len(indexes)),
		TableID:           tableID,
		Name:              req.Name,
		Database:          req.Database,
		Columns:           append([]ColumnDef(nil), req.Columns...),
		PrimaryKey:        append([]PKColumn(nil), req.PrimaryKey...),
		Indexes:           indexes,
		ForeignKeys:       append([]ForeignKeyDef(nil), req.ForeignKeys...),
		Checks:            append([]CheckDef(nil), req.Checks...),
		AutoIncColumn:     autoIncrementColumnName(req.Columns),
		SchemaVersion:     1,
		Collation:         req.Collation,
		Comment:           req.Comment,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	data, err := json.Marshal(def)
	if err != nil {
		return nil, &TableMetadataError{Op: "marshal", Err: err}
	}

	stored, err := s.kv.PutIfAbsent(ctx, tableMetadataKey(req.Database, req.Name), data)
	if err != nil {
		return nil, err
	}
	if !stored {
		return nil, &TableAlreadyExistsError{Database: req.Database, Name: req.Name}
	}

	return def, nil
}

// DropTable removes a table in one transaction: its catalog entry, DDL
// journal if any, TRUNCATE generation counter and two sequence counters are
// deleted, the database's catalog epoch is bumped, and a ReclaimJob is
// published for the rows and index entries of its current generation (older
// generations already have their TRUNCATE jobs). The data is not deleted
// inline: a statement that resolved the table before the drop may still be
// reading it, so it goes through the same quiescence as a truncated
// generation (ReclaimQuiescence), and a crash at any point leaves either the
// whole table or the job that reclaims it. DML planned against the table
// cannot land after the drop: its commit compares the metadata record, which
// is gone.
//
// req.Resolution, when set, fences the drop on what the caller resolved: a
// TRUNCATE or DDL landing after that resolution refuses it with the typed
// stale error instead of dropping a state the caller never saw.
//
// It deliberately does not check the key layout: dropping a table created
// by v0.20.0 through v0.21.0 is the remedy TableLayoutUnsupportedError
// names. Such a table's old-layout data lives under LegacyTableKeyPrefixes,
// which the job names as well.
func (s *TableService) DropTable(ctx context.Context, req *DropTableRequest) error {
	if err := validateTableRef(req.Database, req.Name); err != nil {
		return err
	}

	metaKey := tableMetadataKey(req.Database, req.Name)
	journalKey := tableDDLJournalKey(req.Database, req.Name)
	epochKey := tableCatalogEpochKey(req.Database)

	return s.runReclaimPublishingCAS(ctx, req.Database, req.Name, func() (*reclaimPublication, error) {
		rev, err := s.mvcc.GetRevision(ctx, metaKey)
		if err != nil {
			return nil, &TableNotFoundError{Database: req.Database, Name: req.Name}
		}
		var def TableDef
		if err := json.Unmarshal(rev.Value, &def); err != nil {
			return nil, &TableMetadataError{Op: "unmarshal", Err: err}
		}
		genKey := tableGenerationKey(req.Database, def.TableID)
		genRev, err := s.revisionOrAbsent(ctx, genKey)
		if err != nil {
			return nil, err
		}
		generation := decodeSequenceCounter(genRev.Value)
		if r := req.Resolution; r != nil {
			if r.TableID != def.TableID || r.MetadataRevision != rev.ModRevision {
				return nil, &TableDefinitionStaleError{Database: req.Database, Name: req.Name, Reason: "its definition changed before the drop"}
			}
			if r.GenerationRevision != genRev.ModRevision {
				return nil, &TableGenerationStaleError{Database: req.Database, Name: req.Name, Expected: r.Generation, Actual: generation}
			}
		}

		prefixes := []string{
			tableDataPrefixFor(req.Database, def.TableID, generation),
			tableIndexGenerationPrefixFor(req.Database, def.TableID, generation),
		}
		if def.Layout != TableLayoutCurrent {
			prefixes = append(prefixes, LegacyTableKeyPrefixes(req.Database, def.TableID)...)
		}

		return &reclaimPublication{
			compares: []Compare{
				{Key: []byte(metaKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: rev.ModRevision},
				// A TRUNCATE landing between this read and this commit fails
				// the attempt, which re-reads and names the new generation.
				{Key: []byte(genKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: genRev.ModRevision},
			},
			ops: []Op{
				{Type: OpDelete, Key: []byte(metaKey)},
				{Type: OpDelete, Key: []byte(journalKey)},
				// The generation counter and the sequences go in the SAME
				// transaction as the definition that names their TableID: a
				// crash can leave neither without the other, and TableID is
				// never reused, so nothing would ever compute their keys
				// again.
				{Type: OpDelete, Key: []byte(genKey)},
				{Type: OpDelete, Key: []byte(tableSequenceKey(req.Database, TableAutoIncrementSequenceName(def.TableID)))},
				{Type: OpDelete, Key: []byte(tableSequenceKey(req.Database, TableRowIDSequenceName(def.TableID)))},
				{Type: OpPut, Key: []byte(epochKey), Value: epochValue()},
			},
			label:    "drop:" + req.Database + ":" + strconv.FormatUint(def.TableID, 10) + ":" + strconv.FormatUint(generation, 10),
			prefixes: prefixes,
			// Committing deletes metaKey, which settles the attempt.
			fences: []ReclaimFence{
				{Key: metaKey, ModRevision: rev.ModRevision},
				{Key: genKey, ModRevision: genRev.ModRevision},
			},
		}, nil
	})
}

// RenameTable changes a table's name. Because row and index keys are
// composed from the table's immutable TableID (see tableDataPrefixFor,
// tableIndexPrefixFor), not from Name, this is a pure catalog
// compare-and-swap: the definition is moved from the old metadata key to
// the new one and no data key changes, so no data moves.
func (s *TableService) RenameTable(ctx context.Context, req *RenameTableRequest) error {
	if err := validateTableRef(req.Database, req.From); err != nil {
		return err
	}
	if err := validateIdentifier("table", req.To); err != nil {
		return err
	}
	if req.From == req.To {
		return nil
	}

	fromKey := tableMetadataKey(req.Database, req.From)
	toKey := tableMetadataKey(req.Database, req.To)
	epochKey := tableCatalogEpochKey(req.Database)

	return s.runCatalogCAS(ctx, req.Database, req.From, func() ([]Compare, []Op, error) {
		noRewrite, err := s.requireNoRewrite(ctx, req.Database, req.From)
		if err != nil {
			return nil, nil, err
		}
		fromRev, err := s.mvcc.GetRevision(ctx, fromKey)
		if err != nil {
			return nil, nil, &TableNotFoundError{Database: req.Database, Name: req.From}
		}

		var def TableDef
		if err := json.Unmarshal(fromRev.Value, &def); err != nil {
			return nil, nil, &TableMetadataError{Op: "unmarshal", Err: err}
		}

		if existing, err := s.kv.Get(ctx, toKey); err == nil && existing != nil {
			return nil, nil, &TableAlreadyExistsError{Database: req.Database, Name: req.To}
		}

		def.Name = req.To
		def.UpdatedAt = time.Now().UTC()
		data, err := json.Marshal(&def)
		if err != nil {
			return nil, nil, &TableMetadataError{Op: "marshal", Err: err}
		}

		// Guard against a concurrent CreateTable landing on the target name
		// between the existence check above and this proposal's commit: the
		// second compare requires toKey to still be absent (ModRevision 0),
		// the same "absent" encoding SequenceService uses. Either compare
		// failing surfaces uniformly as a CAS conflict and is retried.
		compares := []Compare{
			{Key: []byte(fromKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: fromRev.ModRevision},
			{Key: []byte(toKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: 0},
			noRewrite,
		}
		ops := []Op{
			{Type: OpDelete, Key: []byte(fromKey)},
			{Type: OpPut, Key: []byte(toKey), Value: data},
			{Type: OpPut, Key: []byte(epochKey), Value: epochValue()},
		}
		return compares, ops, nil
	})
}

// TruncateTable atomically advances database.name's generation, optionally
// resets its AUTO_INCREMENT counter, and publishes a ReclaimJob for the
// superseded generation's data -- see the TableServicer doc comment on this
// method and reclaim_job.go's package doc comment for the full design.
//
// It does not write the table's own metadata record (Name, Columns,
// SchemaVersion, ...): the generation lives at a key of its own
// (tableGenerationKey), so a TRUNCATE fails a racing DML statement through
// its generation guard alone, never its definition guard. It does compare
// the record, so a DDL landing between this attempt's read and its commit
// fails the attempt, which re-reads.
//
// Like every other catalog change it refuses while a rewrite journal exists
// (requireNoRewrite): a rewrite resuming against a generation it no longer
// matches is exactly the hazard the generation guard exists to catch, and
// refusing up front is simpler than teaching the rewrite executor to retry.
func (s *TableService) TruncateTable(ctx context.Context, database, name string, resetSequence bool) (*TableDef, error) {
	if err := validateTableRef(database, name); err != nil {
		return nil, err
	}

	metaKey := tableMetadataKey(database, name)

	var result *TableDef
	err := s.runReclaimPublishingCAS(ctx, database, name, func() (*reclaimPublication, error) {
		result = nil
		// The new generation becomes current when this attempt commits,
		// which is after this instant -- a valid, conservative start for a
		// caller's pin on it.
		attemptAt := time.Now()

		noRewrite, err := s.requireNoRewrite(ctx, database, name)
		if err != nil {
			return nil, err
		}

		metaRev, err := s.mvcc.GetRevision(ctx, metaKey)
		if err != nil {
			return nil, &TableNotFoundError{Database: database, Name: name}
		}
		var def TableDef
		if err := json.Unmarshal(metaRev.Value, &def); err != nil {
			return nil, &TableMetadataError{Op: "unmarshal", Err: err}
		}
		if err := checkTableLayout(&def, database, name); err != nil {
			return nil, err
		}

		genKey := tableGenerationKey(database, def.TableID)
		genRev, err := s.revisionOrAbsent(ctx, genKey)
		if err != nil {
			return nil, err
		}
		oldGeneration := decodeSequenceCounter(genRev.Value)
		newGeneration := oldGeneration + 1

		compares := []Compare{
			// Absent decodes as ModRevision 0, the same "absent" encoding
			// SequenceService and RenameTable's own guard use, so a table
			// truncated for the first time and one truncated again flow
			// through the identical compare.
			{Key: []byte(genKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: genRev.ModRevision},
			// The definition this attempt read must still be current at
			// commit time: a concurrent RENAME/DROP/ALTER landing in between
			// fails this attempt rather than let it commit against a
			// definition that is no longer the table's.
			{Key: []byte(metaKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: metaRev.ModRevision},
			// A rewrite journal created concurrently must fail this attempt
			// for the same reason the up-front check above exists.
			noRewrite,
		}
		ops := []Op{
			{Type: OpPut, Key: []byte(genKey), Value: encodeSequenceCounter(newGeneration)},
		}

		if resetSequence {
			seqKey := tableSequenceKey(database, TableAutoIncrementSequenceName(def.TableID))
			seqRev, err := s.revisionOrAbsent(ctx, seqKey)
			if err != nil {
				return nil, err
			}
			// The compare and the Put are unconditional, even when the
			// observed counter already reads 0: the compare is what catches
			// a concurrent SequenceServicer.Allocate that writes a nonzero
			// counter between this read and this transaction's commit.
			compares = append(compares, Compare{
				Key: []byte(seqKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: seqRev.ModRevision,
			})
			ops = append(ops, Op{Type: OpPut, Key: []byte(seqKey), Value: encodeSequenceCounter(0)})
		}

		def.Generation = newGeneration
		def.GenerationReadAt = attemptAt
		// The compare above holds metaKey at this revision through commit.
		def.MetadataRevision = metaRev.ModRevision
		result = &def
		return &reclaimPublication{
			compares: compares,
			ops:      ops,
			label:    "table:" + database + ":" + strconv.FormatUint(def.TableID, 10) + ":" + strconv.FormatUint(oldGeneration, 10),
			// The whole generation's index keyspace, not one prefix per
			// catalogued index: see tableIndexGenerationPrefixFor.
			prefixes: []string{
				tableDataPrefixFor(database, def.TableID, oldGeneration),
				tableIndexGenerationPrefixFor(database, def.TableID, oldGeneration),
			},
			// Committing bumps genKey, and anything else that could still
			// let this attempt commit leaves metaKey unchanged -- so once
			// either reads a different ModRevision this attempt is settled.
			// genKey can only return to absent (ModRevision 0) through DROP
			// TABLE, which deletes metaKey in the same transaction.
			fences: []ReclaimFence{
				{Key: metaKey, ModRevision: metaRev.ModRevision},
				{Key: genKey, ModRevision: genRev.ModRevision},
			},
		}, nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// RenameIndex renames a secondary index. Index entries are keyed by the
// index's incarnation, not its name (see TableIndex.Incarnation), so this is
// a pure catalog compare-and-swap: no entry moves, nothing is orphaned, and a
// later index created under the old name gets an incarnation -- and a
// keyspace -- of its own.
func (s *TableService) RenameIndex(ctx context.Context, req *RenameTableIndexRequest) error {
	if err := validateTableRef(req.Database, req.Table); err != nil {
		return err
	}
	if req.From == "" {
		return &TableInvalidNameError{Kind: "index", Name: req.From, Reason: "must not be empty"}
	}
	if req.To == "" {
		return &TableInvalidNameError{Kind: "index", Name: req.To, Reason: "must not be empty"}
	}

	metaKey := tableMetadataKey(req.Database, req.Table)
	epochKey := tableCatalogEpochKey(req.Database)

	return s.runCatalogCAS(ctx, req.Database, req.Table, func() ([]Compare, []Op, error) {
		noRewrite, err := s.requireNoRewrite(ctx, req.Database, req.Table)
		if err != nil {
			return nil, nil, err
		}
		metaRev, err := s.mvcc.GetRevision(ctx, metaKey)
		if err != nil {
			return nil, nil, &TableNotFoundError{Database: req.Database, Name: req.Table}
		}
		var def TableDef
		if err := json.Unmarshal(metaRev.Value, &def); err != nil {
			return nil, nil, &TableMetadataError{Op: "unmarshal", Err: err}
		}
		if err := checkTableLayout(&def, req.Database, req.Table); err != nil {
			return nil, nil, err
		}

		idx := -1
		for i, existing := range def.Indexes {
			if existing.Name == req.To {
				return nil, nil, &TableIndexAlreadyExistsError{Database: req.Database, Table: req.Table, Index: req.To}
			}
			if existing.Name == req.From {
				idx = i
			}
		}
		if idx == -1 {
			return nil, nil, &TableIndexNotFoundError{Database: req.Database, Table: req.Table, Index: req.From}
		}
		renamed := def.Indexes[idx]
		renamed.Name = req.To
		if err := validateIndex(columnNameSet(&def), renamed); err != nil {
			return nil, nil, err
		}

		def.Indexes = append(def.Indexes[:idx:idx], append([]TableIndex{renamed}, def.Indexes[idx+1:]...)...)
		def.UpdatedAt = time.Now().UTC()
		data, err := json.Marshal(&def)
		if err != nil {
			return nil, nil, &TableMetadataError{Op: "marshal", Err: err}
		}
		compares := []Compare{
			{Key: []byte(metaKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: metaRev.ModRevision},
			noRewrite,
		}
		ops := []Op{
			{Type: OpPut, Key: []byte(metaKey), Value: data},
			{Type: OpPut, Key: []byte(epochKey), Value: epochValue()},
		}
		return compares, ops, nil
	})
}

// reclaimPublication is one attempt of a reclaim-publishing catalog
// operation: the compare-and-swap it proposes, plus the keyspace it orphans
// and the fences a sweeper uses to settle its index entry (see
// ReclaimIndexEntry.Fences).
type reclaimPublication struct {
	compares []Compare
	ops      []Op
	label    string
	prefixes []string
	fences   []ReclaimFence
}

// runReclaimPublishingCAS is runCatalogCAS for an operation that orphans a
// keyspace. Every attempt publishes under a fresh job ID (newReclaimJobID):
//
//  1. its ReclaimIndexEntry, alone, against the system store;
//  2. its own compare-and-swap with the ReclaimJob record added as one more
//     Op, so the job commits if and only if the operation does.
//
// An attempt whose compare-and-swap definitely lost withdraws its own entry
// before retrying; because the ID is this attempt's alone, no other attempt's
// entry can be withdrawn by mistake. An attempt whose outcome is unknown
// leaves its entry for the sweeper, whose fence check settles it either way.
//
// A publication naming no prefixes orphans nothing: it is proposed as a plain
// compare-and-swap, with no index entry and no job, so an operation that only
// sometimes orphans a keyspace (CompleteRewrite, AbortRewrite) goes through
// one path either way.
func (s *TableService) runReclaimPublishingCAS(ctx context.Context, database, table string, build func() (*reclaimPublication, error)) error {
	// The tenant is part of what locates the job's store. It is carried on
	// ctx rather than widening every signature: the SQL layer binds the
	// session's tenant before calling in, and tenant.FromContext falls back
	// to the system tenant for a context with none, like every other
	// tenant-context reader in the tree.
	tenantID := tenant.FromContext(ctx)
	conflictKey := tableMetadataKey(database, table)

	err := doWithCatalogRetry(ctx, func(ctx context.Context) error {
		pub, err := build()
		if err != nil {
			return err
		}
		if len(pub.prefixes) == 0 {
			resp, err := s.txn.Execute(ctx, pub.compares, pub.ops, nil)
			if err != nil {
				return err
			}
			if !resp.Succeeded {
				return &TransactionConflictError{Key: conflictKey}
			}
			return nil
		}
		if s.reclaimIndex == nil {
			return &ReclaimIndexUnconfiguredError{Database: database, Name: table}
		}

		now := time.Now().UTC()
		jobID := newReclaimJobID(tenantID, pub.label)
		indexOp, err := ReclaimIndexOp(ReclaimIndexEntry{
			JobID:     jobID,
			TenantID:  tenantID,
			Database:  database,
			Placement: s.reclaimIndex.placement,
			Origin:    s.reclaimIndex.origin,
			Fences:    pub.fences,
			CreatedAt: now,
		})
		if err != nil {
			return err
		}
		jobOp, err := ReclaimJobOp(ReclaimJob{
			ID: jobID, TenantID: tenantID, Database: database, KeyPrefixes: pub.prefixes, CreatedAt: now,
		})
		if err != nil {
			return err
		}

		resp, err := s.reclaimIndex.txn.Execute(ctx, nil, []Op{indexOp}, nil)
		if err != nil {
			return err
		}
		if !resp.Succeeded {
			return &TransactionConflictError{Key: string(indexOp.Key)}
		}

		ops := make([]Op, 0, len(pub.ops)+1)
		ops = append(ops, pub.ops...)
		ops = append(ops, jobOp)
		resp, err = s.txn.Execute(ctx, pub.compares, ops, nil)
		if err != nil {
			return err
		}
		if !resp.Succeeded {
			// Best effort: if the withdrawal itself fails, the entry's
			// fences settle it on a later sweep instead.
			_, _ = s.reclaimIndex.txn.Execute(ctx, nil, []Op{{Type: OpDelete, Key: indexOp.Key}}, nil)
			return &TransactionConflictError{Key: conflictKey}
		}
		return nil
	})
	if err == nil {
		return nil
	}
	var conflict *TransactionConflictError
	if errors.As(err, &conflict) {
		return &TableDDLConflictError{Database: database, Name: table, Err: conflict}
	}
	return err
}

// GetTable returns a table's current definition with a fresh linearizable
// read (see the no-caching doc comment on TableService). Returns
// TableRewriteInProgressError when a TableDDLJournal is present for the
// table -- its rows are in a mixed encoding until the rewrite completes or
// is resumed, so it must not be served.
func (s *TableService) GetTable(ctx context.Context, database, name string) (*TableDef, error) {
	if err := validateTableRef(database, name); err != nil {
		return nil, err
	}

	_, journalErr := s.kv.Get(ctx, tableDDLJournalKey(database, name))
	inRewrite := journalErr == nil

	metaRev, err := s.mvcc.GetRevision(ctx, tableMetadataKey(database, name))
	if err != nil || metaRev == nil || metaRev.Value == nil {
		if inRewrite {
			return nil, &TableRewriteInProgressError{Database: database, Name: name}
		}
		return nil, &TableNotFoundError{Database: database, Name: name}
	}

	var def TableDef
	if err := json.Unmarshal(metaRev.Value, &def); err != nil {
		return nil, &TableMetadataError{Op: "unmarshal", Err: err}
	}
	// The layout is checked before the rewrite journal: a table created by
	// v0.20.0 through v0.21.0 whose rewrite was interrupted must still
	// resolve as what it is -- droppable -- not as a rewrite that can never
	// be resumed.
	if err := checkTableLayout(&def, database, name); err != nil {
		return nil, err
	}
	if inRewrite {
		return nil, &TableRewriteInProgressError{Database: database, Name: name}
	}
	def.MetadataRevision = metaRev.ModRevision

	readAt := time.Now()
	genRev, err := s.revisionOrAbsent(ctx, tableGenerationKey(database, def.TableID))
	if err != nil {
		return nil, err
	}
	def.Generation = decodeSequenceCounter(genRev.Value)
	def.GenerationRevision = genRev.ModRevision
	def.GenerationReadAt = readAt

	return &def, nil
}

// readGeneration returns tableID's current generation, or 0 for a table that
// has never been truncated (the same "absent key decodes as zero" contract
// SequenceServicer uses; see tableGenerationKey).
func (s *TableService) readGeneration(ctx context.Context, database string, tableID uint64) (uint64, error) {
	data, err := s.kv.Get(ctx, tableGenerationKey(database, tableID))
	if err != nil {
		if isKeyNotFound(err) {
			return 0, nil
		}
		return 0, err
	}
	if data == nil {
		return 0, nil
	}
	return decodeSequenceCounter(data), nil
}

// revisionOrAbsent reads key's current revision, returning a zero-value
// *KeyValue (ModRevision 0, Value nil, decoding as counter 0 via
// decodeSequenceCounter) rather than an error when key has never been
// written -- the same "absent decodes as zero" contract readGeneration and
// SequenceServicer use. TruncateTable relies on this for both the
// generation counter (absent on a table's very first TRUNCATE) and the
// AUTO_INCREMENT sequence (absent on a table that has never had a row
// inserted), so either key's first-ever write is guarded by the same
// ModRevision-0 compare-and-swap as every later one, with no special case
// at the call site.
func (s *TableService) revisionOrAbsent(ctx context.Context, key string) (*KeyValue, error) {
	rev, err := s.mvcc.GetRevision(ctx, key)
	if err != nil {
		if isKeyNotFound(err) {
			return &KeyValue{}, nil
		}
		return nil, err
	}
	return rev, nil
}

// requireNoRewrite refuses a catalog change to database.name while an
// ALTER TABLE rewrite is in flight (a TableDDLJournal exists), and returns
// the Compare that keeps it refused through commit: journalKey must still be
// absent. A rewrite finishes by writing its journaled Target over the
// metadata record (CompleteRewrite); a change accepted in between -- an
// index created, dropped or renamed, a foreign key, a rename -- would be
// silently undone by that write, resurrecting a dropped index whose entries
// are being reclaimed or re-issuing an index incarnation. The Compare, not
// only the up-front check, is what closes the window between the two.
func (s *TableService) requireNoRewrite(ctx context.Context, database, name string) (Compare, error) {
	journalKey := tableDDLJournalKey(database, name)
	rev, err := s.revisionOrAbsent(ctx, journalKey)
	if err != nil {
		return Compare{}, err
	}
	if rev.ModRevision != 0 {
		return Compare{}, &TableRewriteInProgressError{Database: database, Name: name}
	}
	return Compare{Key: []byte(journalKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: 0}, nil
}

// ListTables returns the names of every table in the given database. The
// catalog metadata prefix also contains each table's epoch and DDL-journal
// keys (tableCatalogEpochKey, tableDDLJournalKey), which are filtered out.
func (s *TableService) ListTables(ctx context.Context, database string) ([]string, error) {
	if database == "" {
		return nil, &TableEmptyDatabaseError{}
	}

	prefix := tableCatalogScanPrefix(database)
	keys, err := s.kv.List(ctx, prefix)
	if err != nil {
		return nil, &TableMetadataError{Op: "list", Err: err}
	}

	epochKey := tableCatalogEpochKey(database)
	names := make([]string, 0, len(keys))
	for _, key := range keys {
		if key == epochKey || strings.HasSuffix(key, tableDDLJournalSuffix) {
			continue
		}
		name := strings.TrimPrefix(key, prefix)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

// AlterTable applies one or more column changes to a table.
//
// AlterAddColumn and AlterRenameColumn are metadata-only: they are applied
// directly to the catalog record in the same compare-and-swap that commits
// them. AlterDropColumn and AlterModifyColumn require re-encoding existing
// rows, which this service does not perform -- it journals the target
// definition as a pending rewrite (TableDDLJournal, DDLStateRewriting) in
// the opening compare-and-swap and returns TableRewriteRequiredError. The
// live catalog record is left untouched by a rewrite-triggering call: the
// rewrite executor (a later phase) is what eventually commits the new
// definition and deletes the journal in its closing compare-and-swap.
//
// A request mixing a rewrite-requiring change with a metadata-only change
// applies both to the journaled target definition, but neither takes
// effect on the live table until the rewrite executor runs.
func (s *TableService) AlterTable(ctx context.Context, req *AlterTableRequest) (*TableDef, error) {
	if err := validateTableRef(req.Database, req.Name); err != nil {
		return nil, err
	}
	if len(req.Changes) == 0 {
		return nil, &TableNoChangesError{}
	}

	metaKey := tableMetadataKey(req.Database, req.Name)
	journalKey := tableDDLJournalKey(req.Database, req.Name)
	epochKey := tableCatalogEpochKey(req.Database)

	var result *TableDef
	err := s.runCatalogCAS(ctx, req.Database, req.Name, func() ([]Compare, []Op, error) {
		result = nil

		// A table with a rewrite already in flight must not accept another
		// DDL statement -- metadata-only or not -- until that rewrite
		// completes or is resumed: the closing CAS a resumed rewrite issues
		// unconditionally replaces metaKey with journal.Target, which would
		// silently discard any change accepted here in the meantime.
		if journal, jerr := s.kv.Get(ctx, journalKey); jerr == nil && journal != nil {
			return nil, nil, &TableRewriteInProgressError{Database: req.Database, Name: req.Name}
		}

		rev, err := s.mvcc.GetRevision(ctx, metaKey)
		if err != nil {
			return nil, nil, &TableNotFoundError{Database: req.Database, Name: req.Name}
		}
		if req.ExpectedRevision != 0 && req.ExpectedRevision != rev.ModRevision {
			return nil, nil, &TableStaleRevisionError{
				Database: req.Database, Name: req.Name,
				Expected: req.ExpectedRevision, Actual: rev.ModRevision,
			}
		}

		var def TableDef
		if err := json.Unmarshal(rev.Value, &def); err != nil {
			return nil, nil, &TableMetadataError{Op: "unmarshal", Err: err}
		}
		if err := checkTableLayout(&def, req.Database, req.Name); err != nil {
			return nil, nil, err
		}

		fromVersion := def.SchemaVersion
		requiresRewrite, layoutChanged, err := applyAlterChanges(&def, req.Changes)
		if err != nil {
			return nil, nil, err
		}

		if layoutChanged {
			def.SchemaVersion++
		}
		def.UpdatedAt = time.Now().UTC()

		compares := []Compare{{
			Key: []byte(metaKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: rev.ModRevision,
		}}

		if requiresRewrite {
			journal := &TableDDLJournal{
				State:       DDLStateRewriting,
				FromVersion: fromVersion,
				ToVersion:   def.SchemaVersion,
				Target:      &def,
				// The record Target was derived from, which CompleteRewrite
				// requires to be unchanged when it writes Target back.
				MetadataRevision: rev.ModRevision,
				Changes:          append([]AlterTableChange(nil), req.Changes...),
				StartedAt:        time.Now().UTC(),
			}
			journalData, jerr := json.Marshal(journal)
			if jerr != nil {
				return nil, nil, &TableMetadataError{Op: "marshal-journal", Err: jerr}
			}
			ops := []Op{
				{Type: OpPut, Key: []byte(journalKey), Value: journalData},
				{Type: OpPut, Key: []byte(epochKey), Value: epochValue()},
			}
			return compares, ops, nil
		}

		data, merr := json.Marshal(&def)
		if merr != nil {
			return nil, nil, &TableMetadataError{Op: "marshal", Err: merr}
		}
		ops := []Op{
			{Type: OpPut, Key: []byte(metaKey), Value: data},
			{Type: OpPut, Key: []byte(epochKey), Value: epochValue()},
		}
		result = &def
		return compares, ops, nil
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, &TableRewriteRequiredError{Database: req.Database, Name: req.Name}
	}
	readAt := time.Now()
	generation, gerr := s.readGeneration(ctx, req.Database, result.TableID)
	if gerr != nil {
		return nil, gerr
	}
	result.Generation = generation
	result.GenerationReadAt = readAt
	return result, nil
}

// applyAlterChanges applies every change in changes to def in place. It
// returns requiresRewrite when any change needs a row rewrite (drop or
// modify column) and layoutChanged when the column list itself changed
// (add, drop, or modify -- not a pure rename, which does not affect how a
// row's bytes decode). def.AutoIncColumn is recomputed from the resulting
// column list once, after every change has been applied, rather than
// incrementally per change, so a request that both drops the old
// AUTO_INCREMENT column and adds a new one in the same call is evaluated
// against its final state rather than rejected on an intermediate one.
func applyAlterChanges(def *TableDef, changes []AlterTableChange) (requiresRewrite, layoutChanged bool, err error) {
	// Incarnations above this were allocated by this ALTER itself.
	freshAbove := def.IndexIncarnations
	for _, change := range changes {
		switch change.Action {
		case AlterAddColumn:
			if err := validateNewColumn(def, change.Column); err != nil {
				return false, false, err
			}
			def.Columns = append(def.Columns, change.Column)
			layoutChanged = true

		case AlterDropColumn:
			idx, ferr := findColumnIndex(def, change.Name)
			if ferr != nil {
				return false, false, ferr
			}
			def.Columns = append(def.Columns[:idx:idx], def.Columns[idx+1:]...)
			narrowed := indexesOn(def, change.Name)
			dropColumnReferences(def, change.Name)
			reincarnateIndexes(def, narrowed, freshAbove)
			requiresRewrite = true
			layoutChanged = true

		case AlterModifyColumn:
			idx, ferr := findColumnIndex(def, change.Name)
			if ferr != nil {
				return false, false, ferr
			}
			newCol := change.Column
			if newCol.Name == "" {
				newCol.Name = change.Name
			}
			if err := validateIdentifier("column", newCol.Name); err != nil {
				return false, false, err
			}
			if !strings.EqualFold(newCol.Name, change.Name) {
				if _, dupErr := findColumnIndex(def, newCol.Name); dupErr == nil {
					return false, false, &TableDuplicateColumnError{Column: newCol.Name}
				}
			}
			if !isSupportedColumnType(newCol.Type) {
				return false, false, &TableUnsupportedColumnTypeError{Column: newCol.Name, Type: newCol.Type}
			}
			// A modify whose LAYOUT fields (type, nullability,
			// auto-increment) are unchanged only restates metadata --
			// default, comment, collation, or the name itself. The row
			// codec encodes positionally against type and order, so no
			// stored row's meaning changes and no rewrite is journaled:
			// ALTER ... SET DEFAULT and column restatements apply in the
			// same compare-and-swap as every other metadata change.
			current := def.Columns[idx]
			layoutUnchanged := strings.EqualFold(current.Type, newCol.Type) &&
				current.Nullable == newCol.Nullable &&
				current.AutoIncrement == newCol.AutoIncrement
			if layoutUnchanged {
				if !strings.EqualFold(current.Name, newCol.Name) {
					renameColumnReferences(def, current.Name, newCol.Name)
				}
				def.Columns[idx] = newCol
				break
			}
			if !strings.EqualFold(current.Name, newCol.Name) {
				renameColumnReferences(def, current.Name, newCol.Name)
			}
			def.Columns[idx] = newCol
			reincarnateIndexes(def, indexesOn(def, newCol.Name), freshAbove)
			requiresRewrite = true
			layoutChanged = true

		case AlterRenameColumn:
			idx, ferr := findColumnIndex(def, change.Name)
			if ferr != nil {
				return false, false, ferr
			}
			if err := validateIdentifier("column", change.NewName); err != nil {
				return false, false, err
			}
			if !strings.EqualFold(change.NewName, change.Name) {
				if _, dupErr := findColumnIndex(def, change.NewName); dupErr == nil {
					return false, false, &TableDuplicateColumnError{Column: change.NewName}
				}
			}
			renameColumnReferences(def, change.Name, change.NewName)
			def.Columns[idx].Name = change.NewName

		case AlterAddCheck:
			if err := validateIdentifier("check constraint", change.Check.Name); err != nil {
				return false, false, err
			}
			for _, c := range def.Checks {
				if strings.EqualFold(c.Name, change.Check.Name) {
					return false, false, &TableCheckAlreadyExistsError{Table: def.Name, Check: change.Check.Name}
				}
			}
			def.Checks = append(def.Checks, change.Check)

		case AlterDropCheck:
			dropped := false
			kept := def.Checks[:0]
			for _, c := range def.Checks {
				if strings.EqualFold(c.Name, change.Name) {
					dropped = true
					continue
				}
				kept = append(kept, c)
			}
			if !dropped {
				return false, false, &TableCheckNotFoundError{Table: def.Name, Check: change.Name}
			}
			def.Checks = kept

		default:
			return false, false, &TableUnsupportedAlterActionError{Action: string(change.Action)}
		}
	}

	autoIncCount := 0
	autoIncName := ""
	for _, c := range def.Columns {
		if c.AutoIncrement {
			autoIncCount++
			autoIncName = c.Name
		}
	}
	if autoIncCount > 1 {
		return false, false, &TableMultipleAutoIncrementError{}
	}
	def.AutoIncColumn = autoIncName

	return requiresRewrite, layoutChanged, nil
}

// renameColumnReferences follows a column rename through every catalog
// structure that names the column: the primary key, each index, and each
// foreign key (both its own key columns and, for a self-referential
// constraint, its parent columns). It is the rename counterpart of
// dropColumnReferences, and exists for the same reason: a definition whose
// PrimaryKey or Indexes name a column that no longer exists fails
// catalog.NewKeyCodec/PrimaryKeySchemaOf with a column-not-found error the
// next time the table is served -- a rename that only touched def.Columns
// would break every table whose renamed column participates in a key.
func renameColumnReferences(def *TableDef, oldName, newName string) {
	for i := range def.PrimaryKey {
		if strings.EqualFold(def.PrimaryKey[i].Name, oldName) {
			def.PrimaryKey[i].Name = newName
		}
	}
	for i := range def.Indexes {
		for j := range def.Indexes[i].Columns {
			if strings.EqualFold(def.Indexes[i].Columns[j].Name, oldName) {
				def.Indexes[i].Columns[j].Name = newName
			}
		}
	}
	for i := range def.ForeignKeys {
		fk := &def.ForeignKeys[i]
		for j := range fk.Columns {
			if strings.EqualFold(fk.Columns[j], oldName) {
				fk.Columns[j] = newName
			}
		}
		if strings.EqualFold(fk.ParentTable, def.Name) {
			for j := range fk.ParentColumns {
				if strings.EqualFold(fk.ParentColumns[j], oldName) {
					fk.ParentColumns[j] = newName
				}
			}
		}
	}
}

// indexesOn returns the incarnations of def's indexes covering column
// (matched case-insensitively).
func indexesOn(def *TableDef, column string) map[uint64]struct{} {
	on := make(map[uint64]struct{})
	for _, idx := range def.Indexes {
		for _, c := range idx.Columns {
			if strings.EqualFold(c.Name, column) {
				on[idx.Incarnation] = struct{}{}
				break
			}
		}
	}
	return on
}

// reincarnateIndexes gives each index of def whose incarnation is in which a
// new incarnation from def's IndexIncarnations counter. An index whose
// column set or column encoding a rewrite changes must not keep its
// incarnation: its existing entries were encoded for the old definition. The
// rewrite rebuilds the new incarnation, and the transaction that makes the
// definition current reclaims the old one (CompleteRewrite). An index that
// already has an incarnation above freshAbove was re-incarnated by an
// earlier change of the same ALTER and is left alone.
func reincarnateIndexes(def *TableDef, which map[uint64]struct{}, freshAbove uint64) {
	for i := range def.Indexes {
		if _, ok := which[def.Indexes[i].Incarnation]; !ok || def.Indexes[i].Incarnation > freshAbove {
			continue
		}
		def.IndexIncarnations++
		def.Indexes[i].Incarnation = def.IndexIncarnations
	}
}

// dropColumnReferences removes name (matched case-insensitively) from
// def.PrimaryKey, from every def.Indexes entry, and from every
// def.ForeignKeys entry, dropping any index or constraint that would
// otherwise be left referencing a column that no longer exists. It is
// called after name has already been removed from def.Columns, so nothing
// in the resulting definition references a column that no longer exists --
// leaving a dangling reference would fail catalog.NewKeyCodec/
// PrimaryKeySchemaOf with a column-not-found error the next time the table
// is resolved, rather than at the ALTER TABLE statement that caused it.
//
// Dropping a constraint rather than refusing the column drop is safe
// because the SQL layer never reaches here for that case: go-mysql-server
// checks both declared and referencing constraints and rejects DROP COLUMN
// with ErrForeignKeyDropColumn before any ALTER is proposed. This is the
// catalog's own last line of defense for a direct service caller, and it
// mirrors what the same function already does for an index left with no
// columns.
func dropColumnReferences(def *TableDef, name string) {
	dropForeignKeyReferences(def, name)

	if len(def.PrimaryKey) > 0 {
		kept := def.PrimaryKey[:0]
		for _, pk := range def.PrimaryKey {
			if !strings.EqualFold(pk.Name, name) {
				kept = append(kept, pk)
			}
		}
		def.PrimaryKey = kept
	}

	if len(def.Indexes) == 0 {
		return
	}
	keptIdx := def.Indexes[:0]
	for _, idx := range def.Indexes {
		cols := idx.Columns[:0]
		for _, c := range idx.Columns {
			if !strings.EqualFold(c.Name, name) {
				cols = append(cols, c)
			}
		}
		idx.Columns = cols
		if len(idx.Columns) > 0 {
			keptIdx = append(keptIdx, idx)
		}
	}
	def.Indexes = keptIdx
}

// dropForeignKeyReferences removes every constraint on def that names the
// dropped column, either among its own key columns or -- for a
// self-referential constraint, the only case whose parent columns live on
// this same table -- among its parent columns. See dropColumnReferences.
func dropForeignKeyReferences(def *TableDef, name string) {
	if len(def.ForeignKeys) == 0 {
		return
	}
	kept := def.ForeignKeys[:0]
	for _, fk := range def.ForeignKeys {
		cols := fk.Columns
		if strings.EqualFold(fk.ParentTable, def.Name) {
			cols = append(append([]string(nil), fk.Columns...), fk.ParentColumns...)
		}
		if !containsFold(cols, name) {
			kept = append(kept, fk)
		}
	}
	def.ForeignKeys = kept
}

// containsFold reports whether values contains name, compared
// case-insensitively.
func containsFold(values []string, name string) bool {
	for _, v := range values {
		if strings.EqualFold(v, name) {
			return true
		}
	}
	return false
}

// CreateIndex adds a secondary index to a table's catalog entry.
func (s *TableService) CreateIndex(ctx context.Context, req *CreateTableIndexRequest) error {
	if err := validateTableRef(req.Database, req.Table); err != nil {
		return err
	}

	metaKey := tableMetadataKey(req.Database, req.Table)
	epochKey := tableCatalogEpochKey(req.Database)

	return s.runCatalogCAS(ctx, req.Database, req.Table, func() ([]Compare, []Op, error) {
		noRewrite, err := s.requireNoRewrite(ctx, req.Database, req.Table)
		if err != nil {
			return nil, nil, err
		}
		rev, err := s.mvcc.GetRevision(ctx, metaKey)
		if err != nil {
			return nil, nil, &TableNotFoundError{Database: req.Database, Name: req.Table}
		}
		var def TableDef
		if err := json.Unmarshal(rev.Value, &def); err != nil {
			return nil, nil, &TableMetadataError{Op: "unmarshal", Err: err}
		}
		if err := checkTableLayout(&def, req.Database, req.Table); err != nil {
			return nil, nil, err
		}

		if err := validateIndex(columnNameSet(&def), req.Index); err != nil {
			return nil, nil, err
		}
		for _, existing := range def.Indexes {
			if existing.Name == req.Index.Name {
				return nil, nil, &TableIndexAlreadyExistsError{Database: req.Database, Table: req.Table, Index: req.Index.Name}
			}
		}

		def.IndexIncarnations++
		added := req.Index
		added.Incarnation = def.IndexIncarnations
		def.Indexes = append(def.Indexes, added)
		def.UpdatedAt = time.Now().UTC()
		data, err := json.Marshal(&def)
		if err != nil {
			return nil, nil, &TableMetadataError{Op: "marshal", Err: err}
		}

		compares := []Compare{
			{Key: []byte(metaKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: rev.ModRevision},
			noRewrite,
		}
		ops := []Op{
			{Type: OpPut, Key: []byte(metaKey), Value: data},
			{Type: OpPut, Key: []byte(epochKey), Value: epochValue()},
		}
		return compares, ops, nil
	})
}

// DropIndex removes a secondary index from a table's catalog entry and, in
// the same transaction, publishes a ReclaimJob for its entries at the
// table's current generation. They are not deleted inline: a statement that
// resolved the table before the drop may still be reading them, so they go
// through the same quiescence as a truncated generation (ReclaimQuiescence).
// The compare on the generation key makes a racing TRUNCATE fail this
// attempt, so the job always names the generation the entries are under.
func (s *TableService) DropIndex(ctx context.Context, database, table, index string) error {
	if err := validateTableRef(database, table); err != nil {
		return err
	}
	if index == "" {
		return &TableInvalidNameError{Kind: "index", Name: index, Reason: "must not be empty"}
	}

	metaKey := tableMetadataKey(database, table)
	epochKey := tableCatalogEpochKey(database)

	return s.runReclaimPublishingCAS(ctx, database, table, func() (*reclaimPublication, error) {
		noRewrite, err := s.requireNoRewrite(ctx, database, table)
		if err != nil {
			return nil, err
		}
		rev, err := s.mvcc.GetRevision(ctx, metaKey)
		if err != nil {
			return nil, &TableNotFoundError{Database: database, Name: table}
		}
		var def TableDef
		if err := json.Unmarshal(rev.Value, &def); err != nil {
			return nil, &TableMetadataError{Op: "unmarshal", Err: err}
		}
		if err := checkTableLayout(&def, database, table); err != nil {
			return nil, err
		}

		idx := -1
		for i, existing := range def.Indexes {
			if existing.Name == index {
				idx = i
				break
			}
		}
		if idx == -1 {
			return nil, &TableIndexNotFoundError{Database: database, Table: table, Index: index}
		}
		dropped := def.Indexes[idx]

		genKey := tableGenerationKey(database, def.TableID)
		genRev, err := s.revisionOrAbsent(ctx, genKey)
		if err != nil {
			return nil, err
		}
		generation := decodeSequenceCounter(genRev.Value)

		def.Indexes = append(def.Indexes[:idx:idx], def.Indexes[idx+1:]...)
		def.UpdatedAt = time.Now().UTC()
		data, err := json.Marshal(&def)
		if err != nil {
			return nil, &TableMetadataError{Op: "marshal", Err: err}
		}

		return &reclaimPublication{
			compares: []Compare{
				{Key: []byte(metaKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: rev.ModRevision},
				{Key: []byte(genKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: genRev.ModRevision},
				noRewrite,
			},
			ops: []Op{
				{Type: OpPut, Key: []byte(metaKey), Value: data},
				{Type: OpPut, Key: []byte(epochKey), Value: epochValue()},
			},
			label: "index:" + database + ":" + strconv.FormatUint(def.TableID, 10) + ":" +
				strconv.FormatUint(generation, 10) + ":" + strconv.FormatUint(dropped.Incarnation, 10),
			prefixes: []string{tableIndexPrefixFor(database, def.TableID, generation, dropped.Incarnation)},
			// Committing rewrites metaKey, so it settles this attempt the
			// moment it lands.
			fences: []ReclaimFence{
				{Key: metaKey, ModRevision: rev.ModRevision},
				{Key: genKey, ModRevision: genRev.ModRevision},
			},
		}, nil
	})
}

// RewriteJournal returns the in-flight DDL rewrite journal for a table
// together with its still-live (pre-rewrite) definition. See the doc
// comment on TableServicer.RewriteJournal.
func (s *TableService) RewriteJournal(ctx context.Context, database, name string) (*TableDDLJournal, *TableDef, error) {
	if err := validateTableRef(database, name); err != nil {
		return nil, nil, err
	}

	journalData, err := s.kv.Get(ctx, tableDDLJournalKey(database, name))
	if err != nil || journalData == nil {
		return nil, nil, &TableNoRewriteInProgressError{Database: database, Name: name}
	}
	var journal TableDDLJournal
	if err := json.Unmarshal(journalData, &journal); err != nil {
		return nil, nil, &TableMetadataError{Op: "unmarshal-journal", Err: err}
	}

	currentData, err := s.kv.Get(ctx, tableMetadataKey(database, name))
	if err != nil || currentData == nil {
		return nil, nil, &TableNotFoundError{Database: database, Name: name}
	}
	var current TableDef
	if err := json.Unmarshal(currentData, &current); err != nil {
		return nil, nil, &TableMetadataError{Op: "unmarshal", Err: err}
	}
	if err := checkTableLayout(&current, database, name); err != nil {
		return nil, nil, err
	}
	readAt := time.Now()
	generation, gerr := s.readGeneration(ctx, database, current.TableID)
	if gerr != nil {
		return nil, nil, gerr
	}
	current.Generation = generation
	current.GenerationReadAt = readAt

	return &journal, &current, nil
}

// AbortRewrite discards an in-flight rewrite's journal, restoring the table
// to exactly its pre-ALTER definition — which the journal never touched, so
// the abort is a single conditional delete of the journal record.
//
// It exists for the DETERMINISTIC failure class: a rewrite that failed
// because the data cannot satisfy the new definition ('company-x' will never
// convert to bigint) fails identically on every retry, so keeping the
// journal for crash-resume semantics turns one bad ALTER into a permanently
// wedged table — every subsequent access re-attempts the impossible rewrite
// and fails. Transient failures (a storage error mid-write, a crash) must
// NOT abort: their journal is what makes the interrupted rewrite resumable.
// The caller is the one who can tell the two apart.
//
// A rewrite rebuilds every index it narrows or re-encodes under a fresh
// incarnation that only the journal's Target lists; entries it built for
// them before failing would otherwise be unreachable. The abort publishes a
// ReclaimJob for those incarnations in the same transaction that discards
// the journal.
func (s *TableService) AbortRewrite(ctx context.Context, database, name string) error {
	if err := validateTableRef(database, name); err != nil {
		return err
	}
	journalKey := tableDDLJournalKey(database, name)
	metaKey := tableMetadataKey(database, name)
	return s.runReclaimPublishingCAS(ctx, database, name, func() (*reclaimPublication, error) {
		rev, err := s.mvcc.GetRevision(ctx, journalKey)
		if err != nil {
			return nil, &TableNoRewriteInProgressError{Database: database, Name: name}
		}
		pub := &reclaimPublication{
			compares: []Compare{{
				Key: []byte(journalKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: rev.ModRevision,
			}},
			ops: []Op{{Type: OpDelete, Key: []byte(journalKey)}},
			// Committing deletes journalKey.
			fences: []ReclaimFence{{Key: journalKey, ModRevision: rev.ModRevision}},
		}

		var journal TableDDLJournal
		if err := json.Unmarshal(rev.Value, &journal); err != nil || journal.Target == nil {
			// Nothing names fresh incarnations: discard the journal alone.
			return pub, nil
		}
		metaRev, err := s.revisionOrAbsent(ctx, metaKey)
		if err != nil {
			return nil, err
		}
		if metaRev.ModRevision == 0 {
			return pub, nil
		}
		var current TableDef
		if err := json.Unmarshal(metaRev.Value, &current); err != nil {
			return nil, &TableMetadataError{Op: "unmarshal", Err: err}
		}
		genKey := tableGenerationKey(database, current.TableID)
		genRev, err := s.revisionOrAbsent(ctx, genKey)
		if err != nil {
			return nil, err
		}
		generation := decodeSequenceCounter(genRev.Value)
		pub.compares = append(pub.compares, Compare{
			Key: []byte(genKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: genRev.ModRevision,
		})
		pub.label = "abort:" + database + ":" + strconv.FormatUint(current.TableID, 10) + ":" + strconv.FormatUint(generation, 10)
		pub.prefixes = incarnationPrefixes(database, current.TableID, generation, retiredIncarnations(journal.Target, &current))
		return pub, nil
	})
}

// CompleteRewrite finalizes an in-flight rewrite. See the doc comment on
// TableServicer.CompleteRewrite.
//
// The transaction that makes the journal's Target the definition also
// publishes a ReclaimJob for every index incarnation the definition stops
// listing (retiredIncarnations): an index the rewrite narrowed or re-encoded
// was rebuilt under a new incarnation, and one it emptied is gone. Their
// entries are reclaimed only after quiescence, since a statement that
// resolved the old definition may still be reading them. It is fenced on the
// metadata record and the generation, so the job names exactly the keyspace
// the replaced definition used.
func (s *TableService) CompleteRewrite(ctx context.Context, database, name string) (*TableDef, error) {
	if err := validateTableRef(database, name); err != nil {
		return nil, err
	}

	metaKey := tableMetadataKey(database, name)
	journalKey := tableDDLJournalKey(database, name)
	epochKey := tableCatalogEpochKey(database)

	var result *TableDef
	err := s.runReclaimPublishingCAS(ctx, database, name, func() (*reclaimPublication, error) {
		result = nil

		journalRev, err := s.mvcc.GetRevision(ctx, journalKey)
		if err != nil {
			return nil, &TableNoRewriteInProgressError{Database: database, Name: name}
		}
		var journal TableDDLJournal
		if err := json.Unmarshal(journalRev.Value, &journal); err != nil {
			return nil, &TableMetadataError{Op: "unmarshal-journal", Err: err}
		}
		if journal.Target == nil {
			return nil, &TableRewriteTargetMissingError{Database: database, Name: name}
		}
		if err := checkTableLayout(journal.Target, database, name); err != nil {
			return nil, err
		}
		// Target was derived from the metadata record AlterTable read;
		// writing it over a record that has changed since would undo that
		// change. Every catalog change refuses while the journal exists
		// (requireNoRewrite), so this cannot happen through the service --
		// but a moved record is refused outright, not retried: a retry
		// would read the same journal and fail the same way.
		metaRev, err := s.revisionOrAbsent(ctx, metaKey)
		if err != nil {
			return nil, err
		}
		if metaRev.ModRevision != journal.MetadataRevision {
			return nil, &TableDefinitionStaleError{Database: database, Name: name, Reason: "its definition changed under the rewrite"}
		}
		var current TableDef
		if err := json.Unmarshal(metaRev.Value, &current); err != nil {
			return nil, &TableMetadataError{Op: "unmarshal", Err: err}
		}
		genKey := tableGenerationKey(database, current.TableID)
		genRev, err := s.revisionOrAbsent(ctx, genKey)
		if err != nil {
			return nil, err
		}
		generation := decodeSequenceCounter(genRev.Value)

		target := *journal.Target
		target.UpdatedAt = time.Now().UTC()
		data, err := json.Marshal(&target)
		if err != nil {
			return nil, &TableMetadataError{Op: "marshal", Err: err}
		}

		result = &target
		return &reclaimPublication{
			compares: []Compare{
				{Key: []byte(journalKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: journalRev.ModRevision},
				// And it must still be that record when this commits.
				{Key: []byte(metaKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: journal.MetadataRevision},
				// The job names this generation's keyspace.
				{Key: []byte(genKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: genRev.ModRevision},
			},
			ops: []Op{
				{Type: OpPut, Key: []byte(metaKey), Value: data},
				{Type: OpDelete, Key: []byte(journalKey)},
				{Type: OpPut, Key: []byte(epochKey), Value: epochValue()},
			},
			label:    "rewrite:" + database + ":" + strconv.FormatUint(current.TableID, 10) + ":" + strconv.FormatUint(generation, 10),
			prefixes: incarnationPrefixes(database, current.TableID, generation, retiredIncarnations(&current, &target)),
			// Committing overwrites metaKey.
			fences: []ReclaimFence{
				{Key: metaKey, ModRevision: metaRev.ModRevision},
				{Key: genKey, ModRevision: genRev.ModRevision},
			},
		}, nil
	})
	if err != nil {
		return nil, err
	}
	readAt := time.Now()
	generation, gerr := s.readGeneration(ctx, database, result.TableID)
	if gerr != nil {
		return nil, gerr
	}
	result.Generation = generation
	result.GenerationReadAt = readAt
	return result, nil
}

// retiredIncarnations returns, in from's index order, every index
// incarnation from lists and to does not: the indexes a definition change
// dropped or rebuilt under a new incarnation.
func retiredIncarnations(from, to *TableDef) []uint64 {
	var retired []uint64
	for _, idx := range from.Indexes {
		if !listsIncarnation(to, idx.Incarnation) {
			retired = append(retired, idx.Incarnation)
		}
	}
	return retired
}

// listsIncarnation reports whether def lists an index with incarnation.
func listsIncarnation(def *TableDef, incarnation uint64) bool {
	for _, idx := range def.Indexes {
		if idx.Incarnation == incarnation {
			return true
		}
	}
	return false
}

// incarnationPrefixes returns the index-entry prefix of each incarnation at
// generation.
func incarnationPrefixes(database string, tableID, generation uint64, incarnations []uint64) []string {
	prefixes := make([]string, len(incarnations))
	for i, incarnation := range incarnations {
		prefixes[i] = tableIndexPrefixFor(database, tableID, generation, incarnation)
	}
	return prefixes
}

// ---------------------------------------------------------------------------
// Compare-and-swap retry helpers, shared with SequenceService
// ---------------------------------------------------------------------------

// runCatalogCAS runs build, proposes the compares/ops it returns through
// TransactionServicer, and retries with bounded jittered backoff
// (catalogRetryConfig) when the proposal loses a compare-and-swap race.
// build is invoked fresh on every attempt -- including the first -- so it
// always observes the catalog record's current ModRevision rather than one
// captured before an earlier, failed attempt. An error returned by build
// that is not itself a CAS-conflict signal (TableNotFoundError, a
// validation error, TableStaleRevisionError, ...) is not retried and is
// returned to the caller immediately. After the retry budget is exhausted
// on a genuine CAS conflict, the *TransactionConflictError is wrapped in a
// TableDDLConflictError carrying database/table context.
func (s *TableService) runCatalogCAS(ctx context.Context, database, table string, build func() ([]Compare, []Op, error)) error {
	conflictKey := tableMetadataKey(database, table)

	err := doWithCatalogRetry(ctx, func(ctx context.Context) error {
		compares, ops, berr := build()
		if berr != nil {
			return berr
		}

		resp, terr := s.txn.Execute(ctx, compares, ops, nil)
		if terr != nil {
			return terr
		}
		if !resp.Succeeded {
			return &TransactionConflictError{Key: conflictKey}
		}
		return nil
	})
	if err == nil {
		return nil
	}

	var conflict *TransactionConflictError
	if errors.As(err, &conflict) {
		return &TableDDLConflictError{Database: database, Name: table, Err: conflict}
	}
	return err
}

// catalogCASMaxAttempts bounds the number of compare-and-swap attempts
// runCatalogCAS and SequenceService.casLoop make before giving up.
const catalogCASMaxAttempts = 8

// doWithCatalogRetry retries op up to catalogCASMaxAttempts times with
// exponential, jittered backoff, but only when op's error is a
// *TransactionConflictError -- i.e. only on a genuine, server-observed
// losing race against a concurrent writer. Any other error (validation
// failures, not-found, a stale caller-supplied ExpectedRevision, ...) is
// deterministic and returned on the first attempt without wasting the
// retry budget on it.
func doWithCatalogRetry(ctx context.Context, op retry.Operation) error {
	cfg := &retry.Config{
		MaxAttempts: catalogCASMaxAttempts,
		Backoff: &retry.ExponentialBackoff{
			InitialDelay: 2 * time.Millisecond,
			MaxDelay:     40 * time.Millisecond,
			Multiplier:   2.0,
			Jitter:       0.3,
		},
		RetryableFunc: isTransactionConflict,
	}
	return retry.Do(ctx, cfg, op)
}

// isTransactionConflict reports whether err is (or wraps) a
// *TransactionConflictError.
func isTransactionConflict(err error) bool {
	var conflict *TransactionConflictError
	return errors.As(err, &conflict)
}

// isKeyNotFound reports whether err is (or wraps) a *KeyNotFoundError.
func isKeyNotFound(err error) bool {
	var knf *KeyNotFoundError
	return errors.As(err, &knf)
}

// epochValue encodes the current time as an 8-byte big-endian UnixNano
// value. tableCatalogEpochKey's value only needs to change on every catalog
// mutation, not carry any particular meaning, so any monotonically
// non-decreasing source works; wall-clock time is used because it is also
// useful for a future cache to log "last invalidated at" without a second
// key.
func epochValue() []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(time.Now().UnixNano())) //nolint:gosec // G115: UnixNano always positive after 1970
	return b
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// validateTableRef validates that database is non-empty and name is a
// well-formed table identifier.
func validateTableRef(database, name string) error {
	if database == "" {
		return &TableEmptyDatabaseError{}
	}
	return validateIdentifier("table", name)
}

// validateIdentifier validates that name is non-empty, does not start with
// the "__" prefix reserved for catalog system keys (tableCatalogEpochKey,
// tableDDLJournalKey), starts with a letter or underscore, and contains
// only letters, digits, and underscores thereafter. kind labels the
// identifier's role ("table", "column", "index") in the resulting error.
func validateIdentifier(kind, name string) error {
	if name == "" {
		return &TableInvalidNameError{Kind: kind, Name: name, Reason: "must not be empty"}
	}
	if strings.HasPrefix(name, "__") {
		return &TableInvalidNameError{Kind: kind, Name: name, Reason: `must not start with the reserved prefix "__"`}
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9':
			if i == 0 {
				return &TableInvalidNameError{Kind: kind, Name: name, Reason: "must not start with a digit"}
			}
		default:
			return &TableInvalidNameError{Kind: kind, Name: name, Reason: "must contain only letters, digits, and underscores"}
		}
	}
	return nil
}

// validateCreateTableRequest validates a CreateTableRequest: table and
// column identifiers, duplicate column names, at most one AUTO_INCREMENT
// column, primary key columns that exist, and index columns that exist.
func validateCreateTableRequest(req *CreateTableRequest) error {
	if req.Database == "" {
		return &TableEmptyDatabaseError{}
	}
	if err := validateIdentifier("table", req.Name); err != nil {
		return err
	}
	if len(req.Columns) == 0 {
		return &TableNoColumnsError{}
	}

	colSet := make(map[string]struct{}, len(req.Columns))
	autoIncCount := 0
	for _, col := range req.Columns {
		if err := validateIdentifier("column", col.Name); err != nil {
			return err
		}
		lower := strings.ToLower(col.Name)
		if _, dup := colSet[lower]; dup {
			return &TableDuplicateColumnError{Column: col.Name}
		}
		colSet[lower] = struct{}{}

		if !isSupportedColumnType(col.Type) {
			return &TableUnsupportedColumnTypeError{Column: col.Name, Type: col.Type}
		}
		if col.AutoIncrement {
			autoIncCount++
		}
	}
	if autoIncCount > 1 {
		return &TableMultipleAutoIncrementError{}
	}

	for _, pk := range req.PrimaryKey {
		if _, ok := colSet[strings.ToLower(pk.Name)]; !ok {
			return &TablePrimaryKeyColumnNotFoundError{Column: pk.Name}
		}
	}

	for _, idx := range req.Indexes {
		if err := validateIndex(colSet, idx); err != nil {
			return err
		}
	}

	return nil
}

// validateNewColumn validates a column being appended by AlterAddColumn:
// identifier well-formedness, no duplicate against the table's existing
// columns, and a supported type. The AUTO_INCREMENT-count-at-most-one
// invariant is checked once, in applyAlterChanges, after every change in
// the request has been applied.
func validateNewColumn(def *TableDef, col ColumnDef) error {
	if err := validateIdentifier("column", col.Name); err != nil {
		return err
	}
	if _, err := findColumnIndex(def, col.Name); err == nil {
		return &TableDuplicateColumnError{Column: col.Name}
	}
	if !isSupportedColumnType(col.Type) {
		return &TableUnsupportedColumnTypeError{Column: col.Name, Type: col.Type}
	}
	return nil
}

// validateIndex validates a TableIndex definition: non-empty name, at
// least one column, and every named column present in colSet (a
// case-insensitively lower-cased set of the table's column names).
func validateIndex(colSet map[string]struct{}, idx TableIndex) error {
	if idx.Name == "" {
		return &TableInvalidNameError{Kind: "index", Name: idx.Name, Reason: "must not be empty"}
	}
	if len(idx.Columns) == 0 {
		return &TableIndexNoColumnsError{Index: idx.Name}
	}
	for _, c := range idx.Columns {
		if _, ok := colSet[strings.ToLower(c.Name)]; !ok {
			return &TableIndexColumnNotFoundError{Index: idx.Name, Column: c.Name}
		}
	}
	return nil
}

// columnNameSet returns the case-insensitively lower-cased set of def's
// column names, for use with validateIndex.
func columnNameSet(def *TableDef) map[string]struct{} {
	set := make(map[string]struct{}, len(def.Columns))
	for _, c := range def.Columns {
		set[strings.ToLower(c.Name)] = struct{}{}
	}
	return set
}

// findColumnIndex returns the index of the column named name (matched
// case-insensitively) in def.Columns, or TableColumnNotFoundError if none
// matches.
func findColumnIndex(def *TableDef, name string) (int, error) {
	for i, c := range def.Columns {
		if strings.EqualFold(c.Name, name) {
			return i, nil
		}
	}
	return -1, &TableColumnNotFoundError{Column: name}
}

// autoIncrementColumnName returns the name of the column with
// AutoIncrement set, or "" if none of cols has it. Callers have already
// validated that at most one does.
func autoIncrementColumnName(cols []ColumnDef) string {
	for _, c := range cols {
		if c.AutoIncrement {
			return c.Name
		}
	}
	return ""
}

// supportedColumnTypePrefixes are the SQL type keywords qrdb currently
// accepts for a column's Type string. Matching is a case-insensitive prefix
// test, so parameterized types ("VARCHAR(255)", "DECIMAL(10,2)") validate
// without a full SQL type grammar here -- the authoritative parse happens
// wherever the type string is consumed for row encoding. This deliberately
// mirrors the vocabulary entity.GoTypeToSQLType produces (see the doc
// comment on ColumnDef.Type) plus the additional MySQL type keywords a
// hand-written CREATE TABLE statement commonly uses.
var supportedColumnTypePrefixes = []string{
	"TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT",
	"DECIMAL", "NUMERIC", "FLOAT", "DOUBLE", "REAL", "BIT",
	"CHAR", "VARCHAR", "TINYTEXT", "TEXT", "MEDIUMTEXT", "LONGTEXT",
	"BINARY", "VARBINARY", "TINYBLOB", "BLOB", "MEDIUMBLOB", "LONGBLOB",
	"DATE", "DATETIME", "TIMESTAMP", "TIME", "YEAR",
	"BOOLEAN", "BOOL", "JSON", "ENUM", "SET",
}

// isSupportedColumnType reports whether t's leading keyword (case
// insensitive, ignoring any parenthesized length/precision suffix) is one
// of supportedColumnTypePrefixes.
func isSupportedColumnType(t string) bool {
	up := strings.ToUpper(strings.TrimSpace(t))
	if up == "" {
		return false
	}
	for _, prefix := range supportedColumnTypePrefixes {
		if strings.HasPrefix(up, prefix) {
			return true
		}
	}
	return false
}

// NextUserSequenceValue implements UserSequencer over the same
// SequenceServicer the table-ID allocator uses, under the client-sequence
// namespace (see UserSequenceName).
func (s *TableService) NextUserSequenceValue(ctx context.Context, database, name string) (uint64, error) {
	return s.seq.Allocate(ctx, database, UserSequenceName(name), 1)
}

// ResetUserSequence implements UserSequencer.
//
// It reaches SequenceServicer.Delete rather than Reset. Under
// materialize-on-first-use the two are observationally identical -- Peek
// reads 0 and the next allocation returns 1 either way -- but Reset writes
// a zero counter and so leaves the key behind forever, which is the leak
// go-qrdb#411 describes in the collection drop path. DROP SEQUENCE is the
// only caller (see pkg/sql/pgsql's handleSequenceDDL), so this reclaims the
// key with no change to what a SQL client can observe.
func (s *TableService) ResetUserSequence(ctx context.Context, database, name string) error {
	return s.seq.Delete(ctx, database, UserSequenceName(name))
}

// Compile-time assertion that the table catalog also serves client-named
// sequences.
var _ UserSequencer = (*TableService)(nil)
