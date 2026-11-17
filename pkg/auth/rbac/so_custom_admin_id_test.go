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

// A tenant may create a custom role whose caller-supplied ID is the literal
// "admin". A separate SO holding that role holds no Admin role, so strict
// separation must not refuse it as an SO/Admin combination: Admin is the
// built-in role's persisted ID, never a custom role that happens to share
// the role's name as its ID.
func TestAuthorize_StrictSO_CustomRoleWithAdminIDIsNotAdmin(t *testing.T) {
	ctx := context.Background()
	roles := authtest.BuiltInRoles(t, "acme")
	custom, err := roles.CreateRole(ctx, &role.CreateRoleRequest{
		ID:          role.RoleNameAdmin,
		Name:        "reader",
		TenantID:    "acme",
		Permissions: []permission.Permission{"kv:get"},
	})
	if err != nil {
		t.Fatalf("create the custom role: %v", err)
	}
	if custom.ID != role.RoleNameAdmin {
		t.Fatalf("custom role ID = %q, want %q", custom.ID, role.RoleNameAdmin)
	}

	so := &auth.Identity{Subject: "secofficer", TenantID: "acme",
		Roles: []string{role.SystemRoleID("acme", role.RoleNameSO), custom.ID}}
	err = New(roles, WithStrictSeparation(true)).Authorize(ctxFor("acme"), so, "kv", "get")
	var conflict *auth.SORoleConflictError
	if errors.As(err, &conflict) {
		t.Fatalf("a separate SO holding a custom role with ID %q was refused as SO+Admin: %v", custom.ID, err)
	}
	if err != nil {
		t.Fatalf("kv:get through the custom role: %v", err)
	}

	// The built-in Admin role, by its persisted ID, still conflicts.
	admin, err := roles.GetRoleByName(ctx, "acme", role.RoleNameAdmin)
	if err != nil {
		t.Fatal(err)
	}
	both := &auth.Identity{Subject: "both", TenantID: "acme",
		Roles: []string{role.SystemRoleID("acme", role.RoleNameSO), admin.ID}}
	err = New(roles, WithStrictSeparation(true)).Authorize(ctxFor("acme"), both, "kv", "get")
	if !errors.As(err, &conflict) {
		t.Fatalf("SO holding the built-in Admin role: %v, want *auth.SORoleConflictError", err)
	}
}
