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

// Service-error propagation: a failing (or absent) backing service must
// surface its error unchanged through both the direct Transport methods and
// the Request dispatch table, for the admin, KV, time-series, vector and
// lease services.

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

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
