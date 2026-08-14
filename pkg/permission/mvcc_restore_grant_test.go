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
// mvcc:restore placement
//
// Selective point-in-time restore is a privileged BULK MUTATION reachable
// through a data-plane verb, so where it sits in the built-in roles is a
// security property, not a convenience. These tests pin both directions: the
// roles that must hold it, and — more importantly — the roles that must not.
// ---------------------------------------------------------------------------

func TestMVCCRestore_IsRegistered(t *testing.T) {
	if !IsRegistered(MVCCRestore) {
		t.Fatal("mvcc:restore is not in the permission registry")
	}
	if MVCCRestore != "mvcc:restore" {
		t.Fatalf("MVCCRestore = %q, want mvcc:restore", MVCCRestore)
	}
	if Resource(MVCCRestore) != "mvcc" || Action(MVCCRestore) != "restore" {
		t.Fatalf("resource/action = %q/%q, want mvcc/restore",
			Resource(MVCCRestore), Action(MVCCRestore))
	}
}

func TestMVCCRestore_GrantedToAdminAndOperator(t *testing.T) {
	for _, role := range []string{RoleAdmin, RoleOperator} {
		if !HasRoleGrant(role, MVCCRestore) {
			t.Errorf("role %q does not hold mvcc:restore", role)
		}
	}
}

// TestMVCCRestore_NotGrantedToUnprivilegedRoles is the guard that matters. A
// restore rewrites live keys from history: granting it to User would let an
// ordinary consumer undo another party's writes across a prefix, and granting
// it to Auditor would end that role's independence (FIPS 140-3 AS10.10
// requires auditors to be unable to modify state).
func TestMVCCRestore_NotGrantedToUnprivilegedRoles(t *testing.T) {
	for _, role := range []string{RoleUser, RoleAuditor} {
		if HasRoleGrant(role, MVCCRestore) {
			t.Errorf("role %q must not hold mvcc:restore", role)
		}
	}
}

// TestMVCCRestore_NotGrantedToSecurityOfficer pins the separation-of-duties
// half: the Security Officer holds governance verbs only and no data-plane
// verb at all, so a restore must be unreachable to it however the role is
// materialized.
func TestMVCCRestore_NotGrantedToSecurityOfficer(t *testing.T) {
	if HasRoleGrant(RoleSO, MVCCRestore) {
		t.Fatal("the Security Officer must hold no data-plane verb, including mvcc:restore")
	}
	for _, p := range RolePermissions(RoleSO) {
		if p == MVCCRestore {
			t.Fatal("mvcc:restore appears in the materialized Security Officer role")
		}
	}
}

// TestMVCCRestore_IsUndeclaredDuty documents that mvcc:restore is deliberately
// left undeclared. Duty declaration exists to decide Security Officer
// grantability, and undeclared is the fail-safe state meaning "not Security
// Officer business" — the correct classification for the whole tenant data
// plane. Declaring it DutyGovernance would make it SO-grantable, which is the
// outcome the previous test forbids.
func TestMVCCRestore_IsUndeclaredDuty(t *testing.T) {
	if got := LookupDuty(MVCCRestore); got != DutyUndeclared {
		t.Fatalf("LookupDuty(mvcc:restore) = %v, want %v", got, DutyUndeclared)
	}
	if IsCryptoUse(MVCCRestore) {
		t.Fatal("mvcc:restore performs no cryptographic primitive")
	}
}

// TestMVCCRestore_IsSelfScoped pins the tenancy reach. Unlike mvcc:compact —
// which acts on the store-global revision counter and is therefore ScopeCross,
// reserved for the system tenant — a restore names the keys it writes and is
// confined to the caller's namespace by server.namespacedMVCCService, so it
// keeps the default self scope and is usable by a tenant-local Admin.
func TestMVCCRestore_IsSelfScoped(t *testing.T) {
	if got := LookupScope(MVCCRestore); got != ScopeSelf {
		t.Fatalf("LookupScope(mvcc:restore) = %v, want %v", got, ScopeSelf)
	}
	if IsCrossScope(MVCCRestore) {
		t.Fatal("mvcc:restore must not be cross-scope: a restore is namespace-confined")
	}
}

// TestMVCCRestore_IsDistinctFromReadAndCompact keeps the verb from being
// conflated with the permissions it sits between: reading history
// (mvcc:get_at_revision) and discarding it (mvcc:compact) are different
// authorities from rewriting live keys out of it.
func TestMVCCRestore_IsDistinctFromReadAndCompact(t *testing.T) {
	if MVCCRestore == MVCCGetAtRevision || MVCCRestore == MVCCCompact {
		t.Fatal("mvcc:restore must be its own permission")
	}
	// A role holding the historical read must not thereby hold the restore.
	if HasRoleGrant(RoleUser, MVCCGetAtRevision) && HasRoleGrant(RoleUser, MVCCRestore) {
		t.Fatal("holding the historical read must not imply holding the restore")
	}
}

func TestMVCCRestore_AppearsInMVCCCategory(t *testing.T) {
	for _, cat := range Categories() {
		if cat.Name != "MVCC Service" {
			continue
		}
		for _, p := range cat.Permissions {
			if p == MVCCRestore {
				return
			}
		}
		t.Fatal("mvcc:restore is missing from the MVCC Service category")
	}
	t.Fatal("the MVCC Service category is missing")
}
