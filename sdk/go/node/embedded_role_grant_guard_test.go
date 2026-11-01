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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// grantGuardTenant is the tenant whose Admin writes roles in these tests.
const grantGuardTenant = "acme"

// tenantAdminContext returns a context carrying a tenant Admin of
// grantGuardTenant, bound to that tenant as the auth middleware binds it, on a
// memory node with RBAC on, with a custom role holding tenants:read already in
// the tenant. It returns the node's embedded role client and the custom
// role's id.
func tenantAdminContext(t *testing.T) (context.Context, transport.RoleClient, string) {
	t.Helper()
	n := newMemoryNodeWithUser(t)
	svcNode := n.Services()
	adminRoleID := seedAdminRole(t, svcNode.Role, grantGuardTenant)
	customRoleID := seedNarrowRole(t, svcNode.Role, grantGuardTenant, "custom", permission.TenantsRead)
	ctx := tenant.WithTenantID(auth.WithIdentity(context.Background(), &auth.Identity{
		Subject:  "acme-admin",
		TenantID: grantGuardTenant,
		Roles:    []string{adminRoleID},
	}), grantGuardTenant)
	return ctx, n.Client().Roles(), customRoleID
}

// wantGrantExceeds fails unless err is the grant guard's refusal naming perm.
func wantGrantExceeds(t *testing.T, err error, perm permission.Permission) {
	t.Helper()
	var exceeds *role.GrantExceedsHolderError
	if !errors.As(err, &exceeds) {
		t.Fatalf("err = %v (%T), want *role.GrantExceedsHolderError", err, err)
	}
	if exceeds.Permission != perm.String() || exceeds.TenantID != grantGuardTenant {
		t.Fatalf("refusal names %s in %s, want %s in %s", exceeds.Permission, exceeds.TenantID, perm, grantGuardTenant)
	}
}

// TestEmbeddedUpdateRole_RefusesWideningPastTheCaller proves the embedded
// transport's role writes go through the grant guard buildEmbeddedOpts wires:
// a tenant Admin cannot widen a custom role to keys:sign, which Admin does not
// hold, and the role keeps the permissions it had.
func TestEmbeddedUpdateRole_RefusesWideningPastTheCaller(t *testing.T) {
	ctx, roles, customRoleID := tenantAdminContext(t)
	_, err := roles.UpdateRole(ctx, &transport.UpdateRoleRequest{
		ID:          customRoleID,
		Permissions: []string{string(permission.TenantsRead), string(permission.KeysSign)},
	})
	wantGrantExceeds(t, err, permission.KeysSign)

	got, err := roles.GetRole(ctx, customRoleID)
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if len(got.Permissions) != 1 || got.Permissions[0] != string(permission.TenantsRead) {
		t.Fatalf("role permissions = %v after a refused update, want [%s]", got.Permissions, permission.TenantsRead)
	}
}

// TestEmbeddedCreateRole_RefusesAGrantPastTheCaller proves a tenant Admin
// cannot create a role carrying keys:sign over the embedded transport.
func TestEmbeddedCreateRole_RefusesAGrantPastTheCaller(t *testing.T) {
	ctx, roles, _ := tenantAdminContext(t)
	_, err := roles.CreateRole(ctx, &transport.CreateRoleRequest{
		Name:        "signer",
		TenantID:    grantGuardTenant,
		Permissions: []string{string(permission.KeysSign)},
	})
	wantGrantExceeds(t, err, permission.KeysSign)
}

// TestEmbeddedRoleWrites_AdmitGrantsTheCallerHolds proves the guard admits a
// grant within the Admin's own permissions, on create and on update.
func TestEmbeddedRoleWrites_AdmitGrantsTheCallerHolds(t *testing.T) {
	ctx, roles, customRoleID := tenantAdminContext(t)
	held := []string{string(permission.TenantsRead), string(permission.RolesList)}
	if _, err := roles.CreateRole(ctx, &transport.CreateRoleRequest{
		Name: "reader", TenantID: grantGuardTenant, Permissions: held,
	}); err != nil {
		t.Fatalf("CreateRole within the Admin's permissions: %v", err)
	}
	updated, err := roles.UpdateRole(ctx, &transport.UpdateRoleRequest{ID: customRoleID, Permissions: held})
	if err != nil {
		t.Fatalf("UpdateRole within the Admin's permissions: %v", err)
	}
	if len(updated.Permissions) != len(held) {
		t.Fatalf("updated permissions = %v, want %v", updated.Permissions, held)
	}
}
