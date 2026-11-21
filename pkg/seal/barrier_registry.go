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
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// BarrierRegistry manages the system barrier and per-tenant barriers.
// It provides a centralized registry for creating, initializing, and
// looking up tenant-specific barriers.
//
// When configured with WithVolumeAllocator, WithVolumeResolver and
// WithPerTenantRegistry, the registry can satisfy RegisterTenant calls
// that pass a nil backend by resolving tenantID's already-reserved
// catalog Assignment to a local path, opening a per-tenant Pebble there,
// and wrapping it as the per-tenant StorageBackend. This is the
// integration point that gives barrier-mode tenants their own
// independent Pebble instances.
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

	// unsealedMu guards unsealedObservers and orders an observer's
	// registration against a tenant barrier's storing, so every tenant
	// barrier the registry holds carries every observer (OnTenantUnsealed).
	unsealedMu        sync.Mutex
	unsealedObservers []func(ctx context.Context, tenantID string)

	// poolMu guards perTenantReg/volumeAllocator/volumeResolver, which may be
	// attached either at construction (Options) or post-hoc via Attach.
	poolMu       sync.RWMutex
	perTenantReg pebblestore.PerTenantRegistry

	// remover is the filesystem tenant directories are reclaimed through; nil
	// means the operating system. See WithDirectoryRemover.
	remover DirectoryRemover

	// migrationRecoverer, when non-nil, reconciles an interrupted encryption
	// migration recorded in a tenant's journal BEFORE the tenant barrier is
	// built (see MigrationRecoverer). Guarded by poolMu like the other
	// boot-wired composition fields.
	migrationRecoverer MigrationRecoverer

	// startupReadRetry, when non-nil, is what RestoreVolumeTenants runs its
	// system-shard reads through (see StartupReadRetry). Guarded by poolMu.
	startupReadRetry StartupReadRetry

	// auditTrail is the compliance trail every tenant barrier records on
	// (WithAuditTrail). Guarded by poolMu.
	auditTrail audit.AuditLogger

	// volumeAllocator resolves (Reserve/Resolve/Release) a tenant's named
	// volume pool and tenant-relative path -- the catalog built over
	// pkg/volume (see WithVolumeAllocator). It replaces the node-local
	// round-robin pool/slot-assignment machinery above: the catalog already
	// gives every node the SAME answer for a tenant, so there is no slot to
	// translate. Guarded by poolMu like the rest of the boot-wired fields.
	volumeAllocator volume.Allocator

	// volumeResolver resolves a pool name to THIS node's own opened volume
	// root directory (see WithVolumeResolver). pkg/seal does not open
	// volumes itself -- the composition layer does, from node configuration
	// -- so this is injected exactly as MigrationRecoverer is.
	volumeResolver VolumeResolver
}

// VolumeResolver resolves a pool name to this node's own opened volume
// root directory (pkg/volume.Volume.Root()). It is injected via
// WithVolumeResolver because pkg/seal cannot open volumes itself: the
// composition layer does, from node configuration, and tracks the
// Lockable ones in a VolumeLifecycle beside it (decision D6).
//
// Returns *volume.PoolUnavailableError-shaped errors for a pool this node
// does not map, mapped but failed to open, or mapped and open but
// currently locked/unavailable (a Lockable volume before its first
// unlock, after a failed unlock, or after a re-seal); BarrierRegistry
// treats all three as "this tenant's pool is unavailable on this node"
// and refuses only that tenant, never the whole node. Root must never
// fall through to the bare mount point for a volume it cannot confirm is
// unlocked.
type VolumeResolver interface {
	Root(ctx context.Context, pool string) (string, error)

	// Pools returns the name of every pool this node has open, for the
	// deleted-tenant reconcile's "never remove a path under a pool root"
	// guard (replaces the old PoolBasePaths).
	Pools() []string
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

// StartupReadRetry runs read, one boot-time step that reads the system
// shard, until it succeeds or fails with an error the composer does not
// recognize as the system shard settling (an election in progress, or a new
// leader's ReadIndex warm-up), within a bound of its own; op names the step.
// It returns read's last error.
//
// The concrete implementation lives at the server layer (pkg/server owns the
// classification of a settling system shard) and is injected here via
// WithStartupReadRetry because pkg/seal cannot import pkg/server.
type StartupReadRetry func(ctx context.Context, op string, read func(context.Context) error) error

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

// WithStartupReadRetry attaches the retry RestoreVolumeTenants runs each of
// its system-shard reads through: the catalog listing, each tenant's barrier
// posture, and each tenant's re-attach, which resolves the tenant's
// Assignment. A restart brings every node up together, so the restore can
// run while the system shard has no leader; without a retry one such read
// fails the restore -- or one tenant's -- for the life of the process.
//
// Passing nil is a no-op. When never configured, each read is attempted once.
func WithStartupReadRetry(retry StartupReadRetry) Option {
	return func(r *BarrierRegistry) {
		r.poolMu.Lock()
		defer r.poolMu.Unlock()
		if retry != nil {
			r.startupReadRetry = retry
		}
	}
}

// WithVolumeAllocator attaches the catalog (pkg/volume.Catalog, typed as
// its narrower volume.Allocator seam) RegisterTenant's nil-backend flow and
// RegisterVolumeTenant/ReattachVolumeTenant resolve a tenant's pool and
// tenant-relative path through. Passing nil is a no-op.
func WithVolumeAllocator(allocator volume.Allocator) Option {
	return func(r *BarrierRegistry) {
		r.poolMu.Lock()
		defer r.poolMu.Unlock()
		if allocator != nil {
			r.volumeAllocator = allocator
		}
	}
}

// WithVolumeResolver attaches the resolver that maps a pool name to this
// node's own opened volume root directory (typically *volume.OpenVolumes).
// Passing nil is a no-op. Both WithVolumeAllocator and WithVolumeResolver
// must be set before a nil-backend RegisterTenant call, or
// RegisterVolumeTenant/ReattachVolumeTenant, can succeed.
func WithVolumeResolver(resolver VolumeResolver) Option {
	return func(r *BarrierRegistry) {
		r.poolMu.Lock()
		defer r.poolMu.Unlock()
		if resolver != nil {
			r.volumeResolver = resolver
		}
	}
}

// volumeResources returns the configured volume allocator and resolver
// under a read lock.
func (r *BarrierRegistry) volumeResources() (volume.Allocator, VolumeResolver) {
	r.poolMu.RLock()
	defer r.poolMu.RUnlock()
	return r.volumeAllocator, r.volumeResolver
}

// HasVolumePool reports whether this registry has BOTH a volume.Allocator
// and a VolumeResolver attached -- the precondition every IsolationVolume /
// IsolationRaft pool-resolution path (RequireInsideLocalPool and everything
// built on it) requires. Composition layers (pkg/server.TenantInfraConfig,
// cmd/qrdbd, sdk/go/node) use it as the single presence gate for whether to
// build the volume/raft tenant host registries at all, replacing the old
// "is a *pool.VolumePoolSubsystem configured" check now that there is no
// such subsystem.
func (r *BarrierRegistry) HasVolumePool() bool {
	allocator, resolver := r.volumeResources()
	return allocator != nil && resolver != nil
}

// VolumeAllocator returns the volume.Allocator attached to this registry
// (seal.WithVolumeAllocator / Attach), or nil when none is configured. The
// TenantService's own volume-pool reservation (reserveAndRegisterVolumeTenant)
// needs the SAME allocator the registry resolves tenant placement through,
// so a composition layer that builds a TenantService over this registry
// (pkg/server/lifecycle.BuildServices) wires this straight into
// tenant.WithVolumeAllocator rather than threading a second copy of the
// allocator through its own Infra field.
func (r *BarrierRegistry) VolumeAllocator() volume.Allocator {
	allocator, _ := r.volumeResources()
	return allocator
}

// VolumeService returns the attached allocator's volume.Service view (pool
// management: CreatePool, ListPools, GetPool, UpdatePool, DeletePool,
// ListAssignments) when the concrete value attached via WithVolumeAllocator
// also implements it -- true for *volume.Catalog, the only production
// implementation, which satisfies both the narrow Allocator seam this
// registry resolves tenant placement through and the full management
// surface docs/volume-pools/api.md describes. Composition layers
// (cmd/qrdbd, sdk/go/node) use this to wire every transport's volume-pool
// management endpoints from the SAME catalog instance, without threading a
// second copy of it through their own boot-time variables. ok is false
// when no allocator is attached, or the attached one does not also
// implement Service (a narrower test double, for example).
func (r *BarrierRegistry) VolumeService() (svc volume.Service, ok bool) {
	allocator, _ := r.volumeResources()
	if allocator == nil {
		return nil, false
	}
	svc, ok = allocator.(volume.Service)
	return svc, ok
}

// RequireInsideLocalPool resolves tenantID's assignment through allocator,
// resolves its Assignment.Pool to this node's own mount through resolver,
// and joins Assignment.Path onto it, verifying the result is strictly
// inside that root (invariant 2). This is the renamed, catalog-backed
// successor to the pre-catalog recovery fix's unexported
// containedTenantPath: it is now the single place every tenant-directory
// operation -- registration, removal, reconcile -- resolves a tenant's
// local directory, replacing the node-local round-robin pool/slot
// machinery entirely. The catalog already gives every node the SAME
// Assignment (Reserve is one Raft transaction), so there is nothing left
// to translate per node beyond its own mount for the named pool.
//
// Symbolic links in the pool's root are rejected once, at volume-open
// time, by the Provider that built it (pkg/volume's "dir" provider calls
// validation.AbsoluteCanonicalPath); this function does not re-resolve
// the ROOT on every call, since Volume.Root() does not change for the
// lifetime of an open Volume. The TENANT directory under that root is a
// different matter: it is created (and can be replaced) long after the
// pool was opened, so every call also Lstats the joined tenant path and,
// when it exists, requires it to be a real directory whose fully
// symlink-resolved location is EXACTLY resolvedRoot/<Assignment.Path> --
// never anywhere else. Without this, an operator (or a compromised
// writer) who plants a symlink at the tenant's path could redirect
// create/open/reattach/delete outside the pool entirely, or onto a
// sibling tenant's directory (deleting tenant A would actually delete
// tenant B). A tenant directory that does not exist yet -- the common
// case on first create -- has nothing to resolve and is accepted as-is.
//
// Returns *volume.AssignmentNotFoundError when tenantID has no assignment,
// whatever resolver.Root returns (typically *volume.PoolUnavailableError)
// when this node cannot resolve the assignment's pool, and
// *TenantPathIsSymlinkError when the tenant directory itself is, or
// resolves through, a symlink.
func (r *BarrierRegistry) RequireInsideLocalPool(ctx context.Context, tenantID string) (string, error) {
	allocator, resolver := r.volumeResources()
	if allocator == nil || resolver == nil {
		return "", ErrPoolNotConfigured
	}
	assignment, err := allocator.Resolve(ctx, tenantID)
	if err != nil {
		return "", err
	}
	return placementRoot(ctx, resolver, tenantID, assignment.Pool, assignment.Path)
}

// PlacementDataRoot returns this node's directory for tenantID at path on
// pool, with every containment check RequireInsideLocalPool makes, for a
// placement the catalog no longer holds: once one node's teardown released
// a retired tenant's Assignment, a node that missed the teardown still has
// its own directory there, and the tombstone's pool is what names it.
// Returns ErrPoolNotConfigured when this node has no volume pools.
func (r *BarrierRegistry) PlacementDataRoot(ctx context.Context, tenantID, pool, path string) (string, error) {
	if tenantID == "" {
		return "", ErrEmptyTenantID
	}
	_, resolver := r.volumeResources()
	if resolver == nil {
		return "", ErrPoolNotConfigured
	}
	return placementRoot(ctx, resolver, tenantID, pool, path)
}

// placementRoot joins path onto pool's root on this node and refuses any
// result that is not exactly one real directory component below it.
func placementRoot(ctx context.Context, resolver VolumeResolver, tenantID, pool, path string) (string, error) {
	root, err := resolver.Root(ctx, pool)
	if err != nil {
		return "", err
	}

	joined := filepath.Join(root, path)
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
	if err := requireNotSymlinked(root, joined, path, tenantID); err != nil {
		return "", err
	}
	return joined, nil
}

// requireNotSymlinked refuses joined (root's assignment.Path child) when it
// is a symlink, or when its fully symlink-resolved location is not EXACTLY
// resolvedRoot/<assignmentPath>. A path that does not exist yet has
// nothing to resolve and is accepted.
func requireNotSymlinked(root, joined, assignmentPath, tenantID string) error {
	fi, statErr := os.Lstat(joined)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return nil
		}
		return &TenantPathIsSymlinkError{TenantID: tenantID, Path: joined, Err: statErr}
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return &TenantPathIsSymlinkError{TenantID: tenantID, Path: joined}
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return &TenantPathIsSymlinkError{TenantID: tenantID, Path: joined, Err: err}
	}
	resolvedJoined, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return &TenantPathIsSymlinkError{TenantID: tenantID, Path: joined, Err: err}
	}
	if resolvedJoined != filepath.Join(resolvedRoot, assignmentPath) {
		return &TenantPathIsSymlinkError{TenantID: tenantID, Path: joined}
	}
	return nil
}

// isUnresolvableAssignment reports whether err means "this tenant has no
// directory to remove on this node" rather than a real failure: either no
// Assignment exists at all (*volume.AssignmentNotFoundError), or the
// Assignment is a D9 interim-shaped record ({pool, slot}, no Path field)
// that Resolve refuses per decision D9 (*volume.InvalidAssignmentPathError).
// Both cases are "nothing resolvable here," not "something is wrong" --
// callers that distinguish "no record at all" from "a record exists but
// is unusable" (HasTenantState) check the two types separately instead of
// using this helper.
func isUnresolvableAssignment(err error) bool {
	var notFound *volume.AssignmentNotFoundError
	if errors.As(err, &notFound) {
		return true
	}
	var invalidPath *volume.InvalidAssignmentPathError
	return errors.As(err, &invalidPath)
}

// PoolBasePaths returns the local-filesystem root of every pool this node
// has open, via the configured VolumeResolver. These are the roots under
// which pool-allocated tenant directories are realized
// ({poolRoot}/{tenantID}); the deleted-tenant reconcile's residue guard
// must never remove a path that is or contains one. Returns an empty
// slice (nil error) when no resolver is configured, so callers can range
// over the result unconditionally.
func (r *BarrierRegistry) PoolBasePaths(ctx context.Context) ([]string, error) {
	_, resolver := r.volumeResources()
	if resolver == nil {
		return nil, nil
	}
	names := resolver.Pools()
	out := make([]string, 0, len(names))
	for _, name := range names {
		root, err := resolver.Root(ctx, name)
		if err != nil {
			// A pool that was open a moment ago and failed since is reported
			// by health, not here; skip it rather than failing every other
			// pool's guard along with it.
			if r.logger != nil {
				r.logger.Warn("registry: pool base path unavailable", slog.String("pool", name), slog.Any("error", err))
			}
			continue
		}
		out = append(out, root)
	}
	return out, nil
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
//
// Every OnUnsealed and OnSealing observer registered on the replaced barrier
// moves to b: the registry's own system-managed tenant unseal, the shard
// pauser's resume and the volume lifecycle's lock and unlock all follow the
// barrier that now guards the node, and the replaced barrier keeps none, so a
// node sealed after the swap resumes its shards and unlocks its volumes when
// b unseals.
func (r *BarrierRegistry) SetSystemBarrier(b *StorageBarrier) {
	old := r.system
	r.system = b
	if b == nil || b == old {
		return
	}
	if old != nil {
		old.moveObserversTo(b)
		return
	}
	b.OnUnsealed(r.unsealSystemManagedTenants)
}

// ReplaceTenantBarrier swaps the registered barrier for an existing tenant,
// used by the tenant-scoped encryption-migration commit once that tenant's
// stores are re-encrypted. Returns ErrTenantNotFound if the tenant is not
// registered (a shared-barrier tenant has no dedicated barrier to replace).
func (r *BarrierRegistry) ReplaceTenantBarrier(tenantID string, tb *TenantBarrier) error {
	r.unsealedMu.Lock()
	defer r.unsealedMu.Unlock()
	old, ok := r.tenants.Load(tenantID)
	if !ok {
		return ErrTenantNotFound
	}
	r.tenants.Store(tenantID, tb)
	if old.(*TenantBarrier).Barrier() != tb.Barrier() { //nolint:errcheck // type assertion on a value this container only ever holds
		watchTenantUnsealed(tenantID, tb, r.unsealedObservers)
	}
	return nil
}

// OnTenantUnsealed registers fn to run, with the tenant's ID, each time a
// tenant barrier this registry holds is initialized or unsealed. It covers
// the tenants registered now and every tenant registered or replaced later,
// so a caller waiting on a tenant's barrier learns the moment it is usable
// rather than on its next poll. fn runs synchronously on the unsealing
// goroutine and must not block. A nil fn is ignored.
func (r *BarrierRegistry) OnTenantUnsealed(fn func(ctx context.Context, tenantID string)) {
	if fn == nil {
		return
	}
	r.unsealedMu.Lock()
	defer r.unsealedMu.Unlock()
	r.unsealedObservers = append(r.unsealedObservers, fn)
	observer := []func(context.Context, string){fn}
	r.tenants.Range(func(key, value any) bool {
		watchTenantUnsealed(key.(string), value.(*TenantBarrier), observer) //nolint:errcheck // type assertions on values this container only ever holds
		return true
	})
}

// watchTenantUnsealed attaches observers to tb's barrier, each called with
// tenantID. The caller holds the registry's unsealedMu.
func watchTenantUnsealed(tenantID string, tb *TenantBarrier, observers []func(context.Context, string)) {
	for _, fn := range observers {
		tb.Barrier().OnUnsealed(func(ctx context.Context) { fn(ctx, tenantID) })
	}
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
// When base is nil, the registry resolves tenantID's already-reserved
// catalog Assignment (see WithVolumeAllocator / WithVolumeResolver),
// opens or returns an existing per-tenant Pebble at the resolved path via
// the configured PerTenantRegistry (see WithPerTenantRegistry), and wraps
// the resulting Pebble as the per-tenant StorageBackend. This is the
// barrier-mode flow.
//
// Returns ErrTenantAlreadyExists if the tenant id is already registered,
// and ErrPoolNotConfigured / ErrPerTenantRegistryNotConfigured when the
// pooled flow is requested but the registry isn't wired for it.
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
		config.AuditLogger = NewComplianceAuditLogger(r.auditTrail, tenantid.System, TenantBarrierAuditName(tenantID))
		r.poolMu.RUnlock()
	}
	tb, err := NewTenantBarrier(tenantID, r.logger, base, config, strategies...)
	if err != nil {
		return nil, err
	}

	r.unsealedMu.Lock()
	defer r.unsealedMu.Unlock()
	if _, loaded := r.tenants.LoadOrStore(tenantID, tb); loaded {
		_ = tb.Close()
		return nil, ErrTenantAlreadyExists
	}
	watchTenantUnsealed(tenantID, tb, r.unsealedObservers)

	return tb, nil
}

// allocatePooledBackend resolves the local directory the catalog's
// Assignment names for tenantID -- this node's own mount for the
// Assignment's pool, joined with the Assignment's tenant-relative path --
// opens a per-tenant Pebble in its "barrier" subdirectory, and wraps it as a
// StorageBackend. For a
// tenant.Service-registered tenant the Assignment already exists:
// reserving it is that caller's job (pkg/tenant.CreateTenant, via
// volume.Allocator.Reserve) before RegisterTenant is ever called, exactly
// as architecture.md's Data flow section orders it -- entitlement and
// reservation precede registration. For the generic, pool-name-free
// registration primitives (RegisterBarrierTenant and friends, below)
// ensureGenericPoolAssignment reserves one on demand.
func (r *BarrierRegistry) allocatePooledBackend(ctx context.Context, tenantID string) (StorageBackend, error) {
	// Checked before ensureGenericPoolAssignment's Reserve below: a Reserve
	// that then can't be used because no per-tenant registry exists would
	// leak a capacity reservation for a registration that was always going
	// to fail.
	perTenantReg := r.perTenantRegistry()
	if perTenantReg == nil {
		return nil, ErrPerTenantRegistryNotConfigured
	}

	if err := r.ensureGenericPoolAssignment(ctx, tenantID); err != nil {
		return nil, err
	}

	dataRoot, err := r.RequireInsideLocalPool(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	// The same layout as a volume tenant's (registerVolumeTenant), so the
	// boot restore finds every tenant's barrier store in one place.
	barrierPath := filepath.Join(dataRoot, "barrier")
	storage, err := perTenantReg.Open(ctx, tenantID, barrierPath)
	if err != nil {
		return nil, err
	}
	if r.logger != nil {
		r.logger.Info("barrier-mode tenant resolved through its volume pool assignment",
			slog.String("tenant", tenantID),
			slog.String("barrier_path", barrierPath))
	}

	return NewStorageAdapter(storage), nil
}

// ensureGenericPoolAssignment reserves tenantID a path on this node's
// lexicographically-first local pool when it has no catalog Assignment
// yet. It exists for the pool-name-free registrations: RegisterBarrierTenant
// (an IsolationBarrier tenant names no pool and has no pool entitlement),
// and the generic primitives RegisterTenantPooledWithStrategy and
// RegisterTenantPooledShamir. The reservation it writes is capacity 0
// (untracked against the pool's capacity), drawing from whatever single
// pool the composer configured with no entitlement check -- these are
// trusted, in-process callers (pkg/tenant.CreateTenant for a barrier
// tenant, pkg/bootstrap), not raw tenant-create requests. A volume or raft
// tenant never reaches here with a missing Assignment, since
// pkg/tenant.CreateTenant has already Reserved one (with its own entitled
// pool and real quota) before registering; for it this is a no-op, because
// Resolve already succeeds.
func (r *BarrierRegistry) ensureGenericPoolAssignment(ctx context.Context, tenantID string) error {
	allocator, resolver := r.volumeResources()
	if allocator == nil || resolver == nil {
		return ErrPoolNotConfigured
	}
	if _, err := allocator.Resolve(ctx, tenantID); err == nil {
		return nil
	} else {
		var notFound *volume.AssignmentNotFoundError
		if !errors.As(err, &notFound) {
			return err
		}
	}
	pools := resolver.Pools()
	if len(pools) == 0 {
		return ErrPoolNotConfigured
	}
	_, err := allocator.Reserve(ctx, tenantID, pools[0], tenantID, 0)
	return err
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

// perTenantRegistry returns the configured per-tenant Pebble registry
// under a read lock. It is the shared accessor used by
// allocatePooledBackend and registerVolumeTenant.
func (r *BarrierRegistry) perTenantRegistry() pebblestore.PerTenantRegistry {
	r.poolMu.RLock()
	defer r.poolMu.RUnlock()
	return r.perTenantReg
}

// migrationRecovererHook returns the configured MigrationRecoverer (nil when
// none) under the same read lock that guards the other boot-wired fields.
func (r *BarrierRegistry) migrationRecovererHook() MigrationRecoverer {
	r.poolMu.RLock()
	defer r.poolMu.RUnlock()
	return r.migrationRecoverer
}

// startupRead runs read through the configured StartupReadRetry, or once when
// none is configured.
func (r *BarrierRegistry) startupRead(ctx context.Context, op string, read func(context.Context) error) error {
	r.poolMu.RLock()
	retry := r.startupReadRetry
	r.poolMu.RUnlock()
	if retry == nil {
		return read(ctx)
	}
	return retry(ctx, op, read)
}

// LocalTenantDataRoot returns the local volume-pool DataRoot for tenantID,
// resolved fresh through RequireInsideLocalPool on every call: the
// catalog's Assignment is already the durable, cluster-wide answer (no
// node-local Pick to avoid re-running), so there is nothing to cache.
//
// Returns *volume.AssignmentNotFoundError when the tenant has not been
// placed on a pool, and whatever RequireInsideLocalPool's resolution step
// returns (typically *volume.PoolUnavailableError) when this node cannot
// resolve the assignment's pool.
func (r *BarrierRegistry) LocalTenantDataRoot(ctx context.Context, tenantID string) (string, error) {
	return r.RequireInsideLocalPool(ctx, tenantID)
}

// tenantBarrierPostureStorePrefix is the key prefix under which a volume or
// raft tenant's barrier key-management posture is persisted in the system
// barrier store: the one bit about such a tenant pkg/seal still needs
// that nothing else in its reach can supply. WHERE the tenant lives comes
// entirely from the catalog now (volume.Allocator); this is the only
// state that survives the migration off the old tenantRootRecord, because
// AutoUnseal is a pkg/seal key-management concept, not a volume-pools
// location concept, and pkg/seal cannot read pkg/tenant's TenantInfo
// (pkg/tenant already imports pkg/seal, so the reverse would cycle).
//
// Key: tenantBarrierPostureStorePrefix + tenantID
// Value: JSON-encoded tenantBarrierPosture.
const tenantBarrierPostureStorePrefix = "__registry:tenant_posture/"

// tenantBarrierPosture is the persisted form of a volume/raft tenant's
// barrier key-management posture.
type tenantBarrierPosture struct {
	// AutoUnseal indicates whether the tenant uses the system barrier as its
	// unseal authority (true) or self-manages unseal credentials (false).
	AutoUnseal bool `json:"auto_unseal"`
}

// persistTenantBarrierPosture writes tenantID's AutoUnseal flag to the
// system barrier store. The system barrier must be unsealed; a sealed
// barrier is silently skipped (the session can still operate, but the
// entry will be missing on the next restart).
func (r *BarrierRegistry) persistTenantBarrierPosture(ctx context.Context, tenantID string, autoUnseal bool) error {
	if r.system == nil || r.system.IsSealed() {
		return nil
	}
	blob, err := json.Marshal(tenantBarrierPosture{AutoUnseal: autoUnseal})
	if err != nil {
		return err
	}
	return r.system.Put(ctx, tenantBarrierPostureStorePrefix+tenantID, blob)
}

// loadTenantBarrierPosture reads the persisted posture record for
// tenantID. Returns (false, false, nil) when the key is absent or the
// system barrier is sealed: the caller's fail-closed default (self-managed)
// applies either way.
func (r *BarrierRegistry) loadTenantBarrierPosture(ctx context.Context, tenantID string) (autoUnseal, found bool, err error) {
	if r.system == nil || r.system.IsSealed() {
		return false, false, nil
	}
	data, err := r.system.Get(ctx, tenantBarrierPostureStorePrefix+tenantID)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return false, false, nil
		}
		return false, false, err
	}
	var rec tenantBarrierPosture
	if uerr := json.Unmarshal(data, &rec); uerr != nil {
		// Corrupt entry: treat as absent so a fresh registration can overwrite,
		// and the fail-closed (self-managed) default applies in the meantime.
		return false, false, nil
	}
	return rec.AutoUnseal, true, nil
}

// RestoreVolumeTenants re-registers every IsolationVolume / IsolationRaft
// tenant barrier this node can resolve: every Assignment the catalog
// holds, for a pool this node maps, that is not yet in the in-memory
// registry. Each one is re-registered via ReattachVolumeTenant and, for
// system-managed (auto-unseal) tenants, unsealed credential-free.
//
// This is the boot-time complement to reattachRaftTenants: while
// reattachRaftTenants handles the node that was the cluster leader during
// the original tenant CREATE (it has TenantInfo in its local seal store),
// RestoreVolumeTenants handles FOLLOWER nodes that received key delivery
// but never stored TenantInfo. Their barrier key material is persisted
// on-disk under the pool; this method re-opens it.
//
// The method is idempotent: tenants already present in the registry are
// skipped, and an Assignment for a pool this node does not map is skipped
// quietly -- some other node serves it. Individual re-registration
// failures are logged and skipped rather than aborting.
//
// Nothing re-runs the restore later in the process, so each of its
// system-shard reads -- the catalog listing, a tenant's barrier posture, and
// the Assignment its re-attach resolves -- runs through the StartupReadRetry
// attached with WithStartupReadRetry: a restart brings every node up
// together, and a read made while the system shard elects a leader fails
// without one. A listing that still fails is returned; a tenant whose
// posture read still fails is restored self-managed (sealed), and one whose
// re-attach still fails is not restored.
func (r *BarrierRegistry) RestoreVolumeTenants(ctx context.Context) error {
	if r.system == nil || r.system.IsSealed() {
		return nil
	}
	allocator, resolver := r.volumeResources()
	if allocator == nil || resolver == nil {
		return nil
	}
	var assignments []volume.Assignment
	err := r.startupRead(ctx, "volume tenant restore: catalog list", func(actx context.Context) error {
		var lerr error
		assignments, lerr = allocator.ListAll(actx)
		return lerr
	})
	if err != nil {
		return err
	}
	for _, a := range assignments {
		tenantID := a.TenantID
		if _, rerr := r.GetTenant(tenantID); rerr == nil {
			continue // already registered (e.g. this node was the create leader)
		}
		if _, rerr := resolver.Root(ctx, a.Pool); rerr != nil {
			continue // this node does not map (or cannot open) a.Pool; another node serves it
		}

		var autoUnseal bool
		perr := r.startupRead(ctx, "volume tenant restore: barrier posture", func(actx context.Context) error {
			var lerr error
			autoUnseal, _, lerr = r.loadTenantBarrierPosture(actx, tenantID)
			return lerr
		})
		if perr != nil {
			r.logger.Warn("registry: restore: failed to read tenant barrier posture; defaulting to self-managed",
				slog.String("tenant", tenantID), slog.Any("error", perr))
		}

		// The re-attach opens the tenant's store and persists its posture, so
		// it keeps ctx rather than taking a read attempt's bound; it is retried
		// for the Assignment it resolves before anything is opened.
		rerr := r.startupRead(ctx, "volume tenant restore: re-attach", func(context.Context) error {
			_, _, aerr := r.ReattachVolumeTenant(ctx, tenantID, autoUnseal)
			return aerr
		})
		if rerr != nil {
			if errors.Is(rerr, ErrTenantAlreadyExists) {
				continue // registered concurrently
			}
			var mm *DataRootMountMissingError
			if errors.As(rerr, &mm) {
				// The dedicated disk/mount for this tenant is not present. Fail
				// THIS tenant's restore closed (never open a fresh empty
				// barrier) but do not crash the node; a restart after the
				// operator mounts the volume restores it.
				r.logger.Error("registry: restore: tenant barrier store absent; dedicated mount likely not present — refusing fresh empty barrier",
					slog.String("tenant", tenantID),
					slog.String("pool", a.Pool),
					slog.Any("error", rerr))
				continue
			}
			r.logger.Warn("registry: restore: failed to register tenant",
				slog.String("tenant", tenantID),
				slog.String("pool", a.Pool),
				slog.Any("error", rerr))
			continue
		}
		if !autoUnseal {
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
			slog.String("pool", a.Pool))
	}
	return nil
}

// RegisterVolumeTenant registers a durable, barrier-isolated tenant for the
// IsolationVolume mode and returns the tenant's filesystem DataRoot. The
// IsolationVolume layout reserves the pool directory as the tenant's DataRoot
// and places the barrier IAM Pebble in a dedicated "barrier" subdirectory,
// leaving "databases" free for the per-tenant Raft host's state. Every pooled
// tenant barrier uses the same layout (allocatePooledBackend,
// RegisterBarrierTenant). The resulting layout is:
//
//	{poolRoot}/{tenantID}/barrier      — barrier IAM Pebble (this registration)
//	{poolRoot}/{tenantID}/databases    — per-tenant Raft host state (caller)
//
// tenantID's Assignment must already exist (the caller reserved it via
// volume.Allocator.Reserve before calling this); there is no "pick a fresh
// path" branch left, because the catalog -- not this call -- is where a
// tenant's pool and path are decided. Reserve itself is one Raft
// transaction, so there is no "Assignment written, barrier registration
// still to come" window a crash or a write failure could ever observe
// half-done the way the pre-catalog tenant-root record could: that record
// was a second, separate write this call made AFTER choosing a path, and a
// fault injected into it (*seal.TenantRootPersistError, removed with the
// record itself) is what the deleted TestRegisterVolumeTenant_TenantRootWriteFailureFailsTheCreate
// covered. The property it proved -- a registration that cannot durably
// record its placement is undone, not served until the next restart loses
// it -- is now true by construction, not by a rollback this call performs.
//
// autoUnseal selects the tenant's key-management posture: when true the tenant
// DEK is wrapped under the SYSTEM barrier (SystemBarrierStrategy) so it can be
// initialized and re-attached credential-free; when false the tenant inherits
// the system barrier's own (credential-bound) sealing strategies and the tenant
// admin self-manages init/unseal. Either way the returned barrier is registered
// but NOT initialized or unsealed; the caller drives that (Initialize on create
// for auto-unseal tenants, Unseal on re-attach; self-managed tenants are left
// sealed for the tenant admin), mirroring the IsolationBarrier flow. The
// posture is persisted here so RestoreVolumeTenants can recover it at boot.
//
// RegisterVolumeTenant is the CREATE path: it never asserts the on-disk store
// already exists, because a freshly-reserved Assignment is legitimately new.
// The boot RE-ATTACH path must instead REQUIRE the store to pre-exist; that is
// ReattachVolumeTenant.
func (r *BarrierRegistry) RegisterVolumeTenant(ctx context.Context, tenantID string, autoUnseal bool) (string, *TenantBarrier, error) {
	return r.registerVolumeTenant(ctx, tenantID, autoUnseal, false)
}

// ReattachVolumeTenant re-registers an EXISTING IsolationVolume / IsolationRaft
// tenant barrier at its catalog-resolved DataRoot during boot. It is identical
// to RegisterVolumeTenant except that it REQUIRES the tenant's on-disk barrier
// store to already be present: the Assignment was reserved on a prior run, so
// {dataRoot}/barrier MUST exist. When it is absent — typically because the
// operator's dedicated disk/mount is not mounted at boot — it returns
// DataRootMountMissingError instead of letting perTenantReg.Open create a
// fresh, empty barrier on the underlying root filesystem (a fail-open
// availability and data-confusion hazard).
func (r *BarrierRegistry) ReattachVolumeTenant(ctx context.Context, tenantID string, autoUnseal bool) (string, *TenantBarrier, error) {
	return r.registerVolumeTenant(ctx, tenantID, autoUnseal, true)
}

// registerVolumeTenant is the shared implementation of RegisterVolumeTenant
// (mustExist=false) and ReattachVolumeTenant (mustExist=true). When mustExist
// is true the tenant's persisted barrier store must already be present on disk
// before it is opened; see ReattachVolumeTenant.
func (r *BarrierRegistry) registerVolumeTenant(ctx context.Context, tenantID string, autoUnseal, mustExist bool) (string, *TenantBarrier, error) {
	if tenantID == "" {
		return "", nil, ErrEmptyTenantID
	}
	perTenantReg := r.perTenantRegistry()
	if perTenantReg == nil {
		return "", nil, ErrPerTenantRegistryNotConfigured
	}

	// Refuse while the system barrier is sealed, rather than proceeding to
	// resolve the assignment and open a Pebble store for it.
	// TenantService.CreateTenant already fails closed here (its tombstone
	// check reads through the system barrier itself), so this only matters
	// for a caller on this registry directly, bypassing TenantService --
	// which is exactly the case that must fail closed too. Checked after the
	// wiring precondition above (no per-tenant registry) so a
	// misconfigured-but-unsealed call still reports ITS OWN error.
	if r.system == nil || r.system.IsSealed() {
		return "", nil, ErrSealed
	}

	dataRoot, err := r.RequireInsideLocalPool(ctx, tenantID)
	if err != nil {
		return "", nil, err
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

	cfg := r.tenantBarrierConfig(tenantID)
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
	// Persist the posture so RestoreVolumeTenants can recover it on this or
	// any other node after a restart; WHERE the tenant lives needs nothing
	// persisted here -- the catalog's Assignment already is that record.
	if perr := r.persistTenantBarrierPosture(ctx, tenantID, autoUnseal); perr != nil {
		if r.logger != nil {
			r.logger.Warn("registry: failed to persist tenant barrier posture; restart re-attach may default to self-managed",
				slog.String("tenant", tenantID),
				slog.Any("error", perr))
		}
	}
	if r.logger != nil {
		r.logger.Info("volume-mode tenant resolved through its volume pool assignment",
			slog.String("tenant", tenantID),
			slog.String("data_root", dataRoot),
			slog.String("barrier_path", barrierPath))
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
	config := r.tenantBarrierConfig(tenantID)
	return r.RegisterTenant(context.Background(), tenantID, base, config, r.systemStrategies()...)
}

// RegisterBarrierTenant registers an IsolationBarrier tenant's durable
// barrier. The tenant is placed on this node's first local pool when it has
// no Assignment yet (ensureGenericPoolAssignment), then registered exactly as
// a volume tenant is: its barrier store at {dataRoot}/barrier and its
// key-management posture persisted, so the boot restore
// (RestoreVolumeTenants) re-attaches it -- and, for a system-managed tenant,
// unseals it -- after a restart. Its records, its home store included, are
// encrypted under the tenant's own DEK in that store, never in the shared
// system store.
//
// autoUnseal selects the key-management posture, as for RegisterVolumeTenant:
// true wraps the DEK under the system barrier so the tenant initializes and
// re-attaches credential-free; false inherits the system barrier's own
// strategies and leaves init and unseal to the tenant admin. The barrier is
// returned registered but neither initialized nor unsealed.
func (r *BarrierRegistry) RegisterBarrierTenant(ctx context.Context, tenantID string, autoUnseal bool) (*TenantBarrier, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	// Checked before ensureGenericPoolAssignment's Reserve, as in
	// allocatePooledBackend: a reservation for a registration that was always
	// going to fail would leak.
	if r.perTenantRegistry() == nil {
		return nil, ErrPerTenantRegistryNotConfigured
	}
	if r.system == nil || r.system.IsSealed() {
		return nil, ErrSealed
	}
	if err := r.ensureGenericPoolAssignment(ctx, tenantID); err != nil {
		return nil, err
	}
	_, tb, err := r.registerVolumeTenant(ctx, tenantID, autoUnseal, false)
	return tb, err
}

// RegisterTenantPooledWithStrategy creates and registers a durable,
// barrier-isolated tenant on a durable Pebble-pool backend, sealing the
// tenant's DEK with the GIVEN strategies instead of inheriting the system
// barrier's posture.
//
// This is the registration path for IsolationBarrier tenants that supply
// their own sealing strategy (e.g. TPM2 or Shamir): the per-tenant path is
// pulled from the configured volume pool and a dedicated per-tenant Pebble
// is opened at that path (see WithVolumeAllocator / WithVolumeResolver / WithPerTenantRegistry). When
// the volume pool / per-tenant registry is not configured, RegisterTenant
// returns ErrPoolNotConfigured / ErrPerTenantRegistryNotConfigured.
//
// At least one strategy must be supplied; an IsolationBarrier tenant that
// inherits the system posture registers through RegisterBarrierTenant.
func (r *BarrierRegistry) RegisterTenantPooledWithStrategy(
	ctx context.Context,
	tenantID string,
	strategies ...SealingStrategy,
) (*TenantBarrier, error) {
	return r.RegisterTenant(ctx, tenantID, nil, r.tenantBarrierConfig(tenantID), strategies...)
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
	cfg := r.tenantBarrierConfig(tenantID)
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

// ReconfigureTenantShamir installs the Shamir strategy and threshold/total
// configuration on an ALREADY-REGISTERED tenant barrier shell, honoring an
// explicit --strategy shamir choice over whatever strategy the shell was
// pre-registered with (e.g. a dedicated-isolation tenant's self-managed
// shell, registered with the node's default strategy at `tenant create`
// time, long before its barrier's own strategy is chosen at `tenant
// barrier-init`). This mirrors the parity fix InitializeTenantWithStrategy
// already applies for passphrase/none (bypassing the shell's own
// preference) -- Shamir needs the same treatment because InitializeShamir
// reads its threshold/total/strategy from the barrier's OWN configuration
// rather than taking them as call parameters.
//
// The tenant must already be registered (via any Register* constructor) and
// must not yet be initialized; threshold/total are validated by
// NewShamirStrategy exactly as RegisterTenantPooledShamir validates them for
// a fresh registration.
func (r *BarrierRegistry) ReconfigureTenantShamir(tenantID string, threshold, total int, ttl time.Duration) error {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		return err
	}
	strategy, err := NewShamirStrategy(threshold, total)
	if err != nil {
		return err
	}
	tb.Barrier().SetShamirStrategy(strategy)
	tb.Barrier().SetShamirConfig(&ShamirConfig{Threshold: threshold, TotalShares: total, QuorumTTL: ttl})
	return nil
}

// tenantBarrierConfig returns the canonical BarrierConfig for a per-tenant
// barrier. It is the single source of truth for the per-tenant root-key
// path so every registration variant derives it identically.
func (r *BarrierRegistry) tenantBarrierConfig(tenantID string) BarrierConfig {
	cfg := BarrierConfig{
		RootKeyPath: "tenant/" + tenantID + "/root-key",
	}
	// A tenant barrier drains its stores within the node's seal.drain_timeout:
	// the system barrier's resolved bound is the one source of truth.
	if system := r.SystemBarrier(); system != nil {
		cfg.SealDrainTimeout = system.Config().SealDrainTimeout
	}
	return cfg
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

// systemStrategies returns the system barrier's sealing strategies as a
// slice suitable for passing to RegisterTenant. It is the inheritance seam
// used by RegisterTenantDefault and RegisterVolumeTenant.
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
// operation for a tenant that has been deleted: the catalog's Assignment and
// the tenant barrier posture record survive it, and RestoreVolumeTenants
// reads exactly those records at boot -- so a deleted volume or raft
// tenant's barrier is re-registered on the next restart, at the same
// location, over the same on-disk store (go-qrdb#413).
//
// It also removes the tenant's dedicated DataRoot from disk. That directory
// holds the tenant's own Raft state machines and its barrier store; nothing
// names it once the records above are gone, so leaving it is a permanent
// filesystem leak of the deleted tenant's (now key-less) ciphertext.
//
// The steps run in the order that survives a failure at any point: resolve
// where the data is, release the in-memory barrier and close the store,
// remove the bytes, and only then drop the records that name them. A
// failed removal therefore leaves the record in place, so the next call
// knows where the directory is and finishes the job; dropping the records
// first would orphan a directory that nothing names any more. DestroyTenant
// is idempotent and an already-destroyed tenant returns ErrTenantNotFound
// only when it was not registered here at all.
//
// A tenant that is registered but has no DataRoot (IsolationBarrier in
// keyspace mode, or a memory-backed test tenant) skips the filesystem step.
// A resolution failure that is NOT "no assignment" (e.g. the pool is
// temporarily unavailable on this node) is returned rather than treated as
// "nothing to remove": releasing the catalog reservation while the data is
// merely unreachable right now, not absent, would orphan it.
func (r *BarrierRegistry) DestroyTenant(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return ErrEmptyTenantID
	}

	root, rootErr := r.RequireInsideLocalPool(ctx, tenantID)
	hasRoot := rootErr == nil
	if rootErr != nil && !isUnresolvableAssignment(rootErr) {
		return rootErr
	}

	removeErr := r.ReleaseTenant(ctx, tenantID)
	if removeErr != nil && !errors.Is(removeErr, ErrTenantNotFound) {
		return removeErr
	}

	if hasRoot {
		if err := r.RemoveDataRoot(ctx, root); err != nil {
			return &TenantDataRootRemoveError{TenantID: tenantID, DataRoot: root, Err: err}
		}
		if r.logger != nil {
			r.logger.Info("registry: destroyed tenant data root",
				slog.String("tenant", tenantID),
				slog.String("data_root", root))
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
	if perTenantReg := r.perTenantRegistry(); perTenantReg != nil {
		if err := perTenantReg.Close(ctx, tenantID); err != nil {
			return &TenantStoreCloseError{TenantID: tenantID, Err: err}
		}
	}
	return removeErr
}

// DropTenantRecords deletes the two durable records the boot path restores a
// tenant barrier from: the tenant barrier posture record and the catalog's
// Assignment (released, which also returns the tenant's reservation to its
// pool). A missing record is success; a live failure is not, because
// leaving either behind resurrects the barrier at the next restart.
//
// Run it LAST, after the directories the records name have been removed: the
// record is how a retried reclamation finds a directory whose removal failed.
func (r *BarrierRegistry) DropTenantRecords(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return ErrEmptyTenantID
	}
	if r.system != nil && !r.system.IsSealed() {
		if err := r.system.Delete(ctx, tenantBarrierPostureStorePrefix+tenantID); err != nil &&
			!errors.Is(err, ErrSecretNotFound) {
			return &TenantRecordDeleteError{TenantID: tenantID, Record: "tenant-barrier-posture", Err: err}
		}
	}
	if allocator, _ := r.volumeResources(); allocator != nil {
		if err := allocator.Release(ctx, tenantID); err != nil {
			return &TenantRecordDeleteError{TenantID: tenantID, Record: "volume-pool-assignment", Err: err}
		}
	}
	return nil
}

// TenantDataRootCandidates returns every local directory that holds, or
// would hold, tenantID's data on THIS node. The catalog gives a tenant at
// most one Assignment (invariant 3), so there is at most one candidate now
// -- the directory RequireInsideLocalPool resolves -- where the pre-catalog
// model had to probe every configured pool base for a match.
//
// An absent Assignment is reported as no candidates (nil, nil), the direct
// analog of "no record found." Any other resolution failure (e.g. the
// pool is temporarily unavailable on this node) is returned rather than
// treated as "nothing here": the caller must not conclude there is
// nothing to reclaim when the truth is merely "cannot see it right now".
func (r *BarrierRegistry) TenantDataRootCandidates(ctx context.Context, tenantID string) ([]string, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	path, err := r.RequireInsideLocalPool(ctx, tenantID)
	if err != nil {
		if isUnresolvableAssignment(err) {
			return nil, nil
		}
		return nil, err
	}
	return []string{path}, nil
}

// HasTenantState reports whether this node still holds ANY registry state
// for tenantID: a barrier registered in memory, or a catalog Assignment. A
// retired id with neither and no directory on disk has been fully
// reclaimed here.
func (r *BarrierRegistry) HasTenantState(ctx context.Context, tenantID string) (bool, error) {
	if tenantID == "" {
		return false, ErrEmptyTenantID
	}
	if _, ok := r.tenants.Load(tenantID); ok {
		return true, nil
	}
	allocator, _ := r.volumeResources()
	if allocator == nil {
		return false, nil
	}
	_, err := allocator.Resolve(ctx, tenantID)
	if err == nil {
		return true, nil
	}
	var notFound *volume.AssignmentNotFoundError
	if errors.As(err, &notFound) {
		return false, nil // no record at all
	}
	var invalidPath *volume.InvalidAssignmentPathError
	if errors.As(err, &invalidPath) {
		// A D9 interim-shaped record ({pool, slot}, refused by Resolve's
		// own ValidateAssignmentPath check): the record genuinely EXISTS
		// (it is state that still needs reclaiming via
		// DestroyTenant/reconcile), it is simply unusable.
		return true, nil
	}
	return false, err
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
	return r.unsealTenant(tenantID, func(tb *TenantBarrier) error {
		return tb.Unseal(ctx, creds)
	})
}

// UnsealTenantWithStrategy unseals the tenant barrier identified by tenantID
// with exactly strategy, never with a strategy the barrier's shell was
// registered with under the same ID; a root key sealed by a different
// strategy is refused with *UnsealStrategyMismatchError. It is ordered
// against a whole-node seal exactly as UnsealTenant is.
func (r *BarrierRegistry) UnsealTenantWithStrategy(ctx context.Context, tenantID string, strategy SealingStrategy, creds Credentials) error {
	return r.unsealTenant(tenantID, func(tb *TenantBarrier) error {
		return tb.UnsealWithStrategy(ctx, strategy, creds)
	})
}

// unsealTenant runs unseal against the registered tenant barrier under
// sealMu and, on success, drops the tenant from the node-seal record.
func (r *BarrierRegistry) unsealTenant(tenantID string, unseal func(*TenantBarrier) error) error {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		return err
	}
	r.sealMu.Lock()
	defer r.sealMu.Unlock()
	if err := unseal(tb); err != nil {
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
		// A Seal that errors after sealing (a drain timeout) still leaves
		// the tenant sealed: record it so the node unseal restores it.
		if err := tb.Seal(); err != nil {
			errs = append(errs, err)
		}
		if tenantID, ok := key.(string); ok && systemManaged && tb.IsSealed() {
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
