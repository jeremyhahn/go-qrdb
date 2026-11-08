// Copyright 2026 Jeremy Hahn
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

// grantFixture is the grant rule (go-qrdb#1278) over the real RBAC authorizer:
// a freshly seeded acme, the role service guarded by role.NewGrantGuard, and
// the authorizer the guard asks.
type grantFixture struct {
	roles role.RoleServicer
	authz *RBACAuthorizer
}

func newGrantFixture(t *testing.T) *grantFixture {
	t.Helper()
	raw := authtest.BuiltInRoles(t, tenantid.System, "acme")
	authz := New(raw)
	return &grantFixture{roles: role.NewGrantGuard(raw, authz), authz: authz}
}

// as is ctx carrying id as the calling principal, targeting acme.
func as(id *auth.Identity) context.Context {
	return auth.WithIdentity(tenant.WithTenantID(context.Background(), "acme"), id)
}

// system is acme with no principal: the bootstrap and seeding path.
func system() context.Context {
	return tenant.WithTenantID(context.Background(), "acme")
}

// holds reports whether id holds p in acme, as the authorizer evaluates it.
func (f *grantFixture) holds(id *auth.Identity, p permission.Permission) bool {
	return f.authz.Authorize(system(), id, permission.Resource(p), permission.Action(p)) == nil
}

// soOnly returns a permission acme's SO holds and acme's Admin does not.
func (f *grantFixture) soOnly(t *testing.T) permission.Permission {
	t.Helper()
	so, admin := authtest.SOIdentity("acme", "officer"), authtest.AdminIdentity("acme", "admin")
	for _, p := range permission.ExpandAll(permission.RolePermissions(role.RoleNameSO)) {
		if f.holds(so, p) && !f.holds(admin, p) {
			return p
		}
	}
	t.Fatal("no permission acme's SO holds that acme's Admin lacks")
	return ""
}

// adminHeld returns two permissions acme's Admin holds in acme.
func (f *grantFixture) adminHeld(t *testing.T) []permission.Permission {
	t.Helper()
	admin := authtest.AdminIdentity("acme", "admin")
	var held []permission.Permission
	for _, p := range permission.ExpandAll(permission.RolePermissions(role.RoleNameAdmin)) {
		if f.holds(admin, p) {
			held = append(held, p)
		}
		if len(held) == 2 {
			return held
		}
	}
	t.Fatal("acme's Admin holds fewer than two permissions in acme")
	return nil
}

func wantGrantRefused(t *testing.T, err error, perm permission.Permission) *role.GrantExceedsHolderError {
	t.Helper()
	var exceeds *role.GrantExceedsHolderError
	if !errors.As(err, &exceeds) {
		t.Fatalf("err = %v, want *role.GrantExceedsHolderError", err)
	}
	if exceeds.Permission != perm.String() || exceeds.TenantID != "acme" {
		t.Fatalf("refusal = %+v, want permission %s in acme", exceeds, perm)
	}
	return exceeds
}

// A tenant Admin cannot mint a role carrying keys:sign, key custody it does
// not hold: the self-grant #1278 reports.
func TestGrantRule_TenantAdminCannotCreateRoleWithKeysSign(t *testing.T) {
	f := newGrantFixture(t)
	admin := authtest.AdminIdentity("acme", "admin")
	if f.holds(admin, permission.KeysSign) {
		t.Fatal("precondition: acme's Admin holds keys:sign")
	}
	_, err := f.roles.CreateRole(as(admin), &role.CreateRoleRequest{
		Name: "signer", TenantID: "acme", Permissions: []permission.Permission{permission.KeysSign},
	})
	wantGrantRefused(t, err, permission.KeysSign)
	if r, _ := f.roles.GetRoleByName(system(), "acme", "signer"); r != nil {
		t.Fatal("the refused role was created")
	}
}

// TestGrantRule_SOGrantsACryptoPermissionItsRolesCover proves the SO, which
// administers roles but never uses keys, may create a role holding keys:sign
// when its roles otherwise cover it: the grant check skips only the SO
// cryptographic denial. Authorize still refuses that SO the sign itself.
func TestGrantRule_SOGrantsACryptoPermissionItsRolesCover(t *testing.T) {
	f := newGrantFixture(t)
	so := role.SystemRoleID("acme", role.RoleNameSO)
	usr := role.SystemRoleID("acme", role.RoleNameUser)
	officer := &auth.Identity{Subject: "officer", TenantID: "acme", Roles: []string{so, usr}}
	sign := func(id *auth.Identity) error {
		return f.authz.Authorize(as(id), id, permission.Resource(permission.KeysSign), permission.Action(permission.KeysSign))
	}
	if err := sign(&auth.Identity{Subject: "u", TenantID: "acme", Roles: []string{usr}}); err != nil {
		t.Fatalf("precondition: acme's User role does not cover keys:sign: %v", err)
	}
	if err := sign(officer); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("Authorize(keys:sign) for the SO = %v, want a denial", err)
	}
	if err := f.authz.AuthorizeGrant(as(officer), officer, permission.Resource(permission.KeysSign), permission.Action(permission.KeysSign)); err != nil {
		t.Fatalf("AuthorizeGrant(keys:sign) for the SO = %v, want nil", err)
	}
	if _, err := f.roles.CreateRole(as(officer), &role.CreateRoleRequest{
		Name: "signer", TenantID: "acme", Permissions: []permission.Permission{permission.KeysSign},
	}); err != nil {
		t.Fatalf("SO CreateRole(keys:sign) = %v, want success", err)
	}
	if err := sign(officer); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("after the grant, Authorize(keys:sign) for the SO = %v, want a denial", err)
	}
}

// TestGrantRule_SOCannotGrantACryptoPermissionItsRolesLack proves the grant
// check lifts only the SO cryptographic denial: an SO whose roles do not
// cover keys:sign is still refused with GrantExceedsHolderError.
func TestGrantRule_SOCannotGrantACryptoPermissionItsRolesLack(t *testing.T) {
	f := newGrantFixture(t)
	officer := &auth.Identity{Subject: "officer", TenantID: "acme", Roles: []string{role.SystemRoleID("acme", role.RoleNameSO)}}
	_, err := f.roles.CreateRole(as(officer), &role.CreateRoleRequest{
		Name: "signer", TenantID: "acme", Permissions: []permission.Permission{permission.KeysSign},
	})
	wantGrantRefused(t, err, permission.KeysSign)
}

// A tenant Admin cannot widen an existing role past what it holds, nor
// assign a role the SO created that carries a permission the Admin lacks.
func TestGrantRule_TenantAdminCannotGrantThroughAnotherRole(t *testing.T) {
	f := newGrantFixture(t)
	admin, so := authtest.AdminIdentity("acme", "admin"), authtest.SOIdentity("acme", "officer")
	held, lacked := f.adminHeld(t), f.soOnly(t)

	ops, err := f.roles.CreateRole(as(admin), &role.CreateRoleRequest{Name: "ops", TenantID: "acme", Permissions: held[:1]})
	if err != nil {
		t.Fatalf("create a held subset: %v", err)
	}
	_, err = f.roles.UpdateRole(as(admin), &role.UpdateRoleRequest{ID: ops.ID, Permissions: []permission.Permission{held[0], lacked}})
	wantGrantRefused(t, err, lacked)
	if got, _ := f.roles.GetRole(system(), ops.ID); len(got.Permissions) != 1 {
		t.Fatalf("refused update was applied: %v", got.Permissions)
	}

	officer, err := f.roles.CreateRole(as(so), &role.CreateRoleRequest{Name: "officer-duty", TenantID: "acme", Permissions: []permission.Permission{lacked}})
	if err != nil {
		t.Fatalf("SO creates a role it holds: %v", err)
	}
	exceeds := wantGrantRefused(t, role.ValidateAssignable(as(admin), f.roles, f.authz, officer.ID, "acme"), lacked)
	if exceeds.RoleID != officer.ID {
		t.Fatalf("refusal names role %q, want %q", exceeds.RoleID, officer.ID)
	}
	if err := role.ValidateAssignable(as(so), f.roles, f.authz, officer.ID, "acme"); err != nil {
		t.Fatalf("the SO assigns a role it holds: %v", err)
	}
}

// A subset of the caller's permissions is created, updated and assigned.
func TestGrantRule_HeldSubsetIsGranted(t *testing.T) {
	f := newGrantFixture(t)
	admin := authtest.AdminIdentity("acme", "admin")
	held := f.adminHeld(t)

	r, err := f.roles.CreateRole(as(admin), &role.CreateRoleRequest{Name: "helper", TenantID: "acme", Permissions: held[:1]})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.roles.UpdateRole(as(admin), &role.UpdateRoleRequest{ID: r.ID, Permissions: held}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := role.ValidateAssignable(as(admin), f.roles, f.authz, r.ID, "acme"); err != nil {
		t.Fatalf("assign: %v", err)
	}
}

// The tenant Admin assigns acme's built-in User role to another principal,
// though it does not hold User's crypto-use duty: the duty split stands. It
// cannot bind User to itself, which is #1278's self-grant through a built-in.
func TestGrantRule_BuiltInUserIsAssignableButNotSelfBindable(t *testing.T) {
	f := newGrantFixture(t)
	admin := authtest.AdminIdentity("acme", "admin")
	if f.holds(admin, permission.KeysSign) {
		t.Fatal("precondition: acme's Admin holds keys:sign")
	}
	userRole := role.SystemRoleID("acme", role.RoleNameUser)
	if err := role.ValidateAssignable(as(admin), f.roles, f.authz, userRole, "acme"); err != nil {
		t.Fatalf("Admin assigns User to another principal: %v", err)
	}
	var exceeds *role.GrantExceedsHolderError
	err := role.ValidateSelfAssignable(as(admin), f.roles, f.authz, userRole, "acme")
	if !errors.As(err, &exceeds) || exceeds.RoleID != userRole || exceeds.TenantID != "acme" {
		t.Fatalf("Admin binds User to itself: err = %v, want *role.GrantExceedsHolderError naming User", err)
	}
	if f.holds(admin, permission.Permission(exceeds.Permission)) {
		t.Fatalf("refused %s, which acme's Admin holds", exceeds.Permission)
	}
}

// A caller holding a wildcard grants what the wildcard covers, the wildcard
// itself included, and nothing past it.
func TestGrantRule_WildcardHolderGrantsWhatItCovers(t *testing.T) {
	f := newGrantFixture(t)
	kv, err := f.roles.CreateRole(system(), &role.CreateRoleRequest{
		Name: "kv-all", TenantID: "acme", Permissions: []permission.Permission{"kv:*", permission.RolesCreate},
	})
	if err != nil {
		t.Fatalf("seed the wildcard role: %v", err)
	}
	holder := &auth.Identity{Subject: "kv-admin", TenantID: "acme", Roles: []string{kv.ID}}

	for name, perms := range map[string][]permission.Permission{
		"kv-reader-writer": {permission.KVGet, permission.KVPut},
		"kv-delegate":      {"kv:*"},
	} {
		if _, err := f.roles.CreateRole(as(holder), &role.CreateRoleRequest{
			Name: name, TenantID: "acme", Permissions: perms,
		}); err != nil {
			t.Fatalf("grant %v under kv:* = %v, want nil", perms, err)
		}
	}
	_, err = f.roles.CreateRole(as(holder), &role.CreateRoleRequest{
		Name: "kv-sign", TenantID: "acme", Permissions: []permission.Permission{permission.KVGet, permission.KeysSign},
	})
	wantGrantRefused(t, err, permission.KeysSign)
}

// Bootstrap, first boot and the tenant seeder write roles with no principal;
// the rule is for grants by a principal and leaves them alone.
func TestGrantRule_SystemWorkIsNotAGrant(t *testing.T) {
	f := newGrantFixture(t)
	if _, err := f.roles.CreateRole(system(), &role.CreateRoleRequest{
		Name: "custody", TenantID: "acme", Permissions: []permission.Permission{permission.KeysSign},
	}); err != nil {
		t.Fatalf("system create: %v", err)
	}
	if _, err := f.roles.ReconcileBuiltInRoles(system(), "acme"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}
