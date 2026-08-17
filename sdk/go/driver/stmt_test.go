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

import (
	"context"
	sqld "database/sql/driver"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestCountPlaceholders_Various verifies placeholder counting with various queries.
func TestCountPlaceholders_Various(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
	}{
		// An empty string is not a statement, so the count is unknown (-1)
		// rather than zero. Reporting -1 tells database/sql to skip its
		// argument-count check and let the engine report the real error.
		{"empty query", "", -1},
		{"unparseable query", "NOT SQL AT ALL", -1},

		{"no placeholders", "SELECT * FROM t", 0},
		{"single placeholder", "SELECT * FROM t WHERE id = ?", 1},
		{"two placeholders", "INSERT INTO t VALUES (?, ?)", 2},
		{"three placeholders", "UPDATE t SET a=?, b=? WHERE c=?", 3},

		// Question marks that are not placeholders must not be counted.
		// Miscounting rejects a correct call before it reaches the engine.
		{"placeholder in quote skipped", "SELECT '?' FROM t", 0},
		{"mixed quote and real", "SELECT '?' FROM t WHERE a=? AND b=?", 2},
		{"question mark in comment skipped", "SELECT a FROM t -- why?\nWHERE b = ?", 1},
		{"question mark in backtick identifier skipped", "SELECT `who?` FROM t WHERE b = ?", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countPlaceholders(tt.query)
			if got != tt.want {
				t.Errorf("countPlaceholders(%q) = %d, want %d", tt.query, got, tt.want)
			}
		})
	}
}

// TestValuesToNamed verifies valuesToNamed converts correctly.
func TestValuesToNamed(t *testing.T) {
	args := []sqld.Value{"hello", int64(42), nil}
	named := valuesToNamed(args)

	if len(named) != 3 {
		t.Fatalf("len = %d, want 3", len(named))
	}
	for i, nv := range named {
		if nv.Ordinal != i+1 {
			t.Errorf("named[%d].Ordinal = %d, want %d", i, nv.Ordinal, i+1)
		}
		if nv.Value != args[i] {
			t.Errorf("named[%d].Value = %v, want %v", i, nv.Value, args[i])
		}
	}
}

// TestValuesToNamed_Empty verifies empty slice returns empty named slice.
func TestValuesToNamed_Empty(t *testing.T) {
	named := valuesToNamed(nil)
	if len(named) != 0 {
		t.Errorf("len = %d, want 0", len(named))
	}
}

// TestStmt_CloseAndOpsReturnError verifies stmt operations on closed stmt return StmtClosedError.
func TestStmt_CloseAndOpsReturnError(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=stmtclosetest")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	rawStmt, err := conn.Prepare("SELECT 1")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	s := rawStmt.(*stmt)

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err = s.Exec(nil)
	if err == nil {
		t.Fatal("expected error after Close")
	}
	var se *StmtClosedError
	if !errors.As(err, &se) {
		t.Errorf("Exec: expected *StmtClosedError, got %T: %v", err, err)
	}

	_, err = s.Query(nil)
	if err == nil {
		t.Fatal("expected error after Close")
	}
	if !errors.As(err, &se) {
		t.Errorf("Query: expected *StmtClosedError, got %T: %v", err, err)
	}

	_, err = s.ExecContext(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error after Close")
	}
	if !errors.As(err, &se) {
		t.Errorf("ExecContext: expected *StmtClosedError, got %T: %v", err, err)
	}

	_, err = s.QueryContext(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error after Close")
	}
	if !errors.As(err, &se) {
		t.Errorf("QueryContext: expected *StmtClosedError, got %T: %v", err, err)
	}
}

// TestStmt_NumInput verifies NumInput returns correct placeholder count.
//
// The statement is prepared against a table that exists, because Prepare now
// resolves the query through the engine and therefore reports an unknown table
// at prepare time — the same as MySQL's PREPARE.
func TestStmt_NumInput(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=numinputtest")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	rawStmt, err := conn.Prepare("SELECT * FROM kv WHERE `key`=? AND value=?")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer func() { _ = rawStmt.Close() }()

	s := rawStmt.(*stmt)
	if n := s.NumInput(); n != 2 {
		t.Errorf("NumInput() = %d, want 2", n)
	}
}

// TestStmt_PrepareRejectsUnknownTable verifies that preparing against a table
// that does not exist fails at prepare time rather than deferring the error to
// execution, matching MySQL's PREPARE behaviour.
func TestStmt_PrepareRejectsUnknownTable(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=preparestricttest")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Prepare("SELECT * FROM definitely_not_a_table WHERE a=?"); err == nil {
		t.Fatal("expected Prepare to reject an unknown table, got nil error")
	}
}

// TestNetworkStmt_CloseAndOpsReturnError verifies networkStmt operations on closed stmt.
func TestNetworkStmt_CloseAndOpsReturnError(t *testing.T) {
	conn := newFakeNetworkConn(&fakeSQLClient{}, "db")
	s := &networkStmt{conn: conn, query: "SELECT 1"}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := s.Exec(nil)
	if err == nil {
		t.Fatal("expected error after Close")
	}
	var se *StmtClosedError
	if !errors.As(err, &se) {
		t.Errorf("Exec: expected *StmtClosedError, got %T: %v", err, err)
	}

	_, err = s.Query(nil)
	if err == nil {
		t.Fatal("expected error after Close")
	}
	if !errors.As(err, &se) {
		t.Errorf("Query: expected *StmtClosedError, got %T: %v", err, err)
	}

	_, err = s.ExecContext(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error after Close")
	}
	if !errors.As(err, &se) {
		t.Errorf("ExecContext: expected *StmtClosedError, got %T: %v", err, err)
	}

	_, err = s.QueryContext(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error after Close")
	}
	if !errors.As(err, &se) {
		t.Errorf("QueryContext: expected *StmtClosedError, got %T: %v", err, err)
	}
}

// TestNetworkStmt_NumInput verifies NumInput for networkStmt.
func TestNetworkStmt_NumInput(t *testing.T) {
	conn := newFakeNetworkConn(&fakeSQLClient{}, "db")
	s := &networkStmt{conn: conn, query: "INSERT INTO t VALUES (?, ?, ?)"}
	if n := s.NumInput(); n != 3 {
		t.Errorf("NumInput() = %d, want 3", n)
	}
}

// TestNetworkStmt_PrepareFromConn verifies Prepare via networkConn.
func TestNetworkStmt_PrepareFromConn(t *testing.T) {
	conn := newFakeNetworkConn(&fakeSQLClient{}, "db")
	raw, err := conn.Prepare("SELECT ?")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	ns, ok := raw.(*networkStmt)
	if !ok {
		t.Fatalf("expected *networkStmt, got %T", raw)
	}
	if ns.query != "SELECT ?" {
		t.Errorf("query = %q, want SELECT ?", ns.query)
	}
}

// TestStmt_ExecAndQuery_NotClosed verifies Exec and Query work on open stmt.
func TestStmt_ExecAndQuery_NotClosed(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=stmtexectest")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	rawStmt, err := conn.Prepare("INSERT INTO kv (`key`, `value`) VALUES ('stmtkey', 'stmtval')")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer func() { _ = rawStmt.Close() }()

	s := rawStmt.(*stmt)

	// Test Exec (legacy driver.Value path).
	res, err := s.Exec(nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil result")
	}

	// Prepare a SELECT and test Query (legacy driver.Value path).
	rawStmt2, err := conn.Prepare("SELECT `key` FROM kv WHERE `key` = 'stmtkey'")
	if err != nil {
		t.Fatalf("Prepare SELECT: %v", err)
	}
	defer func() { _ = rawStmt2.Close() }()

	s2 := rawStmt2.(*stmt)
	rows, err := s2.Query(nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer func() { _ = rows.Close() }()
}

// TestNetworkStmt_ExecAndQuery_NotClosed verifies Exec and Query on open networkStmt.
func TestNetworkStmt_ExecAndQuery_NotClosed(t *testing.T) {
	conn := newFakeNetworkConn(&fakeSQLClient{result: &transport.SQLQueryResult{
		Columns: []string{"id"},
	}}, "testdb")

	rawStmt, err := conn.Prepare("SELECT `key` FROM kv")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer func() { _ = rawStmt.Close() }()

	ns := rawStmt.(*networkStmt)

	// Test Exec (legacy driver.Value path).
	res, err := ns.Exec(nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil result")
	}

	// Test Query (legacy driver.Value path).
	rows, err := ns.Query(nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer func() { _ = rows.Close() }()
}

// TestNetworkStmt_ExecContext_NotClosed verifies ExecContext on open networkStmt.
func TestNetworkStmt_ExecContext_NotClosed(t *testing.T) {
	conn := newFakeNetworkConn(&fakeSQLClient{result: &transport.SQLQueryResult{RowCount: 1}}, "testdb")
	ns := &networkStmt{conn: conn, query: "DELETE FROM kv WHERE `key` = 'x'"}

	res, err := ns.ExecContext(context.Background(), nil)
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil result")
	}
}

// TestNetworkStmt_QueryContext_NotClosed verifies QueryContext on open networkStmt.
func TestNetworkStmt_QueryContext_NotClosed(t *testing.T) {
	conn := newFakeNetworkConn(&fakeSQLClient{result: &transport.SQLQueryResult{
		Columns: []string{"key"},
	}}, "testdb")
	ns := &networkStmt{conn: conn, query: "SELECT `key` FROM kv"}

	rows, err := ns.QueryContext(context.Background(), nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	defer func() { _ = rows.Close() }()
}

// TestStmt_InterfaceCompliance verifies stmt/networkStmt implement required interfaces.
func TestStmt_InterfaceCompliance(t *testing.T) {
	var s1 sqld.Stmt = &stmt{}
	var s2 sqld.Stmt = &networkStmt{}
	if s1 == nil || s2 == nil {
		t.Fatal("stmt/networkStmt should implement driver.Stmt")
	}

	var se1 sqld.StmtExecContext = &stmt{}
	var se2 sqld.StmtExecContext = &networkStmt{}
	if se1 == nil || se2 == nil {
		t.Fatal("stmt/networkStmt should implement driver.StmtExecContext")
	}
}
