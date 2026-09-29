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

// go-qrdb#192 item 4: the authorizer consumes auth.Identity.RoleBindings, and
// a binding scoped to one tenant grants ONLY there.
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

// bindingIdentity is a system-tenant identity (so the ScopeSelf system-tenant
// rule always admits, regardless of target) holding NO plain Roles and
// exactly one RoleBinding scoped to tenant A. Isolating the scope rule this
// way means a denial in tenant B can only be explained by the binding not
// resolving there — never by the ordinary ScopeSelf tenant check, which
// would deny a non-system identity acting outside its own tenant for an
// unrelated reason.
func bindingIdentity(roleID, boundTenant string) *auth.Identity {
	return &auth.Identity{
		Subject:      "svc-1",
		TenantID:     tenantid.System,
		RoleBindings: []auth.RoleBinding{{RoleID: roleID, TenantID: boundTenant}},
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

// TestBoundRoleIDs_FiltersByTargetTenant asserts only the matching binding's
// RoleID is returned when an identity carries bindings for several tenants.
func TestBoundRoleIDs_FiltersByTargetTenant(t *testing.T) {
	id := &auth.Identity{
		Subject: "s",
		RoleBindings: []auth.RoleBinding{
			{RoleID: "role-a", TenantID: "tenant-a"},
			{RoleID: "role-b", TenantID: "tenant-b"},
		},
	}
	got := boundRoleIDs(ctxFor("tenant-a"), id)
	if len(got) != 1 || got[0] != "role-a" {
		t.Errorf("boundRoleIDs(tenant-a) = %v, want [role-a]", got)
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
// and ResolveSystemRoleID treats *role.RoleNotFoundError as "not seeded yet,
// fall back to the canonical deterministic id" -- exactly the behavior the
// strict-SoD tests below rely on.
func (m *tenantAwareRoleService) GetRoleByName(_ context.Context, _, name string) (*role.Role, error) {
	return nil, &role.RoleNotFoundError{ID: name}
}

// TestRoleBinding_CrossTenantPermissionsAreUnionedNotCombined is the
// regression test for the bug effectiveRoles' doc comment describes. Before
// the fix, an identity's home-tenant Roles and a bound role from a DIFFERENT
// tenant were combined into ONE role-id list and resolved in ONE tenant
// context, which failed the ENTIRE call the moment the two tenants
// disagreed — denying access a valid binding alone would have granted. The
// fix resolves them as two separate calls, each in the tenant its role ids
// actually belong to, and unions the resulting permission sets.
func TestRoleBinding_CrossTenantPermissionsAreUnionedNotCombined(t *testing.T) {
	svc := &tenantAwareRoleService{perms: map[string]map[string][]permission.Permission{
		tenantid.System: {"home-role": {permission.KVGet}},
		"acme":          {"acme-role": {permission.KVPut}},
	}}
	authz := New(svc)
	id := &auth.Identity{
		Subject:      "svc-1",
		TenantID:     tenantid.System,
		Roles:        []string{"home-role"},
		RoleBindings: []auth.RoleBinding{{RoleID: "acme-role", TenantID: "acme"}},
	}

	// The bound role's OWN permission (kv:put), resolved in "acme": if this
	// call combined "home-role" (which exists only in the system tenant)
	// into the same resolve, kvRoleStore-shaped behavior would fail the
	// whole lookup and this would be denied even though the binding alone
	// grants it.
	if err := authz.Authorize(ctxFor("acme"), id, "kv", "put"); err != nil {
		t.Fatalf("bound role in acme must grant kv:put there despite an unrelated home-tenant role: %v", err)
	}
	// The home role's own permission (kv:get) still resolves normally when
	// targeting the system tenant, proving the split did not break ordinary
	// same-tenant resolution.
	if err := authz.Authorize(ctxFor(tenantid.System), id, "kv", "get"); err != nil {
		t.Fatalf("home role must still grant kv:get in its own tenant: %v", err)
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
		TenantID:     tenantid.System,
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

// TestStripOwnerRole_StripsBoundCrossTenantOwnerGrant is the regression test
// for the strict-SoD finding: the Owner strip resolved ownerIDs only in the
// identity's HOME tenant, so a bound Owner grant naming a DIFFERENT tenant
// was never stripped at all under strict separation of duties.
func TestStripOwnerRole_StripsBoundCrossTenantOwnerGrant(t *testing.T) {
	svc := &tenantAwareRoleService{perms: map[string]map[string][]permission.Permission{
		"acme": {},
	}}
	authz := New(svc, WithStrictSeparation(true))
	ownerID := role.SystemRoleID("acme", role.RoleNameOwner)
	id := &auth.Identity{
		Subject:      "svc-1",
		TenantID:     tenantid.System,
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
		t.Fatal("a bound cross-tenant Owner grant must be stripped under strict SoD, not honored")
	}
	var rnf *role.RoleNotFoundError
	if errors.As(err, &rnf) {
		t.Fatalf("Owner id reached ResolvePermissions unstripped: %v", err)
	}
}

// nameLookupFailingRoleService resolves permissions like its embedded
// tenant-aware double but fails every by-name lookup, so the system-role
// resolution the SO deny-list and the Owner strip depend on cannot answer.
type nameLookupFailingRoleService struct {
	*tenantAwareRoleService
}

func (nameLookupFailingRoleService) GetRoleByName(context.Context, string, string) (*role.Role, error) {
	return nil, errors.New("role store unavailable")
}

// TestAuthorize_BoundGrantFailuresDeny asserts every failure on the bound-role
// path denies with an error rather than being read as "no grant, no
// restriction": the SO check, the strict-SoD Owner strip, and the permission
// resolution of the bound roles themselves.
func TestAuthorize_BoundGrantFailuresDeny(t *testing.T) {
	bound := &auth.Identity{
		Subject:      "svc-1",
		TenantID:     tenantid.System,
		RoleBindings: []auth.RoleBinding{{RoleID: "acme-custom", TenantID: "acme"}},
	}
	failingNames := nameLookupFailingRoleService{&tenantAwareRoleService{perms: map[string]map[string][]permission.Permission{
		"acme": {"acme-custom": {permission.KVGet}},
	}}}
	tests := []struct {
		name     string
		authz    *RBACAuthorizer
		resource string
		action   string
	}{
		{name: "SO check cannot resolve the bound role", authz: New(failingNames), resource: "keys", action: "sign"},
		{name: "Owner strip cannot resolve the bound role", authz: New(failingNames, WithStrictSeparation(true)), resource: "kv", action: "get"},
		{name: "bound role unknown in its tenant", authz: New(&tenantAwareRoleService{perms: map[string]map[string][]permission.Permission{"acme": {}}}), resource: "kv", action: "get"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.authz.Authorize(ctxFor("acme"), bound, tt.resource, tt.action)
			var re *ResolveError
			if !errors.As(err, &re) {
				t.Fatalf("Authorize() = %v, want *ResolveError", err)
			}
		})
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
