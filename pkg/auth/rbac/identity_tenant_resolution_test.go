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
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// authorizeAgainst runs one decision with target bound into the context, the
// way AuthMiddleware.AuthorizeTenant binds it, on a fresh authorizer so the
// permission cache is cold.
func authorizeAgainst(t *testing.T, tenantIDs []string, callerTenant, target string, perm permission.Permission) error {
	t.Helper()
	a := New(authtest.BuiltInRoles(t, tenantIDs...))
	ctx := tenant.WithTenantID(context.Background(), target)
	return a.Authorize(ctx, authtest.AdminIdentity(callerTenant, "admin"), permission.Resource(perm), permission.Action(perm))
}

// TestAuthorize_ColdCacheResolvesRolesInTheIdentitysTenant: the platform
// operator's roles live in the system tenant, and the role store refuses a
// role outside the context tenant. Resolved under the TARGET's context they
// came back not-found, so the operator's first decision against another
// tenant failed.
func TestAuthorize_ColdCacheResolvesRolesInTheIdentitysTenant(t *testing.T) {
	tenants := []string{tenant.SystemTenantID, "acme"}
	if err := authorizeAgainst(t, tenants, tenant.SystemTenantID, "acme", permission.TenantsRead); err != nil {
		t.Fatalf("system admin reading acme on a cold cache = %v, want nil", err)
	}
	if err := authorizeAgainst(t, tenants, tenant.SystemTenantID, "acme", permission.TenantsSetQuotas); err != nil {
		t.Fatalf("system admin setting acme's quotas on a cold cache = %v, want nil", err)
	}
}

// A tenant identity targeting another tenant resolves its OWN roles and is
// then refused by the scope rule -- a permission denial, not a resolution
// failure that happened to deny.
func TestAuthorize_CrossTenantTargetIsAScopeDenialNotAResolveFailure(t *testing.T) {
	err := authorizeAgainst(t, []string{"acme", "globex"}, "acme", "globex", permission.TenantsRead)
	var denied *PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("acme admin reading globex = %v, want *PermissionDeniedError", err)
	}
	var resolve *ResolveError
	if errors.As(err, &resolve) {
		t.Fatalf("acme admin reading globex failed to resolve its own roles: %v", err)
	}
}

// TestAuthorize_ForeignTenantIdentityCannotBorrowTheSystemAdminRole: an acme
// identity whose role list names the SYSTEM tenant's Admin role ID must not
// exercise it, least of all against the system tenant itself. Roles resolve
// under the identity's own tenant, where that role does not exist; resolving
// them under the target tenant is what would have let the system tenant's
// role store answer for it.
func TestAuthorize_ForeignTenantIdentityCannotBorrowTheSystemAdminRole(t *testing.T) {
	borrower := &auth.Identity{
		Subject:  "acme-admin",
		TenantID: "acme",
		Roles:    []string{role.SystemRoleID(tenant.SystemTenantID, role.RoleNameAdmin)},
	}
	for _, perm := range []permission.Permission{
		permission.TenantsRead, permission.TenantsCreate, permission.TenantsSetQuotas, permission.TenantsListCross,
	} {
		a := New(authtest.BuiltInRoles(t, tenant.SystemTenantID, "acme"))
		ctx := tenant.WithTenantID(context.Background(), tenant.SystemTenantID)
		if err := a.Authorize(ctx, borrower, permission.Resource(perm), permission.Action(perm)); err == nil {
			t.Errorf("acme identity holding the system Admin role ID was granted %s against system", perm)
		}
	}
}
