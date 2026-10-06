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

// The authorizer consumes auth.Identity.RoleBindings. A binding names only its
// holder's own tenant and grants only there.
package rbac

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// bindingIdentity is an identity of boundTenant holding NO plain Roles and
// exactly one RoleBinding, in its own tenant.
func bindingIdentity(roleID, boundTenant string) *auth.Identity {
	return &auth.Identity{
		Subject:      "svc-1",
		TenantID:     boundTenant,
		RoleBindings: []auth.RoleBinding{{RoleID: roleID, TenantID: boundTenant}},
	}
}

// TestRoleBinding_ForeignTenantBindingGrantsNothing: a binding may name only
// its holder's own tenant. One naming another tenant -- which the store
// refuses at write, but an embedded composer can put on the identity it
// supplies -- grants nothing, even in the tenant it names and even for a
// system-tenant identity, whose ScopeSelf permissions would otherwise be
// admitted there.
func TestRoleBinding_ForeignTenantBindingGrantsNothing(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"role-a": {permission.KVGet},
	})
	a := New(svc)
	id := &auth.Identity{
		Subject:      "svc-1",
		TenantID:     tenantid.System,
		RoleBindings: []auth.RoleBinding{{RoleID: "role-a", TenantID: "tenant-a"}},
	}

	err := a.Authorize(ctxFor("tenant-a"), id, "kv", "get")
	var denied *PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("a foreign-tenant binding granted kv:get in the tenant it names: err = %v, want *PermissionDeniedError", err)
	}
}

// TestRoleBinding_GrantsInItsOwnTenant is the positive half: a binding scoped
// to tenant A grants the bound role's permissions when the request's target
// tenant is A.
func TestRoleBinding_GrantsInItsOwnTenant(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"role-a": {permission.KVGet},
	})
	a := New(svc)
	id := bindingIdentity("role-a", "tenant-a")

	if err := a.Authorize(ctxFor("tenant-a"), id, "kv", "get"); err != nil {
		t.Fatalf("a binding scoped to tenant-a must grant kv:get there: %v", err)
	}
}

// TestRoleBinding_DoesNotGrantInAnotherTenant is the negative half: the SAME
// identity, the SAME binding, against a DIFFERENT target tenant, must be
// denied — the binding does not follow the identity across tenants.
func TestRoleBinding_DoesNotGrantInAnotherTenant(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"role-a": {permission.KVGet},
	})
	a := New(svc)
	id := bindingIdentity("role-a", "tenant-a")

	err := a.Authorize(ctxFor("tenant-b"), id, "kv", "get")
	if err == nil {
		t.Fatal("a binding scoped to tenant-a must NOT grant kv:get in tenant-b")
	}
	var denied *PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("err = %v, want *PermissionDeniedError", err)
	}
}

// TestRoleBinding_PlainRolesUnaffectedByForeignBinding asserts the two grant
// paths are independent: an identity's ordinary Roles still resolve as
// before even when it ALSO carries a RoleBinding for an unrelated tenant.
func TestRoleBinding_PlainRolesUnaffectedByForeignBinding(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"plain-role": {permission.KVGet},
		"role-a":     {permission.KVPut},
	})
	a := New(svc)
	id := &auth.Identity{
		Subject:      "svc-1",
		TenantID:     "tenant-b",
		Roles:        []string{"plain-role"},
		RoleBindings: []auth.RoleBinding{{RoleID: "role-a", TenantID: "tenant-a"}},
	}

	// The plain role still grants kv:get in the identity's own tenant.
	if err := a.Authorize(ctxFor("tenant-b"), id, "kv", "get"); err != nil {
		t.Fatalf("the identity's own Roles must still grant kv:get in its own tenant: %v", err)
	}
	// The foreign binding's permission (kv:put) must NOT leak into tenant-b.
	if err := a.Authorize(ctxFor("tenant-b"), id, "kv", "put"); err == nil {
		t.Fatal("a binding scoped to tenant-a must not grant kv:put in tenant-b")
	}
}

// TestBoundRoleIDs_NoBindingsIsNil asserts the helper's cheap path.
func TestBoundRoleIDs_NoBindingsIsNil(t *testing.T) {
	id := &auth.Identity{Subject: "s", TenantID: "acme"}
	if got := boundRoleIDs(ctxFor("acme"), id); got != nil {
		t.Errorf("boundRoleIDs(no bindings) = %v, want nil", got)
	}
}

// TestBoundRoleIDs_OnlyTheIdentitysOwnTenant asserts a binding counts only
// when the target is the identity's own tenant and the binding names it: an
// identity carrying a binding for another tenant gets nothing from it there.
func TestBoundRoleIDs_OnlyTheIdentitysOwnTenant(t *testing.T) {
	id := &auth.Identity{
		Subject:  "s",
		TenantID: "tenant-a",
		RoleBindings: []auth.RoleBinding{
			{RoleID: "role-a", TenantID: "tenant-a"},
			{RoleID: "role-b", TenantID: "tenant-b"},
		},
	}
	if got := boundRoleIDs(ctxFor("tenant-a"), id); len(got) != 1 || got[0] != "role-a" {
		t.Errorf("boundRoleIDs(tenant-a) = %v, want [role-a]", got)
	}
	if got := boundRoleIDs(ctxFor("tenant-b"), id); got != nil {
		t.Errorf("boundRoleIDs(tenant-b) = %v, want nil: a foreign binding grants nothing", got)
	}
	system := &auth.Identity{Subject: "s", RoleBindings: []auth.RoleBinding{{RoleID: "role-s", TenantID: tenantid.System}}}
	if got := boundRoleIDs(ctxFor(tenantid.System), system); len(got) != 1 || got[0] != "role-s" {
		t.Errorf("boundRoleIDs(system) for an identity with an empty tenant = %v, want [role-s]", got)
	}
}

// ---------------------------------------------------------------------------
// Cross-tenant resolution (round 2 fix): SEPARATE resolves, unioned
// permissions, never one combined role-id list resolved in one tenant.
// ---------------------------------------------------------------------------

// tenantAwareRoleService is a REALISTIC RoleServicer double: unlike
// mockRoleService's single flat map (which ignores ctx's tenant entirely and
// never errors on an unknown id), it resolves role ids WITHIN THE TENANT ctx
// names, exactly as the production routingRoleService -> kvRoleStore chain
// does, and returns *role.RoleNotFoundError for an id absent from that
// tenant's set -- also matching production, where kvRoleStore.
// ResolvePermissions fails the WHOLE call on the first unresolvable id.
// mockRoleService's always-succeeds flat map could not exercise the bug
// these tests exist to catch: a single ResolvePermissions call given role
// ids from two DIFFERENT tenants failed outright in production, and a mock
// that never fails could not have shown that (round 2 review finding).
type tenantAwareRoleService struct {
	role.RoleServicer
	perms map[string]map[string][]permission.Permission // tenantID -> roleID -> perms
}

func (m *tenantAwareRoleService) ResolvePermissions(ctx context.Context, roleIDs []string) ([]permission.Permission, error) {
	byRole := m.perms[tenant.FromContext(ctx)]
	seen := make(map[permission.Permission]struct{})
	var out []permission.Permission
	for _, id := range roleIDs {
		perms, ok := byRole[id]
		if !ok {
			return nil, &role.RoleNotFoundError{ID: id}
		}
		for _, p := range perms {
			if _, dup := seen[p]; !dup {
				seen[p] = struct{}{}
				out = append(out, p)
			}
		}
	}
	return out, nil
}

// GetRoleByName always reports absence: this double tracks roles by id only,
// and the RoleServicer interface requires the method even though nothing in
// this file's tests resolves a role by name.
func (m *tenantAwareRoleService) GetRoleByName(_ context.Context, _, name string) (*role.Role, error) {
	return nil, &role.RoleNotFoundError{ID: name}
}

// TestRoleBinding_PermissionsUnionWithRoles asserts a bound role's permissions
// are resolved alongside, and unioned with, the identity's own Roles: each
// grants in the identity's tenant.
func TestRoleBinding_PermissionsUnionWithRoles(t *testing.T) {
	svc := &tenantAwareRoleService{perms: map[string]map[string][]permission.Permission{
		"acme": {"home-role": {permission.KVGet}, "acme-role": {permission.KVPut}},
	}}
	authz := New(svc)
	id := &auth.Identity{
		Subject:      "svc-1",
		TenantID:     "acme",
		Roles:        []string{"home-role"},
		RoleBindings: []auth.RoleBinding{{RoleID: "acme-role", TenantID: "acme"}},
	}
	for _, action := range []string{"get", "put"} {
		if err := authz.Authorize(ctxFor("acme"), id, "kv", action); err != nil {
			t.Fatalf("Authorize(kv:%s) = %v, want allowed by the union", action, err)
		}
	}
}

// TestAuthorize_SODenyList_AppliesToABoundGrant is the regression test for
// the SO deny-list finding: the FIPS 140-3 AS10.03 denial examined only
// identity.Roles, so an SO grant that arrived via a RoleBinding evaded it
// entirely. It must be denied identically regardless of which source named
// the SO role.
func TestAuthorize_SODenyList_AppliesToABoundGrant(t *testing.T) {
	svc := &tenantAwareRoleService{perms: map[string]map[string][]permission.Permission{
		"acme": {},
	}}
	authz := New(svc)
	soRoleID := role.SystemRoleID("acme", role.RoleNameSO)
	id := &auth.Identity{
		Subject:      "svc-1",
		TenantID:     "acme",
		RoleBindings: []auth.RoleBinding{{RoleID: soRoleID, TenantID: "acme"}},
	}

	err := authz.Authorize(ctxFor("acme"), id, "keys", "sign")
	if err == nil {
		t.Fatal("a bound SO grant must be denied keys:sign under the FIPS 140-3 deny-list")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got: %v", err)
	}
}

// TestStripOwnerRole_StripsBoundOwnerGrant asserts a bound Owner grant is
// stripped under strict separation of duties exactly as a held one is.
func TestStripOwnerRole_StripsBoundOwnerGrant(t *testing.T) {
	svc := &tenantAwareRoleService{perms: map[string]map[string][]permission.Permission{
		"acme": {},
	}}
	authz := New(svc, WithStrictSeparation(true))
	ownerID := role.SystemRoleID("acme", role.RoleNameOwner)
	id := &auth.Identity{
		Subject:      "svc-1",
		TenantID:     "acme",
		RoleBindings: []auth.RoleBinding{{RoleID: ownerID, TenantID: "acme"}},
	}

	// Owner would otherwise resolve via ResolvePermissions to whatever the
	// role grants (permission.All() in production); this double's "acme"
	// tenant has no roles configured at all, so if the id were NOT stripped
	// it would fail resolution as a *role.RoleNotFoundError instead of being
	// cleanly denied by scope. Asserting the specific type distinguishes
	// "stripped, so nothing is granted" from "resolution blew up".
	err := authz.Authorize(ctxFor("acme"), id, "kv", "get")
	if err == nil {
		t.Fatal("a bound Owner grant must be stripped under strict SoD, not honored")
	}
	var rnf *role.RoleNotFoundError
	if errors.As(err, &rnf) {
		t.Fatalf("Owner id reached ResolvePermissions unstripped: %v", err)
	}
}

// TestAuthorize_BoundGrantFailureDeniesForUnknownRole asserts a bound role
// unknown in its target tenant denies with an error rather than being read
// as "no grant, no restriction": ResolvePermissions reports it as
// *role.RoleNotFoundError, wrapped in *ResolveError.
//
// The SO deny-list and the strict-SoD Owner strip are not covered here: they
// match a bound role id against its role NAME and canonical identifier
// directly, in memory (roleIDsHoldSystemRole, stripOwnerRole), with no role
// store lookup to fail.
func TestAuthorize_BoundGrantFailureDeniesForUnknownRole(t *testing.T) {
	bound := &auth.Identity{
		Subject:      "svc-1",
		TenantID:     "acme",
		RoleBindings: []auth.RoleBinding{{RoleID: "acme-custom", TenantID: "acme"}},
	}
	authz := New(&tenantAwareRoleService{perms: map[string]map[string][]permission.Permission{"acme": {}}})
	err := authz.Authorize(ctxFor("acme"), bound, "kv", "get")
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("Authorize() = %v, want *ResolveError", err)
	}
}

// TestAuthorize_ScopePermissionsUnionWithRoleGrants asserts pre-resolved scope
// permissions add to what the identity's roles grant.
func TestAuthorize_ScopePermissionsUnionWithRoleGrants(t *testing.T) {
	svc := &tenantAwareRoleService{perms: map[string]map[string][]permission.Permission{
		"acme": {"reader": {permission.KVGet}},
	}}
	id := &auth.Identity{
		Subject:          "svc-1",
		TenantID:         "acme",
		Roles:            []string{"reader"},
		ScopePermissions: []string{string(permission.KVPut)},
	}
	authz := New(svc)
	for _, action := range []string{"get", "put"} {
		if err := authz.Authorize(ctxFor("acme"), id, "kv", action); err != nil {
			t.Errorf("Authorize(kv:%s) = %v, want allowed", action, err)
		}
	}
}
