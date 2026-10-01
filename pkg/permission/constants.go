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
// KV permissions
// ---------------------------------------------------------------------------

const (
	// KVPut grants write access to key-value pairs.
	KVPut Permission = "kv:put"
	// KVGet grants read access to key-value pairs.
	KVGet Permission = "kv:get"
	// KVDelete grants delete access to key-value pairs.
	KVDelete Permission = "kv:delete"
	// KVList grants list access to key-value pairs.
	KVList Permission = "kv:list"
	// KVExists grants existence check access to key-value pairs.
	KVExists Permission = "kv:exists"
	// KVRange grants range scan access to key-value pairs.
	KVRange Permission = "kv:range"
)

// ---------------------------------------------------------------------------
// MVCC permissions
// ---------------------------------------------------------------------------

const (
	// MVCCPutWithLease grants write access with lease attachment.
	MVCCPutWithLease Permission = "mvcc:put_with_lease"
	// MVCCGetRevision grants read access to a specific revision.
	MVCCGetRevision Permission = "mvcc:get_revision"
	// MVCCGetAtRevision grants read access at a historical revision.
	MVCCGetAtRevision Permission = "mvcc:get_at_revision"
	// MVCCRange grants range scan access over MVCC data.
	MVCCRange Permission = "mvcc:range"
	// MVCCCompact grants compaction access to MVCC data.
	MVCCCompact Permission = "mvcc:compact"
	// MVCCCurrentRevision grants access to query the current revision.
	MVCCCurrentRevision Permission = "mvcc:current_revision"
	// MVCCRestore grants selective point-in-time restore: returning a
	// bounded, explicitly scoped set of user keys to the value they held at
	// an earlier MVCC revision by proposing new writes.
	//
	// It is a privileged BULK MUTATION, not a read, and is separated from
	// mvcc:get_at_revision for that reason: reading history is an ordinary
	// data-plane capability, while rewriting a range of live keys from
	// history can undo a deliberate change across many keys at once. It is
	// granted only to Admin and Operator — never to User, Auditor, or the
	// Security Officer (who holds no data-plane verb at all).
	//
	// Unlike mvcc:compact this is NOT store-global: a restore names the keys
	// it writes and is confined to the caller's tenant and database namespace
	// by server.namespacedMVCCService, so it keeps the default ScopeSelf.
	MVCCRestore Permission = "mvcc:restore"
)

// ---------------------------------------------------------------------------
// Transaction permissions
// ---------------------------------------------------------------------------

const (
	// TransactionExecute grants access to execute compare-and-swap transactions.
	TransactionExecute Permission = "transaction:execute"
)

// ---------------------------------------------------------------------------
// Batch permissions
// ---------------------------------------------------------------------------

const (
	// BatchExecute grants access to execute batch operations.
	BatchExecute Permission = "batch:execute"
)

// ---------------------------------------------------------------------------
// Watch permissions
// ---------------------------------------------------------------------------

const (
	// WatchWatch grants access to watch a single key for changes.
	WatchWatch Permission = "watch:watch"
	// WatchWatchPrefix grants access to watch a key prefix for changes.
	WatchWatchPrefix Permission = "watch:watch_prefix"
)

// ---------------------------------------------------------------------------
// Lease permissions
// ---------------------------------------------------------------------------

const (
	// LeaseGrant grants access to create new leases.
	LeaseGrant Permission = "lease:grant"
	// LeaseRevoke grants access to revoke existing leases.
	LeaseRevoke Permission = "lease:revoke"
	// LeaseKeepAlive grants access to refresh lease TTL.
	LeaseKeepAlive Permission = "lease:keepalive"
	// LeaseTTL grants access to query lease time-to-live.
	LeaseTTL Permission = "lease:ttl"
	// LeaseList grants access to list all active leases.
	LeaseList Permission = "lease:list"
)

// ---------------------------------------------------------------------------
// Index permissions
// ---------------------------------------------------------------------------

const (
	// IndexCreate grants access to create secondary indexes.
	IndexCreate Permission = "index:create"
	// IndexDrop grants access to drop secondary indexes.
	IndexDrop Permission = "index:drop"
	// IndexQuery grants access to query secondary indexes.
	IndexQuery Permission = "index:query"
	// IndexScan grants access to scan secondary indexes.
	IndexScan Permission = "index:scan"
)

// ---------------------------------------------------------------------------
// TimeSeries permissions
// ---------------------------------------------------------------------------

const (
	// TSWrite grants access to write time-series data points.
	TSWrite Permission = "timeseries:write"
	// TSWriteBatch grants access to write time-series data points in batches.
	TSWriteBatch Permission = "timeseries:write_batch"
	// TSQuery grants access to query time-series data.
	TSQuery Permission = "timeseries:query"
	// TSAggregate grants access to aggregate time-series data.
	TSAggregate Permission = "timeseries:aggregate"
	// TSWritePoint grants access to write a single validated data point.
	TSWritePoint Permission = "timeseries:write_point"
	// TSRichQuery grants access to perform rich queries with aggregation and grouping.
	TSRichQuery Permission = "timeseries:rich_query"
	// TSDeleteSeries grants access to delete time-series data.
	TSDeleteSeries Permission = "timeseries:delete_series"
	// TSGetRetention grants access to read retention policies.
	TSGetRetention Permission = "timeseries:get_retention"
	// TSSetRetention grants access to set retention policies.
	TSSetRetention Permission = "timeseries:set_retention"
	// TSListMetrics grants access to list metric names.
	TSListMetrics Permission = "timeseries:list_metrics"
)

// ---------------------------------------------------------------------------
// Vector permissions
// ---------------------------------------------------------------------------

const (
	// VectorNamespaceCreate grants access to create vector namespaces.
	VectorNamespaceCreate Permission = "vector:namespace_create"
	// VectorNamespaceDelete grants access to delete vector namespaces.
	VectorNamespaceDelete Permission = "vector:namespace_delete"
	// VectorNamespaceList grants access to list vector namespaces.
	VectorNamespaceList Permission = "vector:namespace_list"
	// VectorNamespaceInfo grants access to query vector namespace info.
	VectorNamespaceInfo Permission = "vector:namespace_info"
	// VectorUpsert grants access to upsert vectors.
	VectorUpsert Permission = "vector:upsert"
	// VectorGet grants access to get vectors by ID.
	VectorGet Permission = "vector:get"
	// VectorDelete grants access to delete vectors.
	VectorDelete Permission = "vector:delete"
	// VectorBatchUpsert grants access to batch upsert vectors.
	VectorBatchUpsert Permission = "vector:batch_upsert"
	// VectorBatchDelete grants access to batch delete vectors.
	VectorBatchDelete Permission = "vector:batch_delete"
	// VectorList grants access to list vectors.
	VectorList Permission = "vector:list"
	// VectorUpdateMetadata grants access to update vector metadata.
	VectorUpdateMetadata Permission = "vector:update_metadata"
	// VectorSearchSimilar grants access to search for similar vectors.
	VectorSearchSimilar Permission = "vector:search_similar"
	// VectorSearchByMetadata grants access to search vectors by metadata.
	VectorSearchByMetadata Permission = "vector:search_by_metadata"
	// VectorHybridSearch grants access to hybrid vector/metadata search.
	VectorHybridSearch Permission = "vector:hybrid_search"
	// VectorSearchByID grants access to search vectors by ID.
	VectorSearchByID Permission = "vector:search_by_id"
	// VectorRebuildIndex grants access to rebuild vector indexes.
	VectorRebuildIndex Permission = "vector:rebuild_index"
	// VectorGetRebuildProg grants access to query vector index rebuild progress.
	VectorGetRebuildProg Permission = "vector:get_rebuild_progress"
	// VectorGetIndexStats grants access to query vector index statistics.
	VectorGetIndexStats Permission = "vector:get_index_stats"
	// VectorCompact grants access to compact vector storage.
	VectorCompact Permission = "vector:compact"
	// VectorGetCompactProg grants access to query vector compaction progress.
	VectorGetCompactProg Permission = "vector:get_compaction_progress"
)

// ---------------------------------------------------------------------------
// Admin permissions
// ---------------------------------------------------------------------------

const (
	// AdminHealth grants access to health check endpoints.
	AdminHealth Permission = "admin:health"
	// AdminStatus grants access to cluster status endpoints.
	AdminStatus Permission = "admin:status"
	// AdminPeers grants access to peer listing endpoints.
	AdminPeers Permission = "admin:peers"
	// AdminAddMember grants access to add cluster members.
	AdminAddMember Permission = "admin:add_member"
	// AdminRemoveMember grants access to remove cluster members.
	AdminRemoveMember Permission = "admin:remove_member"
	// AdminSnapshot grants access to trigger cluster snapshots.
	AdminSnapshot Permission = "admin:snapshot"
	// AdminDrain grants access to drain a node for maintenance.
	AdminDrain Permission = "admin:drain"
	// AdminCapabilities grants access to query node capabilities.
	AdminCapabilities Permission = "admin:capabilities"
)

// ---------------------------------------------------------------------------
// User management permissions
// ---------------------------------------------------------------------------

const (
	// UsersCreate grants access to create user accounts.
	UsersCreate Permission = "users:create"
	// UsersRead grants access to read user account details.
	UsersRead Permission = "users:read"
	// UsersUpdate grants access to update user account details.
	UsersUpdate Permission = "users:update"
	// UsersDelete grants access to delete user accounts.
	UsersDelete Permission = "users:delete"
	// UsersList grants access to list user accounts.
	UsersList Permission = "users:list"
	// UsersEnable grants access to enable user accounts.
	UsersEnable Permission = "users:enable"
	// UsersDisable grants access to disable user accounts.
	UsersDisable Permission = "users:disable"
	// UsersGetByCertThumbprint grants access to resolve a user by an mTLS
	// certificate SPKI thumbprint. It is a read, distinct from UsersRead so the
	// identity-lookup surface can be granted independently.
	UsersGetByCertThumbprint Permission = "users:get_by_cert_thumbprint"
	// UsersAddCertBinding grants access to bind an mTLS certificate thumbprint
	// to a user. It mutates the user's identity record and is a sensitive
	// operation (it controls which certificates authenticate as the user), so
	// it is a distinct permission rather than folded into UsersUpdate.
	UsersAddCertBinding Permission = "users:add_cert_binding"
	// UsersRemoveCertBinding grants access to WITHDRAW an mTLS certificate
	// thumbprint from a user — the only way to unenroll a key, and the
	// remediation for one an operator believes is compromised.
	//
	// It is separate from UsersAddCertBinding, not folded into it, because the
	// two are not the same authority. Enrolment only ever NARROWS an account:
	// the first binding closes it from "any certificate naming it" to "only
	// these keys". Withdrawal can WIDEN it — removing the last binding returns
	// the account to admitting any certificate whose CN and OU name it (see
	// pkg/auth/middleware.checkCertBinding). A grant that lets a caller tighten
	// an account's admission must not silently also let them loosen it, which
	// is what a shared permission would mean. It is also the ordinary shape of
	// an enrolment service: binding a certificate at invitation redemption is a
	// routine automated act, withdrawing an operator's binding is not.
	UsersRemoveCertBinding Permission = "users:remove_cert_binding"
)

// ---------------------------------------------------------------------------
// Role management permissions
// ---------------------------------------------------------------------------

const (
	// RolesCreate grants access to create roles.
	RolesCreate Permission = "roles:create"
	// RolesRead grants access to read role details.
	RolesRead Permission = "roles:read"
	// RolesUpdate grants access to update role details.
	RolesUpdate Permission = "roles:update"
	// RolesDelete grants access to delete roles.
	RolesDelete Permission = "roles:delete"
	// RolesList grants access to list roles.
	RolesList Permission = "roles:list"
	// RolesAssign grants access to assign roles to users.
	RolesAssign Permission = "roles:assign"
	// RolesUnassign grants access to unassign roles from users.
	RolesUnassign Permission = "roles:unassign"
	// PermissionsList grants access to list available permission categories.
	PermissionsList Permission = "permissions:list"
	// PermissionsResolve grants access to resolve the effective (unioned)
	// permissions for a set of roles.
	PermissionsResolve Permission = "permissions:resolve"
)

// ---------------------------------------------------------------------------
// Certificate management permissions
// ---------------------------------------------------------------------------

const (
	// CertsGenerate grants access to generate TLS certificates.
	CertsGenerate Permission = "certs:generate"
	// CertsExport grants access to export TLS certificates as PKCS#12.
	CertsExport Permission = "certs:export"
	// CertsRevoke grants access to revoke TLS certificates.
	CertsRevoke Permission = "certs:revoke"
	// CertsList grants access to list TLS certificates.
	CertsList Permission = "certs:list"
	// CertsRead grants access to read a single certificate's metadata.
	CertsRead Permission = "certs:read"
)

// ---------------------------------------------------------------------------
// Keys permissions (cryptographic key operations)
// ---------------------------------------------------------------------------

const (
	// KeysSign grants access to sign data using a key.
	KeysSign Permission = "keys:sign"
	// KeysVerify grants access to verify signatures.
	KeysVerify Permission = "keys:verify"
	// KeysEncrypt grants access to encrypt data using a key.
	KeysEncrypt Permission = "keys:encrypt"
	// KeysDecrypt grants access to decrypt data using a key.
	KeysDecrypt Permission = "keys:decrypt"
	// KeysWrap grants access to wrap (export-encrypt) a key.
	KeysWrap Permission = "keys:wrap"
	// KeysUnwrap grants access to unwrap (import-decrypt) a key.
	KeysUnwrap Permission = "keys:unwrap"
	// KeysCreate grants access to create new cryptographic keys.
	KeysCreate Permission = "keys:create"
	// KeysRotate grants access to rotate existing keys.
	KeysRotate Permission = "keys:rotate"
	// KeysDelete grants access to delete keys.
	KeysDelete Permission = "keys:delete"
	// KeysList grants access to list key metadata.
	KeysList Permission = "keys:list"
	// KeysGet grants access to read key metadata (public fields only).
	KeysGet Permission = "keys:get"
	// KeysImport grants access to import external key material under management.
	KeysImport Permission = "keys:import"
	// KeysExport grants access to export managed key material.
	KeysExport Permission = "keys:export"
	// KeysDerive grants access to derive key material from a key (KDF / ECDH).
	KeysDerive Permission = "keys:derive"
)

// ---------------------------------------------------------------------------
// Hardware backend permissions (TPM 2.0 / PKCS#11 / cloud KMS inventory)
// ---------------------------------------------------------------------------

const (
	// BackendsList grants access to list the configured hardware crypto backends.
	BackendsList Permission = "backends:list"
	// BackendsRead grants access to read a single hardware crypto backend's details.
	BackendsRead Permission = "backends:read"
)

// ---------------------------------------------------------------------------
// PKCS#11 PIN management permissions
// ---------------------------------------------------------------------------

const (
	// PINRead grants access to read PIN state (lockout status, verify PIN).
	PINRead Permission = "pin:read"
	// PINWrite grants access to set, change, or reset SO/user PINs and lockout.
	PINWrite Permission = "pin:write"
)

// ---------------------------------------------------------------------------
// PIV (smart-card) slot permissions
// ---------------------------------------------------------------------------

const (
	// PIVRead grants access to read PIV slot metadata and certificates.
	PIVRead Permission = "piv:read"
	// PIVWrite grants access to store, generate, or import PIV slot material.
	PIVWrite Permission = "piv:write"
	// PIVUse grants access to use a PIV slot key for cryptographic operations.
	PIVUse Permission = "piv:use"
	// PIVDelete grants access to delete PIV slot certificates.
	PIVDelete Permission = "piv:delete"
)

// ---------------------------------------------------------------------------
// Password vault permissions
// ---------------------------------------------------------------------------

const (
	// PasswordsRead grants access to read or list stored passwords.
	PasswordsRead Permission = "passwords:read"
	// PasswordsWrite grants access to add or update stored passwords.
	PasswordsWrite Permission = "passwords:write"
	// PasswordsDelete grants access to delete stored passwords.
	PasswordsDelete Permission = "passwords:delete"
	// PasswordsGenerate grants access to generate new passwords.
	PasswordsGenerate Permission = "passwords:generate"
	// PasswordsLock grants access to lock the password vault.
	PasswordsLock Permission = "passwords:lock"
	// PasswordsUnlock grants access to unlock the password vault.
	PasswordsUnlock Permission = "passwords:unlock"
)

// ---------------------------------------------------------------------------
// SSH key vault permissions
// ---------------------------------------------------------------------------

const (
	// SSHRead grants access to read, list, and export SSH keys, known hosts,
	// and authorized keys. Export returns private key material for a key whose
	// backend allows it, so this is not a metadata-only verb.
	SSHRead Permission = "ssh:read"
	// SSHWrite grants access to generate or import SSH keys, sign with them,
	// and add known-host or authorized-key entries.
	SSHWrite Permission = "ssh:write"
	// SSHDelete grants access to delete SSH keys and remove known-host or
	// authorized-key entries.
	SSHDelete Permission = "ssh:delete"
)

// ---------------------------------------------------------------------------
// Seal management permissions
// ---------------------------------------------------------------------------

const (
	// SealBootstrap grants access to bootstrap the seal subsystem.
	SealBootstrap Permission = "seal:bootstrap"
	// SealUnseal grants access to unseal the node.
	SealUnseal Permission = "seal:unseal"
	// SealSeal grants access to seal the node.
	SealSeal Permission = "seal:seal"
	// SealStatus grants access to query seal status.
	SealStatus Permission = "seal:status"
	// SealRotateRootKey grants access to rotate the barrier root key (SO only).
	SealRotateRootKey Permission = "seal:rotate_root_key"
	// SealRekey grants access to rekey Shamir shares (SO only).
	SealRekey Permission = "seal:rekey"
	// SealEnableEncryption grants access to migrate a previously-unencrypted
	// barrier to a keyed strategy (enable encryption after the fact). System
	// scope is SO/Admin only; tenant scope additionally requires ownership of a
	// tenant with its own dedicated barrier.
	SealEnableEncryption Permission = "seal:enable_encryption"
	// SealGenerateRecovery grants access to generate offline recovery keys (SO only).
	SealGenerateRecovery Permission = "seal:generate_recovery"
	// SealRecoverWithKeys grants access to recover the barrier using offline recovery keys.
	SealRecoverWithKeys Permission = "seal:recover_with_keys"
	// SealInitializeShamir grants access to initialize Shamir secret sharing (SO only).
	SealInitializeShamir Permission = "seal:initialize_shamir"
	// BarrierUnsealShare grants access to submit a single Shamir share toward unseal quorum.
	// This is the sole permission granted to the Custodian role.
	BarrierUnsealShare Permission = "barrier:unseal-share"

	// SealOfficerRegister grants a system admin the ability to register a Security
	// Officer for the system barrier.
	SealOfficerRegister Permission = "seal:officer_register"

	// SealOfficerList grants access to list registered Security Officers and their
	// claim status for the system barrier.
	SealOfficerList Permission = "seal:officer_list"

	// SealSubmitSealVote grants a Security Officer the ability to submit a seal vote
	// (share) to collectively seal a Shamir-protected system barrier.
	SealSubmitSealVote Permission = "seal:submit_seal_vote"
)

// ---------------------------------------------------------------------------
// Ceremony permissions
// ---------------------------------------------------------------------------

const (
	// CeremonyInitialize grants access to initialize the key ceremony (SO only).
	CeremonyInitialize Permission = "ceremony:initialize"
	// CeremonyEnrollCustodian grants access to enroll a custodian during ceremony (SO only).
	CeremonyEnrollCustodian Permission = "ceremony:enroll_custodian"
	// CeremonyRevokeCustodian grants access to revoke a custodian (SO only).
	CeremonyRevokeCustodian Permission = "ceremony:revoke_custodian"
	// CeremonyClaimCert grants access to claim a certificate during ceremony (enrolled officer).
	CeremonyClaimCert Permission = "ceremony:claim_cert"
	// CeremonyClaimShareBegin grants access to begin claiming a Shamir share (enrolled custodian).
	CeremonyClaimShareBegin Permission = "ceremony:claim_share_begin"
	// CeremonyClaimShareComplete grants access to complete claiming a Shamir share (enrolled custodian).
	CeremonyClaimShareComplete Permission = "ceremony:claim_share_complete"
	// CeremonySignCSR grants access to sign a CSR during ceremony (SO only).
	CeremonySignCSR Permission = "ceremony:sign_csr"
	// CeremonyStatus grants access to query ceremony state (SO, Admin, Operator, Auditor).
	CeremonyStatus Permission = "ceremony:status"
)

// ---------------------------------------------------------------------------
// Identity-gated sentinel
// ---------------------------------------------------------------------------

// IdentityGated is a sentinel permission used as a transport permission-map
// value for endpoints whose authorization is enforced at the handler layer
// rather than by the RBAC policy decision point. The auth middleware requires
// an authenticated identity for these endpoints and then passes the request
// through without a role-permission check; the handler performs the binding
// authorization (officer/custodian registry lookup, certificate-thumbprint
// match, and nonce-signature or factor verification). This is the Vault
// posture for share-claim flows: possession of the enrolled credential is the
// authorization, and no RBAC user record is required — Security Officers are
// registered in the ceremony registry during the sealed window, before the
// Raft-backed user store exists.
//
// IdentityGated is deliberately NOT registered in the permission registry: it
// can never be granted to a role, never appears in All(), ByResource, or the
// UI catalog, and grant-validation paths reject it as unregistered.
const IdentityGated Permission = "identity:gated"

// ---------------------------------------------------------------------------
// Audit permissions
// ---------------------------------------------------------------------------

const (
	// AuditRead grants access to read audit events.
	AuditRead Permission = "audit:read"
	// AuditQuery grants access to query audit events with filtering.
	AuditQuery Permission = "audit:query"
	// AuditStream grants access to stream audit events in real time.
	AuditStream Permission = "audit:stream"
)

// ---------------------------------------------------------------------------
// AuthFactor permissions (stubs — implementation in Phase 3)
// ---------------------------------------------------------------------------

const (
	// AuthFactorEnroll grants access to enroll an authentication factor.
	AuthFactorEnroll Permission = "authfactor:enroll"
	// AuthFactorChallenge grants access to request an auth factor challenge.
	AuthFactorChallenge Permission = "authfactor:challenge"
	// AuthFactorVerify grants access to verify an auth factor response.
	AuthFactorVerify Permission = "authfactor:verify"
	// AuthFactorList grants access to list enrolled auth factors.
	AuthFactorList Permission = "authfactor:list"
	// AuthFactorRevoke grants access to revoke an enrolled auth factor.
	AuthFactorRevoke Permission = "authfactor:revoke"
)

// ---------------------------------------------------------------------------
// Tenant management permissions
// ---------------------------------------------------------------------------

const (
	// TenantsCreate grants access to create tenants.
	TenantsCreate Permission = "tenants:create"
	// TenantsRead grants access to read tenant details.
	TenantsRead Permission = "tenants:read"
	// TenantsUpdate grants access to update tenant details.
	TenantsUpdate Permission = "tenants:update"
	// TenantsDelete grants access to delete tenants.
	TenantsDelete Permission = "tenants:delete"
	// TenantsList grants access to list tenants.
	TenantsList Permission = "tenants:list"

	// TenantsSetIsolation grants access to change a tenant's data-isolation
	// mode. This is a privileged provisioning operation distinct from generic
	// TenantsUpdate (display-name changes), because the isolation tier governs
	// where and how a tenant's data is physically stored.
	TenantsSetIsolation Permission = "tenants:set_isolation"

	// TenantsSetEntitlements grants access to change a tenant's feature
	// entitlements (which isolation tiers and dedicated-storage features the
	// tenant's plan permits). This is a system-admin plan-management operation.
	TenantsSetEntitlements Permission = "tenants:set_entitlements"

	// TenantsSetQuotas grants access to change a tenant's engine-level resource
	// quotas (max databases, keys, storage bytes, etc.). This is a privileged
	// plan-management operation exercisable only by system administrators.
	TenantsSetQuotas Permission = "tenants:set_quotas"

	// TenantBarrierInit grants a tenant admin the ability to initialize
	// their own tenant's barrier with a caller-supplied passphrase. This
	// permission is tenant-scoped (ScopeTenantOnly): it is exercisable only
	// by an admin acting on their own tenant, and is denied to the system
	// tenant — the platform operator does not hold tenant key material.
	TenantBarrierInit Permission = "tenant_barrier:init"

	// TenantBarrierUnseal grants a tenant admin the ability to unseal their
	// own tenant's barrier. Tenant-scoped (ScopeTenantOnly).
	TenantBarrierUnseal Permission = "tenant_barrier:unseal"

	// TenantBarrierSeal grants a tenant admin the ability to seal their own
	// tenant's barrier. Tenant-scoped (ScopeTenantOnly).
	TenantBarrierSeal Permission = "tenant_barrier:seal"

	// TenantBarrierStatus grants a tenant admin the ability to read the
	// sealed/initialized state and active strategy of their own tenant's
	// barrier. Tenant-scoped (ScopeTenantOnly): reading another tenant's
	// barrier state leaks isolation metadata, so the system tenant and any
	// cross-tenant access are denied.
	TenantBarrierStatus Permission = "tenant_barrier:status"

	// TenantBarrierOfficerRegister grants a tenant-admin the ability to register
	// a Security Officer for their tenant's barrier. Tenant-scoped (ScopeTenantOnly).
	TenantBarrierOfficerRegister Permission = "tenant_barrier:officer_register"

	// TenantBarrierOfficerList grants access to list Security Officers for a tenant
	// barrier. Tenant-scoped (ScopeTenantOnly).
	TenantBarrierOfficerList Permission = "tenant_barrier:officer_list"

	// TenantBarrierClaimShare grants a tenant Security Officer the ability to claim
	// their Shamir share from the tenant barrier. Tenant-scoped (ScopeTenantOnly).
	// The handler additionally enforces that the caller's mTLS cert thumbprint matches
	// the officer record (identity-gated, not just role-gated).
	TenantBarrierClaimShare Permission = "tenant_barrier:claim_share"

	// TenantBarrierSealVote grants a tenant Security Officer the ability to submit
	// a seal vote for their tenant's barrier. Tenant-scoped (ScopeTenantOnly).
	TenantBarrierSealVote Permission = "tenant_barrier:seal_vote"

	// TenantBarrierEnableEncryption grants access to migrate a tenant's dedicated
	// barrier from the none (unencrypted) strategy to a keyed strategy in place.
	// This is a privileged operation equivalent to SealEnableEncryption scoped to
	// a single tenant's barrier. Tenant-scoped (ScopeTenantOnly).
	TenantBarrierEnableEncryption Permission = "tenant_barrier:enable_encryption"
)

// Database management permissions. A database is a logical engine/storage
// container; these gate its lifecycle and configuration.
const (
	DatabasesCreate Permission = "databases:create"
	DatabasesRead   Permission = "databases:read"
	DatabasesList   Permission = "databases:list"
	DatabasesDelete Permission = "databases:delete"
	// DatabasesUpdate covers all database configuration updates (barrier,
	// connection, etcd, protocol, raft).
	DatabasesUpdate Permission = "databases:update"

	// DatabasesCreateRaft gates creation of a dedicated Raft-backed database.
	// It is checked in addition to DatabasesCreate for raft-backed databases,
	// since a dedicated Raft group is a privileged provisioning operation.
	DatabasesCreateRaft Permission = "databases:create_raft"

	// DatabasesCreateVolume gates creation of a database on a dedicated storage
	// volume. It is checked in addition to DatabasesCreate for volume-isolated
	// databases.
	DatabasesCreateVolume Permission = "databases:create_volume"

	// DatabasesScale gates changing the replication factor of an existing
	// dedicated-raft database -- a privileged infra operation distinct from
	// DatabasesUpdate (it consumes quota and mutates cluster placement/Raft
	// membership, unlike a config-field update).
	DatabasesScale Permission = "databases:scale"

	// Database encryption-barrier lifecycle. Sealing/unsealing a database's
	// at-rest barrier is a sensitive custody operation, gated separately from
	// generic configuration updates.
	DatabasesSeal          Permission = "databases:seal"
	DatabasesUnseal        Permission = "databases:unseal"
	DatabasesBarrierStatus Permission = "databases:barrier_status"
)

// Collection management permissions. Collections are document containers; the
// document-level actions gate reads/writes of their contents.
const (
	CollectionsCreate Permission = "collections:create"
	CollectionsRead   Permission = "collections:read"
	CollectionsList   Permission = "collections:list"
	CollectionsDrop   Permission = "collections:drop"
	CollectionsInsert Permission = "collections:insert"
	CollectionsUpdate Permission = "collections:update"
	CollectionsDelete Permission = "collections:delete"
)

// Distributed lock permissions.
const (
	LocksAcquire Permission = "locks:acquire"
	LocksRelease Permission = "locks:release"
	LocksExtend  Permission = "locks:extend"
	LocksStatus  Permission = "locks:status"
	LocksList    Permission = "locks:list"
)

// Named-credential rotation permissions. Credentials custody secret material,
// so write/rotate/delete are sensitive and distinct from reads.
const (
	CredentialsSubmit          Permission = "credentials:submit"
	CredentialsStrategy        Permission = "credentials:strategy"
	CredentialsList            Permission = "credentials:list"
	CredentialsDelete          Permission = "credentials:delete"
	CredentialsRotate          Permission = "credentials:rotate"
	CredentialsGCExpired       Permission = "credentials:gc_expired"
	CredentialsRetrieveVersion Permission = "credentials:retrieve_version"
	CredentialsListVersions    Permission = "credentials:list_versions"
	// CredentialsStore persists a credential value; CredentialsRetrieve reads
	// the current value (sensitive: returns secret material).
	CredentialsStore    Permission = "credentials:store"
	CredentialsRetrieve Permission = "credentials:retrieve"
)

// Cluster shard-control permissions. Starting/stopping shards and reading
// shard/topology state are cluster-admin operations.
const (
	AdminStartShard Permission = "admin:start_shard"
	AdminStopShard  Permission = "admin:stop_shard"
	AdminShardState Permission = "admin:shard_state"
	AdminTopology   Permission = "admin:topology"
)

// Per-user authentication-factor permissions (the user-side factor lifecycle:
// WebAuthn/FIDO2/PIV enrollments bound to a user).
const (
	UserAuthFactorsAdd     Permission = "user_authfactors:add"
	UserAuthFactorsGet     Permission = "user_authfactors:get"
	UserAuthFactorsList    Permission = "user_authfactors:list"
	UserAuthFactorsRemove  Permission = "user_authfactors:remove"
	UserAuthFactorsRotate  Permission = "user_authfactors:rotate"
	UserAuthFactorsRelabel Permission = "user_authfactors:relabel"
	UserAuthFactorsLogin   Permission = "user_authfactors:login"
)

// Tenant DEK keyring permissions. The keyring holds a tenant's versioned
// data encryption keys; the keys never leave the server. Every verb acts on
// the caller's OWN tenant only — the service refuses another tenant even for
// a system-tenant caller — so all six are ScopeSelf (registered in init).
const (
	// DEKEncrypt seals caller data under the tenant's current key version.
	DEKEncrypt Permission = "dek:encrypt"
	// DEKDecrypt opens data sealed under any live key version.
	DEKDecrypt Permission = "dek:decrypt"
	// DEKRewrap re-seals a ciphertext under the current key version without
	// returning its plaintext.
	DEKRewrap Permission = "dek:rewrap"
	// DEKRotate adds a key version and makes it current.
	DEKRotate Permission = "dek:rotate"
	// DEKRetire destroys an old key version's key.
	DEKRetire Permission = "dek:retire"
	// DEKStatus reads the keyring's version metadata (never key material).
	DEKStatus Permission = "dek:status"
)

// IndexList grants listing of secondary index names (a read, distinct from
// IndexQuery which reads index contents).
const IndexList Permission = "index:list"

// Backup/restore permissions. Restore is the most destructive (it overwrites
// shard state), so it is distinct from create/read.
const (
	BackupCreate  Permission = "backup:create"
	BackupList    Permission = "backup:list"
	BackupRead    Permission = "backup:read"
	BackupDelete  Permission = "backup:delete"
	BackupRestore Permission = "backup:restore"
	BackupVerify  Permission = "backup:verify"
)

// Cluster rebalance permissions. Execute physically moves shards between nodes
// and is distinct from the read-only stats/plan/report operations.
const (
	RebalanceStats   Permission = "rebalance:stats"
	RebalancePlan    Permission = "rebalance:plan"
	RebalanceExecute Permission = "rebalance:execute"
	RebalanceReport  Permission = "rebalance:report"
)

// Embedding permissions (text→vector embedding service).
const (
	EmbeddingEmbed      Permission = "embedding:embed"
	EmbeddingListModels Permission = "embedding:list_models"
)

// RAG (retrieval-augmented generation) permissions.
const (
	RAGIngest         Permission = "rag:ingest"
	RAGQuery          Permission = "rag:query"
	RAGListDocuments  Permission = "rag:list_documents"
	RAGDeleteDocument Permission = "rag:delete_document"
)

// ---------------------------------------------------------------------------
// Categories (for UI rendering)
// ---------------------------------------------------------------------------

// allVectorPermissions returns all vector permissions in a single slice.
func allVectorPermissions() []Permission {
	return []Permission{
		VectorNamespaceCreate, VectorNamespaceDelete, VectorNamespaceList, VectorNamespaceInfo,
		VectorUpsert, VectorGet, VectorDelete, VectorBatchUpsert, VectorBatchDelete, VectorList,
		VectorUpdateMetadata, VectorSearchSimilar, VectorSearchByMetadata, VectorHybridSearch,
		VectorSearchByID, VectorRebuildIndex, VectorGetRebuildProg, VectorGetIndexStats,
		VectorCompact, VectorGetCompactProg,
	}
}

// categories is the authoritative grouping of permissions for UI rendering.
var categories = []Category{
	{
		Name:  "KV Service",
		Group: "Data Services",
		Permissions: []Permission{
			KVPut, KVGet, KVDelete, KVList, KVExists, KVRange,
		},
	},
	{
		Name:  "MVCC Service",
		Group: "Data Services",
		Permissions: []Permission{
			MVCCPutWithLease, MVCCGetRevision, MVCCGetAtRevision,
			MVCCRange, MVCCCompact, MVCCCurrentRevision,
			MVCCRestore,
		},
	},
	{
		Name:  "Transaction Service",
		Group: "Data Services",
		Permissions: []Permission{
			TransactionExecute,
		},
	},
	{
		Name:  "Batch Service",
		Group: "Data Services",
		Permissions: []Permission{
			BatchExecute,
		},
	},
	{
		Name:  "Watch Service",
		Group: "Data Services",
		Permissions: []Permission{
			WatchWatch, WatchWatchPrefix,
		},
	},
	{
		Name:  "Lease Service",
		Group: "Data Services",
		Permissions: []Permission{
			LeaseGrant, LeaseRevoke, LeaseKeepAlive, LeaseTTL, LeaseList,
		},
	},
	{
		Name:  "Index Service",
		Group: "Data Services",
		Permissions: []Permission{
			IndexCreate, IndexDrop, IndexQuery, IndexScan,
		},
	},
	{
		Name:  "TimeSeries Service",
		Group: "Data Services",
		Permissions: []Permission{
			TSWrite, TSWriteBatch, TSQuery, TSAggregate,
			TSWritePoint, TSRichQuery, TSDeleteSeries,
			TSGetRetention, TSSetRetention, TSListMetrics,
		},
	},
	{
		Name:        "Vector Service",
		Group:       "Data Services",
		Permissions: allVectorPermissions(),
	},
	{
		Name:  "Admin Service",
		Group: "Admin Services",
		Permissions: []Permission{
			AdminHealth, AdminStatus, AdminPeers, AdminAddMember,
			AdminRemoveMember, AdminSnapshot, AdminDrain, AdminCapabilities,
		},
	},
	{
		Name:  "User Management",
		Group: "Management Services",
		Permissions: []Permission{
			UsersCreate, UsersRead, UsersUpdate, UsersDelete,
			UsersList, UsersEnable, UsersDisable,
			UsersGetByCertThumbprint, UsersAddCertBinding,
			UsersRemoveCertBinding,
		},
	},
	{
		Name:  "Role Management",
		Group: "Management Services",
		Permissions: []Permission{
			RolesCreate, RolesRead, RolesUpdate, RolesDelete,
			RolesList, RolesAssign, RolesUnassign, PermissionsList,
			PermissionsResolve,
		},
	},
	{
		Name:  "Certificate Management",
		Group: "Management Services",
		Permissions: []Permission{
			CertsGenerate, CertsExport, CertsRevoke, CertsList, CertsRead,
		},
	},
	{
		Name:  "Keys",
		Group: "Cryptographic Services",
		Permissions: []Permission{
			KeysSign, KeysVerify, KeysEncrypt, KeysDecrypt,
			KeysWrap, KeysUnwrap, KeysCreate, KeysRotate,
			KeysDelete, KeysList, KeysGet, KeysImport, KeysExport, KeysDerive,
		},
	},
	{
		Name:  "Hardware Backends",
		Group: "Cryptographic Services",
		Permissions: []Permission{
			BackendsList, BackendsRead,
		},
	},
	{
		Name:  "PKCS#11 PIN",
		Group: "Cryptographic Services",
		Permissions: []Permission{
			PINRead, PINWrite,
		},
	},
	{
		Name:  "PIV Smart Card",
		Group: "Cryptographic Services",
		Permissions: []Permission{
			PIVRead, PIVWrite, PIVUse, PIVDelete,
		},
	},
	{
		Name:  "Password Vault",
		Group: "Security Services",
		Permissions: []Permission{
			PasswordsRead, PasswordsWrite, PasswordsDelete,
			PasswordsGenerate, PasswordsLock, PasswordsUnlock,
		},
	},
	{
		Name:  "SSH Key Vault",
		Group: "Security Services",
		Permissions: []Permission{
			SSHRead, SSHWrite, SSHDelete,
		},
	},
	{
		Name:  "Seal Management",
		Group: "Admin Services",
		Permissions: []Permission{
			SealBootstrap, SealUnseal, SealSeal, SealStatus,
			SealRotateRootKey, SealRekey, SealEnableEncryption,
			SealGenerateRecovery, SealRecoverWithKeys, SealInitializeShamir,
			BarrierUnsealShare, SealOfficerRegister, SealOfficerList,
			SealSubmitSealVote,
		},
	},
	{
		Name:  "Ceremony",
		Group: "Admin Services",
		Permissions: []Permission{
			CeremonyInitialize, CeremonyEnrollCustodian, CeremonyRevokeCustodian,
			CeremonyClaimCert, CeremonyClaimShareBegin, CeremonyClaimShareComplete,
			CeremonySignCSR, CeremonyStatus,
		},
	},
	{
		Name:  "Audit",
		Group: "Admin Services",
		Permissions: []Permission{
			AuditRead, AuditQuery, AuditStream,
		},
	},
	{
		Name:  "Authentication Factors",
		Group: "Admin Services",
		Permissions: []Permission{
			AuthFactorEnroll, AuthFactorChallenge, AuthFactorVerify,
			AuthFactorList, AuthFactorRevoke,
		},
	},
	{
		Name:  "Tenant Management",
		Group: "Management Services",
		Permissions: []Permission{
			TenantsCreate, TenantsRead, TenantsUpdate, TenantsDelete, TenantsList,
			TenantsSetIsolation, TenantsSetEntitlements, TenantsSetQuotas,
			TenantsListCross, TenantsCreateCross, TenantsApprove,
			TenantBarrierInit, TenantBarrierUnseal, TenantBarrierSeal, TenantBarrierStatus,
			TenantBarrierOfficerRegister, TenantBarrierOfficerList,
			TenantBarrierClaimShare, TenantBarrierSealVote,
			TenantBarrierEnableEncryption,
		},
	},
	{
		Name:  "Volume Pool Management",
		Group: "Management Services",
		Permissions: []Permission{
			VolumePoolCreate, VolumePoolList, VolumePoolRead,
			VolumePoolUpdate, VolumePoolDelete,
		},
	},
	{
		Name:  "Registration Configuration",
		Group: "Management Services",
		Permissions: []Permission{
			RegistrationConfigRead, RegistrationConfigUpdate,
		},
	},
	{
		Name:  "Cross-Tenant Audit",
		Group: "Admin Services",
		Permissions: []Permission{
			AuditReadCross,
		},
	},
	{
		Name:  "CA Operator",
		Group: "Admin Services",
		Permissions: []Permission{
			CertsRevokeCross,
		},
	},
	{
		Name:  "Database Management",
		Group: "Management Services",
		Permissions: []Permission{
			DatabasesCreate, DatabasesRead, DatabasesList, DatabasesDelete, DatabasesUpdate,
			DatabasesCreateRaft, DatabasesCreateVolume, DatabasesScale,
			DatabasesSeal, DatabasesUnseal, DatabasesBarrierStatus,
		},
	},
	{
		Name:  "Collection Management",
		Group: "Management Services",
		Permissions: []Permission{
			CollectionsCreate, CollectionsRead, CollectionsList, CollectionsDrop,
			CollectionsInsert, CollectionsUpdate, CollectionsDelete,
		},
	},
	{
		Name:  "Lock Management",
		Group: "Data Services",
		Permissions: []Permission{
			LocksAcquire, LocksRelease, LocksExtend, LocksStatus, LocksList,
		},
	},
	{
		Name:  "Credential Management",
		Group: "Security Services",
		Permissions: []Permission{
			CredentialsSubmit, CredentialsStrategy, CredentialsList, CredentialsDelete,
			CredentialsRotate, CredentialsGCExpired, CredentialsRetrieveVersion, CredentialsListVersions,
			CredentialsStore, CredentialsRetrieve,
		},
	},
	{
		Name:  "Shard Control",
		Group: "Admin Services",
		Permissions: []Permission{
			AdminStartShard, AdminStopShard, AdminShardState, AdminTopology,
		},
	},
	{
		Name:  "Backup & Restore",
		Group: "Admin Services",
		Permissions: []Permission{
			BackupCreate, BackupList, BackupRead, BackupDelete, BackupRestore, BackupVerify,
		},
	},
	{
		Name:  "Cluster Rebalance",
		Group: "Admin Services",
		Permissions: []Permission{
			RebalanceStats, RebalancePlan, RebalanceExecute, RebalanceReport,
		},
	},
	{
		Name:  "User Authentication Factors",
		Group: "Security Services",
		Permissions: []Permission{
			UserAuthFactorsAdd, UserAuthFactorsGet, UserAuthFactorsList, UserAuthFactorsRemove,
			UserAuthFactorsRotate, UserAuthFactorsRelabel, UserAuthFactorsLogin,
		},
	},
	{
		Name:  "Tenant Data Encryption Keys",
		Group: "Security Services",
		Permissions: []Permission{
			DEKEncrypt, DEKDecrypt, DEKRewrap, DEKRotate, DEKRetire, DEKStatus,
		},
	},
	{
		Name:  "Secondary Index (list)",
		Group: "Data Services",
		Permissions: []Permission{
			IndexList,
		},
	},
	{
		Name:  "Embedding",
		Group: "AI Services",
		Permissions: []Permission{
			EmbeddingEmbed, EmbeddingListModels,
		},
	},
	{
		Name:  "RAG",
		Group: "AI Services",
		Permissions: []Permission{
			RAGIngest, RAGQuery, RAGListDocuments, RAGDeleteDocument,
		},
	},
}

// credentialPermissions returns every named-credential permission. The
// credential store is a single system-scoped barrier keyed by credential name
// with NO tenant namespacing (see pkg/credentials/versioned.go), so these
// permissions MUST be system-scoped: a tenant-local role must never be able to
// satisfy them and thereby read or mutate another tenant's (or the platform's)
// credential material. Tagging them ScopeCross confines every credential
// operation to the structurally privileged system tenant.
func credentialPermissions() []Permission {
	return []Permission{
		CredentialsSubmit, CredentialsStrategy, CredentialsList, CredentialsDelete,
		CredentialsRotate, CredentialsGCExpired, CredentialsRetrieveVersion,
		CredentialsListVersions, CredentialsStore, CredentialsRetrieve,
	}
}

func init() {
	// Register all permissions from all categories into the global registry.
	for _, cat := range categories {
		Register(cat.Permissions...)
	}
	// The credential store is not tenant-namespaced; confine every credential
	// permission to the system tenant so a tenant-scoped role can never satisfy
	// it (latent cross-tenant hole — no built-in tenant role grants these).
	RegisterScope(ScopeCross, credentialPermissions()...)
	// The cert-binding PAIR declares its scope explicitly rather than taking
	// the ScopeSelf default. Both resolve to ScopeSelf either way, so this
	// changes no decision today — it states one.
	//
	// LookupScope returns ScopeSelf for any permission nobody tagged, so an
	// omission and a decision are indistinguishable in the registry, and the
	// omission is the one that admits. These two decide which certificates
	// authenticate as an account, so "nobody has thought about this yet" is not
	// an acceptable reading of their scope. The declaration is deliberately
	// narrow: it says these two were considered, and says nothing about any
	// other untagged permission in this package.
	RegisterScope(ScopeSelf, UsersAddCertBinding, UsersRemoveCertBinding)
	// The DEK keyring verbs declare ScopeSelf explicitly, for the reason the
	// cert-binding pair does: they gate key custody, so their scope is a
	// decision, not a default. ScopeSelf lets a system-tenant identity pass
	// the authorizer for another tenant; the DEK service closes that by
	// acting on the caller's own tenant only.
	RegisterScope(ScopeSelf, DEKEncrypt, DEKDecrypt, DEKRewrap, DEKRotate, DEKRetire, DEKStatus)
}
