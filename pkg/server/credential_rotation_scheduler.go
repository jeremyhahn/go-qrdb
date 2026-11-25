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

	// sealed is the node's seal-state probe, the one its transports gate
	// on; a pass is skipped while it reports sealed. Nil means the node has
	// no seal, so every pass runs.
	sealed SealedWindowProbe

	// ticks, when non-nil, replaces the period ticker as run's pass
	// trigger, so a test drives each pass with one send.
	ticks <-chan time.Time

	running atomic.Bool
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

	// A sealed node serves no credential request on any transport, so it
	// claims, rotates and commits nothing either.
	if r.sealed != nil && r.sealed.IsSealed() {
		r.logger.Debug("credential rotation scheduler: node sealed, pass skipped")
		return 0
	}

	rotated, err := r.rotator.CredentialRotateDue(ctx, r.now())
	if err != nil {
		r.logger.Warn("credential rotation scheduler: rotation failed",
			slog.Int("rotated", rotated), slog.Any("error", err))
	}
	return rotated
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
	scheduler := s.newCredentialRotationScheduler(rotator)
	ctx, cancel := context.WithCancel(context.Background())
	s.credentialRotationCancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		scheduler.run(ctx)
	}()
}

// newCredentialRotationScheduler builds the scheduler startCredentialRotationScheduler
// runs: at the resolved period, gated on the seal probe the node's
// transports are given.
func (s *Server) newCredentialRotationScheduler(rotator credentialRotator) *credentialRotationScheduler {
	return &credentialRotationScheduler{
		rotator: rotator,
		period:  s.credentialRotationPeriod(),
		now:     time.Now,
		logger:  s.logger,
		sealed:  s.opts.sealedWindowProbe,
	}
}

// credentialRotationPeriod resolves how often the rotation scheduler runs:
// WithCredentialRotationPeriod when given, else the server configuration's
// seal.credential_rotation_check_period, so every entry point that builds a
// Server from a configuration (qrdbd and the embedded SDK node) honours it,
// else credentials.DefaultRotationCheckPeriod.
func (s *Server) credentialRotationPeriod() time.Duration {
	if s.opts.credentialRotationPeriod > 0 {
		return s.opts.credentialRotationPeriod
	}
	if s.config != nil {
		// Load validates the period, so an error here means a hand-built
		// configuration; the default stands in for it.
		if period, err := s.config.Seal.CredentialRotationCheckPeriodDuration(); err == nil {
			return period
		}
	}
	return credentials.DefaultRotationCheckPeriod
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
