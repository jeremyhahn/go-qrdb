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

import (
	"database/sql"
	"reflect"
	"testing"
)

// ---------------------------------------------------------------------------
// ENUM and SET are the two column types go-mysql-server represents
// symbolically: a cell holds the uint16 ordinal of an ENUM label, or the
// uint64 bitmask of a SET's members, and only the column's declared type can
// say which label those integers stand for. This driver used to convert row
// cells by switching on their Go type alone, so both arrived as integers:
// SELECT status returned 2 where 'active' was inserted, and SELECT perms
// returned 5 where 'read,admin' was inserted.
//
// The tests below pin the resolved values through a real engine, a real
// relational table, and database/sql. The cross-transport half of the
// guarantee -- that these agree with what the MySQL wire protocol returns
// for the same row -- lives in pkg/sql/mysql, which can reach both this
// driver and a wire server.
// ---------------------------------------------------------------------------

// createSymbolicTable creates the table every test in this file shares.
func createSymbolicTable(t *testing.T, db *sql.DB) {
	t.Helper()
	const ddl = `CREATE TABLE accounts (
		id INT PRIMARY KEY,
		status ENUM('pending','active','banned'),
		perms SET('read','write','admin')
	)`
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
}

// TestEmbeddedSymbolic_EnumRoundTrip asserts an ENUM label inserted is the
// label read back, for every member of the declared value list -- including
// ordinal 1, whose integer form is the easiest wrong answer to mistake for
// a plausible one.
func TestEmbeddedSymbolic_EnumRoundTrip(t *testing.T) {
	db, _ := openRelationalTestDB(t, "enumroundtrip")
	createSymbolicTable(t, db)

	labels := []string{"pending", "active", "banned"}
	for i, want := range labels {
		if _, err := db.Exec("INSERT INTO accounts (id, status, perms) VALUES (?, ?, 'read')", i+1, want); err != nil {
			t.Fatalf("INSERT %q: %v", want, err)
		}

		var got string
		if err := db.QueryRow("SELECT status FROM accounts WHERE id = ?", i+1).Scan(&got); err != nil {
			t.Fatalf("SELECT %q: %v", want, err)
		}
		if got != want {
			t.Errorf("status = %q, want %q (an integer here means the ordinal leaked out unresolved)", got, want)
		}
	}
}

// TestEmbeddedSymbolic_EnumUntypedScan asserts the value reaches an
// interface-typed destination as a string. Scanning into a *string can mask
// the defect, because database/sql will happily render an int64 ordinal as
// its decimal digits; scanning into an *any cannot.
func TestEmbeddedSymbolic_EnumUntypedScan(t *testing.T) {
	db, _ := openRelationalTestDB(t, "enumuntyped")
	createSymbolicTable(t, db)

	if _, err := db.Exec("INSERT INTO accounts VALUES (1, 'active', 'read,admin')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	var status, perms any
	if err := db.QueryRow("SELECT status, perms FROM accounts WHERE id = 1").Scan(&status, &perms); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if status != "active" {
		t.Errorf("status = %#v (%T), want \"active\"", status, status)
	}
	if perms != "read,admin" {
		t.Errorf("perms = %#v (%T), want \"read,admin\"", perms, perms)
	}
}

// TestEmbeddedSymbolic_SetRoundTrip covers the SET analogue, including the
// empty set and a non-contiguous member selection whose bitmask (5) is a
// particularly convincing wrong answer.
func TestEmbeddedSymbolic_SetRoundTrip(t *testing.T) {
	db, _ := openRelationalTestDB(t, "setroundtrip")
	createSymbolicTable(t, db)

	tests := []struct {
		id       int
		inserted string
		want     string
	}{
		{1, "", ""},
		{2, "read", "read"},
		{3, "read,admin", "read,admin"},
		{4, "admin,read", "read,admin"},
		{5, "read,write,admin", "read,write,admin"},
	}

	for _, tc := range tests {
		if _, err := db.Exec("INSERT INTO accounts (id, status, perms) VALUES (?, 'active', ?)", tc.id, tc.inserted); err != nil {
			t.Fatalf("INSERT %q: %v", tc.inserted, err)
		}

		var got string
		if err := db.QueryRow("SELECT perms FROM accounts WHERE id = ?", tc.id).Scan(&got); err != nil {
			t.Fatalf("SELECT %q: %v", tc.inserted, err)
		}
		if got != tc.want {
			t.Errorf("perms for %q = %q, want %q", tc.inserted, got, tc.want)
		}
	}
}

// TestEmbeddedSymbolic_EnumInWherePredicate asserts resolution on the read
// path did not disturb the engine's own comparisons, which operate on the
// ordinal and must keep doing so.
func TestEmbeddedSymbolic_EnumInWherePredicate(t *testing.T) {
	db, _ := openRelationalTestDB(t, "enumwhere")
	createSymbolicTable(t, db)

	rows := []struct {
		id     int
		status string
	}{{1, "active"}, {2, "banned"}, {3, "active"}}
	for _, r := range rows {
		if _, err := db.Exec("INSERT INTO accounts (id, status, perms) VALUES (?, ?, 'read')", r.id, r.status); err != nil {
			t.Fatalf("INSERT: %v", err)
		}
	}

	t.Run("literal predicate", func(t *testing.T) {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM accounts WHERE status = 'active'").Scan(&n); err != nil {
			t.Fatalf("SELECT: %v", err)
		}
		if n != 2 {
			t.Errorf("count = %d, want 2", n)
		}
	})

	t.Run("bound parameter predicate", func(t *testing.T) {
		var id int
		if err := db.QueryRow("SELECT id FROM accounts WHERE status = ?", "banned").Scan(&id); err != nil {
			t.Fatalf("SELECT: %v", err)
		}
		if id != 2 {
			t.Errorf("id = %d, want 2", id)
		}
	})

	t.Run("set predicate", func(t *testing.T) {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM accounts WHERE perms = 'read'").Scan(&n); err != nil {
			t.Fatalf("SELECT: %v", err)
		}
		if n != 3 {
			t.Errorf("count = %d, want 3", n)
		}
	})
}

// TestEmbeddedSymbolic_Null asserts a NULL ENUM or SET stays NULL. Ordinal 0
// is not a member of any ENUM, so resolving a NULL cell as if it held one
// would be both wrong and plausible-looking.
func TestEmbeddedSymbolic_Null(t *testing.T) {
	db, _ := openRelationalTestDB(t, "enumnull")
	createSymbolicTable(t, db)

	if _, err := db.Exec("INSERT INTO accounts (id, status, perms) VALUES (1, NULL, NULL)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	var status, perms sql.NullString
	if err := db.QueryRow("SELECT status, perms FROM accounts WHERE id = 1").Scan(&status, &perms); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if status.Valid {
		t.Errorf("status = %q, want NULL", status.String)
	}
	if perms.Valid {
		t.Errorf("perms = %q, want NULL", perms.String)
	}

	var raw any
	if err := db.QueryRow("SELECT status FROM accounts WHERE id = 1").Scan(&raw); err != nil {
		t.Fatalf("SELECT untyped: %v", err)
	}
	if raw != nil {
		t.Errorf("status = %#v, want nil", raw)
	}
}

// TestEmbeddedSymbolic_UpdateThenSelect asserts the resolution survives a
// rewrite of the cell, not just its initial insert.
func TestEmbeddedSymbolic_UpdateThenSelect(t *testing.T) {
	db, _ := openRelationalTestDB(t, "enumupdate")
	createSymbolicTable(t, db)

	if _, err := db.Exec("INSERT INTO accounts VALUES (1, 'pending', 'read')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if _, err := db.Exec("UPDATE accounts SET status = 'banned', perms = 'write,admin' WHERE id = 1"); err != nil {
		t.Fatalf("UPDATE: %v", err)
	}

	var status, perms string
	if err := db.QueryRow("SELECT status, perms FROM accounts WHERE id = 1").Scan(&status, &perms); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if status != "banned" {
		t.Errorf("status = %q, want banned", status)
	}
	if perms != "write,admin" {
		t.Errorf("perms = %q, want write,admin", perms)
	}
}

// TestEmbeddedSymbolic_ColumnTypeScanType asserts the driver advertises the
// type it actually produces. This is the surface GORM's Migrator.ColumnTypes
// reports, and where the ordinal leak was first observed; advertising uint16
// for a column that yields a string is a contract no row can satisfy.
func TestEmbeddedSymbolic_ColumnTypeScanType(t *testing.T) {
	db, _ := openRelationalTestDB(t, "enumscantype")
	createSymbolicTable(t, db)

	if _, err := db.Exec("INSERT INTO accounts VALUES (1, 'active', 'read,admin')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows, err := db.Query("SELECT id, status, perms FROM accounts")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer func() { _ = rows.Close() }()

	types, err := rows.ColumnTypes()
	if err != nil {
		t.Fatalf("ColumnTypes: %v", err)
	}

	want := map[string]reflect.Type{
		"id":     reflect.TypeOf(int32(0)),
		"status": reflect.TypeOf(""),
		"perms":  reflect.TypeOf(""),
	}
	for _, ct := range types {
		if got := ct.ScanType(); got != want[ct.Name()] {
			t.Errorf("%s ScanType = %v, want %v", ct.Name(), got, want[ct.Name()])
		}
	}

	// The advertised scan type must be honoured by an actual scan.
	if !rows.Next() {
		t.Fatalf("no rows: %v", rows.Err())
	}
	var id int32
	var status, perms string
	if err := rows.Scan(&id, &status, &perms); err != nil {
		t.Fatalf("Scan into advertised types: %v", err)
	}
	if status != "active" || perms != "read,admin" {
		t.Errorf("status/perms = %q/%q, want active/read,admin", status, perms)
	}
}

// TestEmbeddedSymbolic_MultiRowScan asserts every row of a multi-row result
// is resolved, not just the first, and that resolution is per column rather
// than per result set.
func TestEmbeddedSymbolic_MultiRowScan(t *testing.T) {
	db, _ := openRelationalTestDB(t, "enummultirow")
	createSymbolicTable(t, db)

	inserts := []struct {
		id     int
		status string
		perms  string
	}{
		{1, "pending", "read"},
		{2, "active", "read,write"},
		{3, "banned", "admin"},
	}
	for _, in := range inserts {
		if _, err := db.Exec("INSERT INTO accounts VALUES (?, ?, ?)", in.id, in.status, in.perms); err != nil {
			t.Fatalf("INSERT: %v", err)
		}
	}

	rows, err := db.Query("SELECT id, status, perms FROM accounts ORDER BY id")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer func() { _ = rows.Close() }()

	i := 0
	for rows.Next() {
		var id int
		var status, perms string
		if err := rows.Scan(&id, &status, &perms); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if i >= len(inserts) {
			t.Fatalf("more rows than inserted")
		}
		want := inserts[i]
		if id != want.id || status != want.status || perms != want.perms {
			t.Errorf("row %d = (%d, %q, %q), want (%d, %q, %q)", i, id, status, perms, want.id, want.status, want.perms)
		}
		i++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if i != len(inserts) {
		t.Errorf("scanned %d rows, want %d", i, len(inserts))
	}
}
