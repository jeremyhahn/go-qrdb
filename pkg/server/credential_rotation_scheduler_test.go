// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// fakeCredentialRotator records each RotateDue call and reports it on calls.
type fakeCredentialRotator struct {
	service.CredentialServicer
	n     atomic.Int64
	err   error
	calls chan time.Time
}

func (f *fakeCredentialRotator) CredentialRotateDue(_ context.Context, now time.Time) (int, error) {
	f.n.Add(1)
	if f.calls != nil {
		f.calls <- now
	}
	return 1, f.err
}

var _ credentialRotator = (*fakeCredentialRotator)(nil)

func newTestCredentialRotationScheduler(r credentialRotator, ticks <-chan time.Time) *credentialRotationScheduler {
	return &credentialRotationScheduler{
		rotator: r,
		period:  time.Hour,
		now:     time.Now,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		ticks:   ticks,
	}
}

func TestCredentialRotationScheduler_TickRunsRotateDue(t *testing.T) {
	ticks := make(chan time.Time)
	rotator := &fakeCredentialRotator{calls: make(chan time.Time, 1)}
	sched := newTestCredentialRotationScheduler(rotator, ticks)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sched.run(ctx)
		close(done)
	}()

	ticks <- time.Now()
	<-rotator.calls
	ticks <- time.Now()
	<-rotator.calls
	cancel()
	<-done

	if got := rotator.n.Load(); got != 2 {
		t.Fatalf("RotateDue calls = %d, want 2", got)
	}
}

func TestCredentialRotationScheduler_PassOnceReportsRotatedOnError(t *testing.T) {
	rotator := &fakeCredentialRotator{err: errors.New("source down")}
	sched := newTestCredentialRotationScheduler(rotator, nil)

	if got := sched.passOnce(context.Background()); got != 1 {
		t.Fatalf("passOnce = %d, want 1", got)
	}
}

// deadlineRecordingRotator records whether the context of a pass carries a
// deadline.
type deadlineRecordingRotator struct {
	service.CredentialServicer
	passes      int
	hasDeadline bool
}

func (d *deadlineRecordingRotator) CredentialRotateDue(ctx context.Context, _ time.Time) (int, error) {
	d.passes++
	_, d.hasDeadline = ctx.Deadline()
	return 0, ctx.Err()
}

// A pass runs under the scheduler's own context: each credential's attempt
// is bounded by the credential service, so a deadline on the whole pass
// would let one slow credential starve those after it.
func TestCredentialRotationScheduler_PassOnceSetsNoDeadline(t *testing.T) {
	rotator := &deadlineRecordingRotator{}
	sched := newTestCredentialRotationScheduler(rotator, nil)

	sched.passOnce(context.Background())
	if rotator.passes != 1 {
		t.Fatalf("passes = %d, want 1", rotator.passes)
	}
	if rotator.hasDeadline {
		t.Fatal("the context passOnce hands the rotator has a deadline, want none")
	}
}

func TestCredentialRotationScheduler_PassOnceSkipsWhenRunning(t *testing.T) {
	rotator := &fakeCredentialRotator{}
	sched := newTestCredentialRotationScheduler(rotator, nil)
	sched.running.Store(true)

	if got := sched.passOnce(context.Background()); got != 0 {
		t.Fatalf("passOnce = %d, want 0", got)
	}
	if rotator.n.Load() != 0 {
		t.Fatal("RotateDue ran while a pass was already running")
	}
}

// TestCredentialRotationScheduler_PassOnceSkipsWhileSealed proves a pass on
// a node whose system barrier is sealed, or is being sealed (held), never
// reaches CredentialRotateDue, and that an unseal (release) lets passes
// rotate again.
func TestCredentialRotationScheduler_PassOnceSkipsWhileSealed(t *testing.T) {
	ctx := context.Background()
	rot := &fakeCredentialRotator{}
	r := newTestCredentialRotationScheduler(rot, nil)
	var sealed atomic.Bool
	r.sealed = sealed.Load

	sealed.Store(true)
	r.passOnce(ctx)
	if got := rot.n.Load(); got != 0 {
		t.Fatalf("sealed barrier: CredentialRotateDue called %d times, want 0", got)
	}

	sealed.Store(false)
	if err := r.hold(ctx); err != nil {
		t.Fatalf("hold: %v", err)
	}
	r.passOnce(ctx)
	if got := rot.n.Load(); got != 0 {
		t.Fatalf("sealing barrier: CredentialRotateDue called %d times, want 0", got)
	}

	r.release(ctx)
	r.passOnce(ctx)
	if got := rot.n.Load(); got != 1 {
		t.Fatalf("unsealed barrier: CredentialRotateDue called %d times, want 1", got)
	}
}

// blockingCredentialRotator parks CredentialRotateDue until release closes.
type blockingCredentialRotator struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingCredentialRotator) CredentialRotateDue(context.Context, time.Time) (int, error) {
	close(b.entered)
	<-b.release
	return 1, nil
}

// TestCredentialRotationScheduler_HoldWaitsForAnInFlightPass proves the
// sealing hook returns only once the pass already rotating has finished, so
// the barrier's key is not wiped under it.
func TestCredentialRotationScheduler_HoldWaitsForAnInFlightPass(t *testing.T) {
	ctx := context.Background()
	rot := &blockingCredentialRotator{entered: make(chan struct{}), release: make(chan struct{})}
	r := newTestCredentialRotationScheduler(rot, nil)

	passDone := make(chan struct{})
	go func() {
		defer close(passDone)
		r.passOnce(ctx)
	}()
	<-rot.entered

	held := make(chan struct{})
	go func() {
		defer close(held)
		_ = r.hold(ctx)
	}()
	select {
	case <-held:
		t.Fatal("hold returned while a pass was still rotating")
	case <-passDone:
		t.Fatal("the pass finished before it was released")
	default:
	}
	close(rot.release)
	<-passDone
	<-held
	if r.running.Load() {
		t.Fatal("running still set after the pass finished")
	}
}

func TestCredentialRotationScheduler_StopEndsGoroutine(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.opts.credentialSvc = &fakeCredentialRotator{}

	s.startCredentialRotationScheduler()
	if s.credentialRotationCancel == nil {
		t.Fatal("scheduler not started for a rotating credential service")
	}
	s.stopCredentialRotationScheduler()
	s.wg.Wait()
	if s.credentialRotationCancel != nil {
		t.Fatal("cancel not cleared on stop")
	}
	// A second stop is a no-op.
	s.stopCredentialRotationScheduler()
}

func TestCredentialRotationScheduler_NoServiceNoScheduler(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	s.startCredentialRotationScheduler()
	if s.credentialRotationCancel != nil {
		t.Fatal("scheduler started without a credential service")
	}
}

func TestWithCredentialRotationPeriod(t *testing.T) {
	var o serverOptions
	WithCredentialRotationPeriod(5 * time.Second)(&o)
	if o.credentialRotationPeriod != 5*time.Second {
		t.Fatalf("period = %v, want 5s", o.credentialRotationPeriod)
	}
	WithCredentialRotationPeriod(0)(&o)
	WithCredentialRotationPeriod(-time.Second)(&o)
	if o.credentialRotationPeriod != 5*time.Second {
		t.Fatalf("a non-positive period changed it to %v", o.credentialRotationPeriod)
	}
	if credentials.DefaultRotationCheckPeriod != time.Minute {
		t.Fatalf("default period = %v, want 1m", credentials.DefaultRotationCheckPeriod)
	}
}
