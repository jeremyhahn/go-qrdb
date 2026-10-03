//go:build qrdb_sql

// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package infoschema

import (
	"testing"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/memory"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/information_schema"
	"github.com/stretchr/testify/require"
)

// TestConstraintColumnUsageSkipsNonUniqueIndexes covers the index filter.
//
// constraint_column_usage lists columns used by a CONSTRAINT. A plain
// secondary index is not a constraint -- it enforces nothing -- so listing its
// columns would tell a migrator (gorm.io/driver/postgres reads this view) that
// a uniqueness constraint exists where none does, and it would then skip
// creating the real one.
func TestConstraintColumnUsageSkipsNonUniqueIndexes(t *testing.T) {
	engine, ctx := newIndexTestEngine(t)

	rows := query(t, engine, ctx,
		"SELECT table_name, column_name FROM information_schema.constraint_column_usage ORDER BY table_name, column_name")

	for _, row := range rows {
		table, _ := row[0].(string)
		column, _ := row[1].(string)
		if table == "widgets" && column == "category" {
			t.Fatal("a non-unique secondary index was reported as a constraint")
		}
	}

	// The primary key and the unique index on the same table must still be
	// listed, so the filter is not simply dropping everything.
	var sawPK, sawUnique bool
	for _, row := range rows {
		table, _ := row[0].(string)
		column, _ := row[1].(string)
		if table != "widgets" {
			continue
		}
		switch column {
		case "id":
			sawPK = true
		case "sku":
			sawUnique = true
		}
	}
	if !sawPK {
		t.Error("the primary key column was not reported")
	}
	if !sawUnique {
		t.Error("the unique-index column was not reported")
	}
}

// newIndexTestEngine builds an engine with a table carrying a primary key, a
// unique index and a plain secondary index, so all three index kinds meet the
// filter.
func newIndexTestEngine(t *testing.T) (*sqle.Engine, *sql.Context) {
	t.Helper()

	Register()
	t.Cleanup(func() {
		delete(information_schema.NewInformationSchemaTablesToAdd, ConstraintColumnUsageTableName)
	})

	const dbName = "ccuindex"

	db := memory.NewDatabase(dbName)
	provider := memory.NewDBProvider(db)
	engine := sqle.NewDefault(provider)

	ctx := sql.NewContext(t.Context(),
		sql.WithSession(memory.NewSession(sql.NewBaseSession(), provider)))
	ctx.SetCurrentDatabase(dbName)

	for _, statement := range []string{
		"CREATE TABLE widgets (id BIGINT PRIMARY KEY, sku VARCHAR(64), category VARCHAR(64))",
		"CREATE UNIQUE INDEX unique_sku ON widgets (sku)",
		"CREATE INDEX idx_category ON widgets (category)",
	} {
		_, iter, _, err := engine.Query(ctx, statement)
		require.NoErrorf(t, err, "setup: %s", statement)
		require.NoError(t, iter.Close(ctx))
	}

	return engine, ctx
}
