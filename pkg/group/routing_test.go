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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// routeResolver resolves a tenant to its own store when it has one (a
// dedicated-isolation tenant behind its barrier), else to the shared store,
// and refuses a sealed tenant with tenant.ErrTenantSealed.
type routeResolver struct {
	shared  kvstore.KVStore
	tenants map[string]kvstore.KVStore
	sealed  map[string]bool
}

func (r *routeResolver) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	if r.sealed[tenantID] {
		return nil, tenant.ErrTenantSealed
	}
	if kv, ok := r.tenants[tenantID]; ok {
		return kv, nil
	}
	return r.shared, nil
}

func newRouted(t *testing.T) (*routeResolver, GroupServicer) {
	t.Helper()
	resolver := &routeResolver{
		shared:  kvstore.NewMemoryKVStore(),
		tenants: map[string]kvstore.KVStore{"vault": kvstore.NewMemoryKVStore()},
		sealed:  map[string]bool{},
	}
	svc, err := NewRoutingService(resolver, WithMemberRevoker(&recordingRevoker{}))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	return resolver, svc
}

func TestNewRoutingGroupService_NilResolver(t *testing.T) {
	if _, err := NewRoutingService(nil); err == nil {
		t.Fatal("NewRoutingService(nil) succeeded")
	}
}

// A dedicated-isolation tenant's group is written inside its own store, not
// the shared one, and every by-id operation reaches it there.
func TestRoutingGroupService_BarrierTenantUsesOwnStore(t *testing.T) {
	resolver, svc := newRouted(t)
	ctx := tenant.WithTenantID(context.Background(), "vault")
	g, err := svc.CreateGroup(ctx, &CreateGroupRequest{TenantID: "vault", Name: "keepers"})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	own, err := NewStore(resolver.tenants["vault"])
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := own.GetGroup(ctx, g.ID); err != nil {
		t.Fatalf("group not in the tenant's own store: %v", err)
	}
	shared, err := NewStore(resolver.shared)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	var nf *GroupNotFoundError
	if _, err := shared.GetGroup(ctx, g.ID); !errors.As(err, &nf) {
		t.Fatalf("group leaked into the shared store: %v", err)
	}
	if got, err := svc.GetGroup(ctx, g.ID); err != nil || got.Name != "keepers" {
		t.Fatalf("GetGroup = %+v, %v", got, err)
	}
	if got, err := svc.GetGroupByName(ctx, "vault", "keepers"); err != nil || got.ID != g.ID {
		t.Fatalf("GetGroupByName = %+v, %v", got, err)
	}
	desc := "vault keepers"
	if got, err := svc.UpdateGroup(ctx, &UpdateGroupRequest{ID: g.ID, Description: &desc}); err != nil || got.Description != desc {
		t.Fatalf("UpdateGroup = %+v, %v", got, err)
	}
	if list, err := svc.ListGroups(ctx, &ListGroupsRequest{TenantID: "vault"}); err != nil || list.Total != 1 {
		t.Fatalf("ListGroups = %+v, %v", list, err)
	}
	acme := tenant.WithTenantID(context.Background(), "acme")
	if list, err := svc.ListGroups(acme, &ListGroupsRequest{TenantID: "acme"}); err != nil || list.Total != 0 {
		t.Fatalf("acme ListGroups = %+v, %v, want empty", list, err)
	}
	if _, err := svc.GetGroup(acme, g.ID); !errors.As(err, &nf) {
		t.Fatalf("acme GetGroup = %v, want *GroupNotFoundError", err)
	}
	if err := svc.DeleteGroup(ctx, g.ID); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if _, err := own.GetGroup(ctx, g.ID); !errors.As(err, &nf) {
		t.Fatalf("group after delete = %v, want *GroupNotFoundError", err)
	}
}

// A sealed tenant's groups fail closed: no read, update, delete or list
// reaches anywhere else.
func TestRoutingGroupService_SealedTenantFailsClosed(t *testing.T) {
	resolver, svc := newRouted(t)
	ctx := tenant.WithTenantID(context.Background(), "vault")
	g, err := svc.CreateGroup(ctx, &CreateGroupRequest{TenantID: "vault", Name: "keepers"})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	resolver.sealed["vault"] = true
	if _, err := svc.GetGroup(ctx, g.ID); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("GetGroup = %v, want ErrTenantSealed", err)
	}
	if _, err := svc.GetGroupByName(ctx, "vault", "keepers"); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("GetGroupByName = %v, want ErrTenantSealed", err)
	}
	if _, err := svc.UpdateGroup(ctx, &UpdateGroupRequest{ID: g.ID}); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("UpdateGroup = %v, want ErrTenantSealed", err)
	}
	if err := svc.DeleteGroup(ctx, g.ID); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("DeleteGroup = %v, want ErrTenantSealed", err)
	}
	if _, err := svc.ListGroups(ctx, &ListGroupsRequest{TenantID: "vault"}); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("ListGroups = %v, want ErrTenantSealed", err)
	}
	if _, err := svc.CreateGroup(ctx, &CreateGroupRequest{TenantID: "vault", Name: "more"}); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("CreateGroup = %v, want ErrTenantSealed", err)
	}
}

// The routed store claims each group id in the identity index and a delete
// releases it; a delete whose record is already gone (an interrupted delete,
// or the tenant's store dropped) reconciles the stale claim and succeeds.
func TestRoutingGroupService_DeletedGroupClaimIsReconciled(t *testing.T) {
	resolver, svc := newRouted(t)
	ctx := tenant.WithTenantID(context.Background(), "vault")
	index, err := tenant.NewResolvedIdentityIndex(resolver)
	if err != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", err)
	}
	g, err := svc.CreateGroup(ctx, &CreateGroupRequest{TenantID: "vault", Name: "keepers"})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	claim, held, err := index.Lookup(ctx, tenant.IdentityGroup, g.ID)
	if err != nil || !held || claim.Tenant != "vault" {
		t.Fatalf("claim = %+v held=%v err=%v, want held by vault", claim, held, err)
	}
	// Remove the record behind the router's back: the claim survives.
	bare, err := NewStore(resolver.tenants["vault"], WithMemberRevoker(&recordingRevoker{}))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := bare.DeleteGroup(ctx, g.ID); err != nil {
		t.Fatalf("bare DeleteGroup: %v", err)
	}
	if _, held, _ := index.Lookup(ctx, tenant.IdentityGroup, g.ID); !held {
		t.Fatal("precondition: the claim should outlive the record")
	}
	if err := svc.DeleteGroup(ctx, g.ID); err != nil {
		t.Fatalf("DeleteGroup of a stale claim = %v, want nil", err)
	}
	if _, held, err := index.Lookup(ctx, tenant.IdentityGroup, g.ID); err != nil || held {
		t.Fatalf("claim after reconcile held=%v err=%v, want released", held, err)
	}
	var nf *GroupNotFoundError
	if err := svc.DeleteGroup(ctx, "no-such-group"); !errors.As(err, &nf) {
		t.Fatalf("DeleteGroup unknown = %v, want *GroupNotFoundError", err)
	}

	// An ordinary routed delete releases the claim itself.
	g2, err := svc.CreateGroup(ctx, &CreateGroupRequest{TenantID: "vault", Name: "second"})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if err := svc.DeleteGroup(ctx, g2.ID); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if _, held, err := index.Lookup(ctx, tenant.IdentityGroup, g2.ID); err != nil || held {
		t.Fatalf("claim after delete held=%v err=%v, want released", held, err)
	}
}

func TestRoutingGroupService_ClaimReleaseAndRevoke(t *testing.T) {
	resolver := &routeResolver{
		shared:  kvstore.NewMemoryKVStore(),
		tenants: map[string]kvstore.KVStore{"vault": kvstore.NewMemoryKVStore()},
		sealed:  map[string]bool{},
	}
	svc, err := NewRoutingService(resolver, WithMemberRevoker(&recordingRevoker{}),
		WithRoleLookup(stubRoles{r: &role.Role{ID: "r1", TenantID: "vault"}}))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "vault")
	g, err := svc.CreateGroup(ctx, &CreateGroupRequest{TenantID: "vault", Name: "keepers", RoleIDs: []string{"r1"}})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	_, token, err := svc.ClaimGroup(ctx, g.ID, ClaimMember, nil)
	if err != nil {
		t.Fatalf("ClaimGroup: %v", err)
	}
	if err := svc.ReleaseClaim(ctx, g.ID, token); err != nil {
		t.Fatalf("ReleaseClaim: %v", err)
	}
	if _, _, err := svc.ClaimGroup(ctx, "no-such-group", ClaimMember, nil); err == nil {
		t.Fatal("ClaimGroup of an unknown id succeeded")
	}
	if err := svc.ReleaseClaim(ctx, "no-such-group", "tok"); err != nil {
		t.Fatalf("ReleaseClaim of an unknown id = %v, want nil", err)
	}

	revoker, err := NewRoutingRoleRevoker(resolver)
	if err != nil {
		t.Fatalf("NewRoutingRoleRevoker: %v", err)
	}
	if n, err := revoker.RevokeRole(ctx, "vault", "r1"); err != nil || n != 1 {
		t.Fatalf("RevokeRole = %d, %v; want 1, nil", n, err)
	}
	if got, err := svc.GetGroup(ctx, g.ID); err != nil || len(got.RoleIDs) != 0 {
		t.Fatalf("group after RevokeRole = %+v, %v; want no roles", got, err)
	}

	routed := svc.(*routingGroupService)
	inner, err := NewStore(resolver.shared)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	routed.stores.Store(resolver.shared, struct{ GroupServicer }{inner})
	_, err = routed.RevokeRole(tenant.WithTenantID(context.Background(), "acme"), "acme", "r1")
	wantErr[*StoreError](t, "RevokeRole through a non-revoker", err)

	resolver.sealed["vault"] = true
	if _, _, err := svc.ClaimGroup(ctx, g.ID, ClaimMember, nil); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("sealed ClaimGroup = %v", err)
	}
	if err := svc.ReleaseClaim(ctx, g.ID, token); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("sealed ReleaseClaim = %v", err)
	}
	_, err = revoker.RevokeRole(ctx, "vault", "r1")
	wantErr[*StoreError](t, "sealed RevokeRole", err)
}

type resolverFunc func(ctx context.Context, tenantID string) (kvstore.KVStore, error)

func (f resolverFunc) Resolve(ctx context.Context, tenantID string) (kvstore.KVStore, error) {
	return f(ctx, tenantID)
}

// uncomparableKV is an atomic store whose value cannot key a map.
type uncomparableKV struct {
	*kvstore.MemoryKVStore
	_ []int
}

func TestRoutingGroupService_StoreForAndNilRequests(t *testing.T) {
	ctx := tenant.WithTenantID(context.Background(), "acme")
	kv := uncomparableKV{MemoryKVStore: kvstore.NewMemoryKVStore()}
	svc, err := NewRoutingService(resolverFunc(func(context.Context, string) (kvstore.KVStore, error) { return kv, nil }))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	g, err := svc.CreateGroup(ctx, &CreateGroupRequest{TenantID: "acme", Name: "eng"})
	if err != nil {
		t.Fatalf("CreateGroup over an uncomparable store: %v", err)
	}
	if got, err := svc.GetGroup(ctx, g.ID); err != nil || got.Name != "eng" {
		t.Fatalf("GetGroup = %+v, %v", got, err)
	}
	_, err = svc.CreateGroup(ctx, nil)
	wantErr[*ValidationError](t, "CreateGroup(nil)", err)
	_, err = svc.ListGroups(ctx, nil)
	wantErr[*ValidationError](t, "ListGroups(nil)", err)
	_, err = svc.CreateGroup(ctx, &CreateGroupRequest{TenantID: "other", Name: "x"})
	wantErr[*tenant.CrossTenantDeniedError](t, "cross-tenant CreateGroup", err)
	if _, err := svc.GetGroup(ctx, "no-such-group"); err == nil {
		t.Fatal("GetGroup unknown succeeded")
	}
	if _, err := svc.UpdateGroup(ctx, &UpdateGroupRequest{ID: "no-such-group"}); err == nil {
		t.Fatal("UpdateGroup unknown succeeded")
	}
	if _, err := svc.UpdateGroup(ctx, nil); err == nil {
		t.Fatal("UpdateGroup(nil) succeeded")
	}
	_, err = svc.GetGroupByName(ctx, "acme", "nobody")
	wantErr[*GroupNotFoundError](t, "GetGroupByName unknown", err)

	na, err := NewRoutingService(resolverFunc(func(context.Context, string) (kvstore.KVStore, error) {
		return nonAtomicKV{KVStore: kvstore.NewMemoryKVStore()}, nil
	}))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	if _, err := na.ListGroups(ctx, &ListGroupsRequest{TenantID: "acme"}); !errors.Is(err, ErrStoreNotAtomic) {
		t.Fatalf("ListGroups over a non-atomic store = %v", err)
	}
}
