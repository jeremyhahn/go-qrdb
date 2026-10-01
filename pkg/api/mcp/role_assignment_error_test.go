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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestRoleAssignmentRefusals_Classify asserts each refusal the
// role-assignment gate can return reaches an MCP caller with its own code,
// through the one classification every transport renders.
func TestRoleAssignmentRefusals_Classify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want wire.Code
	}{
		{name: "role not assignable", err: &role.NotAssignableError{RoleID: "r1", TenantID: "acme"}, want: wire.CodeInvalidArgument},
		{name: "unknown role", err: &role.ReferenceError{RoleID: "r1", Err: &role.RoleNotFoundError{ID: "r1"}}, want: wire.CodeInvalidArgument},
		{name: "no role service", err: &role.ServiceRequiredError{}, want: wire.CodeUnimplemented},
		{name: "unknown target user", err: &user.UserNotFoundError{ID: 9}, want: wire.CodeNotFound},
		{name: "unauthenticated", err: auth.ErrUnauthenticated, want: wire.CodeUnauthenticated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ServiceErrorCode(&ToolError{Tool: "qrdb_user_update", Err: tt.err}); got != tt.want {
				t.Errorf("code = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestUserTools_RoleAssignmentRefusals drives the create and update tools
// into the gate's refusals: a role the node cannot validate, and an update
// whose target user does not exist.
func TestUserTools_RoleAssignmentRefusals(t *testing.T) {
	missingUser := &mockUserService{getUserFn: func(context.Context, uint64) (*user.User, error) {
		return nil, &user.UserNotFoundError{ID: 9}
	}}

	t.Run("create names a role with no role service", func(t *testing.T) {
		_, err := handleUserCreate(&mockUserService{}, nil, nil, nil)(context.Background(),
			makeReq(map[string]any{"username": "alice", "role_ids": "r1"}))
		if got := ServiceErrorCode(err); got != wire.CodeUnimplemented {
			t.Errorf("code = %v (err %v), want Unimplemented", got, err)
		}
	})
	t.Run("update of a missing user", func(t *testing.T) {
		_, err := handleUserUpdate(missingUser, nil, nil, &mockRoleService{})(context.Background(),
			makeReq(map[string]any{"id": "9", "role_ids": "r1"}))
		var unf *user.UserNotFoundError
		if !errors.As(err, &unf) {
			t.Fatalf("handler error = %v, want *user.UserNotFoundError", err)
		}
	})
}
