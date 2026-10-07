// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build qrdb_mysql

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"testing"
	"time"

	vtmysql "github.com/dolthub/vitess/go/mysql"
	gomysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/rowlock"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
)

// idleTimeoutFixture starts a MySQL listener bounded by wait/write over a
// real in-memory store (row locks need one; see sqltest.NewLockingHarness)
// with an `accounts` table, and returns a pooled *sql.DB plus a raw
// cursorClient connected to the same server for the binary protocol a
// server-side cursor needs.
func idleTimeoutFixture(t *testing.T, wait, write time.Duration) (*sql.DB, *cursorClient, bool) {
	t.Helper()
	h, closeFn, err := sqltest.NewLockingHarness()
	require.NoError(t, err)
	t.Cleanup(closeFn)
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithDatabaseName("qrdb"),
		WithServiceRegistry(h.Registry),
		WithWaitTimeout(wait),
		WithWriteTimeout(write),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Stop)

	cfg := gomysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = srv.Addr()
	cfg.User = "qrdb"
	cfg.DBName = "qrdb"
	cfg.AllowNativePasswords = true
	cfg.Timeout = 30 * time.Second
	db, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("CREATE TABLE accounts (id BIGINT PRIMARY KEY, balance BIGINT NOT NULL)")
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO accounts VALUES (1, 100)")
	require.NoError(t, err)

	host, portStr, err := net.SplitHostPort(srv.Addr())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	vc, err := vtmysql.Connect(ctx, &vtmysql.ConnParams{Host: host, Port: port, Uname: "qrdb", DbName: "qrdb"})
	require.NoError(t, err)
	t.Cleanup(vc.Close)
	return db, &cursorClient{conn: vc.Conn}, vc.Capabilities&vtmysql.CapabilityClientDeprecateEOF != 0
}

// query sends a COM_QUERY expected to answer with a plain OK packet, such as
// BEGIN, over the raw binary-protocol connection a cursorClient wraps.
func (c *cursorClient) query(t *testing.T, sql string) {
	t.Helper()
	require.NoError(t, c.write(append([]byte{0x03}, sql...))) // COM_QUERY
	c.mustRead(t)
}

// TestWaitTimeout_ClosesIdleConnection: a connection that sends no command for
// longer than sql.wait_timeout is closed by the server, independent of
// sql.query_timeout (which bounds a running statement, not an idle one). A
// query run before the bound succeeds normally.
func TestWaitTimeout_ClosesIdleConnection(t *testing.T) {
	const wait = 300 * time.Millisecond
	db, _, _ := idleTimeoutFixture(t, wait, 0)
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	var one int
	require.NoError(t, conn.QueryRowContext(context.Background(), "SELECT 1").Scan(&one),
		"a query issued before the idle bound")

	time.Sleep(wait + 2*time.Second)

	err = conn.QueryRowContext(context.Background(), "SELECT 1").Scan(&one)
	require.Error(t, err, "a query on a connection idle past wait_timeout")
}

// TestWaitTimeout_ActiveConnectionUnaffected: a connection that keeps issuing
// commands more often than sql.wait_timeout is never closed, even once the
// cumulative time since it was opened exceeds the bound.
func TestWaitTimeout_ActiveConnectionUnaffected(t *testing.T) {
	const wait = 300 * time.Millisecond
	db, _, _ := idleTimeoutFixture(t, wait, 0)
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	deadline := time.Now().Add(3 * wait)
	for time.Now().Before(deadline) {
		var one int
		require.NoError(t, conn.QueryRowContext(context.Background(), "SELECT 1").Scan(&one))
		time.Sleep(wait / 4)
	}
}

// TestWaitTimeout_ClosesIdleCursorAndReleasesItsLock: a connection that opens
// a server-side cursor (COM_STMT_EXECUTE with the cursor flag) on a locking
// SELECT and then never issues COM_STMT_FETCH is idle exactly like any other
// connection with no pending command, so sql.wait_timeout closes it and
// releases what its statement held -- demonstrated here by the row lock the
// FOR UPDATE cursor took, which another connection cannot acquire (NOWAIT)
// until the idle cursor's connection is gone. sql.query_timeout does not
// reach this case: the cursor is served entirely inside vitess/go-mysql-server
// without ever reaching the statement gate query_timeout bounds.
func TestWaitTimeout_ClosesIdleCursorAndReleasesItsLock(t *testing.T) {
	const wait = 300 * time.Millisecond
	db, c, deprecateEOF := idleTimeoutFixture(t, wait, 0)
	other := pinned(t, db)

	c.query(t, "BEGIN")
	stmt := c.openCursor(t, "SELECT id, balance FROM accounts WHERE id = 1 FOR UPDATE", deprecateEOF)

	requireMySQLError(t, drain(other, "SELECT * FROM accounts WHERE id = 1 FOR UPDATE NOWAIT"), rowlock.SQLCodeLockNowait)

	// Never fetch. Once the connection has been idle past wait_timeout, the
	// lock its open cursor held is released, exactly as it is for an
	// abruptly dropped connection (TestMySQLWire_ClosedConnection_ReleasesItsLocks).
	deadline := time.Now().Add(wait + 10*time.Second)
	for {
		err := drain(other, "SELECT * FROM accounts WHERE id = 1 FOR UPDATE NOWAIT")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the idle cursor's lock was never released: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The connection itself is gone, not just the lock it held.
	_, fetchErr := c.fetch(stmt)
	require.Error(t, fetchErr, "a fetch on the connection the idle bound closed")
	var netErr net.Error
	if errors.As(fetchErr, &netErr) {
		require.False(t, netErr.Timeout(), "the fetch hung instead of failing: %v", fetchErr)
	}
}
