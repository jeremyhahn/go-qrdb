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

// audit:delete names the audit-delete verb. The SO and Admin hold it; the
// Auditor, which reviews the trail, does not (FIPS 140-3 role separation).

package permission

import "testing"

func TestAuditDelete_Value(t *testing.T) {
	if AuditDelete != "audit:delete" {
		t.Fatalf("AuditDelete = %q, want %q", AuditDelete, "audit:delete")
	}
}

func TestAuditDelete_IsRegistered(t *testing.T) {
	found := false
	for _, p := range All() {
		if p == AuditDelete {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("%s is not in the permission registry", AuditDelete)
	}
}

func TestAuditDelete_IsSelfScopeGovernance(t *testing.T) {
	if got := mustScope(t, AuditDelete); got != ScopeSelf {
		t.Errorf("LookupScope(%s) = %v, want ScopeSelf", AuditDelete, got)
	}
	if got := LookupDuty(AuditDelete); got != DutyGovernance {
		t.Errorf("LookupDuty(%s) = %v, want DutyGovernance", AuditDelete, got)
	}
}

func TestAuditDelete_GrantedToSOAndAdminOnly(t *testing.T) {
	want := map[string]bool{
		RoleSO:       true,
		RoleAdmin:    true,
		RoleOperator: false,
		RoleUser:     false,
		RoleAuditor:  false,
	}
	for role, granted := range want {
		if got := HasRoleGrant(role, AuditDelete); got != granted {
			t.Errorf("HasRoleGrant(%s, %s) = %v, want %v", role, AuditDelete, got, granted)
		}
	}
}
