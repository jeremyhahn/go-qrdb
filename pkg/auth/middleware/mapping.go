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

package middleware

import (
	"strings"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// grpcPermissions maps gRPC full method names to the permission required
// to invoke them. Methods not present in this map are considered public
// (no authorization required).
var grpcPermissions = map[string]permission.Permission{

	// KV
	pb.QRDBService_Put_FullMethodName:    permission.KVPut,
	pb.QRDBService_Get_FullMethodName:    permission.KVGet,
	pb.QRDBService_Delete_FullMethodName: permission.KVDelete,
	pb.QRDBService_List_FullMethodName:   permission.KVList,
	pb.QRDBService_Scan_FullMethodName:   permission.KVRange,

	// MVCC
	pb.QRDBService_PutWithLease_FullMethodName:    permission.MVCCPutWithLease,
	pb.QRDBService_GetRevision_FullMethodName:     permission.MVCCGetRevision,
	pb.QRDBService_GetAtRevision_FullMethodName:   permission.MVCCGetAtRevision,
	pb.QRDBService_Range_FullMethodName:           permission.MVCCRange,
	pb.QRDBService_Compact_FullMethodName:         permission.MVCCCompact,
	pb.QRDBService_CurrentRevision_FullMethodName: permission.MVCCCurrentRevision,

	// Transaction
	pb.QRDBService_Transaction_FullMethodName: permission.TransactionExecute,

	// Batch
	pb.QRDBService_Batch_FullMethodName: permission.BatchExecute,

	// Watch
	pb.QRDBService_Watch_FullMethodName: permission.WatchWatch,

	// Lease
	pb.QRDBService_LeaseGrant_FullMethodName:      permission.LeaseGrant,
	pb.QRDBService_LeaseRevoke_FullMethodName:     permission.LeaseRevoke,
	pb.QRDBService_LeaseKeepAlive_FullMethodName:  permission.LeaseKeepAlive,
	pb.QRDBService_LeaseTimeToLive_FullMethodName: permission.LeaseTTL,
	pb.QRDBService_LeaseList_FullMethodName:       permission.LeaseList,

	// Index
	pb.QRDBService_IndexCreate_FullMethodName: permission.IndexCreate,
	pb.QRDBService_IndexDrop_FullMethodName:   permission.IndexDrop,
	pb.QRDBService_IndexQuery_FullMethodName:  permission.IndexQuery,
	pb.QRDBService_IndexScan_FullMethodName:   permission.IndexScan,

	// TimeSeries
	pb.QRDBService_TSWrite_FullMethodName:        permission.TSWrite,
	pb.QRDBService_TSWriteBatch_FullMethodName:   permission.TSWriteBatch,
	pb.QRDBService_TSQuery_FullMethodName:        permission.TSQuery,
	pb.QRDBService_TSAggregate_FullMethodName:    permission.TSAggregate,
	pb.QRDBService_TSWritePoint_FullMethodName:   permission.TSWritePoint,
	pb.QRDBService_TSRichQuery_FullMethodName:    permission.TSRichQuery,
	pb.QRDBService_TSDeleteSeries_FullMethodName: permission.TSDeleteSeries,
	pb.QRDBService_TSGetRetention_FullMethodName: permission.TSGetRetention,
	pb.QRDBService_TSSetRetention_FullMethodName: permission.TSSetRetention,
	pb.QRDBService_TSListMetrics_FullMethodName:  permission.TSListMetrics,

	// Vector
	pb.QRDBService_VectorCreateNamespace_FullMethodName:       permission.VectorNamespaceCreate,
	pb.QRDBService_VectorDeleteNamespace_FullMethodName:       permission.VectorNamespaceDelete,
	pb.QRDBService_VectorListNamespaces_FullMethodName:        permission.VectorNamespaceList,
	pb.QRDBService_VectorGetNamespaceInfo_FullMethodName:      permission.VectorNamespaceInfo,
	pb.QRDBService_VectorUpsert_FullMethodName:                permission.VectorUpsert,
	pb.QRDBService_VectorGet_FullMethodName:                   permission.VectorGet,
	pb.QRDBService_VectorDelete_FullMethodName:                permission.VectorDelete,
	pb.QRDBService_VectorBatchUpsert_FullMethodName:           permission.VectorBatchUpsert,
	pb.QRDBService_VectorBatchDelete_FullMethodName:           permission.VectorBatchDelete,
	pb.QRDBService_VectorList_FullMethodName:                  permission.VectorList,
	pb.QRDBService_VectorUpdateMetadata_FullMethodName:        permission.VectorUpdateMetadata,
	pb.QRDBService_VectorSearchSimilar_FullMethodName:         permission.VectorSearchSimilar,
	pb.QRDBService_VectorSearchByMetadata_FullMethodName:      permission.VectorSearchByMetadata,
	pb.QRDBService_VectorHybridSearch_FullMethodName:          permission.VectorHybridSearch,
	pb.QRDBService_VectorSearchByID_FullMethodName:            permission.VectorSearchByID,
	pb.QRDBService_VectorRebuildIndex_FullMethodName:          permission.VectorRebuildIndex,
	pb.QRDBService_VectorGetRebuildProgress_FullMethodName:    permission.VectorGetRebuildProg,
	pb.QRDBService_VectorGetIndexStats_FullMethodName:         permission.VectorGetIndexStats,
	pb.QRDBService_VectorCompactNamespace_FullMethodName:      permission.VectorCompact,
	pb.QRDBService_VectorGetCompactionProgress_FullMethodName: permission.VectorGetCompactProg,

	// Admin
	pb.QRDBService_Health_FullMethodName:          permission.AdminHealth,
	pb.QRDBService_Status_FullMethodName:          permission.AdminStatus,
	pb.QRDBService_Peers_FullMethodName:           permission.AdminPeers,
	pb.QRDBService_AddMember_FullMethodName:       permission.AdminAddMember,
	pb.QRDBService_RemoveMember_FullMethodName:    permission.AdminRemoveMember,
	pb.QRDBService_RequestSnapshot_FullMethodName: permission.AdminSnapshot,
	pb.QRDBService_Drain_FullMethodName:           permission.AdminDrain,
	pb.QRDBService_Capabilities_FullMethodName:    permission.AdminCapabilities,

	// User management
	pb.QRDBService_CreateUser_FullMethodName:  permission.UsersCreate,
	pb.QRDBService_GetUser_FullMethodName:     permission.UsersRead,
	pb.QRDBService_UpdateUser_FullMethodName:  permission.UsersUpdate,
	pb.QRDBService_DeleteUser_FullMethodName:  permission.UsersDelete,
	pb.QRDBService_ListUsers_FullMethodName:   permission.UsersList,
	pb.QRDBService_EnableUser_FullMethodName:  permission.UsersEnable,
	pb.QRDBService_DisableUser_FullMethodName: permission.UsersDisable,

	// Cert-binding: resolving a user by SPKI thumbprint is a read; binding a
	// thumbprint to a user mutates that user's identity record and therefore
	// requires the same write permission as any other user update. Without
	// these mappings the gRPC interceptor would treat them as public methods
	// (authenticated but unauthorized), allowing any caller to bind an
	// arbitrary thumbprint to an arbitrary user.
	pb.QRDBService_GetUserByCertThumbprint_FullMethodName: permission.UsersGetByCertThumbprint,
	pb.QRDBService_AddCertBinding_FullMethodName:          permission.UsersAddCertBinding,

	// Role management
	pb.QRDBService_CreateRole_FullMethodName:             permission.RolesCreate,
	pb.QRDBService_GetRole_FullMethodName:                permission.RolesRead,
	pb.QRDBService_GetRoleByName_FullMethodName:          permission.RolesRead,
	pb.QRDBService_UpdateRole_FullMethodName:             permission.RolesUpdate,
	pb.QRDBService_DeleteRole_FullMethodName:             permission.RolesDelete,
	pb.QRDBService_ListRoles_FullMethodName:              permission.RolesList,
	pb.QRDBService_AssignRole_FullMethodName:             permission.RolesAssign,
	pb.QRDBService_UnassignRole_FullMethodName:           permission.RolesUnassign,
	pb.QRDBService_ListPermissions_FullMethodName:        permission.PermissionsList,
	pb.QRDBService_ResolveRolePermissions_FullMethodName: permission.PermissionsResolve,

	// Certificate management
	pb.QRDBService_GenerateCert_FullMethodName: permission.CertsGenerate,
	pb.QRDBService_ExportCert_FullMethodName:   permission.CertsExport,
	pb.QRDBService_RevokeCert_FullMethodName:   permission.CertsRevoke,
	pb.QRDBService_ListCerts_FullMethodName:    permission.CertsList,

	// Per-tenant barrier (JSON-over-gRPC TenantBarrierService; no generated
	// pb stub, so the full method names are string literals). These carry
	// the tenant-only permissions whose ScopeTenantOnly rule denies the
	// system tenant and any cross-tenant access. The handler enforces the
	// fine-grained gate against the request's tenant_id; this coarse
	// mapping ensures the caller holds the permission at all.
	"/qrdb.v1.TenantBarrierService/Init":   permission.TenantBarrierInit,
	"/qrdb.v1.TenantBarrierService/Unseal": permission.TenantBarrierUnseal,
	"/qrdb.v1.TenantBarrierService/Seal":   permission.TenantBarrierSeal,
	"/qrdb.v1.TenantBarrierService/Status": permission.TenantBarrierStatus,

	// Audit service (JSON-over-gRPC, hand-rolled ServiceDesc; registered in
	// server.Start when an audit logger is configured).
	"/qrdb.v1.AuditService/Query": permission.AuditQuery,

	// Barrier management (mirrors restPermissions /v1/barrier/*).
	pb.QRDBService_BarrierInitialize_FullMethodName:         permission.SealBootstrap,
	pb.QRDBService_BarrierUnseal_FullMethodName:             permission.SealUnseal,
	pb.QRDBService_BarrierSeal_FullMethodName:               permission.SealSeal,
	pb.QRDBService_BarrierStatus_FullMethodName:             permission.SealStatus,
	pb.QRDBService_BarrierShamirInitialize_FullMethodName:   permission.SealInitializeShamir,
	pb.QRDBService_BarrierShamirUnsealShare_FullMethodName:  permission.BarrierUnsealShare,
	pb.QRDBService_BarrierShamirUnsealShares_FullMethodName: permission.BarrierUnsealShare,
	pb.QRDBService_BarrierRekey_FullMethodName:              permission.SealRekey,
	pb.QRDBService_BarrierRecoveryGenerate_FullMethodName:   permission.SealGenerateRecovery,
	pb.QRDBService_BarrierRecoveryRecover_FullMethodName:    permission.SealRecoverWithKeys,
	pb.QRDBService_BarrierRecoveryDelete_FullMethodName:     permission.SealGenerateRecovery,
	pb.QRDBService_BarrierRecoveryStatus_FullMethodName:     permission.SealStatus,
	pb.QRDBService_BarrierRootToken_FullMethodName:          permission.SealBootstrap,

	// Ceremony management.
	pb.QRDBService_CeremonyInitialize_FullMethodName:            permission.CeremonyInitialize,
	pb.QRDBService_CeremonyStatus_FullMethodName:                permission.CeremonyStatus,
	pb.QRDBService_CeremonyClaimCertBegin_FullMethodName:        permission.CeremonyClaimCert,
	pb.QRDBService_CeremonyClaimCertComplete_FullMethodName:     permission.CeremonyClaimCert,
	pb.QRDBService_CeremonyClaimShare_FullMethodName:            permission.CeremonyClaimShareComplete,
	pb.QRDBService_CeremonySignCSR_FullMethodName:               permission.CeremonySignCSR,
	pb.QRDBService_CeremonyEnrollExternalOfficer_FullMethodName: permission.CeremonyEnrollCustodian,
	pb.QRDBService_CeremonyClaimShareBegin_FullMethodName:       permission.CeremonyClaimShareBegin,
	pb.QRDBService_CeremonyClaimShareComplete_FullMethodName:    permission.CeremonyClaimShareComplete,
	pb.QRDBService_CeremonySubmitUnsealShare_FullMethodName:     permission.BarrierUnsealShare,

	// Tenant management.
	pb.QRDBService_TenantCreate_FullMethodName:       permission.TenantsCreate,
	pb.QRDBService_TenantGet_FullMethodName:          permission.TenantsRead,
	pb.QRDBService_TenantList_FullMethodName:         permission.TenantsList,
	pb.QRDBService_TenantUpdate_FullMethodName:       permission.TenantsUpdate,
	pb.QRDBService_TenantUpdateQuotas_FullMethodName: permission.TenantsUpdate,
	pb.QRDBService_TenantDelete_FullMethodName:       permission.TenantsDelete,

	// Backup management.
	pb.QRDBService_BackupCreate_FullMethodName:  permission.BackupCreate,
	pb.QRDBService_BackupList_FullMethodName:    permission.BackupList,
	pb.QRDBService_BackupGet_FullMethodName:     permission.BackupRead,
	pb.QRDBService_BackupDelete_FullMethodName:  permission.BackupDelete,
	pb.QRDBService_BackupRestore_FullMethodName: permission.BackupRestore,
	pb.QRDBService_BackupVerify_FullMethodName:  permission.BackupVerify,

	// Cluster rebalance.
	pb.QRDBService_RebalanceGetShardStats_FullMethodName:      permission.RebalanceStats,
	pb.QRDBService_RebalanceCreatePlan_FullMethodName:         permission.RebalancePlan,
	pb.QRDBService_RebalanceExecute_FullMethodName:            permission.RebalanceExecute,
	pb.QRDBService_RebalanceGetImbalanceReport_FullMethodName: permission.RebalanceReport,

	// Embedding.
	pb.QRDBService_Embed_FullMethodName:               permission.EmbeddingEmbed,
	pb.QRDBService_BatchEmbed_FullMethodName:          permission.EmbeddingEmbed,
	pb.QRDBService_ListEmbeddingModels_FullMethodName: permission.EmbeddingListModels,

	// RAG (retrieval-augmented generation).
	pb.QRDBService_RAGIngestDocument_FullMethodName: permission.RAGIngest,
	pb.QRDBService_RAGQuery_FullMethodName:          permission.RAGQuery,
	pb.QRDBService_RAGDeleteDocument_FullMethodName: permission.RAGDeleteDocument,
	pb.QRDBService_RAGListDocuments_FullMethodName:  permission.RAGListDocuments,

	// Database management.
	pb.QRDBService_DatabaseCreate_FullMethodName:           permission.DatabasesCreate,
	pb.QRDBService_DatabaseGet_FullMethodName:              permission.DatabasesRead,
	pb.QRDBService_DatabaseList_FullMethodName:             permission.DatabasesList,
	pb.QRDBService_DatabaseDrop_FullMethodName:             permission.DatabasesDelete,
	pb.QRDBService_DatabaseUpdateBarrier_FullMethodName:    permission.DatabasesUpdate,
	pb.QRDBService_DatabaseUpdateConnection_FullMethodName: permission.DatabasesUpdate,
	pb.QRDBService_DatabaseUpdateEtcd_FullMethodName:       permission.DatabasesUpdate,
	pb.QRDBService_DatabaseUpdateProtocol_FullMethodName:   permission.DatabasesUpdate,
	pb.QRDBService_DatabaseUpdateRaft_FullMethodName:       permission.DatabasesUpdate,
	pb.QRDBService_DatabaseSeal_FullMethodName:             permission.DatabasesSeal,
	pb.QRDBService_DatabaseUnseal_FullMethodName:           permission.DatabasesUnseal,
	pb.QRDBService_DatabaseBarrierStatus_FullMethodName:    permission.DatabasesBarrierStatus,

	// Collection / Document management.
	pb.QRDBService_CreateCollection_FullMethodName:  permission.CollectionsCreate,
	pb.QRDBService_DropCollection_FullMethodName:    permission.CollectionsDrop,
	pb.QRDBService_GetCollectionInfo_FullMethodName: permission.CollectionsRead,
	pb.QRDBService_ListCollections_FullMethodName:   permission.CollectionsList,
	pb.QRDBService_InsertDocument_FullMethodName:    permission.CollectionsInsert,
	pb.QRDBService_GetDocument_FullMethodName:       permission.CollectionsRead,
	pb.QRDBService_ListDocuments_FullMethodName:     permission.CollectionsList,
	pb.QRDBService_UpdateDocument_FullMethodName:    permission.CollectionsUpdate,
	pb.QRDBService_DeleteDocument_FullMethodName:    permission.CollectionsDelete,

	// Credential management.
	pb.QRDBService_CredentialSubmit_FullMethodName:          permission.CredentialsSubmit,
	pb.QRDBService_CredentialStrategy_FullMethodName:        permission.CredentialsStrategy,
	pb.QRDBService_CredentialList_FullMethodName:            permission.CredentialsList,
	pb.QRDBService_CredentialDelete_FullMethodName:          permission.CredentialsDelete,
	pb.QRDBService_CredentialRotate_FullMethodName:          permission.CredentialsRotate,
	pb.QRDBService_CredentialGCExpired_FullMethodName:       permission.CredentialsGCExpired,
	pb.QRDBService_CredentialRetrieveVersion_FullMethodName: permission.CredentialsRetrieveVersion,
	pb.QRDBService_CredentialListVersions_FullMethodName:    permission.CredentialsListVersions,
	pb.QRDBService_CredentialStore_FullMethodName:           permission.CredentialsStore,
	pb.QRDBService_CredentialRetrieve_FullMethodName:        permission.CredentialsRetrieve,

	// Distributed locks.
	pb.QRDBService_LockAcquire_FullMethodName: permission.LocksAcquire,
	pb.QRDBService_LockTryLock_FullMethodName: permission.LocksAcquire,
	pb.QRDBService_LockRelease_FullMethodName: permission.LocksRelease,
	pb.QRDBService_LockExtend_FullMethodName:  permission.LocksExtend,
	pb.QRDBService_LockStatus_FullMethodName:  permission.LocksStatus,
	pb.QRDBService_LockList_FullMethodName:    permission.LocksList,

	// Shard control.
	pb.QRDBService_StartShard_FullMethodName:    permission.AdminStartShard,
	pb.QRDBService_StopShard_FullMethodName:     permission.AdminStopShard,
	pb.QRDBService_GetShardState_FullMethodName: permission.AdminShardState,
	pb.QRDBService_GetTopology_FullMethodName:   permission.AdminTopology,

	// Misc.
	pb.QRDBService_GetUserByUsername_FullMethodName: permission.UsersRead,
	pb.QRDBService_IndexList_FullMethodName:         permission.IndexList,
}

// restPermissions maps "METHOD /path" to the permission required for
// that REST endpoint. Routes not present in this map are considered
// public (no authorization required).
var restPermissions = map[string]permission.Permission{

	// KV -- the /kv path is method-dispatched in the REST server.
	"PUT /kv":       permission.KVPut,
	"GET /kv":       permission.KVGet,
	"DELETE /kv":    permission.KVDelete,
	"GET /kv/scan":  permission.KVList,
	"GET /kv/range": permission.KVRange,

	// MVCC
	"PUT /kv/lease":    permission.MVCCPutWithLease,
	"GET /kv/revision": permission.MVCCCurrentRevision,
	"POST /kv/compact": permission.MVCCCompact,

	// Transaction
	"POST /tx": permission.TransactionExecute,

	// Batch
	"POST /batch": permission.BatchExecute,

	// Watch
	"GET /watch": permission.WatchWatch,

	// Lease
	"POST /lease/grant":     permission.LeaseGrant,
	"POST /lease/revoke":    permission.LeaseRevoke,
	"POST /lease/keepalive": permission.LeaseKeepAlive,
	"GET /lease/ttl":        permission.LeaseTTL,
	"GET /lease/list":       permission.LeaseList,

	// Index
	"POST /index/create": permission.IndexCreate,
	"POST /index/drop":   permission.IndexDrop,
	"GET /index/query":   permission.IndexQuery,
	"GET /index/scan":    permission.IndexScan,

	// TimeSeries
	"POST /ts/write":       permission.TSWrite,
	"POST /ts/write-batch": permission.TSWriteBatch,
	"POST /ts/write-point": permission.TSWritePoint,
	"GET /ts/query":        permission.TSQuery,
	"POST /ts/query-rich":  permission.TSRichQuery,
	"GET /ts/aggregate":    permission.TSAggregate,
	"GET /ts/metrics":      permission.TSListMetrics,

	// Admin
	// /health is public (exempted in RESTMiddleware before authentication).
	"GET /status":             permission.AdminStatus,
	"GET /peers":              permission.AdminPeers,
	"POST /membership/add":    permission.AdminAddMember,
	"POST /membership/remove": permission.AdminRemoveMember,
	"POST /snapshot":          permission.AdminSnapshot,
	"POST /drain":             permission.AdminDrain,
	"GET /capabilities":       permission.AdminCapabilities,
	"POST /admin/shards":      permission.AdminStartShard,
	"GET /admin/topology":     permission.AdminTopology,

	// Vector
	"POST /vector/namespace":             permission.VectorNamespaceCreate,
	"POST /vector/namespace/delete":      permission.VectorNamespaceDelete,
	"GET /vector/namespaces":             permission.VectorNamespaceList,
	"GET /vector/namespace/info":         permission.VectorNamespaceInfo,
	"POST /vector/upsert":                permission.VectorUpsert,
	"GET /vector/get":                    permission.VectorGet,
	"POST /vector/delete":                permission.VectorDelete,
	"POST /vector/batch/upsert":          permission.VectorBatchUpsert,
	"POST /vector/batch/delete":          permission.VectorBatchDelete,
	"GET /vector/list":                   permission.VectorList,
	"POST /vector/metadata":              permission.VectorUpdateMetadata,
	"POST /vector/search/similar":        permission.VectorSearchSimilar,
	"POST /vector/search/metadata":       permission.VectorSearchByMetadata,
	"POST /vector/search/hybrid":         permission.VectorHybridSearch,
	"POST /vector/search/by-id":          permission.VectorSearchByID,
	"POST /vector/index/rebuild":         permission.VectorRebuildIndex,
	"GET /vector/index/rebuild/progress": permission.VectorGetRebuildProg,
	"GET /vector/index/stats":            permission.VectorGetIndexStats,
	"POST /vector/compact":               permission.VectorCompact,
	"GET /vector/compact/progress":       permission.VectorGetCompactProg,

	// User management
	"POST /users":     permission.UsersCreate,
	"GET /users/list": permission.UsersList,

	// Role management
	"POST /roles":                     permission.RolesCreate,
	"GET /roles/list":                 permission.RolesList,
	"GET /roles/by-name":              permission.RolesRead,
	"POST /roles/resolve-permissions": permission.PermissionsResolve,
	"POST /roles/assign":              permission.RolesAssign,
	"POST /roles/unassign":            permission.RolesUnassign,
	"GET /permissions":                permission.PermissionsList,

	// Certificate management
	"POST /certs/generate": permission.CertsGenerate,
	"POST /certs/export":   permission.CertsExport,
	"POST /certs/revoke":   permission.CertsRevoke,
	"GET /certs/list":      permission.CertsList,

	// Barrier management (previously missing — security gap closed)
	"POST /v1/barrier/initialize":           permission.SealBootstrap,
	"POST /v1/barrier/unseal":               permission.SealUnseal,
	"POST /v1/barrier/seal":                 permission.SealSeal,
	"GET /v1/barrier/status":                permission.SealStatus,
	"POST /v1/barrier/shamir/initialize":    permission.SealInitializeShamir,
	"POST /v1/barrier/shamir/unseal-share":  permission.BarrierUnsealShare,
	"POST /v1/barrier/shamir/unseal-shares": permission.BarrierUnsealShare,
	"POST /v1/barrier/shamir/verify":        permission.SealStatus,
	"POST /v1/barrier/rekey":                permission.SealRekey,
	"POST /v1/barrier/recovery/generate":    permission.SealGenerateRecovery,
	"POST /v1/barrier/recovery/recover":     permission.SealRecoverWithKeys,
	"DELETE /v1/barrier/recovery":           permission.SealGenerateRecovery,
	"GET /v1/barrier/recovery/status":       permission.SealStatus,
	"POST /v1/barrier/root-token":           permission.SealBootstrap,

	// Ceremony management (previously missing — security gap closed)
	"POST /v1/ceremony/initialize":           permission.CeremonyInitialize,
	"GET /v1/ceremony/status":                permission.CeremonyStatus,
	"POST /v1/ceremony/claim-cert/begin":     permission.CeremonyClaimCert,
	"POST /v1/ceremony/claim-cert/complete":  permission.CeremonyClaimCert,
	"POST /v1/ceremony/claim-share":          permission.CeremonyClaimShareComplete,
	"POST /v1/ceremony/sign-csr":             permission.CeremonySignCSR,
	"POST /v1/init/enroll-external-officer":  permission.CeremonyEnrollCustodian,
	"POST /v1/ceremony/enroll-custodian":     permission.CeremonyEnrollCustodian,
	"POST /v1/ceremony/revoke-custodian":     permission.CeremonyRevokeCustodian,
	"POST /v1/ceremony/claim-share/begin":    permission.CeremonyClaimShareBegin,
	"POST /v1/ceremony/claim-share/complete": permission.CeremonyClaimShareComplete,
	"POST /v1/barrier/unseal-share":          permission.BarrierUnsealShare,

	// Audit (Phase 6 — handler stubs pending)
	"GET /v1/audit/events": permission.AuditQuery,

	// AuthFactor (factor-type challenge/response lifecycle). The path-parameter
	// routes (/v1/authfactors/{factor}/*, /v1/authfactors/{id}) live in
	// restPathParamPermissions so they match concrete paths; only the static
	// list route is registered here.
	"GET /v1/authfactors/list": permission.AuthFactorList,

	// Tenant management (static routes; per-tenant routes are templated below).
	"POST /v1/tenants":     permission.TenantsCreate,
	"GET /v1/tenants/list": permission.TenantsList,

	// Backup management (static routes; per-backup routes are templated below).
	"POST /v1/backups":         permission.BackupCreate,
	"GET /v1/backups/list":     permission.BackupList,
	"POST /v1/backups/restore": permission.BackupRestore,

	// Cluster rebalance (static routes; per-shard stats route is templated below).
	"POST /v1/rebalance/plan":    permission.RebalancePlan,
	"POST /v1/rebalance/execute": permission.RebalanceExecute,
	"GET /v1/rebalance/report":   permission.RebalanceReport,

	// Database management (static routes; per-database routes are templated below).
	"POST /v1/databases":     permission.DatabasesCreate,
	"GET /v1/databases/list": permission.DatabasesList,

	// Collection management (static routes; per-collection routes are templated below).
	"POST /v1/collections":     permission.CollectionsCreate,
	"GET /v1/collections/list": permission.CollectionsList,

	// Credential management (static routes; per-credential routes are templated below).
	"POST /v1/credentials/submit":  permission.CredentialsSubmit,
	"POST /v1/credentials/store":   permission.CredentialsStore,
	"GET /v1/credentials/strategy": permission.CredentialsStrategy,
	"GET /v1/credentials":          permission.CredentialsList,

	// Distributed locks.
	"POST /lock/acquire": permission.LocksAcquire,
	"POST /lock/trylock": permission.LocksAcquire,
	"POST /lock/release": permission.LocksRelease,
	"POST /lock/extend":  permission.LocksExtend,
	"GET /lock/status":   permission.LocksStatus,
	"GET /lock/list":     permission.LocksList,

	// Secondary index listing.
	"GET /index/list": permission.IndexList,

	// Batch import.
	"POST /batch/import": permission.BatchExecute,

	// SQL query (read-through over the KV range surface).
	"POST /v1/sql/query": permission.KVRange,

	// Embedding.
	"POST /v1/embeddings":       permission.EmbeddingEmbed,
	"POST /v1/embeddings/batch": permission.EmbeddingEmbed,
	"GET /v1/embedding/models":  permission.EmbeddingListModels,

	// RAG.
	"POST /v1/rag/ingest":           permission.RAGIngest,
	"POST /v1/rag/query":            permission.RAGQuery,
	"POST /v1/rag/documents/delete": permission.RAGDeleteDocument,
	"GET /v1/rag/documents":         permission.RAGListDocuments,

	// Per-user auth-factor management (static routes; per-factor routes templated below).
	"POST /v1/users/factors":        permission.UserAuthFactorsAdd,
	"GET /v1/users/factors/list":    permission.UserAuthFactorsList,
	"POST /v1/users/factors/login":  permission.UserAuthFactorsLogin,
	"POST /v1/users/factors/rotate": permission.UserAuthFactorsRotate,
}

// restPathParamPermissions maps templated REST routes (those carrying path
// parameters) to the permission they require. Keys use "{name}" placeholders
// for path-parameter segments, e.g. "GET /users/{id}". They are compiled into
// restPatterns at init and matched by LookupREST only after an exact
// restPermissions lookup misses.
//
// Registering these closes a structural authorization gap: the REST server
// dispatches path-parameter routes by prefix, but the auth middleware looked
// up the literal request path against restPermissions (exact match only).
// Concrete paths such as "/users/abc123" never matched, so every state-
// changing per-resource route was treated as public (authenticated but
// unauthorized). Each templated route is now mapped to its required
// permission.
var restPathParamPermissions = map[string]permission.Permission{
	// User management (per-user routes dispatched via the /users/ prefix).
	"GET /users/{id}":                            permission.UsersRead,
	"PUT /users/{id}":                            permission.UsersUpdate,
	"DELETE /users/{id}":                         permission.UsersDelete,
	"POST /users/{id}/enable":                    permission.UsersEnable,
	"POST /users/{id}/disable":                   permission.UsersDisable,
	"POST /users/{id}/cert-bindings":             permission.UsersAddCertBinding,
	"GET /users/by-cert-thumbprint/{thumbprint}": permission.UsersGetByCertThumbprint,

	// Role management (per-role routes dispatched via the /roles/ prefix).
	"GET /roles/{id}":    permission.RolesRead,
	"PUT /roles/{id}":    permission.RolesUpdate,
	"DELETE /roles/{id}": permission.RolesDelete,

	// Tenant management (per-tenant routes dispatched via /v1/tenants/).
	// The per-tenant barrier sub-routes (/v1/tenants/{id}/barrier/*) are NOT
	// listed here: they are tenant-scoped (ScopeTenantOnly) and enforced
	// in-handler via authorizeTenantBarrierOp, which validates the request's
	// {id} against the caller's own tenant — a check the path-agnostic
	// middleware cannot perform.
	"GET /v1/tenants/{id}":         permission.TenantsRead,
	"PUT /v1/tenants/{id}":         permission.TenantsUpdate,
	"DELETE /v1/tenants/{id}":      permission.TenantsDelete,
	"POST /v1/tenants/{id}/quotas": permission.TenantsUpdate,

	// Backup management (per-backup routes dispatched via /v1/backups/).
	"GET /v1/backups/{id}":         permission.BackupRead,
	"DELETE /v1/backups/{id}":      permission.BackupDelete,
	"POST /v1/backups/{id}/verify": permission.BackupVerify,

	// Cluster rebalance per-shard stats route (dispatched via /v1/rebalance/shards/).
	"GET /v1/rebalance/shards/{id}": permission.RebalanceStats,

	// Admin per-shard routes (dispatched via the /admin/shards/ prefix).
	"DELETE /admin/shards/{id}":    permission.AdminStopShard,
	"GET /admin/shards/{id}/state": permission.AdminShardState,

	// AuthFactor (factor-type lifecycle). Relocated from restPermissions: a
	// literal "{factor}"/"{id}" key never matches a concrete request path.
	"POST /v1/authfactors/{factor}/enroll":    permission.AuthFactorEnroll,
	"POST /v1/authfactors/{factor}/challenge": permission.AuthFactorChallenge,
	"POST /v1/authfactors/{factor}/verify":    permission.AuthFactorVerify,
	"DELETE /v1/authfactors/{id}":             permission.AuthFactorRevoke,

	// Time-series per-metric routes (dispatched via the /ts/series/ prefix).
	"DELETE /ts/series/{metric}":        permission.TSDeleteSeries,
	"GET /ts/series/{metric}/retention": permission.TSGetRetention,
	"PUT /ts/series/{metric}/retention": permission.TSSetRetention,

	// Database management (per-database routes dispatched via /v1/databases/).
	// The config sub-resources are updated with PATCH (see handleDatabaseByName).
	"GET /v1/databases/{name}":                permission.DatabasesRead,
	"DELETE /v1/databases/{name}":             permission.DatabasesDelete,
	"PATCH /v1/databases/{name}/barrier":      permission.DatabasesUpdate,
	"PATCH /v1/databases/{name}/connection":   permission.DatabasesUpdate,
	"PATCH /v1/databases/{name}/etcd":         permission.DatabasesUpdate,
	"PATCH /v1/databases/{name}/protocol":     permission.DatabasesUpdate,
	"PATCH /v1/databases/{name}/raft":         permission.DatabasesUpdate,
	"POST /v1/databases/{name}/seal":          permission.DatabasesSeal,
	"POST /v1/databases/{name}/unseal":        permission.DatabasesUnseal,
	"GET /v1/databases/{name}/barrier/status": permission.DatabasesBarrierStatus,

	// Credential management (per-credential routes dispatched via /v1/credentials/).
	"DELETE /v1/credentials/{name}":                 permission.CredentialsDelete,
	"POST /v1/credentials/{name}/rotate":            permission.CredentialsRotate,
	"POST /v1/credentials/{name}/gc":                permission.CredentialsGCExpired,
	"GET /v1/credentials/{name}/versions":           permission.CredentialsListVersions,
	"GET /v1/credentials/{name}/versions/{version}": permission.CredentialsRetrieveVersion,
	"GET /v1/credentials/{name}/value":              permission.CredentialsRetrieve,

	// Per-user auth-factor management (per-factor routes dispatched via /v1/users/factors/).
	"GET /v1/users/factors/{id}":       permission.UserAuthFactorsGet,
	"DELETE /v1/users/factors/{id}":    permission.UserAuthFactorsRemove,
	"PUT /v1/users/factors/{id}/label": permission.UserAuthFactorsRelabel,

	// Collection / Document management (routes under /v1/collections/{database}/{name}).
	"GET /v1/collections/{database}/{name}":                   permission.CollectionsRead,
	"DELETE /v1/collections/{database}/{name}":                permission.CollectionsDrop,
	"POST /v1/collections/{database}/{name}/documents":        permission.CollectionsInsert,
	"GET /v1/collections/{database}/{name}/documents":         permission.CollectionsList,
	"GET /v1/collections/{database}/{name}/documents/{id}":    permission.CollectionsRead,
	"PUT /v1/collections/{database}/{name}/documents/{id}":    permission.CollectionsUpdate,
	"DELETE /v1/collections/{database}/{name}/documents/{id}": permission.CollectionsDelete,
}

// restPattern is a compiled templated route. A segment equal to "" matches any
// single path segment (a path parameter); every other segment must match
// literally. literals records the number of fixed segments so the most
// specific pattern wins when more than one matches.
type restPattern struct {
	method   string
	segments []string
	literals int
	perm     permission.Permission
}

// restPatterns holds restPathParamPermissions compiled for matching. Built
// once at package init.
var restPatterns []restPattern

func init() {
	for key, perm := range restPathParamPermissions {
		method, path, ok := splitMethodPath(key)
		if !ok {
			continue
		}
		rawSegs := strings.Split(strings.Trim(path, "/"), "/")
		segs := make([]string, len(rawSegs))
		literals := 0
		for i, s := range rawSegs {
			if len(s) >= 2 && s[0] == '{' && s[len(s)-1] == '}' {
				segs[i] = "" // wildcard
			} else {
				segs[i] = s
				literals++
			}
		}
		restPatterns = append(restPatterns, restPattern{
			method:   method,
			segments: segs,
			literals: literals,
			perm:     perm,
		})
	}
}

// splitMethodPath splits a "METHOD /path" key into its method and path parts.
func splitMethodPath(key string) (method, path string, ok bool) {
	sp := strings.IndexByte(key, ' ')
	if sp < 0 {
		return "", "", false
	}
	return key[:sp], key[sp+1:], true
}

// quicPermissions maps native QUIC method names to the permission required
// to invoke them. Methods not present in this map are considered public
// (no authorization required).
var quicPermissions = map[string]permission.Permission{

	// KV
	"kv.Put":    permission.KVPut,
	"kv.Get":    permission.KVGet,
	"kv.Delete": permission.KVDelete,
	"kv.List":   permission.KVList,
	"kv.Scan":   permission.KVRange,

	// MVCC
	"mvcc.PutWithLease":    permission.MVCCPutWithLease,
	"mvcc.GetRevision":     permission.MVCCGetRevision,
	"mvcc.GetAtRevision":   permission.MVCCGetAtRevision,
	"mvcc.Range":           permission.MVCCRange,
	"mvcc.Compact":         permission.MVCCCompact,
	"mvcc.CurrentRevision": permission.MVCCCurrentRevision,

	// Transaction
	"tx.Transaction": permission.TransactionExecute,

	// Batch
	"batch.Batch": permission.BatchExecute,

	// Watch
	"watch.Watch":       permission.WatchWatch,
	"watch.WatchPrefix": permission.WatchWatch,

	// Lease
	"lease.Grant":      permission.LeaseGrant,
	"lease.Revoke":     permission.LeaseRevoke,
	"lease.KeepAlive":  permission.LeaseKeepAlive,
	"lease.TimeToLive": permission.LeaseTTL,
	"lease.List":       permission.LeaseList,

	// Index
	"index.Create": permission.IndexCreate,
	"index.Drop":   permission.IndexDrop,
	"index.Query":  permission.IndexQuery,
	"index.Scan":   permission.IndexScan,

	// TimeSeries
	"ts.Write":        permission.TSWrite,
	"ts.WriteBatch":   permission.TSWriteBatch,
	"ts.Query":        permission.TSQuery,
	"ts.Aggregate":    permission.TSAggregate,
	"ts.WritePoint":   permission.TSWritePoint,
	"ts.RichQuery":    permission.TSRichQuery,
	"ts.DeleteSeries": permission.TSDeleteSeries,
	"ts.GetRetention": permission.TSGetRetention,
	"ts.SetRetention": permission.TSSetRetention,
	"ts.ListMetrics":  permission.TSListMetrics,

	// Vector
	"vector.CreateNamespace":       permission.VectorNamespaceCreate,
	"vector.DeleteNamespace":       permission.VectorNamespaceDelete,
	"vector.ListNamespaces":        permission.VectorNamespaceList,
	"vector.GetNamespaceInfo":      permission.VectorNamespaceInfo,
	"vector.Upsert":                permission.VectorUpsert,
	"vector.Get":                   permission.VectorGet,
	"vector.Delete":                permission.VectorDelete,
	"vector.BatchUpsert":           permission.VectorBatchUpsert,
	"vector.BatchDelete":           permission.VectorBatchDelete,
	"vector.List":                  permission.VectorList,
	"vector.UpdateMetadata":        permission.VectorUpdateMetadata,
	"vector.SearchSimilar":         permission.VectorSearchSimilar,
	"vector.SearchByMetadata":      permission.VectorSearchByMetadata,
	"vector.HybridSearch":          permission.VectorHybridSearch,
	"vector.SearchByID":            permission.VectorSearchByID,
	"vector.RebuildIndex":          permission.VectorRebuildIndex,
	"vector.GetRebuildProgress":    permission.VectorGetRebuildProg,
	"vector.GetIndexStats":         permission.VectorGetIndexStats,
	"vector.CompactNamespace":      permission.VectorCompact,
	"vector.GetCompactionProgress": permission.VectorGetCompactProg,

	// Admin
	"admin.Health":          permission.AdminHealth,
	"admin.Status":          permission.AdminStatus,
	"admin.Peers":           permission.AdminPeers,
	"admin.AddMember":       permission.AdminAddMember,
	"admin.RemoveMember":    permission.AdminRemoveMember,
	"admin.RequestSnapshot": permission.AdminSnapshot,
	"admin.Drain":           permission.AdminDrain,
	"admin.Capabilities":    permission.AdminCapabilities,

	// User management
	"user.Create":  permission.UsersCreate,
	"user.Get":     permission.UsersRead,
	"user.Update":  permission.UsersUpdate,
	"user.Delete":  permission.UsersDelete,
	"user.List":    permission.UsersList,
	"user.Enable":  permission.UsersEnable,
	"user.Disable": permission.UsersDisable,

	// Cert-binding (parity with gRPC/REST): thumbprint lookup is a read, cert
	// binding is a user mutation. Absent these, the QUIC authorizer treats the
	// methods as public.
	"user.GetByCertThumbprint": permission.UsersGetByCertThumbprint,
	"user.AddCertBinding":      permission.UsersAddCertBinding,

	// Role management
	"role.Create":             permission.RolesCreate,
	"role.Get":                permission.RolesRead,
	"role.GetByName":          permission.RolesRead,
	"role.Update":             permission.RolesUpdate,
	"role.Delete":             permission.RolesDelete,
	"role.List":               permission.RolesList,
	"role.Assign":             permission.RolesAssign,
	"role.Unassign":           permission.RolesUnassign,
	"role.ListPermissions":    permission.PermissionsList,
	"role.ResolvePermissions": permission.PermissionsResolve,

	// Certificate management
	"cert.Generate": permission.CertsGenerate,
	"cert.Export":   permission.CertsExport,
	"cert.Revoke":   permission.CertsRevoke,
	"cert.List":     permission.CertsList,

	// Barrier management
	"barrier.Initialize":       permission.SealBootstrap,
	"barrier.Unseal":           permission.SealUnseal,
	"barrier.Seal":             permission.SealSeal,
	"barrier.Status":           permission.SealStatus,
	"barrier.ShamirInitialize": permission.SealInitializeShamir,
	"barrier.UnsealShare":      permission.BarrierUnsealShare,
	"barrier.UnsealShares":     permission.BarrierUnsealShare,
	"barrier.Rekey":            permission.SealRekey,
	"barrier.GenerateRecovery": permission.SealGenerateRecovery,
	"barrier.RecoverWithKeys":  permission.SealRecoverWithKeys,
	"barrier.RootToken":        permission.SealBootstrap,

	// Per-tenant barrier (parity with REST/gRPC TenantBarrierService). These
	// carry the tenant-scoped (ScopeTenantOnly) permissions; the handler
	// additionally enforces the fine-grained gate against the request's
	// tenant_id. Without these mappings the QUIC authorizer treats the
	// methods as public.
	"tenant.BarrierInit":   permission.TenantBarrierInit,
	"tenant.BarrierUnseal": permission.TenantBarrierUnseal,
	"tenant.BarrierSeal":   permission.TenantBarrierSeal,
	"tenant.BarrierStatus": permission.TenantBarrierStatus,

	// Shard control.
	"admin.StartShard":    permission.AdminStartShard,
	"admin.StopShard":     permission.AdminStopShard,
	"admin.GetShardState": permission.AdminShardState,
	"admin.GetTopology":   permission.AdminTopology,

	// Audit.
	"audit.Query": permission.AuditQuery,

	// AuthFactor (factor-type challenge/response lifecycle).
	"authfactor.Enroll":    permission.AuthFactorEnroll,
	"authfactor.Challenge": permission.AuthFactorChallenge,
	"authfactor.Verify":    permission.AuthFactorVerify,
	"authfactor.List":      permission.AuthFactorList,
	"authfactor.Revoke":    permission.AuthFactorRevoke,

	// Barrier recovery / shamir (parity with REST/gRPC).
	"barrier.RecoveryDelete":     permission.SealGenerateRecovery,
	"barrier.RecoveryGenerate":   permission.SealGenerateRecovery,
	"barrier.RecoveryRecover":    permission.SealRecoverWithKeys,
	"barrier.RecoveryStatus":     permission.SealStatus,
	"barrier.ShamirUnsealShare":  permission.BarrierUnsealShare,
	"barrier.ShamirUnsealShares": permission.BarrierUnsealShare,

	// Ceremony management.
	"ceremony.ClaimCertBegin":     permission.CeremonyClaimCert,
	"ceremony.ClaimCertComplete":  permission.CeremonyClaimCert,
	"ceremony.ClaimShare":         permission.CeremonyClaimShareComplete,
	"ceremony.ClaimShareBegin":    permission.CeremonyClaimShareBegin,
	"ceremony.ClaimShareComplete": permission.CeremonyClaimShareComplete,
	"ceremony.SubmitUnsealShare":  permission.BarrierUnsealShare,
	"ceremony.Initialize":         permission.CeremonyInitialize,
	"ceremony.SignCSR":            permission.CeremonySignCSR,
	"ceremony.Status":             permission.CeremonyStatus,

	// Collection / Document management.
	"collection.Create":         permission.CollectionsCreate,
	"collection.Drop":           permission.CollectionsDrop,
	"collection.GetInfo":        permission.CollectionsRead,
	"collection.List":           permission.CollectionsList,
	"collection.InsertDocument": permission.CollectionsInsert,
	"collection.GetDocument":    permission.CollectionsRead,
	"collection.ListDocuments":  permission.CollectionsList,
	"collection.UpdateDocument": permission.CollectionsUpdate,
	"collection.DeleteDocument": permission.CollectionsDelete,

	// Credential management.
	"credential.Submit":          permission.CredentialsSubmit,
	"credential.Strategy":        permission.CredentialsStrategy,
	"credential.List":            permission.CredentialsList,
	"credential.Delete":          permission.CredentialsDelete,
	"credential.Rotate":          permission.CredentialsRotate,
	"credential.GCExpired":       permission.CredentialsGCExpired,
	"credential.RetrieveVersion": permission.CredentialsRetrieveVersion,
	"credential.ListVersions":    permission.CredentialsListVersions,
	"credential.Store":           permission.CredentialsStore,
	"credential.Retrieve":        permission.CredentialsRetrieve,

	// Database management.
	"db.Create":           permission.DatabasesCreate,
	"db.Get":              permission.DatabasesRead,
	"db.List":             permission.DatabasesList,
	"db.Drop":             permission.DatabasesDelete,
	"db.UpdateBarrier":    permission.DatabasesUpdate,
	"db.UpdateConnection": permission.DatabasesUpdate,
	"db.UpdateEtcd":       permission.DatabasesUpdate,
	"db.UpdateProtocol":   permission.DatabasesUpdate,
	"db.UpdateRaft":       permission.DatabasesUpdate,
	"db.Seal":             permission.DatabasesSeal,
	"db.Unseal":           permission.DatabasesUnseal,
	"db.BarrierStatus":    permission.DatabasesBarrierStatus,

	// Distributed locks.
	"lock.Acquire": permission.LocksAcquire,
	"lock.TryLock": permission.LocksAcquire,
	"lock.Release": permission.LocksRelease,
	"lock.Extend":  permission.LocksExtend,
	"lock.Status":  permission.LocksStatus,
	"lock.List":    permission.LocksList,

	// Tenant management.
	"tenant.Create":       permission.TenantsCreate,
	"tenant.Get":          permission.TenantsRead,
	"tenant.List":         permission.TenantsList,
	"tenant.Update":       permission.TenantsUpdate,
	"tenant.UpdateQuotas": permission.TenantsUpdate,
	"tenant.Delete":       permission.TenantsDelete,

	// Backup management.
	"backup.Create":  permission.BackupCreate,
	"backup.List":    permission.BackupList,
	"backup.Get":     permission.BackupRead,
	"backup.Delete":  permission.BackupDelete,
	"backup.Restore": permission.BackupRestore,
	"backup.Verify":  permission.BackupVerify,

	// Cluster rebalance.
	"rebalance.GetShardStats":      permission.RebalanceStats,
	"rebalance.CreatePlan":         permission.RebalancePlan,
	"rebalance.Execute":            permission.RebalanceExecute,
	"rebalance.GetImbalanceReport": permission.RebalanceReport,

	// Embedding.
	"embedding.Embed":      permission.EmbeddingEmbed,
	"embedding.BatchEmbed": permission.EmbeddingEmbed,
	"embedding.ListModels": permission.EmbeddingListModels,

	// RAG (retrieval-augmented generation).
	"rag.IngestDocument": permission.RAGIngest,
	"rag.Query":          permission.RAGQuery,
	"rag.DeleteDocument": permission.RAGDeleteDocument,
	"rag.ListDocuments":  permission.RAGListDocuments,

	// Per-user auth-factor management.
	"userauthfactor.Add":     permission.UserAuthFactorsAdd,
	"userauthfactor.Get":     permission.UserAuthFactorsGet,
	"userauthfactor.List":    permission.UserAuthFactorsList,
	"userauthfactor.Remove":  permission.UserAuthFactorsRemove,
	"userauthfactor.Rotate":  permission.UserAuthFactorsRotate,
	"userauthfactor.Relabel": permission.UserAuthFactorsRelabel,
	"userauthfactor.Login":   permission.UserAuthFactorsLogin,

	// Misc.
	"user.GetByUsername": permission.UsersRead,
	"index.List":         permission.IndexList,
}

// mcpPermissions maps MCP tool names to the permission required to invoke
// them. The MCP tools wrap the same service-layer operations as the gRPC,
// REST and native-QUIC transports, so each tool is mapped to the identical
// permission its sibling operation carries in quicPermissions / grpcPermissions
// / restPermissions. Tools not present in this map are considered public
// (no authorization required) — however the MCP enforcement middleware is
// default-deny, so completeness here is a security requirement: every
// registered tool MUST appear below.
//
// A handful of MCP-only domains (database, collection, distributed lock,
// embedding, RAG, per-user auth-factor, and shard/topology admin) have no
// dedicated permission constant in pkg/permission. Rather than invent new
// constants, each such tool is mapped to the closest EXISTING permission by
// operation semantics, mirroring how the underlying primitive is custodied:
//   - database/collection containers are KV-backed   → KV* family
//   - distributed locks are lease-backed             → Lease* family
//   - embedding/RAG operate on the vector store      → Vector* family
//   - per-user auth-factor management                → AuthFactor* family
//   - shard lifecycle / topology are cluster admin   → Admin* family
//
// These approximations are called out in the SDK report; if any domain later
// gains a dedicated permission, the mapping should be tightened to it.
var mcpPermissions = map[string]permission.Permission{

	// KV
	"qrdb_put":    permission.KVPut,
	"qrdb_get":    permission.KVGet,
	"qrdb_delete": permission.KVDelete,
	"qrdb_list":   permission.KVList,
	"qrdb_scan":   permission.KVRange,

	// MVCC
	"qrdb_mvcc_put_with_lease":   permission.MVCCPutWithLease,
	"qrdb_mvcc_get_revision":     permission.MVCCGetRevision,
	"qrdb_mvcc_get_at_revision":  permission.MVCCGetAtRevision,
	"qrdb_mvcc_range":            permission.MVCCRange,
	"qrdb_mvcc_compact":          permission.MVCCCompact,
	"qrdb_mvcc_current_revision": permission.MVCCCurrentRevision,

	// Transaction
	"qrdb_tx_execute": permission.TransactionExecute,

	// Batch
	"qrdb_batch_execute": permission.BatchExecute,

	// Watch
	"qrdb_watch": permission.WatchWatch,

	// Lease
	"qrdb_lease_grant":     permission.LeaseGrant,
	"qrdb_lease_revoke":    permission.LeaseRevoke,
	"qrdb_lease_keepalive": permission.LeaseKeepAlive,
	"qrdb_lease_ttl":       permission.LeaseTTL,
	"qrdb_lease_list":      permission.LeaseList,

	// Index
	"qrdb_index_create": permission.IndexCreate,
	"qrdb_index_drop":   permission.IndexDrop,
	"qrdb_index_query":  permission.IndexQuery,
	"qrdb_index_scan":   permission.IndexScan,
	"qrdb_index_list":   permission.IndexList,

	// TimeSeries
	"qrdb_ts_write":                permission.TSWrite,
	"qrdb_ts_write_batch":          permission.TSWriteBatch,
	"qrdb_ts_write_point":          permission.TSWritePoint,
	"qrdb_ts_query":                permission.TSQuery,
	"qrdb_ts_rich_query":           permission.TSRichQuery,
	"qrdb_ts_aggregate":            permission.TSAggregate,
	"qrdb_ts_delete_series":        permission.TSDeleteSeries,
	"qrdb_ts_get_retention_policy": permission.TSGetRetention,
	"qrdb_ts_set_retention_policy": permission.TSSetRetention,
	"qrdb_ts_list_metrics":         permission.TSListMetrics,

	// Vector
	"qrdb_vector_create_namespace":        permission.VectorNamespaceCreate,
	"qrdb_vector_delete_namespace":        permission.VectorNamespaceDelete,
	"qrdb_vector_list_namespaces":         permission.VectorNamespaceList,
	"qrdb_vector_get_namespace_info":      permission.VectorNamespaceInfo,
	"qrdb_vector_upsert":                  permission.VectorUpsert,
	"qrdb_vector_get":                     permission.VectorGet,
	"qrdb_vector_delete":                  permission.VectorDelete,
	"qrdb_vector_batch_upsert":            permission.VectorBatchUpsert,
	"qrdb_vector_batch_delete":            permission.VectorBatchDelete,
	"qrdb_vector_list":                    permission.VectorList,
	"qrdb_vector_update_metadata":         permission.VectorUpdateMetadata,
	"qrdb_vector_search_similar":          permission.VectorSearchSimilar,
	"qrdb_vector_search_by_metadata":      permission.VectorSearchByMetadata,
	"qrdb_vector_hybrid_search":           permission.VectorHybridSearch,
	"qrdb_vector_search_by_id":            permission.VectorSearchByID,
	"qrdb_vector_rebuild_index":           permission.VectorRebuildIndex,
	"qrdb_vector_get_rebuild_progress":    permission.VectorGetRebuildProg,
	"qrdb_vector_get_index_stats":         permission.VectorGetIndexStats,
	"qrdb_vector_compact_namespace":       permission.VectorCompact,
	"qrdb_vector_get_compaction_progress": permission.VectorGetCompactProg,

	// Admin
	"qrdb_health":        permission.AdminHealth,
	"qrdb_status":        permission.AdminStatus,
	"qrdb_peers":         permission.AdminPeers,
	"qrdb_add_member":    permission.AdminAddMember,
	"qrdb_remove_member": permission.AdminRemoveMember,
	"qrdb_snapshot":      permission.AdminSnapshot,
	"qrdb_drain":         permission.AdminDrain,
	"qrdb_capabilities":  permission.AdminCapabilities,

	// Shard lifecycle / topology.
	"qrdb_start_shard": permission.AdminStartShard,
	"qrdb_stop_shard":  permission.AdminStopShard,
	"qrdb_shard_state": permission.AdminShardState,
	"qrdb_topology":    permission.AdminTopology,

	// User management
	"qrdb_user_create":                 permission.UsersCreate,
	"qrdb_user_get":                    permission.UsersRead,
	"qrdb_user_get_by_username":        permission.UsersRead,
	"qrdb_user_update":                 permission.UsersUpdate,
	"qrdb_user_delete":                 permission.UsersDelete,
	"qrdb_user_list":                   permission.UsersList,
	"qrdb_user_enable":                 permission.UsersEnable,
	"qrdb_user_disable":                permission.UsersDisable,
	"qrdb_user_get_by_cert_thumbprint": permission.UsersGetByCertThumbprint,
	"qrdb_user_add_cert_binding":       permission.UsersAddCertBinding,

	// Role management
	"qrdb_role_create":         permission.RolesCreate,
	"qrdb_role_get":            permission.RolesRead,
	"qrdb_role_get_by_name":    permission.RolesRead,
	"qrdb_role_update":         permission.RolesUpdate,
	"qrdb_role_delete":         permission.RolesDelete,
	"qrdb_role_list":           permission.RolesList,
	"qrdb_role_assign":         permission.RolesAssign,
	"qrdb_role_unassign":       permission.RolesUnassign,
	"qrdb_permissions_list":    permission.PermissionsList,
	"qrdb_permissions_resolve": permission.PermissionsResolve,

	// Certificate management
	"qrdb_cert_generate": permission.CertsGenerate,
	"qrdb_cert_export":   permission.CertsExport,
	"qrdb_cert_revoke":   permission.CertsRevoke,
	"qrdb_cert_list":     permission.CertsList,

	// Tenant management
	"qrdb_tenant_create":        permission.TenantsCreate,
	"qrdb_tenant_get":           permission.TenantsRead,
	"qrdb_tenant_update":        permission.TenantsUpdate,
	"qrdb_tenant_update_quotas": permission.TenantsUpdate,
	"qrdb_tenant_delete":        permission.TenantsDelete,
	"qrdb_tenant_list":          permission.TenantsList,

	// Backup management.
	"qrdb_backup_create":  permission.BackupCreate,
	"qrdb_backup_list":    permission.BackupList,
	"qrdb_backup_get":     permission.BackupRead,
	"qrdb_backup_delete":  permission.BackupDelete,
	"qrdb_backup_restore": permission.BackupRestore,
	"qrdb_backup_verify":  permission.BackupVerify,

	// Cluster rebalance.
	"qrdb_rebalance_shard_stats": permission.RebalanceStats,
	"qrdb_rebalance_plan":        permission.RebalancePlan,
	"qrdb_rebalance_execute":     permission.RebalanceExecute,
	"qrdb_rebalance_report":      permission.RebalanceReport,

	// Per-tenant barrier (tenant-scoped, ScopeTenantOnly). The MCP handler must
	// additionally enforce the fine-grained gate against the request's tenant;
	// this coarse mapping ensures the caller holds the permission at all.
	"qrdb_tenant_barrier_init":   permission.TenantBarrierInit,
	"qrdb_tenant_barrier_unseal": permission.TenantBarrierUnseal,
	"qrdb_tenant_barrier_seal":   permission.TenantBarrierSeal,
	"qrdb_tenant_barrier_status": permission.TenantBarrierStatus,

	// Global barrier management (mirrors restPermissions /v1/barrier/* exactly).
	"qrdb_barrier_initialize":           permission.SealBootstrap,
	"qrdb_barrier_unseal":               permission.SealUnseal,
	"qrdb_barrier_seal":                 permission.SealSeal,
	"qrdb_barrier_status":               permission.SealStatus,
	"qrdb_barrier_shamir_initialize":    permission.SealInitializeShamir,
	"qrdb_barrier_shamir_unseal_share":  permission.BarrierUnsealShare,
	"qrdb_barrier_shamir_unseal_shares": permission.BarrierUnsealShare,
	"qrdb_barrier_rekey":                permission.SealRekey,
	"qrdb_barrier_recovery_generate":    permission.SealGenerateRecovery,
	"qrdb_barrier_recovery_recover":     permission.SealRecoverWithKeys,
	"qrdb_barrier_recovery_delete":      permission.SealGenerateRecovery,
	"qrdb_barrier_recovery_status":      permission.SealStatus,
	"qrdb_barrier_root_token":           permission.SealBootstrap,

	// Ceremony management (mirrors restPermissions /v1/ceremony/*).
	"qrdb_ceremony_initialize":           permission.CeremonyInitialize,
	"qrdb_ceremony_status":               permission.CeremonyStatus,
	"qrdb_ceremony_claim_cert_begin":     permission.CeremonyClaimCert,
	"qrdb_ceremony_claim_cert_complete":  permission.CeremonyClaimCert,
	"qrdb_ceremony_claim_share":          permission.CeremonyClaimShareComplete,
	"qrdb_ceremony_sign_csr":             permission.CeremonySignCSR,
	"qrdb_ceremony_claim_share_begin":    permission.CeremonyClaimShareBegin,
	"qrdb_ceremony_claim_share_complete": permission.CeremonyClaimShareComplete,
	"qrdb_ceremony_submit_unseal_share":  permission.BarrierUnsealShare,

	// Credentials.
	"qrdb_credential_submit":           permission.CredentialsSubmit,
	"qrdb_credential_strategy":         permission.CredentialsStrategy,
	"qrdb_credential_list":             permission.CredentialsList,
	"qrdb_credential_delete":           permission.CredentialsDelete,
	"qrdb_credential_rotate":           permission.CredentialsRotate,
	"qrdb_credential_gc_expired":       permission.CredentialsGCExpired,
	"qrdb_credential_retrieve_version": permission.CredentialsRetrieveVersion,
	"qrdb_credential_list_versions":    permission.CredentialsListVersions,
	"qrdb_credential_store":            permission.CredentialsStore,
	"qrdb_credential_retrieve":         permission.CredentialsRetrieve,

	// Database management.
	"qrdb_database_create":            permission.DatabasesCreate,
	"qrdb_database_get":               permission.DatabasesRead,
	"qrdb_database_list":              permission.DatabasesList,
	"qrdb_database_drop":              permission.DatabasesDelete,
	"qrdb_database_update_barrier":    permission.DatabasesUpdate,
	"qrdb_database_update_connection": permission.DatabasesUpdate,
	"qrdb_database_update_etcd":       permission.DatabasesUpdate,
	"qrdb_database_update_protocol":   permission.DatabasesUpdate,
	"qrdb_database_update_raft":       permission.DatabasesUpdate,
	"qrdb_database_seal":              permission.DatabasesSeal,
	"qrdb_database_unseal":            permission.DatabasesUnseal,
	"qrdb_database_barrier_status":    permission.DatabasesBarrierStatus,

	// Collection / Document management.
	"qrdb_collection_create":         permission.CollectionsCreate,
	"qrdb_collection_get":            permission.CollectionsRead,
	"qrdb_collection_info":           permission.CollectionsRead,
	"qrdb_collection_list":           permission.CollectionsList,
	"qrdb_collection_delete":         permission.CollectionsDelete,
	"qrdb_collection_drop":           permission.CollectionsDrop,
	"qrdb_collection_update":         permission.CollectionsUpdate,
	"qrdb_collection_insert":         permission.CollectionsInsert,
	"qrdb_collection_list_documents": permission.CollectionsList,

	// Distributed locks.
	"qrdb_lock_acquire": permission.LocksAcquire,
	"qrdb_lock_trylock": permission.LocksAcquire,
	"qrdb_lock_release": permission.LocksRelease,
	"qrdb_lock_extend":  permission.LocksExtend,
	"qrdb_lock_status":  permission.LocksStatus,
	"qrdb_lock_list":    permission.LocksList,

	// Embedding.
	"qrdb_embed_text":            permission.EmbeddingEmbed,
	"qrdb_batch_embed":           permission.EmbeddingEmbed,
	"qrdb_list_embedding_models": permission.EmbeddingListModels,

	// RAG.
	"qrdb_rag_ingest":          permission.RAGIngest,
	"qrdb_rag_query":           permission.RAGQuery,
	"qrdb_rag_list_documents":  permission.RAGListDocuments,
	"qrdb_rag_delete_document": permission.RAGDeleteDocument,

	// Audit
	"qrdb_audit_query": permission.AuditQuery,

	// AuthFactor (factor-type challenge/response lifecycle).
	"qrdb_authfactor_enroll":    permission.AuthFactorEnroll,
	"qrdb_authfactor_challenge": permission.AuthFactorChallenge,
	"qrdb_authfactor_verify":    permission.AuthFactorVerify,
	"qrdb_authfactor_list":      permission.AuthFactorList,
	"qrdb_authfactor_revoke":    permission.AuthFactorRevoke,

	// Per-user auth-factor management.
	"qrdb_user_authfactor_add":     permission.UserAuthFactorsAdd,
	"qrdb_user_authfactor_list":    permission.UserAuthFactorsList,
	"qrdb_user_authfactor_get":     permission.UserAuthFactorsGet,
	"qrdb_user_authfactor_remove":  permission.UserAuthFactorsRemove,
	"qrdb_user_authfactor_rotate":  permission.UserAuthFactorsRotate,
	"qrdb_user_authfactor_relabel": permission.UserAuthFactorsRelabel,
	"qrdb_user_authfactor_login":   permission.UserAuthFactorsLogin,
}

// LookupMCP returns the permission required for an MCP tool name. Returns an
// empty Permission and false if the tool has no mapping. Mirrors LookupQUIC.
func LookupMCP(toolName string) (permission.Permission, bool) {
	p, ok := mcpPermissions[toolName]
	return p, ok
}

// LookupGRPC returns the permission required for a gRPC full method name.
// Returns an empty Permission and false if the method has no mapping,
// indicating a public endpoint that does not require authorization.
func LookupGRPC(fullMethod string) (permission.Permission, bool) {
	p, ok := grpcPermissions[fullMethod]
	return p, ok
}

// LookupREST returns the permission required for a REST route identified by
// its HTTP method and path. An exact "METHOD /path" match in restPermissions
// is tried first; on a miss the path is matched against the templated
// path-parameter routes (restPatterns). Returns an empty Permission and false
// only when neither matches, indicating a public endpoint that does not
// require authorization.
func LookupREST(method, path string) (permission.Permission, bool) {
	if p, ok := restPermissions[method+" "+path]; ok {
		return p, true
	}
	return matchRESTPattern(method, path)
}

// matchRESTPattern matches a concrete method+path against the compiled
// templated routes. When several patterns match, the one with the most
// literal (non-wildcard) segments wins, so a fixed route like
// "/users/by-cert-thumbprint/{thumbprint}" is preferred over a more general
// "/users/{id}/..." sibling.
func matchRESTPattern(method, path string) (permission.Permission, bool) {
	reqSegs := strings.Split(strings.Trim(path, "/"), "/")
	best := -1
	var bestPerm permission.Permission
	for i := range restPatterns {
		p := &restPatterns[i]
		if p.method != method || len(p.segments) != len(reqSegs) {
			continue
		}
		matched := true
		for j, seg := range p.segments {
			if seg == "" { // wildcard path parameter
				if reqSegs[j] == "" {
					matched = false
					break
				}
				continue
			}
			if seg != reqSegs[j] {
				matched = false
				break
			}
		}
		if matched && p.literals > best {
			best = p.literals
			bestPerm = p.perm
		}
	}
	if best < 0 {
		return "", false
	}
	return bestPerm, true
}

// sqlPermissions maps SQL statement patterns to the permission required.
// The key format is "VERB table" (e.g., "SELECT kv", "INSERT kv").
// Statements not present in this map are considered public (no
// authorization required), covering SET, SHOW, USE, BEGIN, COMMIT.
var sqlPermissions = map[string]permission.Permission{

	// KV data operations
	"SELECT kv": permission.KVRange,
	"INSERT kv": permission.KVPut,
	"UPDATE kv": permission.KVPut,
	"DELETE kv": permission.KVDelete,

	// Admin read-only tables
	"SELECT status": permission.AdminStatus,
	"SELECT peers":  permission.AdminPeers,
}

// LookupSQL returns the permission required for a SQL operation identified
// by its verb and table name. The lookup key is "VERB table". Returns an
// empty Permission and false if the operation has no mapping, indicating a
// public operation that does not require authorization.
func LookupSQL(key string) (permission.Permission, bool) {
	p, ok := sqlPermissions[key]
	return p, ok
}
