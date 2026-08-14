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
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// Compile-time interface check.
var _ UserServicer = (*routingUserService)(nil)

// routingUserService is a UserServicer decorator that routes each call to
// the per-tenant kvUserStore backed by the KVStore the StoreResolver
// returns for the request's tenant. For a logical-prefix (or system)
// tenant the resolver returns the shared system KVStore, so routing is
// transparent and behaviour is unchanged. For a barrier-isolated tenant the
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
type routingUserService struct {
	resolver tenant.StoreResolver
	idgen    dao.IDGenerator
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
	return &routingUserService{resolver: resolver, idgen: idgen, opts: opts}, nil
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

// GetUser routes on the tenant bound to the context. User IDs are globally
// unique Snowflakes, but the record physically lives in the tenant's store,
// so the lookup must target that store.
func (r *routingUserService) GetUser(ctx context.Context, id uint64) (*User, error) {
	svc, err := r.storeFor(ctx, tenant.FromContext(ctx))
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
func (r *routingUserService) UserByCertThumbprint(ctx context.Context, thumbprint string) (*User, error) {
	svc, err := r.storeFor(ctx, tenant.FromContext(ctx))
	if err != nil {
		return nil, err
	}
	return svc.UserByCertThumbprint(ctx, thumbprint)
}

// AddCertBinding routes on the context tenant.
func (r *routingUserService) AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*User, error) {
	svc, err := r.storeFor(ctx, tenant.FromContext(ctx))
	if err != nil {
		return nil, err
	}
	return svc.AddCertBinding(ctx, userID, thumbprint, notAfterUnix)
}

// UpdateUser routes on the context tenant.
func (r *routingUserService) UpdateUser(ctx context.Context, req *UpdateUserRequest) (*User, error) {
	svc, err := r.storeFor(ctx, tenant.FromContext(ctx))
	if err != nil {
		return nil, err
	}
	return svc.UpdateUser(ctx, req)
}

// DeleteUser routes on the context tenant.
func (r *routingUserService) DeleteUser(ctx context.Context, id uint64) error {
	svc, err := r.storeFor(ctx, tenant.FromContext(ctx))
	if err != nil {
		return err
	}
	return svc.DeleteUser(ctx, id)
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

// EnableUser routes on the context tenant.
func (r *routingUserService) EnableUser(ctx context.Context, id uint64) error {
	svc, err := r.storeFor(ctx, tenant.FromContext(ctx))
	if err != nil {
		return err
	}
	return svc.EnableUser(ctx, id)
}

// DisableUser routes on the context tenant.
func (r *routingUserService) DisableUser(ctx context.Context, id uint64) error {
	svc, err := r.storeFor(ctx, tenant.FromContext(ctx))
	if err != nil {
		return err
	}
	return svc.DisableUser(ctx, id)
}
