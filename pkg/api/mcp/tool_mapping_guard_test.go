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

//go:build codec_json && raft

package mcp

// tool_mapping_guard_test.go is the structural guard for the MCP
// authorization surface. Since the unified-authz cutover, every tool is
// registered through ONE registration helper (Server.addTools) that
// simultaneously declares the tool's permission in the server's
// MCPToolRegistry (the serverTool.Perm field, declared WITH the tool) — and
// addTools panics at construction if a tool carries no permission. This file
// guards that machinery:
//
//  1. A fully wired server (every optional service present, so every
//     conditional addTools group fires) builds a registry that matches,
//     exactly, the curated tool -> permission table the old mcpPermissions
//     map declared (minus its dead qrdb_ceremony_claim_share entry that
//     never had a tool, and with the raw-share tools the unified share-holder
//     quorum retired replaced by the quorum, rekey and revocation tools), so
//     the migration to registration-site permissions cannot silently change a
//     tool's required permission.
//  2. The set of tools registered on the underlying mcp-go server is
//     EXACTLY the set covered by the registry (the MCP analogue of REST's
//     walk-the-router assertion).
//  3. Every registered permission exists in the global permission catalog
//     (except the deliberately unregistered permission.IdentityGated
//     sentinel).
//  4. AuthorizeMCP is DEFAULT-DENY against the real registry: an unknown
//     tool — including the deleted dead table entry — is denied without
//     consulting the fine-grained authorizer, while a registered tool
//     authorizes with its declared permission.

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// expectedToolPerms is the authoritative tool -> permission table for the MCP
// transport. The share-holder quorum tools (qrdb_barrier_quorum_begin/submit
// and their tenant forms) are permission.IdentityGated: the holder's factor
// proof is the credential, checked by the service.
var expectedToolPerms = map[string]permission.Permission{
	"qrdb_put":    permission.KVPut,
	"qrdb_get":    permission.KVGet,
	"qrdb_delete": permission.KVDelete,
	"qrdb_list":   permission.KVList,
	"qrdb_scan":   permission.KVRange,
	// The guarded write verbs take the permission of the write they perform,
	// exactly as their REST routes do (go-qrdb#127): a conditional create and
	// a conditional edit are puts, a conditional delete is a delete. They are
	// NOT separately gated — reachability with ordinary kv:put / kv:delete is
	// the same contract every other transport carries, and is why the
	// conformance matrix seeds those two permissions and no more.
	"qrdb_put_if_absent":                  permission.KVPut,
	"qrdb_compare_and_delete":             permission.KVDelete,
	"qrdb_compare_and_swap":               permission.KVPut,
	"qrdb_mvcc_put_with_lease":            permission.MVCCPutWithLease,
	"qrdb_mvcc_get_revision":              permission.MVCCGetRevision,
	"qrdb_mvcc_get_at_revision":           permission.MVCCGetAtRevision,
	"qrdb_mvcc_range":                     permission.MVCCRange,
	"qrdb_mvcc_compact":                   permission.MVCCCompact,
	"qrdb_mvcc_current_revision":          permission.MVCCCurrentRevision,
	"qrdb_mvcc_restore_to_revision":       permission.MVCCRestore,
	"qrdb_tx_execute":                     permission.TransactionExecute,
	"qrdb_batch_execute":                  permission.BatchExecute,
	"qrdb_batch_import":                   permission.BatchExecute,
	"qrdb_watch":                          permission.WatchWatch,
	"qrdb_lease_grant":                    permission.LeaseGrant,
	"qrdb_lease_revoke":                   permission.LeaseRevoke,
	"qrdb_lease_keepalive":                permission.LeaseKeepAlive,
	"qrdb_lease_ttl":                      permission.LeaseTTL,
	"qrdb_lease_list":                     permission.LeaseList,
	"qrdb_index_create":                   permission.IndexCreate,
	"qrdb_index_drop":                     permission.IndexDrop,
	"qrdb_index_query":                    permission.IndexQuery,
	"qrdb_index_scan":                     permission.IndexScan,
	"qrdb_index_list":                     permission.IndexList,
	"qrdb_ts_write":                       permission.TSWrite,
	"qrdb_ts_write_batch":                 permission.TSWriteBatch,
	"qrdb_ts_write_point":                 permission.TSWritePoint,
	"qrdb_ts_query":                       permission.TSQuery,
	"qrdb_ts_rich_query":                  permission.TSRichQuery,
	"qrdb_ts_aggregate":                   permission.TSAggregate,
	"qrdb_ts_delete_series":               permission.TSDeleteSeries,
	"qrdb_ts_get_retention_policy":        permission.TSGetRetention,
	"qrdb_ts_set_retention_policy":        permission.TSSetRetention,
	"qrdb_ts_list_metrics":                permission.TSListMetrics,
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
	"qrdb_health":                         permission.AdminHealth,
	"qrdb_status":                         permission.AdminStatus,
	"qrdb_peers":                          permission.AdminPeers,
	"qrdb_add_member":                     permission.AdminAddMember,
	"qrdb_remove_member":                  permission.AdminRemoveMember,
	"qrdb_snapshot":                       permission.AdminSnapshot,
	"qrdb_drain":                          permission.AdminDrain,
	"qrdb_capabilities":                   permission.AdminCapabilities,
	"qrdb_start_shard":                    permission.AdminStartShard,
	"qrdb_stop_shard":                     permission.AdminStopShard,
	"qrdb_shard_state":                    permission.AdminShardState,
	"qrdb_topology":                       permission.AdminTopology,
	"qrdb_node_compaction":                permission.AdminNodeCompaction,
	"qrdb_update_node_compaction":         permission.AdminNodeCompaction,
	"qrdb_user_create":                    permission.UsersCreate,
	"qrdb_user_get":                       permission.UsersRead,
	"qrdb_user_get_by_username":           permission.UsersRead,
	"qrdb_user_get_by_email":              permission.UsersRead,
	"qrdb_user_update":                    permission.UsersUpdate,
	"qrdb_user_delete":                    permission.UsersDelete,
	"qrdb_user_list":                      permission.UsersList,
	"qrdb_user_enable":                    permission.UsersEnable,
	"qrdb_user_disable":                   permission.UsersDisable,
	"qrdb_user_get_by_cert_thumbprint":    permission.UsersGetByCertThumbprint,
	"qrdb_user_add_cert_binding":          permission.UsersAddCertBinding,
	"qrdb_user_remove_cert_binding":       permission.UsersRemoveCertBinding,
	"qrdb_role_create":                    permission.RolesCreate,
	"qrdb_role_get":                       permission.RolesRead,
	"qrdb_role_get_by_name":               permission.RolesRead,
	"qrdb_role_update":                    permission.RolesUpdate,
	"qrdb_role_delete":                    permission.RolesDelete,
	"qrdb_role_list":                      permission.RolesList,
	"qrdb_role_assign":                    permission.RolesAssign,
	"qrdb_role_unassign":                  permission.RolesUnassign,
	"qrdb_permissions_list":               permission.PermissionsList,
	"qrdb_permissions_resolve":            permission.PermissionsResolve,
	"qrdb_role_reconcile_builtin":         permission.RolesCreate,
	"qrdb_cert_generate":                  permission.CertsGenerate,
	"qrdb_cert_export":                    permission.CertsExport,
	"qrdb_cert_revoke":                    permission.CertsRevoke,
	"qrdb_cert_list":                      permission.CertsList,
	"qrdb_tenant_create":                  permission.TenantsCreate,
	"qrdb_tenant_get":                     permission.TenantsRead,
	"qrdb_tenant_update":                  permission.TenantsUpdate,
	"qrdb_tenant_update_quotas":           permission.TenantsSetQuotas,
	"qrdb_tenant_update_entitlements":     permission.TenantsSetEntitlements,
	"qrdb_tenant_list_deleted":            permission.TenantsListDeleted,
	"qrdb_tenant_reconcile_deleted":       permission.TenantsReconcileDeleted,
	"qrdb_tenant_delete":                  permission.TenantsDelete,
	"qrdb_tenant_list":                    permission.TenantsList,
	"qrdb_backup_create":                  permission.BackupCreate,
	"qrdb_backup_list":                    permission.BackupList,
	"qrdb_backup_get":                     permission.BackupRead,
	"qrdb_backup_delete":                  permission.BackupDelete,
	"qrdb_backup_restore":                 permission.BackupRestore,
	"qrdb_backup_verify":                  permission.BackupVerify,
	"qrdb_rebalance_shard_stats":          permission.RebalanceStats,
	"qrdb_rebalance_plan":                 permission.RebalancePlan,
	"qrdb_rebalance_execute":              permission.RebalanceExecute,
	"qrdb_rebalance_report":               permission.RebalanceReport,
	// Init/Unseal/UnsealShare/Status are IdentityGated at the tool layer —
	// the RBAC check runs inside the handlers with the shared
	// trust-on-first-init / sealed-window fallbacks (see
	// middleware.AuthorizeTenantBarrierInit / AuthorizeTenantBarrierSealedOp),
	// identical to REST, gRPC, and QUIC.
	"qrdb_tenant_barrier_init":                   permission.IdentityGated,
	"qrdb_tenant_barrier_unseal":                 permission.IdentityGated,
	"qrdb_tenant_barrier_seal":                   permission.TenantBarrierSeal,
	"qrdb_tenant_barrier_status":                 permission.IdentityGated,
	"qrdb_tenant_barrier_officer_register":       permission.TenantBarrierOfficerRegister,
	"qrdb_tenant_barrier_officer_list":           permission.TenantBarrierOfficerList,
	"qrdb_tenant_barrier_share_claim_begin":      permission.IdentityGated,
	"qrdb_tenant_barrier_share_claim_complete":   permission.IdentityGated,
	"qrdb_tenant_barrier_quorum_begin":           permission.IdentityGated,
	"qrdb_tenant_barrier_quorum_submit":          permission.IdentityGated,
	"qrdb_tenant_barrier_rekey_start":            permission.TenantBarrierRekey,
	"qrdb_tenant_barrier_officer_revoke":         permission.TenantBarrierOfficerRevoke,
	"qrdb_barrier_initialize":                    permission.SealBootstrap,
	"qrdb_barrier_unseal":                        permission.SealUnseal,
	"qrdb_barrier_seal":                          permission.SealSeal,
	"qrdb_barrier_status":                        permission.SealStatus,
	"qrdb_barrier_cluster_unseal":                permission.SealUnseal,
	"qrdb_barrier_shamir_initialize":             permission.SealInitializeShamir,
	"qrdb_barrier_enable_encryption":             permission.SealEnableEncryption,
	"qrdb_barrier_recovery_generate":             permission.SealGenerateRecovery,
	"qrdb_barrier_recovery_delete":               permission.SealGenerateRecovery,
	"qrdb_barrier_recovery_status":               permission.SealStatus,
	"qrdb_barrier_officer_register":              permission.SealOfficerRegister,
	"qrdb_barrier_officer_list":                  permission.SealOfficerList,
	"qrdb_barrier_share_claim_begin":             permission.IdentityGated,
	"qrdb_barrier_share_claim_complete":          permission.IdentityGated,
	"qrdb_barrier_recovery_officer_register":     permission.SealOfficerRegister,
	"qrdb_barrier_recovery_share_claim_begin":    permission.IdentityGated,
	"qrdb_barrier_recovery_share_claim_complete": permission.IdentityGated,
	"qrdb_barrier_quorum_begin":                  permission.IdentityGated,
	"qrdb_barrier_quorum_submit":                 permission.IdentityGated,
	"qrdb_barrier_rekey_start":                   permission.SealRekey,
	"qrdb_barrier_officer_revoke":                permission.SealOfficerRevoke,
	"qrdb_ceremony_initialize":                   permission.CeremonyInitialize,
	"qrdb_ceremony_status":                       permission.CeremonyStatus,
	"qrdb_ceremony_sign_csr":                     permission.CeremonySignCSR,
	"qrdb_credential_submit":                     permission.CredentialsSubmit,
	"qrdb_credential_strategy":                   permission.CredentialsStrategy,
	"qrdb_credential_list":                       permission.CredentialsList,
	"qrdb_credential_delete":                     permission.CredentialsDelete,
	"qrdb_credential_rotate":                     permission.CredentialsRotate,
	"qrdb_credential_gc_expired":                 permission.CredentialsGCExpired,
	"qrdb_credential_retrieve_version":           permission.CredentialsRetrieveVersion,
	"qrdb_credential_list_versions":              permission.CredentialsListVersions,
	"qrdb_credential_store":                      permission.CredentialsStore,
	"qrdb_credential_retrieve":                   permission.CredentialsRetrieve,
	"qrdb_dek_status":                            permission.DEKStatus,
	"qrdb_dek_rotate":                            permission.DEKRotate,
	"qrdb_dek_retire":                            permission.DEKRetire,
	"qrdb_dek_encrypt":                           permission.DEKEncrypt,
	"qrdb_dek_decrypt":                           permission.DEKDecrypt,
	"qrdb_dek_rewrap":                            permission.DEKRewrap,
	"qrdb_database_create":                       permission.DatabasesCreate,
	"qrdb_database_get":                          permission.DatabasesRead,
	"qrdb_database_list":                         permission.DatabasesList,
	"qrdb_database_drop":                         permission.DatabasesDelete,
	"qrdb_database_update_barrier":               permission.DatabasesUpdate,
	"qrdb_database_update_connection":            permission.DatabasesUpdate,
	"qrdb_database_update_sql":                   permission.DatabasesUpdate,
	"qrdb_database_update_compaction":            permission.MVCCCompact,
	"qrdb_database_update_etcd":                  permission.DatabasesUpdate,
	"qrdb_database_update_protocol":              permission.DatabasesUpdate,
	"qrdb_database_update_raft":                  permission.DatabasesUpdate,
	"qrdb_database_scale":                        permission.DatabasesScale,
	"qrdb_database_seal":                         permission.DatabasesSeal,
	"qrdb_database_unseal":                       permission.DatabasesUnseal,
	"qrdb_database_barrier_status":               permission.DatabasesBarrierStatus,
	"qrdb_database_barrier_officer_register":     permission.DatabasesOfficerRegister,
	"qrdb_database_barrier_officer_list":         permission.DatabasesOfficerList,
	"qrdb_database_barrier_shamir_initialize":    permission.DatabasesInitializeShamir,
	"qrdb_database_barrier_share_claim_begin":    permission.IdentityGated,
	"qrdb_database_barrier_share_claim_complete": permission.IdentityGated,
	"qrdb_database_barrier_quorum_begin":         permission.IdentityGated,
	"qrdb_database_barrier_quorum_submit":        permission.IdentityGated,
	"qrdb_database_barrier_rekey_start":          permission.DatabasesRekey,
	"qrdb_database_barrier_officer_revoke":       permission.DatabasesOfficerRevoke,
	"qrdb_sql_query":                             permission.KVRange,
	"qrdb_collection_create":                     permission.CollectionsCreate,
	"qrdb_collection_get":                        permission.CollectionsRead,
	"qrdb_collection_info":                       permission.CollectionsRead,
	"qrdb_collection_list":                       permission.CollectionsList,
	"qrdb_collection_delete":                     permission.CollectionsDelete,
	"qrdb_collection_drop":                       permission.CollectionsDrop,
	"qrdb_collection_update":                     permission.CollectionsUpdate,
	"qrdb_collection_insert":                     permission.CollectionsInsert,
	"qrdb_collection_list_documents":             permission.CollectionsList,
	"qrdb_lock_acquire":                          permission.LocksAcquire,
	"qrdb_lock_trylock":                          permission.LocksAcquire,
	"qrdb_lock_release":                          permission.LocksRelease,
	"qrdb_lock_extend":                           permission.LocksExtend,
	"qrdb_lock_status":                           permission.LocksStatus,
	"qrdb_lock_list":                             permission.LocksList,
	"qrdb_embed_text":                            permission.EmbeddingEmbed,
	"qrdb_batch_embed":                           permission.EmbeddingEmbed,
	"qrdb_list_embedding_models":                 permission.EmbeddingListModels,
	"qrdb_rag_ingest":                            permission.RAGIngest,
	"qrdb_rag_query":                             permission.RAGQuery,
	"qrdb_rag_list_documents":                    permission.RAGListDocuments,
	"qrdb_rag_delete_document":                   permission.RAGDeleteDocument,
	"qrdb_audit_query":                           permission.AuditQuery,
	"qrdb_audit_verify":                          permission.AuditVerify,
	"qrdb_authfactor_enroll":                     permission.AuthFactorEnroll,
	"qrdb_authfactor_challenge":                  permission.AuthFactorChallenge,
	"qrdb_authfactor_verify":                     permission.AuthFactorVerify,
	"qrdb_authfactor_list":                       permission.AuthFactorList,
	"qrdb_authfactor_revoke":                     permission.AuthFactorRevoke,
	"qrdb_user_authfactor_add":                   permission.UserAuthFactorsAdd,
	"qrdb_user_authfactor_list":                  permission.UserAuthFactorsList,
	"qrdb_user_authfactor_get":                   permission.UserAuthFactorsGet,
	"qrdb_user_authfactor_remove":                permission.UserAuthFactorsRemove,
	"qrdb_user_authfactor_rotate":                permission.UserAuthFactorsRotate,
	"qrdb_user_authfactor_relabel":               permission.UserAuthFactorsRelabel,
	"qrdb_user_authfactor_login":                 permission.UserAuthFactorsLogin,
}

// ---------------------------------------------------------------------------
// Guard-local stubs for the services whose shared mocks live behind the full
// tag set (backup, rebalance, audit). They exist solely so the fully wired
// guard server registers every tool group under the plain codec_json+raft
// tags.
// ---------------------------------------------------------------------------

type guardBackupSvc struct{}

func (guardBackupSvc) CreateBackup(context.Context, service.BackupOptions) (*service.BackupInfo, error) {
	return &service.BackupInfo{}, nil
}
func (guardBackupSvc) ListBackups(context.Context) ([]service.BackupInfo, error) { return nil, nil }
func (guardBackupSvc) GetBackup(context.Context, string) (*service.BackupInfo, error) {
	return &service.BackupInfo{}, nil
}
func (guardBackupSvc) DeleteBackup(context.Context, string) error { return nil }
func (guardBackupSvc) RestoreBackup(context.Context, service.RestoreOptions) (*service.RestoreResult, error) {
	return &service.RestoreResult{}, nil
}
func (guardBackupSvc) VerifyBackup(context.Context, string) (*service.VerifyResult, error) {
	return &service.VerifyResult{}, nil
}

type guardRebalanceSvc struct{}

func (guardRebalanceSvc) GetShardStats(context.Context, uint64) (*service.ShardStats, error) {
	return &service.ShardStats{}, nil
}
func (guardRebalanceSvc) CreateRebalancePlan(context.Context, *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
	return &service.RebalancePlanResult{}, nil
}
func (guardRebalanceSvc) ExecuteRebalance(context.Context, *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
	return &service.RebalanceExecutionResult{}, nil
}
func (guardRebalanceSvc) GetImbalanceReport(context.Context) (*service.ImbalanceReportResult, error) {
	return &service.ImbalanceReportResult{}, nil
}

type guardAuditLogger struct{}

func (guardAuditLogger) Emit(context.Context, audit.AuditEvent) error { return nil }
func (guardAuditLogger) Query(context.Context, audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}
func (guardAuditLogger) Close() error { return nil }

// guardMCPAllowAuthorizer permits everything and counts calls.
type guardMCPAllowAuthorizer struct {
	calls int
}

func (a *guardMCPAllowAuthorizer) Authorize(context.Context, *auth.Identity, string, string) error {
	a.calls++
	return nil
}

// newFullyWiredGuardServer builds a *Server with every optional service
// wired so every conditional addTools group in NewServer fires, reusing the
// package's existing test doubles where they are visible under the plain
// codec_json+raft tags.
func newFullyWiredGuardServer(t *testing.T, extra ...ServerOption) *Server {
	t.Helper()
	opts := []ServerOption{
		WithMVCC(newMockMVCCService()),
		WithTransaction(&mockTransactionService{}),
		WithBatch(&mockBatchService{}),
		WithWatch(&mockWatchService{}),
		WithLease(&mockLeaseService{}),
		WithLock(newCovMockLockSvc()),
		WithIndex(&mockIndexService{}),
		WithTimeSeries(&mockTimeSeriesService{}),
		WithVector(&mockVectorService{}),
		WithEmbedding(&mockEmbeddingService{}),
		WithRAG(&mockRAGSvc{}),
		WithUserService(&mockUserService{}),
		WithRoleService(&mockRoleService{}),
		WithCertProvider(&mockCertProvider{}),
		WithCollectionService(newMockCollectionService()),
		WithDatabaseService(newMockDatabaseService()),
		WithBarrierService(newCovMockBarrierSvc()),
		WithEncryptionMigrator(&captureMCPMigrator{}),
		WithSQLService(&mcpSQLServiceMock{}),
		WithCeremonyService(newCovMockCeremonySvc()),
		WithCredentialService(newCovMockCredentialSvc()),
		WithDEKService(newTestDEKService(t)),
		WithTenantService(newCovMockTenantSvc()),
		WithBackupService(guardBackupSvc{}),
		WithRebalanceService(guardRebalanceSvc{}),
		WithAuthFactorService(newCovMockAuthFactorSvc()),
		WithUserAuthFactorService(newCovMockUserAuthFactorSvc()),
		WithAuditLogger(guardAuditLogger{}),
	}
	opts = append(opts, extra...)
	return NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", opts...)
}

// TestToolMappingGuard_RegistryMatchesExpectations asserts exact equality
// between the registry a fully wired server builds and the curated table, in
// both directions.
func TestToolMappingGuard_RegistryMatchesExpectations(t *testing.T) {
	srv := newFullyWiredGuardServer(t)
	entries := srv.toolPerms.Entries()

	for tool, want := range expectedToolPerms {
		got, ok := entries[tool]
		if !ok {
			t.Errorf("tool %q missing from registry (was in the authoritative table)", tool)
			continue
		}
		if got != want {
			t.Errorf("tool %q: registry permission %q, want %q", tool, got, want)
		}
	}
	for tool := range entries {
		if _, ok := expectedToolPerms[tool]; !ok {
			t.Errorf("tool %q registered but not in the authoritative table; update the guard deliberately", tool)
		}
	}
	if len(entries) != len(expectedToolPerms) {
		t.Errorf("registry has %d tools, authoritative table has %d", len(entries), len(expectedToolPerms))
	}
}

// TestToolMappingGuard_ListedToolsExactlyCovered is the MCP analogue of
// REST's walk-the-router assertion: the set of tools registered on the
// underlying mcp-go server is exactly the set covered by the registry, so no
// built-in tool can be served without a permission and no permission can be
// declared for a tool that is not actually served. (ServiceModule tools
// registered directly on the mcp-go server would surface here as a
// registry gap — they are deliberately denied by AuthorizeMCP until they
// gain registry coverage.)
func TestToolMappingGuard_ListedToolsExactlyCovered(t *testing.T) {
	srv := newFullyWiredGuardServer(t)
	listed := srv.MCPServer().ListTools()
	entries := srv.toolPerms.Entries()

	for name := range listed {
		if _, ok := entries[name]; !ok {
			t.Errorf("tool %q served by the mcp-go server but not covered by the registry (denied at runtime)", name)
		}
	}
	for name := range entries {
		if _, ok := listed[name]; !ok {
			t.Errorf("tool %q covered by the registry but not served by the mcp-go server", name)
		}
	}
}

// TestToolMappingGuard_PermissionsRegistered verifies every permission
// referenced by the registry exists in the global permission catalog, except
// the deliberately unregistered identity-gated sentinel.
func TestToolMappingGuard_PermissionsRegistered(t *testing.T) {
	srv := newFullyWiredGuardServer(t)
	for tool, perm := range srv.toolPerms.Entries() {
		if perm == permission.IdentityGated {
			continue
		}
		if !permission.IsRegistered(perm) {
			t.Errorf("MCP tool %q declares unregistered permission %q", tool, perm)
		}
	}
}

// TestToolMappingGuard_DefaultDenyRegression drives AuthorizeMCP against the
// REAL registry: an unknown tool is denied without consulting the
// fine-grained authorizer; the old table's dead entry is likewise denied
// (it never had a tool); registered tools reach the authorizer with their
// declared permission and succeed.
func TestToolMappingGuard_DefaultDenyRegression(t *testing.T) {
	authz := &guardMCPAllowAuthorizer{}
	mw := authmw.New(authz)
	srv := newFullyWiredGuardServer(t, WithAuthMiddleware(mw))
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{
		Subject:  "guard-user",
		TenantID: "t1",
		Roles:    []string{"admin"},
	})

	if err := mw.AuthorizeMCP(ctx, srv.toolPerms, "qrdb_totally_unknown_tool", nil); err == nil {
		t.Error("unknown tool must be denied (default-deny)")
	}
	for _, dead := range []string{
		"qrdb_ceremony_claim_share",
		// Retired by the unified share-holder quorum.
		"qrdb_barrier_shamir_unseal_share", "qrdb_barrier_root_token", "qrdb_barrier_rekey",
		"qrdb_ceremony_begin_claim_share", "qrdb_ceremony_submit_unseal_share", "qrdb_tenant_barrier_unseal_share",
	} {
		if err := mw.AuthorizeMCP(ctx, srv.toolPerms, dead, nil); err == nil {
			t.Errorf("dead tool %s must be denied (it has no tool)", dead)
		}
	}
	if authz.calls != 0 {
		t.Errorf("authorizer consulted %d times for denied tools, want 0", authz.calls)
	}

	// A few known tools spanning permission families resolve and authorize.
	for _, tool := range []string{"qrdb_put", "qrdb_status", "qrdb_tenant_update_entitlements", "qrdb_audit_query"} {
		if err := mw.AuthorizeMCP(ctx, srv.toolPerms, tool, nil); err != nil {
			t.Errorf("registered tool %q: unexpected authorize error: %v", tool, err)
		}
	}
	if authz.calls != 4 {
		t.Errorf("authorizer consulted %d times for allowed tools, want 4", authz.calls)
	}
}
