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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// createAdminRole seeds tenantID's built-in roles the way bootstrap does
// (ReconcileBuiltInRoles, so each sits under its canonical id with its
// canonical grants) and returns the Admin role the store persisted.
func createAdminRole(t *testing.T, svc RoleServicer, tenantID string) *Role {
	t.Helper()
	ctx := tenant.WithTenantID(context.Background(), tenantID)
	if _, err := svc.ReconcileBuiltInRoles(ctx, tenantID); err != nil {
		t.Fatalf("ReconcileBuiltInRoles(%s): %v", tenantID, err)
	}
	r, err := svc.GetRoleByName(ctx, tenantID, RoleNameAdmin)
	if err != nil {
		t.Fatalf("GetRoleByName(admin, %s): %v", tenantID, err)
	}
	return r
}

func TestIsTenantOwner_TenantAdminIsOwner(t *testing.T) {
	svc := newTestStore(t)
	admin := createAdminRole(t, svc, "acme")

	owner, err := IsTenantOwner(context.Background(), svc, &auth.Identity{TenantID: "acme", Roles: []string{admin.ID}})
	if err != nil {
		t.Fatalf("IsTenantOwner: %v", err)
	}
	if !owner {
		t.Fatal("tenant Admin must be the tenant owner")
	}
}

func TestIsTenantOwner_NonAdminIsNotOwner(t *testing.T) {
	svc := newTestStore(t)
	createAdminRole(t, svc, "acme")
	viewer := createTestRole(t, svc, "viewer", "acme")

	owner, err := IsTenantOwner(context.Background(), svc, &auth.Identity{TenantID: "acme", Roles: []string{viewer.ID}})
	if err != nil {
		t.Fatalf("IsTenantOwner: %v", err)
	}
	if owner {
		t.Fatal("a non-Admin role must not make its holder the tenant owner")
	}
}

// A role NAME on the identity is not a grant: only the persisted id is.
func TestIsTenantOwner_RoleNameDoesNotMatch(t *testing.T) {
	svc := newTestStore(t)
	createAdminRole(t, svc, "acme")

	owner, err := IsTenantOwner(context.Background(), svc, &auth.Identity{TenantID: "acme", Roles: []string{RoleNameAdmin}})
	if err != nil {
		t.Fatalf("IsTenantOwner: %v", err)
	}
	if owner {
		t.Fatal("the role name must not resolve to the Admin role")
	}
}

// A tenant whose Admin role was never seeded has no owner, even for a
// caller presenting that role's canonical id.
func TestIsTenantOwner_UnseededAdminIsNotOwner(t *testing.T) {
	svc := newTestStore(t)

	owner, err := IsTenantOwner(context.Background(), svc, &auth.Identity{
		TenantID: "acme",
		Roles:    []string{SystemRoleID("acme", RoleNameAdmin)},
	})
	if err != nil {
		t.Fatalf("IsTenantOwner: %v", err)
	}
	if owner {
		t.Fatal("a tenant with no Admin role grants no ownership")
	}
}

func TestIsTenantOwner_SystemAdminOwnsOnlySystemTenant(t *testing.T) {
	svc := newTestStore(t)
	sysAdmin := createAdminRole(t, svc, tenantid.System)
	createAdminRole(t, svc, "acme")
	ctx := context.Background()

	owner, err := IsTenantOwner(ctx, svc, &auth.Identity{TenantID: tenantid.System, Roles: []string{sysAdmin.ID}})
	if err != nil {
		t.Fatalf("IsTenantOwner(system): %v", err)
	}
	if !owner {
		t.Fatal("system-tenant Admin must own the system tenant")
	}

	// The same grant presented under another tenant owns nothing there.
	owner, err = IsTenantOwner(ctx, svc, &auth.Identity{TenantID: "acme", Roles: []string{sysAdmin.ID}})
	if err != nil {
		t.Fatalf("IsTenantOwner(acme): %v", err)
	}
	if owner {
		t.Fatal("a system-tenant Admin grant must confer no ownership of another tenant")
	}
}

func TestIsTenantOwner_EmptyTenantIsSystem(t *testing.T) {
	svc := newTestStore(t)
	sysAdmin := createAdminRole(t, svc, tenantid.System)

	owner, err := IsTenantOwner(context.Background(), svc, &auth.Identity{Roles: []string{sysAdmin.ID}})
	if err != nil {
		t.Fatalf("IsTenantOwner: %v", err)
	}
	if !owner {
		t.Fatal("an empty TenantID is the system tenant")
	}
}

// The decision is read from the role store each time it is asked: a role
// cannot be renamed INTO Admin, and the identity that no longer carries the
// Admin role id (its assignment withdrawn) is no longer the owner.
func TestIsTenantOwner_RenameAndRemovalReflected(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	admin := createAdminRole(t, svc, "acme")
	ops := createTestRole(t, svc, "ops", "acme")

	if _, err := svc.UpdateRole(ctx, &UpdateRoleRequest{ID: ops.ID, Name: RoleNameAdmin}); err == nil {
		t.Fatal("renaming a custom role to the Admin name must be refused")
	}
	owner, err := IsTenantOwner(ctx, svc, &auth.Identity{TenantID: "acme", Roles: []string{ops.ID}})
	if err != nil {
		t.Fatalf("IsTenantOwner after refused rename: %v", err)
	}
	if owner {
		t.Fatal("a custom role must not become the owner role by any rename")
	}

	if err := svc.DeleteRole(ctx, admin.ID); err == nil {
		t.Fatal("the Admin system role must not be deletable")
	}
	if owner, err = IsTenantOwner(ctx, svc, &auth.Identity{TenantID: "acme", Roles: []string{ops.ID, admin.ID}}); err != nil || !owner {
		t.Fatalf("holder of Admin: owner=%v err=%v", owner, err)
	}
	owner, err = IsTenantOwner(ctx, svc, &auth.Identity{TenantID: "acme", Roles: []string{ops.ID}})
	if err != nil {
		t.Fatalf("IsTenantOwner after removal: %v", err)
	}
	if owner {
		t.Fatal("an identity whose Admin assignment was removed must not be the owner")
	}
}

func TestIsTenantOwner_BindingInOwnTenantCounts(t *testing.T) {
	svc := newTestStore(t)
	admin := createAdminRole(t, svc, "acme")

	owner, err := IsTenantOwner(context.Background(), svc, &auth.Identity{
		TenantID:     "acme",
		RoleBindings: []auth.RoleBinding{{RoleID: admin.ID, TenantID: "acme"}},
	})
	if err != nil {
		t.Fatalf("IsTenantOwner: %v", err)
	}
	if !owner {
		t.Fatal("an Admin binding in the identity's own tenant must make it the owner")
	}
}

func TestIsTenantOwner_BindingInOtherTenantIgnored(t *testing.T) {
	svc := newTestStore(t)
	admin := createAdminRole(t, svc, "acme")
	// backing is the full array behind roles, so an append into the
	// caller's spare capacity shows up at backing[1].
	backing := make([]string, 4)
	roles := backing[:1]
	roles[0] = "r-1"

	owner, err := IsTenantOwner(context.Background(), svc, &auth.Identity{
		TenantID:     "globex",
		Roles:        roles,
		RoleBindings: []auth.RoleBinding{{RoleID: admin.ID, TenantID: "acme"}, {RoleID: "r-2", TenantID: "globex"}},
	})
	if err != nil {
		t.Fatalf("IsTenantOwner: %v", err)
	}
	if owner {
		t.Fatal("a binding granted in another tenant must not count")
	}
	if got := backing[1]; got != "" {
		t.Fatalf("IsTenantOwner must not write into the identity's Roles backing array, found %q", got)
	}
}

func TestIsTenantOwner_NilIdentity(t *testing.T) {
	owner, err := IsTenantOwner(context.Background(), newTestStore(t), nil)
	if err != nil || owner {
		t.Fatalf("nil identity: owner=%v err=%v", owner, err)
	}
}

func TestIsTenantOwner_NilResolverRefused(t *testing.T) {
	_, err := IsTenantOwner(context.Background(), nil, &auth.Identity{TenantID: "acme", Roles: []string{"r-1"}})
	var want *ResolverRequiredError
	if !errors.As(err, &want) {
		t.Fatalf("want *ResolverRequiredError, got %v", err)
	}
}

func TestIsTenantOwner_StoreErrorReturned(t *testing.T) {
	_, err := IsTenantOwner(context.Background(), errNameResolver{err: errors.New("store unavailable")}, &auth.Identity{TenantID: "acme", Roles: []string{"r-1"}})
	if err == nil {
		t.Fatal("a store error must be returned, not read as \"not owner\"")
	}
}
