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

// guard bounds every wait on the sweeper so a broken one fails the test rather
// than hanging it. It is a failure ceiling, never a value a test depends on:
// the tests drive the sweep by advancing a fake clock, not by waiting.
const guard = 5 * time.Second

// startSweeper runs RunExpiry on its own goroutine and returns once it has armed
// its ticker, so the caller's first Advance cannot land before the sweeper is
// listening for it. The returned stop cancels the sweeper and waits for it to
// return.
func startSweeper(t *testing.T, lr *Lessor, fake *clocktest.Fake, interval time.Duration, isLeader func() bool, revoke RevokeFunc) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		lr.RunExpiry(ctx, interval, isLeader, revoke, nil)
	}()

	armCtx, cancelArm := context.WithTimeout(ctx, guard)
	defer cancelArm()
	if err := fake.BlockUntil(armCtx, 1); err != nil {
		cancel()
		t.Fatalf("the sweeper never armed its ticker: %v", err)
	}
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(guard):
			t.Fatal("RunExpiry did not return after its context was canceled")
		}
	}
}

// TestLessor_RunExpiry_RevokesExpiredOnLeader verifies that the leader-side
// sweeper revokes a lease once it passes its TTL. The sweep is driven by
// advancing the lessor's clock past the one-second minimum TTL.
func TestLessor_RunExpiry_RevokesExpiredOnLeader(t *testing.T) {
	fake := clocktest.New()
	lr := NewLessor(WithClock(fake))
	defer lr.Close()

	l, err := lr.Grant(nopWriter{}, 1)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	revoked := make(chan int64, 4)
	revoke := func(_ context.Context, id int64) error {
		revoked <- id
		// Simulate the consensus-applied revoke removing the lease, so the
		// sweeper does not propose it again on the next tick.
		_, _ = lr.Revoke(nopWriter{}, id)
		return nil
	}
	stop := startSweeper(t, lr, fake, 50*time.Millisecond, func() bool { return true }, revoke)

	fake.Advance(1100 * time.Millisecond)

	select {
	case id := <-revoked:
		if id != l.ID {
			t.Fatalf("revoked lease %d, want %d", id, l.ID)
		}
	case <-time.After(guard):
		t.Fatal("the expired lease was never revoked")
	}

	// A second sweep finds nothing left to revoke.
	fake.Advance(time.Second)
	stop()
	if extra := len(revoked); extra != 0 {
		t.Fatalf("the sweeper proposed %d further revokes for a lease already gone", extra)
	}
}

// TestLessor_RunExpiry_LeavesAnUnexpiredLeaseAlone verifies the sweeper does not
// revoke a lease whose TTL has not run out, however many ticks pass.
func TestLessor_RunExpiry_LeavesAnUnexpiredLeaseAlone(t *testing.T) {
	fake := clocktest.New()
	lr := NewLessor(WithClock(fake))
	defer lr.Close()

	if _, err := lr.Grant(nopWriter{}, 10); err != nil {
		t.Fatalf("grant: %v", err)
	}

	var revoked atomic.Bool
	polled := make(chan struct{}, 16)
	isLeader := func() bool {
		polled <- struct{}{}
		return true
	}
	stop := startSweeper(t, lr, fake, time.Second, isLeader, func(context.Context, int64) error {
		revoked.Store(true)
		return nil
	})

	fake.Advance(9 * time.Second)
	waitForPoll(t, polled)
	stop()

	if revoked.Load() {
		t.Fatal("a lease with a second of its TTL left was revoked")
	}
}

// TestLessor_RunExpiry_NoSweepOnFollower verifies the leadership gate: even
// after a lease has expired, a non-leader never proposes a revoke.
func TestLessor_RunExpiry_NoSweepOnFollower(t *testing.T) {
	fake := clocktest.New()
	lr := NewLessor(WithClock(fake))
	defer lr.Close()

	if _, err := lr.Grant(nopWriter{}, 1); err != nil {
		t.Fatalf("grant: %v", err)
	}

	var called atomic.Bool
	polled := make(chan struct{}, 16)
	isLeader := func() bool {
		polled <- struct{}{}
		return false
	}
	stop := startSweeper(t, lr, fake, 50*time.Millisecond, isLeader, func(context.Context, int64) error {
		called.Store(true)
		return nil
	})

	// Well past the 1s TTL, so the lease is expired when the sweeper looks.
	fake.Advance(2 * time.Second)
	waitForPoll(t, polled)
	stop()

	if called.Load() {
		t.Fatal("revoke was called on a follower; lease expiry must run only on the leader")
	}
}

// TestLessor_RunExpiry_ZeroIntervalUsesTheDefault verifies a non-positive
// interval ticks at DefaultExpiryInterval and no faster.
func TestLessor_RunExpiry_ZeroIntervalUsesTheDefault(t *testing.T) {
	fake := clocktest.New()
	lr := NewLessor(WithClock(fake))
	defer lr.Close()

	polled := make(chan struct{}, 16)
	isLeader := func() bool {
		polled <- struct{}{}
		return true
	}
	stop := startSweeper(t, lr, fake, 0, isLeader, func(context.Context, int64) error { return nil })
	defer stop()

	fake.Advance(DefaultExpiryInterval - time.Millisecond)
	select {
	case <-polled:
		t.Fatal("the sweeper ticked before DefaultExpiryInterval elapsed")
	default:
	}

	fake.Advance(time.Millisecond)
	waitForPoll(t, polled)
}

func waitForPoll(t *testing.T, polled <-chan struct{}) {
	t.Helper()
	select {
	case <-polled:
	case <-time.After(guard):
		t.Fatal("the sweeper never ran a sweep")
	}
}

// TestLessor_RunExpiry_DisabledWithNilCallbacks verifies RunExpiry returns
// immediately (no goroutine leak, no panic) when wiring is incomplete.
func TestLessor_RunExpiry_DisabledWithNilCallbacks(t *testing.T) {
	lr := NewLessor()
	defer lr.Close()

	done := make(chan struct{})
	go func() {
		// nil revoke -> immediate return even with a never-canceled context.
		lr.RunExpiry(context.Background(), time.Second, func() bool { return true }, nil, nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(guard):
		t.Fatal("RunExpiry did not return immediately with a nil revoke callback")
	}
}
