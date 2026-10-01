// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/permission"

// ---------------------------------------------------------------------------
// Cross-scope permissions
//
// Every permission qrdb declares PermissionScopeCross, re-exported so a
// consumer gating its own surface on one of them names qrdb's grant instead
// of restating the string. A cross-scope permission is exercisable only by an
// identity in the system tenant; the RBAC authorizer refuses it to a
// tenant-local identity whatever its roles grant, with PermissionDeniedError.
//
// TestSDK_CrossScopePermissions_AreComplete fails when the registry holds a
// cross-scope permission this file does not name.
// ---------------------------------------------------------------------------

const (
	// PermissionTenantsListCross ("tenants:list:cross") lists every tenant on
	// the platform.
	PermissionTenantsListCross = permission.TenantsListCross

	// PermissionTenantsCreateCross ("tenants:create:cross") creates a tenant
	// outside the caller's own scope.
	PermissionTenantsCreateCross = permission.TenantsCreateCross

	// PermissionTenantsApprove ("tenants:approve") approves a pending tenant
	// registration.
	PermissionTenantsApprove = permission.TenantsApprove

	// PermissionTenantsCreate ("tenants:create") creates a tenant. It is
	// cross-scope: a new tenant is by definition outside the caller's own.
	PermissionTenantsCreate = permission.TenantsCreate

	// PermissionTenantsSetIsolation ("tenants:set_isolation") changes a
	// tenant's isolation mode.
	PermissionTenantsSetIsolation = permission.TenantsSetIsolation

	// PermissionTenantsSetEntitlements ("tenants:set_entitlements") changes a
	// tenant's entitlements.
	PermissionTenantsSetEntitlements = permission.TenantsSetEntitlements

	// PermissionTenantsSetQuotas ("tenants:set_quotas") changes a tenant's
	// quotas.
	PermissionTenantsSetQuotas = permission.TenantsSetQuotas

	// PermissionTenantsListDeleted ("tenants:list_deleted") enumerates retired
	// tenant ids and their deletion records.
	PermissionTenantsListDeleted = permission.TenantsListDeleted

	// PermissionTenantsReconcileDeleted ("tenants:reconcile_deleted") runs the
	// deleted-tenant reconcile on a node, removing retired tenants' directories
	// from its disk.
	PermissionTenantsReconcileDeleted = permission.TenantsReconcileDeleted

	// PermissionAuditReadCross ("audit:read:cross") reads audit events across
	// every tenant. The per-tenant audit read is self-scope.
	PermissionAuditReadCross = permission.AuditReadCross

	// PermissionCertsRevokeCross ("certs:revoke:cross") writes a cluster-wide
	// deny marker for a serial the node holds no issued record for.
	PermissionCertsRevokeCross = permission.CertsRevokeCross

	// PermissionVolumePoolCreate ("volumepool:create") registers a volume
	// pool.
	PermissionVolumePoolCreate = permission.VolumePoolCreate

	// PermissionVolumePoolList ("volumepool:list") lists volume pools.
	PermissionVolumePoolList = permission.VolumePoolList

	// PermissionVolumePoolRead ("volumepool:read") reads one volume pool's
	// configuration and utilization.
	PermissionVolumePoolRead = permission.VolumePoolRead

	// PermissionVolumePoolUpdate ("volumepool:update") changes a volume
	// pool's backend set or strategy.
	PermissionVolumePoolUpdate = permission.VolumePoolUpdate

	// PermissionVolumePoolDelete ("volumepool:delete") removes a volume pool.
	PermissionVolumePoolDelete = permission.VolumePoolDelete

	// PermissionRegistrationConfigRead ("registration_config:read") reads the
	// platform-wide self-service registration policy.
	PermissionRegistrationConfigRead = permission.RegistrationConfigRead

	// PermissionRegistrationConfigUpdate ("registration_config:update")
	// changes the platform-wide self-service registration policy.
	PermissionRegistrationConfigUpdate = permission.RegistrationConfigUpdate

	// PermissionMVCCCompact ("mvcc:compact") compacts MVCC history. It is
	// cross-scope because compaction acts on the whole store, not on one
	// tenant's namespace.
	PermissionMVCCCompact = permission.MVCCCompact
)

// The named-credential permissions are cross-scope because the credential
// store is one system-scoped barrier with no tenant namespacing: a
// tenant-local role must never satisfy them.
const (
	// PermissionCredentialsSubmit ("credentials:submit") submits a named
	// credential.
	PermissionCredentialsSubmit = permission.CredentialsSubmit

	// PermissionCredentialsStrategy ("credentials:strategy") reads a named
	// credential's sealing strategy.
	PermissionCredentialsStrategy = permission.CredentialsStrategy

	// PermissionCredentialsList ("credentials:list") lists named credentials.
	PermissionCredentialsList = permission.CredentialsList

	// PermissionCredentialsDelete ("credentials:delete") deletes a named
	// credential.
	PermissionCredentialsDelete = permission.CredentialsDelete

	// PermissionCredentialsRotate ("credentials:rotate") rotates a named
	// credential to a new version.
	PermissionCredentialsRotate = permission.CredentialsRotate

	// PermissionCredentialsGCExpired ("credentials:gc_expired") removes
	// expired credential versions.
	PermissionCredentialsGCExpired = permission.CredentialsGCExpired

	// PermissionCredentialsRetrieveVersion ("credentials:retrieve_version")
	// retrieves one version of a named credential.
	PermissionCredentialsRetrieveVersion = permission.CredentialsRetrieveVersion

	// PermissionCredentialsListVersions ("credentials:list_versions") lists a
	// named credential's versions.
	PermissionCredentialsListVersions = permission.CredentialsListVersions

	// PermissionCredentialsStore ("credentials:store") stores a named
	// credential.
	PermissionCredentialsStore = permission.CredentialsStore

	// PermissionCredentialsRetrieve ("credentials:retrieve") retrieves a named
	// credential's current version.
	PermissionCredentialsRetrieve = permission.CredentialsRetrieve
)

// The node-barrier permissions are cross-scope because the node barrier is a
// singleton that protects every tenant's data, so an identity of one tenant
// must never initialize, unseal, seal, rekey or recover it, rotate its root
// key, set up its recovery and custody, or decide who holds a share of its
// key. A tenant's own barrier is governed by the tenant_barrier permissions,
// which are not cross-scope.
const (
	// PermissionSealBootstrap ("seal:bootstrap") initializes the node barrier
	// and mints its root token.
	PermissionSealBootstrap = permission.SealBootstrap

	// PermissionSealUnseal ("seal:unseal") unseals the node barrier.
	PermissionSealUnseal = permission.SealUnseal

	// PermissionSealRecoverWithKeys ("seal:recover_with_keys") recovers the
	// node barrier with its offline recovery keys.
	PermissionSealRecoverWithKeys = permission.SealRecoverWithKeys

	// PermissionSealEnableEncryption ("seal:enable_encryption") migrates the
	// node barrier, or a tenant's barrier named in the request, to an
	// encrypting strategy.
	PermissionSealEnableEncryption = permission.SealEnableEncryption

	// PermissionSealSubmitSealVote ("seal:submit_seal_vote") submits a share
	// toward the quorum that seals a Shamir-protected node barrier.
	PermissionSealSubmitSealVote = permission.SealSubmitSealVote

	// PermissionSealOfficerRegister ("seal:officer_register") registers a
	// Security Officer into the node barrier's unseal or recovery domain.
	PermissionSealOfficerRegister = permission.SealOfficerRegister

	// PermissionSealOfficerList ("seal:officer_list") lists the node
	// barrier's Security Officers and their claim status.
	PermissionSealOfficerList = permission.SealOfficerList

	// PermissionCeremonyInitialize ("ceremony:initialize") initializes the
	// node key ceremony.
	PermissionCeremonyInitialize = permission.CeremonyInitialize

	// PermissionCeremonyEnrollCustodian ("ceremony:enroll_custodian") enrolls
	// a custodian, or an external officer, into the node key ceremony.
	PermissionCeremonyEnrollCustodian = permission.CeremonyEnrollCustodian

	// PermissionCeremonyRevokeCustodian ("ceremony:revoke_custodian") revokes
	// a node key-ceremony custodian.
	PermissionCeremonyRevokeCustodian = permission.CeremonyRevokeCustodian

	// PermissionCeremonySignCSR ("ceremony:sign_csr") signs a custodian
	// enrollment CSR with the cluster CA.
	PermissionCeremonySignCSR = permission.CeremonySignCSR

	// PermissionSealSeal ("seal:seal") seals the node barrier.
	PermissionSealSeal = permission.SealSeal

	// PermissionSealRekey ("seal:rekey") rekeys the node barrier's unseal
	// shares.
	PermissionSealRekey = permission.SealRekey

	// PermissionSealRotateRootKey ("seal:rotate_root_key") rotates the node
	// barrier's root key.
	PermissionSealRotateRootKey = permission.SealRotateRootKey

	// PermissionSealGenerateRecovery ("seal:generate_recovery") generates
	// recovery keys for the node barrier.
	PermissionSealGenerateRecovery = permission.SealGenerateRecovery

	// PermissionSealInitializeShamir ("seal:initialize_shamir") initializes
	// Shamir custody of the node barrier.
	PermissionSealInitializeShamir = permission.SealInitializeShamir
)
