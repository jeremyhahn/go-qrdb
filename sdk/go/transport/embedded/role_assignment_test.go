// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// The role-assignment write gate (go-qrdb#192 item 3 round 2: "embedded
// UpdateUser bypasses the new authorization/validation gate entirely"), on
// the embedded transport.
package embedded

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// roleAssignmentIdentityCtx builds a ctx carrying an authenticated identity
// via the SAME auth.WithIdentity/tenant.WithTenantID convention
// AuthorizeTenant reads, which is this gate's identity source on the
// embedded transport.
func roleAssignmentIdentityCtx(tenantID string, roles ...string) context.Context {
	id := &auth.Identity{Subject: "svc-1", TenantID: tenantID, Roles: roles}
	return tenant.WithTenantID(auth.WithIdentity(context.Background(), id), tenantID)
}

// TestEmbeddedCreateUser_NoMiddlewareSkipsOnlyAuthorization asserts an
// embedder that installs no AuthMiddleware (sdk/go/node with auth disabled)
// gets no roles:assign check, exactly like a network transport without one,
// while role references are still validated.
func TestEmbeddedCreateUser_NoMiddlewareSkipsOnlyAuthorization(t *testing.T) {
	roleSvc := &mockRoleServicer{
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			if id != "role-a" {
				return nil, &role.RoleNotFoundError{ID: id}
			}
			return &role.Role{ID: id, TenantID: "acme"}, nil
		},
	}
	userSvc := &mockUserServicer{
		createUserFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			return &user.User{ID: 1, Username: req.Username, TenantID: req.TenantID}, nil
		},
	}

	tests := []struct {
		name    string
		opts    []Option
		roleIDs []string
		check   func(t *testing.T, err error)
	}{
		{name: "valid role admitted", opts: []Option{WithRoleService(roleSvc)}, roleIDs: []string{"role-a"}, check: func(t *testing.T, err error) {
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
		}},
		{name: "missing role refused", opts: []Option{WithRoleService(roleSvc)}, roleIDs: []string{"role-x"}, check: func(t *testing.T, err error) {
			var rnf *role.RoleNotFoundError
			if !errors.As(err, &rnf) {
				t.Fatalf("err = %v, want *role.RoleNotFoundError", err)
			}
		}},
		{name: "no role service refused", roleIDs: []string{"role-a"}, check: func(t *testing.T, err error) {
			var req *role.ServiceRequiredError
			if !errors.As(err, &req) {
				t.Fatalf("err = %v, want *role.ServiceRequiredError", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, err := New(newMockKVService(), &mockAdminService{}, append([]Option{WithUserService(userSvc)}, tt.opts...)...)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = tr.CreateUser(tenant.WithTenantID(context.Background(), "acme"), &transport.CreateUserRequest{
				Username: "alice", TenantID: "acme", RoleIDs: tt.roleIDs,
			})
			tt.check(t, err)
		})
	}
}

// TestEmbeddedCreateUser_DeniedWithoutRolesAssign is the round-2 regression
// test: CreateUser must require roles:assign before minting a user with
// RoleIDs, once an RBAC authorizer is configured.
func TestEmbeddedCreateUser_DeniedWithoutRolesAssign(t *testing.T) {
	roleSvc := &mockRoleServicer{
		resolvePermsFn: func(context.Context, []string) ([]permission.Permission, error) {
			// users:create granted, roles:assign withheld: the denial below
			// must be attributable to roles:assign alone, not to the base
			// users:create check CreateUser also runs -- granting NO
			// permissions at all could not tell the two gates apart.
			return []permission.Permission{permission.UsersCreate}, nil
		},
	}
	userSvc := &mockUserServicer{
		createUserFn: func(context.Context, *user.CreateUserRequest) (*user.User, error) {
			t.Fatal("CreateUser must not be reached when roles:assign is denied")
			return nil, nil
		},
	}
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithUserService(userSvc),
		WithRoleService(roleSvc),
		WithAuthMiddleware(authmw.New(rbac.New(roleSvc), authmw.WithRoleResolver(roleSvc))),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := roleAssignmentIdentityCtx("acme", "member")
	_, err = tr.CreateUser(ctx, &transport.CreateUserRequest{
		Username: "alice", TenantID: "acme", RoleIDs: []string{"role-a"},
	})
	if err == nil {
		t.Fatal("CreateUser with RoleIDs must be denied without roles:assign")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("err = %v, want an authorization denial", err)
	}
}

// TestEmbeddedCreateUser_AllowedWithRolesAssignAndValidRole asserts the
// positive path end to end: permission granted, referenced role exists in
// the target tenant.
func TestEmbeddedCreateUser_AllowedWithRolesAssignAndValidRole(t *testing.T) {
	roleSvc := &mockRoleServicer{
		resolvePermsFn: func(context.Context, []string) ([]permission.Permission, error) {
			return []permission.Permission{permission.RolesAssign, permission.UsersCreate}, nil
		},
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			return &role.Role{ID: id, TenantID: "acme"}, nil
		},
	}
	created := false
	userSvc := &mockUserServicer{
		createUserFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			created = true
			return &user.User{ID: 1, Username: req.Username, TenantID: req.TenantID}, nil
		},
	}
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithUserService(userSvc),
		WithRoleService(roleSvc),
		WithAuthMiddleware(authmw.New(rbac.New(roleSvc), authmw.WithRoleResolver(roleSvc))),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := roleAssignmentIdentityCtx("acme", "admin")
	if _, err := tr.CreateUser(ctx, &transport.CreateUserRequest{
		Username: "alice", TenantID: "acme", RoleIDs: []string{"role-a"},
	}); err != nil {
		t.Fatalf("CreateUser with roles:assign and a valid role must succeed: %v", err)
	}
	if !created {
		t.Fatal("CreateUser was never reached")
	}
}

// TestEmbeddedCreateUser_RejectsRoleFromAnotherTenant asserts role
// existence/tenant validation runs on the embedded transport too.
func TestEmbeddedCreateUser_RejectsRoleFromAnotherTenant(t *testing.T) {
	roleSvc := &mockRoleServicer{
		resolvePermsFn: func(context.Context, []string) ([]permission.Permission, error) {
			return []permission.Permission{permission.RolesAssign, permission.UsersCreate}, nil
		},
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			return &role.Role{ID: id, TenantID: "globex"}, nil // wrong tenant
		},
	}
	userSvc := &mockUserServicer{
		createUserFn: func(context.Context, *user.CreateUserRequest) (*user.User, error) {
			t.Fatal("CreateUser must not be reached when the role is not assignable")
			return nil, nil
		},
	}
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithUserService(userSvc),
		WithRoleService(roleSvc),
		WithAuthMiddleware(authmw.New(rbac.New(roleSvc), authmw.WithRoleResolver(roleSvc))),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := roleAssignmentIdentityCtx("acme", "admin")
	_, err = tr.CreateUser(ctx, &transport.CreateUserRequest{
		Username: "alice", TenantID: "acme", RoleIDs: []string{"role-a"},
	})
	var na *role.NotAssignableError
	if !errors.As(err, &na) {
		t.Fatalf("err = %v, want *role.NotAssignableError", err)
	}
}

// TestEmbeddedUpdateUser_DeniedWithoutRolesAssign is the UpdateUser sibling
// of TestEmbeddedCreateUser_DeniedWithoutRolesAssign.
func TestEmbeddedUpdateUser_DeniedWithoutRolesAssign(t *testing.T) {
	roleSvc := &mockRoleServicer{
		resolvePermsFn: func(context.Context, []string) ([]permission.Permission, error) {
			// users:update granted, roles:assign withheld -- see the
			// identical comment on TestEmbeddedCreateUser_DeniedWithoutRolesAssign.
			return []permission.Permission{permission.UsersUpdate}, nil
		},
	}
	userSvc := &mockUserServicer{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			return &user.User{ID: id, TenantID: "acme"}, nil
		},
		updateUserFn: func(context.Context, *user.UpdateUserRequest) (*user.User, error) {
			t.Fatal("UpdateUser must not be reached when roles:assign is denied")
			return nil, nil
		},
	}
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithUserService(userSvc),
		WithRoleService(roleSvc),
		WithAuthMiddleware(authmw.New(rbac.New(roleSvc), authmw.WithRoleResolver(roleSvc))),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := roleAssignmentIdentityCtx("acme", "member")
	_, err = tr.UpdateUser(ctx, &transport.UpdateUserRequest{ID: 1, RoleIDs: []string{"role-a"}})
	if err == nil {
		t.Fatal("UpdateUser touching RoleIDs must be denied without roles:assign")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("err = %v, want an authorization denial", err)
	}
}

// TestEmbeddedUpdateUser_WithoutRoleFieldsIsUngatedByRolesAssign asserts an
// ordinary update naming none of RoleIDs/RoleBindings/Attributes is not
// gated behind roles:assign.
func TestEmbeddedUpdateUser_WithoutRoleFieldsIsUngatedByRolesAssign(t *testing.T) {
	roleSvc := &mockRoleServicer{
		resolvePermsFn: func(context.Context, []string) ([]permission.Permission, error) {
			// users:update only -- roles:assign is deliberately absent, so a
			// grant here would hide the gate this test pins.
			return []permission.Permission{permission.UsersUpdate}, nil
		},
	}
	updated := false
	userSvc := &mockUserServicer{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			return &user.User{ID: id, TenantID: "acme"}, nil
		},
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			updated = true
			return &user.User{ID: req.ID, TenantID: "acme"}, nil
		},
	}
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithUserService(userSvc),
		WithRoleService(roleSvc),
		WithAuthMiddleware(authmw.New(rbac.New(roleSvc), authmw.WithRoleResolver(roleSvc))),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := roleAssignmentIdentityCtx("acme", "member")
	if _, err := tr.UpdateUser(ctx, &transport.UpdateUserRequest{ID: 1, Name: "still just a rename"}); err != nil {
		t.Fatalf("an update naming no role-assignment fields must not require roles:assign: %v", err)
	}
	if !updated {
		t.Fatal("UpdateUser was never reached")
	}
}

// TestEmbeddedSORole_OnlyTheTenantSOGrantsOrRevokesIt holds the embedded
// transport to the rule every wire transport enforces (go-qrdb#1042): a tenant
// Admin, holding roles:assign and roles:unassign, cannot give a user the
// tenant's SO role or take it away, and nothing is written when it tries; the
// tenant's own SO can do both.
func TestEmbeddedSORole_OnlyTheTenantSOGrantsOrRevokesIt(t *testing.T) {
	const soID = "acme-so"
	roleSvc := &mockRoleServicer{
		resolvePermsFn: func(context.Context, []string) ([]permission.Permission, error) {
			return []permission.Permission{permission.RolesAssign, permission.RolesUnassign}, nil
		},
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			return &role.Role{ID: id, TenantID: "acme"}, nil
		},
		getRoleByNameFn: func(_ context.Context, tenantID, name string) (*role.Role, error) {
			if tenantID != "acme" || name != role.RoleNameSO {
				return nil, &role.RoleNotFoundError{ID: name}
			}
			return &role.Role{ID: soID, TenantID: "acme", Name: role.RoleNameSO}, nil
		},
	}

	tests := []struct {
		name       string
		callerRole string
		holds      []string
		call       func(*Transport, context.Context) error
		wantDenied bool
	}{
		{"admin assigns SO", "admin", nil, func(tr *Transport, ctx context.Context) error { return tr.AssignRole(ctx, 1, soID) }, true},
		{"admin unassigns SO", "admin", []string{soID}, func(tr *Transport, ctx context.Context) error { return tr.UnassignRole(ctx, 1, soID) }, true},
		{"admin assigns another role", "admin", nil, func(tr *Transport, ctx context.Context) error { return tr.AssignRole(ctx, 1, "role-a") }, false},
		{"SO assigns SO", soID, nil, func(tr *Transport, ctx context.Context) error { return tr.AssignRole(ctx, 1, soID) }, false},
		{"SO unassigns SO", soID, []string{soID}, func(tr *Transport, ctx context.Context) error { return tr.UnassignRole(ctx, 1, soID) }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writes := 0
			userSvc := &mockUserServicer{
				getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
					return &user.User{ID: id, TenantID: "acme", RoleIDs: tt.holds}, nil
				},
				updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
					writes++
					return &user.User{ID: req.ID, TenantID: "acme"}, nil
				},
			}
			tr, err := New(newMockKVService(), &mockAdminService{},
				WithUserService(userSvc),
				WithRoleService(roleSvc),
				WithAuthMiddleware(authmw.New(rbac.New(roleSvc), authmw.WithRoleResolver(roleSvc))),
			)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			err = tt.call(tr, roleAssignmentIdentityCtx("acme", tt.callerRole))
			if !tt.wantDenied {
				if err != nil {
					t.Fatalf("want success, got %v", err)
				}
				if writes != 1 {
					t.Fatalf("want one write, got %d", writes)
				}
				return
			}
			var denied *authmw.SORoleRequiredError
			if !errors.As(err, &denied) {
				t.Fatalf("want *SORoleRequiredError, got %T: %v", err, err)
			}
			if writes != 0 {
				t.Fatalf("a refused SO change must write nothing, got %d writes", writes)
			}
		})
	}
}

// TestEmbeddedSORole_AdminWriteOverlappingAnSOGrant: an SO grant that lands
// after the gate read the record and before the Admin's write commits is not
// removed by that write. The user service double judges the request's Guard
// against the record it replaces, as the user store does against the base of
// its conditional commit.
func TestEmbeddedSORole_AdminWriteOverlappingAnSOGrant(t *testing.T) {
	const soID = "acme-so"
	roleSvc := &mockRoleServicer{
		resolvePermsFn: func(context.Context, []string) ([]permission.Permission, error) {
			return []permission.Permission{permission.RolesAssign, permission.RolesUnassign, permission.UsersUpdate}, nil
		},
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			return &role.Role{ID: id, TenantID: "acme"}, nil
		},
		getRoleByNameFn: func(_ context.Context, tenantID, name string) (*role.Role, error) {
			if tenantID != "acme" || name != role.RoleNameSO {
				return nil, &role.RoleNotFoundError{ID: name}
			}
			return &role.Role{ID: soID, TenantID: "acme", Name: role.RoleNameSO}, nil
		},
	}

	tests := []struct {
		name string
		call func(*Transport, context.Context) error
	}{
		{"update role_ids", func(tr *Transport, ctx context.Context) error {
			_, err := tr.UpdateUser(ctx, &transport.UpdateUserRequest{ID: 1, RoleIDs: []string{"role-a"}})
			return err
		}},
		{"unassign SO", func(tr *Transport, ctx context.Context) error { return tr.UnassignRole(ctx, 1, soID) }},
		{"assign another role", func(tr *Transport, ctx context.Context) error {
			// The handler appends to the record it read before the grant, so
			// its write would drop the SO role.
			return tr.AssignRole(ctx, 1, "role-b")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := user.User{ID: 1, TenantID: "acme", RoleIDs: []string{"role-a"}}
			userSvc := &mockUserServicer{
				getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
					cp := rec
					return &cp, nil
				},
				updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
					// The SO's grant commits first.
					rec.RoleIDs = append(slices.Clone(rec.RoleIDs), soID)
					next := rec
					if req.RoleIDs != nil {
						next.RoleIDs = req.RoleIDs
					}
					if req.Guard != nil {
						if err := req.Guard(&rec, &next); err != nil {
							return nil, err
						}
					}
					rec = next
					cp := rec
					return &cp, nil
				},
			}
			tr, err := New(newMockKVService(), &mockAdminService{},
				WithUserService(userSvc),
				WithRoleService(roleSvc),
				WithAuthMiddleware(authmw.New(rbac.New(roleSvc), authmw.WithRoleResolver(roleSvc))),
			)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			err = tt.call(tr, roleAssignmentIdentityCtx("acme", "admin"))
			var denied *authmw.SORoleRequiredError
			if !errors.As(err, &denied) {
				t.Fatalf("want *SORoleRequiredError, got %T: %v", err, err)
			}
			if !slices.Contains(rec.RoleIDs, soID) {
				t.Fatalf("the SO grant was removed: roles = %v", rec.RoleIDs)
			}
		})
	}
}
