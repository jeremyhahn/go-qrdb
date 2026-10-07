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

package driver

import "testing"

// TestEmbeddedRelational_OnDuplicateKeyReturning_RowMatchesDeclaredSchema is
// the conformance proof for docs/sql/upstream-gaps.md #2 through
// database/sql itself, independent of pkg/sql/mysql's wire-level proof.
//
// Upstream, an upsert that hits go-mysql-server's ON DUPLICATE KEY UPDATE
// path emits the OLD and NEW tuples concatenated: a row wider than the
// RETURNING projection declares. This embedded driver's Rows.Next refuses a
// row whose width disagrees with Columns() with *driver.RowWidthError
// (rows_embedded.go) -- database/sql sizes its scan destination from
// Columns(), so a wider row would index past it -- which is exactly the
// contract a GORM application leans on when it reads a primary key back
// after `ON CONFLICT ... DO UPDATE ... RETURNING`. An unrepaired row would
// surface here as that typed error rather than as the pre-update value.
func TestEmbeddedRelational_OnDuplicateKeyReturning_RowMatchesDeclaredSchema(t *testing.T) {
	db, _ := openRelationalTestDB(t, "ondupreturning")

	if _, err := db.Exec("CREATE TABLE people (id BIGINT PRIMARY KEY, name VARCHAR(64))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec("INSERT INTO people (id, name) VALUES (7, 'carol')"); err != nil {
		t.Fatalf("seed insert: %v", err)
	}

	var id int64
	var name string
	err := db.QueryRow(
		"INSERT INTO people (id, name) VALUES (7, 'carol-2') ON DUPLICATE KEY UPDATE name = VALUES(name) RETURNING id, name",
	).Scan(&id, &name)
	if err != nil {
		t.Fatalf("upsert RETURNING: %v", err)
	}
	if id != 7 {
		t.Fatalf("id = %v, want 7", id)
	}
	if name != "carol-2" {
		t.Fatalf("name = %q, want carol-2 (the post-update value, not the pre-update one a naive truncation would leak)", name)
	}

	var readback string
	if err := db.QueryRow("SELECT name FROM people WHERE id = 7").Scan(&readback); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if readback != "carol-2" {
		t.Fatalf("readback = %q, want carol-2", readback)
	}
}

// TestEmbeddedRelational_OnDuplicateKeyReturning_InsertPathUnaffected covers
// the same statement when NO conflict occurs: the plain-insert branch
// already projects its RETURNING row correctly upstream, and the repair
// must not re-project an already-correct row.
func TestEmbeddedRelational_OnDuplicateKeyReturning_InsertPathUnaffected(t *testing.T) {
	db, _ := openRelationalTestDB(t, "ondupreturninginsert")

	if _, err := db.Exec("CREATE TABLE people (id BIGINT PRIMARY KEY, name VARCHAR(64))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	var id int64
	var name string
	err := db.QueryRow(
		"INSERT INTO people (id, name) VALUES (9, 'dave') ON DUPLICATE KEY UPDATE name = VALUES(name) RETURNING id, name",
	).Scan(&id, &name)
	if err != nil {
		t.Fatalf("insert-path upsert RETURNING: %v", err)
	}
	if id != 9 || name != "dave" {
		t.Fatalf("got (id=%v, name=%q), want (9, \"dave\")", id, name)
	}
}
