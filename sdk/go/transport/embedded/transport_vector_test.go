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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

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

func TestEmbeddedVector_Dispatch(t *testing.T) {
	tr := newExtTransport(t, WithVectorService(&mockVectorService{}), WithBatchService(&fullBatchExecSvc{}))
	ctx := context.Background()

	request := func(t *testing.T, verb string, req, out any) {
		t.Helper()
		if err := tr.Request(ctx, verb, req, out); err != nil {
			t.Fatalf("%s dispatch: %v", verb, err)
		}
	}

	t.Run("CreateNamespace", func(t *testing.T) {
		var resp *transport.CreateNamespaceResponse
		request(t, "CreateNamespace", &transport.CreateNamespaceRequest{Namespace: "ns", Dimension: 4}, &resp)
		if resp.Namespace != "ns" || resp.Dimension != 4 || !resp.Success {
			t.Errorf("CreateNamespace = %+v, want ns/4/success", resp)
		}
	})
	t.Run("DeleteNamespace", func(t *testing.T) {
		var resp *transport.DeleteNamespaceResponse
		request(t, "DeleteNamespace", &transport.DeleteNamespaceRequest{}, &resp)
		if resp.VectorsDeleted != 5 {
			t.Errorf("VectorsDeleted = %d, want 5", resp.VectorsDeleted)
		}
	})
	t.Run("ListNamespaces", func(t *testing.T) {
		var resp *transport.ListNamespacesResponse
		request(t, "ListNamespaces", &transport.ListNamespacesRequest{}, &resp)
		if len(resp.Namespaces) != 1 || resp.Namespaces[0].Namespace != "ns" || resp.Namespaces[0].Dimension != 128 {
			t.Errorf("Namespaces = %+v, want one ns/128 entry", resp.Namespaces)
		}
	})
	t.Run("GetNamespaceInfo", func(t *testing.T) {
		var resp *transport.GetNamespaceInfoResponse
		request(t, "GetNamespaceInfo", &transport.GetNamespaceInfoRequest{Namespace: "ns"}, &resp)
		if resp.Namespace != "ns" || resp.Dimension != 128 || resp.VectorCount != 100 {
			t.Errorf("GetNamespaceInfo = %+v, want ns/128/100", resp)
		}
	})
	t.Run("UpsertVector", func(t *testing.T) {
		var resp *transport.UpsertVectorResponse
		request(t, "UpsertVector", &transport.UpsertVectorRequest{ID: 7, Vector: []float32{0.1}}, &resp)
		if resp.ID != 7 || resp.Version != 1 {
			t.Errorf("UpsertVector = %+v, want ID 7 version 1", resp)
		}
	})
	t.Run("GetVector", func(t *testing.T) {
		var resp *transport.GetVectorResponse
		request(t, "GetVector", &transport.GetVectorRequest{ID: 7}, &resp)
		if !resp.Found || resp.ID != 7 || len(resp.Vector) != 1 || resp.Metadata["k"] != "v" {
			t.Errorf("GetVector = %+v, want found ID 7 with vector and metadata", resp)
		}
	})
	t.Run("DeleteVector", func(t *testing.T) {
		var resp *transport.DeleteVectorResponse
		request(t, "DeleteVector", &transport.DeleteVectorRequest{ID: 1}, &resp)
		if !resp.Success {
			t.Errorf("DeleteVector = %+v, want success", resp)
		}
	})
	t.Run("BatchUpsert", func(t *testing.T) {
		var resp *transport.BatchUpsertResponse
		req := &transport.BatchUpsertRequest{Vectors: []*transport.VectorData{{ID: 1, Vector: []float32{0.1}}, {ID: 2, Vector: []float32{0.2}}}}
		request(t, "BatchUpsert", req, &resp)
		if resp.InsertedCount != 2 {
			t.Errorf("InsertedCount = %d, want 2", resp.InsertedCount)
		}
	})
	t.Run("BatchDeleteVectors", func(t *testing.T) {
		var resp *transport.BatchDeleteVectorsResponse
		request(t, "BatchDeleteVectors", &transport.BatchDeleteVectorsRequest{IDs: []uint64{1, 2, 3}}, &resp)
		if resp.DeletedCount != 3 {
			t.Errorf("DeletedCount = %d, want 3", resp.DeletedCount)
		}
	})
	t.Run("ListVectors", func(t *testing.T) {
		var resp *transport.ListVectorsResponse
		request(t, "ListVectors", &transport.ListVectorsRequest{}, &resp)
		if len(resp.Vectors) != 1 || resp.Vectors[0].ID != 1 {
			t.Errorf("Vectors = %+v, want one vector ID 1", resp.Vectors)
		}
	})
	t.Run("UpdateMetadata", func(t *testing.T) {
		var resp *transport.UpdateVectorMetadataResponse
		request(t, "UpdateMetadata", &transport.UpdateVectorMetadataRequest{ID: 7}, &resp)
		if resp.ID != 7 || resp.Version != 2 {
			t.Errorf("UpdateMetadata = %+v, want ID 7 version 2", resp)
		}
	})
	t.Run("SearchSimilar", func(t *testing.T) {
		var resp *transport.SearchSimilarResponse
		request(t, "SearchSimilar", &transport.SearchSimilarRequest{K: 5}, &resp)
		if len(resp.Results) != 1 || resp.Results[0].ID != 1 || resp.Results[0].Score != 0.95 {
			t.Errorf("Results = %+v, want one result ID 1 score 0.95", resp.Results)
		}
	})
	t.Run("SearchByMetadata", func(t *testing.T) {
		var resp *transport.SearchByMetadataResponse
		request(t, "SearchByMetadata", &transport.SearchByMetadataRequest{}, &resp)
		if len(resp.Results) != 1 || resp.Results[0].ID != 1 {
			t.Errorf("Results = %+v, want one result ID 1", resp.Results)
		}
	})
	t.Run("HybridSearch", func(t *testing.T) {
		var resp *transport.HybridSearchResponse
		request(t, "HybridSearch", &transport.HybridSearchRequest{}, &resp)
		if len(resp.Results) != 1 || resp.Results[0].ID != 1 {
			t.Errorf("Results = %+v, want one result ID 1", resp.Results)
		}
	})
	t.Run("SearchByID", func(t *testing.T) {
		var resp *transport.SearchByIDResponse
		request(t, "SearchByID", &transport.SearchByIDRequest{ID: 1, K: 5}, &resp)
		if len(resp.Results) != 1 || resp.Results[0].ID != 1 {
			t.Errorf("Results = %+v, want one result ID 1", resp.Results)
		}
	})
	t.Run("RebuildIndex", func(t *testing.T) {
		var resp *transport.RebuildIndexResponse
		request(t, "RebuildIndex", &transport.RebuildIndexRequest{}, &resp)
		if resp.JobID != "j1" {
			t.Errorf("JobID = %q, want j1", resp.JobID)
		}
	})
	t.Run("GetRebuildProgress", func(t *testing.T) {
		var resp *transport.GetRebuildProgressResponse
		request(t, "GetRebuildProgress", &transport.GetRebuildProgressRequest{}, &resp)
		if resp.JobID != "j1" || resp.PercentComplete != 50 {
			t.Errorf("GetRebuildProgress = %+v, want j1 at 50%%", resp)
		}
	})
	t.Run("GetIndexStats", func(t *testing.T) {
		var resp *transport.GetIndexStatsResponse
		request(t, "GetIndexStats", &transport.GetIndexStatsRequest{}, &resp)
		if resp.IndexType != transport.IndexHNSW {
			t.Errorf("IndexType = %v, want %v", resp.IndexType, transport.IndexHNSW)
		}
	})
	t.Run("CompactNamespace", func(t *testing.T) {
		var resp *transport.CompactNamespaceResponse
		request(t, "CompactNamespace", &transport.CompactNamespaceRequest{}, &resp)
		if resp.OperationID != "op1" {
			t.Errorf("OperationID = %q, want op1", resp.OperationID)
		}
	})
	t.Run("GetCompactionProgress", func(t *testing.T) {
		var resp *transport.GetCompactionProgressResponse
		request(t, "GetCompactionProgress", &transport.GetCompactionProgressRequest{}, &resp)
		if !resp.InProgress || resp.PercentComplete != 50 {
			t.Errorf("GetCompactionProgress = %+v, want in progress at 50%%", resp)
		}
	})
	t.Run("ExecuteBatch", func(t *testing.T) {
		var resp *transport.BatchResponse
		request(t, "ExecuteBatch", &transport.BatchRequest{Ops: []transport.Op{{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v")}}}, &resp)
		if resp == nil {
			t.Fatal("ExecuteBatch returned a nil BatchResponse")
		}
	})
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
