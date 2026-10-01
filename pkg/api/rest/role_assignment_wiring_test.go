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

// The role-assignment write gate (go-qrdb#192 item 3), at the REST transport
// boundary, driven through the FULL request chain (authenticate, bind
// tenant, authorize the static route permission, THEN this package's own
// dynamic gate) rather than by calling the handler directly. pkg/auth/
// middleware's own tests prove the gate DECIDES correctly against the real
// RBAC authorizer; this proves REST actually CALLS it.
package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// newRoleAssignmentWiringServer builds a server reachable through the FULL
// HTTP handler chain, with a caller in tenant "acme" whose granted
// permissions are exactly perms.
func newRoleAssignmentWiringServer(t *testing.T, perms []permission.Permission) (*Server, *certScopeAuthenticator) {
	t.Helper()

	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			u := testUser()
			u.ID = id
			u.TenantID = "acme"
			return u, nil
		},
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			u := testUser()
			u.ID = req.ID
			u.RoleIDs = req.RoleIDs
			return u, nil
		},
	}
	roleSvc := &mockRoleService{
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			return &role.Role{ID: id, TenantID: "acme"}, nil
		},
		resolvePermissionsFn: func(context.Context, []string) ([]permission.Permission, error) {
			return perms, nil
		},
	}

	caller := &certScopeAuthenticator{}
	caller.as("acme", "acme-admin")

	mw := authmw.New(rbac.New(roleSvc), authmw.WithAuthenticator(caller))
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithUserService(userSvc),
		WithRoleService(roleSvc),
		WithAuthMiddleware(mw),
	)
	return srv, caller
}

func doUpdateUser(srv *Server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/users/1001", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, req)
	return rec
}

// TestUpdateUser_RoleIDsRequiresRolesAssign_Denied asserts that a caller
// holding UsersUpdate but NOT RolesAssign is refused when the write touches
// RoleIDs — the static per-route permission alone is not enough.
func TestUpdateUser_RoleIDsRequiresRolesAssign_Denied(t *testing.T) {
	srv, _ := newRoleAssignmentWiringServer(t, []permission.Permission{permission.UsersUpdate})

	rec := doUpdateUser(srv, `{"role_ids":["role-a"]}`)
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 or 403 (missing roles:assign): body=%s", rec.Code, rec.Body.String())
	}
}

// TestUpdateUser_RoleIDsRequiresRolesAssign_Allowed asserts that a caller
// holding BOTH UsersUpdate and RolesAssign succeeds.
func TestUpdateUser_RoleIDsRequiresRolesAssign_Allowed(t *testing.T) {
	srv, _ := newRoleAssignmentWiringServer(t, []permission.Permission{permission.UsersUpdate, permission.RolesAssign})

	rec := doUpdateUser(srv, `{"role_ids":["role-a"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: body=%s", rec.Code, rec.Body.String())
	}
}

// TestUpdateUser_WithoutRoleFieldsDoesNotRequireRolesAssign asserts that an
// ordinary update naming none of RoleIDs/RoleBindings/Attributes is NOT
// gated behind roles:assign — the stronger check is triggered by the
// fields present, not by the route.
func TestUpdateUser_WithoutRoleFieldsDoesNotRequireRolesAssign(t *testing.T) {
	srv, _ := newRoleAssignmentWiringServer(t, []permission.Permission{permission.UsersUpdate})

	rec := doUpdateUser(srv, `{"name":"still just a rename"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: body=%s", rec.Code, rec.Body.String())
	}
}

// TestUpdateUser_RoleIDNotInTargetTenantIsRejected asserts the role
// existence/tenant-membership validation: a role that resolves but does not
// belong to the caller's tenant is refused with a client error, even though
// the caller holds roles:assign.
func TestUpdateUser_RoleIDNotInTargetTenantIsRejected(t *testing.T) {
	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			u := testUser()
			u.ID = id
			u.TenantID = "acme"
			return u, nil
		},
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			t.Fatal("UpdateUser must not be reached when role validation fails")
			return nil, nil
		},
	}
	roleSvc := &mockRoleService{
		getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
			return &role.Role{ID: id, TenantID: "globex"}, nil // NOT acme
		},
		resolvePermissionsFn: func(context.Context, []string) ([]permission.Permission, error) {
			return []permission.Permission{permission.UsersUpdate, permission.RolesAssign}, nil
		},
	}
	caller := &certScopeAuthenticator{}
	caller.as("acme", "acme-admin")
	mw := authmw.New(rbac.New(roleSvc), authmw.WithAuthenticator(caller))
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithUserService(userSvc),
		WithRoleService(roleSvc),
		WithAuthMiddleware(mw),
	)

	rec := doUpdateUser(srv, `{"role_ids":["role-a"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (role not assignable in this tenant): body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err == nil {
		if msg, _ := body["error"].(string); msg != "" && !strings.Contains(msg, "not assignable") {
			t.Errorf("error message = %q, want it to mention the role is not assignable", msg)
		}
	}
}
