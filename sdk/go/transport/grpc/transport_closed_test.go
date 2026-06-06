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

package grpc

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newClosedTransport creates a Transport that has already been closed.
func newClosedTransport() *Transport {
	tr := &Transport{config: &transport.Config{}}
	tr.closed.Store(true)
	return tr
}

func assertClosed(t *testing.T, err error) {
	t.Helper()
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ===========================================================================
// transport.go (Scan / Exists / Capabilities)
// ===========================================================================

func TestGRPCScan_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.Scan(context.Background(), "p")
	assertClosed(t, err)
}

func TestGRPCExists_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.Exists(context.Background(), "k")
	assertClosed(t, err)
}

func TestGRPCCapabilities_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.Capabilities(context.Background())
	assertClosed(t, err)
}

// ===========================================================================
// transport_barrier.go closed-paths
// ===========================================================================

func TestGRPCBarrier_AfterClose(t *testing.T) {
	tr := newClosedTransport()

	if err := tr.BarrierInitialize(context.Background(), &transport.BarrierInitializeRequest{}); !errors.Is(err, (*transport.ClosedError)(nil)) {
		var ce *transport.ClosedError
		if !errors.As(err, &ce) {
			t.Errorf("BarrierInitialize: %v", err)
		}
	}
	var ce *transport.ClosedError
	if err := tr.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{}); !errors.As(err, &ce) {
		t.Errorf("BarrierUnseal: %v", err)
	}
	if err := tr.BarrierSeal(context.Background()); !errors.As(err, &ce) {
		t.Errorf("BarrierSeal: %v", err)
	}
	if _, err := tr.BarrierStatus(context.Background()); !errors.As(err, &ce) {
		t.Errorf("BarrierStatus: %v", err)
	}
	if err := tr.BarrierDeleteRecoveryKeys(context.Background()); !errors.As(err, &ce) {
		t.Errorf("BarrierDeleteRecoveryKeys: %v", err)
	}
	if _, err := tr.BarrierHasRecoveryKeys(context.Background()); !errors.As(err, &ce) {
		t.Errorf("BarrierHasRecoveryKeys: %v", err)
	}
}

// ===========================================================================
// transport_batch.go
// ===========================================================================

func TestGRPCExecuteBatch_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.ExecuteBatch(context.Background(), nil)
	assertClosed(t, err)
}

func TestGRPCImportFile_NotImplemented(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	_, err := tr.ImportFile(context.Background(), "csv", bytes.NewReader(nil), transport.ImportOptions{})
	var nie *transport.NotImplementedError
	if !errors.As(err, &nie) {
		t.Fatalf("expected NotImplementedError, got %T: %v", err, err)
	}
}

// ===========================================================================
// transport_dek.go — all NotImplemented
// ===========================================================================

func TestGRPCDEK_NotImplemented(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	var nie *transport.NotImplementedError

	if err := tr.DEKInitialize(context.Background(), &transport.DEKInitializeRequest{}); !errors.As(err, &nie) {
		t.Errorf("DEKInitialize: %v", err)
	}
	if _, err := tr.DEKGetStatus(context.Background()); !errors.As(err, &nie) {
		t.Errorf("DEKGetStatus: %v", err)
	}
	if err := tr.DEKRotate(context.Background()); !errors.As(err, &nie) {
		t.Errorf("DEKRotate: %v", err)
	}
	if _, err := tr.DEKGetCurrent(context.Background()); !errors.As(err, &nie) {
		t.Errorf("DEKGetCurrent: %v", err)
	}
	if err := tr.DEKSyncToNode(context.Background(), &transport.DEKSyncRequest{}); !errors.As(err, &nie) {
		t.Errorf("DEKSyncToNode: %v", err)
	}
	if _, err := tr.TenantDEKGetOrCreate(context.Background(), &transport.TenantDEKRequest{}); !errors.As(err, &nie) {
		t.Errorf("TenantDEKGetOrCreate: %v", err)
	}
	if err := tr.TenantDEKRotate(context.Background(), &transport.TenantDEKRotateRequest{}); !errors.As(err, &nie) {
		t.Errorf("TenantDEKRotate: %v", err)
	}
	if err := tr.TenantDEKDelete(context.Background(), &transport.TenantDEKDeleteRequest{}); !errors.As(err, &nie) {
		t.Errorf("TenantDEKDelete: %v", err)
	}
	if _, err := tr.TenantDEKList(context.Background()); !errors.As(err, &nie) {
		t.Errorf("TenantDEKList: %v", err)
	}
	if _, err := tr.TenantDEKGetStatus(context.Background(), &transport.TenantDEKRequest{}); !errors.As(err, &nie) {
		t.Errorf("TenantDEKGetStatus: %v", err)
	}
}

func TestGRPCDEK_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if err := tr.DEKInitialize(context.Background(), &transport.DEKInitializeRequest{}); !errors.As(err, &ce) {
		t.Errorf("DEKInitialize: %v", err)
	}
	if _, err := tr.DEKGetStatus(context.Background()); !errors.As(err, &ce) {
		t.Errorf("DEKGetStatus: %v", err)
	}
	if err := tr.DEKRotate(context.Background()); !errors.As(err, &ce) {
		t.Errorf("DEKRotate: %v", err)
	}
	if _, err := tr.DEKGetCurrent(context.Background()); !errors.As(err, &ce) {
		t.Errorf("DEKGetCurrent: %v", err)
	}
	if err := tr.DEKSyncToNode(context.Background(), &transport.DEKSyncRequest{}); !errors.As(err, &ce) {
		t.Errorf("DEKSyncToNode: %v", err)
	}
	if _, err := tr.TenantDEKGetOrCreate(context.Background(), &transport.TenantDEKRequest{}); !errors.As(err, &ce) {
		t.Errorf("TenantDEKGetOrCreate: %v", err)
	}
	if err := tr.TenantDEKRotate(context.Background(), &transport.TenantDEKRotateRequest{}); !errors.As(err, &ce) {
		t.Errorf("TenantDEKRotate: %v", err)
	}
	if err := tr.TenantDEKDelete(context.Background(), &transport.TenantDEKDeleteRequest{}); !errors.As(err, &ce) {
		t.Errorf("TenantDEKDelete: %v", err)
	}
	if _, err := tr.TenantDEKList(context.Background()); !errors.As(err, &ce) {
		t.Errorf("TenantDEKList: %v", err)
	}
	if _, err := tr.TenantDEKGetStatus(context.Background(), &transport.TenantDEKRequest{}); !errors.As(err, &ce) {
		t.Errorf("TenantDEKGetStatus: %v", err)
	}
}

// ===========================================================================
// transport_authfactor.go
// ===========================================================================

func TestGRPCAuthFactor_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.EnrollFactor(context.Background(), &transport.EnrollFactorRequest{}); !errors.As(err, &ce) {
		t.Errorf("EnrollFactor: %v", err)
	}
	if _, err := tr.ChallengeFactor(context.Background(), &transport.ChallengeFactorRequest{}); !errors.As(err, &ce) {
		t.Errorf("ChallengeFactor: %v", err)
	}
	if err := tr.VerifyFactor(context.Background(), &transport.VerifyFactorRequest{}); !errors.As(err, &ce) {
		t.Errorf("VerifyFactor: %v", err)
	}
	if _, err := tr.ListFactors(context.Background(), &transport.ListFactorsRequest{}); !errors.As(err, &ce) {
		t.Errorf("ListFactors: %v", err)
	}
	if err := tr.RevokeFactor(context.Background(), &transport.RevokeFactorRequest{}); !errors.As(err, &ce) {
		t.Errorf("RevokeFactor: %v", err)
	}
}

// ===========================================================================
// transport_database.go
// ===========================================================================

func TestGRPCDatabase_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{}); !errors.As(err, &ce) {
		t.Errorf("CreateDatabase: %v", err)
	}
	if _, err := tr.GetDatabase(context.Background(), "n"); !errors.As(err, &ce) {
		t.Errorf("GetDatabase: %v", err)
	}
	if _, err := tr.ListDatabases(context.Background()); !errors.As(err, &ce) {
		t.Errorf("ListDatabases: %v", err)
	}
	if err := tr.DropDatabase(context.Background(), "n"); !errors.As(err, &ce) {
		t.Errorf("DropDatabase: %v", err)
	}
	if _, err := tr.UpdateDatabaseProtocol(context.Background(), transport.UpdateDatabaseProtocolRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateDatabaseProtocol: %v", err)
	}
	if _, err := tr.UpdateDatabaseEtcd(context.Background(), transport.UpdateDatabaseEtcdRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateDatabaseEtcd: %v", err)
	}
	if _, err := tr.UpdateDatabaseRaft(context.Background(), transport.UpdateDatabaseRaftRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateDatabaseRaft: %v", err)
	}
	if _, err := tr.UpdateDatabaseBarrier(context.Background(), transport.UpdateDatabaseBarrierRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateDatabaseBarrier: %v", err)
	}
	if _, err := tr.UpdateDatabaseConnection(context.Background(), transport.UpdateDatabaseConnectionRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateDatabaseConnection: %v", err)
	}
}

// ===========================================================================
// transport_index.go
// ===========================================================================

func TestGRPCIndex_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if err := tr.IndexCreate(context.Background(), "i", "f", transport.IndexConfig{}); !errors.As(err, &ce) {
		t.Errorf("IndexCreate: %v", err)
	}
	if err := tr.IndexDrop(context.Background(), "i"); !errors.As(err, &ce) {
		t.Errorf("IndexDrop: %v", err)
	}
	if _, err := tr.IndexQuery(context.Background(), "i", "v"); !errors.As(err, &ce) {
		t.Errorf("IndexQuery: %v", err)
	}
	if _, err := tr.IndexScan(context.Background(), "i", "a", "z"); !errors.As(err, &ce) {
		t.Errorf("IndexScan: %v", err)
	}
	if _, err := tr.IndexList(context.Background()); !errors.As(err, &ce) {
		t.Errorf("IndexList: %v", err)
	}
}

// ===========================================================================
// transport_lease.go
// ===========================================================================

func TestGRPCLease_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.LeaseGrant(context.Background(), 60); !errors.As(err, &ce) {
		t.Errorf("LeaseGrant: %v", err)
	}
	if err := tr.LeaseRevoke(context.Background(), 1); !errors.As(err, &ce) {
		t.Errorf("LeaseRevoke: %v", err)
	}
	if _, err := tr.LeaseKeepAlive(context.Background(), 1); !errors.As(err, &ce) {
		t.Errorf("LeaseKeepAlive: %v", err)
	}
	if _, err := tr.LeaseTimeToLive(context.Background(), 1); !errors.As(err, &ce) {
		t.Errorf("LeaseTimeToLive: %v", err)
	}
	if _, err := tr.LeaseList(context.Background()); !errors.As(err, &ce) {
		t.Errorf("LeaseList: %v", err)
	}
}

// ===========================================================================
// transport_lock.go
// ===========================================================================

func TestGRPCLock_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.LockAcquire(context.Background(), &transport.LockAcquireRequest{}); !errors.As(err, &ce) {
		t.Errorf("LockAcquire: %v", err)
	}
	if _, err := tr.LockTryLock(context.Background(), &transport.LockAcquireRequest{}); !errors.As(err, &ce) {
		t.Errorf("LockTryLock: %v", err)
	}
	if err := tr.LockRelease(context.Background(), &transport.LockReleaseRequest{}); !errors.As(err, &ce) {
		t.Errorf("LockRelease: %v", err)
	}
	if _, err := tr.LockExtend(context.Background(), &transport.LockExtendRequest{}); !errors.As(err, &ce) {
		t.Errorf("LockExtend: %v", err)
	}
	if _, err := tr.LockStatus(context.Background(), "k"); !errors.As(err, &ce) {
		t.Errorf("LockStatus: %v", err)
	}
	if _, err := tr.LockList(context.Background(), ""); !errors.As(err, &ce) {
		t.Errorf("LockList: %v", err)
	}
}

// ===========================================================================
// transport_mvcc.go
// ===========================================================================

func TestGRPCMVCC_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.PutWithLease(context.Background(), "k", []byte("v"), 0); !errors.As(err, &ce) {
		t.Errorf("PutWithLease: %v", err)
	}
	if _, err := tr.GetRevision(context.Background(), "k"); !errors.As(err, &ce) {
		t.Errorf("GetRevision: %v", err)
	}
	if _, err := tr.GetAtRevision(context.Background(), "k", 1); !errors.As(err, &ce) {
		t.Errorf("GetAtRevision: %v", err)
	}
	if _, err := tr.Range(context.Background(), []byte("a"), []byte("z"), transport.RangeOptions{}); !errors.As(err, &ce) {
		t.Errorf("Range: %v", err)
	}
	if err := tr.Compact(context.Background(), 1); !errors.As(err, &ce) {
		t.Errorf("Compact: %v", err)
	}
	if _, err := tr.CurrentRevision(context.Background()); !errors.As(err, &ce) {
		t.Errorf("CurrentRevision: %v", err)
	}
}

// ===========================================================================
// transport_timeseries.go
// ===========================================================================

func TestGRPCTimeSeries_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if err := tr.TSWrite(context.Background(), &transport.TSDataPoint{}); !errors.As(err, &ce) {
		t.Errorf("TSWrite: %v", err)
	}
	if err := tr.TSWriteBatch(context.Background(), nil); !errors.As(err, &ce) {
		t.Errorf("TSWriteBatch: %v", err)
	}
	if _, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{}); !errors.As(err, &ce) {
		t.Errorf("TSQuery: %v", err)
	}
	if _, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{}); !errors.As(err, &ce) {
		t.Errorf("TSAggregate: %v", err)
	}
	if err := tr.TSWritePoint(context.Background(), &transport.TSDataPoint{}); !errors.As(err, &ce) {
		t.Errorf("TSWritePoint: %v", err)
	}
	if _, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{}); !errors.As(err, &ce) {
		t.Errorf("TSRichQuery: %v", err)
	}
	if _, err := tr.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{}); !errors.As(err, &ce) {
		t.Errorf("TSDeleteSeries: %v", err)
	}
	if _, err := tr.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{}); !errors.As(err, &ce) {
		t.Errorf("TSGetRetention: %v", err)
	}
	if err := tr.TSSetRetention(context.Background(), &transport.TSSetRetentionRequest{}); !errors.As(err, &ce) {
		t.Errorf("TSSetRetention: %v", err)
	}
	if _, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{}); !errors.As(err, &ce) {
		t.Errorf("TSListMetrics: %v", err)
	}
}

// ===========================================================================
// transport_transaction.go
// ===========================================================================

func TestGRPCTransaction_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.ExecuteTransaction(context.Background(), nil)
	assertClosed(t, err)
}

// ===========================================================================
// transport_vector.go
// ===========================================================================

func TestGRPCVector_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.CreateNamespace(context.Background(), &transport.CreateNamespaceRequest{}); !errors.As(err, &ce) {
		t.Errorf("CreateNamespace: %v", err)
	}
	if _, err := tr.DeleteNamespace(context.Background(), &transport.DeleteNamespaceRequest{}); !errors.As(err, &ce) {
		t.Errorf("DeleteNamespace: %v", err)
	}
	if _, err := tr.ListNamespaces(context.Background(), &transport.ListNamespacesRequest{}); !errors.As(err, &ce) {
		t.Errorf("ListNamespaces: %v", err)
	}
	if _, err := tr.GetNamespaceInfo(context.Background(), &transport.GetNamespaceInfoRequest{}); !errors.As(err, &ce) {
		t.Errorf("GetNamespaceInfo: %v", err)
	}
	if _, err := tr.UpsertVector(context.Background(), &transport.UpsertVectorRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpsertVector: %v", err)
	}
	if _, err := tr.GetVector(context.Background(), &transport.GetVectorRequest{}); !errors.As(err, &ce) {
		t.Errorf("GetVector: %v", err)
	}
	if _, err := tr.DeleteVector(context.Background(), &transport.DeleteVectorRequest{}); !errors.As(err, &ce) {
		t.Errorf("DeleteVector: %v", err)
	}
	if _, err := tr.BatchUpsert(context.Background(), &transport.BatchUpsertRequest{}); !errors.As(err, &ce) {
		t.Errorf("BatchUpsert: %v", err)
	}
	if _, err := tr.BatchDeleteVectors(context.Background(), &transport.BatchDeleteVectorsRequest{}); !errors.As(err, &ce) {
		t.Errorf("BatchDeleteVectors: %v", err)
	}
	if _, err := tr.ListVectors(context.Background(), &transport.ListVectorsRequest{}); !errors.As(err, &ce) {
		t.Errorf("ListVectors: %v", err)
	}
	if _, err := tr.UpdateMetadata(context.Background(), &transport.UpdateVectorMetadataRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateMetadata: %v", err)
	}
	if _, err := tr.SearchSimilar(context.Background(), &transport.SearchSimilarRequest{}); !errors.As(err, &ce) {
		t.Errorf("SearchSimilar: %v", err)
	}
	if _, err := tr.SearchByMetadata(context.Background(), &transport.SearchByMetadataRequest{}); !errors.As(err, &ce) {
		t.Errorf("SearchByMetadata: %v", err)
	}
	if _, err := tr.HybridSearch(context.Background(), &transport.HybridSearchRequest{}); !errors.As(err, &ce) {
		t.Errorf("HybridSearch: %v", err)
	}
	if _, err := tr.SearchByID(context.Background(), &transport.SearchByIDRequest{}); !errors.As(err, &ce) {
		t.Errorf("SearchByID: %v", err)
	}
	if _, err := tr.RebuildIndex(context.Background(), &transport.RebuildIndexRequest{}); !errors.As(err, &ce) {
		t.Errorf("RebuildIndex: %v", err)
	}
	if _, err := tr.GetRebuildProgress(context.Background(), &transport.GetRebuildProgressRequest{}); !errors.As(err, &ce) {
		t.Errorf("GetRebuildProgress: %v", err)
	}
	if _, err := tr.GetIndexStats(context.Background(), &transport.GetIndexStatsRequest{}); !errors.As(err, &ce) {
		t.Errorf("GetIndexStats: %v", err)
	}
	if _, err := tr.CompactNamespace(context.Background(), &transport.CompactNamespaceRequest{}); !errors.As(err, &ce) {
		t.Errorf("CompactNamespace: %v", err)
	}
	if _, err := tr.GetCompactionProgress(context.Background(), &transport.GetCompactionProgressRequest{}); !errors.As(err, &ce) {
		t.Errorf("GetCompactionProgress: %v", err)
	}
}
