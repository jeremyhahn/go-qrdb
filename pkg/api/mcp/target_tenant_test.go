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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
)

// TestRouteCheck_BindingGrantsInItsOwnTenantOnly drives qrdb_user_update
// through this server's tool authorization middleware as a system-tenant
// operator whose only grant is a binding for tenant acme: the acme user's
// update runs, the globex user's is refused before the tool handler.
func TestRouteCheck_BindingGrantsInItsOwnTenantOnly(t *testing.T) {
	tests := []struct {
		name  string
		id    uint64
		allow bool
	}{
		{name: "user in the bound tenant", id: authtest.UserInBindingTenant, allow: true},
		{name: "user in another tenant", id: authtest.UserInOtherTenant, allow: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := authtest.NewBindingUsers()
			mw := authmw.New(rbac.New(authtest.BindingRoles(t)),
				authmw.WithAuthenticator(authtest.NewFixedAuthenticator(authtest.BindingOperator())),
				authmw.WithUserService(users))
			srv := NewServer(newMockKVService(), newMockAdminService(), ":0", WithUserService(users), WithAuthMiddleware(mw))
			ctx, _, err := mw.Authenticate(context.Background())
			if err != nil {
				t.Fatalf("Authenticate: %v", err)
			}
			req := makeReq(map[string]any{"id": strconv.FormatUint(tt.id, 10), "name": "renamed"})
			req.Params.Name = "qrdb_user_update"

			handler := srv.toolAuthzMiddleware()(handleUserUpdate(users, nil, mw, nil))
			res, err := handler(ctx, req)
			if err != nil {
				t.Fatalf("handler error = %v", err)
			}
			if res.IsError == tt.allow {
				t.Fatalf("IsError = %v, want %v: %+v", res.IsError, !tt.allow, res.Content)
			}
			if reached := len(users.Updated()) > 0; reached != tt.allow {
				t.Fatalf("store reached = %v, want %v", reached, tt.allow)
			}
		})
	}
}

// TestIdentityTargets_ReadTheArguments asserts the argument readers fall back
// to the caller's own tenant on arguments they cannot read.
func TestIdentityTargets_ReadTheArguments(t *testing.T) {
	targets := identityTargets()
	bad := map[string]any{"id": 7, "user_id": "x", "tenant_id": 3}
	for _, tool := range []string{"qrdb_user_update", "qrdb_role_assign", "qrdb_user_list", "qrdb_role_get"} {
		if got := targets[tool](bad); got != authmw.SelfTarget() {
			t.Errorf("%s on unreadable arguments = %+v, want the caller's own tenant", tool, got)
		}
	}
	if got := targets["qrdb_role_get"](map[string]any{"id": "r"}); got != authmw.RoleTarget("r") {
		t.Errorf("qrdb_role_get = %+v, want the role target", got)
	}
}
