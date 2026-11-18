package rbac

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
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
