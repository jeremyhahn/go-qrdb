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

package provider

import (
	"errors"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/catalog"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// DML for RelationalTable: INSERT/UPDATE/DELETE/REPLACE, with atomic
// secondary-index maintenance.
//
// Correctness requirement: a row's secondary-index entries live in the
// table's own keyspace (service.TableIndexPrefix), physically alongside the
// row itself, and MUST be written in the same TransactionServicer.Execute
// call as the row -- never as a second, independently-committed proposal.
// index.IndexServicer writes cannot be composed into that CAS, so using it
// for a relational table's secondary indexes would make index maintenance
// non-atomic with the row write: a crash (or a lost leadership mid-write)
// between the two would leave the index disagreeing with the table, and a
// UNIQUE constraint could never be enforced at all, since "check, then
// write" across two separate proposals always has a race window between
// them. Every write below builds ONE set of compares/ops covering the row
// key and every affected index entry, and proposes them through exactly one
// Execute call.
//
// UNIQUE enforcement rides on the same CAS: a Compare requiring the
// candidate unique-index entry's Version to be 0 (absent) is added
// alongside the row write, so a colliding INSERT/UPDATE fails the whole
// transaction atomically -- nothing is written, not even the row itself.
// See resolveDuplicate for how a failed CAS is turned into the specific
// sql.NewUniqueKeyErr (PRIMARY or a named index) that go-mysql-server's own
// INSERT/REPLACE/ON DUPLICATE KEY UPDATE execution loop
// (rowexec/insert.go) expects, which is what ultimately surfaces to a
// client as MySQL error 1062 (see sdk/go/gorm/error_translator.go).
// ---------------------------------------------------------------------------

// Compile-time interface assertions.
var (
	_ sqle.InsertableTable   = (*RelationalTable)(nil)
	_ sqle.UpdatableTable    = (*RelationalTable)(nil)
	_ sqle.DeletableTable    = (*RelationalTable)(nil)
	_ sqle.ReplaceableTable  = (*RelationalTable)(nil)
	_ sqle.TruncateableTable = (*RelationalTable)(nil)

	_ sqle.RowInserter = (*relationalWriter)(nil)
	_ sqle.RowUpdater  = (*relationalWriter)(nil)
	_ sqle.RowDeleter  = (*relationalWriter)(nil)
	_ sqle.RowReplacer = (*relationalWriter)(nil)
)

const (
	// maxRowKeySize bounds an encoded primary key (and, by construction,
	// every secondary-index entry key derived from it -- see indexEntryKey),
	// mirroring pkg/service/batch_service.go's maxImportKeySize: both are
	// ultimately bounded by pkg/protocol's uint32 key-length framing, but
	// this is the same conservative, well-under-that-ceiling value the
	// Import path already uses.
	maxRowKeySize = 65535

	// maxRowValueSize bounds a single encoded row (or index-entry value),
	// mirroring maxImportValueSize.
	maxRowValueSize = 4 << 20 // 4 MB

	// maxRowProposalBytes bounds the estimated encoded size of a single
	// INSERT/UPDATE/DELETE's compares+ops, mirroring maxImportProposalBytes
	// -- comfortably under go-quicraft's per-entry ceiling (proto.
	// MaxEntrySize, 8 MiB) to leave headroom for op-framing overhead this
	// estimate does not account for exactly.
	maxRowProposalBytes = 8 << 20 // 8 MiB

	// rowOpOverheadBytes is a conservative fixed per-operation estimate
	// (framing, key/value length prefixes) added on top of an op's own
	// key+value bytes, mirroring pkg/service's ddlOpOverheadBytes.
	rowOpOverheadBytes = 32

	// writeMaxAttempts bounds the number of compare-and-swap attempts
	// Insert/Update make when a failed attempt resolves to a transient race
	// (see resolveDuplicate) rather than a genuine duplicate -- e.g. the row
	// or index entry that caused the CAS to fail was concurrently deleted
	// between the failed Execute and the diagnostic read that explains it.
	writeMaxAttempts = 5
)

// ---------------------------------------------------------------------------
// indexSpec: shared between the read path (relational_table.go) and the
// write path (this file), so both derive identical physical index keys.
// ---------------------------------------------------------------------------

// indexSpecColumn resolves one column of a secondary index to its schema
// ordinal, declared SQL type, and nullability. nullable is load-bearing on
// the read path, not merely descriptive: a row with a NULL in ANY indexed
// column contributes no physical index entry at all (see indexSpec.encode),
// so a leading-prefix lookup that leaves a nullable column unbound could
// silently miss rows -- see indexSpec.pointPrefix.
type indexSpecColumn struct {
	name      string
	ordinal   int
	colType   sqle.Type
	fieldType string
	nullable  bool
}

// indexSpec resolves one secondary index (def.Indexes[i]) to its columns'
// schema ordinals, its uniqueness, and the KeyCodecs its indexed values are
// encoded with.
//
// Multi-column indexes are the reason codecs is a slice rather than a single
// codec. codecs[k-1] encodes the index's first k columns; codecs[len-1] is
// the full-tuple codec every write uses. Because catalog.KeyCodec composes
// its components through index.EncodeCompoundValue -- a plain concatenation
// of self-delimiting, individually order-preserving component encodings --
// the encoding of the first k columns is a byte-exact PREFIX of the
// encoding of all of them. That is what makes a leading-column-prefix
// lookup a bounded prefix scan over the physical index keys rather than a
// table scan, and it is why this reuses catalog.KeyCodec (which already
// solved composite-key encoding for composite PRIMARY KEYs) instead of
// introducing a second encoder.
type indexSpec struct {
	name    string
	comment string
	unique  bool

	// incarnation is the index's TableIndex.Incarnation: its entries live
	// under TableIndexPrefix(..., incarnation), never under its name.
	incarnation uint64

	// prefixed reports that at least one column declares a MySQL prefix
	// length (KEY idx (name(10))). Such an index is written and its
	// uniqueness enforced over the truncated value, exactly as MySQL does,
	// but it is never used to ANSWER a lookup: two distinct values sharing
	// a truncated prefix produce the same entry, so an entry match is not a
	// value match, and RelationalTable.PreciseMatch promises the planner
	// that a lookup this table answers needs no re-filtering.
	prefixed bool

	columns []indexSpecColumn
	codecs  []*catalog.KeyCodec // codecs[k-1] encodes the leading k columns
}

// valueCodec returns the codec covering every column of the index -- the
// one every write path encodes a row's full index tuple with.
func (s indexSpec) valueCodec() *catalog.KeyCodec { return s.codecs[len(s.codecs)-1] }

// ordinals returns the schema ordinals of the index's columns, in declared
// index order.
func (s indexSpec) ordinals() []int {
	out := make([]int, len(s.columns))
	for i, c := range s.columns {
		out[i] = c.ordinal
	}
	return out
}

// indexColumns renders the spec's columns in the form qrdbIndex reports to
// the query planner (and, through it, to information_schema.STATISTICS).
func (s indexSpec) indexColumns() []indexColumn {
	out := make([]indexColumn, len(s.columns))
	for i, c := range s.columns {
		out[i] = indexColumn{name: c.name, fieldType: c.fieldType}
	}
	return out
}

// encode encodes row's full index tuple. ok is false when any indexed
// column is NULL (or absent from row), which contributes no index entry at
// all: that is what makes a UNIQUE index over a nullable column permit any
// number of NULL-bearing rows, matching MySQL, and it is why a lookup that
// leaves a nullable column unbound cannot be answered from the index.
func (s indexSpec) encode(row sqle.Row) (encoded string, ok bool, err error) {
	vals := make([]any, len(s.columns))
	for i, c := range s.columns {
		if c.ordinal >= len(row) || row[c.ordinal] == nil {
			return "", false, nil
		}
		vals[i] = row[c.ordinal]
	}
	encoded, err = s.valueCodec().EncodeValues(vals)
	if err != nil {
		return "", false, err
	}
	return encoded, true, nil
}

// buildIndexSpecs resolves every def.Indexes entry to an indexSpec. An
// index with no columns, or one naming a column that no longer exists on
// schema (which validateIndex prevents at CREATE/ALTER INDEX time, so this
// is defensive rather than an expected path), is silently skipped rather
// than failing table resolution outright.
func buildIndexSpecs(def *service.TableDef, schema sqle.Schema) ([]indexSpec, error) {
	specs := make([]indexSpec, 0, len(def.Indexes))
	for _, idx := range def.Indexes {
		spec, err := buildIndexSpec(def, schema, idx)
		if err != nil {
			if isIndexResolutionError(err) {
				continue
			}
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// isIndexResolutionError reports whether err is one of the two "this index
// cannot be resolved against the current schema" conditions buildIndexSpecs
// skips over, as distinct from a genuine encoding failure it must surface.
func isIndexResolutionError(err error) bool {
	var noColumns *IndexNoColumnsError
	var notFound *ColumnNotFoundOnTableError
	return errors.As(err, &noColumns) || errors.As(err, &notFound)
}

// primaryKeyIndexOf renders def's declared primary key as an index
// definition, so RelationalTable can resolve it through the very same
// buildIndexSpec every secondary index goes through -- one resolution, one
// encoding, one lookup shape, whatever the key's arity. The primary key is
// unique by definition and carries no comment.
func primaryKeyIndexOf(def *service.TableDef) service.TableIndex {
	cols := make([]service.IndexCol, len(def.PrimaryKey))
	for i, pk := range def.PrimaryKey {
		cols[i] = service.IndexCol(pk)
	}
	return service.TableIndex{Name: primaryIndexName, Columns: cols, Unique: true}
}

// buildIndexSpec resolves a single catalog index definition against schema,
// building one KeyCodec per leading-column prefix (see indexSpec.codecs).
func buildIndexSpec(def *service.TableDef, schema sqle.Schema, idx service.TableIndex) (indexSpec, error) {
	if len(idx.Columns) == 0 {
		return indexSpec{}, &IndexNoColumnsError{Index: idx.Name}
	}

	columns := make([]indexSpecColumn, len(idx.Columns))
	keyCols := make([]sqle.IndexColumn, len(idx.Columns))
	prefixed := false
	for i, col := range idx.Columns {
		ordinal := columnOrdinalOf(schema, col.Name)
		if ordinal < 0 {
			return indexSpec{}, &ColumnNotFoundOnTableError{Table: def.Name, Column: col.Name}
		}
		if col.PrefixLength > 0 {
			prefixed = true
		}
		columns[i] = indexSpecColumn{
			name:      col.Name,
			ordinal:   ordinal,
			colType:   schema[ordinal].Type,
			fieldType: indexFieldTypeOf(schema[ordinal].Type),
			nullable:  schema[ordinal].Nullable,
		}
		keyCols[i] = sqle.IndexColumn{Name: col.Name, Length: int64(col.PrefixLength)}
	}

	codecs := make([]*catalog.KeyCodec, len(keyCols))
	for k := range keyCols {
		codec, err := indexValueKeyCodec(def, keyCols[:k+1])
		if err != nil {
			return indexSpec{}, err
		}
		codecs[k] = codec
	}

	return indexSpec{
		name:        idx.Name,
		comment:     idx.Comment,
		unique:      idx.Unique,
		incarnation: idx.Incarnation,
		prefixed:    prefixed,
		columns:     columns,
		codecs:      codecs,
	}, nil
}

// columnOrdinalOf returns the index of the column named name in schema, or
// -1 when no such column exists.
func columnOrdinalOf(schema sqle.Schema, name string) int {
	for i, c := range schema {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// indexEntryKey returns the physical key for one secondary-index entry.
//
// A unique index needs no primary-key suffix: at most one row may ever hold
// a given value, so the value alone is both a valid entry key and exactly
// what a single Compare against that key needs in order to enforce the
// constraint atomically (see resolveDuplicate) -- there is no other row's
// entry it could ever collide with. A non-unique index appends the row's
// own encoded primary key so that multiple rows sharing a value get
// distinct physical keys instead of colliding into one; encodedVal is
// always self-delimiting (catalog.KeyCodec / index.EncodeCompoundValue), so
// a prefix scan for encodedVal alone (see lookupIndexValue) can never spill
// into a different value's entries regardless of what pkKey follows it.
func indexEntryKey(database string, tableID, generation, incarnation uint64, unique bool, encodedVal, pkKey string) string {
	prefix := service.TableIndexPrefix(database, tableID, generation, incarnation)
	if unique {
		return prefix + encodedVal
	}
	return prefix + encodedVal + pkKey
}

// ---------------------------------------------------------------------------
// Generation fencing (go-qrdb#547)
//
// Every DML commit -- autocommit (below) and buffered/interactive
// transaction (WriteSet.commit, writeset.go) alike -- includes
// generationCompare in the SAME Raft transaction as its row and index
// writes: TRUNCATE (TableService.TruncateTable) advances a table's
// generation atomically, and a commit whose Compare list still names the
// generation it was PLANNED under fails the instant that no longer matches,
// with no separate read of its own -- the Compare rides in a transaction
// the write already has to propose. Only on the FAILURE path does
// checkGenerationStale spend one extra read to tell "the generation moved"
// apart from "a candidate key collided".
//
// This is what keeps the guard's cost from scaling with row count, not what
// makes it free: resolving the table AT ALL, once per statement
// (TableService.GetTable), already performs one linearizable read of the
// generation key (readGeneration) -- the same statement-scoped cost
// GetTable already paid for the journal and metadata keys before this
// existed. A multi-row INSERT resolves the table once and calls Insert once
// per row on that SAME handle, so this cost is O(1) per statement, not
// O(rows) -- see TestGenerationFencing_BulkInsert_NoPerRowConsensusRead.
// ---------------------------------------------------------------------------

// generationCompare returns the Compare that fences a DML commit on the
// generation g names -- see the section doc
// comment above.
//
// Generation 0 -- a table that has never been TRUNCATEd -- is compared
// against an EMPTY Value rather than EncodeTableGeneration(0)'s eight zero
// bytes: the generation key itself does not exist until the first TRUNCATE
// ever writes it (see TableService.TruncateTable's revisionOrAbsent), and
// CompareValue's absent-key semantics (mvcc.Evaluate, pebbleEvaluateCompare)
// read an absent key's value as empty, not as any particular byte pattern.
// Comparing against the encoded zero would therefore fail on EVERY table
// that had never been truncated -- which, before a table's first TRUNCATE,
// is every table -- rejecting every single DML statement. Generation 1+
// compares against the exact encoding TruncateTable wrote, which by then is
// genuinely present.
func (t *RelationalTable) generationCompare(g *tableGeneration) service.Compare {
	cmp := service.Compare{
		Key:    []byte(service.TableGenerationKey(t.def.Database, t.def.TableID)),
		Target: service.CompareValue,
		Result: service.CompareEqual,
	}
	if g.generation > 0 {
		cmp.Value = service.EncodeTableGeneration(g.generation)
	}
	return cmp
}

// metadataCompare returns the Compare that fences a DML commit on the
// definition t was resolved from: the metadata record's ModRevision as
// GetTable read it (TableDef.MetadataRevision). Any DDL rewrites that record
// -- CREATE, DROP or RENAME INDEX, ALTER, RENAME TABLE, DROP TABLE -- so a
// statement planned against an index set the table no longer has fails
// instead of committing index maintenance for the wrong one: an index
// created meanwhile would otherwise permanently lack this row's entry, and a
// dropped one would gain entries after its reclamation. ok is false for a
// definition read without its revision, which cannot be fenced this way.
func (t *RelationalTable) metadataCompare() (cmp service.Compare, ok bool) {
	if t.def.MetadataRevision == 0 {
		return service.Compare{}, false
	}
	return service.Compare{
		Key:         []byte(service.TableMetadataKey(t.def.Database, t.def.Name)),
		Target:      service.CompareModRevision,
		Result:      service.CompareEqual,
		ModRevision: t.def.MetadataRevision,
	}, true
}

// resolutionCompares returns every Compare a DML commit carries to fence
// what its statement resolved: the generation, and the definition.
func (t *RelationalTable) resolutionCompares(g *tableGeneration) []service.Compare {
	compares := []service.Compare{t.generationCompare(g)}
	if cmp, ok := t.metadataCompare(); ok {
		compares = append(compares, cmp)
	}
	return compares
}

// resolutionFence is the per-table fence a buffered transaction registers
// (WriteSet.RequireResolution): the same Compares an autocommit statement
// carries, keyed by the generation key, plus what they expect.
func (t *RelationalTable) resolutionFence(g *tableGeneration) resolutionGuard {
	return resolutionGuard{
		key:              string(service.TableGenerationKey(t.def.Database, t.def.TableID)),
		compares:         t.resolutionCompares(g),
		database:         t.def.Database,
		table:            t.def.Name,
		generation:       g.generation,
		metadataKey:      service.TableMetadataKey(t.def.Database, t.def.Name),
		metadataRevision: t.def.MetadataRevision,
	}
}

// generationFencedChunkSize bounds how many ops one compare-and-swap
// proposal carries when a caller needs to write a POTENTIALLY LARGE,
// enumerated set of physical keys under a resolution fence -- CREATE
// INDEX's build (indexBuildInserter), which has no natural per-row CAS the
// way ordinary DML does. See writeChunkedWithResolutionFence.
const generationFencedChunkSize = 500

// writeChunkedWithResolutionFence submits ops in chunks of at most
// generationFencedChunkSize, each chunk proposed as its OWN compare-and-swap
// guarded by t's current generationCompare AND by the table's metadata
// record still being at metadataRevision. A TRUNCATE, or a DDL such as DROP
// INDEX, that commits in the window between chunks fails the NEXT chunk's
// CAS immediately, stopping further writes into a keyspace the reclaim
// sweeper is already draining -- rather than a single up-front check
// followed by an unconditional write loop, which only detects a race that
// happened before the check, not one that happens during the write itself.
//
// metadataRevision is passed rather than taken from t.def: the index being
// built was added after t resolved the table, so the revision to hold is
// the one that lists it (see catalogIncarnation).
//
// This is deliberately NOT used on the per-row DML insert path
// (relational_writer.go's Insert/Update/Delete): those already carry the
// resolution Compares in the SAME proposal their own row/index write makes,
// at zero added cost. This helper exists for a caller with NO such natural
// per-operation proposal to ride along in -- CREATE INDEX's build, which
// enumerates and writes an unbounded number of keys.
func (t *RelationalTable) writeChunkedWithResolutionFence(ctx *sqle.Context, ops []service.Op, metadataRevision int64) error {
	if t.txnSvc == nil {
		return &TransactionServiceUnavailableError{Table: t.def.Name}
	}
	g := t.gen()
	metaKey := service.TableMetadataKey(t.def.Database, t.def.Name)
	compares := []service.Compare{
		t.generationCompare(g),
		{Key: []byte(metaKey), Target: service.CompareModRevision, Result: service.CompareEqual, ModRevision: metadataRevision},
	}
	for start := 0; start < len(ops); start += generationFencedChunkSize {
		end := min(start+generationFencedChunkSize, len(ops))
		resp, err := t.txnSvc.Execute(ctx, compares, ops[start:end], nil)
		if err != nil {
			return err
		}
		if !resp.Succeeded {
			if staleErr := t.checkGenerationStale(ctx, g); staleErr != nil {
				return staleErr
			}
			if t.mvccSvc == nil {
				return &RelationalMVCCUnavailableError{Table: t.def.Name}
			}
			live, err := t.mvccSvc.GetRevision(ctx, metaKey)
			if err != nil && !isNotFound(err) {
				return err
			}
			if err != nil || live == nil || live.ModRevision != metadataRevision {
				return t.definitionStale("its definition changed during the index build")
			}
			return &TransactionConflictRetriesExhaustedError{Table: t.def.Name}
		}
	}
	return nil
}

// checkGenerationStale reports a wrapped *service.TableGenerationStaleError
// when t's table has been truncated since g was resolved, by reading the live
// generation directly -- only ever after a CAS carrying the generation
// Compare has failed for some reason, or to re-prove an old pin. A nil
// return means it was NOT a stale generation.
//
// The error is wrapped in sql.ErrLockDeadlock -- go-mysql-server's own
// serialization-failure vocabulary, MySQL ER_LOCK_DEADLOCK/1213 over the
// wire -- exactly as QRDBSession.CommitTransaction wraps the buffered
// transaction's equivalent conflict, so autocommit and buffered DML map to
// the identical MySQL error code and, via pkg/sql/pgsql's
// sql.ErrLockDeadlock.Is check, the identical PostgreSQL SQLSTATE 40001:
// "retry the statement".
func (t *RelationalTable) checkGenerationStale(ctx *sqle.Context, g *tableGeneration) error {
	live, err := service.TableGeneration(ctx, t.kvSvc, t.def.Database, t.def.TableID)
	if err != nil {
		return err
	}
	if live == g.generation {
		return nil
	}
	stale := &service.TableGenerationStaleError{
		Database: t.def.Database, Name: t.def.Name,
		Expected: g.generation, Actual: live,
	}
	return sqle.ErrLockDeadlock.Wrap(stale, stale.Error())
}

// checkResolutionStale is checkGenerationStale for a DML commit, which is
// also fenced on the definition (metadataCompare): after a failed CAS it
// reports which of the two moved, if either. A nil return means neither
// did, and the caller proceeds to its duplicate-vs-transient classification
// (resolveDuplicate).
func (t *RelationalTable) checkResolutionStale(ctx *sqle.Context, g *tableGeneration) error {
	if err := t.checkGenerationStale(ctx, g); err != nil {
		return err
	}
	if _, fenced := t.metadataCompare(); !fenced || t.mvccSvc == nil {
		return nil
	}
	live, err := t.mvccSvc.GetRevision(ctx, service.TableMetadataKey(t.def.Database, t.def.Name))
	if err != nil {
		if isNotFound(err) {
			return t.definitionStale("the table was renamed or dropped")
		}
		return err
	}
	if live.ModRevision != t.def.MetadataRevision {
		return t.definitionStale("its definition changed")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Size limits
// ---------------------------------------------------------------------------

func checkKeySize(table, key string) error {
	if len(key) > maxRowKeySize {
		return &RowKeyTooLargeError{Table: table, Size: len(key), Max: maxRowKeySize}
	}
	return nil
}

func checkValueSize(table string, value []byte) error {
	if len(value) > maxRowValueSize {
		return &RowValueTooLargeError{Table: table, Size: len(value), Max: maxRowValueSize}
	}
	return nil
}

// checkProposalSize estimates the encoded size of compares+ops the same
// conservative way pkg/service's DDL/Import chunkers do (key+value bytes
// plus a fixed per-operation overhead) and rejects the statement before it
// is ever proposed when that estimate exceeds maxRowProposalBytes.
func checkProposalSize(table string, compares []service.Compare, ops []service.Op) error {
	total := 0
	for _, c := range compares {
		total += len(c.Key) + len(c.Value) + rowOpOverheadBytes
	}
	for _, op := range ops {
		total += len(op.Key) + len(op.Value) + rowOpOverheadBytes
	}
	if total > maxRowProposalBytes {
		return &RowTransactionTooLargeError{Table: table, Size: total, Max: maxRowProposalBytes}
	}
	return nil
}

// ---------------------------------------------------------------------------
// requireWritable
// ---------------------------------------------------------------------------

// requireWritable reports TransactionServiceUnavailableError when t was
// constructed without WithTransactionService, the dependency every DML
// method below requires.
func (t *RelationalTable) requireWritable() error {
	if t.txnSvc == nil {
		return &TransactionServiceUnavailableError{Table: t.def.Name}
	}
	return nil
}

// ---------------------------------------------------------------------------
// DML accessors
// ---------------------------------------------------------------------------

// Inserter returns a RowInserter for the table.
func (t *RelationalTable) Inserter(_ *sqle.Context) sqle.RowInserter {
	return &relationalWriter{table: t}
}

// Deleter returns a RowDeleter for the table.
func (t *RelationalTable) Deleter(_ *sqle.Context) sqle.RowDeleter {
	return &relationalWriter{table: t}
}

// Updater returns a RowUpdater for the table. This replaces the
// Phase-5 stub that unconditionally failed every UPDATE.
func (t *RelationalTable) Updater(_ *sqle.Context) sqle.RowUpdater {
	return &relationalWriter{table: t}
}

// Replacer returns a RowReplacer for the table. RowReplacer requires
// nothing beyond RowInserter+RowDeleter (relationalWriter already
// implements both), so REPLACE INTO composes for free: go-mysql-server's
// insertIter calls Insert, and on a duplicate-key error (see
// resolveDuplicate/sql.NewUniqueKeyErr) calls Delete with the *existing*
// conflicting row before retrying Insert -- see rowexec/insert.go. Because
// Delete recomputes a row's index entries from the row it is given, this
// naturally deletes the old row's index entries atomically with the old
// row itself, and the retried Insert atomically (re)writes the new row and
// its own index entries -- exactly the same per-write atomicity guarantee
// every other write in this file has, applied twice in sequence rather than
// once.
func (t *RelationalTable) Replacer(_ *sqle.Context) sqle.RowReplacer {
	return &relationalWriter{table: t}
}

// Truncate advances the table to a new generation in one Raft transaction
// (TableServicer.TruncateTable) rather than scanning and deleting its rows
// and index entries -- which is what makes it O(1) regardless of table
// size. The table's current rows and index entries, at the OLD generation's
// key prefixes, are simply never resolved again by anything that reads the
// table normally (see TableDataPrefix/TableIndexPrefix); TruncateTable
// publishes a ReclaimJob in the same transaction so the background
// ReclaimSweeper deletes them without racing whatever DML was still
// committing under the old generation up to the instant this commits (see
// go-qrdb#547).
//
// MySQL resets AUTO_INCREMENT on TRUNCATE, returning the table to its
// just-created state; PostgreSQL's plain TRUNCATE does not touch a
// sequence it owns. Both behaviors go through the identical generation
// bump -- rows are gone either way -- and differ only in whether
// TruncateTable is also asked to reset the sequence, which is exactly what
// t.postgres (see WithPostgreSQLDialect) decides here.
//
// Unlike real MySQL's row count for DELETE, TRUNCATE's own affected-rows
// count is not derived from the table's prior contents (MySQL does not
// report one either); this always returns 0.
func (t *RelationalTable) Truncate(ctx *sqle.Context) (int, error) {
	if t.tableSvc == nil {
		return 0, &TableServiceUnavailableError{Table: t.def.Name}
	}
	if err := flushPendingWriteSet(ctx); err != nil {
		return 0, err
	}

	// The tenant is bound onto the context here, not added as a parameter
	// to TruncateTable: TableService.TruncateTable derives it via
	// tenant.FromContext, the same way the rest of the SQL layer derives
	// its own session tenant, so the tenant/database ReclaimJob and
	// ReclaimIndexEntry records need (see go-qrdb#547's sweeper-scoping
	// design) travel through without widening TableServicer's public
	// signature.
	tenantCtx := tenant.WithTenantID(ctx, sessionTenantIDOf(ctx))
	newDef, err := t.tableSvc.TruncateTable(tenantCtx, t.def.Database, t.def.Name, !t.postgres)
	if err != nil {
		return 0, err
	}

	t.setGeneration(newDef.Generation, newDef.GenerationReadAt)
	t.autoInc.discard()

	return 0, nil
}

// ---------------------------------------------------------------------------
// relationalWriter
// ---------------------------------------------------------------------------

// relationalWriter implements sql.RowInserter, sql.RowUpdater,
// sql.RowDeleter, and sql.RowReplacer for a RelationalTable by delegating
// to TransactionServicer -- see the package-level doc comment above for why
// every write composes its row mutation and secondary-index maintenance
// into a single compare-and-swap.
type relationalWriter struct {
	table *RelationalTable
}

// StatementBegin marks the start of a statement. Inside an interactive
// transaction it opens an implicit statement savepoint on the transaction's
// WriteSet, so a statement that fails part way can be undone whole (see
// writeset_statement.go). Outside one it does nothing: each autocommit row
// write is its own compare-and-swap, committed before the next row is
// written, and there is nothing in flight to check-point.
func (w *relationalWriter) StatementBegin(ctx *sqle.Context) {
	if ws := statementWriteSet(ctx); ws != nil {
		ws.beginStatement()
	}
}

// DiscardChanges undoes everything the failed statement buffered into the
// interactive transaction's WriteSet since StatementBegin, across every
// table the statement edited. The transaction's earlier statements, its
// row locks and its reads are kept. Outside a transaction it does nothing:
// the rows the statement already wrote are committed (see StatementBegin).
func (w *relationalWriter) DiscardChanges(ctx *sqle.Context, _ error) error {
	if ws := statementWriteSet(ctx); ws != nil {
		ws.discardStatement()
	}
	return nil
}

// StatementComplete marks the end of a statement, keeping what it buffered
// -- unless the statement's context was cancelled (KILL QUERY, a client
// disconnect, a deadline). go-mysql-server's table-editor iterator reports
// a cancellation noticed between rows from Next without recording it as
// the statement's error, so a killed statement reaches StatementComplete,
// not DiscardChanges; it is discarded here, as a failed one is.
func (w *relationalWriter) StatementComplete(ctx *sqle.Context) error {
	ws := statementWriteSet(ctx)
	if ws == nil {
		return nil
	}
	if ctx.Err() != nil {
		ws.discardStatement()
		return nil
	}
	ws.completeStatement()
	return nil
}

// Close finalizes the writer. No-op: an autocommit write is durable as soon
// as its Execute call returns, and a buffered one is the transaction's to
// commit.
func (w *relationalWriter) Close(_ *sqle.Context) error { return nil }

// Insert inserts row, together with one physical entry per secondary index
// row's columns participate in, atomically (see the package doc comment).
// The row's own primary key and every UNIQUE index's new value must not
// already exist; a collision on either fails the whole transaction and
// surfaces as a MySQL 1062 duplicate-entry error (see resolveDuplicate).
//
// For a table with no declared primary key, the row's key is a freshly
// allocated hidden row id (see relational_rowid.go), so the PRIMARY compare
// below can never lose: two identical rows are two distinct rows, exactly as
// in MySQL.
//
// When an interactive transaction is open (see RelationalTable.
// activeWriteSet), the write is buffered into its WriteSet instead of being
// proposed immediately -- see bufferInsert.
func (w *relationalWriter) Insert(ctx *sqle.Context, row sqle.Row) error {
	t := w.table
	g := t.gen()
	if err := t.requireWritable(); err != nil {
		return err
	}

	row, err := t.allocateRowID(ctx, row)
	if err != nil {
		return err
	}

	pk, err := t.keyCodec.Encode(row)
	if err != nil {
		return err
	}
	if err := checkKeySize(t.def.Name, pk); err != nil {
		return err
	}
	encodedRow, err := t.rowCodec.Encode(row)
	if err != nil {
		return err
	}
	if err := checkValueSize(t.def.Name, encodedRow); err != nil {
		return err
	}
	rowKey := g.dataPrefix + pk

	actions, candidates, err := t.buildInsertIndexWrites(g, row, pk)
	if err != nil {
		return err
	}
	candidates = append([]uniqueCandidate{{key: rowKey, isPK: true}}, candidates...)

	// Pessimistic mode locks the keys the row is about to occupy and reads
	// them at their latest committed version, so the duplicate check below
	// sees a concurrent inserter's committed row instead of failing on it at
	// COMMIT.
	if err := t.lockForWrite(ctx, candidateKeys(candidates)); err != nil {
		return err
	}

	ws, err := t.activeWriteSet(ctx)
	if err != nil {
		return err
	}
	if ws != nil {
		return t.bufferInsert(ctx, ws, g, candidates, rowKey, encodedRow, actions)
	}

	for attempt := 0; attempt < writeMaxAttempts; attempt++ {
		idxOps, idxCompares := actionsToOpsAndCompares(actions)
		compares := append([]service.Compare{
			{Key: []byte(rowKey), Target: service.CompareVersion, Result: service.CompareEqual, Version: 0},
		}, t.resolutionCompares(g)...)
		compares = append(compares, idxCompares...)
		ops := append([]service.Op{{Type: service.OpPut, Key: []byte(rowKey), Value: encodedRow}}, idxOps...)

		if err := checkProposalSize(t.def.Name, compares, ops); err != nil {
			return err
		}

		resp, err := t.txnSvc.Execute(ctx, compares, ops, nil)
		if err != nil {
			return err
		}
		if resp.Succeeded {
			return nil
		}

		if staleErr := t.checkResolutionStale(ctx, g); staleErr != nil {
			return staleErr
		}

		dupErr, transient, rerr := t.resolveDuplicate(ctx, g, candidates)
		if rerr != nil {
			return rerr
		}
		if !transient {
			return dupErr
		}
		// transient: whatever caused this attempt to lose the race is gone
		// on recheck (e.g. a concurrent DELETE); retry.
	}
	return &TransactionConflictRetriesExhaustedError{Table: t.def.Name}
}

// bufferInsert is Insert's interactive-transaction path: it first checks
// candidates against the transaction's own view (buffered writes overlaid
// on the pinned snapshot, via resolveDuplicate/t.txGet) so a duplicate --
// including one this SAME transaction already buffered, which the
// commit-time compare-and-swap alone cannot catch since nothing has been
// proposed yet -- is reported synchronously, at INSERT time, exactly as
// autocommit Insert reports it. Only once every candidate is confirmed
// absent does it buffer the row and its index entries into ws.
func (t *RelationalTable) bufferInsert(ctx *sqle.Context, ws *WriteSet, g *tableGeneration, candidates []uniqueCandidate, rowKey string, encodedRow []byte, actions []indexAction) error {
	if dupErr, transient, err := t.resolveDuplicate(ctx, g, candidates); err != nil {
		return err
	} else if !transient {
		return dupErr
	}

	if err := ws.PutUnique(ctx, rowKey, encodedRow); err != nil {
		return err
	}
	return applyActionsToWriteSet(ctx, ws, actions)
}

// indexAction describes one secondary-index physical entry an Insert or
// Update must write or delete, in a form that is agnostic to how it is
// ultimately applied: as part of a single autocommit compare-and-swap (see
// actionsToOpsAndCompares) or buffered into an open interactive
// transaction's WriteSet (see applyActionsToWriteSet). unique marks an
// entry that must not already exist -- a newly-occupied UNIQUE index value
// -- which the autocommit path guards with a Compare and the buffered path
// with WriteSet.PutUnique; delete marks removal of an entry, mutually
// exclusive with unique.
type indexAction struct {
	key    string
	value  []byte
	delete bool
	unique bool
}

// actionsToOpsAndCompares renders actions into the Op/Compare lists an
// autocommit compare-and-swap proposes alongside the row's own write.
func actionsToOpsAndCompares(actions []indexAction) (ops []service.Op, compares []service.Compare) {
	for _, a := range actions {
		if a.delete {
			ops = append(ops, service.Op{Type: service.OpDelete, Key: []byte(a.key)})
			continue
		}
		ops = append(ops, service.Op{Type: service.OpPut, Key: []byte(a.key), Value: a.value})
		if a.unique {
			compares = append(compares, service.Compare{
				Key: []byte(a.key), Target: service.CompareVersion, Result: service.CompareEqual, Version: 0,
			})
		}
	}
	return ops, compares
}

// applyActionsToWriteSet buffers every action into ws, via PutUnique for a
// unique entry, Put otherwise, or Delete for a removal.
func applyActionsToWriteSet(ctx *sqle.Context, ws *WriteSet, actions []indexAction) error {
	for _, a := range actions {
		var err error
		switch {
		case a.delete:
			err = ws.Delete(ctx, a.key)
		case a.unique:
			err = ws.PutUnique(ctx, a.key, a.value)
		default:
			err = ws.Put(ctx, a.key, a.value)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// buildInsertIndexWrites returns one indexAction per secondary index every
// one of whose columns is non-NULL in row (see indexEntryKey and
// indexSpec.encode), and the matching uniqueCandidate list resolveDuplicate
// uses to explain a failed CAS or a buffered-transaction duplicate.
//
// For a multi-column index the encoded value is the whole tuple, so a
// UNIQUE index's Compare guards the tuple rather than its leading column:
// ('a','x') and ('a','y') occupy two distinct entries and coexist, exactly
// as they do in MySQL.
func (t *RelationalTable) buildInsertIndexWrites(g *tableGeneration, row sqle.Row, pk string) (actions []indexAction, candidates []uniqueCandidate, err error) {
	for _, spec := range t.indexSpecs {
		encoded, ok, eerr := spec.encode(row)
		if eerr != nil {
			return nil, nil, eerr
		}
		if !ok {
			continue // A NULL in any indexed column contributes no entry.
		}
		key := indexEntryKey(t.def.Database, t.def.TableID, g.generation, spec.incarnation, spec.unique, encoded, pk)
		if err := checkKeySize(t.def.Name, key); err != nil {
			return nil, nil, err
		}
		actions = append(actions, indexAction{key: key, value: []byte(pk), unique: spec.unique})
		if spec.unique {
			candidates = append(candidates, uniqueCandidate{key: key, indexName: spec.name})
		}
	}
	return actions, candidates, nil
}

// Delete deletes row and every secondary-index entry it participates in,
// atomically. Unlike Insert/Update, Delete never fails on a compare: an
// empty Compare list is vacuously satisfied (see TransactionServicer.
// Execute), so deleting a row that has already been deleted (e.g. by a
// concurrent statement) is a benign no-op rather than an error, matching
// ordinary SQL DELETE semantics for a row that no longer matches.
//
// When an interactive transaction is open, the deletion is buffered into
// its WriteSet instead of being proposed immediately; WriteSet.Delete still
// derives a commit-time precondition from the pinned snapshot (see that
// method), so a row concurrently modified since the snapshot was taken
// still causes a conflict at COMMIT, even though this Delete call itself
// never fails.
func (w *relationalWriter) Delete(ctx *sqle.Context, row sqle.Row) error {
	t := w.table
	g := t.gen()
	if err := t.requireWritable(); err != nil {
		return err
	}

	pk, err := t.keyCodec.Encode(row)
	if err != nil {
		return err
	}
	rowKey := g.dataPrefix + pk

	actions := []indexAction{{key: rowKey, delete: true}}
	for _, spec := range t.indexSpecs {
		encoded, ok, eerr := spec.encode(row)
		if eerr != nil {
			return eerr
		}
		if !ok {
			continue
		}
		key := indexEntryKey(t.def.Database, t.def.TableID, g.generation, spec.incarnation, spec.unique, encoded, pk)
		actions = append(actions, indexAction{key: key, delete: true})
	}

	// Pessimistic mode holds the row being deleted. A row the transaction
	// read under its lock is the row the statement matched; one that reached
	// Delete another way (a foreign-key cascade, or a read that took no
	// lock) is locked here and must still be the row the statement matched
	// -- see lockMatchedRow.
	if _, pessimistic := t.writeLock(ctx); pessimistic {
		if err := t.lockMatchedRow(ctx, rowKey, row); err != nil {
			return err
		}
	}

	ws, err := t.activeWriteSet(ctx)
	if err != nil {
		return err
	}
	if ws != nil {
		return applyActionsToWriteSet(ctx, ws, actions)
	}

	// As in Update: a row read under a lock was read at a known revision,
	// and the autocommit delete is guarded on it, so a writer that took no
	// lock cannot have its change deleted.
	rowGuard, guarded := t.lockedRowGuard(ctx, rowKey)
	ops, _ := actionsToOpsAndCompares(actions)
	compares := t.resolutionCompares(g)
	if guarded {
		compares = append(compares, rowGuard)
	}
	if err := checkProposalSize(t.def.Name, compares, ops); err != nil {
		return err
	}

	resp, err := t.txnSvc.Execute(ctx, compares, ops, nil)
	if err != nil {
		return err
	}
	if resp.Succeeded {
		return nil
	}
	if staleErr := t.checkResolutionStale(ctx, g); staleErr != nil {
		return staleErr
	}
	if guarded {
		moved, gerr := t.rowGuardFailed(ctx, rowGuard)
		if gerr != nil {
			return gerr
		}
		if moved {
			return rowChangedError(rowKey)
		}
	}
	// The generation compare and the row guard are the only preconditions
	// this proposal carries, so a failure that is neither should not happen;
	// report it as a conflict rather than silently treating it as success.
	return &TransactionConflictRetriesExhaustedError{Table: t.def.Name}
}

// Update updates a row from old to newRow, atomically maintaining every
// secondary-index entry whose physical key or value changes as a result
// (see diffIndexEntries) -- including every entry belonging to a changed
// primary key, which changes the *row's own* storage key and, for a
// non-unique index, every one of its physical entry keys too, since those
// are suffixed with the primary key (see indexEntryKey).
//
// In autocommit mode, Update does not guard the row's own key with a
// ModRevision compare: relationalWriter's autocommit path has no isolation
// story beyond a single statement, and neither does this table's sibling
// KV/entity writers (kvWriter.Update, entityWriter.Update), which this
// deliberately stays consistent with. The only compares an autocommit
// Update proposes are therefore UNIQUE-index absence checks (and, when the
// primary key changes, a PRIMARY absence check on the new key) -- exactly
// the same "must not already exist" shape Insert uses, so any CAS failure
// here is unambiguously a duplicate-key condition, not a stale-read race,
// and resolveDuplicate handles both identically.
//
// Inside an interactive transaction (see RelationalTable.activeWriteSet),
// the row's own key IS guarded by a snapshot-derived ModRevision compare --
// see bufferUpdate -- giving Update genuine first-committer-wins isolation
// for the lifetime of that transaction, which a single autocommit statement
// does not need (its own CAS is already the only write in flight).
//
// For a table keyed by the hidden row id, pkChanged is always false: newRow
// is derived from old with the statement's SET list applied, no SET can name
// the hidden column (go-mysql-server rejects that outright), and old's row
// id came from the key the row was read under. So the row is always rewritten
// in place under its existing key, and only index entries whose own values
// actually changed are touched.
func (w *relationalWriter) Update(ctx *sqle.Context, old, newRow sqle.Row) error {
	t := w.table
	g := t.gen()
	if err := t.requireWritable(); err != nil {
		return err
	}

	oldPK, err := t.keyCodec.Encode(old)
	if err != nil {
		return err
	}
	newPK, err := t.keyCodec.Encode(newRow)
	if err != nil {
		return err
	}
	if err := checkKeySize(t.def.Name, newPK); err != nil {
		return err
	}
	encodedNewRow, err := t.rowCodec.Encode(newRow)
	if err != nil {
		return err
	}
	if err := checkValueSize(t.def.Name, encodedNewRow); err != nil {
		return err
	}

	oldRowKey := g.dataPrefix + oldPK
	newRowKey := g.dataPrefix + newPK
	pkChanged := oldPK != newPK

	actions, candidates, err := t.diffIndexEntries(g, old, newRow, oldPK, newPK, pkChanged)
	if err != nil {
		return err
	}
	if pkChanged {
		candidates = append([]uniqueCandidate{{key: newRowKey, isPK: true}}, candidates...)
	}

	// Pessimistic mode holds the row being rewritten and the keys it moves
	// to; see lockMatchedRow for a row that was not read under its lock.
	if _, pessimistic := t.writeLock(ctx); pessimistic {
		if err := t.lockMatchedRow(ctx, oldRowKey, old); err != nil {
			return err
		}
		if err := t.lockForWrite(ctx, candidateKeys(candidates)); err != nil {
			return err
		}
	}

	ws, err := t.activeWriteSet(ctx)
	if err != nil {
		return err
	}
	if ws != nil {
		return t.bufferUpdate(ctx, ws, g, candidates, oldRowKey, newRowKey, encodedNewRow, pkChanged, actions)
	}

	// A row this statement read under a lock was read at a known revision;
	// the autocommit write is guarded on it, so a writer that took no lock
	// cannot have its change overwritten (go-qrdb row-locking's commit CAS).
	rowGuard, guarded := t.lockedRowGuard(ctx, oldRowKey)

	for attempt := 0; attempt < writeMaxAttempts; attempt++ {
		idxOps, idxCompares := actionsToOpsAndCompares(actions)
		var compares []service.Compare
		var ops []service.Op
		if guarded {
			compares = append(compares, rowGuard)
		}

		if pkChanged {
			compares = append(compares, service.Compare{
				Key: []byte(newRowKey), Target: service.CompareVersion, Result: service.CompareEqual, Version: 0,
			})
			ops = append(ops, service.Op{Type: service.OpDelete, Key: []byte(oldRowKey)})
			ops = append(ops, service.Op{Type: service.OpPut, Key: []byte(newRowKey), Value: encodedNewRow})
		} else {
			ops = append(ops, service.Op{Type: service.OpPut, Key: []byte(newRowKey), Value: encodedNewRow})
		}
		compares = append(compares, t.resolutionCompares(g)...)
		compares = append(compares, idxCompares...)
		ops = append(ops, idxOps...)

		if err := checkProposalSize(t.def.Name, compares, ops); err != nil {
			return err
		}

		resp, err := t.txnSvc.Execute(ctx, compares, ops, nil)
		if err != nil {
			return err
		}
		if resp.Succeeded {
			return nil
		}

		if staleErr := t.checkResolutionStale(ctx, g); staleErr != nil {
			return staleErr
		}
		if guarded {
			if moved, gerr := t.rowGuardFailed(ctx, rowGuard); gerr != nil || moved {
				if gerr != nil {
					return gerr
				}
				return rowChangedError(oldRowKey)
			}
		}

		dupErr, transient, rerr := t.resolveDuplicate(ctx, g, candidates)
		if rerr != nil {
			return rerr
		}
		if !transient {
			return dupErr
		}
	}
	return &TransactionConflictRetriesExhaustedError{Table: t.def.Name}
}

// candidateKeys returns the keys of candidates, in order.
func candidateKeys(candidates []uniqueCandidate) []string {
	keys := make([]string, len(candidates))
	for i, c := range candidates {
		keys[i] = c.key
	}
	return keys
}

// lockedRowGuard returns the commit precondition of a row the statement's
// transaction read under a lock, and whether there is one.
func (t *RelationalTable) lockedRowGuard(ctx *sqle.Context, key string) (service.Compare, bool) {
	_, ws := lockState(ctx)
	if ws == nil {
		return service.Compare{}, false
	}
	c, ok := ws.currentState(key)
	if !ok {
		return service.Compare{}, false
	}
	return c.compare(key), true
}

// rowGuardFailed reports whether the row a lockedRowGuard guards no longer
// holds the state it was read in.
func (t *RelationalTable) rowGuardFailed(ctx *sqle.Context, guard service.Compare) (bool, error) {
	kv, err := t.mvccSvc.GetRevision(ctx, string(guard.Key))
	switch {
	case err != nil && isNotFound(err):
		return guard.Target != service.CompareVersion, nil
	case err != nil:
		return false, err
	case guard.Target == service.CompareVersion:
		return true, nil
	default:
		return kv.ModRevision != guard.ModRevision, nil
	}
}

// bufferUpdate is Update's interactive-transaction path. Every candidate --
// the new primary key when it moved, and every UNIQUE index value the row
// newly occupies whether or not the primary key moved -- is first checked
// against the transaction's own view, exactly as bufferInsert does and for
// the same reason: the commit-time absence compare cannot see a value this
// same transaction already gave another row, and a value a committed row
// holds must fail the statement with 1062, not the commit.
//
// A pkChanged update is then, from the write-set's point of view, a delete
// of oldRowKey plus a unique-guarded insert of newRowKey; an in-place
// update buffers newRowKey (== oldRowKey) with Put, which derives a
// snapshot-ModRevision precondition rather than an absence check, giving
// the row itself first-committer-wins protection for the duration of the
// transaction.
func (t *RelationalTable) bufferUpdate(ctx *sqle.Context, ws *WriteSet, g *tableGeneration, candidates []uniqueCandidate, oldRowKey, newRowKey string, encodedNewRow []byte, pkChanged bool, actions []indexAction) error {
	if dupErr, transient, err := t.resolveDuplicate(ctx, g, candidates); err != nil {
		return err
	} else if !transient {
		return dupErr
	}
	if pkChanged {
		if err := ws.Delete(ctx, oldRowKey); err != nil {
			return err
		}
		if err := ws.PutUnique(ctx, newRowKey, encodedNewRow); err != nil {
			return err
		}
	} else {
		if err := ws.Put(ctx, newRowKey, encodedNewRow); err != nil {
			return err
		}
	}
	return applyActionsToWriteSet(ctx, ws, actions)
}

// diffIndexEntries computes the indexAction list needed to move every
// secondary index's physical entries from old's values to newRow's values,
// plus the matching uniqueCandidate list that names every UNIQUE index's
// newly-occupied entry.
//
// A unique index's entry key carries no primary key (see indexEntryKey), so
// when the indexed value itself is unchanged but the row's primary key
// changed, the entry's key stays the same and only its stored value (the
// primary key) needs overwriting -- with no unique guard, since this row
// already owns that entry. A non-unique index's entry key carries the
// primary key, so ANY primary-key change moves every one of its entries to
// a new physical key regardless of whether the indexed value itself
// changed.
//
// "the indexed value" is the whole index tuple for a multi-column index,
// and an UPDATE that changes any one of its columns -- or that nulls any
// one of them, which removes the entry entirely (see indexSpec.encode) --
// is therefore a value change for that index.
func (t *RelationalTable) diffIndexEntries(g *tableGeneration, old, newRow sqle.Row, oldPK, newPK string, pkChanged bool) (actions []indexAction, candidates []uniqueCandidate, err error) {
	for _, spec := range t.indexSpecs {
		var oldEncoded, newEncoded string
		var oldExists, newExists bool
		if oldEncoded, oldExists, err = spec.encode(old); err != nil {
			return nil, nil, err
		}
		if newEncoded, newExists, err = spec.encode(newRow); err != nil {
			return nil, nil, err
		}

		if spec.unique {
			actions, candidates, err = t.diffUniqueIndexEntry(g, spec, actions, candidates,
				oldExists, newExists, oldEncoded, newEncoded, oldPK, newPK, pkChanged)
		} else {
			actions, err = t.diffNonUniqueIndexEntry(g, spec, actions,
				oldExists, newExists, oldEncoded, newEncoded, oldPK, newPK)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	return actions, candidates, nil
}

// diffUniqueIndexEntry applies one UNIQUE indexSpec's contribution to
// diffIndexEntries. See that method's doc comment for the physical-key
// reasoning this implements.
func (t *RelationalTable) diffUniqueIndexEntry(
	g *tableGeneration, spec indexSpec,
	actions []indexAction, candidates []uniqueCandidate,
	oldExists, newExists bool, oldEncoded, newEncoded, oldPK, newPK string, pkChanged bool,
) ([]indexAction, []uniqueCandidate, error) {
	prefix := service.TableIndexPrefix(t.def.Database, t.def.TableID, g.generation, spec.incarnation)

	switch {
	case !oldExists && !newExists:
		// Nothing to do.

	case oldExists && !newExists:
		actions = append(actions, indexAction{key: prefix + oldEncoded, delete: true})

	case !oldExists && newExists:
		key := prefix + newEncoded
		if err := checkKeySize(t.def.Name, key); err != nil {
			return nil, nil, err
		}
		actions = append(actions, indexAction{key: key, value: []byte(newPK), unique: true})
		candidates = append(candidates, uniqueCandidate{key: key, indexName: spec.name})

	case oldEncoded == newEncoded:
		// Same value: the entry's key is unchanged. Only its stored primary
		// key needs overwriting, and only when the primary key itself moved.
		if pkChanged {
			actions = append(actions, indexAction{key: prefix + oldEncoded, value: []byte(newPK)})
		}

	default:
		// Value changed to a different, still-non-NULL value.
		actions = append(actions, indexAction{key: prefix + oldEncoded, delete: true})
		key := prefix + newEncoded
		if err := checkKeySize(t.def.Name, key); err != nil {
			return nil, nil, err
		}
		actions = append(actions, indexAction{key: key, value: []byte(newPK), unique: true})
		candidates = append(candidates, uniqueCandidate{key: key, indexName: spec.name})
	}
	return actions, candidates, nil
}

// diffNonUniqueIndexEntry applies one non-unique indexSpec's contribution
// to diffIndexEntries. See that method's doc comment for the physical-key
// reasoning this implements: because a non-unique entry's key is suffixed
// with the row's own primary key, a primary-key change always moves the
// entry, never merely overwrites it in place.
func (t *RelationalTable) diffNonUniqueIndexEntry(
	g *tableGeneration, spec indexSpec, actions []indexAction,
	oldExists, newExists bool, oldEncoded, newEncoded, oldPK, newPK string,
) ([]indexAction, error) {
	var oldKey, newKey string
	if oldExists {
		oldKey = indexEntryKey(t.def.Database, t.def.TableID, g.generation, spec.incarnation, false, oldEncoded, oldPK)
	}
	if newExists {
		newKey = indexEntryKey(t.def.Database, t.def.TableID, g.generation, spec.incarnation, false, newEncoded, newPK)
	}

	if oldKey == newKey {
		return actions, nil // covers "neither exists" and "identical entry" alike.
	}
	if oldExists {
		actions = append(actions, indexAction{key: oldKey, delete: true})
	}
	if newExists {
		if err := checkKeySize(t.def.Name, newKey); err != nil {
			return nil, err
		}
		actions = append(actions, indexAction{key: newKey, value: []byte(newPK)})
	}
	return actions, nil
}

// ---------------------------------------------------------------------------
// Duplicate resolution
// ---------------------------------------------------------------------------

// uniqueCandidate is one "must not already exist" Compare a failed
// Insert/Update transaction might have lost -- either the row's own primary
// key (isPK true) or a named UNIQUE index's new entry.
type uniqueCandidate struct {
	key       string
	indexName string
	isPK      bool
}

// resolveDuplicate determines which candidate, if any, currently exists
// from the caller's point of view -- read through t.txGet, so inside an
// interactive transaction this sees the transaction's own buffered writes
// overlaid on its pinned snapshot, and in autocommit mode a direct
// linearizable read -- so the caller can return a properly typed MySQL 1062
// duplicate-entry error rather than an opaque conflict. It serves two
// distinct callers: after a failed autocommit compare-and-swap (Insert/
// Update), to explain what caused the failure, and, proactively, before an
// interactive transaction buffers a new unique candidate at all (see
// bufferInsert/bufferUpdate), since the write-set's own commit-time compare
// cannot by itself detect two INSERTs of the same key within the SAME
// still-uncommitted transaction.
//
// candidates are checked in order (row's own primary key first, then each
// UNIQUE index in schema order); the first one found to currently exist is
// reported as the cause. When NONE of them exist, the failure must have been
// transient -- e.g. a competing row that held one of these values a moment
// ago (when the CAS evaluated its compares) was concurrently deleted before
// this diagnostic read ran -- and transient is reported true so the caller
// can safely retry the whole write (autocommit) or proceed to buffer it
// (interactive transaction).
func (t *RelationalTable) resolveDuplicate(ctx *sqle.Context, g *tableGeneration, candidates []uniqueCandidate) (dupErr error, transient bool, err error) {
	for _, c := range candidates {
		raw, gerr := t.txGet(ctx, c.key)
		if gerr != nil {
			if isNotFound(gerr) {
				continue
			}
			return nil, false, gerr
		}

		var existingRaw []byte
		var existingPK string
		if c.isPK {
			existingRaw, existingPK = raw, c.key[len(g.dataPrefix):]
		} else {
			existingPK = string(raw)
			existingRaw, gerr = t.txGet(ctx, g.dataPrefix+existingPK)
			if gerr != nil {
				if isNotFound(gerr) {
					continue
				}
				return nil, false, gerr
			}
		}

		existingRow, derr := t.decodeRow(existingPK, existingRaw)
		if derr != nil {
			return nil, false, derr
		}
		name := c.indexName
		if c.isPK {
			name = "PRIMARY"
		}
		return sqle.NewUniqueKeyErr(name, c.isPK, existingRow), false, nil
	}
	return nil, true, nil
}

// isNotFound reports whether err is (or wraps) *service.KeyNotFoundError.
func isNotFound(err error) bool {
	var notFound *service.KeyNotFoundError
	return errors.As(err, &notFound)
}
