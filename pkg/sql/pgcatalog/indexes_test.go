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
	"testing"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/memory"
	sql "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIndexesReportsEveryIndexUnderItsPostgreSQLName covers what a PostgreSQL
// client actually asks pg_indexes: whether a named index exists on a table.
//
// The naming is the substantive part. go-mysql-server calls a table's primary
// key "PRIMARY", MySQL's spelling; PostgreSQL names it after its table, and
// "<table>_pkey" is the name gorm.io/driver/postgres derives and compares
// against. Reporting the MySQL name would answer "no such index" for every
// primary key that does exist.
func TestIndexesReportsEveryIndexUnderItsPostgreSQLName(t *testing.T) {
	catalog, ctx := newIndexesCatalog(t)

	got := map[string][]string{}
	for _, row := range scan(t, ctx, assigned(t, IndexesRelation, catalog)) {
		schemaName := row[0].(string) //nolint:errcheck // the relation's own schema declares these columns
		if schemaName != "idxtest" {
			continue // information_schema's own tables are not under test
		}
		tableName := row[1].(string) //nolint:errcheck // as above
		indexName := row[2].(string) //nolint:errcheck // as above
		got[tableName] = append(got[tableName], indexName)
	}

	assert.Equal(t, map[string][]string{
		"orders": {"orders_pkey"},
		"people": {"people_pkey", "unique_email"},
	}, got)
}

// TestIndexesDescribesEachIndex covers the indexdef column, which is
// reconstructed from the index's own metadata because nothing in the engine
// keeps the original statement text, and the tablespace column, which is NULL
// because QRDB has no tablespaces to name.
func TestIndexesDescribesEachIndex(t *testing.T) {
	catalog, ctx := newIndexesCatalog(t)

	definitions := map[string]any{}
	for _, row := range scan(t, ctx, assigned(t, IndexesRelation, catalog)) {
		if row[0].(string) != "idxtest" { //nolint:errcheck // declared column type
			continue
		}
		definitions[row[2].(string)] = row[4] //nolint:errcheck // declared column type
		assert.Nil(t, row[3], "QRDB has no tablespaces to report")
	}

	assert.Equal(t, map[string]any{
		"orders_pkey":  "CREATE UNIQUE INDEX orders_pkey ON idxtest.orders USING btree (id)",
		"people_pkey":  "CREATE UNIQUE INDEX people_pkey ON idxtest.people USING btree (id)",
		"unique_email": "CREATE UNIQUE INDEX unique_email ON idxtest.people USING btree (email)",
	}, definitions)
}

// TestIndexNameRenamesOnlyThePrimaryKey pins the one rename this relation
// performs, so that it stays confined to the primary key.
func TestIndexNameRenamesOnlyThePrimaryKey(t *testing.T) {
	assert.Equal(t, "users_pkey", indexName(stubIndex{id: primaryIndexID}, "users"))
	assert.Equal(t, "idx_users_email", indexName(stubIndex{id: "idx_users_email"}, "users"))
}

// TestIndexColumnNamesLeavesAnUnqualifiedExpressionWhole covers the guard
// against inventing a column name: an expression that is not this table's
// qualified column is reported as it is, not truncated at an arbitrary dot.
func TestIndexColumnNamesLeavesAnUnqualifiedExpressionWhole(t *testing.T) {
	index := stubIndex{expressions: []string{"users.id", "other.id", "lower(name)"}}
	assert.Equal(t, []string{"id", "other.id", "lower(name)"}, indexColumnNames(index, "users"))
}

// stubIndex is a minimal sql.Index for the naming helpers, which read only an
// index's ID and expressions.
type stubIndex struct {
	sql.Index
	id          string
	expressions []string
}

func (i stubIndex) ID() string            { return i.id }
func (i stubIndex) Expressions() []string { return i.expressions }

// newIndexesCatalog builds a catalog over an in-memory database holding a
// table with a primary key and a unique index, and one with a primary key
// only. The engine is built first so that its own catalog -- the one the
// relation is assigned to -- is the one holding the tables.
func newIndexesCatalog(t *testing.T) (sql.Catalog, *sql.Context) {
	t.Helper()

	const dbName = "idxtest"

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

	return engine.Analyzer.Catalog, ctx
}
