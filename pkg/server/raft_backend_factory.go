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
	"path/filepath"
	"sync"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb/waldb"

	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// placementResolveTimeout bounds a placement-table lookup performed while
// resolving a dedicated-raft database's shard, so a stalled read pipeline does
// not block shard resolution indefinitely.
const placementResolveTimeout = 5 * time.Second

// ErrRaftDatabaseUnknown is returned when a dedicated-raft database cannot be
// resolved to a persisted shard (no on-disk metadata for the (tenant, db) pair).
var ErrRaftDatabaseUnknown = errors.New("server: dedicated-raft database not found")

// ErrRaftBackendUnavailable is returned when the RaftBackendFactory is consulted
// without the shared host, barrier registry, or database-info source configured.
var ErrRaftBackendUnavailable = errors.New("server: raft backend factory not configured")

// RaftBackendError wraps a failure starting, resolving, or stopping a dedicated
// per-database raft shard. It identifies the (tenant, db) pair and the operation.
type RaftBackendError struct {
	TenantID string
	Database string
	Op       string
	Err      error
}

// Error implements the error interface.
func (e *RaftBackendError) Error() string {
	return "server: raft backend " + e.Op + " for " + e.TenantID + "/" + e.Database + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for errors.Is/As.
func (e *RaftBackendError) Unwrap() error { return e.Err }

// raftShardKey identifies a dedicated raft shard by its owning tenant and
// database name. Two tenants may use the same database name; the composite key
// keeps their dedicated shards distinct.
type raftShardKey struct {
	tenantID string
	database string
}

// raftShard owns one dedicated per-database raft group running on the shared
// system Host. Its on-disk state-machine data lives under the tenant's DataRoot
// ({DataRoot}/databases/{db}/shard-<N>), encrypted at rest under the tenant
// barrier's DEK; the Raft log stays in the shared host LogDB. It exposes a full
// data-plane service set (KV/MVCC/lease/watch/timeseries/...) bound to its shard.
type raftShard struct {
	tenantID string
	database string
	shardID  uint64
	hub      *watch.Hub
	lessor   *lease.Lessor
	services *DatabaseServices
	cancel   context.CancelFunc

	// dedicatedWAL is the per-tenant-KEY WAL store registered in the shared
	// host's multiplex for this shard (4d Option C), or nil when the shard uses
	// the shared system-barrier WAL. On teardown it is unregistered and closed.
	dedicatedWAL logdb.LogDB
}

// raftDatabaseInfoSource resolves the dedicated shard ID and on-disk DataDir for
// a (tenant, db) pair from the persisted database metadata. It is satisfied by
// a database.Manager via managerRaftInfoSource and by the replicated placement
// table via placementRaftInfoSource.
type raftDatabaseInfoSource interface {
	// databaseInfo returns the allocated shard ID and on-disk DataDir for the
	// (tenant, db) database, or ok=false when no such database exists or it is
	// not owned by the tenant.
	databaseInfo(tenantID, db string) (shardID uint64, dataDir string, ok bool)

	// placementInfo returns the full Placement for (tenantID, db) when
	// placement-based routing is configured and the local node hosts a replica
	// of that database. It reports false when no placement entry exists, when
	// the local node has no assigned replica, or when placement routing is not
	// configured (meta-derived sources always return false).
	placementInfo(tenantID, db string) (*placement.Placement, bool)

	// dedicatedWAL reports whether the (tenant, db) database opted into a
	// per-tenant-KEY WAL (4d Option C). Meta-derived sources read the persisted
	// DatabaseInfo flag; the placement source reads the replicated entry so a
	// follower honors the same WAL routing the leader chose at create time.
	dedicatedWAL(tenantID, db string) bool
}

// RaftBackendFactory manages the lifecycle of dedicated per-database raft groups
// for IsolationRaft tenants. Each dedicated raft database runs as its own shard
// on the SHARED system Host: the shard's on-disk state-machine data is rooted
// under the tenant's DataRoot (via quicraftconfig.Config.DataDir) while the Raft
// log remains in the shared host LogDB. The factory builds and caches the
// data-plane service set per (tenant, db); the resolver routes IsolationRaft
// requests to it, and the daemon boot re-attaches each tenant's shards.
//
// RaftBackendFactory is safe for concurrent use.
type RaftBackendFactory struct {
	host           *quicraft.Host
	adapter        *raftbackend.HostAdapter
	localReplicaID uint64
	localRaftAddr  string
	barrierReg     *seal.BarrierRegistry
	dbInfo         raftDatabaseInfoSource
	logger         *slog.Logger

	// walMux, when non-nil, is the shared host's multiplex LogDB into which a
	// dedicated per-tenant-KEY WAL is registered for a shard that opted into
	// 4d Option C. It is nil for single-node/standalone hosts (no multiplex);
	// such hosts always fall back to the shared system-barrier WAL.
	walMux *database.MultiplexLogDB

	mu     sync.Mutex
	shards map[raftShardKey]*raftShard
}

// RaftBackendOption customizes a RaftBackendFactory.
type RaftBackendOption func(*RaftBackendFactory)

// WithMultiplexLogDB wires the shared host's multiplex LogDB so the factory can
// register a dedicated per-tenant-KEY WAL for shards that opted into 4d Option C.
// When unset (single-node/standalone, or seal disabled) every shard uses the
// shared system-barrier WAL (Route A) and the opt-in is ignored with a log line.
func WithMultiplexLogDB(mux *database.MultiplexLogDB) RaftBackendOption {
	return func(f *RaftBackendFactory) { f.walMux = mux }
}

// LocalRootResolver resolves the local volume-pool DataRoot for a tenant
// without opening any storage. It is injected into placementRaftInfoSource so
// follower replicas (whose placement Replica.DataRoot is empty) can determine
// their own local shard DataDir. It is satisfied by
// (*seal.BarrierRegistry).LocalTenantDataRoot.
type LocalRootResolver func(ctx context.Context, tenantID string) (string, error)

// WithPlacementInfoSource makes the replicated placement table the source of
// truth for resolving a dedicated-raft database's shard. When a placement entry
// for (tenant, db) exists and assigns a replica to localNodeID, that entry's
// shard ID and the local replica's DataRoot are used; otherwise resolution
// falls back to the meta.json-derived source so Phase-4b single-node databases
// (created before placement) keep working unchanged. store and localNodeID must
// be non-empty for the override to take effect.
//
// resolver is optional: when non-nil it is called to compute the local
// DataRoot for a follower replica whose placement Replica.DataRoot is empty
// (BUG 1 fix — leaders record "" for remote replicas so each node resolves
// its own path). When resolver is nil, replicas with an empty DataRoot fall
// back to the meta-derived source.
func WithPlacementInfoSource(store *placement.Store, localNodeID string, resolver ...LocalRootResolver) RaftBackendOption {
	return func(f *RaftBackendFactory) {
		if store == nil || localNodeID == "" || f.dbInfo == nil {
			return
		}
		var res LocalRootResolver
		if len(resolver) > 0 {
			res = resolver[0]
		}
		f.dbInfo = &placementRaftInfoSource{
			store:             store,
			localNodeID:       localNodeID,
			fallback:          f.dbInfo,
			timeout:           placementResolveTimeout,
			localRootResolver: res,
		}
	}
}

// NewRaftBackendFactory creates a factory that hosts dedicated per-database raft
// groups on the shared system Host. localReplicaID and localRaftAddr identify
// the local replica on the shared host (used as the single member of each MVP
// single-node shard). barrierReg supplies the per-tenant at-rest encryptor and
// sealed-state gate; dbInfo resolves the persisted shard ID and DataRoot path
// for each (tenant, db) database. All four dependencies must be non-nil for the
// factory to resolve any database.
func NewRaftBackendFactory(
	host *quicraft.Host,
	localReplicaID uint64,
	localRaftAddr string,
	barrierReg *seal.BarrierRegistry,
	dbInfo raftDatabaseInfoSource,
	logger *slog.Logger,
	opts ...RaftBackendOption,
) *RaftBackendFactory {
	if logger == nil {
		logger = slog.Default()
	}
	var adapter *raftbackend.HostAdapter
	if host != nil {
		adapter = raftbackend.New(host)
	}
	f := &RaftBackendFactory{
		host:           host,
		adapter:        adapter,
		localReplicaID: localReplicaID,
		localRaftAddr:  localRaftAddr,
		barrierReg:     barrierReg,
		dbInfo:         dbInfo,
		logger:         logger,
		shards:         make(map[raftShardKey]*raftShard),
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// NewRaftBackendFactoryFromManager is the daemon-facing constructor that derives
// the database-info source from a database.Manager.
func NewRaftBackendFactoryFromManager(
	host *quicraft.Host,
	localReplicaID uint64,
	localRaftAddr string,
	barrierReg *seal.BarrierRegistry,
	manager *database.Manager,
	systemTenant string,
	logger *slog.Logger,
	opts ...RaftBackendOption,
) *RaftBackendFactory {
	return NewRaftBackendFactory(host, localReplicaID, localRaftAddr, barrierReg,
		&managerRaftInfoSource{manager: manager, systemTenant: systemTenant}, logger, opts...)
}

// Services returns the data-plane service set backing the dedicated raft
// database (tenantID, db), starting and caching the shard on first access. The
// tenant's barrier must already be registered and unsealed; a sealed or missing
// barrier returns an error so the request fails closed.
//
// When the configured info source carries a placement entry for (tenantID, db)
// that assigns a replica to the local node, Services starts the shard as a
// placement-aware member: every assigned replica bootstraps with the full
// placement member set (join=false, replicaID=placement-assigned), the same
// membership model the system shard uses. This is correct for both initial
// creation and restart re-attach. If no placement is found the existing
// single-node path is used unchanged (Phase-4b compatibility).
func (f *RaftBackendFactory) Services(ctx context.Context, tenantID, db string) (*DatabaseServices, error) {
	if f == nil || f.host == nil || f.barrierReg == nil || f.dbInfo == nil {
		return nil, &RaftBackendError{TenantID: tenantID, Database: db, Op: "resolve", Err: ErrRaftBackendUnavailable}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	key := raftShardKey{tenantID: tenantID, database: db}
	if rs, ok := f.shards[key]; ok {
		return rs.services, nil
	}

	tb, err := f.barrierReg.GetTenant(tenantID)
	if err != nil {
		return nil, &RaftBackendError{TenantID: tenantID, Database: db, Op: "barrier-lookup", Err: err}
	}
	if tb.IsSealed() {
		return nil, &RaftBackendError{TenantID: tenantID, Database: db, Op: "resolve", Err: seal.ErrSealed}
	}

	// M2: single store.Get covers both the shard info and the placement entry.
	// When the info source is a placementRaftInfoSource, databaseAndPlacementInfo
	// is called once; for all other source types we use the standard two-step.
	var (
		shardID uint64
		dataDir string
		pInfo   *placement.Placement
	)
	if src, ok := f.dbInfo.(*placementRaftInfoSource); ok {
		var resolved bool
		shardID, dataDir, pInfo, resolved = src.databaseAndPlacementInfo(tenantID, db)
		if !resolved {
			// Placement table had no entry (or resolver not wired); fall back.
			shardID, dataDir, resolved = src.fallback.databaseInfo(tenantID, db)
			if !resolved {
				return nil, &RaftBackendError{TenantID: tenantID, Database: db, Op: "db-info", Err: ErrRaftDatabaseUnknown}
			}
		}
	} else {
		var resolved bool
		shardID, dataDir, resolved = f.dbInfo.databaseInfo(tenantID, db)
		if !resolved {
			return nil, &RaftBackendError{TenantID: tenantID, Database: db, Op: "db-info", Err: ErrRaftDatabaseUnknown}
		}
		pInfo, _ = f.dbInfo.placementInfo(tenantID, db)
	}

	// Phase 4c-1d: when the info source carries a placement entry that assigns a
	// replica to the local node, start the dedicated shard as a placement member.
	//
	// Every assigned replica bootstraps with the FULL placement member set and
	// join=false, mirroring the system shard's membership model. This is correct
	// for both initial creation (all replicas bootstrap the group together once
	// their barrier key arrives and quorum forms) and restart re-attach (the
	// shared host LogDB restores each replica's persisted log and committed
	// membership; re-applying the bootstrap members on top is safe — see
	// go-quicraft Host.StartShard, which reconciles membership from committed
	// config-change entries).
	//
	// A follower MUST NOT re-attach with join=true: go-quicraft treats a
	// join=true start as a brand-new joining replica (peer.MarkJoining) with no
	// initial membership, which defers serving its state machine until a fresh
	// snapshot arrives from the leader. An already-populated replica restarted
	// that way answers reads with 404 until then — silently stranding committed
	// data on whichever node happens to serve the request (the dedicated-group
	// no-data-loss restart bug).
	//
	// NOTE (Phase 4c-2 follow-on, not yet implemented): the blanket join=false
	// here is correct ONLY for founding members that bootstrap the group
	// together (initial create + restart re-attach). A replica added DYNAMICALLY
	// after group formation — e.g. via a 4c-2 rebalance / RequestAddNode — has no
	// committed log of its own and MUST start with join=true so go-quicraft
	// streams it a snapshot from the leader. When dynamic add is implemented,
	// distinguish founding members from late joiners (e.g. a placement-entry flag)
	// and pass join=true only for the latter; do not flip this blanket value.
	// 4d Option C: resolve whether this shard opted into a per-tenant-KEY WAL.
	// The placement entry is authoritative cluster-wide (so followers agree with
	// the leader); the meta-derived source covers Phase-4b single-node databases.
	dedicatedWAL := false
	if pInfo != nil {
		dedicatedWAL = pInfo.DedicatedWAL
	} else {
		dedicatedWAL = f.dbInfo.dedicatedWAL(tenantID, db)
	}

	if pInfo != nil {
		localReplica, rOK := pInfo.ReplicaForNode(f.localRaftAddr)
		if rOK {
			members := pInfo.Members()
			rs, merr := f.startShardAsMember(tenantID, db, shardID, dataDir, tb,
				localReplica.ReplicaID, false, members, dedicatedWAL)
			if merr != nil {
				return nil, merr
			}
			f.shards[key] = rs
			return rs.services, nil
		}
	}

	rs, err := f.startShard(tenantID, db, shardID, dataDir, tb, dedicatedWAL)
	if err != nil {
		return nil, err
	}
	f.shards[key] = rs
	return rs.services, nil
}

// Attach starts (and caches) a dedicated raft shard eagerly. It is used at boot
// to re-attach IsolationRaft databases before serving. It is idempotent: an
// already-attached database is a no-op.
func (f *RaftBackendFactory) Attach(ctx context.Context, tenantID, db string) error {
	_, err := f.Services(ctx, tenantID, db)
	return err
}

// StartedShard identifies a locally started dedicated raft shard.
type StartedShard struct {
	// TenantID is the tenant that owns the database.
	TenantID string
	// Database is the database name.
	Database string
}

// ListStarted returns a snapshot of every (TenantID, Database) pair whose
// dedicated raft shard is currently started on this node. It is used by the
// TenantShardReconciler to detect orphans (locally running shards with no
// matching placement entry).
func (f *RaftBackendFactory) ListStarted() []StartedShard {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]StartedShard, 0, len(f.shards))
	for k := range f.shards {
		out = append(out, StartedShard{TenantID: k.tenantID, Database: k.database})
	}
	return out
}

// startShard starts a single-node dedicated shard using the factory's local
// replica ID and address. It is the Phase-4b compatibility path: a single local
// replica bootstrapped with join=false. Caller holds f.mu.
func (f *RaftBackendFactory) startShard(tenantID, db string, shardID uint64, dataDir string, tb *seal.TenantBarrier, dedicatedWAL bool) (*raftShard, error) {
	members := map[uint64]string{f.localReplicaID: f.localRaftAddr}
	return f.startShardAsMember(tenantID, db, shardID, dataDir, tb, f.localReplicaID, false, members, dedicatedWAL)
}

// startShardAsMember builds the per-shard subsystems and starts the shard on
// the shared host with explicit membership control. replicaID is the
// placement-assigned replica ID for the local node. When join is false, this
// node bootstraps as leader with members containing all initial replicas; when
// join is true, this node joins an existing group (members is ignored and may
// be empty). The caller holds f.mu.
//
// Leader shards wait for leadership election before binding services. Joining
// followers do not wait for leadership — they are not the leader.
func (f *RaftBackendFactory) startShardAsMember(
	tenantID, db string,
	shardID uint64,
	dataDir string,
	tb *seal.TenantBarrier,
	replicaID uint64,
	join bool,
	members map[uint64]string,
	dedicatedWAL bool,
) (*raftShard, error) {
	// quicraftconfig.Config.DataDir must be an absolute path; the volume-pool
	// DataRoot is contained but normalize defensively so a relative pool path
	// does not fail shard start.
	absDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, &RaftBackendError{TenantID: tenantID, Database: db, Op: "data-dir", Err: err}
	}

	lessor := lease.NewLessor()
	hub := watch.NewHub()
	createFn := newDataPlaneCreateFunc(tb, lessor, hub)

	// 4d Option C: when the database opted into a per-tenant-KEY WAL and the
	// shared host carries a multiplex LogDB, open a dedicated single-barrier WAL
	// under the tenant's DataRoot encrypted by the tenant barrier and register it
	// for this shard BEFORE starting the shard, so the shard's recovery reads and
	// first appends route to it. Registration also makes the host's snapshot
	// encryptor (via WithShardBarriers) seal this shard's snapshots under the same
	// tenant barrier. If the opt-in cannot be honored (no multiplex, sealed/absent
	// barrier, or the WAL fails to open) the shard falls back to the shared
	// system-barrier WAL (Route A) so it still starts; the fallback is logged.
	registeredWAL := f.openAndRegisterDedicatedWAL(tenantID, db, shardID, absDir, tb, dedicatedWAL)

	shardCfg := quicraftconfig.Config{
		ShardID:   shardID,
		ReplicaID: replicaID,
		DataDir:   absDir,
	}
	if err := f.host.StartShard(members, join, createFn, shardCfg); err != nil {
		if registeredWAL != nil {
			if detached, ok := f.walMux.Unregister(shardID); ok && detached != nil {
				_ = detached.Close()
			}
		}
		return nil, &RaftBackendError{TenantID: tenantID, Database: db, Op: "start-shard", Err: err}
	}

	// Wait for leadership only when this node is the bootstrap leader (join=false).
	// A joining follower is never the initial leader; waiting on it would block
	// indefinitely until the leader is elected on another node.
	if !join {
		if err := waitForShardLeader(f.host, shardID); err != nil {
			_ = f.host.StopShard(shardID)
			if registeredWAL != nil {
				if detached, ok := f.walMux.Unregister(shardID); ok && detached != nil {
					_ = detached.Close()
				}
			}
			return nil, &RaftBackendError{TenantID: tenantID, Database: db, Op: "await-leader", Err: err}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	rs := &raftShard{
		tenantID:     tenantID,
		database:     db,
		shardID:      shardID,
		hub:          hub,
		lessor:       lessor,
		services:     buildDataPlaneServices(f.adapter, shardID, hub),
		cancel:       cancel,
		dedicatedWAL: registeredWAL,
	}
	rs.services.Name = db

	// Leader-side lease-expiry sweeper. The gate re-checks each tick so it is
	// correct across elections: only the current leader proposes revocations.
	// replicaID (the placement-assigned ID for this node) is used rather than
	// f.localReplicaID so multi-replica shards correctly gate on the right ID.
	if rs.services.Lease != nil {
		localReplicaID := replicaID
		go rs.lessor.RunExpiry(ctx, lease.DefaultExpiryInterval,
			func() bool {
				id, ok, lerr := f.host.GetLeaderID(shardID)
				return lerr == nil && ok && id == localReplicaID
			},
			rs.services.Lease.Revoke,
			f.logger,
		)
	}

	f.logger.Info("dedicated raft database shard started",
		slog.String("tenant", tenantID),
		slog.String("database", db),
		slog.Uint64("shard_id", shardID),
		slog.Uint64("replica_id", replicaID),
		slog.Bool("join", join),
		slog.Bool("dedicated_wal", registeredWAL != nil),
		slog.String("data_dir", absDir))

	return rs, nil
}

// dedicatedWALSubdir is the subdirectory under a dedicated-raft database's
// DataRoot that holds its per-tenant-KEY WAL store (4d Option C). It is a
// sibling of the state-machine data so both live under the tenant's encrypted
// DataRoot subtree.
const dedicatedWALSubdir = "wal"

// openAndRegisterDedicatedWAL opens a dedicated single-barrier WAL under the
// shard's DataRoot encrypted by the tenant barrier and registers it in the
// shared host's multiplex for shardID. It returns the registered LogDB on
// success, or nil when the opt-in cannot be honored (no multiplex wired, the
// barrier is unavailable, or the WAL fails to open) — in which case the shard
// transparently uses the shared system-barrier WAL. The caller holds f.mu.
func (f *RaftBackendFactory) openAndRegisterDedicatedWAL(
	tenantID, db string,
	shardID uint64,
	absDir string,
	tb *seal.TenantBarrier,
	dedicatedWAL bool,
) logdb.LogDB {
	if !dedicatedWAL {
		return nil
	}
	if f.walMux == nil {
		f.logger.Warn("dedicated WAL requested but no multiplex LogDB is wired; falling back to shared WAL",
			slog.String("tenant", tenantID), slog.String("database", db), slog.Uint64("shard_id", shardID))
		return nil
	}
	if tb == nil || tb.IsSealed() {
		f.logger.Warn("dedicated WAL requested but tenant barrier is unavailable; falling back to shared WAL",
			slog.String("tenant", tenantID), slog.String("database", db), slog.Uint64("shard_id", shardID))
		return nil
	}
	barrier := tb.Barrier().EpochBarrier()
	walDir := filepath.Join(absDir, dedicatedWALSubdir)
	dedDB, err := waldb.Open(walDir, waldb.WithBarrier(barrier))
	if err != nil {
		f.logger.Error("failed to open dedicated tenant WAL; falling back to shared WAL",
			slog.String("tenant", tenantID), slog.String("database", db),
			slog.Uint64("shard_id", shardID), slog.String("wal_dir", walDir), slog.Any("error", err))
		return nil
	}
	if err := f.walMux.Register(shardID, dedDB, barrier); err != nil {
		f.logger.Error("failed to register dedicated tenant WAL; falling back to shared WAL",
			slog.String("tenant", tenantID), slog.String("database", db),
			slog.Uint64("shard_id", shardID), slog.Any("error", err))
		_ = dedDB.Close()
		return nil
	}
	f.logger.Info("dedicated tenant WAL opened (per-tenant-KEY WAL, 4d Option C)",
		slog.String("tenant", tenantID), slog.String("database", db),
		slog.Uint64("shard_id", shardID), slog.String("wal_dir", walDir))
	return dedDB
}

// Close stops the named tenant database's dedicated shard (releasing the Pebble
// lock on its DataRoot subtree) and evicts it from the cache. It does NOT close
// the shared host. It is a no-op for an unknown (tenant, db) pair.
func (f *RaftBackendFactory) Close(tenantID, db string) error {
	f.mu.Lock()
	key := raftShardKey{tenantID: tenantID, database: db}
	rs, ok := f.shards[key]
	if ok {
		delete(f.shards, key)
	}
	f.mu.Unlock()
	if !ok {
		return nil
	}
	return f.stopShard(rs)
}

// CloseAll stops every live dedicated shard. Errors are joined so a single
// failing shard does not prevent the rest from stopping. The shared host is not
// closed.
func (f *RaftBackendFactory) CloseAll() error {
	f.mu.Lock()
	shards := f.shards
	f.shards = make(map[raftShardKey]*raftShard)
	f.mu.Unlock()

	var errs []error
	for _, rs := range shards {
		if err := f.stopShard(rs); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// stopShard cancels the shard's lease sweeper, stops it on the shared host, and
// — for a per-tenant-KEY WAL shard (4d Option C) — unregisters and closes the
// dedicated WAL. Closing releases the WAL's file handles; the actual crypto-shred
// of the tenant DEKs is performed by the tenant barrier purge on tenant delete,
// and the on-disk ciphertext is removed when the tenant DataRoot subtree is
// deleted on database drop.
func (f *RaftBackendFactory) stopShard(rs *raftShard) error {
	if rs.cancel != nil {
		rs.cancel()
	}
	if err := f.host.StopShard(rs.shardID); err != nil {
		return &RaftBackendError{TenantID: rs.tenantID, Database: rs.database, Op: "stop-shard", Err: err}
	}
	if rs.dedicatedWAL != nil && f.walMux != nil {
		if detached, ok := f.walMux.Unregister(rs.shardID); ok && detached != nil {
			if err := detached.Close(); err != nil {
				f.logger.Warn("failed to close dedicated tenant WAL on shard stop",
					slog.String("tenant", rs.tenantID), slog.String("database", rs.database),
					slog.Uint64("shard_id", rs.shardID), slog.Any("error", err))
			}
		}
	}
	f.logger.Info("dedicated raft database shard stopped",
		slog.String("tenant", rs.tenantID),
		slog.String("database", rs.database),
		slog.Uint64("shard_id", rs.shardID),
		slog.Bool("dedicated_wal", rs.dedicatedWAL != nil))
	return nil
}

// managerRaftInfoSource adapts a database.Manager to the raftDatabaseInfoSource
// interface, resolving the persisted shard ID and DataDir for a (tenant, db)
// pair and enforcing per-tenant ownership.
type managerRaftInfoSource struct {
	manager      *database.Manager
	systemTenant string
}

// databaseInfo resolves the shard ID and DataDir for a dedicated-raft database
// owned by the tenant. It returns ok=false when the database does not exist, is
// not raft-backed, or is owned by a different tenant.
func (s *managerRaftInfoSource) databaseInfo(tenantID, db string) (uint64, string, bool) {
	if s.manager == nil {
		return 0, "", false
	}
	dbase, err := s.manager.Get(db)
	if err != nil {
		return 0, "", false
	}
	info := dbase.Info()
	if info.Backend != database.BackendRaft {
		return 0, "", false
	}
	owner := info.TenantID
	if tenantID == s.systemTenant {
		if owner != "" && owner != s.systemTenant {
			return 0, "", false
		}
	} else if owner != tenantID {
		return 0, "", false
	}
	if info.ShardID == 0 || info.DataDir == "" {
		return 0, "", false
	}
	return info.ShardID, info.DataDir, true
}

// placementInfo always returns false for the meta-derived source: it does not
// carry a replicated placement table.
func (s *managerRaftInfoSource) placementInfo(_ string, _ string) (*placement.Placement, bool) {
	return nil, false
}

// dedicatedWAL reports the persisted per-tenant-KEY WAL opt-in for the database.
func (s *managerRaftInfoSource) dedicatedWAL(_ string, db string) bool {
	if s.manager == nil {
		return false
	}
	dbase, err := s.manager.Get(db)
	if err != nil {
		return false
	}
	return dbase.Info().DedicatedWAL
}

// compile-time check that managerRaftInfoSource satisfies the interface.
var _ raftDatabaseInfoSource = (*managerRaftInfoSource)(nil)

// placementRaftInfoSource resolves a dedicated-raft database's shard from the
// replicated placement table, falling back to a meta.json-derived source when
// no placement entry exists. It makes the placement table the source of truth
// for (tenant, db) -> (shardID, dataRoot) while preserving Phase-4b single-node
// behavior: a single-replica placement whose only replica is the local node
// resolves to exactly the same shard ID and DataRoot the manager would return.
type placementRaftInfoSource struct {
	store             *placement.Store
	localNodeID       string
	fallback          raftDatabaseInfoSource
	timeout           time.Duration
	localRootResolver LocalRootResolver
}

// databaseInfo returns the local replica's shard ID and DataRoot for the
// (tenant, db) database. When a placement entry exists and assigns a replica to
// the local node, that entry wins; otherwise it falls back to the meta-derived
// source. A placement entry that does NOT assign a replica to the local node
// still defers to the fallback so this stage never regresses (multi-node
// placement materialization is Phase 4c-1d).
//
// BUG 1 fix: leaders record "" in Replica.DataRoot for non-local replicas so
// each follower resolves its own local pool path. When DataRoot is empty and
// localRootResolver is configured, databaseInfo calls the resolver to compute
// {pool_root}/{tenantID} and returns {pool_root}/{tenantID}/databases/{db}.
func (s *placementRaftInfoSource) databaseInfo(tenantID, db string) (uint64, string, bool) {
	shardID, dataDir, p, ok := s.databaseAndPlacementInfo(tenantID, db)
	if ok {
		return shardID, dataDir, true
	}
	_ = p
	return s.fallback.databaseInfo(tenantID, db)
}

// databaseAndPlacementInfo performs a single placement-table lookup and returns
// the shard ID, resolved DataDir, the full Placement, and ok=true when the
// local node hosts a replica for (tenantID, db). This avoids two separate
// store.Get calls in Services (M2 optimization).
func (s *placementRaftInfoSource) databaseAndPlacementInfo(tenantID, db string) (shardID uint64, dataDir string, p *placement.Placement, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	p, err := s.store.Get(ctx, tenantID, db)
	if err != nil || p == nil || p.ShardID == 0 {
		return 0, "", nil, false
	}
	r, assigned := p.ReplicaForNode(s.localNodeID)
	if !assigned {
		return 0, "", nil, false
	}
	if r.DataRoot != "" {
		return p.ShardID, r.DataRoot, p, true
	}
	// DataRoot is empty — this node is a follower replica recorded with "" by
	// the leader's persistPlacement. Resolve the local pool path if a resolver
	// is wired in.
	if s.localRootResolver == nil {
		return 0, "", nil, false
	}
	resolveCtx, resolveCancel := context.WithTimeout(context.Background(), s.timeout)
	defer resolveCancel()
	tenantRoot, rerr := s.localRootResolver(resolveCtx, tenantID)
	if rerr != nil {
		return 0, "", nil, false
	}
	return p.ShardID, filepath.Join(tenantRoot, "databases", db), p, true
}

// placementInfo returns the full Placement for (tenantID, db) when an entry
// exists and assigns a replica to the local node. It delegates to
// databaseAndPlacementInfo so callers do not pay an additional store.Get.
func (s *placementRaftInfoSource) placementInfo(tenantID, db string) (*placement.Placement, bool) {
	_, _, p, ok := s.databaseAndPlacementInfo(tenantID, db)
	return p, ok
}

// dedicatedWAL reads the per-tenant-KEY WAL opt-in from the replicated placement
// entry (authoritative cluster-wide) and falls back to the meta-derived source
// for databases without a placement entry.
func (s *placementRaftInfoSource) dedicatedWAL(tenantID, db string) bool {
	if _, _, p, ok := s.databaseAndPlacementInfo(tenantID, db); ok && p != nil {
		return p.DedicatedWAL
	}
	return s.fallback.dedicatedWAL(tenantID, db)
}

// compile-time check that placementRaftInfoSource satisfies the interface.
var _ raftDatabaseInfoSource = (*placementRaftInfoSource)(nil)
