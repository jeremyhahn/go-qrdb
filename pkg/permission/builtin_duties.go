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

// ---------------------------------------------------------------------------
// Built-in duty declarations
//
// This file states, for every qrdb permission whose separated duty matters,
// which side of the FIPS 140-3 AS10.03 governance / cryptographic-use line it
// falls on. Two groups need a declaration:
//
//   - DutyGovernance — every permission the Security Officer holds by default.
//     RegisterRoleGrant will not place an undeclared permission on the
//     Security Officer role, so this group and registerSOGrants in
//     builtin_role_grants.go cannot silently drift apart: adding an SO grant
//     without declaring the permission's duty panics at init().
//   - DutyCryptoUse — every permission that performs a cryptographic
//     primitive with key material. Declaring these is what permanently bars
//     them from the Security Officer role, and (because declarations are
//     immutable) what stops a later re-declaration from relabelling them as
//     governance.
//
// Every other permission — the whole tenant data plane, and the read/write
// surfaces of families the Security Officer has no part in — is deliberately
// left undeclared. Undeclared means "not Security-Officer business", which is
// both the truth and the fail-safe answer.
//
// registerBuiltInDuties is called from builtin_role_grants.go's init(), before
// the grants themselves, rather than from an init() here: the ordering is a
// hard requirement (a grant is validated against the declaration), so it is
// expressed as an explicit call rather than left to depend on file ordering.
// ---------------------------------------------------------------------------

// registerBuiltInDuties declares the duty of every qrdb permission the
// Security-Officer separation-of-duties guard needs to reason about.
func registerBuiltInDuties() {
	registerCryptoUseDuties()
	registerGovernanceDuties()
}

// registerCryptoUseDuties declares the permissions that perform a
// cryptographic primitive with key material the module holds. FIPS 140-3
// AS10.03 bars the Security Officer from all of them.
//
// keys:sign, keys:encrypt, keys:decrypt, keys:wrap, keys:unwrap and
// keys:derive are also caught by the resource-independent verb backstop in
// duty.go; they are declared anyway so the classification is stated where a
// reader looks for it rather than inferred from a spelling. keys:verify and
// piv:use have no such backstop — "verify" is ambiguous across families and
// "use" says nothing on its own — so for those two the declaration is the
// only thing standing between them and a Security Officer grant.
func registerCryptoUseDuties() {
	RegisterDuty(DutyCryptoUse,
		// Asymmetric and symmetric primitives over caller-supplied input.
		KeysSign, KeysVerify, KeysEncrypt, KeysDecrypt,
		// Key transport and key agreement / derivation. Wrapping and
		// unwrapping move key material under another key, and derivation
		// (KDF / ECDH) produces new key material from existing key material;
		// both are key USE, distinct from the keys:import / keys:export
		// lifecycle verbs the Security Officer does hold.
		KeysWrap, KeysUnwrap, KeysDerive,
		// Hardware-token key use. piv:use is "use a PIV slot key for
		// cryptographic operations" — the same duty as keys:sign, reached
		// through a smart card. The Security Officer administers PIV slots
		// (piv:read / piv:write / piv:delete) but never uses their keys.
		PIVUse,
	)
}

// registerGovernanceDuties declares the module-governance permissions the
// Security Officer holds. Grouped by family, in the order registerSOGrants
// places them.
//
// Several of these are cryptographic BY MECHANISM yet are governance BY DUTY,
// which is precisely why the classification is declared rather than sniffed
// from the action verb:
//
//   - seal:unseal / seal:seal / seal:rotate_root_key / seal:rekey /
//     seal:recover_with_keys / seal:generate_recovery reconstruct or re-derive
//     the barrier root key. That is module lifecycle — the operation FIPS
//     140-3 assigns to the Security Officer — not the module performing a
//     primitive on a requester's data.
//   - ceremony:sign_csr signs a custodian enrollment CSR and certs:generate
//     signs a certificate: issuance is a governance decision about identity,
//     and the Security Officer is the identity authority.
//   - keys:import / keys:export are key entry and key output, the Crypto
//     Officer's key-management duty. They confer no ability to compute with a
//     key inside the module.
//   - escrow:create / escrow:recover / escrow:list / escrow:revoke move
//     wrapped key material to and from an external custodian (KMIP, a
//     federation agent) — key entry/output again, just across a trust
//     boundary rather than to a local file.
func registerGovernanceDuties() {
	RegisterDuty(DutyGovernance,
		// Barrier / seal lifecycle (SO-exclusive).
		SealBootstrap, SealUnseal, SealSeal,
		SealStatus, SealRotateRootKey, SealRekey,
		SealGenerateRecovery, SealRecoverWithKeys,
		SealInitializeShamir,
		// Security Officer registry and seal voting (SO-exclusive).
		SealOfficerRegister, SealOfficerList,
		SealSubmitSealVote,
		// Shamir ceremony (SO-exclusive).
		CeremonyInitialize, CeremonyEnrollCustodian,
		CeremonyRevokeCustodian, CeremonyClaimCert,
		CeremonySignCSR, CeremonyStatus,
		// Certificate administration.
		CertsGenerate, CertsExport, CertsRevoke,
		CertsList, CertsRead,
		// Role and permission administration (SO-exclusive).
		RolesCreate, RolesRead, RolesUpdate,
		RolesDelete, RolesList, RolesAssign,
		RolesUnassign, PermissionsList,
		// User administration.
		UsersCreate, UsersRead, UsersUpdate,
		UsersDelete, UsersList, UsersEnable,
		UsersDisable,
		// Tenant administration.
		TenantsCreate, TenantsRead, TenantsUpdate,
		TenantsDelete, TenantsList,
		TenantsSetIsolation, TenantsSetEntitlements,
		// Key lifecycle — creation, rotation, destruction, inventory, and key
		// entry/output. Never key use: the crypto verbs of the same family are
		// declared DutyCryptoUse above. keys:rotate is declared here even
		// though the Security Officer is not granted it, so that every verb of
		// the keys family carries a classification (pinned by
		// TestBuiltInDuties_KeyFamiliesFullyClassified).
		KeysCreate, KeysRotate, KeysDelete, KeysList, KeysGet,
		KeysImport, KeysExport,
		// Key escrow: entrusting wrapped key material to an external KMIP
		// server or federation agent, and reconstructing it back. Key entry
		// and key output by another name, so it sits with keys:import /
		// keys:export rather than with the cryptographic primitives above.
		EscrowCreate, EscrowRecover, EscrowList, EscrowRevoke,
		// Hardware crypto backend inventory.
		BackendsList, BackendsRead,
		// PKCS#11 PIN management and PIV slot administration. Governance of
		// the token, not use of its keys (piv:use is DutyCryptoUse).
		PINRead, PINWrite,
		PIVRead, PIVWrite, PIVDelete,
		// Audit oversight.
		AuditRead, AuditQuery,
		// Cluster and node control.
		AdminHealth, AdminStatus, AdminPeers,
		AdminAddMember, AdminRemoveMember,
		AdminSnapshot, AdminDrain, AdminCapabilities,
		// Authentication-factor lifecycle. Enrolling and revoking a factor is
		// identity governance; authfactor:challenge / authfactor:verify — the
		// login path that checks an assertion — are deliberately NOT declared
		// and so are not SO-grantable.
		AuthFactorEnroll, AuthFactorList, AuthFactorRevoke,
		// Certificate authority hierarchy: creating and retiring signing CAs.
		CACreate, CARead, CAList, CAUpdate,
		// ACME server administration: accounts, orders, and External Account
		// Bindings for the CA the Security Officer governs.
		ACMEAccountsRead, ACMEAccountsList, ACMEAccountsDisable,
		ACMEOrdersRead, ACMEOrdersList,
		ACMEEABRead, ACMEEABList,
		ACMEEABCreate, ACMEEABDelete,
	)
}
