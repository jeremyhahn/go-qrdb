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

// ---------------------------------------------------------------------------
// vlog:* placement
//
// The built-in grants are the design's Authorization table
// (docs/databases/architecture.md, "Verifiable log"). Signing a head is a use
// of the owner's key, so vlog:sign_head follows keys:sign: an Operator grant,
// never an Admin one, and declared crypto use so the Security Officer cannot
// hold it either.
// ---------------------------------------------------------------------------

// allVlogPermissions lists the eight verifiable-log permissions.
func allVlogPermissions() []Permission {
	return []Permission{
		VlogCreate, VlogList, VlogAppend, VlogGet,
		VlogProof, VlogHead, VlogExpire, VlogSignHead,
	}
}

// vlogGrants is the design's Authorization table: role to the vlog:*
// permissions it holds. A role absent from the table holds none.
var vlogGrants = map[string][]Permission{
	RoleAdmin:    {VlogCreate, VlogExpire, VlogList, VlogAppend, VlogGet, VlogProof, VlogHead},
	RoleOperator: {VlogSignHead, VlogList, VlogAppend, VlogGet, VlogProof, VlogHead},
	RoleUser:     {VlogList, VlogAppend, VlogGet, VlogProof, VlogHead},
	RoleAuditor:  {VlogList, VlogGet, VlogProof, VlogHead},
}

func TestVlogPermissions_Strings(t *testing.T) {
	want := map[Permission]string{
		VlogCreate:   "vlog:create",
		VlogList:     "vlog:list",
		VlogAppend:   "vlog:append",
		VlogGet:      "vlog:get",
		VlogProof:    "vlog:proof",
		VlogHead:     "vlog:head",
		VlogExpire:   "vlog:expire",
		VlogSignHead: "vlog:sign_head",
	}
	for p, s := range want {
		if string(p) != s {
			t.Errorf("permission = %q, want %q", p, s)
		}
		if Resource(p) != "vlog" {
			t.Errorf("Resource(%q) = %q, want vlog", p, Resource(p))
		}
	}
}

func TestVlogPermissions_RegisteredAndSelfScoped(t *testing.T) {
	for _, p := range allVlogPermissions() {
		if !IsRegistered(p) {
			t.Errorf("%q is not in the permission registry", p)
		}
		if got := mustScope(t, p); got != ScopeSelf {
			t.Errorf("LookupScope(%q) = %v, want %v", p, got, ScopeSelf)
		}
		if IsCrossScope(p) {
			t.Errorf("%q must not be cross-scope: a log belongs to one tenant", p)
		}
	}
}

func TestVlogPermissions_BuiltInRoleGrants(t *testing.T) {
	for _, role := range []string{RoleSO, RoleAdmin, RoleOperator, RoleUser, RoleAuditor} {
		held := make(map[Permission]bool)
		for _, p := range vlogGrants[role] {
			held[p] = true
		}
		for _, p := range allVlogPermissions() {
			if got := HasRoleGrant(role, p); got != held[p] {
				t.Errorf("HasRoleGrant(%q, %q) = %v, want %v", role, p, got, held[p])
			}
		}
	}
}

func TestVlogSignHead_NeverAdminOrSecurityOfficer(t *testing.T) {
	for _, role := range []string{RoleAdmin, RoleSO} {
		if HasRoleGrant(role, VlogSignHead) {
			t.Errorf("role %q must not hold vlog:sign_head", role)
		}
		for _, p := range RolePermissions(role) {
			if p == VlogSignHead {
				t.Errorf("vlog:sign_head appears in the materialized %q role", role)
			}
		}
	}
	if !HasRoleGrant(RoleOperator, KeysSign) || !HasRoleGrant(RoleOperator, VlogSignHead) {
		t.Error("the Operator must hold vlog:sign_head beside keys:sign")
	}
}

func TestVlogAuditor_ReadOnly(t *testing.T) {
	for _, p := range []Permission{VlogCreate, VlogAppend, VlogExpire, VlogSignHead} {
		if HasRoleGrant(RoleAuditor, p) {
			t.Errorf("the Auditor must not hold %q", p)
		}
	}
}

func TestVlogSignHead_IsDeclaredCryptoUse(t *testing.T) {
	if got := LookupDuty(VlogSignHead); got != DutyCryptoUse {
		t.Fatalf("LookupDuty(vlog:sign_head) = %v, want %v", got, DutyCryptoUse)
	}
	if !IsCryptoUse(VlogSignHead) {
		t.Fatal("vlog:sign_head must be crypto use")
	}
	if LookupDuty(VlogSignHead) != LookupDuty(KeysSign) {
		t.Fatal("vlog:sign_head must carry the same duty as keys:sign")
	}
}

func TestVlogSignHead_SOGrantPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("RegisterRoleGrant(RoleSO, vlog:sign_head) did not panic")
		}
		violation, ok := r.(*SoDViolationError)
		if !ok {
			t.Fatalf("panicked with %T (%v), want *SoDViolationError", r, r)
		}
		if violation.Role != RoleSO || violation.Permission != VlogSignHead {
			t.Errorf("SoDViolationError = %+v", violation)
		}
		if HasRoleGrant(RoleSO, VlogSignHead) {
			t.Error("the rejected grant was recorded")
		}
	}()
	RegisterRoleGrant(RoleSO, VlogSignHead)
}

func TestVlogPermissions_OtherVerbsAreUndeclaredDuty(t *testing.T) {
	for _, p := range allVlogPermissions() {
		if p == VlogSignHead {
			continue
		}
		if got := LookupDuty(p); got != DutyUndeclared {
			t.Errorf("LookupDuty(%q) = %v, want %v", p, got, DutyUndeclared)
		}
		if IsCryptoUse(p) {
			t.Errorf("%q performs no cryptographic primitive", p)
		}
	}
}

func TestVlogPermissions_AppearInCategory(t *testing.T) {
	for _, cat := range Categories() {
		if cat.Name != "Verifiable Log Service" {
			continue
		}
		in := make(map[Permission]bool)
		for _, p := range cat.Permissions {
			in[p] = true
		}
		for _, p := range allVlogPermissions() {
			if !in[p] {
				t.Errorf("%q is missing from the Verifiable Log Service category", p)
			}
		}
		return
	}
	t.Fatal("the Verifiable Log Service category is missing")
}
