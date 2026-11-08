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

// TestSOExclusive_ReconcileAllTenants proves ReconcileBuiltInRolesAllTenants
// is SO-exclusive under strict separation: the system Admin, which holds
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
		want    codes.Code
	}{
		{name: "system admin, strict", roleIDs: []string{admin}, strict: true, want: codes.PermissionDenied},
		{name: "SO holding admin, strict", roleIDs: []string{admin, so}, strict: true, want: codes.PermissionDenied},
		{name: "separate SO, strict", roleIDs: []string{so}, strict: true, want: codes.OK},
		{name: "system admin, standard", roleIDs: []string{admin}, want: codes.OK},
	}
	for _, c := range callers {
		t.Run(c.name, func(t *testing.T) {
			users := authtest.NewBindingUsers()
			ctx := tenant.WithTenantID(context.Background(), tenantid.System)
			if _, err := users.UpdateUser(ctx, &user.UpdateUserRequest{ID: authtest.SystemUserID, RoleIDs: c.roleIDs}); err != nil {
				t.Fatalf("seed caller: %v", err)
			}
			roles := authtest.BindingRoles(t)
			caller := &auth.Identity{Subject: "operator", TenantID: tenantid.System}
			mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles),
				authmw.WithAuthenticator(authtest.NewFixedAuthenticator(caller)),
				authmw.WithUserService(users), authmw.WithStrictSeparation(c.strict))
			srv := &qrdbServiceServer{userSvc: users, roleSvc: roles, authMW: mw,
				tenantSvc: liveGRPCTenants(t, authtest.BindingTenant)}
			handler := func(ctx context.Context, _ any) (any, error) {
				return srv.ReconcileBuiltInRolesAllTenants(ctx, &emptypb.Empty{})
			}
			info := &grpc.UnaryServerInfo{FullMethod: pb.QRDBService_ReconcileBuiltInRolesAllTenants_FullMethodName}
			_, err := mw.UnaryInterceptor()(context.Background(), &emptypb.Empty{}, info, handler)
			if got := status.Code(err); got != c.want {
				t.Fatalf("code = %v, want %v: %v", got, c.want, err)
			}
		})
	}
}
