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

package user

import (
	"context"
	"errors"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// Compile-time interface check.
var _ UserServicer = (*routingUserService)(nil)

// routingUserService is a UserServicer decorator that routes each call to
// the per-tenant kvUserStore backed by the KVStore the StoreResolver
// returns for the request's tenant. For a logical-prefix (or system) tenant
// the resolver returns the shared system KVStore CONFINED to that tenant's
// keyspace, so user records live under "{tenant}/" — the tenant alone,
// because identity is tenant-wide and a user must authenticate whichever
// database a request names. The reserved "__sys:" prefix is not exempt (#41),
// and the tenant segments this store embeds in its own keys are therefore
// redundant with the namespace rather than the thing enforcing isolation. For
// a barrier-isolated tenant the
// resolver returns a KVStore backed by the tenant's own TenantBarrier, so
// the user record is encrypted with the tenant DEK and stored under the
// tenant's "{tenantID}/" namespace — never in the shared store.
//
// SECURITY: when a barrier tenant's barrier is sealed, the resolver returns
// tenant.ErrTenantSealed and the routed call fails closed; there is no
// fallback to the shared store.
//
// Per-tenant kvUserStore instances are cached in a sync.Map keyed by the
// resolved KVStore pointer identity, so repeated calls for the same tenant
// reuse one store instance without locking on the hot path.
//
// Operations that name a user by its globally unique ID resolve the tenant
// that holds it through a tenant.IdentityIndex before routing (see
// targetTenant). The router both READS that index here and, by passing
// WithIdentityIndex down to every per-tenant store it builds, ensures the
// store that writes a user is the one that writes its index entry.
type routingUserService struct {
	resolver tenant.StoreResolver
	idgen    dao.IDGenerator
	identity tenant.IdentityIndex
	opts     []Option // applied to every per-tenant kvUserStore (e.g. audit logger)
	stores   sync.Map // map[kvstore.KVStore]UserServicer
}

// NewRoutingService wraps user services with per-tenant store routing. The
// resolver maps a tenant ID to its backing KVStore; idgen is the
// node-unique Snowflake source threaded into each per-tenant kvUserStore.
// Both must be non-nil. Optional Options are applied to every per-tenant
// store the router builds, so cross-cutting concerns like audit logging
// reach barrier-isolated tenants.
func NewRoutingService(resolver tenant.StoreResolver, idgen dao.IDGenerator, opts ...Option) (UserServicer, error) {
	if resolver == nil {
		return nil, &StoreError{Op: "new_routing", Detail: "store resolver must not be nil"}
	}
	if idgen == nil {
		return nil, &StoreError{Op: "new_routing", Detail: "id generator must not be nil"}
	}
	index, err := tenant.NewResolvedIdentityIndex(resolver)
	if err != nil {
		return nil, &StoreError{Op: "new_routing", Detail: "identity index", Err: err}
	}
	return &routingUserService{
		resolver: resolver,
		idgen:    idgen,
		identity: index,
		// The identity-index option is appended LAST so a routed deployment
		// cannot be configured without it: every per-tenant store this
		// router builds maintains the index the router reads.
		opts: append(append([]Option(nil), opts...), WithIdentityIndex(index)),
	}, nil
}

// targetTenant returns the tenant whose store holds the user with this id.
//
// A user id is globally unique but user STORAGE is per-tenant, so an
// operation carrying only an id — every by-id method below — cannot assume
// the caller's own tenant holds the record. tenant.OwningTenant is the
// shared rule: the owning tenant when the caller may reach it, the caller's
// own tenant when the id is unindexed or out of the caller's reach, and an
// error propagated (never swallowed into a fallback) otherwise.
//
// SECURITY: an owning tenant whose barrier is sealed surfaces
// tenant.ErrTenantSealed from storeFor below. The operation fails closed; it
// does not read the shared store instead.
func (r *routingUserService) targetTenant(ctx context.Context, id uint64, op string) (string, error) {
	if id == 0 {
		// Zero is not a user id. Route to the caller's own tenant so the
		// store answers with the ValidationError its callers expect, rather
		// than an index error describing the lookup.
		return tenant.FromContext(ctx), nil
	}
	return tenant.OwningTenant(ctx, r.identity, tenant.IdentityUser, identityID(id), op)
}

// storeFor resolves the backing KVStore for tenantID and returns a
// kvUserStore bound to it, caching the store by KVStore identity.
func (r *routingUserService) storeFor(ctx context.Context, tenantID string) (UserServicer, error) {
	kv, err := r.resolver.Resolve(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return r.storeForKV(kv)
}

// storeForKV builds (or returns a cached) kvUserStore for a resolved
// KVStore. Caching by KVStore identity means the shared system store maps
// to exactly one kvUserStore and each barrier tenant maps to its own.
func (r *routingUserService) storeForKV(kv kvstore.KVStore) (UserServicer, error) {
	// A store whose dynamic type is NOT comparable skips the cache and gets a
	// fresh service. KVStore is an interface and nothing requires its
	// implementations to be comparable, so using such a value as a sync.Map
	// key panics with "hash of unhashable type" before any user operation
	// runs. The cache is an optimization; correctness must not depend on the
	// caller's choice of representation.
	if !kvstore.UsableAsMapKey(kv) {
		return NewStore(kv, r.idgen, r.opts...)
	}
	if cached, ok := r.stores.Load(kv); ok {
		return cached.(UserServicer), nil //nolint:errcheck // type assertion on a value this container only ever holds
	}
	svc, err := NewStore(kv, r.idgen, r.opts...)
	if err != nil {
		return nil, err
	}
	actual, _ := r.stores.LoadOrStore(kv, svc)
	return actual.(UserServicer), nil //nolint:errcheck // type assertion on a value this container only ever holds
}

// CreateUser routes on the EFFECTIVE tenant reconciled from the caller's
// authenticated identity and the request's TenantID (see
// tenant.EffectiveTenant). A normal tenant caller may only create users in
// its own tenant; a request tenant naming a different tenant is rejected with
// *tenant.CrossTenantDeniedError. The request's TenantID is normalized to the
// effective tenant before the store call so the stored record's tenant is
// authoritative rather than attacker-controlled.
func (r *routingUserService) CreateUser(ctx context.Context, req *CreateUserRequest) (*User, error) {
	requested := ""
	if req != nil {
		requested = req.TenantID
	}
	eff, err := tenant.EffectiveTenant(ctx, requested, "create user")
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
	return svc.CreateUser(ctx, req)
}

// GetUser routes on the tenant that HOLDS the id. User IDs are globally
// unique Snowflakes, but the record physically lives in one tenant's store,
// so the lookup must target that store rather than the caller's.
func (r *routingUserService) GetUser(ctx context.Context, id uint64) (*User, error) {
	target, err := r.targetTenant(ctx, id, "read user")
	if err != nil {
		return nil, err
	}
	svc, err := r.storeFor(ctx, target)
	if err != nil {
		return nil, err
	}
	return svc.GetUser(ctx, id)
}

// GetUserByUsername routes on the EFFECTIVE tenant reconciled from the
// caller's identity and the explicit tenant argument. Enumeration/read of
// another tenant's users is denied for a normal tenant caller
// (*tenant.CrossTenantDeniedError); the effective tenant — never the raw
// argument — is passed to the store lookup.
func (r *routingUserService) GetUserByUsername(ctx context.Context, tenantID, username string) (*User, error) {
	eff, err := tenant.EffectiveTenant(ctx, tenantID, "read user by username")
	if err != nil {
		return nil, err
	}
	svc, err := r.storeFor(ctx, eff)
	if err != nil {
		return nil, err
	}
	return svc.GetUserByUsername(ctx, eff, username)
}

// UserByCertThumbprint routes on the context tenant.
//
// It is the one caller-routed method with no id to place: the lookup key is
// a certificate thumbprint resolved through the tenant's own cert index, and
// the tenant is established by the presented certificate before this runs
// (pkg/auth/wiring pins the admin cert to the tenant its OU names). There is
// nothing for the identity index to resolve, so it stays context-routed.
func (r *routingUserService) UserByCertThumbprint(ctx context.Context, thumbprint string) (*User, error) {
	svc, err := r.storeFor(ctx, tenant.FromContext(ctx))
	if err != nil {
		return nil, err
	}
	return svc.UserByCertThumbprint(ctx, thumbprint)
}

// AddCertBinding routes on the tenant that holds the user id.
func (r *routingUserService) AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*User, error) {
	target, err := r.targetTenant(ctx, userID, "bind user certificate")
	if err != nil {
		return nil, err
	}
	svc, err := r.storeFor(ctx, target)
	if err != nil {
		return nil, err
	}
	return svc.AddCertBinding(ctx, userID, thumbprint, notAfterUnix)
}

// UpdateUser routes on the tenant that holds the request's user id. The
// request cannot move a user between tenants — UpdateUserRequest has no
// tenant field — so the index entry stays correct across an update.
func (r *routingUserService) UpdateUser(ctx context.Context, req *UpdateUserRequest) (*User, error) {
	id := uint64(0)
	if req != nil {
		id = req.ID
	}
	target, err := r.targetTenant(ctx, id, "update user")
	if err != nil {
		return nil, err
	}
	svc, err := r.storeFor(ctx, target)
	if err != nil {
		return nil, err
	}
	return svc.UpdateUser(ctx, req)
}

// DeleteUser routes on the tenant that holds the id.
//
// As in role.DeleteRole, a not-found is also what a RETRY sees after an
// earlier attempt removed the record but failed to release the claim. The
// store removes the record last, so the claim is then the only thing left;
// releasing it here completes the delete rather than leaving a routing entry
// nothing can remove. The reconciliation is scoped to the tenant this call
// was routed to and releases only a claim that tenant holds.
func (r *routingUserService) DeleteUser(ctx context.Context, id uint64) error {
	target, err := r.targetTenant(ctx, id, "delete user")
	if err != nil {
		return err
	}
	svc, err := r.storeFor(ctx, target)
	if err != nil {
		return err
	}
	derr := svc.DeleteUser(ctx, id)
	var notFound *UserNotFoundError
	if !errors.As(derr, &notFound) {
		return derr
	}
	released, rerr := tenant.ReconcileDeletedIdentity(ctx, r.identity, tenant.IdentityUser, identityID(id), target)
	if rerr != nil {
		return rerr
	}
	if released {
		return nil
	}
	return derr
}

// ListUsers routes on the EFFECTIVE tenant reconciled from the caller's
// identity and the request's TenantID. A normal tenant caller may only
// enumerate its own tenant; a request tenant naming a different tenant is
// rejected with *tenant.CrossTenantDeniedError. The request's TenantID filter
// is normalized to the effective tenant before the store scan.
func (r *routingUserService) ListUsers(ctx context.Context, req *ListUsersRequest) (*ListUsersResponse, error) {
	requested := ""
	if req != nil {
		requested = req.TenantID
	}
	eff, err := tenant.EffectiveTenant(ctx, requested, "list users")
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
	return svc.ListUsers(ctx, req)
}

// EnableUser routes on the tenant that holds the id.
func (r *routingUserService) EnableUser(ctx context.Context, id uint64) error {
	target, err := r.targetTenant(ctx, id, "enable user")
	if err != nil {
		return err
	}
	svc, err := r.storeFor(ctx, target)
	if err != nil {
		return err
	}
	return svc.EnableUser(ctx, id)
}

// DisableUser routes on the tenant that holds the id.
func (r *routingUserService) DisableUser(ctx context.Context, id uint64) error {
	target, err := r.targetTenant(ctx, id, "disable user")
	if err != nil {
		return err
	}
	svc, err := r.storeFor(ctx, target)
	if err != nil {
		return err
	}
	return svc.DisableUser(ctx, id)
}
