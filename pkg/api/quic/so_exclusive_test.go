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
	"strconv"
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

// TestSOExclusive_PostureGatesEveryRoleOp holds the QUIC create role,
// per-tenant reconcile and all-tenants reconcile to the exact posture gate
// (go-qrdb#1129): under strict separation an Admin without the SO role is
// refused StatusPermissionDenied with SO_ROLE_REQUIRED and an SO holding
// Admin with SO_ROLE_CONFLICT; a separate SO passes. Under the standard
// posture all three callers pass, the Admin keeping its SO duties.
func TestSOExclusive_PostureGatesEveryRoleOp(t *testing.T) {
	sys := tenantid.System
	ops := []struct {
		name    string
		payload string
		call    func(*Server, context.Context, []byte) ([]byte, error)
	}{
		{name: "create role", payload: `{"name":"reviewer","tenantID":"` + sys + `","permissions":["kv:get"]}`,
			call: (*Server).handleCreateRole},
		{name: "reconcile built-in roles", payload: `{"tenantID":"` + sys + `"}`,
			call: (*Server).handleReconcileRoles},
		{name: "reconcile every tenant", call: (*Server).handleReconcileRolesAllTenants},
	}
	callers := []struct {
		name       string
		roleNames  []string
		wantReason string
	}{
		{name: "admin without SO", roleNames: []string{role.RoleNameAdmin}, wantReason: auth.ReasonSORoleRequired},
		{name: "SO holding admin", roleNames: []string{role.RoleNameAdmin, role.RoleNameSO}, wantReason: auth.ReasonSORoleConflict},
		{name: "separate SO", roleNames: []string{role.RoleNameSO}},
	}
	for _, strict := range []bool{true, false} {
		for _, op := range ops {
			for _, c := range callers {
				t.Run(op.name+"/"+c.name+"/strict="+strconv.FormatBool(strict), func(t *testing.T) {
					roles := authtest.BindingRoles(t)
					sysCtx := tenant.WithTenantID(context.Background(), sys)
					roleIDs := make([]string, 0, len(c.roleNames))
					for _, name := range c.roleNames {
						r, err := roles.GetRoleByName(sysCtx, sys, name)
						if err != nil {
							t.Fatalf("system %s role: %v", name, err)
						}
						roleIDs = append(roleIDs, r.ID)
					}
					mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles), authmw.WithStrictSeparation(strict))
					srv := NewServer(nil, nil, "localhost:0", WithRoleService(roles),
						WithTenantService(liveQUICTenants(t, "acme")), WithAuthMiddleware(mw))
					ctx := auth.WithIdentity(sysCtx, &auth.Identity{Subject: "system-caller", TenantID: sys, Roles: roleIDs})
					_, err := op.call(srv, ctx, []byte(op.payload))
					if !strict || c.wantReason == "" {
						if err != nil {
							t.Fatalf("want admitted, got %v", err)
						}
						return
					}
					if code, _ := mapServiceError(err); code != wire.StatusPermissionDenied {
						t.Fatalf("status = %v, want StatusPermissionDenied: %v", code, err)
					}
					if info := errorPayloadOf(err).Info; info == nil || info.Reason != c.wantReason {
						t.Fatalf("refusal info = %+v, want reason %q", info, c.wantReason)
					}
				})
			}
		}
	}
}
