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

// countingDeletes counts the transactions that delete lock records.
type countingDeletes struct {
	service.TransactionServicer
	deletes atomic.Int32
}

func (c *countingDeletes) Execute(ctx context.Context, cmp []service.Compare, s, fo []service.Op) (*service.TransactionResponse, error) {
	for _, op := range s {
		if op.Type == service.OpDelete {
			c.deletes.Add(1)
			break
		}
	}
	return c.TransactionServicer.Execute(ctx, cmp, s, fo)
}

func TestSession_FreshOwnerOnADeadLeaseRetriesWithALiveOne(t *testing.T) {
	s := newStack(t)
	tbl, counting := countedTable(t, s)
	sess := NewSession()
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	a := sess.NewOwner()
	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	dead := a.lease.id
	require.NoError(t, a.Release(context.Background()))
	// The lease ends under the session -- expired, or swept -- before its
	// keepalive notices.
	require.NoError(t, s.deps.Lease.Revoke(context.Background(), dead))

	b := sess.NewOwner()
	require.NoError(t, b.Acquire(context.Background(), tbl, []string{keyB}, ModeExclusive, 0),
		"an owner that held nothing has lost nothing: it is granted a live lease, not a 1213")
	assert.NotEqual(t, dead, b.lease.id)
	assert.True(t, leaseAlive(t, s, b.lease.id))
	assert.Equal(t, []string{b.ID() + "/x"}, queueOwners(t, tbl, keyB))
	assert.Equal(t, int32(2), counting.grants.Load())
	require.NoError(t, b.Release(context.Background()))
	assert.Empty(t, queueOwners(t, tbl, keyB))
}

func TestSession_RetiredLeaseIsLostToEveryOwnerOnIt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		retire func(t *testing.T, sess *Session, a *Owner)
	}{
		{"failed delete", func(t *testing.T, _ *Session, a *Owner) {
			require.NoError(t, a.Release(context.Background()))
		}},
		{"close", func(t *testing.T, sess *Session, _ *Owner) {
			require.NoError(t, sess.Close(context.Background()))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStack(t)
			deps := s.deps
			deps.Txn = failingDeletes{s.deps.Txn}
			tbl, err := New(deps, "acme/shop")
			require.NoError(t, err)
			sess := NewSession()
			t.Cleanup(func() { _ = sess.Close(context.Background()) })

			a, b := sess.NewOwner(), sess.NewOwner()
			require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
			require.NoError(t, b.Acquire(context.Background(), tbl, []string{keyB}, ModeExclusive, 0))
			require.Equal(t, a.lease.id, b.lease.id)

			tc.retire(t, sess, a)

			assert.Empty(t, queueOwners(t, tbl, keyB), "the retired lease took b's record with it")
			var lost *LockLostError
			require.ErrorAs(t, b.Acquire(context.Background(), tbl, []string{keyB}, ModeExclusive, 0), &lost,
				"b must not go on believing it holds a lock whose record is gone")
			assert.False(t, b.Active())
		})
	}
}

// releasingTxn releases owner the moment its first enqueue is applied, as a
// connection closing under an in-flight statement does.
type releasingTxn struct {
	service.TransactionServicer
	owner *Owner
	once  atomic.Bool
}

func (r *releasingTxn) Execute(ctx context.Context, c []service.Compare, s, fo []service.Op) (*service.TransactionResponse, error) {
	resp, err := r.TransactionServicer.Execute(ctx, c, s, fo)
	if len(s) > 0 && s[0].Type == service.OpPut && r.once.CompareAndSwap(false, true) {
		_ = r.owner.Release(context.Background())
	}
	return resp, err
}

func TestAcquire_OwnerReleasedDuringEnqueue_LeavesNoRecord(t *testing.T) {
	s := newStack(t)
	sess := NewSession()
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	o := sess.NewOwner()
	deps := s.deps
	deps.Txn = &releasingTxn{TransactionServicer: s.deps.Txn, owner: o}
	tbl, err := New(deps, "acme/shop")
	require.NoError(t, err)

	var released *OwnerReleasedError
	require.ErrorAs(t, o.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0), &released)
	assert.Empty(t, queueOwners(t, tbl, keyA), "a record written for a released owner is deleted, not orphaned on the session's lease")
}

// blockedError is what mustReturnWithin reports when ch delivered nothing.
type blockedError struct{ what string }

func (e *blockedError) Error() string { return e.what + " is blocked" }

// mustReturnWithin waits briefly for ch, reporting a blockedError instead of
// failing the test, so the caller can unblock what it is waiting behind
// first.
func mustReturnWithin(t *testing.T, ch <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(2 * time.Second):
		return &blockedError{what: what}
	}
}

// waitEntered reports whether a second Grant entered g within a second.
func waitEntered(g *gatedLease) bool {
	select {
	case <-g.entered:
		return true
	case <-time.After(time.Second):
		return false
	}
}

// gatedLease holds every Grant until release is closed.
type gatedLease struct {
	service.LeaseServicer
	entered chan struct{}
	release chan struct{}
	grants  atomic.Int32
	revokes atomic.Int32
}

func newGatedLease(inner service.LeaseServicer) *gatedLease {
	return &gatedLease{LeaseServicer: inner, entered: make(chan struct{}, 4), release: make(chan struct{})}
}

func (g *gatedLease) Grant(ctx context.Context, ttl int64) (*lease.Lease, error) {
	g.grants.Add(1)
	g.entered <- struct{}{}
	<-g.release
	return g.LeaseServicer.Grant(ctx, ttl)
}

func (g *gatedLease) Revoke(ctx context.Context, id int64) error {
	g.revokes.Add(1)
	return g.LeaseServicer.Revoke(ctx, id)
}

func TestSession_SlowGrantDoesNotBlockTheSession(t *testing.T) {
	s := newStack(t)
	shop := s.table(t)
	gate := newGatedLease(s.deps.Lease)
	deps := s.deps
	deps.Lease = gate
	billing, err := New(deps, "acme/billing")
	require.NoError(t, err)
	sess := NewSession()
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	held := sess.NewOwner()
	require.NoError(t, held.Acquire(context.Background(), shop, []string{keyA}, ModeExclusive, 0))

	slow := acquireAsync(sess.NewOwner(), billing, []string{keyB}, ModeExclusive, 0)
	<-gate.entered

	fast := acquireAsync(sess.NewOwner(), shop, []string{keyC}, ModeExclusive, 0)
	err = mustReturnWithin(t, fast, "an owner on the session's existing lease")
	close(gate.release)
	require.NoError(t, err, "a grant in flight in another store must not hold the session")
	require.NoError(t, mustReturn(t, slow, "the slow grant"))
}

func TestSession_ConcurrentFirstGrantsKeepOneLease(t *testing.T) {
	s := newStack(t)
	gate := newGatedLease(s.deps.Lease)
	deps := s.deps
	deps.Lease = gate
	tbl, err := New(deps, "acme/shop")
	require.NoError(t, err)
	sess := NewSession()
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	a, b := sess.NewOwner(), sess.NewOwner()
	aDone := acquireAsync(a, tbl, []string{keyA}, ModeExclusive, 0)
	bDone := acquireAsync(b, tbl, []string{keyB}, ModeExclusive, 0)
	<-gate.entered
	second := waitEntered(gate)
	close(gate.release)
	require.NoError(t, mustReturn(t, aDone, "a"))
	require.NoError(t, mustReturn(t, bDone, "b"))
	require.True(t, second, "both first acquires grant concurrently, outside the session's lock")

	assert.Equal(t, a.lease.id, b.lease.id, "both owners end on the one lease the session keeps")
	assert.Equal(t, int32(2), gate.grants.Load())
	assert.Equal(t, int32(1), gate.revokes.Load(), "the losing grant is revoked")
	assert.True(t, leaseAlive(t, s, a.lease.id))
}

func TestSession_EndingSessionRevokesOnceAndDeletesNothing(t *testing.T) {
	s := newStack(t)
	deps := s.deps
	counter := &countingDeletes{TransactionServicer: s.deps.Txn}
	deps.Txn = counter
	tbl, err := New(deps, "acme/shop")
	require.NoError(t, err)
	sess := NewSession()

	o := sess.NewOwner()
	require.NoError(t, o.Acquire(context.Background(), tbl, []string{keyA, keyB}, ModeExclusive, 0))
	require.NoError(t, sess.Close(context.Background()))
	require.NoError(t, o.Release(context.Background()))

	assert.Zero(t, counter.deletes.Load(), "the revoke already removed the records; one proposal is enough")
	assert.Empty(t, queueOwners(t, tbl, keyA))
	assert.Empty(t, queueOwners(t, tbl, keyB))
}
