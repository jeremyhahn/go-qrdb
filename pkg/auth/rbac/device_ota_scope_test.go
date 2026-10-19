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
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// devicePermissions are the device-onboarding and OTA permissions: each
// tenant onboards, activates and confirms for its own devices only.
var devicePermissions = []permission.Permission{
	permission.DevicesOnboard,
	permission.OTAActivate,
	permission.OTAConfirm,
}

// authorizeDevice decides perm for a caller holding the named built-in role in
// callerTenant against target, the tenant a device handler names (as
// AuthMiddleware.AuthorizeTenant injects it). The roles resolve through the
// real built-in role store seeded into every tenant, so the decision is the
// one a request on any transport gets.
func authorizeDevice(t *testing.T, roleName, callerTenant, target string, perm permission.Permission) error {
	t.Helper()
	a := New(authtest.BuiltInRoles(t, tenantid.System, "acme", "globex"))
	id := &auth.Identity{
		Subject:  roleName + "@" + callerTenant,
		TenantID: callerTenant,
		Roles:    []string{role.SystemRoleID(callerTenant, roleName)},
	}
	ctx := tenant.WithTenantID(context.Background(), target)
	return a.Authorize(ctx, id, permission.Resource(perm), permission.Action(perm))
}

// TestAuthorize_DevicePermissions_AdminInOwnTenant proves the built-in tenant
// Admin onboards devices and activates and confirms updates in its own tenant,
// and the system tenant's Admin does so in the system tenant.
func TestAuthorize_DevicePermissions_AdminInOwnTenant(t *testing.T) {
	for _, tenantID := range []string{"acme", tenantid.System} {
		for _, perm := range devicePermissions {
			if err := authorizeDevice(t, role.RoleNameAdmin, tenantID, tenantID, perm); err != nil {
				t.Errorf("Admin@%s %s on %s: got %v, want allowed", tenantID, perm, tenantID, err)
			}
		}
	}
}

// TestAuthorize_DevicePermissions_CrossTenantDenied is the confinement case: a
// tenant-A Admin holding the permission cannot onboard a device into, or
// activate or confirm an update for, tenant B. The refusal is a scope denial,
// not a failure to resolve the caller's own roles.
func TestAuthorize_DevicePermissions_CrossTenantDenied(t *testing.T) {
	for _, perm := range devicePermissions {
		err := authorizeDevice(t, role.RoleNameAdmin, "acme", "globex", perm)
		var denied *PermissionDeniedError
		if !errors.As(err, &denied) {
			t.Fatalf("Admin@acme %s on globex: got %v, want *PermissionDeniedError", perm, err)
		}
		var resolve *ResolveError
		if errors.As(err, &resolve) {
			t.Fatalf("Admin@acme %s on globex failed to resolve its own roles: %v", perm, err)
		}
	}
}

// TestAuthorize_DevicePermissions_SystemTenantHasNoLift proves the system
// tenant's Admin reaches no tenant's devices: ScopeSelf has no system-tenant
// lift, and the system tenant holds no cross-scope device permission.
func TestAuthorize_DevicePermissions_SystemTenantHasNoLift(t *testing.T) {
	for _, perm := range devicePermissions {
		err := authorizeDevice(t, role.RoleNameAdmin, tenantid.System, "acme", perm)
		var denied *PermissionDeniedError
		if !errors.As(err, &denied) {
			t.Errorf("Admin@system %s on acme: got %v, want *PermissionDeniedError", perm, err)
		}
	}
}

// TestAuthorize_DevicePermissions_RolesWithoutTheGrantDenied proves the
// built-in User and Auditor roles carry none of the three, in their own tenant.
func TestAuthorize_DevicePermissions_RolesWithoutTheGrantDenied(t *testing.T) {
	for _, roleName := range []string{role.RoleNameUser, role.RoleNameAuditor} {
		for _, perm := range devicePermissions {
			err := authorizeDevice(t, roleName, "acme", "acme", perm)
			var denied *PermissionDeniedError
			if !errors.As(err, &denied) {
				t.Errorf("%s@acme %s on acme: got %v, want *PermissionDeniedError", roleName, perm, err)
			}
		}
	}
}
