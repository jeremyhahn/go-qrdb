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

//go:build qrdb_mysql

package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/rowlock"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// newRowLockWire starts a MySQL wire server over a real in-memory store
// (sqltest.NewLockingHarness) and returns a client pool connected to it.
func newRowLockWire(t *testing.T, opts ...func(*gomysql.Config)) *sql.DB {
	t.Helper()
	h, closeFn, err := sqltest.NewLockingHarness()
	require.NoError(t, err)
	t.Cleanup(closeFn)

	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithDatabaseName("qrdb"),
		WithServiceRegistry(h.Registry),
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
	for _, o := range opts {
		o(cfg)
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("CREATE TABLE accounts (id BIGINT PRIMARY KEY, balance BIGINT NOT NULL)")
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO accounts VALUES (1, 100), (2, 200)")
	require.NoError(t, err)
	return db
}

// pinned returns one dedicated connection: a session.
func pinned(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	c, err := db.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func mustExec(t *testing.T, c *sql.Conn, q string) {
	t.Helper()
	_, err := c.ExecContext(context.Background(), q)
	require.NoError(t, err, q)
}

// drain runs a statement that returns rows and drains them.
func drain(c *sql.Conn, q string) error {
	rows, err := c.QueryContext(context.Background(), q)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() { //nolint:revive // draining
	}
	return rows.Err()
}

func requireMySQLError(t *testing.T, err error, num uint16) {
	t.Helper()
	var myErr *gomysql.MySQLError
	require.True(t, errors.As(err, &myErr), "want MySQL error %d, got %v", num, err)
	require.Equal(t, num, myErr.Number, "error: %v", err)
}

func TestMySQLWire_ForUpdate_NowaitTimeoutAndShare(t *testing.T) {
	db := newRowLockWire(t)
	c1, c2 := pinned(t, db), pinned(t, db)

	mustExec(t, c1, "BEGIN")
	require.NoError(t, drain(c1, "SELECT * FROM accounts WHERE id = 1 FOR UPDATE"))

	requireMySQLError(t, drain(c2, "SELECT * FROM accounts WHERE id = 1 FOR UPDATE NOWAIT"), rowlock.SQLCodeLockNowait)
	requireMySQLError(t, drain(c2, "SELECT * FROM accounts WHERE id = 1 FOR SHARE NOWAIT"), rowlock.SQLCodeLockNowait)

	mustExec(t, c2, "SET innodb_lock_wait_timeout = 1")
	requireMySQLError(t, drain(c2, "SELECT * FROM accounts WHERE id = 1 LOCK IN SHARE MODE"), rowlock.SQLCodeLockWaitTimeout)

	mustExec(t, c1, "COMMIT")
	require.NoError(t, drain(c2, "SELECT * FROM accounts WHERE id = 1 FOR SHARE"))
}

func TestMySQLWire_Deadlock_Is1213(t *testing.T) {
	db := newRowLockWire(t)
	c1, c2 := pinned(t, db), pinned(t, db)

	mustExec(t, c1, "BEGIN")
	require.NoError(t, drain(c1, "SELECT * FROM accounts WHERE id = 1 FOR UPDATE"))
	mustExec(t, c2, "BEGIN")
	require.NoError(t, drain(c2, "SELECT * FROM accounts WHERE id = 2 FOR UPDATE"))

	waiting := make(chan error, 1)
	go func() { waiting <- drain(c1, "SELECT * FROM accounts WHERE id = 2 FOR UPDATE") }()
	select {
	case err := <-waiting:
		t.Fatalf("c1 returned while it should wait: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	requireMySQLError(t, drain(c2, "SELECT * FROM accounts WHERE id = 1 FOR UPDATE"), 1213)
	mustExec(t, c2, "ROLLBACK")
	select {
	case err := <-waiting:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the survivor never acquired its lock")
	}
	mustExec(t, c1, "COMMIT")
}

func TestMySQLWire_SetQrdbConcurrency_And_SkipLocked(t *testing.T) {
	db := newRowLockWire(t)
	c := pinned(t, db)

	mustExec(t, c, "SET qrdb_concurrency = 'pessimistic'")
	var mode string
	require.NoError(t, c.QueryRowContext(context.Background(), "SELECT @@qrdb_concurrency").Scan(&mode))
	assert.Equal(t, "pessimistic", mode)

	requireMySQLError(t, drain(c, "SELECT * FROM accounts FOR UPDATE SKIP LOCKED"), provider.SQLCodeSkipLockedUnsupported)
}

func TestMySQLWire_ClosedConnection_ReleasesItsLocks(t *testing.T) {
	db := newRowLockWire(t)
	holder, other := pinned(t, db), pinned(t, db)

	mustExec(t, holder, "BEGIN")
	require.NoError(t, drain(holder, "SELECT * FROM accounts WHERE id = 1 FOR UPDATE"))
	requireMySQLError(t, drain(other, "SELECT * FROM accounts WHERE id = 1 FOR UPDATE NOWAIT"), rowlock.SQLCodeLockNowait)

	// Drop the holder's connection outright, mid-transaction.
	_ = holder.Raw(func(any) error { return driver.ErrBadConn })

	deadline := time.Now().Add(5 * time.Second)
	for {
		err := drain(other, "SELECT * FROM accounts WHERE id = 1 FOR UPDATE NOWAIT")
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the dropped connection's lock was never released: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Under CLIENT_MULTI_STATEMENTS one COM_QUERY carries several statements.
// Lock intent is read from each statement's own text: a FOR UPDATE that is
// not the first statement still locks, and a FOR UPDATE in one statement
// does not leak into the next.
func TestMySQLWire_MultiStatements_LockIntentPerStatement(t *testing.T) {
	db := newRowLockWire(t, func(c *gomysql.Config) { c.MultiStatements = true })
	c1, c2 := pinned(t, db), pinned(t, db)

	mustExec(t, c1, "BEGIN; SELECT 1; SELECT * FROM accounts WHERE id = 1 FOR UPDATE; SELECT * FROM accounts WHERE id = 2")
	requireMySQLError(t, drain(c2, "SELECT * FROM accounts WHERE id = 1 FOR UPDATE NOWAIT"), rowlock.SQLCodeLockNowait)
	require.NoError(t, drain(c2, "SELECT * FROM accounts WHERE id = 2 FOR UPDATE NOWAIT"),
		"the plain SELECT after the locking one took no lock")
	mustExec(t, c1, "COMMIT")
	mustExec(t, c2, "ROLLBACK")
	require.NoError(t, drain(c2, "SELECT * FROM accounts WHERE id = 1 FOR UPDATE NOWAIT"))
}
