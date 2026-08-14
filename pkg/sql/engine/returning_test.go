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

package engine_test

import (
	"io"
	"strings"
	"testing"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/memory"
	gmssql "github.com/dolthub/go-mysql-server/sql"
)

// TestInsertReturning_YieldsRequestedColumn pins the behaviour of
// INSERT … RETURNING, which qrdb's PostgreSQL endpoint depends on and which
// go-mysql-server does not test upstream.
//
// This is a dependency-pinning regression test, not a test of qrdb code. It
// exists because the previously pinned go-mysql-server (v0.20.0) returned the
// WRONG column for this statement — the returning expression's index was
// shifted by one, so `RETURNING a` on (a, b, c) yielded b, `RETURNING c`
// errored outright with "unable to find field with index 3 in row of 3
// columns", and `RETURNING id` on an auto-increment table returned the
// inserted name instead of the generated id.
//
// gorm.io/driver/postgres populates a model's primary key from exactly that
// last form, so the bug silently produced wrong primary keys rather than an
// error. Nothing in qrdb would catch a regression to it, because the defect
// lives entirely in the dependency. Hence this test.
func TestInsertReturning_YieldsRequestedColumn(t *testing.T) {
	engine, ctx := newReturningEngine(t)

	tests := []struct {
		name  string
		query string
		want  any
	}{
		{
			name:  "first column",
			query: "INSERT INTO cols (a, b, c) VALUES (10, 20, 30) RETURNING a",
			want:  int64(10),
		},
		{
			name:  "middle column",
			query: "INSERT INTO cols (a, b, c) VALUES (11, 21, 31) RETURNING b",
			want:  int64(21),
		},
		{
			// The last column is the case v0.20.0 failed hardest on: it read
			// past the end of the row and returned an error.
			name:  "last column",
			query: "INSERT INTO cols (a, b, c) VALUES (12, 22, 32) RETURNING c",
			want:  int64(32),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := queryRows(t, engine, ctx, tt.query)
			if len(rows) != 1 {
				t.Fatalf("got %d rows, want 1", len(rows))
			}
			if len(rows[0]) != 1 {
				t.Fatalf("got %d columns, want 1", len(rows[0]))
			}
			if rows[0][0] != tt.want {
				t.Errorf("RETURNING value = %v (%T), want %v", rows[0][0], rows[0][0], tt.want)
			}
		})
	}
}

// TestInsertReturning_YieldsGeneratedAutoIncrementID pins the exact form
// gorm.io/driver/postgres uses to populate a model's primary key after Create.
func TestInsertReturning_YieldsGeneratedAutoIncrementID(t *testing.T) {
	engine, ctx := newReturningEngine(t)

	rows := queryRows(t, engine, ctx, "INSERT INTO people (name) VALUES ('alice') RETURNING id")
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("got rows %v, want one row of one column", rows)
	}

	// The generated id, not the inserted name. v0.20.0 returned "alice" here.
	id, ok := rows[0][0].(int64)
	if !ok {
		t.Fatalf("RETURNING id = %v (%T), want an int64 id", rows[0][0], rows[0][0])
	}
	if id <= 0 {
		t.Errorf("generated id = %d, want a positive auto-increment value", id)
	}
}

// TestInsertReturning_MultipleRowsAndColumns verifies a multi-row insert
// returns one row per inserted row, and that multiple returning expressions
// are supported. Both errored on v0.20.0.
func TestInsertReturning_MultipleRowsAndColumns(t *testing.T) {
	engine, ctx := newReturningEngine(t)

	rows := queryRows(t, engine, ctx,
		"INSERT INTO people (name) VALUES ('bob'), ('carol') RETURNING id, name")
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	for i, row := range rows {
		if len(row) != 2 {
			t.Fatalf("row %d has %d columns, want 2", i, len(row))
		}
		if _, ok := row[0].(int64); !ok {
			t.Errorf("row %d id = %v (%T), want int64", i, row[0], row[0])
		}
	}
	if got := rows[0][1]; got != "bob" {
		t.Errorf("row 0 name = %v, want bob", got)
	}
	if got := rows[1][1]; got != "carol" {
		t.Errorf("row 1 name = %v, want carol", got)
	}
}

// TestUpdateAndDeleteReturning_DoNotParse pins a known limitation.
//
// The vitess grammar gained RETURNING for INSERT only. UPDATE … RETURNING and
// DELETE … RETURNING are executable — go-mysql-server honours the Returning
// field on both plan nodes when the AST is constructed programmatically — but
// they cannot be parsed from SQL text, so they fail before reaching the
// planner.
//
// This matters for the PostgreSQL endpoint: those two forms must be either
// clause-peeled and attached to the AST, or rejected explicitly. What they
// must not do is fall through silently, which is how the previous translator
// behaved when it stripped RETURNING outright.
//
// If this test starts failing because the statements now parse, the
// limitation has been lifted upstream and the PostgreSQL path can drop its
// special handling.
func TestUpdateAndDeleteReturning_DoNotParse(t *testing.T) {
	engine, ctx := newReturningEngine(t)

	execStatement(t, engine, ctx, "INSERT INTO people (name) VALUES ('dave')")

	queries := []string{
		"UPDATE people SET name = 'dave2' WHERE name = 'dave' RETURNING name",
		"DELETE FROM people WHERE name = 'dave' RETURNING id",
	}

	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			_, _, _, err := engine.Query(ctx, query)
			if err == nil {
				t.Fatal("statement parsed; the upstream grammar limitation may have been lifted — " +
					"see this test's doc comment")
			}
			if !strings.Contains(err.Error(), "syntax error") {
				t.Errorf("error = %v, want a syntax error", err)
			}
		})
	}
}

// newReturningEngine builds an in-memory engine with the tables the RETURNING
// tests use.
func newReturningEngine(t *testing.T) (*sqle.Engine, *gmssql.Context) {
	t.Helper()

	const dbName = "returningtest"

	// Primary key indexes are always enabled on current go-mysql-server;
	// the opt-in EnablePrimaryKeyIndexes toggle no longer exists.
	db := memory.NewDatabase(dbName)

	provider := memory.NewDBProvider(db)
	engine := sqle.NewDefault(provider)

	ctx := gmssql.NewContext(t.Context(), gmssql.WithSession(memory.NewSession(gmssql.NewBaseSession(), provider)))
	ctx.SetCurrentDatabase(dbName)

	execStatement(t, engine, ctx,
		"CREATE TABLE people (id BIGINT PRIMARY KEY AUTO_INCREMENT, name VARCHAR(64))")
	execStatement(t, engine, ctx,
		"CREATE TABLE cols (a BIGINT, b BIGINT, c BIGINT)")

	return engine, ctx
}

// execStatement runs a statement and discards its result.
func execStatement(t *testing.T, engine *sqle.Engine, ctx *gmssql.Context, query string) {
	t.Helper()

	_, iter, _, err := engine.Query(ctx, query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if _, err := drain(ctx, iter); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// queryRows runs a query and returns all of its rows.
func queryRows(t *testing.T, engine *sqle.Engine, ctx *gmssql.Context, query string) []gmssql.Row {
	t.Helper()

	_, iter, _, err := engine.Query(ctx, query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	rows, err := drain(ctx, iter)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return rows
}

// drain consumes a row iterator, always closing it.
func drain(ctx *gmssql.Context, iter gmssql.RowIter) (rows []gmssql.Row, err error) {
	defer func() {
		if closeErr := iter.Close(ctx); err == nil {
			err = closeErr
		}
	}()

	for {
		row, nextErr := iter.Next(ctx)
		if nextErr == io.EOF {
			return rows, nil
		}
		if nextErr != nil {
			return nil, nextErr
		}
		rows = append(rows, row)
	}
}
