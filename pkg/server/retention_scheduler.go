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

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// DefaultRetentionInterval is how often the retention scheduler sweeps
// time-series metrics for points their policy has expired.
const DefaultRetentionInterval = time.Minute

// retentionPassTimeout bounds the work one pass spends on one store.
const retentionPassTimeout = 30 * time.Second

// scheduledRetentionStore is one store whose metrics the scheduler sweeps.
type scheduledRetentionStore struct {
	// name identifies the store in logs.
	name string
	// ts is the store's time-series service.
	ts service.TimeSeriesServicer
	// leader, when set, gates the store: it is swept only while this node
	// leads its shard, so exactly one node sweeps each store. nil for a
	// store that is this node's alone.
	leader service.ShardLeader
}

// retentionScheduler runs the leader-driven time-series retention sweep --
// the same leader-gated, per-tick pattern as the lease-expiry sweeper
// (pkg/lease.Lessor.RunExpiry) and the compaction scheduler
// (compaction_scheduler.go), applied to time-series retention.
//
// Each pass visits the stores this node may act on -- the same set, and
// the same gating, as the compaction scheduler -- lists every metric with
// a point ever written, and for each metric with a configured retention
// policy proposes one ApplyRetention(metric, cutoff) command, with cutoff
// computed from this node's clock. The proposal is a Raft command
// (protocol.OpTSApplyRetention) applied identically on every replica from
// the cutoff it carries: a replica never consults its own clock, so every
// replica of a group deletes the identical set of points (see
// docs/time-series/engineering.md "Retention enforcement"). Deletion
// within one command is bounded (timeseries.MaxRetentionDeletesPerApply);
// a metric with a larger backlog is swept across several ticks rather than
// in one unbounded Raft entry.
type retentionScheduler struct {
	stores   func(ctx context.Context) []scheduledRetentionStore
	interval time.Duration
	now      func() time.Time
	logger   *slog.Logger

	// defaultRetention is how long a metric with no retention policy, or
	// one whose duration is zero, keeps its points
	// (timeseries.default_retention). Zero keeps them all.
	defaultRetention time.Duration

	// ticks, when non-nil, replaces the interval ticker as run's pass
	// trigger, so a test drives each pass with one send.
	ticks <-chan time.Time

	running atomic.Bool
}

// run sweeps on the configured interval until ctx is cancelled.
func (r *retentionScheduler) run(ctx context.Context) {
	ticks := r.ticks
	if ticks == nil {
		ticker := time.NewTicker(r.interval)
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

// passOnce sweeps every store this node may act on once and returns how
// many metrics it swept. It is a no-op when a pass is already running.
func (r *retentionScheduler) passOnce(ctx context.Context) int {
	if !r.running.CompareAndSwap(false, true) {
		return 0
	}
	defer r.running.Store(false)

	swept := 0
	for _, st := range r.stores(ctx) {
		if st.leader != nil && !st.leader.LeadsShard() {
			continue
		}
		swept += r.runStore(ctx, st)
	}
	return swept
}

// runStore sweeps every metric with a configured retention policy in one
// store.
func (r *retentionScheduler) runStore(ctx context.Context, st scheduledRetentionStore) int {
	sctx, cancel := context.WithTimeout(ctx, retentionPassTimeout)
	defer cancel()

	// ListMetrics is the discovery surface: a metric with a policy but no
	// point ever written has nothing to prune yet, and appears here as
	// soon as its first point lands.
	metrics, err := st.ts.ListMetrics("", 0)
	if err != nil {
		r.logger.Warn("retention scheduler: metric list unreadable",
			slog.String("store", st.name), slog.Any("error", err))
		return 0
	}

	now := r.now()
	swept := 0
	for _, metric := range metrics {
		policy, err := st.ts.GetRetentionPolicy(metric)
		if err != nil {
			continue
		}
		duration := r.retentionFor(policy)
		if duration <= 0 {
			continue
		}
		cutoff := now.Add(-duration)
		if err := st.ts.ApplyRetention(sctx, metric, cutoff); err != nil {
			r.logWriteRefusal(metric, err, st.name)
			continue
		}
		swept++
	}
	return swept
}

// retentionFor is the retention a metric keeps: its own policy's duration,
// or, for a metric with no policy or one whose duration is zero, the
// node's timeseries.default_retention. Zero keeps every point.
func (r *retentionScheduler) retentionFor(policy *service.RetentionPolicy) time.Duration {
	if policy != nil && policy.Duration > 0 {
		return policy.Duration
	}
	return r.defaultRetention
}

// logWriteRefusal logs a refused sweep. A database that was dropped, or
// dropped and created again, refuses through its drop fence: the sweep
// named an incarnation that no longer owns the metric's namespace.
func (r *retentionScheduler) logWriteRefusal(metric string, err error, store string) {
	var dropping *database.DroppingError
	var refused *service.WriteGuardRefusedError
	if errors.As(err, &dropping) || errors.As(err, &refused) {
		r.logger.Debug("retention scheduler: sweep refused by the database's drop fence; skipped",
			slog.String("store", store), slog.String("metric", metric), slog.Any("error", err))
		return
	}
	r.logger.Warn("retention scheduler: sweep failed",
		slog.String("store", store), slog.String("metric", metric), slog.Any("error", err))
}

// retentionStores returns the stores this server's node sweeps retention
// on -- the same set, and the same gating, as compactionStores:
//
//   - each shared bundle's time-series service, gated by leadership of its
//     shard (reported by that bundle's MVCC service, which is bound to the
//     same shard);
//   - each started volume tenant's Host, which is node-local;
//   - each started raft group's time-series service, gated by leadership
//     of the group.
//
// A bundle with no time-series service (the capability not compiled in, or
// not wired for this composition) is left out.
func (s *Server) retentionStores(ctx context.Context) []scheduledRetentionStore {
	var out []scheduledRetentionStore
	add := func(name string, ts service.TimeSeriesServicer, leaderSrc any, gated bool) {
		if ts == nil {
			return
		}
		st := scheduledRetentionStore{name: name, ts: ts}
		if gated {
			leader, ok := leaderSrc.(service.ShardLeader)
			if !ok {
				// A gated store that cannot report leadership is swept by
				// no node rather than by every node.
				return
			}
			st.leader = leader
		}
		out = append(out, st)
	}

	for _, svcs := range s.sharedStoreBundles() {
		add("shared:"+svcs.Name, svcs.TimeSeries, svcs.MVCC, true)
	}
	if s.opts.tenantHosts != nil {
		for tenantID, svcs := range s.opts.tenantHosts.startedServices() {
			if svcs != nil {
				add("tenant host "+tenantID, svcs.TimeSeries, nil, false)
			}
		}
	}
	if s.opts.raftBackend != nil {
		for _, shard := range s.opts.raftBackend.ListStarted() {
			svcs, err := s.opts.raftBackend.Services(ctx, shard.TenantID, shard.Database)
			if err != nil || svcs == nil {
				continue
			}
			add("raft "+tenant.ComposeNamespace(shard.TenantID, shard.Database), svcs.TimeSeries, svcs.MVCC, true)
		}
	}
	return out
}

// WithRetentionInterval overrides how often the retention scheduler sweeps
// time-series metrics for expired points. A non-positive value keeps
// DefaultRetentionInterval.
func WithRetentionInterval(interval time.Duration) ServerOption {
	return func(o *serverOptions) {
		if interval > 0 {
			o.retentionInterval = interval
		}
	}
}

// WithDefaultRetention sets how long the retention scheduler keeps the
// points of a metric with no retention policy, or with a policy whose
// duration is zero (timeseries.default_retention). A metric's own non-zero
// duration always wins. Zero or less, the default, keeps such a metric's
// points for good.
func WithDefaultRetention(retention time.Duration) ServerOption {
	return func(o *serverOptions) {
		if retention > 0 {
			o.defaultRetention = retention
		}
	}
}

// startRetentionScheduler starts the scheduler. It runs on every node;
// each pass acts only on the stores this node leads or holds alone.
func (s *Server) startRetentionScheduler() {
	interval := s.opts.retentionInterval
	if interval <= 0 {
		interval = DefaultRetentionInterval
	}
	scheduler := &retentionScheduler{
		stores:           s.retentionStores,
		interval:         interval,
		now:              time.Now,
		logger:           s.logger,
		defaultRetention: s.opts.defaultRetention,
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.retentionCancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		scheduler.run(ctx)
	}()
}

// stopRetentionScheduler stops the scheduler started by
// startRetentionScheduler. Stop waits for it through the server's wait
// group.
func (s *Server) stopRetentionScheduler() {
	if s.retentionCancel != nil {
		s.retentionCancel()
		s.retentionCancel = nil
	}
}
