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
	"io"
	"strings"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// sql.ForeignKeyTable for RelationalTable: store and report, never enforce.
//
// Referential integrity is enforced by go-mysql-server, not here.
// go-mysql-server already implements the whole of it -- parent-existence
// checks on INSERT/UPDATE, ON DELETE/ON UPDATE CASCADE, SET NULL, SET
// DEFAULT and RESTRICT, cycle detection and depth limiting -- in its
// analyzer (applyForeignKeys) and plan layer (ForeignKeyEditor,
// ForeignKeyReferenceHandler), driven entirely through this interface plus
// the index lookups RelationalTable already serves as an
// IndexAddressableTable/IndexedTable. A second enforcement engine here
// would duplicate that logic and immediately begin diverging from it, so
// this file's entire job is to persist constraint metadata faithfully, hand
// it back exactly as given, and expose a table editor the engine can drive.
//
// The one thing that follows from that division: anything this layer cannot
// represent must be REJECTED, never quietly accepted. A constraint stored
// but not reported back, or reported back in a weakened form, would be a
// constraint the engine believes it is enforcing and is not. See
// foreignKeyDefFromSQL for what is rejected and why.
// ---------------------------------------------------------------------------

// Compile-time interface assertions.
var (
	_ sqle.ForeignKeyTable  = (*RelationalTable)(nil)
	_ sqle.ForeignKeyEditor = (*relationalForeignKeyEditor)(nil)
)

// ---------------------------------------------------------------------------
// Referential action mapping
// ---------------------------------------------------------------------------

// foreignKeyActions maps the persisted referential-action text to the
// go-mysql-server constant it round-trips to. The empty string maps to
// DefaultAction, matching ForeignKeyDef's documented "empty for the
// default", so a definition written by a caller that left the action unset
// reads back as the engine's own no-explicit-action value.
//
// A map rather than a switch because the reverse direction is a plain
// string conversion: the persisted text IS the constant's value, which is
// what makes the round trip exact rather than lossy.
var foreignKeyActions = map[string]sqle.ForeignKeyReferentialAction{
	"": sqle.ForeignKeyReferentialAction_DefaultAction,
	string(sqle.ForeignKeyReferentialAction_DefaultAction): sqle.ForeignKeyReferentialAction_DefaultAction,
	string(sqle.ForeignKeyReferentialAction_Restrict):      sqle.ForeignKeyReferentialAction_Restrict,
	string(sqle.ForeignKeyReferentialAction_Cascade):       sqle.ForeignKeyReferentialAction_Cascade,
	string(sqle.ForeignKeyReferentialAction_NoAction):      sqle.ForeignKeyReferentialAction_NoAction,
	string(sqle.ForeignKeyReferentialAction_SetNull):       sqle.ForeignKeyReferentialAction_SetNull,
	string(sqle.ForeignKeyReferentialAction_SetDefault):    sqle.ForeignKeyReferentialAction_SetDefault,
}

// foreignKeyAction resolves persisted referential-action text, reporting
// ForeignKeyActionUnsupportedError for anything outside the enumeration
// go-mysql-server can act on. Accepting an unknown action would mean
// storing a constraint whose behavior on a parent-row change nobody can
// name.
func foreignKeyAction(text string) (sqle.ForeignKeyReferentialAction, error) {
	action, ok := foreignKeyActions[strings.ToUpper(strings.TrimSpace(text))]
	if !ok {
		return "", &ForeignKeyActionUnsupportedError{Action: text}
	}
	return action, nil
}

// ---------------------------------------------------------------------------
// Constraint translation
// ---------------------------------------------------------------------------

// foreignKeyDefFromSQL translates a go-mysql-server constraint into the
// persisted ForeignKeyDef vocabulary, rejecting anything qrdb cannot
// represent faithfully:
//
//   - A schema-qualified constraint. qrdb's Database is not a
//     sql.SchemaDatabase, so a non-empty schema name could only be dropped,
//     which would silently retarget the constraint at whatever table
//     happens to carry the same bare name.
//   - A cross-database constraint. Every catalog key, row key, and index
//     key in this package is scoped to one database (see the key-layout
//     comment in pkg/service/table_types.go), and TableServicer instances
//     are resolved per (tenant, database), so there is no read path from
//     one database's table to another's. Storing such a constraint would
//     produce one the engine could never resolve a parent for.
//
// database and table name the declaring table; they are not stored on the
// constraint (see ForeignKeyDef) because they are always the record it
// lives on.
func foreignKeyDefFromSQL(database, table string, fk sqle.ForeignKeyConstraint) (service.ForeignKeyDef, error) {
	if fk.SchemaName != "" || fk.ParentSchema != "" {
		return service.ForeignKeyDef{}, &ForeignKeySchemaUnsupportedError{Name: fk.Name}
	}
	if fk.Database != "" && !strings.EqualFold(fk.Database, database) {
		return service.ForeignKeyDef{}, &ForeignKeyCrossDatabaseUnsupportedError{
			Name: fk.Name, Database: database, ParentDatabase: fk.Database,
		}
	}
	if fk.ParentDatabase != "" && !strings.EqualFold(fk.ParentDatabase, database) {
		return service.ForeignKeyDef{}, &ForeignKeyCrossDatabaseUnsupportedError{
			Name: fk.Name, Database: database, ParentDatabase: fk.ParentDatabase,
		}
	}
	if fk.Table != "" && !strings.EqualFold(fk.Table, table) {
		return service.ForeignKeyDef{}, &ForeignKeyTableMismatchError{
			Name: fk.Name, Declared: fk.Table, Table: table,
		}
	}
	if _, err := foreignKeyAction(string(fk.OnUpdate)); err != nil {
		return service.ForeignKeyDef{}, err
	}
	if _, err := foreignKeyAction(string(fk.OnDelete)); err != nil {
		return service.ForeignKeyDef{}, err
	}

	return service.ForeignKeyDef{
		Name:          fk.Name,
		Columns:       append([]string(nil), fk.Columns...),
		ParentTable:   fk.ParentTable,
		ParentColumns: append([]string(nil), fk.ParentColumns...),
		OnUpdate:      string(fk.OnUpdate),
		OnDelete:      string(fk.OnDelete),
		Resolved:      fk.IsResolved,
		NotValid:      fk.IsNotValid,
		MatchType:     uint8(fk.MatchType),
	}, nil
}

// foreignKeyConstraintOf is foreignKeyDefFromSQL's inverse: it rebuilds the
// go-mysql-server constraint from a persisted definition, filling the
// declaring and parent database from database (the only value either can
// have, see foreignKeyDefFromSQL) and the declaring table from table.
func foreignKeyConstraintOf(database, table string, def service.ForeignKeyDef) (sqle.ForeignKeyConstraint, error) {
	onUpdate, err := foreignKeyAction(def.OnUpdate)
	if err != nil {
		return sqle.ForeignKeyConstraint{}, err
	}
	onDelete, err := foreignKeyAction(def.OnDelete)
	if err != nil {
		return sqle.ForeignKeyConstraint{}, err
	}

	return sqle.ForeignKeyConstraint{
		Name:           def.Name,
		Database:       database,
		Table:          table,
		ParentDatabase: database,
		ParentTable:    def.ParentTable,
		OnUpdate:       onUpdate,
		OnDelete:       onDelete,
		Columns:        append([]string(nil), def.Columns...),
		ParentColumns:  append([]string(nil), def.ParentColumns...),
		IsResolved:     def.Resolved,
		IsNotValid:     def.NotValid,
		MatchType:      sqle.ForeignKeyMatchType(def.MatchType),
	}, nil
}

// ---------------------------------------------------------------------------
// ForeignKeyTable
// ---------------------------------------------------------------------------

// CreateIndexForForeignKey creates the secondary index a foreign key needs
// on this table's key columns, and populates it from the table's existing
// rows.
//
// The backfill is this method's own responsibility, unlike CreateIndex:
// go-mysql-server calls CreateIndex through its DDL executor, which then
// drives IndexBuildingTable (ShouldBuildIndex/BuildIndex) to populate the
// new index, but it calls THIS method directly from plan.ResolveForeignKey
// with no such follow-up. Leaving the index empty on a non-empty table
// would not fail loudly; it would make every subsequent parent-existence
// lookup return no rows, turning the constraint the index exists to enforce
// into one that rejects every child row.
func (t *RelationalTable) CreateIndexForForeignKey(ctx *sqle.Context, indexDef sqle.IndexDef) error {
	if err := t.createIndex(ctx, indexDef, true); err != nil {
		return err
	}
	return t.backfillIndex(ctx, indexDef)
}

// backfillIndex populates indexDef's physical entries from every row
// currently in the table, reusing the very RowInserter IndexBuildingTable
// hands go-mysql-server (BuildIndex) and the table's own full scan, so a
// foreign key's index is built by exactly the same code path -- and lands
// in exactly the same physical layout -- as a CREATE INDEX built one.
func (t *RelationalTable) backfillIndex(ctx *sqle.Context, indexDef sqle.IndexDef) error {
	inserter, err := t.BuildIndex(ctx, indexDef)
	if err != nil {
		return err
	}

	partIter, err := t.Partitions(ctx)
	if err != nil {
		return err
	}
	rowIter := sqle.NewTableRowIter(ctx, t, partIter)

	if err := insertAllRows(ctx, rowIter, inserter); err != nil {
		_ = rowIter.Close(ctx)
		_ = inserter.DiscardChanges(ctx, err)
		return err
	}
	if err := rowIter.Close(ctx); err != nil {
		return err
	}
	return inserter.Close(ctx)
}

// insertAllRows drains rows into inserter, stopping at the first error.
func insertAllRows(ctx *sqle.Context, rows sqle.RowIter, inserter sqle.RowInserter) error {
	for {
		row, err := rows.Next(ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := inserter.Insert(ctx, row); err != nil {
			return err
		}
	}
}

// GetDeclaredForeignKeys returns the constraints declared by this table.
//
// The catalog record is re-read on every call rather than served from the
// definition this table was resolved with. That is not defensive: within a
// single CREATE TABLE, go-mysql-server resolves each declared constraint in
// turn against the SAME table instance, and uses the list this method
// returns both to reject a duplicate constraint name and to pick the next
// free "<table>_ibfk_N" for an unnamed one. Answering from a snapshot taken
// before the first constraint was added would give every unnamed constraint
// in the statement the same generated name.
func (t *RelationalTable) GetDeclaredForeignKeys(ctx *sqle.Context) ([]sqle.ForeignKeyConstraint, error) {
	if t.tableSvc == nil {
		return nil, &TableServiceUnavailableError{Table: t.def.Name}
	}
	def, err := t.tableSvc.GetTable(ctx, t.def.Database, t.def.Name)
	if err != nil {
		return nil, err
	}

	fks := make([]sqle.ForeignKeyConstraint, 0, len(def.ForeignKeys))
	for _, stored := range def.ForeignKeys {
		fk, cerr := foreignKeyConstraintOf(def.Database, def.Name, stored)
		if cerr != nil {
			return nil, cerr
		}
		fks = append(fks, fk)
	}
	return fks, nil
}

// GetReferencedForeignKeys returns every constraint in the database that
// names this table as its parent -- the reverse of the catalog's own
// keying, which stores a constraint only on the table that declares it.
// go-mysql-server needs it to know which children a DELETE or UPDATE of a
// row in this table must cascade to, restrict on, or null out.
//
// See TableService.ReferencingForeignKeys for how the reverse lookup is
// answered in a single catalog scan, and for the cost that remains.
func (t *RelationalTable) GetReferencedForeignKeys(ctx *sqle.Context) ([]sqle.ForeignKeyConstraint, error) {
	if t.tableSvc == nil {
		return nil, &TableServiceUnavailableError{Table: t.def.Name}
	}
	return referencingForeignKeyConstraints(ctx, t.tableSvc, t.def.Database, t.def.Name)
}

// referencingForeignKeyConstraints renders, in go-mysql-server's shape,
// every constraint in database that names parent as its parent table.
func referencingForeignKeyConstraints(ctx *sqle.Context, tableSvc service.TableServicer, database, parent string) ([]sqle.ForeignKeyConstraint, error) {
	refs, err := tableSvc.ReferencingForeignKeys(ctx, database, parent)
	if err != nil {
		return nil, err
	}

	fks := make([]sqle.ForeignKeyConstraint, 0, len(refs))
	for _, ref := range refs {
		fk, cerr := foreignKeyConstraintOf(database, ref.Table, ref.ForeignKey)
		if cerr != nil {
			return nil, cerr
		}
		fks = append(fks, fk)
	}
	return fks, nil
}

// AddForeignKey declares a new constraint on this table. A constraint name
// already in use anywhere in the database is rejected, matching MySQL,
// where constraint names are database-scoped rather than table-scoped (a
// stricter rule than the declaring-table-only check go-mysql-server makes
// before calling this).
func (t *RelationalTable) AddForeignKey(ctx *sqle.Context, fk sqle.ForeignKeyConstraint) error {
	if t.tableSvc == nil {
		return &TableServiceUnavailableError{Table: t.def.Name}
	}
	if err := flushPendingWriteSet(ctx); err != nil {
		return err
	}

	def, err := foreignKeyDefFromSQL(t.def.Database, t.def.Name, fk)
	if err != nil {
		return err
	}
	return foreignKeyMySQLError(t.tableSvc.AddForeignKey(ctx, &service.CreateTableForeignKeyRequest{
		Database:   t.def.Database,
		Table:      t.def.Name,
		ForeignKey: def,
	}))
}

// DropForeignKey removes a constraint. tableName selects the declaring
// table when go-mysql-server routes the call through some other table's
// ForeignKeyTable (it passes the declaring table's own name at every
// current call site); empty means this table. schemaName must be empty --
// see foreignKeyDefFromSQL for why a schema-qualified constraint is
// rejected rather than approximated.
func (t *RelationalTable) DropForeignKey(ctx *sqle.Context, fkName, tableName, schemaName string) error {
	if t.tableSvc == nil {
		return &TableServiceUnavailableError{Table: t.def.Name}
	}
	if schemaName != "" {
		return &ForeignKeySchemaUnsupportedError{Name: fkName}
	}
	if err := flushPendingWriteSet(ctx); err != nil {
		return err
	}

	target := tableName
	if target == "" {
		target = t.def.Name
	}
	return foreignKeyMySQLError(t.tableSvc.DropForeignKey(ctx, t.def.Database, target, fkName))
}

// UpdateForeignKey replaces the constraint named fkName on THIS table with
// fk, which go-mysql-server uses both to record that a constraint has been
// resolved and to rewrite the table and column names a constraint carries
// after a rename.
//
// The replacement is always applied to this table's own catalog record,
// never to the table fk.Table names, even though the interface describes
// the latter. RENAME TABLE is why: go-mysql-server sets fk.Table to the NEW
// name and calls this on the table being renamed BEFORE the rename itself
// is applied, so routing on fk.Table would address a catalog record that
// does not exist yet. Every current call site invokes this on the declaring
// table, so the receiver is always the correct target.
//
// Unlike Add and Drop, this does not flush a pending write set. It is not
// only a DDL entry point: go-mysql-server also calls it from the analyzer,
// mid-statement, to record that a previously unresolved constraint has now
// been validated. Treating that as an implicit COMMIT would end a user's
// open transaction as a side effect of planning a statement inside it.
func (t *RelationalTable) UpdateForeignKey(ctx *sqle.Context, fkName string, fk sqle.ForeignKeyConstraint) error {
	if t.tableSvc == nil {
		return &TableServiceUnavailableError{Table: t.def.Name}
	}

	def, err := foreignKeyDefFromSQL(t.def.Database, t.def.Name, sqle.ForeignKeyConstraint{
		Name:           fk.Name,
		ParentDatabase: fk.ParentDatabase,
		ParentTable:    fk.ParentTable,
		SchemaName:     fk.SchemaName,
		ParentSchema:   fk.ParentSchema,
		OnUpdate:       fk.OnUpdate,
		OnDelete:       fk.OnDelete,
		Columns:        fk.Columns,
		ParentColumns:  fk.ParentColumns,
		IsResolved:     fk.IsResolved,
		IsNotValid:     fk.IsNotValid,
		MatchType:      fk.MatchType,
	})
	if err != nil {
		return err
	}
	return foreignKeyMySQLError(t.tableSvc.UpdateForeignKey(ctx, &service.UpdateTableForeignKeyRequest{
		Database:   t.def.Database,
		Table:      t.def.Name,
		Name:       fkName,
		ForeignKey: def,
	}))
}

// GetForeignKeyEditor returns the editor go-mysql-server drives to apply a
// referential action -- the cascading UPDATE or DELETE, or the SET NULL,
// that a change to a parent row implies for its children -- and to look up
// parent rows through an index when checking a child row.
func (t *RelationalTable) GetForeignKeyEditor(_ *sqle.Context) sqle.ForeignKeyEditor {
	return &relationalForeignKeyEditor{relationalWriter: &relationalWriter{table: t}}
}

// foreignKeyMySQLError converts the catalog's own duplicate-constraint-name
// error into go-mysql-server's, so a client sees the MySQL error it would
// see from any other engine rather than a qrdb-internal one. Every other
// error is returned unchanged; this is a translation at the SQL boundary,
// not a general error-mapping layer.
func foreignKeyMySQLError(err error) error {
	var duplicate *service.TableForeignKeyAlreadyExistsError
	if errors.As(err, &duplicate) {
		return sqle.ErrForeignKeyDuplicateName.New(duplicate.Name)
	}
	var notFound *service.TableForeignKeyNotFoundError
	if errors.As(err, &notFound) {
		return sqle.ErrForeignKeyNotFound.New(notFound.Name, notFound.Table)
	}
	return err
}

// ---------------------------------------------------------------------------
// ForeignKeyEditor
// ---------------------------------------------------------------------------

// relationalForeignKeyEditor is the sql.ForeignKeyEditor
// GetForeignKeyEditor returns: sql.TableEditor plus sql.IndexAddressable.
//
// Both halves already exist. relationalWriter is this table's ordinary DML
// writer and is already a full sql.TableEditor (RowReplacer + RowUpdater),
// so a cascading UPDATE or DELETE goes through the exact same atomic
// row-plus-index compare-and-swap a user-issued one does. The
// IndexAddressable half delegates to the table, which is already an
// IndexAddressableTable/IndexedTable, so a parent-existence check resolves
// through the same accelerated lookup a WHERE clause on the same column
// would.
type relationalForeignKeyEditor struct {
	*relationalWriter
}

// GetIndexes returns the table's indexes.
func (e *relationalForeignKeyEditor) GetIndexes(ctx *sqle.Context) ([]sqle.Index, error) {
	return e.table.GetIndexes(ctx)
}

// IndexedAccess returns the table as an IndexedTable for lookup.
func (e *relationalForeignKeyEditor) IndexedAccess(ctx *sqle.Context, lookup sqle.IndexLookup) sqle.IndexedTable {
	return e.table.IndexedAccess(ctx, lookup)
}

// PreciseMatch reports whether an indexed access answers the pushed-down
// predicate exactly; see RelationalTable.PreciseMatch.
func (e *relationalForeignKeyEditor) PreciseMatch() bool {
	return e.table.PreciseMatch()
}
