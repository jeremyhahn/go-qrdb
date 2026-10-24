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
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// otaPermissions are the two OTA permissions a tenant grants through a
// custom role: each tenant activates and confirms updates for its own
// devices only.
var otaPermissions = []permission.Permission{
	permission.OTAActivate,
	permission.OTAConfirm,
}

// otaCustomRoleCaller returns an authorizer over the real role store and an
// identity in acme whose only role is a tenant-defined custom role granting
// exactly perms. No built-in role is attached, so a decision can come from the
// custom role alone.
func otaCustomRoleCaller(t *testing.T, perms ...permission.Permission) (*RBACAuthorizer, *auth.Identity) {
	t.Helper()
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme", "globex")
	id := authtest.Grant(t, roles, &auth.Identity{Subject: "ota-operator", TenantID: "acme"}, perms...)
	return New(roles), id
}

// authorizeOTA decides perm for id against target, the tenant an OTA handler
// names (as AuthMiddleware.AuthorizeTenant injects it).
func authorizeOTA(a *RBACAuthorizer, id *auth.Identity, target string, perm permission.Permission) error {
	ctx := tenant.WithTenantID(context.Background(), target)
	return a.Authorize(ctx, id, permission.Resource(perm), permission.Action(perm))
}

// TestAuthorize_OTACustomRole_OwnTenant proves a custom role holding an OTA
// permission in acme authorizes it on acme's own devices.
func TestAuthorize_OTACustomRole_OwnTenant(t *testing.T) {
	for _, perm := range otaPermissions {
		a, id := otaCustomRoleCaller(t, perm)
		if err := authorizeOTA(a, id, "acme", perm); err != nil {
			t.Errorf("custom role@acme %s on acme: got %v, want allowed", perm, err)
		}
	}
}

// TestAuthorize_OTACustomRole_OtherTenantDenied proves the same grant reaches
// no other tenant: ScopeSelf confines it to the tenant the role lives in, and
// the refusal is a policy denial, not a failure to resolve the caller's roles.
func TestAuthorize_OTACustomRole_OtherTenantDenied(t *testing.T) {
	for _, perm := range otaPermissions {
		a, id := otaCustomRoleCaller(t, perm)
		for _, target := range []string{"globex", tenantid.System} {
			err := authorizeOTA(a, id, target, perm)
			var denied *PermissionDeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("custom role@acme %s on %s: got %v, want *PermissionDeniedError", perm, target, err)
			}
			var resolve *ResolveError
			if errors.As(err, &resolve) {
				t.Fatalf("custom role@acme %s on %s failed to resolve its own roles: %v", perm, target, err)
			}
		}
	}
}

// TestAuthorize_OTACustomRole_PermissionsAreDistinct proves activate and
// confirm are separate grants: a role holding one does not authorize the
// other, so a tenant can split activation from confirmation across roles.
func TestAuthorize_OTACustomRole_PermissionsAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		held, asked permission.Permission
	}{
		{permission.OTAActivate, permission.OTAConfirm},
		{permission.OTAConfirm, permission.OTAActivate},
	} {
		a, id := otaCustomRoleCaller(t, tc.held)
		var denied *PermissionDeniedError
		if err := authorizeOTA(a, id, "acme", tc.asked); !errors.As(err, &denied) {
			t.Errorf("custom role holding %s asked %s on acme: got %v, want *PermissionDeniedError", tc.held, tc.asked, err)
		}
	}
}

// TestAuthorize_OTACustomRole_ForeignRoleIDGrantsNothing proves the grant
// cannot be carried into another tenant by naming the role: an identity in
// globex presenting acme's role ID is refused on globex, because roles resolve
// within the caller's own tenant and acme's role is not found there.
func TestAuthorize_OTACustomRole_ForeignRoleIDGrantsNothing(t *testing.T) {
	for _, perm := range otaPermissions {
		a, acmeID := otaCustomRoleCaller(t, perm)
		intruder := &auth.Identity{Subject: "intruder", TenantID: "globex", Roles: acmeID.Roles}
		err := authorizeOTA(a, intruder, "globex", perm)
		var resolve *ResolveError
		if !errors.As(err, &resolve) {
			t.Errorf("globex identity holding acme's role ID asked %s on globex: got %v, want *ResolveError (the role is not in globex)", perm, err)
		}
	}
}
