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

package unix

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

// TestStartRegistersTargetTenants asserts the unix listener's Start registers
// the gRPC target table on its AuthMiddleware, so UpdateUser on this
// transport is authorized against the user's own tenant: a system-tenant
// operator whose only grant is a binding for acme may update the acme user
// and is refused the globex one.
func TestStartRegistersTargetTenants(t *testing.T) {
	users := authtest.NewBindingUsers()
	mw := authmw.New(rbac.New(authtest.BindingRoles(t)),
		authmw.WithAuthenticator(authtest.NewFixedAuthenticator(authtest.BindingOperator())),
		authmw.WithUserService(users))
	srv := NewServer(newMockKVService(), &mockAdminService{}, tempSocketPath(t),
		WithAuthMiddleware(mw),
		WithTLS(selfSignedTLSConfig(t)),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

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
			reached := false
			handler := func(context.Context, any) (any, error) {
				reached = true
				return &pb.UserResponse{}, nil
			}
			info := &grpc.UnaryServerInfo{FullMethod: pb.QRDBService_UpdateUser_FullMethodName}
			_, err := mw.UnaryInterceptor()(context.Background(), &pb.UpdateUserProtoRequest{Id: tt.id}, info, handler)
			if got := status.Code(err); got != tt.want {
				t.Fatalf("code = %v, want %v (err %v)", got, tt.want, err)
			}
			if reached != (tt.want == codes.OK) {
				t.Fatalf("handler reached = %v, want %v", reached, tt.want == codes.OK)
			}
		})
	}
}
