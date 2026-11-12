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
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
)

// credentialRotator carries out the rotation policies due at now.
// service.CredentialService implements it; it is not a transport verb.
type credentialRotator interface {
	CredentialRotateDue(ctx context.Context, now time.Time) (int, error)
}

// credentialRotationScheduler runs the named credentials' rotation
// policies on every node. It needs no leader gate: RotateDue claims each
// due rotation with a compare-and-swap of the credential's metadata, so
// exactly one node rotates a credential per interval
// (docs/credentials/engineering.md "Scheduled rotation").
type credentialRotationScheduler struct {
	rotator credentialRotator
	period  time.Duration
	now     func() time.Time
	logger  *slog.Logger

	// ticks, when non-nil, replaces the period ticker as run's pass
	// trigger, so a test drives each pass with one send.
	ticks <-chan time.Time

	// sealed, when non-nil, reports whether the node's system barrier is
	// sealed; a pass while it is rotates nothing.
	sealed func() bool

	running atomic.Bool

	// passMu is held for the whole of a pass. hold takes it, so a barrier
	// that starts to seal waits for an in-flight pass to finish, and no
	// pass starts while held is set.
	passMu sync.Mutex
	held   bool
}

// run rotates on the configured period until ctx is cancelled.
func (r *credentialRotationScheduler) run(ctx context.Context) {
	ticks := r.ticks
	if ticks == nil {
		ticker := time.NewTicker(r.period)
		defer ticker.Stop()
		ticks = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			r.passOnce(ctx)
		}
	}
}

// passOnce runs RotateDue once and returns how many credentials it
// rotated. It is a no-op when a pass is already running. The pass runs under
// the scheduler's context with no deadline of its own: the credential
// service bounds each credential's attempt, so a slow one does not starve
// those after it, and Stop ends the pass by cancelling the context.
func (r *credentialRotationScheduler) passOnce(ctx context.Context) int {
	if !r.running.CompareAndSwap(false, true) {
		return 0
	}
	defer r.running.Store(false)

	r.passMu.Lock()
	defer r.passMu.Unlock()
	// A sealed node serves no credential: it neither claims a due policy
	// nor draws a value for one.
	if r.held || (r.sealed != nil && r.sealed()) {
		return 0
	}

	rotated, err := r.rotator.CredentialRotateDue(ctx, r.now())
	if err != nil {
		r.logger.Warn("credential rotation scheduler: rotation failed",
			slog.Int("rotated", rotated), slog.Any("error", err))
	}
	return rotated
}

// hold waits for an in-flight pass to finish and keeps every later pass
// from rotating until release. The system barrier runs it as it starts to
// seal, while the root key is still live.
func (r *credentialRotationScheduler) hold(context.Context) error {
	r.passMu.Lock()
	r.held = true
	r.passMu.Unlock()
	return nil
}

// release lets passes rotate again once the system barrier is unsealed.
func (r *credentialRotationScheduler) release(context.Context) {
	r.passMu.Lock()
	r.held = false
	r.passMu.Unlock()
}

// WithCredentialRotationPeriod overrides how often the credential rotation
// scheduler runs the due rotation policies. A non-positive value keeps
// credentials.DefaultRotationCheckPeriod.
func WithCredentialRotationPeriod(period time.Duration) ServerOption {
	return func(o *serverOptions) {
		if period > 0 {
			o.credentialRotationPeriod = period
		}
	}
}

// startCredentialRotationScheduler starts the scheduler when the server
// has a credential service that can rotate on a schedule.
func (s *Server) startCredentialRotationScheduler() {
	rotator, ok := s.opts.credentialSvc.(credentialRotator)
	if !ok {
		return
	}
	period := s.opts.credentialRotationPeriod
	if period <= 0 {
		period = credentials.DefaultRotationCheckPeriod
	}
	scheduler := &credentialRotationScheduler{
		rotator: rotator,
		period:  period,
		now:     time.Now,
		logger:  s.logger,
	}
	if s.opts.barrierRegistry != nil {
		if sb := s.opts.barrierRegistry.SystemBarrier(); sb != nil {
			scheduler.sealed = sb.IsSealed
			sb.OnSealing(scheduler.hold)
			sb.OnUnsealed(scheduler.release)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.credentialRotationCancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		scheduler.run(ctx)
	}()
}

// stopCredentialRotationScheduler stops the scheduler started by
// startCredentialRotationScheduler. Stop waits for it through the server's
// wait group.
func (s *Server) stopCredentialRotationScheduler() {
	if s.credentialRotationCancel != nil {
		s.credentialRotationCancel()
		s.credentialRotationCancel = nil
	}
}
