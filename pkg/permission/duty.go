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

import "sync"

// ---------------------------------------------------------------------------
// Duty classification
//
// FIPS 140-3 AS10.03 separates module GOVERNANCE (who configures the module,
// manages its keys' lifecycle, and administers identities) from cryptographic
// USE (who asks the module to perform a cryptographic primitive with key
// material). The Security Officer holds the former and must never hold the
// latter.
//
// Deciding which side of that line a permission falls on cannot be inferred
// from its resource family: "keys" contains both (keys:sign is use, keys:create
// is governance), and cryptographic use is not confined to "keys" (piv:use, and
// go-quicbaas's attestation:attest, which extends a PCR on the host TPM and
// asks it for a signed quote). It also cannot be inferred reliably from the
// action verb alone: seal:unseal and ceremony:sign_csr are cryptographic by
// mechanism yet are the Security Officer's own FIPS-assigned module-lifecycle
// duties, while backup:verify is an integrity check that touches no key.
//
// The classification is therefore metadata the permission's AUTHOR declares,
// exactly as role placement is (RegisterRoleGrant) and tenancy reach is
// (RegisterScope), with two properties that make it fail-safe:
//
//  1. Omission denies. The zero value is DutyUndeclared, and
//     RegisterRoleGrant refuses to place an undeclared permission on the
//     Security Officer role. A new permission cannot become SO-grantable by
//     nobody having thought about it; the author must state its nature, and
//     the omission is caught at registration time, not by review.
//  2. Declaration is immutable, and a governance declaration cannot launder an
//     unambiguously cryptographic verb (see cryptoPrimitiveActions).
//
// Only two things need declaring: permissions the Security Officer is meant to
// hold (DutyGovernance) and permissions that perform a cryptographic primitive
// (DutyCryptoUse). Everything else — the tenant data plane and every other
// non-SO surface — is correctly left undeclared: undeclared already means "not
// Security-Officer business".
// ---------------------------------------------------------------------------

// Duty classifies which of the separated duties a permission belongs to under
// FIPS 140-3 AS10.03. It is the metadata the Security-Officer separation-of-
// duties guard in RegisterRoleGrant decides on.
type Duty int

const (
	// DutyUndeclared is the zero value: the permission's author has not
	// stated which separated duty it belongs to. It is NOT a synonym for
	// "harmless" — it is the fail-safe state, and an undeclared permission is
	// never grantable to the Security Officer role. Most permissions are
	// legitimately undeclared (the whole tenant data plane), because the
	// Security Officer holds none of them.
	DutyUndeclared Duty = iota

	// DutyGovernance is module and security governance: barrier and ceremony
	// lifecycle, key lifecycle (create / delete / import / export / list),
	// identity, role, tenant and certificate administration, audit
	// visibility, and cluster control. These are the Security Officer's own
	// duties, so DutyGovernance is the ONLY classification RegisterRoleGrant
	// accepts for a Security Officer grant.
	DutyGovernance

	// DutyCryptoUse is the performance of a cryptographic primitive with key
	// material the module holds — signing, encryption, decryption, key
	// wrapping, key derivation, hardware-token key use. FIPS 140-3 AS10.03
	// bars the Security Officer from these: an identity that governs the
	// module must not also be able to use it as a cryptographic oracle.
	// RegisterRoleGrant panics on any attempt to grant one to the Security
	// Officer, and the runtime deny-list in pkg/auth/rbac rejects them again
	// at authorization time.
	DutyCryptoUse
)

// String returns a stable, human-readable label for the duty, suitable for
// log and error messages.
func (d Duty) String() string {
	switch d {
	case DutyUndeclared:
		return "undeclared"
	case DutyGovernance:
		return "governance"
	case DutyCryptoUse:
		return "crypto-use"
	default:
		return "unknown"
	}
}

// Valid reports whether d is a duty a caller may declare. DutyUndeclared is
// deliberately NOT valid: it is the state a permission is in when nothing has
// been declared, expressed by omitting the RegisterDuty call rather than by
// declaring it.
func (d Duty) Valid() bool {
	switch d {
	case DutyGovernance, DutyCryptoUse:
		return true
	default:
		return false
	}
}

// cryptoPrimitiveActions is the resource-independent backstop for the Security
// Officer guard: action verbs that name a cryptographic primitive performed
// with key material, whatever resource family they appear in. A permission
// whose action is one of these is treated as DutyCryptoUse EVEN IF it was
// declared DutyGovernance, so a governance declaration — the one mistake the
// author-declares-it model is exposed to — cannot launder foo:sign into a
// Security Officer grant.
//
// The set names cryptographic primitives only. A future family that wants one
// of these verbs for a non-cryptographic meaning must choose a different verb:
// the FIPS 140-3 AS10.03 invariant outranks vocabulary convenience, and the
// registration-time panic says so explicitly.
//
// Deliberately EXCLUDED:
//
//   - "verify" — ambiguous across families. backup:verify and objstore:verify
//     are integrity checks over stored bytes that touch no key. keys:verify IS
//     cryptographic use and is barred, but by its explicit DutyCryptoUse
//     declaration (see builtin_duties.go), not by its spelling.
//   - "seal" / "unseal" — the barrier's seal:seal / seal:unseal are the
//     Security Officer's FIPS-assigned module-lifecycle duty, not key use on
//     a requester's behalf. They are declared DutyGovernance.
//   - "generate" — certs:generate is CA issuance governance;
//     passwords:generate and dynamic_secrets:generate mint credentials.
//     None is a primitive invoked on caller-supplied input.
var cryptoPrimitiveActions = map[string]struct{}{
	"sign":    {},
	"encrypt": {},
	"decrypt": {},
	"wrap":    {},
	"unwrap":  {},
	"derive":  {},
	"kdf":     {},
	"mac":     {},
	"hmac":    {},
	"attest":  {},
	"quote":   {},
}

var (
	dutyMu sync.RWMutex

	// dutyRegistry maps a permission to its declared duty. A permission
	// absent from the map is DutyUndeclared. Entries are write-once: see
	// RegisterDuty.
	dutyRegistry = map[Permission]Duty{}
)

// RegisterDuty declares the separated duty that each permission in perms
// belongs to. It is safe for concurrent use and is called at the permission's
// declaration site — from an init() alongside Register and RegisterScope, or
// from a higher layer (go-quicbaas, go-xkms) through the sdk/go re-export
// before role seeding.
//
// Declaring the duty is what makes a permission grantable to the Security
// Officer role: RegisterRoleGrant rejects any Security Officer grant of a
// permission that is not declared DutyGovernance. Permissions the Security
// Officer never holds need no declaration.
//
// A declaration is immutable. Re-declaring the same duty is a no-op (so
// repeated or idempotent Register calls are safe), but declaring a DIFFERENT
// duty for a permission that already has one panics with *DutyConflictError:
// re-declaration would otherwise be a way to relabel a cryptographic
// permission as governance and hand it to the Security Officer. Passing a
// duty that is not Valid (including DutyUndeclared, which is expressed by
// omitting the call) panics with *InvalidDutyError.
//
// Panics are deliberate, matching RegisterRoleGrant: both are
// package-init-time or bootstrap-time programmer errors discovered before any
// request is served, with no reasonable recovery.
func RegisterDuty(duty Duty, perms ...Permission) {
	if !duty.Valid() {
		panic(&InvalidDutyError{Duty: duty})
	}

	dutyMu.Lock()
	defer dutyMu.Unlock()
	for _, p := range perms {
		if existing, ok := dutyRegistry[p]; ok && existing != duty {
			panic(&DutyConflictError{Permission: p, Existing: existing, Requested: duty})
		}
		dutyRegistry[p] = duty
	}
}

// LookupDuty returns the declared duty for p, or DutyUndeclared when its
// author has not declared one. Undeclared is the fail-safe state, not a
// permissive default: see the Duty constants.
func LookupDuty(p Permission) Duty {
	dutyMu.RLock()
	defer dutyMu.RUnlock()
	return dutyRegistry[p]
}

// IsCryptoUse reports whether p performs a cryptographic primitive with key
// material and is therefore barred from the Security Officer role by FIPS
// 140-3 AS10.03. A permission qualifies when its action is one of the
// unambiguous cryptographic primitives (cryptoPrimitiveActions — applied to
// every resource family, and not overridable by a declaration) or when its
// author declared it DutyCryptoUse.
//
// IsCryptoUse is NOT the whole Security Officer guard: a permission that is
// not crypto use still needs an explicit DutyGovernance declaration to be
// SO-grantable (see RegisterRoleGrant). The predicate is exported so the
// runtime deny-list in pkg/auth/rbac — an independent, defense-in-depth
// mechanism that does not consult this registry — can be proven to agree with
// this one rather than merely believed to.
func IsCryptoUse(p Permission) bool {
	if _, ok := cryptoPrimitiveActions[Action(p)]; ok {
		return true
	}
	return LookupDuty(p) == DutyCryptoUse
}
