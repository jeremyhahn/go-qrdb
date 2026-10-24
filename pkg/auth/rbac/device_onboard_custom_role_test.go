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

// onboarderAuthorizer returns an authorizer over acme and globex, where acme
// has defined a custom role carrying devices:onboard alone, and that role's ID.
func onboarderAuthorizer(t *testing.T) (*RBACAuthorizer, string) {
	t.Helper()
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme", "globex")
	id := authtest.CustomRole(t, roles, "acme", "device-onboarder", permission.DevicesOnboard)
	return New(roles), id
}

func authorizeAs(a *RBACAuthorizer, id *auth.Identity, target string, perm permission.Permission) error {
	ctx := tenant.WithTenantID(context.Background(), target)
	return a.Authorize(ctx, id, permission.Resource(perm), permission.Action(perm))
}

func TestAuthorize_DevicesOnboard_CustomRoleInOwnTenant(t *testing.T) {
	a, roleID := onboarderAuthorizer(t)
	id := &auth.Identity{Subject: "dana", TenantID: "acme", Roles: []string{roleID}}
	if err := authorizeAs(a, id, "acme", permission.DevicesOnboard); err != nil {
		t.Fatalf("device-onboarder@acme devices:onboard on acme: %v", err)
	}
}

func TestAuthorize_DevicesOnboard_CustomRoleOnOtherTenantDenied(t *testing.T) {
	a, roleID := onboarderAuthorizer(t)
	id := &auth.Identity{Subject: "dana", TenantID: "acme", Roles: []string{roleID}}
	err := authorizeAs(a, id, "globex", permission.DevicesOnboard)
	var denied *PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("device-onboarder@acme devices:onboard on globex: got %v, want *PermissionDeniedError", err)
	}
}

// TestAuthorize_DevicesOnboard_ForeignRoleIDGrantsNothing presents acme's role
// ID from a globex identity: the grant belongs to the tenant that defined the
// role, so it opens neither globex nor acme to a globex caller.
func TestAuthorize_DevicesOnboard_ForeignRoleIDGrantsNothing(t *testing.T) {
	a, roleID := onboarderAuthorizer(t)
	id := &auth.Identity{Subject: "eve", TenantID: "globex", Roles: []string{roleID}}
	for _, target := range []string{"globex", "acme"} {
		if err := authorizeAs(a, id, target, permission.DevicesOnboard); err == nil {
			t.Errorf("globex identity holding acme's role: devices:onboard on %s allowed", target)
		}
	}
}

// TestAuthorize_DevicesOnboard_CustomRoleGrantsOnlyOnboarding checks the role
// carries onboarding alone: neither OTA permission rides along with it.
func TestAuthorize_DevicesOnboard_CustomRoleGrantsOnlyOnboarding(t *testing.T) {
	a, roleID := onboarderAuthorizer(t)
	id := &auth.Identity{Subject: "dana", TenantID: "acme", Roles: []string{roleID}}
	for _, perm := range []permission.Permission{permission.OTAActivate, permission.OTAConfirm} {
		var denied *PermissionDeniedError
		if err := authorizeAs(a, id, "acme", perm); !errors.As(err, &denied) {
			t.Errorf("device-onboarder@acme %s on acme: got %v, want *PermissionDeniedError", perm, err)
		}
	}
}

// TestAuthorize_DevicesOnboard_OtherTenantsOperatorDenied checks a built-in
// role that does not carry the permission is refused even in its own tenant,
// so holding devices:onboard is what admits the acme caller.
func TestAuthorize_DevicesOnboard_OtherTenantsOperatorDenied(t *testing.T) {
	a, _ := onboarderAuthorizer(t)
	id := &auth.Identity{Subject: "olga", TenantID: "globex", Roles: []string{role.SystemRoleID("globex", role.RoleNameOperator)}}
	var denied *PermissionDeniedError
	if err := authorizeAs(a, id, "globex", permission.DevicesOnboard); !errors.As(err, &denied) {
		t.Fatalf("Operator@globex devices:onboard on globex: got %v, want *PermissionDeniedError", err)
	}
}
