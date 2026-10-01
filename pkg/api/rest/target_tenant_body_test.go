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
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// bindingServer serves the full HTTP chain as the binding fixture's operator:
// a system-tenant identity whose only grant is acme's built-in Admin role.
func bindingServer(t *testing.T) (*Server, *authtest.BindingUsers) {
	t.Helper()
	users := authtest.NewBindingUsers()
	roles := authtest.BindingRoles(t)
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithUserService(users),
		WithRoleService(roles),
		WithAuthMiddleware(authmw.New(rbac.New(roles),
			authmw.WithAuthenticator(authtest.NewFixedAuthenticator(authtest.BindingOperator())),
			authmw.WithUserService(users),
			authmw.WithRoleService(roles))),
	)
	return srv, users
}

func serve(srv *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, req)
	return rec
}

// TestRouteCheck_BodyTargetIsTheValueTheHandlerUses drives the body-targeted
// routes with bodies whose keys a map lookup and the handler's struct decode
// read differently. encoding/json matches a struct field case-insensitively
// and the last occurrence wins, so the route check must authorize the value
// that decode produces, never a different spelling of the same key.
func TestRouteCheck_BodyTargetIsTheValueTheHandlerUses(t *testing.T) {
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
			status: http.StatusForbidden,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, users := bindingServer(t)
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

// TestRouteCheck_UnreadableBodyIsRefused asserts a targeted route refuses a
// body it cannot read rather than authorizing it against the caller's own
// tenant: an undecodable body is 400, an oversize one 413, and neither reaches
// the store.
func TestRouteCheck_UnreadableBodyIsRefused(t *testing.T) {
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
			srv, users := bindingServer(t)
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

// TestRouteCheck_QueryTarget asserts a query-named tenant is the one
// authorized: listing acme's users is allowed, listing globex's is refused.
func TestRouteCheck_QueryTarget(t *testing.T) {
	tests := []struct {
		tenant string
		status int
	}{
		{authtest.BindingTenant, http.StatusOK},
		{authtest.OtherTenant, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.tenant, func(t *testing.T) {
			srv, _ := bindingServer(t)
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
			srv, users := bindingServer(t)
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
