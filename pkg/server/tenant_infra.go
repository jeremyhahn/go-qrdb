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

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// listTenantsWhenReady lists tenants at boot, waiting out the ReadIndex
// warm-up window on a freshly elected leader. A linearizable read issued
// before the leader commits its first entry of the current term fails with
// quicraft.ErrReadIndexNotReady; that state resolves as soon as the no-op
// commit lands, so the read is retried on that specific transient. Any
// other error — and the transient persisting past the deadline — is
// returned to abort boot, preserving fail-closed behavior.
func listTenantsWhenReady(ctx context.Context, tenantSvc tenant.TenantServicer) ([]tenant.TenantInfo, error) {
	return whenReadIndexReady(ctx, func() ([]tenant.TenantInfo, error) {
		return tenantSvc.ListTenants(ctx)
	})
}

// TenantInfra is the assembled per-tenant isolation stack for one node. All
// three fields are optional — a nil field means the corresponding subsystem
// was not configured (e.g. no VolumePool or no shared Host).
type TenantInfra struct {
	// Hosts is the IsolationVolume registry; non-nil when BarrierReg,
	// TenantSvc, and VolumePool are all present in TenantInfraConfig.
	Hosts *TenantHostRegistry

	// Raft is the IsolationRaft factory; non-nil when Hosts is non-nil AND
	// Host is present in TenantInfraConfig.
	Raft *RaftBackendFactory

	// Reconciler drives ongoing placement-table convergence for IsolationRaft
	// shards. Non-nil when Raft is non-nil AND PlacementStore is present.
	// The caller is responsible for starting it with its own context:
	//
	//   if ti.Reconciler != nil { go ti.Reconciler.Run(ctx) }
	Reconciler *TenantShardReconciler
}

// TenantInfraConfig carries the dependencies required by BuildTenantInfra.
// All pointer fields are optional — nil values disable the corresponding
// subsystem and are propagated unchanged to the TenantInfra output.
type TenantInfraConfig struct {
	// Ctx is passed to RestoreVolumeTenants and the eager reattach passes.
	Ctx context.Context

	// BarrierReg is the per-tenant barrier registry. Its presence (non-nil)
	// AND VolumePool's presence gate the entire IsolationVolume build.
	BarrierReg *seal.BarrierRegistry

	// TenantSvc provides the cluster-replicated tenant metadata (DataRoot,
	// isolation type, auto-unseal flag). Its presence AND BarrierReg AND
	// VolumePool gate the IsolationVolume host registry build.
	TenantSvc tenant.TenantServicer

	// TenantLookup is the tenant-metadata lookup the LONG-LIVED registries
	// consult at use time. TenantSvc is read only by the one-shot boot passes
	// inside BuildTenantInfra; the TenantHostRegistry built here holds its
	// lookup for the life of the node, which in a composition that rebuilds
	// its service graph in place (sdk/go/node's system-scope encryption
	// migration) is longer than any one tenant service lives. Such a
	// composition passes a lookup that resolves the CURRENT service on every
	// call; one whose tenant service is never swapped (the daemon) passes the
	// service itself. Required whenever the IsolationVolume registry is built;
	// omitting it returns TenantInfraConfigError rather than silently capturing
	// TenantSvc (go-qrdb#383).
	TenantLookup TenantLookup

	// VolumePool is the per-tenant storage subsystem. Its presence (non-nil)
	// gates the entire IsolationVolume + IsolationRaft build.
	VolumePool *pool.VolumePoolSubsystem

	// Logger is the structured logger. Defaults to slog.Default() when nil.
	Logger *slog.Logger

	// Host is the shared quicraft.Host that carries the IsolationRaft shards'
	// Raft log. Its presence gates the IsolationRaft factory build.
	Host *quicraft.Host

	// Manager is the database manager that records persisted shard metadata.
	// Required when Host is set: IsolationRaft needs a database.Manager to
	// track per-tenant shard IDs and to walk existing shards on reattach.
	// Passing a non-nil Host with a nil Manager returns TenantInfraConfigError.
	Manager *database.Manager

	// ReplicaID is this node's Raft replica ID.
	ReplicaID uint64

	// RaftAddr is this node's Raft address string (used as placement NodeID).
	RaftAddr string

	// SystemTenant is the tenant ID reserved for the system shard.
	SystemTenant string

	// PlacementStore, when non-nil, makes the replicated placement table the
	// canonical source for IsolationRaft shard resolution, and causes a
	// TenantShardReconciler to be built.
	PlacementStore *placement.Store

	// MultiplexLogDB, when non-nil, routes opted-in IsolationRaft shards to
	// per-tenant-KEY WALs inside the shared host's multiplex LogDB.
	MultiplexLogDB *database.MultiplexLogDB
}

// BuildTenantInfra assembles the per-tenant isolation stack from cfg and
// returns a TenantInfra whose fields are non-nil only for the subsystems
// that could be built given the available configuration. On error, any
// partially built registries are closed before returning.
//
// Configuration contract: Manager is required when Host is set. Violating
// this contract returns TenantInfraConfigError; IsolationRaft cannot operate
// without a database.Manager to track per-tenant shard metadata.
//
// The returned Reconciler (when non-nil) is NOT started; the caller starts
// it with its own long-lived context so lifecycle ownership stays with the
// caller:
//
//	ti, err := BuildTenantInfra(cfg)
//	if err != nil { ... }
//	if ti.Reconciler != nil { go ti.Reconciler.Run(bootCtx) }
func BuildTenantInfra(cfg TenantInfraConfig) (*TenantInfra, error) {
	ti := &TenantInfra{}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Validate the configuration contract before any side effect: the barrier
	// restore below is not undone by an error return.
	if cfg.BarrierReg != nil && cfg.TenantSvc != nil && cfg.VolumePool != nil && cfg.TenantLookup == nil {
		return nil, &TenantInfraConfigError{
			Field:   "TenantLookup",
			Message: "required when the IsolationVolume registry is built; the registry outlives a single tenant service",
		}
	}

	// Wire the tenant-barrier crash recoverer BEFORE any barrier is restored:
	// RegisterVolumeTenant runs it over the tenant's freshly-opened backend so
	// a migration interrupted in the swap→adopt commit window is rolled
	// forward (or an uncommitted one rolled back) from its journal — with the
	// store-directory set and staged snapshots read from the journal itself —
	// before the barrier and then the tenant's data stores come up. This is
	// the tenant-scope analogue of the system barrier's boot-time
	// RecoverInterruptedMigration call, shared by the daemon and the embedded
	// node through this builder.
	if cfg.BarrierReg != nil {
		cfg.BarrierReg.Attach(seal.WithMigrationRecoverer(
			func(ctx context.Context, backend seal.StorageBackend, rootKeyPath string) error {
				return database.RecoverInterruptedMigration(ctx, backend, rootKeyPath, nil)
			}))
	}

	// Restore every per-tenant barrier (IsolationVolume + IsolationRaft) from
	// THIS node's own persisted tenant-root records before re-attaching either
	// tenant type. RestoreVolumeTenants re-registers and (for auto-unseal
	// tenants) unseals each barrier using the node-local DataRoot recorded by
	// RegisterVolumeTenant on a prior run. Failures are logged and deferred to
	// the reconciler rather than aborting.
	//
	// Retired tenants are reclaimed FIRST: RestoreVolumeTenants restores a
	// barrier from any tenant-root record it finds, and a node that missed a
	// delete still holds the retired tenant's record and directories. The
	// reconcile releases the barrier, removes the directories and then drops
	// the record -- judged by the tombstone alone, never a missing
	// placement or tenant record, and never touching a directory a live
	// tenant claims. Nothing is running yet, so there is no runtime to stop.
	if cfg.BarrierReg != nil && cfg.TenantSvc != nil {
		reconcileDeletedTenantsAtBoot(cfg.Ctx, cfg.TenantSvc, logger)
	}
	if cfg.BarrierReg != nil && cfg.VolumePool != nil {
		if err := cfg.BarrierReg.RestoreVolumeTenants(cfg.Ctx); err != nil {
			logger.Warn("start: RestoreVolumeTenants failed; some tenant barriers may be deferred",
				slog.Any("error", err))
		}
	}

	// IsolationVolume host registry. Each volume tenant runs a dedicated
	// single-node Raft Host on its own DataRoot, encrypted under the tenant
	// DEK. Boot re-attach eagerly rebuilds the Host of each volume tenant
	// whose barrier is locally available so persisted data is reachable after
	// a restart; tenants with no local barrier are deferred to lazy attach.
	if cfg.BarrierReg != nil && cfg.TenantSvc != nil && cfg.VolumePool != nil {
		ti.Hosts = NewTenantHostRegistryFromLookup(cfg.BarrierReg, cfg.TenantLookup, logger)
		if err := reattachVolumeTenants(cfg.Ctx, cfg.TenantSvc, cfg.BarrierReg, ti.Hosts, logger); err != nil {
			_ = ti.Hosts.CloseAll()
			return nil, err
		}
	}

	// IsolationRaft factory. Each IsolationRaft tenant runs one Raft shard
	// per database on the SHARED system Host. Boot re-attach re-registers and
	// (for system-managed tenants) unseals each raft tenant's barrier and
	// re-starts its database shards before serving.
	//
	// Validate the Host+Manager contract before entering the block: Manager is
	// required when Host is set (reattachRaftTenants calls manager.List() and
	// NewRaftBackendFactoryFromManager dereferences the manager unconditionally).
	// Return an explicit typed error rather than panicking on nil dereference.
	if cfg.Host != nil && cfg.Manager == nil {
		if ti.Hosts != nil {
			_ = ti.Hosts.CloseAll()
		}
		return nil, &TenantInfraConfigError{
			Field:   "Manager",
			Message: "required when Host is set; IsolationRaft needs a database manager to track shard metadata",
		}
	}
	if cfg.BarrierReg != nil && cfg.TenantSvc != nil && cfg.VolumePool != nil && cfg.Host != nil {
		var raftOpts []RaftBackendOption
		if cfg.PlacementStore != nil {
			raftOpts = append(raftOpts,
				WithPlacementInfoSource(cfg.PlacementStore, cfg.RaftAddr, cfg.BarrierReg.LocalTenantDataRoot))
		}
		if cfg.MultiplexLogDB != nil {
			raftOpts = append(raftOpts, WithMultiplexLogDB(cfg.MultiplexLogDB))
		}
		ti.Raft = NewRaftBackendFactoryFromManager(
			cfg.Host, cfg.ReplicaID, cfg.RaftAddr, cfg.BarrierReg, cfg.Manager, cfg.SystemTenant, logger, raftOpts...)

		if err := reattachRaftTenants(cfg.Ctx, cfg.TenantSvc, cfg.BarrierReg, ti.Raft, cfg.Manager, logger); err != nil {
			_ = ti.Raft.CloseAll()
			if ti.Hosts != nil {
				_ = ti.Hosts.CloseAll()
			}
			return nil, err
		}

		// TenantShardReconciler drives ongoing convergence between the
		// replicated placement table and the set of locally started dedicated
		// raft shards. Built but not started here; the caller starts it with
		// its long-lived context.
		if cfg.PlacementStore != nil {
			ti.Reconciler = NewTenantShardReconciler(
				cfg.PlacementStore,
				ti.Raft,
				cfg.RaftAddr,
				logger,
				WithBarrierReadiness(cfg.BarrierReg),
				WithJoinObserver(ti.Raft),
				WithPromoter(newRaftPromoter(cfg.Host)),
			)
		}
	}

	// The on-demand reconcile must stop a retired tenant's runtimes before it
	// removes their directories; the runtimes exist from here on.
	if cfg.TenantSvc != nil && (ti.Hosts != nil || ti.Raft != nil) {
		WireTenantRuntimeCloser(cfg.TenantSvc, ti.Hosts, ti.Raft)
	}

	return ti, nil
}

// ServerOptions returns the server.ServerOption slice that wires this
// TenantInfra into a Server, matching the WithTenantHostRegistry /
// WithRaftBackendFactory calls in the daemon's server-options block.
func (ti *TenantInfra) ServerOptions() []ServerOption {
	var opts []ServerOption
	if ti.Hosts != nil {
		opts = append(opts, WithTenantHostRegistry(ti.Hosts))
	}
	if ti.Raft != nil {
		opts = append(opts, WithRaftBackendFactory(ti.Raft))
	}
	return opts
}

// reattachVolumeTenants eagerly rebuilds the per-tenant Raft Host of every
// IsolationVolume tenant whose per-tenant barrier is already available on
// THIS node after a restart. The barrier itself is restored beforehand by
// BarrierRegistry.RestoreVolumeTenants, which re-registers and (for
// auto-unseal tenants) unseals each barrier from the node-local persisted
// tenant-root record.
//
// TenantInfo is replicated cluster metadata, so this node sees every volume
// tenant — including those it holds no local key material for. The DataRoot
// in TenantInfo is the CREATING node's path and is NOT valid elsewhere, so
// it is deliberately never used here to (re-)register a barrier. A tenant
// whose barrier is absent from the local registry is skipped; its Host
// attaches lazily on first access. A self-managed tenant whose barrier is
// registered-but-sealed is left sealed for the tenant admin to unseal. For
// an auto-unseal tenant whose barrier is ready, the per-tenant Host is
// eagerly re-attached; a re-attach failure aborts boot.
func reattachVolumeTenants(
	ctx context.Context,
	tenantSvc tenant.TenantServicer,
	barrierReg *seal.BarrierRegistry,
	hostReg *TenantHostRegistry,
	logger *slog.Logger,
) error {
	tenants, err := listTenantsWhenReady(ctx, tenantSvc)
	if err != nil {
		return err
	}
	for i := range tenants {
		t := tenants[i]
		if t.EffectiveIsolationType() != tenant.IsolationVolume {
			continue
		}
		// The barrier is restored from this node's own tenant-root record by
		// RestoreVolumeTenants. When it is absent, this node holds no key
		// material for the tenant; defer to lazy attach rather than registering
		// with the (foreign) replicated DataRoot.
		tb, gerr := barrierReg.GetTenant(t.ID)
		if gerr != nil {
			logger.Info("volume tenant not provisioned on this node; deferring Host attach",
				slog.String("tenant", t.ID))
			continue
		}
		// A self-managed tenant comes up SEALED: the tenant admin must re-unseal
		// before the data plane is reachable, and the per-tenant Host builds
		// lazily on first access after unseal. Do NOT unseal or build its Host
		// here. Only a system-managed (auto-unseal) tenant is unsealed
		// credential-free (the system barrier is the unseal authority) and its
		// Host eagerly re-attached.
		if !t.BarrierAutoUnseal {
			logger.Info("volume tenant left sealed for tenant-managed unseal",
				slog.String("tenant", t.ID))
			continue
		}
		// Auto-unseal tenants are unsealed by RestoreVolumeTenants. Tolerate a
		// still-sealed barrier here as a best-effort fallback before attaching.
		if tb.IsSealed() {
			if uerr := barrierReg.UnsealTenant(ctx, t.ID, seal.Credentials{}); uerr != nil && !errors.Is(uerr, seal.ErrAlreadyUnsealed) {
				return &VolumeReattachError{TenantID: t.ID, Err: uerr}
			}
		}
		if aerr := hostReg.Attach(ctx, t.ID); aerr != nil {
			return &VolumeReattachError{TenantID: t.ID, Err: aerr}
		}
		logger.Info("re-attached volume tenant",
			slog.String("tenant", t.ID),
			slog.String("data_root", t.DataRoot))
	}
	return nil
}

// reattachRaftTenants eagerly re-starts the dedicated raft database shards of
// every IsolationRaft tenant whose per-tenant barrier is already available on
// THIS node after a restart. The barrier itself is restored beforehand by
// BarrierRegistry.RestoreVolumeTenants, which re-registers and (for
// auto-unseal tenants) unseals each barrier from the node-local persisted
// tenant-root record.
//
// TenantInfo is replicated cluster metadata, so this node sees every raft
// tenant — including those it holds no local key material for (e.g. it never
// received key delivery). The DataRoot carried in TenantInfo is the CREATING
// node's path and is NOT valid on other nodes, so it is deliberately never
// used here to (re-)register a barrier. Instead, a tenant whose barrier is
// absent from the local registry is skipped and deferred to the
// TenantShardReconciler, which attaches it once key delivery completes.
//
// A self-managed tenant whose barrier is registered-but-sealed is left sealed
// — the tenant admin re-unseals and the shards attach lazily on first access.
// For an auto-unseal tenant whose barrier is ready, every dedicated database
// shard known to the local manager is eagerly re-attached on the shared host
// before serving so its data is reachable immediately. A shard whose
// re-attach fails aborts boot.
func reattachRaftTenants(
	ctx context.Context,
	tenantSvc tenant.TenantServicer,
	barrierReg *seal.BarrierRegistry,
	raftReg *RaftBackendFactory,
	manager *database.Manager,
	logger *slog.Logger,
) error {
	tenants, err := listTenantsWhenReady(ctx, tenantSvc)
	if err != nil {
		return err
	}
	// Snapshot the manager's database list once: it is invariant across the
	// loop, so re-listing per tenant would be O(tenants x databases). A nil
	// manager only reaches here in tests that exercise the skip-everything
	// path; production always supplies one (BuildTenantInfra enforces it).
	var allDatabases []database.DatabaseInfo
	if manager != nil {
		allDatabases = manager.List()
	}
	for i := range tenants {
		t := tenants[i]
		if t.EffectiveIsolationType() != tenant.IsolationRaft {
			continue
		}
		// The barrier is restored from this node's own tenant-root record by
		// RestoreVolumeTenants. When it is absent, this node holds no key
		// material for the tenant yet; defer to the reconciler rather than
		// registering with the (foreign) replicated DataRoot.
		tb, gerr := barrierReg.GetTenant(t.ID)
		if gerr != nil {
			logger.Info("raft tenant not yet provisioned on this node; deferring shard attach to reconciler",
				slog.String("tenant", t.ID))
			continue
		}
		// Self-managed tenants stay sealed: the admin re-unseals and the shards
		// attach lazily on first access. Do not unseal or start shards here.
		if !t.BarrierAutoUnseal {
			logger.Info("raft tenant left sealed for tenant-managed unseal",
				slog.String("tenant", t.ID))
			continue
		}
		// Auto-unseal tenants are unsealed by RestoreVolumeTenants. Tolerate a
		// still-sealed barrier here as a best-effort fallback before attaching.
		if tb.IsSealed() {
			if uerr := barrierReg.UnsealTenant(ctx, t.ID, seal.Credentials{}); uerr != nil && !errors.Is(uerr, seal.ErrAlreadyUnsealed) {
				return &RaftReattachError{TenantID: t.ID, Err: uerr}
			}
		}
		// Eagerly re-start every dedicated raft database shard owned by this
		// tenant that the local manager knows about. The databases were
		// re-opened by dbManager.OpenAll (placeholder backend + meta.json
		// carrying the persisted shard ID); here we re-bind the live shard on
		// the shared host. Databases not present locally are attached by the
		// reconciler instead.
		for _, info := range allDatabases {
			if info.TenantID != t.ID || info.Backend != database.BackendRaft {
				continue
			}
			if aerr := raftReg.Attach(ctx, t.ID, info.Name); aerr != nil {
				return &RaftReattachError{TenantID: t.ID, Database: info.Name, Err: aerr}
			}
			logger.Info("re-attached raft database shard",
				slog.String("tenant", t.ID),
				slog.String("database", info.Name),
				slog.Uint64("shard_id", info.ShardID))
		}
	}
	return nil
}
