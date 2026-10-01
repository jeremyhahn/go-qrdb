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
	"slices"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// TestAssignRole_GoesThroughTheRoleAssignmentGate asserts AssignRole
// validates the role as every other role-assignment write does: another
// tenant's role is refused and never reaches the store.
func TestAssignRole_GoesThroughTheRoleAssignmentGate(t *testing.T) {
	tests := []struct {
		name   string
		roleID string
		want   codes.Code
	}{
		{name: "the user's own tenant's role", roleID: role.SystemRoleID(authtest.BindingTenant, role.RoleNameUser), want: codes.OK},
		{name: "another tenant's role", roleID: role.SystemRoleID(authtest.OtherTenant, role.RoleNameAdmin), want: codes.InvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := authtest.NewBindingUsers()
			srv := &qrdbServiceServer{userSvc: users, roleSvc: authtest.BindingRoles(t)}
			_, err := srv.AssignRole(context.Background(), &pb.AssignRoleRequest{UserId: authtest.UserInBindingTenant, RoleId: tt.roleID})
			if got := status.Code(err); got != tt.want {
				t.Fatalf("code = %v, want %v (err %v)", got, tt.want, err)
			}
			if assigned := slices.Contains(users.User(authtest.UserInBindingTenant).RoleIDs, tt.roleID); assigned != (tt.want == codes.OK) {
				t.Fatalf("assigned = %v, want %v", assigned, tt.want == codes.OK)
			}
		})
	}
}
