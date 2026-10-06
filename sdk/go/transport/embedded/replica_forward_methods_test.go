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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// allRefusingRegistry resolves every service family of every (tenant,
// database) the way a node that holds no replica of it does: with the
// placement's refusal. It is the generic twin of nonReplicaRegistry
// (replica_forward_test.go), which only overrides KVService -- this covers
// every family replica_forward_methods.go generates a forwarding method
// for, so each one's "forwardTo sees the refusal and forwards" branch is
// reachable without a family-specific registry.
type allRefusingRegistry struct{}

func (allRefusingRegistry) refusal(tenantID, db string) *placement.NotAReplicaError {
	return &placement.NotAReplicaError{TenantID: tenantID, Database: db, ShardID: 9, NodeID: "node-c",
		LeaderReplicaID: 1, Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "node-a"}}}
}

func (r allRefusingRegistry) KVService(t, db string) (service.KVServicer, error) {
	return nil, r.refusal(t, db)
}
func (r allRefusingRegistry) MVCCService(t, db string) (service.MVCCKVServicer, error) {
	return nil, r.refusal(t, db)
}
func (r allRefusingRegistry) WatchService(t, db string) (service.WatchServicer, error) {
	return nil, r.refusal(t, db)
}
func (r allRefusingRegistry) LeaseService(t, db string) (service.LeaseServicer, error) {
	return nil, r.refusal(t, db)
}
func (r allRefusingRegistry) IndexService(t, db string) (service.IndexServicer, error) {
	return nil, r.refusal(t, db)
}
func (r allRefusingRegistry) TimeSeriesService(t, db string) (service.TimeSeriesServicer, error) {
	return nil, r.refusal(t, db)
}
func (r allRefusingRegistry) VectorService(t, db string) (service.VectorServicer, error) {
	return nil, r.refusal(t, db)
}
func (r allRefusingRegistry) TransactionService(t, db string) (service.TransactionServicer, error) {
	return nil, r.refusal(t, db)
}
func (r allRefusingRegistry) BatchService(t, db string) (service.BatchServicer, error) {
	return nil, r.refusal(t, db)
}
func (r allRefusingRegistry) TableService(t, db string) (service.TableServicer, error) {
	return nil, r.refusal(t, db)
}
func (r allRefusingRegistry) SequenceService(t, db string) (service.SequenceServicer, error) {
	return nil, r.refusal(t, db)
}
func (r allRefusingRegistry) PrivilegeService(t, db string) (service.PrivilegeServicer, error) {
	return nil, r.refusal(t, db)
}
func (r allRefusingRegistry) CollectionService(t, db string) (service.CollectionServicer, error) {
	return nil, r.refusal(t, db)
}
func (r allRefusingRegistry) LockService(t, db string) (service.LockServicer, error) {
	return nil, r.refusal(t, db)
}

var _ service.Registry = allRefusingRegistry{}

// allFamiliesForwardingScope builds a forwardingScope whose base Transport
// serves every family replica_forward_methods.go generates a method for
// (KV, MVCC, transaction, batch, lease, index, time series, vector,
// collection, lock), scoped to "orders" through allRefusingRegistry so every
// one of those families' calls refuses as a non-replica and forwards through
// fwd. The mock services are never actually invoked: registry resolution
// fails before any of them would be reached, exactly as it does on the real
// per-family scoped adapters (database_scope_services.go resolves first).
func allFamiliesForwardingScope(t *testing.T, fwd ReplicaForwarder) *forwardingScope {
	t.Helper()
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithMVCCService(newMockMVCCService()),
		WithTransactionService(&mockTxService{}),
		WithBatchService(&fullBatchService{}),
		WithLeaseService(&mockLeaseService{}),
		WithIndexService(&mockIndexService{}),
		WithTimeSeriesService(&mockTSService{}),
		WithVectorService(&mockVectorService{}),
		WithCollectionService(&mockCollectionService{}),
		WithLockService(&mockLockService{}),
		WithServiceRegistry(allRefusingRegistry{}),
		WithReplicaForwarding(fwd, fixedCaller{id: alice}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	scoped, err := tr.WithDatabase("orders")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	fs, ok := scoped.(*forwardingScope)
	if !ok {
		t.Fatalf("scoped transport is %T, want *forwardingScope", scoped)
	}
	return fs
}

// TestForwardingScope_EveryDataPlaneMethodForwards is the behavioural proof
// that EVERY method replica_forward_methods.go generates -- not just the KV
// verbs replica_forward_test.go exercises -- really does forward a
// non-replica refusal to the replica, for every family the forwarding scope
// serves: MVCC, transactions, batches, imports, leases, locks, indexes, time
// series, vectors and collections. Each case calls one forwardingScope
// method with a near-empty request (content is irrelevant: the registry
// refuses before any of it is read) and asserts the forwarder was reached
// exactly once more than before.
func TestForwardingScope_EveryDataPlaneMethodForwards(t *testing.T) {
	fwd := &recordingForwarder{status: 200}
	fs := allFamiliesForwardingScope(t, fwd)
	ctx := acmeCtx()

	cases := []struct {
		name string
		call func() error
	}{
		{"Exists", func() error { _, err := fs.Exists(ctx, "k1"); return err }},
		{"List", func() error { _, err := fs.List(ctx, "k1"); return err }},
		{"Scan", func() error { _, err := fs.Scan(ctx, "k1"); return err }},
		{"CompareAndDelete", func() error { _, err := fs.CompareAndDelete(ctx, "k1", []byte("v")); return err }},
		{"CompareAndSwap", func() error { _, err := fs.CompareAndSwap(ctx, "k1", []byte("old"), []byte("new")); return err }},
		{"PutIfAbsent", func() error { _, err := fs.PutIfAbsent(ctx, "k1", []byte("v")); return err }},
		{"Compact", func() error { return fs.Compact(ctx, 1) }},
		{"CurrentRevision", func() error { _, err := fs.CurrentRevision(ctx); return err }},
		{"GetAtRevision", func() error { _, err := fs.GetAtRevision(ctx, "k1", 1); return err }},
		{"GetRevision", func() error { _, err := fs.GetRevision(ctx, "k1"); return err }},
		{"PutWithLease", func() error { _, err := fs.PutWithLease(ctx, "k1", []byte("v"), 1); return err }},
		{"Range", func() error { _, err := fs.Range(ctx, []byte("a"), []byte("z"), transport.RangeOptions{}); return err }},
		{"RestoreToRevision", func() error { _, err := fs.RestoreToRevision(ctx, &transport.RestoreRevisionRequest{}); return err }},
		{"ExecuteTransaction", func() error { _, err := fs.ExecuteTransaction(ctx, &transport.TransactionRequest{}); return err }},
		{"ExecuteBatch", func() error {
			_, err := fs.ExecuteBatch(ctx, []transport.Op{{Key: []byte("k"), Value: []byte("v")}})
			return err
		}},
		{"ImportAbort", func() error { return fs.ImportAbort(ctx, "sess") }},
		{"ImportAppend", func() error { return fs.ImportAppend(ctx, "sess", 0, []byte("d")) }},
		{"ImportBegin", func() error { _, err := fs.ImportBegin(ctx, transport.ImportOptions{}); return err }},
		{"ImportCommit", func() error { _, err := fs.ImportCommit(ctx, "sess"); return err }},
		{"LeaseGrant", func() error { _, err := fs.LeaseGrant(ctx, 60); return err }},
		{"LeaseKeepAlive", func() error { _, err := fs.LeaseKeepAlive(ctx, 1); return err }},
		{"LeaseList", func() error { _, err := fs.LeaseList(ctx); return err }},
		{"LeaseRevoke", func() error { return fs.LeaseRevoke(ctx, 1) }},
		{"LeaseTimeToLive", func() error { _, err := fs.LeaseTimeToLive(ctx, 1); return err }},
		{"LockAcquire", func() error { _, err := fs.LockAcquire(ctx, &transport.LockAcquireRequest{Key: "k"}); return err }},
		{"LockExtend", func() error { _, err := fs.LockExtend(ctx, &transport.LockExtendRequest{Key: "k"}); return err }},
		{"LockList", func() error { _, err := fs.LockList(ctx, "k"); return err }},
		{"LockRelease", func() error { return fs.LockRelease(ctx, &transport.LockReleaseRequest{Key: "k"}) }},
		{"LockStatus", func() error { _, err := fs.LockStatus(ctx, "k"); return err }},
		{"LockTryLock", func() error { _, err := fs.LockTryLock(ctx, &transport.LockAcquireRequest{Key: "k"}); return err }},
		{"IndexCreate", func() error { return fs.IndexCreate(ctx, "idx", "field", transport.IndexConfig{}) }},
		{"IndexDrop", func() error { return fs.IndexDrop(ctx, "idx") }},
		{"IndexList", func() error { _, err := fs.IndexList(ctx); return err }},
		{"IndexQuery", func() error { _, err := fs.IndexQuery(ctx, "idx", "val"); return err }},
		{"IndexScan", func() error { _, err := fs.IndexScan(ctx, "idx", nil, nil); return err }},
		{"TSAggregate", func() error { _, err := fs.TSAggregate(ctx, &transport.TSAggregateRequest{}); return err }},
		{"TSDeleteSeries", func() error { _, err := fs.TSDeleteSeries(ctx, &transport.TSDeleteRequest{}); return err }},
		{"TSGetRetention", func() error { _, err := fs.TSGetRetention(ctx, &transport.TSGetRetentionRequest{}); return err }},
		{"TSListMetrics", func() error { _, err := fs.TSListMetrics(ctx, &transport.TSListMetricsRequest{}); return err }},
		{"TSQuery", func() error { _, err := fs.TSQuery(ctx, &transport.TSQueryRequest{}); return err }},
		{"TSRichQuery", func() error { _, err := fs.TSRichQuery(ctx, &transport.TSRichQueryRequest{}); return err }},
		{"TSSetRetention", func() error { return fs.TSSetRetention(ctx, &transport.TSSetRetentionRequest{}) }},
		{"TSWrite", func() error { return fs.TSWrite(ctx, &transport.TSDataPoint{}) }},
		{"TSWriteBatch", func() error { return fs.TSWriteBatch(ctx, []transport.TSDataPoint{{}}) }},
		{"TSWritePoint", func() error { return fs.TSWritePoint(ctx, &transport.TSDataPoint{}) }},
		{"BatchDeleteVectors", func() error { _, err := fs.BatchDeleteVectors(ctx, &transport.BatchDeleteVectorsRequest{}); return err }},
		{"BatchUpsert", func() error { _, err := fs.BatchUpsert(ctx, &transport.BatchUpsertRequest{}); return err }},
		{"CompactNamespace", func() error { _, err := fs.CompactNamespace(ctx, &transport.CompactNamespaceRequest{}); return err }},
		{"CreateNamespace", func() error { _, err := fs.CreateNamespace(ctx, &transport.CreateNamespaceRequest{}); return err }},
		{"DeleteNamespace", func() error { _, err := fs.DeleteNamespace(ctx, &transport.DeleteNamespaceRequest{}); return err }},
		{"DeleteVector", func() error { _, err := fs.DeleteVector(ctx, &transport.DeleteVectorRequest{}); return err }},
		{"GetCompactionProgress", func() error {
			_, err := fs.GetCompactionProgress(ctx, &transport.GetCompactionProgressRequest{})
			return err
		}},
		{"GetIndexStats", func() error { _, err := fs.GetIndexStats(ctx, &transport.GetIndexStatsRequest{}); return err }},
		{"GetNamespaceInfo", func() error { _, err := fs.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{}); return err }},
		{"GetRebuildProgress", func() error { _, err := fs.GetRebuildProgress(ctx, &transport.GetRebuildProgressRequest{}); return err }},
		{"GetVector", func() error { _, err := fs.GetVector(ctx, &transport.GetVectorRequest{}); return err }},
		{"HybridSearch", func() error { _, err := fs.HybridSearch(ctx, &transport.HybridSearchRequest{}); return err }},
		{"ListNamespaces", func() error { _, err := fs.ListNamespaces(ctx, &transport.ListNamespacesRequest{}); return err }},
		{"ListVectors", func() error { _, err := fs.ListVectors(ctx, &transport.ListVectorsRequest{}); return err }},
		{"RebuildIndex", func() error { _, err := fs.RebuildIndex(ctx, &transport.RebuildIndexRequest{}); return err }},
		{"SearchByID", func() error { _, err := fs.SearchByID(ctx, &transport.SearchByIDRequest{}); return err }},
		{"SearchByMetadata", func() error { _, err := fs.SearchByMetadata(ctx, &transport.SearchByMetadataRequest{}); return err }},
		{"SearchSimilar", func() error { _, err := fs.SearchSimilar(ctx, &transport.SearchSimilarRequest{}); return err }},
		{"UpdateMetadata", func() error { _, err := fs.UpdateMetadata(ctx, &transport.UpdateVectorMetadataRequest{}); return err }},
		{"UpsertVector", func() error { _, err := fs.UpsertVector(ctx, &transport.UpsertVectorRequest{}); return err }},
		{"CreateCollection", func() error { _, err := fs.CreateCollection(ctx, transport.CreateCollectionRequest{}); return err }},
		{"DeleteDocument", func() error { return fs.DeleteDocument(ctx, "col", "db", 1) }},
		{"DropCollection", func() error { return fs.DropCollection(ctx, "col", "db") }},
		{"GetCollectionInfo", func() error { _, err := fs.GetCollectionInfo(ctx, "col", "db"); return err }},
		{"GetDocument", func() error { _, err := fs.GetDocument(ctx, "col", "db", 1); return err }},
		{"InsertDocument", func() error { _, err := fs.InsertDocument(ctx, transport.InsertDocumentRequest{}); return err }},
		{"ListCollections", func() error { _, err := fs.ListCollections(ctx, "col"); return err }},
		{"ListDocuments", func() error { _, err := fs.ListDocuments(ctx, transport.ListDocumentsRequest{}); return err }},
		{"UpdateDocument", func() error { return fs.UpdateDocument(ctx, transport.UpdateDocumentRequest{}) }},
	}

	for _, tc := range cases {
		before := len(fwd.calls)
		_ = tc.call()
		if after := len(fwd.calls); after != before+1 {
			t.Errorf("%s: forwarder reached %d time(s), want exactly 1 more (before=%d, after=%d)",
				tc.name, after-before, before, after)
			continue
		}
		last := fwd.calls[len(fwd.calls)-1]
		if last.database != "orders" || last.caller != alice {
			t.Errorf("%s: forwarded on %q as %+v, want orders as alice", tc.name, last.database, last.caller)
		}
	}
}
