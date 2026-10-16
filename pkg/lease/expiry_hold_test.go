// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package lease

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
)

// recordHoldPolls returns a hold condition that reports held and signals
// every time the sweeper consults it.
func recordHoldPolls(held *atomic.Bool) (hold func() bool, polled <-chan struct{}) {
	ch := make(chan struct{}, 1)
	return func() bool {
		select {
		case ch <- struct{}{}:
		default:
		}
		return held.Load()
	}, ch
}

// revokeRecorder returns a RevokeFunc that records each proposed revoke and
// applies it to lr, as the consensus-applied revoke would.
func revokeRecorder(lr *Lessor) (RevokeFunc, chan int64) {
	revoked := make(chan int64, 4)
	return func(_ context.Context, id int64) error {
		revoked <- id
		_, _ = lr.Revoke(nopWriter{}, id)
		return nil
	}, revoked
}

// TestLessor_RunExpiry_HeldSweepRevokesNothing is the go-qrdb#662 regression
// for the sweeper: a revoke is a proposal, and while the node barrier is
// sealed the sweep must not propose one, however long the lease has been
// expired.
func TestLessor_RunExpiry_HeldSweepRevokesNothing(t *testing.T) {
	fake := clocktest.New()
	lr := NewLessor(WithClock(fake))
	defer lr.Close()

	if _, err := lr.Grant(nopWriter{}, 1); err != nil {
		t.Fatalf("grant: %v", err)
	}
	var held atomic.Bool
	held.Store(true)
	hold, polled := recordHoldPolls(&held)
	revoke, revoked := revokeRecorder(lr)
	stop := startSweeper(t, lr, fake, 50*time.Millisecond, func() bool { return true }, revoke,
		WithExpiryHold(hold))

	fake.Advance(5 * time.Second)
	waitForPoll(t, polled)
	stop()

	if n := len(revoked); n != 0 {
		t.Fatalf("the held sweep proposed %d revokes", n)
	}
}

// TestLessor_RunExpiry_ReleaseGrantsAFullTTL verifies that when the hold is
// released every lease gets a full TTL before the sweep judges it: the holder
// could not renew while the node was held, so it is not revoked for that span,
// and it is revoked once a full TTL passes without a renewal.
func TestLessor_RunExpiry_ReleaseGrantsAFullTTL(t *testing.T) {
	fake := clocktest.New()
	lr := NewLessor(WithClock(fake))
	defer lr.Close()

	l, err := lr.Grant(nopWriter{}, 1)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	var held atomic.Bool
	held.Store(true)
	hold, holdPolled := recordHoldPolls(&held)
	isLeader, leaderPolled := recordLeaderPolls(true)
	revoke, revoked := revokeRecorder(lr)
	stop := startSweeper(t, lr, fake, 50*time.Millisecond, isLeader, revoke, WithExpiryHold(hold))
	defer stop()

	// Expired four seconds over while held.
	fake.Advance(5 * time.Second)
	waitForPoll(t, holdPolled)

	// Release. The next sweep promotes, then judges; a second sweep proves the
	// first one finished without proposing anything.
	held.Store(false)
	for range 2 {
		fake.Advance(50 * time.Millisecond)
		waitForPoll(t, leaderPolled)
	}
	if n := len(revoked); n != 0 {
		t.Fatalf("the sweep revoked %d leases the hold kept from renewing", n)
	}

	// A full TTL with no renewal: now the lease has genuinely lapsed.
	fake.Advance(1100 * time.Millisecond)
	select {
	case id := <-revoked:
		if id != l.ID {
			t.Fatalf("revoked lease %d, want %d", id, l.ID)
		}
	case <-time.After(guard):
		t.Fatal("the lease was never revoked after its post-release TTL")
	}
}

// TestLessor_RunExpiry_NilHoldNeverHolds verifies WithExpiryHold(nil) leaves
// the sweep as it was without the option.
func TestLessor_RunExpiry_NilHoldNeverHolds(t *testing.T) {
	fake := clocktest.New()
	lr := NewLessor(WithClock(fake))
	defer lr.Close()

	if _, err := lr.Grant(nopWriter{}, 1); err != nil {
		t.Fatalf("grant: %v", err)
	}
	revoke, revoked := revokeRecorder(lr)
	stop := startSweeper(t, lr, fake, 50*time.Millisecond, func() bool { return true }, revoke,
		WithExpiryHold(nil))
	defer stop()

	fake.Advance(1100 * time.Millisecond)
	select {
	case <-revoked:
	case <-time.After(guard):
		t.Fatal("a nil hold suspended the sweep")
	}
}

// TestLessor_Promote_ExtendsExpiredLeases verifies promote gives a lapsed
// lease a full TTL from now.
func TestLessor_Promote_ExtendsExpiredLeases(t *testing.T) {
	fake := clocktest.New()
	lr := NewLessor(WithClock(fake))
	defer lr.Close()

	l, err := lr.Grant(nopWriter{}, 2)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	fake.Advance(10 * time.Second)
	if ids := lr.ExpiredLeaseIDs(); len(ids) != 1 || ids[0] != l.ID {
		t.Fatalf("expired = %v, want [%d]", ids, l.ID)
	}

	lr.renewAfterHold()
	if ids := lr.ExpiredLeaseIDs(); len(ids) != 0 {
		t.Fatalf("expired after promote = %v, want none", ids)
	}
	fake.Advance(2100 * time.Millisecond)
	if ids := lr.ExpiredLeaseIDs(); len(ids) != 1 {
		t.Fatalf("expired a full TTL after promote = %v, want [%d]", ids, l.ID)
	}
}

// TestLessor_Promote_NeverShortens verifies promote leaves alone a lease
// whose expiry is already later than a full TTL from now.
func TestLessor_Promote_NeverShortens(t *testing.T) {
	fake := clocktest.New()
	lr := NewLessor(WithClock(fake))
	defer lr.Close()

	far := fake.Now().Add(time.Hour)
	lr.mu.Lock()
	lr.leases[4242] = &lease{id: 4242, ttl: 1, expiry: far, keys: map[string]struct{}{}}
	lr.mu.Unlock()

	lr.renewAfterHold()

	l := lr.leases[4242]
	l.mu.RLock()
	defer l.mu.RUnlock()
	if !l.expiry.Equal(far) {
		t.Fatalf("promote moved expiry to %v, want %v", l.expiry, far)
	}
}
