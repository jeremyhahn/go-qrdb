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
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
)

// newFakeClock is a manually advanced clock for the Lessor's clock seam.
func newFakeClock() *clocktest.Fake { return clocktest.NewAt(time.Unix(1_000_000, 0)) }

func leaderLessor(t *testing.T, clk *clocktest.Fake, leading *atomic.Bool) *Lessor {
	t.Helper()
	lr := NewLessor(WithClock(clk))
	isLeader := leading.Load
	lr.leader.Store(&isLeader)
	return lr
}

func TestRenewLocal_ExtendsDeadlineWithoutTheLog(t *testing.T) {
	clk := newFakeClock()
	var leading atomic.Bool
	leading.Store(true)
	lr := leaderLessor(t, clk, &leading)
	l, err := lr.Grant(nopWriter{}, 10)
	if err != nil {
		t.Fatal(err)
	}

	clk.Advance(8 * time.Second)
	got, err := lr.RenewLocal(l.ID)
	if err != nil {
		t.Fatalf("RenewLocal: %v", err)
	}
	if got.RemainingTTL != 10 {
		t.Fatalf("remaining after renewal = %d, want 10", got.RemainingTTL)
	}
	clk.Advance(9 * time.Second)
	if ids := lr.ExpiredLeaseIDs(); len(ids) != 0 {
		t.Fatalf("lease expired 9s after a renewal with a 10s TTL: %v", ids)
	}
	clk.Advance(2 * time.Second)
	if ids := lr.ExpiredLeaseIDs(); len(ids) != 1 {
		t.Fatalf("lease did not expire 11s after its last renewal: %v", ids)
	}
}

func TestRenewLocal_RefusedWhenNotPrimary(t *testing.T) {
	clk := newFakeClock()
	var leading atomic.Bool
	lr := leaderLessor(t, clk, &leading)
	l, _ := lr.Grant(nopWriter{}, 10)

	clk.Advance(5 * time.Second)
	var np *NotPrimaryError
	if _, err := lr.RenewLocal(l.ID); !errors.As(err, &np) {
		t.Fatalf("follower RenewLocal error = %v, want *NotPrimaryError", err)
	}
	if got := lr.Lookup(l.ID).RemainingTTL; got != 5 {
		t.Fatalf("refused renewal moved the deadline: remaining %d, want 5", got)
	}

	bare := NewLessor()
	b, _ := bare.Grant(nopWriter{}, 10)
	if _, err := bare.RenewLocal(b.ID); !errors.As(err, &np) {
		t.Fatalf("RenewLocal without RunExpiry error = %v, want *NotPrimaryError", err)
	}
}

func TestRenewLocal_UnknownAndClosed(t *testing.T) {
	clk := newFakeClock()
	var leading atomic.Bool
	leading.Store(true)
	lr := leaderLessor(t, clk, &leading)

	var nf *NotFoundError
	if _, err := lr.RenewLocal(42); !errors.As(err, &nf) || nf.ID != 42 {
		t.Fatalf("unknown lease error = %v, want *NotFoundError{42}", err)
	}
	lr.Close()
	var closed *LessorClosedError
	if _, err := lr.RenewLocal(42); !errors.As(err, &closed) {
		t.Fatalf("closed lessor error = %v, want *LessorClosedError", err)
	}
}

func TestTimeToLive_FollowerRefusesItsStaleDeadline(t *testing.T) {
	clk := newFakeClock()
	var leading, following atomic.Bool
	leading.Store(true)
	primary := leaderLessor(t, clk, &leading)
	follower := leaderLessor(t, clk, &following)
	l, err := primary.Grant(nopWriter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := follower.Grant(nopWriter{}, 10); err != nil {
		t.Fatal(err)
	}

	clk.Advance(8 * time.Second)
	if _, err := primary.RenewLocal(l.ID); err != nil {
		t.Fatalf("RenewLocal: %v", err)
	}
	got, err := primary.TimeToLive(l.ID)
	if err != nil {
		t.Fatalf("primary TimeToLive: %v", err)
	}
	if got.RemainingTTL != 10 {
		t.Fatalf("primary remaining = %d, want 10", got.RemainingTTL)
	}

	var stale *StaleTTLError
	if _, err := follower.TimeToLive(l.ID); !errors.As(err, &stale) {
		t.Fatalf("follower TimeToLive error = %v, want *StaleTTLError", err)
	}
	if stale.Lease == nil || stale.Lease.ID != l.ID || stale.Lease.TTL != 10 {
		t.Fatalf("StaleTTLError lease = %+v, want lease %d with TTL 10", stale.Lease, l.ID)
	}
}

func TestTimeToLive_ExistenceIsEveryReplicasToAnswer(t *testing.T) {
	clk := newFakeClock()
	var following atomic.Bool
	follower := leaderLessor(t, clk, &following)
	var nf *NotFoundError
	if _, err := follower.TimeToLive(42); !errors.As(err, &nf) || nf.ID != 42 {
		t.Fatalf("follower unknown lease error = %v, want *NotFoundError{42}", err)
	}

	bare := NewLessor(WithClock(clk))
	l, _ := bare.Grant(nopWriter{}, 10)
	clk.Advance(3 * time.Second)
	got, err := bare.TimeToLive(l.ID)
	if err != nil {
		t.Fatalf("TimeToLive without RunExpiry: %v", err)
	}
	if got.RemainingTTL != 7 {
		t.Fatalf("remaining = %d, want 7", got.RemainingTTL)
	}
	if _, err := bare.TimeToLive(43); !errors.As(err, &nf) || nf.ID != 43 {
		t.Fatalf("unknown lease error = %v, want *NotFoundError{43}", err)
	}
}

func TestKeepAlive_NeverShortensADeadline(t *testing.T) {
	clk := newFakeClock()
	lr := NewLessor(WithClock(clk))
	l, _ := lr.Grant(nopWriter{}, 10)
	lr.Promote(20 * time.Second) // deadline = now + 10s + 20s

	clk.Advance(time.Second)
	got, err := lr.KeepAlive(nopWriter{}, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RemainingTTL < 28 {
		t.Fatalf("KeepAlive shortened the promoted deadline: remaining %ds, want >= 28", got.RemainingTTL)
	}
}

func TestPromote_DeadlineIsAtLeastTTLPlusGrace(t *testing.T) {
	clk := newFakeClock()
	lr := NewLessor(WithClock(clk))
	short, _ := lr.Grant(nopWriter{}, 2)
	long, _ := lr.Grant(nopWriter{}, 100)

	clk.Advance(1500 * time.Millisecond) // short has 0.5s left, long has 98.5s
	lr.Promote(5 * time.Second)

	if got := lr.Lookup(short.ID).RemainingTTL; got != 7 { // 2s TTL + 5s grace
		t.Fatalf("short lease remaining = %d, want 7", got)
	}
	if got := lr.Lookup(long.ID).RemainingTTL; got != 105 { // 100s TTL + 5s grace
		t.Fatalf("long lease remaining = %d, want 105", got)
	}
	clk.Advance(6900 * time.Millisecond)
	if ids := lr.ExpiredLeaseIDs(); len(ids) != 0 {
		t.Fatalf("lease expired inside its grace: %v", ids)
	}
	clk.Advance(200 * time.Millisecond)
	if ids := lr.ExpiredLeaseIDs(); len(ids) != 1 || ids[0] != short.ID {
		t.Fatalf("expired = %v, want only the short lease", ids)
	}
}

func TestPromote_ZeroGraceStillGivesAFullTTL(t *testing.T) {
	clk := newFakeClock()
	lr := NewLessor(WithClock(clk))
	l, _ := lr.Grant(nopWriter{}, 10)
	clk.Advance(9 * time.Second)
	lr.Promote(0)
	if got := lr.Lookup(l.ID).RemainingTTL; got != 10 {
		t.Fatalf("remaining = %d, want 10", got)
	}
}

func TestPromote_CountsFromTheCheckpoint(t *testing.T) {
	clk := newFakeClock()
	lr := NewLessor(WithClock(clk))
	l, _ := lr.Grant(nopWriter{}, 600)
	if err := checkpointNow(lr, nopWriter{}, []Checkpoint{{ID: l.ID, RemainingTTL: 100}}); err != nil {
		t.Fatal(err)
	}

	clk.Advance(550 * time.Second) // 50s left on this replica's own deadline
	lr.Promote(5 * time.Second)
	if got := lr.Lookup(l.ID).RemainingTTL; got != 105 { // 100s checkpoint + 5s grace
		t.Fatalf("remaining = %d, want 105", got)
	}

	// A deadline already beyond the checkpoint is kept.
	lr.Promote(0)
	if got := lr.Lookup(l.ID).RemainingTTL; got != 105 {
		t.Fatalf("Promote shortened a deadline: remaining %d, want 105", got)
	}
}

func TestPromote_DropsThePendingMarks(t *testing.T) {
	clk := newFakeClock()
	var leading atomic.Bool
	leading.Store(true)
	lr := leaderLessor(t, clk, &leading)
	l, _ := lr.Grant(nopWriter{}, 600)
	lr.dueCheckpoints(1, time.Minute, false)

	var ce *CheckpointedError
	if _, err := lr.RenewLocal(l.ID); !errors.As(err, &ce) {
		t.Fatalf("RenewLocal with a checkpoint pending = %v, want *CheckpointedError", err)
	}
	lr.Promote(0)
	if _, err := lr.RenewLocal(l.ID); err != nil {
		t.Fatalf("RenewLocal after Promote: %v", err)
	}
}

// observedLeader wraps leading in a leadership test that reports every call on
// the returned channel. RunExpiry asks once when it starts and once per tick,
// so receiving from it is how a test knows the sweeper reached a tick: the
// tick before it has been fully processed by then.
func observedLeader(leading *atomic.Bool) (func() bool, <-chan bool) {
	calls := make(chan bool, 16)
	return func() bool {
		v := leading.Load()
		calls <- v
		return v
	}, calls
}

// awaitLeaderCall receives one leadership test from calls, failing after guard.
func awaitLeaderCall(t *testing.T, calls <-chan bool) bool {
	t.Helper()
	select {
	case v := <-calls:
		return v
	case <-time.After(guard):
		t.Fatal("the sweeper never consulted its leadership test")
		return false
	}
}

// awaitRemaining waits, without moving the clock, until lease id has want
// seconds left: the change another goroutine makes to the lease's deadline is
// the only signal that it has made it. guard bounds the wait.
func awaitRemaining(t *testing.T, lr *Lessor, id int64, want int64) {
	t.Helper()
	deadline := time.Now().Add(guard)
	for {
		got := lr.Lookup(id).RemainingTTL
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lease %d has %ds left, want %d", id, got, want)
		}
		runtime.Gosched()
	}
}

func TestRunExpiry_PromotesOnTheLeaderEdgeOnly(t *testing.T) {
	clk := newFakeClock()
	lr := NewLessor(WithClock(clk))
	l, _ := lr.Grant(nopWriter{}, 10)

	var leading atomic.Bool
	isLeader, calls := observedLeader(&leading)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		lr.RunExpiry(ctx, time.Second, isLeader,
			func(context.Context, int64) error { return nil }, nil,
			WithFailoverGrace(30*time.Second))
	}()
	awaitLeaderCall(t, calls) // the start-up test
	armCtx, cancelArm := context.WithTimeout(ctx, guard)
	defer cancelArm()
	if err := clk.BlockUntil(armCtx, 1); err != nil {
		t.Fatalf("the sweeper never armed its ticker: %v", err)
	}

	// Two follower ticks: the second call proves the first tick is done.
	for range 2 {
		clk.Advance(time.Second)
		awaitLeaderCall(t, calls)
	}
	if got := lr.Lookup(l.ID).RemainingTTL; got != 8 {
		t.Fatalf("a follower's deadline moved: remaining %d, want 8", got)
	}

	// The leader edge promotes: TTL + grace from the tick that saw it. The
	// loop promotes after its leadership test returns, so the test waits for
	// the promotion itself before it moves the clock again.
	leading.Store(true)
	clk.Advance(time.Second)
	awaitLeaderCall(t, calls)
	awaitRemaining(t, lr, l.ID, 40)

	// The next tick is no edge, and does not promote again.
	clk.Advance(time.Second)
	awaitLeaderCall(t, calls)
	clk.Advance(time.Second)
	awaitLeaderCall(t, calls) // proves the previous tick is done
	if got := lr.Lookup(l.ID).RemainingTTL; got != 38 {
		t.Fatalf("lease promoted again: remaining %d, want 38 (10s TTL + 30s grace, two ticks before)", got)
	}
	cancel()
	<-done
	var np *NotPrimaryError
	if _, err := lr.RenewLocal(l.ID); !errors.As(err, &np) {
		t.Fatalf("RenewLocal after RunExpiry returned error = %v, want *NotPrimaryError", err)
	}
}

func TestRunExpiry_CheckpointsLongLeasesWhileLeading(t *testing.T) {
	clk := newFakeClock()
	lr := NewLessor(WithClock(clk))
	long, _ := lr.Grant(nopWriter{}, 60)
	short, _ := lr.Grant(nopWriter{}, 1)

	var leading atomic.Bool
	leading.Store(true)
	cp := newApplyingCheckpointer(lr)
	cp.proposed = make(chan []Checkpoint, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		lr.RunExpiry(ctx, time.Hour, leading.Load,
			func(context.Context, int64) error { return nil }, nil,
			WithCheckpoint(time.Second, cp))
	}()
	defer func() { cancel(); <-done }()

	armCtx, cancelArm := context.WithTimeout(ctx, guard)
	defer cancelArm()
	if err := clk.BlockUntil(armCtx, 2); err != nil {
		t.Fatalf("the sweeper never armed its tickers: %v", err)
	}
	clk.Advance(time.Second)
	select {
	case cps := <-cp.proposed:
		if len(cps) != 1 || cps[0] != (Checkpoint{ID: long.ID, RemainingTTL: 59}) {
			t.Fatalf("checkpointed %v, want only lease %d at 59s (lease %d is too short)", cps, long.ID, short.ID)
		}
	case <-time.After(guard):
		t.Fatal("the long lease was never checkpointed")
	}
}

// TestRunExpiry_PromotesOnlyOnceThePreviousTermIsApplied: a replica that has
// just become leader may not yet have applied the checkpoint its predecessor
// committed. Promoting then would count the lease from its full TTL, handing
// it more time than it was renewed for, so the loop promotes only once
// AwaitApplied returns -- here, once the checkpoint has been applied -- and
// tries again on the next tick when it cannot.
func TestRunExpiry_PromotesOnlyOnceThePreviousTermIsApplied(t *testing.T) {
	clk := newFakeClock()
	lr := NewLessor(WithClock(clk))
	l, _ := lr.Grant(nopWriter{}, 60)
	clk.Advance(40 * time.Second)

	cp := newApplyingCheckpointer()
	awaits := 0
	var beforePromotion int64
	cp.await = func() error {
		awaits++
		if awaits == 1 {
			return &NotPrimaryError{}
		}
		beforePromotion = lr.Lookup(l.ID).RemainingTTL
		return checkpointNow(lr, nopWriter{}, []Checkpoint{{ID: l.ID, RemainingTTL: 20}})
	}

	var leading atomic.Bool
	isLeader, calls := observedLeader(&leading)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		lr.RunExpiry(ctx, time.Second, isLeader,
			func(context.Context, int64) error { return nil }, nil,
			WithCheckpoint(time.Hour, cp), WithFailoverGrace(30*time.Second))
	}()
	defer func() { cancel(); <-done }()
	awaitLeaderCall(t, calls) // the start-up test
	armCtx, cancelArm := context.WithTimeout(ctx, guard)
	defer cancelArm()
	if err := clk.BlockUntil(armCtx, 2); err != nil {
		t.Fatalf("the sweeper never armed its tickers: %v", err)
	}

	// Three leader ticks: the first cannot await the previous term, the
	// second promotes, and the third's call proves the second is done.
	leading.Store(true)
	for range 3 {
		clk.Advance(time.Second)
		awaitLeaderCall(t, calls)
	}
	// The leader calls are buffered, so the clock may have moved one tick on
	// by the time the loop reads it: each bound allows that second. A
	// promotion from the full TTL would read 88 or more.
	if beforePromotion < 17 || beforePromotion > 18 {
		t.Fatalf("remaining %d after the failed await, want the follower's 17-18: it promoted", beforePromotion)
	}
	if got := lr.Lookup(l.ID).RemainingTTL; got < 49 || got > 50 {
		t.Fatalf("remaining %d after promotion, want 49-50 (the 20s checkpointed + 30s grace)", got)
	}
}

func TestWithCheckpoint_DefaultsANonPositiveInterval(t *testing.T) {
	var c expiryConfig
	WithCheckpoint(0, newApplyingCheckpointer())(&c)
	if c.checkpointInterval != DefaultCheckpointInterval {
		t.Fatalf("interval = %v, want %v", c.checkpointInterval, DefaultCheckpointInterval)
	}
	if DefaultCheckpointInterval != 5*time.Minute {
		t.Fatalf("DefaultCheckpointInterval = %v, want etcd's 5m", DefaultCheckpointInterval)
	}
}
