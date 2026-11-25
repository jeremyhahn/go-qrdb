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

// TestAuthorize_StrictAdmin_CustomRoleWithSOIDIsNotSO covers the mirror of
// TestAuthorize_StrictSO_CustomRoleWithAdminIDIsNotAdmin: the combined-role
// check matches SO by its canonical (persisted) ID only, so a tenant's custom
// role given the caller-supplied ID "so" does not make an Admin an SO+Admin.
func TestAuthorize_StrictAdmin_CustomRoleWithSOIDIsNotSO(t *testing.T) {
	ctx := context.Background()
	roles := authtest.BuiltInRoles(t, "acme")
	custom, err := roles.CreateRole(ctx, &role.CreateRoleRequest{
		ID:          role.RoleNameSO,
		Name:        "reviewer",
		TenantID:    "acme",
		Permissions: []permission.Permission{"kv:get"},
	})
	if err != nil {
		t.Fatalf("create the custom role: %v", err)
	}
	if custom.ID != role.RoleNameSO {
		t.Fatalf("custom role ID = %q, want %q", custom.ID, role.RoleNameSO)
	}
	admin, err := roles.GetRoleByName(ctx, "acme", role.RoleNameAdmin)
	if err != nil {
		t.Fatal(err)
	}
	authz := New(roles, WithStrictSeparation(true))

	for _, tc := range []struct {
		name     string
		identity *auth.Identity
	}{
		{
			name: "home roles",
			identity: &auth.Identity{Subject: "admin", TenantID: "acme",
				Roles: []string{admin.ID, custom.ID}},
		},
		{
			name: "custom role via binding",
			identity: &auth.Identity{Subject: "admin", TenantID: "acme",
				Roles:        []string{admin.ID},
				RoleBindings: []auth.RoleBinding{{RoleID: custom.ID, TenantID: "acme"}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := authz.Authorize(ctxFor("acme"), tc.identity, "users", "list")
			var conflict *auth.SORoleConflictError
			if errors.As(err, &conflict) {
				t.Fatalf("an Admin holding a custom role with ID %q was refused as SO+Admin: %v", custom.ID, err)
			}
			if err != nil {
				t.Fatalf("users:list through the Admin role: %v", err)
			}
		})
	}

	// The built-in SO role, by its persisted ID, still conflicts.
	so, err := roles.GetRoleByName(ctx, "acme", role.RoleNameSO)
	if err != nil {
		t.Fatal(err)
	}
	both := &auth.Identity{Subject: "both", TenantID: "acme", Roles: []string{so.ID, admin.ID}}
	err = authz.Authorize(ctxFor("acme"), both, "users", "list")
	var conflict *auth.SORoleConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("built-in SO holding the built-in Admin role: %v, want *auth.SORoleConflictError", err)
	}
}

// TestAuthorize_StrictSystemAdminWithBoundSO_RefusedOnEveryTarget asserts the
// SO/Admin invariant reads the identity's home-tenant bindings whatever tenant
// the request targets: a system identity holding the built-in Admin in Roles
// and the built-in system SO by binding is refused on a request against its
// own tenant AND on one against another tenant (docs/SPEC.md: no principal
// combines SO and Admin).
func TestAuthorize_StrictSystemAdminWithBoundSO_RefusedOnEveryTarget(t *testing.T) {
	ctx := context.Background()
	roles := authtest.BuiltInRoles(t, tenantid.System)
	admin, err := roles.GetRoleByName(ctx, tenantid.System, role.RoleNameAdmin)
	if err != nil {
		t.Fatal(err)
	}
	so, err := roles.GetRoleByName(ctx, tenantid.System, role.RoleNameSO)
	if err != nil {
		t.Fatal(err)
	}
	authz := New(roles, WithStrictSeparation(true))
	identity := &auth.Identity{Subject: "sysadmin", TenantID: tenantid.System,
		Roles:        []string{admin.ID},
		RoleBindings: []auth.RoleBinding{{RoleID: so.ID, TenantID: tenantid.System}}}

	for _, target := range []string{tenantid.System, "acme"} {
		t.Run(target, func(t *testing.T) {
			err := authz.Authorize(ctxFor(target), identity, "admin", "drain")
			var conflict *auth.SORoleConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("admin:drain targeting %q by a system Admin with a bound system SO: %v, want *auth.SORoleConflictError", target, err)
			}
			if conflict.TenantID != tenantid.System {
				t.Errorf("conflict tenant = %q, want %q", conflict.TenantID, tenantid.System)
			}
		})
	}
}
