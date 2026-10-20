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

// tenantAdminBarrierPermissions are the tenant barrier operations a tenant
// Admin performs on its OWN tenant's barrier.
var tenantAdminBarrierPermissions = []Permission{
	TenantBarrierInit,
	TenantBarrierUnseal,
	TenantBarrierSeal,
	TenantBarrierStatus,
	TenantBarrierOfficerRegister,
	TenantBarrierOfficerList,
	TenantBarrierOfficerRevoke,
	TenantBarrierRekey,
}

// TestTenantBarrierStatus_AdminHoldsGrant is the regression for go-qrdb#812:
// Admin was granted init/unseal/seal but never status, so a tenant's first
// admin could read its barrier's status only through the sealed-window
// fallback. Once the barrier unsealed that fallback closed, RBAC became the
// only path, and every node refused the admin's status read.
func TestTenantBarrierStatus_AdminHoldsGrant(t *testing.T) {
	if !HasRoleGrant(RoleAdmin, TenantBarrierStatus) {
		t.Fatalf("HasRoleGrant(%q, %q): got false, want true", RoleAdmin, TenantBarrierStatus)
	}
	found := false
	for _, p := range RolePermissions(RoleAdmin) {
		if p == TenantBarrierStatus {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("RolePermissions(%q) lacks %q", RoleAdmin, TenantBarrierStatus)
	}
}

// TestTenantBarrierStatus_TenantOnlyScope pins the reach of the grant: holding
// it lets an Admin read its own tenant's barrier only, never another tenant's
// and never from the system tenant.
func TestTenantBarrierStatus_TenantOnlyScope(t *testing.T) {
	got, err := LookupScope(TenantBarrierStatus)
	if err != nil {
		t.Fatalf("LookupScope(%q): %v", TenantBarrierStatus, err)
	}
	if got != ScopeTenantOnly {
		t.Errorf("LookupScope(%q): got %s, want %s", TenantBarrierStatus, got, ScopeTenantOnly)
	}
	if IsCrossScope(TenantBarrierStatus) {
		t.Errorf("IsCrossScope(%q): got true, want false", TenantBarrierStatus)
	}
}

// TestTenantBarrierPermissions_BuiltInRoleGrants pins which built-in roles
// carry the tenant barrier operations: the tenant Admin holds every one, so
// it can read the state of a barrier it may init, unseal and seal; the
// Operator, User and Auditor roles hold none.
func TestTenantBarrierPermissions_BuiltInRoleGrants(t *testing.T) {
	for _, tc := range []struct {
		role string
		want bool
	}{
		{RoleAdmin, true},
		{RoleOperator, false},
		{RoleUser, false},
		{RoleAuditor, false},
	} {
		for _, p := range tenantAdminBarrierPermissions {
			if got := HasRoleGrant(tc.role, p); got != tc.want {
				t.Errorf("HasRoleGrant(%q, %q): got %t, want %t", tc.role, p, got, tc.want)
			}
		}
	}
}
