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

// nodeBarrierSOPermissions are the operations on the NODE barrier that
// change its state: initializing it and minting its root token
// (seal:bootstrap), unsealing and recovering it, rekeying it, rotating its
// root key, generating (and deleting) its recovery keys, initializing its
// Shamir sealing, migrating it to an encrypting strategy, sealing it, and
// voting to seal it. Sealing and unsealing are operational verbs Admin and
// Operator hold, not SO-exclusive ones, but sealing stops every tenant's data
// plane at once and unsealing opens it.
//
// The node barrier is a platform singleton. There is one per node, it
// protects every tenant's data, and no tenant owns it. These permissions were
// ScopeSelf, and ScopeSelf compares the caller's tenant with the request's
// tenant -- which, for an operation that names no tenant, is the caller's own.
// The check was therefore vacuous: an SO of tenant A, holding the built-in SO
// role of tenant A and presenting tenant A's own valid SO PIN, could rekey the
// barrier that protects every tenant. A tenant's own barrier is governed by
// the tenant_barrier:* permissions, which these are not.
//
// seal:enable_encryption names its target in the request body -- the node
// barrier, or a tenant's barrier by id -- and the route binds no tenant, so
// under ScopeSelf a tenant-local holder could migrate the node barrier or
// another tenant's. It is ScopeCross with the rest: migrating a barrier is a
// platform-operator act. A tenant acting on its own barrier holds
// tenant_barrier:enable_encryption.
//
// They are ScopeCross: only an identity of the system tenant may exercise
// them, on every transport, with the SO dual-factor gate on or off -- the
// platform-operator scope the tenant-administration rulings assign to
// platform-wide state.
func nodeBarrierSOPermissions() []Permission {
	return []Permission{
		SealBootstrap,
		SealEnableEncryption,
		SealGenerateRecovery,
		SealInitializeShamir,
		SealRecoverWithKeys,
		SealRekey,
		SealRotateRootKey,
		SealSeal,
		SealSubmitSealVote,
		SealUnseal,
	}
}

// nodeBarrierShareHolderPermissions govern WHO holds a share of the node
// barrier's key: registering a Security Officer into its unseal domain
// (seal:officer_register, POST /v1/barrier/officers) or its recovery domain
// (the same permission, POST /v1/barrier/recovery/officers), listing those
// officers with their certificate thumbprints, and the node key ceremony's
// custodian enrollment, revocation, initialization and CSR signing.
//
// Membership of that set is the node key itself, by another route. Every
// distribution -- rekey, revocation, recovery-key generation -- sends a share
// to every registered officer, so whoever can add M officers can reconstruct
// the node unseal key or reach the root-token quorum. These routes name no
// tenant, so under ScopeSelf the check compared the caller's tenant with
// itself: a tenant's SO, holding its own tenant's built-in SO role, could
// plant officers in the node barrier and read back the node's officer names
// and thumbprints. ceremony:sign_csr signs with the cluster CA, which no
// tenant owns either.
//
// They are ScopeCross for the same reason as nodeBarrierSOPermissions. This
// does not touch first-boot enrollment: the boot-time sealed-window listener
// has no authentication middleware and admits anonymous officer registration
// only while the enrollment ceremony is pre-operational
// (sealedwindow.PreOperationalOnly), so no scope rule runs there. A tenant's
// own Security Officers are governed by tenant_barrier:officer_register and
// tenant_barrier:officer_list, which are ScopeTenantOnly and bound to the
// path tenant.
func nodeBarrierShareHolderPermissions() []Permission {
	return []Permission{
		CeremonyEnrollCustodian,
		CeremonyInitialize,
		CeremonyRevokeCustodian,
		CeremonySignCSR,
		SealOfficerList,
		SealOfficerRegister,
	}
}

func init() {
	RegisterScope(ScopeCross, nodeBarrierSOPermissions()...)
	RegisterScope(ScopeCross, nodeBarrierShareHolderPermissions()...)
}
