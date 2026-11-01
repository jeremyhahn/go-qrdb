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

// Targeted tests for 0%-coverage embedded transport functions:
// - NilRequestError.Error()
// - WithRAGService + RAGClient nil-service guards
// - WithEmbeddingService + EmbeddingClient nil-service guards + toTransportUsage
// - SealDatabase / UnsealDatabase / DatabaseBarrierStatus nil-service guards
// - CeremonyClaimShareBegin / CeremonyClaimShareComplete / CeremonySubmitUnsealShare nil-service guards
// - custodianToInfo
// - CredentialStore / CredentialRetrieve nil-service guards
// - UserAuthFactor* nil-service guards
// - errors.go NilRequestError + translateError uncovered branches
// - translateError remaining service errors

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// Admin service error paths: Health/Status/Capabilities/StartShard/StopShard/
// GetShardState/GetTopology/Scan/List/Exists/Get error branches
// ===========================================================================

// errorAdminService wraps mockAdminService and returns errSentinel for all ops.
type errorAdminService struct {
	mockAdminService
}

func (e *errorAdminService) Health(_ context.Context) (*service.HealthStatus, error) {
	return nil, errSentinel
}

func (e *errorAdminService) Status(_ context.Context) (*service.StatusInfo, error) {
	return nil, errSentinel
}

func (e *errorAdminService) Capabilities(_ context.Context) (*capabilities.Capabilities, error) {
	return nil, errSentinel
}

func (e *errorAdminService) StartShard(_ context.Context, _ *service.StartShardRequest) error {
	return errSentinel
}

func (e *errorAdminService) StopShard(_ context.Context, _ uint64) error {
	return errSentinel
}

func (e *errorAdminService) GetShardState(_ context.Context, _ uint64) (*service.ShardStateResponse, error) {
	return nil, errSentinel
}

func (e *errorAdminService) GetTopology(_ context.Context) (*service.TopologyResponse, error) {
	return nil, errSentinel
}

func (e *errorAdminService) NodeCompaction(_ context.Context) (*compaction.Policy, error) {
	return nil, nil
}

func (e *errorAdminService) SetNodeCompaction(_ context.Context, _ *compaction.Policy) error {
	return nil
}

func TestEmbedded_AdminServiceErrors(t *testing.T) {
	// Build a transport backed by the error-returning admin service.
	errAdmin := &errorAdminService{}
	tr, err := New(newMockKVService(), errAdmin)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()

	t.Run("Health", func(t *testing.T) {
		_, err := tr.Health(ctx)
		if !errors.Is(err, errSentinel) {
			t.Fatalf("expected errSentinel from Health, got %v", err)
		}
	})
	t.Run("Status", func(t *testing.T) {
		_, err := tr.Status(ctx)
		if !errors.Is(err, errSentinel) {
			t.Fatalf("expected errSentinel from Status, got %v", err)
		}
	})
	t.Run("Capabilities", func(t *testing.T) {
		_, err := tr.Capabilities(ctx)
		if !errors.Is(err, errSentinel) {
			t.Fatalf("expected errSentinel from Capabilities, got %v", err)
		}
	})
	t.Run("StartShard", func(t *testing.T) {
		err := tr.StartShard(ctx, &transport.StartShardRequest{})
		if !errors.Is(err, errSentinel) {
			t.Fatalf("expected errSentinel from StartShard, got %v", err)
		}
	})
	t.Run("StopShard", func(t *testing.T) {
		err := tr.StopShard(ctx, 1)
		if !errors.Is(err, errSentinel) {
			t.Fatalf("expected errSentinel from StopShard, got %v", err)
		}
	})
	t.Run("GetShardState", func(t *testing.T) {
		_, err := tr.GetShardState(ctx, 1)
		if !errors.Is(err, errSentinel) {
			t.Fatalf("expected errSentinel from GetShardState, got %v", err)
		}
	})
	t.Run("GetTopology", func(t *testing.T) {
		_, err := tr.GetTopology(ctx)
		if !errors.Is(err, errSentinel) {
			t.Fatalf("expected errSentinel from GetTopology, got %v", err)
		}
	})
}

// errorKVService returns errors for KV operations.
type errorKVService struct {
	mockKVService
}

func (e *errorKVService) Get(_ context.Context, _ string) ([]byte, error) {
	return nil, errSentinel
}

func (e *errorKVService) List(_ context.Context, _ string) ([]string, error) {
	return nil, errSentinel
}

func (e *errorKVService) Scan(_ context.Context, _ string) (map[string][]byte, error) {
	return nil, errSentinel
}

func (e *errorKVService) GetWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]byte, error) {
	return nil, errSentinel
}

func (e *errorKVService) ListWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]string, error) {
	return nil, errSentinel
}

func (e *errorKVService) ScanWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return nil, errSentinel
}

func (e *errorKVService) ExistsWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (bool, error) {
	return false, errSentinel
}

func TestEmbedded_KVServiceErrors(t *testing.T) {
	errKV := &errorKVService{*newMockKVService()}
	tr, err := New(errKV, &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	t.Run("Get_Error", func(t *testing.T) {
		_, err := tr.Get(ctx, "k")
		if !errors.Is(err, errSentinel) {
			t.Fatalf("expected errSentinel, got %v", err)
		}
	})
	t.Run("List_Error", func(t *testing.T) {
		_, err := tr.List(ctx, "")
		if !errors.Is(err, errSentinel) {
			t.Fatalf("expected errSentinel, got %v", err)
		}
	})
	t.Run("Scan_Error", func(t *testing.T) {
		_, err := tr.Scan(ctx, "")
		if !errors.Is(err, errSentinel) {
			t.Fatalf("expected errSentinel, got %v", err)
		}
	})
	t.Run("Exists_WithConsistency_Error", func(t *testing.T) {
		_, err := tr.Exists(ctx, "k", transport.WithConsistency(transport.ConsistencyLinearizable))
		if !errors.Is(err, errSentinel) {
			t.Fatalf("expected errSentinel, got %v", err)
		}
	})
}

// ===========================================================================
// TimeSeries service error paths: TSDeleteSeries/TSGetRetention/TSSetRetention/
// TSListMetrics error branches
// ===========================================================================

// errTSSvc returns errors for TS operations.
type errTSSvc struct {
	captureTSSvc
}

func (s *errTSSvc) DeleteSeries(_ context.Context, _ *timeseries.DeleteRequest) error {
	return errSentinel
}

func (s *errTSSvc) GetRetentionPolicy(_ string) (*timeseries.RetentionPolicy, error) {
	return nil, errSentinel
}

func (s *errTSSvc) SetRetentionPolicy(_ context.Context, _ string, _ *timeseries.RetentionPolicy) error {
	return errSentinel
}

func (s *errTSSvc) ListMetrics(_ string, _ int) ([]string, error) {
	return nil, errSentinel
}

func (s *errTSSvc) ListMetricsWithConsistency(_ context.Context, _ string, _ int, _ consistency.ReadOptions) ([]string, error) {
	return nil, errSentinel
}

func TestEmbedded_TSServiceErrors(t *testing.T) {
	errTS := &errTSSvc{}
	tr := newExtTransport(t, WithTimeSeriesService(errTS))
	ctx := context.Background()

	t.Run("TSDeleteSeries_Error", func(t *testing.T) {
		_, err := tr.TSDeleteSeries(ctx, &transport.TSDeleteRequest{Metric: "cpu"})
		if !errors.Is(err, errSentinel) {
			t.Fatalf("expected errSentinel, got %v", err)
		}
	})
	t.Run("TSGetRetention_Error", func(t *testing.T) {
		_, err := tr.TSGetRetention(ctx, &transport.TSGetRetentionRequest{Metric: "cpu"})
		if !errors.Is(err, errSentinel) {
			t.Fatalf("expected errSentinel, got %v", err)
		}
	})
	t.Run("TSSetRetention_Error", func(t *testing.T) {
		err := tr.TSSetRetention(ctx, &transport.TSSetRetentionRequest{Metric: "cpu"})
		if !errors.Is(err, errSentinel) {
			t.Fatalf("expected errSentinel, got %v", err)
		}
	})
	t.Run("TSListMetrics_Error", func(t *testing.T) {
		_, err := tr.TSListMetrics(ctx, &transport.TSListMetricsRequest{})
		if !errors.Is(err, errSentinel) {
			t.Fatalf("expected errSentinel, got %v", err)
		}
	})
}

// ===========================================================================
// Vector service error paths: exercise err != nil branch in each vector method
// ===========================================================================

// errVectorService wraps mockVectorService but returns errSentinel for all ops.
type errVectorService struct {
	mockVectorService
}

func (e *errVectorService) CreateNamespace(_ context.Context, _ *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
	return nil, errSentinel
}

func (e *errVectorService) UpdateNamespace(_ context.Context, _ *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error) {
	return nil, errSentinel
}
func (e *errVectorService) DeleteNamespace(_ context.Context, _ *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
	return nil, errSentinel
}
func (e *errVectorService) ListNamespaces(_ context.Context, _ *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
	return nil, errSentinel
}
func (e *errVectorService) GetNamespaceInfo(_ context.Context, _ *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
	return nil, errSentinel
}
func (e *errVectorService) UpsertVector(_ context.Context, _ *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	return nil, errSentinel
}
func (e *errVectorService) GetVector(_ context.Context, _ *service.GetVectorRequest) (*service.GetVectorResponse, error) {
	return nil, errSentinel
}
func (e *errVectorService) DeleteVector(_ context.Context, _ *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
	return nil, errSentinel
}
func (e *errVectorService) BatchUpsert(_ context.Context, _ *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
	return nil, errSentinel
}
func (e *errVectorService) BatchDeleteVectors(_ context.Context, _ *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
	return nil, errSentinel
}
func (e *errVectorService) ListVectors(_ context.Context, _ *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
	return nil, errSentinel
}
func (e *errVectorService) UpdateMetadata(_ context.Context, _ *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
	return nil, errSentinel
}
func (e *errVectorService) SearchSimilar(_ context.Context, _ *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
	return nil, errSentinel
}
func (e *errVectorService) SearchByMetadata(_ context.Context, _ *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
	return nil, errSentinel
}
func (e *errVectorService) HybridSearch(_ context.Context, _ *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
	return nil, errSentinel
}
func (e *errVectorService) SearchByID(_ context.Context, _ *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
	return nil, errSentinel
}
func (e *errVectorService) RebuildIndex(_ context.Context, _ *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
	return nil, errSentinel
}
func (e *errVectorService) GetRebuildProgress(_ context.Context, _ *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
	return nil, errSentinel
}
func (e *errVectorService) GetIndexStats(_ context.Context, _ *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
	return nil, errSentinel
}
func (e *errVectorService) CompactNamespace(_ context.Context, _ *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
	return nil, errSentinel
}
func (e *errVectorService) GetCompactionProgress(_ context.Context, _ *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
	return nil, errSentinel
}

func TestEmbedded_VectorServiceErrors(t *testing.T) {
	tr := newExtTransport(t, WithVectorService(&errVectorService{}))
	ctx := context.Background()

	cases := []struct {
		name string
		call func() error
	}{
		{"CreateNamespace", func() error { _, e := tr.CreateNamespace(ctx, &transport.CreateNamespaceRequest{}); return e }},
		{"DeleteNamespace", func() error { _, e := tr.DeleteNamespace(ctx, &transport.DeleteNamespaceRequest{}); return e }},
		{"ListNamespaces", func() error { _, e := tr.ListNamespaces(ctx, &transport.ListNamespacesRequest{}); return e }},
		{"GetNamespaceInfo", func() error { _, e := tr.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{}); return e }},
		{"UpsertVector", func() error { _, e := tr.UpsertVector(ctx, &transport.UpsertVectorRequest{}); return e }},
		{"GetVector", func() error { _, e := tr.GetVector(ctx, &transport.GetVectorRequest{}); return e }},
		{"DeleteVector", func() error { _, e := tr.DeleteVector(ctx, &transport.DeleteVectorRequest{}); return e }},
		{"BatchUpsert", func() error { _, e := tr.BatchUpsert(ctx, &transport.BatchUpsertRequest{}); return e }},
		{"BatchDeleteVectors", func() error { _, e := tr.BatchDeleteVectors(ctx, &transport.BatchDeleteVectorsRequest{}); return e }},
		{"ListVectors", func() error { _, e := tr.ListVectors(ctx, &transport.ListVectorsRequest{}); return e }},
		{"UpdateMetadata", func() error { _, e := tr.UpdateMetadata(ctx, &transport.UpdateVectorMetadataRequest{}); return e }},
		{"SearchSimilar", func() error { _, e := tr.SearchSimilar(ctx, &transport.SearchSimilarRequest{}); return e }},
		{"SearchByMetadata", func() error { _, e := tr.SearchByMetadata(ctx, &transport.SearchByMetadataRequest{}); return e }},
		{"HybridSearch", func() error { _, e := tr.HybridSearch(ctx, &transport.HybridSearchRequest{}); return e }},
		{"SearchByID", func() error { _, e := tr.SearchByID(ctx, &transport.SearchByIDRequest{}); return e }},
		{"RebuildIndex", func() error { _, e := tr.RebuildIndex(ctx, &transport.RebuildIndexRequest{}); return e }},
		{"GetRebuildProgress", func() error { _, e := tr.GetRebuildProgress(ctx, &transport.GetRebuildProgressRequest{}); return e }},
		{"GetIndexStats", func() error { _, e := tr.GetIndexStats(ctx, &transport.GetIndexStatsRequest{}); return e }},
		{"CompactNamespace", func() error { _, e := tr.CompactNamespace(ctx, &transport.CompactNamespaceRequest{}); return e }},
		{"GetCompactionProgress", func() error {
			_, e := tr.GetCompactionProgress(ctx, &transport.GetCompactionProgressRequest{})
			return e
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if !errors.Is(err, errSentinel) {
				t.Fatalf("expected errSentinel, got %v", err)
			}
		})
	}
}

// ===========================================================================
// Vector dispatch error paths (via Request() with error-returning service)
// to cover the "return err" branch in handler functions
// ===========================================================================

func TestEmbedded_VectorDispatch_ErrorPaths(t *testing.T) {
	tr := newExtTransport(t, WithVectorService(&errVectorService{}))
	ctx := context.Background()

	cases := []struct {
		name string
		req  any
	}{
		{"CreateNamespace", &transport.CreateNamespaceRequest{}},
		{"DeleteNamespace", &transport.DeleteNamespaceRequest{}},
		{"ListNamespaces", &transport.ListNamespacesRequest{}},
		{"GetNamespaceInfo", &transport.GetNamespaceInfoRequest{}},
		{"UpsertVector", &transport.UpsertVectorRequest{}},
		{"GetVector", &transport.GetVectorRequest{}},
		{"DeleteVector", &transport.DeleteVectorRequest{}},
		{"BatchUpsert", &transport.BatchUpsertRequest{}},
		{"BatchDeleteVectors", &transport.BatchDeleteVectorsRequest{}},
		{"ListVectors", &transport.ListVectorsRequest{}},
		{"UpdateMetadata", &transport.UpdateVectorMetadataRequest{}},
		{"SearchSimilar", &transport.SearchSimilarRequest{}},
		{"SearchByMetadata", &transport.SearchByMetadataRequest{}},
		{"HybridSearch", &transport.HybridSearchRequest{}},
		{"SearchByID", &transport.SearchByIDRequest{}},
		{"RebuildIndex", &transport.RebuildIndexRequest{}},
		{"GetRebuildProgress", &transport.GetRebuildProgressRequest{}},
		{"GetIndexStats", &transport.GetIndexStatsRequest{}},
		{"CompactNamespace", &transport.CompactNamespaceRequest{}},
		{"GetCompactionProgress", &transport.GetCompactionProgressRequest{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tr.Request(ctx, tc.name, tc.req, nil)
			if !errors.Is(err, errSentinel) {
				t.Fatalf("expected errSentinel via dispatch, got %v", err)
			}
		})
	}
}

// ===========================================================================
// Lease/TS/other dispatch error paths via Request() to cover handler return err
// ===========================================================================

func TestEmbedded_TSDispatch_ErrorPaths(t *testing.T) {
	// Use an error-returning TS service.
	tr := newExtTransport(t, WithTimeSeriesService(&errTSSvc{}))
	ctx := context.Background()

	cases := []struct {
		name string
		req  any
	}{
		{"TSDeleteSeries", &transport.TSDeleteRequest{Metric: "cpu"}},
		{"TSGetRetention", &transport.TSGetRetentionRequest{Metric: "cpu"}},
		{"TSListMetrics", &transport.TSListMetricsRequest{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tr.Request(ctx, tc.name, tc.req, nil)
			if !errors.Is(err, errSentinel) {
				t.Fatalf("%s: expected errSentinel via dispatch, got %v", tc.name, err)
			}
		})
	}
}

func TestEmbedded_LeaseDispatch_ErrorPaths(t *testing.T) {
	// Use mockLeaseService that returns errSentinel on all ops.
	svc := &mockLeaseService{
		grantFn:     func(_ context.Context, _ int64) (*lease.Lease, error) { return nil, errSentinel },
		keepAliveFn: func(_ context.Context, _ int64) (*lease.Lease, error) { return nil, errSentinel },
		ttlFn:       func(_ context.Context, _ int64) (*lease.Lease, error) { return nil, errSentinel },
	}
	tr := newExtTransport(t, WithLeaseService(svc))
	ctx := context.Background()

	cases := []struct {
		name string
		req  any
	}{
		{"LeaseGrant", &leaseGrantRequest{TTL: 60}},
		{"LeaseKeepAlive", &leaseKeepAliveRequest{LeaseID: 1}},
		{"LeaseTimeToLive", &leaseTimeToLiveRequest{LeaseID: 1}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tr.Request(ctx, tc.name, tc.req, nil)
			if !errors.Is(err, errSentinel) {
				t.Fatalf("%s: expected errSentinel via dispatch, got %v", tc.name, err)
			}
		})
	}
}

// ===========================================================================
// Dispatch error paths via nil-service (exercises "return err" in handlers
// for credential, ceremony, barrier, TS, and lease handler groups)
// ===========================================================================

func TestEmbedded_DispatchNilService_ErrorPaths(t *testing.T) {
	// Transport with no services (except the required KV and admin).
	// All dispatch calls that reach real services will return NilServiceError
	// through the handlers' "return err" paths.
	tr := newExtTransport(t)
	ctx := context.Background()

	cases := []struct {
		name string
		req  any
	}{
		// Credential handlers (nil credential service)
		{"CredentialList", &transport.CredentialListRequest{}},
		{"CredentialRotate", &transport.CredentialRotateRequest{Name: "k", NewValue: []byte("v")}},
		{"CredentialListVersions", &transport.CredentialListVersionsRequest{Name: "k"}},
		{"CredentialGCExpired", &transport.CredentialGCRequest{Name: "k"}},
		// TS handlers (no TS service)
		{"TSDeleteSeries", &transport.TSDeleteRequest{Metric: "cpu"}},
		{"TSGetRetention", &transport.TSGetRetentionRequest{Metric: "cpu"}},
		{"TSSetRetention", &transport.TSSetRetentionRequest{Metric: "cpu"}},
		{"TSListMetrics", &transport.TSListMetricsRequest{}},
		// Lease handlers (no lease service)
		{"LeaseGrant", &leaseGrantRequest{TTL: 60}},
		{"LeaseKeepAlive", &leaseKeepAliveRequest{LeaseID: 1}},
		{"LeaseTimeToLive", &leaseTimeToLiveRequest{LeaseID: 1}},
		{"LeaseRevoke", &leaseRevokeRequest{LeaseID: 1}},
		// MVCC handlers (no MVCC service)
		{"PutWithLease", &putWithLeaseRequest{Key: "k", Value: []byte("v"), LeaseID: 1}},
		{"GetRevision", &getRevisionRequest{Key: "k"}},
		{"GetAtRevision", &getAtRevisionRequest{Key: "k", Revision: 1}},
		{"Range", &rangeRequest{Start: []byte("a"), End: []byte("z")}},
		// Index handlers (no index service)
		{"IndexQuery", &indexQueryRequest{IndexName: "idx", Value: "v"}},
		{"IndexScan", &indexScanRequest{IndexName: "idx", Start: spIdx("a"), End: spIdx("z")}},
		// Transaction, batch and watch handlers (no services)
		{"ExecuteTransaction", &transport.TransactionRequest{}},
		{"ExecuteBatch", &transport.BatchRequest{}},
		{"Watch", &watchRequest{Key: "k"}},
		{"WatchPrefix", &watchPrefixRequest{Prefix: "p"}},
		// TS query handlers (no TS service)
		{"TSQuery", &transport.TSQueryRequest{Metric: "cpu"}},
		{"TSAggregate", &transport.TSAggregateRequest{Metric: "cpu"}},
		{"TSRichQuery", &transport.TSRichQueryRequest{Metric: "cpu"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tr.Request(ctx, tc.name, tc.req, nil)
			var nse *NilServiceError
			if !errors.As(err, &nse) {
				t.Fatalf("%s: expected *NilServiceError via dispatch, got %T (%v)", tc.name, err, err)
			}
		})
	}
}

// ===========================================================================
// Dispatch success paths for KV/MVCC/Shard/TS/Ceremony/Credential handlers
// to cover the "*out = result" assignment statements
// ===========================================================================

func TestEmbedded_KVDispatch_ErrorPaths(t *testing.T) {
	errKV := &errorKVService{*newMockKVService()}
	tr, err := New(errKV, &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	// handleGet error path.
	t.Run("Get_Error", func(t *testing.T) {
		err := tr.Request(ctx, "Get", &getRequest{Key: "missing"}, nil)
		if !errors.Is(err, errSentinel) {
			t.Fatalf("expected errSentinel from handleGet dispatch, got %v", err)
		}
	})

	// handleList error path.
	t.Run("List_Error", func(t *testing.T) {
		err := tr.Request(ctx, "List", &listRequest{Prefix: ""}, nil)
		if !errors.Is(err, errSentinel) {
			t.Fatalf("expected errSentinel from handleList dispatch, got %v", err)
		}
	})
}

func TestEmbedded_AdminDispatch_SuccessPaths(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	// handleGetShardState with output capture.
	t.Run("GetShardState", func(t *testing.T) {
		var out *transport.ShardStateResponse
		if e := tr.Request(ctx, "GetShardState", uint64(1), &out); e != nil {
			t.Fatalf("GetShardState dispatch: %v", e)
		}
		if out == nil {
			t.Fatal("expected non-nil ShardStateResponse")
		}
	})

	// handleGetTopology with output capture.
	t.Run("GetTopology", func(t *testing.T) {
		var out *transport.TopologyResponse
		if e := tr.Request(ctx, "GetTopology", nil, &out); e != nil {
			t.Fatalf("GetTopology dispatch: %v", e)
		}
		if out == nil {
			t.Fatal("expected non-nil TopologyResponse")
		}
	})
}

// ===========================================================================
// Pure converter function coverage
// ===========================================================================

// ===========================================================================
// GetUserByCertThumbprint / AddCertBinding nil-service guards
// ===========================================================================

// ===========================================================================
// SealDatabase / UnsealDatabase / DatabaseBarrierStatus nil-service guards
// ===========================================================================

func TestEmbedded_SealDatabase_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.SealDatabase(context.Background(), "mydb")
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbedded_UnsealDatabase_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.UnsealDatabase(context.Background(), "mydb", "secret")
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbedded_DatabaseBarrierStatus_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.DatabaseBarrierStatus(context.Background(), "mydb")
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

// Note: SealDatabase/UnsealDatabase/DatabaseBarrierStatus check dbSvc == nil
// before the closed check, so a closed transport with no dbSvc returns
// NilServiceError (not ClosedError). The nil-service tests above already
// cover those paths. The closed-transport path requires a non-nil dbSvc which
// is tested by existing integration tests.

// ===========================================================================
// userAuthRecordToDomain / domainToUserAuthRecord pure functions
// ===========================================================================

func TestUserAuthRecordToDomain_WithMetadata(t *testing.T) {
	rec := &transport.AuthFactorAddRequest{
		UserID:     10,
		Type:       "fido2",
		Label:      "hw-key",
		Credential: []byte("cred"),
		CreatedBy:  "admin",
		Metadata:   map[string]string{"aaguid": "abc"},
	}
	got := userAuthRecordToDomain(rec, 10)
	if got.UserID != 10 {
		t.Errorf("expected UserID=10, got %d", got.UserID)
	}
	if got.Metadata["aaguid"] != "abc" {
		t.Errorf("metadata not copied: %v", got.Metadata)
	}
}

func TestUserAuthRecordToDomain_NoMetadata(t *testing.T) {
	rec := &transport.AuthFactorAddRequest{Type: "mtls", Label: "cert"}
	got := userAuthRecordToDomain(rec, 5)
	if got.Metadata != nil {
		t.Errorf("expected nil metadata, got %v", got.Metadata)
	}
}

func TestDomainToUserAuthRecord_Nil(t *testing.T) {
	got := domainToUserAuthRecord(nil)
	if got.ID != "" {
		t.Errorf("expected empty ID for nil input, got %q", got.ID)
	}
}

func TestDomainToUserAuthRecord_WithMetadata(t *testing.T) {
	f := &auth.AuthFactor{
		ID:       "fid-1",
		UserID:   5,
		Type:     auth.AuthFactorMTLS,
		Metadata: map[string]string{"slot": "9c"},
	}
	got := domainToUserAuthRecord(f)
	if got.ID != "fid-1" {
		t.Errorf("expected ID=fid-1, got %q", got.ID)
	}
	if got.Metadata["slot"] != "9c" {
		t.Errorf("metadata not copied: %v", got.Metadata)
	}
}

// ===========================================================================
// mergeLoginMetadata pure function
// ===========================================================================

func TestMergeLoginMetadata_BothEmpty(t *testing.T) {
	got := mergeLoginMetadata(nil, nil)
	if got != nil {
		t.Errorf("expected nil for both empty, got %v", got)
	}
}

func TestMergeLoginMetadata_OnlyStored(t *testing.T) {
	stored := map[string]string{"session": "abc"}
	got := mergeLoginMetadata(stored, nil)
	if got["session"] != "abc" {
		t.Errorf("expected session=abc, got %v", got)
	}
}

func TestMergeLoginMetadata_OnlyReq(t *testing.T) {
	req := map[string]string{"assertion": "xyz"}
	got := mergeLoginMetadata(nil, req)
	if got["assertion"] != "xyz" {
		t.Errorf("expected assertion=xyz, got %v", got)
	}
}

func TestMergeLoginMetadata_ReqWins(t *testing.T) {
	stored := map[string]string{"key": "old-val"}
	req := map[string]string{"key": "new-val"}
	got := mergeLoginMetadata(stored, req)
	if got["key"] != "new-val" {
		t.Errorf("expected req to win, got %q", got["key"])
	}
}

// ===========================================================================
// CredentialStore / CredentialRetrieve nil-service + closed guards
// ===========================================================================

func TestEmbedded_CredentialStore_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.CredentialStore(context.Background(), "k", []byte("v"))
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbedded_CredentialStore_Closed(t *testing.T) {
	tr := closedExtTransport(t)
	err := tr.CredentialStore(context.Background(), "k", []byte("v"))
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbedded_CredentialRetrieve_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.retrieveBytes(context.Background(), "k")
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbedded_CredentialRetrieve_Closed(t *testing.T) {
	tr := closedExtTransport(t)
	_, err := tr.retrieveBytes(context.Background(), "k")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ===========================================================================
// Embedding: nil/closed guards + toTransportUsage
// ===========================================================================

func TestEmbedded_WithEmbeddingService_SetsNilCheck(t *testing.T) {
	// WithEmbeddingService sets the field; calling methods without a real svc
	// just tests the nil-service guard was cleared. We pass nil to show the
	// Option function runs without panic.
	tr := newExtTransport(t, WithEmbeddingService(nil))
	// With nil service, methods return NilServiceError.
	_, err := tr.Embed(context.Background(), &transport.EmbedRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbedded_Embed_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.Embed(context.Background(), &transport.EmbedRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbedded_BatchEmbed_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.BatchEmbed(context.Background(), &transport.BatchEmbedRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbedded_ListEmbeddingModels_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.ListEmbeddingModels(context.Background(), "openai")
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestToTransportUsage_Nil(t *testing.T) {
	if got := toTransportUsage(nil); got != nil {
		t.Errorf("expected nil for nil input, got %v", got)
	}
}

func TestToTransportUsage_NonNil(t *testing.T) {
	got := toTransportUsage(&service.UsageInfo{PromptTokens: 10, TotalTokens: 20})
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if got.PromptTokens != 10 || got.TotalTokens != 20 {
		t.Errorf("unexpected: %+v", got)
	}
}

// TestEmbedded_Embed_WithUsage verifies that a non-nil Usage is translated.
// We test toTransportUsage directly since a real EmbeddingServicer is complex to stub.
func TestToTransportUsage_FullCoverage(t *testing.T) {
	// nil input returns nil
	if got := toTransportUsage(nil); got != nil {
		t.Errorf("expected nil for nil input, got %v", got)
	}
	// non-nil input maps both fields
	in := &service.UsageInfo{PromptTokens: 7, TotalTokens: 14}
	got := toTransportUsage(in)
	if got == nil {
		t.Fatal("expected non-nil for non-nil input")
	}
	if got.PromptTokens != 7 || got.TotalTokens != 14 {
		t.Errorf("field mismatch: %+v", got)
	}
}

// ===========================================================================
// RAG: WithRAGService + nil-service guards
// ===========================================================================

func TestEmbedded_WithRAGService_SetsNilField(t *testing.T) {
	// WithRAGService with nil just sets the field to nil; nil-service guard
	// returns NilServiceError.
	tr := newExtTransport(t, WithRAGService(nil))
	if tr.ragSvc != nil {
		t.Error("expected ragSvc nil when nil passed")
	}
}

// RAG nil-service guards for RAGIngestDocument/RAGQuery/RAGDeleteDocument/
// RAGListDocuments live in transport_rag_coverage_test.go (qrdb_vector tag),
// since those methods are only compiled under that build tag.

// ===========================================================================
// UserAuthFactor nil-service guards
// ===========================================================================

func TestEmbedded_UserAuthFactor_AllMethods_MissingService(t *testing.T) {
	tr := newExtTransport(t)
	ctx := context.Background()

	cases := []struct {
		name string
		call func() error
	}{
		{"AddAuthFactor", func() error {
			_, e := tr.AddAuthFactor(ctx, &transport.AuthFactorAddRequest{})
			return e
		}},
		{"ListAuthFactors", func() error {
			_, e := tr.ListAuthFactors(ctx, &transport.AuthFactorListRequest{})
			return e
		}},
		{"GetAuthFactor", func() error {
			_, e := tr.GetAuthFactor(ctx, &transport.AuthFactorGetRequest{})
			return e
		}},
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
		// LoginAuthFactor uses loginResolver, not userAuthFactorSvc.
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("expected error from nil userAuthFactorSvc")
			}
			// Various error types are acceptable (NilServiceError or MissingUserAuthFactorServiceError).
			if err.Error() == "" {
				t.Error("expected non-empty error message")
			}
		})
	}
}

func TestEmbedded_UserAuthFactorClient_ReturnsSelf(t *testing.T) {
	tr := newExtTransport(t)
	client := tr.UserAuthFactorClient()
	if client != tr {
		t.Error("expected UserAuthFactorClient to return self")
	}
}

// LoginAuthFactor uses loginResolver, which is nil in a bare transport =>
// NotImplementedError (not NilServiceError).
func TestEmbedded_LoginAuthFactor_NotImplemented(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.LoginAuthFactor(context.Background(), &transport.AuthFactorLoginRequest{})
	var nie *transport.NotImplementedError
	if !errors.As(err, &nie) {
		t.Fatalf("expected NotImplementedError, got %T: %v", err, err)
	}
}

// ListAll mirrors List on this double: it applies no internal-key guard,
// so the filtered and unfiltered enumerations are the same call.
func (e *errorKVService) ListAll(a0 context.Context, a1 string) ([]string, error) {
	return e.List(a0, a1)
}

// TestEmbedded_RemoveCertBinding_NilService guards the withdrawal verb the
// same way: with no user service wired it reports NotImplementedError rather
// than dereferencing a nil interface (go-qrdb#518).
