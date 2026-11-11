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

	"github.com/jeremyhahn/go-qrdb/pkg/config"
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

// testSealProbe is a SealedWindowProbe whose state a test flips.
type testSealProbe struct{ sealed atomic.Bool }

func (p *testSealProbe) IsSealed() bool { return p.sealed.Load() }

// TestCredentialRotationScheduler_SealedNodeSkipsPass: while the node's seal
// probe (the one its transports gate on) reports sealed, a pass calls no
// rotation source, so nothing is claimed, rotated or committed; after the
// unseal the next pass rotates.
func TestCredentialRotationScheduler_SealedNodeSkipsPass(t *testing.T) {
	rotator := &fakeCredentialRotator{calls: make(chan time.Time, 2)}
	sched := newTestCredentialRotationScheduler(rotator, nil)
	probe := &testSealProbe{}
	probe.sealed.Store(true)
	sched.sealed = probe

	if got := sched.passOnce(context.Background()); got != 0 {
		t.Fatalf("sealed pass rotated %d, want 0", got)
	}
	if got := rotator.n.Load(); got != 0 {
		t.Fatalf("sealed pass made %d RotateDue calls, want 0", got)
	}

	probe.sealed.Store(false)
	sched.passOnce(context.Background())
	if got := rotator.n.Load(); got != 1 {
		t.Fatalf("unsealed pass made %d RotateDue calls, want 1", got)
	}
}

// TestCredentialRotationScheduler_StartWiresTheSealProbe: the scheduler a
// Server starts gates on the same probe its transports are given.
func TestCredentialRotationScheduler_StartWiresTheSealProbe(t *testing.T) {
	probe := &testSealProbe{}
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.opts.sealedWindowProbe = probe
	sched := s.newCredentialRotationScheduler(&fakeCredentialRotator{})
	if sched.sealed != SealedWindowProbe(probe) {
		t.Fatal("scheduler is not gated on the server's seal probe")
	}
}

// TestCredentialRotationPeriod_FromConfiguration: a Server built from a
// configuration (as both qrdbd and the embedded SDK node build theirs) runs
// the scheduler at seal.credential_rotation_check_period; the
// WithCredentialRotationPeriod option overrides it, and with neither the
// default applies.
func TestCredentialRotationPeriod_FromConfiguration(t *testing.T) {
	cfg := &config.Config{}
	cfg.Seal.CredentialRotationCheckPeriod = "250ms"
	s := &Server{config: cfg, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if got := s.newCredentialRotationScheduler(&fakeCredentialRotator{}).period; got != 250*time.Millisecond {
		t.Fatalf("scheduler period from configuration = %v, want 250ms", got)
	}

	WithCredentialRotationPeriod(3 * time.Second)(&s.opts)
	if got := s.newCredentialRotationScheduler(&fakeCredentialRotator{}).period; got != 3*time.Second {
		t.Fatalf("period with the option = %v, want 3s", got)
	}

	if got := (&Server{}).credentialRotationPeriod(); got != credentials.DefaultRotationCheckPeriod {
		t.Fatalf("period with no configuration = %v, want the default", got)
	}
}
