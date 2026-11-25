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

package grpc

import (
	"context"
	"strconv"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
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
		name, method string
		req          any
		call         func(*qrdbServiceServer, context.Context, any) (any, error)
	}{
		{name: "create role", method: pb.QRDBService_CreateRole_FullMethodName,
			req: &pb.CreateRoleRequest{Name: "reviewer", TenantId: authtest.BindingTenant, Permissions: []string{"kv:get"}},
			call: func(s *qrdbServiceServer, ctx context.Context, req any) (any, error) {
				return s.CreateRole(ctx, req.(*pb.CreateRoleRequest))
			}},
		{name: "reconcile built-in roles", method: pb.QRDBService_ReconcileBuiltInRoles_FullMethodName,
			req: &pb.ReconcileBuiltInRolesRequest{TenantId: authtest.BindingTenant},
			call: func(s *qrdbServiceServer, ctx context.Context, req any) (any, error) {
				return s.ReconcileBuiltInRoles(ctx, req.(*pb.ReconcileBuiltInRolesRequest))
			}},
	}
	callers := []struct {
		name    string
		roleIDs []string
		want    codes.Code
	}{
		{name: "tenant admin", roleIDs: []string{admin}, want: codes.PermissionDenied},
		{name: "SO holding admin", roleIDs: []string{admin, so}, want: codes.PermissionDenied},
		{name: "separate SO", roleIDs: []string{so}, want: codes.OK},
	}
	for _, op := range ops {
		for _, c := range callers {
			t.Run(op.name+"/"+c.name, func(t *testing.T) {
				users := authtest.NewBindingUsers()
				ctx := tenant.WithTenantID(context.Background(), tenantid.System)
				if _, err := users.UpdateUser(ctx, &user.UpdateUserRequest{ID: authtest.BindingTenantAdminID, RoleIDs: c.roleIDs}); err != nil {
					t.Fatalf("seed caller: %v", err)
				}
				roles := authtest.BindingRoles(t)
				mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles),
					authmw.WithAuthenticator(authtest.NewFixedAuthenticator(authtest.BindingTenantAdmin())),
					authmw.WithUserService(users), authmw.WithStrictSeparation(true))
				srv := &qrdbServiceServer{userSvc: users, roleSvc: roles, authMW: mw}
				handler := func(ctx context.Context, req any) (any, error) { return op.call(srv, ctx, req) }
				_, err := mw.UnaryInterceptor()(context.Background(), op.req, &grpc.UnaryServerInfo{FullMethod: op.method}, handler)
				if got := status.Code(err); got != c.want {
					t.Fatalf("code = %v, want %v: %v", got, c.want, err)
				}
			})
		}
	}
}

// TestSOExclusive_PostureGatesEveryRoleOp holds the gRPC create role,
// per-tenant reconcile and all-tenants reconcile to the exact posture gate
// (go-qrdb#1129): under strict separation an Admin without the SO role is
// refused PermissionDenied with SO_ROLE_REQUIRED and an SO holding Admin
// with SO_ROLE_CONFLICT; a separate SO passes. Under the standard posture
// all three callers pass, the Admin keeping its SO duties.
func TestSOExclusive_PostureGatesEveryRoleOp(t *testing.T) {
	sys := tenantid.System
	ops := []struct {
		name string
		call func(*qrdbServiceServer, context.Context) error
	}{
		{name: "create role", call: func(s *qrdbServiceServer, ctx context.Context) error {
			_, err := s.CreateRole(ctx, &pb.CreateRoleRequest{Name: "reviewer", TenantId: sys, Permissions: []string{"kv:get"}})
			return err
		}},
		{name: "reconcile built-in roles", call: func(s *qrdbServiceServer, ctx context.Context) error {
			_, err := s.ReconcileBuiltInRoles(ctx, &pb.ReconcileBuiltInRolesRequest{TenantId: sys})
			return err
		}},
		{name: "reconcile every tenant", call: func(s *qrdbServiceServer, ctx context.Context) error {
			_, err := s.ReconcileBuiltInRolesAllTenants(ctx, &emptypb.Empty{})
			return err
		}},
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
					srv := &qrdbServiceServer{roleSvc: roles, tenantSvc: liveGRPCTenants(t, "acme"), authMW: mw}
					ctx := auth.WithIdentity(sysCtx, &auth.Identity{Subject: "system-caller", TenantID: sys, Roles: roleIDs})
					err := op.call(srv, ctx)
					if !strict || c.wantReason == "" {
						if err != nil {
							t.Fatalf("want admitted, got %v", err)
						}
						return
					}
					st := status.Convert(err)
					if st.Code() != codes.PermissionDenied {
						t.Fatalf("code = %v, want PermissionDenied: %v", st.Code(), err)
					}
					if reason, _, ok := refusalFromStatus(st); !ok || reason != c.wantReason {
						t.Fatalf("reason = %q (%v), want %q", reason, ok, c.wantReason)
					}
				})
			}
		}
	}
}
