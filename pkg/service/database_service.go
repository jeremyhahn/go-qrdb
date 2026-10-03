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

package service

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

const (
	// scaleUpKeyDeliveryMaxAttempts bounds
	// deliverTenantKeyToReplicaWithRetry's retry loop.
	scaleUpKeyDeliveryMaxAttempts = 8

	// scaleUpKeyDeliveryRetryDelay is the fixed backoff between key-delivery
	// retry attempts. 8 attempts * 300ms gives ~2.1s of retry budget,
	// comfortably covering the sub-2s system-shard replication + apply
	// latency observed in practice for a freshly scaled-up replica's own
	// placement view to catch up to the leader's just-committed write (see
	// deliverTenantKeyToReplicaWithRetry).
	scaleUpKeyDeliveryRetryDelay = 300 * time.Millisecond
)

// DatabaseServicer defines CRUD operations for database management.
type DatabaseServicer interface {
	// Create creates a new database with the given configuration.
	Create(cfg database.DatabaseConfig) (*database.DatabaseInfo, error)

	// Get returns information about a specific database.
	Get(name string) (*database.DatabaseInfo, error)

	// List returns information about all open databases.
	List() ([]database.DatabaseInfo, error)

	// Drop closes and removes a database, deleting its data — both the
	// files it owns on this node and the keys it owns in the store it
	// shares with other databases. It takes a context because the second
	// half is a replicated operation (go-qrdb#412).
	Drop(ctx context.Context, name string) error

	// UpdateProtocol updates the wire protocol configuration for a database
	// and returns the updated runtime information.
	UpdateProtocol(name string, protocol database.ProtocolConfig) (*database.DatabaseInfo, error)

	// UpdateEtcd updates the etcd wire protocol configuration for a database
	// and returns the updated runtime information.
	UpdateEtcd(name string, etcd *database.EtcdConfig) (*database.DatabaseInfo, error)

	// UpdateRaft updates the Raft consensus configuration for a database.
	UpdateRaft(name string, raft *database.DatabaseRaftConfig) (*database.DatabaseInfo, error)

	// UpdateBarrier updates the encryption barrier configuration for a database.
	UpdateBarrier(name string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error)

	// UpdateConnection updates the remote connection configuration for a database.
	UpdateConnection(name string, conn *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error)

	// UpdateSQL updates a database's SQL settings -- its sql.concurrency.
	UpdateSQL(name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error)

	// SealDatabase seals the encryption barrier for a database after verifying
	// the caller's tenant owns it (system tenant may target any database).
	SealDatabase(ctx context.Context, name string) error

	// UnsealDatabase unseals the encryption barrier for a database after
	// verifying the caller's tenant owns it (system tenant may target any
	// database).
	UnsealDatabase(ctx context.Context, name string, creds seal.Credentials) error

	// DatabaseBarrierStatus returns the barrier status for a database after
	// verifying the caller's tenant owns it (system tenant may target any
	// database).
	DatabaseBarrierStatus(ctx context.Context, name string) (*DatabaseBarrierStatusInfo, error)

	// Tenant-scoped operations (context-based tenant enforcement)

	// CreateForTenant creates a new database scoped to the context's tenant.
	CreateForTenant(ctx context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error)

	// GetForTenant returns database info after verifying tenant ownership.
	GetForTenant(ctx context.Context, name string) (*database.DatabaseInfo, error)

	// ListForTenant returns databases owned by the context's tenant.
	ListForTenant(ctx context.Context) ([]database.DatabaseInfo, error)

	// DropForTenant drops a database after verifying tenant ownership.
	DropForTenant(ctx context.Context, name string) error

	// UpdateProtocolForTenant updates protocol config with tenant enforcement.
	UpdateProtocolForTenant(ctx context.Context, name string, protocol database.ProtocolConfig) (*database.DatabaseInfo, error)

	// UpdateEtcdForTenant updates etcd config with tenant enforcement.
	UpdateEtcdForTenant(ctx context.Context, name string, etcd *database.EtcdConfig) (*database.DatabaseInfo, error)

	// UpdateRaftForTenant updates Raft config with tenant enforcement.
	UpdateRaftForTenant(ctx context.Context, name string, raft *database.DatabaseRaftConfig) (*database.DatabaseInfo, error)

	// UpdateBarrierForTenant updates barrier config with tenant enforcement.
	UpdateBarrierForTenant(ctx context.Context, name string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error)

	// UpdateConnectionForTenant updates connection config with tenant enforcement.
	UpdateConnectionForTenant(ctx context.Context, name string, conn *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error)

	// UpdateSQLForTenant updates SQL settings with tenant enforcement.
	UpdateSQLForTenant(ctx context.Context, name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error)

	// UpdateCompactionForTenant sets the scheduled-compaction policy of a
	// database the context's tenant owns -- the system tenant included, which
	// is held to its own databases. A nil policy turns it off.
	UpdateCompactionForTenant(ctx context.Context, name string, policy *compaction.Policy) (*database.DatabaseInfo, error)

	// ScaleForTenant changes the replication factor of a dedicated-raft
	// database after verifying tenant ownership and quota.
	ScaleForTenant(ctx context.Context, name string, targetReplicas int) (*database.DatabaseInfo, error)
}

// DatabaseCredentialCreator is implemented by DatabaseServicer implementations
// that support supplying barrier credentials at create time (e.g.
// --barrier-passphrase). Transport handlers should type-assert the resolved
// DatabaseServicer for this interface before falling back to plain Create /
// CreateForTenant when a caller supplies barrier credentials. *DatabaseService
// implements this; it is additive rather than part of DatabaseServicer so
// that existing DatabaseServicer mocks are unaffected.
type DatabaseCredentialCreator interface {
	CreateWithCredentials(ctx context.Context, cfg database.DatabaseConfig, creds seal.Credentials) (*database.DatabaseInfo, error)
	CreateForTenantWithCredentials(ctx context.Context, cfg database.DatabaseConfig, creds seal.Credentials) (*database.DatabaseInfo, error)
}

// Compile-time interface check.
var _ DatabaseCredentialCreator = (*DatabaseService)(nil)

// CreateDatabaseForTenantWithCredentials creates cfg via the tenant-scoped
// path, threading creds through when svc supports credentialed creation
// (DatabaseCredentialCreator). When svc does not implement the interface
// (e.g. a test double), creds is silently dropped and the plain
// CreateForTenant path is used; this only matters when cfg.Barrier.Enabled
// is true, in which case EnsureBarrier initializes the barrier with an
// empty secret. Shared by every transport handler (REST, gRPC, QUIC, MCP,
// Unix) so the create-with-barrier-secret plumbing is implemented exactly
// once.
func CreateDatabaseForTenantWithCredentials(ctx context.Context, svc DatabaseServicer, cfg database.DatabaseConfig, creds seal.Credentials) (*database.DatabaseInfo, error) {
	if cc, ok := svc.(DatabaseCredentialCreator); ok {
		return cc.CreateForTenantWithCredentials(ctx, cfg, creds)
	}
	return svc.CreateForTenant(ctx, cfg)
}

// DatabaseServiceOption configures optional DatabaseService behavior.
type DatabaseServiceOption func(*DatabaseService)

// WithBarrierManager sets the DatabaseBarrierManager for seal/unseal operations.
func WithBarrierManager(bm *database.DatabaseBarrierManager) DatabaseServiceOption {
	return func(s *DatabaseService) { s.barrierMgr = bm }
}

// WithQuotaChecker sets a quota.Checker that is consulted before tenant-scoped
// database creation. When nil the check is skipped.
func WithQuotaChecker(checker quota.Checker) DatabaseServiceOption {
	return func(s *DatabaseService) { s.quotaChecker = checker }
}

// WithEntitlementChecker sets a quota.EntitlementChecker that is consulted
// before tenant-scoped database creation to verify the tenant's plan permits
// the requested isolation mode and dedicated-storage features. When nil the
// entitlement check is skipped (backward compatible).
func WithEntitlementChecker(checker quota.EntitlementChecker) DatabaseServiceOption {
	return func(s *DatabaseService) { s.entitlementChecker = checker }
}

// WithTenantInfoLookup sets the tenant-metadata lookup used to route an
// IsolationVolume tenant's database DataDir under the tenant's dedicated
// DataRoot ({DataRoot}/databases/{db}). When nil, DataDir routing is skipped
// and databases use the shared per-database layout (backward compatible).
func WithTenantInfoLookup(lookup TenantInfoLookup) DatabaseServiceOption {
	return func(s *DatabaseService) { s.tenantInfo = lookup }
}

// WithPlacementStore sets the replicated placement table into which a
// dedicated-raft (IsolationRaft) database's computed Placement is persisted on
// create and removed on drop. When nil, no placement is persisted (the
// dedicated-raft database falls back to single-node, meta-derived resolution).
// WithPlacementScheduler must also be set for placement to be computed.
func WithPlacementStore(store *placement.Store) DatabaseServiceOption {
	return func(s *DatabaseService) { s.placementStore = store }
}

// WithPlacementScheduler sets the load-aware scheduler used to compute a
// dedicated-raft database's replica Placement at create time and to clamp the
// requested replication factor to the cluster size. When nil, placement is not
// computed and the replication factor is not clamped to the live cluster size
// (only the static quota caps apply).
func WithPlacementScheduler(scheduler *placement.Scheduler) DatabaseServiceOption {
	return func(s *DatabaseService) { s.placementScheduler = scheduler }
}

// WithDatabaseLogger sets the structured logger used for placement and
// rollback diagnostics. Defaults to slog.Default().
func WithDatabaseLogger(logger *slog.Logger) DatabaseServiceOption {
	return func(s *DatabaseService) {
		if logger != nil {
			s.logger = logger
		}
	}
}

// WithQuotaUsageRecorder sets the recorder used to increment/decrement a
// tenant's raft-group usage counter when a dedicated-raft (IsolationRaft)
// database is created or dropped, so the MaxRaftGroups quota is enforced
// per-database. When nil, raft-group usage is not recorded (the numeric quota
// Check still runs but reads a static counter).
func WithQuotaUsageRecorder(rec QuotaUsageRecorder) DatabaseServiceOption {
	return func(s *DatabaseService) { s.quotaUsage = rec }
}

// WithKeyDistributor sets the distributor that ships the tenant barrier key to
// follower replicas immediately after a dedicated-raft database placement is
// persisted. When nil, key distribution is skipped — followers reconcile and
// pull the key during their shard start sequence (Phase 4c-1d).
func WithKeyDistributor(d KeyDistributor) DatabaseServiceOption {
	return func(s *DatabaseService) { s.keyDistributor = d }
}

// WithBarrierLookup sets the lookup used to resolve a tenant's dedicated
// storage barrier when distributing keys to follower replicas. It is satisfied
// by *seal.BarrierRegistry. When nil, key distribution is skipped even if a
// KeyDistributor is set.
func WithBarrierLookup(lookup TenantBarrierLookup) DatabaseServiceOption {
	return func(s *DatabaseService) { s.barrierLookup = lookup }
}

// WithLocalNodeID sets the local Raft node ID passed to KeyDistributor as
// selfNodeID so the distributor skips the local replica (which already holds
// the key). It should be set to the node's Raft address (the same value that
// appears as Replica.NodeID in the placement table).
func WithLocalNodeID(nodeID string) DatabaseServiceOption {
	return func(s *DatabaseService) { s.localNodeID = nodeID }
}

// WithRaftHost sets the Raft membership-change handle used to invite follower
// replicas immediately after a dedicated-raft (IsolationRaft) database is
// placed. When nil, the RequestAddNodeWithIdentity step is skipped and the per-node
// TenantShardReconciler converges the follower start on subsequent ticks.
func WithRaftHost(h RaftHost) DatabaseServiceOption {
	return func(s *DatabaseService) { s.raftHost = h }
}

// WithScaleLeaderWaitTimeout overrides the bounded wait (default
// defaultScaleLeaderWaitTimeout) that ScaleForTenant's scaleUp/scaleDown
// apply for the dedicated shard to report a known Raft leader before issuing
// a membership change. Primarily intended for tests exercising the
// wait/timeout paths without the full production bound.
func WithScaleLeaderWaitTimeout(timeout time.Duration) DatabaseServiceOption {
	return func(s *DatabaseService) { s.scaleLeaderWaitTimeout = timeout }
}

// WithScaleLeaderWaitInterval overrides the poll interval (default
// defaultScaleLeaderWaitInterval) waitForShardLeaderKnown uses between
// LeaderReplicaID calls.
func WithScaleLeaderWaitInterval(interval time.Duration) DatabaseServiceOption {
	return func(s *DatabaseService) { s.scaleLeaderWaitInterval = interval }
}

// RaftHost is the narrow Raft membership-change interface needed by
// DatabaseService to invite follower replicas immediately after a
// dedicated-raft (IsolationRaft) database is placed.
//
// Errors from AddFollowerNode are non-fatal in the create flow: the shard may
// not yet be running when CreateForTenant executes (TenantShardReconciler
// materializes it asynchronously). The per-node reconciler retries
// convergence on subsequent ticks. callers inject it via WithRaftHost or
// SetRaftHost; when nil the step is silently skipped.
type RaftHost interface {
	// AddFollowerNode registers a voting replica (replicaID, raftAddr) in
	// the shard identified by shardID. The call blocks until the config
	// change commits or ctx is cancelled. An error does not fail database
	// creation; the follower reconciler and raft retries converge.
	//
	// Used ONLY for founding members at create time (CreateForTenant), where
	// every replica bootstraps together from the group's genesis
	// configuration and there is no existing quorum to transiently break.
	// scaleUp (adding a replica to an ALREADY-RUNNING group) deliberately
	// does NOT use this method -- see AddObserver.
	AddFollowerNode(ctx context.Context, shardID, replicaID uint64, raftAddr string) error

	// AddObserver registers a NON-VOTING observer (replicaID, raftAddr) in
	// the shard identified by shardID. The call blocks until the config
	// change commits or ctx is cancelled. Used exclusively by
	// DatabaseService.scaleUp to admit a dynamically added replica into an
	// ALREADY-RUNNING group: because an observer does not count toward
	// quorum, admitting it can never transiently change (and therefore never
	// breaks) the group's existing voting majority -- unlike admitting it
	// directly as a voter, which shrinks the effective majority the instant
	// the config change commits, before the new replica has any data of its
	// own to help satisfy it (the flaky quorum-break bug this method fixes;
	// see the elastic-scaling design doc §B.2/§D.4). The observer is later
	// promoted to a full voting member by TenantShardReconciler's Promoter,
	// once it has caught up, on the shard's current leader -- see
	// TenantShardReconciler.maybePromoteReadyObservers.
	AddObserver(ctx context.Context, shardID, replicaID uint64, raftAddr string) error

	// RemoveFollowerNode evicts a voting replica from the shard identified by
	// shardID. Blocks until the config change commits or ctx is cancelled.
	// Used by ScaleForTenant's scale-down path; unlike AddFollowerNode, an
	// error here fails the whole scale-down call (see selectReplicasToRemove
	// and ScaleForTenant for the failure-ordering rationale).
	RemoveFollowerNode(ctx context.Context, shardID, replicaID uint64) error

	// LeaderReplicaID returns the shard's current leader replica ID and
	// whether a leader is known. Used by ScaleForTenant's scale-down
	// replica-selection policy so the current leader is removed last.
	LeaderReplicaID(ctx context.Context, shardID uint64) (id uint64, ok bool, err error)

	// RequestLeaderTransfer asks the shard's current leader to hand off
	// leadership to targetReplicaID. Fire-and-forget: go-quicraft returns
	// once the request is accepted, and the transfer itself completes
	// asynchronously -- callers must poll LeaderReplicaID to observe
	// completion. Used by removeReplicaFromGroup to move leadership away
	// from a replica BEFORE evicting it: RemoveFollowerNode targeting the
	// CURRENT LEADER is unsafe on its own -- go-quicraft applies the config
	// change to the leader's own log and the leader steps down as soon as it
	// applies the entry locally, without necessarily waiting for the entry
	// to replicate to any surviving voter first. If the removed leader is
	// then torn down (as the placement reconciler's demotion path does
	// immediately once the placement update commits), a surviving voter
	// that never received the entry is stranded believing the OLD (larger)
	// configuration still applies, and can never win an election alone --
	// see transferLeadershipAwayFromRemoved's doc comment for the concrete
	// failure sequence this method exists to prevent.
	RequestLeaderTransfer(ctx context.Context, shardID, targetReplicaID uint64) error
}

// TenantInfoLookup resolves tenant metadata needed to route IsolationVolume
// database storage. It is satisfied by tenant.TenantServicer.
type TenantInfoLookup interface {
	GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error)
}

// QuotaUsageRecorder records per-tenant resource-usage changes. It is satisfied
// by tenant.TenantServicer and is used to keep the raft-group usage counter
// accurate as dedicated-raft databases are created and dropped.
type QuotaUsageRecorder interface {
	IncrementQuotaUsage(ctx context.Context, tenantID string, resource quota.Resource, delta int64) (*tenant.TenantInfo, error)
	DecrementQuotaUsage(ctx context.Context, tenantID string, resource quota.Resource, delta int64) (*tenant.TenantInfo, error)
}

// KeyDistributor ships the tenant barrier key to follower replicas after a
// dedicated-raft (IsolationRaft) database is placed. It is satisfied by
// *clusterkeys.Sender (duck-typed; defined here so service does not import
// clusterkeys — the dependency flows one way: daemon → service, daemon → clusterkeys).
type KeyDistributor interface {
	DistributeTenantKey(ctx context.Context, p *placement.Placement, leaderBarrier *seal.StorageBarrier, selfNodeID string, autoUnseal bool) error
}

// TenantBarrierLookup resolves the dedicated storage barrier for a given
// tenant. It is satisfied by *seal.BarrierRegistry.
type TenantBarrierLookup interface {
	GetTenant(tenantID string) (*seal.TenantBarrier, error)
}

// deliverTenantKeyToReplica ships the tenant barrier key to dst via
// deliverer, resolving the tenant's StorageBarrier through lookup. It
// constructs a single-replica Placement containing only dst so that
// DistributeTenantKey delivers to exactly that one recipient, skipping the
// local node identified by selfNodeID (which already holds the key as the
// current leader). Reached, via deliverTenantKeyToReplicaWithRetry, by both
// DatabaseService.scaleUp and RebalanceService.moveTenantGroupShard (through
// the shared addObserverReplica helper) so the single-replica key-delivery
// step is defined exactly once; KeyDistributor/TenantBarrierLookup and
// RebalanceKeyDeliverer/RebalanceBarrierLookup are structurally identical
// interfaces, so both callers' fields satisfy this signature without
// adaptation.
func deliverTenantKeyToReplica(ctx context.Context, deliverer KeyDistributor, lookup TenantBarrierLookup, p *placement.Placement, dst placement.Replica, selfNodeID string) error {
	tb, err := lookup.GetTenant(p.TenantID)
	if err != nil {
		return err
	}
	autoUnseal := tb.Barrier().ActiveStrategy() == seal.StrategySystemBarrier

	singleReplica := &placement.Placement{
		TenantID: p.TenantID,
		Database: p.Database,
		ShardID:  p.ShardID,
		RF:       1,
		Replicas: []placement.Replica{dst},
	}
	return deliverer.DistributeTenantKey(ctx, singleReplica, tb.Barrier(), selfNodeID, autoUnseal)
}

// deliverTenantKeyToReplicaWithRetry wraps deliverTenantKeyToReplica with a
// bounded, fixed-delay retry to ride out a replication race that is unique
// to a newly-admitted-as-observer replica (as opposed to CreateForTenant's
// initial key delivery to a founding member, which does not retry): the
// replica's OWN local placement view is replicated asynchronously via the
// system Raft group, so clusterkeys' receiver-side placement gate (which
// checks the follower's own just-applied placement snapshot) can reject a
// key-delivery envelope sent immediately after the leader's placement.Put
// -- even though that write already committed on the leader -- simply
// because the follower has not yet applied it locally. This applies equally
// to DatabaseService.scaleUp's new replicas and a RebalanceService MOVE's
// destination replica (both reach this function via the shared
// addObserverReplica helper). clusterkeys.Sender.Deliver collapses every
// non-200 response (including this transient rejection) into a single
// sentinel error, so this retries unconditionally rather than branching on
// error type; that is safe because the call is already documented as
// best-effort, and re-delivery of the same key is idempotent on the
// receiver.
//
// Without this retry, a replica whose only delivery attempt loses this race
// never receives its tenant key, so TenantShardReconciler's BarrierReadiness
// gate defers starting its shard indefinitely. The leader admits that
// replica as a non-voting OBSERVER (AddObserver happens after this call
// within the same addObserverReplica invocation, see below) precisely so a
// replica stuck waiting for its key can never strand the group's existing
// voting quorum -- but it also never catches up or gets promoted to a voter
// until the key arrives, so this retry still matters for scale-up (and a
// MOVE's destination) to actually converge.
func deliverTenantKeyToReplicaWithRetry(ctx context.Context, deliverer KeyDistributor, lookup TenantBarrierLookup, p *placement.Placement, dst placement.Replica, selfNodeID string) error {
	var err error
	for attempt := 1; attempt <= scaleUpKeyDeliveryMaxAttempts; attempt++ {
		if err = deliverTenantKeyToReplica(ctx, deliverer, lookup, p, dst, selfNodeID); err == nil {
			return nil
		}
		if attempt == scaleUpKeyDeliveryMaxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(scaleUpKeyDeliveryRetryDelay):
		}
	}
	return err
}

// addObserverReplica is the observer-first "add one replica" step shared by
// DatabaseService.scaleUp (adding a batch of replicas toward a scale-up
// target, one at a time) and RebalanceService.moveTenantGroupShard (adding
// exactly one destination replica for a MOVE): persist an updated placement
// with r appended, best-effort deliver the tenant barrier key to r, then
// admit r into the dedicated Raft group as a NON-VOTING observer.
//
// Placement MUST be persisted before key delivery is attempted: the
// receiver's placement gate (pkg/clusterkeys/receiver.go) authorizes a
// delivery only once its own replicated copy of the placement table lists
// it as the assigned replica -- see deliverTenantKeyToReplicaWithRetry's doc
// comment. r must already carry State == placement.ReplicaStateObserver (set
// by the scheduler for scale-up, or explicitly by the caller for a MOVE) so
// the reconciler's Observer -> ObserverReady -> Active promotion state
// machine (TenantShardReconciler.maybePromoteToObserverReady /
// maybePromoteReadyObservers) treats it identically to a scale-up replica.
// An observer is never admitted directly as a voter: doing so can
// transiently shrink the group's effective voting quorum below what the
// already-established leader controls, before the new replica has any data
// of its own to help satisfy it -- see the elastic-scaling design doc
// §B.2/§D.4.
//
// Returns:
//   - updated: current with r appended. Equal to current, unchanged, when
//     the placement-put itself failed; otherwise reflects the just-persisted
//     state even when AddObserver subsequently failed (placement already
//     committed by that point, so a retry has something to converge from).
//   - keyDeliveryErr: a non-fatal key-delivery failure for the caller to log
//     (the destination's own reconciler pulls the key independently on its
//     next pass); nil when delivery succeeded or was skipped (kd/bl nil).
//   - placementFailed: true when err originated from the placement-put
//     rather than AddObserver, letting a caller that reports a step name
//     (RebalanceService.moveTenantGroupShard) distinguish the two failure
//     points.
//   - err: a fatal error from the placement-put or AddObserver. Non-nil err
//     means the caller must fail the whole operation; unlike keyDeliveryErr,
//     nothing else in the system retries a config change AddObserver itself
//     reported as failed.
func addObserverReplica(
	ctx context.Context,
	raftHost RaftHost,
	ps *placement.Store,
	kd KeyDistributor,
	bl TenantBarrierLookup,
	current *placement.Placement,
	r placement.Replica,
	selfNodeID string,
) (updated *placement.Placement, keyDeliveryErr error, placementFailed bool, err error) {
	// Refuse a replica on a node that cannot apply what the group uses
	// before anything is persisted: a refused add leaves the placement as
	// it was, so no reconciler starts a shard there.
	if aerr := admitReplica(raftHost, current.ShardID, r.RaftAddr); aerr != nil {
		return current, nil, false, aerr
	}
	updated = clonePlacementWithReplica(current, r)
	swapped, perr := ps.CompareAndSwapPlacement(ctx, current, updated)
	if perr != nil {
		return current, nil, true, perr
	}
	if !swapped {
		return current, nil, true, &placement.StoreError{
			TenantID: current.TenantID,
			Database: current.Database,
			Op:       "put",
			Err:      &placement.ConflictError{},
		}
	}

	if kd != nil && bl != nil {
		if kerr := deliverTenantKeyToReplicaWithRetry(ctx, kd, bl, updated, r, selfNodeID); kerr != nil {
			keyDeliveryErr = kerr
		}
	}

	if raftHost != nil {
		if aerr := raftHost.AddObserver(ctx, updated.ShardID, r.ReplicaID, r.RaftAddr); aerr != nil {
			return updated, keyDeliveryErr, false, aerr
		}
	}

	return updated, keyDeliveryErr, false, nil
}

// removeReplicaFromGroup is the "remove one replica" step shared by
// DatabaseService.scaleDown and RebalanceService.moveTenantGroupShard:
// persist an updated placement with replicaID dropped, then evict it from
// Raft consensus.
//
// Returns updated (current with replicaID dropped; unchanged on a
// placement-put failure), placementFailed (true when err originated from the
// placement-put rather than RemoveFollowerNode, letting a caller that
// reports a step name distinguish the two failure points), and err (a fatal
// error from either step -- the placement already reflects the intended
// post-removal state by the time RemoveFollowerNode is attempted, so a
// RemoveFollowerNode failure leaves the Raft config change for the
// TenantShardReconciler to eventually converge, but still fails the call so
// the caller can alert or retry).
//
// removeReplicaFromGroup deliberately does NOT attempt to transfer
// leadership away from replicaID here, even though removing the CURRENT
// LEADER via RemoveFollowerNode without first transferring leadership away
// is unsafe (see transferLeadershipAwayFromRemoved's doc comment): RaftHost
// only ever exposes the LOCAL executing node's Raft state, and every caller
// of removeReplicaFromGroup only reaches it after confirming (via
// resolveGroupLeader/resolveMoveLeader) that the LOCAL node IS the current
// leader -- so "replicaID is the current leader" at this point can only
// ever mean "replicaID is the LOCAL node itself". Transferring leadership
// away from the local node HERE would make the local node no longer the
// leader, and the very next line's RemoveFollowerNode call -- which can
// only ever be issued by the current leader -- would then be rejected by
// go-quicraft. This case must instead be handled by the CALLER, before any
// mutation: see RebalanceService.resolveMoveLeader, which proactively
// transfers leadership away and reports itself as NOT the leader whenever
// the MOVE's source is the local node, so the whole request is forwarded
// to (and re-executed in full by) the new leader instead.
// DatabaseService.scaleDown never hits this case at all --
// selectReplicasToRemove never selects the current leader for removal
// unless it is the group's last remaining replica, which never happens
// while the RF floor (>= 1) is enforced.
func removeReplicaFromGroup(
	ctx context.Context,
	raftHost RaftHost,
	ps *placement.Store,
	current *placement.Placement,
	replicaID uint64,
) (updated *placement.Placement, placementFailed bool, err error) {
	updated = dropReplicaFromPlacement(current, replicaID)
	swapped, perr := ps.CompareAndSwapPlacement(ctx, current, updated)
	if perr != nil {
		return current, true, perr
	}
	if !swapped {
		return current, true, &placement.StoreError{
			TenantID: current.TenantID,
			Database: current.Database,
			Op:       "put",
			Err:      &placement.ConflictError{},
		}
	}
	if raftHost != nil {
		if rerr := raftHost.RemoveFollowerNode(ctx, updated.ShardID, replicaID); rerr != nil {
			return updated, false, rerr
		}
	}
	return updated, false, nil
}

const (
	// defaultLeaderTransferWaitTimeout bounds how long
	// transferLeadershipAwayFromRemoved waits for a requested leadership
	// transfer to complete before giving up.
	defaultLeaderTransferWaitTimeout = 5 * time.Second

	// defaultLeaderTransferWaitInterval is the poll interval
	// transferLeadershipAwayFromRemoved uses between LeaderReplicaID calls
	// while waiting for a requested transfer to complete.
	defaultLeaderTransferWaitInterval = 100 * time.Millisecond
)

// transferLeadershipAwayFromRemoved requests a leadership transfer away from
// replicaID when it is currently shardID's leader, then blocks until the
// transfer completes or defaultLeaderTransferWaitTimeout elapses.
//
// RemoveFollowerNode targeting the CURRENT LEADER is unsafe on its own:
// go-quicraft applies the config change to the leader's own log and the
// leader steps down as soon as it applies the entry locally, without
// necessarily waiting for the entry to replicate to any surviving voter
// first. The caller's demotion path (TenantShardReconciler, driven by the
// placement update removeReplicaFromGroup already persisted) tears the
// removed replica down almost immediately once the config change commits --
// if that happens before the entry reaches a surviving voter, that voter is
// stranded believing the OLD (larger) configuration still applies, and can
// never win an election alone (this was observed directly: a 2-voter group
// whose leader self-removed left the surviving voter campaigning forever
// because it never received the RemoveNode entry). Transferring leadership
// away FIRST avoids the race entirely: the entry is proposed and driven to
// full replication by a leader that is NOT the one being removed.
//
// before is the placement snapshot BEFORE replicaID was dropped, used to
// locate another ACTIVE (voting) replica to transfer to -- only a voting
// member can become leader, so an OBSERVER or ObserverReady replica is
// never a valid transfer target. A no-op (nil error) when replicaID is not
// currently the leader, or when no other voting replica exists to transfer
// to (every DatabaseService.scaleDown call site is provably unaffected:
// selectReplicasToRemove never selects the current leader for removal
// unless it is the sole remaining voter, which this function also treats as
// a no-op since there is nowhere to transfer to).
func transferLeadershipAwayFromRemoved(ctx context.Context, raftHost RaftHost, before *placement.Placement, shardID, replicaID uint64) error {
	leaderID, ok, err := raftHost.LeaderReplicaID(ctx, shardID)
	if err != nil || !ok || leaderID != replicaID {
		return nil
	}

	var target uint64
	for _, r := range before.Replicas {
		if r.ReplicaID != replicaID && r.State == placement.ReplicaStateActive {
			target = r.ReplicaID
			break
		}
	}
	if target == 0 {
		return nil
	}

	if terr := raftHost.RequestLeaderTransfer(ctx, shardID, target); terr != nil {
		return terr
	}

	deadline := time.Now().Add(defaultLeaderTransferWaitTimeout)
	for {
		if id, ok, lerr := raftHost.LeaderReplicaID(ctx, shardID); lerr == nil && ok && id != replicaID {
			return nil
		}
		if time.Now().After(deadline) {
			return &LeaderTransferTimeoutError{ShardID: shardID, FromReplicaID: replicaID, Timeout: defaultLeaderTransferWaitTimeout}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(defaultLeaderTransferWaitInterval):
		}
	}
}

// resolveGroupLeader determines whether selfNodeID is the current Raft
// leader of the dedicated group behind placement p, consulting raftHost's
// live leader state. Shared by DatabaseService.resolveScaleLeader and
// RebalanceService.resolveMoveLeader so a scale or MOVE membership change is
// routed to the correct node with identical semantics -- see
// resolveScaleLeader's doc comment for the full return-value rationale,
// which applies unchanged here.
func resolveGroupLeader(ctx context.Context, raftHost RaftHost, selfNodeID string, p *placement.Placement) (isLocalLeader bool, leaderAddr string, err error) {
	if raftHost == nil || selfNodeID == "" {
		return true, "", nil
	}

	var local *placement.Replica
	for i := range p.Replicas {
		if p.Replicas[i].NodeID == selfNodeID {
			local = &p.Replicas[i]
			break
		}
	}
	if local == nil {
		// The local node is not a member of this dedicated group at all
		// (e.g. an RF=1 placement landed its sole replica on a different
		// node): raftHost has no local Raft state for the shard, so it
		// cannot resolve a live leader either. Redirect to any placement
		// member -- for RF=1 that member IS the leader; for RF>1 it is
		// itself a member and will re-resolve (and re-forward if needed)
		// using its own live state.
		return false, p.Replicas[0].RaftAddr, nil
	}

	leaderID, ok, lerr := raftHost.LeaderReplicaID(ctx, p.ShardID)
	if lerr != nil || !ok {
		return true, "", nil
	}
	if leaderID == local.ReplicaID {
		return true, "", nil
	}
	for i := range p.Replicas {
		if p.Replicas[i].ReplicaID == leaderID {
			return false, p.Replicas[i].RaftAddr, nil
		}
	}
	// The live leader replica ID is not present in this (possibly stale)
	// placement snapshot -- degrade to local execution rather than forward
	// to an unresolvable address.
	return true, "", nil
}

// DatabaseService implements DatabaseServicer using a database.Manager.
type DatabaseService struct {
	manager            *database.Manager
	barrierMgr         *database.DatabaseBarrierManager
	sealer             clusterseal.Sealer
	quotaChecker       quota.Checker
	entitlementChecker quota.EntitlementChecker
	tenantInfo         TenantInfoLookup
	quotaUsage         QuotaUsageRecorder
	placementStore     *placement.Store
	placementScheduler *placement.Scheduler
	keyDistributor     KeyDistributor
	barrierLookup      TenantBarrierLookup
	localNodeID        string
	raftHost           RaftHost
	logger             *slog.Logger

	// scaleLeaderWaitTimeout/scaleLeaderWaitInterval bound
	// waitForShardLeaderKnown, called by scaleUp/scaleDown before issuing a
	// membership change. Defaulted in NewDatabaseService and overridable via
	// WithScaleLeaderWaitTimeout/WithScaleLeaderWaitInterval, primarily so
	// tests can exercise the wait/timeout paths without the full production
	// bound.
	scaleLeaderWaitTimeout  time.Duration
	scaleLeaderWaitInterval time.Duration

	// compactionPolicies reaches a database's scheduled-compaction policy in
	// the database's own store; nil until the server wires it
	// (SetCompactionPolicies), and then UpdateCompactionForTenant refuses.
	compactionPolicies CompactionPolicies

	// scaleMu serializes concurrent ScaleForTenant calls to the same
	// (tenant, database) on this node -- see ScaleForTenant for the
	// concurrency rationale (§F of the elastic-scaling design). Keyed by
	// tenantID+"/"+name, values are *sync.Mutex.
	scaleMu sync.Map
}

// Compile-time interface check.
var _ DatabaseServicer = (*DatabaseService)(nil)

var _ CompactionPolicyWiring = (*DatabaseService)(nil)

// NewDatabaseService creates a new DatabaseService backed by the given manager.
func NewDatabaseService(manager *database.Manager, opts ...DatabaseServiceOption) DatabaseServicer {
	s := &DatabaseService{
		manager:                 manager,
		logger:                  slog.Default(),
		scaleLeaderWaitTimeout:  defaultScaleLeaderWaitTimeout,
		scaleLeaderWaitInterval: defaultScaleLeaderWaitInterval,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// SetDistributor injects the key distributor, tenant barrier lookup, and local
// node ID after construction. The daemon calls this after the
// clusterkeys.Sender and LeaderAdminResolver are available (which is later in
// the startup sequence than when the DatabaseService is built by
// lifecycle.BuildServices). Thread-safety: this must be called before any
// tenant database creation requests are served.
func (s *DatabaseService) SetDistributor(d KeyDistributor, lookup TenantBarrierLookup, nodeID string) {
	s.keyDistributor = d
	s.barrierLookup = lookup
	s.localNodeID = nodeID
}

// SetRaftHost injects the Raft membership-change handle after construction.
// The daemon calls this after the raft host adapter is available (which is
// later in the startup sequence than when DatabaseService is built by
// lifecycle.BuildServices). Thread-safety: this must be called before any
// tenant database creation requests are served.
func (s *DatabaseService) SetRaftHost(h RaftHost) { s.raftHost = h }

// Create creates a new database with the given configuration and returns
// its runtime information. Equivalent to CreateWithCredentials with an empty
// seal.Credentials -- barrier initialization with an empty secret only
// succeeds when cfg.Barrier is nil or not enabled.
func (s *DatabaseService) Create(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return s.CreateWithCredentials(context.Background(), cfg, seal.Credentials{})
}

// CreateWithCredentials creates a new database with the given configuration,
// using creds to initialize a barrier when cfg.Barrier.Enabled is true.
//
// This method is additive to DatabaseServicer (not part of the interface) so
// existing DatabaseServicer implementers and mocks are unaffected. Transport
// handlers that accept barrier credentials at create time (e.g.
// --barrier-passphrase) should type-assert the resolved DatabaseServicer for
// DatabaseCredentialCreator before falling back to plain Create.
func (s *DatabaseService) CreateWithCredentials(ctx context.Context, cfg database.DatabaseConfig, creds seal.Credentials) (*database.DatabaseInfo, error) {
	// A Shamir M-of-N the seal cannot split is refused before anything is
	// reserved or persisted; the manager checks it again in Validate.
	if err := cfg.Barrier.Validate(); err != nil {
		return nil, err
	}
	if err := refuseEtcdNotCompiled(cfg.Protocol.Etcd); err != nil {
		return nil, err
	}
	if err := s.validateRaftTopology(cfg.Name, &cfg); err != nil {
		return nil, err
	}
	db, err := s.manager.CreateWithCredentials(ctx, cfg, creds)
	if err != nil {
		return nil, err
	}
	if err := s.clearPredecessorSeal(ctx, db); err != nil {
		s.rollBackCreate(ctx, db, err)
		return nil, err
	}
	info := db.Info()
	return &info, nil
}

// Get returns runtime information about a specific database.
func (s *DatabaseService) Get(name string) (*database.DatabaseInfo, error) {
	db, err := s.manager.Get(name)
	if err != nil {
		return nil, err
	}
	info := db.Info()
	return &info, nil
}

// List returns runtime information about all open databases, sorted by name.
func (s *DatabaseService) List() ([]database.DatabaseInfo, error) {
	return s.manager.List(), nil
}

// Drop closes and removes a database, reclaiming its shared-store keyspace,
// its barrier and its data directory. For a dedicated-raft database, it
// also commits the replicated placement drop fence before reclaiming
// anything and removes the placement entry once the drop completes -- see
// DropForTenant, which shares dropRaftPlacement/clearRaftPlacementFence/
// finishRaftPlacementTeardown, for the fence's rationale and retry
// semantics (go-qrdb#482). See database.Manager.Drop for the ordering and
// for what an interrupted drop leaves behind.
//
// The owning tenant is resolved from the local catalog because this,
// unlike DropForTenant, has no tenant-scoped context to read it from. That
// lookup fails once an earlier attempt has already removed the local
// catalog entry -- at that point the caller is retrying a drop whose
// manager-level work is done and only the placement teardown remains, but
// this entry point cannot identify WHICH tenant's placement to finish
// tearing down without it, so that retry is left to DropForTenant (which
// has the tenant from its own context) instead. It DOES succeed, however,
// when the local record is merely FENCED rather than gone -- Manager.Get
// returns *database.DroppingError in that case (a restart mid-drop reopens
// the database still fenced), and that error itself carries the tenant ID
// (go-qrdb#482 round 3): without this fallback, a retry issued after a
// restart could never resume a placement-fenced drop at all, since the
// normal lookup path is exactly what the fence refuses.
func (s *DatabaseService) Drop(ctx context.Context, name string) error {
	if err := database.CheckDroppable(name); err != nil {
		return err
	}
	tenantID := ""
	if db, err := s.manager.Get(name); err == nil {
		tenantID = db.TenantID()
	} else {
		var dropping *database.DroppingError
		if errors.As(err, &dropping) {
			tenantID = dropping.TenantID
		}
	}

	var hadPlacement, owned bool
	if tenantID != "" {
		var err error
		hadPlacement, owned, err = s.dropRaftPlacement(ctx, tenantID, name)
		if err != nil {
			return err
		}
	}

	if err := s.manager.Drop(ctx, name); err != nil {
		if hadPlacement && owned && isReclamationFailure(err) {
			s.clearRaftPlacementFence(ctx, tenantID, name)
		}
		return err
	}

	if hadPlacement {
		if err := s.markRaftPlacementDropCommitted(ctx, tenantID, name); err != nil {
			return err
		}
		return s.finishRaftPlacementTeardown(ctx, tenantID, name)
	}
	return nil
}

// isReclamationFailure reports whether err is a failed manager-level drop
// that was rolled back COMPLETELY (database.DropRolledBack): one that never
// got past persisting its local fence, or one whose store fence and then
// local fence were both cleared again. Only then may the replicated
// placement fence be rolled back too. Every other failure -- a backend
// close, a directory removal, or a rollback whose store fence could not be
// cleared -- leaves the database fenced locally and in its store, and the
// replicated fence must stay set to match: clearing it would route every
// node's traffic to a database that refuses every write (go-qrdb#482).
func isReclamationFailure(err error) bool {
	return database.DropRolledBack(err)
}

// dropRaftPlacement commits the replicated drop fence (placement.Placement.
// Dropping) for (tenantID, name) BEFORE any reclamation runs, so every
// node's own resolution (RaftBackendFactory.Services) refuses new
// operations against the database immediately, cluster-wide -- not only on
// whichever node handles this Drop call (go-qrdb#482). This is what a
// node-local database.Manager fence cannot provide on its own for a
// dedicated-raft database: a follower's local catalog may legitimately not
// carry the entry at all, but every replica reads the same placement table.
//
// hadPlacement is true only when a placement entry was found (and
// therefore fenced): a database with no placement entry is not a
// dedicated-raft database, or placement is not wired into this
// composition, and database.Manager's own local fence already covers it
// either way. It is deliberately a different signal from "is this database
// raft-backed" (info.Backend == database.BackendRaft): a raft-backed
// database can exist with no placement store wired in at all (a
// single-node deployment with no scheduler), in which case there is
// nothing here to fence.
//
// owned reports whether THIS call performed the false-to-true transition
// (see Store.TryMarkDropping): false means another attempt -- concurrent, or
// an earlier one still live after a restart -- already owns the fence, and
// this call's caller must never be the one to clear it (go-qrdb#482 round 3,
// finding 4).
func (s *DatabaseService) dropRaftPlacement(ctx context.Context, tenantID, name string) (hadPlacement, owned bool, err error) {
	if s.placementStore == nil {
		return false, false, nil
	}
	if _, err := s.placementStore.Get(ctx, tenantID, name); err != nil {
		if errors.Is(err, placement.ErrPlacementNotFound) {
			return false, false, nil
		}
		return false, false, err
	}
	owned, err = s.placementStore.TryMarkDropping(ctx, tenantID, name)
	if err != nil {
		return true, false, err
	}
	return true, owned, nil
}

// clearRaftPlacementFence rolls back dropRaftPlacement's fence after a
// failed manager-level drop, so the placement entry -- which still
// describes a live database -- is resolvable again. Callers MUST gate this
// on dropRaftPlacement's own owned result and on isReclamationFailure: see
// their doc comments for why an unowned fence or a non-reclamation failure
// must never reach this call (go-qrdb#482 round 3, findings 4 and 7).
//
// The failure is logged, not returned: the manager-level drop already
// failed and is what the caller reports, and a fence that failed to clear
// leaves the database in the SAME fail-closed state the failed drop already
// left it in, not a new one -- an operator retrying the drop calls
// dropRaftPlacement again regardless.
func (s *DatabaseService) clearRaftPlacementFence(ctx context.Context, tenantID, name string) {
	// Under a context the failed request's cancellation cannot reach: see
	// database.DropRollbackContext.
	rollbackCtx, cancel := database.DropRollbackContext(ctx)
	defer cancel()
	if err := s.placementStore.SetDropping(rollbackCtx, tenantID, name, false); err != nil {
		s.logger.Warn("failed to clear the replicated drop fence after a failed drop",
			slog.String("tenant", tenantID), slog.String("database", name), slog.Any("error", err))
	}
}

// markRaftPlacementDropCommitted records, via Store.MarkDropCommitted, that
// this node's database-level drop just succeeded, BEFORE the placement
// entry itself is removed by finishRaftPlacementTeardown. See
// Placement.DropCommitted's doc comment for why a later retry landing on a
// different node needs this signal to tell "an earlier attempt already
// finished tearing this database down" apart from "this node never had the
// local record to begin with" (go-qrdb#482 round 3, finding 5).
//
// The failure is returned, not merely logged (round 3's second review,
// finding 8): the caller must fail the whole Drop/DropForTenant call
// rather than proceed to finishRaftPlacementTeardown regardless. If that
// deletion then ALSO fails -- or the process crashes between the two -- a
// marker that silently never got set would strand the surviving,
// DropCommitted=false placement entry exactly as finding 5 describes,
// permanently unable to prove to a later retry (on any node) that the
// local drop this entry is fenced for has, in fact, already happened.
// Reporting failure here instead forces a retry, which re-attempts this
// idempotent marker (and then the deletion) until both converge.
func (s *DatabaseService) markRaftPlacementDropCommitted(ctx context.Context, tenantID, name string) error {
	return s.placementStore.MarkDropCommitted(ctx, tenantID, name)
}

// raftPlacementDropAlreadyCommitted reports whether (tenantID, name)'s
// placement entry already records a completed local drop
// (Placement.DropCommitted), or has already been fully torn down (no entry
// at all -- an earlier retry finished both halves). See
// Placement.DropCommitted's doc comment: this is the signal that lets
// DropForTenant tell a legitimate retry-after-teardown apart from a request
// that reached a node which never had the local catalog record at all
// (go-qrdb#482 round 3, finding 5).
func (s *DatabaseService) raftPlacementDropAlreadyCommitted(ctx context.Context, tenantID, name string) (bool, error) {
	p, err := s.placementStore.Get(ctx, tenantID, name)
	if err != nil {
		if errors.Is(err, placement.ErrPlacementNotFound) {
			return true, nil
		}
		return false, err
	}
	return p.DropCommitted, nil
}

// finishRaftPlacementTeardown removes (tenantID, name)'s placement entry --
// which also removes the Dropping fence, since the whole entry goes with
// it -- so the reconciler tears down its replicas.
//
// UNLIKE the reclamation and the manager-level drop, a failure here is NOT
// swallowed and logged: dropRaftPlacement's fence survives together with
// the entry it is part of, which is exactly what keeps every node's
// placement-based resolution refusing new operations instead of a stale
// entry that is simultaneously unreachable (the local catalog record is
// already gone) AND unrefused (nothing marks the shard as still being torn
// down). Reporting success here over that would be the same defect
// go-qrdb#412 reported for the shared-store keyspace, one layer up. The
// caller retrying the drop -- through DropForTenant, which recognizes an
// already-gone local catalog entry as "finish the placement teardown", not
// as a failure -- is what completes it.
func (s *DatabaseService) finishRaftPlacementTeardown(ctx context.Context, tenantID, name string) error {
	return s.placementStore.Delete(ctx, tenantID, name)
}

// UpdateProtocol updates the wire protocol configuration for the named
// database and returns the updated runtime information.
func (s *DatabaseService) UpdateProtocol(name string, protocol database.ProtocolConfig) (*database.DatabaseInfo, error) {
	if err := s.manager.UpdateProtocol(name, protocol); err != nil {
		return nil, err
	}
	db, err := s.manager.Get(name)
	if err != nil {
		return nil, err
	}
	info := db.Info()
	return &info, nil
}

// refuseEtcdNotCompiled returns a typed *CapabilityUnsupportedError when etcd
// is being enabled on a binary not built with qrdb_etcd, or nil otherwise
// (etcd not requested, or this binary has it). Every entry point that can
// persist an EtcdConfig -- Create, CreateForTenant, UpdateEtcd,
// UpdateEtcdForTenant -- calls this before the manager is reached, so the
// typed refusal is what every transport sees and nothing is ever written for
// a protocol this binary cannot serve. See docs/etcd-compatibility/
// architecture.md's Build Tags section.
func refuseEtcdNotCompiled(etcd *database.EtcdConfig) error {
	if etcd == nil || !etcd.Enabled || capabilities.Has("etcd") {
		return nil
	}
	return &CapabilityUnsupportedError{Message: "database: etcd wire protocol not compiled in: requires build tag qrdb_etcd"}
}

// UpdateEtcd updates the etcd wire protocol configuration for the named
// database and returns the updated runtime information.
func (s *DatabaseService) UpdateEtcd(name string, etcd *database.EtcdConfig) (*database.DatabaseInfo, error) {
	if err := refuseEtcdNotCompiled(etcd); err != nil {
		return nil, err
	}
	if err := s.manager.UpdateEtcd(name, etcd); err != nil {
		return nil, err
	}
	db, err := s.manager.Get(name)
	if err != nil {
		return nil, err
	}
	info := db.Info()
	return &info, nil
}

// UpdateRaft updates the Raft consensus configuration for the named
// database and returns the updated runtime information.
func (s *DatabaseService) UpdateRaft(name string, raft *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	if err := s.manager.UpdateRaft(name, raft); err != nil {
		return nil, err
	}
	db, err := s.manager.Get(name)
	if err != nil {
		return nil, err
	}
	info := db.Info()
	return &info, nil
}

// UpdateBarrier updates the encryption barrier configuration for the named
// database and returns the updated runtime information.
func (s *DatabaseService) UpdateBarrier(name string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	if err := s.manager.UpdateBarrier(name, barrier); err != nil {
		return nil, err
	}
	db, err := s.manager.Get(name)
	if err != nil {
		return nil, err
	}
	info := db.Info()
	return &info, nil
}

// UpdateConnection updates the remote connection configuration for the named
// database and returns the updated runtime information.
func (s *DatabaseService) UpdateConnection(name string, conn *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	if err := s.manager.UpdateConnection(name, conn); err != nil {
		return nil, err
	}
	db, err := s.manager.Get(name)
	if err != nil {
		return nil, err
	}
	info := db.Info()
	return &info, nil
}

// UpdateSQL updates the SQL settings of the named database -- its
// sql.concurrency, see docs/row-locking/ -- and returns the updated runtime
// information.
func (s *DatabaseService) UpdateSQL(name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	if err := s.manager.UpdateSQL(name, sqlCfg); err != nil {
		return nil, err
	}
	db, err := s.manager.Get(name)
	if err != nil {
		return nil, err
	}
	info := db.Info()
	return &info, nil
}

// authorizeDatabaseTenant verifies that the caller bound to ctx is permitted
// to perform a per-database control operation on the named database. Database
// names are a single global namespace, so a per-database barrier operation
// addressed purely by name could otherwise reach another tenant's database;
// transport RBAC scopes the caller but the service layer must independently
// fail closed (mirroring the tenant-ownership check the database.Manager
// applies to its *ForTenant methods, and the IAM cross-tenant rule).
//
// There is no cross-tenant database access. Every caller -- the system
// tenant's included -- may target only a database its own tenant owns
// (ownedDatabase); any other database answers with the same
// *database.NotFoundError a missing one gets, so the answer does not tell the
// caller which names other tenants hold. The system tenant owns the databases
// whose TenantID is the system tenant or empty (the bootstrap and legacy
// ones).
func (s *DatabaseService) authorizeDatabaseTenant(ctx context.Context, name string) error {
	_, err := s.ownedDatabase(ctx, name)
	return err
}

// ownedDatabase returns the named database when the caller's tenant
// (tenant.FromContext) owns it, and *database.NotFoundError when it is
// missing or another tenant's. It is the one ownership check every
// per-database verb of this service makes, for the system tenant as for any
// other: the system tenant is confined to its own databases exactly as a
// tenant is. Node-internal components that need a database regardless of its
// owner use the database manager directly, never a caller identity.
func (s *DatabaseService) ownedDatabase(ctx context.Context, name string) (*database.Database, error) {
	return s.manager.GetOwned(name, tenant.FromContext(ctx))
}

// SealDatabase seals the encryption barrier for the named database. Only the
// tenant that owns the database may seal it, the system tenant included. Once
// the cluster has activated cluster-seal the seal is recorded and every node
// holding the database's barrier seals it.
func (s *DatabaseService) SealDatabase(ctx context.Context, name string) error {
	// Authorize ownership BEFORE consulting the barrier manager: a non-owning
	// tenant must be answered as if the database did not exist, and must never
	// learn whether the target database or its barrier even exists.
	if err := s.authorizeDatabaseTenant(ctx, name); err != nil {
		return err
	}
	// A Shamir barrier is sealed by a quorum of its holders
	// (DatabaseBarrierQuorumSubmit), never by one caller -- refused before
	// anything is recorded.
	if s.barrierMgr != nil && s.barrierMgr.RequiresQuorum(name) {
		return &database.BarrierSealError{Name: name, Err: seal.ErrShamirBarrierRequiresQuorum}
	}
	return s.sealOwnedDatabase(ctx, name)
}

// sealOwnedDatabase seals name, which the caller's tenant owns
// (authorizeDatabaseTenant has checked that): cluster-wide through the sealer
// when active -- the record first, then every node holding the database's
// barrier seals it -- on this node only otherwise, ending its streams.
func (s *DatabaseService) sealOwnedDatabase(ctx context.Context, name string) error {
	if s.barrierMgr == nil {
		return &BarrierManagerNotConfiguredError{}
	}
	if clusterseal.ActiveIn(s.sealer) {
		// A database with no barrier of its own has no key to wipe and none
		// to unseal with: a record sealing it could never be cleared.
		if _, err := s.barrierMgr.BarrierStatus(name); err != nil {
			return err
		}
		_, err := s.sealer.Seal(ctx, s.databaseBarrier(ctx, name), clusterseal.PrincipalFrom(ctx))
		return err
	}
	if err := s.sealDatabaseHere(name); err != nil {
		return err
	}
	// A node-local seal ends the database's open streams and its tenant's
	// SQL sessions, as a cluster seal does: after the barrier is sealed and
	// the database marked, so a stream opening between the two finds it
	// refused.
	if streams := clusterseal.StreamsOf(s.sealer); streams != nil {
		b := s.databaseBarrier(ctx, name)
		streams.EndDatabase(b.Tenant(), b.Database(), &clusterseal.SealedError{Barrier: b})
	}
	return nil
}

// SealDatabaseLocal seals, on this node, the database name owned by
// tenantID, and marks it sealed. It is the node enforcement a database seal
// record drives, and it seals only the database the record names: a database
// another tenant owns -- a name reused after a drop -- is answered as missing,
// *database.NotFoundError, and left alone. It authorizes no caller.
func (s *DatabaseService) SealDatabaseLocal(tenantID, name string) error {
	// The drop fence does not hold back a seal: a database being dropped
	// still has a key to wipe.
	if _, err := s.manager.GetOwnedIgnoringFence(name, tenantID); err != nil {
		return err
	}
	return s.sealDatabaseHere(name)
}

// SealBarrierDatabases seals every database barrier on this node and marks
// the databases sealed: the database half of SealWholeNode. See
// database.Manager.SealBarrierDatabases.
func (s *DatabaseService) SealBarrierDatabases() error {
	return s.manager.SealBarrierDatabases()
}

// sealDatabaseHere seals name's barrier on this node and marks the database
// sealed. Its callers have established whose database it is and carry the
// authority to seal it: an owner's seal of a non-Shamir barrier, a completed
// holder quorum, or a cluster seal record.
func (s *DatabaseService) sealDatabaseHere(name string) error {
	if s.barrierMgr == nil {
		return &BarrierManagerNotConfiguredError{}
	}
	if err := s.barrierMgr.SealAuthorized(name); err != nil {
		return err
	}
	// Reflect the sealed state on the Database's own status so
	// DatabaseInfo.Status (database info / database list) stays consistent
	// with the barrier registry's authoritative sealed/unsealed state.
	if db, err := s.manager.Get(name); err == nil {
		db.SetStatus(database.StatusSealed)
	}
	return nil
}

// UnsealDatabase unseals the encryption barrier for the named database on
// this node, owner only. When the database's cluster seal record says sealed,
// the same call clears it; if the clear cannot be written the barrier is
// sealed again and the call fails.
func (s *DatabaseService) UnsealDatabase(ctx context.Context, name string, creds seal.Credentials) error {
	if err := s.authorizeDatabaseTenant(ctx, name); err != nil {
		return err
	}
	if s.barrierMgr == nil {
		return &BarrierManagerNotConfiguredError{}
	}
	if err := s.barrierMgr.UnsealDatabase(ctx, name, creds); err != nil {
		return err
	}
	if db, err := s.manager.Get(name); err == nil {
		db.SetStatus(database.StatusOpen)
	}
	return s.clearDatabaseSeal(ctx, name)
}

// DatabaseBarrierStatus returns the barrier status for the named database,
// owner only, with its cluster seal state.
func (s *DatabaseService) DatabaseBarrierStatus(ctx context.Context, name string) (*DatabaseBarrierStatusInfo, error) {
	if err := s.authorizeDatabaseTenant(ctx, name); err != nil {
		return nil, err
	}
	if s.barrierMgr == nil {
		return nil, &BarrierManagerNotConfiguredError{}
	}
	st, err := s.barrierMgr.BarrierStatus(name)
	if err != nil {
		return nil, err
	}
	return &DatabaseBarrierStatusInfo{
		BarrierStatus: st,
		ClusterSeal:   clusterseal.ReportOf(s.sealer, s.databaseBarrier(ctx, name)),
	}, nil
}

// isolationBarrierDataPath maps each tenant isolation mode that serves its
// databases from a tenant-owned store to that store's description. A
// per-database barrier under one of these modes would wrap a store no request
// reads, so CreateForTenantWithCredentials refuses it with
// database.BarrierOffDataPathError.
var isolationBarrierDataPath = map[tenant.IsolationType]string{
	tenant.IsolationVolume: database.ServedByVolumeTenantStore,
	tenant.IsolationRaft:   database.ServedByRaftTenantGroup,
}

// CreateForTenant creates a new database scoped to the context's tenant
// and returns its runtime information. Equivalent to
// CreateForTenantWithCredentials with an empty seal.Credentials -- barrier
// initialization with an empty secret only succeeds when cfg.Barrier is nil
// or not enabled.
func (s *DatabaseService) CreateForTenant(ctx context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return s.CreateForTenantWithCredentials(ctx, cfg, seal.Credentials{})
}

// CreateForTenantWithCredentials creates a new database scoped to the
// context's tenant, using creds to initialize a barrier when
// cfg.Barrier.Enabled is true, and returns its runtime information.
//
// This method is additive to DatabaseServicer (not part of the interface);
// see CreateWithCredentials and DatabaseCredentialCreator.
func (s *DatabaseService) CreateForTenantWithCredentials(ctx context.Context, cfg database.DatabaseConfig, creds seal.Credentials) (*database.DatabaseInfo, error) {
	if err := cfg.Barrier.Validate(); err != nil {
		return nil, err
	}
	if err := refuseEtcdNotCompiled(cfg.Protocol.Etcd); err != nil {
		return nil, err
	}
	if tenantid.IsSystem(tenant.FromContext(ctx)) {
		// The system tenant creates its own databases. A tenant named in
		// the request is discarded, as a tenant caller's is: no caller
		// creates a database in another tenant.
		cfg.TenantID = ""
		return s.CreateWithCredentials(ctx, cfg, creds)
	}
	tenantID := tenant.FromContext(ctx)

	// A caller-supplied DataDir is never trusted for a tenant-scoped create:
	// it is only ever set below, computed server-side from the tenant's own
	// registered DataRoot for IsolationVolume/IsolationRaft tenants. Every
	// other tenant (prefix/shared isolation, or an isolation tenant with no
	// DataRoot yet allocated) falls through with DataDir cleared, which
	// routes it to database.Manager's own default path. Discarding client
	// input here closes a path-traversal vector where DataDir could
	// otherwise be pointed at another database's directory, a reserved
	// system path, or anywhere outside the intended storage tree;
	// database.Manager.validateDataDir provides defense-in-depth on top of
	// this for every caller, including the system-tenant path above.
	cfg.DataDir = ""

	// Entitlement plane (consulted before the numeric quota plane): verify the
	// tenant's plan permits the requested isolation tier / dedicated feature.
	// Raft-backed databases are a dedicated tier requiring both the "raft"
	// isolation-mode entitlement and the dedicated_raft feature.
	if s.entitlementChecker != nil && cfg.Backend == database.BackendRaft {
		if err := s.entitlementChecker.CheckIsolation(ctx, tenantID, quota.IsolationModeRaft); err != nil {
			return nil, err
		}
		if err := s.entitlementChecker.CheckFeature(ctx, tenantID, quota.FeatureDedicatedRaft); err != nil {
			return nil, err
		}
	}

	// IsolationVolume tenants store every database on their dedicated DataRoot
	// ({DataRoot}/databases/{db}) and run a per-tenant Raft Host. IsolationRaft
	// tenants store every database on their DataRoot too, but each database is a
	// dedicated Raft GROUP (its own shard) on the shared system Host. In both
	// cases re-verify the entitlements (defense-in-depth alongside the
	// tenant-create gate) and route the catalog DataDir under the tenant's
	// DataRoot so the on-disk location is physically isolated.
	//
	// tenantBarrierAutoUnseal is captured here so the Phase 4c-1c key
	// distribution below can determine whether the tenant uses system-managed
	// (true) or self-managed (false) barrier mode without a second GetTenant call.
	var tenantBarrierAutoUnseal bool
	if s.tenantInfo != nil {
		info, err := s.tenantInfo.GetTenant(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		// A volume- or raft-isolated tenant serves every database from the
		// tenant's own store, never from the per-database store a barrier
		// would wrap, so a per-database barrier there protects nothing.
		// Refused before any quota reservation or store I/O.
		if servedBy, offPath := isolationBarrierDataPath[info.EffectiveIsolationType()]; offPath && cfg.Barrier != nil && cfg.Barrier.Enabled {
			return nil, &database.BarrierOffDataPathError{Name: cfg.Name, ServedBy: servedBy}
		}
		switch info.IsolationType {
		case tenant.IsolationVolume:
			if s.entitlementChecker != nil {
				if err := s.entitlementChecker.CheckIsolation(ctx, tenantID, quota.IsolationModeVolume); err != nil {
					return nil, err
				}
				if err := s.entitlementChecker.CheckFeature(ctx, tenantID, quota.FeatureVolumeStorage); err != nil {
					return nil, err
				}
			}
			if info.DataRoot != "" {
				cfg.DataDir = filepath.Join(info.DataRoot, "databases", cfg.Name)
			}
		case tenant.IsolationRaft:
			if s.entitlementChecker != nil {
				if err := s.entitlementChecker.CheckIsolation(ctx, tenantID, quota.IsolationModeRaft); err != nil {
					return nil, err
				}
				if err := s.entitlementChecker.CheckFeature(ctx, tenantID, quota.FeatureDedicatedRaft); err != nil {
					return nil, err
				}
			}
			// Every database of an IsolationRaft tenant is a dedicated Raft group.
			// Force the raft backend and ensure a Raft config exists so the
			// Manager claims the database a cluster-wide shard ID.
			cfg.Backend = database.BackendRaft
			if cfg.Raft == nil {
				cfg.Raft = &database.DatabaseRaftConfig{ClusterMode: database.RaftLocal}
			} else if cfg.Raft.ClusterMode == "" {
				cfg.Raft.ClusterMode = database.RaftLocal
			}
			if info.DataRoot != "" {
				cfg.DataDir = filepath.Join(info.DataRoot, "databases", cfg.Name)
			}
			// Capture the barrier mode for the Phase 4c-1c key delivery below.
			tenantBarrierAutoUnseal = info.BarrierAutoUnseal
		}
	}

	// Reject an incoherent or unsupported raft topology request before any
	// quota reservation, placement, or store I/O -- see validateRaftTopology.
	if err := s.validateRaftTopology(cfg.Name, &cfg); err != nil {
		return nil, err
	}

	// Replicas (replication factor) is meaningful only for a dedicated-raft
	// database. Default an unset/invalid value to a single replica; for any
	// other backend the field is ignored entirely so it is neither persisted
	// nor metered. Multi-node placement of those replicas is a later stage; this
	// stage only carries, validates, and quota-meters the value.
	if cfg.Backend == database.BackendRaft {
		if cfg.Replicas < 1 {
			cfg.Replicas = 1
		}
		// Clamp the requested replication factor to the live cluster size: a
		// group cannot have more voting replicas than there are nodes
		// (anti-affinity places at most one replica per node). The static
		// per-group/aggregate quota caps are enforced separately below. When no
		// scheduler is wired the cluster size is unknown and only the quota caps
		// apply.
		if s.placementScheduler != nil {
			if size := s.placementScheduler.ClusterSize(); size >= 1 && cfg.Replicas > size {
				s.logger.Info("clamping replication factor to cluster size",
					slog.String("tenant", tenantID),
					slog.String("database", cfg.Name),
					slog.Int("requested", cfg.Replicas),
					slog.Int("cluster_size", size))
				cfg.Replicas = size
			}
		}
	} else {
		cfg.Replicas = 0
	}

	// 4d Option C: a per-tenant-KEY WAL opt-in is honored only for a
	// dedicated-raft database (the tenant's own barrier encrypts the dedicated
	// WAL). Gate the opt-in on both the dedicated_wal entitlement and the
	// MaxDedicatedWALGroups quota; a non-raft request silently clears the flag so
	// it is neither persisted nor metered. An ineligible raft request fails
	// closed rather than silently downgrading, so the caller learns the opt-in
	// was rejected (a downgrade-to-shared would weaken the requested isolation
	// without notice).
	if cfg.DedicatedWAL {
		if cfg.Backend != database.BackendRaft {
			cfg.DedicatedWAL = false
		} else {
			if s.entitlementChecker != nil {
				if err := s.entitlementChecker.CheckFeature(ctx, tenantID, quota.FeatureDedicatedWAL); err != nil {
					return nil, err
				}
			}
			if s.quotaChecker != nil {
				if err := s.quotaChecker.Check(ctx, tenantID, quota.ResourceDedicatedWALGroups); err != nil {
					return nil, err
				}
			}
		}
	}

	// Numeric quota plane: database count, plus raft-group count, per-group
	// replication-factor cap, and aggregate replica budget for raft databases.
	if s.quotaChecker != nil {
		if err := s.quotaChecker.Check(ctx, tenantID, quota.ResourceDatabases); err != nil {
			return nil, err
		}
		if cfg.Backend == database.BackendRaft {
			if err := s.quotaChecker.Check(ctx, tenantID, quota.ResourceRaftGroups); err != nil {
				return nil, err
			}
			replicas := int64(cfg.Replicas)
			// Per-group cap: this single group's RF must not exceed
			// MaxReplicasPerGroup (the ResourceReplicasPerGroup current usage is
			// always zero, so this reduces to replicas <= cap when capped).
			if err := s.quotaChecker.CheckN(ctx, tenantID, quota.ResourceReplicasPerGroup, replicas); err != nil {
				return nil, err
			}
			// Aggregate budget: existing total + this group's RF must not exceed
			// MaxTotalReplicas when capped.
			if err := s.quotaChecker.CheckN(ctx, tenantID, quota.ResourceTotalReplicas, replicas); err != nil {
				return nil, err
			}
		}
	}

	// Reserve the raft-group and total-replica usage before creating so both
	// quotas are enforced per dedicated-raft database (the Checks above read
	// these counters). Roll back every reservation if a later step fails.
	raftReserved := false
	replicasReserved := int64(0)
	dedicatedWALReserved := false
	if cfg.Backend == database.BackendRaft && s.quotaUsage != nil {
		if _, err := s.quotaUsage.IncrementQuotaUsage(ctx, tenantID, quota.ResourceRaftGroups, 1); err != nil {
			return nil, err
		}
		raftReserved = true
		replicas := int64(cfg.Replicas)
		if _, err := s.quotaUsage.IncrementQuotaUsage(ctx, tenantID, quota.ResourceTotalReplicas, replicas); err != nil {
			_, _ = s.quotaUsage.DecrementQuotaUsage(ctx, tenantID, quota.ResourceRaftGroups, 1)
			return nil, err
		}
		replicasReserved = replicas
		if cfg.DedicatedWAL {
			if _, err := s.quotaUsage.IncrementQuotaUsage(ctx, tenantID, quota.ResourceDedicatedWALGroups, 1); err != nil {
				_, _ = s.quotaUsage.DecrementQuotaUsage(ctx, tenantID, quota.ResourceTotalReplicas, replicas)
				_, _ = s.quotaUsage.DecrementQuotaUsage(ctx, tenantID, quota.ResourceRaftGroups, 1)
				return nil, err
			}
			dedicatedWALReserved = true
		}
	}

	// releaseReservations rolls back the quota reservations made above. It is
	// invoked when database creation or placement persistence fails.
	releaseReservations := func() {
		if raftReserved {
			_, _ = s.quotaUsage.DecrementQuotaUsage(ctx, tenantID, quota.ResourceRaftGroups, 1)
		}
		if replicasReserved > 0 {
			_, _ = s.quotaUsage.DecrementQuotaUsage(ctx, tenantID, quota.ResourceTotalReplicas, replicasReserved)
		}
		if dedicatedWALReserved {
			_, _ = s.quotaUsage.DecrementQuotaUsage(ctx, tenantID, quota.ResourceDedicatedWALGroups, 1)
		}
	}

	db, err := s.manager.CreateForTenantWithCredentials(ctx, cfg, creds)
	if err != nil {
		releaseReservations()
		return nil, err
	}
	if err := s.clearPredecessorSeal(ctx, db); err != nil {
		s.rollBackCreate(ctx, db, err)
		releaseReservations()
		return nil, err
	}
	info := db.Info()

	// Phase 4c-1b: for a dedicated-raft database, compute the replica placement
	// from current cluster topology and persist it in the replicated placement
	// table so the per-node reconciler (Phase 4c-1d) can materialize the
	// replicas. Computing the placement here makes the decision deterministic
	// and durable; this stage does NOT start shards across nodes. A placement
	// failure rolls the create back so the database is never left without a
	// placement entry that downstream stages depend on.
	if info.Backend == database.BackendRaft && s.placementStore != nil && s.placementScheduler != nil {
		plan, placeErr := s.persistPlacement(ctx, tenantID, &info)
		if placeErr != nil {
			if dropErr := s.manager.DropForTenant(ctx, cfg.Name); dropErr != nil {
				s.logger.Error("failed to roll back database after placement failure",
					slog.String("tenant", tenantID),
					slog.String("database", cfg.Name),
					slog.Any("error", dropErr))
			}
			releaseReservations()
			return nil, placeErr
		}

		// Phase 4c-1c: ship the tenant barrier key to every follower replica.
		// A delivery error must NOT fail the create — followers reconcile and
		// pull the key during their own shard start sequence (Phase 4c-1d).
		// All three of keyDistributor, barrierLookup, and localNodeID must be
		// present; single-node or non-clustered deployments leave them unset.
		if plan != nil && s.keyDistributor != nil && s.barrierLookup != nil && s.localNodeID != "" {
			tb, lookupErr := s.barrierLookup.GetTenant(tenantID)
			if lookupErr != nil {
				s.logger.Warn("clusterkeys: tenant barrier lookup failed, skipping key distribution",
					slog.String("tenant", tenantID),
					slog.String("database", info.Name),
					slog.Any("error", lookupErr))
			} else if deliverErr := s.keyDistributor.DistributeTenantKey(ctx, plan, tb.Barrier(), s.localNodeID, tenantBarrierAutoUnseal); deliverErr != nil {
				s.logger.Warn("clusterkeys: key distribution failed, followers will reconcile in Phase 4c-1d",
					slog.String("tenant", tenantID),
					slog.String("database", info.Name),
					slog.Any("error", deliverErr))
			}
		}

		// Phase 4c-1d (leader-side membership invite): after placement is
		// persisted the local leader calls AddFollowerNode for every follower
		// replica so the Raft group's initial config change is committed before
		// TenantShardReconciler starts materializing followers. This is an
		// optimistic best-effort step — if the dedicated shard is not yet running
		// (the reconciler materializes it asynchronously) AddFollowerNode returns
		// an error, which is logged non-fatally; the reconciler and Raft retries
		// converge. Only the leader node performs this step; followers are
		// identified as replicas whose NodeID differs from the local node ID.
		if plan != nil && s.raftHost != nil && s.localNodeID != "" {
			leaderReplica, leaderOK := plan.Leader()
			if leaderOK && leaderReplica.NodeID == s.localNodeID {
				for _, replica := range plan.Replicas {
					if replica.NodeID == s.localNodeID {
						continue
					}
					if addErr := s.raftHost.AddFollowerNode(ctx, plan.ShardID, replica.ReplicaID, replica.RaftAddr); addErr != nil {
						s.logger.Warn("raft: AddFollowerNode failed, reconciler will retry",
							slog.String("tenant", tenantID),
							slog.String("database", info.Name),
							slog.String("follower_node", replica.NodeID),
							slog.Any("error", addErr))
					}
				}
			}
		}
	}

	return &info, nil
}

// validateRaftTopology enforces that a raft-backed database's requested Raft
// cluster-mode topology is coherent with this node's configuration. It is a
// no-op for non-raft backends and for a raft-backed database with no explicit
// Raft config (the IsolationRaft branch above always defaults one).
//
//   - RaftNone paired with Backend == BackendRaft is rejected: a dedicated
//     Raft group inherently runs Raft consensus, so "none" (standalone,
//     non-raft) is an incoherent combination with a dedicated Raft backend.
//   - RaftDistributed requires the placement subsystem (WithPlacementStore +
//     WithPlacementScheduler) to be wired on this node -- the multi-node
//     replica placement, tenant-key distribution, and follower-invite
//     pipeline (see persistPlacement) that actually materializes replicas
//     across the cluster. Without it the request fails closed with
//     RaftDistributedUnavailableError instead of silently collapsing to a
//     single local replica.
//   - RaftLocal (and the unset default, which the IsolationRaft branch fills
//     in) runs the existing MVP path unchanged: when the placement subsystem
//     happens to be wired and Replicas > 1 is also requested, the request is
//     still honored via the same pipeline as today -- this method only adds
//     new, additive validation and never changes existing accepted requests.
func (s *DatabaseService) validateRaftTopology(name string, cfg *database.DatabaseConfig) error {
	if cfg.Backend != database.BackendRaft || cfg.Raft == nil {
		return nil
	}
	switch cfg.Raft.ClusterMode {
	case database.RaftNone:
		return &InvalidRaftTopologyError{
			Database: name,
			Mode:     string(cfg.Raft.ClusterMode),
			Reason:   `a dedicated raft-backed database requires cluster mode "local" or "distributed", not "none"`,
		}
	case database.RaftDistributed:
		if s.placementStore == nil || s.placementScheduler == nil {
			return &RaftDistributedUnavailableError{Database: name}
		}
	}
	return nil
}

// persistPlacement computes and stores the replica placement for a freshly
// created dedicated-raft database. The leader's DataRoot (dataDir) is recorded
// for the local node only; remote replicas receive an empty string so each
// follower resolves its own local pool path when the reconciler materializes
// the replica (Phase 4c-1d, BUG 1 fix). The computed plan is returned so the
// caller can pass it directly to the key distributor without a round-trip
// Store.Get call.
func (s *DatabaseService) persistPlacement(ctx context.Context, tenantID string, info *database.DatabaseInfo) (*placement.Placement, error) {
	rf := info.Replicas
	if rf < 1 {
		rf = 1
	}
	dataDir := info.DataDir
	localNodeID := s.localNodeID
	plan, err := s.placementScheduler.Place(ctx, placement.PlaceRequest{
		TenantID: tenantID,
		Database: info.Name,
		ShardID:  info.ShardID,
		RF:       rf,
		// Return the leader's dataDir only for the local node; remote replicas
		// record "" so each follower computes its own local pool path via the
		// LocalRootResolver wired into placementRaftInfoSource.
		DataRootForNode: func(nodeID string) string {
			if localNodeID == "" || nodeID == localNodeID {
				return dataDir
			}
			return ""
		},
	})
	if err != nil {
		return nil, err
	}
	// Propagate the per-tenant-KEY WAL opt-in to every replica via the
	// replicated placement table so followers materialized by the reconciler
	// route this shard's Raft log to a tenant-barrier-encrypted dedicated WAL.
	plan.DedicatedWAL = info.DedicatedWAL
	if err := s.placementStore.Put(ctx, plan); err != nil {
		return nil, err
	}
	s.logger.Info("persisted dedicated raft placement",
		slog.String("tenant", tenantID),
		slog.String("database", info.Name),
		slog.Uint64("shard_id", plan.ShardID),
		slog.Int("rf", plan.RF),
		slog.Uint64("leader_replica_id", plan.LeaderReplicaID))
	return plan, nil
}

// GetForTenant returns runtime information about a database after verifying
// tenant ownership via the context. For a dedicated-raft (distributed
// placement) database, the reported Replicas is refreshed from the
// replicated placement table (see refreshReplicasFromPlacement) so it stays
// accurate cluster-wide after a ScaleForTenant call, which very often
// executes on a different node than the one serving this read.
func (s *DatabaseService) GetForTenant(ctx context.Context, name string) (*database.DatabaseInfo, error) {
	db, err := s.ownedDatabase(ctx, name)
	if err != nil {
		return nil, err
	}
	info := db.Info()
	s.refreshReplicasFromPlacement(ctx, &info)
	return s.withCompactionPolicy(ctx, &info)
}

// ListForTenant returns runtime information about databases owned by the
// context's tenant, sorted by name. Each dedicated-raft database's Replicas
// is refreshed from the placement table; see refreshReplicasFromPlacement.
func (s *DatabaseService) ListForTenant(ctx context.Context) ([]database.DatabaseInfo, error) {
	infos, err := s.manager.ListForTenant(ctx)
	if err != nil {
		return nil, err
	}
	for i := range infos {
		s.refreshReplicasFromPlacement(ctx, &infos[i])
	}
	return infos, nil
}

// refreshReplicasFromPlacement overrides info.Replicas (and info.ShardID)
// with the replicated placement table's current values when a placement
// entry exists for (info.TenantID, info.Name). The placement table is the
// sole cluster-wide source of truth for a dedicated-raft database's
// replication factor: info.Replicas as sourced from db.Info() only reflects
// the local manager's meta.json, which is written once at create time on the
// node that ran the create and is never touched by a ScaleForTenant call
// executed on a different node (the dedicated group's current leader, which
// commonly is not the create node -- see ScaleForTenant). A no-op when the
// placement subsystem is not configured, the database is not raft-backed, or
// the database has no placement entry (predates placement, or was created
// with raft cluster mode "local").
func (s *DatabaseService) refreshReplicasFromPlacement(ctx context.Context, info *database.DatabaseInfo) {
	if s.placementStore == nil || info.Backend != database.BackendRaft {
		return
	}
	p, err := s.placementStore.Get(ctx, info.TenantID, info.Name)
	if err != nil {
		return
	}
	info.Replicas = p.RF
	info.ShardID = p.ShardID
}

// DropForTenant drops a database after verifying tenant ownership via the
// context. For a dedicated-raft (IsolationRaft) database it also commits the
// replicated placement drop fence BEFORE anything is reclaimed, releases the
// tenant's raft-group usage slot once the manager-level drop succeeds, and
// removes the placement entry last (see dropRaftPlacement,
// clearRaftPlacementFence, finishRaftPlacementTeardown, and Drop, which
// shares them) so every node's own resolution refuses new operations
// against the database for the whole of the drop, not only on the node that
// issued it (go-qrdb#482).
//
// This node must have a local record for name -- resolved via
// GetForTenantIgnoringFence, which (unlike GetForTenant) still returns the
// database while its local drop fence is set, since resuming this very
// drop's own claim is exactly the one caller the fence should never refuse
// -- before it ever touches the replicated fence. A request that reaches a
// node with NO local record at all (an ordinary, load-balanced client
// landing on a non-authoritative replica, not a retry) is refused
// immediately, UNLESS the replicated placement entry already shows
// DropCommitted (Placement.DropCommitted): that marker means an earlier
// attempt, on WHATEVER node held the record, already finished the local
// half, and this call only needs to finish the remaining placement
// teardown. Claiming the fence first and discovering the local absence only
// afterward -- the round-2 shape -- left a live database permanently fenced
// cluster-wide for a client with no guaranteed route back to the node that
// actually holds the record (go-qrdb#482 round 3, findings 6 and 9).
//
// Phase 4c: stopping the database's dedicated shard on the shared host (and
// removing its replicated data) is handled at the server layer alongside
// multi-node placement; this method releases only the on-disk catalog/dir,
// the replicated placement entry, and the quota slot.
func (s *DatabaseService) DropForTenant(ctx context.Context, name string) error {
	if err := database.CheckDroppable(name); err != nil {
		return err
	}
	tenantID := tenant.FromContext(ctx)
	if tenantid.IsSystem(tenantID) {
		// Bookkeeping, not ownership: ownedDatabase holds the system tenant to
		// the one ownership rule like every tenant, but a system database was
		// never metered against a quota nor given a placement entry
		// (CreateForTenant serves tenants), so there is nothing to release.
		if _, err := s.ownedDatabase(ctx, name); err != nil {
			return err
		}
		return s.Drop(ctx, name)
	}

	// wasRaft/replicaCount/wasDedicatedWAL back the quota release below.
	// GetForTenantIgnoringFence -- not GetForTenant -- so a resumed drop
	// (this node's own record, still locally fenced from before a crash)
	// still reports them instead of silently skipping every quota
	// decrement (go-qrdb#482 round 3, finding 9).
	wasRaft := false
	replicaCount := 0
	wasDedicatedWAL := false
	db, dbErr := s.manager.GetForTenantIgnoringFence(ctx, name)
	switch {
	case dbErr == nil:
		info := db.Info()
		wasRaft = info.Backend == database.BackendRaft
		replicaCount = info.Replicas
		wasDedicatedWAL = info.DedicatedWAL
	case s.placementStore != nil:
		// No local record on this node for name at all. See this method's
		// doc comment: refuse now, before the replicated fence is ever
		// touched, unless the replicated entry already proves an earlier
		// attempt finished the local half elsewhere.
		committed, cerr := s.raftPlacementDropAlreadyCommitted(ctx, tenantID, name)
		if cerr != nil {
			return cerr
		}
		if !committed {
			return dbErr
		}
	default:
		// No placement wired in at all (a single-node deployment with no
		// scheduler): there is no replicated signal to consult, so a
		// missing local record is unconditionally a genuine failure.
		return dbErr
	}

	hadPlacement, owned, err := s.dropRaftPlacement(ctx, tenantID, name)
	if err != nil {
		return err
	}

	if err := s.manager.DropForTenant(ctx, name); err != nil {
		var notFound *database.NotFoundError
		if hadPlacement && errors.As(err, &notFound) {
			committed, cerr := s.raftPlacementDropAlreadyCommitted(ctx, tenantID, name)
			if cerr != nil {
				return cerr
			}
			if committed {
				return s.finishRaftPlacementTeardown(ctx, tenantID, name)
			}
			// Defense in depth: the upfront GetForTenantIgnoringFence check
			// above already refuses a request with no local record and no
			// DropCommitted proof, so manager.DropForTenant should not be
			// able to reach here with a fresh, non-committed NotFoundError.
			// Falling through to return err below reports it truthfully
			// regardless (go-qrdb#482 round 3, findings 5 and 6).
		}
		if hadPlacement && owned && isReclamationFailure(err) {
			s.clearRaftPlacementFence(ctx, tenantID, name)
		}
		return err
	}

	if wasRaft && s.quotaUsage != nil {
		_, _ = s.quotaUsage.DecrementQuotaUsage(ctx, tenantID, quota.ResourceRaftGroups, 1)
		if replicaCount > 0 {
			_, _ = s.quotaUsage.DecrementQuotaUsage(ctx, tenantID, quota.ResourceTotalReplicas, int64(replicaCount))
		}
		if wasDedicatedWAL {
			_, _ = s.quotaUsage.DecrementQuotaUsage(ctx, tenantID, quota.ResourceDedicatedWALGroups, 1)
		}
	}

	// Phase 4c-1b: remove the database's placement entry so the reconciler
	// tears down its replicas -- see finishRaftPlacementTeardown for why a
	// failure here is no longer swallowed. markRaftPlacementDropCommitted
	// runs first so a retry that reaches finishRaftPlacementTeardown after a
	// failure here (or lands on a different node entirely) can tell this
	// drop apart from one that never actually happened -- see
	// raftPlacementDropAlreadyCommitted above.
	if hadPlacement {
		if err := s.markRaftPlacementDropCommitted(ctx, tenantID, name); err != nil {
			return err
		}
		return s.finishRaftPlacementTeardown(ctx, tenantID, name)
	}
	return nil
}

// UpdateProtocolForTenant updates the wire protocol configuration for the named
// database with tenant enforcement and returns the updated runtime information.
func (s *DatabaseService) UpdateProtocolForTenant(ctx context.Context, name string, protocol database.ProtocolConfig) (*database.DatabaseInfo, error) {
	if err := s.manager.UpdateProtocolForTenant(ctx, name, protocol); err != nil {
		return nil, err
	}
	return s.GetForTenant(ctx, name)
}

// UpdateEtcdForTenant updates the etcd wire protocol configuration for the named
// database with tenant enforcement and returns the updated runtime information.
func (s *DatabaseService) UpdateEtcdForTenant(ctx context.Context, name string, etcd *database.EtcdConfig) (*database.DatabaseInfo, error) {
	if err := refuseEtcdNotCompiled(etcd); err != nil {
		return nil, err
	}
	if err := s.manager.UpdateEtcdForTenant(ctx, name, etcd); err != nil {
		return nil, err
	}
	return s.GetForTenant(ctx, name)
}

// UpdateRaftForTenant updates the Raft consensus configuration for the named
// database with tenant enforcement and returns the updated runtime information.
func (s *DatabaseService) UpdateRaftForTenant(ctx context.Context, name string, raft *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	if err := s.manager.UpdateRaftForTenant(ctx, name, raft); err != nil {
		return nil, err
	}
	return s.GetForTenant(ctx, name)
}

// UpdateBarrierForTenant updates the encryption barrier configuration for the named
// database with tenant enforcement and returns the updated runtime information.
func (s *DatabaseService) UpdateBarrierForTenant(ctx context.Context, name string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	if err := s.manager.UpdateBarrierForTenant(ctx, name, barrier); err != nil {
		return nil, err
	}
	return s.GetForTenant(ctx, name)
}

// UpdateConnectionForTenant updates the remote connection configuration for the named
// database with tenant enforcement and returns the updated runtime information.
func (s *DatabaseService) UpdateConnectionForTenant(ctx context.Context, name string, conn *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	if err := s.manager.UpdateConnectionForTenant(ctx, name, conn); err != nil {
		return nil, err
	}
	return s.GetForTenant(ctx, name)
}

// UpdateSQLForTenant updates the SQL settings of the named database with
// tenant enforcement and returns the updated runtime information.
func (s *DatabaseService) UpdateSQLForTenant(ctx context.Context, name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	if err := s.manager.UpdateSQLForTenant(ctx, name, sqlCfg); err != nil {
		return nil, err
	}
	return s.GetForTenant(ctx, name)
}

// UpdateCompactionForTenant sets the scheduled-compaction policy of the named
// database and returns its updated runtime information. Every caller,
// the system tenant included, reaches only its own databases: a policy
// compacts that database's history, and no one compacts another tenant's.
//
// The policy is not catalog state. It is a replicated record in the
// database's own store, written through the database's own view
// (CompactionPolicies) as a Raft proposal, so every replica of the
// database's group holds it, a backup carries it, and whichever node leads
// the group runs it. See docs/key-value/engineering.md "Scheduled
// compaction".
func (s *DatabaseService) UpdateCompactionForTenant(ctx context.Context, name string, policy *compaction.Policy) (*database.DatabaseInfo, error) {
	if policy != nil {
		if err := policy.Validate(); err != nil {
			return nil, err
		}
	}
	owner, err := s.manager.CompactionOwnerForTenant(ctx, name)
	if err != nil {
		return nil, err
	}
	if s.compactionPolicies == nil {
		return nil, &CompactionPolicyUnavailableError{Database: name}
	}
	if err := s.compactionPolicies.SetCompactionPolicy(ctx, owner, name, policy); err != nil {
		return nil, err
	}
	return s.GetForTenant(ctx, name)
}

// SetCompactionPolicies implements CompactionPolicyWiring: the server gives
// the service the port to databases' stores once its registry exists, before
// it serves.
func (s *DatabaseService) SetCompactionPolicies(p CompactionPolicies) {
	s.compactionPolicies = p
}

// withCompactionPolicy reports info's scheduled-compaction policy from the
// database's own store. A sealed database's views are refused by design, so
// its policy is not reported while it is sealed; nothing else is skipped --
// a store that cannot be read fails the call.
func (s *DatabaseService) withCompactionPolicy(ctx context.Context, info *database.DatabaseInfo) (*database.DatabaseInfo, error) {
	if s.compactionPolicies == nil || info.Status == database.StatusSealed {
		return info, nil
	}
	owner := info.TenantID
	if owner == "" {
		owner = tenantid.System
	}
	policy, err := s.compactionPolicies.CompactionPolicy(ctx, owner, info.Name)
	if err != nil {
		return nil, err
	}
	info.Compaction = policy
	return info, nil
}

// ScaleForTenant changes the replication factor of an existing dedicated-raft
// database owned by the context's tenant, adding or removing voting replicas
// without recreating the database. It reuses the same quota-enforcement path
// as CreateForTenant (quotaChecker.CheckN on ResourceReplicasPerGroup and
// ResourceTotalReplicas) so a tenant can never scale past its own quota, and
// the same placement/RaftHost primitives moveTenantGroupShard uses.
//
// targetReplicas < 1 is rejected with *ReplicaFloorError (use DropForTenant to
// remove a database entirely). targetReplicas == the current replication
// factor is an idempotent no-op that returns the current DatabaseInfo without
// touching quota or placement -- this also makes a client-side retry of a
// call that already succeeded server-side safe to repeat. A database with no
// placement entry (predates placement, or was created with Raft cluster mode
// "local") is rejected with *NotDedicatedRaftDatabaseError.
//
// Concurrent ScaleForTenant calls for the same (tenant, database) on this
// node are serialized via scaleMu so PlaceAdditional's ReplicaID assignment
// (based on the current placement's max ReplicaID) and selectReplicasToRemove
// always observe a placement no other in-flight scale call is also mutating.
// Cross-node races are excluded upstream: RequestAddNodeWithIdentity/RequestRemoveNode are
// hard-rejected on any non-leader node, and non-leader transports forward to
// the leader rather than serving the request locally.
// resolveScaleLeader determines whether the local node is the current Raft
// leader of the dedicated group behind placement p, so ScaleForTenant can
// either execute the membership change locally or return a *NotLeaderError
// that lets the calling transport forward the request to a node that can.
// This reuses the same leader-forwarding mechanism the KV PUT/DELETE paths
// already use (REST's tryForwardWrite/forwardToLeader) rather than
// inventing a second one.
//
// Unlike CreateForTenant's plan.Leader() check (which only ever needs the
// STATIC founding leader, queried immediately after placement before any
// election could have occurred), ScaleForTenant must consult the LIVE Raft
// leader via raftHost.LeaderReplicaID: a prior scale-down may have removed
// the founding leader, or an ordinary election may have moved leadership.
//
// Returns (true, "", nil) -- execute locally -- when raftHost or
// localNodeID is unset (single-node / non-clustered deployments keep
// today's local-execution behavior), when the local node is confirmed to be
// the current leader, or when the current leader cannot be determined
// locally (degrades to local execution so the underlying
// RequestAddNodeWithIdentity/RequestRemoveNode call, which performs its own leader
// check, surfaces the definitive error). Returns (false, leaderAddr, nil)
// when another node is the leader (or, when the local node is not even a
// member of the group, the address of any member): leaderAddr is always a
// RaftAddr taken from p.Replicas so the REST layer's peerAdminRegistry can
// translate it to an admin URL.
func (s *DatabaseService) resolveScaleLeader(ctx context.Context, p *placement.Placement) (isLocalLeader bool, leaderAddr string, err error) {
	return resolveGroupLeader(ctx, s.raftHost, s.localNodeID, p)
}

const (
	// defaultScaleLeaderWaitTimeout bounds how long scaleUp/scaleDown wait
	// for the dedicated shard to report a known Raft leader before issuing a
	// membership change (AddObserver/RemoveFollowerNode). At RF=1 -- the
	// common scale-up starting point -- the sole replica has not necessarily
	// campaigned and won its own single-voter election by the instant
	// ScaleForTenant executes, and go-quicraft hard-rejects a membership
	// change with "not leader for shard N, leader unknown" until that
	// election completes. waitForShardLeaderKnown polls for it instead of
	// racing it. Overridable via WithScaleLeaderWaitTimeout.
	defaultScaleLeaderWaitTimeout = 10 * time.Second

	// defaultScaleLeaderWaitInterval is the default poll interval used by
	// waitForShardLeaderKnown. Overridable via WithScaleLeaderWaitInterval.
	defaultScaleLeaderWaitInterval = 250 * time.Millisecond
)

// waitForShardLeaderKnown polls host.LeaderReplicaID for shardID until a
// leader is known, ctx is cancelled, or timeout elapses. Returns
// *ShardLeaderTimeoutError on timeout (not a silent success) so
// scaleUp/scaleDown fail closed instead of proceeding to an
// AddObserver/RemoveFollowerNode call that go-quicraft is certain to
// reject while the shard's leader is unknown.
func waitForShardLeaderKnown(ctx context.Context, host RaftHost, shardID uint64, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, ok, err := host.LeaderReplicaID(ctx, shardID); err == nil && ok {
			return nil
		}
		if time.Now().After(deadline) {
			return &ShardLeaderTimeoutError{ShardID: shardID, Timeout: timeout}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (s *DatabaseService) ScaleForTenant(ctx context.Context, name string, targetReplicas int) (*database.DatabaseInfo, error) {
	tenantID := tenant.FromContext(ctx)

	// ScaleForTenant has no non-tenant-scoped counterpart, unlike the legacy
	// Update*ForTenant methods, since scaling was never available before
	// placement existed -- reject the system tenant explicitly rather than
	// relying on a manager lookup to do it implicitly (see below).
	if tenantid.IsSystem(tenantID) {
		return nil, &database.TenantIDRequiredError{Operation: "ScaleForTenant"}
	}

	if s.placementStore == nil || s.placementScheduler == nil {
		return nil, &RaftDistributedUnavailableError{Database: name}
	}

	muKey := tenantID + "/" + name
	muAny, _ := s.scaleMu.LoadOrStore(muKey, &sync.Mutex{})
	mu := muAny.(*sync.Mutex) //nolint:errcheck // type assertion on a value this container only ever holds
	mu.Lock()
	defer mu.Unlock()

	// The replicated placement table is the sole cluster-wide authority for
	// a dedicated-raft database's existence, tenant ownership, and current
	// replication factor. A scale request is routed (via resolveScaleLeader
	// + NotLeaderError below) to the dedicated group's CURRENT Raft leader,
	// which is very often NOT the node that originally created the database
	// -- that node's per-node singleton *database.Manager is the only place
	// the database is registered, so requiring a manager lookup here (as a
	// prior version of this method did) 404s on every node except the
	// creator. placementStore.Get is itself tenant-scoped (keyed by
	// tenantID+database), so a successful load already proves tenant
	// ownership -- no separate manager-based ownership check is needed.
	p, err := s.placementStore.Get(ctx, tenantID, name)
	if err != nil {
		if errors.Is(err, placement.ErrPlacementNotFound) {
			return nil, &NotDedicatedRaftDatabaseError{Database: name}
		}
		return nil, err
	}
	if p.Dropping {
		// A snapshot read after the fence was set must never proceed: a
		// scale that mutates quota and Raft membership while the database
		// is being torn down would leave the drop with a moving RF to
		// reconcile against, or resurrect capacity a completed drop already
		// released (go-qrdb#482 round 3, finding 5).
		return nil, &database.DroppingError{Name: name, TenantID: tenantID}
	}

	// A membership change must be issued on the dedicated group's current
	// Raft leader: go-quicraft rejects RequestAddNodeWithIdentity/RequestRemoveNode on
	// any other member, and a node that does not host the shard at all
	// cannot even ask (quicraft.ErrShardNotFound). Resolve this ONCE, up
	// front -- before quota reservation -- so a forwarded retry performs
	// quota reservation, placement mutation, and the Raft config change all
	// on the same (leader) node rather than splitting them across a forward.
	// The REST layer's tryForwardWrite/forwardToLeader (the same mechanism
	// already used for KV PUT/DELETE) proxies the request there.
	if isLeader, leaderAddr, rerr := s.resolveScaleLeader(ctx, p); rerr != nil {
		return nil, rerr
	} else if !isLeader {
		return nil, &NotLeaderError{ShardID: p.ShardID, LeaderAddress: leaderAddr}
	}

	// db is an OPTIONAL local-enrichment source, never a requirement: it is
	// only non-nil on the node that originally created this database (or one
	// that has otherwise loaded it into its own catalog). It is commonly nil
	// on the dedicated group's leader node once a scale request has been
	// forwarded here -- the normal, expected case for a distributed
	// placement whose leader differs from the creator. scaleInfoFromPlacement
	// builds a complete DatabaseInfo directly from placement when db is nil.
	db, _ := s.manager.GetForTenant(ctx, name)

	if targetReplicas < 1 {
		return nil, &ReplicaFloorError{Database: name, Requested: targetReplicas}
	}
	if targetReplicas == p.RF {
		return scaleInfoFromPlacement(db, p), nil
	}

	if targetReplicas > p.RF {
		return s.scaleUp(ctx, tenantID, name, db, p, targetReplicas, targetReplicas-p.RF)
	}
	return s.scaleDown(ctx, tenantID, name, db, p, targetReplicas, p.RF-targetReplicas)
}

// scaleInfoFromPlacement builds the *database.DatabaseInfo returned by
// ScaleForTenant (and its scaleUp/scaleDown helpers). When db is non-nil
// (the local per-node manager singleton happens to hold this database), its
// info is enriched with db.Info() and Replicas/ShardID are refreshed from p
// so the response always reports the just-applied (or concurrently changed)
// replication factor rather than a possibly stale local meta.json snapshot.
// When db is nil -- the common case for a distributed dedicated-raft
// database, since a scale request executes on the dedicated group's CURRENT
// leader, which very often never created the database in its own singleton
// manager -- info is built directly from placement, the sole cluster-wide
// source of truth for this database's identity and replication factor.
func scaleInfoFromPlacement(db *database.Database, p *placement.Placement) *database.DatabaseInfo {
	if db != nil {
		info := db.Info()
		info.Replicas = p.RF
		info.ShardID = p.ShardID
		return &info
	}
	return &database.DatabaseInfo{
		TenantID: p.TenantID,
		Name:     p.Database,
		Backend:  database.BackendRaft,
		Status:   database.StatusOpen,
		ShardID:  p.ShardID,
		Replicas: p.RF,
	}
}

// scaleUp implements the observer-first B.2 scale-up sequence (see the
// elastic-scaling design doc §B.2/§D.4): quota check, quota reserve,
// PlaceAdditional, then per new replica (ascending ReplicaID, one at a time)
// placement-put -> key-delivery (bounded-retried, see
// deliverTenantKeyToReplicaWithRetry) -> AddObserver. Each new replica is
// admitted as a NON-VOTING observer, never directly as a voter: adding a
// voter to a small group can transiently shrink the effective quorum below
// what the ALREADY-established leader controls (RF=1 -> 2 requires 2/2
// votes the instant the config change commits, but the brand-new replica
// cannot vote until it has caught up), causing an election storm on every
// scale-up that outraces the new replica's snapshot catch-up. An observer
// never counts toward quorum, so admitting one can never break it. Promotion
// to a full voting member happens asynchronously afterward, off this
// request path, via TenantShardReconciler's Observer -> ObserverReady (self
// -reported by the replica's own host once caught up) -> Active (performed
// by the shard's current leader) state machine -- see
// TenantShardReconciler.maybePromoteToObserverReady/maybePromoteReadyObservers.
//
// Placement MUST be persisted before key delivery is attempted -- the
// receiver's placement gate (pkg/clusterkeys/receiver.go) only authorizes a
// delivery once its own copy of the replicated placement table lists it as
// the assigned replica, so delivering the key first would have the receiver
// reject every attempt unconditionally (see the loop's own comment).
// Key-delivery errors are logged non-fatally (the follower reconciles and
// pulls the key during its own shard start sequence). A placement-put OR
// AddObserver failure fails the call and stops processing further replicas:
// unlike key delivery, nothing else in the system retries a config change
// that RequestAddObserverWithIdentity itself reported as failed, so silently continuing
// here would return a false success to the caller while the replica never
// actually joined the group. Only the unconsumed portion of the quota
// reservation (delta - the number of replicas actually persisted to
// placement AND admitted as observers) is rolled back, so a
// partially-applied scale-up plus a retry never double-reserves.
func (s *DatabaseService) scaleUp(ctx context.Context, tenantID, name string, db *database.Database, p *placement.Placement, targetReplicas, delta int) (*database.DatabaseInfo, error) {
	if s.quotaChecker != nil {
		if err := s.quotaChecker.CheckN(ctx, tenantID, quota.ResourceReplicasPerGroup, int64(targetReplicas)); err != nil {
			return nil, err
		}
		if err := s.quotaChecker.CheckN(ctx, tenantID, quota.ResourceTotalReplicas, int64(delta)); err != nil {
			return nil, err
		}
	}

	// AddObserver below is hard-rejected by go-quicraft until the shard
	// reports a known Raft leader. At RF=1 -- the common scale-up starting
	// point -- the sole replica has not necessarily campaigned and won its
	// own single-voter election by the instant ScaleForTenant executes, so
	// wait for that election to complete before reserving quota or computing
	// a placement plan that would otherwise go unused. Fails closed with a
	// typed error (no quota reservation, no placement mutation) rather than
	// racing an election that has not yet settled.
	if s.raftHost != nil {
		if err := waitForShardLeaderKnown(ctx, s.raftHost, p.ShardID, s.scaleLeaderWaitTimeout, s.scaleLeaderWaitInterval); err != nil {
			return nil, err
		}
	}

	reserved := false
	if s.quotaUsage != nil {
		if _, err := s.quotaUsage.IncrementQuotaUsage(ctx, tenantID, quota.ResourceTotalReplicas, int64(delta)); err != nil {
			return nil, err
		}
		reserved = true
	}

	plan, err := s.placementScheduler.PlaceAdditional(ctx, p, delta)
	if err != nil {
		if reserved {
			_, _ = s.quotaUsage.DecrementQuotaUsage(ctx, tenantID, quota.ResourceTotalReplicas, int64(delta))
		}
		return nil, err
	}

	baseMaxID := placement.MaxReplicaID(p)
	newReplicas := make([]placement.Replica, 0, delta)
	for _, r := range plan.Replicas {
		if r.ReplicaID > baseMaxID {
			newReplicas = append(newReplicas, r)
		}
	}
	sort.Slice(newReplicas, func(i, j int) bool { return newReplicas[i].ReplicaID < newReplicas[j].ReplicaID })

	current := p
	var added int
	var loopErr error
	for _, r := range newReplicas {
		// Placement must be persisted BEFORE key delivery is attempted, not
		// after -- see addObserverReplica's doc comment for the receiver
		// placement-gate rationale this ordering satisfies.
		updated, kerr, _, err := addObserverReplica(ctx, s.raftHost, s.placementStore, s.keyDistributor, s.barrierLookup, current, r, s.localNodeID)
		current = updated
		if kerr != nil {
			s.logger.Warn("clusterkeys: key distribution failed for scale-up replica, follower will reconcile",
				slog.String("tenant", tenantID),
				slog.String("database", name),
				slog.String("replica_node", r.NodeID),
				slog.Any("error", kerr))
		}
		if err != nil {
			loopErr = err
			break
		}
		added++
	}

	if reserved {
		if unconsumed := int64(delta - added); unconsumed > 0 {
			_, _ = s.quotaUsage.DecrementQuotaUsage(ctx, tenantID, quota.ResourceTotalReplicas, unconsumed)
		}
	}

	// The local per-node manager singleton is a best-effort display cache,
	// never a requirement: it is only non-nil on the node that originally
	// created this database, which very often is not the node that just
	// executed this scale (see ScaleForTenant). The placement table's RF
	// (current.RF) is the authoritative, cluster-wide value; a missing local
	// manager entry must never fail an otherwise-successful scale.
	if added > 0 && db != nil {
		if err := s.manager.UpdateReplicas(name, current.RF); err != nil {
			s.logger.Warn("database: local manager UpdateReplicas failed after scale-up (best-effort; the placement table's RF is authoritative)",
				slog.String("tenant", tenantID),
				slog.String("database", name),
				slog.Any("error", err))
		}
	}

	if loopErr != nil {
		return nil, loopErr
	}

	return scaleInfoFromPlacement(db, current), nil
}

// scaleDown implements the C.3 scale-down sequence: no quota check (freeing
// capacity never violates a cap), selectReplicasToRemove picks removeCount
// replicas (leader last), then per replica (one at a time) placement-update
// -> RemoveFollowerNode. Unlike scale-up's AddObserver, a
// RemoveFollowerNode error fails the whole call (the placement already
// reflects the intended post-scale state and the reconciler will eventually
// stop the removed replica locally; the caller retries). Whatever prefix of
// removals did succeed is still persisted (manager.UpdateReplicas + quota
// decrement for the actually-removed count), so a retry converges rather than
// double-applying.
func (s *DatabaseService) scaleDown(ctx context.Context, tenantID, name string, db *database.Database, p *placement.Placement, targetReplicas, removeCount int) (*database.DatabaseInfo, error) {
	var leaderReplicaID uint64
	if s.raftHost != nil {
		// RemoveFollowerNode below is hard-rejected by go-quicraft until the
		// shard reports a known Raft leader (see scaleUp's identical wait for
		// the rationale). Fail closed with a typed error before any
		// placement mutation rather than racing an election that has not yet
		// settled.
		if err := waitForShardLeaderKnown(ctx, s.raftHost, p.ShardID, s.scaleLeaderWaitTimeout, s.scaleLeaderWaitInterval); err != nil {
			return nil, err
		}
		if id, ok, err := s.raftHost.LeaderReplicaID(ctx, p.ShardID); err == nil && ok {
			leaderReplicaID = id
		}
	}

	toRemove := selectReplicasToRemove(p, removeCount, leaderReplicaID)

	current := p
	var removed int
	var loopErr error
	for _, r := range toRemove {
		updated, _, err := removeReplicaFromGroup(ctx, s.raftHost, s.placementStore, current, r.ReplicaID)
		current = updated
		if err != nil {
			loopErr = err
			break
		}
		removed++
	}

	if removed > 0 {
		// See scaleUp's identical comment: the local manager entry is a
		// best-effort display cache, never a requirement -- the placement
		// table's RF (current.RF) is authoritative.
		if db != nil {
			if err := s.manager.UpdateReplicas(name, current.RF); err != nil {
				s.logger.Warn("database: local manager UpdateReplicas failed after scale-down (best-effort; the placement table's RF is authoritative)",
					slog.String("tenant", tenantID),
					slog.String("database", name),
					slog.Any("error", err))
			}
		}
		if s.quotaUsage != nil {
			_, _ = s.quotaUsage.DecrementQuotaUsage(ctx, tenantID, quota.ResourceTotalReplicas, int64(removed))
		}
	}

	if loopErr != nil {
		return nil, loopErr
	}

	return scaleInfoFromPlacement(db, current), nil
}

// selectReplicasToRemove picks removeCount replicas to evict from p, ordered
// so every non-leader replica (descending ReplicaID -- newest-added-first,
// naturally undoing the most recent scale-ups before touching founding
// members) is removed before the current leader (leaderReplicaID, from
// RaftHost.LeaderReplicaID). The leader is appended last and is only ever
// actually selected when removeCount reaches the full replica count (out of
// ScaleForTenant's reach: the floor validation in ScaleForTenant guarantees
// removeCount <= len(p.Replicas)-1, so the leader is never returned by a
// valid call -- it is simply guaranteed to be the very last candidate if the
// caller ever did ask for all of them). leaderReplicaID == 0 (no known
// leader) degrades gracefully to plain descending-ReplicaID order.
func selectReplicasToRemove(p *placement.Placement, removeCount int, leaderReplicaID uint64) []placement.Replica {
	candidates := make([]placement.Replica, len(p.Replicas))
	copy(candidates, p.Replicas)
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ReplicaID > candidates[j].ReplicaID })

	ordered := make([]placement.Replica, 0, len(candidates))
	var leader *placement.Replica
	for i := range candidates {
		if candidates[i].ReplicaID == leaderReplicaID {
			l := candidates[i]
			leader = &l
			continue
		}
		ordered = append(ordered, candidates[i])
	}
	if leader != nil {
		ordered = append(ordered, *leader)
	}

	if removeCount > len(ordered) {
		removeCount = len(ordered)
	}
	return ordered[:removeCount]
}
