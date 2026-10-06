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
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// bindingInterceptor runs fullMethod's handler behind the auth interceptor
// over the binding fixture, as caller.
func bindingInterceptor(t *testing.T, caller *auth.Identity, users *authtest.BindingUsers, fullMethod string, req any, call func(*qrdbServiceServer, context.Context, any) (any, error)) error {
	t.Helper()
	roles := authtest.BindingRoles(t)
	mw := authmw.New(rbac.New(roles),
		authmw.WithAuthenticator(authtest.NewFixedAuthenticator(caller)),
		authmw.WithUserService(users))
	srv := &qrdbServiceServer{userSvc: users, roleSvc: roles, authMW: mw}
	handler := func(ctx context.Context, req any) (any, error) { return call(srv, ctx, req) }
	_, err := mw.UnaryInterceptor()(context.Background(), req, &grpc.UnaryServerInfo{FullMethod: fullMethod}, handler)
	return err
}

func callUpdateUser(s *qrdbServiceServer, ctx context.Context, r any) (any, error) {
	return s.UpdateUser(ctx, r.(*pb.UpdateUserProtoRequest))
}

// TestTenantAdmin_ActsInOwnTenantOnly: acme's administrator updates acme's
// user, and an update of globex's user is answered not-found and reaches no
// store.
func TestTenantAdmin_ActsInOwnTenantOnly(t *testing.T) {
	tests := []struct {
		name   string
		caller *auth.Identity
		id     uint64
		want   codes.Code
	}{
		{name: "tenant admin, own tenant's user", caller: authtest.BindingTenantAdmin(), id: authtest.UserInBindingTenant, want: codes.OK},
		{name: "tenant admin, another tenant's user", caller: authtest.BindingTenantAdmin(), id: authtest.UserInOtherTenant, want: codes.NotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := authtest.NewBindingUsers()
			err := bindingInterceptor(t, tt.caller, users, pb.QRDBService_UpdateUser_FullMethodName, &pb.UpdateUserProtoRequest{Id: tt.id, Name: "renamed"}, callUpdateUser)
			if got := status.Code(err); got != tt.want {
				t.Fatalf("code = %v, want %v (err %v)", got, tt.want, err)
			}
			if reached := len(users.Updated()) > 0; reached != (tt.want == codes.OK) {
				t.Fatalf("store reached = %v, want %v", reached, tt.want == codes.OK)
			}
		})
	}
}

// TestCrossTenantBinding_WriteRefused: writing a RoleBinding that names any
// tenant other than the user's own is refused with InvalidArgument
// (*user.RoleBindingTenantError) and stored nowhere -- by acme's
// administrator, who holds roles:assign over the user -- while an own-tenant
// binding is written.
func TestCrossTenantBinding_WriteRefused(t *testing.T) {
	tests := []struct {
		name    string
		binding *pb.RoleBinding
		want    codes.Code
	}{
		{name: "another tenant", binding: &pb.RoleBinding{RoleId: role.SystemRoleID(authtest.OtherTenant, role.RoleNameAdmin), TenantId: authtest.OtherTenant}, want: codes.InvalidArgument},
		{name: "the system tenant", binding: &pb.RoleBinding{RoleId: "admin", TenantId: "system"}, want: codes.InvalidArgument},
		{name: "own tenant", binding: &pb.RoleBinding{RoleId: role.SystemRoleID(authtest.BindingTenant, role.RoleNameUser), TenantId: authtest.BindingTenant}, want: codes.OK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := authtest.NewBindingUsers()
			req := &pb.UpdateUserProtoRequest{Id: authtest.UserInBindingTenant, HasRoleBindings: true, RoleBindings: []*pb.RoleBinding{tt.binding}}
			err := bindingInterceptor(t, authtest.BindingTenantAdmin(), users, pb.QRDBService_UpdateUser_FullMethodName, req, callUpdateUser)
			if got := status.Code(err); got != tt.want {
				t.Fatalf("code = %v, want %v (err %v)", got, tt.want, err)
			}
			stored := len(users.User(authtest.UserInBindingTenant).RoleBindings) > 0
			if stored != (tt.want == codes.OK) {
				t.Fatalf("binding stored = %v, want %v", stored, tt.want == codes.OK)
			}
		})
	}
}
