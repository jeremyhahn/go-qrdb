// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !integration

package node

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	embeddederr "github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// seedAdminRole creates an Admin role in the given tenant on the node's role
// service and returns the deterministic role ID an identity references. The
// role carries the canonical Admin self-scope permission tenants:read and the
// cross-scope permission tenants:list:cross, so the scope rule (ScopeSelf vs
// ScopeCross) is exercised through the wired authorizer.
func seedAdminRole(t *testing.T, roleSvc role.RoleServicer, tenantID string) string {
	t.Helper()
	// Seeded with the CANONICAL Admin grant set, not a hand-picked pair.
	// A system role name may only be created as the role it denotes
	// (go-qrdb#17), and the canonical set contains both permissions this test
	// needs — tenants:read (self-scope) and tenants:list:cross (cross-scope) —
	// so the scope rule is still exercised, now through the real Admin role
	// rather than a stand-in wearing its name.
	id := role.SystemRoleID(tenantID, role.RoleNameAdmin)
	var adminPerms []permission.Permission
	for _, r := range role.BuiltInRoles(tenantID) {
		if r.Name == role.RoleNameAdmin {
			adminPerms = r.Permissions
			break
		}
	}
	require.NotEmpty(t, adminPerms, "canonical Admin permissions must resolve")
	_, err := roleSvc.CreateRole(context.Background(), &role.CreateRoleRequest{
		ID:          id,
		Name:        role.RoleNameAdmin,
		TenantID:    tenantID,
		Permissions: adminPerms,
	})
	require.NoError(t, err)
	return id
}

// seedNarrowRole creates an ordinary (non-system) role holding exactly the
// permissions given, and returns its id.
//
// A deny test must not seed the canonical Admin role: Admin's grant set is
// broad — it includes keys:delete among 110 permissions — so "the action is
// not in the role" would be a claim about a list this test does not control
// and did not check. A role whose entire grant set is written here cannot
// drift out from under the assertion.
func seedNarrowRole(t *testing.T, roleSvc role.RoleServicer, tenantID, name string, perms ...permission.Permission) string {
	t.Helper()
	r, err := roleSvc.CreateRole(context.Background(), &role.CreateRoleRequest{
		Name:        name,
		TenantID:    tenantID,
		Permissions: perms,
	})
	require.NoError(t, err)
	require.False(t, r.System, "a deny fixture must not be a system role")
	return r.ID
}

// TestEmbeddedNode_RBACAuthorize_Works is the regression guard for the gating
// wall: before this fix the embedded transport defined WithRBACAuthorizer but
// never called it, so node.Client().RBAC().Authorize returned
// MissingRBACAuthorizerError. It now resolves through the qrdb RBAC authorizer
// wired in buildEmbeddedOpts.
func TestEmbeddedNode_RBACAuthorize_Works(t *testing.T) {
	ctx := context.Background()
	n := newMemoryNodeWithUser(t)
	svcNode := n.Services()
	require.NotNil(t, svcNode.Role, "memory node must expose a Role service")

	client := n.Client()
	require.NotNil(t, client, "unsealed memory node must expose an in-process client")
	rbacClient := client.RBAC()
	require.NotNil(t, rbacClient)

	// Seed an Admin role in the system tenant; the identity references it.
	adminRoleID := seedAdminRole(t, svcNode.Role, tenantid.System)

	adminID := &transport.Identity{
		Subject:  "admin",
		TenantID: tenantid.System,
		Roles:    []string{adminRoleID},
	}

	// Permitted: self-scope tenants:read on the admin's own (system) tenant.
	selfCtx := tenant.WithTenantID(ctx, tenantid.System)
	err := rbacClient.Authorize(selfCtx, adminID, "tenants", "read")
	require.NoError(t, err, "wired authorizer must admit admin@system for tenants:read")

	// Crucially: the error is NOT the missing-authorizer wall.
	var missing *embeddederr.MissingRBACAuthorizerError
	assert.NotErrorAs(t, err, &missing, "authorizer must be wired, not missing")
}

// TestEmbeddedNode_RBACAuthorize_DeniesUnpermitted asserts an identity whose
// resolved permissions do not include the requested action is denied with the
// canonical rbac.PermissionDeniedError (and not MissingRBACAuthorizerError).
func TestEmbeddedNode_RBACAuthorize_DeniesUnpermitted(t *testing.T) {
	ctx := context.Background()
	n := newMemoryNodeWithUser(t)
	svcNode := n.Services()

	roleID := seedNarrowRole(t, svcNode.Role, tenantid.System, "reader",
		permission.TenantsRead)
	rbacClient := n.Client().RBAC()

	id := &transport.Identity{
		Subject:  "admin",
		TenantID: tenantid.System,
		Roles:    []string{roleID},
	}

	// The seeded role holds tenants:read and nothing else, so keys:delete
	// must be denied at the permission-resolution layer.
	selfCtx := tenant.WithTenantID(ctx, tenantid.System)
	err := rbacClient.Authorize(selfCtx, id, "keys", "delete")
	require.Error(t, err, "unpermitted action must be denied")

	var denied *rbac.PermissionDeniedError
	assert.ErrorAs(t, err, &denied, "expected rbac.PermissionDeniedError")

	var missing *embeddederr.MissingRBACAuthorizerError
	assert.NotErrorAs(t, err, &missing, "denial must come from the authorizer, not a missing one")
}

// TestEmbeddedNode_RBACAuthorize_ScopeRule exercises the system-tenant scope
// rule transitively through the wired authorizer: a ScopeCross permission is
// admitted for a system-tenant identity but denied for a tenant-scoped one
// holding the very same permission. Each identity resolves in its own tenant
// context (the way the auth middleware binds it after authentication), which
// is also what the shared role store's tenant-ownership guard requires.
func TestEmbeddedNode_RBACAuthorize_ScopeRule(t *testing.T) {
	ctx := context.Background()
	n := newMemoryNodeWithUser(t)
	svcNode := n.Services()
	rbacClient := n.Client().RBAC()

	sysRoleID := seedAdminRole(t, svcNode.Role, tenantid.System)
	acmeRoleID := seedAdminRole(t, svcNode.Role, "acme")

	// System-tenant admin: ScopeCross tenants:list:cross is admitted because
	// the identity sits in the structurally privileged system tenant.
	sysID := &transport.Identity{
		Subject:  "admin",
		TenantID: tenantid.System,
		Roles:    []string{sysRoleID},
	}
	sysCtx := tenant.WithTenantID(ctx, tenantid.System)
	require.NoError(t, rbacClient.Authorize(sysCtx, sysID, "tenants", "list:cross"),
		"system-tenant admin must be admitted for the cross-scope permission")

	// Tenant-scoped admin holding the SAME permission is denied the cross-scope
	// action: the scope rule requires the system tenant.
	acmeID := &transport.Identity{
		Subject:  "admin",
		TenantID: "acme",
		Roles:    []string{acmeRoleID},
	}
	acmeCtx := tenant.WithTenantID(ctx, "acme")
	err := rbacClient.Authorize(acmeCtx, acmeID, "tenants", "list:cross")
	require.Error(t, err, "non-system identity must be denied the cross-scope permission")
	var denied *rbac.PermissionDeniedError
	assert.ErrorAs(t, err, &denied)
}

// TestEmbeddedNode_RBACListPermissions_Works asserts the descriptive
// ListPermissions surface still resolves through the wired role service.
func TestEmbeddedNode_RBACListPermissions_Works(t *testing.T) {
	ctx := context.Background()
	n := newMemoryNodeWithUser(t)
	svcNode := n.Services()
	rbacClient := n.Client().RBAC()

	adminRoleID := seedAdminRole(t, svcNode.Role, tenantid.System)
	id := &transport.Identity{
		Subject:  "admin",
		TenantID: tenantid.System,
		Roles:    []string{adminRoleID},
	}

	perms, err := rbacClient.ListPermissions(ctx, id)
	require.NoError(t, err)
	require.NotEmpty(t, perms)

	byName := make(map[string]transport.PermissionInfo, len(perms))
	for _, p := range perms {
		byName[p.Name] = p
	}
	cross, ok := byName["tenants:list:cross"]
	require.True(t, ok, "expected tenants:list:cross in resolved permissions")
	assert.Equal(t, transport.PermissionScopeCross, cross.Scope)

	self, ok := byName[string(permission.TenantsRead)]
	require.True(t, ok, "expected tenants:read in resolved permissions")
	assert.Equal(t, transport.PermissionScopeSelf, self.Scope)
}
