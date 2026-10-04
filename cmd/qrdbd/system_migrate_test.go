// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func validSystemMigrateOpts() seal.MigrationOptions {
	return seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(context.Background(), []byte("daemon-migrate-pw")),
	}
}

// TestDaemonSystemMigrator_EnqueuesAndReportsOutcome proves a ScopeSystem
// request is handed to the boot loop over migrateCh and the caller blocks until
// the loop reports the recycle outcome.
func TestDaemonSystemMigrator_EnqueuesAndReportsOutcome(t *testing.T) {
	ch := make(chan *systemMigrationRequest, 1)
	m := newDaemonSystemMigrator(ch, true)

	// Stand-in boot loop: receive the request and report success.
	go func() {
		req := <-ch
		if req.opts.TargetStrategy != seal.StrategyPassphrase {
			t.Errorf("boot loop got strategy %q", req.opts.TargetStrategy)
		}
		req.done <- nil
	}()

	err := m.EnableEncryption(context.Background(),
		service.BarrierScope{Kind: service.ScopeSystem}, validSystemMigrateOpts())
	if err != nil {
		t.Fatalf("EnableEncryption: %v", err)
	}
}

// TestDaemonSystemMigrator_PropagatesRecodeError proves a recycle failure
// reported by the boot loop propagates back to the caller.
func TestDaemonSystemMigrator_PropagatesRecodeError(t *testing.T) {
	ch := make(chan *systemMigrationRequest, 1)
	m := newDaemonSystemMigrator(ch, true)
	sentinel := errors.New("recode boom")

	go func() {
		req := <-ch
		req.done <- sentinel
	}()

	err := m.EnableEncryption(context.Background(),
		service.BarrierScope{Kind: service.ScopeSystem}, validSystemMigrateOpts())
	if !errors.Is(err, sentinel) {
		t.Fatalf("EnableEncryption error = %v, want %v", err, sentinel)
	}
}

// TestDaemonSystemMigrator_InProgressRejected proves a second system migration
// while one is pending (channel full, boot loop busy) is rejected rather than
// queued.
func TestDaemonSystemMigrator_InProgressRejected(t *testing.T) {
	ch := make(chan *systemMigrationRequest, 1)
	m := newDaemonSystemMigrator(ch, true)

	// Occupy the single-slot channel with no consumer: the boot loop is "busy".
	// This exercises the channel-full fallback in EnableEncryption's select
	// (reached here because the channel was filled directly, bypassing the
	// inProgress CompareAndSwap); TestDaemonSystemMigrator_ConcurrentRequestRejected
	// below covers the case this fallback alone cannot: a second request
	// arriving after the boot loop has already dequeued the first.
	ch <- &systemMigrationRequest{opts: validSystemMigrateOpts(), done: make(chan error, 1)}

	err := m.EnableEncryption(context.Background(),
		service.BarrierScope{Kind: service.ScopeSystem}, validSystemMigrateOpts())
	if !errors.Is(err, seal.ErrMigrationInProgress) {
		t.Fatalf("EnableEncryption error = %v, want seal.ErrMigrationInProgress", err)
	}
}

// TestDaemonSystemMigrator_ConcurrentRequestRejected is the go-qrdb#550
// regression test: the in-progress guard must cover the ENTIRE migration
// lifetime, from acceptance to terminal outcome, not merely channel
// occupancy. It proves a second request made AFTER a stand-in boot loop has
// already dequeued the first (channel empty) but BEFORE the first's
// migration has terminated is still rejected, and that the guard clears once
// the first genuinely completes (a third request then succeeds).
func TestDaemonSystemMigrator_ConcurrentRequestRejected(t *testing.T) {
	ch := make(chan *systemMigrationRequest, 1)
	m := newDaemonSystemMigrator(ch, true)

	dequeued := make(chan struct{})
	proceed := make(chan struct{})
	go func() {
		req := <-ch // boot loop "receives": the channel is empty from here on
		close(dequeued)
		<-proceed // hold the migration "in progress" until the test says go
		req.done <- nil
	}()

	firstErrCh := make(chan error, 1)
	go func() {
		firstErrCh <- m.EnableEncryption(context.Background(),
			service.BarrierScope{Kind: service.ScopeSystem}, validSystemMigrateOpts())
	}()

	// Every blocking receive in this test is bounded: a regression that
	// drops the first request, never dequeues it, or never completes it
	// must fail this test promptly rather than hang until the suite-wide
	// timeout (go-qrdb#550 review).
	const waitBound = 2 * time.Second

	select {
	case <-dequeued: // channel is empty now; only the in-progress flag still says busy
	case <-time.After(waitBound):
		t.Fatal("stand-in boot loop never dequeued the first request")
	}

	// Bounded so a missing/broken guard fails this test promptly instead of
	// hanging until the suite timeout: without the guard this call would
	// enqueue behind a boot loop stand-in that only ever reads once, and
	// EnableEncryption would block forever on a req.done nobody sends.
	secondCtx, cancel := context.WithTimeout(context.Background(), waitBound)
	defer cancel()
	err := m.EnableEncryption(secondCtx,
		service.BarrierScope{Kind: service.ScopeSystem}, validSystemMigrateOpts())
	if !errors.Is(err, seal.ErrMigrationInProgress) {
		t.Fatalf("concurrent request error = %v, want seal.ErrMigrationInProgress", err)
	}

	close(proceed)
	select {
	case err := <-firstErrCh:
		if err != nil {
			t.Fatalf("first request: %v", err)
		}
	case <-time.After(waitBound):
		t.Fatal("first request never completed after proceed was closed")
	}

	// The guard clears on the first request's terminal outcome: a third
	// request, after a fresh stand-in boot loop, now succeeds.
	go func() {
		req := <-ch
		req.done <- nil
	}()
	thirdCtx, cancel2 := context.WithTimeout(context.Background(), waitBound)
	defer cancel2()
	if err := m.EnableEncryption(thirdCtx,
		service.BarrierScope{Kind: service.ScopeSystem}, validSystemMigrateOpts()); err != nil {
		t.Fatalf("third request after completion: %v", err)
	}
}

// TestDaemonSystemMigrator_CancelledCallerDoesNotReleaseGuardEarly is the
// go-qrdb#550 round-4 regression test for the ctx.Done() branch specifically
// (TestDaemonSystemMigrator_ConcurrentRequestRejected above never cancels a
// caller's context, so it cannot see this path at all). A caller can stop
// waiting on EnableEncryption while its migration keeps running server-side;
// the in-progress guard must clear only on that migration's OWN terminal
// outcome, never merely because the caller gave up. Replacing the deferred
// release goroutine with an immediate inProgress.Store(false) on
// ctx.Done() -- a tempting "simplification" -- passes every other test in
// this file and fails only this one, by allowing a second, equally
// destructive migration to start while the first is still recoding the data
// plane.
func TestDaemonSystemMigrator_CancelledCallerDoesNotReleaseGuardEarly(t *testing.T) {
	ch := make(chan *systemMigrationRequest, 1)
	m := newDaemonSystemMigrator(ch, true)

	const waitBound = 2 * time.Second

	dequeued := make(chan struct{})
	proceed := make(chan struct{})
	go func() {
		req := <-ch // boot loop "receives": the migration is now running server-side
		close(dequeued)
		<-proceed // hold the migration "in progress" until the test says go
		req.done <- nil
	}()

	firstCtx, firstCancel := context.WithCancel(context.Background())
	firstErrCh := make(chan error, 1)
	go func() {
		firstErrCh <- m.EnableEncryption(firstCtx,
			service.BarrierScope{Kind: service.ScopeSystem}, validSystemMigrateOpts())
	}()

	select {
	case <-dequeued:
	case <-time.After(waitBound):
		t.Fatal("stand-in boot loop never dequeued the first request")
	}

	// Cancel the FIRST caller while its migration is still running
	// server-side (proceed has not been closed yet).
	firstCancel()
	select {
	case err := <-firstErrCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first request error = %v, want context.Canceled", err)
		}
	case <-time.After(waitBound):
		t.Fatal("first request never returned after its context was cancelled")
	}

	// The migration is STILL running server-side (proceed is still open).
	// A second request must be refused because the guard has not seen the
	// first's TERMINAL outcome yet -- not merely because a request happens
	// to be sitting in the channel. An implementation that clears
	// inProgress immediately on ctx.Done() fails exactly here, since by
	// this point the channel is already empty and nothing else marks the
	// migration as still running except the guard itself.
	secondCtx, secondCancel := context.WithTimeout(context.Background(), waitBound)
	defer secondCancel()
	if err := m.EnableEncryption(secondCtx,
		service.BarrierScope{Kind: service.ScopeSystem}, validSystemMigrateOpts()); !errors.Is(err, seal.ErrMigrationInProgress) {
		t.Fatalf("second request (while the cancelled first is still running) error = %v, want seal.ErrMigrationInProgress", err)
	}

	// Let the first migration reach its terminal outcome.
	close(proceed)

	// The guard clears asynchronously -- a background goroutine drains
	// req.done and clears inProgress once the cancelled caller is no longer
	// waiting on it -- so there is no single instant at which a third
	// request is guaranteed to succeed. Poll for it, bounded, with a fresh
	// stand-in boot loop already waiting to serve the moment it is
	// accepted.
	go func() {
		req := <-ch
		req.done <- nil
	}()
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		return m.EnableEncryption(ctx,
			service.BarrierScope{Kind: service.ScopeSystem}, validSystemMigrateOpts()) == nil
	}, waitBound, 20*time.Millisecond,
		"in-progress guard never cleared after the cancelled first migration's terminal outcome")
}

// TestDaemonSystemMigrator_NonSystemScopeRejected proves the daemon system
// migrator rejects a non-system scope (the scoped composite must route tenant
// scope elsewhere, never here).
func TestDaemonSystemMigrator_NonSystemScopeRejected(t *testing.T) {
	m := newDaemonSystemMigrator(make(chan *systemMigrationRequest, 1), true)
	err := m.EnableEncryption(context.Background(),
		service.BarrierScope{Kind: service.ScopeTenant, TenantID: "acme"}, validSystemMigrateOpts())
	if !errors.Is(err, service.ErrMigrationProviderUnavailable) {
		t.Fatalf("tenant scope error = %v, want ErrMigrationProviderUnavailable", err)
	}
}

// TestDaemonSystemMigrator_SealDisabledRejected proves a system-scope request
// is rejected, before ever being enqueued, on a migrator constructed with
// sealEnabled=false (mirrors a daemon started with seal.enabled: false).
// Regression test for go-qrdb#550: this is what keeps a seal-disabled node's
// gRPC/Unix enable-encryption route from ever enqueueing a request the boot
// loop cannot service (handleSystemMigration's sealSub is nil in that state).
func TestDaemonSystemMigrator_SealDisabledRejected(t *testing.T) {
	ch := make(chan *systemMigrationRequest, 1)
	m := newDaemonSystemMigrator(ch, false)

	err := m.EnableEncryption(context.Background(),
		service.BarrierScope{Kind: service.ScopeSystem}, validSystemMigrateOpts())
	if !errors.Is(err, service.ErrMigrationProviderUnavailable) {
		t.Fatalf("EnableEncryption error = %v, want ErrMigrationProviderUnavailable", err)
	}
	select {
	case <-ch:
		t.Fatal("a seal-disabled request must not be enqueued")
	case <-time.After(50 * time.Millisecond):
	}
}

// TestDaemonSystemMigrator_InvalidOptsRejectedBeforeEnqueue proves option
// validation runs before the request reaches the boot loop (nothing is enqueued
// for invalid input).
func TestDaemonSystemMigrator_InvalidOptsRejectedBeforeEnqueue(t *testing.T) {
	ch := make(chan *systemMigrationRequest, 1)
	m := newDaemonSystemMigrator(ch, true)

	// StrategyNone is not a valid migration target.
	bad := validSystemMigrateOpts()
	bad.TargetStrategy = seal.StrategyNone
	err := m.EnableEncryption(context.Background(),
		service.BarrierScope{Kind: service.ScopeSystem}, bad)
	if err == nil {
		t.Fatal("invalid opts must be rejected")
	}
	select {
	case <-ch:
		t.Fatal("invalid request must not be enqueued")
	case <-time.After(50 * time.Millisecond):
	}
}

// TestHandleSystemMigration_NilSealSubsystem_ReturnsTypedError is the
// go-qrdb#550 regression test for the defensive guard: handleSystemMigration
// must return a typed error and report it on req.done, not panic on
// sealSub.SealBarrier, when reached with a nil seal subsystem. In production
// daemonSystemMigrator.EnableEncryption already prevents this by rejecting a
// seal-disabled request before enqueueing (see
// TestDaemonSystemMigrator_SealDisabledRejected); this test calls
// handleSystemMigration directly so the guard is verified independently of
// that earlier check.
func TestHandleSystemMigration_NilSealSubsystem_ReturnsTypedError(t *testing.T) {
	req := &systemMigrationRequest{
		opts: validSystemMigrateOpts(),
		done: make(chan error, 1),
	}

	reboot, err := handleSystemMigration(req, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	if reboot {
		t.Error("reboot = true, want false")
	}
	var sealDisabled *SystemMigrationSealDisabledError
	if !errors.As(err, &sealDisabled) {
		t.Fatalf("err = %v (%T), want *SystemMigrationSealDisabledError", err, err)
	}
	select {
	case doneErr := <-req.done:
		if !errors.As(doneErr, &sealDisabled) {
			t.Fatalf("req.done error = %v (%T), want *SystemMigrationSealDisabledError", doneErr, doneErr)
		}
	default:
		t.Fatal("req.done was never signaled")
	}
}

// TestBuildSysMigrator_PinsSealConfigEnabled is the go-qrdb#550 wiring-pin
// test: buildSysMigrator's sealEnabled must come from f.sealConfig.Enabled,
// not a constant. A future edit to runStart that hardcodes true (or false)
// when calling buildSysMigrator fails this test rather than surfacing only
// as the go-qrdb#550 panic under gRPC/Unix load on a live seal-disabled
// daemon.
func TestBuildSysMigrator_PinsSealConfigEnabled(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		f := &startFlags{sealConfig: qrdbconfig.SealConfig{Enabled: enabled}}
		m := buildSysMigrator(f, make(chan *systemMigrationRequest, 1))
		if m.sealEnabled != enabled {
			t.Errorf("f.sealConfig.Enabled = %v: buildSysMigrator's sealEnabled = %v, want %v",
				enabled, m.sealEnabled, enabled)
		}
	}
}

// TestKeyedSystemStrategy covers the keyed-strategy selection used by the
// system recode. Only a PASSPHRASE target yields a self-contained sealing
// strategy; a Shamir target is minted through recodeSystemBarrier's
// MintKeyedShamirSibling branch (a SealingStrategy cannot represent Shamir's
// ceremony), so keyedSystemStrategy rejects both Shamir and None.
func TestKeyedSystemStrategy(t *testing.T) {
	s, err := keyedSystemStrategy(seal.MigrationOptions{TargetStrategy: seal.StrategyPassphrase})
	if err != nil || s == nil {
		t.Fatalf("keyedSystemStrategy(passphrase) = %v, %v", s, err)
	}
	for _, id := range []seal.StrategyID{seal.StrategyShamir, seal.StrategyNone} {
		if _, err := keyedSystemStrategy(seal.MigrationOptions{TargetStrategy: id}); err == nil {
			t.Fatalf("keyedSystemStrategy(%q) must error (not a self-contained keyed strategy)", id)
		}
	}
}
