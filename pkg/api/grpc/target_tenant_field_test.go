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

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// TestRouteCheck_FieldTargets drives the methods whose target is a request
// field other than a user id in the path sense — a tenant to list or create
// in, a user named inside a role assignment — through the auth interceptor as
// the binding fixture's operator, whose only grant is a binding for acme.
// Each is allowed on acme and refused on globex before the handler runs.
func TestRouteCheck_FieldTargets(t *testing.T) {
	acmeUser := role.SystemRoleID(authtest.BindingTenant, role.RoleNameUser)
	tests := []struct {
		name   string
		method string
		req    any
		call   func(*qrdbServiceServer, context.Context, any) (any, error)
		want   codes.Code
	}{
		{
			name: "list in the bound tenant", method: pb.QRDBService_ListUsers_FullMethodName,
			req: &pb.ListUsersRequest{TenantId: authtest.BindingTenant},
			call: func(s *qrdbServiceServer, ctx context.Context, r any) (any, error) {
				return s.ListUsers(ctx, r.(*pb.ListUsersRequest))
			},
			want: codes.OK,
		},
		{
			name: "list in another tenant", method: pb.QRDBService_ListUsers_FullMethodName,
			req: &pb.ListUsersRequest{TenantId: authtest.OtherTenant},
			call: func(s *qrdbServiceServer, ctx context.Context, r any) (any, error) {
				return s.ListUsers(ctx, r.(*pb.ListUsersRequest))
			},
			want: codes.PermissionDenied,
		},
		{
			name: "create in the bound tenant", method: pb.QRDBService_CreateUser_FullMethodName,
			req: &pb.CreateUserRequest{TenantId: authtest.BindingTenant, Username: "carol", WithoutEmailLoginKey: true},
			call: func(s *qrdbServiceServer, ctx context.Context, r any) (any, error) {
				return s.CreateUser(ctx, r.(*pb.CreateUserRequest))
			},
			want: codes.OK,
		},
		{
			name: "create in another tenant", method: pb.QRDBService_CreateUser_FullMethodName,
			req: &pb.CreateUserRequest{TenantId: authtest.OtherTenant, Username: "mallory", WithoutEmailLoginKey: true},
			call: func(s *qrdbServiceServer, ctx context.Context, r any) (any, error) {
				return s.CreateUser(ctx, r.(*pb.CreateUserRequest))
			},
			want: codes.PermissionDenied,
		},
		{
			name: "assign to a user in the bound tenant", method: pb.QRDBService_AssignRole_FullMethodName,
			req: &pb.AssignRoleRequest{UserId: authtest.UserInBindingTenant, RoleId: acmeUser},
			call: func(s *qrdbServiceServer, ctx context.Context, r any) (any, error) {
				return s.AssignRole(ctx, r.(*pb.AssignRoleRequest))
			},
			want: codes.OK,
		},
		{
			name: "assign to a user in another tenant", method: pb.QRDBService_AssignRole_FullMethodName,
			req: &pb.AssignRoleRequest{UserId: authtest.UserInOtherTenant, RoleId: acmeUser},
			call: func(s *qrdbServiceServer, ctx context.Context, r any) (any, error) {
				return s.AssignRole(ctx, r.(*pb.AssignRoleRequest))
			},
			want: codes.PermissionDenied,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := authtest.NewBindingUsers()
			roles := authtest.BindingRoles(t)
			mw := authmw.New(rbac.New(roles),
				authmw.WithAuthenticator(authtest.NewFixedAuthenticator(authtest.BindingOperator())),
				authmw.WithUserService(users),
				authmw.WithRoleService(roles))
			if err := mw.RegisterGRPCTargets(IdentityTargets()); err != nil {
				t.Fatalf("RegisterGRPCTargets: %v", err)
			}
			srv := &qrdbServiceServer{userSvc: users, roleSvc: roles, authMW: mw}
			reached := false
			handler := func(ctx context.Context, req any) (any, error) {
				reached = true
				return tt.call(srv, ctx, req)
			}
			_, err := mw.UnaryInterceptor()(context.Background(), tt.req, &grpc.UnaryServerInfo{FullMethod: tt.method}, handler)
			if got := status.Code(err); got != tt.want {
				t.Fatalf("code = %v, want %v (err %v)", got, tt.want, err)
			}
			if reached != (tt.want == codes.OK) {
				t.Fatalf("handler reached = %v, want %v", reached, tt.want == codes.OK)
			}
		})
	}
}
