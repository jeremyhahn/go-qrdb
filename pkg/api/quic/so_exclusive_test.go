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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// Under strict separation, role create and built-in reconcile are
// SO-exclusive acts: the tenant admin is refused, and so is an SO that also
// holds the admin role; only a separate SO passes (go-qrdb#1128).
func TestSOExclusive_RoleCreateAndReconcile(t *testing.T) {
	admin := role.SystemRoleID(authtest.BindingTenant, role.RoleNameAdmin)
	so := role.SystemRoleID(authtest.BindingTenant, role.RoleNameSO)
	ops := []struct {
		name    string
		payload string
		call    func(*Server, context.Context, []byte) ([]byte, error)
	}{
		{name: "create role", payload: `{"name":"reviewer","tenantID":"` + authtest.BindingTenant + `","permissions":["kv:get"]}`,
			call: (*Server).handleCreateRole},
		{name: "reconcile built-in roles", payload: `{"tenantID":"` + authtest.BindingTenant + `"}`,
			call: (*Server).handleReconcileRoles},
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
				users := authtest.NewBindingUsers()
				seedCtx := tenant.WithTenantID(context.Background(), tenantid.System)
				if _, err := users.UpdateUser(seedCtx, &user.UpdateUserRequest{ID: authtest.BindingTenantAdminID, RoleIDs: c.roleIDs}); err != nil {
					t.Fatalf("seed caller: %v", err)
				}
				roles := authtest.BindingRoles(t)
				mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles),
					authmw.WithAuthenticator(authtest.NewFixedAuthenticator(authtest.BindingTenantAdmin())),
					authmw.WithUserService(users), authmw.WithStrictSeparation(true))
				srv := NewServer(nil, nil, "localhost:0", WithUserService(users), WithRoleService(roles), WithAuthMiddleware(mw))
				ctx, err := mw.AuthenticateQUIC(context.Background())
				if err != nil {
					t.Fatalf("AuthenticateQUIC: %v", err)
				}
				_, err = op.call(srv, ctx, []byte(op.payload))
				if !c.refused {
					if err != nil {
						t.Fatalf("want success, got %v", err)
					}
					return
				}
				if code, _ := mapServiceError(err); code != wire.StatusPermissionDenied {
					t.Fatalf("status = %v, want StatusPermissionDenied: %v", code, err)
				}
			})
		}
	}
}

// TestSOExclusive_ReconcileAllTenants proves the all-tenant reconcile verb is
// SO-exclusive under strict separation: the system Admin, which holds
// roles:reconcile_all, is refused, and so is a system SO still holding admin;
// a separate system SO runs it. In the standard posture the system Admin
// runs it as before.
func TestSOExclusive_ReconcileAllTenants(t *testing.T) {
	admin := role.SystemRoleID(tenantid.System, role.RoleNameAdmin)
	so := role.SystemRoleID(tenantid.System, role.RoleNameSO)
	callers := []struct {
		name    string
		roleIDs []string
		strict  bool
		refused bool
	}{
		{name: "system admin, strict", roleIDs: []string{admin}, strict: true, refused: true},
		{name: "SO holding admin, strict", roleIDs: []string{admin, so}, strict: true, refused: true},
		{name: "separate SO, strict", roleIDs: []string{so}, strict: true},
		{name: "system admin, standard", roleIDs: []string{admin}},
	}
	for _, c := range callers {
		t.Run(c.name, func(t *testing.T) {
			users := authtest.NewBindingUsers()
			seedCtx := tenant.WithTenantID(context.Background(), tenantid.System)
			if _, err := users.UpdateUser(seedCtx, &user.UpdateUserRequest{ID: authtest.SystemUserID, RoleIDs: c.roleIDs}); err != nil {
				t.Fatalf("seed caller: %v", err)
			}
			roles := authtest.BindingRoles(t)
			caller := &auth.Identity{Subject: "operator", TenantID: tenantid.System}
			mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles),
				authmw.WithAuthenticator(authtest.NewFixedAuthenticator(caller)),
				authmw.WithUserService(users), authmw.WithStrictSeparation(c.strict))
			srv := NewServer(nil, nil, "localhost:0", WithUserService(users), WithRoleService(roles),
				WithTenantService(liveQUICTenants(t, authtest.BindingTenant)), WithAuthMiddleware(mw))
			ctx, err := mw.AuthenticateQUIC(context.Background())
			if err != nil {
				t.Fatalf("AuthenticateQUIC: %v", err)
			}
			_, err = srv.handleReconcileRolesAllTenants(ctx, nil)
			if !c.refused {
				if err != nil {
					t.Fatalf("want success, got %v", err)
				}
				return
			}
			if code, _ := mapServiceError(err); code != wire.StatusPermissionDenied {
				t.Fatalf("status = %v, want StatusPermissionDenied: %v", code, err)
			}
		})
	}
}
