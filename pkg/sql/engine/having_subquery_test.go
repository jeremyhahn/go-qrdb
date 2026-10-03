//go:build qrdb_sql

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

package engine

import (
	"io"
	"testing"

	"github.com/dolthub/go-mysql-server/memory"
	"github.com/dolthub/go-mysql-server/sql"
)

// newHavingEngine builds a qrdb engine over one in-memory table with two
// groupable columns.
func newHavingEngine(t *testing.T) func(string) ([]sql.Row, error) {
	t.Helper()

	const dbName = "havingtest"
	db := memory.NewDatabase(dbName)
	provider := memory.NewDBProvider(db)
	eng := New(Config{Provider: provider})
	t.Cleanup(func() { _ = eng.Close() })

	ctx := sql.NewContext(t.Context(), sql.WithSession(memory.NewSession(sql.NewBaseSession(), provider)))
	ctx.SetCurrentDatabase(dbName)

	run := func(q string) ([]sql.Row, error) {
		_, iter, _, err := eng.Query(ctx, q)
		if err != nil {
			return nil, err
		}
		var rows []sql.Row
		for {
			row, nextErr := iter.Next(ctx)
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				_ = iter.Close(ctx)
				return nil, nextErr
			}
			rows = append(rows, row)
		}
		return rows, iter.Close(ctx)
	}

	for _, q := range []string{
		"CREATE TABLE users (id BIGINT PRIMARY KEY AUTO_INCREMENT, name VARCHAR(64), age BIGINT)",
		"INSERT INTO users (name, age) VALUES ('a', 10), ('a', 20), ('b', 30), ('b', 40), ('c', 1)",
	} {
		if _, err := run(q); err != nil {
			t.Fatalf("setup %s: %v", q, err)
		}
	}
	return run
}

// TestHavingScalarSubquery_ComputesItsOwnAggregate pins the repair in
// having_subquery.go against the exact statement shape GORM's
// TestSubQueryWithHaving sends.
//
// Upstream go-mysql-server deduplicates the subquery's AVG against the outer
// query's identically named AVG at bind time, so the subquery loses its own
// aggregation, returns one row per source row, and the comparison fails with
// "the subquery returned more than 1 row". The correct answer is one group:
// the overall average is 20.2, and only group b (average 35) exceeds it.
func TestHavingScalarSubquery_ComputesItsOwnAggregate(t *testing.T) {
	run := newHavingEngine(t)

	rows, err := run(
		"SELECT AVG(age) AS avgage FROM users GROUP BY name HAVING AVG(age) > (SELECT AVG(age) FROM users)")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("rows = %v, want exactly [[35]]", rows)
	}
	if got, ok := rows[0][0].(float64); !ok || got != 35 {
		t.Fatalf("group average = %v (%T), want 35", rows[0][0], rows[0][0])
	}
}

// TestHavingScalarSubquery_WithWhereAndQualifiedColumns covers the
// qualifier-rewrite half of the repair: the renamed table's qualified
// references inside the subquery's own WHERE must follow the rename, or the
// repair would turn a valid query into "unknown table".
func TestHavingScalarSubquery_WithWhereAndQualifiedColumns(t *testing.T) {
	run := newHavingEngine(t)

	rows, err := run(
		"SELECT AVG(age) AS avgage FROM users GROUP BY name " +
			"HAVING AVG(age) > (SELECT AVG(users.age) FROM users WHERE users.name <> 'c')")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// Average over a,b groups = 25; only group b (35) exceeds it.
	if len(rows) != 1 || rows[0][0] != float64(35) {
		t.Fatalf("rows = %v, want [[35]]", rows)
	}
}

// TestHavingScalarSubquery_AlreadyAliasedTableUntouched pins that a subquery
// whose table carries its own alias is left exactly as written — an aliased
// table never collides, and rewriting user-chosen aliases would change error
// messages and EXPLAIN output for no repair value.
func TestHavingScalarSubquery_AlreadyAliasedTableUntouched(t *testing.T) {
	run := newHavingEngine(t)

	rows, err := run(
		"SELECT AVG(age) AS avgage FROM users GROUP BY name HAVING AVG(age) > (SELECT AVG(u.age) FROM users AS u)")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 || rows[0][0] != float64(35) {
		t.Fatalf("rows = %v, want [[35]]", rows)
	}
}

// TestHavingWithoutSubquery_Untouched covers the pass-through: plain HAVING
// clauses are the common case and must not be rewritten at all.
func TestHavingWithoutSubquery_Untouched(t *testing.T) {
	run := newHavingEngine(t)

	rows, err := run("SELECT name, AVG(age) FROM users GROUP BY name HAVING AVG(age) > 20 ORDER BY name")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 || rows[0][0] != "b" {
		t.Fatalf("rows = %v, want [[b 35]]", rows)
	}
}
