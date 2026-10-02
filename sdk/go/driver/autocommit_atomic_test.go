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

//go:build qrdb_sql

package driver

import "testing"

// TestEmbedded_AutocommitStatementIsAtomic: a multi-row INSERT outside a
// transaction whose third row is a duplicate leaves its first two rows
// absent -- a statement is its own transaction, as in InnoDB.
func TestEmbedded_AutocommitStatementIsAtomic(t *testing.T) {
	db, _ := openRelationalTestDB(t, "atomic")

	if _, err := db.Exec("CREATE TABLE widgets (id BIGINT PRIMARY KEY, name VARCHAR(16))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec("INSERT INTO widgets (id, name) VALUES (1, 'a')"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.Exec("INSERT INTO widgets (id, name) VALUES (10, 'x'), (11, 'y'), (1, 'dup')"); err == nil {
		t.Fatal("row 3 duplicates id 1; the statement must fail")
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM widgets WHERE id IN (10, 11)").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("%d rows of the failed statement were committed, want 0", n)
	}
}
