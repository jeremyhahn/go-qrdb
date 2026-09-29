// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package role

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// Compile-time interface check.
var _ RoleServicer = (*routingRoleService)(nil)

// routingRoleService is a RoleServicer decorator that routes each call to
// the per-tenant kvRoleStore backed by the KVStore the StoreResolver
// returns for the request's tenant. Prefix/system tenants resolve to the
// shared system KVStore CONFINED to that tenant's keyspace, so role records
// live under "{tenant}/" — the tenant alone, because identity is tenant-wide.
// The reserved "__sys:" prefix is not exempt (#41), and the tenant segments
// this store embeds in its own keys are redundant with the namespace rather
// than what enforces isolation; barrier-isolated tenants resolve to
// a KVStore backed by the tenant's own TenantBarrier, so role records are
// encrypted with the tenant DEK and stored under the tenant's namespace.
//
// SECURITY: a sealed barrier tenant surfaces tenant.ErrTenantSealed from
// the resolver and the routed call fails closed — never falling back to the
// shared store.
//
// Operations that name a role by its globally unique ID resolve the tenant
// that holds it through a tenant.IdentityIndex before routing (see
// targetTenant). The router both READS that index and, by passing
// WithIdentityIndex down to every per-tenant store it builds, ensures the
// store that writes a role is the one that writes its index entry.
type routingRoleService struct {
	resolver tenant.StoreResolver
	identity tenant.IdentityIndex
	opts     []Option // applied to every per-tenant kvRoleStore (e.g. audit logger)
	stores   sync.Map // map[kvstore.KVStore]RoleServicer: the store every operation uses

	// atomicStores caches the system tenant's CREATE store, built with
	// WithAtomicIdentity. It is separate from stores so that only a create
	// ever pays for, or depends on, the capability probe; see createStoreFor.
	atomicStores sync.Map // map[kvstore.KVStore]RoleServicer

	// atomicCapability caches the answer to the ONE runtime probe this
	// router needs: whether the system tenant's resolved store actually
	// supports guarded batches, not merely whether its TYPE says so. It holds
	// capabilityUnknown until a probe returns a definitive answer; see
	// atomicCapable.
	atomicCapability atomic.Int32
}

// NewRoutingService wraps role services with per-tenant store routing. The
// resolver maps a tenant ID to its backing KVStore and must not be nil.
// Optional Options are applied to every per-tenant store the router builds,
// so cross-cutting concerns like audit logging reach barrier-isolated tenants.
func NewRoutingService(resolver tenant.StoreResolver, opts ...Option) (RoleServicer, error) {
	if resolver == nil {
		return nil, &StoreError{Op: "new_routing", Detail: "store resolver must not be nil"}
	}
	index, err := tenant.NewResolvedIdentityIndex(resolver)
	if err != nil {
		return nil, &StoreError{Op: "new_routing", Detail: "identity index", Err: err}
	}
	return &routingRoleService{
		resolver: resolver,
		identity: index,
		// Appended LAST so a routed deployment cannot be configured without
		// it: every per-tenant store this router builds maintains the index
		// the router reads.
		opts: append(append([]Option(nil), opts...), WithIdentityIndex(index)),
	}, nil
}

// targetTenant returns the tenant whose store holds the role with this id.
// See tenant.OwningTenant for the rule; an owning tenant whose barrier is
// sealed makes storeFor fail closed with tenant.ErrTenantSealed.
func (r *routingRoleService) targetTenant(ctx context.Context, id, op string) (string, error) {
	return tenant.OwningTenant(ctx, r.identity, tenant.IdentityRole, id, op)
}

// storeFor resolves the backing KVStore for tenantID and returns the
// role store bound to it, caching the store by KVStore identity. It never
// touches consensus beyond what the operation itself does: reads, lookups and
// authorization go through here, and must keep working when no leader is
// available to answer a capability probe.
func (r *routingRoleService) storeFor(ctx context.Context, tenantID string) (RoleServicer, error) {
	kv, err := r.resolver.Resolve(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return r.storeForKV(kv, &r.stores, r.opts)
}

// createStoreFor is storeFor for the CREATE path: the one operation whose
// store may carry WithAtomicIdentity, and therefore the only one that probes.
func (r *routingRoleService) createStoreFor(ctx context.Context, tenantID string) (RoleServicer, error) {
	kv, err := r.resolver.Resolve(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	// WithAtomicIdentity's same-physical-store precondition holds for
	// EXACTLY one tenant here: tenant.NewResolvedIdentityIndex always
	// resolves its own backing store via resolver.Resolve(ctx,
	// tenantid.System) (go-qrdb#555 review, round 3 — "the services
	// exposed afterward are routed stores... this misses the embedded
	// transport/composer"). For any OTHER tenant, the identity index's
	// store and this tenant's own resolved store are DIFFERENT physical
	// objects (a prefix tenant's own namespace vs. the system tenant's),
	// and folding a claim key derived from one into a guarded batch
	// addressed to the other would route it through the WRONG store's
	// namespace confinement — silently writing the claim somewhere
	// Lookup/Forget never look, which is worse than the sequential path
	// this replaces. Only the system tenant's resolved store is ever
	// offered the option; every other tenant keeps the sequential path
	// unconditionally, exactly as before this fix.
	//
	// Capability is still checked, not assumed: an embedder whose system
	// tenant store does not implement kvstore.MultiGuardedBatchKVStore
	// (a non-transactional backend) keeps the sequential path rather than
	// failing construction outright. Unlike cmd/qrdbd's single, fixed state
	// machine — where the owner ruled "fail closed, no silent fallback"
	// because one deployment either can or cannot back the guarantee at
	// all — this router serves potentially many heterogeneous per-tenant
	// backends from ONE process, and a single tenant's backend lacking the
	// capability must not make role/user creation impossible for every
	// OTHER tenant.
	//
	// The type assertion alone is NOT enough (go-qrdb#555 review, round 4):
	// pkg/server/lifecycle's guarded adapter satisfies
	// kvstore.MultiGuardedBatchKVStore whenever a non-nil TransactionServicer
	// exists, which BuildServices wires unconditionally — including for
	// "memory"/"concurrent" state machines, whose engines reject the
	// transaction at RUN TIME. Trusting the type there would enable atomic
	// identity for a state machine that cannot honour it, and the first
	// role create would hard-fail instead of falling back. atomicCapable
	// asks the state machine itself, with a genuine no-op guarded proposal,
	// and caches only a definitive answer — see its doc.
	if !tenantid.IsSystem(tenantID) {
		return r.storeForKV(kv, &r.stores, r.opts)
	}
	multi, ok := kv.(kvstore.MultiGuardedBatchKVStore)
	if !ok {
		return r.storeForKV(kv, &r.stores, r.opts)
	}
	capable, err := r.atomicCapable(ctx, multi)
	if err != nil {
		return nil, &StoreError{Op: "resolve", Detail: "guarded batch capability probe", Err: err}
	}
	if !capable {
		return r.storeForKV(kv, &r.stores, r.opts)
	}
	return r.storeForKV(kv, &r.atomicStores, append(slices.Clone(r.opts), WithAtomicIdentity()))
}

// storeForKV builds (or returns the one cached in cache) a role store for a
// resolved KVStore with opts.
func (r *routingRoleService) storeForKV(kv kvstore.KVStore, cache *sync.Map, opts []Option) (RoleServicer, error) {
	// Non-comparable stores skip the cache — see the equivalent note in
	// user.routingUserService.storeForKV. Using such a value as a sync.Map
	// key panics before any role operation runs.
	if !kvstore.UsableAsMapKey(kv) {
		return NewStore(kv, opts...)
	}
	if cached, ok := cache.Load(kv); ok {
		return cached.(RoleServicer), nil //nolint:errcheck // type assertion on a value this container only ever holds
	}
	svc, err := NewStore(kv, opts...)
	if err != nil {
		return nil, err
	}
	actual, _ := cache.LoadOrStore(kv, svc)
	return actual.(RoleServicer), nil //nolint:errcheck // type assertion on a value this container only ever holds
}

// Values of atomicCapability.
const (
	capabilityUnknown int32 = iota
	capabilityPresent
	capabilityAbsent
)

// atomicCapable reports whether guarded can honour the atomic identity batch,
// not merely whether its TYPE offers it. Every guarded adapter in this
// codebase satisfies kvstore.MultiGuardedBatchKVStore whenever a transaction
// plane exists — unconditionally for pkg/server/lifecycle's composition — but
// the "memory" and "concurrent" state machines refuse a transaction at apply
// time, so the type alone would enable atomic identity on a node that cannot
// honour it and the first create would fail instead of taking the sequential
// path.
//
// The probe is kvstore.ProbeGuardedBatch, and ONLY its definitive answers are
// cached, for the router's lifetime: the system tenant's resolved store and
// the state machine behind it do not change while the process runs. An
// inconclusive probe — no leader yet, a cancelled ctx — is returned as an
// error and NOT cached. Reading it as "incapable" would put the system tenant
// on the sequential path for the life of the process, silently reopening the
// multi-node first-boot race the atomic path exists to close; failing the one
// operation is the honest answer, and the condition that made the probe
// inconclusive would have failed that operation anyway.
func (r *routingRoleService) atomicCapable(ctx context.Context, guarded kvstore.MultiGuardedBatchKVStore) (bool, error) {
	if state := r.atomicCapability.Load(); state != capabilityUnknown {
		return state == capabilityPresent, nil
	}
	capable, err := kvstore.ProbeGuardedBatch(ctx, guarded)
	if err != nil {
		return false, err
	}
	state := capabilityAbsent
	if capable {
		state = capabilityPresent
	}
	r.atomicCapability.CompareAndSwap(capabilityUnknown, state)
	return capable, nil
}

// CreateRole routes on the EFFECTIVE tenant reconciled from the caller's
// authenticated identity and the request's TenantID (see
// tenant.EffectiveTenant). A normal tenant caller may only create roles in its
// own tenant; a request tenant naming a different tenant is rejected with
// *tenant.CrossTenantDeniedError. The request's TenantID is normalized to the
// effective tenant before the store call so the stored record's tenant is
// authoritative rather than attacker-controlled.
func (r *routingRoleService) CreateRole(ctx context.Context, req *CreateRoleRequest) (*Role, error) {
	requested := ""
	if req != nil {
		requested = req.TenantID
	}
	eff, err := tenant.EffectiveTenant(ctx, requested, "create role")
	if err != nil {
		return nil, err
	}
	svc, err := r.createStoreFor(ctx, eff)
	if err != nil {
		return nil, err
	}
	if req != nil {
		normalized := *req
		normalized.TenantID = eff
		req = &normalized
	}
	return svc.CreateRole(ctx, req)
}

// GetRole routes on the tenant that HOLDS the id. Role IDs are globally
// unique but the record physically lives in one tenant's store.
func (r *routingRoleService) GetRole(ctx context.Context, id string) (*Role, error) {
	target, err := r.targetTenant(ctx, id, "read role")
	if err != nil {
		return nil, err
	}
	svc, err := r.storeFor(ctx, target)
	if err != nil {
		return nil, err
	}
	return svc.GetRole(ctx, id)
}

// GetRoleByName routes on the EFFECTIVE tenant reconciled from the caller's
// identity and the explicit tenant argument. Read of another tenant's role is
// denied for a normal tenant caller (*tenant.CrossTenantDeniedError); the
// effective tenant — never the raw argument — is passed to the store lookup.
func (r *routingRoleService) GetRoleByName(ctx context.Context, tenantID, name string) (*Role, error) {
	eff, err := tenant.EffectiveTenant(ctx, tenantID, "read role by name")
	if err != nil {
		return nil, err
	}
	svc, err := r.storeFor(ctx, eff)
	if err != nil {
		return nil, err
	}
	return svc.GetRoleByName(ctx, eff, name)
}

// UpdateRole routes on the tenant that holds the request's role id.
// UpdateRoleRequest has no tenant field, so an update cannot move a role
// between tenants and the index entry stays correct across it.
func (r *routingRoleService) UpdateRole(ctx context.Context, req *UpdateRoleRequest) (*Role, error) {
	id := ""
	if req != nil {
		id = req.ID
	}
	target, err := r.targetTenant(ctx, id, "update role")
	if err != nil {
		return nil, err
	}
	svc, err := r.storeFor(ctx, target)
	if err != nil {
		return nil, err
	}
	return svc.UpdateRole(ctx, req)
}

// DeleteRole routes on the tenant that holds the id.
//
// A not-found from the store is not necessarily a delete of something that
// never existed: it is also what a RETRY sees after an earlier attempt
// removed the record but failed to release the claim. The store removes the
// record last, so at that point the claim is the only thing left, and
// finishing the release here completes the delete instead of leaving a
// routing entry no operation can ever remove — GetRole short-circuiting on
// the missing record is precisely what made that state permanent.
//
// The reconciliation is scoped to the tenant this call was ROUTED to, and
// tenant.ReconcileDeletedIdentity releases only a claim that tenant holds, so
// a retry can neither act on another tenant's role nor free another tenant's
// id. When nothing was claimed the not-found answer stands.
func (r *routingRoleService) DeleteRole(ctx context.Context, id string) error {
	target, err := r.targetTenant(ctx, id, "delete role")
	if err != nil {
		return err
	}
	svc, err := r.storeFor(ctx, target)
	if err != nil {
		return err
	}
	derr := svc.DeleteRole(ctx, id)
	var notFound *RoleNotFoundError
	if !errors.As(derr, &notFound) {
		return derr
	}
	released, rerr := tenant.ReconcileDeletedIdentity(ctx, r.identity, tenant.IdentityRole, id, target)
	if rerr != nil {
		return rerr
	}
	if released {
		return nil
	}
	return derr
}

// ListRoles routes on the EFFECTIVE tenant reconciled from the caller's
// identity and the request's TenantID. A normal tenant caller may only
// enumerate its own tenant; a request tenant naming a different tenant is
// rejected with *tenant.CrossTenantDeniedError. The request's TenantID filter
// is normalized to the effective tenant before the store scan.
func (r *routingRoleService) ListRoles(ctx context.Context, req *ListRolesRequest) (*ListRolesResponse, error) {
	requested := ""
	if req != nil {
		requested = req.TenantID
	}
	eff, err := tenant.EffectiveTenant(ctx, requested, "list roles")
	if err != nil {
		return nil, err
	}
	svc, err := r.storeFor(ctx, eff)
	if err != nil {
		return nil, err
	}
	if req != nil {
		normalized := *req
		normalized.TenantID = eff
		req = &normalized
	}
	return svc.ListRoles(ctx, req)
}

// ResolvePermissions routes on the context tenant, deliberately, and is the
// one by-id method that does NOT consult the identity index.
//
// It resolves the CALLER's own role bindings during authorization. Those
// bindings were read from the caller's own user record in the caller's own
// tenant, so the roles they name belong to that tenant; resolving them
// anywhere else would let a binding that named another tenant's role import
// that role's permissions. The index is a routing aid for a caller who holds
// only an id — it must not become a way to widen a grant.
func (r *routingRoleService) ResolvePermissions(ctx context.Context, roleIDs []string) ([]permission.Permission, error) {
	svc, err := r.storeFor(ctx, tenant.FromContext(ctx))
	if err != nil {
		return nil, err
	}
	return svc.ResolvePermissions(ctx, roleIDs)
}
