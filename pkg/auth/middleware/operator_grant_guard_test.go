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

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestOperatorGrantGuard_AdmittedOperatorCreatesAndAssignsCustomRole: a
// system operator admitted to write acme's roles creates a custom role there
// through the real grant-guarded role service and assigns it to an acme user.
// The grant rule still binds it -- a permission it does not hold in its own
// tenant is refused -- and so does the SO-role restriction; without the
// admission the create is refused.
func TestOperatorGrantGuard_AdmittedOperatorCreatesAndAssignsCustomRole(t *testing.T) {
	f := newSORoleFixture(t)
	m := builtInMiddleware(t, withOperatorTrails(&mockAuditLogger{}),
		WithRoleResolver(f.roles), WithStrictSeparation(true),
		WithIsolationPolicyReader(policyFixture(tenant.ClusterIsolationPolicyOperatorAccess, tenant.TenantIsolationPolicyInherit)))
	guarded := role.NewGrantGuard(f.roles, m.Authorizer())

	admitted, err := m.AuthorizeOperatorIdentityAccess(operatorCtx(), "acme", permission.RolesWriteTenant)
	if err != nil {
		t.Fatalf("admission = %v", err)
	}
	custom, err := guarded.CreateRole(admitted, &role.CreateRoleRequest{
		Name: "readers", TenantID: "acme", Permissions: []permission.Permission{permission.RolesAssign},
	})
	if err != nil {
		t.Fatalf("admitted operator CreateRole = %v, want nil", err)
	}
	if _, err := guarded.UpdateRole(admitted, &role.UpdateRoleRequest{
		ID: custom.ID, Permissions: []permission.Permission{permission.RolesAssign, permission.RolesUnassign},
	}); err != nil {
		t.Fatalf("admitted operator UpdateRole = %v, want nil", err)
	}

	var exceeds *role.GrantExceedsHolderError
	if _, err := guarded.CreateRole(admitted, &role.CreateRoleRequest{
		Name: "beyond", TenantID: "acme", Permissions: []permission.Permission{beyondOperator},
	}); !errors.As(err, &exceeds) {
		t.Fatalf("admitted operator granting %s = %v, want *GrantExceedsHolderError", beyondOperator, err)
	}

	bare := &userTenantFixture{u: &user.User{ID: 7, TenantID: "acme"}}
	if err := m.AuthorizeRoleAssignment(admitted, bare, f.roles, 7, custom.ID); err != nil {
		t.Fatalf("admitted operator assigning the custom role = %v, want nil", err)
	}
	var soRequired *SORoleRequiredError
	if err := m.AuthorizeRoleAssignment(admitted, bare, f.roles, 7, f.so); !errors.As(err, &soRequired) {
		t.Fatalf("admitted operator assigning the SO role = %v, want *SORoleRequiredError", err)
	}

	if _, err := guarded.CreateRole(operatorCtx(), &role.CreateRoleRequest{
		Name: "unadmitted", TenantID: "acme", Permissions: []permission.Permission{permission.RolesAssign},
	}); err == nil {
		t.Fatal("unadmitted operator CreateRole in acme succeeded, want a refusal")
	}
}

// beyondOperator is a permission the system Admin -- the operator -- does not
// hold: the Admin role manages identities and reads no data.
const beyondOperator = permission.KVGet
