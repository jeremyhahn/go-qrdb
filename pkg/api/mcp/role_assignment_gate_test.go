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

package mcp

import (
	"context"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// TestAssignRole_GoesThroughTheRoleAssignmentGate asserts qrdb_role_assign
// validates the role as every other role-assignment write does.
func TestAssignRole_GoesThroughTheRoleAssignmentGate(t *testing.T) {
	tests := []struct {
		name   string
		roleID string
		ok     bool
	}{
		{name: "the user's own tenant's role", roleID: role.SystemRoleID(authtest.BindingTenant, role.RoleNameUser), ok: true},
		{name: "another tenant's role", roleID: role.SystemRoleID(authtest.OtherTenant, role.RoleNameAdmin), ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := authtest.NewBindingUsers()
			res, err := handleRoleAssign(authtest.BindingRoles(t), users, nil)(context.Background(),
				makeReq(map[string]any{"user_id": "200", "role_id": tt.roleID}))
			if tt.ok && (err != nil || res.IsError) {
				t.Fatalf("assign = (%+v, %v), want success", res, err)
			}
			if !tt.ok {
				if got := ServiceErrorCode(err); got != wire.CodeInvalidArgument {
					t.Fatalf("code = %v (err %v), want InvalidArgument", got, err)
				}
			}
			if assigned := slices.Contains(users.User(authtest.UserInBindingTenant).RoleIDs, tt.roleID); assigned != tt.ok {
				t.Fatalf("assigned = %v, want %v", assigned, tt.ok)
			}
		})
	}
}
