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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/validation"
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

	// sealMu serializes a whole-node seal (SealAll) against the node
	// unseal's re-unseal of the tenants it sealed and against the explicit
	// tenant seal and unseal (SealTenant, UnsealTenant,
	// UnsealTenantWithShare), and guards nodeSealed and unsealGate.
	sealMu sync.Mutex
	// nodeSealed holds the system-managed tenants the last whole-node seal
	// moved from unsealed to sealed -- the only tenants the node unseal
	// brings back -- each with the error of its last re-unseal attempt.
	// An explicit tenant seal removes a tenant (SealTenant).
	nodeSealed map[string]error
	// unsealGate, when set, vetoes a tenant's re-unseal (WithTenantUnsealGate).
	unsealGate func(tenantID string) bool

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

	// remover is the filesystem tenant directories are reclaimed through; nil
	// means the operating system. See WithDirectoryRemover.
	remover DirectoryRemover

	// migrationRecoverer, when non-nil, reconciles an interrupted encryption
	// migration recorded in a tenant's journal BEFORE the tenant barrier is
	// built (see MigrationRecoverer). Guarded by poolMu like the other
	// boot-wired composition fields.
	migrationRecoverer MigrationRecoverer

	// auditTrail is the compliance trail every tenant barrier records on
	// (WithAuditTrail). Guarded by poolMu.
	auditTrail audit.AuditLogger
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

// WithTenantUnsealGate makes the node unseal leave sealed every tenant for
// which refuses reports true -- the composer passes the cluster seal's
// verdict on the tenant's record (sealed, or unreadable), so a tenant sealed
// cluster-wide is never brought back by the node's own unseal. Nil refuses
// none.
func WithTenantUnsealGate(refuses func(tenantID string) bool) Option {
	return func(r *BarrierRegistry) {
		r.sealMu.Lock()
		r.unsealGate = refuses
		r.sealMu.Unlock()
	}
}

// TenantUnsealRefused reports whether the node unseal would leave tenantID
// sealed because the registry's unseal gate refuses it
// (WithTenantUnsealGate): its cluster seal record is sealed or cannot be
// read. Without a gate it refuses none.
func (r *BarrierRegistry) TenantUnsealRefused(tenantID string) bool {
	r.sealMu.Lock()
	gate := r.unsealGate
	r.sealMu.Unlock()
	return gate != nil && gate(tenantID)
}

// PendingTenantUnseals returns the system-managed tenants the last node seal
// sealed whose unseal with the node barrier has not succeeded, each with the
// error of its last attempt (nil if none was made yet). The node's next
// unseal retries them; a tenant's own unseal recovers it at any time.
func (r *BarrierRegistry) PendingTenantUnseals() map[string]error {
	r.sealMu.Lock()
	defer r.sealMu.Unlock()
	out := make(map[string]error, len(r.nodeSealed))
	for id, err := range r.nodeSealed {
		out[id] = err
	}
	return out
}

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

// DetachReplicatedSlotStore clears the replicated slot store attached by
// WithReplicatedSlotStore, returning the registry to node-local Pick.
// Callers retract the attachment when the store it wraps (the replicated
// system KV) has been stopped -- a boot attempt that attached it and then
// failed -- so neither this registry nor a caller holding a reference to it
// is left pointing at the stopped backend. This is defensive hygiene, not a
// fallback that serves a request on the node's behalf: a clustered SDK boot
// that failed this far along has already re-sealed the node (see
// Node.completeClusteredBoot's failure defer in sdk/go/node), and
// TenantService.CreateTenant -- the supported entry point into tenant
// provisioning -- reads through the system barrier before it reaches a slot
// store at all, so it fails closed (seal.ErrSealed) regardless of whether a
// slot store happens to be attached. RegisterVolumeTenant/ReattachVolumeTenant
// on this registry do NOT carry the same check if called directly, bypassing
// TenantService; detaching here narrows that window but does not close it.
// Idempotent: calling it when no slot store is attached is a no-op.
func (r *BarrierRegistry) DetachReplicatedSlotStore() {
	r.poolMu.Lock()
	defer r.poolMu.Unlock()
	r.slotStore = nil
}

// HasReplicatedSlotStore reports whether a replicated slot store is
// currently attached (via WithReplicatedSlotStore or Attach), as opposed to
// the registry resolving a pooled tenant's slot through node-local Pick.
// Exported for callers that need to assert the attachment state directly,
// such as a test proving DetachReplicatedSlotStore actually cleared it.
func (r *BarrierRegistry) HasReplicatedSlotStore() bool {
	return r.replicatedSlotStore() != nil
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
		logger:     logger,
		system:     system,
		nodeSealed: map[string]error{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	system.OnUnsealed(r.unsealSystemManagedTenants)
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
func (r *BarrierRegistry) SetSystemBarrier(b *StorageBarrier) {
	r.system = b
	if b != nil {
		b.OnUnsealed(r.unsealSystemManagedTenants)
	}
}

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

	if config.AuditLogger == nil {
		r.poolMu.RLock()
		config.AuditLogger = NewComplianceAuditLogger(r.auditTrail, tenantID, TenantBarrierAuditName(tenantID))
		r.poolMu.RUnlock()
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

// assertTenantStorePresent verifies a tenant's persisted barrier Pebble
// directory exists and is non-empty before it is re-opened on the boot
// re-attach path. A missing directory (dedicated mount not present) or an
// existing-but-empty directory (mount present but never populated / wrong
// path) both yield DataRootMountMissingError, so re-attach fails closed
// instead of opening a fresh empty barrier over the missing store.
func assertTenantStorePresent(tenantID, barrierPath string) error {
	entries, err := os.ReadDir(barrierPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &DataRootMountMissingError{TenantID: tenantID, BarrierPath: barrierPath}
		}
		return &DataRootMountMissingError{TenantID: tenantID, BarrierPath: barrierPath, Err: err}
	}
	if len(entries) == 0 {
		return &DataRootMountMissingError{TenantID: tenantID, BarrierPath: barrierPath}
	}
	return nil
}

// PoolBasePaths returns the local-filesystem base path of every candidate in
// every configured volume pool. These are the roots under which pool-allocated
// tenant DataRoots are realized ({poolBase}/{tenantID}); an operator-supplied
// dedicated DataRoot must not overlap them, or a dedicated disk could nest
// inside the pool and cause data-confusion. Returns an empty slice (nil error)
// when no volume pool is configured, so callers can range over the result
// unconditionally.
func (r *BarrierRegistry) PoolBasePaths(ctx context.Context) ([]string, error) {
	pool, _, _ := r.poolResources()
	if pool == nil {
		return nil, nil
	}
	pools, err := pool.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range pools {
		for _, cand := range p.Candidates {
			accessor, ok := cand.(common.PathAccessor)
			if !ok {
				continue
			}
			if lp := accessor.LocalPath(); lp != "" {
				out = append(out, lp)
			}
		}
	}
	return out, nil
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
		return v.(string), nil //nolint:errcheck // type assertion on a value this container only ever holds
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
// Value: JSON-encoded tenantRootRecord.
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
		if _, _, rerr := r.ReattachVolumeTenant(ctx, tenantID, rec.DataRoot, rec.AutoUnseal); rerr != nil {
			if errors.Is(rerr, ErrTenantAlreadyExists) {
				continue // registered concurrently
			}
			var mm *DataRootMountMissingError
			if errors.As(rerr, &mm) {
				// The dedicated disk/mount for this tenant is not present. Fail
				// THIS tenant's restore closed (never open a fresh empty
				// barrier) but do not crash the node; the reconciler retries on
				// the next pass, by which point the operator may have mounted
				// the volume.
				r.logger.Error("registry: restore: tenant barrier store absent; dedicated mount likely not present — refusing fresh empty barrier",
					slog.String("tenant", tenantID),
					slog.String("data_root", rec.DataRoot),
					slog.Any("error", rerr))
				continue
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
//
// RegisterVolumeTenant is the CREATE path: it never asserts the on-disk store
// already exists, because both a pool-allocated tenant (dataRoot == "") and a
// first-ever operator-pinned tenant (dataRoot supplied via CreateTenant's
// WithDataRoot, whose directory may not exist yet) are legitimately fresh. The
// boot RE-ATTACH path must instead REQUIRE the store to pre-exist; that is
// ReattachVolumeTenant.
func (r *BarrierRegistry) RegisterVolumeTenant(ctx context.Context, tenantID, dataRoot string, autoUnseal bool) (string, *TenantBarrier, error) {
	return r.registerVolumeTenant(ctx, tenantID, dataRoot, autoUnseal, false)
}

// ReattachVolumeTenant re-registers an EXISTING IsolationVolume / IsolationRaft
// tenant barrier at a persisted DataRoot during boot. It is identical to
// RegisterVolumeTenant except that it REQUIRES the tenant's on-disk barrier
// store to already be present: the DataRoot record was written on a prior run,
// so {dataRoot}/barrier MUST exist. When it is absent — typically because the
// operator's dedicated disk/mount is not mounted at boot — it returns
// DataRootMountMissingError instead of letting perTenantReg.Open create a
// fresh, empty barrier on the underlying root filesystem (a fail-open
// availability and data-confusion hazard). dataRoot must be non-empty.
func (r *BarrierRegistry) ReattachVolumeTenant(ctx context.Context, tenantID, dataRoot string, autoUnseal bool) (string, *TenantBarrier, error) {
	return r.registerVolumeTenant(ctx, tenantID, dataRoot, autoUnseal, true)
}

// registerVolumeTenant is the shared implementation of RegisterVolumeTenant
// (mustExist=false) and ReattachVolumeTenant (mustExist=true). When mustExist
// is true the tenant's persisted barrier store must already be present on disk
// before it is opened; see ReattachVolumeTenant.
func (r *BarrierRegistry) registerVolumeTenant(ctx context.Context, tenantID, dataRoot string, autoUnseal, mustExist bool) (string, *TenantBarrier, error) {
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

	// Refuse while the system barrier is sealed, rather than proceeding to
	// pick a pool slot and open a Pebble store for it. refuseClaimedRoot
	// below cannot even perform its DataRoot-overlap check in that state --
	// it can't read the tenant-root records it compares against, so it
	// treats "sealed" the same as "nothing is claimed" and returns nil --
	// so without this check a sealed system barrier silently skipped that
	// guard instead of refusing the call that needs it.
	// TenantService.CreateTenant already fails closed here (its tombstone
	// check reads through the system barrier itself), so this only matters
	// for a caller on this registry directly, bypassing TenantService --
	// which is exactly the case that must fail closed too. Checked after the
	// wiring preconditions above (empty id, no pool, no per-tenant registry)
	// so a misconfigured-but-unsealed call still reports ITS OWN error.
	if r.system == nil || r.system.IsSealed() {
		return "", nil, ErrSealed
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

	// A CREATE must not take a directory another tenant's tenant-root record
	// still names. That record may belong to a deleted tenant this node has
	// not reclaimed yet (it was down during the delete); the deleted-tenant
	// reconcile would then read the record and remove the directory -- now
	// the new tenant's. Refusing here closes that window from the other side.
	if !mustExist {
		if err := r.refuseClaimedRoot(ctx, tenantID, dataRoot); err != nil {
			return "", nil, err
		}
	}

	barrierPath := filepath.Join(dataRoot, "barrier")
	// Re-attach must not silently mint a fresh barrier when the dedicated mount
	// is absent: the store was created on a prior run, so it MUST already be
	// present. Assert BEFORE perTenantReg.Open (which would otherwise create an
	// empty Pebble at the missing path).
	if mustExist {
		if err := assertTenantStorePresent(tenantID, barrierPath); err != nil {
			return "", nil, err
		}
	}
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

// refuseClaimedRoot returns *DataRootClaimedError when a tenant-root record
// for any OTHER tenant names a directory that is, contains or lies inside
// dataRoot. With the system barrier sealed there are no readable records and
// nothing to refuse on.
func (r *BarrierRegistry) refuseClaimedRoot(ctx context.Context, tenantID, dataRoot string) error {
	if r.system == nil || r.system.IsSealed() {
		return nil
	}
	keys, err := r.system.List(ctx, tenantRootStorePrefix)
	if err != nil {
		return &ClaimedRootCheckError{TenantID: tenantID, Err: err}
	}
	for _, key := range keys {
		other := strings.TrimPrefix(key, tenantRootStorePrefix)
		if other == tenantID || other == "" {
			continue
		}
		rec, found, lerr := r.loadTenantRoot(ctx, other)
		if lerr != nil {
			return &ClaimedRootCheckError{TenantID: tenantID, Err: lerr}
		}
		if found && rec.DataRoot != "" && validation.PathsOverlap(filepath.Clean(rec.DataRoot), filepath.Clean(dataRoot)) {
			return &DataRootClaimedError{TenantID: tenantID, DataRoot: dataRoot, ClaimedBy: other}
		}
	}
	return nil
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
// Shamir section so InitializeShamir and the holder quorum can drive it.
//
// threshold must be >= 2 and <= total. When ttl is zero the barrier falls back
// to the default quorum share TTL. The strategy splits the tenant's root key
// under unseal-key indirection; the shares are sealed to the tenant's holders
// by InitializeTenantShamir and are never persisted.
func (r *BarrierRegistry) RegisterTenantPooledShamir(
	ctx context.Context,
	tenantID string,
	threshold, total int,
	ttl time.Duration,
) (*TenantBarrier, error) {
	strategy, err := NewShamirStrategy(threshold, total)
	if err != nil {
		return nil, err
	}
	cfg := tenantBarrierConfig(tenantID)
	cfg.Shamir = &ShamirConfig{
		Threshold:   threshold,
		TotalShares: total,
		QuorumTTL:   ttl,
	}
	return r.RegisterTenant(ctx, tenantID, nil, cfg, strategy)
}

// InitializeTenantShamir initializes the tenant barrier identified by tenantID
// under Shamir custody and seals the unseal key's shares to the tenant's
// enrolled share holders via the barrier's CeremonyDistributor. Returns
// metadata only. The tenant must already be registered via
// RegisterTenantPooledShamir.
//
// It is the barrier-level initialization and does not serialize holder
// registrations or unenrolments against it: the tenant service initializes
// through the tenant's holder authority (ceremony.ScopedCeremonyService
// InitializeShamir), which does.
func (r *BarrierRegistry) InitializeTenantShamir(ctx context.Context, tenantID string, policy FactorPolicy) (*ShamirInitParams, error) {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		return nil, err
	}
	return tb.InitializeShamir(ctx, policy)
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
	return val.(*TenantBarrier), nil //nolint:errcheck // type assertion on a value this container only ever holds
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
	return val.(*TenantBarrier).Close() //nolint:errcheck // type assertion on a value this container only ever holds
}

// UnregisterTenant is a convenience alias for RemoveTenant.
func (r *BarrierRegistry) UnregisterTenant(tenantID string) error {
	return r.RemoveTenant(tenantID)
}

// DestroyTenant removes a tenant barrier AND every durable record that would
// bring it back.
//
// UnregisterTenant seals and forgets the in-memory barrier, which is the
// right operation for a node shutting a tenant down. It is NOT the right
// operation for a tenant that has been deleted: the DataRoot record under
// "__registry:tenant_root/" and the volume-pool slot assignment survive it,
// and RestoreVolumeTenants reads exactly those records at boot -- so a
// deleted volume or raft tenant's barrier is re-registered on the next
// restart, at the same DataRoot, over the same on-disk store (go-qrdb#413).
//
// It also removes the tenant's dedicated DataRoot from disk. That directory
// holds the tenant's own Raft state machines and its barrier store; nothing
// names it once the records above are gone, so leaving it is a permanent
// filesystem leak of the deleted tenant's (now key-less) ciphertext.
//
// The steps run in the order that survives a failure at any point: release
// the in-memory barrier and close the store, remove the bytes, and only then
// drop the records that name them. A failed removal therefore leaves the
// record in place, so the next call knows where the directory is and finishes
// the job; dropping the records first would orphan an operator-supplied
// DataRoot that nothing names any more. DestroyTenant is idempotent and an
// already-destroyed tenant returns ErrTenantNotFound only when it was not
// registered here at all.
//
// A tenant that is registered but has no DataRoot (IsolationBarrier in
// keyspace mode, or a memory-backed test tenant) skips the filesystem step.
func (r *BarrierRegistry) DestroyTenant(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return ErrEmptyTenantID
	}

	dataRoot, hasRoot := r.tenantDataRoots.Load(tenantID)
	if !hasRoot {
		// Not registered on this node in this process (a follower, or a
		// restart that has not re-attached yet). The persisted record is
		// still authoritative for where the data is.
		if rec, found, err := r.loadTenantRoot(ctx, tenantID); err == nil && found {
			dataRoot, hasRoot = rec.DataRoot, rec.DataRoot != ""
		}
	}

	removeErr := r.ReleaseTenant(ctx, tenantID)
	if removeErr != nil && !errors.Is(removeErr, ErrTenantNotFound) {
		return removeErr
	}

	if hasRoot {
		root, _ := dataRoot.(string)
		if root != "" {
			if err := r.RemoveDataRoot(root); err != nil {
				return &TenantDataRootRemoveError{TenantID: tenantID, DataRoot: root, Err: err}
			}
			if r.logger != nil {
				r.logger.Info("registry: destroyed tenant data root",
					slog.String("tenant", tenantID),
					slog.String("data_root", root))
			}
		}
	}

	if err := r.DropTenantRecords(ctx, tenantID); err != nil {
		return err
	}
	return removeErr
}

// ReleaseTenant lets go of everything this process holds for tenantID without
// touching anything durable: it seals and forgets the in-memory barrier and
// closes the per-tenant store, so the directory under it can be removed
// without unlinking files from under an open handle.
//
// It is the first of the three steps DestroyTenant runs, exposed for a caller
// that must decide path by path which directories are safe to remove -- the
// deleted-tenant reconcile. It returns ErrTenantNotFound when the tenant was
// not registered in memory, having still closed any per-tenant store.
func (r *BarrierRegistry) ReleaseTenant(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return ErrEmptyTenantID
	}
	removeErr := r.RemoveTenant(tenantID)
	if removeErr != nil && !errors.Is(removeErr, ErrTenantNotFound) {
		return removeErr
	}
	r.tenantDataRoots.Delete(tenantID)
	if _, _, perTenantReg := r.poolResources(); perTenantReg != nil {
		if err := perTenantReg.Close(ctx, tenantID); err != nil {
			return &TenantStoreCloseError{TenantID: tenantID, Err: err}
		}
	}
	return removeErr
}

// DropTenantRecords deletes the two durable records the boot path restores a
// tenant barrier from: the tenant-root record naming its DataRoot and the
// volume-pool slot assignment. A missing record is success; a live failure is
// not, because leaving either behind resurrects the barrier at the next
// restart.
//
// Run it LAST, after the directories the records name have been removed: the
// record is how a retried reclamation finds a directory whose removal failed.
func (r *BarrierRegistry) DropTenantRecords(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return ErrEmptyTenantID
	}
	if r.system != nil && !r.system.IsSealed() {
		if err := r.system.Delete(ctx, tenantRootStorePrefix+tenantID); err != nil &&
			!errors.Is(err, ErrSecretNotFound) {
			return &TenantRecordDeleteError{TenantID: tenantID, Record: "tenant-root", Err: err}
		}
	}
	if slotStore := r.replicatedSlotStore(); slotStore != nil {
		if err := slotStore.Delete(ctx, slotAssignmentPrefix+tenantID); err != nil &&
			!errors.Is(err, ErrSecretNotFound) {
			return &TenantRecordDeleteError{TenantID: tenantID, Record: "slot-assignment", Err: err}
		}
	}
	return nil
}

// TenantDataRootCandidates returns every local directory that holds, or would
// hold, tenantID's data on THIS node: the DataRoot it is registered at in this
// process, the DataRoot its local tenant-root record names, and
// {poolBase}/{tenantID} under every local volume-pool base. None is checked
// for existence.
//
// Paths are returned AS RECORDED -- never cleaned -- and de-duplicated on
// their cleaned form. That is deliberate: a recorded "/srv/qrdb/x/.." cleans
// to "/srv/qrdb", a different and existing directory, so a caller deciding
// what to remove must validate the path it was given before it cleans
// anything, and it can only do that if it is given the raw path.
//
// It reads, and changes nothing. A caller deciding what to remove must weigh
// each path itself -- in particular a recorded DataRoot is whatever was
// recorded, and an operator-supplied one can later be reused by another
// tenant.
func (r *BarrierRegistry) TenantDataRootCandidates(ctx context.Context, tenantID string) ([]string, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	seen := make(map[string]struct{})
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		key := filepath.Clean(p)
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}
	if v, ok := r.tenantDataRoots.Load(tenantID); ok {
		root, _ := v.(string)
		add(root)
	}
	rec, found, err := r.loadTenantRoot(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if found {
		add(rec.DataRoot)
	}
	bases, err := r.PoolBasePaths(ctx)
	if err != nil {
		return nil, err
	}
	for _, base := range bases {
		if p, cerr := containedTenantPath(base, tenantID); cerr == nil {
			add(p)
		}
	}
	return out, nil
}

// HasTenantState reports whether this node still holds ANY registry state
// for tenantID: a barrier registered in memory, a tenant-root record, or a
// volume-pool slot assignment. A retired id with none of these and no
// directory on disk has been fully reclaimed here.
func (r *BarrierRegistry) HasTenantState(ctx context.Context, tenantID string) (bool, error) {
	if tenantID == "" {
		return false, ErrEmptyTenantID
	}
	if _, ok := r.tenants.Load(tenantID); ok {
		return true, nil
	}
	if r.system != nil && !r.system.IsSealed() {
		if _, err := r.system.Get(ctx, tenantRootStorePrefix+tenantID); err == nil {
			return true, nil
		} else if !errors.Is(err, ErrSecretNotFound) {
			return false, err
		}
	}
	if slotStore := r.replicatedSlotStore(); slotStore != nil {
		if _, err := slotStore.Get(ctx, slotAssignmentPrefix+tenantID); err == nil {
			return true, nil
		} else if !errors.Is(err, ErrSecretNotFound) {
			return false, err
		}
	}
	return false, nil
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

// InitializeTenantWithStrategy initializes the tenant barrier identified by
// tenantID using EXACTLY the supplied strategy, bypassing the barrier's
// best-strategy preference selection (see
// StorageBarrier.InitializeWithStrategy). The tenant must already be
// registered.
func (r *BarrierRegistry) InitializeTenantWithStrategy(ctx context.Context, tenantID string, strategy SealingStrategy, creds Credentials) error {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		return err
	}
	return tb.Barrier().InitializeWithStrategy(ctx, strategy, creds)
}

// UnsealTenant unseals the tenant barrier identified by tenantID using the
// supplied credentials. The tenant must already be registered and
// initialized.
//
// The unseal runs under sealMu, ordered against a whole-node seal: one that
// succeeded happened before the node seal, which then records the tenant as
// its own to bring back; one that runs after it finds the system barrier
// sealed.
func (r *BarrierRegistry) UnsealTenant(ctx context.Context, tenantID string, creds Credentials) error {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		return err
	}
	r.sealMu.Lock()
	defer r.sealMu.Unlock()
	if err := tb.Unseal(ctx, creds); err != nil {
		return err
	}
	delete(r.nodeSealed, tenantID)
	return nil
}

// SubmitTenantHolderShare counts one authenticated holder's share toward the
// tenant barrier's round (StorageBarrier.SubmitHolderShareWith); a completed
// seal round runs seal. An unseal submission runs under sealMu, ordered
// against a whole-node seal as UnsealTenant is, and an unseal it completes
// clears the tenant's pending node unseal. A seal submission takes no lock:
// its seal -- SealTenant, directly or through a cluster seal's enforcement --
// takes sealMu itself.
func (r *BarrierRegistry) SubmitTenantHolderShare(ctx context.Context, tenantID string, sub *HolderSubmission, seal func() error) (*QuorumOutcome, error) {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		if sub != nil {
			secure.Wipe(sub.Share)
		}
		return nil, err
	}
	if sub == nil || sub.Op != OpUnseal {
		return tb.Barrier().SubmitHolderShareWith(ctx, sub, seal)
	}
	r.sealMu.Lock()
	defer r.sealMu.Unlock()
	out, err := tb.Barrier().SubmitHolderShareWith(ctx, sub, seal)
	if err == nil && out.Complete {
		delete(r.nodeSealed, tenantID)
	}
	return out, err
}

// SealTenant seals the tenant barrier identified by tenantID, releasing its
// in-memory key material. The tenant must already be registered.
func (r *BarrierRegistry) SealTenant(tenantID string) error {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		return err
	}
	// An explicit seal -- the tenant's own admin, or its cluster record --
	// is not the node seal's to undo: the node unseal leaves it sealed. The
	// bookkeeping and the seal are one step under sealMu, so a node seal
	// cannot find the tenant still unsealed in between and record it as its
	// own.
	r.sealMu.Lock()
	defer r.sealMu.Unlock()
	delete(r.nodeSealed, tenantID)
	sealTenantHook(tenantID)
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
		ids = append(ids, key.(string)) //nolint:errcheck // type assertion on a value this container only ever holds
		return true
	})
	return ids
}

// unsealSystemManagedTenants unseals, when the system barrier unseals, the
// system-managed (auto-unseal) tenants the last whole-node seal sealed -- and
// only those: a tenant sealed for its own reasons, before the node seal or
// explicitly since (SealTenant), stays sealed, as does one the unseal gate
// refuses (its cluster record is sealed or unreadable). Each is unsealed
// credential-free, its root key being wrapped by the system barrier.
//
// It runs on a context detached from the unseal request: the system unseal
// has succeeded, and a caller that stops waiting must not leave the node's
// own tenants half restored. A tenant that fails to unseal stays pending
// with its error (PendingTenantUnseals) and is retried at the next system
// unseal; the tenant's own unseal recovers it at any time.
func (r *BarrierRegistry) unsealSystemManagedTenants(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	r.sealMu.Lock()
	defer r.sealMu.Unlock()
	for tenantID := range r.nodeSealed {
		tb, err := r.GetTenant(tenantID)
		if err != nil {
			delete(r.nodeSealed, tenantID)
			continue
		}
		if r.unsealGate != nil && r.unsealGate(tenantID) {
			r.logger.Info("registry: system-managed tenant stays sealed: its cluster seal record refuses it",
				slog.String("tenant", tenantID))
			delete(r.nodeSealed, tenantID)
			continue
		}
		if !tb.IsSealed() {
			delete(r.nodeSealed, tenantID)
			continue
		}
		if err := tb.Unseal(ctx, Credentials{}); err != nil && !errors.Is(err, ErrAlreadyUnsealed) {
			r.nodeSealed[tenantID] = err
			r.logger.Error("registry: system-managed tenant did not unseal with the system barrier; it stays sealed and is retried at the next system unseal",
				slog.String("tenant", tenantID), slog.Any("error", err))
			continue
		}
		delete(r.nodeSealed, tenantID)
	}
}

// sealTenantHook and sealAllTenantHook run inside SealTenant and SealAll,
// between the registry's bookkeeping for a tenant and that tenant's seal,
// still under sealMu. They are no-ops in production -- direct assignments,
// not conditionals -- and exist so a test can land a concurrent node seal or
// tenant unseal exactly where the bookkeeping and the barrier's state could
// otherwise disagree.
var (
	sealTenantHook    = func(string) {}
	sealAllTenantHook = func(string) {}
)

// SealAll seals every registered tenant barrier and then the system
// barrier. It accumulates errors so a single failing tenant does not
// prevent the remaining barriers from being sealed; the first error
// encountered is returned.
func (r *BarrierRegistry) SealAll() error {
	r.sealMu.Lock()
	defer r.sealMu.Unlock()
	return errors.Join(r.sealTenantsLocked(), r.system.Seal())
}

// SealTenants seals every registered tenant barrier, leaving the system
// barrier to the caller: the half of SealAll a registry that shares another
// registry's system barrier -- the database barrier manager's -- contributes
// to a whole-node seal. The system-managed tenants it moves from unsealed to
// sealed are recorded, under sealMu, as the node seal's to bring back when
// the system barrier unseals (unsealSystemManagedTenants); a tenant already
// sealed stays the way it was sealed. It accumulates errors so a single
// failing tenant does not prevent the rest from being sealed.
func (r *BarrierRegistry) SealTenants() error {
	r.sealMu.Lock()
	defer r.sealMu.Unlock()
	return r.sealTenantsLocked()
}

// sealTenantsLocked is SealTenants; sealMu is held.
func (r *BarrierRegistry) sealTenantsLocked() error {
	var errs []error
	r.tenants.Range(func(key, val any) bool {
		tb := val.(*TenantBarrier) //nolint:errcheck // type assertion on a value this container only ever holds
		wasUnsealed := !tb.IsSealed()
		systemManaged := wasUnsealed && tb.Status().Strategy == StrategySystemBarrier
		if tenantID, ok := key.(string); ok {
			sealAllTenantHook(tenantID)
		}
		if err := tb.Seal(); err != nil {
			errs = append(errs, err)
			return true
		}
		if tenantID, ok := key.(string); ok && systemManaged {
			r.nodeSealed[tenantID] = nil
		}
		return true
	})
	return errors.Join(errs...)
}

// Close seals and closes every registered tenant barrier and then closes
// the system barrier, releasing all underlying storage handles. It
// accumulates errors so a single failing barrier does not prevent the
// remaining barriers from being closed.
func (r *BarrierRegistry) Close() error {
	var errs []error
	r.tenants.Range(func(_, val any) bool {
		if err := val.(*TenantBarrier).Close(); err != nil { //nolint:errcheck // type assertion on a value this container only ever holds
			errs = append(errs, err)
		}
		return true
	})
	if err := r.system.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
