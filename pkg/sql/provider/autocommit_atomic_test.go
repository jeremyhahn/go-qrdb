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

package provider

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sqlengine "github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
)

// countIDs returns how many of ids exist in shop.t.
func countIDs(t *testing.T, c *txConn, in string) int64 {
	t.Helper()
	_, rows := c.mustExec(t, "SELECT COUNT(*) FROM t WHERE id IN ("+in+")")
	return rows[0][0].(int64)
}

func TestAutocommit_MultiRowInsertFailingOnRow3_LeavesNoRow(t *testing.T) {
	h := newDDLHarness()
	c := newTxConn(t, h.newDDLProvider("shop"), "shop")
	c.mustExec(t, "CREATE TABLE t (id BIGINT PRIMARY KEY, v VARCHAR(8))")
	c.mustExec(t, "INSERT INTO t VALUES (1, 'a')")

	_, _, err := c.exec("INSERT INTO t VALUES (10, 'x'), (11, 'y'), (1, 'dup')")
	require.Error(t, err)
	assert.Zero(t, countIDs(t, c, "10, 11"), "a failed autocommit statement leaves none of its rows")

	c.mustExec(t, "INSERT INTO t VALUES (10, 'x'), (11, 'y')")
	assert.Equal(t, int64(2), countIDs(t, c, "10, 11"), "and the session's next statement runs normally")
}

func TestAutocommit_MultiRowUpdateFailingPartWay_ChangesNoRow(t *testing.T) {
	h := newDDLHarness()
	c := newTxConn(t, h.newDDLProvider("shop"), "shop")
	c.mustExec(t, "CREATE TABLE t (id BIGINT PRIMARY KEY, v VARCHAR(8), u BIGINT, UNIQUE KEY uu (u))")
	c.mustExec(t, "INSERT INTO t VALUES (1, 'a', 1), (2, 'b', 2), (3, 'c', 3), (4, 'd', 103)")

	// u + 100 collides on row 3 only: 103 belongs to row 4.
	_, _, err := c.exec("UPDATE t SET u = u + 100 WHERE id IN (1, 2, 3)")
	require.Error(t, err)
	_, rows := c.mustExec(t, "SELECT u FROM t ORDER BY id")
	require.Len(t, rows, 4)
	assert.EqualValues(t, []any{int64(1), int64(2), int64(3)}, []any{rows[0][0], rows[1][0], rows[2][0]},
		"no row of the failed UPDATE was changed")
}

func TestAutocommit_DuplicateHandlingStillSeesTheRow(t *testing.T) {
	h := newDDLHarness()
	c := newTxConn(t, h.newDDLProvider("shop"), "shop")
	c.mustExec(t, "CREATE TABLE t (id BIGINT PRIMARY KEY, v VARCHAR(8))")
	c.mustExec(t, "INSERT INTO t VALUES (1, 'a')")

	c.mustExec(t, "INSERT IGNORE INTO t VALUES (1, 'ignored'), (2, 'b')")
	c.mustExec(t, "INSERT INTO t VALUES (1, 'z') ON DUPLICATE KEY UPDATE v = 'upd'")
	c.mustExec(t, "INSERT INTO t VALUES (3, 'c'), (3, 'd') ON DUPLICATE KEY UPDATE v = 'same'")
	_, rows := c.mustExec(t, "SELECT id, v FROM t ORDER BY id")
	require.Len(t, rows, 3)
	assert.Equal(t, "upd", rows[0][1])
	assert.Equal(t, "b", rows[1][1])
	assert.Equal(t, "same", rows[2][1], "a statement sees the rows it wrote itself")
}

// racingTxn commits a row under prefix just before the first transaction
// that writes one, as a concurrent statement winning the race between this
// one's duplicate check and its commit would.
type racingTxn struct {
	service.TransactionServicer
	prefix string
	done   atomic.Bool
}

func (r *racingTxn) Execute(ctx context.Context, c []service.Compare, s, fo []service.Op) (*service.TransactionResponse, error) {
	for _, op := range s {
		if op.Type == service.OpPut && strings.HasPrefix(string(op.Key), r.prefix) && r.done.CompareAndSwap(false, true) {
			if _, err := r.TransactionServicer.Execute(ctx, nil, []service.Op{op}, nil); err != nil {
				return nil, err
			}
			break
		}
	}
	return r.TransactionServicer.Execute(ctx, c, s, fo)
}

func TestAutocommit_DuplicateCommittedConcurrently_Is1062(t *testing.T) {
	h := newDDLHarness()
	c := newTxConn(t, h.newDDLProvider("shop"), "shop")
	c.mustExec(t, "CREATE TABLE t (id BIGINT PRIMARY KEY, v VARCHAR(8))")

	def, err := h.tableSvc.GetTable(context.Background(), "shop", "t")
	require.NoError(t, err)
	reg := h.registry.(*fakeRelationalRegistry)
	reg.txnSvc = &racingTxn{TransactionServicer: h.txnSvc, prefix: service.TableDataPrefix("shop", def.TableID, 0)}

	_, _, err = c.exec("INSERT INTO t VALUES (7, 'mine')")
	require.Error(t, err)
	assert.Equal(t, 1062, sqle.CastSQLError(err).Num,
		"a key taken between the check and the commit is a duplicate (1062), not a retry: %v", err)
}

// proposalCounter counts every transaction proposed, and those that delete
// row-lock records.
type proposalCounter struct {
	service.TransactionServicer
	proposals atomic.Int32
}

func (p *proposalCounter) Execute(ctx context.Context, c []service.Compare, s, fo []service.Op) (*service.TransactionResponse, error) {
	p.proposals.Add(1)
	return p.TransactionServicer.Execute(ctx, c, s, fo)
}

func TestPessimisticAutocommit_ReleaseIsFoldedIntoTheCommit(t *testing.T) {
	hs, closeFn, err := sqltest.NewLockingHarness()
	require.NoError(t, err)
	t.Cleanup(closeFn)
	reg := hs.Registry.(*sqltest.FakeRegistry)
	inner, err := reg.TransactionServiceFn("", "shop")
	require.NoError(t, err)
	counter := &proposalCounter{TransactionServicer: inner}
	reg.TransactionServiceFn = func(string, string) (service.TransactionServicer, error) { return counter, nil }
	prov := NewProvider("shop", hs.KVSvc, WithRegistry(hs.Registry))
	eng := sqlengine.New(sqlengine.Config{Provider: prov})
	t.Cleanup(func() { _ = eng.Close() })
	lh := &lockHarness{prov: prov, mvcc: hs.MVCCSvc, eng: eng}
	setupAccounts(t, lh)

	c := lh.conn(t)
	c.must("SET qrdb_concurrency = 'pessimistic'")
	c.must("UPDATE accounts SET balance = 0 WHERE id = 1") // the connection's lease is granted here

	counter.proposals.Store(0)
	c.must("UPDATE accounts SET balance = 5 WHERE id = 1")
	assert.Equal(t, int32(2), counter.proposals.Load(),
		"a pessimistic single-row UPDATE proposes its lock enqueue and one commit that also releases the lock")
	assert.Zero(t, lh.lockRecords(t))
	rows := c.must("SELECT balance FROM accounts WHERE id = 1")
	assert.EqualValues(t, 5, rows[0][0])
}

func TestPessimisticAutocommit_LostCommitStillReleasesTheLocks(t *testing.T) {
	hs, closeFn, err := sqltest.NewLockingHarness()
	require.NoError(t, err)
	t.Cleanup(closeFn)
	lh := &lockHarness{mvcc: hs.MVCCSvc}
	reg := hs.Registry.(*sqltest.FakeRegistry)
	inner, err := reg.TransactionServiceFn("", "shop")
	require.NoError(t, err)
	reg.TransactionServiceFn = func(string, string) (service.TransactionServicer, error) {
		return &losingTxn{TransactionServicer: inner}, nil
	}
	lh.prov = NewProvider("shop", hs.KVSvc, WithRegistry(hs.Registry))
	lh.eng = sqlengine.New(sqlengine.Config{Provider: lh.prov})
	t.Cleanup(func() { _ = lh.eng.Close() })
	setupAccounts(t, lh)

	c := lh.conn(t)
	c.must("SET qrdb_concurrency = 'pessimistic'")
	_, err = c.exec("UPDATE accounts SET balance = 5 WHERE id = 1")
	require.Error(t, err)
	assert.Zero(t, lh.lockRecords(t), "a commit that loses its compare-and-swap still releases the statement's locks")
}

// losingTxn makes every transaction that writes a row and releases row
// locks lose its compare-and-swap, applying its failure branch.
type losingTxn struct{ service.TransactionServicer }

func (l *losingTxn) Execute(ctx context.Context, c []service.Compare, s, fo []service.Op) (*service.TransactionResponse, error) {
	if len(fo) > 0 && len(s) > len(fo) {
		if _, err := l.TransactionServicer.Execute(ctx, nil, fo, nil); err != nil {
			return nil, err
		}
		return &service.TransactionResponse{Succeeded: false}, nil
	}
	return l.TransactionServicer.Execute(ctx, c, s, fo)
}

func TestWriteSetFor_ImplicitReadsOfAnotherStoreGoDirect(t *testing.T) {
	ctx, sess := newTxContext(false)
	_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	ws := sess.writeSet()
	ws.beginImplicit()

	store := newMemStore()
	shop := testUsersDef()
	billing := testUsersDef()
	billing.Database = "billing"
	newTable := func(def *service.TableDef) *RelationalTable {
		tbl, err := NewRelationalTable(def, store, store, WithTransactionService(&memTxn{store: store}))
		require.NoError(t, err)
		return tbl
	}
	shopT, billingT := newTable(shop), newTable(billing)

	got, err := shopT.writeSetFor(ctx, false)
	require.NoError(t, err)
	assert.Nil(t, got, "a read before the statement writes anything is direct")

	got, err = shopT.writeSetFor(ctx, true)
	require.NoError(t, err)
	assert.Same(t, ws, got, "a write binds the statement's implicit transaction")

	got, err = billingT.writeSetFor(ctx, false)
	require.NoError(t, err)
	assert.Nil(t, got, "a read of another store goes direct")

	_, err = billingT.writeSetFor(ctx, true)
	var cross *CrossDatabaseTransactionError
	require.True(t, errors.As(err, &cross), "a statement cannot commit to two stores at once: %v", err)
}

func TestCommandBegin_DiscardsALeftoverImplicitTransaction(t *testing.T) {
	ctx, sess := newTxContext(false)
	_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	leftover := sess.writeSet()
	leftover.beginImplicit()

	require.NoError(t, sess.CommandBegin())
	fresh := sess.writeSet()
	require.NotNil(t, fresh)
	assert.NotSame(t, leftover, fresh)
	assert.False(t, fresh.Implicit(), "the next statement starts clean")
	sess.CommandEnd()

	require.NoError(t, sess.CommandBegin())
	assert.Same(t, fresh, sess.writeSet(), "a write-set that is not a leftover statement's is kept")
}

// Compile-time check that the doubles above are what they claim.
var (
	_ service.TransactionServicer = (*racingTxn)(nil)
	_ service.TransactionServicer = (*proposalCounter)(nil)
	_ service.TransactionServicer = (*losingTxn)(nil)
)
