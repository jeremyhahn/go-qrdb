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
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	keyA = "__tbl:shop/1/0/row-a"
	keyB = "__tbl:shop/1/0/row-b"
	keyC = "__tbl:shop/1/0/row-c"
)

// ---------------------------------------------------------------------------
// Exclusion and compatibility
// ---------------------------------------------------------------------------

func TestAcquire_ExclusiveExcludesExclusive_NowaitFailsAndWithdraws(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a, b := NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()); _ = b.Release(context.Background()) })

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, time.Second))
	assert.True(t, a.Holds(keyA, ModeExclusive))

	err := b.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0)
	var nowait *LockNotAvailableError
	require.ErrorAs(t, err, &nowait)
	assert.Equal(t, keyA, nowait.Key)
	assert.False(t, b.Holds(keyA, ModeExclusive))

	// The refused request's record was withdrawn, so it blocks nobody.
	assert.Equal(t, []string{a.ID() + "/x"}, queueOwners(t, tbl, keyA))
}

func TestAcquire_SharedIsCompatibleWithShared_NotWithExclusive(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a, b, c := NewSession().NewOwner(), NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() {
		for _, o := range []*Owner{a, b, c} {
			_ = o.Release(context.Background())
		}
	})

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeShared, 0))
	require.NoError(t, b.Acquire(context.Background(), tbl, []string{keyA}, ModeShared, 0))

	var nowait *LockNotAvailableError
	require.ErrorAs(t, c.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0), &nowait)
}

func TestAcquire_ReentryIsFree_AndExclusiveCoversShared(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a := NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()) })

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA, keyA}, ModeExclusive, 0))
	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeShared, 0))
	assert.Equal(t, []string{a.ID() + "/x"}, queueOwners(t, tbl, keyA), "re-entry writes no second record")
}

func TestAcquire_UpgradeAlone_IsGranted(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a := NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()) })

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeShared, 0))
	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	assert.True(t, a.Holds(keyA, ModeExclusive))
}

func TestAcquire_UpgradeWaitsForOtherSharedHolder(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a, b := NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()); _ = b.Release(context.Background()) })

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeShared, 0))
	require.NoError(t, b.Acquire(context.Background(), tbl, []string{keyA}, ModeShared, 0))

	upgraded := acquireAsync(a, tbl, []string{keyA}, ModeExclusive, 5*time.Second)
	mustNotReturn(t, upgraded, 150*time.Millisecond, "upgrade while another reader holds the row")

	require.NoError(t, b.Release(context.Background()))
	require.NoError(t, mustReturn(t, upgraded, "upgrade"))
	assert.True(t, a.Holds(keyA, ModeExclusive))
}

// ---------------------------------------------------------------------------
// FIFO wait queue and wake-up
// ---------------------------------------------------------------------------

func TestAcquire_WaitersAreGrantedInFIFOOrder(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a, b, c := NewSession().NewOwner(), NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() {
		for _, o := range []*Owner{a, b, c} {
			_ = o.Release(context.Background())
		}
	})

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	bDone := acquireAsync(b, tbl, []string{keyA}, ModeExclusive, 5*time.Second)
	waitQueued(t, tbl, keyA, 2)
	cDone := acquireAsync(c, tbl, []string{keyA}, ModeShared, 5*time.Second)
	waitQueued(t, tbl, keyA, 3)

	require.NoError(t, a.Release(context.Background()))
	require.NoError(t, mustReturn(t, bDone, "first waiter"))
	// The shared request queued behind an exclusive one waits for it, even
	// though nothing incompatible is GRANTED ahead of it any longer.
	mustNotReturn(t, cDone, 150*time.Millisecond, "a shared request behind a waiting exclusive one")

	require.NoError(t, b.Release(context.Background()))
	require.NoError(t, mustReturn(t, cDone, "second waiter"))
	assert.True(t, c.Holds(keyA, ModeShared))
}

func TestAcquire_SharedBehindWaitingExclusive_DoesNotStarveWriter(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a, w, r := NewSession().NewOwner(), NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() {
		for _, o := range []*Owner{a, w, r} {
			_ = o.Release(context.Background())
		}
	})

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeShared, 0))
	wDone := acquireAsync(w, tbl, []string{keyA}, ModeExclusive, 5*time.Second)
	waitQueued(t, tbl, keyA, 2)

	var nowait *LockNotAvailableError
	require.ErrorAs(t, r.Acquire(context.Background(), tbl, []string{keyA}, ModeShared, 0), &nowait,
		"a reader arriving behind a waiting writer must not jump it")

	require.NoError(t, a.Release(context.Background()))
	require.NoError(t, mustReturn(t, wDone, "writer"))
}

// ---------------------------------------------------------------------------
// Timeouts
// ---------------------------------------------------------------------------

func TestAcquire_LockWaitTimeout_FailsOnlyTheRequest(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a, b := NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()); _ = b.Release(context.Background()) })

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	require.NoError(t, b.Acquire(context.Background(), tbl, []string{keyB}, ModeExclusive, 0))

	start := time.Now()
	err := b.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 100*time.Millisecond)
	var timeout *LockWaitTimeoutError
	require.ErrorAs(t, err, &timeout)
	assert.GreaterOrEqual(t, time.Since(start), 100*time.Millisecond)
	assert.Equal(t, keyA, timeout.Key)
	assert.Equal(t, ModeExclusive, timeout.Mode)
	assert.Equal(t, 100*time.Millisecond, timeout.Wait)

	// Only the request failed: its waiting record is gone, the lock it
	// already held is still held, and the owner is still usable.
	assert.Equal(t, []string{a.ID() + "/x"}, queueOwners(t, tbl, keyA))
	assert.True(t, b.Holds(keyB, ModeExclusive))
	assert.Equal(t, []string{b.ID() + "/x"}, queueOwners(t, tbl, keyB))
	require.NoError(t, b.Acquire(context.Background(), tbl, []string{keyC}, ModeExclusive, 0))
}

func TestAcquire_PartiallyGrantedRequest_KeepsWhatItGot(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a, b := NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()); _ = b.Release(context.Background()) })

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyB}, ModeExclusive, 0))
	err := b.Acquire(context.Background(), tbl, []string{keyA, keyB}, ModeExclusive, 50*time.Millisecond)
	var timeout *LockWaitTimeoutError
	require.ErrorAs(t, err, &timeout)
	assert.True(t, b.Holds(keyA, ModeExclusive), "the key granted in the same request stays held")
	assert.Equal(t, []string{a.ID() + "/x"}, queueOwners(t, tbl, keyB))
}

func TestAcquire_ContextCancelled_WithdrawsWaitingRecord(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a, b := NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()); _ = b.Release(context.Background()) })

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Acquire(ctx, tbl, []string{keyA}, ModeExclusive, 5*time.Second) }()
	waitQueued(t, tbl, keyA, 2)
	cancel()
	require.ErrorIs(t, mustReturn(t, done, "cancelled waiter"), context.Canceled)
	assert.Equal(t, []string{a.ID() + "/x"}, queueOwners(t, tbl, keyA))
}

// ---------------------------------------------------------------------------
// Deadlocks
// ---------------------------------------------------------------------------

func TestAcquire_TwoPartyDeadlock_NewestRequestIsVictimAndReleased(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a, b := NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()); _ = b.Release(context.Background()) })

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	require.NoError(t, b.Acquire(context.Background(), tbl, []string{keyB}, ModeExclusive, 0))

	aDone := acquireAsync(a, tbl, []string{keyB}, ModeExclusive, 5*time.Second)
	waitQueued(t, tbl, keyB, 2)

	err := b.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 5*time.Second)
	var dl *DeadlockError
	require.ErrorAs(t, err, &dl)
	assert.Equal(t, keyA, dl.Key)
	assert.False(t, dl.Unsearched)

	// The victim was released as a whole, so the survivor proceeds.
	require.NoError(t, mustReturn(t, aDone, "survivor"))
	assert.False(t, b.Holds(keyB, ModeExclusive))
	var released *OwnerReleasedError
	require.ErrorAs(t, b.Acquire(context.Background(), tbl, []string{keyC}, ModeShared, 0), &released)
}

// Both requests of a cycle can check for it after both have enqueued, and
// both then find it. Only the one whose edge is newest -- the one that closed
// the cycle -- is its victim; the older one waits and is granted.
func TestAcquire_TwoPartyDeadlock_BothCheckingAfterward_OnlyTheNewestIsVictim(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a, b := NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()); _ = b.Release(context.Background()) })

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	require.NoError(t, b.Acquire(context.Background(), tbl, []string{keyB}, ModeExclusive, 0))

	tblA, gateA := gatedTable(t, s)
	tblB, gateB := gatedTable(t, s)
	aDone := acquireAsync(a, tblA, []string{keyB}, ModeExclusive, 5*time.Second)
	mustReach(t, gateA, "the older request")
	bDone := acquireAsync(b, tblB, []string{keyA}, ModeExclusive, 5*time.Second)
	mustReach(t, gateB, "the newer request")

	// Both requests have enqueued, so the older request's check finds the
	// cycle too.
	close(gateA.proceed)
	close(gateB.proceed)

	var dl *DeadlockError
	require.ErrorAs(t, mustReturn(t, bDone, "the newer request"), &dl)
	require.NoError(t, mustReturn(t, aDone, "the older request"), "one victim per cycle")
	assert.True(t, a.Holds(keyB, ModeExclusive))
	assert.Equal(t, []string{a.ID() + "/x"}, queueOwners(t, tbl, keyB))
}

// Both requests of a two-party cycle have enqueued before either detector
// reads the lock table -- what a loaded cluster does when the older
// request's enqueue and read are slower than the newer one's -- so both
// detectors see the whole cycle. Exactly one is the victim: the newest
// request, whose edge closed the cycle. The older one waits and is granted
// once the victim's rollback frees the row.
func TestAcquire_ConcurrentDetection_OnlyTheNewestRequestIsVictim(t *testing.T) {
	s := newStack(t)
	d := s.deps
	gate := newDetectionBarrier(s.deps.MVCC, 2)
	d.MVCC = gate
	tbl, err := New(d, "acme/shop")
	require.NoError(t, err)
	a, b := NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()); _ = b.Release(context.Background()) })

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	require.NoError(t, b.Acquire(context.Background(), tbl, []string{keyB}, ModeExclusive, 0))

	aDone := acquireAsync(a, tbl, []string{keyB}, ModeExclusive, 5*time.Second)
	select {
	case <-gate.arrived: // a has enqueued and is held before its detection read
	case err := <-aDone:
		t.Fatalf("a returned before detecting: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("a never reached deadlock detection")
	}
	bDone := acquireAsync(b, tbl, []string{keyA}, ModeExclusive, 5*time.Second)

	var dl *DeadlockError
	require.ErrorAs(t, mustReturn(t, bDone, "the newest request"), &dl)
	assert.Equal(t, keyA, dl.Key)
	require.NoError(t, mustReturn(t, aDone, "the older request"), "the older request was made a second victim")
	assert.True(t, a.Holds(keyB, ModeExclusive))
}

// The same with three parties: every detector reads a table in which the
// whole cycle is queued, and only the request that closed it is the victim.
func TestAcquire_ConcurrentDetection_ThreePartyCycleHasOneVictim(t *testing.T) {
	s := newStack(t)
	d := s.deps
	gate := newDetectionBarrier(s.deps.MVCC, 3)
	d.MVCC = gate
	tbl, err := New(d, "acme/shop")
	require.NoError(t, err)
	a, b, c := NewSession().NewOwner(), NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() {
		for _, o := range []*Owner{a, b, c} {
			_ = o.Release(context.Background())
		}
	})

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	require.NoError(t, b.Acquire(context.Background(), tbl, []string{keyB}, ModeExclusive, 0))
	require.NoError(t, c.Acquire(context.Background(), tbl, []string{keyC}, ModeExclusive, 0))

	arrive := func(done <-chan error, who string) {
		t.Helper()
		select {
		case <-gate.arrived:
		case err := <-done:
			t.Fatalf("%s returned before detecting: %v", who, err)
		case <-time.After(5 * time.Second):
			t.Fatalf("%s never reached deadlock detection", who)
		}
	}
	aDone := acquireAsync(a, tbl, []string{keyB}, ModeExclusive, 5*time.Second)
	arrive(aDone, "a")
	bDone := acquireAsync(b, tbl, []string{keyC}, ModeExclusive, 5*time.Second)
	arrive(bDone, "b")
	cDone := acquireAsync(c, tbl, []string{keyA}, ModeExclusive, 5*time.Second)

	var dl *DeadlockError
	require.ErrorAs(t, mustReturn(t, cDone, "c"), &dl)
	require.NoError(t, mustReturn(t, bDone, "b"), "b was made a second victim")
	require.NoError(t, b.Release(context.Background()))
	require.NoError(t, mustReturn(t, aDone, "a"), "a was made a second victim")
}

func TestAcquire_ThreePartyDeadlock_IsDetected(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a, b, c := NewSession().NewOwner(), NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() {
		for _, o := range []*Owner{a, b, c} {
			_ = o.Release(context.Background())
		}
	})

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	require.NoError(t, b.Acquire(context.Background(), tbl, []string{keyB}, ModeExclusive, 0))
	require.NoError(t, c.Acquire(context.Background(), tbl, []string{keyC}, ModeExclusive, 0))

	aDone := acquireAsync(a, tbl, []string{keyB}, ModeExclusive, 5*time.Second)
	waitQueued(t, tbl, keyB, 2)
	bDone := acquireAsync(b, tbl, []string{keyC}, ModeExclusive, 5*time.Second)
	waitQueued(t, tbl, keyC, 2)

	var dl *DeadlockError
	require.ErrorAs(t, c.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 5*time.Second), &dl)
	require.NoError(t, mustReturn(t, bDone, "b"))
	require.NoError(t, b.Release(context.Background()))
	require.NoError(t, mustReturn(t, aDone, "a"))
}

func TestAcquire_UpgradeDeadlock_IsDetected(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a, b := NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()); _ = b.Release(context.Background()) })

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeShared, 0))
	require.NoError(t, b.Acquire(context.Background(), tbl, []string{keyA}, ModeShared, 0))
	aDone := acquireAsync(a, tbl, []string{keyA}, ModeExclusive, 5*time.Second)
	waitQueued(t, tbl, keyA, 3)

	var dl *DeadlockError
	require.ErrorAs(t, b.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 5*time.Second), &dl)
	require.NoError(t, mustReturn(t, aDone, "a's upgrade"))
}

func TestAcquire_WaitWithoutCycle_IsNotADeadlock(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a, b := NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()); _ = b.Release(context.Background()) })

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	bDone := acquireAsync(b, tbl, []string{keyA}, ModeExclusive, 5*time.Second)
	mustNotReturn(t, bDone, 100*time.Millisecond, "a plain waiter")
	require.NoError(t, a.Release(context.Background()))
	require.NoError(t, mustReturn(t, bDone, "b"))
}

func TestAcquire_LockTableTooLargeToSearch_RequesterIsVictim(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	bulk, a, b := NewSession().NewOwner(), NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() {
		for _, o := range []*Owner{bulk, a, b} {
			_ = o.Release(context.Background())
		}
	})

	keys := make([]string, MaxDetectionRecords)
	for i := range keys {
		keys[i] = "__tbl:shop/9/0/bulk-" + strconv.Itoa(i)
	}
	require.NoError(t, bulk.Acquire(context.Background(), tbl, keys, ModeShared, 0))
	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))

	var dl *DeadlockError
	require.ErrorAs(t, b.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 5*time.Second), &dl)
	assert.True(t, dl.Unsearched)
	assert.Contains(t, dl.Error(), strconv.Itoa(MaxDetectionRecords))
}

// ---------------------------------------------------------------------------
// Leases: release, expiry, loss
// ---------------------------------------------------------------------------

func TestRelease_DeletesEveryRecord_AndIsIdempotent(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a := NewSession().NewOwner()
	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA, keyB}, ModeExclusive, 0))
	assert.True(t, a.Active())

	require.NoError(t, a.Release(context.Background()))
	require.NoError(t, a.Release(context.Background()))
	assert.False(t, a.Active())
	assert.Empty(t, queueOwners(t, tbl, keyA))
	assert.Empty(t, queueOwners(t, tbl, keyB))
}

func TestRelease_OwnerThatNeverLocked_IsANoOp(t *testing.T) {
	a := NewSession().NewOwner()
	require.NoError(t, a.Release(context.Background()))
	assert.False(t, a.Active())
}

func TestRelease_WorksWithACancelledContext(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a := NewSession().NewOwner()
	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, a.Release(ctx), "a dropped connection's context is already done")
	assert.Empty(t, queueOwners(t, tbl, keyA))
}

func TestLeaseExpiry_ReleasesACrashedHoldersLocks_AndWakesWaiters(t *testing.T) {
	s := newStack(t)
	s.runExpiry(t)

	crashedDeps := s.deps
	crashedDeps.Lease = silentLease{s.deps.Lease}
	crashedTable, err := New(crashedDeps, "acme/shop", WithLeaseTTL(1))
	require.NoError(t, err)
	tbl := s.table(t)

	crashed, b := NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() { _ = b.Release(context.Background()) })
	require.NoError(t, crashed.Acquire(context.Background(), crashedTable, []string{keyA}, ModeExclusive, 0))

	start := time.Now()
	require.NoError(t, b.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 10*time.Second))
	assert.Less(t, time.Since(start), 5*time.Second, "the crashed holder's lease must expire on its TTL")
	assert.Equal(t, []string{b.ID() + "/x"}, queueOwners(t, tbl, keyA))
}

func TestKeepalive_KeepsLocksPastTheLeaseTTL(t *testing.T) {
	s := newStack(t)
	s.runExpiry(t)
	tbl := s.table(t, WithLeaseTTL(1))

	a, b := NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()); _ = b.Release(context.Background()) })
	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))

	time.Sleep(1500 * time.Millisecond)
	var nowait *LockNotAvailableError
	require.ErrorAs(t, b.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0), &nowait,
		"a live holder's lease is renewed and its lock survives the TTL")
}

func TestAcquire_WaiterWhoseLeaseIsRevoked_GetsLockLost(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a, b := NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()); _ = b.Release(context.Background()) })

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	bDone := acquireAsync(b, tbl, []string{keyA}, ModeExclusive, 5*time.Second)
	waitQueued(t, tbl, keyA, 2)

	b.mu.Lock()
	leaseID := b.lease.id
	b.mu.Unlock()
	require.NoError(t, s.deps.Lease.Revoke(context.Background(), leaseID))

	var lost *LockLostError
	require.ErrorAs(t, mustReturn(t, bDone, "waiter"), &lost)
	assert.False(t, b.Active())
}

func TestAcquire_AfterKeepaliveReportsLoss_FailsLockLost(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a := NewSession().NewOwner()
	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	a.lease.lost.Store(true)

	var lost *LockLostError
	require.ErrorAs(t, a.Acquire(context.Background(), tbl, []string{keyB}, ModeExclusive, 0), &lost)
	assert.False(t, a.Active())
	assert.Empty(t, queueOwners(t, tbl, keyA))
}

// ---------------------------------------------------------------------------
// No node-local state: what failover relies on
// ---------------------------------------------------------------------------

func TestLocks_AreReplicatedState_NotHeldByTheTableHandle(t *testing.T) {
	s := newStack(t)
	// Two independent handles, as two nodes (or a node before and after a
	// leader change) would build: everything they agree on comes from the
	// replicated records, nothing from the handle.
	node1, node2 := s.table(t), s.table(t)
	a, b := NewSession().NewOwner(), NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()); _ = b.Release(context.Background()) })

	require.NoError(t, a.Acquire(context.Background(), node1, []string{keyA}, ModeExclusive, 0))
	bDone := acquireAsync(b, node2, []string{keyA}, ModeExclusive, 5*time.Second)
	mustNotReturn(t, bDone, 100*time.Millisecond, "waiter on another handle")
	require.NoError(t, a.Release(context.Background()))
	require.NoError(t, mustReturn(t, bDone, "waiter on another handle"))
}

// ---------------------------------------------------------------------------
// Refusals
// ---------------------------------------------------------------------------

func TestAcquire_SecondScope_IsRefusedBeforeWriting(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	other, err := New(s.deps, "acme/other")
	require.NoError(t, err)
	a := NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()) })

	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeShared, 0))
	var mismatch *ScopeMismatchError
	require.ErrorAs(t, a.Acquire(context.Background(), other, []string{keyB}, ModeShared, 0), &mismatch)
	assert.Equal(t, "acme/shop", mismatch.Bound)
	assert.Equal(t, "acme/other", mismatch.Requested)
	assert.Empty(t, queueOwners(t, tbl, keyB))
}

func TestAcquire_InvalidMode_IsRefused(t *testing.T) {
	s := newStack(t)
	var invalid *InvalidModeError
	require.ErrorAs(t, NewSession().NewOwner().Acquire(context.Background(), s.table(t), []string{keyA}, Mode(9), 0), &invalid)
	assert.Contains(t, invalid.Error(), "9")
}

func TestAcquire_NoKeys_IsANoOp(t *testing.T) {
	s := newStack(t)
	a := NewSession().NewOwner()
	require.NoError(t, a.Acquire(context.Background(), s.table(t), nil, ModeExclusive, 0))
	assert.False(t, a.Active(), "no lease is granted for an empty request")
}

func TestNew_MissingDependencies_AreNamed(t *testing.T) {
	s := newStack(t)
	cases := map[string]func(d *Deps){
		"transaction service": func(d *Deps) { d.Txn = nil },
		"mvcc service":        func(d *Deps) { d.MVCC = nil },
		"lease service":       func(d *Deps) { d.Lease = nil },
		"watch service":       func(d *Deps) { d.Watch = nil },
	}
	for name, drop := range cases {
		t.Run(name, func(t *testing.T) {
			d := s.deps
			drop(&d)
			_, err := New(d, "acme/shop")
			var missing *MissingDependencyError
			require.ErrorAs(t, err, &missing)
			assert.Equal(t, name, missing.Name)
		})
	}
}

func TestNew_WithLeaseTTL_IgnoresNonPositive(t *testing.T) {
	s := newStack(t)
	assert.Equal(t, DefaultLeaseTTL, s.table(t, WithLeaseTTL(0)).leaseTTL)
	assert.Equal(t, int64(3), s.table(t, WithLeaseTTL(3)).leaseTTL)
	assert.Equal(t, "acme/shop", s.table(t).Scope())
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

func TestErrors_Messages(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&LockWaitTimeoutError{Wait: 50 * time.Second}, "lock wait timeout exceeded after 50s"},
		{&LockNotAvailableError{}, "NOWAIT"},
		{&DeadlockError{}, "deadlock found"},
		{&DeadlockError{Unsearched: true}, "more than"},
		{&LockLostError{}, "lease was lost"},
		{&ScopeMismatchError{Bound: "a", Requested: "b"}, "bound to a"},
		{&OwnerReleasedError{}, "released"},
		{&InvalidModeError{Mode: 7}, "invalid lock mode 7"},
		{&MissingDependencyError{Name: "x"}, "missing dependency: x"},
		{&CorruptRecordError{Key: "k"}, `"k"`},
	}
	for _, c := range cases {
		assert.Contains(t, c.err.Error(), c.want)
		assert.Contains(t, c.err.Error(), "rowlock:")
	}
	assert.True(t, errors.As(error(&DeadlockError{}), new(*DeadlockError)))
}
