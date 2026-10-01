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
)

// TestRouteCheck_BindingGrantsInItsOwnTenantOnly drives UpdateUser through
// the auth interceptor with the table this package registers
// (IdentityTargets), as a system-tenant operator whose only grant is a
// binding for tenant acme: the acme user is updated, the globex user is
// refused before the handler runs. The unix listener registers the same
// table on the same interceptor, so this covers it too.
func TestRouteCheck_BindingGrantsInItsOwnTenantOnly(t *testing.T) {
	tests := []struct {
		name string
		id   uint64
		want codes.Code
	}{
		{name: "user in the bound tenant", id: authtest.UserInBindingTenant, want: codes.OK},
		{name: "user in another tenant", id: authtest.UserInOtherTenant, want: codes.PermissionDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := authtest.NewBindingUsers()
			mw := authmw.New(rbac.New(authtest.BindingRoles(t)),
				authmw.WithAuthenticator(authtest.NewFixedAuthenticator(authtest.BindingOperator())),
				authmw.WithUserService(users))
			if err := mw.RegisterGRPCTargets(IdentityTargets()); err != nil {
				t.Fatalf("RegisterGRPCTargets: %v", err)
			}
			srv := &qrdbServiceServer{userSvc: users}
			info := &grpc.UnaryServerInfo{FullMethod: pb.QRDBService_UpdateUser_FullMethodName}
			handler := func(ctx context.Context, req any) (any, error) {
				return srv.UpdateUser(ctx, req.(*pb.UpdateUserProtoRequest))
			}
			_, err := mw.UnaryInterceptor()(context.Background(), &pb.UpdateUserProtoRequest{Id: tt.id, Name: "renamed"}, info, handler)
			if got := status.Code(err); got != tt.want {
				t.Fatalf("code = %v, want %v (err %v)", got, tt.want, err)
			}
			if reached := len(users.Updated()) > 0; reached != (tt.want == codes.OK) {
				t.Fatalf("store reached = %v, want %v", reached, tt.want == codes.OK)
			}
		})
	}
}

// TestIdentityTargets_ReadTheRequestField asserts each extractor reads the
// field its methods carry, and falls back to the caller's own tenant for a
// request that does not carry it.
func TestIdentityTargets_ReadTheRequestField(t *testing.T) {
	targets := IdentityTargets()
	cases := []struct {
		method string
		req    any
		want   authmw.Target
	}{
		{pb.QRDBService_GetUser_FullMethodName, &pb.GetUserRequest{Id: 7}, authmw.UserTarget(7)},
		{pb.QRDBService_AddCertBinding_FullMethodName, &pb.AddCertBindingRequest{UserId: 7}, authmw.UserTarget(7)},
		{pb.QRDBService_GetRole_FullMethodName, &pb.GetRoleRequest{Id: "r"}, authmw.RoleTarget("r")},
		{pb.QRDBService_ListUsers_FullMethodName, &pb.ListUsersRequest{TenantId: "acme"}, authmw.TenantTarget("acme")},
	}
	for _, tc := range cases {
		if got := targets[tc.method](tc.req); got != tc.want {
			t.Errorf("%s: target = %+v, want %+v", tc.method, got, tc.want)
		}
	}
	for i, fn := range []authmw.GRPCTargetFunc{targetUserID, targetUserRef, targetRoleID, targetTenantField} {
		if got := fn(struct{}{}); got != authmw.SelfTarget() {
			t.Errorf("extractor %d on a request without the field = %+v, want the caller's own tenant", i, got)
		}
	}
}
