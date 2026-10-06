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
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// TestTenantAdmin_ArgumentNamedTenantOrUser drives, as acme's administrator,
// the tools whose arguments name a tenant to list or create in, or a user
// inside a role assignment, through the tool authorization middleware. Each
// acts in acme and is refused in globex, and a refusal writes nothing.
func TestTenantAdmin_ArgumentNamedTenantOrUser(t *testing.T) {
	acmeUser := role.SystemRoleID(authtest.BindingTenant, role.RoleNameUser)
	type handlerFor func(*authtest.BindingUsers) func(*authmw.AuthMiddleware, role.RoleServicer) mcpserver.ToolHandlerFunc
	list := func(u *authtest.BindingUsers) func(*authmw.AuthMiddleware, role.RoleServicer) mcpserver.ToolHandlerFunc {
		return func(*authmw.AuthMiddleware, role.RoleServicer) mcpserver.ToolHandlerFunc {
			return handleUserList(u, nil)
		}
	}
	create := func(u *authtest.BindingUsers) func(*authmw.AuthMiddleware, role.RoleServicer) mcpserver.ToolHandlerFunc {
		return func(mw *authmw.AuthMiddleware, r role.RoleServicer) mcpserver.ToolHandlerFunc {
			return handleUserCreate(u, nil, mw, r)
		}
	}
	assign := func(u *authtest.BindingUsers) func(*authmw.AuthMiddleware, role.RoleServicer) mcpserver.ToolHandlerFunc {
		return func(mw *authmw.AuthMiddleware, r role.RoleServicer) mcpserver.ToolHandlerFunc {
			return handleRoleAssign(r, u, mw)
		}
	}
	tests := []struct {
		name    string
		tool    string
		args    map[string]any
		handler handlerFor
		allow   bool
	}{
		{name: "list in own tenant", tool: "qrdb_user_list", args: map[string]any{"tenant_id": authtest.BindingTenant}, handler: list, allow: true},
		{name: "list in another tenant", tool: "qrdb_user_list", args: map[string]any{"tenant_id": authtest.OtherTenant}, handler: list},
		{name: "create in own tenant", tool: "qrdb_user_create", args: map[string]any{"tenant_id": authtest.BindingTenant, "username": "carol", "without_email_login_key": true}, handler: create, allow: true},
		{name: "create in another tenant", tool: "qrdb_user_create", args: map[string]any{"tenant_id": authtest.OtherTenant, "username": "mallory", "without_email_login_key": true}, handler: create},
		{name: "assign to a user in own tenant", tool: "qrdb_role_assign", args: map[string]any{"user_id": float64(authtest.UserInBindingTenant), "role_id": acmeUser}, handler: assign, allow: true},
		{name: "assign to a user in another tenant", tool: "qrdb_role_assign", args: map[string]any{"user_id": float64(authtest.UserInOtherTenant), "role_id": acmeUser}, handler: assign},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := authtest.NewBindingUsers()
			ok, refusal := runBindingTool(t, authtest.BindingTenantAdmin(), users, tt.tool, tt.args, tt.handler(users))
			if ok != tt.allow {
				t.Fatalf("succeeded = %v, want %v (%s)", ok, tt.allow, refusal)
			}
			if !tt.allow {
				if got := users.Created(); len(got) != 0 {
					t.Fatalf("users created in %v, want none", got)
				}
				if other := users.User(authtest.UserInOtherTenant); len(other.RoleIDs) != 0 {
					t.Fatalf("globex user's roles = %v, want none", other.RoleIDs)
				}
			}
		})
	}
}
