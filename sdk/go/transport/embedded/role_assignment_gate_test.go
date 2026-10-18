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

package embedded

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestAssignRole_GoesThroughTheRoleAssignmentGate asserts the embedded
// AssignRole validates the role as every other role-assignment write does,
// with or without an AuthMiddleware: another tenant's role is refused.
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
			tr := newAuthTransport(t, WithUserService(users), WithRoleService(authtest.BindingRoles(t)))
			err := tr.AssignRole(context.Background(), authtest.UserInBindingTenant, tt.roleID)
			var notAssignable *role.NotAssignableError
			if tt.ok != (err == nil) || (!tt.ok && !errors.As(err, &notAssignable)) {
				t.Fatalf("AssignRole = %v, want ok=%v (else *role.NotAssignableError)", err, tt.ok)
			}
			if assigned := slices.Contains(users.User(authtest.UserInBindingTenant).RoleIDs, tt.roleID); assigned != tt.ok {
				t.Fatalf("assigned = %v, want %v", assigned, tt.ok)
			}
		})
	}
}

// TestRoleMembershipWrites_AuthorizeAgainstTheUsersTenant asserts that with
// an AuthMiddleware installed, AssignRole and UnassignRole are authorized in
// the TARGET user's tenant: acme's administrator may change an acme user's
// roles and is refused a globex user's.
func TestRoleMembershipWrites_AuthorizeAgainstTheUsersTenant(t *testing.T) {
	tests := []struct {
		name   string
		userID uint64
		ok     bool
	}{
		{name: "user in the bound tenant", userID: authtest.UserInBindingTenant, ok: true},
		{name: "user in another tenant", userID: authtest.UserInOtherTenant, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := authtest.NewBindingUsers()
			roles := authtest.BindingRoles(t)
			mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles), authmw.WithUserService(users),
				authmw.WithAuthenticator(authtest.NewFixedAuthenticator(authtest.BindingTenantAdmin())))
			ctx, _, err := mw.Authenticate(context.Background())
			if err != nil {
				t.Fatalf("Authenticate: %v", err)
			}
			tr := newAuthTransport(t, WithUserService(users), WithRoleService(roles), WithAuthMiddleware(mw))
			tenantRole := role.SystemRoleID(users.User(tt.userID).TenantID, role.RoleNameUser)

			for verb, call := range map[string]func() error{
				"assign":   func() error { return tr.AssignRole(ctx, tt.userID, tenantRole) },
				"unassign": func() error { return tr.UnassignRole(ctx, tt.userID, tenantRole) },
			} {
				err := call()
				if tt.ok && err != nil {
					t.Fatalf("%s = %v, want allowed", verb, err)
				}
				// A user in another tenant is not found by the routed user
				// service (tenant.OwningTenant confines the by-id lookup to
				// the caller's own tenant), not refused by the entry check:
				// acme's administrator holds roles:assign in acme, and
				// nothing is left to deny it against an id the lookup
				// resolves within the caller's own tenant.
				if !tt.ok && !errors.As(err, new(*user.UserNotFoundError)) {
					t.Fatalf("%s = %v, want *user.UserNotFoundError", verb, err)
				}
			}
		})
	}
}
