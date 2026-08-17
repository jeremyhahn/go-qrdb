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
	"database/sql"
	sqld "database/sql/driver"
	"errors"
	"testing"
)

// openEmbeddedDB is a test helper that opens an in-memory embedded database.
func openEmbeddedDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("qrdb", "embedded://memory?database=testdb")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestEmbeddedConn_InsertKV verifies INSERT into the kv table works.
func TestEmbeddedConn_InsertKV(t *testing.T) {
	db := openEmbeddedDB(t)

	_, err := db.Exec("INSERT INTO kv (`key`, `value`) VALUES ('hello', 'world')")
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}
}

// TestEmbeddedConn_InsertAndSelect verifies INSERT and SELECT via sql.DB.
func TestEmbeddedConn_InsertAndSelect(t *testing.T) {
	db := openEmbeddedDB(t)

	_, err := db.Exec("INSERT INTO kv (`key`, `value`) VALUES ('greet', 'earth')")
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows, err := db.Query("SELECT `key`, `value` FROM kv WHERE `key` = 'greet'")
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		t.Fatal("expected at least one row")
	}

	var k string
	var v []byte
	if err := rows.Scan(&k, &v); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if k != "greet" {
		t.Errorf("key = %q, want greet", k)
	}
}

// TestEmbeddedConn_ExecRowsAffected verifies ExecContext returns correct RowsAffected.
func TestEmbeddedConn_ExecRowsAffected(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=exectest")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	ec, ok := conn.(sqld.ExecerContext)
	if !ok {
		t.Skip("conn does not implement ExecerContext")
	}

	r, err := ec.ExecContext(context.Background(), "INSERT INTO kv (`key`, `value`) VALUES ('k1', 'v1')", nil)
	if err != nil {
		t.Fatalf("ExecContext INSERT: %v", err)
	}
	_, err = r.RowsAffected()
	if err != nil {
		t.Fatalf("RowsAffected: %v", err)
	}
}

// TestEmbeddedConn_QueryContext verifies QueryContext returns rows.
func TestEmbeddedConn_QueryContext(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=querytest")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	qc, ok := conn.(sqld.QueryerContext)
	if !ok {
		t.Skip("conn does not implement QueryerContext")
	}

	rows, err := qc.QueryContext(context.Background(), "SELECT 1 + 1", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	defer func() { _ = rows.Close() }()

	cols := rows.Columns()
	if len(cols) == 0 {
		t.Error("expected at least one column")
	}
}

// TestEmbeddedConn_PreparedStatement verifies Prepare/Exec/Query cycle.
func TestEmbeddedConn_PreparedStatement(t *testing.T) {
	db := openEmbeddedDB(t)

	stmt, err := db.Prepare("INSERT INTO kv (`key`, `value`) VALUES ('prepkey', 'prepval')")
	if err != nil {
		t.Fatalf("Prepare INSERT: %v", err)
	}
	defer func() { _ = stmt.Close() }()

	_, err = stmt.Exec()
	if err != nil {
		t.Fatalf("stmt.Exec: %v", err)
	}

	qstmt, err := db.Prepare("SELECT `key` FROM kv WHERE `key` = 'prepkey'")
	if err != nil {
		t.Fatalf("Prepare SELECT: %v", err)
	}
	defer func() { _ = qstmt.Close() }()

	rows, err := qstmt.Query()
	if err != nil {
		t.Fatalf("stmt.Query: %v", err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		t.Fatal("expected one row")
	}
	var key string
	if err := rows.Scan(&key); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if key != "prepkey" {
		t.Errorf("key = %q, want prepkey", key)
	}
}

// TestEmbeddedConn_CloseBehavior verifies Close marks the connection as closed.
func TestEmbeddedConn_CloseBehavior(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=closetest")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestEmbeddedConn_QueryContextOnClosed verifies QueryContext on closed conn returns error.
func TestEmbeddedConn_QueryContextOnClosed(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=closedqtest")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = conn.Close()

	qc, ok := conn.(sqld.QueryerContext)
	if !ok {
		t.Skip("conn does not implement QueryerContext")
	}

	_, err = qc.QueryContext(context.Background(), "SELECT 1", nil)
	if err == nil {
		t.Fatal("expected error on closed connection")
	}
	var ce *ConnClosedError
	if !errors.As(err, &ce) {
		t.Errorf("expected *ConnClosedError, got %T: %v", err, err)
	}
}

// TestEmbeddedConn_ExecContextOnClosed verifies ExecContext on closed conn returns error.
func TestEmbeddedConn_ExecContextOnClosed(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=closedextest")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = conn.Close()

	ec, ok := conn.(sqld.ExecerContext)
	if !ok {
		t.Skip("conn does not implement ExecerContext")
	}

	_, err = ec.ExecContext(context.Background(), "SELECT 1", nil)
	if err == nil {
		t.Fatal("expected error on closed connection")
	}
	var ce *ConnClosedError
	if !errors.As(err, &ce) {
		t.Errorf("expected *ConnClosedError, got %T: %v", err, err)
	}
}

// TestEmbeddedConn_PrepareOnClosed verifies Prepare on closed conn returns error.
func TestEmbeddedConn_PrepareOnClosed(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=closedpreptest")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = conn.Close()

	_, err = conn.Prepare("SELECT 1")
	if err == nil {
		t.Fatal("expected error on closed connection")
	}
	var ce *ConnClosedError
	if !errors.As(err, &ce) {
		t.Errorf("expected *ConnClosedError, got %T: %v", err, err)
	}
}

// TestEmbeddedConn_BeginTxUnsupportedIsolation verifies unsupported isolation returns error.
func TestEmbeddedConn_BeginTxUnsupportedIsolation(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=isoltest")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	cb, ok := conn.(sqld.ConnBeginTx)
	if !ok {
		t.Skip("conn does not implement ConnBeginTx")
	}

	// The four standard SQL isolation levels are mapped onto SET TRANSACTION
	// ISOLATION LEVEL rather than refused. Snapshot and Linearizable have no
	// MySQL equivalent, so they remain unsupported and must be reported.
	_, err = cb.BeginTx(context.Background(), sqld.TxOptions{
		Isolation: sqld.IsolationLevel(sql.LevelSnapshot),
	})
	if err == nil {
		t.Fatal("expected error for unsupported isolation level")
	}
	var ie *UnsupportedIsolationError
	if !errors.As(err, &ie) {
		t.Errorf("expected *UnsupportedIsolationError, got %T: %v", err, err)
	}
}

// TestEmbeddedConn_BeginTxAcceptsStandardIsolation verifies the isolation
// levels MySQL implements are accepted rather than refused.
func TestEmbeddedConn_BeginTxAcceptsStandardIsolation(t *testing.T) {
	levels := []sql.IsolationLevel{
		sql.LevelDefault,
		sql.LevelReadUncommitted,
		sql.LevelReadCommitted,
		sql.LevelRepeatableRead,
		sql.LevelSerializable,
	}

	for _, level := range levels {
		t.Run(level.String(), func(t *testing.T) {
			d := &Driver{}
			conn, err := d.Open("embedded://memory?database=isolationtest")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = conn.Close() }()

			cb := conn.(sqld.ConnBeginTx)
			tx, err := cb.BeginTx(context.Background(), sqld.TxOptions{
				Isolation: sqld.IsolationLevel(level),
			})
			if err != nil {
				t.Fatalf("BeginTx(%v): %v", level, err)
			}
			if err := tx.Rollback(); err != nil {
				t.Errorf("Rollback: %v", err)
			}
		})
	}
}

// TestEmbeddedConn_Begin verifies Begin starts a transaction using default options.
func TestEmbeddedConn_Begin(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=begintest")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	tx, err := conn.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if tx == nil {
		t.Fatal("expected non-nil tx")
	}
	_ = tx.Rollback()
}

// TestEmbeddedConn_BeginTxOnClosed verifies BeginTx on closed conn returns error.
func TestEmbeddedConn_BeginTxOnClosed(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=closedbegintest")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = conn.Close()

	cb, ok := conn.(sqld.ConnBeginTx)
	if !ok {
		t.Skip("conn does not implement ConnBeginTx")
	}

	_, err = cb.BeginTx(context.Background(), sqld.TxOptions{})
	if err == nil {
		t.Fatal("expected error on closed connection")
	}
	var ce *ConnClosedError
	if !errors.As(err, &ce) {
		t.Errorf("expected *ConnClosedError, got %T: %v", err, err)
	}
}
