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
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// expiredLessor returns a primary lessor holding one lease of ttl seconds
// whose deadline the fake clock has already passed.
func expiredLessor(t *testing.T, ttl int64) (*Lessor, int64) {
	t.Helper()
	clk := newFakeClock()
	var leading atomic.Bool
	leading.Store(true)
	lr := leaderLessor(t, clk, &leading)
	lr.beginPrimary()
	l, err := lr.Grant(nopWriter{}, ttl)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Duration(ttl+1) * time.Second)
	return lr, l.ID
}

// renewExpired calls RenewLocal on an expired lease and returns the
// *ExpiredError it must answer.
func renewExpired(t *testing.T, lr *Lessor, id int64) *ExpiredError {
	t.Helper()
	got, err := lr.RenewLocal(id)
	var expired *ExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("RenewLocal on an expired lease = (%+v, %v), want *ExpiredError", got, err)
	}
	if expired.ID != id {
		t.Fatalf("ExpiredError.ID = %d, want %d", expired.ID, id)
	}
	return expired
}

// awaitAsync runs Await on its own goroutine and returns where its answer
// arrives.
func awaitAsync(ctx context.Context, expired *ExpiredError) <-chan error {
	answer := make(chan error, 1)
	go func() { answer <- expired.Await(ctx) }()
	return answer
}

// arrive receives from ch, failing after guard with the step that did not
// arrive: a test of this file fails rather than hangs.
func arrive[T any](t *testing.T, ch <-chan T, step string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(guard):
		t.Fatalf("%s never arrived", step)
	}
}

func requireUnanswered(t *testing.T, answer <-chan error) {
	t.Helper()
	select {
	case err := <-answer:
		t.Fatalf("Await answered %v before the lease's fate was known", err)
	default:
	}
}

func receive(t *testing.T, answer <-chan error) error {
	t.Helper()
	select {
	case err := <-answer:
		return err
	case <-time.After(guard):
		t.Fatal("Await never answered")
		return nil
	}
}

func TestRenewLocal_ExpiredLeaseIsNotRenewed(t *testing.T) {
	lr, id := expiredLessor(t, 3)

	renewExpired(t, lr, id)
	if ids := lr.ExpiredLeaseIDs(); len(ids) != 1 || ids[0] != id {
		t.Fatalf("refused renewal moved the deadline: expired = %v, want [%d]", ids, id)
	}
	if _, err := lr.RenewLocal(id); err == nil {
		t.Fatal("a second renewal of the expired lease succeeded")
	}
}

func TestRenewLocal_ExpiredCheckpointedLeaseAwaitsTooRatherThanGoingToTheLog(t *testing.T) {
	lr, id := expiredLessor(t, 3)
	lr.leases[id].checkpointTTL = 2

	renewExpired(t, lr, id)
}

// TestRunExpiry_RenewalOfAnExpiringLeaseAwaitsItsStalledRevoke is the
// overtaking of main run 55593: the expiry loop has judged the lease and
// proposed its revoke, which a stalled disk holds back, and a keep-alive
// arrives meanwhile. The renewal must answer that the lease is gone once the
// revoke applies, never that it lives on.
func TestRunExpiry_RenewalOfAnExpiringLeaseAwaitsItsStalledRevoke(t *testing.T) {
	clk := newFakeClock()
	lr := NewLessor(WithClock(clk))
	l, err := lr.Grant(nopWriter{}, 3)
	if err != nil {
		t.Fatal(err)
	}

	proposed := make(chan int64, 1)
	stalled := make(chan struct{})
	revoke := func(ctx context.Context, id int64) error {
		proposed <- id
		select {
		case <-stalled:
		case <-ctx.Done():
			return ctx.Err()
		}
		_, err := lr.Revoke(nopWriter{}, id)
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		lr.RunExpiry(ctx, time.Second, func() bool { return true }, revoke, nil)
	}()
	defer func() {
		cancel()
		arrive(t, done, "the expiry loop stopping")
	}()
	armCtx, cancelArm := context.WithTimeout(ctx, guard)
	defer cancelArm()
	if err := clk.BlockUntil(armCtx, 1); err != nil {
		t.Fatalf("the sweeper never armed its ticker: %v", err)
	}

	clk.Advance(4 * time.Second)
	select {
	case id := <-proposed:
		if id != l.ID {
			t.Fatalf("revoked lease %d, want %d", id, l.ID)
		}
	case <-time.After(guard):
		t.Fatal("the expiry loop never proposed the revoke")
	}

	expired := renewExpired(t, lr, l.ID)
	answer := awaitAsync(context.Background(), expired)
	requireUnanswered(t, answer)

	close(stalled)
	var nf *NotFoundError
	if err := receive(t, answer); !errors.As(err, &nf) || nf.ID != l.ID {
		t.Fatalf("Await after the revoke applied = %v, want *NotFoundError for %d", err, l.ID)
	}
}

func TestRunExpiry_StandingDownWakesARenewalAwaitingAnExpiredLease(t *testing.T) {
	clk := newFakeClock()
	lr := NewLessor(WithClock(clk))
	l, err := lr.Grant(nopWriter{}, 3)
	if err != nil {
		t.Fatal(err)
	}

	var leading atomic.Bool
	leading.Store(true)
	isLeader, calls := observedLeader(&leading)
	proposed := make(chan struct{}, 4)
	revoke := func(context.Context, int64) error {
		proposed <- struct{}{}
		return errors.New("lease test: revoke proposal lost")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		lr.RunExpiry(ctx, time.Second, isLeader, revoke, nil)
	}()
	defer func() {
		cancel()
		arrive(t, done, "the expiry loop stopping")
	}()
	awaitLeaderCall(t, calls) // the start-up test
	armCtx, cancelArm := context.WithTimeout(ctx, guard)
	defer cancelArm()
	if err := clk.BlockUntil(armCtx, 1); err != nil {
		t.Fatalf("the sweeper never armed its ticker: %v", err)
	}

	clk.Advance(4 * time.Second)
	awaitLeaderCall(t, calls)
	arrive(t, proposed, "the expiry loop proposing the revoke")

	expired := renewExpired(t, lr, l.ID)
	answer := awaitAsync(context.Background(), expired)
	requireUnanswered(t, answer)

	leading.Store(false)
	clk.Advance(time.Second)
	awaitLeaderCall(t, calls)
	var np *NotPrimaryError
	if err := receive(t, answer); !errors.As(err, &np) {
		t.Fatalf("Await after the loop stood down = %v, want *NotPrimaryError", err)
	}
}

func TestExpiredError_AwaitAnswersNotPrimaryOnClose(t *testing.T) {
	lr, id := expiredLessor(t, 3)
	answer := awaitAsync(context.Background(), renewExpired(t, lr, id))
	requireUnanswered(t, answer)

	lr.Close()
	var np *NotPrimaryError
	if err := receive(t, answer); !errors.As(err, &np) {
		t.Fatalf("Await after Close = %v, want *NotPrimaryError", err)
	}
}

func TestExpiredError_NoPrimaryLoopAnswersNotPrimaryAtOnce(t *testing.T) {
	lr, id := expiredLessor(t, 3)
	lr.endPrimary()

	err := renewExpired(t, lr, id).Await(context.Background())
	var np *NotPrimaryError
	if !errors.As(err, &np) {
		t.Fatalf("Await with no primary loop = %v, want *NotPrimaryError", err)
	}
}

func TestExpiredError_AwaitAnswersTheContextWhenItEndsFirst(t *testing.T) {
	lr, id := expiredLessor(t, 3)
	ctx, cancel := context.WithCancel(context.Background())
	answer := awaitAsync(ctx, renewExpired(t, lr, id))
	requireUnanswered(t, answer)

	cancel()
	err := receive(t, answer)
	var rae *RenewAwaitError
	if !errors.As(err, &rae) || rae.ID != id || !errors.Is(err, context.Canceled) {
		t.Fatalf("Await after cancel = %v, want *RenewAwaitError wrapping context.Canceled", err)
	}
	if rae.Error() == "" {
		t.Fatal("RenewAwaitError has an empty message")
	}
}

func TestPrimary_BeginAndEndAreIdempotent(t *testing.T) {
	lr := NewLessor()
	lr.endPrimary()
	lr.beginPrimary()
	first := lr.demoted()
	lr.beginPrimary()
	if lr.demoted() != first {
		t.Fatal("a second beginPrimary replaced the open demotec")
	}
	lr.endPrimary()
	lr.endPrimary()
	select {
	case <-first:
	default:
		t.Fatal("endPrimary left demotec open")
	}
}

// TestRestore_KeepsTheRevokeChannelOfASurvivingLease restores a snapshot that
// keeps one expired lease and drops another, each with a renewal awaiting it.
func TestRestore_KeepsTheRevokeChannelOfASurvivingLease(t *testing.T) {
	clk := newFakeClock()
	var leading atomic.Bool
	leading.Store(true)
	lr := leaderLessor(t, clk, &leading)
	lr.beginPrimary()
	st := memory.NewStorage()
	kept, err := lr.Grant(st, 3)
	if err != nil {
		t.Fatal(err)
	}
	dropped, err := lr.Grant(st, 3)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(4 * time.Second)
	keptAnswer := awaitAsync(context.Background(), renewExpired(t, lr, kept.ID))
	droppedAnswer := awaitAsync(context.Background(), renewExpired(t, lr, dropped.ID))

	// The snapshot is the table after a revoke this replica never applied.
	other := NewLessor()
	if err := other.Restore(st); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Revoke(st, dropped.ID); err != nil {
		t.Fatal(err)
	}
	if err := lr.Restore(st); err != nil {
		t.Fatal(err)
	}

	var nf *NotFoundError
	if err := receive(t, droppedAnswer); !errors.As(err, &nf) || nf.ID != dropped.ID {
		t.Fatalf("Await on the lease the snapshot dropped = %v, want *NotFoundError", err)
	}
	requireUnanswered(t, keptAnswer)
	if _, err := lr.Revoke(st, kept.ID); err != nil {
		t.Fatal(err)
	}
	if err := receive(t, keptAnswer); !errors.As(err, &nf) || nf.ID != kept.ID {
		t.Fatalf("Await on the restored lease after its revoke = %v, want *NotFoundError", err)
	}
}

func TestLease_CloseRevokedToleratesALeaseWithoutAChannel(t *testing.T) {
	(&lease{}).closeRevoked()
}
