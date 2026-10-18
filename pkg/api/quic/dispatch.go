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

package quic

import (
	"fmt"
	"sort"
	"strings"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// handle registers a unary frame-method handler in the dispatch map and
// declares the permission required to invoke it in the server's
// QUICMethodRegistry — ONE registration per method, with authorization
// declared at the registration site (this replaced the old hand-maintained
// quicPermissions table). AuthorizeQUIC consults the registry with
// DEFAULT-DENY semantics, so a method registered here is reachable only by a
// caller holding perm (or, for the permission.IdentityGated sentinel, an
// authenticated identity; the handler enforces the real binding check).
func (s *Server) handle(method string, perm permission.Permission, h methodHandler) {
	s.handleIf(true, method, perm, h)
}

// handleIf registers the method's PERMISSION unconditionally but attaches
// the handler to the dispatch map only when enabled (its backing service is
// wired). The unconditional registry entry preserves the legacy middleware
// contract, where the permission table was static: a request for an un-wired
// method is authorized against its declared permission first and then falls
// through to the dispatch loop's StatusUnimplemented, exactly as before the
// cutover.
func (s *Server) handleIf(enabled bool, method string, perm permission.Permission, h methodHandler) {
	s.methodPerms.Register(method, perm)
	if !enabled {
		return
	}
	s.handlers[method] = h
}

// handlePublicIf registers a method served WITHOUT authentication or
// authorization (QUICMethodRegistry.RegisterPublic), attaching the handler
// only when enabled, like handleIf. Public methods are a curated built-in
// set, never a module declaration.
func (s *Server) handlePublicIf(enabled bool, method string, h methodHandler) {
	s.methodPerms.RegisterPublic(method)
	if !enabled {
		return
	}
	s.handlers[method] = h
}

// handleStreamOnly declares the permission for a server-streaming method
// that is dispatched by handleStream's stream path (see isWatchMethod)
// rather than the unary dispatch map, so it has a registry entry but no
// methodHandler.
func (s *Server) handleStreamOnly(method string, perm permission.Permission) {
	s.methodPerms.Register(method, perm)
}

// buildHandlers populates the handler dispatch map with all QUIC method
// handlers and, through the s.handle/s.handleIf helpers, declares each
// method's required permission in the QUICMethodRegistry at the same site.
// Each method name maps to a handler function that decodes the protobuf
// request, calls the service layer, and encodes the response. KV and admin
// handlers are registered only when the respective service is non-nil;
// absent methods return StatusUnimplemented via the dispatch loop (their
// permissions are still declared, preserving the static-table contract).
//
// A method with no registration is denied at authorization rather than
// authorized and then answered StatusUnimplemented; that covers the dead
// aliases the unified-authz cutover dropped and the share verbs the unified
// share-holder quorum retired.
func (s *Server) buildHandlers() {
	s.handlers = make(map[string]methodHandler)
	s.methodPerms = authmw.NewQUICMethodRegistry()

	// KV handlers are registered when a KV service is reachable — either the
	// singleton, or a service registry that resolves one per (tenant,
	// database).
	//
	// The registry half was missing, and it made a supported construction
	// unusable: WithServiceRegistry's contract is that data-plane handlers
	// resolve their servicer from the registry "when nil they use the
	// singleton service fields", and every handler below already does exactly
	// that through kvSvcFor. But gating registration on the singleton alone
	// meant a server built with a registry and no singleton kvSvc — the
	// multi-database shape — had NO kv method in the dispatch map at all, so
	// the loop answered StatusUnimplemented before kvSvcFor could resolve
	// anything. The mvcc and tx families never had this problem because they
	// register unconditionally and self-resolve.
	//
	// Found by the local reviewer on the guarded verbs (go-qrdb#124) and fixed
	// for the whole family: making only the three new methods registry-aware
	// would leave a registry-backed server able to CompareAndSwap a key it
	// cannot Put.
	kvOn := s.kvSvc != nil || s.registry != nil
	s.handleIf(kvOn, "kv.Put", permission.KVPut, s.handleKVPut)
	s.handleIf(kvOn, "kv.Get", permission.KVGet, s.handleKVGet)
	s.handleIf(kvOn, "kv.Delete", permission.KVDelete, s.handleKVDelete)
	s.handleIf(kvOn, "kv.List", permission.KVList, s.handleKVList)
	s.handleIf(kvOn, "kv.Scan", permission.KVRange, s.handleKVScan)

	// Guarded write verbs (go-qrdb#124). The permissions match the gRPC
	// service's proto-declared options exactly (qrdb.proto: PutIfAbsent and
	// CompareAndSwap are kv:put, CompareAndDelete is kv:delete) rather than
	// introducing a guarded-specific permission: a guarded put is still a put
	// and a guarded delete is still a delete, so a caller already trusted to
	// write a key is trusted to write it conditionally. Diverging here would
	// mean the same identity could reach the verb over gRPC and not over QUIC,
	// which is the uniformity the capability interface exists to promise.
	s.handleIf(kvOn, "kv.PutIfAbsent", permission.KVPut, s.handleKVPutIfAbsent)
	s.handleIf(kvOn, "kv.CompareAndDelete", permission.KVDelete, s.handleKVCompareAndDelete)
	s.handleIf(kvOn, "kv.CompareAndSwap", permission.KVPut, s.handleKVCompareAndSwap)

	// The admin handlers are registered only when an admin service is
	// provided.
	adminOn := s.adminSvc != nil
	s.handleIf(adminOn, "admin.Health", permission.AdminHealth, s.handleHealth)
	s.handleIf(adminOn, "admin.Status", permission.AdminStatus, s.handleStatus)
	s.handleIf(adminOn, "admin.Peers", permission.AdminPeers, s.handlePeers)
	s.handleIf(adminOn, "admin.AddMember", permission.AdminAddMember, s.handleAddMember)
	s.handleIf(adminOn, "admin.RemoveMember", permission.AdminRemoveMember, s.handleRemoveMember)
	s.handleIf(adminOn, "admin.PromoteMember", permission.AdminAddMember, s.handlePromoteMember)
	s.handleIf(adminOn, "admin.TransferLeadership", permission.AdminRemoveMember, s.handleTransferLeadership)
	s.handleIf(adminOn, "admin.RequestSnapshot", permission.AdminSnapshot, s.handleRequestSnapshot)
	s.handleIf(adminOn, "admin.Drain", permission.AdminDrain, s.handleDrain)
	s.handleIf(adminOn, "admin.Capabilities", permission.AdminCapabilities, s.handleCapabilities)
	s.handleIf(adminOn, "admin.StartShard", permission.AdminStartShard, s.handleStartShard)
	s.handleIf(adminOn, "admin.StopShard", permission.AdminStopShard, s.handleStopShard)
	s.handleIf(adminOn, "admin.GetShardState", permission.AdminShardState, s.handleGetShardState)
	s.handleIf(adminOn, "admin.GetTopology", permission.AdminTopology, s.handleGetTopology)
	s.handleIf(adminOn, "admin.GetReplicaStates", permission.AdminReplicaState, s.handleGetReplicaStates)
	s.handleIf(adminOn, "admin.GetNodeCompaction", permission.AdminNodeCompaction, s.handleGetNodeCompaction)
	s.handleIf(adminOn, "admin.SetNodeCompaction", permission.AdminNodeCompaction, s.handleSetNodeCompaction)
	s.handleIf(adminOn, "admin.GetStateHashes", permission.AdminStateHash, s.handleGetStateHashes)
	s.handleIf(adminOn, "admin.MintJoinToken", permission.ClusterJoinToken, s.handleMintJoinToken)
	s.handleIf(adminOn, "admin.AddObserverMember", permission.AdminAddMember, s.handleAddObserverMember)
	s.handleIf(adminOn, "admin.ListPendingJoinRequests", permission.ClusterJoinToken, s.handleListPendingJoinRequests)
	s.handleIf(adminOn, "admin.ApproveJoinRequest", permission.ClusterJoinToken, s.handleApproveJoinRequest)
	s.handleIf(adminOn, "admin.RejectJoinRequest", permission.ClusterJoinToken, s.handleRejectJoinRequest)
	s.handleIf(adminOn, "admin.RetireJoinIdentity", permission.ClusterJoinIdentityRetire, s.handleRetireJoinIdentity)

	// MVCC
	s.handle("mvcc.PutWithLease", permission.MVCCPutWithLease, s.handlePutWithLease)
	s.handle("mvcc.GetRevision", permission.MVCCGetRevision, s.handleGetRevision)
	s.handle("mvcc.GetAtRevision", permission.MVCCGetAtRevision, s.handleGetAtRevision)
	s.handle("mvcc.Range", permission.MVCCRange, s.handleRange)
	s.handle("mvcc.Compact", permission.MVCCCompact, s.handleCompact)
	s.handle("mvcc.CurrentRevision", permission.MVCCCurrentRevision, s.handleCurrentRevision)
	s.handle("mvcc.RestoreToRevision", permission.MVCCRestore, s.handleRestoreToRevision)

	// Transaction
	s.handle("tx.Transaction", permission.TransactionExecute, s.handleTransaction)

	// Batch
	s.handle("batch.Batch", permission.BatchExecute, s.handleBatch)
	s.handle("batch.ImportBegin", permission.BatchExecute, s.handleImportBegin)
	s.handle("batch.ImportAppend", permission.BatchExecute, s.handleImportAppend)
	s.handle("batch.ImportCommit", permission.BatchExecute, s.handleImportCommit)
	s.handle("batch.ImportAbort", permission.BatchExecute, s.handleImportAbort)

	// Watch is server-streaming: handled by handleWatchStream in the stream
	// dispatch path (see handleStream + isWatchMethod), NOT registered here as
	// a unary handler. Authorization still applies: the stream methods declare
	// their permission in the registry so AuthorizeQUIC resolves them.
	s.handleStreamOnly("watch.Watch", permission.WatchWatch)
	s.handleStreamOnly("watch.WatchPrefix", permission.WatchWatch)

	// Lease
	s.handle("lease.Grant", permission.LeaseGrant, s.handleLeaseGrant)
	s.handle("lease.Revoke", permission.LeaseRevoke, s.handleLeaseRevoke)
	s.handle("lease.KeepAlive", permission.LeaseKeepAlive, s.handleLeaseKeepAlive)
	s.handle("lease.TimeToLive", permission.LeaseTTL, s.handleLeaseTimeToLive)
	s.handle("lease.List", permission.LeaseList, s.handleLeaseList)

	// Lock
	s.handle("lock.Acquire", permission.LocksAcquire, s.handleLockAcquire)
	s.handle("lock.TryLock", permission.LocksAcquire, s.handleLockTryLock)
	s.handle("lock.Release", permission.LocksRelease, s.handleLockRelease)
	s.handle("lock.Extend", permission.LocksExtend, s.handleLockExtend)
	s.handle("lock.Status", permission.LocksStatus, s.handleLockStatus)
	s.handle("lock.List", permission.LocksList, s.handleLockList)

	// Index
	s.handle("index.Create", permission.IndexCreate, s.handleIndexCreate)
	s.handle("index.Drop", permission.IndexDrop, s.handleIndexDrop)
	s.handle("index.Query", permission.IndexQuery, s.handleIndexQuery)
	s.handle("index.Scan", permission.IndexScan, s.handleIndexScan)
	s.handle("index.List", permission.IndexList, s.handleIndexList)

	// TimeSeries
	s.handle("ts.Write", permission.TSWrite, s.handleTSWrite)
	s.handle("ts.WriteBatch", permission.TSWriteBatch, s.handleTSWriteBatch)
	s.handle("ts.Query", permission.TSQuery, s.handleTSQuery)
	s.handle("ts.Aggregate", permission.TSAggregate, s.handleTSAggregate)
	s.handle("ts.WritePoint", permission.TSWritePoint, s.handleTSWritePoint)
	s.handle("ts.RichQuery", permission.TSRichQuery, s.handleTSRichQuery)
	s.handle("ts.DeleteSeries", permission.TSDeleteSeries, s.handleTSDeleteSeries)
	s.handle("ts.GetRetention", permission.TSGetRetention, s.handleTSGetRetention)
	s.handle("ts.SetRetention", permission.TSSetRetention, s.handleTSSetRetention)
	s.handle("ts.ListMetrics", permission.TSListMetrics, s.handleTSListMetrics)

	// Vector
	s.handle("vector.CreateNamespace", permission.VectorNamespaceCreate, s.handleVectorCreateNamespace)
	s.handle("vector.DeleteNamespace", permission.VectorNamespaceDelete, s.handleVectorDeleteNamespace)
	s.handle("vector.ListNamespaces", permission.VectorNamespaceList, s.handleVectorListNamespaces)
	s.handle("vector.GetNamespaceInfo", permission.VectorNamespaceInfo, s.handleVectorGetNamespaceInfo)
	s.handle("vector.Upsert", permission.VectorUpsert, s.handleVectorUpsert)
	s.handle("vector.Get", permission.VectorGet, s.handleVectorGet)
	s.handle("vector.Delete", permission.VectorDelete, s.handleVectorDelete)
	s.handle("vector.BatchUpsert", permission.VectorBatchUpsert, s.handleVectorBatchUpsert)
	s.handle("vector.BatchDelete", permission.VectorBatchDelete, s.handleVectorBatchDelete)
	s.handle("vector.List", permission.VectorList, s.handleVectorList)
	s.handle("vector.UpdateMetadata", permission.VectorUpdateMetadata, s.handleVectorUpdateMetadata)
	s.handle("vector.SearchSimilar", permission.VectorSearchSimilar, s.handleVectorSearchSimilar)
	s.handle("vector.SearchByMetadata", permission.VectorSearchByMetadata, s.handleVectorSearchByMetadata)
	s.handle("vector.HybridSearch", permission.VectorHybridSearch, s.handleVectorHybridSearch)
	s.handle("vector.SearchByID", permission.VectorSearchByID, s.handleVectorSearchByID)
	s.handle("vector.RebuildIndex", permission.VectorRebuildIndex, s.handleVectorRebuildIndex)
	s.handle("vector.GetRebuildProgress", permission.VectorGetRebuildProg, s.handleVectorGetRebuildProgress)
	s.handle("vector.GetIndexStats", permission.VectorGetIndexStats, s.handleVectorGetIndexStats)
	s.handle("vector.CompactNamespace", permission.VectorCompact, s.handleVectorCompactNamespace)
	s.handle("vector.GetCompactionProgress", permission.VectorGetCompactProg, s.handleVectorGetCompactionProgress)

	// User management. Thumbprint lookup is a read, cert binding is a user
	// mutation (parity with gRPC/REST).
	s.handle("user.Create", permission.UsersCreate, s.handleCreateUser)
	s.handle("user.Get", permission.UsersRead, s.handleGetUser)
	s.handle("user.GetByUsername", permission.UsersRead, s.handleGetUserByUsername)
	s.handle("user.GetByEmail", permission.UsersRead, s.handleGetUserByEmail)
	s.handle("user.GetByCertThumbprint", permission.UsersGetByCertThumbprint, s.handleGetUserByCertThumbprint)
	s.handle("user.AddCertBinding", permission.UsersAddCertBinding, s.handleAddCertBinding)
	s.handle("user.RemoveCertBinding", permission.UsersRemoveCertBinding, s.handleRemoveCertBinding)
	s.handle("user.Update", permission.UsersUpdate, s.handleUpdateUser)
	s.handle("user.Delete", permission.UsersDelete, s.handleDeleteUser)
	s.handle("user.List", permission.UsersList, s.handleListUsers)
	s.handle("user.Enable", permission.UsersEnable, s.handleEnableUser)
	s.handle("user.Disable", permission.UsersDisable, s.handleDisableUser)

	// Role management
	s.handle("role.Create", permission.RolesCreate, s.handleCreateRole)
	s.handle("role.Get", permission.RolesRead, s.handleGetRole)
	s.handle("role.GetByName", permission.RolesRead, s.handleGetRoleByName)
	s.handle("role.Update", permission.RolesUpdate, s.handleUpdateRole)
	s.handle("role.Delete", permission.RolesDelete, s.handleDeleteRole)
	s.handle("role.List", permission.RolesList, s.handleListRoles)
	s.handle("role.Assign", permission.RolesAssign, s.handleAssignRole)
	s.handle("role.Unassign", permission.RolesUnassign, s.handleUnassignRole)
	s.handle("role.ListPermissions", permission.PermissionsList, s.handleListPermissions)
	s.handle("role.ResolvePermissions", permission.PermissionsResolve, s.handleResolvePermissions)
	// The caller's own grants need authentication only: the identity is the
	// authenticated one, never a payload field.
	s.handle("role.ListOwnPermissions", permission.IdentityGated, s.handleListOwnPermissions)
	// Asking about the caller needs authentication only; asking about
	// another identity is gated in the evaluator by permissions:resolve.
	s.handle("role.Authorize", permission.IdentityGated, s.handleAuthorize)
	// roles:create, not a verb of its own — see the REST route comment: a
	// dedicated permission would be missing from every role seeded before it
	// existed, so nobody could hold the grant needed to repair that absence.
	s.handle("role.ReconcileBuiltIn", permission.RolesCreate, s.handleReconcileRoles)

	// Certificate management
	s.handle("cert.Issue", permission.CertsIssue, s.handleIssueCert)
	s.handle("cert.Enroll", permission.CertsEnroll, s.handleEnrollCert)
	s.handle("cert.Delegate", permission.CertsDelegate, s.handleIssueDelegation)
	s.handle("cert.Revoke", permission.CertsRevoke, s.handleRevokeCert)
	s.handle("cert.List", permission.CertsList, s.handleListCerts)

	// Invitation self-enrolment: user.Invite is the caller's own tenant's
	// (users:invite); invitation.Redeem is PUBLIC -- the redeemer holds no
	// credential yet, so the token is the authority (see server.go's auth
	// block, which skips both authentication and authorization for it).
	invitesOn := s.invitations != nil
	s.handleIf(invitesOn, methodInviteUser, permission.UsersInvite, s.handleInviteUser)
	s.handlePublicIf(invitesOn, methodRedeemInvitation, s.handleRedeemInvitation)

	// Collection
	s.handle("collection.Create", permission.CollectionsCreate, s.handleCollectionCreate)
	s.handle("collection.GetInfo", permission.CollectionsRead, s.handleCollectionGetInfo)
	s.handle("collection.List", permission.CollectionsList, s.handleCollectionList)
	s.handle("collection.Drop", permission.CollectionsDrop, s.handleCollectionDrop)
	s.handle("collection.InsertDocument", permission.CollectionsInsert, s.handleCollectionInsertDocument)
	s.handle("collection.GetDocument", permission.CollectionsRead, s.handleCollectionGetDocument)
	s.handle("collection.UpdateDocument", permission.CollectionsUpdate, s.handleCollectionUpdateDocument)
	s.handle("collection.DeleteDocument", permission.CollectionsDelete, s.handleCollectionDeleteDocument)
	s.handle("collection.ListDocuments", permission.CollectionsList, s.handleCollectionListDocuments)

	// Database management
	s.handle("db.Create", permission.DatabasesCreate, s.handleDatabaseCreate)
	s.handle("db.Get", permission.DatabasesRead, s.handleDatabaseGet)
	s.handle("db.List", permission.DatabasesList, s.handleDatabaseList)
	s.handle("db.Drop", permission.DatabasesDelete, s.handleDatabaseDrop)
	s.handle("db.UpdateProtocol", permission.DatabasesUpdate, s.handleDatabaseUpdateProtocol)
	s.handle("db.UpdateEtcd", permission.DatabasesUpdate, s.handleDatabaseUpdateEtcd)
	s.handle("db.UpdateRaft", permission.DatabasesUpdate, s.handleDatabaseUpdateRaft)
	s.handle("db.Scale", permission.DatabasesScale, s.handleDatabaseScale)
	s.handle("db.UpdateBarrier", permission.DatabasesUpdate, s.handleDatabaseUpdateBarrier)
	s.handle("db.UpdateConnection", permission.DatabasesUpdate, s.handleDatabaseUpdateConnection)
	s.handle("db.UpdateSQL", permission.DatabasesUpdate, s.handleDatabaseUpdateSQL)
	// Setting a compaction policy is compacting on a schedule.
	s.handle("db.UpdateCompaction", permission.MVCCCompact, s.handleDatabaseUpdateCompaction)
	s.handle("db.Seal", permission.DatabasesSeal, s.handleDatabaseSeal)
	s.handle("db.Unseal", permission.DatabasesUnseal, s.handleDatabaseUnseal)
	s.handle("db.BarrierStatus", permission.DatabasesBarrierStatus, s.handleDatabaseBarrierStatus)
	// Shamir custody of a database barrier; claims and quorum submissions are
	// identity-gated.
	s.handle("db.BarrierOfficerRegister", permission.DatabasesOfficerRegister, s.handleDatabaseBarrierOfficerRegister)
	s.handle("db.BarrierOfficerList", permission.DatabasesOfficerList, s.handleDatabaseBarrierOfficerList)
	s.handle("db.BarrierShamirInitialize", permission.DatabasesInitializeShamir, s.handleDatabaseBarrierShamirInitialize)
	s.handle("db.BarrierShareClaimBegin", permission.IdentityGated, s.handleDatabaseBarrierShareClaimBegin)
	s.handle("db.BarrierShareClaimComplete", permission.IdentityGated, s.handleDatabaseBarrierShareClaimComplete)
	s.handle("db.BarrierQuorumBegin", permission.IdentityGated, s.handleDatabaseBarrierQuorumBegin)
	s.handle("db.BarrierQuorumSubmit", permission.IdentityGated, s.handleDatabaseBarrierQuorumSubmit)
	s.handle("db.BarrierRekeyStart", permission.DatabasesRekey, s.handleDatabaseBarrierRekeyStart)
	s.handle("db.BarrierOfficerRevoke", permission.DatabasesOfficerRevoke, s.handleDatabaseBarrierOfficerRevoke)
	s.handle("db.BarrierOfficerUnenrol", permission.DatabasesOfficerUnenrol, s.handleDatabaseBarrierOfficerUnenrol)

	// SQL query (read-through over the KV range surface). Mirrors the REST
	// "POST /sql/query" route / the gRPC SQLQuery permission option.
	s.handle("sql.Query", permission.KVRange, s.handleSQLQuery)

	// Barrier
	s.handle("barrier.Initialize", permission.SealBootstrap, s.handleBarrierInitialize)
	s.handle("barrier.Unseal", permission.SealUnseal, s.handleBarrierUnseal)
	s.handle("barrier.Seal", permission.SealSeal, s.handleBarrierSeal)
	s.handle("barrier.Status", permission.SealStatus, s.handleBarrierStatus)
	s.handle("barrier.ClusterUnseal", permission.SealUnseal, s.handleBarrierClusterUnseal)
	s.handle("barrier.ShamirInitialize", permission.SealInitializeShamir, s.handleBarrierShamirInitialize)
	s.handle("barrier.RecoveryGenerate", permission.SealGenerateRecovery, s.handleBarrierRecoveryGenerate)
	s.handle("barrier.RecoveryDelete", permission.SealGenerateRecovery, s.handleBarrierRecoveryDelete)
	s.handle("barrier.RecoveryStatus", permission.SealStatus, s.handleBarrierRecoveryStatus)
	// Barrier Security Officer (system barrier SO distribution). Share-claim
	// routes are identity-gated (see permission.IdentityGated): the registered
	// officer's mTLS certificate is the credential, and the handler enforces
	// registry lookup, thumbprint match, and nonce signature.
	s.handle("barrier.OfficerRegister", permission.SealOfficerRegister, s.handleBarrierOfficerRegister)
	s.handle("barrier.OfficerList", permission.SealOfficerList, s.handleBarrierOfficerList)
	s.handle("barrier.ShareClaimBegin", permission.IdentityGated, s.handleBarrierShareClaimBegin)
	s.handle("barrier.ShareClaimComplete", permission.IdentityGated, s.handleBarrierShareClaimComplete)
	s.handle("barrier.RecoveryOfficerRegister", permission.SealOfficerRegister, s.handleBarrierRecoveryOfficerRegister)
	s.handle("barrier.RecoveryShareClaimBegin", permission.IdentityGated, s.handleBarrierRecoveryShareClaimBegin)
	s.handle("barrier.RecoveryShareClaimComplete", permission.IdentityGated, s.handleBarrierRecoveryShareClaimComplete)
	s.handle("barrier.QuorumBegin", permission.IdentityGated, s.handleBarrierQuorumBegin)
	s.handle("barrier.QuorumSubmit", permission.IdentityGated, s.handleBarrierQuorumSubmit)
	s.handle("barrier.RekeyStart", permission.SealRekey, s.handleBarrierRekeyStart)
	s.handle("barrier.OfficerRevoke", permission.SealOfficerRevoke, s.handleBarrierOfficerRevoke)
	// Authorized per request: seal:enable_encryption for the node's barrier,
	// tenant_barrier:enable_encryption for a named tenant's (go-qrdb#791).
	s.handle("barrier.EnableEncryption", permission.IdentityGated, s.handleBarrierEnableEncryption)

	// Ceremony. Share holders claim their envelopes through the barrier
	// holder methods above (barrier.ShareClaimBegin/Complete), not here.
	s.handle("ceremony.Initialize", permission.CeremonyInitialize, s.handleCeremonyInitialize)
	s.handle("ceremony.Status", permission.CeremonyStatus, s.handleCeremonyStatus)
	s.handle("ceremony.SignCSR", permission.CeremonySignCSR, s.handleCeremonySignCSR)

	// Tenant management
	s.handle("tenant.Create", permission.TenantsCreate, s.handleTenantCreate)
	s.handle("tenant.TrustAnchorAdd", permission.TrustAnchorsAdd, s.handleTrustAnchorAdd)
	s.handle("tenant.TrustAnchorList", permission.TrustAnchorsList, s.handleTrustAnchorList)
	s.handle("tenant.TrustAnchorRemove", permission.TrustAnchorsRemove, s.handleTrustAnchorRemove)
	s.handle("tenant.Get", permission.TenantsRead, s.handleTenantGet)
	s.handle("tenant.List", permission.TenantsList, s.handleTenantList)
	s.handle("tenant.Update", permission.TenantsUpdate, s.handleTenantUpdate)
	s.handle("tenant.Delete", permission.TenantsDelete, s.handleTenantDelete)
	s.handle("tenant.UpdateQuotas", permission.TenantsSetQuotas, s.handleTenantUpdateQuotas)
	s.handle("tenant.UpdateEntitlements", permission.TenantsSetEntitlements, s.handleTenantUpdateEntitlements)
	s.handle("tenant.ListDeleted", permission.TenantsListDeleted, s.handleTenantListDeleted)
	s.handle("tenant.ReconcileDeleted", permission.TenantsReconcileDeleted, s.handleTenantReconcileDeleted)

	// Volume pool management (docs/volume-pools/api.md)
	s.handle("volumepool.List", permission.VolumePoolList, s.handleVolumePoolList)
	s.handle("volumepool.Get", permission.VolumePoolRead, s.handleVolumePoolGet)
	s.handle("volumepool.Create", permission.VolumePoolCreate, s.handleVolumePoolCreate)
	s.handle("volumepool.Update", permission.VolumePoolUpdate, s.handleVolumePoolUpdate)
	s.handle("volumepool.Delete", permission.VolumePoolDelete, s.handleVolumePoolDelete)
	s.handle("volumepool.ListTenants", permission.VolumePoolRead, s.handleVolumePoolListTenants)

	// Credential management
	credOn := s.credentialSvc != nil
	s.handleIf(credOn, "credential.Submit", permission.CredentialsSubmit, s.handleCredentialSubmit)
	s.handleIf(credOn, "credential.Strategy", permission.CredentialsStrategy, s.handleCredentialStrategy)
	s.handleIf(credOn, "credential.Delete", permission.CredentialsDelete, s.handleCredentialDelete)
	s.handleIf(credOn, "credential.List", permission.CredentialsList, s.handleCredentialList)
	s.handleIf(credOn, "credential.Rotate", permission.CredentialsRotate, s.handleCredentialRotate)
	s.handleIf(credOn, "credential.RetrieveVersion", permission.CredentialsRetrieveVersion, s.handleCredentialRetrieveVersion)
	s.handleIf(credOn, "credential.ListVersions", permission.CredentialsListVersions, s.handleCredentialListVersions)
	s.handleIf(credOn, "credential.GCExpired", permission.CredentialsGCExpired, s.handleCredentialGCExpired)
	s.handleIf(credOn, "credential.Store", permission.CredentialsStore, s.handleCredentialStore)
	s.handleIf(credOn, "credential.Retrieve", permission.CredentialsRetrieve, s.handleCredentialRetrieve)

	// Tenant DEK keyring. Every method acts on the caller's own tenant.
	dekOn := s.dekSvc != nil
	s.handleIf(dekOn, "dek.Status", permission.DEKStatus, s.handleDEKStatus)
	s.handleIf(dekOn, "dek.Rotate", permission.DEKRotate, s.handleDEKRotate)
	s.handleIf(dekOn, "dek.Retire", permission.DEKRetire, s.handleDEKRetire)
	s.handleIf(dekOn, "dek.Encrypt", permission.DEKEncrypt, s.handleDEKEncrypt)
	s.handleIf(dekOn, "dek.Decrypt", permission.DEKDecrypt, s.handleDEKDecrypt)
	s.handleIf(dekOn, "dek.Rewrap", permission.DEKRewrap, s.handleDEKRewrap)

	// Per-tenant barrier lifecycle and SO distribution. These carry the
	// tenant-scoped (ScopeTenantOnly) permissions; the handler additionally
	// enforces the fine-grained gate against the request's tenant_id.
	// Share-claim routes are identity-gated (see permission.IdentityGated);
	// the handler enforces the per-tenant registry lookup, thumbprint match,
	// and nonce signature.
	tenantOn := s.tenantSvc != nil
	// Init/Unseal/UnsealShare/Status are IdentityGated at the dispatch
	// layer: a self-managed tenant's IAM is encrypted behind the very
	// barrier these operate on, so the RBAC check runs inside the handlers
	// with the shared trust-on-first-init / sealed-window fallbacks
	// (middleware.BeginTenantBarrierInit / AuthorizeTenantBarrierInitProof /
	// AuthorizeTenantBarrierSealedOp), identical to REST, gRPC, and MCP.
	s.handleIf(tenantOn, "tenant.BarrierInitBegin", permission.IdentityGated, s.handleTenantBarrierInitBegin)
	s.handleIf(tenantOn, "tenant.BarrierInitComplete", permission.IdentityGated, s.handleTenantBarrierInit)
	s.handleIf(tenantOn, "tenant.BarrierUnseal", permission.IdentityGated, s.handleTenantBarrierUnseal)
	s.handleIf(tenantOn, "tenant.BarrierSeal", permission.TenantBarrierSeal, s.handleTenantBarrierSeal)
	s.handleIf(tenantOn, "tenant.BarrierStatus", permission.IdentityGated, s.handleTenantBarrierStatus)
	s.handleIf(tenantOn, "tenant.BarrierOfficerRegister", permission.TenantBarrierOfficerRegister, s.handleTenantBarrierOfficerRegister)
	s.handleIf(tenantOn, "tenant.BarrierOfficerList", permission.TenantBarrierOfficerList, s.handleTenantBarrierOfficerList)
	s.handleIf(tenantOn, "tenant.BarrierShareClaimBegin", permission.IdentityGated, s.handleTenantBarrierShareClaimBegin)
	s.handleIf(tenantOn, "tenant.BarrierShareClaimComplete", permission.IdentityGated, s.handleTenantBarrierShareClaimComplete)
	s.handleIf(tenantOn, "tenant.BarrierQuorumBegin", permission.IdentityGated, s.handleTenantBarrierQuorumBegin)
	s.handleIf(tenantOn, "tenant.BarrierQuorumSubmit", permission.IdentityGated, s.handleTenantBarrierQuorumSubmit)
	s.handleIf(tenantOn, "tenant.BarrierRekeyStart", permission.TenantBarrierRekey, s.handleTenantBarrierRekeyStart)
	s.handleIf(tenantOn, "tenant.BarrierOfficerRevoke", permission.TenantBarrierOfficerRevoke, s.handleTenantBarrierOfficerRevoke)

	// Backup management
	backupOn := s.backupSvc != nil
	s.handleIf(backupOn, "backup.Create", permission.BackupCreate, s.handleBackupCreate)
	s.handleIf(backupOn, "backup.List", permission.BackupList, s.handleBackupList)
	s.handleIf(backupOn, "backup.Get", permission.BackupRead, s.handleBackupGet)
	s.handleIf(backupOn, "backup.Delete", permission.BackupDelete, s.handleBackupDelete)
	s.handleIf(backupOn, "backup.Restore", permission.BackupRestore, s.handleBackupRestore)
	s.handleIf(backupOn, "backup.Verify", permission.BackupVerify, s.handleBackupVerify)

	// Cluster rebalance
	rebalanceOn := s.rebalanceSvc != nil
	s.handleIf(rebalanceOn, "rebalance.GetShardStats", permission.RebalanceStats, s.handleRebalanceGetShardStats)
	s.handleIf(rebalanceOn, "rebalance.CreatePlan", permission.RebalancePlan, s.handleRebalanceCreatePlan)
	s.handleIf(rebalanceOn, "rebalance.Execute", permission.RebalanceExecute, s.handleRebalanceExecute)
	s.handleIf(rebalanceOn, "rebalance.GetImbalanceReport", permission.RebalanceReport, s.handleRebalanceGetImbalanceReport)

	// Embedding
	embeddingOn := s.embeddingSvc != nil
	s.handleIf(embeddingOn, "embedding.Embed", permission.EmbeddingEmbed, s.handleEmbed)
	s.handleIf(embeddingOn, "embedding.BatchEmbed", permission.EmbeddingEmbed, s.handleBatchEmbed)
	s.handleIf(embeddingOn, "embedding.ListModels", permission.EmbeddingListModels, s.handleListEmbeddingModels)

	// RAG (retrieval-augmented generation)
	ragOn := s.ragSvc != nil
	s.handleIf(ragOn, "rag.IngestDocument", permission.RAGIngest, s.handleRAGIngestDocument)
	s.handleIf(ragOn, "rag.Query", permission.RAGQuery, s.handleRAGQuery)
	s.handleIf(ragOn, "rag.DeleteDocument", permission.RAGDeleteDocument, s.handleRAGDeleteDocument)
	s.handleIf(ragOn, "rag.ListDocuments", permission.RAGListDocuments, s.handleRAGListDocuments)

	// Auth factors (factor-type challenge/response lifecycle)
	factorOn := s.authFactorSvc != nil
	s.handleIf(factorOn, "authfactor.Enroll", permission.AuthFactorEnroll, s.handleAuthFactorEnroll)
	s.handleIf(factorOn, "authfactor.Challenge", permission.AuthFactorChallenge, s.handleAuthFactorChallenge)
	s.handleIf(factorOn, "authfactor.Verify", permission.AuthFactorVerify, s.handleAuthFactorVerify)
	s.handleIf(factorOn, "authfactor.List", permission.AuthFactorList, s.handleAuthFactorList)
	s.handleIf(factorOn, "authfactor.Revoke", permission.AuthFactorRevoke, s.handleAuthFactorRevoke)

	// User auth factors (M14)
	userFactorOn := s.userAuthFactorSvc != nil
	s.handleIf(userFactorOn, "userauthfactor.Add", permission.UserAuthFactorsAdd, s.handleUserAuthFactorAdd)
	s.handleIf(userFactorOn, "userauthfactor.List", permission.UserAuthFactorsList, s.handleUserAuthFactorList)
	s.handleIf(userFactorOn, "userauthfactor.Get", permission.UserAuthFactorsGet, s.handleUserAuthFactorGet)
	s.handleIf(userFactorOn, "userauthfactor.Remove", permission.UserAuthFactorsRemove, s.handleUserAuthFactorRemove)
	s.handleIf(userFactorOn, "userauthfactor.Delete", permission.UserAuthFactorsAdd, s.handleUserAuthFactorDelete)
	s.handleIf(userFactorOn, "userauthfactor.Rotate", permission.UserAuthFactorsRotate, s.handleUserAuthFactorRotate)
	s.handleIf(userFactorOn, "userauthfactor.Relabel", permission.UserAuthFactorsRelabel, s.handleUserAuthFactorRelabel)
	s.handleIf(userFactorOn, "userauthfactor.Login", permission.UserAuthFactorsLogin, s.handleUserAuthFactorLogin)

	// Audit
	s.handleIf(s.auditLogger != nil || s.replicatedAudit != nil, "audit.Query", permission.AuditQuery, s.handleAuditQuery)
	s.handleIf(s.auditLogger != nil, "audit.VerifyChain", permission.AuditVerify, s.handleAuditVerifyChain)
	s.handleIf(s.replicatedAudit != nil, "audit.VerifyReplicatedChain", permission.AuditVerifyReplicated, s.handleAuditVerifyReplicatedChain)

	// Capability modules (pkg/apimodule.ServiceModule). Registered last so a
	// module's full-gRPC-method frame key can never shadow a built-in's
	// dot-separated key; see registerModules.
	s.registerModules()

	s.assertMethodsAuthorized()
}

// assertMethodsAuthorized is the DEFAULT-DENY startup assertion: it walks the
// dispatch map and panics if any registered handler is missing from the
// methodPerms registry (i.e. was assigned to s.handlers directly instead of
// through s.handle/s.handleIf, and would therefore be denied at runtime).
// This mirrors the REST server's assertRoutesAuthorized structural invariant.
func (s *Server) assertMethodsAuthorized() {
	var missing []string
	for method := range s.handlers {
		if !s.methodPerms.Covered(method) {
			missing = append(missing, method)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		panic(fmt.Sprintf(
			"quic: DEFAULT-DENY startup assertion failed: %d method(s) registered without a permission: %s",
			len(missing), strings.Join(missing, ", ")))
	}
}

// MethodPermissions returns a snapshot of the per-method permission table
// this server's handlers declared at registration time (the s.handle,
// s.handleIf and s.handleStreamOnly calls in buildHandlers, above). It
// exists for cross-transport parity tests -- the embedded transport's own
// operationPermissions table (sdk/go/transport/embedded) is a hand
// transcription of this one, and a drift test needs a way to read QUIC's
// copy from outside this package without duplicating buildHandlers'
// construction. The entries cover every declared method regardless of
// whether its backing service is wired (handleIf registers the permission
// unconditionally), matching methodPerms' own "static table" contract.
//
// It is a read-only snapshot: mutating the returned map does not affect
// this server's live registry.
func (s *Server) MethodPermissions() map[string]permission.Permission {
	return s.methodPerms.Entries()
}
