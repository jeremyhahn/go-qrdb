// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package middleware

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestOperatorRoleAssignment_AdmittedOperatorAssignsAndUnassigns: under real
// RBAC, a system operator admitted to acme for roles:write:tenant assigns and
// unassigns a role on an acme user; the role's validation and the SO-role
// restriction still apply, and without the admission the same calls are
// refused because roles:assign/unassign are ScopeSelf.
func TestOperatorRoleAssignment_AdmittedOperatorAssignsAndUnassigns(t *testing.T) {
	f := newSORoleFixture(t)
	m := builtInMiddleware(t, withOperatorTrails(&mockAuditLogger{}),
		WithRoleResolver(f.roles), WithStrictSeparation(true),
		WithIsolationPolicyReader(policyFixture(tenant.ClusterIsolationPolicyOperatorAccess, tenant.TenantIsolationPolicyInherit)))

	for _, perm := range []permission.Permission{permission.RolesWriteTenant, permission.UsersWriteTenant} {
		admitted, err := m.AuthorizeOperatorIdentityAccess(operatorCtx(), "acme", perm)
		if err != nil {
			t.Fatalf("%s: admission = %v", perm, err)
		}
		bare := &userTenantFixture{u: &user.User{ID: 7, TenantID: "acme"}}
		if err := m.AuthorizeRoleAssignment(admitted, bare, f.roles, 7, f.userRole); err != nil {
			t.Fatalf("%s: admitted operator assign = %v, want nil", perm, err)
		}
		holder := &userTenantFixture{u: &user.User{ID: 7, TenantID: "acme", RoleIDs: []string{f.userRole}}}
		if err := m.AuthorizeRoleUnassignment(admitted, holder, 7, f.userRole); err != nil {
			t.Fatalf("%s: admitted operator unassign = %v, want nil", perm, err)
		}
		var soRequired *auth.SORoleRequiredError
		if err := m.AuthorizeRoleAssignment(admitted, bare, f.roles, 7, f.so); !errors.As(err, &soRequired) {
			t.Fatalf("%s: admitted operator assigning the SO role = %v, want *SORoleRequiredError", perm, err)
		}
	}

	bare := &userTenantFixture{u: &user.User{ID: 7, TenantID: "acme"}}
	if err := m.AuthorizeRoleAssignment(operatorCtx(), bare, f.roles, 7, f.userRole); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("unadmitted operator assign = %v, want an authorization denial", err)
	}
	readOnly, err := m.AuthorizeOperatorIdentityAccess(operatorCtx(), "acme", permission.RolesReadTenant)
	if err != nil {
		t.Fatalf("read admission = %v", err)
	}
	if err := m.AuthorizeRoleAssignment(readOnly, bare, f.roles, 7, f.userRole); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("read-admitted operator assign = %v, want an authorization denial", err)
	}
}
