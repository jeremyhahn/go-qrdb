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
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// Mock: VectorServicer (full impl)
// ===========================================================================

type mockVectorService struct{}

var _ service.VectorServicer = (*mockVectorService)(nil)

func (m *mockVectorService) CreateNamespace(_ context.Context, req *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
	return &service.CreateNamespaceResponse{Namespace: req.Namespace, Dimension: req.Dimension, Success: true}, nil
}

func (m *mockVectorService) UpdateNamespace(_ context.Context, req *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error) {
	return &service.UpdateNamespaceResponse{Namespace: req.Namespace, RequiresRebuild: len(req.IndexConfig) > 0, Success: true}, nil
}

func (m *mockVectorService) DeleteNamespace(_ context.Context, _ *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
	return &service.DeleteNamespaceResponse{Success: true, VectorsDeleted: 5}, nil
}

func (m *mockVectorService) ListNamespaces(_ context.Context, _ *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
	return &service.ListNamespacesResponse{
		Namespaces: []*service.NamespaceInfo{{Namespace: "ns", Dimension: 128, VectorCount: 100, DistanceMetric: "L2"}},
		TotalCount: 1,
	}, nil
}

func (m *mockVectorService) GetNamespaceInfo(_ context.Context, req *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
	return &service.GetNamespaceInfoResponse{Namespace: req.Namespace, Dimension: 128, VectorCount: 100, DistanceMetric: "L2"}, nil
}

func (m *mockVectorService) UpsertVector(_ context.Context, req *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	return &service.UpsertVectorResponse{ID: req.ID, Version: 1, Success: true}, nil
}

func (m *mockVectorService) GetVector(_ context.Context, req *service.GetVectorRequest) (*service.GetVectorResponse, error) {
	return &service.GetVectorResponse{ID: req.ID, Vector: []float32{0.1}, Metadata: map[string]interface{}{"k": "v"}, Version: 1, Found: true}, nil
}

func (m *mockVectorService) DeleteVector(_ context.Context, _ *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
	return &service.DeleteVectorResponse{Success: true}, nil
}

func (m *mockVectorService) BatchUpsert(_ context.Context, req *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
	return &service.BatchUpsertResponse{IDs: []uint64{1}, Versions: []uint64{1}, Success: true, InsertedCount: len(req.Vectors)}, nil
}

func (m *mockVectorService) BatchDeleteVectors(_ context.Context, req *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
	return &service.BatchDeleteVectorsResponse{DeletedCount: len(req.IDs), Success: true}, nil
}

func (m *mockVectorService) ListVectors(_ context.Context, _ *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
	return &service.ListVectorsResponse{
		Vectors:    []*service.VectorData{{ID: 1, Vector: []float32{0.1}, Metadata: map[string]interface{}{"k": "v"}, Version: 1}},
		TotalCount: 1,
	}, nil
}

func (m *mockVectorService) UpdateMetadata(_ context.Context, req *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
	return &service.UpdateVectorMetadataResponse{ID: req.ID, Version: 2, Success: true}, nil
}

func (m *mockVectorService) SearchSimilar(_ context.Context, _ *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
	return &service.SearchSimilarResponse{
		Results: []*service.VectorSearchResult{{ID: 1, Score: 0.95, Vector: []float32{0.1}, Metadata: map[string]interface{}{"k": "v"}, Rank: 1}},
	}, nil
}

func (m *mockVectorService) SearchByMetadata(_ context.Context, _ *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
	return &service.SearchByMetadataResponse{
		Results: []*service.VectorData{{ID: 1, Vector: []float32{0.1}, Version: 1}}, TotalCount: 1,
	}, nil
}

func (m *mockVectorService) HybridSearch(_ context.Context, _ *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
	return &service.HybridSearchResponse{Results: []*service.VectorSearchResult{{ID: 1, Score: 0.95}}}, nil
}

func (m *mockVectorService) SearchByID(_ context.Context, _ *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
	return &service.SearchByIDResponse{Results: []*service.VectorSearchResult{{ID: 1, Score: 0.95}}}, nil
}

func (m *mockVectorService) RebuildIndex(_ context.Context, _ *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
	return &service.RebuildIndexResponse{JobID: "j1", Status: "running", Success: true}, nil
}

func (m *mockVectorService) GetRebuildProgress(_ context.Context, _ *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
	return &service.GetRebuildProgressResponse{JobID: "j1", Status: "running", PercentComplete: 50.0}, nil
}

func (m *mockVectorService) GetIndexStats(_ context.Context, _ *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
	return &service.GetIndexStatsResponse{
		Namespace:   "ns",
		IndexType:   "HNSW",
		VectorCount: 100,
		Dimension:   128,
		SearchPerformance: &service.IndexPerformanceMetrics{
			AverageSearchTimeMs: 1.5,
		},
	}, nil
}

func (m *mockVectorService) CompactNamespace(_ context.Context, _ *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
	return &service.CompactNamespaceResponse{OperationID: "op1", Success: true}, nil
}

func (m *mockVectorService) GetCompactionProgress(_ context.Context, _ *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
	return &service.GetCompactionProgressResponse{OperationID: "op1", InProgress: true, PercentComplete: 50}, nil
}

// recordingVectorService is a mockVectorService that keeps the service
// request each index-maintenance call receives and answers with every
// response field set, so a test checks both what the transport sent and
// what it decoded.
type recordingVectorService struct {
	mockVectorService
	rebuild *service.RebuildIndexRequest
	compact *service.CompactNamespaceRequest
	upsert  *service.BatchUpsertRequest
}

func (r *recordingVectorService) RebuildIndex(_ context.Context, req *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
	r.rebuild = req
	return &service.RebuildIndexResponse{
		JobID: "j1", Status: "running", VectorsProcessed: 3, TotalVectors: 6,
		PercentComplete: 50, EstimatedTimeRemaining: 7, Timestamp: 9, Success: true,
	}, nil
}

func (r *recordingVectorService) CompactNamespace(_ context.Context, req *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
	r.compact = req
	completed := int64(12)
	return &service.CompactNamespaceResponse{
		OperationID: "op1", StartedAt: 11, CompletedAt: &completed, BytesCompacted: 4096, Success: true,
	}, nil
}

func (r *recordingVectorService) BatchUpsert(_ context.Context, req *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
	r.upsert = req
	return &service.BatchUpsertResponse{
		IDs: []uint64{1, 2}, Versions: []uint64{3, 4}, Timestamp: 5, Success: true, InsertedCount: len(req.Vectors),
	}, nil
}

// metricVectorService is a mockVectorService whose GetNamespaceInfo reports
// a chosen distance metric.
type metricVectorService struct {
	mockVectorService
	metric string
}

func (m *metricVectorService) GetNamespaceInfo(_ context.Context, req *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
	return &service.GetNamespaceInfoResponse{Namespace: req.Namespace, Dimension: 2, DistanceMetric: m.metric}, nil
}

// TestEmbeddedVector_GetNamespaceInfo_DistanceMetric proves the embedded
// transport reports the SDK constant every other transport returns, and
// refuses a metric it does not know with the typed error rather than
// passing it on.
func TestEmbeddedVector_GetNamespaceInfo_DistanceMetric(t *testing.T) {
	ctx := context.Background()
	for reported, want := range map[string]transport.DistanceMetric{
		"MANHATTAN": transport.DistanceManhattan,
		"L2":        transport.DistanceEuclidean,
		"cosine":    transport.DistanceCosine,
	} {
		tr := newExtTransport(t, WithVectorService(&metricVectorService{metric: reported}))
		info, err := tr.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{Namespace: "ns"})
		if err != nil {
			t.Fatalf("GetNamespaceInfo with %q: %v", reported, err)
		}
		if info.DistanceMetric != want {
			t.Errorf("GetNamespaceInfo with %q: metric %q, want %q", reported, info.DistanceMetric, want)
		}
	}

	tr := newExtTransport(t, WithVectorService(&metricVectorService{metric: "CHEBYSHEV"}))
	info, err := tr.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{Namespace: "ns"})
	var unknown *transport.UnknownDistanceMetricError
	if !errors.As(err, &unknown) || unknown.Metric != "CHEBYSHEV" {
		t.Fatalf("GetNamespaceInfo = %+v, %v; want *UnknownDistanceMetricError{CHEBYSHEV}", info, err)
	}
}

// TestEmbeddedVector_SendsTheDocumentedRequest checks, for each vector
// operation called directly and through Request, the service request the
// transport built and the answer it decoded from the service's.
func TestEmbeddedVector_SendsTheDocumentedRequest(t *testing.T) {
	rebuild := &transport.RebuildIndexRequest{Namespace: "ns", IndexConfig: map[string]string{"m": "16"}, Async: true, ShardID: 2}
	rebuildSent := &service.RebuildIndexRequest{Namespace: "ns", IndexConfig: map[string]string{"m": "16"}, Async: true, ShardID: 2}
	rebuildWant := &transport.RebuildIndexResponse{
		JobID: "j1", Status: "running", VectorsProcessed: 3, TotalVectors: 6,
		PercentComplete: 50, EstimatedTimeRemaining: 7, Timestamp: 9, Success: true,
	}

	compact := &transport.CompactNamespaceRequest{Namespace: "ns", Async: true, TimeoutMs: 30, ShardID: 2}
	compactSent := &service.CompactNamespaceRequest{Namespace: "ns", Async: true, TimeoutMs: 30, ShardID: 2}
	completed := int64(12)
	compactWant := &transport.CompactNamespaceResponse{
		OperationID: "op1", StartedAt: 11, CompletedAt: &completed, BytesCompacted: 4096, Success: true,
	}

	upsert := &transport.BatchUpsertRequest{
		Namespace: "ns",
		Vectors:   []*transport.VectorData{{ID: 1, Vector: []float32{0.1}, Metadata: map[string]interface{}{"k": "v"}, Version: 1}},
		ShardID:   2,
	}
	upsertSent := &service.BatchUpsertRequest{
		Namespace: "ns",
		Vectors:   []*service.VectorData{{ID: 1, Vector: []float32{0.1}, Metadata: map[string]interface{}{"k": "v"}, Version: 1}},
		ShardID:   2,
	}
	upsertWant := &transport.BatchUpsertResponse{IDs: []uint64{1, 2}, Versions: []uint64{3, 4}, Timestamp: 5, Success: true, InsertedCount: 1}

	tests := []struct {
		name     string
		call     func(context.Context, *Transport) (any, error)
		sent     func(*recordingVectorService) any
		wantSent any
		want     any
	}{
		{
			name: "RebuildIndex",
			call: func(ctx context.Context, tr *Transport) (any, error) { return tr.RebuildIndex(ctx, rebuild) },
			sent: func(s *recordingVectorService) any { return s.rebuild }, wantSent: rebuildSent, want: rebuildWant,
		},
		{
			name: "Request/RebuildIndex",
			call: func(ctx context.Context, tr *Transport) (any, error) {
				var out *transport.RebuildIndexResponse
				err := tr.Request(ctx, "RebuildIndex", rebuild, &out)
				return out, err
			},
			sent: func(s *recordingVectorService) any { return s.rebuild }, wantSent: rebuildSent, want: rebuildWant,
		},
		{
			name: "CompactNamespace",
			call: func(ctx context.Context, tr *Transport) (any, error) { return tr.CompactNamespace(ctx, compact) },
			sent: func(s *recordingVectorService) any { return s.compact }, wantSent: compactSent, want: compactWant,
		},
		{
			name: "Request/CompactNamespace",
			call: func(ctx context.Context, tr *Transport) (any, error) {
				var out *transport.CompactNamespaceResponse
				err := tr.Request(ctx, "CompactNamespace", compact, &out)
				return out, err
			},
			sent: func(s *recordingVectorService) any { return s.compact }, wantSent: compactSent, want: compactWant,
		},
		{
			name: "BatchUpsert",
			call: func(ctx context.Context, tr *Transport) (any, error) { return tr.BatchUpsert(ctx, upsert) },
			sent: func(s *recordingVectorService) any { return s.upsert }, wantSent: upsertSent, want: upsertWant,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &recordingVectorService{}
			tr := newExtTransport(t, WithVectorService(svc))
			got, err := tc.call(context.Background(), tr)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if sent := tc.sent(svc); !reflect.DeepEqual(sent, tc.wantSent) {
				t.Errorf("service received %+v, want %+v", sent, tc.wantSent)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("answer %+v, want %+v", got, tc.want)
			}
		})
	}
}

// vectorCall is one vector operation called with an empty request.
type vectorCall struct {
	name string
	call func(context.Context, *Transport) (any, error)
}

func vectorCalls() []vectorCall {
	return []vectorCall{
		{"CreateNamespace", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.CreateNamespace(ctx, &transport.CreateNamespaceRequest{})
		}},
		{"UpdateNamespace", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.UpdateNamespace(ctx, &transport.UpdateNamespaceRequest{})
		}},
		{"DeleteNamespace", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.DeleteNamespace(ctx, &transport.DeleteNamespaceRequest{})
		}},
		{"ListNamespaces", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.ListNamespaces(ctx, &transport.ListNamespacesRequest{})
		}},
		{"GetNamespaceInfo", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{})
		}},
		{"UpsertVector", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.UpsertVector(ctx, &transport.UpsertVectorRequest{})
		}},
		{"GetVector", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.GetVector(ctx, &transport.GetVectorRequest{})
		}},
		{"DeleteVector", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.DeleteVector(ctx, &transport.DeleteVectorRequest{})
		}},
		{"BatchUpsert", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.BatchUpsert(ctx, &transport.BatchUpsertRequest{})
		}},
		{"BatchDeleteVectors", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.BatchDeleteVectors(ctx, &transport.BatchDeleteVectorsRequest{})
		}},
		{"ListVectors", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.ListVectors(ctx, &transport.ListVectorsRequest{})
		}},
		{"UpdateMetadata", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.UpdateMetadata(ctx, &transport.UpdateVectorMetadataRequest{})
		}},
		{"SearchSimilar", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.SearchSimilar(ctx, &transport.SearchSimilarRequest{})
		}},
		{"SearchByMetadata", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.SearchByMetadata(ctx, &transport.SearchByMetadataRequest{})
		}},
		{"HybridSearch", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.HybridSearch(ctx, &transport.HybridSearchRequest{})
		}},
		{"SearchByID", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.SearchByID(ctx, &transport.SearchByIDRequest{})
		}},
		{"RebuildIndex", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.RebuildIndex(ctx, &transport.RebuildIndexRequest{})
		}},
		{"GetRebuildProgress", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.GetRebuildProgress(ctx, &transport.GetRebuildProgressRequest{})
		}},
		{"GetIndexStats", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.GetIndexStats(ctx, &transport.GetIndexStatsRequest{})
		}},
		{"CompactNamespace", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.CompactNamespace(ctx, &transport.CompactNamespaceRequest{})
		}},
		{"GetCompactionProgress", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.GetCompactionProgress(ctx, &transport.GetCompactionProgressRequest{})
		}},
	}
}

// TestEmbeddedVector_NilService checks that every vector operation on a
// transport built without a vector service refuses with the typed error and
// no answer.
func TestEmbeddedVector_NilService(t *testing.T) {
	tr := newExtTransport(t)
	for _, tc := range vectorCalls() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.call(context.Background(), tr)
			var ne *NilServiceError
			if !errors.As(err, &ne) {
				t.Fatalf("%s error = %T %v, want NilServiceError", tc.name, err, err)
			}
			if !reflect.ValueOf(got).IsNil() {
				t.Errorf("%s answer = %+v, want none", tc.name, got)
			}
		})
	}
}

// TestEmbeddedVector_Closed checks that a closed transport refuses every
// vector operation with ClosedError before the service is called.
func TestEmbeddedVector_Closed(t *testing.T) {
	svc := &recordingVectorService{}
	tr := closedExtTransport(t, WithVectorService(svc))
	for _, tc := range vectorCalls() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.call(context.Background(), tr)
			var ce *transport.ClosedError
			if !errors.As(err, &ce) {
				t.Fatalf("%s error = %T %v, want ClosedError", tc.name, err, err)
			}
			if !reflect.ValueOf(got).IsNil() {
				t.Errorf("%s answer = %+v, want none", tc.name, got)
			}
		})
	}
	if svc.rebuild != nil || svc.compact != nil || svc.upsert != nil {
		t.Errorf("closed transport reached the service: %+v", svc)
	}
}
