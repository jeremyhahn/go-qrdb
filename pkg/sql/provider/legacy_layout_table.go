//go:build qrdb_sql

// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/mysql"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/catalog"
)

// ErrCodeTableLayoutUnsupported is the MySQL error number a statement that
// opens a table created by v0.20.0 through v0.21.0 fails with: MySQL's own
// ER_TABLE_NEEDS_UPGRADE (1459, SQLSTATE HY000), the error MySQL raises for
// a table whose on-disk format the server cannot use until it is rebuilt.
// Here the remedy is DROP TABLE and CREATE TABLE -- see
// service.TableLayoutUnsupportedError. PostgreSQL clients receive SQLSTATE
// 55000 (object_not_in_prerequisite_state) for the same failure.
const ErrCodeTableLayoutUnsupported = service.TableLayoutUnsupportedSQLCode

// sqlStateTableLayoutUnsupported is the SQLSTATE MySQL reports with
// ER_TABLE_NEEDS_UPGRADE.
const sqlStateTableLayoutUnsupported = "HY000"

// tableLayoutSQLError renders err as the MySQL wire error. A
// *mysql.SQLError is the one shape sql.CastSQLError forwards verbatim --
// any other error, however well typed, reaches a client as the generic
// 1105 -- so the refusal is built in that shape, carrying the service
// error's own message, which names the table and the remedy.
func tableLayoutSQLError(err *service.TableLayoutUnsupportedError) *mysql.SQLError {
	return mysql.NewSQLError(ErrCodeTableLayoutUnsupported, sqlStateTableLayoutUnsupported, "%s", err.Error())
}

// legacyLayoutTable stands in for a table created by v0.20.0 through
// v0.21.0, whose rows and index entries this build cannot address (see
// service.TableLayoutUnsupportedError). It resolves -- so DROP TABLE, the
// remedy, can name it -- but every read and every write fails with the
// layout error: it is never opened as an empty table, and its indexes are
// never read at all.
type legacyLayoutTable struct {
	database string
	name     string
	schema   sqle.Schema
	tableSvc service.TableServicer
	err      *mysql.SQLError
}

var (
	_ sqle.Table               = (*legacyLayoutTable)(nil)
	_ sqle.InsertableTable     = (*legacyLayoutTable)(nil)
	_ sqle.UpdatableTable      = (*legacyLayoutTable)(nil)
	_ sqle.DeletableTable      = (*legacyLayoutTable)(nil)
	_ sqle.ReplaceableTable    = (*legacyLayoutTable)(nil)
	_ sqle.TruncateableTable   = (*legacyLayoutTable)(nil)
	_ sqle.StatisticsTable     = (*legacyLayoutTable)(nil)
	_ sqle.CommentedTable      = (*legacyLayoutTable)(nil)
	_ sqle.AlterableTable      = (*legacyLayoutTable)(nil)
	_ sqle.IndexAlterableTable = (*legacyLayoutTable)(nil)
	_ sqle.ForeignKeyTable     = (*legacyLayoutTable)(nil)
	_ sqle.ForeignKeyEditor    = (*legacyLayoutEditor)(nil)
	_ sqle.RowReplacer         = (*legacyLayoutEditor)(nil)
	_ sqle.RowUpdater          = (*legacyLayoutEditor)(nil)
)

// newLegacyLayoutTable builds the stand-in for the table err names. It
// presents the legacy definition's columns, so a statement is analyzed as it
// would be for any table and then refused at execution with the layout
// error, rather than failing analysis with an unrelated "no such column". A
// definition whose columns no longer decode is presented with none; the
// table can still be dropped.
//
// tableSvc answers the one catalog question DROP TABLE asks of it -- which
// tables reference it -- so a legacy parent is refused exactly as any other
// referenced parent is.
func newLegacyLayoutTable(name string, tableSvc service.TableServicer, err *service.TableLayoutUnsupportedError) *legacyLayoutTable {
	var schema sqle.Schema
	if err.Definition != nil {
		if s, serr := catalog.SchemaOf(err.Definition); serr == nil {
			schema = s
		}
	}
	return &legacyLayoutTable{
		database: err.Database, name: name, schema: schema,
		tableSvc: tableSvc, err: tableLayoutSQLError(err),
	}
}

func (t *legacyLayoutTable) Name() string                     { return t.name }
func (t *legacyLayoutTable) String() string                   { return t.name }
func (t *legacyLayoutTable) Schema(*sqle.Context) sqle.Schema { return t.schema }
func (t *legacyLayoutTable) Collation() sqle.CollationID      { return sqle.Collation_Default }

// Partitions refuses every read.
func (t *legacyLayoutTable) Partitions(*sqle.Context) (sqle.PartitionIter, error) { return nil, t.err }

// PartitionRows refuses every read.
func (t *legacyLayoutTable) PartitionRows(*sqle.Context, sqle.Partition) (sqle.RowIter, error) {
	return nil, t.err
}

// Truncate refuses: the remedy is DROP TABLE, which reclaims the legacy data.
func (t *legacyLayoutTable) Truncate(*sqle.Context) (int, error) { return 0, t.err }

func (t *legacyLayoutTable) Inserter(*sqle.Context) sqle.RowInserter {
	return &legacyLayoutEditor{err: t.err}
}
func (t *legacyLayoutTable) Updater(*sqle.Context) sqle.RowUpdater {
	return &legacyLayoutEditor{err: t.err}
}
func (t *legacyLayoutTable) Deleter(*sqle.Context) sqle.RowDeleter {
	return &legacyLayoutEditor{err: t.err}
}
func (t *legacyLayoutTable) Replacer(*sqle.Context) sqle.RowReplacer {
	return &legacyLayoutEditor{err: t.err}
}

// RowCount reports no rows, inexactly, without an error: information_schema
// lists every table of a database through it, and one legacy table must not
// fail the listing of the others. The table's rows are never counted -- this
// build cannot address them.
func (t *legacyLayoutTable) RowCount(*sqle.Context) (uint64, bool, error) { return 0, false, nil }

// DataLength reports zero for the reason RowCount does.
func (t *legacyLayoutTable) DataLength(*sqle.Context) (uint64, error) { return 0, nil }

// Comment is the refusal itself, so information_schema.TABLES and SHOW TABLE
// STATUS name the remedy where an operator listing tables will see it.
func (t *legacyLayoutTable) Comment() string { return t.err.Message }

// AddColumn refuses: the table's rows cannot be read to be re-encoded.
func (t *legacyLayoutTable) AddColumn(*sqle.Context, *sqle.Column, *sqle.ColumnOrder) error {
	return t.err
}

// DropColumn refuses, as AddColumn does.
func (t *legacyLayoutTable) DropColumn(*sqle.Context, string) error { return t.err }

// ModifyColumn refuses, as AddColumn does.
func (t *legacyLayoutTable) ModifyColumn(*sqle.Context, string, *sqle.Column, *sqle.ColumnOrder) error {
	return t.err
}

// CreateIndex refuses: the table's rows cannot be read to be indexed.
func (t *legacyLayoutTable) CreateIndex(*sqle.Context, sqle.IndexDef) error { return t.err }

// DropIndex refuses: its entries sit under the old layout, reclaimed only by
// dropping the table.
func (t *legacyLayoutTable) DropIndex(*sqle.Context, string) error { return t.err }

// RenameIndex refuses, as DropIndex does.
func (t *legacyLayoutTable) RenameIndex(*sqle.Context, string, string) error { return t.err }

// GetIndexes reports none. It cannot refuse: go-mysql-server treats it as
// infallible and panics on its error on DROP TABLE's own path, the remedy.
// Listing the stored indexes instead would offer the planner index access
// to entries this build cannot address. SHOW INDEX therefore lists nothing
// for a legacy table; its table comment (Comment) names the refusal, and
// every read of it refuses.
func (t *legacyLayoutTable) GetIndexes(*sqle.Context) ([]sqle.Index, error) { return nil, nil }

// IndexedAccess offers no index access.
func (t *legacyLayoutTable) IndexedAccess(*sqle.Context, sqle.IndexLookup) sqle.IndexedTable {
	return nil
}

// PreciseMatch reports that no index access substitutes for a filter.
func (t *legacyLayoutTable) PreciseMatch() bool { return false }

// CreateIndexForForeignKey refuses, as CreateIndex does.
func (t *legacyLayoutTable) CreateIndexForForeignKey(*sqle.Context, sqle.IndexDef) error {
	return t.err
}

// GetDeclaredForeignKeys reports none: no constraint is enforced from a
// table whose rows cannot be read. It must not refuse, because DROP TABLE --
// the remedy -- asks it, and it must not list the stored constraints either,
// because DROP TABLE then drops each one through DropForeignKey, which
// refuses. The constraints go with the definition that holds them.
func (t *legacyLayoutTable) GetDeclaredForeignKeys(*sqle.Context) ([]sqle.ForeignKeyConstraint, error) {
	return nil, nil
}

// GetReferencedForeignKeys reports, from the catalog, the constraints of
// other tables that name this one as their parent, so DROP TABLE refuses a
// referenced legacy parent exactly as it refuses any other.
func (t *legacyLayoutTable) GetReferencedForeignKeys(ctx *sqle.Context) ([]sqle.ForeignKeyConstraint, error) {
	return referencingForeignKeyConstraints(ctx, t.tableSvc, t.database, t.name)
}

// AddForeignKey refuses.
func (t *legacyLayoutTable) AddForeignKey(*sqle.Context, sqle.ForeignKeyConstraint) error {
	return t.err
}

// DropForeignKey refuses.
func (t *legacyLayoutTable) DropForeignKey(*sqle.Context, string, string, string) error {
	return t.err
}

// UpdateForeignKey refuses.
func (t *legacyLayoutTable) UpdateForeignKey(*sqle.Context, string, sqle.ForeignKeyConstraint) error {
	return t.err
}

// GetForeignKeyEditor returns an editor refusing every row operation.
func (t *legacyLayoutTable) GetForeignKeyEditor(*sqle.Context) sqle.ForeignKeyEditor {
	return &legacyLayoutEditor{err: t.err}
}

// legacyLayoutEditor is every write editor of a legacyLayoutTable: each row
// operation fails with the layout error.
type legacyLayoutEditor struct{ err *mysql.SQLError }

func (e *legacyLayoutEditor) StatementBegin(*sqle.Context)                   {}
func (e *legacyLayoutEditor) DiscardChanges(*sqle.Context, error) error      { return nil }
func (e *legacyLayoutEditor) StatementComplete(*sqle.Context) error          { return nil }
func (e *legacyLayoutEditor) Insert(*sqle.Context, sqle.Row) error           { return e.err }
func (e *legacyLayoutEditor) Update(*sqle.Context, sqle.Row, sqle.Row) error { return e.err }
func (e *legacyLayoutEditor) Delete(*sqle.Context, sqle.Row) error           { return e.err }
func (e *legacyLayoutEditor) Close(*sqle.Context) error                      { return nil }

// IndexedAccess offers no index access to a foreign-key check.
func (e *legacyLayoutEditor) IndexedAccess(*sqle.Context, sqle.IndexLookup) sqle.IndexedTable {
	return nil
}

// GetIndexes reports none, as the table's does.
func (e *legacyLayoutEditor) GetIndexes(*sqle.Context) ([]sqle.Index, error) { return nil, nil }

// PreciseMatch reports that no index access substitutes for a filter.
func (e *legacyLayoutEditor) PreciseMatch() bool { return false }
