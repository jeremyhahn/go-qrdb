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

package permission

import "testing"

// tenantAdminRoleWritePermissions are the role-management writes a tenant
// Admin performs in its OWN tenant: seeding and reconciling the tenant's
// custom roles.
var tenantAdminRoleWritePermissions = []Permission{RolesCreate, RolesUpdate}

func TestRoleWrites_AdminHoldsGrant(t *testing.T) {
	held := make(map[Permission]bool)
	for _, p := range RolePermissions(RoleAdmin) {
		held[p] = true
	}
	for _, p := range tenantAdminRoleWritePermissions {
		if !HasRoleGrant(RoleAdmin, p) {
			t.Errorf("HasRoleGrant(%q, %q): got false, want true", RoleAdmin, p)
		}
		if !held[p] {
			t.Errorf("RolePermissions(%q) lacks %q", RoleAdmin, p)
		}
	}
}

// TestRoleWrites_AdminDoesNotHoldDelete pins the line the grant stops at:
// deleting a role stays with the Security Officer.
func TestRoleWrites_AdminDoesNotHoldDelete(t *testing.T) {
	if HasRoleGrant(RoleAdmin, RolesDelete) {
		t.Errorf("HasRoleGrant(%q, %q): got true, want false", RoleAdmin, RolesDelete)
	}
	if !HasRoleGrant(RoleSO, RolesDelete) {
		t.Errorf("HasRoleGrant(%q, %q): got false, want true", RoleSO, RolesDelete)
	}
}

// TestRoleWrites_AreSelfScoped pins the scope that keeps a tenant Admin's
// role writes in its own tenant. ScopeCross would make the grant inert for
// every tenant Admin; an undeclared scope would fail open.
func TestRoleWrites_AreSelfScoped(t *testing.T) {
	for _, p := range tenantAdminRoleWritePermissions {
		got, err := LookupScope(p)
		if err != nil {
			t.Fatalf("LookupScope(%q): %v", p, err)
		}
		if got != ScopeSelf {
			t.Errorf("LookupScope(%q) = %v, want ScopeSelf", p, got)
		}
	}
}
