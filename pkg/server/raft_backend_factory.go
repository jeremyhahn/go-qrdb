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
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb/waldb"

	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// Compile-time interface assertion: RaftBackendFactory satisfies
// service.ShardRestarter, enabling ONLINE backup restore (stop -> import ->
// restart) for the dedicated per-database Raft shards it manages.
var _ service.ShardRestarter = (*RaftBackendFactory)(nil)

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

// ErrShardMigrationInProgress is returned by Services while a tenant's dedicated
// raft shards are being recycled by an encryption migration. It is retryable:
// once the keyed barrier is installed and the shards re-attach, the resolve
// succeeds.
var ErrShardMigrationInProgress = errors.New("server: tenant raft shard encryption migration in progress")

// refusalFromPlacement returns *database.DroppingError when p's replicated
// drop fence (placement.Placement.Dropping) is set, so Services refuses a
// dedicated-raft database's shard -- before starting one for a database
// already being torn down, and before returning an already-cached bundle
// for a drop that started after it was cached (go-qrdb#482).
//
// Unlike database.Manager's own local fence, this one is checked
// identically on every node: the placement table is replicated, so every
// replica answers the same way regardless of which node issued the Drop or
// which one currently leads the shard.
//
// p is nil when the configured info source carries no placement entry at
// all -- a Phase-4b single-node database created before placement existed,
// or a fallback-only info source with no replicated table behind it -- and
// that is not a refusal: there is no replicated fence to check, and the
// database's own local database.Manager fence (see Manager.Get) already
// covers it.
func refusalFromPlacement(db string, p *placement.Placement) error {
	if p != nil && p.Dropping {
		return &database.DroppingError{Name: db}
	}
	return nil
}

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

// ShardNotManagedError is returned by RaftBackendFactory's
// service.ShardRestarter methods (StopForRestore, RestartAfterRestore) when
// shardID is not a dedicated per-database Raft shard this factory manages —
// e.g. the system shard, a shard hosted on a different node, or (for
// RestartAfterRestore) a shard with no pending StopForRestore call.
type ShardNotManagedError struct {
	ShardID uint64
}

// Error implements the error interface.
func (e *ShardNotManagedError) Error() string {
	return "server: shard " + strconv.FormatUint(e.ShardID, 10) + " is not a dedicated raft shard managed by this node"
}

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
	// replicaID is this node's replica ID in the shard -- the
	// placement-assigned one, not necessarily the factory's localReplicaID.
	replicaID uint64
	dataDir   string // absolute shard DataDir (absDir); SM Pebble at {dataDir}/shard-{shardID}
	hub       *watch.Hub
	lessor    *lease.Lessor
	services  *DatabaseServices
	cancel    context.CancelFunc

	// dedicatedWAL is the per-tenant-KEY WAL store registered in the shared
	// host's multiplex for this shard (4d Option C), or nil when the shard uses
	// the shared system-barrier WAL. On teardown it is unregistered and closed.
	dedicatedWAL logdb.LogDB

	// startedObserver records that the local replica was started with
	// quicraftconfig.Config.IsObserver set — i.e. the placement listed it as
	// a (not-yet-promoted) observer at start time. go-quicraft latches this
	// LOCAL role for the life of the in-process replica and cross-checks it
	// against the replicated membership on every config change
	// (checkSelfRole), so once the shard's leader promotes this replica to a
	// voting member the running replica must be restarted with the flag
	// clear or it refuses to campaign or serve leader reads forever. The
	// TenantShardReconciler reads this through StartedAsObserver to drive
	// exactly that restart.
	startedObserver bool

	// hadPlacement records whether THIS shard was resolved through a real
	// placement entry (pInfo != nil) at the time it was started, as opposed
	// to the Phase-4b fallback path (no placement store wired in at all, or
	// no entry for this database). Services' cache-hit branch uses it to
	// tell two identically-shaped "placementInfo returned ok=false" results
	// apart: a fallback shard that never had placement to check in the
	// first place (ok=false forever, by design) versus a placement-resolved
	// shard whose entry has since been deleted entirely -- which only
	// happens at the very end of a completed drop
	// (finishRaftPlacementTeardown) -- and must be refused even though there
	// is no longer a Dropping flag anywhere to read (go-qrdb#482 round 3,
	// finding 6). The reconciler evicts a dropped database's cache entry
	// asynchronously (on its next tick), so without this check a request
	// landing in the window between the placement delete and that eviction
	// would succeed against a database gone everywhere else in the cluster.
	hadPlacement bool
}

// raftDatabaseInfoSource resolves the dedicated shard ID and on-disk DataDir for
// a (tenant, db) pair from the persisted database metadata. It is satisfied by
// a database.Manager via managerRaftInfoSource and by the replicated placement
// table via placementRaftInfoSource.
type raftDatabaseInfoSource interface {
	// databaseInfo returns the allocated shard ID and on-disk DataDir for the
	// (tenant, db) database, or ok=false when no such database exists or it is
	// not owned by the tenant. ignoreFence resolves a database whose local
	// drop fence is set: the drop itself (FenceServices) is the one caller
	// that must reach a database its own fence refuses.
	databaseInfo(tenantID, db string, ignoreFence bool) (shardID uint64, dataDir string, ok bool)

	// placementInfo returns the full Placement for (tenantID, db) when
	// placement-based routing is configured and the local node hosts a replica
	// of that database. It reports false when no placement entry exists, when
	// the local node has no assigned replica, or when placement routing is not
	// configured (meta-derived sources always return false). A placement
	// table that cannot be READ is an error, never "no entry": answering a
	// read failure with "unknown" is what a drop reads as "no store to
	// clear".
	placementInfo(tenantID, db string) (*placement.Placement, bool, error)

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
	host *quicraft.Host
	// leaderOf reads a shard's current leader replica. It is the host's
	// GetLeaderID; LeadsShard reads leadership through it alone.
	leaderOf       func(shardID uint64) (replicaID uint64, known bool, err error)
	adapter        *raftbackend.HostAdapter
	featureGate    *featuregate.Gate
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

	// pendingRestore tracks shards evicted from shards by StopForRestore that
	// have not yet been restarted by RestartAfterRestore, keyed by shard ID
	// so RestartAfterRestore (which is given only the shard ID by
	// service.RestoreShardOnline) can recover which (tenant, database) to
	// re-Attach. An entry lives here only for the brief window between the
	// two calls during an online backup restore.
	pendingRestore map[uint64]raftShardKey

	// migrating marks tenant IDs whose barrier is mid-encryption-recycle. While
	// set, Services returns ErrShardMigrationInProgress for ANY of that tenant's
	// databases instead of lazily rebuilding a shard against the stale
	// (pre-migration) tenant barrier — the IsolationRaft analogue of
	// TenantHostRegistry.migrating. Keyed by tenantID (string).
	migrating sync.Map

	// recodeOverride, when non-nil, replaces the per-store re-encryption step in
	// MigrateTenantEncryption. It is a TEST-ONLY seam used to inject a recode
	// failure and exercise the crash-safe rollback path (staging removal, journal
	// failure, barrier-untouched, shard re-attach). Production leaves it nil.
	recodeOverride recodeFunc
}

// RaftBackendOption customizes a RaftBackendFactory.
type RaftBackendOption func(*RaftBackendFactory)

// WithFeatureGate makes the factory's shards propose guarded writes only once
// gate enables them for the shard's group. It is required whenever the shared
// Host has a transport: without it such a Host's shards are never sent a
// command an older replica could not apply (see raftbackend.HostAdapter.
// FeatureEnabled).
func WithFeatureGate(gate *featuregate.Gate) RaftBackendOption {
	return func(f *RaftBackendFactory) { f.featureGate = gate }
}

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
	var (
		adapter  *raftbackend.HostAdapter
		leaderOf func(uint64) (uint64, bool, error)
	)
	if host != nil {
		adapter = raftbackend.New(host)
		leaderOf = host.GetLeaderID
	}
	f := &RaftBackendFactory{
		host:           host,
		leaderOf:       leaderOf,
		adapter:        adapter,
		localReplicaID: localReplicaID,
		localRaftAddr:  localRaftAddr,
		barrierReg:     barrierReg,
		dbInfo:         dbInfo,
		logger:         logger,
		shards:         make(map[raftShardKey]*raftShard),
		pendingRestore: make(map[uint64]raftShardKey),
	}
	for _, opt := range opts {
		opt(f)
	}
	if host != nil {
		f.adapter = raftbackend.New(host, raftbackend.WithFeatureGate(f.featureGate))
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

// refuseIfDroppingLocked re-checks the replicated placement fence for
// (tenantID, db) against a shard already known to be cached (rs), and is
// what Services' cache-hit branch and RefuseIfDropping both delegate to.
// The caller MUST already hold f.mu.
//
// pInfo, infoOK are re-read from the info source on every call rather than
// cached on rs, because the whole point is to observe a fence set (or an
// entry deleted) AFTER rs was populated (go-qrdb#482).
func (f *RaftBackendFactory) refuseIfDroppingLocked(tenantID, db string, rs *raftShard) error {
	pInfo, infoOK, err := f.dbInfo.placementInfo(tenantID, db)
	if err != nil {
		return &RaftBackendError{TenantID: tenantID, Database: db, Op: "placement-lookup", Err: err}
	}
	if infoOK {
		return refusalFromPlacement(db, pInfo)
	}
	if rs.hadPlacement {
		// This shard was originally resolved through a real placement
		// entry, and that entry is now gone entirely -- not merely marked
		// dropping, but deleted, which only happens at the very end of a
		// completed drop (finishRaftPlacementTeardown). There is no
		// Dropping flag left anywhere to read, and the reconciler has not
		// evicted this cache entry yet (its teardown is asynchronous), so
		// without this check a request landing in this window would
		// succeed against a database that no longer exists anywhere else
		// in the cluster (go-qrdb#482 round 3, finding 6). A shard that
		// never had placement to begin with (rs.hadPlacement false -- the
		// Phase-4b fallback path) reports infoOK=false permanently and
		// must not be refused on that basis alone.
		return &database.DroppingError{Name: db}
	}
	return nil
}

// cachedServicesLocked decides what a cached shard may serve. The caller
// MUST hold f.mu.
//
// A shard is cached by (tenant, database) NAME, but a database dropped and
// created again under that name is a different database on a different
// shard -- shard IDs are never issued twice (database.Manager), so the shard
// ID identifies the incarnation. The cached shard is therefore checked
// against the current placement on every call:
//
//   - placement names a DIFFERENT shard: the cached one belongs to a dropped
//     incarnation. It is removed from the cache and reported stale, and the
//     caller retires it (stop, purge its state-machine directory) and
//     resolves the current shard instead. Serving it would route the new
//     database's writes into the old one; fencing through it would unfence
//     a shard being torn down.
//   - placement is GONE for a shard resolved through placement: its database
//     was dropped. Services refuses with DroppingError; FenceServices
//     reports the database unknown -- it has no current store to mark, and
//     the doomed one must not be.
//   - placement cannot be READ: an error, on either path. It is never
//     treated as "gone".
//   - otherwise the cached services are served, after Services re-consults
//     the replicated drop marker (a drop that started after the shard was
//     cached).
func (f *RaftBackendFactory) cachedServicesLocked(tenantID, db string, rs *raftShard, honorFence bool) (svcs *DatabaseServices, stale bool, err error) {
	pInfo, infoOK, err := f.dbInfo.placementInfo(tenantID, db)
	if err != nil {
		return nil, false, &RaftBackendError{TenantID: tenantID, Database: db, Op: "placement-lookup", Err: err}
	}
	switch {
	case infoOK && pInfo.ShardID != rs.shardID:
		delete(f.shards, raftShardKey{tenantID: tenantID, database: db})
		f.logger.Info("evicting a dropped incarnation's shard; the database was created again on a new shard",
			slog.String("tenant", tenantID), slog.String("database", db),
			slog.Uint64("old_shard_id", rs.shardID), slog.Uint64("shard_id", pInfo.ShardID))
		return nil, true, nil
	case !infoOK && rs.hadPlacement && !honorFence:
		return nil, false, &RaftBackendError{TenantID: tenantID, Database: db, Op: "resolve", Err: ErrRaftDatabaseUnknown}
	}
	if honorFence {
		if err := f.refuseIfDroppingLocked(tenantID, db, rs); err != nil {
			return nil, false, err
		}
	}
	return rs.services, false, nil
}

// retireShard stops a shard evicted from the cache and removes its own
// state-machine directory. It MUST be called without f.mu: stopping waits on
// the Raft host. The database's data directory is shared by every
// incarnation of the name, so only the retired shard's shard-<id>
// subdirectory is removed and the current shard's survives.
func (f *RaftBackendFactory) retireShard(rs *raftShard) {
	if err := f.stopShard(rs); err != nil {
		f.logger.Warn("failed to stop a dropped incarnation's shard",
			slog.String("tenant", rs.tenantID), slog.String("database", rs.database),
			slog.Uint64("shard_id", rs.shardID), slog.Any("error", err))
		return
	}
	smDir := filepath.Join(rs.dataDir, "shard-"+strconv.FormatUint(rs.shardID, 10))
	if err := os.RemoveAll(smDir); err != nil {
		f.logger.Warn("failed to remove a dropped incarnation's shard data",
			slog.String("tenant", rs.tenantID), slog.String("database", rs.database),
			slog.Uint64("shard_id", rs.shardID), slog.Any("error", err))
	}
}

// RefuseIfDropping re-checks the replicated placement fence for (tenantID,
// db) against whatever shard bundle is currently cached for it, WITHOUT
// resolving or caching a new one.
//
// Writes to a dedicated-raft database do not need it: they carry the drop
// fence guard into the database's own shard, which refuses them at apply
// time. Its one caller is the vector view of such a database, whose writes
// are node-local rather than Raft proposals and so can only be checked
// before they are made (see databaseFence).
//
// A database this factory has never resolved at all reports no refusal:
// there is nothing cached to check staleness against, and the NEXT
// Services call for it performs the identical cold-path check on its own.
func (f *RaftBackendFactory) RefuseIfDropping(tenantID, db string) error {
	if f == nil || f.dbInfo == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rs, ok := f.shards[raftShardKey{tenantID: tenantID, database: db}]
	if !ok {
		return nil
	}
	return f.refuseIfDroppingLocked(tenantID, db, rs)
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
	return f.services(ctx, tenantID, db, true)
}

// FenceServices returns the same service set Services does, WITHOUT
// refusing a database whose replicated placement entry is marked dropping.
//
// It exists for exactly one caller: DROP DATABASE writing -- or, on a failed
// drop, removing -- the drop fence inside the database's own shard
// (service.StoreResolver.FenceTarget). The replicated placement marker is
// set before that write, so Services would refuse the very drop that set
// it. Nothing else may use it: every data-plane resolution goes through
// Services, which is what keeps a database being dropped unresolvable.
func (f *RaftBackendFactory) FenceServices(ctx context.Context, tenantID, db string) (*DatabaseServices, error) {
	return f.services(ctx, tenantID, db, false)
}

// services is Services and FenceServices. honorFence selects whether the
// replicated placement drop marker refuses the resolution.
func (f *RaftBackendFactory) services(ctx context.Context, tenantID, db string, honorFence bool) (*DatabaseServices, error) {
	if f == nil || f.host == nil || f.barrierReg == nil || f.dbInfo == nil {
		return nil, &RaftBackendError{TenantID: tenantID, Database: db, Op: "resolve", Err: ErrRaftBackendUnavailable}
	}

	key := raftShardKey{tenantID: tenantID, database: db}
	f.mu.Lock()
	for {
		rs, ok := f.shards[key]
		if !ok {
			break
		}
		services, stale, err := f.cachedServicesLocked(tenantID, db, rs, honorFence)
		if !stale {
			f.mu.Unlock()
			return services, err
		}
		// The cached shard belonged to a dropped incarnation of the name
		// and is already out of the cache. It is stopped and purged with
		// f.mu RELEASED -- stopping a shard waits on the Raft host -- and
		// the cache is consulted again, in case another caller started
		// the current incarnation's shard meanwhile.
		f.mu.Unlock()
		f.retireShard(rs)
		f.mu.Lock()
	}
	defer f.mu.Unlock()
	// Do not lazily rebuild any shard whose TENANT barrier is mid-recycle: it would
	// open the stale (pre-migration) barrier against data being re-encrypted and
	// cache the broken shard. Guard is tenant-granular (keyed by tenantID) so a db
	// that was not yet started when the recycle began is also blocked. The caller
	// retries once the migration completes.
	if _, migrating := f.migrating.Load(tenantID); migrating {
		return nil, &RaftBackendError{TenantID: tenantID, Database: db, Op: "resolve", Err: ErrShardMigrationInProgress}
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
		var (
			resolved bool
			perr     error
		)
		shardID, dataDir, pInfo, resolved, perr = src.databaseAndPlacementInfo(tenantID, db)
		if perr != nil {
			return nil, &RaftBackendError{TenantID: tenantID, Database: db, Op: "placement-lookup", Err: perr}
		}
		if !resolved {
			// Placement table had no entry (or resolver not wired); fall back.
			shardID, dataDir, resolved = src.fallback.databaseInfo(tenantID, db, !honorFence)
			if !resolved {
				return nil, &RaftBackendError{TenantID: tenantID, Database: db, Op: "db-info", Err: ErrRaftDatabaseUnknown}
			}
		}
	} else {
		var resolved bool
		shardID, dataDir, resolved = f.dbInfo.databaseInfo(tenantID, db, !honorFence)
		if !resolved {
			return nil, &RaftBackendError{TenantID: tenantID, Database: db, Op: "db-info", Err: ErrRaftDatabaseUnknown}
		}
		var perr error
		pInfo, _, perr = f.dbInfo.placementInfo(tenantID, db)
		if perr != nil {
			return nil, &RaftBackendError{TenantID: tenantID, Database: db, Op: "placement-lookup", Err: perr}
		}
	}

	// Cold-path check, mirroring the cache-hit one above: refuse before
	// ever starting or attaching a shard for a database whose replicated
	// placement entry is already marked dropping (go-qrdb#482).
	if honorFence {
		if err := refusalFromPlacement(db, pInfo); err != nil {
			return nil, err
		}
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
	// Phase 4c-2 (observer-first, see the elastic-scaling design doc
	// §B.2/§D.4): a replica added DYNAMICALLY after group formation (via
	// PlaceAdditional / DatabaseService.ScaleForTenant) has no committed log
	// of its own and MUST start with join=true so go-quicraft streams it a
	// snapshot from the leader — the leader admits it as a NON-VOTING
	// observer (RaftHost.AddObserver) BEFORE this attach, so the join
	// bootstrap window never changes the group's existing voting quorum.
	// Founding members, and any replica that has already caught up from a
	// prior join=true bootstrap (whether or not it has since been promoted
	// to a full voter), always use join=false — this is the no-data-loss
	// restart invariant, and it is orthogonal to voter/observer status: that
	// status lives entirely in the committed Raft config, restored from
	// LogDB on any join=false restart regardless of this flag. The
	// placement-entry's Replica.State distinguishes the states: it is
	// ReplicaStateObserver exactly once, for a replica's first attach after
	// being placed by PlaceAdditional and admitted as an observer, and is
	// promoted by TenantShardReconciler to ReplicaStateObserverReady once
	// go-quicraft reports the join bootstrap window closed (see
	// JoinObserver), then to ReplicaStateActive once the shard's current
	// leader promotes the observer to a full voting member (see Promoter).
	// ReplicaStateActive is the zero value, so every placement entry
	// persisted before this field existed (no "state" key) and every
	// Scheduler.Place-computed founding placement decodes to join=false
	// unchanged.
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
			// Every replica the placement names, kept apart from members
			// (which the join path clears): it is who the founding
			// identities cover and who a joiner accepts its bootstrap from.
			peers := pInfo.Members()
			join := localReplica.State == placement.ReplicaStateObserver
			// The LOCAL role must agree with the replicated membership for
			// the whole observer window (go-quicraft checkSelfRole latches a
			// disagreement and refuses campaigning and leader reads): the
			// leader admits the replica via AddObserver before this attach,
			// and it stays an observer in committed membership through
			// ObserverReady until the leader's promotion — so IsObserver
			// covers BOTH states, while join marks only the very first
			// attach (the snapshot-bootstrap window).
			isObserver := localReplica.State == placement.ReplicaStateObserver ||
				localReplica.State == placement.ReplicaStateObserverReady
			// A joining observer bootstraps from the leader's snapshot; the
			// members map seeds a fresh voting membership and must not name
			// a non-voting replica (go-quicraft NonVotingRoleInMembersError),
			// so the join path passes none at all.
			if join {
				members = nil
			}
			rs, merr := f.startShardAsMember(ctx, tenantID, db, shardID, dataDir, tb,
				localReplica.ReplicaID, join, isObserver, members, peers, dedicatedWAL, true)
			if merr != nil && isObserver && !join && errors.Is(merr, quicraft.ErrNonVotingRoleInMembers) {
				// An ObserverReady replica with NO persisted shard state: its
				// prior join bootstrap was wiped (demotion purge racing a
				// re-add, an operator clearing the volume) while the placement
				// still records the post-bootstrap state. Starting it
				// join=false would seed a fresh voting membership naming an
				// observer, which go-quicraft rejects on first start. Re-enter
				// the join bootstrap window instead — the replica has no data,
				// so streaming the leader's snapshot again is exactly the
				// recovery it needs.
				f.logger.Warn("observer replica has no persisted state; re-entering join bootstrap window",
					slog.String("tenant", tenantID),
					slog.String("database", db),
					slog.Uint64("shard_id", shardID),
					slog.Uint64("replica_id", localReplica.ReplicaID))
				rs, merr = f.startShardAsMember(ctx, tenantID, db, shardID, dataDir, tb,
					localReplica.ReplicaID, true, true, nil, peers, dedicatedWAL, true)
			}
			if merr != nil {
				return nil, merr
			}
			f.shards[key] = rs
			return rs.services, nil
		}
	}

	rs, err := f.startShard(ctx, tenantID, db, shardID, dataDir, tb, dedicatedWAL)
	if err != nil {
		return nil, err
	}
	f.shards[key] = rs
	return rs.services, nil
}

// LeadsDatabase reports whether this node's replica currently leads the
// dedicated Raft group of (tenantID, db). It consults only shards this node
// has already started -- it never attaches one -- so asking about a database
// this node is not a member of is free of side effects and answers false.
// The reclaim sweeper uses it to decide which node sweeps a dedicated-group
// database's reclaim jobs (go-qrdb#547).
func (f *RaftBackendFactory) LeadsDatabase(tenantID, db string) bool {
	if f == nil || f.host == nil {
		return false
	}
	f.mu.Lock()
	rs, ok := f.shards[raftShardKey{tenantID: tenantID, database: db}]
	f.mu.Unlock()
	if !ok {
		return false
	}
	info, err := f.host.GetShardInfo(rs.shardID)
	return err == nil && info != nil && info.IsLeader
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

// LeadsShard reports whether this node's replica currently leads the started
// dedicated shard for (tenantID, db). False for a shard that is not started
// here, and whenever leadership cannot be read. A background task that must
// run once per shard rather than once per replica -- the collection sweeper --
// asks this first, so its proposals come from the replica that would serve
// them anyway.
func (f *RaftBackendFactory) LeadsShard(tenantID, db string) bool {
	f.mu.Lock()
	rs, ok := f.shards[raftShardKey{tenantID: tenantID, database: db}]
	f.mu.Unlock()
	if !ok || f.leaderOf == nil {
		return false
	}
	id, known, err := f.leaderOf(rs.shardID)
	return err == nil && known && id == rs.replicaID
}

// StartedAsObserver reports whether the locally running shard for
// (tenantID, db) was started with the observer role latched into its local
// go-quicraft configuration. False for a shard that is not started. The
// TenantShardReconciler uses this to detect a replica the shard's leader
// has since promoted to a voting member: the local role cannot change
// in-process, so the reconciler restarts the shard with the flag clear (see
// raftShard.startedObserver).
func (f *RaftBackendFactory) StartedAsObserver(tenantID, db string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	rs, ok := f.shards[raftShardKey{tenantID: tenantID, database: db}]
	return ok && rs.startedObserver
}

// startShard starts a single-node dedicated shard using the factory's local
// replica ID and address. It is the Phase-4b compatibility path: a single local
// replica bootstrapped with join=false. Caller holds f.mu.
func (f *RaftBackendFactory) startShard(ctx context.Context, tenantID, db string, shardID uint64, dataDir string, tb *seal.TenantBarrier, dedicatedWAL bool) (*raftShard, error) {
	members := map[uint64]string{f.localReplicaID: f.localRaftAddr}
	return f.startShardAsMember(ctx, tenantID, db, shardID, dataDir, tb, f.localReplicaID, false, false, members, members, dedicatedWAL, false)
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
	ctx context.Context,
	tenantID, db string,
	shardID uint64,
	dataDir string,
	tb *seal.TenantBarrier,
	replicaID uint64,
	join bool,
	isObserver bool,
	members map[uint64]string,
	peers map[uint64]string,
	dedicatedWAL bool,
	hadPlacement bool,
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
		ShardID:    shardID,
		ReplicaID:  replicaID,
		DataDir:    absDir,
		IsObserver: isObserver,
	}
	// Every peer is placed on an existing cluster node, whose identity the
	// system shard has already committed.
	identities, err := service.MemberIdentities(ctx, f.host, shardID, replicaID, join, peers)
	if err != nil {
		if registeredWAL != nil {
			if detached, ok := f.walMux.Unregister(shardID); ok && detached != nil {
				_ = detached.Close()
			}
		}
		return nil, &RaftBackendError{TenantID: tenantID, Database: db, Op: "member-identities", Err: err}
	}
	shardCfg.MemberIdentities = identities
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
		replicaID:    replicaID,
		dataDir:      absDir,
		hub:          hub,
		lessor:       lessor,
		services:     buildDataPlaneServices(f.adapter, shardID, hub),
		cancel:       cancel,
		dedicatedWAL: registeredWAL,

		startedObserver: isObserver,
		hadPlacement:    hadPlacement,
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
		slog.Bool("observer", isObserver),
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

// CloseAndPurge stops the named tenant database's dedicated shard (as Close
// does) and then removes its on-disk dataDir subtree. It is used by
// TenantShardReconciler ONLY for demotion (placement says this node is no
// longer a replica) and full-database-delete teardown — NEVER for graceful
// process shutdown (CloseAll), which must leave data on disk for the next
// restart's re-attach. Purging on demotion closes a pre-existing
// encrypted-at-rest retention gap: a demoted replica's ciphertext previously
// remained on disk indefinitely after RebalanceService MOVE, scale-down, or
// DropForTenant. It is a no-op for an unknown (tenant, db) pair.
func (f *RaftBackendFactory) CloseAndPurge(tenantID, db string) error {
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
	if err := f.stopShard(rs); err != nil {
		return err
	}
	if err := os.RemoveAll(rs.dataDir); err != nil {
		return &RaftBackendError{TenantID: tenantID, Database: db, Op: "purge-data-dir", Err: err}
	}
	f.logger.Info("dedicated raft database shard data purged",
		slog.String("tenant", tenantID),
		slog.String("database", db),
		slog.String("data_dir", rs.dataDir))
	return nil
}

// IsShardJoining reports whether shardID is still in its join bootstrap
// window on this host's shared quicraft.Host: started locally with
// join=true and not yet given its first snapshot. Satisfies
// server.JoinObserver so TenantShardReconciler can promote a locally hosted
// ReplicaStateObserver replica to ReplicaStateObserverReady once it has
// caught up. Returns false when no adapter is wired (e.g. a test double
// factory).
func (f *RaftBackendFactory) IsShardJoining(shardID uint64) bool {
	if f.adapter == nil {
		return false
	}
	return f.adapter.IsShardJoining(shardID)
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

// shardOwner returns the (tenantID, database) pair owning the locally started
// dedicated shard identified by shardID, or ok=false if this factory does not
// manage it. Caller must hold f.mu.
func (f *RaftBackendFactory) shardOwner(shardID uint64) (raftShardKey, bool) {
	for k, rs := range f.shards {
		if rs.shardID == shardID {
			return k, true
		}
	}
	return raftShardKey{}, false
}

// StopForRestore stops the dedicated shard identified by shardID, releasing
// its lease-sweeper goroutine and (for a 4d Option C shard) unregistering and
// closing its dedicated per-tenant-KEY WAL — exactly what a graceful Close
// does — then evicts it from the factory's cache so a subsequent Attach/
// Services call rebuilds it fresh rather than returning the now-stopped
// cached service set. The (tenantID, database) owning shardID is stashed so
// RestartAfterRestore can restart it with the same identity.
//
// It satisfies service.ShardRestarter for server.RaftBackendFactory's
// dedicated per-database Raft shards. Returns ShardNotManagedError when
// shardID is not one this factory manages (e.g. the system shard, or a shard
// on a different node).
func (f *RaftBackendFactory) StopForRestore(shardID uint64) error {
	f.mu.Lock()
	key, ok := f.shardOwner(shardID)
	if !ok {
		f.mu.Unlock()
		return &ShardNotManagedError{ShardID: shardID}
	}
	rs := f.shards[key]
	delete(f.shards, key)
	f.mu.Unlock()

	if err := f.stopShard(rs); err != nil {
		// Restore the cache entry: the shard was never actually stopped, so
		// the factory's view of "what is running" must not diverge from
		// reality.
		f.mu.Lock()
		f.shards[key] = rs
		f.mu.Unlock()
		return err
	}

	f.mu.Lock()
	f.pendingRestore[shardID] = key
	f.mu.Unlock()
	return nil
}

// RestartAfterRestore restarts the shard identified by shardID that a prior
// StopForRestore call stopped, using its original (tenantID, database)
// identity — Attach re-derives the exact start configuration (voting
// membership, state-machine factory, on-disk paths) the same way a boot-time
// re-attach does. It is also the rollback path for a failed snapshot import:
// the caller invokes it whether or not the import between StopForRestore and
// this call succeeded, so a failed online restore never leaves the shard
// down.
//
// It satisfies service.ShardRestarter. Returns ShardNotManagedError when
// shardID has no pending StopForRestore call (e.g. RestartAfterRestore
// called twice, or called for a shard this factory never stopped).
func (f *RaftBackendFactory) RestartAfterRestore(ctx context.Context, shardID uint64) error {
	f.mu.Lock()
	key, ok := f.pendingRestore[shardID]
	if !ok {
		f.mu.Unlock()
		return &ShardNotManagedError{ShardID: shardID}
	}
	delete(f.pendingRestore, shardID)
	f.mu.Unlock()

	return f.Attach(ctx, key.tenantID, key.database)
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
func (s *managerRaftInfoSource) databaseInfo(tenantID, db string, ignoreFence bool) (uint64, string, bool) {
	if s.manager == nil {
		return 0, "", false
	}
	get := s.manager.Get
	if ignoreFence {
		get = s.manager.GetIgnoringFence
	}
	dbase, err := get(db)
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
func (s *managerRaftInfoSource) placementInfo(_ string, _ string) (*placement.Placement, bool, error) {
	return nil, false, nil
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
func (s *placementRaftInfoSource) databaseInfo(tenantID, db string, ignoreFence bool) (uint64, string, bool) {
	shardID, dataDir, _, ok, err := s.databaseAndPlacementInfo(tenantID, db)
	if err != nil {
		return 0, "", false
	}
	if ok {
		return shardID, dataDir, true
	}
	return s.fallback.databaseInfo(tenantID, db, ignoreFence)
}

// databaseAndPlacementInfo performs a single placement-table lookup and returns
// the shard ID, resolved DataDir, the full Placement, and ok=true when the
// local node hosts a replica for (tenantID, db). This avoids two separate
// store.Get calls in Services (M2 optimization).
func (s *placementRaftInfoSource) databaseAndPlacementInfo(tenantID, db string) (shardID uint64, dataDir string, p *placement.Placement, ok bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	p, err = s.store.Get(ctx, tenantID, db)
	if err != nil {
		if errors.Is(err, placement.ErrPlacementNotFound) {
			return 0, "", nil, false, nil
		}
		return 0, "", nil, false, err
	}
	if p == nil || p.ShardID == 0 {
		return 0, "", nil, false, nil
	}
	r, assigned := p.ReplicaForNode(s.localNodeID)
	if !assigned {
		return 0, "", nil, false, nil
	}
	if r.DataRoot != "" {
		return p.ShardID, r.DataRoot, p, true, nil
	}
	// DataRoot is empty — this node is a follower replica recorded with "" by
	// the leader's persistPlacement. Resolve the local pool path if a resolver
	// is wired in.
	if s.localRootResolver == nil {
		return 0, "", nil, false, nil
	}
	resolveCtx, resolveCancel := context.WithTimeout(context.Background(), s.timeout)
	defer resolveCancel()
	tenantRoot, rerr := s.localRootResolver(resolveCtx, tenantID)
	if rerr != nil {
		return 0, "", nil, false, rerr
	}
	return p.ShardID, filepath.Join(tenantRoot, "databases", db), p, true, nil
}

// placementInfo returns the full Placement for (tenantID, db) when an entry
// exists and assigns a replica to the local node. It delegates to
// databaseAndPlacementInfo so callers do not pay an additional store.Get.
func (s *placementRaftInfoSource) placementInfo(tenantID, db string) (*placement.Placement, bool, error) {
	_, _, p, ok, err := s.databaseAndPlacementInfo(tenantID, db)
	return p, ok, err
}

// dedicatedWAL reads the per-tenant-KEY WAL opt-in from the replicated placement
// entry (authoritative cluster-wide) and falls back to the meta-derived source
// for databases without a placement entry.
func (s *placementRaftInfoSource) dedicatedWAL(tenantID, db string) bool {
	if _, _, p, ok, err := s.databaseAndPlacementInfo(tenantID, db); err == nil && ok && p != nil {
		return p.DedicatedWAL
	}
	return s.fallback.dedicatedWAL(tenantID, db)
}

// compile-time check that placementRaftInfoSource satisfies the interface.
var _ raftDatabaseInfoSource = (*placementRaftInfoSource)(nil)
