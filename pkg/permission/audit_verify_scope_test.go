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

// The scope audit:verify resolves to.
//
// pkg/audit.Verify walks the node's WHOLE audit trail as one hash chain --
// there is no per-tenant chain to filter it down to -- so ScopeSelf would let
// a tenant-local SO or Auditor trigger a full-trail scan and receive record
// counts, file paths, and chain digests belonging to every other tenant. This
// test is where that statement is legible: if it ever resolves to ScopeSelf
// again, this is the assertion that goes red.

package permission

import "testing"

// TestAuditVerify_IsCrossScope is the load-bearing registration. It was
// ScopeSelf until a review of go-qrdb#559's neighboring commits found that a
// tenant-local grant reached the whole trail, not the granting tenant's slice
// of it -- there is no such slice.
func TestAuditVerify_IsCrossScope(t *testing.T) {
	if got := mustScope(t, AuditVerify); got != ScopeCross {
		t.Fatalf("LookupScope(%s) = %v, want ScopeCross", AuditVerify, got)
	}
	if !IsCrossScope(AuditVerify) {
		t.Error("IsCrossScope = false for audit:verify")
	}
}

// TestAuditVerify_IsRegisteredAndGranted keeps the permission reachable:
// registered in the global registry (so LookupScope and the RBAC authorizer
// see it at all) and granted to at least one built-in role (so some identity
// can hold it -- restricted, by the scope above, to the system tenant).
func TestAuditVerify_IsRegisteredAndGranted(t *testing.T) {
	found := false
	for _, p := range All() {
		if p == AuditVerify {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("%s is not in the permission registry", AuditVerify)
	}
	if !HasRoleGrant(RoleAuditor, AuditVerify) {
		t.Errorf("%s is granted to no built-in role, so no identity can exercise it", AuditVerify)
	}
}
