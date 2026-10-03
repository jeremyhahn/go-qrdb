//go:build qrdb_sql

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

package infoschema

import (
	"fmt"
	"testing"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/memory"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/information_schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegisterIsIdempotent covers the contract Register makes to its callers:
// every PostgreSQL listener calls it before building its engine, and a process
// serving more than one must not end up with more than one entry.
func TestRegisterIsIdempotent(t *testing.T) {
	t.Cleanup(func() {
		delete(information_schema.NewInformationSchemaTablesToAdd, ConstraintColumnUsageTableName)
	})

	Register()
	Register()

	table, ok := information_schema.NewInformationSchemaTablesToAdd[ConstraintColumnUsageTableName]
	require.True(t, ok, "the view must be registered for information_schema to serve it")
	assert.Equal(t, ConstraintColumnUsageTableName, table.Name())
	assert.Len(t, information_schema.NewInformationSchemaTablesToAdd, 1)
}

// TestConstraintColumnUsageReportsPrimaryAndUniqueColumns is the view's whole
// contract, exercised through a real query rather than by calling the row
// iterator directly -- the value of this view is that a client can join it,
// and a join is what would expose a column name or type that does not line up
// with the information_schema tables beside it.
func TestConstraintColumnUsageReportsPrimaryAndUniqueColumns(t *testing.T) {
	engine, ctx := newTestEngine(t)

	rows := query(t, engine, ctx,
		"SELECT constraint_name, table_name, column_name FROM information_schema.constraint_column_usage "+
			"WHERE table_schema = 'ccutest' ORDER BY table_name, constraint_name, column_name")

	assert.Equal(t, [][]any{
		{"PRIMARY", "orders", "id"},
		{"PRIMARY", "people", "id"},
		{"unique_email", "people", "email"},
	}, rows, "every primary key and unique constraint column, and nothing else")
}

// TestConstraintColumnUsageAgreesWithKeyColumnUsage pins the property that
// makes the view joinable: go-mysql-server's own key_column_usage is the
// sibling table a client joins against, and the two have to agree on every
// value used as a join key -- including the "def" catalog placeholder MySQL
// uses where PostgreSQL would name the database.
func TestConstraintColumnUsageAgreesWithKeyColumnUsage(t *testing.T) {
	engine, ctx := newTestEngine(t)

	const projection = "constraint_catalog, constraint_schema, constraint_name, " +
		"table_catalog, table_schema, table_name, column_name"

	ours := query(t, engine, ctx,
		"SELECT "+projection+" FROM information_schema.constraint_column_usage "+
			"WHERE table_schema = 'ccutest' ORDER BY table_name, constraint_name, column_name")

	// key_column_usage additionally reports foreign keys, which this view
	// deliberately omits; restricting it to the constraints both describe is
	// what makes the two comparable.
	theirs := query(t, engine, ctx,
		"SELECT "+projection+" FROM information_schema.key_column_usage "+
			"WHERE table_schema = 'ccutest' AND referenced_table_name IS NULL "+
			"ORDER BY table_name, constraint_name, column_name")

	assert.Equal(t, theirs, ours)
}

// TestConstraintColumnUsageAnswersGORMsMigratorQuery runs the query
// gorm.io/driver/postgres's Migrator actually issues, verbatim but for the
// bound parameters. It is the reason the view exists: without it every
// PostgreSQL schema migration failed with
// "table not found: constraint_column_usage".
//
// Note which catalog value each side is compared against. The query joins
// table_constraints to this view on constraint_catalog -- both "def", MySQL's
// placeholder, which is why taking it from the same source as key_column_usage
// is what makes the join work -- while filtering information_schema.columns on
// its own table_catalog, which reports the database name under QRDB's provider
// and "def" under the in-memory one used here. The two are never compared to
// each other, so the difference is immaterial to the view; the fixture's value
// is used so the test exercises the real join rather than an empty one.
func TestConstraintColumnUsageAnswersGORMsMigratorQuery(t *testing.T) {
	engine, ctx := newTestEngine(t)

	const gormQuery = `SELECT constraint_name FROM information_schema.table_constraints tc ` +
		`JOIN information_schema.constraint_column_usage AS ccu ` +
		`USING (constraint_schema, constraint_catalog, table_name, constraint_name) ` +
		`JOIN information_schema.columns AS c ON c.table_schema = tc.constraint_schema ` +
		`AND tc.table_name = c.table_name AND ccu.column_name = c.column_name ` +
		`WHERE constraint_type IN ('PRIMARY KEY', 'UNIQUE') AND c.table_catalog = 'def' ` +
		`AND c.table_schema = 'ccutest' AND c.table_name = 'people' AND constraint_type = '%s'`

	assert.Equal(t, [][]any{{"PRIMARY"}}, query(t, engine, ctx, fmt.Sprintf(gormQuery, "PRIMARY KEY")))
	assert.Equal(t, [][]any{{"unique_email"}}, query(t, engine, ctx, fmt.Sprintf(gormQuery, "UNIQUE")))
}

// newTestEngine builds an engine over an in-memory database holding one table
// with a primary key and a unique index and one with a primary key only.
// Register runs before the engine is constructed because
// NewInformationSchemaDatabase copies the table map once, at construction.
func newTestEngine(t *testing.T) (*sqle.Engine, *sql.Context) {
	t.Helper()

	Register()
	t.Cleanup(func() {
		delete(information_schema.NewInformationSchemaTablesToAdd, ConstraintColumnUsageTableName)
	})

	const dbName = "ccutest"

	db := memory.NewDatabase(dbName)
	provider := memory.NewDBProvider(db)
	engine := sqle.NewDefault(provider)

	ctx := sql.NewContext(t.Context(),
		sql.WithSession(memory.NewSession(sql.NewBaseSession(), provider)))
	ctx.SetCurrentDatabase(dbName)

	for _, statement := range []string{
		"CREATE TABLE people (id BIGINT PRIMARY KEY, email VARCHAR(64), name VARCHAR(64))",
		"CREATE UNIQUE INDEX unique_email ON people (email)",
		"CREATE TABLE orders (id BIGINT PRIMARY KEY, note VARCHAR(64))",
	} {
		_, iter, _, err := engine.Query(ctx, statement)
		require.NoErrorf(t, err, "setup: %s", statement)
		require.NoError(t, iter.Close(ctx))
	}

	return engine, ctx
}

// query runs a statement and materialises its rows.
func query(t *testing.T, engine *sqle.Engine, ctx *sql.Context, statement string) [][]any {
	t.Helper()

	_, iter, _, err := engine.Query(ctx, statement)
	require.NoError(t, err)
	defer func() { require.NoError(t, iter.Close(ctx)) }()

	rows, err := sql.RowIterToRows(ctx, iter)
	require.NoError(t, err)

	out := make([][]any, 0, len(rows))
	for _, row := range rows {
		cells := make([]any, len(row))
		copy(cells, row)
		out = append(out, cells)
	}
	return out
}
