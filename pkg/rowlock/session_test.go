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

package rowlock

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// countingLease counts the grants and revokes that reach the lease service.
type countingLease struct {
	service.LeaseServicer
	grants, revokes atomic.Int32
}

func (c *countingLease) Grant(ctx context.Context, ttl int64) (*lease.Lease, error) {
	c.grants.Add(1)
	return c.LeaseServicer.Grant(ctx, ttl)
}

func (c *countingLease) Revoke(ctx context.Context, id int64) error {
	c.revokes.Add(1)
	return c.LeaseServicer.Revoke(ctx, id)
}

// failingDeletes refuses every transaction that deletes, so an owner cannot
// delete its own records; enqueues still succeed.
type failingDeletes struct{ service.TransactionServicer }

func (f failingDeletes) Execute(ctx context.Context, c []service.Compare, s, fo []service.Op) (*service.TransactionResponse, error) {
	for _, op := range s {
		if op.Type == service.OpDelete {
			return nil, &injectedError{op: "delete"}
		}
	}
	return f.TransactionServicer.Execute(ctx, c, s, fo)
}

// countedTable returns a lock table whose lease grants and revokes are
// counted.
func countedTable(t *testing.T, s *stack, opts ...Option) (*Table, *countingLease) {
	t.Helper()
	counting := &countingLease{LeaseServicer: s.deps.Lease}
	deps := s.deps
	deps.Lease = counting
	tbl, err := New(deps, "acme/shop", opts...)
	require.NoError(t, err)
	return tbl, counting
}

// leaseAlive reports whether lease id still exists.
func leaseAlive(t *testing.T, s *stack, id int64) bool {
	t.Helper()
	_, err := s.deps.Lease.TimeToLive(context.Background(), id)
	if err == nil {
		return true
	}
	var gone *service.LeaseNotFoundError
	require.ErrorAs(t, err, &gone)
	return false
}

func TestSession_TransactionsShareOneLease(t *testing.T) {
	s := newStack(t)
	tbl, counting := countedTable(t, s)
	sess := NewSession()
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	var leaseID int64
	for i, key := range []string{keyA, keyB, keyA, keyC} {
		o := sess.NewOwner()
		require.NoError(t, o.Acquire(context.Background(), tbl, []string{key}, ModeExclusive, 0))
		if i == 0 {
			leaseID = o.lease.id
		}
		assert.Equal(t, leaseID, o.lease.id, "every transaction of the session binds to the same lease")
		require.NoError(t, o.Release(context.Background()))
		assert.Empty(t, queueOwners(t, tbl, key), "release deletes the transaction's records")
	}
	assert.Equal(t, int32(1), counting.grants.Load(), "one grant for the session, not one per statement")
	assert.Zero(t, counting.revokes.Load(), "no revoke per statement")
	assert.True(t, leaseAlive(t, s, leaseID), "the session's lease outlives its transactions")
}

func TestSession_ReleaseWakesTheNextWaiter(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	holder, waiter := NewSession(), NewSession()
	t.Cleanup(func() { _ = holder.Close(context.Background()); _ = waiter.Close(context.Background()) })

	a := holder.NewOwner()
	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	b := waiter.NewOwner()
	bDone := acquireAsync(b, tbl, []string{keyA}, ModeExclusive, 5*time.Second)
	waitQueued(t, tbl, keyA, 2)
	mustNotReturn(t, bDone, 100*time.Millisecond, "waiter")

	require.NoError(t, a.Release(context.Background()))
	require.NoError(t, mustReturn(t, bDone, "waiter"))
	assert.Equal(t, []string{b.ID() + "/x"}, queueOwners(t, tbl, keyA))
}

func TestSession_DeadlockVictimRecordsAreDeletedAndTheLeaseKept(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	sa, sb := NewSession(), NewSession()
	t.Cleanup(func() { _ = sa.Close(context.Background()); _ = sb.Close(context.Background()) })
	a, b := sa.NewOwner(), sb.NewOwner()

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	require.NoError(t, b.Acquire(context.Background(), tbl, []string{keyB}, ModeExclusive, 0))
	aDone := acquireAsync(a, tbl, []string{keyB}, ModeExclusive, 5*time.Second)
	waitQueued(t, tbl, keyB, 2)

	var deadlock *DeadlockError
	require.ErrorAs(t, b.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 5*time.Second), &deadlock)
	assert.False(t, b.Active(), "the victim's transaction is rolled back")
	require.NoError(t, mustReturn(t, aDone, "survivor"), "the victim's locks are gone, so the survivor proceeds")
	assert.Equal(t, []string{a.ID() + "/x"}, queueOwners(t, tbl, keyB))
	assert.True(t, leaseAlive(t, s, b.lease.id), "the session keeps its lease for its next transaction")
	require.NoError(t, a.Release(context.Background()))
}

func TestSession_CrashedSessionsLocksExpireWithItsLease(t *testing.T) {
	s := newStack(t)
	s.runExpiry(t)
	crashedDeps := s.deps
	crashedDeps.Lease = silentLease{s.deps.Lease}
	crashedTable, err := New(crashedDeps, "acme/shop", WithLeaseTTL(1))
	require.NoError(t, err)
	tbl := s.table(t)

	crashed := NewSession()
	require.NoError(t, crashed.NewOwner().Acquire(context.Background(), crashedTable, []string{keyA}, ModeExclusive, 0))
	live := NewSession()
	t.Cleanup(func() { _ = live.Close(context.Background()) })

	start := time.Now()
	b := live.NewOwner()
	require.NoError(t, b.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 10*time.Second))
	assert.Less(t, time.Since(start), 5*time.Second, "a session that stops renewing loses its locks on the lease TTL")
	assert.Equal(t, []string{b.ID() + "/x"}, queueOwners(t, tbl, keyA))
}

func TestSession_FailedDeleteRevokesTheLease(t *testing.T) {
	s := newStack(t)
	deps := s.deps
	deps.Txn = failingDeletes{s.deps.Txn}
	counting := &countingLease{LeaseServicer: s.deps.Lease}
	deps.Lease = counting
	tbl, err := New(deps, "acme/shop")
	require.NoError(t, err)
	sess := NewSession()
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	a := sess.NewOwner()
	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	first := a.lease.id
	require.NoError(t, a.Release(context.Background()), "the revoke removed what the delete could not")
	assert.Empty(t, queueOwners(t, tbl, keyA), "no record outlives its owner")
	assert.False(t, leaseAlive(t, s, first))

	b := sess.NewOwner()
	require.NoError(t, b.Acquire(context.Background(), tbl, []string{keyB}, ModeExclusive, 0))
	assert.NotEqual(t, first, b.lease.id, "the session grants a fresh lease after retiring one")
	assert.Equal(t, int32(2), counting.grants.Load())
}

func TestSession_IdleLeaseIsRevoked(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t, WithLeaseTTL(1))
	sess := NewSession()
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	o := sess.NewOwner()
	require.NoError(t, o.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	id := o.lease.id
	require.NoError(t, o.Release(context.Background()))

	deadline := time.Now().Add(5 * time.Second)
	for leaseAlive(t, s, id) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	assert.False(t, leaseAlive(t, s, id), "a lease no transaction used for a TTL is revoked")
	sess.mu.Lock()
	assert.Empty(t, sess.leases)
	sess.mu.Unlock()
}

func TestSession_LeaseInUseIsNotIdle(t *testing.T) {
	s := newStack(t)
	s.runExpiry(t)
	tbl := s.table(t, WithLeaseTTL(1))
	sess := NewSession()
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	o := sess.NewOwner()
	require.NoError(t, o.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	time.Sleep(1500 * time.Millisecond)
	assert.True(t, leaseAlive(t, s, o.lease.id), "a held lock keeps its lease renewed")
	assert.Equal(t, []string{o.ID() + "/x"}, queueOwners(t, tbl, keyA))
	require.NoError(t, o.Release(context.Background()))
}

func TestSession_LostLeaseFailsTheTransactionAndIsReplaced(t *testing.T) {
	s := newStack(t)
	tbl, counting := countedTable(t, s)
	sess := NewSession()
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	a := sess.NewOwner()
	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	require.NoError(t, s.deps.Lease.Revoke(context.Background(), a.lease.id))

	var lost *LockLostError
	require.ErrorAs(t, a.Acquire(context.Background(), tbl, []string{keyB}, ModeExclusive, 0), &lost)
	assert.False(t, a.Active())

	b := sess.NewOwner()
	require.NoError(t, b.Acquire(context.Background(), tbl, []string{keyB}, ModeExclusive, 0))
	assert.True(t, leaseAlive(t, s, b.lease.id))
	assert.Equal(t, int32(2), counting.grants.Load(), "the next transaction is granted a live lease")
	require.NoError(t, b.Release(context.Background()))
}

func TestSession_EachStoreHasItsOwnLease_AndCloseRevokesAll(t *testing.T) {
	s := newStack(t)
	shop := s.table(t)
	billing, err := New(s.deps, "acme/billing")
	require.NoError(t, err)
	sess := NewSession()

	a := sess.NewOwner()
	require.NoError(t, a.Acquire(context.Background(), shop, []string{keyA}, ModeExclusive, 0))
	b := sess.NewOwner()
	require.NoError(t, b.Acquire(context.Background(), billing, []string{keyB}, ModeExclusive, 0))
	assert.NotEqual(t, a.lease.id, b.lease.id)

	require.NoError(t, sess.Close(context.Background()))
	assert.False(t, leaseAlive(t, s, a.lease.id))
	assert.False(t, leaseAlive(t, s, b.lease.id))
	assert.Empty(t, queueOwners(t, shop, keyA), "closing the session releases every lock it still held")
	assert.Empty(t, queueOwners(t, billing, keyB))

	c := sess.NewOwner()
	require.NoError(t, c.Acquire(context.Background(), shop, []string{keyC}, ModeExclusive, 0), "a closed session can lock again")
	require.NoError(t, c.Release(context.Background()))
	require.NoError(t, sess.Close(context.Background()))
}

func TestSession_FailedGrantFailsTheRequest(t *testing.T) {
	s := newStack(t)
	deps := s.deps
	deps.Lease = failingGrant{s.deps.Lease}
	tbl, err := New(deps, "acme/shop")
	require.NoError(t, err)

	o := NewSession().NewOwner()
	var injected *injectedError
	require.ErrorAs(t, o.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0), &injected)
	assert.Empty(t, queueOwners(t, tbl, keyA))
	require.NoError(t, o.Release(context.Background()))
}

func TestSession_CloseReportsAFailedRevoke(t *testing.T) {
	s := newStack(t)
	deps := s.deps
	deps.Lease = failingRevoke{s.deps.Lease}
	tbl, err := New(deps, "acme/shop")
	require.NoError(t, err)
	sess := NewSession()
	o := sess.NewOwner()
	require.NoError(t, o.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))

	var injected *injectedError
	require.ErrorAs(t, sess.Close(context.Background()), &injected)
	_ = s.deps.Lease.Revoke(context.Background(), o.lease.id)
}
