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

//go:build integration

package integration_test

import (
	"database/sql"
	"testing"
)

// TestSQL_MySQL_ModifyColumnLeavesTheTableIntact pins the property that
// matters most about a failed DDL rewrite: whether it takes the table with
// it.
//
// ALTER TABLE ... MODIFY COLUMN re-encodes every stored row, because a
// relational row is a positional binary record rather than a self-describing
// one. If that rewrite fails partway, the honest outcome is that the
// statement fails and the table is exactly as it was. The outcome to prevent
// is a table left missing a column, which reports itself afterwards as
// "Unknown column" on a column the caller never dropped.
//
// The test asserts the invariant rather than the outcome: the ALTER may
// succeed or be refused, but either way every column and every row must
// still be there.
func TestSQL_MySQL_ModifyColumnLeavesTheTableIntact(t *testing.T) {
	_, mysqlPort, _, ca := startSingleNodeWithSQLMVCC(t)

	certPath, keyPath := ca.boundAdminCert(t, t.TempDir())
	db := waitForMySQL(t, ca, certPath, keyPath, mysqlPort, "admin")

	mustExec := func(query string) {
		t.Helper()
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}

	mustExec(`DROP TABLE IF EXISTS rewrite_probe`)
	mustExec(`CREATE TABLE rewrite_probe (id BIGINT PRIMARY KEY, name VARCHAR(32), note VARCHAR(64))`)
	mustExec(`INSERT INTO rewrite_probe (id, name, note) VALUES (1, 'alpha', 'first')`)
	mustExec(`INSERT INTO rewrite_probe (id, name, note) VALUES (2, 'beta', 'second')`)

	// Three shapes an ORM migration emits, in increasing order of how much
	// re-encoding they demand. Whether the engine performs each or refuses
	// it is not what this test pins -- only that the table survives either
	// way.
	for _, alter := range []string{
		"ALTER TABLE rewrite_probe MODIFY COLUMN name VARCHAR(128)",
		"ALTER TABLE rewrite_probe MODIFY COLUMN note LONGTEXT",
		"ALTER TABLE rewrite_probe MODIFY COLUMN name LONGTEXT",
	} {
		t.Run(alter, func(t *testing.T) {
			_, alterErr := db.Exec(alter)
			t.Logf("returned: %v", alterErr)
			assertTableIntact(t, db, alterErr)
		})
	}
}

// assertTableIntact checks that every column and row of the probe table
// survived, whatever the ALTER did.
func assertTableIntact(t *testing.T, db *sql.DB, alterErr error) {
	t.Helper()

	rows, err := db.Query("SELECT id, name, note FROM rewrite_probe ORDER BY id")
	if err != nil {
		t.Fatalf("the table no longer answers for its own columns after MODIFY COLUMN "+
			"(which returned %v): %v", alterErr, err)
	}
	defer rows.Close()

	type row struct {
		id         int64
		name, note string
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.name, &r.note); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	want := []row{{1, "alpha", "first"}, {2, "beta", "second"}}
	if len(got) != len(want) {
		t.Fatalf("row count after MODIFY COLUMN: got %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d after MODIFY COLUMN: got %+v, want %+v", i, got[i], want[i])
		}
	}
}
