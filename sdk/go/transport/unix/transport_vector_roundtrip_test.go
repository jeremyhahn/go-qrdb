// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Round-trip tests for the Vector service over the Unix domain socket
// transport. Each test wires a real gRPC-over-UDS server with a stub vector
// service and asserts the SDK transport correctly marshals + unmarshals
// across the wire.

package unix

import (
	"context"
	"errors"
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockVectorSvc — service.VectorServicer stub
// ---------------------------------------------------------------------------

type mockVectorSvc struct {
	createNSFn      func(ctx context.Context, req *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error)
	updateNSFn      func(ctx context.Context, req *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error)
	deleteNSFn      func(ctx context.Context, req *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error)
	listNSFn        func(ctx context.Context, req *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error)
	getNSInfoFn     func(ctx context.Context, req *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error)
	upsertFn        func(ctx context.Context, req *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error)
	getVectorFn     func(ctx context.Context, req *service.GetVectorRequest) (*service.GetVectorResponse, error)
	deleteVectorFn  func(ctx context.Context, req *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error)
	batchUpsertFn   func(ctx context.Context, req *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error)
	batchDeleteFn   func(ctx context.Context, req *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error)
	listVectorsFn   func(ctx context.Context, req *service.ListVectorsRequest) (*service.ListVectorsResponse, error)
	updateMetaFn    func(ctx context.Context, req *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error)
	searchSimilarFn func(ctx context.Context, req *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error)
	searchByMetaFn  func(ctx context.Context, req *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error)
	hybridSearchFn  func(ctx context.Context, req *service.HybridSearchRequest) (*service.HybridSearchResponse, error)
	searchByIDFn    func(ctx context.Context, req *service.SearchByIDRequest) (*service.SearchByIDResponse, error)
	rebuildIndexFn  func(ctx context.Context, req *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error)
	rebuildProgFn   func(ctx context.Context, req *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error)
	indexStatsFn    func(ctx context.Context, req *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error)
	compactFn       func(ctx context.Context, req *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error)
	compactProgFn   func(ctx context.Context, req *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error)
}

func (m *mockVectorSvc) CreateNamespace(ctx context.Context, req *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
	if m.createNSFn != nil {
		return m.createNSFn(ctx, req)
	}
	return &service.CreateNamespaceResponse{
		Namespace: req.Namespace,
		Dimension: req.Dimension,
		Success:   true,
		Timestamp: 1_700_000_000,
	}, nil
}

func (m *mockVectorSvc) UpdateNamespace(ctx context.Context, req *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error) {
	if m.updateNSFn != nil {
		return m.updateNSFn(ctx, req)
	}
	return &service.UpdateNamespaceResponse{
		Namespace:       req.Namespace,
		RequiresRebuild: len(req.IndexConfig) > 0,
		Success:         true,
		Timestamp:       1_700_000_000,
	}, nil
}

func (m *mockVectorSvc) DeleteNamespace(ctx context.Context, req *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
	if m.deleteNSFn != nil {
		return m.deleteNSFn(ctx, req)
	}
	return &service.DeleteNamespaceResponse{Success: true}, nil
}

func (m *mockVectorSvc) ListNamespaces(ctx context.Context, req *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
	if m.listNSFn != nil {
		return m.listNSFn(ctx, req)
	}
	return &service.ListNamespacesResponse{
		Namespaces: []*service.NamespaceInfo{
			{Namespace: "ns1"},
			{Namespace: "ns2"},
		},
		TotalCount: 2,
	}, nil
}

func (m *mockVectorSvc) GetNamespaceInfo(ctx context.Context, req *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
	if m.getNSInfoFn != nil {
		return m.getNSInfoFn(ctx, req)
	}
	return &service.GetNamespaceInfoResponse{
		Namespace:      req.Namespace,
		Dimension:      128,
		VectorCount:    1000,
		DistanceMetric: transport.DistanceCosine,
		IndexType:      transport.IndexHNSW,
		CreatedAt:      1_700_000_000,
		UpdatedAt:      1_700_000_100,
	}, nil
}

func (m *mockVectorSvc) UpsertVector(ctx context.Context, req *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	if m.upsertFn != nil {
		return m.upsertFn(ctx, req)
	}
	return &service.UpsertVectorResponse{ID: req.ID, Version: 1, Success: true}, nil
}

func (m *mockVectorSvc) GetVector(ctx context.Context, req *service.GetVectorRequest) (*service.GetVectorResponse, error) {
	if m.getVectorFn != nil {
		return m.getVectorFn(ctx, req)
	}
	return &service.GetVectorResponse{
		ID:      req.ID,
		Vector:  []float32{0.1, 0.2, 0.3},
		Version: 1,
		Found:   true,
	}, nil
}

func (m *mockVectorSvc) DeleteVector(ctx context.Context, req *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
	if m.deleteVectorFn != nil {
		return m.deleteVectorFn(ctx, req)
	}
	return &service.DeleteVectorResponse{Success: true}, nil
}

func (m *mockVectorSvc) BatchUpsert(ctx context.Context, req *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
	if m.batchUpsertFn != nil {
		return m.batchUpsertFn(ctx, req)
	}
	return &service.BatchUpsertResponse{
		InsertedCount: len(req.Vectors),
		Success:       true,
	}, nil
}

func (m *mockVectorSvc) BatchDeleteVectors(ctx context.Context, req *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
	if m.batchDeleteFn != nil {
		return m.batchDeleteFn(ctx, req)
	}
	return &service.BatchDeleteVectorsResponse{
		DeletedCount: len(req.IDs),
		Success:      true,
	}, nil
}

func (m *mockVectorSvc) ListVectors(ctx context.Context, req *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
	if m.listVectorsFn != nil {
		return m.listVectorsFn(ctx, req)
	}
	return &service.ListVectorsResponse{
		Vectors: []*service.VectorData{
			{ID: 1, Vector: []float32{0.1, 0.2}, Version: 1},
			{ID: 2, Vector: []float32{0.3, 0.4}, Version: 1},
		},
		TotalCount: 2,
	}, nil
}

func (m *mockVectorSvc) UpdateMetadata(ctx context.Context, req *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
	if m.updateMetaFn != nil {
		return m.updateMetaFn(ctx, req)
	}
	return &service.UpdateVectorMetadataResponse{ID: req.ID, Version: 2, Success: true}, nil
}

func (m *mockVectorSvc) SearchSimilar(ctx context.Context, req *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
	if m.searchSimilarFn != nil {
		return m.searchSimilarFn(ctx, req)
	}
	return &service.SearchSimilarResponse{
		Results: []*service.VectorSearchResult{
			{ID: 1, Score: 0.95, Vector: req.QueryVector},
			{ID: 2, Score: 0.80},
		},
		QueryDuration: 1500,
	}, nil
}

func (m *mockVectorSvc) SearchByMetadata(ctx context.Context, req *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
	if m.searchByMetaFn != nil {
		return m.searchByMetaFn(ctx, req)
	}
	return &service.SearchByMetadataResponse{
		Results: []*service.VectorData{
			{ID: 1, Vector: []float32{0.1}},
		},
		TotalCount: 1,
	}, nil
}

func (m *mockVectorSvc) HybridSearch(ctx context.Context, req *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
	if m.hybridSearchFn != nil {
		return m.hybridSearchFn(ctx, req)
	}
	return &service.HybridSearchResponse{
		Results: []*service.VectorSearchResult{
			{ID: 1, Score: 0.9},
		},
		QueryDuration: 2000,
	}, nil
}

func (m *mockVectorSvc) SearchByID(ctx context.Context, req *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
	if m.searchByIDFn != nil {
		return m.searchByIDFn(ctx, req)
	}
	return &service.SearchByIDResponse{
		SourceID: req.ID,
		Results: []*service.VectorSearchResult{
			{ID: 2, Score: 0.95},
		},
		QueryDuration: 1200,
	}, nil
}

func (m *mockVectorSvc) RebuildIndex(ctx context.Context, req *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
	if m.rebuildIndexFn != nil {
		return m.rebuildIndexFn(ctx, req)
	}
	return &service.RebuildIndexResponse{JobID: "job-1", Status: "running", Success: true}, nil
}

func (m *mockVectorSvc) GetRebuildProgress(ctx context.Context, req *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
	if m.rebuildProgFn != nil {
		return m.rebuildProgFn(ctx, req)
	}
	return &service.GetRebuildProgressResponse{
		JobID:           req.JobID,
		Status:          "running",
		PercentComplete: 50.0,
	}, nil
}

func (m *mockVectorSvc) GetIndexStats(ctx context.Context, req *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
	if m.indexStatsFn != nil {
		return m.indexStatsFn(ctx, req)
	}
	return &service.GetIndexStatsResponse{
		IndexType:        transport.IndexHNSW,
		DistanceMetric:   "L2",
		VectorCount:      100,
		Dimension:        128,
		IndexSizeBytes:   1024,
		MemoryUsageBytes: 2048,
	}, nil
}

func (m *mockVectorSvc) CompactNamespace(ctx context.Context, req *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
	if m.compactFn != nil {
		return m.compactFn(ctx, req)
	}
	return &service.CompactNamespaceResponse{OperationID: "compact-1", Success: true}, nil
}

func (m *mockVectorSvc) GetCompactionProgress(ctx context.Context, req *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
	if m.compactProgFn != nil {
		return m.compactProgFn(ctx, req)
	}
	return &service.GetCompactionProgressResponse{
		OperationID:     req.OperationID,
		InProgress:      true,
		PercentComplete: 25.0,
	}, nil
}

var _ service.VectorServicer = (*mockVectorSvc)(nil)

// ---------------------------------------------------------------------------
// Namespace round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_CreateNamespace(t *testing.T) {
	svc := &mockVectorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.CreateNamespace(context.Background(), &transport.CreateNamespaceRequest{
		Namespace:      "vectors",
		Dimension:      128,
		DistanceMetric: transport.DistanceCosine,
		IndexType:      transport.IndexHNSW,
	})
	if err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if resp.Namespace != "vectors" || resp.Dimension != 128 {
		t.Errorf("got %+v", resp)
	}
}

func TestUnixRT_UpdateNamespace(t *testing.T) {
	var got *service.UpdateNamespaceRequest
	svc := &mockVectorSvc{
		updateNSFn: func(_ context.Context, req *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error) {
			got = req
			return &service.UpdateNamespaceResponse{Namespace: req.Namespace, RequiresRebuild: true, Success: true, Timestamp: 7}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.UpdateNamespace(context.Background(), &transport.UpdateNamespaceRequest{
		Namespace:   "vectors",
		IndexConfig: map[string]string{"ef_search": "128"},
		Metadata:    map[string]interface{}{"owner": "search"},
	})
	if err != nil {
		t.Fatalf("UpdateNamespace: %v", err)
	}
	if !resp.Success || !resp.RequiresRebuild || resp.Namespace != "vectors" || resp.Timestamp != 7 {
		t.Errorf("got %+v", resp)
	}
	if got.IndexConfig["ef_search"] != "128" || got.Metadata["owner"] != "search" {
		t.Errorf("service saw %+v", got)
	}
}

func TestUnixRT_UpdateNamespace_TypedRefusals(t *testing.T) {
	refusal := error(&service.NamespaceNotFoundError{Namespace: "missing"})
	svc := &mockVectorSvc{
		updateNSFn: func(context.Context, *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error) {
			return nil, refusal
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	_, err := tr.UpdateNamespace(context.Background(), &transport.UpdateNamespaceRequest{Namespace: "missing"})
	if nf, ok := errors.AsType[*transport.NamespaceNotFoundError](err); !ok || nf.Namespace != "missing" {
		t.Fatalf("err = %v (%T), want *NamespaceNotFoundError", err, err)
	}

	refusal = &service.DimensionMismatchError{Expected: 4, Actual: 8}
	_, err = tr.UpdateNamespace(context.Background(), &transport.UpdateNamespaceRequest{Namespace: "ns", Dimension: 8})
	if dm, ok := errors.AsType[*transport.DimensionMismatchError](err); !ok || dm.Expected != 4 || dm.Got != 8 {
		t.Fatalf("err = %v (%T), want *DimensionMismatchError 4/8", err, err)
	}
}

func TestUnixRT_DeleteNamespace(t *testing.T) {
	svc := &mockVectorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.DeleteNamespace(context.Background(), &transport.DeleteNamespaceRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}
	if !resp.Success {
		t.Error("expected Success=true")
	}
}

func TestUnixRT_ListNamespaces(t *testing.T) {
	svc := &mockVectorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.ListNamespaces(context.Background(), &transport.ListNamespacesRequest{})
	if err != nil {
		t.Fatalf("ListNamespaces: %v", err)
	}
	if len(resp.Namespaces) != 2 {
		t.Errorf("got %d, want 2", len(resp.Namespaces))
	}
}

func TestUnixRT_GetNamespaceInfo(t *testing.T) {
	svc := &mockVectorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.GetNamespaceInfo(context.Background(), &transport.GetNamespaceInfoRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("GetNamespaceInfo: %v", err)
	}
	if resp.Namespace != "ns" || resp.Dimension != 128 || resp.VectorCount != 1000 {
		t.Errorf("got %+v", resp)
	}
}

// ---------------------------------------------------------------------------
// Vector CRUD round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_UpsertVector(t *testing.T) {
	svc := &mockVectorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.UpsertVector(context.Background(), &transport.UpsertVectorRequest{
		Namespace: "ns",
		ID:        42,
		Vector:    []float32{0.1, 0.2, 0.3},
		Metadata:  map[string]interface{}{"tag": "test"},
	})
	if err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	if resp.ID != 42 {
		t.Errorf("got ID=%d, want 42", resp.ID)
	}
}

func TestUnixRT_GetVector(t *testing.T) {
	svc := &mockVectorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.GetVector(context.Background(), &transport.GetVectorRequest{
		Namespace: "ns",
		ID:        5,
	})
	if err != nil {
		t.Fatalf("GetVector: %v", err)
	}
	if resp.ID != 5 || len(resp.Vector) != 3 {
		t.Errorf("got %+v", resp)
	}
}

func TestUnixRT_DeleteVector(t *testing.T) {
	svc := &mockVectorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.DeleteVector(context.Background(), &transport.DeleteVectorRequest{
		Namespace: "ns",
		ID:        5,
	})
	if err != nil {
		t.Fatalf("DeleteVector: %v", err)
	}
	if !resp.Success {
		t.Error("expected Success=true")
	}
}

func TestUnixRT_BatchUpsert(t *testing.T) {
	svc := &mockVectorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.BatchUpsert(context.Background(), &transport.BatchUpsertRequest{
		Namespace: "ns",
		Vectors: []*transport.VectorData{
			{ID: 1, Vector: []float32{0.1}},
			{ID: 2, Vector: []float32{0.2}},
		},
	})
	if err != nil {
		t.Fatalf("BatchUpsert: %v", err)
	}
	if resp.InsertedCount != 2 {
		t.Errorf("got InsertedCount=%d, want 2", resp.InsertedCount)
	}
}

func TestUnixRT_BatchDeleteVectors(t *testing.T) {
	svc := &mockVectorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.BatchDeleteVectors(context.Background(), &transport.BatchDeleteVectorsRequest{
		Namespace: "ns",
		IDs:       []uint64{1, 2, 3},
	})
	if err != nil {
		t.Fatalf("BatchDeleteVectors: %v", err)
	}
	if resp.DeletedCount != 3 {
		t.Errorf("got DeletedCount=%d, want 3", resp.DeletedCount)
	}
}

func TestUnixRT_ListVectors(t *testing.T) {
	svc := &mockVectorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.ListVectors(context.Background(), &transport.ListVectorsRequest{
		Namespace: "ns",
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("ListVectors: %v", err)
	}
	if len(resp.Vectors) != 2 {
		t.Errorf("got %d vectors, want 2", len(resp.Vectors))
	}
}

func TestUnixRT_UpdateVectorMetadata(t *testing.T) {
	svc := &mockVectorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.UpdateMetadata(context.Background(), &transport.UpdateVectorMetadataRequest{
		Namespace: "ns",
		ID:        42,
		Metadata:  map[string]interface{}{"key": "value"},
	})
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if resp.ID != 42 || !resp.Success {
		t.Errorf("got %+v", resp)
	}
}

// ---------------------------------------------------------------------------
// Search round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_SearchSimilar(t *testing.T) {
	svc := &mockVectorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.SearchSimilar(context.Background(), &transport.SearchSimilarRequest{
		Namespace:   "ns",
		QueryVector: []float32{0.5, 0.5, 0.5},
		K:           10,
	})
	if err != nil {
		t.Fatalf("SearchSimilar: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Errorf("got %d results, want 2", len(resp.Results))
	}
}

func TestUnixRT_SearchByMetadata(t *testing.T) {
	svc := &mockVectorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.SearchByMetadata(context.Background(), &transport.SearchByMetadataRequest{
		Namespace: "ns",
		Filters:   map[string]interface{}{"category": "test"},
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("SearchByMetadata: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Errorf("got %d results, want 1", len(resp.Results))
	}
}

func TestUnixRT_HybridSearch(t *testing.T) {
	svc := &mockVectorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.HybridSearch(context.Background(), &transport.HybridSearchRequest{
		Namespace:      "ns",
		QueryVector:    []float32{0.5},
		MetadataFilter: map[string]interface{}{"label": "x"},
		K:              5,
	})
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Errorf("got %d results, want 1", len(resp.Results))
	}
}

func TestUnixRT_SearchByID(t *testing.T) {
	svc := &mockVectorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.SearchByID(context.Background(), &transport.SearchByIDRequest{
		Namespace: "ns",
		ID:        1,
		K:         5,
	})
	if err != nil {
		t.Fatalf("SearchByID: %v", err)
	}
	if resp.SourceID != 1 {
		t.Errorf("got SourceID=%d, want 1", resp.SourceID)
	}
}

// ---------------------------------------------------------------------------
// Index management round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_RebuildIndex(t *testing.T) {
	svc := &mockVectorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.RebuildIndex(context.Background(), &transport.RebuildIndexRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	if resp.JobID != "job-1" {
		t.Errorf("got JobID=%q, want job-1", resp.JobID)
	}
}

func TestUnixRT_GetRebuildProgress(t *testing.T) {
	svc := &mockVectorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.GetRebuildProgress(context.Background(), &transport.GetRebuildProgressRequest{
		Namespace: "ns",
		JobID:     "job-1",
	})
	if err != nil {
		t.Fatalf("GetRebuildProgress: %v", err)
	}
	if resp.PercentComplete != 50.0 {
		t.Errorf("got PercentComplete=%f, want 50.0", resp.PercentComplete)
	}
}

func TestUnixRT_GetIndexStats(t *testing.T) {
	svc := &mockVectorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.GetIndexStats(context.Background(), &transport.GetIndexStatsRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("GetIndexStats: %v", err)
	}
	if resp.VectorCount != 100 || resp.Dimension != 128 {
		t.Errorf("got %+v", resp)
	}
}

func TestUnixRT_CompactNamespace(t *testing.T) {
	svc := &mockVectorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.CompactNamespace(context.Background(), &transport.CompactNamespaceRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("CompactNamespace: %v", err)
	}
	if resp.OperationID != "compact-1" {
		t.Errorf("got OperationID=%q, want compact-1", resp.OperationID)
	}
}

// ---------------------------------------------------------------------------
// Error propagation
// ---------------------------------------------------------------------------

func TestUnixRT_GetVector_ServiceError(t *testing.T) {
	svc := &mockVectorSvc{
		getVectorFn: func(_ context.Context, _ *service.GetVectorRequest) (*service.GetVectorResponse, error) {
			return nil, errors.New("not found")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	_, err := tr.GetVector(context.Background(), &transport.GetVectorRequest{Namespace: "ns", ID: 999})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestUnixRT_SearchSimilar_ServiceError(t *testing.T) {
	svc := &mockVectorSvc{
		searchSimilarFn: func(_ context.Context, _ *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
			return nil, errors.New("search failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	_, err := tr.SearchSimilar(context.Background(), &transport.SearchSimilarRequest{Namespace: "ns"})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestUnixRT_UpsertVector_ServiceError(t *testing.T) {
	svc := &mockVectorSvc{
		upsertFn: func(_ context.Context, _ *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
			return nil, errors.New("upsert failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	_, err := tr.UpsertVector(context.Background(), &transport.UpsertVectorRequest{
		Namespace: "ns",
		ID:        1,
		Vector:    []float32{0.1},
		Metadata:  map[string]interface{}{"a": "b"},
	})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestUnixRT_CreateNamespace_ServiceError(t *testing.T) {
	svc := &mockVectorSvc{
		createNSFn: func(_ context.Context, _ *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
			return nil, errors.New("create namespace failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	_, err := tr.CreateNamespace(context.Background(), &transport.CreateNamespaceRequest{
		Namespace:   "ns",
		Dimension:   128,
		IndexConfig: map[string]string{"m": "16"},
	})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestUnixRT_BatchDeleteVectors_WithFailedIDs(t *testing.T) {
	svc := &mockVectorSvc{
		batchDeleteFn: func(_ context.Context, req *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
			return &service.BatchDeleteVectorsResponse{
				DeletedCount: len(req.IDs) - 1,
				FailedIDs:    []uint64{99},
				Success:      false,
			}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.BatchDeleteVectors(context.Background(), &transport.BatchDeleteVectorsRequest{
		Namespace: "ns",
		IDs:       []uint64{1, 2, 99},
	})
	if err != nil {
		t.Fatalf("BatchDeleteVectors: %v", err)
	}
	if len(resp.FailedIDs) != 1 || resp.FailedIDs[0] != 99 {
		t.Errorf("got FailedIDs=%v, want [99]", resp.FailedIDs)
	}
}

func TestUnixRT_UpdateMetadata_ServiceError(t *testing.T) {
	svc := &mockVectorSvc{
		updateMetaFn: func(_ context.Context, _ *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
			return nil, errors.New("update metadata failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	_, err := tr.UpdateMetadata(context.Background(), &transport.UpdateVectorMetadataRequest{
		Namespace: "ns",
		ID:        1,
		Metadata:  map[string]interface{}{"k": "v"},
	})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestUnixRT_SearchByMetadata_ServiceError(t *testing.T) {
	svc := &mockVectorSvc{
		searchByMetaFn: func(_ context.Context, _ *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
			return nil, errors.New("search by metadata failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	_, err := tr.SearchByMetadata(context.Background(), &transport.SearchByMetadataRequest{
		Namespace: "ns",
		Filters:   map[string]interface{}{"x": "y"},
	})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestUnixRT_HybridSearch_ServiceError(t *testing.T) {
	svc := &mockVectorSvc{
		hybridSearchFn: func(_ context.Context, _ *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
			return nil, errors.New("hybrid search failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	_, err := tr.HybridSearch(context.Background(), &transport.HybridSearchRequest{
		Namespace:      "ns",
		MetadataFilter: map[string]interface{}{"x": "y"},
	})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestUnixRT_SearchByID_ServiceError(t *testing.T) {
	svc := &mockVectorSvc{
		searchByIDFn: func(_ context.Context, _ *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
			return nil, errors.New("search by id failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	_, err := tr.SearchByID(context.Background(), &transport.SearchByIDRequest{Namespace: "ns", ID: 1})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestUnixRT_GetCompactionProgress_Completed(t *testing.T) {
	completedAt := int64(1_700_000_100)
	svc := &mockVectorSvc{
		compactProgFn: func(_ context.Context, _ *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
			return &service.GetCompactionProgressResponse{
				OperationID:     "comp-1",
				InProgress:      false,
				PercentComplete: 100.0,
				StartedAt:       1_700_000_000,
				CompletedAt:     &completedAt,
			}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVector(svc))
	defer cleanup()

	resp, err := tr.GetCompactionProgress(context.Background(), &transport.GetCompactionProgressRequest{
		Namespace:   "ns",
		OperationID: "comp-1",
	})
	if err != nil {
		t.Fatalf("GetCompactionProgress: %v", err)
	}
	if resp.InProgress || resp.PercentComplete != 100.0 {
		t.Errorf("got InProgress=%v PercentComplete=%f, want false and 100", resp.InProgress, resp.PercentComplete)
	}
	if resp.CompletedAt == nil || *resp.CompletedAt != completedAt {
		t.Errorf("got CompletedAt=%v, want %d", resp.CompletedAt, completedAt)
	}
}
