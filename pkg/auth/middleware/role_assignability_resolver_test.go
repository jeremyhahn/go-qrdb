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

package middleware

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestRoleAssignment_OnlyRolesTheRealResolverCanResolve drives the gate's
// validation against the REAL built-in role store and the REAL RBAC
// authorizer. A role written onto a tenant-A user must be one RBAC resolves in
// tenant A: every tenant seeds its own built-ins, and the store resolves a
// role id only in its own tenant, so the system tenant's copy of a built-in is
// unresolvable there. Accepting it would lock the user out -- every later
// decision fails as unavailable -- so the write must be refused.
func TestRoleAssignment_OnlyRolesTheRealResolverCanResolve(t *testing.T) {
	roles := authtest.BindingRoles(t)
	acmeUser := &userTenantFixture{u: &user.User{ID: authtest.UserInBindingTenant, TenantID: authtest.BindingTenant}}
	var mw *AuthMiddleware

	tests := []struct {
		name   string
		roleID string
		ok     bool
	}{
		{name: "the tenant's own built-in", roleID: role.SystemRoleID(authtest.BindingTenant, role.RoleNameAdmin), ok: true},
		{name: "the system tenant's built-in", roleID: role.SystemRoleID(tenantid.System, role.RoleNameAdmin), ok: false},
		{name: "another tenant's built-in", roleID: role.SystemRoleID(authtest.OtherTenant, role.RoleNameAdmin), ok: false},
	}
	authz := rbac.New(roles)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// What RBAC makes of the role once it is on a tenant-A identity.
			holder := &auth.Identity{Subject: "alice", TenantID: authtest.BindingTenant, Roles: []string{tt.roleID}}
			resolveErr := authz.Authorize(tenant.WithTenantID(context.Background(), authtest.BindingTenant), holder, "users", "read")
			var unresolvable *rbac.ResolveError
			if tt.ok == errors.As(resolveErr, &unresolvable) {
				t.Fatalf("RBAC on a holder of %s = %v; the fixture's premise does not hold", tt.roleID, resolveErr)
			}

			err := mw.AuthorizeRoleAssignmentWrite(context.Background(), acmeUser, roles, authtest.UserInBindingTenant, []string{tt.roleID}, nil)
			if tt.ok && err != nil {
				t.Fatalf("assigning %s = %v, want it accepted", tt.roleID, err)
			}
			var notAssignable *role.NotAssignableError
			if !tt.ok && !errors.As(err, &notAssignable) {
				t.Fatalf("assigning %s = %v, want *role.NotAssignableError", tt.roleID, err)
			}
		})
	}
}
