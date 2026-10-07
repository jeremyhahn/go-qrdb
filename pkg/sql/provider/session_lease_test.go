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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/rowlock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sqlengine "github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
)

// leaseCounter counts the lease grants and revokes row locking issues.
type leaseCounter struct {
	service.LeaseServicer
	grants, revokes atomic.Int32
}

func (c *leaseCounter) Grant(ctx context.Context, ttl int64) (*lease.Lease, error) {
	c.grants.Add(1)
	return c.LeaseServicer.Grant(ctx, ttl)
}

func (c *leaseCounter) Revoke(ctx context.Context, id int64) error {
	c.revokes.Add(1)
	return c.LeaseServicer.Revoke(ctx, id)
}

// newCountedLockHarness is newLockHarness with the lease service counted.
func newCountedLockHarness(t *testing.T) (*lockHarness, *leaseCounter) {
	t.Helper()
	h, closeFn, err := sqltest.NewLockingHarness()
	require.NoError(t, err)
	t.Cleanup(closeFn)
	reg := h.Registry.(*sqltest.FakeRegistry)
	inner, err := reg.LeaseServiceFn("", "shop")
	require.NoError(t, err)
	counter := &leaseCounter{LeaseServicer: inner}
	reg.LeaseServiceFn = func(string, string) (service.LeaseServicer, error) { return counter, nil }

	prov := NewProvider("shop", h.KVSvc, WithRegistry(h.Registry))
	eng := sqlengine.New(sqlengine.Config{Provider: prov})
	t.Cleanup(func() { _ = eng.Close() })
	return &lockHarness{prov: prov, mvcc: h.MVCCSvc, eng: eng}, counter
}

func TestPessimisticAutocommit_StatementsShareTheConnectionsLease(t *testing.T) {
	h, counter := newCountedLockHarness(t)
	setupAccounts(t, h)
	c := h.conn(t)
	c.must("SET qrdb_concurrency = 'pessimistic'")

	for i := range 5 {
		c.must("UPDATE accounts SET balance = " + strconv.Itoa(i) + " WHERE id = 1")
		c.must("INSERT INTO accounts VALUES (" + strconv.Itoa(10+i) + ", 1)")
		c.must("DELETE FROM accounts WHERE id = " + strconv.Itoa(10+i))
		assert.Zero(t, h.lockRecords(t), "an autocommit statement's locks are released when it ends")
	}
	assert.Equal(t, int32(1), counter.grants.Load(), "one lease for the connection, not one per statement")
	assert.Zero(t, counter.revokes.Load(), "no revoke per statement")

	other := h.conn(t)
	other.must("SET qrdb_concurrency = 'pessimistic'")
	done := other.async("UPDATE accounts SET balance = 7 WHERE id = 1")
	require.NoError(t, awaitResult(t, done, "the other connection"), "nothing is still locked")

	c.sess.SessionEnd()
	assert.Equal(t, int32(1), counter.revokes.Load(), "the connection's end revokes its lease")
	assert.Zero(t, h.lockRecords(t))
}

// lockDeleteCounter counts transactions that delete row-lock records.
type lockDeleteCounter struct {
	service.TransactionServicer
	deletes atomic.Int32
}

func (c *lockDeleteCounter) Execute(ctx context.Context, cmp []service.Compare, s, fo []service.Op) (*service.TransactionResponse, error) {
	for _, op := range s {
		if op.Type == service.OpDelete && strings.HasPrefix(string(op.Key), rowlock.Prefix) {
			c.deletes.Add(1)
			break
		}
	}
	return c.TransactionServicer.Execute(ctx, cmp, s, fo)
}

func TestSessionEnd_RevokesOnceAndDeletesNoRecord(t *testing.T) {
	h, closeFn, err := sqltest.NewLockingHarness()
	require.NoError(t, err)
	t.Cleanup(closeFn)
	reg := h.Registry.(*sqltest.FakeRegistry)
	inner, err := reg.TransactionServiceFn("", "shop")
	require.NoError(t, err)
	counter := &lockDeleteCounter{TransactionServicer: inner}
	reg.TransactionServiceFn = func(string, string) (service.TransactionServicer, error) { return counter, nil }
	prov := NewProvider("shop", h.KVSvc, WithRegistry(h.Registry))
	eng := sqlengine.New(sqlengine.Config{Provider: prov})
	t.Cleanup(func() { _ = eng.Close() })
	lh := &lockHarness{prov: prov, mvcc: h.MVCCSvc, eng: eng}
	setupAccounts(t, lh)

	c := lh.conn(t)
	c.must("START TRANSACTION")
	c.must("SELECT * FROM accounts FOR UPDATE")
	require.Equal(t, 2, lh.lockRecords(t))

	c.sess.SessionEnd()
	assert.Zero(t, lh.lockRecords(t))
	assert.Zero(t, counter.deletes.Load(), "the lease revoke removes the records; deleting them first is a wasted proposal")
}

func TestPessimisticAutocommit_LockHeldUntilTheStatementEnds(t *testing.T) {
	h, _ := newCountedLockHarness(t)
	setupAccounts(t, h)
	holder := h.conn(t)
	holder.must("SET qrdb_concurrency = 'pessimistic'")
	holder.must("START TRANSACTION")
	holder.must("SELECT * FROM accounts WHERE id = 1 FOR UPDATE")

	c := h.conn(t)
	c.must("SET qrdb_concurrency = 'pessimistic'")
	c.must("UPDATE accounts SET balance = 1 WHERE id = 2")
	done := c.async("UPDATE accounts SET balance = 5 WHERE id = 1")
	blockedFor(t, done, 200*time.Millisecond, "an autocommit update of a locked row")

	holder.must("COMMIT")
	require.NoError(t, awaitResult(t, done, "the waiting update"))
	assert.Zero(t, h.lockRecords(t))
	rows := c.must("SELECT balance FROM accounts WHERE id = 1")
	require.Len(t, rows, 1)
	assert.EqualValues(t, 5, rows[0][0])
}

func TestPessimisticAutocommit_DeadlockVictimIsRolledBackAndTheSessionLocksAgain(t *testing.T) {
	h, counter := newCountedLockHarness(t)
	setupAccounts(t, h)
	a, b := h.conn(t), h.conn(t)
	for _, c := range []*lockConn{a, b} {
		c.must("SET qrdb_concurrency = 'pessimistic'")
		c.must("START TRANSACTION")
	}
	a.must("UPDATE accounts SET balance = 1 WHERE id = 1")
	b.must("UPDATE accounts SET balance = 2 WHERE id = 2")
	aDone := a.async("UPDATE accounts SET balance = 1 WHERE id = 2")
	blockedFor(t, aDone, 100*time.Millisecond, "a's second update")

	_, err := b.exec("UPDATE accounts SET balance = 2 WHERE id = 1")
	requireSQLError(t, err, 1213)
	require.NoError(t, awaitResult(t, aDone, "the survivor"), "the victim's locks are released")
	a.must("COMMIT")
	b.must("ROLLBACK")

	b.must("UPDATE accounts SET balance = 9 WHERE id = 1")
	assert.Zero(t, h.lockRecords(t))
	assert.Equal(t, int32(2), counter.grants.Load(), "one lease per connection, kept across the victim's rollback")
}
