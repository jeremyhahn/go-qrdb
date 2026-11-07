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

package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// malformedCall is one SDK method driven against a tool that answers with
// text that is not JSON.
type malformedCall struct {
	tool string
	call func(ctx context.Context, tr *Transport) error
}

// malformedCalls lists every method whose response is decoded from the tool
// text, keyed by method name.
func malformedCalls() map[string]malformedCall {
	return map[string]malformedCall{
		// Audit.
		"QueryAudit": {"qrdb_audit_query", func(ctx context.Context, tr *Transport) error {
			_, err := tr.QueryAudit(ctx, transport.AuditFilter{})
			return err
		}},
		"VerifyChain": {"qrdb_audit_verify", func(ctx context.Context, tr *Transport) error {
			_, err := tr.VerifyChain(ctx)
			return err
		}},
		"VerifyReplicatedChain": {"qrdb_audit_verify_replicated", func(ctx context.Context, tr *Transport) error {
			_, err := tr.VerifyReplicatedChain(ctx, "acme")
			return err
		}},

		// Cluster and key-value reads.
		"Capabilities": {"qrdb_capabilities", func(ctx context.Context, tr *Transport) error {
			_, err := tr.Capabilities(ctx)
			return err
		}},
		"Status": {"qrdb_status", func(ctx context.Context, tr *Transport) error {
			_, err := tr.Status(ctx)
			return err
		}},
		"Health": {"qrdb_health", func(ctx context.Context, tr *Transport) error {
			_, err := tr.Health(ctx)
			return err
		}},
		"GetTopology": {"qrdb_topology", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetTopology(ctx)
			return err
		}},
		"Peers": {"qrdb_peers", func(ctx context.Context, tr *Transport) error {
			_, err := tr.Peers(ctx)
			return err
		}},
		"GetShardState": {"qrdb_shard_state", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetShardState(ctx, 1)
			return err
		}},
		"List": {"qrdb_list", func(ctx context.Context, tr *Transport) error {
			_, err := tr.List(ctx, "p")
			return err
		}},
		"Scan": {"qrdb_scan", func(ctx context.Context, tr *Transport) error {
			_, err := tr.Scan(ctx, "p")
			return err
		}},
		"ExecuteTransaction": {"qrdb_tx_execute", func(ctx context.Context, tr *Transport) error {
			_, err := tr.ExecuteTransaction(ctx, &transport.TransactionRequest{Compares: []transport.Compare{}})
			return err
		}},
		"CredentialStrategy": {"qrdb_credential_strategy", func(ctx context.Context, tr *Transport) error {
			_, err := tr.CredentialStrategy(ctx)
			return err
		}},

		// Users, roles, certificates.
		"CreateUser": {"qrdb_user_create", func(ctx context.Context, tr *Transport) error {
			_, err := tr.CreateUser(ctx, &transport.CreateUserRequest{Username: "u"})
			return err
		}},
		"GetUser": {"qrdb_user_get", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetUser(ctx, 1)
			return err
		}},
		"GetUserByUsername": {"qrdb_user_get_by_username", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetUserByUsername(ctx, "acme", "u")
			return err
		}},
		"GetUserByEmail": {"qrdb_user_get_by_email", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetUserByEmail(ctx, "acme", "u@db.example.com")
			return err
		}},
		"GetUserByCertThumbprint": {"qrdb_user_get_by_cert_thumbprint", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetUserByCertThumbprint(ctx, "ab")
			return err
		}},
		"AddCertBinding": {"qrdb_user_add_cert_binding", func(ctx context.Context, tr *Transport) error {
			_, err := tr.AddCertBinding(ctx, 1, "ab", 0)
			return err
		}},
		"RemoveCertBinding": {"qrdb_user_remove_cert_binding", func(ctx context.Context, tr *Transport) error {
			_, err := tr.RemoveCertBinding(ctx, 1, "ab")
			return err
		}},
		"UpdateUser": {"qrdb_user_update", func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateUser(ctx, &transport.UpdateUserRequest{ID: 1})
			return err
		}},
		"ListUsers": {"qrdb_user_list", func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListUsers(ctx, &transport.ListUsersRequest{})
			return err
		}},
		"CreateRole": {"qrdb_role_create", func(ctx context.Context, tr *Transport) error {
			_, err := tr.CreateRole(ctx, &transport.CreateRoleRequest{Name: "r"})
			return err
		}},
		"GetRole": {"qrdb_role_get", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetRole(ctx, "r")
			return err
		}},
		"UpdateRole": {"qrdb_role_update", func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateRole(ctx, &transport.UpdateRoleRequest{ID: "r"})
			return err
		}},
		"ListRoles": {"qrdb_role_list", func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListRoles(ctx, &transport.ListRolesRequest{})
			return err
		}},
		"ListPermissions": {"qrdb_permissions_list", func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListPermissions(ctx)
			return err
		}},
		"IssueCert": {"qrdb_cert_issue", func(ctx context.Context, tr *Transport) error {
			_, err := tr.IssueCert(ctx, &transport.IssueCertRequest{Username: "u"})
			return err
		}},
		"EnrollCert": {"qrdb_cert_enroll", func(ctx context.Context, tr *Transport) error {
			_, err := tr.EnrollCert(ctx, &transport.EnrollCertRequest{Username: "u"})
			return err
		}},
		"IssueDelegation": {"qrdb_cert_delegate", func(ctx context.Context, tr *Transport) error {
			_, err := tr.IssueDelegation(ctx, &transport.IssueDelegationRequest{})
			return err
		}},
		"RevokeCert": {"qrdb_cert_revoke", func(ctx context.Context, tr *Transport) error {
			_, err := tr.RevokeCert(ctx, "01")
			return err
		}},
		"ListCerts": {"qrdb_cert_list", func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListCerts(ctx, "acme")
			return err
		}},

		// Barrier.
		"BarrierStatus": {"qrdb_barrier_status", func(ctx context.Context, tr *Transport) error {
			_, err := tr.BarrierStatus(ctx)
			return err
		}},
		"BarrierClusterUnseal": {"qrdb_barrier_cluster_unseal", func(ctx context.Context, tr *Transport) error {
			_, err := tr.BarrierClusterUnseal(ctx)
			return err
		}},
		"BarrierHasRecoveryKeys": {"qrdb_barrier_recovery_status", func(ctx context.Context, tr *Transport) error {
			_, err := tr.BarrierHasRecoveryKeys(ctx)
			return err
		}},
		"OfficerList": {"qrdb_barrier_officer_list", func(ctx context.Context, tr *Transport) error {
			_, err := tr.OfficerList(ctx)
			return err
		}},

		// Collections.
		"CreateCollection": {"qrdb_collection_create", func(ctx context.Context, tr *Transport) error {
			_, err := tr.CreateCollection(ctx, transport.CreateCollectionRequest{Database: "app", Name: "c"})
			return err
		}},
		"GetCollectionInfo": {"qrdb_collection_info", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetCollectionInfo(ctx, "app", "c")
			return err
		}},
		"ListCollections": {"qrdb_collection_list", func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListCollections(ctx, "app")
			return err
		}},
		"InsertDocument": {"qrdb_collection_insert", func(ctx context.Context, tr *Transport) error {
			_, err := tr.InsertDocument(ctx, transport.InsertDocumentRequest{Database: "app", Collection: "c", Document: map[string]any{"k": "v"}})
			return err
		}},
		"GetDocument": {"qrdb_collection_get", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetDocument(ctx, "app", "c", 1)
			return err
		}},
		"ListDocuments": {"qrdb_collection_list_documents", func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListDocuments(ctx, transport.ListDocumentsRequest{Database: "app", Collection: "c"})
			return err
		}},

		// Databases.
		"CreateDatabase": {"qrdb_database_create", func(ctx context.Context, tr *Transport) error {
			_, err := tr.CreateDatabase(ctx, transport.CreateDatabaseRequest{Name: "app"})
			return err
		}},
		"GetDatabase": {"qrdb_database_get", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetDatabase(ctx, "app")
			return err
		}},
		"ListDatabases": {"qrdb_database_list", func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListDatabases(ctx)
			return err
		}},
		"UpdateDatabaseProtocol": {"qrdb_database_update_protocol", func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseProtocol(ctx, transport.UpdateDatabaseProtocolRequest{Name: "app"})
			return err
		}},
		"UpdateDatabaseEtcd": {"qrdb_database_update_etcd", func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseEtcd(ctx, transport.UpdateDatabaseEtcdRequest{Name: "app"})
			return err
		}},
		"UpdateDatabaseRaft": {"qrdb_database_update_raft", func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseRaft(ctx, transport.UpdateDatabaseRaftRequest{Name: "app"})
			return err
		}},
		"ScaleDatabase": {"qrdb_database_scale", func(ctx context.Context, tr *Transport) error {
			_, err := tr.ScaleDatabase(ctx, transport.ScaleDatabaseRequest{Name: "app"})
			return err
		}},
		"UpdateDatabaseBarrier": {"qrdb_database_update_barrier", func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseBarrier(ctx, transport.UpdateDatabaseBarrierRequest{Name: "app", Barrier: &transport.DatabaseBarrierConfig{Enabled: true}})
			return err
		}},
		"UpdateDatabaseSQL": {"qrdb_database_update_sql", func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseSQL(ctx, transport.UpdateDatabaseSQLRequest{Name: "app"})
			return err
		}},
		"UpdateDatabaseCompaction": {"qrdb_database_update_compaction", func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseCompaction(ctx, transport.UpdateDatabaseCompactionRequest{Name: "app"})
			return err
		}},
		"UpdateDatabaseConnection": {"qrdb_database_update_connection", func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseConnection(ctx, transport.UpdateDatabaseConnectionRequest{Name: "app"})
			return err
		}},

		// MVCC.
		"PutWithLease": {"qrdb_mvcc_put_with_lease", func(ctx context.Context, tr *Transport) error {
			_, err := tr.PutWithLease(ctx, "k", []byte("v"), 1)
			return err
		}},
		"GetRevision": {"qrdb_mvcc_get_revision", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetRevision(ctx, "k")
			return err
		}},
		"GetAtRevision": {"qrdb_mvcc_get_at_revision", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetAtRevision(ctx, "k", 1)
			return err
		}},
		"Range": {"qrdb_mvcc_range", func(ctx context.Context, tr *Transport) error {
			_, err := tr.Range(ctx, []byte("a"), []byte("z"), transport.RangeOptions{})
			return err
		}},
		"CurrentRevision": {"qrdb_mvcc_current_revision", func(ctx context.Context, tr *Transport) error {
			_, err := tr.CurrentRevision(ctx)
			return err
		}},
		"RestoreToRevision": {"qrdb_mvcc_restore_to_revision", func(ctx context.Context, tr *Transport) error {
			_, err := tr.RestoreToRevision(ctx, &transport.RestoreRevisionRequest{Revision: 1})
			return err
		}},

		// Tenants.
		"CreateTenant": {"qrdb_tenant_create", func(ctx context.Context, tr *Transport) error {
			_, err := tr.CreateTenant(ctx, &transport.CreateTenantRequest{ID: "acme", Name: "Acme"})
			return err
		}},
		"ListTrustAnchors": {"qrdb_trust_anchor_list", func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListTrustAnchors(ctx, "acme")
			return err
		}},
		"GetTenant": {"qrdb_tenant_get", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetTenant(ctx, "acme")
			return err
		}},
		"ListDeletedTenants": {"qrdb_tenant_list_deleted", func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListDeletedTenants(ctx, nil)
			return err
		}},
		"ReconcileDeletedTenants": {"qrdb_tenant_reconcile_deleted", func(ctx context.Context, tr *Transport) error {
			_, err := tr.ReconcileDeletedTenants(ctx)
			return err
		}},
		"ListTenants": {"qrdb_tenant_list", func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListTenants(ctx)
			return err
		}},
		"UpdateTenant": {"qrdb_tenant_update", func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{TenantID: "acme"})
			return err
		}},
		"UpdateTenantQuotas": {"qrdb_tenant_update_quotas", func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateTenantQuotas(ctx, &transport.UpdateTenantQuotasRequest{TenantID: "acme"})
			return err
		}},
		"UpdateTenantEntitlements": {"qrdb_tenant_update_entitlements", func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateTenantEntitlements(ctx, &transport.UpdateTenantEntitlementsRequest{TenantID: "acme"})
			return err
		}},
		"TenantBarrierStatus": {"qrdb_tenant_barrier_status", func(ctx context.Context, tr *Transport) error {
			_, err := tr.TenantBarrierStatus(ctx, &transport.TenantBarrierStatusRequest{TenantID: "acme"})
			return err
		}},
		"TenantBarrierOfficerList": {"qrdb_tenant_barrier_officer_list", func(ctx context.Context, tr *Transport) error {
			_, err := tr.TenantBarrierOfficerList(ctx, "acme")
			return err
		}},

		// Time series.
		"TSQuery": {"qrdb_ts_query", func(ctx context.Context, tr *Transport) error {
			_, err := tr.TSQuery(ctx, &transport.TSQueryRequest{Metric: "m"})
			return err
		}},
		"TSAggregate": {"qrdb_ts_aggregate", func(ctx context.Context, tr *Transport) error {
			_, err := tr.TSAggregate(ctx, &transport.TSAggregateRequest{Metric: "m", Function: transport.AggAvg})
			return err
		}},
		"TSRichQuery": {"qrdb_ts_rich_query", func(ctx context.Context, tr *Transport) error {
			_, err := tr.TSRichQuery(ctx, &transport.TSRichQueryRequest{Metric: "m"})
			return err
		}},
		"TSGetRetention": {"qrdb_ts_get_retention_policy", func(ctx context.Context, tr *Transport) error {
			_, err := tr.TSGetRetention(ctx, &transport.TSGetRetentionRequest{Metric: "m"})
			return err
		}},
		"TSListMetrics": {"qrdb_ts_list_metrics", func(ctx context.Context, tr *Transport) error {
			_, err := tr.TSListMetrics(ctx, &transport.TSListMetricsRequest{})
			return err
		}},

		// Vectors.
		"CreateNamespace": {"qrdb_vector_create_namespace", func(ctx context.Context, tr *Transport) error {
			_, err := tr.CreateNamespace(ctx, &transport.CreateNamespaceRequest{Namespace: "ns", Dimension: 3})
			return err
		}},
		"DeleteNamespace": {"qrdb_vector_delete_namespace", func(ctx context.Context, tr *Transport) error {
			_, err := tr.DeleteNamespace(ctx, &transport.DeleteNamespaceRequest{Namespace: "ns"})
			return err
		}},
		"ListNamespaces": {"qrdb_vector_list_namespaces", func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListNamespaces(ctx, &transport.ListNamespacesRequest{})
			return err
		}},
		"GetNamespaceInfo": {"qrdb_vector_get_namespace_info", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{Namespace: "ns"})
			return err
		}},
		"UpsertVector": {"qrdb_vector_upsert", func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpsertVector(ctx, &transport.UpsertVectorRequest{Namespace: "ns", ID: 1, Vector: []float32{1, 2, 3}})
			return err
		}},
		"GetVector": {"qrdb_vector_get", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetVector(ctx, &transport.GetVectorRequest{Namespace: "ns", ID: 1})
			return err
		}},
		"DeleteVector": {"qrdb_vector_delete", func(ctx context.Context, tr *Transport) error {
			_, err := tr.DeleteVector(ctx, &transport.DeleteVectorRequest{Namespace: "ns", ID: 1})
			return err
		}},
		"BatchUpsert": {"qrdb_vector_batch_upsert", func(ctx context.Context, tr *Transport) error {
			_, err := tr.BatchUpsert(ctx, &transport.BatchUpsertRequest{Namespace: "ns"})
			return err
		}},
		"BatchDeleteVectors": {"qrdb_vector_batch_delete", func(ctx context.Context, tr *Transport) error {
			_, err := tr.BatchDeleteVectors(ctx, &transport.BatchDeleteVectorsRequest{Namespace: "ns", IDs: []uint64{1}})
			return err
		}},
		"ListVectors": {"qrdb_vector_list", func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListVectors(ctx, &transport.ListVectorsRequest{Namespace: "ns"})
			return err
		}},
		"UpdateMetadata": {"qrdb_vector_update_metadata", func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateMetadata(ctx, &transport.UpdateVectorMetadataRequest{Namespace: "ns", ID: 1})
			return err
		}},
		"SearchSimilar": {"qrdb_vector_search_similar", func(ctx context.Context, tr *Transport) error {
			_, err := tr.SearchSimilar(ctx, &transport.SearchSimilarRequest{Namespace: "ns", QueryVector: []float32{1, 2, 3}, K: 1})
			return err
		}},
		"SearchByMetadata": {"qrdb_vector_search_by_metadata", func(ctx context.Context, tr *Transport) error {
			_, err := tr.SearchByMetadata(ctx, &transport.SearchByMetadataRequest{Namespace: "ns"})
			return err
		}},
		"HybridSearch": {"qrdb_vector_hybrid_search", func(ctx context.Context, tr *Transport) error {
			_, err := tr.HybridSearch(ctx, &transport.HybridSearchRequest{Namespace: "ns", QueryVector: []float32{1, 2, 3}, K: 1})
			return err
		}},
		"SearchByID": {"qrdb_vector_search_by_id", func(ctx context.Context, tr *Transport) error {
			_, err := tr.SearchByID(ctx, &transport.SearchByIDRequest{Namespace: "ns", ID: 1, K: 1})
			return err
		}},
		"RebuildIndex": {"qrdb_vector_rebuild_index", func(ctx context.Context, tr *Transport) error {
			_, err := tr.RebuildIndex(ctx, &transport.RebuildIndexRequest{Namespace: "ns"})
			return err
		}},
		"GetRebuildProgress": {"qrdb_vector_get_rebuild_progress", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetRebuildProgress(ctx, &transport.GetRebuildProgressRequest{Namespace: "ns"})
			return err
		}},
		"GetIndexStats": {"qrdb_vector_get_index_stats", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetIndexStats(ctx, &transport.GetIndexStatsRequest{Namespace: "ns"})
			return err
		}},
		"CompactNamespace": {"qrdb_vector_compact_namespace", func(ctx context.Context, tr *Transport) error {
			_, err := tr.CompactNamespace(ctx, &transport.CompactNamespaceRequest{Namespace: "ns"})
			return err
		}},
		"GetCompactionProgress": {"qrdb_vector_get_compaction_progress", func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetCompactionProgress(ctx, &transport.GetCompactionProgressRequest{Namespace: "ns"})
			return err
		}},
	}
}

// TestMCP_MalformedToolResponse_IsADecodeError pins that a tool answering
// with text that is not JSON surfaces, from every method that decodes one, as
// a *jsonDecodeError naming the method, never as a zero-valued success.
func TestMCP_MalformedToolResponse_IsADecodeError(t *testing.T) {
	calls := malformedCalls()
	stubs := make(map[string]string, len(calls))
	for _, c := range calls {
		stubs[c.tool] = "not json"
	}
	tr, cleanup := connectStubInProcess(t, stubs)
	defer cleanup()
	ctx := context.Background()

	for name, c := range calls {
		err := c.call(ctx, tr)
		var de *jsonDecodeError
		if !errors.As(err, &de) {
			t.Errorf("%s over a malformed %s response = %v, want a jsonDecodeError", name, c.tool, err)
			continue
		}
		var re *transport.RequestError
		if errors.As(err, &re) && re.Method != name {
			t.Errorf("%s decode error names method %q", name, re.Method)
		}
	}
}

// TestMCP_ClosedTransport_RefusesEveryDecodingMethod pins that each of the
// methods above refuses on a closed transport before reaching the server.
func TestMCP_ClosedTransport_RefusesEveryDecodingMethod(t *testing.T) {
	tr, cleanup := connectStubInProcess(t, map[string]string{})
	cleanup()
	for name, c := range malformedCalls() {
		var ce *transport.ClosedError
		if err := c.call(context.Background(), tr); !errors.As(err, &ce) {
			t.Errorf("%s on a closed transport = %v, want a ClosedError", name, err)
		}
	}
}
