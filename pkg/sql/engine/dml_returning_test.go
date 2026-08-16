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

// newDMLReturningEngine builds a qrdb engine (engine.New, so the DML-RETURNING
// repair below is active) over an in-memory database with one table carrying
// an auto-increment primary key and one unique column.
func newDMLReturningEngine(t *testing.T) (*sql.Context, func(string) ([]sql.Row, sql.Schema, error)) {
	t.Helper()

	const dbName = "dmlret"
	db := memory.NewDatabase(dbName)
	provider := memory.NewDBProvider(db)
	eng := New(Config{Provider: provider})
	t.Cleanup(func() { _ = eng.Close() })

	ctx := sql.NewContext(t.Context(), sql.WithSession(memory.NewSession(sql.NewBaseSession(), provider)))
	ctx.SetCurrentDatabase(dbName)

	run := func(q string) ([]sql.Row, sql.Schema, error) {
		sch, iter, _, err := eng.Query(ctx, q)
		if err != nil {
			return nil, nil, err
		}
		var rows []sql.Row
		for {
			row, nextErr := iter.Next(ctx)
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				_ = iter.Close(ctx)
				return nil, nil, nextErr
			}
			rows = append(rows, row)
		}
		if err := iter.Close(ctx); err != nil {
			return nil, nil, err
		}
		return rows, sch, nil
	}

	for _, q := range []string{
		"CREATE TABLE people (id BIGINT PRIMARY KEY AUTO_INCREMENT, name VARCHAR(64), UNIQUE KEY uniq_name (name))",
	} {
		if _, _, err := run(q); err != nil {
			t.Fatalf("setup %s: %v", q, err)
		}
	}
	return ctx, run
}

// TestInsertIgnoreReturning_YieldsRowsForInsertedOnly pins the PostgreSQL
// semantics of INSERT ... ON CONFLICT DO NOTHING RETURNING, which the
// translator renders as INSERT IGNORE ... RETURNING: rows come back for the
// tuples actually inserted, and NOTHING comes back for tuples the conflict
// suppressed.
//
// Upstream go-mysql-server silently discards the RETURNING rows for ignoring
// inserts (docs/sql/upstream-gaps.md #1): its accumulator selection has no
// case for the CheckpointingTableEditorIter an ignoring insert builds, so the
// statement degrades to an OkResult. On the PostgreSQL wire that surfaced as
// `cached plan must not change result type` (SQLSTATE 0A000) on 62 of the
// conformance failures — every GORM belongs-to/many2many create goes through
// exactly this statement shape.
func TestInsertIgnoreReturning_YieldsRowsForInsertedOnly(t *testing.T) {
	_, run := newDMLReturningEngine(t)

	// Fresh row: exactly one returning row, exactly one column.
	rows, sch, err := run("INSERT IGNORE INTO people (name) VALUES ('alice') RETURNING id")
	if err != nil {
		t.Fatalf("fresh insert: %v", err)
	}
	if len(sch) != 1 || sch[0].Name != "id" {
		t.Fatalf("declared schema = %v, want [id]", sch)
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("rows = %v, want one single-column row", rows)
	}
	firstID := rows[0][0]

	// The same name again: the conflict is suppressed and NO row returns,
	// exactly as PostgreSQL's ON CONFLICT DO NOTHING RETURNING behaves.
	rows, _, err = run("INSERT IGNORE INTO people (name) VALUES ('alice') RETURNING id")
	if err != nil {
		t.Fatalf("conflicting insert: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("conflicting insert returned %v, want no rows", rows)
	}

	// Multi-row with one conflict: only the fresh tuple returns, and its id
	// is new rather than a replay of the conflicting row's.
	rows, _, err = run("INSERT IGNORE INTO people (name) VALUES ('alice'), ('bob') RETURNING id")
	if err != nil {
		t.Fatalf("mixed insert: %v", err)
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("mixed insert rows = %v, want one single-column row", rows)
	}
	if rows[0][0] == firstID {
		t.Fatalf("mixed insert returned the conflicting row's id %v", firstID)
	}
}

// TestOnDuplicateKeyReturning_RowMatchesDeclaredSchema pins the upsert form:
// INSERT ... ON DUPLICATE KEY UPDATE ... RETURNING (the translation of
// PostgreSQL's ON CONFLICT DO UPDATE ... RETURNING).
//
// Upstream, the duplicate path emits the OLD and NEW tuples concatenated —
// a four-value row against a one-column declared schema
// (docs/sql/upstream-gaps.md #2) — which the PostgreSQL wire correctly
// refuses to serialize. The repaired path projects the RETURNING expressions
// against the NEW tuple, so updated rows come back exactly as wide as the
// declared schema, with the post-update values.
func TestOnDuplicateKeyReturning_RowMatchesDeclaredSchema(t *testing.T) {
	_, run := newDMLReturningEngine(t)

	rows, _, err := run("INSERT INTO people (id, name) VALUES (7, 'carol') RETURNING id")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(rows) != 1 || rows[0][0] != int64(7) {
		t.Fatalf("seed rows = %v, want [[7]]", rows)
	}

	// The upsert hits the duplicate path: the row must still be one column
	// wide and carry the (unchanged) primary key.
	rows, sch, err := run(
		"INSERT INTO people (id, name) VALUES (7, 'carol-2') ON DUPLICATE KEY UPDATE name = VALUES(name) RETURNING id")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if len(sch) != 1 || sch[0].Name != "id" {
		t.Fatalf("declared schema = %v, want [id]", sch)
	}
	if len(rows) != 1 {
		t.Fatalf("upsert returned %d rows, want 1", len(rows))
	}
	if len(rows[0]) != 1 {
		t.Fatalf("upsert row %v is %d values wide, want 1 (the old+new concatenation leak)",
			rows[0], len(rows[0]))
	}
	if rows[0][0] != int64(7) {
		t.Fatalf("upsert RETURNING id = %v, want 7", rows[0][0])
	}

	// And the update must actually have applied.
	rows, _, err = run("SELECT name FROM people WHERE id = 7")
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if len(rows) != 1 || rows[0][0] != "carol-2" {
		t.Fatalf("readback = %v, want [[carol-2]]", rows)
	}
}

// TestOnDuplicateKeyReturning_InsertPathUnaffected covers the same statement
// when NO conflict occurs: the plain-insert branch already projected its
// RETURNING row correctly upstream, and the repair must leave it alone rather
// than re-projecting an already-projected row.
func TestOnDuplicateKeyReturning_InsertPathUnaffected(t *testing.T) {
	_, run := newDMLReturningEngine(t)

	rows, _, err := run(
		"INSERT INTO people (id, name) VALUES (9, 'dave') ON DUPLICATE KEY UPDATE name = VALUES(name) RETURNING id")
	if err != nil {
		t.Fatalf("insert-path upsert: %v", err)
	}
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != int64(9) {
		t.Fatalf("rows = %v, want [[9]]", rows)
	}
}

// TestPlainInsertReturning_Unchanged pins that the repair does not intercept
// the statement shape upstream already handles correctly.
func TestPlainInsertReturning_Unchanged(t *testing.T) {
	_, run := newDMLReturningEngine(t)

	rows, sch, err := run("INSERT INTO people (name) VALUES ('erin') RETURNING id, name")
	if err != nil {
		t.Fatalf("plain returning: %v", err)
	}
	if len(sch) != 2 {
		t.Fatalf("declared schema = %v, want [id name]", sch)
	}
	if len(rows) != 1 || len(rows[0]) != 2 || rows[0][1] != "erin" {
		t.Fatalf("rows = %v, want one [id erin] row", rows)
	}
}
