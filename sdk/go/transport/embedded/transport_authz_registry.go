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

package embedded

import (
	"context"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// This file is the embedded transport's single operation->permission table
// for the AMBIENT-SELF surface: every operation whose request names no
// target tenant/user/role of its own, and which is therefore authorized
// against the caller's own tenant exactly as the equivalent network route
// authorizes it (see pkg/api/quic/dispatch.go, the one dispatcher whose
// method->permission table is dumped in full below each handle/handleIf
// call -- this registry is a transcription of that table onto the
// embedded method names, not a second table invented independently).
//
// The tenant-administration surface (tenant.go, tenant_seal.go) and the
// identity surface needing user/role target resolution
// (transport_identity_authz.go) are NOT here: both need a target other than
// the caller's own tenant and are authorized individually, the same way
// AuthorizeTenantTarget/AuthorizeTarget(UserTarget|RoleTarget) authorize
// them on every network transport.
//
// A method whose permission carries ScopeCross (admin:*, backup:*,
// rebalance:*, seal:*, ceremony:*, the tenant-provisioning permissions) is
// still authorized here with SelfTarget: AuthorizeTarget resolves that to
// the caller's own (ambient) tenant and the RBAC authorizer's ScopeCross
// rule ignores it, admitting only a system-tenant identity regardless --
// see pkg/auth/rbac.RBACAuthorizer.Authorize's ScopeCross case. Using the
// SAME call for both scopes, rather than branching on scope here, is what
// keeps this table a pure transcription: the scope lives in
// pkg/permission, once.
var operationPermissions = map[string]permission.Permission{
	// KV
	"Put":              permission.KVPut,
	"Get":              permission.KVGet,
	"Delete":           permission.KVDelete,
	"List":             permission.KVList,
	"Scan":             permission.KVRange,
	"Exists":           permission.KVExists,
	"PutIfAbsent":      permission.KVPut,
	"CompareAndDelete": permission.KVDelete,
	"CompareAndSwap":   permission.KVPut,

	// MVCC
	"PutWithLease":      permission.MVCCPutWithLease,
	"GetRevision":       permission.MVCCGetRevision,
	"GetAtRevision":     permission.MVCCGetAtRevision,
	"Range":             permission.MVCCRange,
	"Compact":           permission.MVCCCompact,
	"CurrentRevision":   permission.MVCCCurrentRevision,
	"RestoreToRevision": permission.MVCCRestore,

	// Transaction / batch
	"ExecuteTransaction": permission.TransactionExecute,
	"ExecuteBatch":       permission.BatchExecute,
	"ImportFile":         permission.BatchExecute,

	// Chunked import session (ImportFile's own four steps, decomposed for a
	// caller that drives them directly instead of through the ImportFile
	// helper): the SAME permission.BatchExecute ImportFile already checks,
	// matching native QUIC's batch.ImportBegin/ImportAppend/ImportCommit/
	// ImportAbort registrations exactly (pkg/api/quic/dispatch.go).
	"ImportBegin":  permission.BatchExecute,
	"ImportAppend": permission.BatchExecute,
	"ImportCommit": permission.BatchExecute,
	"ImportAbort":  permission.BatchExecute,

	// Watch (both the exact and the prefix verb authorize the SAME
	// permission.WatchWatch, matching QUIC's handleStreamOnly pair).
	"Watch":       permission.WatchWatch,
	"WatchPrefix": permission.WatchWatch,

	// Lease
	"LeaseGrant":      permission.LeaseGrant,
	"LeaseRevoke":     permission.LeaseRevoke,
	"LeaseKeepAlive":  permission.LeaseKeepAlive,
	"LeaseTimeToLive": permission.LeaseTTL,
	"LeaseList":       permission.LeaseList,

	// Locks
	"LockAcquire": permission.LocksAcquire,
	"LockTryLock": permission.LocksAcquire,
	"LockRelease": permission.LocksRelease,
	"LockExtend":  permission.LocksExtend,
	"LockStatus":  permission.LocksStatus,
	"LockList":    permission.LocksList,

	// Index
	"IndexCreate": permission.IndexCreate,
	"IndexDrop":   permission.IndexDrop,
	"IndexQuery":  permission.IndexQuery,
	"IndexScan":   permission.IndexScan,
	"IndexList":   permission.IndexList,

	// Timeseries
	"TSWrite":        permission.TSWrite,
	"TSWriteBatch":   permission.TSWriteBatch,
	"TSQuery":        permission.TSQuery,
	"TSAggregate":    permission.TSAggregate,
	"TSWritePoint":   permission.TSWritePoint,
	"TSRichQuery":    permission.TSRichQuery,
	"TSDeleteSeries": permission.TSDeleteSeries,
	"TSGetRetention": permission.TSGetRetention,
	"TSSetRetention": permission.TSSetRetention,
	"TSListMetrics":  permission.TSListMetrics,

	// Vector
	"CreateNamespace":       permission.VectorNamespaceCreate,
	"DeleteNamespace":       permission.VectorNamespaceDelete,
	"ListNamespaces":        permission.VectorNamespaceList,
	"GetNamespaceInfo":      permission.VectorNamespaceInfo,
	"UpsertVector":          permission.VectorUpsert,
	"GetVector":             permission.VectorGet,
	"DeleteVector":          permission.VectorDelete,
	"BatchUpsert":           permission.VectorBatchUpsert,
	"BatchDeleteVectors":    permission.VectorBatchDelete,
	"ListVectors":           permission.VectorList,
	"UpdateMetadata":        permission.VectorUpdateMetadata,
	"SearchSimilar":         permission.VectorSearchSimilar,
	"SearchByMetadata":      permission.VectorSearchByMetadata,
	"HybridSearch":          permission.VectorHybridSearch,
	"SearchByID":            permission.VectorSearchByID,
	"RebuildIndex":          permission.VectorRebuildIndex,
	"GetRebuildProgress":    permission.VectorGetRebuildProg,
	"GetIndexStats":         permission.VectorGetIndexStats,
	"CompactNamespace":      permission.VectorCompact,
	"GetCompactionProgress": permission.VectorGetCompactProg,

	// Collections
	"CreateCollection":  permission.CollectionsCreate,
	"GetCollectionInfo": permission.CollectionsRead,
	"ListCollections":   permission.CollectionsList,
	"DropCollection":    permission.CollectionsDrop,
	"InsertDocument":    permission.CollectionsInsert,
	"GetDocument":       permission.CollectionsRead,
	"UpdateDocument":    permission.CollectionsUpdate,
	"DeleteDocument":    permission.CollectionsDelete,
	"ListDocuments":     permission.CollectionsList,

	// GetUserByCertThumbprint names no tenant of its own -- the lookup
	// itself resolves it -- matching QUIC's user.GetByCertThumbprint
	// registration, which carries no identityTargets override either.
	"GetUserByCertThumbprint": permission.UsersGetByCertThumbprint,

	// Permission catalog reads.
	"ListPermissions":    permission.PermissionsList,
	"ResolvePermissions": permission.PermissionsResolve,

	// Certs. RevokeCert, ListCerts, IssueCert, EnrollCert and
	// the TrustAnchor{Add,List,Remove} trio each name a tenant in their
	// request and are authorized individually against it
	// (authorizeCertTenant/authorizeCertRevoke in transport_auth.go,
	// mirroring pkg/auth/middleware/cert_authz.go exactly). ExportCert
	// (and certs:export) are gone entirely: no node may receive a private
	// key to encode on a caller's behalf; a PKCS#12 archive is now built
	// LOCALLY, by whoever already holds the key.

	// RAG
	"RAGIngestDocument": permission.RAGIngest,
	"RAGQuery":          permission.RAGQuery,
	"RAGDeleteDocument": permission.RAGDeleteDocument,
	"RAGListDocuments":  permission.RAGListDocuments,

	// Embedding
	"Embed":               permission.EmbeddingEmbed,
	"BatchEmbed":          permission.EmbeddingEmbed,
	"ListEmbeddingModels": permission.EmbeddingListModels,

	// Auth factors (pkg/service.AuthFactorServicer -- the challenge/response
	// factor lifecycle; NOT the per-user enrollment family below).
	"EnrollFactor":    permission.AuthFactorEnroll,
	"ChallengeFactor": permission.AuthFactorChallenge,
	"VerifyFactor":    permission.AuthFactorVerify,
	"ListFactors":     permission.AuthFactorList,
	"RevokeFactor":    permission.AuthFactorRevoke,

	// User auth factors (M14 per-user MFA enrollment).
	"AddAuthFactor":     permission.UserAuthFactorsAdd,
	"ListAuthFactors":   permission.UserAuthFactorsList,
	"GetAuthFactor":     permission.UserAuthFactorsGet,
	"RemoveAuthFactor":  permission.UserAuthFactorsRemove,
	"RotateAuthFactor":  permission.UserAuthFactorsRotate,
	"RelabelAuthFactor": permission.UserAuthFactorsRelabel,
	"LoginAuthFactor":   permission.UserAuthFactorsLogin,

	// Credentials
	"CredentialSubmit":          permission.CredentialsSubmit,
	"CredentialStrategy":        permission.CredentialsStrategy,
	"CredentialDelete":          permission.CredentialsDelete,
	"CredentialList":            permission.CredentialsList,
	"CredentialRotate":          permission.CredentialsRotate,
	"CredentialRetrieveVersion": permission.CredentialsRetrieveVersion,
	"CredentialListVersions":    permission.CredentialsListVersions,
	"CredentialGCExpired":       permission.CredentialsGCExpired,
	"CredentialStore":           permission.CredentialsStore,
	"CredentialRetrieve":        permission.CredentialsRetrieve,

	// Tenant DEK keyring. Every method acts on the caller's own tenant only
	// (ScopeSelf; the keyring service itself refuses a system-tenant
	// identity acting on another tenant, closing the lift AuthorizeTarget
	// alone would still allow), matching the QUIC dek.* comment exactly.
	"TenantDEKDecrypt": permission.DEKDecrypt,
	"TenantDEKEncrypt": permission.DEKEncrypt,
	"TenantDEKRetire":  permission.DEKRetire,
	"TenantDEKRewrap":  permission.DEKRewrap,
	"TenantDEKRotate":  permission.DEKRotate,
	"TenantDEKStatus":  permission.DEKStatus,

	// Backup (ScopeCross: a backup is of shards, holding every tenant's data).
	"CreateBackup":  permission.BackupCreate,
	"ListBackups":   permission.BackupList,
	"GetBackup":     permission.BackupRead,
	"DeleteBackup":  permission.BackupDelete,
	"RestoreBackup": permission.BackupRestore,
	"VerifyBackup":  permission.BackupVerify,

	// Cluster rebalance (ScopeCross: moves and describes the cluster's shards).
	"CreateRebalancePlan": permission.RebalancePlan,
	"ExecuteRebalance":    permission.RebalanceExecute,
	"GetShardStats":       permission.RebalanceStats,
	"GetImbalanceReport":  permission.RebalanceReport,

	// Database management. No cross-tenant target: a database belongs to
	// the caller's own tenant, exactly as QUIC's db.* registrations carry
	// no identityTargets entry.
	"CreateDatabase":           permission.DatabasesCreate,
	"GetDatabase":              permission.DatabasesRead,
	"ListDatabases":            permission.DatabasesList,
	"DropDatabase":             permission.DatabasesDelete,
	"ScaleDatabase":            permission.DatabasesScale,
	"UpdateDatabaseProtocol":   permission.DatabasesUpdate,
	"UpdateDatabaseEtcd":       permission.DatabasesUpdate,
	"UpdateDatabaseRaft":       permission.DatabasesUpdate,
	"UpdateDatabaseBarrier":    permission.DatabasesUpdate,
	"UpdateDatabaseConnection": permission.DatabasesUpdate,
	"UpdateDatabaseSQL":        permission.DatabasesUpdate,
	// Setting a compaction policy is compacting on a schedule (QUIC's own
	// comment for db.UpdateCompaction).
	"UpdateDatabaseCompaction": permission.MVCCCompact,
	"SealDatabase":             permission.DatabasesSeal,
	"UnsealDatabase":           permission.DatabasesUnseal,

	// Database barrier (Shamir custody of a database barrier). The claim and
	// quorum handshake is IdentityGated, not here -- see
	// transport_database_barrier.go.
	"DatabaseBarrierStatus":           permission.DatabasesBarrierStatus,
	"DatabaseBarrierOfficerRegister":  permission.DatabasesOfficerRegister,
	"DatabaseBarrierOfficerList":      permission.DatabasesOfficerList,
	"DatabaseBarrierShamirInitialize": permission.DatabasesInitializeShamir,
	"DatabaseBarrierRekeyStart":       permission.DatabasesRekey,
	"DatabaseBarrierOfficerRevoke":    permission.DatabasesOfficerRevoke,
	"DatabaseBarrierOfficerUnenrol":   permission.DatabasesOfficerUnenrol,

	// SQL (read-through over the KV range surface; mirrors REST's
	// "POST /sql/query" and the gRPC SQLQuery permission option).
	"Query": permission.KVRange,

	// Ceremony. Share holders claim their envelopes through the barrier
	// holder methods, not here.
	"CeremonyStatus": permission.CeremonyStatus,

	// Node barrier (the system barrier itself -- a platform singleton no
	// tenant owns). Share-claim and quorum routes are IdentityGated, not
	// here -- see transport_barrier.go.
	"BarrierSeal":                 permission.SealSeal,
	"BarrierClusterUnseal":        permission.SealUnseal,
	"BarrierStatus":               permission.SealStatus,
	"BarrierEnableEncryption":     permission.SealEnableEncryption,
	"BarrierGenerateRecoveryKeys": permission.SealGenerateRecovery,
	"BarrierDeleteRecoveryKeys":   permission.SealGenerateRecovery,
	"BarrierHasRecoveryKeys":      permission.SealStatus,
	"OfficerList":                 permission.SealOfficerList,
	"BarrierRekeyStart":           permission.SealRekey,
	"BarrierOfficerRevoke":        permission.SealOfficerRevoke,

	// Admin / cluster membership.
	"Status":            permission.AdminStatus,
	"Peers":             permission.AdminPeers,
	"AddMember":         permission.AdminAddMember,
	"RemoveMember":      permission.AdminRemoveMember,
	"RequestSnapshot":   permission.AdminSnapshot,
	"Drain":             permission.AdminDrain,
	"Capabilities":      permission.AdminCapabilities,
	"StartShard":        permission.AdminStartShard,
	"StopShard":         permission.AdminStopShard,
	"GetShardState":     permission.AdminShardState,
	"GetTopology":       permission.AdminTopology,
	"NodeCompaction":    permission.AdminNodeCompaction,
	"SetNodeCompaction": permission.AdminNodeCompaction,
	"MintJoinToken":     permission.ClusterJoinToken,

	// Audit (the read-side sub-client; EmitAudit is handled on its own in
	// audit.go, because its tenant is stamped from the caller, not read
	// from a request field).
	"AuditClient.QueryAudit":  permission.AuditQuery,
	"AuditClient.VerifyChain": permission.AuditVerify,

	// FactorClient (userID-scoped MFA enrollment lifecycle; the same
	// underlying userAuthFactorSvc the "User auth factors" block above
	// authorizes for UserAuthFactorClient, so the SAME permissions --
	// matching the network's userauthfactor.* route family exactly. Target
	// resolution is NOT SelfTarget, unlike this map's other entries: see
	// authorizeUserTarget calls in factor.go.
	//
	// "Verify" has no dedicated permission of its own: it is the
	// challenge-response check a login flow performs against an
	// already-enrolled factor, so it is gated on user_authfactors:login,
	// the same permission userauthfactor.Login checks on every network
	// transport.

	// VolumePoolClient (ScopeCross: a volume pool is platform storage
	// topology, not a tenant's; docs/volume-pools/api.md). This is a
	// transcription of pkg/api/quic/dispatch.go's volumepool.* registration
	// block, the SAME table every other entry here transcribes: ListPools,
	// GetPool and ListPoolTenants carry the read permissions
	// (ListPoolTenants shares GetPool's volume_pools:read, matching
	// volumepool.ListTenants there), and Create/Update/DeletePool carry
	// their own. AuthorizeTarget resolves SelfTarget to the caller's own
	// tenant and the RBAC authorizer's ScopeCross rule admits only a
	// system-tenant identity regardless, exactly like every other
	// ScopeCross entry in this table.
	"ListPools":       permission.VolumePoolList,
	"GetPool":         permission.VolumePoolRead,
	"CreatePool":      permission.VolumePoolCreate,
	"UpdatePool":      permission.VolumePoolUpdate,
	"DeletePool":      permission.VolumePoolDelete,
	"ListPoolTenants": permission.VolumePoolRead,

	// RBACClient.ListPermissions resolves an ARBITRARY identity's effective
	// permissions (the identity argument, not necessarily the caller) --
	// the same introspection permissions:resolve already gates for the
	// caller's own tenant (ResolvePermissions above), required here too
	// before handing back someone else's grants.
	"RBACClient.ListPermissions": permission.PermissionsResolve,

	// PermissionClient (the permission-catalog sub-client; read-only,
	// mirrors role.ListPermissions's permissions:list on every network
	// transport. Lookup/LookupScope have no network route of their own --
	// embedded/SDK-only catalog conveniences -- so they are gated on the
	// same catalog-read permission as List.
	"PermissionClient.List":        permission.PermissionsList,
	"PermissionClient.Lookup":      permission.PermissionsList,
	"PermissionClient.LookupScope": permission.PermissionsList,

	// SealStore* (a generic secret-store abstraction over a seal strategy's
	// own state, reached only through the embedded SDK -- no network route
	// exists). seal:store_manage is ScopeCross like every other node-barrier
	// permission, so SelfTarget here resolves to the caller's own tenant and
	// the RBAC authorizer's ScopeCross rule admits only a system-tenant
	// identity regardless, exactly like the "Node barrier" block above.
	"SealStorePut":    permission.SealStoreManage,
	"SealStoreGet":    permission.SealStoreManage,
	"SealStoreDelete": permission.SealStoreManage,
	"SealStoreList":   permission.SealStoreManage,
	"SealStoreReseal": permission.SealStoreManage,
	"SealStoreStatus": permission.SealStoreManage,
}

// requireIdentity is the floor for the share-claim and quorum handshakes
// (permission.IdentityGated): no permission check, just an authenticated
// caller -- the holder authenticates through the factor it presents (a
// certificate, a PIN), not a role. A nil t.authMW authorizes everything.
func (t *Transport) requireIdentity(ctx context.Context) error {
	if t.authMW == nil {
		return nil
	}
	return t.authMW.AuthorizeTarget(ctx, permission.IdentityGated, authmw.SelfTarget())
}

// authorizeCertTenant gates a certificate operation on the tenant the
// REQUEST names (generate, list), not the tenant the caller happens to be
// in -- AuthMiddleware.AuthorizeCertTenant, the one cross-transport home of
// the rule REST and gRPC call through the same method. A nil t.authMW
// authorizes everything.
func (t *Transport) authorizeCertTenant(ctx context.Context, tenantID string, perm permission.Permission) error {
	if t.authMW == nil {
		return nil
	}
	return t.authMW.AuthorizeCertTenant(ctx, tenantID, perm)
}

// authorizeCertRevoke gates a revocation on the tenant that owns the serial,
// falling back to the CA-operator authority when the serial owns no record
// here (deny-by-serial) -- AuthMiddleware.AuthorizeCertRevoke. A nil
// t.authMW authorizes everything.
func (t *Transport) authorizeCertRevoke(ctx context.Context, serial string) error {
	if t.authMW == nil {
		return nil
	}
	return t.authMW.AuthorizeCertRevoke(ctx, serial, t.certProv)
}

// UnregisteredOperationError is returned by authorize when called with a
// method key that operationPermissions does not declare. It is a
// programming-error floor, not a caller-facing denial: authorize is called
// only with literal string keys this package itself controls, and every
// one of them is proven present in operationPermissions by
// TestTransportClientAuthorizationSurface_CoversEveryMethod and
// TestSubClientAuthorizationSurface_CoversEveryMethod, so this should be
// unreachable in a build that passes them. It exists so a FUTURE call that
// slips past those gates (a typo, a key removed from the table but not from
// its call site) fails CLOSED in production rather than silently admitting
// every caller -- the fail-open the no-op return used to be, before the
// owner's "one pluggable auth chain" security review asked for enforcement
// rather than a declaration that could drift from it.
type UnregisteredOperationError struct {
	Method string
}

// Error returns a human-readable description.
func (e *UnregisteredOperationError) Error() string {
	return "embedded: " + e.Method + " has no registered permission; refusing rather than admitting an uncontrolled operation"
}

// authorize is the generic RBAC check every ambient-self operation above
// runs: AuthMiddleware.AuthorizeTarget against the caller's own tenant --
// the SAME decision, through the SAME pkg/auth/middleware call and the SAME
// pkg/permission entry, that the network transports make for the SAME
// operation. method must be a key of operationPermissions.
//
// A nil t.authMW authorizes everything, matching a network transport run
// with no AuthMiddleware (--auth-enabled=false): RBAC stays opt-in. Once an
// AuthMiddleware IS installed, an unregistered method key fails CLOSED
// (UnregisteredOperationError), not open: see
// TestOperationPermissions_UnknownKeyFailsClosed.
func (t *Transport) authorize(ctx context.Context, method string) error {
	if t.authMW == nil {
		return nil
	}
	perm, ok := operationPermissions[method]
	if !ok {
		return &UnregisteredOperationError{Method: method}
	}
	return t.authMW.AuthorizeTarget(ctx, perm, authmw.SelfTarget())
}
