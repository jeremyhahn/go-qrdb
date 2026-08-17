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

//go:build qrdb_pgsql

package pgsql

import (
	"testing"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/memory"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
)

// TestResultColumns covers the PARSE-time result-column resolution that the
// PostgreSQL extended query protocol's Describe phase depends on.
//
// The case that matters most is INSERT … RETURNING. A PostgreSQL client
// settles a statement's result shape at Describe, before BIND delivers any
// parameter and before a row exists, and psql-wire refuses at EXECUTE to
// write a row whose width disagrees with what was declared. Declaring no
// columns for a statement that does produce a row is therefore a hard
// failure, not a conservative default: every such statement died with
// "unexpected columns, 0 columns are defined inside the given table but 1
// were given". gorm.io/driver/postgres reads back every generated primary key
// with exactly this statement, so the gap failed essentially every insert an
// ORM issued while leaving plain SELECT traffic working.
func TestResultColumns(t *testing.T) {
	h, ctx := newResultColumnsHandler(t)

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{
			name:  "select",
			query: "SELECT id, name FROM people",
			want:  []string{"id", "name"},
		},
		{
			name:  "select_star",
			query: "SELECT * FROM people",
			want:  []string{"id", "name"},
		},
		{
			name:  "select_with_placeholders",
			query: "SELECT id FROM people WHERE name = ?",
			want:  []string{"id"},
		},
		{
			name: "select_with_a_bound_limit",
			// gorm issues LIMIT $N on every First/Take/Last. A placeholder in
			// a row-count position is why the statement must be probed with
			// its ? intact: any string stand-in makes LIMIT unanalyzable.
			query: "SELECT * FROM people WHERE name = ? ORDER BY people.id LIMIT ?",
			want:  []string{"id", "name"},
		},
		{
			name:  "select_with_a_bound_limit_and_offset",
			query: "SELECT id FROM people LIMIT ? OFFSET ?",
			want:  []string{"id"},
		},
		{
			name:  "insert_returning_one_column",
			query: "INSERT INTO people (name) VALUES (?) RETURNING id",
			want:  []string{"id"},
		},
		{
			name:  "insert_returning_multiple_columns",
			query: "INSERT INTO people (name) VALUES (?) RETURNING id, name",
			want:  []string{"id", "name"},
		},
		{
			name:  "insert_returning_quoted",
			query: "INSERT INTO `people` (`name`) VALUES (?) RETURNING `id`",
			want:  []string{"id"},
		},
		{
			name:  "insert_all_defaults_returning",
			query: "INSERT INTO people () VALUES () RETURNING id",
			want:  []string{"id"},
		},
		{
			name:  "insert_returning_survives_on_duplicate_key_update",
			query: "INSERT INTO people (id, name) VALUES (?, ?) ON DUPLICATE KEY UPDATE name = VALUES(name) RETURNING id",
			want:  []string{"id"},
		},
		{
			name:  "plain_insert_produces_no_rows",
			query: "INSERT INTO people (name) VALUES (?)",
			want:  nil,
		},
		{
			name:  "update_produces_no_rows",
			query: "UPDATE people SET name = ? WHERE id = ?",
			want:  nil,
		},
		{
			name:  "delete_produces_no_rows",
			query: "DELETE FROM people WHERE id = ?",
			want:  nil,
		},
		{
			name: "returning_inside_a_string_literal_is_not_a_returning_clause",
			// The predicate is region-aware, so caller data cannot provoke a
			// probe -- and this statement genuinely produces no rows.
			query: "INSERT INTO people (name) VALUES ('RETURNING id')",
			want:  nil,
		},
		{
			name: "unresolvable_statement_yields_no_columns",
			// Metadata resolution stays silent on failure; diagnosing a bad
			// statement belongs to the execution path, which reports it
			// against the caller's real parameters.
			query: "SELECT id FROM no_such_table",
			want:  nil,
		},
		{
			name:  "unresolvable_returning_statement_yields_no_columns",
			query: "INSERT INTO no_such_table (a) VALUES (?) RETURNING id",
			want:  nil,
		},
		{
			name:  "unparseable_statement_yields_no_columns",
			query: "SELECT FROM WHERE",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			columns := h.resultColumns(ctx, tt.query, nil)

			if tt.want == nil {
				assert.Empty(t, columns)
				return
			}

			names := make([]string, len(columns))
			for i, column := range columns {
				names[i] = column.Name
			}
			assert.Equal(t, tt.want, names)
		})
	}
}

// TestResultColumnsMatchExecutedWidth is the invariant psql-wire enforces at
// EXECUTE and the one the missing INSERT … RETURNING description violated:
// whatever width is declared during Describe must equal the width of the rows
// the statement actually produces.
func TestResultColumnsMatchExecutedWidth(t *testing.T) {
	h, ctx := newResultColumnsHandler(t)

	queries := []string{
		"INSERT INTO people (name) VALUES ('declared') RETURNING id",
		"INSERT INTO people (name) VALUES ('declared2') RETURNING id, name",
		"SELECT id, name FROM people",
	}

	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			declared := h.resultColumns(ctx, query, nil)
			require.NotEmpty(t, declared, "a row-producing statement must describe its columns")

			schema, iter, _, err := h.sqlEngine.Query(ctx, query)
			require.NoError(t, err)
			result, err := collectRows(ctx, schema, iter)
			require.NoError(t, err)
			require.NotEmpty(t, result.rows)

			for _, row := range result.rows {
				assert.Len(t, row, len(declared),
					"executed row width must equal the width declared at PARSE time")
			}
		})
	}
}

// newResultColumnsHandler builds a ParseHandler over an in-memory
// go-mysql-server database holding one real table, which is what lets the
// PARSE-time probe resolve a schema at all.
func newResultColumnsHandler(t *testing.T) (*ParseHandler, *sql.Context) {
	t.Helper()

	const dbName = "resultcolumnstest"

	db := memory.NewDatabase(dbName)
	memProvider := memory.NewDBProvider(db)
	sqlEngine := sqle.NewDefault(memProvider)

	ctx := sql.NewContext(t.Context(),
		sql.WithSession(memory.NewSession(sql.NewBaseSession(), memProvider)))
	ctx.SetCurrentDatabase(dbName)

	_, iter, _, err := sqlEngine.Query(ctx,
		"CREATE TABLE people (id BIGINT PRIMARY KEY AUTO_INCREMENT, name VARCHAR(64))")
	require.NoError(t, err)
	require.NoError(t, iter.Close(ctx))

	return NewParseHandler(sqlEngine, engine.NewTranslator(), dbName), ctx
}
