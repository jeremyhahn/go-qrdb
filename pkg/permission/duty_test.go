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

// cleanupDuty removes probe duty declarations after a test so probes never
// leak into another test's LookupDuty / IsCryptoUse results. Duty declarations
// are immutable in the public API by design (see RegisterDuty), so tests reach
// into the registry directly rather than through an unregister function that
// production code must never have.
func cleanupDuty(t *testing.T, probes ...Permission) {
	t.Helper()
	t.Cleanup(func() {
		dutyMu.Lock()
		defer dutyMu.Unlock()
		for _, p := range probes {
			delete(dutyRegistry, p)
		}
	})
}

// TestDuty_StringLabels verifies every duty has a stable label and that an
// out-of-range value reports "unknown" rather than an index panic — the labels
// appear in registration-time panic messages, which are the only diagnosis a
// contributor gets.
func TestDuty_StringLabels(t *testing.T) {
	tests := []struct {
		duty Duty
		want string
	}{
		{DutyUndeclared, "undeclared"},
		{DutyGovernance, "governance"},
		{DutyCryptoUse, "crypto-use"},
		{Duty(99), "unknown"},
	}
	for _, tc := range tests {
		if got := tc.duty.String(); got != tc.want {
			t.Errorf("Duty(%d).String() = %q, want %q", tc.duty, got, tc.want)
		}
	}
}

// TestDuty_ValidRejectsUndeclaredAndUnknown verifies DutyUndeclared is not a
// declarable value: "undeclared" is expressed by omitting RegisterDuty, so
// accepting it as a declaration would blur the fail-safe default.
func TestDuty_ValidRejectsUndeclaredAndUnknown(t *testing.T) {
	valid := []Duty{DutyGovernance, DutyCryptoUse}
	for _, d := range valid {
		if !d.Valid() {
			t.Errorf("Duty(%s).Valid() = false, want true", d)
		}
	}
	for _, d := range []Duty{DutyUndeclared, Duty(-1), Duty(99)} {
		if d.Valid() {
			t.Errorf("Duty(%d).Valid() = true, want false", d)
		}
	}
}

// TestRegisterDuty_RoundTrip verifies a declaration is visible through
// LookupDuty for every permission in a variadic call.
func TestRegisterDuty_RoundTrip(t *testing.T) {
	probes := []Permission{"test:duty-a", "test:duty-b", "test:duty-c"}
	cleanupDuty(t, probes...)

	RegisterDuty(DutyGovernance, probes...)
	for _, p := range probes {
		if got := LookupDuty(p); got != DutyGovernance {
			t.Errorf("LookupDuty(%q) = %s, want governance", p, got)
		}
	}
}

// TestLookupDuty_UnregisteredIsUndeclared verifies the fail-safe default: a
// permission nobody classified reports DutyUndeclared rather than a permissive
// value.
func TestLookupDuty_UnregisteredIsUndeclared(t *testing.T) {
	if got := LookupDuty("test:duty-never-declared"); got != DutyUndeclared {
		t.Errorf("LookupDuty(undeclared) = %s, want undeclared", got)
	}
}

// TestRegisterDuty_RedeclaringSameDutyIsIdempotent verifies a repeated
// declaration of the SAME duty is accepted. Higher layers document their
// Register() entry points as idempotent and tests call them freely, so a second
// identical declaration must not panic.
func TestRegisterDuty_RedeclaringSameDutyIsIdempotent(t *testing.T) {
	const probe Permission = "test:duty-idempotent"
	cleanupDuty(t, probe)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("re-declaring the same duty panicked: %v", r)
		}
	}()
	RegisterDuty(DutyCryptoUse, probe)
	RegisterDuty(DutyCryptoUse, probe)
	if got := LookupDuty(probe); got != DutyCryptoUse {
		t.Errorf("LookupDuty(%q) = %s, want crypto-use", probe, got)
	}
}

// TestRegisterDuty_ConflictingRedeclarationPanics is the anti-laundering proof:
// once a permission is declared DutyCryptoUse, no later caller — including a
// higher layer through the sdk/go re-export — can relabel it as governance and
// hand it to the Security Officer.
func TestRegisterDuty_ConflictingRedeclarationPanics(t *testing.T) {
	const probe Permission = "test:duty-conflict"
	cleanupDuty(t, probe)

	RegisterDuty(DutyCryptoUse, probe)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("conflicting re-declaration did not panic")
		}
		conflict, ok := r.(*DutyConflictError)
		if !ok {
			t.Fatalf("panicked with %T (%v), want *DutyConflictError", r, r)
		}
		if conflict.Permission != probe || conflict.Existing != DutyCryptoUse || conflict.Requested != DutyGovernance {
			t.Errorf("DutyConflictError = %+v, want {%q crypto-use governance}", conflict, probe)
		}
		// The original declaration must survive the rejected attempt.
		if got := LookupDuty(probe); got != DutyCryptoUse {
			t.Errorf("LookupDuty(%q) = %s after rejected re-declaration, want crypto-use", probe, got)
		}
	}()
	RegisterDuty(DutyGovernance, probe)
}

// TestRegisterDuty_InvalidDutyPanics verifies RegisterDuty rejects values a
// caller may not declare, so a garbage duty surfaces loudly at init() instead
// of silently denying every later Security Officer grant of that permission.
func TestRegisterDuty_InvalidDutyPanics(t *testing.T) {
	for _, d := range []Duty{DutyUndeclared, Duty(99)} {
		t.Run(d.String(), func(t *testing.T) {
			const probe Permission = "test:duty-invalid"
			cleanupDuty(t, probe)
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("RegisterDuty(%s, ...) did not panic", d)
				}
				invalid, ok := r.(*InvalidDutyError)
				if !ok {
					t.Fatalf("panicked with %T (%v), want *InvalidDutyError", r, r)
				}
				if invalid.Duty != d {
					t.Errorf("InvalidDutyError.Duty = %s, want %s", invalid.Duty, d)
				}
				if got := LookupDuty(probe); got != DutyUndeclared {
					t.Errorf("LookupDuty(%q) = %s after rejected declaration, want undeclared", probe, got)
				}
			}()
			RegisterDuty(d, probe)
		})
	}
}

// TestIsCryptoUse_DeclaredCryptoUse verifies the declaration-driven half of the
// classifier, including the two qrdb permissions the resource-independent verb
// backstop cannot recognize: keys:verify ("verify" is ambiguous across
// families) and piv:use ("use" says nothing on its own).
func TestIsCryptoUse_DeclaredCryptoUse(t *testing.T) {
	for _, p := range []Permission{
		KeysSign, KeysVerify, KeysEncrypt, KeysDecrypt,
		KeysWrap, KeysUnwrap, KeysDerive, PIVUse,
	} {
		if !IsCryptoUse(p) {
			t.Errorf("IsCryptoUse(%q) = false, want true", p)
		}
	}
}

// TestIsCryptoUse_VerbBackstopSpansAllResources is the fix for the defect this
// classifier replaces: cryptographic use is not confined to the keys:* family.
// A permission contributed by a higher layer that qrdb has never heard of is
// classified from its action verb alone, so go-quicbaas's attestation:attest —
// a PCR extension plus a signed TPM quote — is caught here even though nothing
// in qrdb declares it.
func TestIsCryptoUse_VerbBackstopSpansAllResources(t *testing.T) {
	for _, p := range []Permission{
		"attestation:attest", "tpm:quote", "envelope:wrap", "envelope:unwrap",
		"hsm:sign", "vault:encrypt", "vault:decrypt", "hkdf:derive",
		"prf:kdf", "integrity:mac", "integrity:hmac",
	} {
		if !IsCryptoUse(p) {
			t.Errorf("IsCryptoUse(%q) = false, want true", p)
		}
		if got := LookupDuty(p); got != DutyUndeclared {
			t.Errorf("precondition failed: LookupDuty(%q) = %s, want undeclared", p, got)
		}
	}
}

// TestIsCryptoUse_GovernanceDeclarationCannotLaunderCryptoVerb verifies the
// backstop is not overridable. A contributor who declares foo:sign as
// governance — the one mistake an author-declares-it model is exposed to — still
// cannot place it on the Security Officer role.
func TestIsCryptoUse_GovernanceDeclarationCannotLaunderCryptoVerb(t *testing.T) {
	const probe Permission = "test:sign"
	cleanupDuty(t, probe)

	RegisterDuty(DutyGovernance, probe)

	if !IsCryptoUse(probe) {
		t.Fatalf("IsCryptoUse(%q) = false after a governance declaration, want true", probe)
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("granting a governance-declared crypto verb to the SO did not panic")
		}
		if _, ok := r.(*SoDViolationError); !ok {
			t.Fatalf("panicked with %T (%v), want *SoDViolationError", r, r)
		}
	}()
	RegisterRoleGrant(RoleSO, probe)
}

// TestIsCryptoUse_ExcludesGovernanceAndAmbiguousVerbs pins the deliberate
// boundary of the classifier. Over-classifying is a real cost — it would deny
// the Security Officer the barrier and issuance operations FIPS 140-3 assigns to
// it, and would deny any role a "verify" that is only an integrity check — so
// these must NOT be treated as cryptographic use.
func TestIsCryptoUse_ExcludesGovernanceAndAmbiguousVerbs(t *testing.T) {
	notCryptoUse := []Permission{
		// Module lifecycle: cryptographic by mechanism, governance by duty.
		SealBootstrap, SealUnseal, SealSeal, SealRotateRootKey, SealRekey,
		SealGenerateRecovery, CeremonySignCSR,
		// Issuance and credential minting, not primitives on caller input.
		CertsIssue, PasswordsGenerate,
		// Key lifecycle, including key entry / output.
		KeysCreate, KeysRotate, KeysDelete, KeysList, KeysGet, KeysImport, KeysExport,
		// PIV slot administration, as opposed to piv:use.
		PIVRead, PIVWrite, PIVDelete,
		// "verify" that is an integrity check over stored bytes, not key use.
		BackupVerify, ObjStoreVerify,
		// Data plane.
		KVPut, KVGet,
	}
	for _, p := range notCryptoUse {
		if IsCryptoUse(p) {
			t.Errorf("IsCryptoUse(%q) = true, want false", p)
		}
	}
}

// TestBuiltInDuties_EverySOGrantIsDeclaredGovernance walks the live registry
// and proves the end state the guard exists to produce: every permission the
// Security Officer actually holds is declared governance and is not a
// cryptographic operation. It is the invariant stated as an assertion over the
// materialized role rather than over the registration path, so it also covers
// grants added by a higher layer at bootstrap time.
func TestBuiltInDuties_EverySOGrantIsDeclaredGovernance(t *testing.T) {
	soPerms := RolePermissions(RoleSO)
	if len(soPerms) == 0 {
		t.Fatal("precondition failed: the Security Officer role has no grants")
	}
	for _, p := range soPerms {
		if IsWildcard(p) {
			t.Errorf("SO holds wildcard permission %q", p)
			continue
		}
		if IsCryptoUse(p) {
			t.Errorf("SO holds cryptographic permission %q (FIPS 140-3 AS10.03)", p)
		}
		if got := LookupDuty(p); got != DutyGovernance {
			t.Errorf("SO holds %q with duty %s, want governance", p, got)
		}
	}
}

// TestBuiltInDuties_KeyFamiliesFullyClassified requires every verb of the two
// families that straddle the governance / crypto-use line — keys and piv — to
// carry an explicit classification. Those are the families where a new verb is
// most likely to be cryptographic, so leaving one undeclared should fail here
// rather than wait for someone to try granting it to the Security Officer.
func TestBuiltInDuties_KeyFamiliesFullyClassified(t *testing.T) {
	for _, resource := range []string{"keys", "piv"} {
		perms := ByResource(resource)
		if len(perms) == 0 {
			t.Fatalf("precondition failed: no registered permissions for resource %q", resource)
		}
		for _, p := range perms {
			if got := LookupDuty(p); got == DutyUndeclared {
				t.Errorf("%q has no declared duty: classify it DutyGovernance (lifecycle) or DutyCryptoUse (key use) in builtin_duties.go", p)
			}
		}
	}
}
