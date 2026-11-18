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

package rbac

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestAuthorize_RolesReconcileAll_PerPosture pins who holds the all-tenant
// built-in role reconcile (#1129): the system-tenant Admin in the standard
// posture, the system-tenant SO only under strict separation of duties. A
// tenant SO never holds it: the permission is cross-scope.
func TestAuthorize_RolesReconcileAll_PerPosture(t *testing.T) {
	ctx := context.Background()
	sysRoles := authtest.BuiltInRoles(t, tenantid.System)
	sysSO, err := sysRoles.GetRoleByName(ctx, tenantid.System, role.RoleNameSO)
	if err != nil {
		t.Fatal(err)
	}
	sysAdmin, err := sysRoles.GetRoleByName(ctx, tenantid.System, role.RoleNameAdmin)
	if err != nil {
		t.Fatal(err)
	}
	acmeRoles := authtest.BuiltInRoles(t, "acme")
	acmeSO, err := acmeRoles.GetRoleByName(ctx, "acme", role.RoleNameSO)
	if err != nil {
		t.Fatal(err)
	}

	so := &auth.Identity{Subject: "so", TenantID: tenantid.System, Roles: []string{sysSO.ID}}
	admin := &auth.Identity{Subject: "admin", TenantID: tenantid.System, Roles: []string{sysAdmin.ID}}
	tenantSO := &auth.Identity{Subject: "tenant-so", TenantID: "acme", Roles: []string{acmeSO.ID}}
	// The system SO held only through a RoleBinding, as the SO deny-list
	// already counts it (Codex r1 P1 on #1129).
	boundSO := &auth.Identity{Subject: "bound-so", TenantID: tenantid.System,
		RoleBindings: []auth.RoleBinding{{RoleID: sysSO.ID, TenantID: tenantid.System}}}

	for _, tc := range []struct {
		name     string
		roles    role.RoleServicer
		identity *auth.Identity
		strict   bool
		allowed  bool
	}{
		{"standard: system Admin", sysRoles, admin, false, true},
		{"standard: system SO", sysRoles, so, false, false},
		{"strict: system SO", sysRoles, so, true, true},
		{"standard: system SO by binding", sysRoles, boundSO, false, false},
		{"strict: system SO by binding", sysRoles, boundSO, true, true},
		{"standard: tenant SO", acmeRoles, tenantSO, false, false},
		{"strict: tenant SO", acmeRoles, tenantSO, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := New(tc.roles, WithStrictSeparation(tc.strict)).
				Authorize(ctxFor(tc.identity.TenantID), tc.identity, "roles", "reconcile_all")
			if tc.allowed && err != nil {
				t.Fatalf("roles:reconcile_all denied: %v", err)
			}
			if !tc.allowed && err == nil {
				t.Fatal("roles:reconcile_all allowed, want denied")
			}
		})
	}
}

// TestListOwnPermissions_ListsWhatAuthorizeGrantsPerPosture pins the listing
// to the authorizer: role.ListOwnPermissions, under each posture, lists
// roles:reconcile_all exactly when Authorize grants it (Codex P1 on #1579).
func TestListOwnPermissions_ListsWhatAuthorizeGrantsPerPosture(t *testing.T) {
	ctx := context.Background()
	sysRoles := authtest.BuiltInRoles(t, tenantid.System)
	sysSO, err := sysRoles.GetRoleByName(ctx, tenantid.System, role.RoleNameSO)
	if err != nil {
		t.Fatal(err)
	}
	sysAdmin, err := sysRoles.GetRoleByName(ctx, tenantid.System, role.RoleNameAdmin)
	if err != nil {
		t.Fatal(err)
	}
	acmeRoles := authtest.BuiltInRoles(t, "acme")
	acmeSO, err := acmeRoles.GetRoleByName(ctx, "acme", role.RoleNameSO)
	if err != nil {
		t.Fatal(err)
	}

	so := &auth.Identity{Subject: "so", TenantID: tenantid.System, Roles: []string{sysSO.ID}}
	admin := &auth.Identity{Subject: "admin", TenantID: tenantid.System, Roles: []string{sysAdmin.ID}}
	tenantSO := &auth.Identity{Subject: "tenant-so", TenantID: "acme", Roles: []string{acmeSO.ID}}
	boundSO := &auth.Identity{Subject: "bound-so", TenantID: tenantid.System,
		RoleBindings: []auth.RoleBinding{{RoleID: sysSO.ID, TenantID: tenantid.System}}}

	for _, tc := range []struct {
		name     string
		roles    role.RoleServicer
		identity *auth.Identity
		strict   bool
		allowed  bool
	}{
		{"standard: system Admin", sysRoles, admin, false, true},
		{"standard: system SO", sysRoles, so, false, false},
		{"strict: system SO", sysRoles, so, true, true},
		{"standard: system SO by binding", sysRoles, boundSO, false, false},
		{"strict: system SO by binding", sysRoles, boundSO, true, true},
		{"standard: tenant SO", acmeRoles, tenantSO, false, false},
		{"strict: tenant SO", acmeRoles, tenantSO, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authErr := New(tc.roles, WithStrictSeparation(tc.strict)).
				Authorize(ctxFor(tc.identity.TenantID), tc.identity, "roles", "reconcile_all")
			if (authErr == nil) != tc.allowed {
				t.Fatalf("Authorize(roles:reconcile_all) = %v, want allowed=%v", authErr, tc.allowed)
			}
			perms, err := role.ListOwnPermissions(auth.WithIdentity(ctxFor(tc.identity.TenantID), tc.identity), tc.roles, tc.strict)
			if err != nil {
				t.Fatal(err)
			}
			listed := false
			for _, p := range perms {
				if p.Name == permission.RolesReconcileAll.String() {
					listed = true
				}
			}
			if listed != tc.allowed {
				t.Fatalf("roles:reconcile_all listed = %v, want %v (as Authorize grants it)", listed, tc.allowed)
			}
		})
	}
}

// TestAuthorize_RolesReconcileAll_CustomRoleWithSOIDIsNotSO pins Codex r2
// P1 on #1129: a custom system-tenant role a caller created with the ID
// "so" is not the built-in SO, so in the strict posture holding it — in
// identity.Roles or through a RoleBinding — earns no SO-exclusive grant,
// neither from Authorize nor from the grant guard's ValidateGrantable.
func TestAuthorize_RolesReconcileAll_CustomRoleWithSOIDIsNotSO(t *testing.T) {
	ctx := context.Background()
	roles := authtest.BuiltInRoles(t, tenantid.System)
	custom, err := roles.CreateRole(ctx, &role.CreateRoleRequest{
		ID:          role.RoleNameSO,
		Name:        "reviewer",
		TenantID:    tenantid.System,
		Permissions: []permission.Permission{"kv:get"},
	})
	if err != nil {
		t.Fatalf("create the custom role: %v", err)
	}
	if custom.ID != role.RoleNameSO {
		t.Fatalf("custom role ID = %q, want %q", custom.ID, role.RoleNameSO)
	}
	authz := New(roles, WithStrictSeparation(true))
	reconcileAll := permission.Permission("roles:reconcile_all")

	for _, tc := range []struct {
		name     string
		identity *auth.Identity
	}{
		{"home roles", &auth.Identity{Subject: "custom-so", TenantID: tenantid.System,
			Roles: []string{custom.ID}}},
		{"via binding", &auth.Identity{Subject: "bound-custom-so", TenantID: tenantid.System,
			RoleBindings: []auth.RoleBinding{{RoleID: custom.ID, TenantID: tenantid.System}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := authz.Authorize(ctxFor(tenantid.System), tc.identity, "roles", "reconcile_all"); err == nil {
				t.Fatal("roles:reconcile_all allowed to a custom role with ID \"so\", want denied")
			}
			gctx := auth.WithIdentity(ctxFor(tenantid.System), tc.identity)
			err := role.ValidateGrantable(gctx, authz, tenantid.System, []permission.Permission{reconcileAll})
			var exceeds *role.GrantExceedsHolderError
			if !errors.As(err, &exceeds) {
				t.Fatalf("ValidateGrantable(roles:reconcile_all) = %v, want *role.GrantExceedsHolderError", err)
			}
		})
	}
}
