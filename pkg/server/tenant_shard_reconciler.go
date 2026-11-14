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
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
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

	// CloseShardIfOwned stops the shard for (tenantID, db), purging its
	// dataDir when purge is set, only when the shard under that name on
	// this host is shardID. The comparison, the invalidation of a start
	// still being planned and the cache eviction are one atomic step, so a
	// successor recreated under the name is never stopped. It returns the
	// other placement's shard id when a different shard was left running.
	CloseShardIfOwned(tenantID, db string, shardID uint64, purge bool) (otherShard uint64, err error)

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
// around the same RequestAddNodeWithIdentity config change used to admit a founding
// follower -- go-quicraft treats RequestAddNodeWithIdentity targeting an EXISTING
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

// ShellRegistrar registers a tenant's barrier on this node as an
// uninitialized, sealed shell when placement assigns the node to one of the
// tenant's groups and BarrierReadiness reports no barrier for it. It is
// satisfied by *tenant.TenantService.
type ShellRegistrar interface {
	// RegisterTenantShell registers tenantID's shell, doing nothing for a
	// tenant that needs none or already has a barrier here.
	RegisterTenantShell(ctx context.Context, tenantID string) error
}

// shellRegistrarBox holds a ShellRegistrar behind an atomic.Pointer.
type shellRegistrarBox struct {
	registrar ShellRegistrar
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

// BarrierUnsealNotifier reports each tenant barrier this node initializes
// or unseals. It is satisfied by *seal.BarrierRegistry.
type BarrierUnsealNotifier interface {
	// OnTenantUnsealed registers fn to run, with the tenant's ID, each time
	// one of the node's tenant barriers is initialized or unsealed. fn must
	// not block.
	OnTenantUnsealed(fn func(ctx context.Context, tenantID string))
}

// WithBarrierUnsealNotifier makes Run reconcile as soon as a tenant barrier
// on this node is initialized or unsealed, so a shard deferred for its
// barrier (WithBarrierReadiness) starts then rather than on the next poll.
// When nil, a deferred shard waits for the next pass.
func WithBarrierUnsealNotifier(n BarrierUnsealNotifier) TenantShardReconcilerOption {
	return func(r *TenantShardReconciler) { r.unsealNotifier = n }
}

// queuePass asks the running reconciler for a pass without blocking; a pass
// already queued absorbs the request.
func (r *TenantShardReconciler) queuePass(context.Context, string) {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// WithSealRecords makes the reconciler hold a tenant's shards while the
// tenant's cluster seal record is sealed, and a database's shard while the
// database's is. A system-managed tenant auto-unseals at boot, before this
// node has read the records; its shards must not start until the record
// allows it. s is asked on every pass, so it must be the installed monitor
// (or a Deferred it has been Set into) by the time the reconciler runs: an
// unset Deferred refuses nothing.
func WithSealRecords(s clusterseal.Sealer) TenantShardReconcilerOption {
	return func(r *TenantShardReconciler) { r.sealRecords = s }
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
	shells          atomic.Pointer[shellRegistrarBox]
	sealRecords     clusterseal.Sealer
	joinObserver    JoinObserver
	promoter        Promoter
	purgeOnDemotion bool
	pollInterval    time.Duration
	logger          *slog.Logger

	// unsealNotifier, when set, reports each tenant barrier this node
	// initializes or unseals (WithBarrierUnsealNotifier); every report
	// queues a pass on kick so a deferred shard starts then.
	unsealNotifier BarrierUnsealNotifier
	kick           chan struct{}

	// placedNames maps "tenant/database id" to the database name and raft
	// shard (placedShard) of every placement this reconciler has seen. The
	// table is keyed by the opaque id, so a delete event names only the id;
	// the shard to close is found here (see handleEvent), and closes only
	// when the shard on this host is that entry's shard.
	placedNames sync.Map
}

// placedShard is what placedNames records for one placement entry.
type placedShard struct {
	name    string
	shardID uint64
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
		kick:            make(chan struct{}, 1),
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
//     placement entry are catalog-managed (opened from the database catalog
//     at boot) and are
//     left running — absence from the placement table is not evidence of orphan
//     status (BUG 2 fix).
func (r *TenantShardReconciler) ReconcileOnce(ctx context.Context) {
	placements, err := r.placement.List(ctx)
	if err != nil {
		r.logger.Warn("placement reconciler: List failed",
			slog.Any("error", err))
		return
	}
	for _, p := range placements {
		r.rememberName(p.TenantID, p.DatabaseID, p.Database, p.ShardID)
	}

	// byShard indexes every entry in the placement table by its raft shard,
	// regardless of whether this node is an assigned replica. This lets the
	// teardown loop distinguish "placement exists, I'm not in it" (demotion
	// → stop) from "no placement entry" (meta-managed → leave running). The
	// shard id is the shard's identity: a fenced predecessor that survived a
	// failed delete shares its name with a recreated successor, never its
	// shard, so the predecessor's replica set never tears the successor down.
	byShard := make(map[uint64]*placement.Placement, len(placements))
	for _, p := range placements {
		byShard[p.ShardID] = p
		if p.IsLocalBinding() {
			// A single-node binding on a non-raft Backend (go-qrdb Lane 2B,
			// OD-4 A): there is no Raft group to attach. The owning node
			// materializes it through database resolution (pkg/server), not
			// through this Raft-shard reconciler.
			continue
		}
		if _, ok := p.ReplicaForNode(r.selfNodeID); !ok {
			continue
		}
		running := r.ensureStarted(ctx, p)
		r.maybeRestartPromotedLocal(ctx, p)
		if running {
			r.maybePromoteToObserverReady(ctx, p)
		}
		r.maybePromoteReadyObservers(ctx, p)
	}

	for _, s := range r.factory.ListStarted() {
		p, hasPlacement := byShard[s.ShardID]
		if s.ShardID == 0 || !hasPlacement || p.TenantID != s.TenantID || p.Database != s.Database {
			// No placement entry for this shard — it is catalog-managed
			// (opened from the database catalog at boot or single-node
			// Phase-4b), or a start in flight with no shard id yet. Do not
			// tear it down.
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
		if err := r.closeDemotedShard(s.TenantID, s.Database, s.ShardID); err != nil {
			r.logger.Warn("placement reconciler: demoted shard stop failed",
				slog.String("tenant", s.TenantID),
				slog.String("database", s.Database),
				slog.Any("error", err))
		}
	}
}

// closeDemotedShard stops (and, by default, purges) the shard for
// (tenantID, db) only when the shard on this host is shardID: the entry
// being torn down owns it. A recreated database of the same name runs a
// different shard, so a fenced predecessor's demotion or delete leaves the
// successor running. The identity check and the teardown are one factory
// operation (ShardLifecycle.CloseShardIfOwned), so a successor recreated
// between them cannot be stopped. A shard id of 0 (unknown) closes nothing.
// Purging is correct here because a demotion or delete means
// this node is no longer (and, for a delete, no node anywhere is) a replica
// of the shard — unlike graceful process shutdown (CloseAll), which must
// never purge because the data must survive for the next restart's
// re-attach.
func (r *TenantShardReconciler) closeDemotedShard(tenantID, db string, shardID uint64) error {
	if shardID == 0 {
		return nil
	}
	other, err := r.factory.CloseShardIfOwned(tenantID, db, shardID, r.purgeOnDemotion)
	if other != 0 {
		r.logger.Info("placement reconciler: shard under this name belongs to another placement, left running",
			slog.String("tenant", tenantID),
			slog.String("database", db),
			slog.Uint64("shard_id", other),
			slog.Uint64("entry_shard_id", shardID))
	}
	return err
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
// A concurrent scale-down (or Drop, or another promotion) that has already
// changed this placement entry since p was read is tolerated: the
// CompareAndSwapPlacement below targets p exactly, so a stale write here is
// declined rather than landing over newer state — the reconciler's next
// tick re-lists the current placement and reconciles from it (matching the
// existing lost-update convention elsewhere in this package, now enforced
// rather than merely accepted).
func (r *TenantShardReconciler) maybePromoteToObserverReady(ctx context.Context, p *placement.Placement) {
	if r.joinObserver == nil {
		return
	}
	if p.Dropping {
		// A database mid-Drop is not scaled: promoting a member here would
		// also overwrite the replicated fence with this stale, pre-Drop
		// snapshot's Dropping=false, via the CompareAndSwapPlacement below
		// racing the Drop's own SetDropping. Refusing outright, rather than
		// relying on the CAS to merely decline, keeps this no-op cheap and
		// avoids a log line for an outcome that recurs every tick until the
		// drop completes.
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

	// CompareAndSwapPlacement, not Put: p is the exact snapshot this
	// function read Dropping and Replicas from, so a swap declined because
	// the entry changed since -- a concurrent Drop's fence, a scale, another
	// promotion -- must never overwrite that newer state with this stale
	// one. The next reconcile tick re-lists the current placement and
	// retries from it, matching this package's existing last-writer-wins
	// convention for lost updates.
	swapped, err := r.placement.CompareAndSwapPlacement(ctx, p, &updated)
	if err != nil {
		r.logger.Warn("placement reconciler: failed to promote observer replica to observer-ready",
			slog.String("tenant", p.TenantID),
			slog.String("database", p.Database),
			slog.Any("error", err))
		return
	}
	if !swapped {
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
// hard-rejects the underlying RequestAddNodeWithIdentity config change on any other
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
	if p.Dropping {
		// See the identical guard in maybePromoteToObserverReady: a database
		// mid-Drop is never promoted, and the CompareAndSwapPlacement below
		// must never have the chance to overwrite the replicated fence with
		// this stale, pre-Drop snapshot's Dropping=false.
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
	// CompareAndSwapPlacement against p, the exact snapshot read above --
	// see maybePromoteToObserverReady's identical comment on why a stale
	// write must never win here.
	swapped, err := r.placement.CompareAndSwapPlacement(ctx, p, &updated)
	if err != nil {
		r.logger.Warn("placement reconciler: failed to persist observer voter promotion",
			slog.String("tenant", p.TenantID),
			slog.String("database", p.Database),
			slog.Any("error", err))
		return
	}
	if !swapped {
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
//
// It reports whether the shard is running here now. Only a running replica can
// have caught up: a shard that never started is not joining either, and must
// not be taken for an observer that finished (maybePromoteToObserverReady).
func (r *TenantShardReconciler) ensureStarted(ctx context.Context, p *placement.Placement) bool {
	if r.sealRecords != nil && (r.sealRecords.Refuses(clusterseal.TenantBarrier(p.TenantID)) ||
		r.sealRecords.Refuses(clusterseal.DatabaseBarrier(p.TenantID, p.Database))) {
		r.logger.Debug("placement reconciler: tenant or database sealed cluster-wide, deferring shard start",
			slog.String("tenant", p.TenantID),
			slog.String("database", p.Database))
		return false
	}
	if r.barriers != nil {
		tb, err := r.barriers.GetTenant(p.TenantID)
		if err != nil || tb == nil {
			r.registerShell(ctx, p.TenantID)
		}
		if err != nil || tb == nil || tb.IsSealed() {
			r.logger.Debug("placement reconciler: barrier not ready, deferring shard start",
				slog.String("tenant", p.TenantID),
				slog.String("database", p.Database))
			return false
		}
	}

	if err := r.factory.Attach(ctx, p.TenantID, p.Database); err != nil {
		r.logger.Warn("placement reconciler: shard start failed",
			slog.String("tenant", p.TenantID),
			slog.String("database", p.Database),
			slog.Any("error", err))
		return false
	}
	return true
}

// registerShell registers tenantID's barrier shell on this node through the
// installed ShellRegistrar, so that status, init and unseal reach a node
// placement has assigned to the tenant before the node holds a key. The shell
// is sealed, so the shard start still defers; a failure is logged and retried
// on the next reconcile.
func (r *TenantShardReconciler) registerShell(ctx context.Context, tenantID string) {
	shells := r.shells.Load()
	if shells == nil {
		return
	}
	if err := shells.registrar.RegisterTenantShell(ctx, tenantID); err != nil {
		r.logger.Warn("placement reconciler: tenant barrier shell registration failed, will retry",
			slog.String("tenant", tenantID),
			slog.Any("error", err))
	}
}

// SetShellRegistrar installs the registrar of the barrier shell a node
// registers when placement assigns it to a tenant's group (registerShell). It
// is set once the tenant service that registers shells exists, which is after
// the reconciler is built; until then, and when s is nil, no shell is
// registered.
func (r *TenantShardReconciler) SetShellRegistrar(s ShellRegistrar) {
	if s == nil {
		r.shells.Store(nil)
		return
	}
	r.shells.Store(&shellRegistrarBox{registrar: s})
}

// Run drives the reconciler lifecycle. It calls ReconcileOnce immediately
// (boot re-attach pass) then:
//   - when placement.Store.Watch succeeds, reacts to typed events (watch path);
//   - when Watch returns ErrWatchUnsupported or any error, polls with
//     ReconcileOnce on a ticker at r.pollInterval (poll path).
//
// On either path, a tenant barrier initialized or unsealed on this node
// (WithBarrierUnsealNotifier) runs a pass at once, so a shard deferred for
// its barrier starts without waiting for the next tick.
//
// Run blocks until ctx is cancelled.
func (r *TenantShardReconciler) Run(ctx context.Context) {
	if r.unsealNotifier != nil {
		r.unsealNotifier.OnTenantUnsealed(r.queuePass)
	}
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
		case <-r.kick:
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
		r.rememberName(ev.TenantID, ev.DatabaseID, ev.Database, ev.Placement.ShardID)
		if ev.Placement.IsLocalBinding() {
			// Same as ReconcileOnce: a local binding has no Raft group for
			// this reconciler to attach.
			break
		}
		if _, ok := ev.Placement.ReplicaForNode(r.selfNodeID); ok {
			if r.ensureStarted(ctx, ev.Placement) {
				r.maybePromoteToObserverReady(ctx, ev.Placement)
			}
			r.maybePromoteReadyObservers(ctx, ev.Placement)
			break
		}
		// This node is no longer in the replica set — demotion (M1). Only
		// the entry's own shard closes: a successor of the same name runs
		// another shard.
		if err := r.closeDemotedShard(ev.TenantID, ev.Database, ev.Placement.ShardID); err != nil {
			r.logger.Warn("placement reconciler: shard close on demotion failed",
				slog.String("tenant", ev.TenantID),
				slog.String("database", ev.Database),
				slog.Any("error", err))
		}
	case placement.EventDelete:
		// A delete names only the entry's id; the shard that closes is the
		// one the entry placed while it existed, found by its shard id, so
		// a recreated database of the same name keeps running.
		name, shardID := r.forgetName(ev.TenantID, ev.DatabaseID)
		if name == "" || shardID == 0 {
			r.logger.Debug("placement reconciler: delete of an entry never seen, nothing to close",
				slog.String("tenant", ev.TenantID),
				slog.String("database_id", ev.DatabaseID))
			break
		}
		if err := r.closeDemotedShard(ev.TenantID, name, shardID); err != nil {
			r.logger.Warn("placement reconciler: shard close on delete failed",
				slog.String("tenant", ev.TenantID),
				slog.String("database", name),
				slog.Any("error", err))
		}
	}
}

// rememberName records the database name and raft shard a placement entry carries, so a
// later delete of the entry (which names only its id) closes that shard.
func (r *TenantShardReconciler) rememberName(tenantID, databaseID, name string, shardID uint64) {
	if databaseID == "" || name == "" {
		return
	}
	r.placedNames.Store(tenantID+"/"+databaseID, placedShard{name: name, shardID: shardID})
}

// forgetName returns and drops the name and shard id recorded for the entry, or "" and 0 when
// the entry was never seen.
func (r *TenantShardReconciler) forgetName(tenantID, databaseID string) (string, uint64) {
	v, ok := r.placedNames.LoadAndDelete(tenantID + "/" + databaseID)
	if !ok {
		return "", 0
	}
	ps, _ := v.(placedShard) //nolint:errcheck // the map only ever holds placedShard
	return ps.name, ps.shardID
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
		case <-r.kick:
			r.ReconcileOnce(ctx)
		}
	}
}
