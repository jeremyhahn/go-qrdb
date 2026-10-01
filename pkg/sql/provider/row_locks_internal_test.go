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

package provider

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/rowlock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// lockTestTable resolves name in h's database as a *RelationalTable under a
// session that has started a transaction, as the engine does before every
// statement.
func lockTestTable(t *testing.T, h *lockHarness, c *lockConn, name string) (*RelationalTable, *sqle.Context) {
	t.Helper()
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(c.sess))
	ctx.SetCurrentDatabase("shop")
	_, err := c.sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	db, err := h.prov.Database(ctx, "shop")
	require.NoError(t, err)
	tbl, ok, err := db.(*Database).GetTableInsensitive(ctx, name)
	require.NoError(t, err)
	require.True(t, ok)
	rt, ok := tbl.(*RelationalTable)
	require.True(t, ok, "%T", tbl)
	return rt, ctx
}

func TestLockFailure_MapsEveryLockError(t *testing.T) {
	ctx := sqle.NewEmptyContext()

	ws := newWriteSet(false, IsolationWritten)
	assert.Nil(t, lockFailure(ctx, ws, nil))

	assert.Equal(t, rowlock.SQLCodeLockWaitTimeout, sqlErrorNumber(lockFailure(ctx, ws, &rowlock.LockWaitTimeoutError{})))
	assert.Equal(t, rowlock.SQLCodeLockNowait, sqlErrorNumber(lockFailure(ctx, ws, &rowlock.LockNotAvailableError{})))

	var cross *CrossDatabaseTransactionError
	require.ErrorAs(t, lockFailure(ctx, ws, &rowlock.ScopeMismatchError{Bound: "acme/shop", Requested: "acme/other"}), &cross)
	assert.Equal(t, "shop", cross.Bound)
	assert.Equal(t, "other", cross.Requested)
	assert.Equal(t, "plain", scopeDatabase("plain"))

	// A released owner on a live transaction is passed through as is; on an
	// aborted one it is the abort.
	released := &rowlock.OwnerReleasedError{}
	assert.Same(t, error(released), lockFailure(ctx, ws, released))

	lost := lockFailure(ctx, ws, &rowlock.LockLostError{})
	assert.Equal(t, mysql.ERLockDeadlock, sqlErrorNumber(lost))
	require.Error(t, ws.aborted, "a lost lock rolls the transaction back")
	var aborted *TransactionAbortedError
	require.True(t, service.AsCause(lockFailure(ctx, ws, released), &aborted))

	other := errors.New("transport failure")
	assert.Same(t, other, lockFailure(ctx, ws, other))
}

func TestRowLockErrors_Messages(t *testing.T) {
	cause := &rowlock.DeadlockError{}
	aborted := &TransactionAbortedError{Cause: cause}
	assert.Contains(t, aborted.Error(), "current transaction is aborted")
	assert.Contains(t, aborted.Error(), "deadlock")
	assert.Same(t, error(cause), aborted.Unwrap())
	assert.NotContains(t, (&TransactionAbortedError{}).Error(), "(")
	assert.Contains(t, (&RowLockingUnavailableError{Table: "t"}).Error(), "t")
	assert.Contains(t, (&SkipLockedUnsupportedError{}).Error(), "SKIP LOCKED")
}

func TestToInt64_EveryWidth(t *testing.T) {
	cases := []struct {
		in   any
		want int64
		ok   bool
	}{
		{int64(5), 5, true}, {int(6), 6, true}, {int32(7), 7, true},
		{uint32(8), 8, true}, {uint64(9), 9, true},
		{uint64(1 << 40), MaxLockWaitTimeoutSeconds, true},
		{"10", 0, false},
	}
	for _, c := range cases {
		got, ok := toInt64(c.in)
		assert.Equal(t, c.ok, ok, "%T", c.in)
		assert.Equal(t, c.want, got, "%T", c.in)
	}
}

func TestSessionSettings_FallBackToDefaults(t *testing.T) {
	// A session built over a variable registry without the QRDB variables
	// (an empty map) answers the defaults.
	sess := NewSession(sqle.NewBaseSession(), nil)
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess))
	require.NoError(t, sess.SetSessionVariable(ctx, LockWaitTimeoutVariable, int64(7)))
	assert.Equal(t, 7*time.Second, sess.lockWaitTimeout(ctx))
	assert.Equal(t, ConcurrencyDatabase, sess.concurrencyChoice(ctx))
	require.NoError(t, sess.CommandBegin())
	sess.CommandEnd()
}

func TestManagerSQLConfig_ReadsTheCatalog(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	_, err := mgr.Create(database.DatabaseConfig{
		Name: "shop", Engine: database.EngineKV, Backend: database.BackendMemory,
		SQL: &database.SQLConfig{Concurrency: database.ConcurrencyPessimistic},
	})
	require.NoError(t, err)

	resolve := managerSQLConfig(mgr)
	assert.Equal(t, database.ConcurrencyPessimistic, resolve("", "shop").EffectiveConcurrency())
	assert.Nil(t, resolve("", "missing"))

	p := NewManagerProvider(mgr, nil)
	assert.NotNil(t, p.sqlConfigOf)
	db := p.newDatabase("shop", database.EngineKV).(*Database)
	assert.Equal(t, database.ConcurrencyPessimistic, db.concurrency(""))
	assert.Equal(t, database.ConcurrencyOptimistic, (&Database{name: "x"}).concurrency(""))
}

func TestForUpdate_LeaseOrWatchUnavailable_IsReported(t *testing.T) {
	for name, set := range map[string]func(r *fakeRelationalRegistry){
		"lease": func(r *fakeRelationalRegistry) { r.leaseErr = &service.TenantNotFoundError{TenantID: "x"} },
		"watch": func(r *fakeRelationalRegistry) { r.watchErr = &service.TenantNotFoundError{TenantID: "x"} },
	} {
		t.Run(name, func(t *testing.T) {
			h := newDDLHarness()
			reg := h.registry.(*fakeRelationalRegistry)
			set(reg)
			conn := newTxConn(t, h.newDDLProvider("shop"), "shop")
			conn.mustExec(t, accountsDDL)
			conn.mustExec(t, "INSERT INTO accounts VALUES (1, 1)")

			q := "SELECT * FROM accounts WHERE id = 1 FOR UPDATE"
			ctx := sqle.NewContext(t.Context(), sqle.WithSession(conn.sess), sqle.WithQuery(q))
			ctx.SetCurrentDatabase("shop")
			_, iter, _, err := conn.eng.Query(ctx, q)
			if err == nil {
				_, err = sqle.RowIterToRows(ctx, iter)
			}
			var notFound *service.TenantNotFoundError
			require.ErrorAs(t, err, &notFound)
		})
	}
}

func TestRowGuardFailed_ComparesTheLiveRow(t *testing.T) {
	h := newLockHarness(t)
	setupAccounts(t, h)
	tbl, ctx := lockTestTable(t, h, h.conn(t), "accounts")
	key := tbl.gen().dataPrefix + "missing"

	moved, err := tbl.rowGuardFailed(ctx, absenceCompare(key))
	require.NoError(t, err)
	assert.False(t, moved, "an absent row that was absent has not moved")

	moved, err = tbl.rowGuardFailed(ctx, modRevisionCompare(key, 5))
	require.NoError(t, err)
	assert.True(t, moved, "a present row that is gone has moved")

	_, ctx2 := lockTestTable(t, h, h.conn(t), "accounts")
	keys, _, err := tbl.primaryKeysFromRanges(ctx2, nil)
	require.NoError(t, err)
	_ = keys
	kvs, _, _, err := h.mvcc.Range(ctx, []byte(tbl.gen().dataPrefix), prefixEnd([]byte(tbl.gen().dataPrefix)))
	require.NoError(t, err)
	require.NotEmpty(t, kvs)
	live := string(kvs[0].Key)

	moved, err = tbl.rowGuardFailed(ctx, absenceCompare(live))
	require.NoError(t, err)
	assert.True(t, moved, "a row that appeared has moved")
	moved, err = tbl.rowGuardFailed(ctx, modRevisionCompare(live, kvs[0].ModRevision))
	require.NoError(t, err)
	assert.False(t, moved)
	moved, err = tbl.rowGuardFailed(ctx, modRevisionCompare(live, kvs[0].ModRevision+100))
	require.NoError(t, err)
	assert.True(t, moved)
}

func TestLockRowForUpdate_RefusesARowThatChangedSinceItWasRead(t *testing.T) {
	h := newLockHarness(t)
	setupAccounts(t, h)
	c := h.conn(t)
	c.must("SET qrdb_concurrency = 'pessimistic'")
	tbl, ctx := lockTestTable(t, h, c, "accounts")
	t.Cleanup(func() { _ = c.sess.Rollback(ctx, nil) })
	key := tbl.gen().dataPrefix

	kvs, _, _, err := h.mvcc.Range(ctx, []byte(key), prefixEnd([]byte(key)))
	require.NoError(t, err)
	require.Len(t, kvs, 2)
	rowKey := string(kvs[0].Key)
	stored, err := tbl.decodeRow(rowKey[len(key):], kvs[0].Value)
	require.NoError(t, err)

	// A row read elsewhere (a cascade) that still matches is accepted and
	// is now held.
	require.NoError(t, tbl.lockMatchedRow(ctx, rowKey, stored))
	_, ws := lockState(ctx)
	assert.True(t, ws.lockOwner().Holds(rowKey, rowlock.ModeExclusive))
	require.NoError(t, tbl.lockMatchedRow(ctx, rowKey, stored), "a held row is not re-checked")

	// One computed from a different version is refused, retryably.
	otherKey := string(kvs[1].Key)
	stale := sqle.Row{stored[0], int64(-1)}
	err = tbl.lockMatchedRow(ctx, otherKey, stale)
	assert.Equal(t, mysql.ERLockDeadlock, sqlErrorNumber(err))
	var conflict *TransactionConflictError
	assert.True(t, service.AsCause(err, &conflict))
}

func TestCurrentRead_WithoutMVCC_IsRefused(t *testing.T) {
	ws := newWriteSet(false, IsolationWritten)
	tbl := &RelationalTable{def: &service.TableDef{Name: "t"}}
	var unavailable *RelationalMVCCUnavailableError
	_, err := tbl.currentRead(sqle.NewEmptyContext(), ws, "k")
	require.ErrorAs(t, err, &unavailable)

	ws.recordCurrent("k", currentRead{found: true, modRevision: 3})
	ws.recordCurrent("k", currentRead{found: false})
	c, ok := ws.currentState("k")
	require.True(t, ok)
	assert.True(t, c.found, "the first current read wins")
	got, err := tbl.currentRead(sqle.NewEmptyContext(), ws, "k")
	require.NoError(t, err)
	assert.Equal(t, int64(3), got.modRevision)
}

func TestLockKeys_WithoutLockTableOrWriteSet(t *testing.T) {
	tbl := &RelationalTable{def: &service.TableDef{Name: "t"}}
	sess := NewSession(sqle.NewBaseSession(), nil)
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess))
	require.NoError(t, tbl.lockKeys(ctx, []string{"k"}, lockRequest{mode: rowlock.ModeShared}), "no transaction, nothing to own the lock")

	_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	var unavailable *RowLockingUnavailableError
	require.ErrorAs(t, tbl.lockKeys(ctx, []string{"k"}, lockRequest{mode: rowlock.ModeShared}), &unavailable)
}

func TestForUpdate_PrimaryKeyRange_LocksTheRowsItSelects(t *testing.T) {
	h := newLockHarness(t)
	admin := setupAccounts(t, h)
	admin.must("INSERT INTO accounts VALUES (3, 300)")
	s1, s2 := h.conn(t), h.conn(t)

	s1.must("BEGIN")
	rows := s1.must("SELECT id FROM accounts WHERE id >= 2 FOR UPDATE")
	require.Len(t, rows, 2)
	_, err := s2.exec("SELECT * FROM accounts WHERE id = 3 FOR UPDATE NOWAIT")
	requireSQLError(t, err, rowlock.SQLCodeLockNowait)
	s2.must("SELECT * FROM accounts WHERE id = 1 FOR UPDATE NOWAIT")
	s1.must("COMMIT")
}

func TestForUpdate_FullScan_SeesTheTransactionsOwnRows(t *testing.T) {
	h := newLockHarness(t)
	setupAccounts(t, h)
	s1 := h.conn(t)
	s1.must("BEGIN")
	s1.must("INSERT INTO accounts VALUES (9, 900)")
	s1.must("DELETE FROM accounts WHERE id = 1")
	rows := s1.must("SELECT id FROM accounts FOR UPDATE")
	ids := map[any]bool{}
	for _, r := range rows {
		ids[r[0]] = true
	}
	assert.True(t, ids[int64(9)], "the transaction's own insert is read and locked")
	assert.False(t, ids[int64(1)], "the transaction's own delete is honoured")
	s1.must("ROLLBACK")
}

func TestDeadlockVictim_DDLEndsTheAbortedTransaction(t *testing.T) {
	h := newLockHarness(t)
	setupAccounts(t, h)
	s1, s2 := h.conn(t), h.conn(t)
	s1.must("BEGIN")
	s1.must("SELECT * FROM accounts WHERE id = 1 FOR UPDATE")
	s2.must("BEGIN")
	s2.must("SELECT * FROM accounts WHERE id = 2 FOR UPDATE")
	waiting := s1.async("SELECT * FROM accounts WHERE id = 2 FOR UPDATE")
	blockedFor(t, waiting, 150*time.Millisecond, "s1")
	_, err := s2.exec("SELECT * FROM accounts WHERE id = 1 FOR UPDATE")
	requireSQLError(t, err, mysql.ERLockDeadlock)

	s2.must("CREATE TABLE after_victim (id BIGINT PRIMARY KEY)")
	s2.must("SELECT * FROM accounts WHERE id = 2")
	require.NoError(t, awaitResult(t, waiting, "s1"))
	s1.must("COMMIT")
}

func TestPessimistic_ForeignKeyCascade_LocksTheChildRow(t *testing.T) {
	h := newLockHarness(t)
	admin := h.conn(t)
	admin.must("CREATE TABLE parents (id BIGINT PRIMARY KEY)")
	admin.must("CREATE TABLE children (id BIGINT PRIMARY KEY, parent_id BIGINT, " +
		"CONSTRAINT fk_parent FOREIGN KEY (parent_id) REFERENCES parents (id) ON DELETE SET NULL)")
	admin.must("INSERT INTO parents VALUES (1)")
	admin.must("INSERT INTO children VALUES (10, 1)")

	holder, deleter := h.conn(t), h.conn(t)
	holder.must("BEGIN")
	holder.must("SELECT * FROM children WHERE id = 10 FOR UPDATE")

	deleter.must("SET qrdb_concurrency = 'pessimistic'")
	deleter.must("SET innodb_lock_wait_timeout = 1")
	deleter.must("BEGIN")
	_, err := deleter.exec("DELETE FROM parents WHERE id = 1")
	requireSQLError(t, err, mysql.ERLockWaitTimeout)
	// The timed-out statement is undone whole, parent delete included (see
	// statement_atomicity_test.go).
	require.Len(t, deleter.must("SELECT * FROM parents"), 1)
	deleter.must("ROLLBACK")
	require.Len(t, admin.must("SELECT * FROM parents"), 1, "the rolled-back delete left the parent")

	holder.must("COMMIT")
	deleter.must("BEGIN")
	deleter.must("DELETE FROM parents WHERE id = 1")
	deleter.must("COMMIT")
	rows := admin.must("SELECT parent_id FROM children WHERE id = 10")
	require.Len(t, rows, 1)
	assert.Nil(t, rows[0][0])
}
