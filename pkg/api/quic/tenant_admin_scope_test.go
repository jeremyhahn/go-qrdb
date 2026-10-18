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
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// bindingServer is a QUIC server over the binding fixture whose callers
// authenticate as caller, plus the context its authorize step runs in.
func bindingServer(t *testing.T, caller *auth.Identity) (*Server, *authtest.BindingUsers, context.Context) {
	t.Helper()
	users := authtest.NewBindingUsers()
	roles := authtest.BindingRoles(t)
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles),
		authmw.WithAuthenticator(authtest.NewFixedAuthenticator(caller)),
		authmw.WithUserService(users))
	srv := NewServer(nil, nil, "localhost:0", WithUserService(users), WithRoleService(roles), WithAuthMiddleware(mw))
	ctx, err := mw.AuthenticateQUIC(context.Background())
	if err != nil {
		t.Fatalf("AuthenticateQUIC: %v", err)
	}
	return srv, users, ctx
}

// TestTenantAdmin_PayloadNamedTenantOrUser drives, as acme's administrator,
// the methods whose payload names a tenant or a user, including payloads
// whose keys differ only in case or repeat (json.Unmarshal matches keys
// case-insensitively and the last occurrence wins). Whatever the handler
// decodes is what is acted on, and it acts in acme only: another tenant named
// is refused, another tenant's user is not found, and neither writes.
func TestTenantAdmin_PayloadNamedTenantOrUser(t *testing.T) {
	acmeUser := role.SystemRoleID(authtest.BindingTenant, role.RoleNameUser)
	tests := []struct {
		name    string
		method  string
		payload string
		want    uint16
	}{
		{name: "case-variant tenant on create", method: "user.Create", payload: `{"username":"mallory","withoutEmailLoginKey":true,"tenantID":"acme","TENANTID":"globex"}`, want: wire.StatusPermissionDenied},
		{name: "duplicate tenant on create, last wins", method: "user.Create", payload: `{"username":"carol","withoutEmailLoginKey":true,"tenantID":"globex","tenantID":"acme"}`, want: wire.StatusOK},
		{name: "case-variant user on assign", method: "role.Assign", payload: `{"userID":200,"USERID":300,"roleID":"` + acmeUser + `"}`, want: wire.StatusNotFound},
		{name: "list in own tenant", method: "user.List", payload: `{"tenantID":"acme"}`, want: wire.StatusOK},
		{name: "list in another tenant", method: "user.List", payload: `{"tenantID":"globex"}`, want: wire.StatusPermissionDenied},
		{name: "assign to a user in own tenant", method: "role.Assign", payload: `{"userID":200,"roleID":"` + acmeUser + `"}`, want: wire.StatusOK},
		{name: "assign to a user in another tenant", method: "role.Assign", payload: `{"userID":300,"roleID":"` + acmeUser + `"}`, want: wire.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, users, ctx := bindingServer(t, authtest.BindingTenantAdmin())
			if got := bindingCall(t, srv, ctx, tt.method, tt.payload); got != tt.want {
				t.Fatalf("status = %d, want %d", got, tt.want)
			}
			for _, tenantID := range users.Created() {
				if tenantID != authtest.BindingTenant {
					t.Fatalf("a user was created in %q", tenantID)
				}
			}
			if other := users.User(authtest.UserInOtherTenant); len(other.RoleIDs) != 0 {
				t.Fatalf("globex user's roles = %v, want none", other.RoleIDs)
			}
		})
	}
}

// TestUndecodablePayloadIsRefused asserts a user or role method whose payload
// does not decode is refused as a bad request and writes nothing.
func TestUndecodablePayloadIsRefused(t *testing.T) {
	for _, method := range []string{"user.Create", "user.Update", "role.Assign", "role.Get"} {
		t.Run(method, func(t *testing.T) {
			srv, users, ctx := bindingServer(t, authtest.BindingTenantAdmin())
			if got := bindingCall(t, srv, ctx, method, `{"tenantID":`); got != wire.StatusInvalidArgument {
				t.Fatalf("status = %d, want StatusInvalidArgument", got)
			}
			if got := users.Created(); len(got) != 0 {
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
			srv, users, ctx := bindingServer(t, authtest.BindingTenantAdmin())
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
