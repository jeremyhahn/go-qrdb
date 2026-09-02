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

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mock_vector_service_test.go holds mockVectorService, the package's shared
// service.VectorServicer double.
//
// It carries NO build constraint on purpose. The double used to live in
// tools_vector_test.go, which is `!integration`, while tool_mapping_guard_test.go
// consumes it under `codec_json && raft` -- a constraint that does not imply
// `!integration`. Under BUILD_TAGS+integration the type vanished and the package
// stopped typechecking (#203, the shape #178 first hit). A test helper's
// constraint must be a SUPERSET of every caller's; the union here is
// unconstrained, so this file is too.

// The compile-time check belongs with the double: it is what turns "a method
// slipped behind a build constraint" into a failure HERE rather than at some
// construction site in another file (#203).
var _ service.VectorServicer = (*mockVectorService)(nil)

type mockVectorService struct {
	createNamespaceFn       func(ctx context.Context, req *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error)
	deleteNamespaceFn       func(ctx context.Context, req *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error)
	listNamespacesFn        func(ctx context.Context, req *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error)
	getNamespaceInfoFn      func(ctx context.Context, req *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error)
	upsertVectorFn          func(ctx context.Context, req *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error)
	getVectorFn             func(ctx context.Context, req *service.GetVectorRequest) (*service.GetVectorResponse, error)
	deleteVectorFn          func(ctx context.Context, req *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error)
	batchUpsertFn           func(ctx context.Context, req *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error)
	batchDeleteVectorsFn    func(ctx context.Context, req *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error)
	listVectorsFn           func(ctx context.Context, req *service.ListVectorsRequest) (*service.ListVectorsResponse, error)
	updateMetadataFn        func(ctx context.Context, req *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error)
	searchSimilarFn         func(ctx context.Context, req *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error)
	searchByMetadataFn      func(ctx context.Context, req *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error)
	hybridSearchFn          func(ctx context.Context, req *service.HybridSearchRequest) (*service.HybridSearchResponse, error)
	searchByIDFn            func(ctx context.Context, req *service.SearchByIDRequest) (*service.SearchByIDResponse, error)
	rebuildIndexFn          func(ctx context.Context, req *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error)
	getRebuildProgressFn    func(ctx context.Context, req *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error)
	getIndexStatsFn         func(ctx context.Context, req *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error)
	compactNamespaceFn      func(ctx context.Context, req *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error)
	getCompactionProgressFn func(ctx context.Context, req *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error)
}

func (m *mockVectorService) CreateNamespace(ctx context.Context, req *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
	if m.createNamespaceFn != nil {
		return m.createNamespaceFn(ctx, req)
	}
	return &service.CreateNamespaceResponse{Namespace: req.Namespace, Dimension: req.Dimension, Success: true, Timestamp: 1000}, nil
}

func (m *mockVectorService) DeleteNamespace(ctx context.Context, req *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
	if m.deleteNamespaceFn != nil {
		return m.deleteNamespaceFn(ctx, req)
	}
	return &service.DeleteNamespaceResponse{Success: true, VectorsDeleted: 5, Timestamp: 1000}, nil
}

func (m *mockVectorService) ListNamespaces(ctx context.Context, req *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
	if m.listNamespacesFn != nil {
		return m.listNamespacesFn(ctx, req)
	}
	return &service.ListNamespacesResponse{
		Namespaces: []*service.NamespaceInfo{{Namespace: "ns1", Dimension: 128}},
		TotalCount: 1,
		Timestamp:  1000,
	}, nil
}

func (m *mockVectorService) GetNamespaceInfo(ctx context.Context, req *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
	if m.getNamespaceInfoFn != nil {
		return m.getNamespaceInfoFn(ctx, req)
	}
	return &service.GetNamespaceInfoResponse{
		Namespace:      req.Namespace,
		Dimension:      128,
		VectorCount:    100,
		DistanceMetric: "COSINE",
		IndexType:      "HNSW",
	}, nil
}

func (m *mockVectorService) UpsertVector(ctx context.Context, req *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	if m.upsertVectorFn != nil {
		return m.upsertVectorFn(ctx, req)
	}
	return &service.UpsertVectorResponse{ID: req.ID, Version: 1, Timestamp: 1000, Success: true}, nil
}

func (m *mockVectorService) GetVector(ctx context.Context, req *service.GetVectorRequest) (*service.GetVectorResponse, error) {
	if m.getVectorFn != nil {
		return m.getVectorFn(ctx, req)
	}
	return &service.GetVectorResponse{
		ID:       req.ID,
		Vector:   []float32{1.0, 2.0, 3.0},
		Metadata: map[string]interface{}{"key": "value"},
		Version:  1,
		Found:    true,
	}, nil
}

func (m *mockVectorService) DeleteVector(ctx context.Context, req *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
	if m.deleteVectorFn != nil {
		return m.deleteVectorFn(ctx, req)
	}
	return &service.DeleteVectorResponse{Success: true, Timestamp: 1000}, nil
}

func (m *mockVectorService) BatchUpsert(ctx context.Context, req *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
	if m.batchUpsertFn != nil {
		return m.batchUpsertFn(ctx, req)
	}
	ids := make([]uint64, len(req.Vectors))
	versions := make([]uint64, len(req.Vectors))
	for i, v := range req.Vectors {
		ids[i] = v.ID
		versions[i] = 1
	}
	return &service.BatchUpsertResponse{IDs: ids, Versions: versions, InsertedCount: len(req.Vectors), Success: true, Timestamp: 1000}, nil
}

func (m *mockVectorService) BatchDeleteVectors(ctx context.Context, req *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
	if m.batchDeleteVectorsFn != nil {
		return m.batchDeleteVectorsFn(ctx, req)
	}
	return &service.BatchDeleteVectorsResponse{DeletedCount: len(req.IDs), Success: true, Timestamp: 1000}, nil
}

func (m *mockVectorService) ListVectors(ctx context.Context, req *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
	if m.listVectorsFn != nil {
		return m.listVectorsFn(ctx, req)
	}
	return &service.ListVectorsResponse{
		Vectors:    []*service.VectorData{{ID: 1, Vector: []float32{1, 2, 3}}},
		TotalCount: 1,
		Timestamp:  1000,
	}, nil
}

func (m *mockVectorService) UpdateMetadata(ctx context.Context, req *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
	if m.updateMetadataFn != nil {
		return m.updateMetadataFn(ctx, req)
	}
	return &service.UpdateVectorMetadataResponse{ID: req.ID, Version: 2, Timestamp: 1000, Success: true}, nil
}

func (m *mockVectorService) SearchSimilar(ctx context.Context, req *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
	if m.searchSimilarFn != nil {
		return m.searchSimilarFn(ctx, req)
	}
	return &service.SearchSimilarResponse{
		Results:   []*service.VectorSearchResult{{ID: 1, Score: 0.95, Distance: 0.05, Rank: 1}},
		Timestamp: 1000,
	}, nil
}

func (m *mockVectorService) SearchByMetadata(ctx context.Context, req *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
	if m.searchByMetadataFn != nil {
		return m.searchByMetadataFn(ctx, req)
	}
	return &service.SearchByMetadataResponse{
		Results:    []*service.VectorData{{ID: 1}},
		TotalCount: 1,
		Timestamp:  1000,
	}, nil
}

func (m *mockVectorService) HybridSearch(ctx context.Context, req *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
	if m.hybridSearchFn != nil {
		return m.hybridSearchFn(ctx, req)
	}
	return &service.HybridSearchResponse{
		Results:         []*service.VectorSearchResult{{ID: 1, Score: 0.9}},
		TotalCandidates: 10,
		TotalFiltered:   1,
		Timestamp:       1000,
	}, nil
}

func (m *mockVectorService) SearchByID(ctx context.Context, req *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
	if m.searchByIDFn != nil {
		return m.searchByIDFn(ctx, req)
	}
	return &service.SearchByIDResponse{
		SourceID:  req.ID,
		Results:   []*service.VectorSearchResult{{ID: 2, Score: 0.9}},
		Timestamp: 1000,
	}, nil
}

func (m *mockVectorService) RebuildIndex(ctx context.Context, req *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
	if m.rebuildIndexFn != nil {
		return m.rebuildIndexFn(ctx, req)
	}
	return &service.RebuildIndexResponse{JobID: "job-1", Status: "started", Success: true, Timestamp: 1000}, nil
}

func (m *mockVectorService) GetRebuildProgress(ctx context.Context, req *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
	if m.getRebuildProgressFn != nil {
		return m.getRebuildProgressFn(ctx, req)
	}
	return &service.GetRebuildProgressResponse{
		JobID:           "job-1",
		Namespace:       req.Namespace,
		Status:          "running",
		PercentComplete: 50.0,
	}, nil
}

func (m *mockVectorService) GetIndexStats(ctx context.Context, req *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
	if m.getIndexStatsFn != nil {
		return m.getIndexStatsFn(ctx, req)
	}
	return &service.GetIndexStatsResponse{
		Namespace:   req.Namespace,
		IndexType:   "HNSW",
		VectorCount: 100,
		Dimension:   128,
		Timestamp:   1000,
	}, nil
}

func (m *mockVectorService) CompactNamespace(ctx context.Context, req *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
	if m.compactNamespaceFn != nil {
		return m.compactNamespaceFn(ctx, req)
	}
	return &service.CompactNamespaceResponse{OperationID: "op-1", StartedAt: 1000, Success: true}, nil
}

func (m *mockVectorService) GetCompactionProgress(ctx context.Context, req *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
	if m.getCompactionProgressFn != nil {
		return m.getCompactionProgressFn(ctx, req)
	}
	return &service.GetCompactionProgressResponse{
		OperationID:     "op-1",
		Namespace:       req.Namespace,
		PercentComplete: 75.0,
		InProgress:      true,
		StartedAt:       1000,
	}, nil
}
