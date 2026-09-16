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

// escrowVocabulary pins the literal wire strings of the key escrow family.
// go-xkms gates its KMIP and federation-agent escrow transports on exactly
// these pairs, so a retyped constant here silently changes who may reach
// escrowed key material.
var escrowVocabulary = []struct {
	perm     Permission
	resource string
	action   string
}{
	{EscrowCreate, "escrow", "create"},
	{EscrowRecover, "escrow", "recover"},
	{EscrowList, "escrow", "list"},
	{EscrowRevoke, "escrow", "revoke"},
}

// TestEscrowPermissions_Vocabulary asserts each escrow permission carries the
// exact wire string, splits into the expected resource and action, and is
// present in the global registry (so a role may actually grant it).
func TestEscrowPermissions_Vocabulary(t *testing.T) {
	for _, tc := range escrowVocabulary {
		want := tc.resource + ":" + tc.action
		if string(tc.perm) != want {
			t.Errorf("permission string: got %q, want %q", tc.perm, want)
		}
		if got := Resource(tc.perm); got != tc.resource {
			t.Errorf("Resource(%q): got %q, want %q", tc.perm, got, tc.resource)
		}
		if got := Action(tc.perm); got != tc.action {
			t.Errorf("Action(%q): got %q, want %q", tc.perm, got, tc.action)
		}
		if !IsRegistered(tc.perm) {
			t.Errorf("IsRegistered(%q): got false, want true", tc.perm)
		}
	}
}

// TestEscrowPermissions_ByResource proves the family is reachable as a whole
// and that no permission outside the four declared verbs claims the escrow
// resource.
func TestEscrowPermissions_ByResource(t *testing.T) {
	got := ByResource("escrow")
	if len(got) != len(escrowVocabulary) {
		t.Fatalf("ByResource(\"escrow\"): got %d permissions %v, want %d", len(got), got, len(escrowVocabulary))
	}

	declared := make(map[Permission]struct{}, len(escrowVocabulary))
	for _, tc := range escrowVocabulary {
		declared[tc.perm] = struct{}{}
	}
	for _, p := range got {
		if _, ok := declared[p]; !ok {
			t.Errorf("ByResource(\"escrow\") returned undeclared permission %q", p)
		}
	}
}

// TestEscrowPermissions_UnknownActionNotRegistered is the negative case: an
// action that was never declared must not resolve, so a typo in a role grant
// is rejected by validatePermissions rather than silently accepted.
func TestEscrowPermissions_UnknownActionNotRegistered(t *testing.T) {
	for _, p := range []Permission{"escrow:read", "escrow:sign", "escrow:", "escrow"} {
		if IsRegistered(p) {
			t.Errorf("IsRegistered(%q): got true, want false", p)
		}
	}
}

// TestEscrowPermissions_Scope requires every escrow permission to be
// ScopeSelf: escrow acts on the caller's own tenant's keys, lifted across
// tenants for the system tenant, exactly like keys:import/keys:export.
func TestEscrowPermissions_Scope(t *testing.T) {
	for _, tc := range escrowVocabulary {
		got, err := LookupScope(tc.perm)
		if err != nil {
			t.Errorf("LookupScope(%q): unexpected error %v", tc.perm, err)
			continue
		}
		if got != ScopeSelf {
			t.Errorf("LookupScope(%q) = %s, want self", tc.perm, got)
		}
	}
}

// TestEscrowPermissions_DutyGovernanceNotCryptoUse requires every escrow
// permission to be classified DutyGovernance and never DutyCryptoUse. Escrow
// moves wrapped key material to and from an external custodian — key entry
// and key output, the same duty as keys:import/keys:export — and performs no
// cryptographic primitive with it, so FIPS 140-3 AS10.03 does not bar the
// Security Officer from holding it.
func TestEscrowPermissions_DutyGovernanceNotCryptoUse(t *testing.T) {
	for _, tc := range escrowVocabulary {
		if IsCryptoUse(tc.perm) {
			t.Errorf("IsCryptoUse(%q) = true, want false", tc.perm)
		}
		if got := LookupDuty(tc.perm); got != DutyGovernance {
			t.Errorf("LookupDuty(%q) = %s, want governance", tc.perm, got)
		}
	}
}

// TestEscrowPermissions_RoleGrants pins which built-in roles hold which
// escrow verb. SO and Admin hold the full lifecycle (parallels their
// keys:import/keys:export grants); Auditor holds only the metadata-only
// escrow:list; Operator and User hold none, matching the key-management
// privileges they are also denied.
func TestEscrowPermissions_RoleGrants(t *testing.T) {
	full := []Permission{EscrowCreate, EscrowRecover, EscrowList, EscrowRevoke}

	for _, role := range []string{RoleSO, RoleAdmin} {
		granted := RolePermissions(role)
		set := make(map[Permission]struct{}, len(granted))
		for _, p := range granted {
			set[p] = struct{}{}
		}
		for _, p := range full {
			if _, ok := set[p]; !ok {
				t.Errorf("RolePermissions(%s) missing %q", role, p)
			}
		}
	}

	auditorSet := make(map[Permission]struct{})
	for _, p := range RolePermissions(RoleAuditor) {
		auditorSet[p] = struct{}{}
	}
	if _, ok := auditorSet[EscrowList]; !ok {
		t.Errorf("RolePermissions(RoleAuditor) missing %q", EscrowList)
	}
	for _, p := range []Permission{EscrowCreate, EscrowRecover, EscrowRevoke} {
		if _, ok := auditorSet[p]; ok {
			t.Errorf("RolePermissions(RoleAuditor) unexpectedly holds %q", p)
		}
	}

	for _, role := range []string{RoleOperator, RoleUser} {
		granted := RolePermissions(role)
		set := make(map[Permission]struct{}, len(granted))
		for _, p := range granted {
			set[p] = struct{}{}
		}
		for _, p := range full {
			if _, ok := set[p]; ok {
				t.Errorf("RolePermissions(%s) unexpectedly holds %q", role, p)
			}
		}
	}
}
