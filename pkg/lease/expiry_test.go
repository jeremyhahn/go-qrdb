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
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLessor_RunExpiry_RevokesExpiredOnLeader verifies that the leader-side
// sweeper revokes a lease once it passes its TTL. The minimum TTL is one
// second, so this test waits just over that.
func TestLessor_RunExpiry_RevokesExpiredOnLeader(t *testing.T) {
	lr := NewLessor()
	defer lr.Close()

	l, err := lr.Grant(1)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	var mu sync.Mutex
	var revoked []int64
	revoke := func(_ context.Context, id int64) error {
		mu.Lock()
		revoked = append(revoked, id)
		mu.Unlock()
		// Simulate the consensus-applied revoke removing the lease, so the
		// sweeper does not propose it again on the next tick.
		_, _ = lr.Revoke(id)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go lr.RunExpiry(ctx, 50*time.Millisecond, func() bool { return true }, revoke, nil)

	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(revoked)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(revoked) != 1 || revoked[0] != l.ID {
		t.Fatalf("revoked = %v, want [%d]", revoked, l.ID)
	}
}

// TestLessor_RunExpiry_NoSweepOnFollower verifies the leadership gate: even
// after a lease has expired, a non-leader never proposes a revoke.
func TestLessor_RunExpiry_NoSweepOnFollower(t *testing.T) {
	lr := NewLessor()
	defer lr.Close()

	if _, err := lr.Grant(1); err != nil {
		t.Fatalf("grant: %v", err)
	}

	var called atomic.Bool
	revoke := func(_ context.Context, _ int64) error {
		called.Store(true)
		return nil
	}

	// Run synchronously until the context elapses (well past the 1s TTL), so
	// the lease is expired for most of the window yet must never be revoked.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	lr.RunExpiry(ctx, 50*time.Millisecond, func() bool { return false }, revoke, nil)

	if called.Load() {
		t.Fatal("revoke was called on a follower; lease expiry must run only on the leader")
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
	case <-time.After(time.Second):
		t.Fatal("RunExpiry did not return immediately with a nil revoke callback")
	}
}
