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

package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// bindingServer serves the full HTTP chain over the binding fixture as caller.
func bindingServer(t *testing.T, caller *auth.Identity, opts ...authmw.Option) (*Server, *authtest.BindingUsers) {
	t.Helper()
	users := authtest.NewBindingUsers()
	roles := authtest.BindingRoles(t)
	opts = append([]authmw.Option{authmw.WithRoleResolver(roles),
		authmw.WithAuthenticator(authtest.NewFixedAuthenticator(caller)),
		authmw.WithUserService(users)}, opts...)
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithUserService(users),
		WithRoleService(roles),
		WithAuthMiddleware(authmw.New(rbac.New(roles), opts...)),
	)
	return srv, users
}

func serve(srv *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, req)
	return rec
}

// TestTenantAdmin_BodyNamedTenantOrUser drives, as acme's administrator, the
// routes whose body names a tenant or a user, with bodies whose keys differ
// only in case or repeat (encoding/json matches a struct field
// case-insensitively and the last occurrence wins). Whatever the handler
// decodes is what is acted on, and it acts in acme only: another tenant named
// is refused, another tenant's user is not found, and neither writes.
func TestTenantAdmin_BodyNamedTenantOrUser(t *testing.T) {
	globexAdmin := role.SystemRoleID(authtest.OtherTenant, role.RoleNameAdmin)
	tests := []struct {
		name    string
		path    string
		body    string
		status  int
		created []string
	}{
		{
			name:   "case-variant tenant on create",
			path:   "/users",
			body:   `{"username":"mallory","without_email_login_key":true,"tenant_id":"acme","TENANT_ID":"globex"}`,
			status: http.StatusForbidden,
		},
		{
			name:    "duplicate tenant key on create: the last one is the one authorized",
			path:    "/users",
			body:    `{"username":"carol","without_email_login_key":true,"tenant_id":"globex","tenant_id":"acme"}`,
			status:  http.StatusCreated,
			created: []string{authtest.BindingTenant},
		},
		{
			name:   "case-variant user on assign",
			path:   "/roles/assign",
			body:   `{"user_id":"200","USER_ID":"300","role_id":"` + globexAdmin + `"}`,
			status: http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, users := bindingServer(t, authtest.BindingTenantAdmin())
			rec := serve(srv, http.MethodPost, tt.path, tt.body)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.status, rec.Body.String())
			}
			if got := users.Created(); !slices.Equal(got, tt.created) {
				t.Fatalf("users created in %v, want %v", got, tt.created)
			}
			if other := users.User(authtest.UserInOtherTenant); len(other.RoleIDs) != 0 {
				t.Fatalf("globex user's roles = %v, want none", other.RoleIDs)
			}
		})
	}
}

// TestIAMBody_UnreadableBodyIsRefused asserts a user or role administration
// route refuses a body it cannot read: an undecodable body is 400, one past
// maxIAMRequestBytes 413, and neither reaches the store.
func TestIAMBody_UnreadableBodyIsRefused(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{name: "undecodable", body: `{"tenant_id":"acme",`, status: http.StatusBadRequest},
		{name: "oversize", body: `{"username":"x","tenant_id":"acme","pad":"` + strings.Repeat("x", 2<<20) + `"}`, status: http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, users := bindingServer(t, authtest.BindingTenantAdmin())
			rec := serve(srv, http.MethodPost, "/users", tt.body)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.status, rec.Body.String())
			}
			if got := users.Created(); len(got) != 0 {
				t.Fatalf("users created in %v, want none", got)
			}
		})
	}
}

// TestTenantAdmin_QueryNamedTenant asserts acme's administrator lists acme's
// users and is refused globex's.
func TestTenantAdmin_QueryNamedTenant(t *testing.T) {
	tests := []struct {
		tenant string
		status int
	}{
		{authtest.BindingTenant, http.StatusOK},
		{authtest.OtherTenant, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.tenant, func(t *testing.T) {
			srv, _ := bindingServer(t, authtest.BindingTenantAdmin())
			rec := serve(srv, http.MethodGet, "/users/list?tenantID="+tt.tenant, "")
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.status, rec.Body.String())
			}
		})
	}
}

// TestAssignRole_GoesThroughTheRoleAssignmentGate asserts /roles/assign
// validates the role as every other role-assignment write does: a role from
// another tenant is refused (400) and never reaches the store, while the
// target tenant's own role is assigned.
func TestAssignRole_GoesThroughTheRoleAssignmentGate(t *testing.T) {
	tests := []struct {
		name   string
		roleID string
		status int
	}{
		{name: "the user's own tenant's role", roleID: role.SystemRoleID(authtest.BindingTenant, role.RoleNameUser), status: http.StatusOK},
		{name: "another tenant's role", roleID: role.SystemRoleID(authtest.OtherTenant, role.RoleNameAdmin), status: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, users := bindingServer(t, authtest.BindingTenantAdmin())
			rec := serve(srv, http.MethodPost, "/roles/assign", `{"user_id":"200","role_id":"`+tt.roleID+`"}`)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.status, rec.Body.String())
			}
			assigned := slices.Contains(users.User(authtest.UserInBindingTenant).RoleIDs, tt.roleID)
			if assigned != (tt.status == http.StatusOK) {
				t.Fatalf("assigned = %v, want %v", assigned, tt.status == http.StatusOK)
			}
		})
	}
}

// TestUpdateUser_DeadRoleBindingIsRefused writes RoleBindings through the
// REST update route with no RBAC configured, so the only refusal left is the
// own-tenant rule itself: a binding outside the user's own tenant is refused
// as a bad field, for a system-tenant user exactly as for a tenant user.
func TestUpdateUser_DeadRoleBindingIsRefused(t *testing.T) {
	globexAdmin := role.SystemRoleID(authtest.OtherTenant, role.RoleNameAdmin)
	acmeUser := role.SystemRoleID(authtest.BindingTenant, role.RoleNameUser)
	tests := []struct {
		name    string
		userID  uint64
		binding string
		status  int
	}{
		{name: "another tenant on a tenant user", userID: authtest.UserInBindingTenant, binding: `{"role_id":"` + globexAdmin + `","tenant_id":"` + authtest.OtherTenant + `"}`, status: http.StatusBadRequest},
		{name: "own tenant on a tenant user", userID: authtest.UserInBindingTenant, binding: `{"role_id":"` + acmeUser + `","tenant_id":"` + authtest.BindingTenant + `"}`, status: http.StatusOK},
		{name: "another tenant on a system user", userID: authtest.SystemUserID, binding: `{"role_id":"` + globexAdmin + `","tenant_id":"` + authtest.OtherTenant + `"}`, status: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := authtest.NewBindingUsers()
			roles := authtest.BindingRoles(t)
			srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
				WithUserService(users), WithRoleService(roles))
			before := len(users.User(tt.userID).RoleBindings)
			rec := serve(srv, http.MethodPut, "/users/"+strconv.FormatUint(tt.userID, 10), `{"role_bindings":[`+tt.binding+`]}`)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.status, rec.Body.String())
			}
			if tt.status != http.StatusOK && len(users.User(tt.userID).RoleBindings) != before {
				t.Fatalf("refused binding was stored: %+v", users.User(tt.userID).RoleBindings)
			}
		})
	}
}

// TestSORole_OnlyTheTenantSOGrantsOrRevokesIt pins separation of duties over
// the tenant's Security Officer role on every REST write that can move it:
// assign, unassign, user create, and user update by role_ids (a replacement,
// so dropping the role revokes it) or role_bindings. The tenant Admin holds
// roles:assign and roles:unassign, and is refused with 403 and nothing written;
// the tenant SO is admitted. A write that leaves alice's SO membership as it
// was is not a grant or a revocation and stays an Admin's.
func TestSORole_OnlyTheTenantSOGrantsOrRevokesIt(t *testing.T) {
	so := role.SystemRoleID(authtest.BindingTenant, role.RoleNameSO)
	userRole := role.SystemRoleID(authtest.BindingTenant, role.RoleNameUser)
	alice := strconv.FormatUint(authtest.UserInBindingTenant, 10)
	tests := []struct {
		name       string
		callerIsSO bool
		aliceIsSO  bool
		method     string
		path       string
		body       string
		status     int
		wantSO     bool
	}{
		{name: "admin assigns SO", method: http.MethodPost, path: "/roles/assign", body: `{"user_id":"` + alice + `","role_id":"` + so + `"}`, status: http.StatusForbidden},
		{name: "SO assigns SO", callerIsSO: true, method: http.MethodPost, path: "/roles/assign", body: `{"user_id":"` + alice + `","role_id":"` + so + `"}`, status: http.StatusOK, wantSO: true},
		{name: "admin assigns another role to an SO", aliceIsSO: true, method: http.MethodPost, path: "/roles/assign", body: `{"user_id":"` + alice + `","role_id":"` + userRole + `"}`, status: http.StatusOK, wantSO: true},
		{name: "admin unassigns SO", aliceIsSO: true, method: http.MethodPost, path: "/roles/unassign", body: `{"user_id":"` + alice + `","role_id":"` + so + `"}`, status: http.StatusForbidden, wantSO: true},
		{name: "SO unassigns SO", callerIsSO: true, aliceIsSO: true, method: http.MethodPost, path: "/roles/unassign", body: `{"user_id":"` + alice + `","role_id":"` + so + `"}`, status: http.StatusOK},
		{name: "admin grants SO by role_ids", method: http.MethodPut, path: "/users/" + alice, body: `{"role_ids":["` + so + `"]}`, status: http.StatusForbidden},
		{name: "SO grants SO by role_ids", callerIsSO: true, method: http.MethodPut, path: "/users/" + alice, body: `{"role_ids":["` + so + `"]}`, status: http.StatusOK, wantSO: true},
		{name: "admin revokes SO by replacing role_ids", aliceIsSO: true, method: http.MethodPut, path: "/users/" + alice, body: `{"role_ids":["` + userRole + `"]}`, status: http.StatusForbidden, wantSO: true},
		{name: "SO revokes SO by replacing role_ids", callerIsSO: true, aliceIsSO: true, method: http.MethodPut, path: "/users/" + alice, body: `{"role_ids":[]}`, status: http.StatusOK},
		{name: "admin keeps SO while editing role_ids", aliceIsSO: true, method: http.MethodPut, path: "/users/" + alice, body: `{"role_ids":["` + so + `","` + userRole + `"]}`, status: http.StatusOK, wantSO: true},
		{name: "admin grants SO by role_bindings", method: http.MethodPut, path: "/users/" + alice, body: `{"role_bindings":[{"role_id":"` + so + `","tenant_id":"` + authtest.BindingTenant + `"}]}`, status: http.StatusForbidden},
		{name: "SO grants SO by role_bindings", callerIsSO: true, method: http.MethodPut, path: "/users/" + alice, body: `{"role_bindings":[{"role_id":"` + so + `","tenant_id":"` + authtest.BindingTenant + `"}]}`, status: http.StatusOK, wantSO: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, users := soRoleServer(t, tt.callerIsSO, tt.aliceIsSO, authmw.WithStrictSeparation(true))
			writes := len(users.Updated())
			rec := serve(srv, tt.method, tt.path, tt.body)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.status, rec.Body.String())
			}
			if tt.status != http.StatusOK && len(users.Updated()) != writes {
				t.Fatalf("refused write was stored: updates %v", users.Updated()[writes:])
			}
			if got := holdsSO(users.User(authtest.UserInBindingTenant), so); got != tt.wantSO {
				t.Fatalf("alice holds SO = %v, want %v", got, tt.wantSO)
			}
		})
	}
}

// TestSORole_OnlyTheTenantSOCreatesAnSO is the create half of
// TestSORole_OnlyTheTenantSOGrantsOrRevokesIt: role_ids on a new user are a
// grant like any other.
func TestSORole_OnlyTheTenantSOCreatesAnSO(t *testing.T) {
	body := `{"username":"sam","without_email_login_key":true,"tenant_id":"` + authtest.BindingTenant +
		`","role_ids":["` + role.SystemRoleID(authtest.BindingTenant, role.RoleNameSO) + `"]}`
	tests := []struct {
		name       string
		callerIsSO bool
		status     int
		created    []string
	}{
		{name: "admin", status: http.StatusForbidden},
		{name: "SO", callerIsSO: true, status: http.StatusCreated, created: []string{authtest.BindingTenant}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, users := soRoleServer(t, tt.callerIsSO, false, authmw.WithStrictSeparation(true))
			rec := serve(srv, http.MethodPost, "/users", body)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.status, rec.Body.String())
			}
			if got := users.Created(); !slices.Equal(got, tt.created) {
				t.Fatalf("users created in %v, want %v", got, tt.created)
			}
		})
	}
}

// Outside strict separation of duties the tenant Admin grants, revokes and
// creates the so role like any other role.
func TestSORole_StandardPostureTheAdminGrantsIt(t *testing.T) {
	so := role.SystemRoleID(authtest.BindingTenant, role.RoleNameSO)
	alice := strconv.FormatUint(authtest.UserInBindingTenant, 10)
	srv, users := soRoleServer(t, false, false)
	if rec := serve(srv, http.MethodPost, "/roles/assign", `{"user_id":"`+alice+`","role_id":"`+so+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("assign status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !holdsSO(users.User(authtest.UserInBindingTenant), so) {
		t.Fatal("alice holds SO = false after the admin's assign, want true")
	}
	if rec := serve(srv, http.MethodPost, "/roles/unassign", `{"user_id":"`+alice+`","role_id":"`+so+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("unassign status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if holdsSO(users.User(authtest.UserInBindingTenant), so) {
		t.Fatal("alice holds SO = true after the admin's unassign, want false")
	}
	body := `{"username":"sam","without_email_login_key":true,"tenant_id":"` + authtest.BindingTenant + `","role_ids":["` + so + `"]}`
	if rec := serve(srv, http.MethodPost, "/users", body); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
}

// soRoleServer is bindingServer as the acme admin, holding acme's SO role
// beside Admin when callerIsSO, with alice holding it when aliceIsSO.
func soRoleServer(t *testing.T, callerIsSO, aliceIsSO bool, opts ...authmw.Option) (*Server, *authtest.BindingUsers) {
	t.Helper()
	srv, users := bindingServer(t, authtest.BindingTenantAdmin(), opts...)
	so := role.SystemRoleID(authtest.BindingTenant, role.RoleNameSO)
	seed := func(id uint64, roleIDs ...string) {
		ctx := tenant.WithTenantID(context.Background(), tenantid.System)
		if _, err := users.UpdateUser(ctx, &user.UpdateUserRequest{ID: id, RoleIDs: roleIDs}); err != nil {
			t.Fatalf("seed user %d: %v", id, err)
		}
	}
	if callerIsSO {
		// Under strict separation the SO is a separate principal: it
		// holds the so role and not the admin role.
		seed(authtest.BindingTenantAdminID, so)
	}
	if aliceIsSO {
		seed(authtest.UserInBindingTenant, so)
	}
	return srv, users
}

// holdsSO reports whether u holds the SO role so, directly or by binding.
func holdsSO(u *user.User, so string) bool {
	if slices.Contains(u.RoleIDs, so) {
		return true
	}
	for _, b := range u.RoleBindings {
		if b.RoleID == so {
			return true
		}
	}
	return false
}

// Under strict separation, role create and built-in reconcile are
// SO-exclusive acts: the tenant admin is refused, and so is an SO that also
// holds the admin role; only a separate SO passes (go-qrdb#1128).
func TestSOExclusive_RoleCreateAndReconcile(t *testing.T) {
	admin := role.SystemRoleID(authtest.BindingTenant, role.RoleNameAdmin)
	so := role.SystemRoleID(authtest.BindingTenant, role.RoleNameSO)
	ops := []struct {
		name, path, body string
		ok               int
	}{
		{name: "create role", path: "/roles", ok: http.StatusCreated,
			body: `{"name":"reviewer","tenant_id":"` + authtest.BindingTenant + `","permissions":["kv:get"]}`},
		{name: "reconcile built-in roles", path: "/roles/reconcile", ok: http.StatusOK,
			body: `{"tenant_id":"` + authtest.BindingTenant + `"}`},
	}
	callers := []struct {
		name    string
		roleIDs []string
		refused bool
	}{
		{name: "tenant admin", roleIDs: []string{admin}, refused: true},
		{name: "SO holding admin", roleIDs: []string{admin, so}, refused: true},
		{name: "separate SO", roleIDs: []string{so}},
	}
	for _, op := range ops {
		for _, c := range callers {
			t.Run(op.name+"/"+c.name, func(t *testing.T) {
				srv, users := bindingServer(t, authtest.BindingTenantAdmin(), authmw.WithStrictSeparation(true))
				ctx := tenant.WithTenantID(context.Background(), tenantid.System)
				if _, err := users.UpdateUser(ctx, &user.UpdateUserRequest{ID: authtest.BindingTenantAdminID, RoleIDs: c.roleIDs}); err != nil {
					t.Fatalf("seed caller: %v", err)
				}
				rec := serve(srv, http.MethodPost, op.path, op.body)
				want := op.ok
				if c.refused {
					want = http.StatusForbidden
				}
				if rec.Code != want {
					t.Fatalf("status = %d, want %d: %s", rec.Code, want, rec.Body.String())
				}
			})
		}
	}
}
