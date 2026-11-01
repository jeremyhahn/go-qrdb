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

package role

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// heldAuthorizer is an auth.Authorizer granting the permissions in held, in
// the tenants in held, and refusing the rest the way the RBAC authorizer
// does: with an error that is auth.ErrUnauthorized.
type heldAuthorizer struct {
	held  map[string]map[permission.Permission]bool // tenant -> permission
	fault error                                     // returned for every call when set
	asked []string                                  // tenant/permission per call
}

var _ auth.Authorizer = (*heldAuthorizer)(nil)

func (a *heldAuthorizer) Authorize(ctx context.Context, _ *auth.Identity, resource, action string) error {
	tenantID := tenant.FromContext(ctx)
	p := permission.Permission(resource + ":" + action)
	a.asked = append(a.asked, tenantID+"/"+p.String())
	if a.fault != nil {
		return a.fault
	}
	if a.held[tenantID][p] {
		return nil
	}
	return &heldDeniedError{}
}

// heldDeniedError is a refusal, as rbac.PermissionDeniedError is one.
type heldDeniedError struct{}

func (*heldDeniedError) Error() string { return "denied" }
func (*heldDeniedError) Unwrap() error { return auth.ErrUnauthorized }

// heldFaultError is an authorizer failure that is not a refusal.
type heldFaultError struct{}

func (*heldFaultError) Error() string { return "role store unavailable" }

func holding(tenantID string, perms ...permission.Permission) *heldAuthorizer {
	set := map[permission.Permission]bool{}
	for _, p := range perms {
		set[p] = true
	}
	return &heldAuthorizer{held: map[string]map[permission.Permission]bool{tenantID: set}}
}

func principal(tenantID string) context.Context {
	return auth.WithIdentity(tenant.WithTenantID(context.Background(), tenantID),
		&auth.Identity{Subject: "alice", TenantID: tenantID})
}

func wantExceeds(t *testing.T, err error, perm permission.Permission) *GrantExceedsHolderError {
	t.Helper()
	var exceeds *GrantExceedsHolderError
	if !errors.As(err, &exceeds) {
		t.Fatalf("err = %v, want *GrantExceedsHolderError", err)
	}
	if exceeds.Permission != perm.String() || exceeds.Subject != "alice" || exceeds.TenantID != "acme" {
		t.Fatalf("refusal = %+v, want alice refused %s in acme", exceeds, perm)
	}
	return exceeds
}

func TestGrantGuard_CreateRefusesAPermissionNotHeld(t *testing.T) {
	inner := newTestStore(t)
	g := NewGrantGuard(inner, holding("acme", permission.KVGet))
	_, err := g.CreateRole(principal("acme"), &CreateRoleRequest{
		Name: "signer", TenantID: "acme", Permissions: []permission.Permission{permission.KVGet, permission.KeysSign},
	})
	wantExceeds(t, err, permission.KeysSign)
	if r, _ := inner.GetRoleByName(context.Background(), "acme", "signer"); r != nil {
		t.Fatal("the refused role was created")
	}
}

func TestGrantGuard_CreateGrantsAHeldSubset(t *testing.T) {
	g := NewGrantGuard(newTestStore(t), holding("acme", permission.KVGet, permission.KVPut, permission.KVList))
	r, err := g.CreateRole(principal("acme"), &CreateRoleRequest{
		Name: "reader", TenantID: "acme", Permissions: []permission.Permission{permission.KVGet, permission.KVPut},
	})
	if err != nil || r == nil {
		t.Fatalf("CreateRole(held subset) = %v, %v", r, err)
	}
}

func TestGrantGuard_CreateChecksTheEffectiveTenant(t *testing.T) {
	authz := holding("acme", permission.KVGet)
	g := NewGrantGuard(newTestStore(t), authz)
	if _, err := g.CreateRole(principal("acme"), &CreateRoleRequest{
		Name: "reader", Permissions: []permission.Permission{permission.KVGet},
	}); err != nil {
		t.Fatalf("CreateRole(tenant from ctx) = %v", err)
	}
	if len(authz.asked) != 1 || authz.asked[0] != "acme/kv:get" {
		t.Fatalf("asked %v, want [acme/kv:get]", authz.asked)
	}
}

func TestGrantGuard_UpdateRefusesAPermissionNotHeld(t *testing.T) {
	inner := newTestStore(t)
	r := createTestRole(t, inner, "ops", "acme")
	g := NewGrantGuard(inner, holding("acme", permission.KVGet, permission.KVPut))
	_, err := g.UpdateRole(principal("acme"), &UpdateRoleRequest{
		ID: r.ID, Permissions: []permission.Permission{permission.KVGet, permission.RolesAssign},
	})
	wantExceeds(t, err, permission.RolesAssign)
	got, _ := inner.GetRole(context.Background(), r.ID)
	if len(got.Permissions) != 2 || got.Permissions[1] != permission.KVPut {
		t.Fatalf("refused update was applied: %v", got.Permissions)
	}
}

func TestGrantGuard_UpdateGrantsAHeldSet(t *testing.T) {
	inner := newTestStore(t)
	r := createTestRole(t, inner, "ops", "acme")
	g := NewGrantGuard(inner, holding("acme", permission.KVGet, permission.KVPut, permission.KVDelete))
	if _, err := g.UpdateRole(principal("acme"), &UpdateRoleRequest{
		ID: r.ID, Permissions: []permission.Permission{permission.KVGet, permission.KVDelete},
	}); err != nil {
		t.Fatalf("UpdateRole(held set) = %v", err)
	}
}

func TestGrantGuard_UpdateChecksTheRolesOwnTenant(t *testing.T) {
	inner := newTestStore(t)
	r := createTestRole(t, inner, "ops", "globex")
	operator := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice"})
	g := NewGrantGuard(inner, holding(tenant.FromContext(operator), permission.KVGet))
	_, err := g.UpdateRole(operator, &UpdateRoleRequest{ID: r.ID, Permissions: []permission.Permission{permission.KVGet}})
	var exceeds *GrantExceedsHolderError
	if !errors.As(err, &exceeds) || exceeds.TenantID != "globex" {
		t.Fatalf("UpdateRole(role in globex) = %v, want a refusal in globex", err)
	}
}

func TestGrantGuard_UpdateOfAnUnknownRoleIsNotFound(t *testing.T) {
	g := NewGrantGuard(newTestStore(t), holding("acme", permission.KVGet))
	_, err := g.UpdateRole(principal("acme"), &UpdateRoleRequest{ID: "missing", Permissions: []permission.Permission{permission.KVGet}})
	var notFound *RoleNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("UpdateRole(unknown) = %v, want *RoleNotFoundError", err)
	}
}

func TestGrantGuard_UpdateWithoutPermissionsIsNotAGrant(t *testing.T) {
	inner := newTestStore(t)
	r := createTestRole(t, inner, "ops", "acme")
	authz := holding("acme")
	g := NewGrantGuard(inner, authz)
	if _, err := g.UpdateRole(principal("acme"), &UpdateRoleRequest{ID: r.ID, Name: "renamed-duty"}); err != nil {
		t.Fatalf("UpdateRole(name only) = %v", err)
	}
	if len(authz.asked) != 0 {
		t.Fatalf("asked %v for an update granting nothing", authz.asked)
	}
}

func TestGrantGuard_NoPrincipalIsSystemWork(t *testing.T) {
	inner := newTestStore(t)
	authz := holding("acme")
	g := NewGrantGuard(inner, authz)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	r, err := g.CreateRole(ctx, &CreateRoleRequest{Name: "custody", TenantID: "acme", Permissions: []permission.Permission{permission.KeysSign}})
	if err != nil {
		t.Fatalf("system CreateRole = %v", err)
	}
	if _, err := g.UpdateRole(ctx, &UpdateRoleRequest{ID: r.ID, Permissions: []permission.Permission{permission.KeysSign, permission.KVGet}}); err != nil {
		t.Fatalf("system UpdateRole = %v", err)
	}
	if len(authz.asked) != 0 {
		t.Fatalf("asked %v for system work", authz.asked)
	}
}

func TestGrantGuard_InvalidPermissionIsRefusedBeforeTheAuthorizer(t *testing.T) {
	authz := holding("acme")
	g := NewGrantGuard(newTestStore(t), authz)
	_, err := g.CreateRole(principal("acme"), &CreateRoleRequest{
		Name: "bad", TenantID: "acme", Permissions: []permission.Permission{"not-a-permission"},
	})
	var exceeds *GrantExceedsHolderError
	if err == nil || errors.As(err, &exceeds) {
		t.Fatalf("CreateRole(invalid permission) = %v, want a validation error", err)
	}
	if len(authz.asked) != 0 {
		t.Fatalf("asked %v for an invalid permission", authz.asked)
	}
}

func TestNewGrantGuard_NilAuthorizerReturnsTheService(t *testing.T) {
	inner := newTestStore(t)
	if g := NewGrantGuard(inner, nil); g != inner {
		t.Fatal("NewGrantGuard(svc, nil) wrapped the service")
	}
	if g := NewGrantGuard(nil, holding("acme")); g != nil {
		t.Fatal("NewGrantGuard(nil, authz) is not nil")
	}
}

func TestValidateGrantable_WildcardIsCheckedThroughWhatItCovers(t *testing.T) {
	covered := permission.Expand("kv:*")
	if len(covered) == 0 {
		t.Fatal("precondition: kv:* covers no registered permission")
	}
	if err := ValidateGrantable(principal("acme"), holding("acme", covered...), "acme", []permission.Permission{"kv:*"}); err != nil {
		t.Fatalf("ValidateGrantable(kv:*, holder of every kv permission) = %v", err)
	}
	wantExceeds(t, ValidateGrantable(principal("acme"), holding("acme", covered[1:]...), "acme", []permission.Permission{"kv:*"}), covered[0])
}

func TestValidateGrantable_AFaultIsNotADenial(t *testing.T) {
	authz := holding("acme")
	authz.fault = &heldFaultError{}
	err := ValidateGrantable(principal("acme"), authz, "acme", []permission.Permission{permission.KVGet})
	var fault *heldFaultError
	if !errors.As(err, &fault) {
		t.Fatalf("ValidateGrantable(authorizer fault) = %v, want the fault", err)
	}
}

func TestValidateGrantable_NothingToCheck(t *testing.T) {
	authz := holding("acme")
	for name, call := range map[string]func() error{
		"nil authorizer": func() error {
			return ValidateGrantable(principal("acme"), nil, "acme", []permission.Permission{permission.KeysSign})
		},
		"no principal": func() error {
			return ValidateGrantable(context.Background(), authz, "acme", []permission.Permission{permission.KeysSign})
		},
		"no permissions": func() error { return ValidateGrantable(principal("acme"), authz, "acme", nil) },
	} {
		if err := call(); err != nil {
			t.Errorf("%s: ValidateGrantable = %v, want nil", name, err)
		}
	}
	if len(authz.asked) != 0 {
		t.Fatalf("asked %v with nothing to check", authz.asked)
	}
}

func TestValidateAssignable_AssignerMustHoldTheRolesPermissions(t *testing.T) {
	svc := newTestStore(t)
	r := createTestRole(t, svc, "ops", "acme")
	exceeds := wantExceeds(t, ValidateAssignable(principal("acme"), svc, holding("acme", permission.KVGet), r.ID, "acme"), permission.KVPut)
	if exceeds.RoleID != r.ID {
		t.Fatalf("refusal names role %q, want %q", exceeds.RoleID, r.ID)
	}
	if err := ValidateAssignable(principal("acme"), svc, holding("acme", permission.KVGet, permission.KVPut), r.ID, "acme"); err != nil {
		t.Fatalf("ValidateAssignable(holder) = %v", err)
	}
}

func TestValidateAssignable_TenantMismatchIsReportedBeforeTheGrantRule(t *testing.T) {
	svc := newTestStore(t)
	r := createTestRole(t, svc, "ops", "acme")
	authz := holding("globex")
	operator := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice"})
	var notAssignable *NotAssignableError
	if err := ValidateAssignable(operator, svc, authz, r.ID, "globex"); !errors.As(err, &notAssignable) {
		t.Fatalf("ValidateAssignable(other tenant's role) = %v, want *NotAssignableError", err)
	}
	if len(authz.asked) != 0 {
		t.Fatalf("asked %v before the tenant check refused", authz.asked)
	}
}

// seededUser is acme's built-in User role, seeded by ReconcileBuiltInRoles.
func seededUser(t *testing.T, svc RoleServicer) *Role {
	t.Helper()
	if _, err := svc.ReconcileBuiltInRoles(tenant.WithTenantID(context.Background(), "acme"), "acme"); err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}
	r, err := svc.GetRole(principal("acme"), SystemRoleID("acme", RoleNameUser))
	if err != nil || len(r.Permissions) == 0 {
		t.Fatalf("seeded User = %v, %v", r, err)
	}
	return r
}

func TestValidateAssignable_BuiltInRoleKeepsTheDutySplit(t *testing.T) {
	svc := newTestStore(t)
	u := seededUser(t, svc)
	authz := holding("acme")
	if err := ValidateAssignable(principal("acme"), svc, authz, u.ID, "acme"); err != nil {
		t.Fatalf("ValidateAssignable(built-in User, another holder) = %v", err)
	}
	if len(authz.asked) != 0 {
		t.Fatalf("asked %v for a built-in role", authz.asked)
	}
}

func TestValidateSelfAssignable_BuiltInRoleBeyondHoldingsRefused(t *testing.T) {
	svc := newTestStore(t)
	u := seededUser(t, svc)
	exceeds := wantExceeds(t, ValidateSelfAssignable(principal("acme"), svc, holding("acme"), u.ID, "acme"),
		permission.ExpandAll(u.Permissions)[0])
	if exceeds.RoleID != u.ID {
		t.Fatalf("refusal names role %q, want %q", exceeds.RoleID, u.ID)
	}
	if err := ValidateSelfAssignable(principal("acme"), svc, holding("acme", permission.ExpandAll(u.Permissions)...), u.ID, "acme"); err != nil {
		t.Fatalf("ValidateSelfAssignable(holder of every User permission) = %v", err)
	}
}

func TestValidateSelfAssignable_CustomRoleBeyondHoldingsRefused(t *testing.T) {
	svc := newTestStore(t)
	r := createTestRole(t, svc, "ops", "acme")
	wantExceeds(t, ValidateSelfAssignable(principal("acme"), svc, holding("acme", permission.KVGet), r.ID, "acme"), permission.KVPut)
	var notAssignable *NotAssignableError
	if err := ValidateSelfAssignable(principal("acme"), svc, holding("acme"), r.ID, "globex"); !errors.As(err, &notAssignable) {
		t.Fatalf("ValidateSelfAssignable(other tenant) = %v, want *NotAssignableError first", err)
	}
}

func TestIsBuiltIn(t *testing.T) {
	svc := newTestStore(t)
	u := seededUser(t, svc)
	for name, tt := range map[string]struct {
		r    *Role
		want bool
	}{
		"seeded built-in":         {u, true},
		"custom role":             {createTestRole(t, svc, "ops", "acme"), false},
		"reserved name, own id":   {&Role{ID: "r1", Name: RoleNameUser, TenantID: "acme", System: true}, false},
		"built-in id, not system": {&Role{ID: u.ID, Name: RoleNameUser, TenantID: "acme"}, false},
		"nil":                     {nil, false},
	} {
		if got := IsBuiltIn(tt.r); got != tt.want {
			t.Errorf("%s: IsBuiltIn = %v, want %v", name, got, tt.want)
		}
	}
}

func TestGrantExceedsHolderError_Message(t *testing.T) {
	create := (&GrantExceedsHolderError{Subject: "alice", TenantID: "acme", Permission: "keys:sign"}).Error()
	if create != "role: alice cannot grant keys:sign in tenant acme: a grantor may grant only permissions it holds" {
		t.Fatalf("Error() = %q", create)
	}
	assign := (&GrantExceedsHolderError{Subject: "alice", TenantID: "acme", Permission: "keys:sign", RoleID: "r1"}).Error()
	if !strings.Contains(assign, "by assigning role r1") {
		t.Fatalf("Error() = %q, want the role named", assign)
	}
}
