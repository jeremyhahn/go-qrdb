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
	stores   sync.Map // map[kvstore.KVStore]RoleServicer
}

// NewRoutingService wraps role services with per-tenant store routing. The
// resolver maps a tenant ID to its backing KVStore and must not be nil.
func NewRoutingService(resolver tenant.StoreResolver) (RoleServicer, error) {
	if resolver == nil {
		return nil, &StoreError{Op: "new_routing", Detail: "store resolver must not be nil"}
	}
	return &routingRoleService{resolver: resolver}, nil
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
		return cached.(RoleServicer), nil
	}
	svc, err := NewStore(kv)
	if err != nil {
		return nil, err
	}
	actual, _ := r.stores.LoadOrStore(kv, svc)
	return actual.(RoleServicer), nil
}

// requestTenant returns the explicit tenant from a value, defaulting to the
// system tenant when empty.
func requestTenant(explicit string) string {
	if explicit == "" {
		return tenant.SystemTenantID
	}
	return explicit
}

// CreateRole routes on the request's TenantID.
func (r *routingRoleService) CreateRole(ctx context.Context, req *CreateRoleRequest) (*Role, error) {
	tid := tenant.SystemTenantID
	if req != nil {
		tid = requestTenant(req.TenantID)
	}
	svc, err := r.storeFor(ctx, tid)
	if err != nil {
		return nil, err
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

// GetRoleByName routes on the explicit tenant argument.
func (r *routingRoleService) GetRoleByName(ctx context.Context, tenantID, name string) (*Role, error) {
	svc, err := r.storeFor(ctx, requestTenant(tenantID))
	if err != nil {
		return nil, err
	}
	return svc.GetRoleByName(ctx, tenantID, name)
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

// ListRoles routes on the request's TenantID.
func (r *routingRoleService) ListRoles(ctx context.Context, req *ListRolesRequest) (*ListRolesResponse, error) {
	tid := tenant.SystemTenantID
	if req != nil {
		tid = requestTenant(req.TenantID)
	}
	svc, err := r.storeFor(ctx, tid)
	if err != nil {
		return nil, err
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
