// Copyright 2026 Jeremy Hahn
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

package pgcatalog

import (
	"context"
	"io"
	"testing"

	gms "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/memory"
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/analyzer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unreadableDatabaseError is the failure a database in the test catalog
// reports from GetTableNames. It is a distinct type rather than errors.New so
// that a test asserting on it names the condition it provoked.
type unreadableDatabaseError struct {
	Name string
}

// Error returns a human-readable description.
func (e *unreadableDatabaseError) Error() string {
	return "pgcatalog test: database " + e.Name + " cannot be listed"
}

// unreadableDatabase is a sql.Database whose table list cannot be read. It
// stands in for the real condition pg_class and information_schema.columns
// both have to survive: one database of many being momentarily unavailable
// (a tenant whose shard has no leader, a catalog read that timed out) while
// the client's query is filtered to a different one entirely.
type unreadableDatabase struct {
	name string
}

// Compile-time interface assertion.
var _ sqle.Database = (*unreadableDatabase)(nil)

// Name returns the database name.
func (d unreadableDatabase) Name() string { return d.name }

// GetTableInsensitive resolves no table.
func (d unreadableDatabase) GetTableInsensitive(_ *sqle.Context, _ string) (sqle.Table, bool, error) {
	return nil, false, nil
}

// GetTableNames always fails.
func (d unreadableDatabase) GetTableNames(_ *sqle.Context) ([]string, error) {
	return nil, &unreadableDatabaseError{Name: d.name}
}

func TestRelationReportsItsSchemaAndCollation(t *testing.T) {
	ctx := sqle.NewEmptyContext()
	for name := range relationRows {
		tbl, ok := Relation(name)
		require.True(t, ok)

		databaseable, ok := tbl.(sqle.Databaseable)
		require.Truef(t, ok, "%s reports its owning schema", name)
		assert.Equalf(t, SchemaName, databaseable.Database(), "%s belongs to pg_catalog", name)

		assert.Equalf(t, sqle.Collation_Default, tbl.Collation(), "%s uses the default collation", name)
		assert.NotEmptyf(t, tbl.Schema(ctx), "%s declares columns", name)
	}
}

func TestPartitionRowsPropagatesARowsFailure(t *testing.T) {
	want := &unreadableDatabaseError{Name: "gorm"}
	failing := &relation{
		name:    TypeRelation,
		schema:  relationSchemas[TypeRelation],
		rows:    func(*sqle.Context, sqle.Catalog) ([]sqle.Row, error) { return nil, want },
		catalog: newTestCatalog("gorm"),
	}

	ctx := sqle.NewEmptyContext()
	_, err := failing.PartitionRows(ctx, relationPartition(TypeRelation))
	require.ErrorIs(t, err, want, "a relation reports a failed scan rather than an empty one")
}

func TestClassRowsSkipAnUnreadableDatabase(t *testing.T) {
	cat := analyzer.NewCatalog(
		memory.NewDBProvider(memory.NewDatabase("gorm"), unreadableDatabase{name: "broken"}),
		sqle.EngineOverrides{})
	ctx := sqle.NewEmptyContext()

	rows := scan(t, ctx, assigned(t, ClassRelation, cat))
	require.NotEmpty(t, rows, "one unavailable database must not empty the whole relation")
	for _, row := range rows {
		assert.NotEqual(t, NamespaceOID("broken"), row[2].(uint32),
			"the unreadable database contributes no relation")
	}

	// pg_namespace only needs the database's name, which is always available,
	// so the same database is still reported there.
	nsRows := scan(t, ctx, assigned(t, NamespaceRelation, cat))
	var found bool
	for _, row := range nsRows {
		if row[1].(string) == "broken" {
			found = true
		}
	}
	assert.True(t, found, "a database that exists is a schema that exists")
}

// newPGEngine returns a go-mysql-server engine over dbs whose catalog serves
// this package's information_schema -- exactly the wiring pkg/sql/engine
// installs for DialectPostgreSQL, reproduced here without importing it (that
// package imports this one).
func newPGEngine(dbs ...sqle.Database) (*gms.Engine, *sqle.Context) {
	pro := memory.NewDBProvider(dbs...)
	a := analyzer.NewBuilder(pro).Build()
	a.Catalog.InfoSchema = InfoSchema()

	ctx := sqle.NewContext(context.Background(),
		sqle.WithSession(memory.NewSession(sqle.NewBaseSession(), pro)))
	return gms.New(a, nil), ctx
}

// queryRows runs query to completion and returns its rows.
func queryRows(t *testing.T, eng *gms.Engine, ctx *sqle.Context, query string) []sqle.Row {
	t.Helper()
	_, iter, _, err := eng.Query(ctx, query)
	require.NoError(t, err)
	var rows []sqle.Row
	for {
		row, err := iter.Next(ctx)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		rows = append(rows, row)
	}
	require.NoError(t, iter.Close(ctx))
	return rows
}

func TestColumnsTableExtendsEveryRowThroughAQuery(t *testing.T) {
	eng, ctx := newPGEngine(memory.NewDatabase("gorm"))
	ctx.SetCurrentDatabase("gorm")

	queryRows(t, eng, ctx, "CREATE TABLE t (id BIGINT PRIMARY KEY, name LONGTEXT, ratio DOUBLE)")

	rows := queryRows(t, eng, ctx,
		"SELECT column_name, udt_name, numeric_precision_radix, identity_increment, table_catalog "+
			"FROM information_schema.columns WHERE table_name = 't' ORDER BY ordinal_position")
	require.Len(t, rows, 3)

	assert.Equal(t, sqle.Row{"id", "int8", uint32(10), nil, "gorm"}, rows[0])
	assert.Equal(t, sqle.Row{"name", "text", nil, nil, "gorm"}, rows[1])
	assert.Equal(t, sqle.Row{"ratio", "float8", uint32(2), nil, "gorm"}, rows[2])
}

func TestColumnsTableRowIterPropagatesAFailure(t *testing.T) {
	cat := analyzer.NewCatalog(
		memory.NewDBProvider(unreadableDatabase{name: "broken"}), sqle.EngineOverrides{})

	// Called the way go-mysql-server calls it (ColumnsTable.PartitionRows),
	// so the wrapper is exercised on its own: it must return the failure
	// rather than an iterator that reports end-of-rows.
	iter, err := NewColumnsTable().RowIter(sqle.NewEmptyContext(), cat, nil)
	assert.Nil(t, iter)
	var want *unreadableDatabaseError
	require.ErrorAs(t, err, &want)
	assert.Equal(t, "broken", want.Name)
}

func TestColumnsTableReportsAnUnreadableDatabase(t *testing.T) {
	eng, ctx := newPGEngine(memory.NewDatabase("gorm"), unreadableDatabase{name: "broken"})
	ctx.SetCurrentDatabase("gorm")

	// information_schema.columns has to visit every database, so unlike
	// pg_class it cannot skip one: go-mysql-server's own iterator fails, and
	// this package's wrapper must surface that rather than swallow it.
	_, iter, _, err := eng.Query(ctx, "SELECT column_name FROM information_schema.columns")
	if err == nil {
		_, err = iter.Next(ctx)
		_ = iter.Close(ctx)
	}
	var want *unreadableDatabaseError
	require.ErrorAs(t, err, &want)
	assert.Equal(t, "broken", want.Name)
}
