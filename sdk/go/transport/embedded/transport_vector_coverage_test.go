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

// ===========================================================================
// Vector success tests
// ===========================================================================

func TestEmbeddedVector_AllMethods_Success(t *testing.T) {
	tr := newExtTransport(t, WithVectorService(&mockVectorService{}))
	ctx := context.Background()

	t.Run("CreateNamespace", func(t *testing.T) {
		if _, err := tr.CreateNamespace(ctx, &transport.CreateNamespaceRequest{Namespace: "ns", Dimension: 128}); err != nil {
			t.Fatalf("CreateNamespace: %v", err)
		}
	})
	t.Run("DeleteNamespace", func(t *testing.T) {
		if _, err := tr.DeleteNamespace(ctx, &transport.DeleteNamespaceRequest{Namespace: "ns"}); err != nil {
			t.Fatalf("DeleteNamespace: %v", err)
		}
	})
	t.Run("ListNamespaces", func(t *testing.T) {
		if _, err := tr.ListNamespaces(ctx, &transport.ListNamespacesRequest{}); err != nil {
			t.Fatalf("ListNamespaces: %v", err)
		}
	})
	t.Run("GetNamespaceInfo", func(t *testing.T) {
		if _, err := tr.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{Namespace: "ns"}); err != nil {
			t.Fatalf("GetNamespaceInfo: %v", err)
		}
	})
	t.Run("UpsertVector", func(t *testing.T) {
		if _, err := tr.UpsertVector(ctx, &transport.UpsertVectorRequest{Namespace: "ns", ID: 1, Vector: []float32{0.1}}); err != nil {
			t.Fatalf("UpsertVector: %v", err)
		}
	})
	t.Run("GetVector", func(t *testing.T) {
		if _, err := tr.GetVector(ctx, &transport.GetVectorRequest{Namespace: "ns", ID: 1}); err != nil {
			t.Fatalf("GetVector: %v", err)
		}
	})
	t.Run("DeleteVector", func(t *testing.T) {
		if _, err := tr.DeleteVector(ctx, &transport.DeleteVectorRequest{Namespace: "ns", ID: 1}); err != nil {
			t.Fatalf("DeleteVector: %v", err)
		}
	})
	t.Run("BatchUpsert", func(t *testing.T) {
		if _, err := tr.BatchUpsert(ctx, &transport.BatchUpsertRequest{
			Namespace: "ns",
			Vectors:   []*transport.VectorData{{ID: 1, Vector: []float32{0.1}, Metadata: map[string]interface{}{"k": "v"}, Version: 1}},
		}); err != nil {
			t.Fatalf("BatchUpsert: %v", err)
		}
	})
	t.Run("BatchDeleteVectors", func(t *testing.T) {
		if _, err := tr.BatchDeleteVectors(ctx, &transport.BatchDeleteVectorsRequest{Namespace: "ns", IDs: []uint64{1}}); err != nil {
			t.Fatalf("BatchDeleteVectors: %v", err)
		}
	})
	t.Run("ListVectors", func(t *testing.T) {
		if _, err := tr.ListVectors(ctx, &transport.ListVectorsRequest{Namespace: "ns"}); err != nil {
			t.Fatalf("ListVectors: %v", err)
		}
	})
	t.Run("UpdateMetadata", func(t *testing.T) {
		if _, err := tr.UpdateMetadata(ctx, &transport.UpdateVectorMetadataRequest{Namespace: "ns", ID: 1}); err != nil {
			t.Fatalf("UpdateMetadata: %v", err)
		}
	})
	t.Run("SearchSimilar", func(t *testing.T) {
		if _, err := tr.SearchSimilar(ctx, &transport.SearchSimilarRequest{Namespace: "ns", K: 5}); err != nil {
			t.Fatalf("SearchSimilar: %v", err)
		}
	})
	t.Run("SearchByMetadata", func(t *testing.T) {
		if _, err := tr.SearchByMetadata(ctx, &transport.SearchByMetadataRequest{Namespace: "ns"}); err != nil {
			t.Fatalf("SearchByMetadata: %v", err)
		}
	})
	t.Run("HybridSearch", func(t *testing.T) {
		if _, err := tr.HybridSearch(ctx, &transport.HybridSearchRequest{Namespace: "ns"}); err != nil {
			t.Fatalf("HybridSearch: %v", err)
		}
	})
	t.Run("SearchByID", func(t *testing.T) {
		if _, err := tr.SearchByID(ctx, &transport.SearchByIDRequest{Namespace: "ns", ID: 1, K: 5}); err != nil {
			t.Fatalf("SearchByID: %v", err)
		}
	})
	t.Run("RebuildIndex", func(t *testing.T) {
		if _, err := tr.RebuildIndex(ctx, &transport.RebuildIndexRequest{Namespace: "ns"}); err != nil {
			t.Fatalf("RebuildIndex: %v", err)
		}
	})
	t.Run("GetRebuildProgress", func(t *testing.T) {
		if _, err := tr.GetRebuildProgress(ctx, &transport.GetRebuildProgressRequest{}); err != nil {
			t.Fatalf("GetRebuildProgress: %v", err)
		}
	})
	t.Run("GetIndexStats", func(t *testing.T) {
		if _, err := tr.GetIndexStats(ctx, &transport.GetIndexStatsRequest{Namespace: "ns"}); err != nil {
			t.Fatalf("GetIndexStats: %v", err)
		}
	})
	t.Run("CompactNamespace", func(t *testing.T) {
		if _, err := tr.CompactNamespace(ctx, &transport.CompactNamespaceRequest{Namespace: "ns"}); err != nil {
			t.Fatalf("CompactNamespace: %v", err)
		}
	})
	t.Run("GetCompactionProgress", func(t *testing.T) {
		if _, err := tr.GetCompactionProgress(ctx, &transport.GetCompactionProgressRequest{Namespace: "ns"}); err != nil {
			t.Fatalf("GetCompactionProgress: %v", err)
		}
	})
}

// ===========================================================================
// Vector dispatch handler success paths
// ===========================================================================

func TestEmbeddedVector_Dispatch(t *testing.T) {
	tr := newExtTransport(t, WithVectorService(&mockVectorService{}), WithBatchService(&fullBatchExecSvc{}))
	ctx := context.Background()

	t.Run("CreateNamespace", func(t *testing.T) {
		var resp *transport.CreateNamespaceResponse
		if err := tr.Request(ctx, "CreateNamespace", &transport.CreateNamespaceRequest{Namespace: "ns"}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("DeleteNamespace", func(t *testing.T) {
		var resp *transport.DeleteNamespaceResponse
		if err := tr.Request(ctx, "DeleteNamespace", &transport.DeleteNamespaceRequest{}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("ListNamespaces", func(t *testing.T) {
		var resp *transport.ListNamespacesResponse
		if err := tr.Request(ctx, "ListNamespaces", &transport.ListNamespacesRequest{}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("GetNamespaceInfo", func(t *testing.T) {
		var resp *transport.GetNamespaceInfoResponse
		if err := tr.Request(ctx, "GetNamespaceInfo", &transport.GetNamespaceInfoRequest{Namespace: "ns"}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("UpsertVector", func(t *testing.T) {
		var resp *transport.UpsertVectorResponse
		if err := tr.Request(ctx, "UpsertVector", &transport.UpsertVectorRequest{ID: 1, Vector: []float32{0.1}}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("GetVector", func(t *testing.T) {
		var resp *transport.GetVectorResponse
		if err := tr.Request(ctx, "GetVector", &transport.GetVectorRequest{ID: 1}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("DeleteVector", func(t *testing.T) {
		var resp *transport.DeleteVectorResponse
		if err := tr.Request(ctx, "DeleteVector", &transport.DeleteVectorRequest{ID: 1}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("BatchUpsert", func(t *testing.T) {
		var resp *transport.BatchUpsertResponse
		if err := tr.Request(ctx, "BatchUpsert", &transport.BatchUpsertRequest{}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("BatchDeleteVectors", func(t *testing.T) {
		var resp *transport.BatchDeleteVectorsResponse
		if err := tr.Request(ctx, "BatchDeleteVectors", &transport.BatchDeleteVectorsRequest{}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("ListVectors", func(t *testing.T) {
		var resp *transport.ListVectorsResponse
		if err := tr.Request(ctx, "ListVectors", &transport.ListVectorsRequest{}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("UpdateMetadata", func(t *testing.T) {
		var resp *transport.UpdateVectorMetadataResponse
		if err := tr.Request(ctx, "UpdateMetadata", &transport.UpdateVectorMetadataRequest{ID: 1}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("SearchSimilar", func(t *testing.T) {
		var resp *transport.SearchSimilarResponse
		if err := tr.Request(ctx, "SearchSimilar", &transport.SearchSimilarRequest{K: 5}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("SearchByMetadata", func(t *testing.T) {
		var resp *transport.SearchByMetadataResponse
		if err := tr.Request(ctx, "SearchByMetadata", &transport.SearchByMetadataRequest{}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("HybridSearch", func(t *testing.T) {
		var resp *transport.HybridSearchResponse
		if err := tr.Request(ctx, "HybridSearch", &transport.HybridSearchRequest{}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("SearchByID", func(t *testing.T) {
		var resp *transport.SearchByIDResponse
		if err := tr.Request(ctx, "SearchByID", &transport.SearchByIDRequest{ID: 1, K: 5}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("RebuildIndex", func(t *testing.T) {
		var resp *transport.RebuildIndexResponse
		if err := tr.Request(ctx, "RebuildIndex", &transport.RebuildIndexRequest{}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("GetRebuildProgress", func(t *testing.T) {
		var resp *transport.GetRebuildProgressResponse
		if err := tr.Request(ctx, "GetRebuildProgress", &transport.GetRebuildProgressRequest{}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("GetIndexStats", func(t *testing.T) {
		var resp *transport.GetIndexStatsResponse
		if err := tr.Request(ctx, "GetIndexStats", &transport.GetIndexStatsRequest{}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("CompactNamespace", func(t *testing.T) {
		var resp *transport.CompactNamespaceResponse
		if err := tr.Request(ctx, "CompactNamespace", &transport.CompactNamespaceRequest{}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("GetCompactionProgress", func(t *testing.T) {
		var resp *transport.GetCompactionProgressResponse
		if err := tr.Request(ctx, "GetCompactionProgress", &transport.GetCompactionProgressRequest{}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("ExecuteBatch", func(t *testing.T) {
		var resp *transport.BatchResponse
		if err := tr.Request(ctx, "ExecuteBatch", &transport.BatchRequest{Ops: []transport.Op{{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v")}}}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
}
