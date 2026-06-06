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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// adminPermissions returns the canonical Admin role permission set used
// by the RBAC tests. It mirrors a subset of the production Admin role:
// tenants self-scope perms (read), the cross-scope tenants:list:cross,
// plus a generic kv:get for self-scope coverage.
func adminPermissions() []permission.Permission {
	return []permission.Permission{
		permission.TenantsRead,
		permission.TenantsListCross,
		"kv:get",
	}
}

// newRBACTransport builds an embedded transport with a real
// rbac.RBACAuthorizer wired against the supplied role service. The
// authorizer enforces the Phase 0 system-tenant rule, which is the
// behaviour we exercise here.
func newRBACTransport(t *testing.T, roleSvc role.RoleServicer) *Transport {
	t.Helper()
	authz := rbac.New(roleSvc)
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithRoleService(roleSvc),
		WithRBACAuthorizer(authz),
	)
	if err != nil {
		t.Fatalf("failed to create transport: %v", err)
	}
	return tr
}

// rbacRoleSvc returns a mock role service whose ResolvePermissions
// returns the Admin permission set for any role list.
func rbacRoleSvc() *mockRoleServicer {
	return &mockRoleServicer{
		resolvePermsFn: func(_ context.Context, _ []string) ([]permission.Permission, error) {
			return adminPermissions(), nil
		},
	}
}

// ---------------------------------------------------------------------------
// Authorize
// ---------------------------------------------------------------------------

func TestRBACClient_Authorize_AdminAtSystem_CrossPermission(t *testing.T) {
	tr := newRBACTransport(t, rbacRoleSvc())
	client := tr.RBACClient()

	id := &transport.Identity{
		Subject:  "admin",
		TenantID: tenant.SystemTenantID,
		Roles:    []string{"role-admin"},
	}
	// Target tenant is bob-corp; identity is in system. ScopeCross
	// permission must be admitted.
	ctx := tenant.WithTenantID(context.Background(), "bob-corp")
	if err := client.Authorize(ctx, id, "tenants", "list:cross"); err != nil {
		t.Errorf("expected admin@system to authorize tenants:list:cross on bob-corp, got %v", err)
	}
}

func TestRBACClient_Authorize_AdminAtAcme_CrossPermissionDenied(t *testing.T) {
	tr := newRBACTransport(t, rbacRoleSvc())
	client := tr.RBACClient()

	id := &transport.Identity{
		Subject:  "admin",
		TenantID: "acme",
		Roles:    []string{"role-admin"},
	}
	ctx := tenant.WithTenantID(context.Background(), "bob-corp")
	err := client.Authorize(ctx, id, "tenants", "list:cross")
	if err == nil {
		t.Fatal("expected denial of cross-scope permission for non-system identity")
	}
	var denied *rbac.PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Errorf("expected *rbac.PermissionDeniedError, got %T: %v", err, err)
	}
}

func TestRBACClient_Authorize_NilIdentity(t *testing.T) {
	tr := newRBACTransport(t, rbacRoleSvc())
	client := tr.RBACClient()

	err := client.Authorize(context.Background(), nil, "tenants", "read")
	if err == nil {
		t.Fatal("expected InvalidIdentityError, got nil")
	}
	var iie *transport.InvalidIdentityError
	if !errors.As(err, &iie) {
		t.Errorf("expected *transport.InvalidIdentityError, got %T", err)
	}
}

func TestRBACClient_Authorize_EmptyTenantIdentity(t *testing.T) {
	tr := newRBACTransport(t, rbacRoleSvc())
	client := tr.RBACClient()

	id := &transport.Identity{Subject: "alice", TenantID: ""}
	err := client.Authorize(context.Background(), id, "tenants", "read")
	if err == nil {
		t.Fatal("expected InvalidIdentityError, got nil")
	}
}

func TestRBACClient_Authorize_MissingAuthorizer(t *testing.T) {
	// Build a transport WITHOUT an authorizer.
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client := tr.RBACClient()

	id := &transport.Identity{Subject: "alice", TenantID: "acme", Roles: []string{"role-admin"}}
	err = client.Authorize(context.Background(), id, "tenants", "read")
	if err == nil {
		t.Fatal("expected MissingRBACAuthorizerError, got nil")
	}
	var miss *MissingRBACAuthorizerError
	if !errors.As(err, &miss) {
		t.Errorf("expected *MissingRBACAuthorizerError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// ListPermissions
// ---------------------------------------------------------------------------

func TestRBACClient_ListPermissions_ResolvesScopes(t *testing.T) {
	tr := newRBACTransport(t, rbacRoleSvc())
	client := tr.RBACClient()

	id := &transport.Identity{
		Subject:  "admin",
		TenantID: tenant.SystemTenantID,
		Roles:    []string{"role-admin"},
	}

	got, err := client.ListPermissions(context.Background(), id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(adminPermissions()) {
		t.Fatalf("expected %d permissions, got %d", len(adminPermissions()), len(got))
	}

	gotByName := make(map[string]transport.PermissionInfo, len(got))
	for _, p := range got {
		gotByName[p.Name] = p
	}
	cross, ok := gotByName["tenants:list:cross"]
	if !ok {
		t.Fatal("expected tenants:list:cross in result")
	}
	if cross.Scope != transport.PermissionScopeCross {
		t.Errorf("expected ScopeCross, got %q", cross.Scope)
	}

	self, ok := gotByName[string(permission.TenantsRead)]
	if !ok {
		t.Fatal("expected tenants:read in result")
	}
	if self.Scope != transport.PermissionScopeSelf {
		t.Errorf("expected ScopeSelf for tenants:read, got %q", self.Scope)
	}
}

func TestRBACClient_ListPermissions_ResolveError(t *testing.T) {
	mock := &mockRoleServicer{
		resolvePermsFn: func(_ context.Context, _ []string) ([]permission.Permission, error) {
			return nil, errors.New("backend down")
		},
	}
	tr := newRBACTransport(t, mock)
	client := tr.RBACClient()

	id := &transport.Identity{Subject: "alice", TenantID: "acme", Roles: []string{"role-admin"}}
	_, err := client.ListPermissions(context.Background(), id)
	if err == nil {
		t.Fatal("expected error from resolve failure, got nil")
	}
}

func TestRBACClient_ListPermissions_MissingRoleService(t *testing.T) {
	// Inject the authorizer but not the role service.
	authz := rbac.New(rbacRoleSvc())
	tr, err := New(newMockKVService(), &mockAdminService{}, WithRBACAuthorizer(authz))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client := tr.RBACClient()

	id := &transport.Identity{Subject: "alice", TenantID: "acme", Roles: []string{"role-admin"}}
	_, err = client.ListPermissions(context.Background(), id)
	if err == nil {
		t.Fatal("expected MissingRoleServiceForRBACError, got nil")
	}
	var miss *MissingRoleServiceForRBACError
	if !errors.As(err, &miss) {
		t.Errorf("expected *MissingRoleServiceForRBACError, got %T", err)
	}
}

// Verify behaviour-coverage of the underlying authorizer: an Admin@acme
// identity admitted on its own tenant for the self-scope tenants:read.
func TestRBACClient_Authorize_AdminAtAcme_SelfPermission_OnOwnTenant(t *testing.T) {
	tr := newRBACTransport(t, rbacRoleSvc())
	client := tr.RBACClient()

	id := &transport.Identity{Subject: "admin", TenantID: "acme", Roles: []string{"role-admin"}}
	ctx := tenant.WithTenantID(context.Background(), "acme")
	if err := client.Authorize(ctx, id, "tenants", "read"); err != nil {
		t.Errorf("expected admin@acme to authorize tenants:read on acme, got %v", err)
	}
}

func TestRBACClient_Authorize_AdminAtAcme_SelfPermission_OnOtherTenant(t *testing.T) {
	tr := newRBACTransport(t, rbacRoleSvc())
	client := tr.RBACClient()

	id := &transport.Identity{Subject: "admin", TenantID: "acme", Roles: []string{"role-admin"}}
	ctx := tenant.WithTenantID(context.Background(), "bob-corp")
	err := client.Authorize(ctx, id, "tenants", "read")
	if err == nil {
		t.Fatal("expected denial of self-scope permission on other tenant")
	}
	var denied *rbac.PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Errorf("expected *rbac.PermissionDeniedError, got %T", err)
	}
}

// Touch the auth.Identity conversion to make sure metadata round-trips.
func TestRBACClient_IdentityRoundTrip(t *testing.T) {
	id := &transport.Identity{
		Subject:  "alice",
		TenantID: "acme",
		Roles:    []string{"r1", "r2"},
		Metadata: map[string]string{"k": "v"},
	}
	dom := identityToDomain(id)
	if dom.Subject != "alice" || dom.TenantID != "acme" {
		t.Errorf("subject/tenant lost in round-trip")
	}
	if len(dom.Roles) != 2 {
		t.Errorf("expected 2 roles, got %d", len(dom.Roles))
	}
	if dom.Metadata["k"] != "v" {
		t.Errorf("metadata lost")
	}
	// Mutating the domain must not affect the SDK input.
	dom.Roles[0] = "x"
	if id.Roles[0] != "r1" {
		t.Error("identityToDomain didn't copy Roles")
	}
}

// confirm conversion handles nil safely.
func TestRBACClient_IdentityToDomain_Nil(t *testing.T) {
	if got := identityToDomain(nil); got != nil {
		t.Errorf("expected nil for nil input, got %v", got)
	}
}

// confirm the unused identity authorizer reference satisfies auth.Authorizer.
var _ auth.Authorizer = (*rbac.RBACAuthorizer)(nil)
