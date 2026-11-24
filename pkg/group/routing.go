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

package group

import (
	"context"
	"errors"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// routingGroupService routes every group operation to the store of the
// tenant that owns the group, shaped one for one on pkg/role's
// routingRoleService: a dedicated-isolation tenant's groups live inside its
// own barrier, and a sealed tenant's groups fail closed with
// tenant.ErrTenantSealed (the resolver refuses to resolve its store).
type routingGroupService struct {
	resolver tenant.StoreResolver
	identity tenant.IdentityIndex
	opts     []Option // applied to every per-tenant kvGroupStore
	stores   sync.Map // map[kvstore.KVStore]GroupServicer
}

// NewRoutingService returns a GroupServicer that resolves each operation's
// tenant store through resolver. By-id operations find the owning tenant in
// the identity index the per-tenant stores maintain (kind
// tenant.IdentityGroup); tenant-scoped operations use the effective tenant
// of the request. opts (role lookup, member revoker) apply to every
// per-tenant store.
func NewRoutingService(resolver tenant.StoreResolver, opts ...Option) (GroupServicer, error) {
	if resolver == nil {
		return nil, &StoreError{Op: "new_routing", Detail: "store resolver must not be nil"}
	}
	index, err := tenant.NewResolvedIdentityIndex(resolver)
	if err != nil {
		return nil, &StoreError{Op: "new_routing", Detail: "identity index", Err: err}
	}
	return &routingGroupService{
		resolver: resolver,
		identity: index,
		// Appended LAST so a routed deployment always maintains the index
		// the router reads.
		opts: append(append([]Option(nil), opts...), WithIdentityIndex(index)),
	}, nil
}

// targetTenant returns the tenant that owns group id.
func (r *routingGroupService) targetTenant(ctx context.Context, id, op string) (string, error) {
	return tenant.OwningTenant(ctx, r.identity, tenant.IdentityGroup, id, op)
}

// storeFor returns the group store over tenantID's resolved store.
func (r *routingGroupService) storeFor(ctx context.Context, tenantID string) (GroupServicer, error) {
	kv, err := r.resolver.Resolve(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if !kvstore.UsableAsMapKey(kv) {
		return NewStore(kv, r.opts...)
	}
	if cached, ok := r.stores.Load(kv); ok {
		return cached.(GroupServicer), nil //nolint:errcheck // the cache only holds GroupServicer
	}
	svc, err := NewStore(kv, r.opts...)
	if err != nil {
		return nil, err
	}
	actual, _ := r.stores.LoadOrStore(kv, svc)
	return actual.(GroupServicer), nil //nolint:errcheck // the cache only holds GroupServicer
}

// CreateGroup creates the group in the effective tenant's store.
func (r *routingGroupService) CreateGroup(ctx context.Context, req *CreateGroupRequest) (*Group, error) {
	requested := ""
	if req != nil {
		requested = req.TenantID
	}
	eff, err := tenant.EffectiveTenant(ctx, requested, "create group")
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
	return svc.CreateGroup(ctx, req)
}

// GetGroup reads the group from its owning tenant's store.
func (r *routingGroupService) GetGroup(ctx context.Context, id string) (*Group, error) {
	target, err := r.targetTenant(ctx, id, "read group")
	if err != nil {
		return nil, err
	}
	svc, err := r.storeFor(ctx, target)
	if err != nil {
		return nil, err
	}
	return svc.GetGroup(ctx, id)
}

// GetGroupByName reads the effective tenant's group by name.
func (r *routingGroupService) GetGroupByName(ctx context.Context, tenantID, name string) (*Group, error) {
	eff, err := tenant.EffectiveTenant(ctx, tenantID, "read group by name")
	if err != nil {
		return nil, err
	}
	svc, err := r.storeFor(ctx, eff)
	if err != nil {
		return nil, err
	}
	return svc.GetGroupByName(ctx, eff, name)
}

// UpdateGroup updates the group in its owning tenant's store.
func (r *routingGroupService) UpdateGroup(ctx context.Context, req *UpdateGroupRequest) (*Group, error) {
	id := ""
	if req != nil {
		id = req.ID
	}
	target, err := r.targetTenant(ctx, id, "update group")
	if err != nil {
		return nil, err
	}
	svc, err := r.storeFor(ctx, target)
	if err != nil {
		return nil, err
	}
	return svc.UpdateGroup(ctx, req)
}

// ClaimGroup claims the group in its owning tenant's store.
func (r *routingGroupService) ClaimGroup(ctx context.Context, id, op string, guard func(*Group) error) (*Group, string, error) {
	target, err := r.targetTenant(ctx, id, "claim group")
	if err != nil {
		return nil, "", err
	}
	svc, err := r.storeFor(ctx, target)
	if err != nil {
		return nil, "", err
	}
	return svc.ClaimGroup(ctx, id, op, guard)
}

// ReleaseClaim releases a claim on the group in its owning tenant's store.
func (r *routingGroupService) ReleaseClaim(ctx context.Context, id, token string) error {
	target, err := r.targetTenant(ctx, id, "release group claim")
	if err != nil {
		return err
	}
	svc, err := r.storeFor(ctx, target)
	if err != nil {
		return err
	}
	return svc.ReleaseClaim(ctx, id, token)
}

// DeleteGroup deletes the group from its owning tenant's store. When the
// store no longer holds the group but the index still points at the tenant
// (a delete that failed after removing the record, or a deleted tenant), the
// stale claim is reconciled and the delete reports success.
func (r *routingGroupService) DeleteGroup(ctx context.Context, id string) error {
	target, err := r.targetTenant(ctx, id, "delete group")
	if err != nil {
		return err
	}
	svc, err := r.storeFor(ctx, target)
	if err != nil {
		return err
	}
	derr := svc.DeleteGroup(ctx, id)
	var notFound *GroupNotFoundError
	if !errors.As(derr, &notFound) {
		return derr
	}
	released, rerr := tenant.ReconcileDeletedIdentity(ctx, r.identity, tenant.IdentityGroup, id, target)
	if rerr != nil {
		return rerr
	}
	if released {
		return nil
	}
	return derr
}

// ListGroups lists the effective tenant's groups from its store.
func (r *routingGroupService) ListGroups(ctx context.Context, req *ListGroupsRequest) (*ListGroupsResponse, error) {
	requested := ""
	if req != nil {
		requested = req.TenantID
	}
	eff, err := tenant.EffectiveTenant(ctx, requested, "list groups")
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
	return svc.ListGroups(ctx, req)
}
