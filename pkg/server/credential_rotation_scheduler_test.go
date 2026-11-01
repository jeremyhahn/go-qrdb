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
