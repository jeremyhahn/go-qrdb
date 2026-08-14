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
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/retry"
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

	// DropTable removes a table's catalog entry. It does not delete the
	// table's row or index data -- that is the responsibility of the
	// (not yet implemented) table data-plane engine that owns
	// tableDataPrefixFor/tableIndexPrefixFor; see the DropTable doc comment
	// on TableService for the reasoning.
	DropTable(ctx context.Context, req *DropTableRequest) error

	// RenameTable changes a table's name without moving any data: row and
	// index keys are composed from the table's immutable TableID, never
	// from its name, so a rename is a pure catalog compare-and-swap.
	RenameTable(ctx context.Context, req *RenameTableRequest) error

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

	// DropIndex removes a secondary index from a table's catalog entry.
	DropIndex(ctx context.Context, database, table, index string) error

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
}

// Compile-time interface check.
var _ TableServicer = (*TableService)(nil)

// NewTableService creates a new TableServicer backed by the given KV read
// interface, MVCC read interface, TransactionServicer, and SequenceServicer.
func NewTableService(kv tableKVService, mvcc tableMVCCService, txn TransactionServicer, seq SequenceServicer) *TableService {
	return &TableService{kv: kv, mvcc: mvcc, txn: txn, seq: seq}
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

	now := time.Now().UTC()
	def := &TableDef{
		TableID:       tableID,
		Name:          req.Name,
		Database:      req.Database,
		Columns:       append([]ColumnDef(nil), req.Columns...),
		PrimaryKey:    append([]PKColumn(nil), req.PrimaryKey...),
		Indexes:       append([]TableIndex(nil), req.Indexes...),
		ForeignKeys:   append([]ForeignKeyDef(nil), req.ForeignKeys...),
		Checks:        append([]CheckDef(nil), req.Checks...),
		AutoIncColumn: autoIncrementColumnName(req.Columns),
		SchemaVersion: 1,
		Collation:     req.Collation,
		Comment:       req.Comment,
		CreatedAt:     now,
		UpdatedAt:     now,
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

// DropTable removes a table's catalog entry (definition, DDL journal if
// any) and bumps the database's catalog epoch, atomically.
//
// It deliberately does not enumerate or delete the table's rows
// (tableDataPrefixFor) or index entries (tableIndexPrefixFor): reclaiming
// that data is the responsibility of the table data-plane engine, which is
// not part of this catalog service and is not yet implemented (the same
// phase boundary AlterTable's rewrite executor sits behind). A caller that
// needs the data reclaimed today must do so itself before or after calling
// DropTable, e.g. by scanning tableDataPrefixFor(database, def.TableID).
func (s *TableService) DropTable(ctx context.Context, req *DropTableRequest) error {
	if err := validateTableRef(req.Database, req.Name); err != nil {
		return err
	}

	metaKey := tableMetadataKey(req.Database, req.Name)
	journalKey := tableDDLJournalKey(req.Database, req.Name)
	epochKey := tableCatalogEpochKey(req.Database)

	return s.runCatalogCAS(ctx, req.Database, req.Name, func() ([]Compare, []Op, error) {
		rev, err := s.mvcc.GetRevision(ctx, metaKey)
		if err != nil {
			return nil, nil, &TableNotFoundError{Database: req.Database, Name: req.Name}
		}

		compares := []Compare{{
			Key: []byte(metaKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: rev.ModRevision,
		}}
		ops := []Op{
			{Type: OpDelete, Key: []byte(metaKey)},
			{Type: OpDelete, Key: []byte(journalKey)},
			{Type: OpPut, Key: []byte(epochKey), Value: epochValue()},
		}
		return compares, ops, nil
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
		}
		ops := []Op{
			{Type: OpDelete, Key: []byte(fromKey)},
			{Type: OpPut, Key: []byte(toKey), Value: data},
			{Type: OpPut, Key: []byte(epochKey), Value: epochValue()},
		}
		return compares, ops, nil
	})
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

	if journal, err := s.kv.Get(ctx, tableDDLJournalKey(database, name)); err == nil && journal != nil {
		return nil, &TableRewriteInProgressError{Database: database, Name: name}
	}

	data, err := s.kv.Get(ctx, tableMetadataKey(database, name))
	if err != nil || data == nil {
		return nil, &TableNotFoundError{Database: database, Name: name}
	}

	var def TableDef
	if err := json.Unmarshal(data, &def); err != nil {
		return nil, &TableMetadataError{Op: "unmarshal", Err: err}
	}
	return &def, nil
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
				Changes:     append([]AlterTableChange(nil), req.Changes...),
				StartedAt:   time.Now().UTC(),
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
			dropColumnReferences(def, change.Name)
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
			def.Columns[idx] = newCol
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
			def.Columns[idx].Name = change.NewName

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
		rev, err := s.mvcc.GetRevision(ctx, metaKey)
		if err != nil {
			return nil, nil, &TableNotFoundError{Database: req.Database, Name: req.Table}
		}
		var def TableDef
		if err := json.Unmarshal(rev.Value, &def); err != nil {
			return nil, nil, &TableMetadataError{Op: "unmarshal", Err: err}
		}

		if err := validateIndex(columnNameSet(&def), req.Index); err != nil {
			return nil, nil, err
		}
		for _, existing := range def.Indexes {
			if existing.Name == req.Index.Name {
				return nil, nil, &TableIndexAlreadyExistsError{Database: req.Database, Table: req.Table, Index: req.Index.Name}
			}
		}

		def.Indexes = append(def.Indexes, req.Index)
		def.UpdatedAt = time.Now().UTC()
		data, err := json.Marshal(&def)
		if err != nil {
			return nil, nil, &TableMetadataError{Op: "marshal", Err: err}
		}

		compares := []Compare{{
			Key: []byte(metaKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: rev.ModRevision,
		}}
		ops := []Op{
			{Type: OpPut, Key: []byte(metaKey), Value: data},
			{Type: OpPut, Key: []byte(epochKey), Value: epochValue()},
		}
		return compares, ops, nil
	})
}

// DropIndex removes a secondary index from a table's catalog entry.
func (s *TableService) DropIndex(ctx context.Context, database, table, index string) error {
	if err := validateTableRef(database, table); err != nil {
		return err
	}
	if index == "" {
		return &TableInvalidNameError{Kind: "index", Name: index, Reason: "must not be empty"}
	}

	metaKey := tableMetadataKey(database, table)
	epochKey := tableCatalogEpochKey(database)

	return s.runCatalogCAS(ctx, database, table, func() ([]Compare, []Op, error) {
		rev, err := s.mvcc.GetRevision(ctx, metaKey)
		if err != nil {
			return nil, nil, &TableNotFoundError{Database: database, Name: table}
		}
		var def TableDef
		if err := json.Unmarshal(rev.Value, &def); err != nil {
			return nil, nil, &TableMetadataError{Op: "unmarshal", Err: err}
		}

		idx := -1
		for i, existing := range def.Indexes {
			if existing.Name == index {
				idx = i
				break
			}
		}
		if idx == -1 {
			return nil, nil, &TableIndexNotFoundError{Database: database, Table: table, Index: index}
		}

		def.Indexes = append(def.Indexes[:idx:idx], def.Indexes[idx+1:]...)
		def.UpdatedAt = time.Now().UTC()
		data, err := json.Marshal(&def)
		if err != nil {
			return nil, nil, &TableMetadataError{Op: "marshal", Err: err}
		}

		compares := []Compare{{
			Key: []byte(metaKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: rev.ModRevision,
		}}
		ops := []Op{
			{Type: OpPut, Key: []byte(metaKey), Value: data},
			{Type: OpPut, Key: []byte(epochKey), Value: epochValue()},
		}
		return compares, ops, nil
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

	return &journal, &current, nil
}

// CompleteRewrite finalizes an in-flight rewrite. See the doc comment on
// TableServicer.CompleteRewrite.
func (s *TableService) CompleteRewrite(ctx context.Context, database, name string) (*TableDef, error) {
	if err := validateTableRef(database, name); err != nil {
		return nil, err
	}

	metaKey := tableMetadataKey(database, name)
	journalKey := tableDDLJournalKey(database, name)
	epochKey := tableCatalogEpochKey(database)

	var result *TableDef
	err := s.runCatalogCAS(ctx, database, name, func() ([]Compare, []Op, error) {
		result = nil

		journalRev, err := s.mvcc.GetRevision(ctx, journalKey)
		if err != nil {
			return nil, nil, &TableNoRewriteInProgressError{Database: database, Name: name}
		}
		var journal TableDDLJournal
		if err := json.Unmarshal(journalRev.Value, &journal); err != nil {
			return nil, nil, &TableMetadataError{Op: "unmarshal-journal", Err: err}
		}
		if journal.Target == nil {
			return nil, nil, &TableRewriteTargetMissingError{Database: database, Name: name}
		}

		target := *journal.Target
		target.UpdatedAt = time.Now().UTC()
		data, err := json.Marshal(&target)
		if err != nil {
			return nil, nil, &TableMetadataError{Op: "marshal", Err: err}
		}

		compares := []Compare{{
			Key: []byte(journalKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: journalRev.ModRevision,
		}}
		ops := []Op{
			{Type: OpPut, Key: []byte(metaKey), Value: data},
			{Type: OpDelete, Key: []byte(journalKey)},
			{Type: OpPut, Key: []byte(epochKey), Value: epochValue()},
		}
		result = &target
		return compares, ops, nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
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
