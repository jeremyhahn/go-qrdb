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

package role

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// The tenant Admin built-in holds roles:create and roles:update so that it
// can seed and reconcile its own tenant's roles. These tests pin the lines
// that grant stops at, all of which live in the role router and store rather
// than in the grant table: another tenant and the system tenant are out of
// reach, and the built-in roles keep their reserved names and fixed grants.

// tenantAdminRolesSetup returns a routed role service over two tenant stores
// and the system store, with acme's built-in roles seeded the way tenant
// creation seeds them, and the context of a caller in acme.
func tenantAdminRolesSetup(t *testing.T) (RoleServicer, context.Context, *fakeKVStore, *fakeKVStore) {
	t.Helper()
	system := newFakeKVStore()
	globex := newFakeKVStore()
	resolver := &fakeResolver{
		shared: system,
		tenants: map[string]kvstore.KVStore{
			"acme":   newFakeKVStore(),
			"globex": globex,
		},
	}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	acme := tenant.WithTenantID(context.Background(), "acme")
	if _, err := svc.ReconcileBuiltInRoles(acme, ""); err != nil {
		t.Fatalf("seed acme's built-in roles: %v", err)
	}
	return svc, acme, system, globex
}

func TestBuiltInRoles_AdminHoldsRoleWrites(t *testing.T) {
	admin := roleByName(BuiltInRoles("acme"), RoleNameAdmin)
	if admin == nil {
		t.Fatal("admin role not found")
	}
	held := make(map[permission.Permission]bool, len(admin.Permissions))
	for _, p := range admin.Permissions {
		held[p] = true
	}
	for _, p := range []permission.Permission{permission.RolesCreate, permission.RolesUpdate} {
		if !held[p] {
			t.Errorf("built-in admin role lacks %q", p)
		}
	}
	if held[permission.RolesDelete] {
		t.Errorf("built-in admin role holds %q, which stays with the Security Officer", permission.RolesDelete)
	}
}

func TestTenantAdmin_CreatesAndUpdatesCustomRoleInOwnTenant(t *testing.T) {
	svc, acme, _, _ := tenantAdminRolesSetup(t)

	created, err := svc.CreateRole(acme, &CreateRoleRequest{
		Name:        "app-reader",
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if created.TenantID != "acme" || created.System {
		t.Fatalf("created role = tenant %q system %v, want tenant acme, not system", created.TenantID, created.System)
	}

	updated, err := svc.UpdateRole(acme, &UpdateRoleRequest{
		ID:          created.ID,
		Name:        "app-writer",
		Permissions: []permission.Permission{permission.KVGet, permission.KVPut},
	})
	if err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if updated.Name != "app-writer" || !permissionSetsEqual(updated.Permissions,
		[]permission.Permission{permission.KVGet, permission.KVPut}) {
		t.Fatalf("updated role = %q %v, want app-writer [kv:get kv:put]", updated.Name, updated.Permissions)
	}
	got, err := svc.GetRoleByName(acme, "", "app-writer")
	if err != nil || got.ID != created.ID {
		t.Fatalf("GetRoleByName(app-writer) = %v, %v; want the updated role", got, err)
	}
}

func TestTenantAdmin_CreateNamingTenantExplicitlyStaysInOwnTenant(t *testing.T) {
	svc, acme, _, _ := tenantAdminRolesSetup(t)

	created, err := svc.CreateRole(acme, &CreateRoleRequest{
		Name:        "named-own",
		TenantID:    "acme",
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole naming its own tenant: %v", err)
	}
	if created.TenantID != "acme" {
		t.Fatalf("created role tenant = %q, want acme", created.TenantID)
	}
}

func TestTenantAdmin_CreateInAnotherTenantRefused(t *testing.T) {
	svc, acme, _, globex := tenantAdminRolesSetup(t)

	_, err := svc.CreateRole(acme, &CreateRoleRequest{
		Name:        "intruder",
		TenantID:    "globex",
		Permissions: []permission.Permission{permission.KVGet},
	})
	var denied *tenant.CrossTenantDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("CreateRole in globex = %v, want CrossTenantDeniedError", err)
	}
	if n := len(globex.m); n != 0 {
		t.Fatalf("globex's store holds %d records after a refused create", n)
	}
}

func TestTenantAdmin_CreateInSystemTenantRefused(t *testing.T) {
	svc, acme, system, _ := tenantAdminRolesSetup(t)
	before := len(system.m)

	_, err := svc.CreateRole(acme, &CreateRoleRequest{
		Name:        "platform-intruder",
		TenantID:    tenantid.System,
		Permissions: []permission.Permission{permission.KVGet},
	})
	var denied *tenant.CrossTenantDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("CreateRole in system = %v, want CrossTenantDeniedError", err)
	}
	if denied.RequestedTenantID != tenantid.System {
		t.Fatalf("denial names requested tenant %q, want %q", denied.RequestedTenantID, tenantid.System)
	}
	if after := len(system.m); after != before {
		t.Fatalf("system store went from %d to %d records after a refused create", before, after)
	}
}

func TestTenantAdmin_UpdateOfAnotherTenantsRoleRefused(t *testing.T) {
	svc, acme, _, _ := tenantAdminRolesSetup(t)
	globexCtx := tenant.WithTenantID(context.Background(), "globex")
	theirs, err := svc.CreateRole(globexCtx, &CreateRoleRequest{
		Name:        "globex-reader",
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole in globex as globex: %v", err)
	}

	_, err = svc.UpdateRole(acme, &UpdateRoleRequest{
		ID:          theirs.ID,
		Permissions: []permission.Permission{permission.KVGet, permission.KVPut},
	})
	var notFound *RoleNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("UpdateRole of globex's role from acme = %v, want RoleNotFoundError", err)
	}
	got, err := svc.GetRole(globexCtx, theirs.ID)
	if err != nil {
		t.Fatalf("GetRole as globex: %v", err)
	}
	if !permissionSetsEqual(got.Permissions, []permission.Permission{permission.KVGet}) {
		t.Fatalf("globex's role permissions = %v after a refused update, want [kv:get]", got.Permissions)
	}
}

func TestTenantAdmin_UpdateOfSystemTenantsRoleRefused(t *testing.T) {
	svc, acme, _, _ := tenantAdminRolesSetup(t)
	systemCtx := tenant.WithTenantID(context.Background(), tenantid.System)
	platform, err := svc.CreateRole(systemCtx, &CreateRoleRequest{
		Name:        "platform-reader",
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole in system as system: %v", err)
	}

	_, err = svc.UpdateRole(acme, &UpdateRoleRequest{ID: platform.ID, Name: "captured"})
	var notFound *RoleNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("UpdateRole of a system role from acme = %v, want RoleNotFoundError", err)
	}
	got, err := svc.GetRole(systemCtx, platform.ID)
	if err != nil {
		t.Fatalf("GetRole as system: %v", err)
	}
	if got.Name != "platform-reader" {
		t.Fatalf("system role renamed to %q by a refused update", got.Name)
	}
}

func TestTenantAdmin_CreateWithBuiltInNameRefused(t *testing.T) {
	svc, acme, _, _ := tenantAdminRolesSetup(t)

	for name := range systemRoleNames() {
		// A non-canonical role under a reserved name is refused on its own
		// terms.
		_, err := svc.CreateRole(acme, &CreateRoleRequest{
			Name:        name,
			Permissions: []permission.Permission{permission.KVGet},
		})
		var reserved *ReservedRoleNameError
		if !errors.As(err, &reserved) {
			t.Errorf("CreateRole(%q, [kv:get]) = %v, want ReservedRoleNameError", name, err)
		}
	}

	// The exact canonical admin role is refused too: the tenant already holds
	// it, and its name is taken.
	canonical := canonicalSystemRole("acme", RoleNameAdmin)
	_, err := svc.CreateRole(acme, &CreateRoleRequest{
		ID:          canonical.ID,
		Name:        canonical.Name,
		Permissions: canonical.Permissions,
	})
	var exists *RoleNameExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("CreateRole(canonical admin) = %v, want RoleNameExistsError", err)
	}
}

func TestTenantAdmin_UpdateOfBuiltInRoleRefused(t *testing.T) {
	svc, acme, _, _ := tenantAdminRolesSetup(t)
	adminID := SystemRoleID("acme", RoleNameAdmin)

	var immutable *SystemRoleImmutableError
	_, err := svc.UpdateRole(acme, &UpdateRoleRequest{
		ID:          adminID,
		Permissions: []permission.Permission{permission.RolesDelete},
	})
	if !errors.As(err, &immutable) {
		t.Fatalf("re-permission built-in admin = %v, want SystemRoleImmutableError", err)
	}
	_, err = svc.UpdateRole(acme, &UpdateRoleRequest{ID: adminID, Name: "not-admin"})
	if !errors.As(err, &immutable) {
		t.Fatalf("rename built-in admin = %v, want SystemRoleImmutableError", err)
	}

	// ReconcileBuiltInRoles still converges the built-ins: nothing was
	// changed, so it reports nothing.
	report, err := svc.ReconcileBuiltInRoles(acme, "")
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}
	if report.Changed() {
		t.Fatalf("ReconcileBuiltInRoles reported changes after refused updates: %+v", report)
	}
}
