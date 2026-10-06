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
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// newEmbeddedTestConn opens a raw embedded connection for driver-level tests.
func newEmbeddedTestConn(t *testing.T, database string) *embeddedConn {
	t.Helper()

	cfg, err := ParseDSN("embedded://memory?database=" + database)
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	connector := NewEmbeddedConnector(cfg)
	t.Cleanup(func() { _ = connector.Close() })

	conn, err := connector.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return conn.(*embeddedConn)
}

// TestEmbeddedConnector_WithIdentity verifies the option reaches the session
// created for each connection, which is what scopes SQL to a tenant.
func TestEmbeddedConnector_WithIdentity(t *testing.T) {
	cfg, err := ParseDSN("embedded://memory?database=identityconn")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}

	connector := NewEmbeddedConnector(cfg, WithIdentity(testIdentity("alice", "acme")))
	defer func() { _ = connector.Close() }()

	conn, err := connector.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if got := conn.(*embeddedConn).session.TenantID(); got != "acme" {
		t.Errorf("session tenant = %q, want acme", got)
	}
}

// TestEmbeddedConnector_DSNTenant verifies the DSN tenant parameter is used
// when no identity option is supplied.
func TestEmbeddedConnector_DSNTenant(t *testing.T) {
	cfg, err := ParseDSN("embedded://memory?database=dsntenant&tenant=acme&user=bob")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	if cfg.Tenant != "acme" || cfg.User != "bob" {
		t.Fatalf("DSN parsed tenant=%q user=%q, want acme/bob", cfg.Tenant, cfg.User)
	}

	connector := NewEmbeddedConnector(cfg)
	defer func() { _ = connector.Close() }()

	conn, err := connector.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if got := conn.(*embeddedConn).session.TenantID(); got != "acme" {
		t.Errorf("session tenant = %q, want acme", got)
	}
}

// TestEmbeddedConnector_Location verifies location resolution: the option wins
// over the DSN, the DSN is parsed when present, and an unknown zone is an
// error rather than a silent fallback to UTC.
func TestEmbeddedConnector_Location(t *testing.T) {
	t.Run("defaults to UTC", func(t *testing.T) {
		cfg, _ := ParseDSN("embedded://memory?database=loc1")
		loc, err := NewEmbeddedConnector(cfg).location()
		if err != nil {
			t.Fatalf("location: %v", err)
		}
		if loc != time.UTC {
			t.Errorf("loc = %v, want UTC", loc)
		}
	})

	t.Run("option wins", func(t *testing.T) {
		want := time.FixedZone("opt", 3600)
		cfg, _ := ParseDSN("embedded://memory?database=loc2&loc=America/New_York")
		loc, err := NewEmbeddedConnector(cfg, WithLocation(want)).location()
		if err != nil {
			t.Fatalf("location: %v", err)
		}
		if loc != want {
			t.Errorf("loc = %v, want the option's zone", loc)
		}
	})

	t.Run("parses DSN zone", func(t *testing.T) {
		cfg, _ := ParseDSN("embedded://memory?database=loc3&loc=America/New_York")
		loc, err := NewEmbeddedConnector(cfg).location()
		if err != nil {
			t.Fatalf("location: %v", err)
		}
		if loc.String() != "America/New_York" {
			t.Errorf("loc = %v, want America/New_York", loc)
		}
	})

	t.Run("rejects unknown zone", func(t *testing.T) {
		cfg, _ := ParseDSN("embedded://memory?database=loc4&loc=Not/AZone")
		_, err := NewEmbeddedConnector(cfg).location()

		var parseErr *DSNParseError
		if !errors.As(err, &parseErr) {
			t.Fatalf("error = %v, want *DSNParseError", err)
		}
	})
}

// TestEmbeddedConn_CheckNamedValue verifies the checker widens numeric types
// and rejects named parameters and unbindable values.
func TestEmbeddedConn_CheckNamedValue(t *testing.T) {
	conn := newEmbeddedTestConn(t, "checknamedvalue")

	t.Run("widens int", func(t *testing.T) {
		nv := &sqld.NamedValue{Ordinal: 1, Value: int32(7)}
		if err := conn.CheckNamedValue(nv); err != nil {
			t.Fatalf("CheckNamedValue: %v", err)
		}
		if nv.Value != int64(7) {
			t.Errorf("Value = %#v, want int64(7)", nv.Value)
		}
	})

	t.Run("rejects named", func(t *testing.T) {
		nv := &sqld.NamedValue{Name: "x", Ordinal: 1, Value: 1}
		err := conn.CheckNamedValue(nv)

		var named *NamedParameterUnsupportedError
		if !errors.As(err, &named) {
			t.Errorf("error = %v, want *NamedParameterUnsupportedError", err)
		}
	})

	t.Run("rejects unbindable", func(t *testing.T) {
		nv := &sqld.NamedValue{Ordinal: 1, Value: make(chan int)}
		err := conn.CheckNamedValue(nv)

		var unsupported *UnsupportedArgumentTypeError
		if !errors.As(err, &unsupported) {
			t.Errorf("error = %v, want *UnsupportedArgumentTypeError", err)
		}
	})
}

// TestEmbeddedConn_ClosedRejectsEverything verifies a closed connection
// refuses all work and reports itself invalid so the pool discards it.
func TestEmbeddedConn_ClosedRejectsEverything(t *testing.T) {
	conn := newEmbeddedTestConn(t, "closedconn")

	if !conn.IsValid() {
		t.Fatal("fresh connection reported invalid")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if conn.IsValid() {
		t.Error("closed connection reported valid")
	}

	// Close is idempotent.
	if err := conn.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}

	ctx := context.Background()
	var closedErr *ConnClosedError

	if _, err := conn.PrepareContext(ctx, "SELECT 1"); !errors.As(err, &closedErr) {
		t.Errorf("PrepareContext error = %v, want *ConnClosedError", err)
	}
	if _, err := conn.QueryContext(ctx, "SELECT 1", nil); !errors.As(err, &closedErr) {
		t.Errorf("QueryContext error = %v, want *ConnClosedError", err)
	}
	if _, err := conn.ExecContext(ctx, "SELECT 1", nil); !errors.As(err, &closedErr) {
		t.Errorf("ExecContext error = %v, want *ConnClosedError", err)
	}
	if _, err := conn.BeginTx(ctx, sqld.TxOptions{}); !errors.As(err, &closedErr) {
		t.Errorf("BeginTx error = %v, want *ConnClosedError", err)
	}
	if err := conn.Ping(ctx); !errors.Is(err, sqld.ErrBadConn) {
		t.Errorf("Ping error = %v, want driver.ErrBadConn", err)
	}
	if err := conn.ResetSession(ctx); !errors.Is(err, sqld.ErrBadConn) {
		t.Errorf("ResetSession error = %v, want driver.ErrBadConn", err)
	}
}

// TestEmbeddedConn_ResetSessionRollsBack verifies a transaction left open by a
// previous pool user is rolled back rather than inherited by the next one.
func TestEmbeddedConn_ResetSessionRollsBack(t *testing.T) {
	conn := newEmbeddedTestConn(t, "resetrollback")
	ctx := context.Background()

	if _, err := conn.BeginTx(ctx, sqld.TxOptions{}); err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if !conn.inTx.Load() {
		t.Fatal("connection does not report being in a transaction")
	}

	if err := conn.ResetSession(ctx); err != nil {
		t.Fatalf("ResetSession: %v", err)
	}
	if conn.inTx.Load() {
		t.Error("transaction still open after ResetSession")
	}
}

// TestEmbeddedConn_ResetSessionFailsClosedOnVariableResetFailure verifies
// that a session variable ResetSession cannot restore discards the
// connection rather than returning it to the pool with that variable still
// at whatever the previous caller left it -- the exact leak ResetSession
// exists to close, now closed on the failure path too.
//
// c.baseline is corrupted directly (white-box: this test lives in package
// driver) to a value qrdb_concurrency's enum type refuses, standing in for
// whatever real condition might make the engine's SetSessionVariable call
// fail: ResetSession does not know or care why the restore failed, only
// that it did, and must fail closed either way.
func TestEmbeddedConn_ResetSessionFailsClosedOnVariableResetFailure(t *testing.T) {
	conn := newEmbeddedTestConn(t, "resetfailclosed")
	ctx := context.Background()

	if _, ok := conn.baseline[provider.ConcurrencyVariable]; !ok {
		t.Fatalf("baseline has no entry for %q; the injected failure below would be a no-op", provider.ConcurrencyVariable)
	}
	conn.baseline[provider.ConcurrencyVariable] = "not-a-real-concurrency-value"

	if err := conn.ResetSession(ctx); !errors.Is(err, sqld.ErrBadConn) {
		t.Fatalf("ResetSession error = %v, want driver.ErrBadConn", err)
	}
	if conn.IsValid() {
		t.Error("connection reported valid after a failed session-variable reset")
	}
}

// TestEmbeddedConn_QueryRejectsBadArgument verifies an unbindable argument is
// reported before the statement reaches the engine.
func TestEmbeddedConn_QueryRejectsBadArgument(t *testing.T) {
	conn := newEmbeddedTestConn(t, "badargument")

	args := []sqld.NamedValue{{Ordinal: 1, Value: make(chan int)}}

	var unsupported *UnsupportedArgumentTypeError
	if _, err := conn.QueryContext(context.Background(), "SELECT ?", args); !errors.As(err, &unsupported) {
		t.Errorf("QueryContext error = %v, want *UnsupportedArgumentTypeError", err)
	}
	if _, err := conn.ExecContext(context.Background(), "SELECT ?", args); !errors.As(err, &unsupported) {
		t.Errorf("ExecContext error = %v, want *UnsupportedArgumentTypeError", err)
	}
}

// TestEmbeddedConn_QueryRejectsInvalidSQL verifies a malformed statement is
// refused with the engine's own error kept reachable. go-mysql-server does not
// classify a syntax error with a MySQL number of its own (it is
// ER_UNKNOWN_ERROR, 1105, over the wire as well), so it is not rendered as a
// *mysql.MySQLError: it stays a *QueryExecError naming the statement.
func TestEmbeddedConn_QueryRejectsInvalidSQL(t *testing.T) {
	conn := newEmbeddedTestConn(t, "invalidsql")

	_, err := conn.QueryContext(context.Background(), "THIS IS NOT SQL", nil)
	if err == nil {
		t.Fatal("expected an error for malformed SQL")
	}

	var execErr *QueryExecError
	if !errors.As(err, &execErr) || execErr.Query != "THIS IS NOT SQL" {
		t.Fatalf("error = %v (%T), want a *QueryExecError naming the statement", err, err)
	}
	if _, isMySQL := err.(*gomysql.MySQLError); isMySQL {
		t.Fatal("an unclassified engine error was given a MySQL number it does not have")
	}
}
