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
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestRoleAssignmentRefusals_Classify asserts each refusal the role-assignment gate
// can return reaches the caller with its own code: a bad role reference is
// the caller's input, a missing target user is NotFound, a missing role
// service is a composition fault, and anything else is an authorization
// decision.
func TestRoleAssignmentRefusals_Classify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{name: "role not assignable", err: &role.NotAssignableError{RoleID: "r1", TenantID: "acme"}, want: codes.InvalidArgument},
		{name: "unknown role", err: &role.ReferenceError{RoleID: "r1", Err: &role.RoleNotFoundError{ID: "r1"}}, want: codes.InvalidArgument},
		{name: "target user not found", err: &user.UserNotFoundError{ID: 7}, want: codes.NotFound},
		{name: "no role service", err: &role.ServiceRequiredError{}, want: codes.Unimplemented},
		{name: "unauthenticated", err: auth.ErrUnauthenticated, want: codes.Unauthenticated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := status.Code(mapServiceError(tt.err)); got != tt.want {
				t.Errorf("mapServiceError() code = %v, want %v", got, tt.want)
			}
		})
	}
}
