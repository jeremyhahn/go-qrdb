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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// TestTenantAdmin_RequestNamedTenantOrUser drives the methods whose request
// names a tenant to list or create in, or a user inside a role assignment,
// through the auth interceptor as acme's administrator. Each acts in acme and
// is refused in globex, and a refusal writes nothing.
func TestTenantAdmin_RequestNamedTenantOrUser(t *testing.T) {
	acmeUser := role.SystemRoleID(authtest.BindingTenant, role.RoleNameUser)
	tests := []struct {
		name   string
		method string
		req    any
		call   func(*qrdbServiceServer, context.Context, any) (any, error)
		want   codes.Code
	}{
		{
			name: "list in own tenant", method: pb.QRDBService_ListUsers_FullMethodName,
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
			name: "create in own tenant", method: pb.QRDBService_CreateUser_FullMethodName,
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
			name: "assign to a user in own tenant", method: pb.QRDBService_AssignRole_FullMethodName,
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
			want: codes.NotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := authtest.NewBindingUsers()
			err := bindingInterceptor(t, authtest.BindingTenantAdmin(), users, tt.method, tt.req, tt.call)
			if got := status.Code(err); got != tt.want {
				t.Fatalf("code = %v, want %v (err %v)", got, tt.want, err)
			}
			if tt.want != codes.OK {
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
