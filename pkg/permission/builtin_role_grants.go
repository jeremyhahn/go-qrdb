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
// Built-in role default grants
//
// This file is the single, authoritative placement of every permission into
// the five canonical FIPS 140-3 / PCI-DSS v4 built-in roles that are derived
// from the registry (SO, Admin, Operator, User, Auditor — see
// pkg/role.BuiltInRoles). It replaces the five hand-maintained permission
// lists that used to live in pkg/role/store.go (soPermissions,
// adminPermissions, operatorPermissions, userPermissions,
// auditorPermissions): role placement is metadata carried by the
// permission, registered here once, rather than a parallel list the role
// package must keep in sync by hand.
//
// The Custodian and Owner roles are NOT derived from this registry:
// Custodian holds exactly the Shamir/barrier share-submission permissions
// (a fixed, minimal set unrelated to the registry-driven roles below), and
// Owner is the standard-posture convenience superuser defined as
// permission.All() — see pkg/role.custodianPermissions / OwnerRole.
// ---------------------------------------------------------------------------

// registerSOGrants places the Security Officer's default permissions. The SO
// manages module lifecycle and governance, so every permission below is
// declared DutyGovernance in builtin_duties.go — RegisterRoleGrant admits
// nothing else onto this role (FIPS 140-3 AS10.03, enforced structurally):
// a cryptographic permission panics, and so does one whose duty nobody
// declared. Adding a grant here without declaring its duty is therefore an
// init()-time failure, not a review item. Cryptographic operations are also
// rejected at authorization time by the RBAC deny-list even if a grant reaches
// a role by some other path (defense-in-depth).
func registerSOGrants() {
	RegisterRoleGrant(RoleSO,
		// Seal management (SO-exclusive)
		SealBootstrap, SealUnseal, SealSeal,
		SealStatus, SealRotateRootKey, SealRekey,
		SealGenerateRecovery, SealRecoverWithKeys,
		SealInitializeShamir,
		// Security Officer operations (SO-exclusive)
		SealOfficerRegister, SealOfficerList,
		SealSubmitSealVote,
		// Ceremony (SO-exclusive)
		CeremonyInitialize, CeremonyEnrollCustodian,
		CeremonyRevokeCustodian, CeremonyClaimCert,
		CeremonySignCSR, CeremonyStatus,
		// Certificate management
		CertsGenerate, CertsExport, CertsRevoke,
		CertsList, CertsRead,
		// Role management (SO-exclusive)
		RolesCreate, RolesRead, RolesUpdate,
		RolesDelete, RolesList, RolesAssign,
		RolesUnassign, PermissionsList,
		// User management
		UsersCreate, UsersRead, UsersUpdate,
		UsersDelete, UsersList, UsersEnable,
		UsersDisable,
		// Tenant management
		TenantsCreate, TenantsRead, TenantsUpdate,
		TenantsDelete, TenantsList,
		TenantsSetIsolation, TenantsSetEntitlements,
		// Key lifecycle (infrastructure keys — delete any key; create infra keys)
		KeysCreate, KeysDelete, KeysList, KeysGet,
		// Key material import/export (privileged lifecycle — NOT sign/encrypt/decrypt)
		KeysImport, KeysExport,
		// Key escrow — full lifecycle (external custody is a key-management
		// duty; parallels keys:import/export above).
		EscrowCreate, EscrowRecover, EscrowList, EscrowRevoke,
		// Hardware crypto backend inventory
		BackendsList, BackendsRead,
		// PKCS#11 PIN management and PIV slot administration (governance, no crypto use)
		PINRead, PINWrite,
		PIVRead, PIVWrite, PIVDelete,
		// Audit (operational oversight)
		AuditRead, AuditQuery,
		// Admin
		AdminHealth, AdminStatus, AdminPeers,
		AdminAddMember, AdminRemoveMember,
		AdminSnapshot, AdminDrain, AdminCapabilities,
		// Auth factor management
		AuthFactorEnroll, AuthFactorList, AuthFactorRevoke,
		// Certificate authority hierarchy. Creating and retiring signing CAs is
		// governance, not key use, so it sits with the SO alongside certs:*.
		CACreate, CARead, CAList, CAUpdate,
		// ACME server administration: the SO governs the CA that the ACME
		// server fronts, so it holds the full account / order / EAB surface.
		ACMEAccountsRead, ACMEAccountsList, ACMEAccountsDisable,
		ACMEOrdersRead, ACMEOrdersList,
		ACMEEABRead, ACMEEABList,
		ACMEEABCreate, ACMEEABDelete,
	)
}

// registerAdminGrants places the Admin built-in role's default permissions.
// Admins manage operational key lifecycle and tenant configuration. They
// cannot initialize the module, reset SO PINs, or perform barrier
// bootstrapping.
//
// The cross-scope permissions near the end of this list (TenantsListCross,
// TenantsCreateCross, TenantsApprove, VolumePool*, RegistrationConfig*,
// AuditReadCross) confer cross-tenant authority only when held by an
// identity in the system tenant (the system-tenant rule in pkg/auth/rbac
// filters them out for tenant-local identities). A tenant-local Admin can
// carry these grants without exercising them outside their own tenant.
func registerAdminGrants() {
	RegisterRoleGrant(RoleAdmin,
		// Key lifecycle (operational keys — cannot delete infrastructure keys)
		KeysCreate, KeysRotate, KeysDelete,
		KeysList, KeysGet,
		// Wrap/unwrap for key transport
		KeysWrap, KeysUnwrap,
		// Key material import/export (privileged lifecycle)
		KeysImport, KeysExport,
		// Key escrow — full lifecycle (parallels keys:import/export above; a
		// tenant Admin escrows and recovers their OWN tenant's keys).
		EscrowCreate, EscrowRecover, EscrowList, EscrowRevoke,
		// Certificate management
		CertsGenerate, CertsExport, CertsRevoke,
		CertsList, CertsRead,
		// Hardware crypto backend inventory
		BackendsList, BackendsRead,
		// PKCS#11 PIN management and PIV slot administration (no crypto use)
		PINRead, PINWrite,
		PIVRead, PIVWrite, PIVDelete,
		// Password vault administration
		PasswordsRead, PasswordsWrite, PasswordsDelete,
		PasswordsGenerate, PasswordsLock, PasswordsUnlock,
		// SSH key vault administration. Parallels the password vault: an Admin
		// owns the full lifecycle of the SSH keys, known hosts, and authorized
		// keys the deployment manages.
		SSHRead, SSHWrite, SSHDelete,
		// Seal management (operational subset: no bootstrap, rotate, rekey)
		SealUnseal, SealSeal, SealStatus,
		SealRecoverWithKeys,
		// Ceremony (status only)
		CeremonyStatus,
		// Tenant management (self-scope: act on own tenant)
		TenantsRead, TenantsUpdate,
		TenantsDelete, TenantsList,
		// Per-tenant barrier management (tenant-only scope: a tenant admin
		// may init/unseal/seal their OWN tenant's barrier; the system tenant
		// is structurally denied these by the ScopeTenantOnly rule even
		// though the perm is present in the role).
		TenantBarrierInit, TenantBarrierUnseal,
		TenantBarrierSeal,
		// Per-tenant Security Officer management (ScopeTenantOnly: tenant admin
		// registers and lists officers for their OWN tenant's barrier only).
		TenantBarrierOfficerRegister, TenantBarrierOfficerList,
		// User management
		UsersCreate, UsersRead, UsersUpdate,
		UsersDelete, UsersList, UsersEnable,
		UsersDisable,
		// Role management (read/assign — cannot create/delete system roles)
		RolesRead, RolesList, RolesAssign,
		RolesUnassign, PermissionsList,
		// Admin service
		AdminHealth, AdminStatus, AdminPeers,
		AdminAddMember, AdminRemoveMember,
		AdminSnapshot, AdminDrain, AdminCapabilities,
		// Selective point-in-time restore. This is the ONE MVCC data-plane
		// verb the Admin role carries: Admin is otherwise a governance role
		// and holds no kv:*/mvcc:* data verbs, but recovering a tenant's keys
		// from an earlier revision after a bad bulk write is data-recovery
		// authority, which belongs with the role that answers for the
		// tenant's data. It stays ScopeSelf, so a tenant Admin restores only
		// their own tenant's keys.
		MVCCRestore,
		// Audit (self-scope)
		AuditRead, AuditQuery,
		// Cross-scope tenant management — only effective for system-tenant Admin.
		TenantsListCross, TenantsCreateCross, TenantsApprove,
		// Tenant provisioning — creating a tenant and setting its plan (quotas,
		// entitlements, isolation tier). Platform-operator operations,
		// ScopeCross (tenant_provisioning_scope.go): only effective for
		// system-tenant Admin, so a tenant Admin cannot raise its own limits.
		TenantsCreate, TenantsSetQuotas, TenantsSetEntitlements, TenantsSetIsolation,
		// Cross-scope volume pool management — only effective for system-tenant Admin.
		VolumePoolCreate, VolumePoolList, VolumePoolRead,
		VolumePoolUpdate, VolumePoolDelete,
		// Cross-scope registration policy — only effective for system-tenant Admin.
		RegistrationConfigRead, RegistrationConfigUpdate,
		// Cross-scope audit — only effective for system-tenant Admin.
		AuditReadCross,
		// Cross-scope CA-operator revocation (deny-by-serial for a serial
		// this node holds no record for) — only effective for system-tenant
		// Admin. The self-scope CertsRevoke above still covers every
		// revocation of the tenant's own issued certificates.
		CertsRevokeCross,
		// Broker-domain tenant security surfaces. Trust pool and ACME
		// issuance are ScopeTenantOnly: a tenant admin manages their OWN
		// tenant's trust pool and ACME issuance, and the system tenant is
		// structurally denied. secrets:* is ScopeSelfStrict instead
		// (go-qrdb#372): a tenant admin still manages only their OWN vault,
		// but the system tenant is confined to its own vault rather than
		// denied outright, since the system tenant needs a private vault
		// over its own namespace exactly as every other tenant does.
		// Parallels the Admin role already holding certs:* and passwords:*.
		// mcp/agent scopes are intentionally NOT granted here — they are
		// wired by the broker's role->scope mapping.
		SecretsRead, SecretsWrite, SecretsDelete,
		SecretsList,
		TrustRead, TrustWrite,
		ACMERead, ACMEWrite,
		// Broker data-plane families (ScopeTenantOnly). A tenant admin owns the
		// full lifecycle of their OWN tenant's data-plane surfaces, so each whole
		// family is granted via a resource:* whole-segment wildcard (expanded
		// against concrete resource:action checks by the RBAC authorizer). The
		// system tenant is structurally denied these by the ScopeTenantOnly rule
		// even though the wildcard is present in the role. Without these grants a
		// human tenant-admin's USER token resolves no data-plane scopes and is
		// denied every broker data-plane operation.
		ObjStoreAll,
		OrchestratorAll,
		SchedulerAll,
		MessagingAll,
		ParametersAll,
		DynamicSecretsAll,
		ProjectAll,
		OllamaAll,
		// Certificate authority hierarchy (self-scope): an Admin creates and
		// retires their own tenant's root / intermediate CAs and selects the
		// default issuer; a system-tenant Admin does the same platform-wide.
		CACreate, CARead, CAList, CAUpdate,
		// ACME server administration (self-scope). Admin is the role that
		// manages ACME accounts, inspects orders, and mints / revokes External
		// Account Bindings. Granting "admin" therefore reaches the ACME admin
		// surface, and revoking it removes that access.
		ACMEAccountsRead, ACMEAccountsList, ACMEAccountsDisable,
		ACMEOrdersRead, ACMEOrdersList,
		ACMEEABRead, ACMEEABList,
		ACMEEABCreate, ACMEEABDelete,
	)
}

// registerOperatorGrants places the Operator built-in role's default
// permissions. Operators perform routine crypto operations and maintenance.
// They can sign, encrypt, and decrypt using existing keys, and seal/unseal
// the barrier, but cannot create/delete keys or manage tenants. Operators
// are also the non-admin humans who USE the broker data-plane services:
// they hold the day-to-day verbs of the eight broker data-plane families
// (objstore, orchestrator, scheduler, messaging, parameters,
// dynamic_secrets, project, ollama) — but NOT each family's administrative
// / governance verbs, which remain Admin-only.
func registerOperatorGrants() {
	RegisterRoleGrant(RoleOperator,
		// Cryptographic operations
		KeysSign, KeysVerify,
		KeysEncrypt, KeysDecrypt,
		KeysList, KeysGet,
		// Key rotation (not creation/deletion)
		KeysRotate,
		// Key derivation (KDF / ECDH key agreement)
		KeysDerive,
		// Hardware crypto backend inventory (read-only — operators use backends)
		BackendsList, BackendsRead,
		// PIN read, PIV slot read + cryptographic use
		PINRead, PIVRead, PIVUse,
		// Password vault (operational use)
		PasswordsRead, PasswordsWrite, PasswordsDelete,
		PasswordsGenerate, PasswordsLock, PasswordsUnlock,
		// SSH key vault (operational use). Operators generate, rotate, and use
		// the SSH keys they administer hosts with. User and Auditor are
		// deliberately NOT granted ssh:read: unlike passwords:read, which is a
		// personal-vault verb, ssh:read reaches key export and therefore private
		// key material for exportable backends.
		SSHRead, SSHWrite, SSHDelete,
		// Barrier (operational: not bootstrap or SO-exclusive ops)
		SealUnseal, SealSeal, SealStatus,
		// Ceremony (status only)
		CeremonyStatus,
		// KV
		KVPut, KVGet, KVDelete,
		KVList, KVExists, KVRange,
		// MVCC
		MVCCPutWithLease, MVCCGetRevision,
		MVCCGetAtRevision, MVCCRange,
		MVCCCompact, MVCCCurrentRevision,
		// Selective point-in-time restore. Recovering a mistakenly
		// overwritten or deleted set of keys is routine operational repair,
		// so it sits with the Operator's other MVCC maintenance verbs. It is
		// NOT granted to User or Auditor: it is a bulk mutation, and an
		// auditor that can rewrite live keys from history is no longer an
		// independent observer (FIPS 140-3 AS10.10).
		MVCCRestore,
		// Transaction
		TransactionExecute,
		// Batch
		BatchExecute,
		// Watch
		WatchWatch, WatchWatchPrefix,
		// Lease
		LeaseGrant, LeaseRevoke,
		LeaseKeepAlive, LeaseTTL, LeaseList,
		// Index
		IndexCreate, IndexDrop,
		IndexQuery, IndexScan,
		// TimeSeries
		TSWrite, TSWriteBatch,
		TSQuery, TSAggregate,
		// Vector
		VectorNamespaceCreate, VectorNamespaceDelete,
		VectorNamespaceList, VectorNamespaceInfo,
		VectorUpsert, VectorGet, VectorDelete,
		VectorBatchUpsert, VectorBatchDelete, VectorList,
		VectorUpdateMetadata, VectorSearchSimilar,
		VectorSearchByMetadata, VectorHybridSearch,
		VectorSearchByID, VectorRebuildIndex,
		VectorGetRebuildProg, VectorGetIndexStats,
		VectorCompact, VectorGetCompactProg,
		// Admin (limited)
		AdminHealth, AdminStatus,
		// Audit (read)
		AuditRead,
		// Broker data-plane families (ScopeTenantOnly): day-to-day, non-admin
		// verbs only. Operators are non-admin humans who USE the BaaS services;
		// they create/read/update/delete THEIR OWN data objects but do NOT hold
		// the administrative / governance verbs of each family (those remain
		// Admin-only via the resource:* wildcards in registerAdminGrants). The
		// concrete perms are enumerated deliberately — operator is NEVER granted
		// a resource:* wildcard, which would sweep in admin verbs such as
		// objstore:admin or dynamic_secrets:role_*. The ScopeTenantOnly rule in
		// pkg/auth/rbac still confines these to the operator's OWN tenant.
		//
		// Object storage: full object lifecycle on the operator's own objects.
		// Excluded: objstore:admin (backend admin/GC/repair/compaction),
		// objstore:write_policy + objstore:tenant_quota_write (governance), and
		// objstore:replicate + objstore:migrate (cross-backend data movement —
		// conservatively reserved to Admin).
		ObjStorePut, ObjStoreGet, ObjStoreDelete,
		ObjStoreList, ObjStoreHead, ObjStoreCopy,
		ObjStoreArchive, ObjStoreRestore,
		ObjStoreVerify, ObjStoreBatch,
		ObjStoreReadPolicy, ObjStoreReadBackend,
		ObjStoreReadStats, ObjStoreTenantUsage,
		ObjStoreTenantQuotaRead,
		// Orchestrator: all 34 concrete verbs. Operators run the full
		// orchestration data plane day-to-day — spawning/observing sessions,
		// authoring workflows, dispatching and completing tasks, managing the
		// agent fleet (including removing dead/stuck agents), reading/writing the
		// shared cache, and broadcasting drain/reload to the fleet. Workflow
		// authoring (orchestrator:workflow_write), removing dead agents
		// (orchestrator:agent_remove), and fleet broadcast
		// (orchestrator:broadcast) are operational verbs, not governance.
		// The concrete verbs are enumerated deliberately rather than granting the
		// orchestrator:* wildcard, so any future admin-only orchestrator verb does
		// NOT silently accrue to operator (that wildcard stays Admin-only).
		OrchestratorSpawn, OrchestratorPrompt,
		OrchestratorStatus, OrchestratorClose,
		OrchestratorList, OrchestratorTaskCreate,
		OrchestratorTaskRead, OrchestratorTaskList,
		OrchestratorTaskClaim, OrchestratorTaskReadmit,
		OrchestratorTaskBlock, OrchestratorTaskComplete,
		OrchestratorTaskDecompose, OrchestratorArtifactSubmit,
		OrchestratorArtifactRead, OrchestratorJobStart,
		OrchestratorJobRead, OrchestratorJobList,
		OrchestratorJobCancel, OrchestratorBackendList,
		OrchestratorDiscover, OrchestratorHook,
		OrchestratorWorkflowRead, OrchestratorWorkflowWrite,
		OrchestratorHealth, OrchestratorInference,
		OrchestratorAgentRegister, OrchestratorAgentRead,
		OrchestratorAgentList, OrchestratorAgentRemove,
		OrchestratorAgentHeartbeat, OrchestratorCacheRead,
		OrchestratorCacheWrite, OrchestratorBroadcast,
		// Messaging: full channel lifecycle + pub/sub on the operator's own
		// channels (delete is a day-to-day op on the user's own data object).
		MessagingChannelCreate, MessagingChannelRead,
		MessagingChannelList, MessagingChannelDelete,
		MessagingPublish, MessagingSubscribe,
		MessagingUnsubscribe, MessagingWatch,
		MessagingStats,
		// Scheduler: full job lifecycle on the operator's own jobs.
		SchedulerCreate, SchedulerRead, SchedulerList,
		SchedulerUpdate, SchedulerDelete,
		SchedulerTrigger, SchedulerPause,
		SchedulerResume, SchedulerCancel,
		SchedulerHistory, SchedulerStatus,
		// Parameters: full CRUD + search on the operator's own parameters.
		ParametersRead, ParametersWrite,
		ParametersDelete, ParametersList,
		ParametersSearch,
		// Dynamic secrets: issue and manage leases. Excluded: all
		// dynamic_secrets:role_* (role definition management is governance).
		DynamicSecretsGenerate, DynamicSecretsLeaseRenew,
		DynamicSecretsLeaseRevoke, DynamicSecretsLeaseList,
		// Project: full project lifecycle. Excluded: project:grant + project:revoke
		// (granting/revoking other agents' project roles is governance).
		ProjectCreate, ProjectRead, ProjectList,
		ProjectUpdate, ProjectDelete,
		// Ollama: inference + read-only model inspection.
		OllamaHealth, OllamaModelList,
		OllamaModelInfo, OllamaEmbed,
		// Certificate authority hierarchy (read-only): operators inspect the
		// signing hierarchy they issue from but never create or retire a CA.
		CARead, CAList,
		// ACME server administration (read-only). Minting an External Account
		// Binding is an endorsement decision and stays with Admin / SO; a
		// deployment that needs a narrow endorser creates a custom role holding
		// acme_eab:create.
		ACMEAccountsRead, ACMEAccountsList,
		ACMEOrdersRead, ACMEOrdersList,
		ACMEEABRead, ACMEEABList,
	)
}

// registerUserGrants places the User built-in role's default permissions.
// Users perform cryptographic operations (sign, verify, encrypt, decrypt)
// using existing keys. They cannot create, delete, or modify keys or
// configurations.
func registerUserGrants() {
	RegisterRoleGrant(RoleUser,
		// Cryptographic operations (read-only key access)
		KeysSign, KeysVerify,
		KeysEncrypt, KeysDecrypt,
		// Key derivation (KDF / ECDH)
		KeysDerive,
		// Key metadata (public fields only)
		KeysGet, KeysList,
		// PIV slot cryptographic use
		PIVUse,
		// Personal password vault (use own credentials)
		PasswordsRead, PasswordsLock, PasswordsUnlock,
		// System health (all roles can check health)
		AdminHealth, AdminStatus,
		// Orchestrator: the consumer slice (17 verbs). End users are CONSUMERS of
		// the orchestration data plane: they spawn/observe/close their own
		// sessions, prompt and run inference, submit and track their own tasks
		// (create/read/list/decompose) and jobs (start/read/list/cancel), read
		// their own artifacts, and read workflows + health. This is the read +
		// own-work slice, NOT the operator/fleet slice.
		//
		// Deliberately EXCLUDED from user (these are operator+ verbs):
		//   - Worker verbs: task_claim, task_readmit, task_block, task_complete,
		//     artifact_submit (a user
		//     submits work, it does not execute the worker side of the queue).
		//   - Agent fleet verbs: agent_register/read/list/remove/heartbeat (fleet
		//     membership and lifecycle are operations, not consumption).
		//   - Shared cache: cache_read, cache_write (cross-session shared state).
		//   - Fleet control: broadcast (drain/reload the fleet).
		//   - Authoring/topology: workflow_write, discover, hook, backend_list.
		OrchestratorSpawn, OrchestratorPrompt,
		OrchestratorStatus, OrchestratorClose,
		OrchestratorList, OrchestratorTaskCreate,
		OrchestratorTaskRead, OrchestratorTaskList,
		OrchestratorTaskDecompose, OrchestratorArtifactRead,
		OrchestratorJobStart, OrchestratorJobRead,
		OrchestratorJobList, OrchestratorJobCancel,
		OrchestratorWorkflowRead, OrchestratorHealth,
		OrchestratorInference,
	)
}

// registerAuditorGrants places the Auditor built-in role's default
// permissions. Auditors have read-only access to audit logs and system
// state. They cannot modify any state (FIPS 140-3 AS10.10).
func registerAuditorGrants() {
	RegisterRoleGrant(RoleAuditor,
		// Audit visibility (the core auditor capability)
		AuditRead, AuditQuery, AuditStream,
		// System state (read-only)
		AdminHealth, AdminStatus, AdminPeers,
		// User, role, cert, tenant listing (metadata only — no writes)
		UsersList, UsersRead,
		RolesList, RolesRead, PermissionsList,
		CertsList, CertsRead,
		// Hardware crypto backend inventory (read-only visibility)
		BackendsList, BackendsRead,
		// PIN / PIV / password state (read-only visibility)
		PINRead, PIVRead, PasswordsRead,
		TenantsList, TenantsRead,
		// Key metadata (public fields only — no cryptographic operations)
		KeysList, KeysGet,
		// Key escrow inventory (metadata only — escrow ID, purpose, timestamps;
		// never key material). Recovering or revoking an escrow is not
		// visibility and stays off the Auditor role.
		EscrowList,
		// Ceremony status
		CeremonyStatus,
		// Auth factor listing
		AuthFactorList,
		// Orchestrator: read-only METADATA slice (11 verbs). Auditors observe the
		// shape and state of the orchestration data plane — session status/list,
		// task read/list, job read/list, workflow read, agent read/list, backend
		// list, and health — for compliance review.
		//
		// Deliberately EXCLUDED (FIPS 140-3 AS10.10, auditors are metadata-only):
		//   - orchestrator:artifact_read and orchestrator:cache_read return tenant
		//     DATA PAYLOADS, not system state; auditors never read payload data.
		//   - All write/exec verbs (inference, prompt, spawn, task_create, ...) —
		//     auditors cannot modify any state.
		OrchestratorStatus, OrchestratorList,
		OrchestratorTaskRead, OrchestratorTaskList,
		OrchestratorJobRead, OrchestratorJobList,
		OrchestratorWorkflowRead, OrchestratorAgentRead,
		OrchestratorAgentList, OrchestratorBackendList,
		OrchestratorHealth,
		// Certificate authority hierarchy and ACME registration metadata
		// (read-only). Auditors review what the CA issued and to whom; EAB
		// reads return metadata only, never the one-time MAC key.
		CARead, CAList,
		ACMEAccountsRead, ACMEAccountsList,
		ACMEOrdersRead, ACMEOrdersList,
		ACMEEABRead, ACMEEABList,
	)
}

func init() {
	// Duty declarations MUST precede the grants: RegisterRoleGrant validates
	// every Security Officer grant against the permission's declared duty, so
	// a grant registered before its declaration would panic. The ordering is
	// an explicit call rather than a reliance on this package's file order.
	registerBuiltInDuties()

	registerSOGrants()
	registerAdminGrants()
	registerOperatorGrants()
	registerUserGrants()
	registerAuditorGrants()
}
