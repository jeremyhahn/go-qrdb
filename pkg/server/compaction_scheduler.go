// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// DefaultCompactionInterval is how often the compaction scheduler runs the
// databases' compaction policies.
const DefaultCompactionInterval = time.Minute

// compactionPassTimeout bounds the work one pass spends on one database.
const compactionPassTimeout = 30 * time.Second

// compactionTarget is one database with a compaction policy: its owning
// tenant, its name, and the policy.
type compactionTarget struct {
	Tenant   string
	Database string
	Policy   *compaction.Policy
}

// key identifies the target's revision samples.
func (t compactionTarget) key() string {
	return t.Tenant + "/" + t.Database
}

// revisionSample is a database's current revision as observed at a time.
type revisionSample struct {
	at       time.Time
	revision int64
}

// compactionScheduler runs each database's scheduled-compaction policy. It
// compacts ONLY databases whose owning tenant configured a policy -- nothing
// is compacted implicitly -- and compacts each through the database's own
// namespaced view, resolved for the owning tenant exactly as an explicit
// Compact from that tenant is, so it can never reach another database's or
// another tenant's history. See docs/key-value/engineering.md "Scheduled
// compaction".
type compactionScheduler struct {
	registry service.Registry
	targets  func() []compactionTarget
	interval time.Duration
	now      func() time.Time
	logger   *slog.Logger

	// ticks, when non-nil, replaces the interval ticker as run's pass
	// trigger, so a test drives each pass with one send.
	ticks <-chan time.Time

	running atomic.Bool

	// samples holds each period-policy database's observed revisions,
	// oldest first. Passes never overlap (running), so it needs no lock.
	samples map[string][]revisionSample
}

// run compacts on the configured interval until ctx is cancelled.
func (c *compactionScheduler) run(ctx context.Context) {
	ticks := c.ticks
	if ticks == nil {
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			c.passOnce(ctx)
		}
	}
}

// passOnce runs every configured policy once and returns how many databases
// it compacted. It is a no-op when a pass is already running.
func (c *compactionScheduler) passOnce(ctx context.Context) int {
	if !c.running.CompareAndSwap(false, true) {
		return 0
	}
	defer c.running.Store(false)

	now := c.now()
	live := make(map[string]bool)
	compacted := 0
	for _, t := range c.targets() {
		if !t.Policy.Enabled() {
			continue
		}
		live[t.key()] = true
		if c.compactTarget(ctx, t, now) {
			compacted++
		}
	}
	// Forget the samples of databases that no longer carry a period policy.
	for k := range c.samples {
		if !live[k] {
			delete(c.samples, k)
		}
	}
	return compacted
}

// compactTarget runs one database's policy and reports whether it compacted.
func (c *compactionScheduler) compactTarget(ctx context.Context, t compactionTarget, now time.Time) bool {
	tctx, cancel := context.WithTimeout(tenant.WithTenantID(ctx, t.Tenant), compactionPassTimeout)
	defer cancel()

	mvcc, err := c.registry.MVCCService(t.Tenant, t.Database)
	if err != nil {
		// Sealed, being dropped, or not served here: skipped for this pass,
		// and never compacted on its behalf any other way.
		c.logger.Debug("compaction scheduler: database not resolvable; skipped",
			slog.String("tenant_id", t.Tenant), slog.String("database", t.Database), slog.Any("error", err))
		return false
	}
	current, err := mvcc.CurrentRevision(tctx)
	if err != nil {
		c.logger.Warn("compaction scheduler: current revision unavailable",
			slog.String("tenant_id", t.Tenant), slog.String("database", t.Database), slog.Any("error", err))
		return false
	}

	target := c.target(t, current, now)
	if target < 1 {
		return false
	}
	err = mvcc.Compact(tctx, target)
	var already *service.RevisionCompactedError
	switch {
	case err == nil:
		c.logger.Info("compaction scheduler: compacted database",
			slog.String("tenant_id", t.Tenant), slog.String("database", t.Database),
			slog.String("mode", string(t.Policy.Mode)), slog.Int64("revision", target))
		return true
	case errors.As(err, &already):
		// Another pass or node got there first; nothing to do.
		return false
	default:
		c.logger.Warn("compaction scheduler: compaction failed",
			slog.String("tenant_id", t.Tenant), slog.String("database", t.Database),
			slog.Int64("revision", target), slog.Any("error", err))
		return false
	}
}

// compactionTargeters computes a policy's target revision. A map rather than
// a switch: the modes are values and the dispatch is a lookup.
var compactionTargeters = map[compaction.Mode]func(c *compactionScheduler, t compactionTarget, current int64, now time.Time) int64{
	compaction.Revision: func(_ *compactionScheduler, t compactionTarget, current int64, _ time.Time) int64 {
		return current - t.Policy.RetainRevisions
	},
	compaction.Period: (*compactionScheduler).periodTarget,
}

// target returns the revision t's policy compacts to now, or a value below 1
// when there is nothing to compact yet.
func (c *compactionScheduler) target(t compactionTarget, current int64, now time.Time) int64 {
	targeter, ok := compactionTargeters[t.Policy.Mode]
	if !ok {
		return 0
	}
	return targeter(c, t, current, now)
}

// periodTarget records the database's current revision and returns the
// newest sampled revision at least the policy's period old: everything newer
// than the period is kept. With no sample that old -- a fresh policy, or a
// restart, since samples live in memory -- it returns 0 and nothing is
// compacted: the policy errs toward keeping history.
func (c *compactionScheduler) periodTarget(t compactionTarget, current int64, now time.Time) int64 {
	key := t.key()
	samples := append(c.samples[key], revisionSample{at: now, revision: current})
	cutoff := now.Add(-t.Policy.RetainDuration())

	// The newest sample at or before the cutoff is the target; every older
	// one is no longer needed.
	newest := -1
	for i, s := range samples {
		if s.at.After(cutoff) {
			break
		}
		newest = i
	}
	var target int64
	if newest >= 0 {
		target = samples[newest].revision
		samples = samples[newest:]
	}
	c.samples[key] = samples
	return target
}

// compactionTargets returns the databases in this node's catalog that carry
// an enabled compaction policy, with their owning tenant.
func (s *Server) compactionTargets() []compactionTarget {
	if s.opts.dbManager == nil {
		return nil
	}
	var out []compactionTarget
	for _, info := range s.opts.dbManager.List() {
		policy := info.Compaction
		if !policy.Enabled() {
			continue
		}
		owner := info.TenantID
		if owner == "" {
			owner = tenant.SystemTenantID
		}
		out = append(out, compactionTarget{Tenant: owner, Database: info.Name, Policy: policy})
	}
	return out
}

// WithCompactionInterval overrides how often the compaction scheduler runs
// the databases' compaction policies. A non-positive value keeps
// DefaultCompactionInterval.
func WithCompactionInterval(interval time.Duration) ServerOption {
	return func(o *serverOptions) {
		if interval > 0 {
			o.compactionInterval = interval
		}
	}
}

// startCompactionScheduler starts the scheduler when the server resolves
// databases through a registry and has a database catalog to read policies
// from.
func (s *Server) startCompactionScheduler() {
	if s.opts.dbManager == nil {
		return
	}
	registry := s.buildServiceRegistry()
	if registry == nil {
		return
	}
	interval := s.opts.compactionInterval
	if interval <= 0 {
		interval = DefaultCompactionInterval
	}
	scheduler := &compactionScheduler{
		registry: registry,
		targets:  s.compactionTargets,
		interval: interval,
		now:      time.Now,
		logger:   s.logger,
		samples:  make(map[string][]revisionSample),
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.compactorCancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		scheduler.run(ctx)
	}()
}

// stopCompactionScheduler stops the scheduler started by
// startCompactionScheduler. Stop waits for it through the server's wait
// group.
func (s *Server) stopCompactionScheduler() {
	if s.compactorCancel != nil {
		s.compactorCancel()
		s.compactorCancel = nil
	}
}
