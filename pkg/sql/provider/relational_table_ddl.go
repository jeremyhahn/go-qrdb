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
	"strings"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// Compile-time interface assertions.
var (
	_ sqle.AlterableTable      = (*RelationalTable)(nil)
	_ sqle.IndexAlterableTable = (*RelationalTable)(nil)
	_ sqle.IndexBuildingTable  = (*RelationalTable)(nil)
	_ sqle.RewritableTable     = (*RelationalTable)(nil)
	_ sqle.CommentedTable      = (*RelationalTable)(nil)
	_ sqle.CheckTable          = (*RelationalTable)(nil)
	_ sqle.CheckAlterableTable = (*RelationalTable)(nil)
)

// ---------------------------------------------------------------------------
// CommentedTable
// ---------------------------------------------------------------------------

// Comment returns the table's comment.
func (t *RelationalTable) Comment() string {
	return t.def.Comment
}

// ---------------------------------------------------------------------------
// AlterableTable (AddColumn is real; DropColumn/ModifyColumn are dead code
// reachable only if something bypasses RewritableTable -- see their doc
// comments)
// ---------------------------------------------------------------------------

// AddColumn appends a column to the table. This is metadata-only: TableDef.
// Columns is append-only, so an existing row's encoding is unaffected (see
// catalog.RowCodec.Decode) and no row is rewritten.
//
// order is rejected when non-nil (FIRST/AFTER <col>): honoring a requested
// position would require reordering TableDef.Columns, which breaks the very
// append-only invariant that keeps ADD COLUMN metadata-only. A caller that
// needs a specific column order has to accept the column appended at the
// end, exactly as if no position were requested.
func (t *RelationalTable) AddColumn(ctx *sqle.Context, column *sqle.Column, order *sqle.ColumnOrder) error {
	if t.tableSvc == nil {
		return &TableServiceUnavailableError{Table: t.def.Name}
	}
	if order != nil {
		return &ColumnOrderUnsupportedError{Table: t.def.Name, Column: column.Name}
	}
	if err := flushPendingWriteSet(ctx); err != nil {
		return err
	}

	cd, err := columnDefFromSQL(column)
	if err != nil {
		return err
	}

	_, err = t.tableSvc.AlterTable(ctx, &service.AlterTableRequest{
		Database: t.def.Database,
		Name:     t.def.Name,
		Changes:  []service.AlterTableChange{{Action: service.AlterAddColumn, Column: cd}},
	})
	return err
}

// DropColumn is unreachable in normal operation: RelationalTable also
// implements sql.RewritableTable, and ShouldRewriteTable unconditionally
// requests a rewrite for a DROP COLUMN (oldColumn != nil), so
// go-mysql-server's DDL executor drives the change through RewriteInserter
// instead of ever calling this method (see rewriteTable/dropColumnIter in
// go-mysql-server's rowexec package). It exists only because AlterableTable
// requires it; if it is ever called directly it returns
// RewriteRequiredError rather than silently no-op'ing or performing a
// change that would leave rows in a mixed encoding.
func (t *RelationalTable) DropColumn(_ *sqle.Context, columnName string) error {
	return &RewriteRequiredError{Table: t.def.Name, Column: columnName, Operation: "DROP COLUMN"}
}

// ModifyColumn handles the one shape go-mysql-server routes here DIRECTLY
// rather than through RewriteInserter: RENAME COLUMN. Its executor
// (rowexec/ddl.go, buildRenameColumn) copies the existing column, swaps the
// name, and calls ModifyColumn without ever consulting ShouldRewriteTable.
//
// A pure rename is metadata-only for this table's physical row format:
// RowCodec encodes rows positionally against the column LIST, never against
// column names, so no stored row changes meaning when a column's name does.
// It is therefore applied as the catalog's AlterRenameColumn.
//
// Anything else that reaches this method changed more than the name, which
// means something bypassed RewritableTable (ShouldRewriteTable requests a
// rewrite for every genuine MODIFY), and it is refused with
// RewriteRequiredError rather than silently leaving rows in a mixed
// encoding -- same posture as DropColumn above.
func (t *RelationalTable) ModifyColumn(ctx *sqle.Context, columnName string, column *sqle.Column, order *sqle.ColumnOrder) error {
	if order != nil || column == nil {
		return &RewriteRequiredError{Table: t.def.Name, Column: columnName, Operation: "MODIFY COLUMN"}
	}

	var current *service.ColumnDef
	for i := range t.def.Columns {
		if strings.EqualFold(t.def.Columns[i].Name, columnName) {
			current = &t.def.Columns[i]
			break
		}
	}
	if current == nil {
		return &RewriteRequiredError{Table: t.def.Name, Column: columnName, Operation: "MODIFY COLUMN"}
	}

	proposed, err := columnDefFromSQL(column)
	if err != nil {
		return err
	}

	// Metadata-only means the LAYOUT fields -- type, nullability,
	// auto-increment -- are unchanged; only the name, default, comment, or
	// collation moved. The row codec encodes positionally against type and
	// order, so none of those change any stored row's meaning, and the
	// catalog applies the restatement without a rewrite (see the
	// AlterModifyColumn case in pkg/service's applyAlterChanges). Anything
	// touching layout is refused: it means something bypassed
	// RewritableTable, and applying it as metadata would leave rows in a
	// mixed encoding.
	if !strings.EqualFold(current.Type, proposed.Type) ||
		current.Nullable != proposed.Nullable ||
		current.AutoIncrement != proposed.AutoIncrement {
		return &RewriteRequiredError{Table: t.def.Name, Column: columnName, Operation: "MODIFY COLUMN"}
	}

	if t.tableSvc == nil {
		return &TableServiceUnavailableError{Table: t.def.Name}
	}
	if err := flushPendingWriteSet(ctx); err != nil {
		return err
	}
	_, err = t.tableSvc.AlterTable(ctx, &service.AlterTableRequest{
		Database: t.def.Database,
		Name:     t.def.Name,
		Changes: []service.AlterTableChange{{
			Action: service.AlterModifyColumn,
			Name:   columnName,
			Column: proposed,
		}},
	})
	return err
}

// AlterableTable embeds UpdatableTable, which requires an Updater method;
// see relational_writer.go for the real implementation (Phase 6), which
// replaced this file's earlier DML-not-supported stub. It is defined there,
// alongside Inserter/Deleter/Replacer/Truncate, rather than here.

// ---------------------------------------------------------------------------
// IndexAlterableTable
// ---------------------------------------------------------------------------

// CreateIndex adds a secondary index to the table's catalog entry. It does
// not itself populate any physical index data -- see IndexBuildingTable
// (BuildIndex), which go-mysql-server calls separately once CreateIndex
// succeeds.
func (t *RelationalTable) CreateIndex(ctx *sqle.Context, indexDef sqle.IndexDef) error {
	return t.createIndex(ctx, indexDef, false)
}

// createIndex is the shared catalog half of CreateIndex and
// CreateIndexForForeignKey. forForeignKey marks the resulting catalog entry
// as one generated to back a foreign key rather than one the user declared,
// which is what lets SHOW CREATE TABLE and the drop-index guard tell the
// two apart.
func (t *RelationalTable) createIndex(ctx *sqle.Context, indexDef sqle.IndexDef, forForeignKey bool) error {
	if t.tableSvc == nil {
		return &TableServiceUnavailableError{Table: t.def.Name}
	}
	if err := flushPendingWriteSet(ctx); err != nil {
		return err
	}
	idx, err := tableIndexFromDef(indexDef)
	if err != nil {
		return err
	}
	idx.ForeignKey = forForeignKey
	// Persist PostgreSQL's index-vs-constraint distinction when the wire
	// front end recorded one for this statement; see
	// QRDBSession.SetStatementIndexOrigin.
	if session, ok := ctx.Session.(*QRDBSession); ok {
		idx.StatementIndex = session.StatementIndexOrigin()
	}
	return t.tableSvc.CreateIndex(ctx, &service.CreateTableIndexRequest{
		Database: t.def.Database,
		Table:    t.def.Name,
		Index:    idx,
	})
}

// DropIndex removes a secondary index from the table's catalog entry. Its
// physical entries are not deleted inline: TableServicer.DropIndex publishes
// a reclaim job for them in the same transaction, and the reclaim sweeper
// deletes them once no statement that resolved the index can still be
// reading them (go-qrdb#547).
func (t *RelationalTable) DropIndex(ctx *sqle.Context, indexName string) error {
	if t.tableSvc == nil {
		return &TableServiceUnavailableError{Table: t.def.Name}
	}
	if err := flushPendingWriteSet(ctx); err != nil {
		return err
	}
	return t.tableSvc.DropIndex(tenant.WithTenantID(ctx, sessionTenantIDOf(ctx)), t.def.Database, t.def.Name, indexName)
}

// RenameIndex renames a secondary index. Its entries are keyed by the
// index's incarnation (service.TableIndex.Incarnation), not its name, so
// this is a single catalog compare-and-swap (TableServicer.RenameIndex): no
// entry moves, nothing is copied or reclaimed, and DML running concurrently
// keeps maintaining the same keyspace under either name. A later index
// created under the old name gets an incarnation, and a keyspace, of its
// own.
func (t *RelationalTable) RenameIndex(ctx *sqle.Context, fromIndexName, toIndexName string) error {
	if t.tableSvc == nil {
		return &TableServiceUnavailableError{Table: t.def.Name}
	}
	if err := flushPendingWriteSet(ctx); err != nil {
		return err
	}
	return t.tableSvc.RenameIndex(ctx, &service.RenameTableIndexRequest{
		Database: t.def.Database, Table: t.def.Name, From: fromIndexName, To: toIndexName,
	})
}

// ---------------------------------------------------------------------------
// IndexBuildingTable
// ---------------------------------------------------------------------------

// ShouldBuildIndex always returns true: a newly created secondary index
// must be populated from the table's existing rows regardless of table
// size, so that any future accelerated read path can trust the index is
// never silently empty on a non-empty table.
func (t *RelationalTable) ShouldBuildIndex(_ *sqle.Context, _ sqle.IndexDef) (bool, error) {
	return true, nil
}

// BuildIndex returns a RowInserter that populates indexDef's physical
// entries from every row go-mysql-server streams through it (one call to
// Insert per existing row -- see the buildIndex helper in go-mysql-server's
// rowexec package).
// It resolves indexDef through the very same tableIndexFromDef/
// buildIndexSpec pair the catalog and the DML writer use, so an index built
// here covers every one of its declared columns and lands in byte-identical
// physical entries to the ones a subsequent INSERT would write.
func (t *RelationalTable) BuildIndex(ctx *sqle.Context, indexDef sqle.IndexDef) (sqle.RowInserter, error) {
	tableIndex, err := tableIndexFromDef(indexDef)
	if err != nil {
		return nil, err
	}
	spec, err := buildIndexSpec(t.def, t.schema, tableIndex)
	if err != nil {
		return nil, err
	}
	incarnation, metadataRevision, err := t.catalogIncarnation(ctx, indexDef.Name)
	if err != nil {
		return nil, err
	}
	spec.incarnation = incarnation
	return &indexBuildInserter{table: t, spec: spec, metadataRevision: metadataRevision}, nil
}

// catalogIncarnation returns the incarnation the catalog assigned the index
// named name -- which only the catalog knows: it is allocated in the same
// compare-and-swap that added the index (TableService.CreateIndex), after
// this handle resolved the table -- and the revision of the definition
// listing it, which the build is fenced on.
func (t *RelationalTable) catalogIncarnation(ctx *sqle.Context, name string) (incarnation uint64, metadataRevision int64, err error) {
	if t.tableSvc == nil {
		return 0, 0, &TableServiceUnavailableError{Table: t.def.Name}
	}
	live, err := t.tableSvc.GetTable(ctx, t.def.Database, t.def.Name)
	if err != nil {
		return 0, 0, err
	}
	idx := findTableIndex(live, name)
	if idx == nil {
		return 0, 0, &service.TableIndexNotFoundError{Database: t.def.Database, Table: t.def.Name, Index: name}
	}
	return idx.Incarnation, live.MetadataRevision, nil
}

// indexBuildInserter is the sql.RowInserter BuildIndex returns. Each Insert
// call writes one physical index entry via indexEntryKey -- the same
// layout relational_writer.go's DML uses, and unique-aware for exactly the
// same reason: a unique index's entry key carries only the encoded value
// (no primary-key suffix), letting a future write's Compare against that
// exact key enforce the constraint atomically, while a non-unique index's
// entry key is suffixed with the primary key so multiple rows sharing a
// value get distinct physical keys. In either case the row's own primary
// key encoding is the stored value -- redundant with a non-unique entry's
// key suffix, but it means a reader never has to parse the two
// variable-length compound encodings apart to recover the primary key,
// only slice the stored value off whole.
type indexBuildInserter struct {
	table *RelationalTable
	spec  indexSpec

	// metadataRevision is the revision of the definition that lists the
	// index being built. Every chunk is fenced on it as well as on the
	// generation: a DROP INDEX (or any other DDL) landing mid-build has
	// published a reclaim of this incarnation, and a later chunk writing
	// into it would outlive that reclaim as orphaned entries.
	metadataRevision int64

	// pending buffers entries between flushes: Insert is called once per
	// existing row by go-mysql-server's own streaming loop, with no
	// control over batching from this side, so accumulating here is what
	// lets the build still fence its writes on the table's generation in
	// bounded chunks (see flush) rather than either buffering the entire
	// index in memory or writing each entry unguarded one at a time.
	pending []service.Op
}

var _ sqle.RowInserter = (*indexBuildInserter)(nil)

// StatementBegin marks the start of the build. No-op: there is no
// in-flight state to check-point.
func (ib *indexBuildInserter) StatementBegin(_ *sqle.Context) {}

// DiscardChanges discards changes made since StatementBegin. No-op: an
// index build is not journaled or otherwise resumable; a caller that needs
// to recover from a discarded build re-issues DROP INDEX followed by CREATE
// INDEX.
func (ib *indexBuildInserter) DiscardChanges(_ *sqle.Context, _ error) error { return nil }

// StatementComplete marks the end of the build. No-op.
func (ib *indexBuildInserter) StatementComplete(_ *sqle.Context) error { return nil }

// Insert buffers one physical index entry for row. A NULL in any indexed
// column contributes no entry, matching ordinary SQL index semantics (NULL
// never matches an equality lookup) and matching what the DML writer does
// for the same row. The buffer is flushed -- as a generation-fenced
// compare-and-swap, see RelationalTable.writeChunkedWithResolutionFence --
// once it reaches generationFencedChunkSize, so a build over a large table
// still commits in bounded chunks rather than accumulating the whole index
// in memory.
func (ib *indexBuildInserter) Insert(ctx *sqle.Context, row sqle.Row) error {
	encodedVal, ok, err := ib.spec.encode(row)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	pkKey, err := ib.table.keyCodec.Encode(row)
	if err != nil {
		return err
	}

	idxKey := indexEntryKey(ib.table.def.Database, ib.table.def.TableID, ib.table.gen().generation, ib.spec.incarnation, ib.spec.unique, encodedVal, pkKey)
	ib.pending = append(ib.pending, service.Op{Type: service.OpPut, Key: []byte(idxKey), Value: []byte(pkKey)})
	if len(ib.pending) < generationFencedChunkSize {
		return nil
	}
	return ib.flush(ctx)
}

// flush proposes every buffered entry as one generation-fenced
// compare-and-swap and clears the buffer, win or lose -- a failed flush
// aborts the whole build (CREATE INDEX fails), so there is nothing left to
// retry it against.
func (ib *indexBuildInserter) flush(ctx *sqle.Context) error {
	if len(ib.pending) == 0 {
		return nil
	}
	ops := ib.pending
	ib.pending = nil
	return ib.table.writeChunkedWithResolutionFence(ctx, ops, ib.metadataRevision)
}

// Close flushes any entries still buffered below generationFencedChunkSize.
func (ib *indexBuildInserter) Close(ctx *sqle.Context) error {
	return ib.flush(ctx)
}

// ---------------------------------------------------------------------------
// RewritableTable
// ---------------------------------------------------------------------------

// ShouldRewriteTable reports true whenever a column is being removed or
// replaced (oldColumn != nil, covering both DROP COLUMN and MODIFY COLUMN)
// and false otherwise (a pure ADD COLUMN, or a call this table's other DDL
// paths already fully handle without a rewrite -- see BuildIndex, which
// always answers CREATE INDEX itself and therefore keeps go-mysql-server
// from ever reaching its own index-rewrite fallback).
//
// DROP and MODIFY COLUMN unconditionally require a rewrite for this table's
// physical row format: RowCodec positionally decodes a row against the
// table's CURRENT column list, so removing or retyping a column the moment
// it happens (rather than only in a subsequent CREATE TABLE) is the only
// way an existing row stays decodable.
//
// Returning false for a pure ADD COLUMN does not, on its own, guarantee
// go-mysql-server skips RewriteInserter: its own addColumnIter also
// requests a rewrite whenever the new column has a default, is NOT NULL, or
// is AUTO_INCREMENT, regardless of this answer (see RewriteInserter's doc
// comment for how that case is still handled correctly -- as a metadata-
// only change, never a physical rewrite).
func (t *RelationalTable) ShouldRewriteTable(_ *sqle.Context, _, _ sqle.PrimaryKeySchema, oldColumn, _ *sqle.Column) bool {
	return oldColumn != nil
}

// RewriteInserter drives a DROP COLUMN or MODIFY COLUMN rewrite, and also
// handles an ADD COLUMN that go-mysql-server insists on routing here despite
// ShouldRewriteTable answering false for it (see that method's doc
// comment): go-mysql-server's own addColumnIter requests a rewrite whenever
// the new column carries a default, is NOT NULL, or is AUTO_INCREMENT,
// independently of what ShouldRewriteTable answered. oldColumn == nil is how
// that case is distinguished here; it is handled as the same metadata-only
// AddColumn this table already implements -- appended columns decode
// existing rows via RowCodec's trailing-default-fill, so no row ever needs
// touching -- and the returned RowInserter's Insert/Close are both no-ops,
// discarding the row stream go-mysql-server insists on providing.
//
// For oldColumn != nil (DROP/MODIFY COLUMN), this is where the actual
// AlterTableChange is submitted to TableServicer, since go-mysql-server
// calls this (and never DropColumn/ModifyColumn directly) whenever
// ShouldRewriteTable answers true: it opens the rewrite (writing
// TableServicer's DDL journal), fetches the resulting journal back to learn
// the target definition, and returns a RowInserter whose Close performs the
// migration -- see rewriteRowInserter and runRewrite
// (relational_rewrite.go) for why the migration itself runs once in Close
// rather than incrementally as go-mysql-server streams rows through Insert.
func (t *RelationalTable) RewriteInserter(
	ctx *sqle.Context,
	_, _ sqle.PrimaryKeySchema,
	oldColumn, newColumn *sqle.Column,
	_ []sqle.IndexColumn,
) (sqle.RowInserter, error) {
	if t.tableSvc == nil {
		return nil, &TableServiceUnavailableError{Table: t.def.Name}
	}
	if err := flushPendingWriteSet(ctx); err != nil {
		return nil, err
	}
	if oldColumn == nil {
		if newColumn == nil {
			return nil, &RewriteRequiresColumnError{Table: t.def.Name}
		}
		if err := t.AddColumn(ctx, newColumn, nil); err != nil {
			return nil, err
		}
		return &noopRowInserter{}, nil
	}

	change, err := alterChangeFromColumns(oldColumn, newColumn)
	if err != nil {
		return nil, err
	}

	_, err = t.tableSvc.AlterTable(ctx, &service.AlterTableRequest{
		Database: t.def.Database,
		Name:     t.def.Name,
		Changes:  []service.AlterTableChange{change},
	})
	if err == nil {
		// The catalog applied the change WITHOUT journaling a rewrite: the
		// modify touched no layout field (a SET DEFAULT, a comment, a
		// restatement go-mysql-server routed here anyway). There is nothing
		// to migrate, so the streamed rows are discarded exactly as in the
		// ADD COLUMN case above.
		return &noopRowInserter{}, nil
	}
	var rewriteRequired *service.TableRewriteRequiredError
	if !errors.As(err, &rewriteRequired) {
		return nil, err
	}

	journal, current, err := t.tableSvc.RewriteJournal(ctx, t.def.Database, t.def.Name)
	if err != nil {
		return nil, err
	}
	if journal.Target == nil {
		return nil, &RewriteJournalMissingTargetError{Database: t.def.Database, Name: t.def.Name}
	}
	if err := validateRewritablePrimaryKey(current, journal.Target); err != nil {
		return nil, err
	}

	return &rewriteRowInserter{
		kvSvc:    t.kvSvc,
		txnSvc:   t.txnSvc,
		tableSvc: t.tableSvc,
		database: t.def.Database,
		name:     t.def.Name,
		current:  current,
		journal:  journal,
	}, nil
}

// noopRowInserter is the sql.RowInserter RewriteInserter returns for an ADD
// COLUMN go-mysql-server insists on routing through the rewrite path (see
// RewriteInserter's doc comment): the metadata-only change has already been
// applied by the time this is returned, so every row go-mysql-server
// streams through Insert is simply discarded, and Close has nothing left to
// do.
type noopRowInserter struct{}

var _ sqle.RowInserter = (*noopRowInserter)(nil)

// StatementBegin marks the start of the operation. No-op.
func (noopRowInserter) StatementBegin(_ *sqle.Context) {}

// DiscardChanges discards changes made since StatementBegin. No-op: the
// metadata change already committed is not undone by this (the same as
// every other DDL statement in this package, none of which support
// mid-statement rollback).
func (noopRowInserter) DiscardChanges(_ *sqle.Context, _ error) error { return nil }

// StatementComplete marks the end of the operation. No-op.
func (noopRowInserter) StatementComplete(_ *sqle.Context) error { return nil }

// Insert discards row. No-op: see the type doc comment.
func (noopRowInserter) Insert(_ *sqle.Context, _ sqle.Row) error { return nil }

// Close finalizes the operation. No-op.
func (noopRowInserter) Close(_ *sqle.Context) error { return nil }

// alterChangeFromColumns translates the (oldColumn, newColumn) pair
// go-mysql-server's RewritableTable contract passes into the single
// AlterTableChange TableServicer.AlterTable expects: newColumn == nil means
// DROP COLUMN, non-nil means MODIFY COLUMN.
func alterChangeFromColumns(oldColumn, newColumn *sqle.Column) (service.AlterTableChange, error) {
	if newColumn == nil {
		return service.AlterTableChange{Action: service.AlterDropColumn, Name: oldColumn.Name}, nil
	}
	cd, err := columnDefFromSQL(newColumn)
	if err != nil {
		return service.AlterTableChange{}, err
	}
	return service.AlterTableChange{Action: service.AlterModifyColumn, Name: oldColumn.Name, Column: cd}, nil
}

// ---------------------------------------------------------------------------
// CheckTable / CheckAlterableTable
// ---------------------------------------------------------------------------

// GetChecks returns the table's CHECK constraints. go-mysql-server reads them
// while planning every INSERT/UPDATE, so a persisted constraint is ENFORCED
// on writes, not merely displayed.
func (t *RelationalTable) GetChecks(_ *sqle.Context) ([]sqle.CheckDefinition, error) {
	checks := make([]sqle.CheckDefinition, len(t.def.Checks))
	for i, c := range t.def.Checks {
		checks[i] = sqle.CheckDefinition{
			Name:            c.Name,
			CheckExpression: c.Expression,
			Enforced:        c.Enforced,
		}
	}
	return checks, nil
}

// CreateCheck persists a CHECK constraint as catalog metadata.
func (t *RelationalTable) CreateCheck(ctx *sqle.Context, check *sqle.CheckDefinition) error {
	if t.tableSvc == nil {
		return &TableServiceUnavailableError{Table: t.def.Name}
	}
	if err := flushPendingWriteSet(ctx); err != nil {
		return err
	}
	_, err := t.tableSvc.AlterTable(ctx, &service.AlterTableRequest{
		Database: t.def.Database,
		Name:     t.def.Name,
		Changes: []service.AlterTableChange{{
			Action: service.AlterAddCheck,
			Check: service.CheckDef{
				Name:       check.Name,
				Expression: check.CheckExpression,
				Enforced:   check.Enforced,
			},
		}},
	})
	return err
}

// DropCheck removes the named constraint, resolving the name the way
// PostgreSQL's single constraint namespace demands.
//
// go-mysql-server's grammar routes ALTER TABLE ... DROP CONSTRAINT <name>
// here whenever the name is not obviously an index or foreign key at parse
// time — but a PostgreSQL client (gorm.io/driver/postgres's migrator above
// all) uses DROP CONSTRAINT for UNIQUE constraints and foreign keys too,
// because in PostgreSQL those all share one namespace. So the name is
// resolved against everything it could denote, in the order most specific to
// this method's own contract: a CHECK constraint, then a unique index, then
// a foreign key. Refusing the latter two here would leave a PostgreSQL
// client with no way to drop a constraint it legitimately created.
func (t *RelationalTable) DropCheck(ctx *sqle.Context, chName string) error {
	if t.tableSvc == nil {
		return &TableServiceUnavailableError{Table: t.def.Name}
	}
	if err := flushPendingWriteSet(ctx); err != nil {
		return err
	}

	for _, c := range t.def.Checks {
		if strings.EqualFold(c.Name, chName) {
			_, err := t.tableSvc.AlterTable(ctx, &service.AlterTableRequest{
				Database: t.def.Database,
				Name:     t.def.Name,
				Changes: []service.AlterTableChange{{
					Action: service.AlterDropCheck,
					Name:   chName,
				}},
			})
			return err
		}
	}
	for _, idx := range t.def.Indexes {
		if strings.EqualFold(idx.Name, chName) {
			return t.DropIndex(ctx, idx.Name)
		}
	}
	for _, fk := range t.def.ForeignKeys {
		if strings.EqualFold(fk.Name, chName) {
			return t.DropForeignKey(ctx, fk.Name, "", "")
		}
	}
	return &service.TableCheckNotFoundError{Table: t.def.Name, Check: chName}
}

// IsStatementIndex reports whether the named unique index was created by a
// standalone CREATE INDEX statement rather than declared as a constraint.
// It is the read side of service.TableIndex.StatementIndex, consumed by
// pkg/sql/pgcatalog's table_constraints filter; ok is false when the table
// has no index of that name.
func (t *RelationalTable) IsStatementIndex(name string) (statementIndex bool, ok bool) {
	idx := findTableIndex(t.def, name)
	if idx == nil {
		return false, false
	}
	return idx.StatementIndex, true
}

// IsForeignKeyIndex reports whether the named index was generated to back
// a foreign key constraint (see createIndex's forForeignKey). Consumed by
// pkg/sql/pgcatalog's index introspection, which must not report such
// indexes: PostgreSQL creates no indexes for foreign keys at all. ok is
// false when the table has no index of that name.
func (t *RelationalTable) IsForeignKeyIndex(name string) (backsForeignKey bool, ok bool) {
	idx := findTableIndex(t.def, name)
	if idx == nil {
		return false, false
	}
	return idx.ForeignKey, true
}
