// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// BarrierRegistry manages the system barrier and per-tenant barriers.
// It provides a centralized registry for creating, initializing, and
// looking up tenant-specific barriers.
//
// When configured with WithVolumePool and WithPerTenantRegistry, the
// registry can satisfy RegisterTenant calls that pass a nil backend
// by allocating a path from the configured volume pool, opening a
// per-tenant Pebble at that path, and wrapping it as the per-tenant
// StorageBackend. This is the integration point that gives barrier-mode
// tenants their own independent Pebble instances.
type BarrierRegistry struct {
	logger  *slog.Logger
	system  *StorageBarrier
	tenants sync.Map // map[string]*TenantBarrier

	// tenantDataRoots records the on-disk DataRoot assigned to each tenant
	// registered via RegisterVolumeTenant. It is consulted by
	// LocalTenantDataRoot so follower replicas can recover the DataRoot of an
	// already-registered tenant without re-running the round-robin pool.Pick.
	// Key: tenantID (string) → value: dataRoot (string).
	tenantDataRoots sync.Map

	// poolMu guards pool/perTenantReg/defaultPoolName/slotStore which may be
	// attached either at construction (Options) or post-hoc via Attach.
	poolMu          sync.RWMutex
	pool            objpool.Manager
	poolStateStore  objpool.StateStore // retained for diagnostics; also pins lifetime
	defaultPoolName string
	perTenantReg    pebblestore.PerTenantRegistry

	// slotStore records the cluster-wide tenant→relative-slot assignment in
	// the replicated system shard (Phase 2). When non-nil (clustered mode),
	// the pooled-allocation path resolves a tenant's relative slot from this
	// store instead of running a node-local pool.Pick — so every replica
	// realizes the SAME relative slot (at its own local pool base) for a
	// tenant. When nil (standalone), each node Picks node-locally, preserving
	// the single-node behavior. The recorded assignment is the single
	// replicated decision; the round-robin cursor that chooses the slot for a
	// new tenant is likewise replicated (see pool.ReplicatedStateStore).
	slotStore StorageBackend

	// migrationRecoverer, when non-nil, reconciles an interrupted encryption
	// migration recorded in a tenant's journal BEFORE the tenant barrier is
	// built (see MigrationRecoverer). Guarded by poolMu like the other
	// boot-wired composition fields.
	migrationRecoverer MigrationRecoverer
}

// MigrationRecoverer reconciles an interrupted encryption migration whose
// journal is persisted at rootKeyPath+".migration" in backend, BEFORE the
// barrier over that root key is constructed. It must be idempotent and
// credential-free: it brings the on-disk state (store directories, staged
// snapshot files, and the sealed root-key blob) to the journal's committed
// state and removes the journal, so the normal barrier build then proceeds on
// reconciled state. A nil journal (no interrupted migration) must be a no-op.
//
// The concrete implementation lives at the data-plane layer (pkg/database
// owns the directory-swap and snapshot-staging mechanics) and is injected
// here via WithMigrationRecoverer because pkg/seal cannot import pkg/database.
type MigrationRecoverer func(ctx context.Context, backend StorageBackend, rootKeyPath string) error

// Option configures a BarrierRegistry at construction time.
//
// Options are applied in order; later options override earlier ones
// for fields they touch. Adding new options is non-breaking.
type Option func(*BarrierRegistry)

// WithVolumePool attaches a volume pool manager and its state store
// to the registry. After this option is applied, RegisterTenant calls
// that pass a nil backend will pull a per-tenant path from the named
// pool. The state store is retained as a lifetime pin and for
// diagnostics — strategies that need to read/write cursors do so via
// the manager.
//
// Passing nil for any argument is a no-op for that argument; both
// must be set before RegisterTenant can satisfy a nil-backend call.
func WithVolumePool(mgr objpool.Manager, state objpool.StateStore, defaultPoolName string) Option {
	return func(r *BarrierRegistry) {
		r.poolMu.Lock()
		defer r.poolMu.Unlock()
		if mgr != nil {
			r.pool = mgr
		}
		if state != nil {
			r.poolStateStore = state
		}
		if defaultPoolName != "" {
			r.defaultPoolName = defaultPoolName
		}
	}
}

// WithPerTenantRegistry attaches the per-tenant Pebble registry. It
// is the second half of the pool-driven RegisterTenant flow: once a
// path is picked from the volume pool, the registry opens (or returns
// an existing) Pebble at that path and the result is wrapped as the
// per-tenant StorageBackend.
func WithPerTenantRegistry(reg pebblestore.PerTenantRegistry) Option {
	return func(r *BarrierRegistry) {
		r.poolMu.Lock()
		defer r.poolMu.Unlock()
		if reg != nil {
			r.perTenantReg = reg
		}
	}
}

// WithReplicatedSlotStore attaches the replicated system-shard store used to
// record and resolve the cluster-wide tenant→relative-slot assignment. After
// this option is applied (clustered mode), pooled tenant provisioning resolves
// a tenant's slot from this store rather than running a node-local pool.Pick,
// so every replica realizes the same relative slot. The store must provide
// cross-node-atomic PutIfAbsent (a Raft-backed KV) so the first writer wins
// when two nodes race to assign the same tenant.
//
// Passing nil is a no-op. When never configured, the registry falls back to
// node-local Pick (single-node / standalone behavior).
func WithReplicatedSlotStore(store StorageBackend) Option {
	return func(r *BarrierRegistry) {
		r.poolMu.Lock()
		defer r.poolMu.Unlock()
		if store != nil {
			r.slotStore = store
		}
	}
}

// WithMigrationRecoverer attaches the crash-recovery hook RegisterVolumeTenant
// runs over a tenant's freshly-opened barrier backend before building the
// barrier. It closes the tenant-scope enable-encryption crash window: a node
// that died between the store-directory swaps and the root-key adoption is
// rolled forward (or an uncommitted migration rolled back) from the journal
// before the barrier — and then the tenant's data stores — come up.
//
// Passing nil is a no-op. When never configured, tenant registration proceeds
// without a recovery pass (the pre-recovery behavior).
func WithMigrationRecoverer(rec MigrationRecoverer) Option {
	return func(r *BarrierRegistry) {
		r.poolMu.Lock()
		defer r.poolMu.Unlock()
		if rec != nil {
			r.migrationRecoverer = rec
		}
	}
}

// NewBarrierRegistry creates a new BarrierRegistry with the given
// system barrier and applies any supplied options. The system barrier
// must not be nil. Options are applied after the registry is
// constructed so they can mutate registry-private state.
func NewBarrierRegistry(logger *slog.Logger, system *StorageBarrier, opts ...Option) (*BarrierRegistry, error) {
	if system == nil {
		return nil, ErrNilSystemBarrier
	}
	r := &BarrierRegistry{
		logger: logger,
		system: system,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r, nil
}

// Attach applies one or more Options post-construction. It is the
// idiomatic seam for daemon startup wiring where the volume pool and
// per-tenant Pebble registry are not built until after the seal
// subsystem (which constructs the BarrierRegistry) has come up.
//
// Attach is safe to call concurrently with reads but is intended to
// run during boot, not under steady-state load.
func (r *BarrierRegistry) Attach(opts ...Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
}

// SystemBarrier returns the system-level barrier.
func (r *BarrierRegistry) SystemBarrier() *StorageBarrier { return r.system }

// SetSystemBarrier replaces the system-level barrier. It exists for the
// encryption-migration commit: after the system barrier's at-rest data has been
// re-encrypted to a keyed strategy, the live none barrier is replaced with the
// freshly-unsealed keyed barrier. The caller MUST have quiesced (torn down) the
// data plane first so no reader observes the swap mid-flight.
func (r *BarrierRegistry) SetSystemBarrier(b *StorageBarrier) { r.system = b }

// ReplaceTenantBarrier swaps the registered barrier for an existing tenant,
// used by the tenant-scoped encryption-migration commit once that tenant's
// stores are re-encrypted. Returns ErrTenantNotFound if the tenant is not
// registered (a shared-barrier tenant has no dedicated barrier to replace).
func (r *BarrierRegistry) ReplaceTenantBarrier(tenantID string, tb *TenantBarrier) error {
	if _, ok := r.tenants.Load(tenantID); !ok {
		return ErrTenantNotFound
	}
	r.tenants.Store(tenantID, tb)
	return nil
}

// PerTenantPebbleRegistry returns the attached per-tenant Pebble
// registry, or nil when no registry has been configured. It is
// exposed so daemon shutdown handlers can iterate the registry and
// close its instances cleanly.
func (r *BarrierRegistry) PerTenantPebbleRegistry() pebblestore.PerTenantRegistry {
	r.poolMu.RLock()
	defer r.poolMu.RUnlock()
	return r.perTenantReg
}

// RegisterTenant creates and registers a new tenant barrier with the
// given storage backend, config, and strategies.
//
// When base is non-nil, the supplied backend is used directly — this
// is the keyspace-mode flow and the legacy direct-construction path
// used by every existing caller.
//
// When base is nil, the registry pulls a path from its configured
// volume pool (see WithVolumePool), opens or returns an existing
// per-tenant Pebble at that path via the configured PerTenantRegistry
// (see WithPerTenantRegistry), and wraps the resulting Pebble as the
// per-tenant StorageBackend. This is the barrier-mode flow.
//
// Returns ErrTenantAlreadyExists if the tenant id is already
// registered, ErrPoolNotConfigured / ErrPerTenantRegistryNotConfigured
// when the pooled flow is requested but the registry isn't wired for
// it, and ErrPoolBackendNotLocal when the pool returns a backend that
// can't host a Pebble.
func (r *BarrierRegistry) RegisterTenant(
	ctx context.Context,
	tenantID string,
	base StorageBackend,
	config BarrierConfig,
	strategies ...SealingStrategy,
) (*TenantBarrier, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if _, loaded := r.tenants.Load(tenantID); loaded {
		return nil, ErrTenantAlreadyExists
	}

	if base == nil {
		var err error
		base, err = r.allocatePooledBackend(ctx, tenantID)
		if err != nil {
			return nil, err
		}
	}

	tb, err := NewTenantBarrier(tenantID, r.logger, base, config, strategies...)
	if err != nil {
		return nil, err
	}

	if _, loaded := r.tenants.LoadOrStore(tenantID, tb); loaded {
		_ = tb.Close()
		return nil, ErrTenantAlreadyExists
	}

	return tb, nil
}

// allocatePooledBackend resolves the local volume-pool base for the tenant
// (cluster-wide relative slot in clustered mode, node-local Pick otherwise),
// opens a per-tenant Pebble at the resolved local path, and wraps it as a
// StorageBackend.
func (r *BarrierRegistry) allocatePooledBackend(ctx context.Context, tenantID string) (StorageBackend, error) {
	pool, defaultPoolName, perTenantReg := r.poolResources()
	if pool == nil {
		return nil, ErrPoolNotConfigured
	}
	if perTenantReg == nil {
		return nil, ErrPerTenantRegistryNotConfigured
	}

	base, err := r.resolveTenantPoolBase(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	tenantPath, err := containedTenantPath(base, tenantID)
	if err != nil {
		return nil, err
	}
	storage, err := perTenantReg.Open(ctx, tenantID, tenantPath)
	if err != nil {
		return nil, err
	}
	if r.logger != nil {
		r.logger.Info("barrier-mode tenant assigned to volume pool path",
			slog.String("tenant", tenantID),
			slog.String("path", tenantPath),
			slog.String("pool", defaultPoolName))
	}

	return NewStorageAdapter(storage), nil
}

// slotAssignmentPrefix is the key prefix under which the cluster-wide
// tenant→relative-slot assignment is recorded in the replicated system shard.
// Key: slotAssignmentPrefix + tenantID; Value: JSON-encoded slotAssignment.
// It shares the __sys:volumepool/ namespace with the round-robin cursor so all
// pool state is identifiable in one prefix scan.
const slotAssignmentPrefix = "__sys:volumepool/assign/"

// slotAssignment is the replicated record of a tenant's pool placement. Slot is
// the RELATIVE candidate index within the named pool — every node realizes it
// at its OWN local candidate path (homogeneous topology), so the absolute path
// differs per node while the relative slot is identical cluster-wide.
type slotAssignment struct {
	Pool string `json:"pool"`
	Slot int    `json:"slot"`
}

// replicatedSlotStore returns the configured replicated slot-assignment store,
// or nil when the registry runs node-local (standalone) pool selection.
func (r *BarrierRegistry) replicatedSlotStore() StorageBackend {
	r.poolMu.RLock()
	defer r.poolMu.RUnlock()
	return r.slotStore
}

// resolveTenantPoolBase resolves the local pool BASE directory under which a
// tenant's dedicated storage is realized (the path the tenant id is later
// joined onto).
//
// In clustered mode (a replicated slot store is attached) it first resolves the
// cluster-wide relative slot recorded for the tenant and realizes it at THIS
// node's own candidate path, WITHOUT advancing the round-robin cursor — so a
// follower / failover leader uses the same relative slot the original assigner
// chose instead of double-Picking. When no assignment exists yet (first
// provision, on the leader) it Picks a slot (advancing the replicated cursor),
// records the assignment, and returns the picked base; if a concurrent writer
// records first, that writer's slot is resolved and returned so all replicas
// converge.
//
// In standalone mode (no slot store) it always Picks node-locally, preserving
// single-node behavior.
func (r *BarrierRegistry) resolveTenantPoolBase(ctx context.Context, tenantID string) (string, error) {
	pool, defaultPoolName, _ := r.poolResources()
	if pool == nil {
		return "", ErrPoolNotConfigured
	}
	slotStore := r.replicatedSlotStore()

	// Clustered: resolve a previously-recorded assignment first (no Pick).
	if slotStore != nil {
		rec, found, err := r.loadSlotAssignment(ctx, tenantID)
		if err != nil && r.logger != nil {
			r.logger.Warn("registry: slot assignment read failed; falling back to pick",
				slog.String("tenant", tenantID), slog.Any("error", err))
		}
		if found {
			if base, lerr := r.localBaseForSlot(ctx, rec.Pool, rec.Slot); lerr == nil {
				return base, nil
			} else if r.logger != nil {
				r.logger.Warn("registry: recorded slot unresolvable; re-picking",
					slog.String("tenant", tenantID),
					slog.Int("slot", rec.Slot),
					slog.Any("error", lerr))
			}
		}
	}

	// Pick a slot. In clustered mode the cursor is replicated, so this is a
	// single cluster-wide advance; in standalone mode it is the node-local
	// Pebble cursor.
	picked, err := pool.Pick(ctx, defaultPoolName, objpool.Hint{TenantID: tenantID})
	if err != nil {
		return "", err
	}
	accessor, ok := picked.(common.PathAccessor)
	if !ok {
		return "", ErrPoolBackendNotLocal
	}
	base := accessor.LocalPath()

	// Clustered: record the assignment. If another node recorded first, defer
	// to its slot so every replica converges on one relative slot.
	if slotStore != nil {
		base = r.recordSlotAssignment(ctx, tenantID, defaultPoolName, base)
	}
	return base, nil
}

// loadSlotAssignment reads the replicated slot assignment for tenantID. A
// missing record returns (zero, false, nil).
func (r *BarrierRegistry) loadSlotAssignment(ctx context.Context, tenantID string) (slotAssignment, bool, error) {
	slotStore := r.replicatedSlotStore()
	if slotStore == nil {
		return slotAssignment{}, false, nil
	}
	data, err := slotStore.Get(ctx, slotAssignmentPrefix+tenantID)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return slotAssignment{}, false, nil
		}
		return slotAssignment{}, false, err
	}
	var rec slotAssignment
	if uerr := json.Unmarshal(data, &rec); uerr != nil {
		// Corrupt entry: treat as absent so a fresh assignment can overwrite.
		return slotAssignment{}, false, nil
	}
	return rec, true, nil
}

// recordSlotAssignment maps pickedBase to its relative slot index and records
// the tenant→slot assignment in the replicated store via PutIfAbsent. It
// returns the BASE the caller should use: the picked base when this node won
// the assignment (or recording was best-effort-skipped), or the winning node's
// resolved base when a concurrent writer recorded first. All failures are
// non-fatal — the picked base is returned and the tenant is still provisioned.
func (r *BarrierRegistry) recordSlotAssignment(ctx context.Context, tenantID, poolName, pickedBase string) string {
	slotStore := r.replicatedSlotStore()
	if slotStore == nil {
		return pickedBase
	}
	slot, ok := r.slotForBase(ctx, poolName, pickedBase)
	if !ok {
		if r.logger != nil {
			r.logger.Warn("registry: picked pool base not found among candidates; assignment not recorded",
				slog.String("tenant", tenantID), slog.String("base", pickedBase))
		}
		return pickedBase
	}
	blob, err := json.Marshal(slotAssignment{Pool: poolName, Slot: slot})
	if err != nil {
		return pickedBase
	}
	stored, err := slotStore.PutIfAbsent(ctx, slotAssignmentPrefix+tenantID, blob)
	if err != nil {
		if r.logger != nil {
			r.logger.Warn("registry: failed to record slot assignment; using node-local pick",
				slog.String("tenant", tenantID), slog.Any("error", err))
		}
		return pickedBase
	}
	if stored {
		if r.logger != nil {
			r.logger.Info("registry: recorded cluster-wide volume-pool slot assignment",
				slog.String("tenant", tenantID),
				slog.String("pool", poolName),
				slog.Int("slot", slot))
		}
		return pickedBase
	}
	// Lost the race: another node recorded first. Resolve its slot so all
	// replicas converge.
	rec, found, rerr := r.loadSlotAssignment(ctx, tenantID)
	if rerr != nil || !found {
		return pickedBase
	}
	if base, lerr := r.localBaseForSlot(ctx, rec.Pool, rec.Slot); lerr == nil {
		return base
	}
	return pickedBase
}

// seedSlotAssignment records the tenant's slot assignment from an already-known
// local base when none is recorded yet. It is the Phase 2 compatibility
// migration: clusters created before the replicated assignment existed have
// only node-local DataRoot records, so on boot re-attach (RegisterVolumeTenant
// with a provided DataRoot) the slot is seeded from that local path. It is
// best-effort and non-fatal.
func (r *BarrierRegistry) seedSlotAssignment(ctx context.Context, tenantID, base string) {
	slotStore := r.replicatedSlotStore()
	if slotStore == nil || base == "" {
		return
	}
	if _, found, _ := r.loadSlotAssignment(ctx, tenantID); found {
		return
	}
	_, defaultPoolName, _ := r.poolResources()
	r.recordSlotAssignment(ctx, tenantID, defaultPoolName, base)
}

// slotForBase returns the relative candidate index whose local path equals base
// within the named pool, or ok=false when the pool or base is not found.
func (r *BarrierRegistry) slotForBase(ctx context.Context, poolName, base string) (int, bool) {
	pool, _, _ := r.poolResources()
	if pool == nil {
		return 0, false
	}
	pools, err := pool.List(ctx)
	if err != nil {
		return 0, false
	}
	for _, p := range pools {
		if p.Name != poolName {
			continue
		}
		for i, cand := range p.Candidates {
			if accessor, ok := cand.(common.PathAccessor); ok && accessor.LocalPath() == base {
				return i, true
			}
		}
	}
	return 0, false
}

// localBaseForSlot resolves the relative slot index to THIS node's local
// candidate path within the named pool. Returns ErrPoolSlotUnresolved when the
// pool is missing, the slot is out of range, or the candidate is not local.
func (r *BarrierRegistry) localBaseForSlot(ctx context.Context, poolName string, slot int) (string, error) {
	pool, _, _ := r.poolResources()
	if pool == nil {
		return "", ErrPoolNotConfigured
	}
	pools, err := pool.List(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range pools {
		if p.Name != poolName {
			continue
		}
		if slot < 0 || slot >= len(p.Candidates) {
			return "", ErrPoolSlotUnresolved
		}
		accessor, ok := p.Candidates[slot].(common.PathAccessor)
		if !ok {
			return "", ErrPoolBackendNotLocal
		}
		return accessor.LocalPath(), nil
	}
	return "", ErrPoolSlotUnresolved
}

// containedTenantPath joins tenantID onto the volume-pool root and
// verifies the result is a strict child of the root. The tenant service
// rejects path-unsafe ids at creation time; this is the fail-closed
// defense at the filesystem sink — a traversal id (one containing path
// separators or ".." components) returns ErrTenantPathEscapesPool
// instead of opening a Pebble store outside the pool.
func containedTenantPath(root, tenantID string) (string, error) {
	joined := filepath.Join(root, tenantID)
	rel, err := filepath.Rel(root, joined)
	if err != nil {
		return "", ErrTenantPathEscapesPool
	}
	// The tenant directory must be exactly one path component below the
	// root: not the root itself ("."), not above it (".." prefix), and
	// not nested deeper (a separator in the relative path).
	if rel == "." || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) ||
		strings.ContainsRune(rel, filepath.Separator) {
		return "", ErrTenantPathEscapesPool
	}
	return joined, nil
}

// poolResources returns the configured volume-pool manager, default pool
// name, and per-tenant Pebble registry under a read lock. It is the shared
// accessor used by allocatePooledBackend and the IsolationVolume path.
func (r *BarrierRegistry) poolResources() (objpool.Manager, string, pebblestore.PerTenantRegistry) {
	r.poolMu.RLock()
	defer r.poolMu.RUnlock()
	return r.pool, r.defaultPoolName, r.perTenantReg
}

// migrationRecovererHook returns the configured MigrationRecoverer (nil when
// none) under the same read lock that guards the other boot-wired fields.
func (r *BarrierRegistry) migrationRecovererHook() MigrationRecoverer {
	r.poolMu.RLock()
	defer r.poolMu.RUnlock()
	return r.migrationRecoverer
}

// LocalTenantDataRoot returns the local volume-pool DataRoot for tenantID as
// recorded when RegisterVolumeTenant was called for that tenant. It is used
// by follower replicas whose placement Replica.DataRoot is empty (BUG 1) to
// recover the correct shard DataDir without re-running the round-robin
// pool.Pick (which would advance the cursor and return a different root).
//
// The tenant MUST have been registered via RegisterVolumeTenant before this
// method is called — it returns ErrTenantNotFound when no DataRoot is on
// record. For the reconciler / shard factory path this is guaranteed: the
// barrier gate in Services() ensures the tenant is registered before
// databaseInfo resolves the shard path.
//
// Returns ErrPoolNotConfigured when no volume pool is attached, and
// ErrTenantNotFound when the tenant has not yet been registered via
// RegisterVolumeTenant.
func (r *BarrierRegistry) LocalTenantDataRoot(ctx context.Context, tenantID string) (string, error) {
	pool, _, _ := r.poolResources()
	if pool == nil {
		return "", ErrPoolNotConfigured
	}
	if v, ok := r.tenantDataRoots.Load(tenantID); ok {
		return v.(string), nil
	}
	// In-memory map miss: the tenant was registered in a prior process run
	// and the tenantDataRoots sync.Map was not yet populated (e.g. on a
	// follower node whose RestoreVolumeTenants hasn't run yet, or a node
	// that received key delivery but restarted before the map was populated).
	// Fall back to the persistent system-barrier store and, on a hit, warm
	// the in-memory map so subsequent calls are O(1).
	rec, ok, err := r.loadTenantRoot(ctx, tenantID)
	if err != nil {
		return "", err
	}
	if !ok || rec.DataRoot == "" {
		return "", ErrTenantNotFound
	}
	// Warm the cache so future calls skip the store lookup.
	r.tenantDataRoots.Store(tenantID, rec.DataRoot)
	return rec.DataRoot, nil
}

// tenantRootStorePrefix is the key prefix under which per-tenant DataRoot
// records are persisted in the system barrier. These records allow follower
// nodes to reconstruct the tenantDataRoots in-memory map after a restart
// without a new pool.Pick call (which would advance the round-robin cursor).
//
// Key: tenantRootStorePrefix + tenantID
// Value: JSON-encoded tenantRootRecord
const tenantRootStorePrefix = "__registry:tenant_root/"

// tenantRootRecord is the persisted form of a volume-tenant assignment.
type tenantRootRecord struct {
	// DataRoot is the on-disk DataRoot allocated for the tenant.
	DataRoot string `json:"data_root"`
	// AutoUnseal indicates whether the tenant uses the system barrier as its
	// unseal authority (true) or self-manages unseal credentials (false).
	AutoUnseal bool `json:"auto_unseal"`
}

// persistTenantRoot writes the tenant's DataRoot and AutoUnseal flag to the
// system barrier store. The system barrier must be unsealed; a sealed barrier
// is silently skipped (the session can still operate, but the entry will be
// missing on the next restart).
func (r *BarrierRegistry) persistTenantRoot(ctx context.Context, tenantID, dataRoot string, autoUnseal bool) error {
	if r.system == nil || r.system.IsSealed() {
		return nil
	}
	blob, err := json.Marshal(tenantRootRecord{DataRoot: dataRoot, AutoUnseal: autoUnseal})
	if err != nil {
		return err
	}
	return r.system.Put(ctx, tenantRootStorePrefix+tenantID, blob)
}

// loadTenantRoot reads the persisted DataRoot record for tenantID from the
// system barrier store. Returns (nil, false, nil) when the key is absent.
func (r *BarrierRegistry) loadTenantRoot(ctx context.Context, tenantID string) (*tenantRootRecord, bool, error) {
	if r.system == nil || r.system.IsSealed() {
		return nil, false, nil
	}
	data, err := r.system.Get(ctx, tenantRootStorePrefix+tenantID)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var rec tenantRootRecord
	if uerr := json.Unmarshal(data, &rec); uerr != nil {
		// Corrupt entry: treat as not found so a fresh registration can overwrite.
		return nil, false, nil
	}
	return &rec, true, nil
}

// RestoreVolumeTenants re-registers all IsolationVolume / IsolationRaft
// tenant barriers whose DataRoot records were persisted to the system barrier
// store (by RegisterVolumeTenant during a prior run). Each tenant that is not
// yet in the in-memory registry is re-registered at its stored DataRoot path
// and, for system-managed (auto-unseal) tenants, unsealed credential-free.
//
// This is the boot-time complement to reattachRaftTenants: while
// reattachRaftTenants handles the node that was the cluster leader during the
// original tenant CREATE (it has TenantInfo in its local seal store),
// RestoreVolumeTenants handles FOLLOWER nodes that received key delivery but
// never stored TenantInfo. Their barrier key material is persisted on-disk at
// the recorded DataRoot; this method re-opens it.
//
// The method is idempotent: tenants already present in the registry are
// skipped. Individual re-registration failures are logged and skipped rather
// than aborting; the caller's reconciler will retry on the next pass.
func (r *BarrierRegistry) RestoreVolumeTenants(ctx context.Context) error {
	if r.system == nil || r.system.IsSealed() {
		return nil
	}
	keys, err := r.system.List(ctx, tenantRootStorePrefix)
	if err != nil {
		return err
	}
	prefixLen := len(tenantRootStorePrefix)
	for _, key := range keys {
		if len(key) <= prefixLen {
			continue
		}
		tenantID := key[prefixLen:]
		// Skip if already registered in the in-memory registry (e.g., the R1
		// leader node that ran reattachRaftTenants successfully).
		if _, rerr := r.GetTenant(tenantID); rerr == nil {
			continue
		}
		data, derr := r.system.Get(ctx, key)
		if derr != nil {
			r.logger.Warn("registry: restore: failed to read tenant root record",
				slog.String("tenant", tenantID),
				slog.Any("error", derr))
			continue
		}
		var rec tenantRootRecord
		if uerr := json.Unmarshal(data, &rec); uerr != nil {
			r.logger.Warn("registry: restore: corrupt tenant root record, skipping",
				slog.String("tenant", tenantID))
			continue
		}
		if rec.DataRoot == "" {
			r.logger.Warn("registry: restore: tenant root record has empty DataRoot, skipping",
				slog.String("tenant", tenantID))
			continue
		}
		if _, _, rerr := r.RegisterVolumeTenant(ctx, tenantID, rec.DataRoot, rec.AutoUnseal); rerr != nil {
			if errors.Is(rerr, ErrTenantAlreadyExists) {
				continue // registered concurrently
			}
			r.logger.Warn("registry: restore: failed to register tenant",
				slog.String("tenant", tenantID),
				slog.String("data_root", rec.DataRoot),
				slog.Any("error", rerr))
			continue
		}
		if !rec.AutoUnseal {
			// Self-managed: leave sealed; the tenant admin drives unseal.
			r.logger.Info("registry: restore: self-managed tenant registered, waiting for tenant unseal",
				slog.String("tenant", tenantID))
			continue
		}
		if uerr := r.UnsealTenant(ctx, tenantID, Credentials{}); uerr != nil {
			r.logger.Warn("registry: restore: auto-unseal failed for restored tenant",
				slog.String("tenant", tenantID),
				slog.Any("error", uerr))
			continue
		}
		r.logger.Info("registry: restore: tenant barrier restored and unsealed",
			slog.String("tenant", tenantID),
			slog.String("data_root", rec.DataRoot))
	}
	return nil
}

// RegisterVolumeTenant registers a durable, barrier-isolated tenant for the
// IsolationVolume mode and returns the tenant's filesystem DataRoot. Unlike
// RegisterTenantPooled (whose barrier Pebble occupies the entire per-tenant
// pool directory), the IsolationVolume layout reserves the pool directory as
// the tenant's DataRoot and places the barrier IAM Pebble in a dedicated
// "barrier" subdirectory, leaving "databases" free for the per-tenant Raft
// host's state. The resulting layout is:
//
//	{poolRoot}/{tenantID}/barrier      — barrier IAM Pebble (this registration)
//	{poolRoot}/{tenantID}/databases    — per-tenant Raft host state (caller)
//
// When dataRoot is empty a fresh path is allocated from the configured volume
// pool; when non-empty (boot re-attach) the supplied DataRoot is reused so the
// tenant re-binds to the same on-disk location.
//
// autoUnseal selects the tenant's key-management posture: when true the tenant
// DEK is wrapped under the SYSTEM barrier (SystemBarrierStrategy) so it can be
// initialized and re-attached credential-free; when false the tenant inherits
// the system barrier's own (credential-bound) sealing strategies and the tenant
// admin self-manages init/unseal. Either way the returned barrier is registered
// but NOT initialized or unsealed; the caller drives that (Initialize on create
// for auto-unseal tenants, Unseal on re-attach; self-managed tenants are left
// sealed for the tenant admin), mirroring the IsolationBarrier flow.
func (r *BarrierRegistry) RegisterVolumeTenant(ctx context.Context, tenantID, dataRoot string, autoUnseal bool) (string, *TenantBarrier, error) {
	if tenantID == "" {
		return "", nil, ErrEmptyTenantID
	}
	pool, defaultPoolName, perTenantReg := r.poolResources()
	if pool == nil {
		return "", nil, ErrPoolNotConfigured
	}
	if perTenantReg == nil {
		return "", nil, ErrPerTenantRegistryNotConfigured
	}

	if dataRoot == "" {
		base, err := r.resolveTenantPoolBase(ctx, tenantID)
		if err != nil {
			return "", nil, err
		}
		dataRoot, err = containedTenantPath(base, tenantID)
		if err != nil {
			return "", nil, err
		}
	} else {
		// Provided DataRoot (boot re-attach / RestoreVolumeTenants). Seed the
		// replicated slot assignment from this node-local path when it is
		// absent — the Phase 2 compatibility migration for clusters that
		// recorded only node-local DataRoots before the cluster-wide assignment
		// existed. Best-effort and non-fatal.
		r.seedSlotAssignment(ctx, tenantID, filepath.Dir(dataRoot))
	}

	barrierPath := filepath.Join(dataRoot, "barrier")
	storage, err := perTenantReg.Open(ctx, tenantID, barrierPath)
	if err != nil {
		return "", nil, err
	}
	backend := NewStorageAdapter(storage)

	cfg := tenantBarrierConfig(tenantID)
	// Reconcile any interrupted encryption migration recorded in the tenant's
	// journal BEFORE the barrier is built: a crash in the swap→adopt commit
	// window leaves half-swapped store dirs, staged snapshot siblings, and a
	// root key the journal has superseded. The barrier (and then the tenant's
	// data stores) must come up over the reconciled state, so a recovery
	// failure fails the registration closed. The tenant's stores are not yet
	// open here — registration precedes every host/shard attach.
	if rec := r.migrationRecovererHook(); rec != nil {
		if rerr := rec(ctx, backend, cfg.RootKeyPath); rerr != nil {
			return "", nil, &MigrationRecoveryError{TenantID: tenantID, Err: rerr}
		}
	}

	strategies := r.systemStrategies()
	if autoUnseal {
		strategies = []SealingStrategy{r.systemBarrierStrategy()}
	}
	tb, err := r.RegisterTenant(ctx, tenantID, backend, cfg, strategies...)
	if err != nil {
		return "", nil, err
	}
	// Record the DataRoot so LocalTenantDataRoot can serve it without a
	// second pool.Pick call (which would advance the round-robin cursor).
	r.tenantDataRoots.Store(tenantID, dataRoot)
	// Persist the DataRoot to the system barrier so that follower nodes
	// (and any node after a restart) can restore their tenantDataRoots
	// map via RestoreVolumeTenants without re-running pool.Pick.
	if perr := r.persistTenantRoot(ctx, tenantID, dataRoot, autoUnseal); perr != nil {
		if r.logger != nil {
			r.logger.Warn("registry: failed to persist tenant root; restart re-attach may require key delivery",
				slog.String("tenant", tenantID),
				slog.Any("error", perr))
		}
	}
	if r.logger != nil {
		r.logger.Info("volume-mode tenant assigned to volume pool path",
			slog.String("tenant", tenantID),
			slog.String("data_root", dataRoot),
			slog.String("barrier_path", barrierPath),
			slog.String("pool", defaultPoolName))
	}
	return dataRoot, tb, nil
}

// RegisterTenantDefault creates and registers a tenant barrier with
// default in-memory storage and the system barrier's strategies. This
// is a convenience shorthand for RegisterTenant with sensible
// defaults; ctx is taken from the background and the keyspace-mode
// path is used.
func (r *BarrierRegistry) RegisterTenantDefault(tenantID string) (*TenantBarrier, error) {
	base := NewMemoryBackend()
	config := tenantBarrierConfig(tenantID)
	return r.RegisterTenant(context.Background(), tenantID, base, config, r.systemStrategies()...)
}

// RegisterTenantPooled creates and registers a durable, barrier-isolated
// tenant. It mirrors RegisterTenantDefault but passes a nil base backend
// so RegisterTenant takes the pool-driven flow: a per-tenant path is
// pulled from the configured volume pool and a dedicated per-tenant Pebble
// is opened at that path (see WithVolumePool / WithPerTenantRegistry). The
// tenant inherits the system barrier's sealing strategies so its DEK is
// wrapped with the same posture as the system barrier.
//
// This is the registration path for IsolationBarrier tenants that do not
// specify their own strategy: their IAM data (and any other per-tenant
// state) is encrypted with the tenant's own DEK and stored under the
// tenant's "{tenantID}/" namespace in an independent backend, never in the
// shared system store. When the volume pool / per-tenant registry is not
// configured, RegisterTenant returns ErrPoolNotConfigured /
// ErrPerTenantRegistryNotConfigured.
//
// Inheritance of the system strategy is the DEFAULT. A tenant that needs
// its own sealing posture (e.g. TPM2 or Shamir) registers via
// RegisterTenantPooledWithStrategy instead.
func (r *BarrierRegistry) RegisterTenantPooled(ctx context.Context, tenantID string) (*TenantBarrier, error) {
	return r.RegisterTenant(ctx, tenantID, nil, tenantBarrierConfig(tenantID), r.systemStrategies()...)
}

// RegisterTenantPooledWithStrategy creates and registers a durable,
// barrier-isolated tenant using the SAME durable Pebble-pool backend as
// RegisterTenantPooled, but seals the tenant's DEK with the GIVEN strategies
// instead of inheriting the system barrier's posture.
//
// This is the registration path for IsolationBarrier tenants that supply
// their own sealing strategy (e.g. TPM2 or Shamir): the per-tenant path is
// pulled from the configured volume pool and a dedicated per-tenant Pebble
// is opened at that path (see WithVolumePool / WithPerTenantRegistry). When
// the volume pool / per-tenant registry is not configured, RegisterTenant
// returns ErrPoolNotConfigured / ErrPerTenantRegistryNotConfigured.
//
// At least one strategy must be supplied; callers that want to inherit the
// system posture use RegisterTenantPooled instead.
func (r *BarrierRegistry) RegisterTenantPooledWithStrategy(
	ctx context.Context,
	tenantID string,
	strategies ...SealingStrategy,
) (*TenantBarrier, error) {
	return r.RegisterTenant(ctx, tenantID, nil, tenantBarrierConfig(tenantID), strategies...)
}

// RegisterTenantPooledShamir creates and registers a durable, barrier-isolated
// tenant whose DEK is sealed with an M-of-N Shamir scheme. It mirrors
// RegisterTenantPooledWithStrategy but additionally constructs a ShamirStrategy
// for the requested threshold/total and populates the per-tenant BarrierConfig's
// Shamir section so InitializeShamir / UnsealWithShare can drive the quorum.
//
// threshold must be >= 2 and <= total. When ttl is zero the barrier falls back
// to the default quorum share TTL. The strategy splits the tenant's root key
// directly (direct mode); the generated shares are returned from
// InitializeTenantShamir and are never persisted.
func (r *BarrierRegistry) RegisterTenantPooledShamir(
	ctx context.Context,
	tenantID string,
	threshold, total int,
	ttl time.Duration,
) (*TenantBarrier, error) {
	strat, err := NewShamirStrategy(threshold, total)
	if err != nil {
		return nil, err
	}
	cfg := tenantBarrierConfig(tenantID)
	cfg.Shamir = &ShamirConfig{
		Threshold:   threshold,
		TotalShares: total,
		QuorumTTL:   ttl,
	}
	return r.RegisterTenant(ctx, tenantID, nil, cfg, strat)
}

// InitializeTenantShamir initializes the tenant barrier identified by tenantID
// using Shamir secret sharing and seals shares to the enrolled Security Officers
// via the barrier's CeremonyDistributor. Returns metadata only; no share bytes
// are returned. The tenant must already be registered via RegisterTenantPooledShamir.
func (r *BarrierRegistry) InitializeTenantShamir(ctx context.Context, tenantID string, creds Credentials) (*ShamirInitParams, error) {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		return nil, err
	}
	return tb.InitializeShamir(ctx, creds)
}

// UnsealTenantWithShare submits a single Shamir share toward the tenant
// barrier's quorum threshold, automatically unsealing once the threshold is
// met. The tenant must already be registered and initialized with Shamir.
func (r *BarrierRegistry) UnsealTenantWithShare(ctx context.Context, tenantID, shareValue string) (*QuorumProgress, error) {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		return nil, err
	}
	return tb.UnsealWithShare(ctx, shareValue)
}

// tenantBarrierConfig returns the canonical BarrierConfig for a per-tenant
// barrier. It is the single source of truth for the per-tenant root-key
// path so every registration variant derives it identically.
func tenantBarrierConfig(tenantID string) BarrierConfig {
	return BarrierConfig{
		RootKeyPath: "tenant/" + tenantID + "/root-key",
	}
}

// systemBarrierStrategy returns a fresh SystemBarrierStrategy that wraps tenant
// root keys under the registry's already-unsealed system barrier. It is the
// composition point for the system-managed auto-unseal key-management mode: a
// tenant registered with this strategy can be initialized at create time and
// re-attached at boot WITHOUT any credential, because the system barrier (the
// unseal authority) is always unsealed.
func (r *BarrierRegistry) systemBarrierStrategy() SealingStrategy {
	return NewSystemBarrierStrategy(r.system)
}

// RegisterTenantPooledSystemManaged creates and registers a durable,
// barrier-isolated tenant whose root key is wrapped under the SYSTEM barrier
// (the system-managed auto-unseal mode). It mirrors RegisterTenantPooled but
// seals the tenant DEK with the SystemBarrierStrategy instead of inheriting the
// system barrier's own (credential-bound) strategy, so the tenant can be
// initialized credential-free at create and re-attached credential-free at
// boot. The pooled Pebble backend is allocated exactly as for
// RegisterTenantPooled; only the sealing posture differs.
func (r *BarrierRegistry) RegisterTenantPooledSystemManaged(ctx context.Context, tenantID string) (*TenantBarrier, error) {
	return r.RegisterTenant(ctx, tenantID, nil, tenantBarrierConfig(tenantID), r.systemBarrierStrategy())
}

// systemStrategies returns the system barrier's sealing strategies as a
// slice suitable for passing to RegisterTenant. It is the inheritance seam
// shared by RegisterTenantDefault and RegisterTenantPooled.
func (r *BarrierRegistry) systemStrategies() []SealingStrategy {
	strategies := r.system.Strategies()
	strategySlice := make([]SealingStrategy, 0, len(strategies))
	for _, st := range strategies {
		strategySlice = append(strategySlice, st)
	}
	return strategySlice
}

// GetTenant looks up a registered tenant barrier by ID. Returns
// ErrEmptyTenantID if tenantID is empty, or ErrTenantNotFound if the
// tenant is not registered.
func (r *BarrierRegistry) GetTenant(tenantID string) (*TenantBarrier, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	val, ok := r.tenants.Load(tenantID)
	if !ok {
		return nil, ErrTenantNotFound
	}
	return val.(*TenantBarrier), nil
}

// Tenant is a convenience alias for GetTenant.
func (r *BarrierRegistry) Tenant(tenantID string) (*TenantBarrier, error) {
	return r.GetTenant(tenantID)
}

// RemoveTenant seals and removes the tenant barrier for the given ID.
// Returns ErrTenantNotFound if the tenant is not registered.
func (r *BarrierRegistry) RemoveTenant(tenantID string) error {
	val, ok := r.tenants.LoadAndDelete(tenantID)
	if !ok {
		return ErrTenantNotFound
	}
	return val.(*TenantBarrier).Close()
}

// UnregisterTenant is a convenience alias for RemoveTenant.
func (r *BarrierRegistry) UnregisterTenant(tenantID string) error {
	return r.RemoveTenant(tenantID)
}

// InitializeTenant initializes the tenant barrier identified by tenantID.
// The tenant must already be registered via RegisterTenant.
func (r *BarrierRegistry) InitializeTenant(ctx context.Context, tenantID string, creds Credentials) error {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		return err
	}
	return tb.Initialize(ctx, creds)
}

// UnsealTenant unseals the tenant barrier identified by tenantID using the
// supplied credentials. The tenant must already be registered and
// initialized.
func (r *BarrierRegistry) UnsealTenant(ctx context.Context, tenantID string, creds Credentials) error {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		return err
	}
	return tb.Unseal(ctx, creds)
}

// SealTenant seals the tenant barrier identified by tenantID, releasing its
// in-memory key material. The tenant must already be registered.
func (r *BarrierRegistry) SealTenant(tenantID string) error {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		return err
	}
	return tb.Seal()
}

// TenantStatus returns the current status of the tenant barrier identified
// by tenantID. The tenant must already be registered.
func (r *BarrierRegistry) TenantStatus(tenantID string) (*BarrierStatus, error) {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		return nil, err
	}
	return tb.Status(), nil
}

// ImportTenant registers a volume-backed tenant barrier and initializes it
// from a replicated root-key context rather than generating a new root key.
// It is the high-level entry point for bootstrapping follower replicas in a
// multi-node tenant Raft group.
//
// The tenant is registered via RegisterVolumeTenant (which allocates a
// pool-backed Pebble if dataRoot is empty, or re-attaches an existing one).
// After registration, one of two initialization paths is taken:
//
//   - autoUnseal=true (system-managed): calls ImportRootKey with the supplied
//     ImportedKey; the root key is re-sealed under this node's system barrier
//     strategy and the barrier is immediately unsealed, yielding identical DEKs
//     to the leader. The system barrier must be unsealed before calling.
//
//   - autoUnseal=false (self-managed): calls ImportSealedBlob with the supplied
//     blob; the portable sealed root key is stored verbatim and the barrier
//     remains sealed. The tenant unseals it later with their own credential.
//
// Returns the registered TenantBarrier on success. If registration or
// initialization fails, the tenant may be left registered but uninitialized;
// callers should call RemoveTenant to clean up on error.
func (r *BarrierRegistry) ImportTenant(
	ctx context.Context,
	tenantID, dataRoot string,
	autoUnseal bool,
	imported *ImportedKey,
	sealedBlob []byte,
) (*TenantBarrier, error) {
	_, tb, err := r.RegisterVolumeTenant(ctx, tenantID, dataRoot, autoUnseal)
	if err != nil {
		return nil, err
	}
	if autoUnseal {
		if err := tb.Barrier().ImportRootKey(ctx, imported, Credentials{}); err != nil {
			return nil, err
		}
	} else {
		if err := tb.Barrier().ImportSealedBlob(ctx, sealedBlob); err != nil {
			return nil, err
		}
	}
	return tb, nil
}

// ListTenants returns the IDs of all registered tenant barriers. The order
// is non-deterministic because the backing store is a sync.Map.
func (r *BarrierRegistry) ListTenants() []string {
	ids := make([]string, 0)
	r.tenants.Range(func(key, _ any) bool {
		ids = append(ids, key.(string))
		return true
	})
	return ids
}

// SealAll seals every registered tenant barrier and then the system
// barrier. It accumulates errors so a single failing tenant does not
// prevent the remaining barriers from being sealed; the first error
// encountered is returned.
func (r *BarrierRegistry) SealAll() error {
	var errs []error
	r.tenants.Range(func(_, val any) bool {
		if err := val.(*TenantBarrier).Seal(); err != nil {
			errs = append(errs, err)
		}
		return true
	})
	if err := r.system.Seal(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Close seals and closes every registered tenant barrier and then closes
// the system barrier, releasing all underlying storage handles. It
// accumulates errors so a single failing barrier does not prevent the
// remaining barriers from being closed.
func (r *BarrierRegistry) Close() error {
	var errs []error
	r.tenants.Range(func(_, val any) bool {
		if err := val.(*TenantBarrier).Close(); err != nil {
			errs = append(errs, err)
		}
		return true
	})
	if err := r.system.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
