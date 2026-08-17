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

	// CloseAndPurge stops the named shard (as Close does) and removes its
	// on-disk dataDir subtree. It is a no-op for an unknown (tenantID, db)
	// pair.
	CloseAndPurge(tenantID, db string) error

	// ListStarted returns the set of (TenantID, Database) pairs whose dedicated
	// raft shards are currently started on this node. Used by ReconcileOnce to
	// detect orphans.
	ListStarted() []StartedShard

	// StartedAsObserver reports whether the locally running shard for
	// (tenantID, db) was started with the observer role latched into its
	// local raft configuration (false when the shard is not started). Used
	// by ReconcileOnce to restart a replica the leader has promoted to a
	// voting member — go-quicraft latches the local role for the life of
	// the in-process replica, so agreement with the promoted membership can
	// only be restored by a restart.
	StartedAsObserver(tenantID, db string) bool
}

// JoinObserver reports whether a locally-started dynamically-added replica
// has finished its join bootstrap (received its first snapshot). Satisfied
// by *RaftBackendFactory, which delegates to *raftbackend.HostAdapter. The
// reconciler uses this to promote a placement entry's local replica from
// ReplicaStateObserver to ReplicaStateObserverReady once the join bootstrap
// window has closed, so a LATER restart of that same replica starts with
// join=false instead of re-entering the join bootstrap window (the exact
// no-data-loss-restart bug this promotion prevents). Reaching
// ReplicaStateObserverReady does NOT yet make the replica a voting member —
// see Promoter for the ObserverReady -> Active (voter) transition, which
// only the shard's current leader may perform.
type JoinObserver interface {
	// IsShardJoining reports whether shardID is still in its join bootstrap
	// window on this host: started locally with join=true and not yet given
	// its first snapshot.
	IsShardJoining(shardID uint64) bool
}

// Promoter promotes a caught-up (ReplicaStateObserverReady) non-voting
// observer replica to full voting membership, and reports which replica
// currently leads a shard. Satisfied by a thin wrapper over the shared
// quicraft.Host (see raftPromoter). PromoteObserver is a thin wrapper
// around the same RequestAddNode config change used to admit a founding
// follower -- go-quicraft treats RequestAddNode targeting an EXISTING
// observer's replica ID as an observer -> voter promotion rather than a
// fresh add (see go-quicraft host.go's requestConfigChange pre-registration
// comment). Only the shard's current Raft leader may successfully call
// PromoteObserver; go-quicraft hard-rejects the underlying config change on
// any other member, so the reconciler always confirms local leadership via
// LeaderReplicaID first.
type Promoter interface {
	// LeaderReplicaID reports whether a leader is known for shardID and, if
	// so, its replica ID.
	LeaderReplicaID(ctx context.Context, shardID uint64) (id uint64, ok bool, err error)

	// PromoteObserver promotes replicaID (currently a non-voting observer of
	// shardID, reachable at raftAddr) to a full voting member. Blocks until
	// the config change commits or ctx is cancelled.
	PromoteObserver(ctx context.Context, shardID, replicaID uint64, raftAddr string) error
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

// WithJoinObserver wires the JoinObserver the reconciler polls to promote a
// locally hosted ReplicaStateObserver replica to ReplicaStateObserverReady
// once its join bootstrap window has closed. When nil (the default),
// promotion never happens and a dynamically added replica's placement entry
// stays Observer forever — safe (every restart keeps retrying join=true)
// but never converges to the cheaper join=false restart path, and never
// becomes eligible for voter promotion (see WithPromoter).
func WithJoinObserver(o JoinObserver) TenantShardReconcilerOption {
	return func(r *TenantShardReconciler) { r.joinObserver = o }
}

// WithPromoter wires the Promoter the reconciler uses to promote a
// caught-up (ReplicaStateObserverReady) non-voting observer to a full
// voting member once THIS node confirms it is the shard's current Raft
// leader. When nil (the default), a replica that has reached
// ReplicaStateObserverReady is never promoted to ReplicaStateActive — it
// remains a permanent, correctly-functioning non-voting observer (safe, but
// it never contributes to quorum or leader elections).
func WithPromoter(p Promoter) TenantShardReconcilerOption {
	return func(r *TenantShardReconciler) { r.promoter = p }
}

// WithPurgeOnDemotion controls whether the reconciler purges a demoted or
// deleted shard's on-disk dataDir (via ShardLifecycle.CloseAndPurge) instead
// of merely stopping it (ShardLifecycle.Close). Defaults to true: closes a
// pre-existing encrypted-at-rest retention gap where a removed replica's
// ciphertext otherwise remains on disk indefinitely. This ONLY affects
// demotion/delete teardown, never graceful process shutdown (CloseAll),
// which must always leave data on disk for the next restart's re-attach.
func WithPurgeOnDemotion(purge bool) TenantShardReconcilerOption {
	return func(r *TenantShardReconciler) { r.purgeOnDemotion = purge }
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
	placement       *placement.Store
	factory         ShardLifecycle
	selfNodeID      string
	barriers        BarrierReadiness
	joinObserver    JoinObserver
	promoter        Promoter
	purgeOnDemotion bool
	pollInterval    time.Duration
	logger          *slog.Logger
}

// NewTenantShardReconciler creates a TenantShardReconciler. store and factory
// must be non-nil; selfNodeID must match the NodeID values used in placement
// entries (typically the Raft address string). logger defaults to slog.Default()
// when nil. purgeOnDemotion defaults to true (see WithPurgeOnDemotion). opts
// are applied in order.
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
		placement:       store,
		factory:         factory,
		selfNodeID:      selfNodeID,
		pollInterval:    defaultReconcilerPollInterval,
		purgeOnDemotion: true,
		logger:          logger,
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
		r.maybeRestartPromotedLocal(ctx, p)
		r.maybePromoteToObserverReady(ctx, p)
		r.maybePromoteReadyObservers(ctx, p)
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
		if err := r.closeDemoted(s.TenantID, s.Database); err != nil {
			r.logger.Warn("placement reconciler: demoted shard stop failed",
				slog.String("tenant", s.TenantID),
				slog.String("database", s.Database),
				slog.Any("error", err))
		}
	}
}

// closeDemoted stops a demoted or deleted shard, purging its on-disk dataDir
// when r.purgeOnDemotion is true (the default) via ShardLifecycle.
// CloseAndPurge. Purging is correct here because a demotion or delete means
// this node is no longer (and, for a delete, no node anywhere is) a replica
// of the shard — unlike graceful process shutdown (CloseAll), which must
// never purge because the data must survive for the next restart's
// re-attach.
func (r *TenantShardReconciler) closeDemoted(tenantID, db string) error {
	if r.purgeOnDemotion {
		return r.factory.CloseAndPurge(tenantID, db)
	}
	return r.factory.Close(tenantID, db)
}

// maybeRestartPromotedLocal restarts the LOCAL replica of p as a full
// voting member once the shard's leader has promoted it: the placement
// entry says ReplicaStateActive but the running replica was started with
// the observer role latched into its local raft configuration.
//
// go-quicraft cross-checks the local role against the replicated
// membership on every applied config change (checkSelfRole) and latches a
// disagreement by refusing campaigning and leader reads — the correct
// fail-safe while the replica is still an observer, but after a committed
// promotion it would leave a full voting member permanently unable to
// stand for election (an availability hole on the next leader failure).
// The local role is immutable in-process, so the only way to restore
// agreement is to stop the shard and re-attach: ensureStarted then starts
// it with join=false and IsObserver=false (ReplicaStateActive), recovering
// its log and committed membership from the shared host's LogDB — the
// no-data-loss restart path.
//
// Close (never CloseAndPurge) is deliberate: this replica remains a member
// and its on-disk state is the whole point of the restart.
func (r *TenantShardReconciler) maybeRestartPromotedLocal(ctx context.Context, p *placement.Placement) {
	local, ok := p.ReplicaForNode(r.selfNodeID)
	if !ok || local.State != placement.ReplicaStateActive {
		return
	}
	if !r.factory.StartedAsObserver(p.TenantID, p.Database) {
		return
	}
	r.logger.Info("placement reconciler: restarting promoted local replica as voting member",
		slog.String("tenant", p.TenantID),
		slog.String("database", p.Database),
		slog.Uint64("replica_id", local.ReplicaID))
	if err := r.factory.Close(p.TenantID, p.Database); err != nil {
		r.logger.Warn("placement reconciler: promoted replica stop failed, will retry",
			slog.String("tenant", p.TenantID),
			slog.String("database", p.Database),
			slog.Any("error", err))
		return
	}
	r.ensureStarted(ctx, p)
}

// maybePromoteToObserverReady flips the local node's replica from
// ReplicaStateObserver to ReplicaStateObserverReady once its join bootstrap
// window has closed, so a LATER restart of that same replica starts with
// join=false instead of re-entering the join bootstrap window. It is a
// no-op when no JoinObserver is wired, when the local replica is not
// Observer, or while the join bootstrap window is still open (i.e.
// IsShardJoining still reports true). This is self-reported by the node
// that owns the replica using purely local state — no cross-node RPC.
//
// Reaching ReplicaStateObserverReady does NOT yet make the replica a voting
// member of the Raft group — it remains the non-voting observer the leader
// admitted via RaftHost.AddObserver. See maybePromoteReadyObservers for the
// ObserverReady -> Active (voter) transition, which only the shard's
// current leader may perform.
//
// A concurrent scale-down that has already dropped this replica from the
// placement table is tolerated: p is the pre-mutation snapshot already used
// to call ensureStarted, so a stale Put here lands harmlessly — the
// reconciler's next tick re-lists the current placement and reconciles from
// it (last-writer-wins, matching the existing convention elsewhere in this
// package).
func (r *TenantShardReconciler) maybePromoteToObserverReady(ctx context.Context, p *placement.Placement) {
	if r.joinObserver == nil {
		return
	}
	local, ok := p.ReplicaForNode(r.selfNodeID)
	if !ok || local.State != placement.ReplicaStateObserver {
		return
	}
	if r.joinObserver.IsShardJoining(p.ShardID) {
		// Still mid-bootstrap; nothing to promote yet.
		return
	}

	replicas := make([]placement.Replica, len(p.Replicas))
	copy(replicas, p.Replicas)
	for i := range replicas {
		if replicas[i].NodeID == r.selfNodeID {
			replicas[i].State = placement.ReplicaStateObserverReady
			break
		}
	}
	updated := *p
	updated.Replicas = replicas

	if err := r.placement.Put(ctx, &updated); err != nil {
		r.logger.Warn("placement reconciler: failed to promote observer replica to observer-ready",
			slog.String("tenant", p.TenantID),
			slog.String("database", p.Database),
			slog.Any("error", err))
		return
	}
	r.logger.Info("placement reconciler: observer replica caught up, awaiting leader voter promotion",
		slog.String("tenant", p.TenantID),
		slog.String("database", p.Database),
		slog.Uint64("replica_id", local.ReplicaID))
}

// maybePromoteReadyObservers promotes every ReplicaStateObserverReady
// replica of p to a full voting member (ReplicaStateActive), but ONLY when
// this node is confirmed to be the shard's current Raft leader — go-quicraft
// hard-rejects the underlying RequestAddNode config change on any other
// member, so a non-leader attempt would simply fail every tick. It is a
// no-op when no Promoter is wired, when the local node is not a member of
// p at all, or when the local node is not (or cannot be confirmed as) the
// current leader.
//
// Unlike maybePromoteToObserverReady (which only ever touches the LOCAL
// replica, self-reported), this promotes ANY ObserverReady replica in p —
// including ones hosted on other nodes — because only the leader can issue
// the promotion, regardless of which node physically hosts the observer
// being promoted. Every eligible replica is promoted in a single pass with
// at most one placementStore.Put, so a scale-up of several replicas
// converges to fully-voting within a few reconcile ticks rather than one
// per replica.
//
// A promotion attempt that fails (e.g. the leadership check raced an
// election) leaves that replica ObserverReady for the next tick to retry —
// it is already a functioning non-voting observer in the meantime, so this
// is safe to defer indefinitely.
func (r *TenantShardReconciler) maybePromoteReadyObservers(ctx context.Context, p *placement.Placement) {
	if r.promoter == nil {
		return
	}
	local, ok := p.ReplicaForNode(r.selfNodeID)
	if !ok {
		return
	}
	leaderID, lok, lerr := r.promoter.LeaderReplicaID(ctx, p.ShardID)
	if lerr != nil || !lok || leaderID != local.ReplicaID {
		// Not the leader (or leadership cannot be confirmed right now) —
		// only the leader may promote an observer.
		return
	}

	replicas := make([]placement.Replica, len(p.Replicas))
	copy(replicas, p.Replicas)
	var promoted []uint64
	for i := range replicas {
		if replicas[i].State != placement.ReplicaStateObserverReady {
			continue
		}
		if err := r.promoter.PromoteObserver(ctx, p.ShardID, replicas[i].ReplicaID, replicas[i].RaftAddr); err != nil {
			r.logger.Warn("placement reconciler: observer promotion failed, will retry",
				slog.String("tenant", p.TenantID),
				slog.String("database", p.Database),
				slog.Uint64("replica_id", replicas[i].ReplicaID),
				slog.Any("error", err))
			continue
		}
		replicas[i].State = placement.ReplicaStateActive
		promoted = append(promoted, replicas[i].ReplicaID)
	}
	if len(promoted) == 0 {
		return
	}

	updated := *p
	updated.Replicas = replicas
	if err := r.placement.Put(ctx, &updated); err != nil {
		r.logger.Warn("placement reconciler: failed to persist observer voter promotion",
			slog.String("tenant", p.TenantID),
			slog.String("database", p.Database),
			slog.Any("error", err))
		return
	}
	r.logger.Info("placement reconciler: promoted observer replica(s) to voting member",
		slog.String("tenant", p.TenantID),
		slog.String("database", p.Database),
		slog.Any("replica_ids", promoted))
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
			r.maybePromoteToObserverReady(ctx, ev.Placement)
			r.maybePromoteReadyObservers(ctx, ev.Placement)
			break
		}
		// This node is no longer in the replica set — demotion (M1).
		if err := r.closeDemoted(ev.TenantID, ev.Database); err != nil {
			r.logger.Warn("placement reconciler: shard close on demotion failed",
				slog.String("tenant", ev.TenantID),
				slog.String("database", ev.Database),
				slog.Any("error", err))
		}
	case placement.EventDelete:
		if err := r.closeDemoted(ev.TenantID, ev.Database); err != nil {
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
