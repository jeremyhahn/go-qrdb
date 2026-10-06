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

import (
	"testing"
)

// cleanupRoleGrant removes a probe permission from the role-grant registry
// after a test so probes never leak into other tests' RolePermissions
// results.
func cleanupRoleGrant(t *testing.T, probes ...Permission) {
	t.Helper()
	t.Cleanup(func() {
		roleGrantMu.Lock()
		for _, p := range probes {
			delete(roleGrantRegistry, p)
		}
		roleGrantMu.Unlock()
		// A probe registered as a grant is also registered as a permission;
		// leave neither behind, or TestEveryPermissionDeclaresItsScope reads
		// the probe as an undeclared built-in in whatever test runs next.
		unregisterForTesting(probes...)
	})
}

// TestRegisterRoleGrant_SingleRole verifies a basic registration is visible
// through both RolePermissions and HasRoleGrant.
func TestRegisterRoleGrant_SingleRole(t *testing.T) {
	const probe Permission = "test:role-grant-single"
	cleanupRoleGrant(t, probe)

	RegisterRoleGrant(RoleOperator, probe)

	if !HasRoleGrant(RoleOperator, probe) {
		t.Errorf("HasRoleGrant(RoleOperator, %q) = false, want true", probe)
	}
	found := false
	for _, p := range RolePermissions(RoleOperator) {
		if p == probe {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("RolePermissions(RoleOperator) does not contain %q", probe)
	}
}

// TestRegisterRoleGrant_MultipleRoles verifies a single permission can be
// granted to more than one built-in role — the registry stores a set of
// roles per permission, not a single value.
func TestRegisterRoleGrant_MultipleRoles(t *testing.T) {
	const probe Permission = "test:role-grant-multi"
	cleanupRoleGrant(t, probe)

	RegisterRoleGrant(RoleOperator, probe)
	RegisterRoleGrant(RoleAuditor, probe)

	if !HasRoleGrant(RoleOperator, probe) {
		t.Errorf("HasRoleGrant(RoleOperator, %q) = false, want true", probe)
	}
	if !HasRoleGrant(RoleAuditor, probe) {
		t.Errorf("HasRoleGrant(RoleAuditor, %q) = false, want true", probe)
	}
	if HasRoleGrant(RoleUser, probe) {
		t.Errorf("HasRoleGrant(RoleUser, %q) = true, want false", probe)
	}
}

// TestRegisterRoleGrant_MultiplePerms verifies a single call applies to
// every permission supplied, mirroring RegisterScope's variadic shape.
func TestRegisterRoleGrant_MultiplePerms(t *testing.T) {
	probes := []Permission{
		"test:role-grant-batch-a",
		"test:role-grant-batch-b",
		"test:role-grant-batch-c",
	}
	cleanupRoleGrant(t, probes...)

	RegisterRoleGrant(RoleAdmin, probes...)
	for _, p := range probes {
		if !HasRoleGrant(RoleAdmin, p) {
			t.Errorf("HasRoleGrant(RoleAdmin, %q) = false, want true", p)
		}
	}
}

// TestRolePermissions_Sorted verifies the returned slice is sorted, matching
// the contract of All() and ByResource().
func TestRolePermissions_Sorted(t *testing.T) {
	probes := []Permission{"test:zzz-last", "test:aaa-first", "test:mmm-mid"}
	cleanupRoleGrant(t, probes...)

	RegisterRoleGrant(RoleUser, probes...)

	out := RolePermissions(RoleUser)
	for i := 1; i < len(out); i++ {
		if out[i-1] > out[i] {
			t.Fatalf("RolePermissions(RoleUser) not sorted: %q > %q", out[i-1], out[i])
		}
	}
}

// TestRolePermissions_UnknownRoleReturnsEmpty verifies a role name with no
// registered grants returns an empty (nil or zero-length) slice rather than
// panicking or returning every permission.
func TestRolePermissions_UnknownRoleReturnsEmpty(t *testing.T) {
	out := RolePermissions("no-such-built-in-role")
	if len(out) != 0 {
		t.Errorf("RolePermissions(unknown role) = %v, want empty", out)
	}
}

// TestHasRoleGrant_UnregisteredPermission verifies HasRoleGrant reports
// false (not a panic) for a permission that was never registered at all.
func TestHasRoleGrant_UnregisteredPermission(t *testing.T) {
	if HasRoleGrant(RoleAdmin, "test:never-registered") {
		t.Error("HasRoleGrant(RoleAdmin, never-registered) = true, want false")
	}
}

// TestRegisterRoleGrant_SODeniesCryptoOperations is the structural-
// enforcement proof for FIPS 140-3 AS10.03: attempting to grant a
// cryptographic permission to the Security Officer role panics with
// SoDViolationError, so the invariant cannot be violated by a future
// contributor — from this package or, via the sdk/go re-export, from a
// higher layer.
//
// The set deliberately reaches past the keys:* family that the guard used to
// be limited to: piv:use is hardware-token key use declared DutyCryptoUse,
// keys:derive is key agreement / KDF, and "attestation:attest" is the shape of
// a permission a higher layer contributes (go-quicbaas's, which extends a PCR
// on the host TPM and asks it for a signed quote) — caught by the
// resource-independent crypto-verb backstop without qrdb knowing the resource
// exists.
func TestRegisterRoleGrant_SODeniesCryptoOperations(t *testing.T) {
	cryptoOps := []Permission{
		KeysSign, KeysVerify, KeysEncrypt, KeysDecrypt, KeysWrap, KeysUnwrap,
		KeysDerive, PIVUse,
		"attestation:attest", "tpm:quote", "envelope:unwrap", "hkdf:derive",
	}
	for _, p := range cryptoOps {
		t.Run(string(p), func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("RegisterRoleGrant(RoleSO, %q) did not panic", p)
				}
				violation, ok := r.(*SoDViolationError)
				if !ok {
					t.Fatalf("RegisterRoleGrant(RoleSO, %q) panicked with %T (%v), want *SoDViolationError", p, r, r)
				}
				if violation.Role != RoleSO || violation.Permission != p {
					t.Errorf("SoDViolationError = %+v, want Role=%q Permission=%q", violation, RoleSO, p)
				}
				// The rejected grant must not have been recorded.
				if HasRoleGrant(RoleSO, p) {
					t.Errorf("HasRoleGrant(RoleSO, %q) = true after rejected registration, want false", p)
				}
			}()
			RegisterRoleGrant(RoleSO, p)
		})
	}
}

// TestRegisterRoleGrant_SODeniesUndeclaredPermission proves the fail-safe half
// of the guard: a permission whose author never declared a duty is NOT
// SO-grantable, so a new permission cannot become a Security Officer grant by
// omission. This is what keeps the guard honest for permissions the crypto-verb
// backstop cannot recognize — a future "hsm:passthrough" or
// "attestation:pcr_extend" is denied until someone classifies it.
func TestRegisterRoleGrant_SODeniesUndeclaredPermission(t *testing.T) {
	undeclared := []Permission{
		"test:so-undeclared-probe",
		KVPut,            // data plane: registered, never classified
		AuthFactorVerify, // login-path assertion check, deliberately unclassified
		ObjStoreVerify,   // ambiguous "verify" the backstop must not claim
	}
	for _, p := range undeclared {
		t.Run(string(p), func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("RegisterRoleGrant(RoleSO, %q) did not panic", p)
				}
				undeclaredErr, ok := r.(*UndeclaredDutyError)
				if !ok {
					t.Fatalf("RegisterRoleGrant(RoleSO, %q) panicked with %T (%v), want *UndeclaredDutyError", p, r, r)
				}
				if undeclaredErr.Role != RoleSO || undeclaredErr.Permission != p {
					t.Errorf("UndeclaredDutyError = %+v, want Role=%q Permission=%q", undeclaredErr, RoleSO, p)
				}
				if HasRoleGrant(RoleSO, p) {
					t.Errorf("HasRoleGrant(RoleSO, %q) = true after rejected registration, want false", p)
				}
			}()
			RegisterRoleGrant(RoleSO, p)
		})
	}
}

// TestRegisterRoleGrant_SODeniesWildcard verifies the Security Officer cannot
// be granted a whole-segment wildcard. A wildcard spans verbs of different
// duties, so it is unclassifiable by construction, and the RBAC authorizer
// expands "keys:*" onto the concrete keys:sign check — a wildcard grant would
// be a crypto grant in disguise. Other roles may hold wildcards (Admin holds
// several), so the rejection is specific to RoleSO.
func TestRegisterRoleGrant_SODeniesWildcard(t *testing.T) {
	for _, p := range []Permission{"keys:*", "*:sign", "*:*"} {
		t.Run(string(p), func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("RegisterRoleGrant(RoleSO, %q) did not panic", p)
				}
				wildcardErr, ok := r.(*WildcardGrantError)
				if !ok {
					t.Fatalf("RegisterRoleGrant(RoleSO, %q) panicked with %T (%v), want *WildcardGrantError", p, r, r)
				}
				if wildcardErr.Role != RoleSO || wildcardErr.Permission != p {
					t.Errorf("WildcardGrantError = %+v, want Role=%q Permission=%q", wildcardErr, RoleSO, p)
				}
				if HasRoleGrant(RoleSO, p) {
					t.Errorf("HasRoleGrant(RoleSO, %q) = true after rejected registration, want false", p)
				}
			}()
			RegisterRoleGrant(RoleSO, p)
		})
	}
}

// TestRegisterRoleGrant_SOAllowsDeclaredGovernance verifies the guard admits
// exactly what it should: a permission its author declared DutyGovernance
// registers onto the Security Officer role without complaint.
func TestRegisterRoleGrant_SOAllowsDeclaredGovernance(t *testing.T) {
	const probe Permission = "test:so-governance-probe"
	cleanupRoleGrant(t, probe)
	cleanupDuty(t, probe)

	RegisterDuty(DutyGovernance, probe)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RegisterRoleGrant(RoleSO, declared-governance probe) panicked: %v", r)
		}
	}()
	RegisterRoleGrant(RoleSO, probe)
	if !HasRoleGrant(RoleSO, probe) {
		t.Errorf("HasRoleGrant(RoleSO, %q) = false, want true", probe)
	}
}

// TestRegisterRoleGrant_SOAllowsKeyLifecycleOperations verifies the SoD check
// separates key USE from key LIFECYCLE: the Security Officer's
// create/delete/list/get governance grants are unaffected. These are already
// registered on RoleSO by builtin_role_grants.go; this test asserts no panic
// occurs and the grants are present.
func TestRegisterRoleGrant_SOAllowsKeyLifecycleOperations(t *testing.T) {
	probes := []Permission{KeysCreate, KeysDelete, KeysList, KeysGet, KeysImport, KeysExport}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RegisterRoleGrant(RoleSO, key-lifecycle verbs) panicked: %v", r)
		}
	}()
	RegisterRoleGrant(RoleSO, probes...)
	for _, p := range probes {
		if !HasRoleGrant(RoleSO, p) {
			t.Errorf("HasRoleGrant(RoleSO, %q) = false, want true", p)
		}
	}
}

// TestRegisterRoleGrant_SOAllowsModuleLifecycleCrypto pins the deliberate
// boundary of the invariant: barrier and ceremony operations are cryptographic
// by mechanism (they reconstruct the root key, they sign a CSR) but are the
// Security Officer's own FIPS-assigned module-lifecycle duty, so they are
// declared DutyGovernance and must keep registering. A verb-sniffing classifier
// that denied these would have broken the module's bootstrap path.
func TestRegisterRoleGrant_SOAllowsModuleLifecycleCrypto(t *testing.T) {
	probes := []Permission{
		SealBootstrap, SealUnseal, SealSeal, SealRotateRootKey, SealRekey,
		SealGenerateRecovery, CeremonySignCSR, CertsIssue,
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RegisterRoleGrant(RoleSO, module-lifecycle verbs) panicked: %v", r)
		}
	}()
	RegisterRoleGrant(RoleSO, probes...)
	for _, p := range probes {
		if !HasRoleGrant(RoleSO, p) {
			t.Errorf("HasRoleGrant(RoleSO, %q) = false, want true", p)
		}
	}
}

// TestRegisterRoleGrant_NonSORoleAllowsCryptoOps verifies the SoD check is
// specific to RoleSO: any other built-in role may hold crypto permissions
// (Operator and User already do, via builtin_role_grants.go), and may hold
// undeclared ones — the duty declaration gates the Security Officer, not the
// vocabulary.
func TestRegisterRoleGrant_NonSORoleAllowsCryptoOps(t *testing.T) {
	const undeclared Permission = "test:non-so-undeclared-probe"
	cleanupRoleGrant(t, undeclared)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RegisterRoleGrant(RoleOperator, ...) panicked: %v", r)
		}
	}()
	RegisterRoleGrant(RoleOperator, KeysSign, PIVUse, undeclared)
	for _, p := range []Permission{KeysSign, PIVUse, undeclared} {
		if !HasRoleGrant(RoleOperator, p) {
			t.Errorf("HasRoleGrant(RoleOperator, %q) = false, want true", p)
		}
	}
}

// TestRegisterRoleGrant_LateRegistrationVisibleImmediately proves the
// ordering-hazard fix: RolePermissions is evaluated against the live
// registry on every call rather than a value memoized at package-init
// time. A permission registered well after every package init() has run —
// this test itself executes after all init()s, simulating a higher-layer
// package (go-quicbaas, go-xkms) registering a contributed permission at
// server-bootstrap time, before role seeding — is reflected the very next
// time RolePermissions is called.
func TestRegisterRoleGrant_LateRegistrationVisibleImmediately(t *testing.T) {
	const late Permission = "test:late-registration"
	cleanupRoleGrant(t, late)

	before := RolePermissions(RoleAdmin)
	for _, p := range before {
		if p == late {
			t.Fatalf("precondition failed: %q already present in RolePermissions(RoleAdmin)", late)
		}
	}

	Register(late)
	RegisterRoleGrant(RoleAdmin, late)

	after := RolePermissions(RoleAdmin)
	found := false
	for _, p := range after {
		if p == late {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("RolePermissions(RoleAdmin) after late registration = %v, missing %q", after, late)
	}
}

// TestResetRoleGrantRegistryForTesting_ClearsAll exercises the test helper
// and restores the registry afterward so later tests in this package are
// unaffected.
func TestResetRoleGrantRegistryForTesting_ClearsAll(t *testing.T) {
	roleGrantMu.RLock()
	snapshot := make(map[Permission]map[string]struct{}, len(roleGrantRegistry))
	for p, roles := range roleGrantRegistry {
		rolesCopy := make(map[string]struct{}, len(roles))
		for r := range roles {
			rolesCopy[r] = struct{}{}
		}
		snapshot[p] = rolesCopy
	}
	roleGrantMu.RUnlock()

	t.Cleanup(func() {
		roleGrantMu.Lock()
		roleGrantRegistry = snapshot
		roleGrantMu.Unlock()
	})

	resetRoleGrantRegistryForTesting()

	if got := RolePermissions(RoleSO); len(got) != 0 {
		t.Errorf("after reset: RolePermissions(RoleSO) = %v, want empty", got)
	}
}
