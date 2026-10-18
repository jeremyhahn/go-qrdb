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

package mcp

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// runBindingTool runs tool's handler behind this server's tool authorization
// middleware over the binding fixture, authenticated as caller. It reports
// whether the call succeeded and, when it did not, the refusal's text: a
// refusal reaches the client either as an error result (the authorization
// middleware's) or as the handler's error.
func runBindingTool(t *testing.T, caller *auth.Identity, users *authtest.BindingUsers, tool string, args map[string]any, inner func(*authmw.AuthMiddleware, role.RoleServicer) mcpserver.ToolHandlerFunc) (bool, string) {
	t.Helper()
	roles := authtest.BindingRoles(t)
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles),
		authmw.WithAuthenticator(authtest.NewFixedAuthenticator(caller)),
		authmw.WithUserService(users))
	srv := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithUserService(users), WithRoleService(roles), WithAuthMiddleware(mw))
	ctx, _, err := mw.Authenticate(context.Background())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	req := makeReq(args)
	req.Params.Name = tool
	res, err := srv.toolAuthzMiddleware()(inner(mw, roles))(ctx, req)
	if err != nil {
		return false, err.Error()
	}
	if res.IsError {
		return false, res.Content[0].(mcp.TextContent).Text
	}
	return true, ""
}

func userUpdateTool(users *authtest.BindingUsers) func(*authmw.AuthMiddleware, role.RoleServicer) mcpserver.ToolHandlerFunc {
	return func(mw *authmw.AuthMiddleware, roles role.RoleServicer) mcpserver.ToolHandlerFunc {
		return handleUserUpdate(users, nil, mw, roles)
	}
}

// TestTenantAdmin_ActsInOwnTenantOnly: acme's administrator updates acme's
// user, and an update of globex's user is answered not-found and reaches no
// store.
func TestTenantAdmin_ActsInOwnTenantOnly(t *testing.T) {
	tests := []struct {
		name   string
		caller *auth.Identity
		id     any
		allow  bool
	}{
		{name: "tenant admin, own tenant's user", caller: authtest.BindingTenantAdmin(), id: strconv.FormatUint(authtest.UserInBindingTenant, 10), allow: true},
		{name: "tenant admin, own tenant's user as a number", caller: authtest.BindingTenantAdmin(), id: float64(authtest.UserInBindingTenant), allow: true},
		{name: "tenant admin, another tenant's user", caller: authtest.BindingTenantAdmin(), id: strconv.FormatUint(authtest.UserInOtherTenant, 10)},
		{name: "tenant admin, another tenant's user as a number", caller: authtest.BindingTenantAdmin(), id: float64(authtest.UserInOtherTenant)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := authtest.NewBindingUsers()
			ok, refusal := runBindingTool(t, tt.caller, users, "qrdb_user_update", map[string]any{"id": tt.id, "name": "renamed"}, userUpdateTool(users))
			if ok != tt.allow {
				t.Fatalf("succeeded = %v, want %v (%s)", ok, tt.allow, refusal)
			}
			if reached := len(users.Updated()) > 0; reached != tt.allow {
				t.Fatalf("store reached = %v, want %v", reached, tt.allow)
			}
		})
	}
}

// TestCrossTenantBinding_WriteRefused: writing a RoleBinding that names any
// tenant other than the user's own is refused (*user.RoleBindingTenantError)
// and stored nowhere, while an own-tenant binding is written.
func TestCrossTenantBinding_WriteRefused(t *testing.T) {
	tests := []struct {
		name    string
		binding string
		allow   bool
	}{
		{name: "another tenant", binding: `{"role_id":"` + role.SystemRoleID(authtest.OtherTenant, role.RoleNameAdmin) + `","tenant_id":"` + authtest.OtherTenant + `"}`},
		{name: "the system tenant", binding: `{"role_id":"admin","tenant_id":"system"}`},
		{name: "own tenant", binding: `{"role_id":"` + role.SystemRoleID(authtest.BindingTenant, role.RoleNameUser) + `","tenant_id":"` + authtest.BindingTenant + `"}`, allow: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := authtest.NewBindingUsers()
			args := map[string]any{"id": strconv.FormatUint(authtest.UserInBindingTenant, 10), "role_bindings": "[" + tt.binding + "]"}
			ok, refusal := runBindingTool(t, authtest.BindingTenantAdmin(), users, "qrdb_user_update", args, userUpdateTool(users))
			if ok != tt.allow {
				t.Fatalf("succeeded = %v, want %v (%s)", ok, tt.allow, refusal)
			}
			if !tt.allow && !strings.Contains(refusal, "a role binding may name only its holder's own tenant") {
				t.Fatalf("refusal = %q, want the *user.RoleBindingTenantError message", refusal)
			}
			stored := len(users.User(authtest.UserInBindingTenant).RoleBindings) > 0
			if stored != tt.allow {
				t.Fatalf("binding stored = %v, want %v", stored, tt.allow)
			}
		})
	}
}
