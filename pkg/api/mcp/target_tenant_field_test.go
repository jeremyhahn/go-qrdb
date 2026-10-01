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
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// TestRouteCheck_ArgumentTargets drives the tools whose target is an argument
// other than the path-like user id — a tenant to list or create in, a user
// named inside a role assignment — through the tool authorization middleware
// as the binding fixture's operator, whose only grant is a binding for acme.
// Each is allowed on acme and refused on globex before the tool handler runs.
func TestRouteCheck_ArgumentTargets(t *testing.T) {
	acmeUser := role.SystemRoleID(authtest.BindingTenant, role.RoleNameUser)
	type handlerFor func(*authtest.BindingUsers, role.RoleServicer, *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc
	list := func(u *authtest.BindingUsers, _ role.RoleServicer, _ *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
		return handleUserList(u, nil)
	}
	create := func(u *authtest.BindingUsers, r role.RoleServicer, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
		return handleUserCreate(u, nil, mw, r)
	}
	assign := func(u *authtest.BindingUsers, r role.RoleServicer, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
		return handleRoleAssign(r, u, mw)
	}
	tests := []struct {
		name    string
		tool    string
		args    map[string]any
		handler handlerFor
		allow   bool
	}{
		{name: "list in the bound tenant", tool: "qrdb_user_list", args: map[string]any{"tenant_id": authtest.BindingTenant}, handler: list, allow: true},
		{name: "list in another tenant", tool: "qrdb_user_list", args: map[string]any{"tenant_id": authtest.OtherTenant}, handler: list, allow: false},
		{name: "create in the bound tenant", tool: "qrdb_user_create", args: map[string]any{"tenant_id": authtest.BindingTenant, "username": "carol", "without_email_login_key": true}, handler: create, allow: true},
		{name: "create in another tenant", tool: "qrdb_user_create", args: map[string]any{"tenant_id": authtest.OtherTenant, "username": "mallory", "without_email_login_key": true}, handler: create, allow: false},
		{name: "assign to a user in the bound tenant", tool: "qrdb_role_assign", args: map[string]any{"user_id": float64(authtest.UserInBindingTenant), "role_id": acmeUser}, handler: assign, allow: true},
		{name: "assign to a user in another tenant", tool: "qrdb_role_assign", args: map[string]any{"user_id": float64(authtest.UserInOtherTenant), "role_id": acmeUser}, handler: assign, allow: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := authtest.NewBindingUsers()
			roles := authtest.BindingRoles(t)
			mw := authmw.New(rbac.New(roles),
				authmw.WithAuthenticator(authtest.NewFixedAuthenticator(authtest.BindingOperator())),
				authmw.WithUserService(users),
				authmw.WithRoleService(roles))
			srv := NewServer(newMockKVService(), newMockAdminService(), ":0",
				WithUserService(users), WithRoleService(roles), WithAuthMiddleware(mw))
			ctx, _, err := mw.Authenticate(context.Background())
			if err != nil {
				t.Fatalf("Authenticate: %v", err)
			}
			req := makeReq(tt.args)
			req.Params.Name = tt.tool

			reached := false
			inner := tt.handler(users, roles, mw)
			handler := srv.toolAuthzMiddleware()(func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				reached = true
				return inner(ctx, r)
			})
			res, err := handler(ctx, req)
			if err != nil {
				t.Fatalf("handler error = %v", err)
			}
			if res.IsError == tt.allow {
				t.Fatalf("IsError = %v, want %v: %+v", res.IsError, !tt.allow, res.Content)
			}
			if reached != tt.allow {
				t.Fatalf("tool handler reached = %v, want %v", reached, tt.allow)
			}
		})
	}
}
