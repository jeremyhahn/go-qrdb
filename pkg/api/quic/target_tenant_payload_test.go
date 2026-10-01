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

package quic

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// bindingServer is a QUIC server whose callers authenticate as the binding
// fixture's operator, plus the context its authorize step runs in.
func bindingServer(t *testing.T) (*Server, *authtest.BindingUsers, context.Context) {
	t.Helper()
	users := authtest.NewBindingUsers()
	roles := authtest.BindingRoles(t)
	mw := authmw.New(rbac.New(roles),
		authmw.WithAuthenticator(authtest.NewFixedAuthenticator(authtest.BindingOperator())),
		authmw.WithUserService(users),
		authmw.WithRoleService(roles))
	srv := NewServer(nil, nil, "localhost:0", WithUserService(users), WithRoleService(roles), WithAuthMiddleware(mw))
	ctx, err := mw.AuthenticateQUIC(context.Background())
	if err != nil {
		t.Fatalf("AuthenticateQUIC: %v", err)
	}
	return srv, users, ctx
}

// TestRouteCheck_PayloadTargetIsTheValueTheHandlerUses drives payloads whose
// keys a map lookup and the handler's struct decode read differently: the
// check must authorize the value json.Unmarshal into the handler's own type
// produces (case-insensitive key match, last occurrence wins).
func TestRouteCheck_PayloadTargetIsTheValueTheHandlerUses(t *testing.T) {
	globexAdmin := role.SystemRoleID(authtest.OtherTenant, role.RoleNameAdmin)
	tests := []struct {
		name    string
		method  string
		payload string
		allow   bool
	}{
		{name: "case-variant tenant on create", method: "user.Create", payload: `{"username":"mallory","withoutEmailLoginKey":true,"tenantID":"acme","TENANTID":"globex"}`, allow: false},
		{name: "duplicate tenant on create, last wins", method: "user.Create", payload: `{"username":"carol","withoutEmailLoginKey":true,"tenantID":"globex","tenantID":"acme"}`, allow: true},
		{name: "case-variant user on assign", method: "role.Assign", payload: `{"userID":200,"USERID":300,"roleID":"` + globexAdmin + `"}`, allow: false},
		{name: "list in the bound tenant", method: "user.List", payload: `{"tenantID":"acme"}`, allow: true},
		{name: "list in another tenant", method: "user.List", payload: `{"tenantID":"globex"}`, allow: false},
		{name: "assign to a user in the bound tenant", method: "role.Assign", payload: `{"userID":200,"roleID":"` + globexAdmin + `"}`, allow: true},
		{name: "assign to a user in another tenant", method: "role.Assign", payload: `{"userID":300,"roleID":"` + globexAdmin + `"}`, allow: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, ctx := bindingServer(t)
			err := srv.authorize(ctx, tt.method, []byte(tt.payload))
			if tt.allow != (err == nil) {
				t.Fatalf("authorize = %v, want allowed=%v", err, tt.allow)
			}
			if !tt.allow && !errors.Is(err, auth.ErrUnauthorized) {
				t.Fatalf("authorize = %v, want an authorization denial", err)
			}
		})
	}
}

// TestRouteCheck_UndecodablePayloadIsRefused asserts a targeted method whose
// payload does not decode is refused as a bad request, not authorized against
// the caller's own tenant.
func TestRouteCheck_UndecodablePayloadIsRefused(t *testing.T) {
	for _, method := range []string{"user.Create", "user.Update", "role.Assign", "role.Get", "tenant.BarrierSeal"} {
		t.Run(method, func(t *testing.T) {
			srv, users, ctx := bindingServer(t)
			err := srv.authorize(ctx, method, []byte(`{"tenantID":`))
			if err == nil {
				t.Fatal("an undecodable payload must be refused")
			}
			if got := authzStatus(err); got != wire.StatusInvalidArgument {
				t.Fatalf("status = %v, want StatusInvalidArgument (err %v)", got, err)
			}
			if got := users.Created(); !slices.Equal(got, nil) {
				t.Fatalf("users created: %v", got)
			}
		})
	}
}

// TestAssignRole_GoesThroughTheRoleAssignmentGate asserts role.Assign
// validates the role as every other role-assignment write does.
func TestAssignRole_GoesThroughTheRoleAssignmentGate(t *testing.T) {
	tests := []struct {
		name   string
		roleID string
		ok     bool
	}{
		{name: "the user's own tenant's role", roleID: role.SystemRoleID(authtest.BindingTenant, role.RoleNameUser), ok: true},
		{name: "another tenant's role", roleID: role.SystemRoleID(authtest.OtherTenant, role.RoleNameAdmin), ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, users, ctx := bindingServer(t)
			_, err := srv.handleAssignRole(ctx, []byte(`{"userID":200,"roleID":"`+tt.roleID+`"}`))
			if tt.ok != (err == nil) {
				t.Fatalf("handleAssignRole = %v, want ok=%v", err, tt.ok)
			}
			if !tt.ok {
				if code, _ := mapServiceError(err); code != wire.StatusInvalidArgument {
					t.Fatalf("status = %v, want StatusInvalidArgument", code)
				}
			}
			if assigned := slices.Contains(users.User(authtest.UserInBindingTenant).RoleIDs, tt.roleID); assigned != tt.ok {
				t.Fatalf("assigned = %v, want %v", assigned, tt.ok)
			}
		})
	}
}
