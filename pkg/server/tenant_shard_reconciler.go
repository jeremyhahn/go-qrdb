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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// defaultReconcilerPollInterval is the fallback reconciliation period used when
// the placement store's backing KV does not implement placement.Watcher.
const defaultReconcilerPollInterval = 5 * time.Second

// ShardLifecycle manages the lifecycle of dedicated Raft shards. It is satisfied
// by *RaftBackendFactory and is kept as an interface so the reconciler can be
// unit-tested with fake backends.
type ShardLifecycle interface {
	// Attach starts (and caches) the dedicated raft shard for (tenantID, db).
	// It is idempotent: an already-attached shard is a no-op.
	Attach(ctx context.Context, tenantID, db string) error

	// Close stops the named shard and evicts it from the cache. It is a no-op
	// for an unknown (tenantID, db) pair.
	Close(tenantID, db string) error

	// ListStarted returns the set of (TenantID, Database) pairs whose dedicated
	// raft shards are currently started on this node. Used by ReconcileOnce to
	// detect orphans.
	ListStarted() []StartedShard
}

// BarrierReadiness checks whether a tenant's barrier is registered and unsealed.
// It is satisfied by *seal.BarrierRegistry. When the reconciler is wired with a
// BarrierReadiness it defers starting a shard whose barrier is absent/sealed
// rather than propagating the error — the key-delivery (Phase 4c-1c) and a
// later reconcile tick converge.
type BarrierReadiness interface {
	// GetTenant returns the registered TenantBarrier for tenantID, or an error
	// when no barrier is registered.
	GetTenant(tenantID string) (*seal.TenantBarrier, error)
}

// TenantShardReconcilerOption configures TenantShardReconciler behavior.
type TenantShardReconcilerOption func(*TenantShardReconciler)

// WithReconcilerPollInterval overrides the default 5 s poll interval used when
// the placement store does not support Watch.
func WithReconcilerPollInterval(d time.Duration) TenantShardReconcilerOption {
	return func(r *TenantShardReconciler) {
		if d > 0 {
			r.pollInterval = d
		}
	}
}

// WithBarrierReadiness wires a barrier readiness checker so the reconciler
// defers starting a shard when the tenant barrier is absent or sealed — key
// delivery may still be in flight. When nil, the reconciler always attempts to
// start and surfaces any barrier error from Attach.
func WithBarrierReadiness(b BarrierReadiness) TenantShardReconcilerOption {
	return func(r *TenantShardReconciler) { r.barriers = b }
}

// TenantShardReconciler watches the replicated placement table and ensures
// every dedicated Raft shard assigned to this node is started, and every
// locally started shard with no matching placement entry is stopped.
//
// It provides three convergence mechanisms:
//   - Boot re-attach: ReconcileOnce is called immediately in Run to start all
//     assigned shards before any traffic is served.
//   - Watch path: when placement.Store.Watch succeeds, the reconciler reacts
//     to each placement event; an EventPut triggers ensureStarted for the
//     affected (tenant, db), an EventDelete closes any locally started shard.
//   - Poll path: when Watch returns ErrWatchUnsupported (or any error), the
//     reconciler falls back to ReconcileOnce on a configurable ticker (default
//     5 s) until ctx is cancelled.
//
// A shard whose tenant barrier is absent or sealed is silently deferred to the
// next reconcile pass; the 4c-1c key delivery and a later tick converge.
//
// TenantShardReconciler is safe for concurrent use.
type TenantShardReconciler struct {
	placement    *placement.Store
	factory      ShardLifecycle
	selfNodeID   string
	barriers     BarrierReadiness
	pollInterval time.Duration
	logger       *slog.Logger
}

// NewTenantShardReconciler creates a TenantShardReconciler. store and factory
// must be non-nil; selfNodeID must match the NodeID values used in placement
// entries (typically the Raft address string). logger defaults to slog.Default()
// when nil. opts are applied in order.
func NewTenantShardReconciler(
	store *placement.Store,
	factory ShardLifecycle,
	selfNodeID string,
	logger *slog.Logger,
	opts ...TenantShardReconcilerOption,
) *TenantShardReconciler {
	if logger == nil {
		logger = slog.Default()
	}
	r := &TenantShardReconciler{
		placement:    store,
		factory:      factory,
		selfNodeID:   selfNodeID,
		pollInterval: defaultReconcilerPollInterval,
		logger:       logger,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// ReconcileOnce performs a single reconciliation pass:
//  1. Lists the placement table.
//  2. For each placement that assigns a replica to this node, calls ensureStarted
//     (which is idempotent and defers barrier-missing shards gracefully).
//  3. Closes a locally started shard ONLY when a placement entry EXISTS for it
//     but no longer assigns a replica to this node (demotion). Shards with no
//     placement entry are meta-managed (started from meta.json at boot) and are
//     left running — absence from the placement table is not evidence of orphan
//     status (BUG 2 fix).
func (r *TenantShardReconciler) ReconcileOnce(ctx context.Context) {
	placements, err := r.placement.List(ctx)
	if err != nil {
		r.logger.Warn("placement reconciler: List failed",
			slog.Any("error", err))
		return
	}

	// allPlacements tracks every entry in the placement table, regardless of
	// whether this node is an assigned replica. This lets the teardown loop
	// distinguish "placement exists, I'm not in it" (demotion → stop) from
	// "no placement entry" (meta-managed → leave running).
	allPlacements := make(map[raftShardKey]*placement.Placement, len(placements))
	for _, p := range placements {
		key := raftShardKey{tenantID: p.TenantID, database: p.Database}
		allPlacements[key] = p
		if _, ok := p.ReplicaForNode(r.selfNodeID); !ok {
			continue
		}
		r.ensureStarted(ctx, p)
	}

	for _, s := range r.factory.ListStarted() {
		key := raftShardKey{tenantID: s.TenantID, database: s.Database}
		p, hasPlacement := allPlacements[key]
		if !hasPlacement {
			// No placement entry — this shard is meta-managed (started from
			// meta.json at boot or single-node Phase-4b). Do not tear it down.
			continue
		}
		if _, ok := p.ReplicaForNode(r.selfNodeID); ok {
			// Still assigned to this node — leave running.
			continue
		}
		// Placement entry exists but this node is no longer a replica (demotion).
		r.logger.Info("placement reconciler: stopping demoted shard",
			slog.String("tenant", s.TenantID),
			slog.String("database", s.Database))
		if err := r.factory.Close(s.TenantID, s.Database); err != nil {
			r.logger.Warn("placement reconciler: demoted shard stop failed",
				slog.String("tenant", s.TenantID),
				slog.String("database", s.Database),
				slog.Any("error", err))
		}
	}
}

// ensureStarted starts the shard for the placement's locally assigned replica
// when it is not already running and the tenant barrier is ready. If the barrier
// is absent or sealed the start is deferred without error — it will be retried
// on the next reconcile pass.
func (r *TenantShardReconciler) ensureStarted(ctx context.Context, p *placement.Placement) {
	if r.barriers != nil {
		tb, err := r.barriers.GetTenant(p.TenantID)
		if err != nil || tb == nil || tb.IsSealed() {
			r.logger.Debug("placement reconciler: barrier not ready, deferring shard start",
				slog.String("tenant", p.TenantID),
				slog.String("database", p.Database))
			return
		}
	}

	if err := r.factory.Attach(ctx, p.TenantID, p.Database); err != nil {
		r.logger.Warn("placement reconciler: shard start failed",
			slog.String("tenant", p.TenantID),
			slog.String("database", p.Database),
			slog.Any("error", err))
	}
}

// Run drives the reconciler lifecycle. It calls ReconcileOnce immediately
// (boot re-attach pass) then:
//   - when placement.Store.Watch succeeds, reacts to typed events (watch path);
//   - when Watch returns ErrWatchUnsupported or any error, polls with
//     ReconcileOnce on a ticker at r.pollInterval (poll path).
//
// Run blocks until ctx is cancelled.
func (r *TenantShardReconciler) Run(ctx context.Context) {
	r.ReconcileOnce(ctx)

	events, err := r.placement.Watch(ctx)
	if err == nil {
		r.runWatch(ctx, events)
		return
	}
	if !errors.Is(err, placement.ErrWatchUnsupported) {
		r.logger.Warn("placement reconciler: Watch failed, falling back to poll",
			slog.Any("error", err))
	}
	r.runPoll(ctx)
}

// runWatch delivers placement change events to the targeted reconcile handlers
// until ctx is cancelled or the event channel is closed. A closed channel
// transitions to the poll path so the reconciler continues to converge.
//
// M1: a safety ticker fires ReconcileOnce at pollInterval so missed or
// out-of-order watch events do not leave shards in a stale state. The ticker
// also catches demotion that arrived while the reconciler was not watching.
func (r *TenantShardReconciler) runWatch(ctx context.Context, events <-chan placement.Event) {
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				r.logger.Info("placement reconciler: watch channel closed, switching to poll")
				r.runPoll(ctx)
				return
			}
			r.handleEvent(ctx, ev)
		case <-ticker.C:
			r.ReconcileOnce(ctx)
		}
	}
}

// handleEvent processes a single typed placement event.
//
// EventPut: when the updated placement assigns a replica to this node, call
// ensureStarted. When the placement NO LONGER assigns a replica to this node
// (demotion via update) and the shard is locally started, close it (M1).
// EventDelete: close any locally started shard unconditionally.
func (r *TenantShardReconciler) handleEvent(ctx context.Context, ev placement.Event) {
	switch ev.Type {
	case placement.EventPut:
		if ev.Placement == nil {
			break
		}
		if _, ok := ev.Placement.ReplicaForNode(r.selfNodeID); ok {
			r.ensureStarted(ctx, ev.Placement)
			break
		}
		// This node is no longer in the replica set — demotion (M1).
		if err := r.factory.Close(ev.TenantID, ev.Database); err != nil {
			r.logger.Warn("placement reconciler: shard close on demotion failed",
				slog.String("tenant", ev.TenantID),
				slog.String("database", ev.Database),
				slog.Any("error", err))
		}
	case placement.EventDelete:
		if err := r.factory.Close(ev.TenantID, ev.Database); err != nil {
			r.logger.Warn("placement reconciler: shard close on delete failed",
				slog.String("tenant", ev.TenantID),
				slog.String("database", ev.Database),
				slog.Any("error", err))
		}
	}
}

// runPoll calls ReconcileOnce on every pollInterval tick until ctx is cancelled.
func (r *TenantShardReconciler) runPoll(ctx context.Context) {
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.ReconcileOnce(ctx)
		}
	}
}
