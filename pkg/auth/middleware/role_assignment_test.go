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

package middleware

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// grantingRoleServicer is the ROLE-RESOLUTION side of rbac.New: it answers
// what permissions the caller's OWN roles carry, entirely separate from
// roleAssignmentFixture below, which answers what a REFERENCED role (the one
// being assigned) resolves to.
type grantingRoleServicer struct {
	role.RoleServicer
	perms []permission.Permission
}

func (r *grantingRoleServicer) ResolvePermissions(context.Context, []string) ([]permission.Permission, error) {
	return r.perms, nil
}

// roleAssignmentFixture answers GetRole for the roles named in a write under
// test — the argument AuthorizeRoleAssignmentWrite validates via
// role.ValidateAssignable, never the caller's own granted roles.
type roleAssignmentFixture struct {
	role.RoleServicer
	roles map[string]*role.Role
}

func (r *roleAssignmentFixture) GetRole(_ context.Context, id string) (*role.Role, error) {
	if r, ok := r.roles[id]; ok {
		return r, nil
	}
	return nil, &role.RoleNotFoundError{ID: id}
}

// userTenantFixture answers GetUser with a fixed record, so
// AuthorizeRoleAssignmentWrite resolves its target tenant from the RECORD
// rather than from the caller's ambient context — the round-2 fix under
// test. Every other UserServicer method panics if called; this gate never
// calls them.
type userTenantFixture struct {
	user.UserServicer
	u   *user.User
	err error
}

func (f *userTenantFixture) GetUser(_ context.Context, id uint64) (*user.User, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.u, nil
}

func roleAssignmentCtx(tenantID string) context.Context {
	id := &auth.Identity{Subject: tenantID + "-admin", TenantID: tenantID, Roles: []string{"admin"}}
	return tenant.WithTenantID(auth.WithIdentity(context.Background(), id), tenantID)
}

// mwWithPerms builds an AuthMiddleware backed by the REAL rbac.RBACAuthorizer
// so these tests exercise the actual scope rule permission.RolesAssign
// carries, not a mock that always agrees.
func mwWithPerms(perms ...permission.Permission) *AuthMiddleware {
	return newTestMiddleware(rbac.New(&grantingRoleServicer{perms: perms}))
}

// ---------------------------------------------------------------------------
// AuthorizeRoleAssignmentWrite (update path)
// ---------------------------------------------------------------------------

// TestAuthorizeRoleAssignmentWrite_NilMiddlewareStillValidates asserts a nil
// middleware (no RBAC configured) skips only the roles:assign authorization:
// role references are still validated, so a composition without RBAC cannot
// write a nonexistent or foreign-tenant role, nor one it has no role service
// to check.
func TestAuthorizeRoleAssignmentWrite_NilMiddlewareStillValidates(t *testing.T) {
	var mw *AuthMiddleware
	userSvc := &userTenantFixture{u: &user.User{ID: 1, TenantID: "acme"}}
	roleSvc := &roleAssignmentFixture{roles: map[string]*role.Role{
		"r-acme":   {ID: "r-acme", TenantID: "acme"},
		"r-globex": {ID: "r-globex", TenantID: "globex"},
	}}

	tests := []struct {
		name     string
		roleSvc  role.RoleServicer
		roleIDs  []string
		bindings []user.RoleBinding
		check    func(t *testing.T, err error)
	}{
		{name: "valid role admitted", roleSvc: roleSvc, roleIDs: []string{"r-acme"}, check: wantNoError},
		{name: "missing role refused", roleSvc: roleSvc, roleIDs: []string{"missing"}, check: wantErrorAs[*role.RoleNotFoundError]},
		{name: "foreign role refused", roleSvc: roleSvc, roleIDs: []string{"r-globex"}, check: wantErrorAs[*role.NotAssignableError]},
		{name: "mismatched binding refused", roleSvc: roleSvc, bindings: []user.RoleBinding{{RoleID: "r-globex", TenantID: "acme"}}, check: wantErrorAs[*role.NotAssignableError]},
		{name: "foreign-tenant binding refused", roleSvc: roleSvc, bindings: []user.RoleBinding{{RoleID: "r-globex", TenantID: "globex"}}, check: wantErrorAs[*user.RoleBindingTenantError]},
		{name: "no role service refused", roleIDs: []string{"r-acme"}, check: wantErrorAs[*role.ServiceRequiredError]},
		{name: "attributes only needs nothing", check: wantNoError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, mw.AuthorizeRoleAssignmentWrite(context.Background(), userSvc, tt.roleSvc, 1, tt.roleIDs, tt.bindings))
		})
	}
}

func wantNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func wantErrorAs[E error](t *testing.T, err error) {
	t.Helper()
	var target E
	if !errors.As(err, &target) {
		t.Fatalf("err = %v, want %T", err, target)
	}
}

func TestAuthorizeRoleAssignmentWrite_DeniesWithoutPermission(t *testing.T) {
	mw := mwWithPerms() // no permissions at all
	ctx := roleAssignmentCtx("acme")
	userSvc := &userTenantFixture{u: &user.User{ID: 1, TenantID: "acme"}}

	err := mw.AuthorizeRoleAssignmentWrite(ctx, userSvc, nil, 1, []string{"r1"}, nil)
	if err == nil {
		t.Fatal("a caller without roles:assign must be refused")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("err = %v, want an authorization denial", err)
	}
}

func TestAuthorizeRoleAssignmentWrite_UnauthenticatedIsRefused(t *testing.T) {
	mw := mwWithPerms(permission.RolesAssign)
	userSvc := &userTenantFixture{u: &user.User{ID: 1, TenantID: "acme"}}

	err := mw.AuthorizeRoleAssignmentWrite(context.Background(), userSvc, nil, 1, []string{"r1"}, nil)
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v, want auth.ErrUnauthenticated", err)
	}
}

// TestAuthorizeRoleAssignmentWrite_TargetTenantComesFromTheRecord is the
// round-2 regression test: the target tenant used for the permission check
// and role validation must be the UPDATED USER's own tenant (read via
// GetUser), never the caller's ambient context tenant. A system-tenant
// caller updating a tenant-A user must be authorized, and its RoleIDs
// validated, against tenant A — not "system".
func TestAuthorizeRoleAssignmentWrite_TargetTenantComesFromTheRecord(t *testing.T) {
	mw := mwWithPerms(permission.RolesAssign)
	// The identity is acme's own admin, but the incoming ctx was already
	// bound to an unrelated tenant (as if inherited from an earlier hop in
	// the same request chain). The record being updated belongs to "acme".
	// The permission check must be decided against the RECORD's tenant
	// ("acme") -- which matches the identity -- never whatever tenant the
	// incoming ctx happened to carry.
	id := &auth.Identity{Subject: "acme-admin", TenantID: "acme", Roles: []string{"admin"}}
	ctx := tenant.WithTenantID(auth.WithIdentity(context.Background(), id), "stale-unrelated-tenant")
	userSvc := &userTenantFixture{u: &user.User{ID: 42, TenantID: "acme"}}
	roleSvc := &roleAssignmentFixture{roles: map[string]*role.Role{
		"r-acme": {ID: "r-acme", TenantID: "acme"},
	}}

	if err := mw.AuthorizeRoleAssignmentWrite(ctx, userSvc, roleSvc, 42, []string{"r-acme"}, nil); err != nil {
		t.Fatalf("a role belonging to the RECORD's tenant must be admitted even though the incoming ctx named a different tenant: %v", err)
	}
}

func TestAuthorizeRoleAssignmentWrite_GetUserErrorPropagates(t *testing.T) {
	mw := mwWithPerms(permission.RolesAssign)
	ctx := roleAssignmentCtx("acme")
	userSvc := &userTenantFixture{err: &user.UserNotFoundError{ID: 99}}

	err := mw.AuthorizeRoleAssignmentWrite(ctx, userSvc, nil, 99, []string{"r1"}, nil)
	var unf *user.UserNotFoundError
	if !errors.As(err, &unf) {
		t.Fatalf("a GetUser failure must propagate verbatim, got %v", err)
	}
}

func TestAuthorizeRoleAssignmentWrite_AllowsWithPermissionAndNilRoleSvcWhenNoReferences(t *testing.T) {
	mw := mwWithPerms(permission.RolesAssign)
	ctx := roleAssignmentCtx("acme")
	userSvc := &userTenantFixture{u: &user.User{ID: 1, TenantID: "acme"}}

	// roleIDs and bindings both empty: an Attributes-only write has no role
	// reference to validate, so a nil roleSvc must not be refused.
	if err := mw.AuthorizeRoleAssignmentWrite(ctx, userSvc, nil, 1, nil, nil); err != nil {
		t.Fatalf("AuthorizeRoleAssignmentWrite(nil roleSvc, no references) = %v, want nil", err)
	}
}

// TestAuthorizeRoleAssignmentWrite_NilRoleSvcRefusesWhenReferencesPresent is
// the round-2 regression test for "a nil role service disables mandatory
// validation": whenever roleIDs or bindings is non-empty, a nil roleSvc must
// REFUSE the write, not silently skip validation.
func TestAuthorizeRoleAssignmentWrite_NilRoleSvcRefusesWhenReferencesPresent(t *testing.T) {
	mw := mwWithPerms(permission.RolesAssign)
	ctx := roleAssignmentCtx("acme")
	userSvc := &userTenantFixture{u: &user.User{ID: 1, TenantID: "acme"}}

	err := mw.AuthorizeRoleAssignmentWrite(ctx, userSvc, nil, 1, []string{"r1"}, nil)
	var required *role.ServiceRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("a nil role service with RoleIDs present must refuse with *role.ServiceRequiredError, got %v", err)
	}
}

func TestAuthorizeRoleAssignmentWrite_ValidatesRoleIDsAgainstTargetTenant(t *testing.T) {
	mw := mwWithPerms(permission.RolesAssign)
	ctx := roleAssignmentCtx("acme")
	userSvc := &userTenantFixture{u: &user.User{ID: 1, TenantID: "acme"}}
	roleSvc := &roleAssignmentFixture{roles: map[string]*role.Role{
		"r-acme":   {ID: "r-acme", TenantID: "acme"},
		"r-globex": {ID: "r-globex", TenantID: "globex"},
	}}

	if err := mw.AuthorizeRoleAssignmentWrite(ctx, userSvc, roleSvc, 1, []string{"r-acme"}, nil); err != nil {
		t.Fatalf("a role that belongs to the target tenant must be admitted: %v", err)
	}

	err := mw.AuthorizeRoleAssignmentWrite(ctx, userSvc, roleSvc, 1, []string{"r-globex"}, nil)
	var na *role.NotAssignableError
	if !errors.As(err, &na) {
		t.Fatalf("a role from a DIFFERENT tenant must be refused, got %v", err)
	}
}

func TestAuthorizeRoleAssignmentWrite_ValidatesRoleIDsPropagatesNotFound(t *testing.T) {
	mw := mwWithPerms(permission.RolesAssign)
	ctx := roleAssignmentCtx("acme")
	userSvc := &userTenantFixture{u: &user.User{ID: 1, TenantID: "acme"}}
	roleSvc := &roleAssignmentFixture{roles: map[string]*role.Role{}}

	err := mw.AuthorizeRoleAssignmentWrite(ctx, userSvc, roleSvc, 1, []string{"missing"}, nil)
	var rnf *role.RoleNotFoundError
	if !errors.As(err, &rnf) {
		t.Fatalf("a nonexistent role must propagate RoleNotFoundError, got %v", err)
	}
}

// TestAuthorizeRoleAssignmentWrite_BindingsValidateAgainstTheirOwnTenant is
// the item-4-adjacent guarantee for item 3: a RoleBinding is validated
// against ITS OWN TenantID, not the target user's home tenant, because a
// binding may legitimately name a tenant other than it (tenant-scoped
// grants).
// TestAuthorizeRoleAssignmentWrite_ForeignTenantBindingRefused: a RoleBinding
// may name only the user's own tenant. A binding naming another is refused
// with *user.RoleBindingTenantError whoever writes it -- a system-tenant
// caller, whose roles:assign ScopeSelf admits in every tenant, a tenant
// caller holding roles:assign in the user's tenant, and a composition with
// no RBAC at all -- and whether or not the role it names belongs to the
// tenant it names.
func TestAuthorizeRoleAssignmentWrite_ForeignTenantBindingRefused(t *testing.T) {
	userSvc := &userTenantFixture{u: &user.User{ID: 1, TenantID: "acme"}}
	roleSvc := &roleAssignmentFixture{roles: map[string]*role.Role{
		"r-acme":   {ID: "r-acme", TenantID: "acme"},
		"r-globex": {ID: "r-globex", TenantID: "globex"},
	}}
	// A system caller is not in this table: the system-tenant lift was
	// removed (owner ruling, 2026-10-01), so a system caller now fails
	// roles:assign authorization against "acme" before reaching the binding
	// check at all, which is a *different*, already-covered scope denial
	// (see TestAuthorizeRoleAssignmentWrite_TargetTenantComesFromTheRecord's
	// sibling tests in rbac_test.go). This table covers callers that DO
	// reach the binding check.
	callers := []struct {
		name string
		mw   *AuthMiddleware
		ctx  context.Context
	}{
		{"tenant caller", mwWithPerms(permission.RolesAssign), roleAssignmentCtx("acme")},
		{"no RBAC", nil, context.Background()},
	}
	for _, c := range callers {
		t.Run(c.name, func(t *testing.T) {
			for _, b := range []user.RoleBinding{
				{RoleID: "r-globex", TenantID: "globex"},
				{RoleID: "r-acme", TenantID: "globex"},
				{RoleID: "admin", TenantID: tenantid.System},
				{RoleID: "admin", TenantID: ""},
			} {
				err := c.mw.AuthorizeRoleAssignmentWrite(c.ctx, userSvc, roleSvc, 1, nil, []user.RoleBinding{{RoleID: "r-acme", TenantID: "acme"}, b})
				var be *user.RoleBindingTenantError
				if !errors.As(err, &be) {
					t.Fatalf("binding %+v: err = %v, want *user.RoleBindingTenantError", b, err)
				}
				if be.UserTenantID != "acme" || be.BindingTenantID != tenantid.Normalize(b.TenantID) {
					t.Errorf("binding %+v: error names user=%q binding=%q", b, be.UserTenantID, be.BindingTenantID)
				}
			}
		})
	}
}

// TestAuthorizeRoleAssignmentWrite_OwnTenantBindingValidated: a binding in
// the user's own tenant is admitted when its role belongs to that tenant and
// refused with *role.NotAssignableError when it does not.
func TestAuthorizeRoleAssignmentWrite_OwnTenantBindingValidated(t *testing.T) {
	mw := mwWithPerms(permission.RolesAssign)
	ctx := roleAssignmentCtx("acme")
	userSvc := &userTenantFixture{u: &user.User{ID: 1, TenantID: "acme"}}
	roleSvc := &roleAssignmentFixture{roles: map[string]*role.Role{
		"r-acme":   {ID: "r-acme", TenantID: "acme"},
		"r-globex": {ID: "r-globex", TenantID: "globex"},
	}}

	if err := mw.AuthorizeRoleAssignmentWrite(ctx, userSvc, roleSvc, 1, nil, []user.RoleBinding{{RoleID: "r-acme", TenantID: "acme"}}); err != nil {
		t.Fatalf("own-tenant binding of an own-tenant role: %v", err)
	}
	err := mw.AuthorizeRoleAssignmentWrite(ctx, userSvc, roleSvc, 1, nil, []user.RoleBinding{{RoleID: "r-globex", TenantID: "acme"}})
	var na *role.NotAssignableError
	if !errors.As(err, &na) {
		t.Fatalf("own-tenant binding of a foreign role: err = %v, want *role.NotAssignableError", err)
	}
}

// TestAuthorizeRoleAssignmentWrite_AuthorizationPrecedesBindingCheck: a
// caller refused roles:assign in the user's tenant learns that, not whether
// the binding it named would have been accepted.
func TestAuthorizeRoleAssignmentWrite_AuthorizationPrecedesBindingCheck(t *testing.T) {
	mw := mwWithPerms(permission.RolesAssign)
	ctx := roleAssignmentCtx("globex")
	userSvc := &userTenantFixture{u: &user.User{ID: 1, TenantID: "acme"}}
	roleSvc := &roleAssignmentFixture{roles: map[string]*role.Role{}}

	err := mw.AuthorizeRoleAssignmentWrite(ctx, userSvc, roleSvc, 1, nil, []user.RoleBinding{{RoleID: "r", TenantID: "globex"}})
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("err = %v, want an authorization denial", err)
	}
	var be *user.RoleBindingTenantError
	if errors.As(err, &be) {
		t.Fatalf("err = %v: the binding check ran before authorization", err)
	}
}

// TestAuthorizeRoleAssignmentWrite_BindingRequiresRolesAssignInItsOwnTenant
// is the round-2 regression test for "roles:assign is checked only in the
// user's tenant, but each binding can name another tenant": a caller who
// holds roles:assign ONLY in the target user's own tenant ("acme") must
// still be refused a binding that names a DIFFERENT tenant ("globex"),
// because writing that binding is itself an act of granting a role WITHIN
// globex, not acme.
// ---------------------------------------------------------------------------
// AuthorizeRoleAssignmentWriteOnCreate
// ---------------------------------------------------------------------------

// TestAuthorizeRoleAssignmentWriteOnCreate_NilMiddlewareStillValidates is the
// create-path twin of TestAuthorizeRoleAssignmentWrite_NilMiddlewareStillValidates.
func TestAuthorizeRoleAssignmentWriteOnCreate_NilMiddlewareStillValidates(t *testing.T) {
	var mw *AuthMiddleware
	roleSvc := &roleAssignmentFixture{roles: map[string]*role.Role{
		"r-acme":   {ID: "r-acme", TenantID: "acme"},
		"r-globex": {ID: "r-globex", TenantID: "globex"},
	}}

	tests := []struct {
		name    string
		roleSvc role.RoleServicer
		roleIDs []string
		check   func(t *testing.T, err error)
	}{
		{name: "valid role admitted", roleSvc: roleSvc, roleIDs: []string{"r-acme"}, check: wantNoError},
		{name: "missing role refused", roleSvc: roleSvc, roleIDs: []string{"missing"}, check: wantErrorAs[*role.RoleNotFoundError]},
		{name: "foreign role refused", roleSvc: roleSvc, roleIDs: []string{"r-globex"}, check: wantErrorAs[*role.NotAssignableError]},
		{name: "no role service refused", roleIDs: []string{"r-acme"}, check: wantErrorAs[*role.ServiceRequiredError]},
	}
	// Bound to the same tenant CreateUser is creating in: EffectiveTenant's
	// cross-tenant refusal applies to every identity, nil-middleware
	// composition included (owner ruling, 2026-10-01), so the ctx must
	// already name the target tenant for this test to reach the validation
	// it actually means to exercise.
	ctx := tenant.WithTenantID(context.Background(), "acme")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, mw.AuthorizeRoleAssignmentWriteOnCreate(ctx, tt.roleSvc, "acme", tt.roleIDs))
		})
	}
}

func TestAuthorizeRoleAssignmentWriteOnCreate_NoRoleIDsIsNoOp(t *testing.T) {
	mw := mwWithPerms() // no permissions at all — would deny if reached
	if err := mw.AuthorizeRoleAssignmentWriteOnCreate(context.Background(), nil, "acme", nil); err != nil {
		t.Fatalf("no RoleIDs must be a no-op, got %v", err)
	}
}

// TestAuthorizeRoleAssignmentWriteOnCreate_DeniesWithoutPermission is the
// round-2 regression test for "CreateUser accepts RoleIDs without
// roles:assign authorization or role existence/tenant validation".
func TestAuthorizeRoleAssignmentWriteOnCreate_DeniesWithoutPermission(t *testing.T) {
	mw := mwWithPerms() // no permissions at all
	ctx := roleAssignmentCtx("acme")

	err := mw.AuthorizeRoleAssignmentWriteOnCreate(ctx, nil, "acme", []string{"r1"})
	if err == nil {
		t.Fatal("a caller without roles:assign must be refused when CreateUser names RoleIDs")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("err = %v, want an authorization denial", err)
	}
}

func TestAuthorizeRoleAssignmentWriteOnCreate_NilRoleSvcRefusesWhenRoleIDsPresent(t *testing.T) {
	mw := mwWithPerms(permission.RolesAssign)
	ctx := roleAssignmentCtx("acme")

	err := mw.AuthorizeRoleAssignmentWriteOnCreate(ctx, nil, "acme", []string{"r1"})
	var required *role.ServiceRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("a nil role service with RoleIDs present must refuse with *role.ServiceRequiredError, got %v", err)
	}
}

// TestAuthorizeRoleAssignmentWriteOnCreate_UsesEffectiveTenant asserts the
// target tenant is CreateUser's own EFFECTIVE tenant resolution
// (tenant.EffectiveTenant), not the caller's ambient tenant taken at face
// value: a system-tenant caller creating a user explicitly in tenant "acme"
// must have its RoleIDs validated against "acme".
// TestAuthorizeRoleAssignmentWriteOnCreate_UsesEffectiveTenant asserts a
// system-tenant caller naming a DIFFERENT tenant than its own is refused by
// tenant.EffectiveTenant before role validation ever runs -- the
// system-tenant lift was removed (owner ruling, 2026-10-01), so this is now
// symmetric with TestAuthorizeRoleAssignmentWriteOnCreate_CrossTenantRequestDenied's
// non-system case rather than its opposite.
func TestAuthorizeRoleAssignmentWriteOnCreate_UsesEffectiveTenant(t *testing.T) {
	mw := mwWithPerms(permission.RolesAssign)
	ctx := roleAssignmentCtx(tenantid.System)
	roleSvc := &roleAssignmentFixture{roles: map[string]*role.Role{
		"r-acme": {ID: "r-acme", TenantID: "acme"},
	}}

	err := mw.AuthorizeRoleAssignmentWriteOnCreate(ctx, roleSvc, "acme", []string{"r-acme"})
	var denied *tenant.CrossTenantDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("a system-tenant caller naming a different tenant must be refused: err = %v, want *tenant.CrossTenantDeniedError", err)
	}
}

// TestAuthorizeRoleAssignmentWriteOnCreate_CrossTenantRequestDenied asserts
// that a non-system caller naming a DIFFERENT tenant than its own is refused
// by tenant.EffectiveTenant before role validation ever runs.
func TestAuthorizeRoleAssignmentWriteOnCreate_CrossTenantRequestDenied(t *testing.T) {
	mw := mwWithPerms(permission.RolesAssign)
	ctx := roleAssignmentCtx("acme")

	err := mw.AuthorizeRoleAssignmentWriteOnCreate(ctx, nil, "globex", []string{"r1"})
	var denied *tenant.CrossTenantDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("a non-system caller naming a foreign tenant must be denied, got %v", err)
	}
}

func TestAuthorizeRoleAssignmentWriteOnCreate_ValidatesRoleNotFound(t *testing.T) {
	mw := mwWithPerms(permission.RolesAssign)
	ctx := roleAssignmentCtx("acme")
	roleSvc := &roleAssignmentFixture{roles: map[string]*role.Role{}}

	err := mw.AuthorizeRoleAssignmentWriteOnCreate(ctx, roleSvc, "acme", []string{"missing"})
	var rnf *role.RoleNotFoundError
	if !errors.As(err, &rnf) {
		t.Fatalf("a nonexistent role must propagate RoleNotFoundError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TouchesRoleAssignment
// ---------------------------------------------------------------------------

func TestTouchesRoleAssignment(t *testing.T) {
	tests := []struct {
		name       string
		roleIDs    []string
		bindings   []user.RoleBinding
		attributes map[string]string
		want       bool
	}{
		{"all nil", nil, nil, nil, false},
		{"role ids set", []string{"r1"}, nil, nil, true},
		{"role ids empty non-nil (clear)", []string{}, nil, nil, true},
		{"bindings set", nil, []user.RoleBinding{{RoleID: "r1", TenantID: "acme"}}, nil, true},
		{"bindings empty non-nil (clear)", nil, []user.RoleBinding{}, nil, true},
		{"attributes set", nil, nil, map[string]string{"k": "v"}, true},
		{"attributes empty non-nil (clear)", nil, nil, map[string]string{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TouchesRoleAssignment(tt.roleIDs, tt.bindings, tt.attributes); got != tt.want {
				t.Errorf("TouchesRoleAssignment() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAuthorizeRoleUnassignment(t *testing.T) {
	acme := &userTenantFixture{u: &user.User{ID: 1, TenantID: "acme"}}
	tests := []struct {
		name  string
		mw    *AuthMiddleware
		ctx   context.Context
		users user.UserServicer
		check func(t *testing.T, err error)
	}{
		{name: "no RBAC configured", mw: nil, ctx: context.Background(), users: acme, check: wantNoError},
		{name: "granted in the user's tenant", mw: mwWithPerms(permission.RolesUnassign), ctx: roleAssignmentCtx("acme"), users: acme, check: wantNoError},
		{name: "caller of another tenant", mw: mwWithPerms(permission.RolesUnassign), ctx: roleAssignmentCtx("globex"), users: acme, check: func(t *testing.T, err error) {
			if !errors.Is(err, auth.ErrUnauthorized) {
				t.Fatalf("err = %v, want an authorization denial", err)
			}
		}},
		{name: "unknown user", mw: mwWithPerms(permission.RolesUnassign), ctx: roleAssignmentCtx("acme"), users: &userTenantFixture{err: &user.UserNotFoundError{ID: 1}}, check: wantErrorAs[*user.UserNotFoundError]},
		{name: "contract-violating nil user", mw: mwWithPerms(permission.RolesUnassign), ctx: roleAssignmentCtx("acme"), users: &userTenantFixture{}, check: wantErrorAs[*user.UserNotFoundError]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, tt.mw.AuthorizeRoleUnassignment(tt.ctx, tt.users, 1, "r1"))
		})
	}
}

// soRoleFixture is the acme tenant's built-in roles with every caller holding
// roles:assign and roles:unassign, so each SO-role test below reaches the SO
// gate; whether it passes is the only question left. admin, so and userRole
// are acme's built-in role IDs and sysSO the system tenant's SO role.
type soRoleFixture struct {
	mw                         *AuthMiddleware
	roles                      role.RoleServicer
	admin, so, userRole, sysSO string
}

func newSORoleFixture(t *testing.T) soRoleFixture {
	t.Helper()
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme")
	return soRoleFixture{
		mw:       newTestMiddleware(rbac.New(&grantingRoleServicer{perms: []permission.Permission{permission.RolesAssign, permission.RolesUnassign}}), WithRoleResolver(roles), WithStrictSeparation(true)),
		roles:    roles,
		admin:    role.SystemRoleID("acme", role.RoleNameAdmin),
		so:       role.SystemRoleID("acme", role.RoleNameSO),
		userRole: role.SystemRoleID("acme", role.RoleNameUser),
		sysSO:    role.SystemRoleID(tenantid.System, role.RoleNameSO),
	}
}

// caller is an acme identity holding roleIDs, bound to its tenant as the
// transport's authentication step binds it.
func (f soRoleFixture) caller(roleIDs ...string) context.Context {
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "acme-caller", TenantID: "acme", Roles: roleIDs})
	return tenant.WithTenantID(ctx, "acme")
}

// TestAuthorizeSORoleChange decides the SO gate itself: a change in whether
// the account holds acme's SO role passes only for acme's own SO, a write
// leaving that membership as it was is not the gate's, and a role store that
// cannot answer refuses rather than admits.
func TestAuthorizeSORoleChange(t *testing.T) {
	f := newSORoleFixture(t)
	sysSO := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "system-so", TenantID: tenantid.System, Roles: []string{f.sysSO}})
	unresolvable := newTestMiddleware(rbac.New(&grantingRoleServicer{}), WithRoleResolver(failingRoles{err: &role.StoreError{Op: "get", Err: context.DeadlineExceeded}}), WithStrictSeparation(true))
	standard := newTestMiddleware(rbac.New(&grantingRoleServicer{perms: []permission.Permission{permission.RolesAssign, permission.RolesUnassign}}), WithRoleResolver(failingRoles{err: &role.StoreError{Op: "get", Err: context.DeadlineExceeded}}))
	tests := []struct {
		name          string
		mw            *AuthMiddleware
		ctx           context.Context
		before, after []string
		check         func(t *testing.T, err error)
	}{
		{name: "admin grants SO", mw: f.mw, ctx: f.caller(f.admin), after: []string{f.so}, check: wantErrorAs[*SORoleRequiredError]},
		{name: "admin revokes SO", mw: f.mw, ctx: f.caller(f.admin), before: []string{f.so, f.userRole}, after: []string{f.userRole}, check: wantErrorAs[*SORoleRequiredError]},
		{name: "admin changes other roles of an SO", mw: f.mw, ctx: f.caller(f.admin), before: []string{f.so}, after: []string{f.userRole, f.so}, check: wantNoError},
		{name: "admin grants a non-SO role", mw: f.mw, ctx: f.caller(f.admin), after: []string{f.userRole}, check: wantNoError},
		{name: "tenant SO grants SO", mw: f.mw, ctx: f.caller(f.admin, f.so), after: []string{f.so}, check: wantNoError},
		{name: "tenant SO revokes SO", mw: f.mw, ctx: f.caller(f.so), before: []string{f.so}, check: wantNoError},
		{name: "system SO grants the tenant's SO", mw: f.mw, ctx: sysSO, after: []string{f.so}, check: wantErrorAs[*SORoleRequiredError]},
		{name: "another tenant's SO role is not this tenant's", mw: f.mw, ctx: f.caller(f.admin), after: []string{f.sysSO}, check: wantNoError},
		{name: "no identity", mw: f.mw, ctx: context.Background(), after: []string{f.so}, check: wantErrorAs[*SORoleRequiredError]},
		{name: "unresolvable SO role", mw: unresolvable, ctx: f.caller(f.so), after: []string{f.so}, check: wantErrorAs[*SORoleUnresolvedError]},
		{name: "unchanged set needs no resolution", mw: unresolvable, ctx: f.caller(f.admin), before: []string{f.so}, after: []string{f.so}, check: wantNoError},
		{name: "no RBAC", ctx: context.Background(), after: []string{f.so}, check: wantNoError},
		{name: "standard: admin grants SO", mw: standard, ctx: f.caller(f.admin), after: []string{f.so}, check: wantNoError},
		{name: "standard: admin revokes SO", mw: standard, ctx: f.caller(f.admin), before: []string{f.so, f.userRole}, after: []string{f.userRole}, check: wantNoError},
		{name: "standard: system SO grants the tenant's SO", mw: standard, ctx: sysSO, after: []string{f.so}, check: wantNoError},
		{name: "standard: no SO role resolution", mw: standard, ctx: context.Background(), after: []string{f.so}, check: wantNoError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, tt.mw.authorizeSORoleChange(tt.ctx, "acme", tt.before, tt.after))
		})
	}
}

// TestSORoleGate_EveryRoleWrite drives the SO gate through each exported
// role-assignment gate, with the role sets each one derives from the record
// and the request: AssignRole appends, UnassignRole removes from RoleIDs
// alone, UpdateUser replaces RoleIDs and RoleBindings independently, and
// CreateUser starts from nothing.
func TestSORoleGate_EveryRoleWrite(t *testing.T) {
	f := newSORoleFixture(t)
	admin, so := f.caller(f.admin), f.caller(f.admin, f.so)
	acmeUser := func(roleIDs []string, bindings ...user.RoleBinding) *userTenantFixture {
		return &userTenantFixture{u: &user.User{ID: 7, TenantID: "acme", RoleIDs: roleIDs, RoleBindings: bindings}}
	}
	soBinding := user.RoleBinding{RoleID: f.so, TenantID: "acme"}
	tests := []struct {
		name  string
		call  func() error
		check func(t *testing.T, err error)
	}{
		{"assign SO as admin", func() error { return f.mw.AuthorizeRoleAssignment(admin, acmeUser(nil), f.roles, 7, f.so) }, wantErrorAs[*SORoleRequiredError]},
		{"assign SO as SO", func() error { return f.mw.AuthorizeRoleAssignment(so, acmeUser(nil), f.roles, 7, f.so) }, wantNoError},
		{"assign SO it already holds as admin", func() error { return f.mw.AuthorizeRoleAssignment(admin, acmeUser([]string{f.so}), f.roles, 7, f.so) }, wantNoError},
		{"assign another role to an SO as admin", func() error {
			return f.mw.AuthorizeRoleAssignment(admin, acmeUser([]string{f.so}), f.roles, 7, f.userRole)
		}, wantNoError},
		{"unassign SO as admin", func() error { return f.mw.AuthorizeRoleUnassignment(admin, acmeUser([]string{f.so}), 7, f.so) }, wantErrorAs[*SORoleRequiredError]},
		{"unassign SO as SO", func() error { return f.mw.AuthorizeRoleUnassignment(so, acmeUser([]string{f.so}), 7, f.so) }, wantNoError},
		{"unassign SO a binding still grants as admin", func() error {
			return f.mw.AuthorizeRoleUnassignment(admin, acmeUser([]string{f.so}, soBinding), 7, f.so)
		}, wantNoError},
		{"unassign another role from an SO as admin", func() error {
			return f.mw.AuthorizeRoleUnassignment(admin, acmeUser([]string{f.so, f.userRole}), 7, f.userRole)
		}, wantNoError},
		{"update role_ids granting SO as admin", func() error {
			return f.mw.AuthorizeRoleAssignmentWrite(admin, acmeUser(nil), f.roles, 7, []string{f.so}, nil)
		}, wantErrorAs[*SORoleRequiredError]},
		{"update role_ids dropping SO as admin", func() error {
			return f.mw.AuthorizeRoleAssignmentWrite(admin, acmeUser([]string{f.so}), f.roles, 7, []string{}, nil)
		}, wantErrorAs[*SORoleRequiredError]},
		{"update role_bindings granting SO as admin", func() error {
			return f.mw.AuthorizeRoleAssignmentWrite(admin, acmeUser(nil), f.roles, 7, nil, []user.RoleBinding{soBinding})
		}, wantErrorAs[*SORoleRequiredError]},
		{"update role_bindings dropping SO as admin", func() error {
			return f.mw.AuthorizeRoleAssignmentWrite(admin, acmeUser(nil, soBinding), f.roles, 7, nil, []user.RoleBinding{})
		}, wantErrorAs[*SORoleRequiredError]},
		{"update role_ids keeping a bound SO as admin", func() error {
			return f.mw.AuthorizeRoleAssignmentWrite(admin, acmeUser(nil, soBinding), f.roles, 7, []string{f.userRole}, nil)
		}, wantNoError},
		{"update role_ids granting SO as SO", func() error {
			return f.mw.AuthorizeRoleAssignmentWrite(so, acmeUser(nil), f.roles, 7, []string{f.so}, nil)
		}, wantNoError},
		{"update attributes of an SO as admin", func() error {
			return f.mw.AuthorizeRoleAssignmentWrite(admin, acmeUser([]string{f.so}), f.roles, 7, nil, nil)
		}, wantNoError},
		{"create with SO as admin", func() error {
			return f.mw.AuthorizeRoleAssignmentWriteOnCreate(admin, f.roles, "acme", []string{f.userRole, f.so})
		}, wantErrorAs[*SORoleRequiredError]},
		{"create with SO as SO", func() error { return f.mw.AuthorizeRoleAssignmentWriteOnCreate(so, f.roles, "acme", []string{f.so}) }, wantNoError},
		{"create without SO as admin", func() error {
			return f.mw.AuthorizeRoleAssignmentWriteOnCreate(admin, f.roles, "acme", []string{f.userRole})
		}, wantNoError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, tt.call())
		})
	}
}

// TestAuthorizeRoleAssignment covers AssignRole's gate apart from the SO
// rule: the record decides the tenant, a missing or unreadable record
// refuses, roles:assign is required, and the role is validated with or
// without RBAC.
func TestAuthorizeRoleAssignment(t *testing.T) {
	f := newSORoleFixture(t)
	acmeUser := &userTenantFixture{u: &user.User{ID: 7, TenantID: "acme"}}
	tests := []struct {
		name  string
		mw    *AuthMiddleware
		ctx   context.Context
		users user.UserServicer
		roles role.RoleServicer
		check func(t *testing.T, err error)
	}{
		{name: "admitted", mw: f.mw, ctx: f.caller(f.admin), users: acmeUser, roles: f.roles, check: wantNoError},
		{name: "no roles:assign", mw: mwWithPerms(), ctx: f.caller(f.admin), users: acmeUser, roles: f.roles, check: func(t *testing.T, err error) {
			if !errors.Is(err, auth.ErrUnauthorized) {
				t.Fatalf("err = %v, want an authorization denial", err)
			}
		}},
		{name: "user lookup fails", mw: f.mw, ctx: f.caller(f.admin), users: &userTenantFixture{err: &user.UserNotFoundError{ID: 7}}, roles: f.roles, check: wantErrorAs[*user.UserNotFoundError]},
		{name: "nil record", mw: f.mw, ctx: f.caller(f.admin), users: &userTenantFixture{}, roles: f.roles, check: wantErrorAs[*user.UserNotFoundError]},
		{name: "no RBAC still validates", ctx: context.Background(), users: acmeUser, check: wantErrorAs[*role.ServiceRequiredError]},
		{name: "unknown role", mw: f.mw, ctx: f.caller(f.admin), users: acmeUser, roles: &roleAssignmentFixture{roles: map[string]*role.Role{}}, check: wantErrorAs[*role.RoleNotFoundError]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, tt.mw.AuthorizeRoleAssignment(tt.ctx, tt.users, tt.roles, 7, f.userRole))
		})
	}
}
