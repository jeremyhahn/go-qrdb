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
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// Compile-time interface check.
var _ RoleServicer = (*routingRoleService)(nil)

// routingRoleService is a RoleServicer decorator that routes each call to
// the per-tenant kvRoleStore backed by the KVStore the StoreResolver
// returns for the request's tenant. Prefix/system tenants resolve to the
// shared system KVStore (transparent); barrier-isolated tenants resolve to
// a KVStore backed by the tenant's own TenantBarrier, so role records are
// encrypted with the tenant DEK and stored under the tenant's namespace.
//
// SECURITY: a sealed barrier tenant surfaces tenant.ErrTenantSealed from
// the resolver and the routed call fails closed — never falling back to the
// shared store.
type routingRoleService struct {
	resolver tenant.StoreResolver
	opts     []Option // applied to every per-tenant kvRoleStore (e.g. audit logger)
	stores   sync.Map // map[kvstore.KVStore]RoleServicer
}

// NewRoutingService wraps role services with per-tenant store routing. The
// resolver maps a tenant ID to its backing KVStore and must not be nil.
// Optional Options are applied to every per-tenant store the router builds,
// so cross-cutting concerns like audit logging reach barrier-isolated tenants.
func NewRoutingService(resolver tenant.StoreResolver, opts ...Option) (RoleServicer, error) {
	if resolver == nil {
		return nil, &StoreError{Op: "new_routing", Detail: "store resolver must not be nil"}
	}
	return &routingRoleService{resolver: resolver, opts: opts}, nil
}

// storeFor resolves the backing KVStore for tenantID and returns a
// kvRoleStore bound to it, caching the store by KVStore identity.
func (r *routingRoleService) storeFor(ctx context.Context, tenantID string) (RoleServicer, error) {
	kv, err := r.resolver.Resolve(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return r.storeForKV(kv)
}

// storeForKV builds (or returns a cached) kvRoleStore for a resolved
// KVStore.
func (r *routingRoleService) storeForKV(kv kvstore.KVStore) (RoleServicer, error) {
	if cached, ok := r.stores.Load(kv); ok {
		return cached.(RoleServicer), nil //nolint:errcheck // type assertion on a value this container only ever holds
	}
	svc, err := NewStore(kv, r.opts...)
	if err != nil {
		return nil, err
	}
	actual, _ := r.stores.LoadOrStore(kv, svc)
	return actual.(RoleServicer), nil //nolint:errcheck // type assertion on a value this container only ever holds
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
	svc, err := r.storeFor(ctx, eff)
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

// GetRole routes on the context tenant. Role IDs are globally unique but the
// record physically lives in the tenant's store.
func (r *routingRoleService) GetRole(ctx context.Context, id string) (*Role, error) {
	svc, err := r.storeFor(ctx, tenant.FromContext(ctx))
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

// UpdateRole routes on the context tenant.
func (r *routingRoleService) UpdateRole(ctx context.Context, req *UpdateRoleRequest) (*Role, error) {
	svc, err := r.storeFor(ctx, tenant.FromContext(ctx))
	if err != nil {
		return nil, err
	}
	return svc.UpdateRole(ctx, req)
}

// DeleteRole routes on the context tenant.
func (r *routingRoleService) DeleteRole(ctx context.Context, id string) error {
	svc, err := r.storeFor(ctx, tenant.FromContext(ctx))
	if err != nil {
		return err
	}
	return svc.DeleteRole(ctx, id)
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

// ResolvePermissions routes on the context tenant. The role IDs are resolved
// against the tenant's own store.
func (r *routingRoleService) ResolvePermissions(ctx context.Context, roleIDs []string) ([]permission.Permission, error) {
	svc, err := r.storeFor(ctx, tenant.FromContext(ctx))
	if err != nil {
		return nil, err
	}
	return svc.ResolvePermissions(ctx, roleIDs)
}
