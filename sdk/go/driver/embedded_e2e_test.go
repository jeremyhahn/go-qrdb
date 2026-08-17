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
	"errors"
	"strings"
	"testing"
	"time"
)

// openTestDB opens an in-memory embedded database for end-to-end assertions.
// Each caller gets its own database name so tests do not share state.
func openTestDB(t *testing.T, name string) *sql.DB {
	t.Helper()

	db, err := sql.Open(driverName, "embedded://memory?database="+name)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestEmbedded_BoundArgumentsAreNotInterpolated is the security-critical
// assertion of the binding rewrite.
//
// A value containing SQL metacharacters must be stored and returned verbatim.
// If arguments were spliced into the statement text, the quote would terminate
// the literal and the trailing SQL would be parsed as code — which is exactly
// what the previous substituteArgs implementation risked on any escaping slip.
func TestEmbedded_BoundArgumentsAreNotInterpolated(t *testing.T) {
	db := openTestDB(t, "bindingsafety")

	const hostile = `'; DROP TABLE kv; --`

	if _, err := db.Exec("INSERT INTO kv (`key`, value) VALUES (?, ?)", "attack", hostile); err != nil {
		t.Fatalf("Exec insert: %v", err)
	}

	var got string
	if err := db.QueryRow("SELECT value FROM kv WHERE `key` = ?", "attack").Scan(&got); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if got != hostile {
		t.Errorf("value = %q, want %q", got, hostile)
	}

	// The table must still exist: the payload was data, never statement text.
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM kv").Scan(&n); err != nil {
		t.Fatalf("table no longer queryable, payload was executed: %v", err)
	}
}

// TestEmbedded_QuotesAndBackslashesRoundTrip verifies argument values survive
// intact regardless of the metacharacters they contain.
func TestEmbedded_QuotesAndBackslashesRoundTrip(t *testing.T) {
	db := openTestDB(t, "bindingroundtrip")

	values := []string{
		`O'Brien`,
		`back\slash`,
		`quote"double`,
		`tick` + "`" + `mark`,
		`newline` + "\n" + `embedded`,
		`percent%underscore_`,
		``,
	}

	for i, want := range values {
		key := "k" + strings.Repeat("x", i)
		if _, err := db.Exec("INSERT INTO kv (`key`, value) VALUES (?, ?)", key, want); err != nil {
			t.Fatalf("Exec insert %q: %v", want, err)
		}

		var got string
		if err := db.QueryRow("SELECT value FROM kv WHERE `key` = ?", key).Scan(&got); err != nil {
			t.Fatalf("QueryRow %q: %v", want, err)
		}
		if got != want {
			t.Errorf("value = %q, want %q", got, want)
		}
	}
}

// TestEmbedded_ContextCancellationPropagates verifies the caller's context
// reaches the engine. It previously did not: every statement built a fresh
// empty context, so a cancelled query ran to completion.
func TestEmbedded_ContextCancellationPropagates(t *testing.T) {
	db := openTestDB(t, "ctxcancel")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := db.ExecContext(ctx, "INSERT INTO kv (`key`, value) VALUES (?, ?)", "k", "v")
	if err == nil {
		t.Fatal("expected an error from a cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// TestEmbedded_SessionPersistsAcrossStatements verifies a connection keeps one
// engine session, so state set by one statement is visible to the next.
//
// This is what makes transactions possible at all: go-mysql-server stores the
// active transaction on the session, so a per-statement session would discard
// it between calls.
func TestEmbedded_SessionPersistsAcrossStatements(t *testing.T) {
	db := openTestDB(t, "sessionpersist")

	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(context.Background(), "SET @marker = 42"); err != nil {
		t.Fatalf("SET: %v", err)
	}

	var got int64
	if err := conn.QueryRowContext(context.Background(), "SELECT @marker").Scan(&got); err != nil {
		t.Fatalf("SELECT @marker: %v", err)
	}
	if got != 42 {
		t.Errorf("@marker = %d, want 42 (session did not persist across statements)", got)
	}
}

// TestEmbedded_RowsAffectedIsExact verifies RowsAffected reports the number of
// rows the statement changed.
//
// The previous implementation counted iterator rows, which yields 1 for every
// DML statement because the engine emits a single OkResult row regardless of
// how many rows were touched.
func TestEmbedded_RowsAffectedIsExact(t *testing.T) {
	db := openTestDB(t, "rowsaffected")

	for _, key := range []string{"a", "b", "c"} {
		if _, err := db.Exec("INSERT INTO kv (`key`, value) VALUES (?, ?)", key, "v"); err != nil {
			t.Fatalf("Exec insert: %v", err)
		}
	}

	res, err := db.Exec("UPDATE kv SET value = ? WHERE `key` IN (?, ?)", "updated", "a", "b")
	if err != nil {
		t.Fatalf("Exec update: %v", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("RowsAffected: %v", err)
	}
	if affected != 2 {
		t.Errorf("RowsAffected = %d, want 2", affected)
	}
}

// TestEmbedded_TypedRoundTrip verifies bound values keep their types through a
// full write/read cycle rather than being flattened to text.
func TestEmbedded_TypedRoundTrip(t *testing.T) {
	db := openTestDB(t, "typedroundtrip")

	binary := []byte{0x00, 0x01, 0xFE, 0xFF}
	if _, err := db.Exec("INSERT INTO kv (`key`, value) VALUES (?, ?)", "bin", binary); err != nil {
		t.Fatalf("Exec insert: %v", err)
	}

	var got []byte
	if err := db.QueryRow("SELECT value FROM kv WHERE `key` = ?", "bin").Scan(&got); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if string(got) != string(binary) {
		t.Errorf("value = %v, want %v", got, binary)
	}
}

// TestEmbedded_NullBindsAsNull verifies a nil argument binds as SQL NULL
// rather than as the text "NULL" or an empty string.
func TestEmbedded_NullBindsAsNull(t *testing.T) {
	db := openTestDB(t, "nullbinding")

	var isNull bool
	if err := db.QueryRow("SELECT ? IS NULL", nil).Scan(&isNull); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if !isNull {
		t.Error("bound nil is not SQL NULL")
	}

	// A non-nil argument must not be NULL, so the check above is not
	// trivially satisfied by everything binding as NULL.
	if err := db.QueryRow("SELECT ? IS NULL", "value").Scan(&isNull); err != nil {
		t.Fatalf("QueryRow non-nil: %v", err)
	}
	if isNull {
		t.Error("bound non-nil value reported as SQL NULL")
	}
}

// TestEmbedded_NullRejectedByNotNullColumn verifies a bound nil reaches the
// engine as a real NULL and is refused by a NOT NULL column.
//
// This is the negative proof that nil is no longer stringified: the previous
// interpolation produced the four characters NULL, which a NOT NULL VARCHAR
// column accepts as an ordinary value, silently storing the wrong data.
func TestEmbedded_NullRejectedByNotNullColumn(t *testing.T) {
	db := openTestDB(t, "nullnotnull")

	_, err := db.Exec("INSERT INTO kv (`key`, value) VALUES (?, ?)", "n", nil)
	if err == nil {
		t.Fatal("expected a NOT NULL violation, got nil error")
	}

	var stateErr *SQLStateError
	if !errors.As(err, &stateErr) {
		t.Fatalf("error = %v (%T), want *SQLStateError", err, err)
	}
	// 1048 is MySQL's ER_BAD_NULL_ERROR, the same code go-sql-driver/mysql
	// surfaces for this over the wire protocol.
	if stateErr.Number != 1048 {
		t.Errorf("error number = %d, want 1048", stateErr.Number)
	}
}

// TestEmbedded_PingAndPoolReuse verifies Ping works and that a connection
// returned to the pool is reusable, exercising ResetSession.
func TestEmbedded_PingAndPoolReuse(t *testing.T) {
	db := openTestDB(t, "pingreuse")
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	for i := 0; i < 3; i++ {
		if _, err := db.Exec("INSERT INTO kv (`key`, value) VALUES (?, ?)", "reuse", "v"); err != nil {
			t.Fatalf("Exec on reused connection: %v", err)
		}
	}

	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("PingContext after reuse: %v", err)
	}
}

// TestEmbedded_PreparedStatementReuse verifies a prepared statement can be
// executed repeatedly with different arguments.
func TestEmbedded_PreparedStatementReuse(t *testing.T) {
	db := openTestDB(t, "preparedreuse")

	stmt, err := db.Prepare("INSERT INTO kv (`key`, value) VALUES (?, ?)")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, key := range []string{"p1", "p2", "p3"} {
		if _, err := stmt.Exec(key, "v"); err != nil {
			t.Fatalf("stmt.Exec(%q): %v", key, err)
		}
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM kv WHERE `key` LIKE ?", "p%").Scan(&n); err != nil {
		t.Fatalf("QueryRow count: %v", err)
	}
	if n != 3 {
		t.Errorf("count = %d, want 3", n)
	}
}

// TestEmbedded_TimeoutIsHonoured verifies a deadline reaches the engine.
func TestEmbedded_TimeoutIsHonoured(t *testing.T) {
	db := openTestDB(t, "timeouthonoured")

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)

	_, err := db.ExecContext(ctx, "INSERT INTO kv (`key`, value) VALUES (?, ?)", "k", "v")
	if err == nil {
		t.Fatal("expected an error from an expired deadline, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
}
