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
	"log/slog"
	"path/filepath"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// DatabaseServicer defines CRUD operations for database management.
type DatabaseServicer interface {
	// Create creates a new database with the given configuration.
	Create(cfg database.DatabaseConfig) (*database.DatabaseInfo, error)

	// Get returns information about a specific database.
	Get(name string) (*database.DatabaseInfo, error)

	// List returns information about all open databases.
	List() ([]database.DatabaseInfo, error)

	// Drop closes and removes a database, deleting its data.
	Drop(name string) error

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

	// SealDatabase seals the encryption barrier for a database.
	SealDatabase(ctx context.Context, name string) error

	// UnsealDatabase unseals the encryption barrier for a database.
	UnsealDatabase(ctx context.Context, name string, creds seal.Credentials) error

	// DatabaseBarrierStatus returns the barrier status for a database.
	DatabaseBarrierStatus(name string) (*seal.BarrierStatus, error)

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
// placed. When nil, the RequestAddNode step is skipped and the per-node
// TenantShardReconciler converges the follower start on subsequent ticks.
func WithRaftHost(h RaftHost) DatabaseServiceOption {
	return func(s *DatabaseService) { s.raftHost = h }
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
	AddFollowerNode(ctx context.Context, shardID, replicaID uint64, raftAddr string) error
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

// DatabaseService implements DatabaseServicer using a database.Manager.
type DatabaseService struct {
	manager            *database.Manager
	barrierMgr         *database.DatabaseBarrierManager
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
}

// Compile-time interface check.
var _ DatabaseServicer = (*DatabaseService)(nil)

// NewDatabaseService creates a new DatabaseService backed by the given manager.
func NewDatabaseService(manager *database.Manager, opts ...DatabaseServiceOption) DatabaseServicer {
	s := &DatabaseService{manager: manager, logger: slog.Default()}
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
// its runtime information.
func (s *DatabaseService) Create(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	db, err := s.manager.Create(cfg)
	if err != nil {
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

// Drop closes and removes a database, deleting its data directory.
func (s *DatabaseService) Drop(name string) error {
	return s.manager.Drop(name)
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

// UpdateEtcd updates the etcd wire protocol configuration for the named
// database and returns the updated runtime information.
func (s *DatabaseService) UpdateEtcd(name string, etcd *database.EtcdConfig) (*database.DatabaseInfo, error) {
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

// SealDatabase seals the encryption barrier for the named database.
func (s *DatabaseService) SealDatabase(ctx context.Context, name string) error {
	if s.barrierMgr == nil {
		return &BarrierManagerNotConfiguredError{}
	}
	return s.barrierMgr.SealDatabase(name)
}

// UnsealDatabase unseals the encryption barrier for the named database.
func (s *DatabaseService) UnsealDatabase(ctx context.Context, name string, creds seal.Credentials) error {
	if s.barrierMgr == nil {
		return &BarrierManagerNotConfiguredError{}
	}
	return s.barrierMgr.UnsealDatabase(ctx, name, creds)
}

// DatabaseBarrierStatus returns the barrier status for the named database.
func (s *DatabaseService) DatabaseBarrierStatus(name string) (*seal.BarrierStatus, error) {
	if s.barrierMgr == nil {
		return nil, &BarrierManagerNotConfiguredError{}
	}
	return s.barrierMgr.BarrierStatus(name)
}

// CreateForTenant creates a new database scoped to the context's tenant
// and returns its runtime information.
func (s *DatabaseService) CreateForTenant(ctx context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	if tenant.FromContext(ctx) == tenant.SystemTenantID {
		return s.Create(cfg)
	}
	tenantID := tenant.FromContext(ctx)

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
			// Manager's shard allocator assigns and persists a unique shard ID.
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

	db, err := s.manager.CreateForTenant(ctx, cfg)
	if err != nil {
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
// tenant ownership via the context.
func (s *DatabaseService) GetForTenant(ctx context.Context, name string) (*database.DatabaseInfo, error) {
	if tenant.FromContext(ctx) == tenant.SystemTenantID {
		return s.Get(name)
	}
	db, err := s.manager.GetForTenant(ctx, name)
	if err != nil {
		return nil, err
	}
	info := db.Info()
	return &info, nil
}

// ListForTenant returns runtime information about databases owned by the
// context's tenant, sorted by name.
func (s *DatabaseService) ListForTenant(ctx context.Context) ([]database.DatabaseInfo, error) {
	if tenant.FromContext(ctx) == tenant.SystemTenantID {
		return s.List()
	}
	return s.manager.ListForTenant(ctx)
}

// DropForTenant drops a database after verifying tenant ownership via the
// context. For a dedicated-raft (IsolationRaft) database it also releases the
// tenant's raft-group usage slot so MaxRaftGroups accounting stays accurate.
//
// Phase 4c: stopping the database's dedicated shard on the shared host (and
// removing its replicated data) is handled at the server layer alongside
// multi-node placement; this method releases only the on-disk catalog/dir and
// the quota slot.
func (s *DatabaseService) DropForTenant(ctx context.Context, name string) error {
	tenantID := tenant.FromContext(ctx)
	if tenantID == tenant.SystemTenantID {
		return s.Drop(name)
	}
	// Capture the backend and replica count before dropping so the raft-group
	// and total-replica counters can be released for a dedicated-raft database.
	wasRaft := false
	replicaCount := 0
	wasDedicatedWAL := false
	if db, err := s.manager.GetForTenant(ctx, name); err == nil {
		info := db.Info()
		wasRaft = info.Backend == database.BackendRaft
		replicaCount = info.Replicas
		wasDedicatedWAL = info.DedicatedWAL
	}
	if err := s.manager.DropForTenant(ctx, name); err != nil {
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
	// tears down its replicas. Best-effort: the catalog drop already succeeded,
	// so a stale placement entry must not fail the drop; it is logged instead.
	if wasRaft && s.placementStore != nil {
		if err := s.placementStore.Delete(ctx, tenantID, name); err != nil {
			s.logger.Warn("failed to remove placement entry on drop",
				slog.String("tenant", tenantID),
				slog.String("database", name),
				slog.Any("error", err))
		}
	}
	return nil
}

// UpdateProtocolForTenant updates the wire protocol configuration for the named
// database with tenant enforcement and returns the updated runtime information.
func (s *DatabaseService) UpdateProtocolForTenant(ctx context.Context, name string, protocol database.ProtocolConfig) (*database.DatabaseInfo, error) {
	if tenant.FromContext(ctx) == tenant.SystemTenantID {
		return s.UpdateProtocol(name, protocol)
	}
	if err := s.manager.UpdateProtocolForTenant(ctx, name, protocol); err != nil {
		return nil, err
	}
	return s.GetForTenant(ctx, name)
}

// UpdateEtcdForTenant updates the etcd wire protocol configuration for the named
// database with tenant enforcement and returns the updated runtime information.
func (s *DatabaseService) UpdateEtcdForTenant(ctx context.Context, name string, etcd *database.EtcdConfig) (*database.DatabaseInfo, error) {
	if tenant.FromContext(ctx) == tenant.SystemTenantID {
		return s.UpdateEtcd(name, etcd)
	}
	if err := s.manager.UpdateEtcdForTenant(ctx, name, etcd); err != nil {
		return nil, err
	}
	return s.GetForTenant(ctx, name)
}

// UpdateRaftForTenant updates the Raft consensus configuration for the named
// database with tenant enforcement and returns the updated runtime information.
func (s *DatabaseService) UpdateRaftForTenant(ctx context.Context, name string, raft *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	if tenant.FromContext(ctx) == tenant.SystemTenantID {
		return s.UpdateRaft(name, raft)
	}
	if err := s.manager.UpdateRaftForTenant(ctx, name, raft); err != nil {
		return nil, err
	}
	return s.GetForTenant(ctx, name)
}

// UpdateBarrierForTenant updates the encryption barrier configuration for the named
// database with tenant enforcement and returns the updated runtime information.
func (s *DatabaseService) UpdateBarrierForTenant(ctx context.Context, name string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	if tenant.FromContext(ctx) == tenant.SystemTenantID {
		return s.UpdateBarrier(name, barrier)
	}
	if err := s.manager.UpdateBarrierForTenant(ctx, name, barrier); err != nil {
		return nil, err
	}
	return s.GetForTenant(ctx, name)
}

// UpdateConnectionForTenant updates the remote connection configuration for the named
// database with tenant enforcement and returns the updated runtime information.
func (s *DatabaseService) UpdateConnectionForTenant(ctx context.Context, name string, conn *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	if tenant.FromContext(ctx) == tenant.SystemTenantID {
		return s.UpdateConnection(name, conn)
	}
	if err := s.manager.UpdateConnectionForTenant(ctx, name, conn); err != nil {
		return nil, err
	}
	return s.GetForTenant(ctx, name)
}
