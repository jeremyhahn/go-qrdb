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

func TestGRPCImportFile_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.ImportFile(context.Background(), "csv", bytes.NewReader(nil), transport.ImportOptions{})
	assertClosed(t, err)
}

// ===========================================================================
// transport_dek.go
// ===========================================================================

func TestGRPCDEK_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	ctx := context.Background()
	checks := map[string]error{}
	_, checks["TenantDEKStatus"] = tr.TenantDEKStatus(ctx, &transport.TenantDEKRequest{})
	_, checks["TenantDEKRotate"] = tr.TenantDEKRotate(ctx, &transport.TenantDEKRequest{})
	_, checks["TenantDEKRetire"] = tr.TenantDEKRetire(ctx, &transport.TenantDEKRetireRequest{Version: 1})
	_, checks["TenantDEKEncrypt"] = tr.TenantDEKEncrypt(ctx, &transport.TenantDEKEncryptRequest{})
	_, checks["TenantDEKDecrypt"] = tr.TenantDEKDecrypt(ctx, &transport.TenantDEKDecryptRequest{})
	_, checks["TenantDEKRewrap"] = tr.TenantDEKRewrap(ctx, &transport.TenantDEKRewrapRequest{})
	for method, err := range checks {
		var ce *transport.ClosedError
		if !errors.As(err, &ce) {
			t.Errorf("%s after close: err = %v, want *transport.ClosedError", method, err)
		}
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
	if _, err := tr.IndexScan(context.Background(), "i", spIdx("a"), spIdx("z")); !errors.As(err, &ce) {
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
	if _, err := tr.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{Revision: 1}); !errors.As(err, &ce) {
		t.Errorf("RestoreToRevision: %v", err)
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

func spIdx(s string) *string { return &s }

// closedCase names one transport call expected to fail with ClosedError.
type closedCase struct {
	name string
	call func() error
}

// runClosedCases runs each call as a subtest and asserts it returns
// ClosedError.
func runClosedCases(t *testing.T, cases []closedCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { assertClosed(t, c.call()) })
	}
}

func TestGRPCUser_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	ctx := context.Background()
	runClosedCases(t, []closedCase{
		{"CreateUser", func() error {
			_, e := tr.CreateUser(ctx, &transport.CreateUserRequest{WithoutEmailLoginKey: true})
			return e
		}},
		{"GetUser", func() error { _, e := tr.GetUser(ctx, 1); return e }},
		{"GetUserByUsername", func() error { _, e := tr.GetUserByUsername(ctx, "t1", "alice"); return e }},
		{"UpdateUser", func() error { _, e := tr.UpdateUser(ctx, &transport.UpdateUserRequest{}); return e }},
		{"DeleteUser", func() error { return tr.DeleteUser(ctx, 1) }},
		{"ListUsers", func() error { _, e := tr.ListUsers(ctx, &transport.ListUsersRequest{}); return e }},
		{"EnableUser", func() error { return tr.EnableUser(ctx, 1) }},
		{"DisableUser", func() error { return tr.DisableUser(ctx, 1) }},
		{"SuspendUser", func() error { return tr.SuspendUser(ctx, 1) }},
		{"UnsuspendUser", func() error { return tr.UnsuspendUser(ctx, 1) }},
	})
}

func TestGRPCRole_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	ctx := context.Background()
	runClosedCases(t, []closedCase{
		{"CreateRole", func() error { _, e := tr.CreateRole(ctx, &transport.CreateRoleRequest{}); return e }},
		{"GetRole", func() error { _, e := tr.GetRole(ctx, "r1"); return e }},
		{"UpdateRole", func() error { _, e := tr.UpdateRole(ctx, &transport.UpdateRoleRequest{}); return e }},
		{"DeleteRole", func() error { return tr.DeleteRole(ctx, "r1") }},
		{"ListRoles", func() error { _, e := tr.ListRoles(ctx, &transport.ListRolesRequest{}); return e }},
		{"AssignRole", func() error { return tr.AssignRole(ctx, 1, "r1") }},
		{"UnassignRole", func() error { return tr.UnassignRole(ctx, 1, "r1") }},
		{"ListPermissions", func() error { _, e := tr.ListPermissions(ctx); return e }},
	})
}

func TestGRPCCert_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	ctx := context.Background()
	runClosedCases(t, []closedCase{
		{"IssueCert", func() error { _, e := tr.IssueCert(ctx, &transport.IssueCertRequest{}); return e }},
		{"EnrollCert", func() error { _, e := tr.EnrollCert(ctx, &transport.EnrollCertRequest{}); return e }},
		{"RevokeCert", func() error { _, e := tr.RevokeCert(ctx, "001"); return e }},
		{"ListCerts", func() error { _, e := tr.ListCerts(ctx, "t1"); return e }},
		{"AddTrustAnchors", func() error { _, e := tr.AddTrustAnchors(ctx, &transport.AddTrustAnchorsRequest{}); return e }},
		{"ListTrustAnchors", func() error { _, e := tr.ListTrustAnchors(ctx, "t1"); return e }},
		{"RemoveTrustAnchor", func() error { return tr.RemoveTrustAnchor(ctx, "t1", "fp1") }},
	})
}

func TestGRPCCollection_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	ctx := context.Background()
	runClosedCases(t, []closedCase{
		{"CreateCollection", func() error {
			_, e := tr.CreateCollection(ctx, transport.CreateCollectionRequest{})
			return e
		}},
		{"GetCollectionInfo", func() error { _, e := tr.GetCollectionInfo(ctx, "db", "users"); return e }},
		{"ListCollections", func() error { _, e := tr.ListCollections(ctx, "db"); return e }},
		{"DropCollection", func() error { return tr.DropCollection(ctx, "db", "users") }},
		{"InsertDocument", func() error {
			_, e := tr.InsertDocument(ctx, transport.InsertDocumentRequest{})
			return e
		}},
		{"GetDocument", func() error { _, e := tr.GetDocument(ctx, "db", "users", 1); return e }},
		{"UpdateDocument", func() error { return tr.UpdateDocument(ctx, transport.UpdateDocumentRequest{}) }},
		{"DeleteDocument", func() error { return tr.DeleteDocument(ctx, "db", "users", 1) }},
		{"ListDocuments", func() error { _, e := tr.ListDocuments(ctx, transport.ListDocumentsRequest{}); return e }},
	})
}

func TestGRPCTenant_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	ctx := context.Background()
	runClosedCases(t, []closedCase{
		{"CreateTenant", func() error { _, e := tr.CreateTenant(ctx, &transport.CreateTenantRequest{}); return e }},
		{"GetTenant", func() error { _, e := tr.GetTenant(ctx, "t1"); return e }},
		{"ListTenants", func() error { _, e := tr.ListTenants(ctx); return e }},
		{"DeleteTenant", func() error { return tr.DeleteTenant(ctx, "t1") }},
		{"UpdateTenant", func() error { _, e := tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{}); return e }},
		{"UpdateTenantQuotas", func() error {
			_, e := tr.UpdateTenantQuotas(ctx, &transport.UpdateTenantQuotasRequest{})
			return e
		}},
	})
}

func TestGRPCUserAuthFactor_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	ctx := context.Background()
	runClosedCases(t, []closedCase{
		{"AddAuthFactor", func() error { _, e := tr.AddAuthFactor(ctx, &transport.AuthFactorAddRequest{}); return e }},
		{"ListAuthFactors", func() error { _, e := tr.ListAuthFactors(ctx, &transport.AuthFactorListRequest{}); return e }},
		{"GetAuthFactor", func() error { _, e := tr.GetAuthFactor(ctx, &transport.AuthFactorGetRequest{}); return e }},
		{"RemoveAuthFactor", func() error {
			_, e := tr.RemoveAuthFactor(ctx, &transport.AuthFactorRemoveRequest{})
			return e
		}},
		{"DeleteAuthFactor", func() error {
			_, e := tr.DeleteAuthFactor(ctx, &transport.AuthFactorDeleteRequest{})
			return e
		}},
		{"RotateAuthFactor", func() error {
			_, e := tr.RotateAuthFactor(ctx, &transport.AuthFactorRotateRequest{})
			return e
		}},
		{"RelabelAuthFactor", func() error {
			_, e := tr.RelabelAuthFactor(ctx, &transport.AuthFactorRelabelRequest{})
			return e
		}},
		{"LoginAuthFactor", func() error {
			_, e := tr.LoginAuthFactor(ctx, &transport.AuthFactorLoginRequest{})
			return e
		}},
	})
}

func TestGRPCWatch_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	ctx := context.Background()
	runClosedCases(t, []closedCase{
		{"Watch", func() error { _, e := tr.Watch(ctx, "k", transport.WatchOptions{}); return e }},
		{"WatchPrefix", func() error { _, e := tr.WatchPrefix(ctx, "p", transport.WatchOptions{}); return e }},
	})
}
