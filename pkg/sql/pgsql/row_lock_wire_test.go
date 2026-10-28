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

//go:build qrdb_pgsql

package pgsql

import (
	"context"
	"testing"
	"time"

	wire "github.com/jeroenrinzema/psql-wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// pgLockConn is one PostgreSQL client connection: the connection context the
// session middleware builds, carried through every statement.
type pgLockConn struct {
	t   *testing.T
	h   *ParseHandler
	ctx context.Context
}

// newPGLockHandler serves a relational database over a real in-memory store
// (sqltest.NewLockingHarness) through the PostgreSQL wire's parse handler.
func newPGLockHandler(t *testing.T) *ParseHandler {
	t.Helper()
	hs, closeFn, err := sqltest.NewLockingHarness()
	require.NoError(t, err)
	t.Cleanup(closeFn)
	prov := provider.NewProvider("qrdb", hs.KVSvc, provider.WithRegistry(hs.Registry))
	eng := engine.New(engine.Config{Provider: prov, Dialect: engine.DialectPostgreSQL})
	t.Cleanup(func() { _ = eng.Close() })
	return NewParseHandler(eng, engine.NewTranslator(), "qrdb")
}

func (h *ParseHandler) lockConn(t *testing.T) *pgLockConn {
	t.Helper()
	ctx, err := newConnectionSessionHandler()(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = closeHandler(nil)(ctx) })
	return &pgLockConn{t: t, h: h, ctx: ctx}
}

func (c *pgLockConn) exec(query string) error {
	_, err := c.h.HandleParse(c.ctx, wire.Query{Query: query, SimpleQuery: true})
	return err
}

func (c *pgLockConn) must(query string) {
	c.t.Helper()
	require.NoError(c.t, c.exec(query), "query %q", query)
}

func (c *pgLockConn) async(query string) <-chan error {
	out := make(chan error, 1)
	go func() { out <- c.exec(query) }()
	return out
}

func setupPGAccounts(t *testing.T, h *ParseHandler) {
	t.Helper()
	admin := h.lockConn(t)
	admin.must("CREATE TABLE accounts (id BIGINT PRIMARY KEY, balance BIGINT NOT NULL)")
	admin.must("INSERT INTO accounts VALUES (1, 100), (2, 200)")
}

func TestPGWire_KeyShareAndNoKeyUpdate_Lock(t *testing.T) {
	h := newPGLockHandler(t)
	setupPGAccounts(t, h)
	c1, c2 := h.lockConn(t), h.lockConn(t)

	c1.must("BEGIN")
	c1.must("SELECT * FROM accounts WHERE id = 1 FOR KEY SHARE")
	c2.must("SELECT * FROM accounts WHERE id = 1 FOR SHARE")
	assert.Equal(t, "55P03", psqlErrorCode(c2.exec("SELECT * FROM accounts WHERE id = 1 FOR NO KEY UPDATE NOWAIT")))
	c1.must("COMMIT")
	c2.must("SELECT * FROM accounts WHERE id = 1 FOR UPDATE NOWAIT")
}

func TestPGWire_LockTimeout_Is55P03(t *testing.T) {
	h := newPGLockHandler(t)
	setupPGAccounts(t, h)
	c1, c2 := h.lockConn(t), h.lockConn(t)

	c1.must("BEGIN")
	c1.must("SELECT * FROM accounts WHERE id = 1 FOR UPDATE")
	c2.must("SET lock_timeout = '500ms'")
	start := time.Now()
	assert.Equal(t, "55P03", psqlErrorCode(c2.exec("SELECT * FROM accounts WHERE id = 1 FOR UPDATE")))
	assert.GreaterOrEqual(t, time.Since(start), time.Second, "a sub-second lock_timeout rounds up to one second")
	c1.must("ROLLBACK")
}

func TestPGWire_SetQrdbConcurrency_ReachesTheEngine(t *testing.T) {
	h := newPGLockHandler(t)
	setupPGAccounts(t, h)
	holder, writer := h.lockConn(t), h.lockConn(t)

	holder.must("BEGIN")
	holder.must("SELECT * FROM accounts WHERE id = 1 FOR UPDATE")

	// Swallowed as a no-op, these two would leave the writer optimistic
	// with the default timeout, and its UPDATE would simply succeed.
	writer.must("SET qrdb_concurrency TO pessimistic")
	writer.must("SET SESSION lock_timeout = 1000")
	assert.Equal(t, "55P03", psqlErrorCode(writer.exec("UPDATE accounts SET balance = 0 WHERE id = 1")))
	holder.must("ROLLBACK")
	writer.must("UPDATE accounts SET balance = 0 WHERE id = 1")
}

func TestPGWire_Deadlock_Is40P01_ThenAborted25P02(t *testing.T) {
	h := newPGLockHandler(t)
	setupPGAccounts(t, h)
	c1, c2 := h.lockConn(t), h.lockConn(t)

	c1.must("BEGIN")
	c1.must("SELECT * FROM accounts WHERE id = 1 FOR UPDATE")
	c2.must("BEGIN")
	c2.must("SELECT * FROM accounts WHERE id = 2 FOR UPDATE")
	waiting := c1.async("SELECT * FROM accounts WHERE id = 2 FOR UPDATE")
	select {
	case err := <-waiting:
		t.Fatalf("c1 returned while it should wait: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	assert.Equal(t, "40P01", psqlErrorCode(c2.exec("SELECT * FROM accounts WHERE id = 1 FOR UPDATE")))
	assert.Equal(t, "25P02", psqlErrorCode(c2.exec("SELECT * FROM accounts WHERE id = 2")))
	c2.must("ROLLBACK")

	select {
	case err := <-waiting:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the survivor never acquired its lock")
	}
	c1.must("COMMIT")
}

func TestPGWire_SkipLocked_Is0A000(t *testing.T) {
	h := newPGLockHandler(t)
	setupPGAccounts(t, h)
	c := h.lockConn(t)
	assert.Equal(t, "0A000", psqlErrorCode(c.exec("SELECT * FROM accounts FOR UPDATE SKIP LOCKED")))
}

func TestPGWire_InvalidLockTimeout_Is22023(t *testing.T) {
	h := newPGLockHandler(t)
	c := h.lockConn(t)
	assert.Equal(t, "22023", psqlErrorCode(c.exec("SET lock_timeout = 'forever'")))
}

func TestRouteSessionSet(t *testing.T) {
	cases := []struct {
		in     string
		routed string
		ok     bool
	}{
		{"SET qrdb_concurrency = 'pessimistic'", "SET qrdb_concurrency = 'pessimistic'", true},
		{"SET qrdb_concurrency TO pessimistic", "SET qrdb_concurrency = 'pessimistic'", true},
		{"set session qrdb_isolation = 'written+reads';", "SET qrdb_isolation = 'written+reads'", true},
		{"SET qrdb_concurrency TO DEFAULT", "SET qrdb_concurrency = DEFAULT", true},
		{"SET lock_timeout = 0", "SET innodb_lock_wait_timeout = 1073741824", true},
		{"SET lock_timeout = '2s'", "SET innodb_lock_wait_timeout = 2", true},
		{"SET lock_timeout TO '1500ms'", "SET innodb_lock_wait_timeout = 2", true},
		{"SET lock_timeout = '1min'", "SET innodb_lock_wait_timeout = 60", true},
		{"SET lock_timeout = 250", "SET innodb_lock_wait_timeout = 1", true},
		{"SET lock_timeout TO DEFAULT", "SET innodb_lock_wait_timeout = DEFAULT", true},
		{"SET search_path TO public", "", false},
		{"SET LOCAL search_path TO public", "", false},
		{"SELECT 1", "", false},
	}
	for _, c := range cases {
		routed, ok, err := routeSessionSet(c.in)
		require.NoError(t, err, c.in)
		assert.Equal(t, c.ok, ok, c.in)
		assert.Equal(t, c.routed, routed, c.in)
	}

	_, ok, err := routeSessionSet("SET LOCAL lock_timeout = '1s'")
	assert.True(t, ok)
	var local *LocalSettingUnsupportedError
	require.ErrorAs(t, err, &local)
	assert.Equal(t, "lock_timeout", local.Name)

	_, ok, err = routeSessionSet("SET lock_timeout = 'soon'")
	assert.True(t, ok)
	var invalid *InvalidSettingError
	require.ErrorAs(t, err, &invalid)
	assert.Contains(t, invalid.Error(), "lock_timeout")
}
