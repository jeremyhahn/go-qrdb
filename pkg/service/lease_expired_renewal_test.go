// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

const expiredRenewalGuard = 5 * time.Second

// expiringPrimary is a lease primary whose expiry loop has judged a 3s lease
// expired and proposed its revoke. The proposal's apply waits on stall, as on
// a stalled disk; revokeErr, when set, makes the proposal fail instead.
type expiringPrimary struct {
	lr       *lease.Lessor
	clk      *clocktest.Fake
	id       int64
	leading  atomic.Bool
	ticks    chan struct{}
	stall    chan struct{}
	proposed chan struct{}
}

func startExpiringPrimary(t *testing.T, revokeErr error) *expiringPrimary {
	t.Helper()
	p := &expiringPrimary{
		clk:      clocktest.NewAt(time.Unix(1_000_000, 0)),
		ticks:    make(chan struct{}, 16),
		stall:    make(chan struct{}),
		proposed: make(chan struct{}, 16),
	}
	p.lr = lease.NewLessor(lease.WithClock(p.clk))
	l, err := p.lr.Grant(nopLeaseWriter{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	p.id = l.ID
	p.leading.Store(true)

	isLeader := func() bool {
		v := p.leading.Load()
		p.ticks <- struct{}{}
		return v
	}
	revoke := func(ctx context.Context, id int64) error {
		p.proposed <- struct{}{}
		if revokeErr != nil {
			return revokeErr
		}
		select {
		case <-p.stall:
		case <-ctx.Done():
			return ctx.Err()
		}
		_, err := p.lr.Revoke(nopLeaseWriter{}, id)
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.lr.RunExpiry(ctx, time.Second, isLeader, revoke, nil)
	}()
	t.Cleanup(func() {
		cancel()
		arrive(t, done, "the expiry loop stopping")
	})

	p.awaitTick(t) // the start-up test
	armCtx, cancelArm := context.WithTimeout(ctx, expiredRenewalGuard)
	defer cancelArm()
	if err := p.clk.BlockUntil(armCtx, 1); err != nil {
		t.Fatalf("the sweeper never armed its ticker: %v", err)
	}
	p.clk.Advance(4 * time.Second)
	p.awaitTick(t)
	select {
	case <-p.proposed:
	case <-time.After(expiredRenewalGuard):
		t.Fatal("the expiry loop never proposed the revoke")
	}
	return p
}

// arrive receives from ch, failing after expiredRenewalGuard with the step
// that did not arrive: a test of this file fails rather than hangs.
func arrive[T any](t *testing.T, ch <-chan T, step string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(expiredRenewalGuard):
		t.Fatalf("%s never arrived", step)
	}
}

func (p *expiringPrimary) awaitTick(t *testing.T) {
	t.Helper()
	select {
	case <-p.ticks:
	case <-time.After(expiredRenewalGuard):
		t.Fatal("the sweeper never consulted its leadership test")
	}
}

// host answers the keep-alive read the way the lease state machine's Lookup
// does, from the lessor, and records whether anything was proposed.
func (p *expiringPrimary) host(read chan<- struct{}, proposed *atomic.Bool) *mockHost {
	return &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			q, ok := query.(LeaseKeepAliveQuery)
			if !ok {
				return nil, errors.New("lease test: unexpected query")
			}
			l, err := p.lr.RenewLocal(q.ID)
			read <- struct{}{}
			if err != nil {
				return nil, err
			}
			return l, nil
		},
		syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
			proposed.Store(true)
			return sm.Result{
				Value: uint64(p.id),
				Data:  protocol.EncodeLeaseInfo(p.id, 3, 3, nil),
			}, nil
		},
	}
}

type nopLeaseWriter struct{}

func (nopLeaseWriter) Set([]byte, []byte) error { return nil }
func (nopLeaseWriter) Delete([]byte) error      { return nil }

type keepAliveAnswer struct {
	lease *Lease
	err   error
}

func keepAliveAsync(svc LeaseServicer, id int64) <-chan keepAliveAnswer {
	answer := make(chan keepAliveAnswer, 1)
	go func() {
		l, err := svc.KeepAlive(context.Background(), id)
		answer <- keepAliveAnswer{lease: l, err: err}
	}()
	return answer
}

func receiveKeepAlive(t *testing.T, answer <-chan keepAliveAnswer) keepAliveAnswer {
	t.Helper()
	select {
	case a := <-answer:
		return a
	case <-time.After(expiredRenewalGuard):
		t.Fatal("KeepAlive never answered")
		return keepAliveAnswer{}
	}
}

// TestLeaseService_KeepAlive_ExpiredLeaseAnswersNotFoundOnceItsStalledRevokeApplies
// is main run 55593: a keep-alive reaches the primary after the lease's
// deadline, while its revoke is proposed and held back by a stalled disk. It
// must not answer that the lease lives on; it answers that the lease is gone
// once the revoke applies, and proposes nothing that could revive it.
func TestLeaseService_KeepAlive_ExpiredLeaseAnswersNotFoundOnceItsStalledRevokeApplies(t *testing.T) {
	p := startExpiringPrimary(t, nil)
	read := make(chan struct{}, 4)
	var proposed atomic.Bool
	svc := NewLeaseService(p.host(read, &proposed), 1)

	answer := keepAliveAsync(svc, p.id)
	select {
	case <-read:
	case <-time.After(expiredRenewalGuard):
		t.Fatal("KeepAlive never read the lease")
	}
	select {
	case a := <-answer:
		t.Fatalf("KeepAlive answered (%+v, %v) while the lease's revoke was in flight", a.lease, a.err)
	default:
	}

	close(p.stall)
	a := receiveKeepAlive(t, answer)
	var lnf *LeaseNotFoundError
	if !errors.As(a.err, &lnf) || lnf.LeaseID != p.id {
		t.Fatalf("KeepAlive after the revoke applied = (%+v, %v), want *LeaseNotFoundError for %d", a.lease, a.err, p.id)
	}
	if proposed.Load() {
		t.Fatal("KeepAlive proposed a renewal of a revoked lease")
	}
}

// TestLeaseService_KeepAlive_ExpiredLeaseRenewsThroughTheLogWhenThePrimaryStandsDown:
// the replica that refused the renewal stops expiring leases before the
// revoke lands, so the next primary decides and the renewal goes to the log.
func TestLeaseService_KeepAlive_ExpiredLeaseRenewsThroughTheLogWhenThePrimaryStandsDown(t *testing.T) {
	p := startExpiringPrimary(t, errors.New("lease test: revoke proposal lost"))
	read := make(chan struct{}, 4)
	var proposed atomic.Bool
	svc := NewLeaseService(p.host(read, &proposed), 1)

	answer := keepAliveAsync(svc, p.id)
	select {
	case <-read:
	case <-time.After(expiredRenewalGuard):
		t.Fatal("KeepAlive never read the lease")
	}
	if proposed.Load() {
		t.Fatal("KeepAlive went to the log before the primary stood down")
	}

	p.leading.Store(false)
	p.clk.Advance(time.Second)
	p.awaitTick(t)
	a := receiveKeepAlive(t, answer)
	if a.err != nil {
		t.Fatalf("KeepAlive after the primary stood down: %v", a.err)
	}
	if !proposed.Load() || a.lease.ID != p.id {
		t.Fatalf("KeepAlive = %+v, proposed %v; want the lease renewed through the log", a.lease, proposed.Load())
	}
}

// TestLeaseService_KeepAlive_ExpiredLeaseWaitEndsWithTheCaller: a caller that
// gives up while the revoke is stalled gets a read error carrying its
// context's error, not a renewal.
func TestLeaseService_KeepAlive_ExpiredLeaseWaitEndsWithTheCaller(t *testing.T) {
	p := startExpiringPrimary(t, nil)
	t.Cleanup(func() { close(p.stall) })
	read := make(chan struct{}, 4)
	var proposed atomic.Bool
	svc := NewLeaseService(p.host(read, &proposed), 1)

	ctx, cancel := context.WithCancel(context.Background())
	answer := make(chan error, 1)
	go func() {
		_, err := svc.KeepAlive(ctx, p.id)
		answer <- err
	}()
	arrive(t, read, "KeepAlive reading the lease")
	cancel()
	select {
	case err := <-answer:
		var re *ReadError
		if !errors.As(err, &re) || !errors.Is(err, context.Canceled) {
			t.Fatalf("KeepAlive after cancel = %v, want *ReadError wrapping context.Canceled", err)
		}
	case <-time.After(expiredRenewalGuard):
		t.Fatal("KeepAlive did not end with its caller")
	}
	if proposed.Load() {
		t.Fatal("KeepAlive proposed after its caller gave up")
	}
}
