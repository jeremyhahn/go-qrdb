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

// buildHandlers populates the handler dispatch map with all QUIC method
// handlers. Each method name maps to a handler function that decodes the
// protobuf request, calls the service layer, and encodes the response.
// KV and admin handlers are registered only when the respective service
// is non-nil; absent methods return StatusUnimplemented via the dispatch loop.
func (s *Server) buildHandlers() {
	s.handlers = make(map[string]methodHandler)

	// KV handlers are registered only when a KV service is provided.
	if s.kvSvc != nil {
		s.handlers["kv.Put"] = s.handleKVPut
		s.handlers["kv.Get"] = s.handleKVGet
		s.handlers["kv.Delete"] = s.handleKVDelete
		s.handlers["kv.List"] = s.handleKVList
		s.handlers["kv.Scan"] = s.handleKVScan
	}

	// Admin handlers are registered only when an admin service is provided.
	if s.adminSvc != nil {
		s.handlers["admin.Health"] = s.handleHealth
		s.handlers["admin.Status"] = s.handleStatus
		s.handlers["admin.Peers"] = s.handlePeers
		s.handlers["admin.AddMember"] = s.handleAddMember
		s.handlers["admin.RemoveMember"] = s.handleRemoveMember
		s.handlers["admin.RequestSnapshot"] = s.handleRequestSnapshot
		s.handlers["admin.Drain"] = s.handleDrain
		s.handlers["admin.Capabilities"] = s.handleCapabilities
		s.handlers["admin.StartShard"] = s.handleStartShard
		s.handlers["admin.StopShard"] = s.handleStopShard
		s.handlers["admin.GetShardState"] = s.handleGetShardState
		s.handlers["admin.GetTopology"] = s.handleGetTopology
	}

	// MVCC
	s.handlers["mvcc.PutWithLease"] = s.handlePutWithLease
	s.handlers["mvcc.GetRevision"] = s.handleGetRevision
	s.handlers["mvcc.GetAtRevision"] = s.handleGetAtRevision
	s.handlers["mvcc.Range"] = s.handleRange
	s.handlers["mvcc.Compact"] = s.handleCompact
	s.handlers["mvcc.CurrentRevision"] = s.handleCurrentRevision

	// Transaction
	s.handlers["tx.Transaction"] = s.handleTransaction

	// Batch
	s.handlers["batch.Batch"] = s.handleBatch

	// Watch — streaming handlers are registered separately
	s.handlers["watch.Watch"] = s.handleWatchSetup
	s.handlers["watch.WatchPrefix"] = s.handleWatchPrefixSetup

	// Lease
	s.handlers["lease.Grant"] = s.handleLeaseGrant
	s.handlers["lease.Revoke"] = s.handleLeaseRevoke
	s.handlers["lease.KeepAlive"] = s.handleLeaseKeepAlive
	s.handlers["lease.TimeToLive"] = s.handleLeaseTimeToLive
	s.handlers["lease.List"] = s.handleLeaseList

	// Lock
	s.handlers["lock.Acquire"] = s.handleLockAcquire
	s.handlers["lock.TryLock"] = s.handleLockTryLock
	s.handlers["lock.Release"] = s.handleLockRelease
	s.handlers["lock.Extend"] = s.handleLockExtend
	s.handlers["lock.Status"] = s.handleLockStatus
	s.handlers["lock.List"] = s.handleLockList

	// Index
	s.handlers["index.Create"] = s.handleIndexCreate
	s.handlers["index.Drop"] = s.handleIndexDrop
	s.handlers["index.Query"] = s.handleIndexQuery
	s.handlers["index.Scan"] = s.handleIndexScan
	s.handlers["index.List"] = s.handleIndexList

	// TimeSeries
	s.handlers["ts.Write"] = s.handleTSWrite
	s.handlers["ts.WriteBatch"] = s.handleTSWriteBatch
	s.handlers["ts.Query"] = s.handleTSQuery
	s.handlers["ts.Aggregate"] = s.handleTSAggregate
	s.handlers["ts.WritePoint"] = s.handleTSWritePoint
	s.handlers["ts.RichQuery"] = s.handleTSRichQuery
	s.handlers["ts.DeleteSeries"] = s.handleTSDeleteSeries
	s.handlers["ts.GetRetention"] = s.handleTSGetRetention
	s.handlers["ts.SetRetention"] = s.handleTSSetRetention
	s.handlers["ts.ListMetrics"] = s.handleTSListMetrics

	// Vector
	s.handlers["vector.CreateNamespace"] = s.handleVectorCreateNamespace
	s.handlers["vector.DeleteNamespace"] = s.handleVectorDeleteNamespace
	s.handlers["vector.ListNamespaces"] = s.handleVectorListNamespaces
	s.handlers["vector.GetNamespaceInfo"] = s.handleVectorGetNamespaceInfo
	s.handlers["vector.Upsert"] = s.handleVectorUpsert
	s.handlers["vector.Get"] = s.handleVectorGet
	s.handlers["vector.Delete"] = s.handleVectorDelete
	s.handlers["vector.BatchUpsert"] = s.handleVectorBatchUpsert
	s.handlers["vector.BatchDelete"] = s.handleVectorBatchDelete
	s.handlers["vector.List"] = s.handleVectorList
	s.handlers["vector.UpdateMetadata"] = s.handleVectorUpdateMetadata
	s.handlers["vector.SearchSimilar"] = s.handleVectorSearchSimilar
	s.handlers["vector.SearchByMetadata"] = s.handleVectorSearchByMetadata
	s.handlers["vector.HybridSearch"] = s.handleVectorHybridSearch
	s.handlers["vector.SearchByID"] = s.handleVectorSearchByID
	s.handlers["vector.RebuildIndex"] = s.handleVectorRebuildIndex
	s.handlers["vector.GetRebuildProgress"] = s.handleVectorGetRebuildProgress
	s.handlers["vector.GetIndexStats"] = s.handleVectorGetIndexStats
	s.handlers["vector.CompactNamespace"] = s.handleVectorCompactNamespace
	s.handlers["vector.GetCompactionProgress"] = s.handleVectorGetCompactionProgress

	// User management
	s.handlers["user.Create"] = s.handleCreateUser
	s.handlers["user.Get"] = s.handleGetUser
	s.handlers["user.Update"] = s.handleUpdateUser
	s.handlers["user.Delete"] = s.handleDeleteUser
	s.handlers["user.List"] = s.handleListUsers
	s.handlers["user.Enable"] = s.handleEnableUser
	s.handlers["user.Disable"] = s.handleDisableUser

	// Role management
	s.handlers["role.Create"] = s.handleCreateRole
	s.handlers["role.Get"] = s.handleGetRole
	s.handlers["role.Update"] = s.handleUpdateRole
	s.handlers["role.Delete"] = s.handleDeleteRole
	s.handlers["role.List"] = s.handleListRoles
	s.handlers["role.Assign"] = s.handleAssignRole
	s.handlers["role.Unassign"] = s.handleUnassignRole
	s.handlers["role.ListPermissions"] = s.handleListPermissions

	// Certificate management
	s.handlers["cert.Generate"] = s.handleGenerateCert
	s.handlers["cert.Export"] = s.handleExportCert
	s.handlers["cert.Revoke"] = s.handleRevokeCert
	s.handlers["cert.List"] = s.handleListCerts

	// Collection
	s.handlers["collection.Create"] = s.handleCollectionCreate
	s.handlers["collection.GetInfo"] = s.handleCollectionGetInfo
	s.handlers["collection.List"] = s.handleCollectionList
	s.handlers["collection.Drop"] = s.handleCollectionDrop
	s.handlers["collection.InsertDocument"] = s.handleCollectionInsertDocument
	s.handlers["collection.GetDocument"] = s.handleCollectionGetDocument
	s.handlers["collection.UpdateDocument"] = s.handleCollectionUpdateDocument
	s.handlers["collection.DeleteDocument"] = s.handleCollectionDeleteDocument
	s.handlers["collection.ListDocuments"] = s.handleCollectionListDocuments

	// Database management
	s.handlers["db.Create"] = s.handleDatabaseCreate
	s.handlers["db.Get"] = s.handleDatabaseGet
	s.handlers["db.List"] = s.handleDatabaseList
	s.handlers["db.Drop"] = s.handleDatabaseDrop
	s.handlers["db.UpdateProtocol"] = s.handleDatabaseUpdateProtocol
	s.handlers["db.UpdateEtcd"] = s.handleDatabaseUpdateEtcd
	s.handlers["db.UpdateRaft"] = s.handleDatabaseUpdateRaft
	s.handlers["db.UpdateBarrier"] = s.handleDatabaseUpdateBarrier
	s.handlers["db.UpdateConnection"] = s.handleDatabaseUpdateConnection

	// Barrier
	s.handlers["barrier.Initialize"] = s.handleBarrierInitialize
	s.handlers["barrier.Unseal"] = s.handleBarrierUnseal
	s.handlers["barrier.Seal"] = s.handleBarrierSeal
	s.handlers["barrier.Status"] = s.handleBarrierStatus
	s.handlers["barrier.ShamirInitialize"] = s.handleBarrierShamirInitialize
	s.handlers["barrier.ShamirUnsealShare"] = s.handleBarrierShamirUnsealShare
	s.handlers["barrier.ShamirUnsealShares"] = s.handleBarrierShamirUnsealShares
	s.handlers["barrier.ShamirListShares"] = s.handleBarrierShamirListShares
	s.handlers["barrier.ShamirDeleteShare"] = s.handleBarrierShamirDeleteShare
	s.handlers["barrier.ShamirDeleteAllShares"] = s.handleBarrierShamirDeleteAllShares
	s.handlers["barrier.ShamirVerify"] = s.handleBarrierShamirVerify
	s.handlers["barrier.Rekey"] = s.handleBarrierRekey
	s.handlers["barrier.RecoveryGenerate"] = s.handleBarrierRecoveryGenerate
	s.handlers["barrier.RecoveryRecover"] = s.handleBarrierRecoveryRecover
	s.handlers["barrier.RecoveryDelete"] = s.handleBarrierRecoveryDelete
	s.handlers["barrier.RecoveryStatus"] = s.handleBarrierRecoveryStatus
	s.handlers["barrier.RootToken"] = s.handleBarrierRootToken

	// Ceremony
	s.handlers["ceremony.Initialize"] = s.handleCeremonyInitialize
	s.handlers["ceremony.Status"] = s.handleCeremonyStatus
	s.handlers["ceremony.ClaimCertBegin"] = s.handleCeremonyClaimCertBegin
	s.handlers["ceremony.ClaimCertComplete"] = s.handleCeremonyClaimCertComplete
	s.handlers["ceremony.ClaimShare"] = s.handleCeremonyClaimShare
	s.handlers["ceremony.SignCSR"] = s.handleCeremonySignCSR
}
