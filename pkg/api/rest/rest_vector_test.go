//go:build !integration

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

package rest

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock: VectorServicer
// ---------------------------------------------------------------------------

type mockVectorService struct {
	createNamespaceFn       func(ctx context.Context, req *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error)
	updateNamespaceFn       func(ctx context.Context, req *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error)
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

func (m *mockVectorService) UpdateNamespace(ctx context.Context, req *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error) {
	if m.updateNamespaceFn != nil {
		return m.updateNamespaceFn(ctx, req)
	}
	return &service.UpdateNamespaceResponse{Namespace: req.Namespace, RequiresRebuild: len(req.IndexConfig) > 0, Timestamp: 1000, Success: true}, nil
}

func (m *mockVectorService) CreateNamespace(ctx context.Context, req *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
	if m.createNamespaceFn != nil {
		return m.createNamespaceFn(ctx, req)
	}
	return &service.CreateNamespaceResponse{Namespace: req.Namespace, Dimension: req.Dimension, Timestamp: 1000, Success: true}, nil
}

func (m *mockVectorService) DeleteNamespace(ctx context.Context, req *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
	if m.deleteNamespaceFn != nil {
		return m.deleteNamespaceFn(ctx, req)
	}
	return &service.DeleteNamespaceResponse{Success: true, VectorsDeleted: 5, Timestamp: 1001}, nil
}

func (m *mockVectorService) ListNamespaces(ctx context.Context, req *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
	if m.listNamespacesFn != nil {
		return m.listNamespacesFn(ctx, req)
	}
	return &service.ListNamespacesResponse{
		Namespaces: []*service.NamespaceInfo{{Namespace: "ns1", Dimension: 128}},
		TotalCount: 1,
		Timestamp:  1002,
	}, nil
}

func (m *mockVectorService) GetNamespaceInfo(ctx context.Context, req *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
	if m.getNamespaceInfoFn != nil {
		return m.getNamespaceInfoFn(ctx, req)
	}
	return &service.GetNamespaceInfoResponse{
		Namespace: req.Namespace, Dimension: 128, VectorCount: 100,
		DistanceMetric: "L2", IndexType: "HNSW",
	}, nil
}

func (m *mockVectorService) UpsertVector(ctx context.Context, req *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	if m.upsertVectorFn != nil {
		return m.upsertVectorFn(ctx, req)
	}
	return &service.UpsertVectorResponse{ID: req.ID, Version: 1, Timestamp: 1003, Success: true}, nil
}

func (m *mockVectorService) GetVector(ctx context.Context, req *service.GetVectorRequest) (*service.GetVectorResponse, error) {
	if m.getVectorFn != nil {
		return m.getVectorFn(ctx, req)
	}
	return &service.GetVectorResponse{ID: req.ID, Vector: []float32{1.0, 2.0}, Version: 1, Found: true}, nil
}

func (m *mockVectorService) DeleteVector(ctx context.Context, req *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
	if m.deleteVectorFn != nil {
		return m.deleteVectorFn(ctx, req)
	}
	return &service.DeleteVectorResponse{Success: true, Timestamp: 1004}, nil
}

func (m *mockVectorService) BatchUpsert(ctx context.Context, req *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
	if m.batchUpsertFn != nil {
		return m.batchUpsertFn(ctx, req)
	}
	return &service.BatchUpsertResponse{IDs: []uint64{1, 2}, Versions: []uint64{1, 1}, Timestamp: 1005, Success: true, InsertedCount: 2}, nil
}

func (m *mockVectorService) BatchDeleteVectors(ctx context.Context, req *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
	if m.batchDeleteVectorsFn != nil {
		return m.batchDeleteVectorsFn(ctx, req)
	}
	return &service.BatchDeleteVectorsResponse{DeletedCount: 2, Timestamp: 1006, Success: true}, nil
}

func (m *mockVectorService) ListVectors(ctx context.Context, req *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
	if m.listVectorsFn != nil {
		return m.listVectorsFn(ctx, req)
	}
	return &service.ListVectorsResponse{
		Vectors:    []*service.VectorData{{ID: 1, Vector: []float32{1.0}}},
		NextCursor: "abc",
		TotalCount: 1,
		Timestamp:  1007,
	}, nil
}

func (m *mockVectorService) UpdateMetadata(ctx context.Context, req *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
	if m.updateMetadataFn != nil {
		return m.updateMetadataFn(ctx, req)
	}
	return &service.UpdateVectorMetadataResponse{ID: req.ID, Version: 2, Timestamp: 1008, Success: true}, nil
}

func (m *mockVectorService) SearchSimilar(ctx context.Context, req *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
	if m.searchSimilarFn != nil {
		return m.searchSimilarFn(ctx, req)
	}
	return &service.SearchSimilarResponse{
		Results:       []*service.VectorSearchResult{{ID: 1, Score: 0.95, Distance: 0.05, Rank: 1}},
		Timestamp:     1009,
		QueryDuration: 100,
	}, nil
}

func (m *mockVectorService) SearchByMetadata(ctx context.Context, req *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
	if m.searchByMetadataFn != nil {
		return m.searchByMetadataFn(ctx, req)
	}
	return &service.SearchByMetadataResponse{
		Results:    []*service.VectorData{{ID: 1, Vector: []float32{1.0}}},
		TotalCount: 1,
		Timestamp:  1010,
	}, nil
}

func (m *mockVectorService) HybridSearch(ctx context.Context, req *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
	if m.hybridSearchFn != nil {
		return m.hybridSearchFn(ctx, req)
	}
	return &service.HybridSearchResponse{
		Results:         []*service.VectorSearchResult{{ID: 1, Score: 0.9, Rank: 1}},
		TotalCandidates: 10,
		TotalFiltered:   1,
		Timestamp:       1011,
		QueryDuration:   50,
	}, nil
}

func (m *mockVectorService) SearchByID(ctx context.Context, req *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
	if m.searchByIDFn != nil {
		return m.searchByIDFn(ctx, req)
	}
	return &service.SearchByIDResponse{
		SourceID:      req.ID,
		Results:       []*service.VectorSearchResult{{ID: 2, Score: 0.8, Rank: 1}},
		Timestamp:     1012,
		QueryDuration: 75,
	}, nil
}

func (m *mockVectorService) RebuildIndex(ctx context.Context, req *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
	if m.rebuildIndexFn != nil {
		return m.rebuildIndexFn(ctx, req)
	}
	return &service.RebuildIndexResponse{JobID: "job-1", Status: "running", Timestamp: 1013, Success: true}, nil
}

func (m *mockVectorService) GetRebuildProgress(ctx context.Context, req *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
	if m.getRebuildProgressFn != nil {
		return m.getRebuildProgressFn(ctx, req)
	}
	return &service.GetRebuildProgressResponse{
		JobID: req.JobID, Namespace: req.Namespace, Status: "running",
		VectorsProcessed: 50, TotalVectors: 100, PercentComplete: 50.0,
	}, nil
}

func (m *mockVectorService) GetIndexStats(ctx context.Context, req *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
	if m.getIndexStatsFn != nil {
		return m.getIndexStatsFn(ctx, req)
	}
	return &service.GetIndexStatsResponse{
		Namespace: req.Namespace, IndexType: "HNSW", VectorCount: 100, Dimension: 128,
		IndexSizeBytes: 4096, MemoryUsageBytes: 2048, Timestamp: 1014,
	}, nil
}

func (m *mockVectorService) CompactNamespace(ctx context.Context, req *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
	if m.compactNamespaceFn != nil {
		return m.compactNamespaceFn(ctx, req)
	}
	return &service.CompactNamespaceResponse{OperationID: "op-1", StartedAt: 1015, Success: true}, nil
}

func (m *mockVectorService) GetCompactionProgress(ctx context.Context, req *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
	if m.getCompactionProgressFn != nil {
		return m.getCompactionProgressFn(ctx, req)
	}
	return &service.GetCompactionProgressResponse{
		OperationID: req.OperationID, Namespace: req.Namespace,
		BytesCompacted: 1024, TotalBytes: 4096, PercentComplete: 25.0, InProgress: true,
	}, nil
}

// ---------------------------------------------------------------------------
// Helper: create server with vector service
// ---------------------------------------------------------------------------

func newVectorServer(vecSvc *mockVectorService) *Server {
	return NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithVector(vecSvc))
}

// ---------------------------------------------------------------------------
// WithVector server option
// ---------------------------------------------------------------------------

func TestWithVector(t *testing.T) {
	svc := &mockVectorService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithVector(svc))
	if srv.vectorSvc == nil {
		t.Fatal("vectorSvc should not be nil after WithVector")
	}
}

func TestWithVector_Nil(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	if srv.vectorSvc != nil {
		t.Fatal("vectorSvc should be nil without WithVector")
	}
}

// ---------------------------------------------------------------------------
// Vector Create Namespace
// ---------------------------------------------------------------------------

func TestHandleVectorCreateNamespace_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	body := `{"namespace":"test-ns","dimension":128,"distance_metric":"L2","index_type":"HNSW"}`
	req := httptest.NewRequest(http.MethodPost, "/vector/namespace", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorCreateNamespaceResponse
	decodeJSON(t, rec, &resp)
	if resp.Namespace != "test-ns" {
		t.Fatalf("namespace = %q, want %q", resp.Namespace, "test-ns")
	}
	if !resp.Success {
		t.Fatal("expected success = true")
	}
}

func TestHandleVectorCreateNamespace_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	body := `{"namespace":"test-ns","dimension":128}`
	req := httptest.NewRequest(http.MethodPost, "/vector/namespace", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorCreateNamespace_BadJSON(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodPost, "/vector/namespace", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorCreateNamespace_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		createNamespaceFn: func(_ context.Context, _ *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
			return nil, &service.NamespaceExistsError{Namespace: "test-ns"}
		},
	}
	srv := newVectorServer(vecSvc)
	body := `{"namespace":"test-ns","dimension":128}`
	req := httptest.NewRequest(http.MethodPost, "/vector/namespace", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusConflict)
}

// ---------------------------------------------------------------------------
// Vector Delete Namespace
// ---------------------------------------------------------------------------

func TestHandleVectorDeleteNamespace_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	body := `{"namespace":"test-ns"}`
	req := httptest.NewRequest(http.MethodPost, "/vector/namespace/delete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorDeleteNamespaceResponse
	decodeJSON(t, rec, &resp)
	if !resp.Success {
		t.Fatal("expected success = true")
	}
}

func TestHandleVectorDeleteNamespace_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	body := `{"namespace":"test-ns"}`
	req := httptest.NewRequest(http.MethodPost, "/vector/namespace/delete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorDeleteNamespace_BadJSON(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodPost, "/vector/namespace/delete", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorDeleteNamespace_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		deleteNamespaceFn: func(_ context.Context, _ *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
			return nil, &service.NamespaceNotFoundError{Namespace: "test-ns"}
		},
	}
	srv := newVectorServer(vecSvc)
	body := `{"namespace":"test-ns"}`
	req := httptest.NewRequest(http.MethodPost, "/vector/namespace/delete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// Vector List Namespaces
// ---------------------------------------------------------------------------

func TestHandleVectorListNamespaces_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/namespaces", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorListNamespacesResponse
	decodeJSON(t, rec, &resp)
	if resp.TotalCount != 1 {
		t.Fatalf("total_count = %d, want 1", resp.TotalCount)
	}
}

func TestHandleVectorListNamespaces_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodGet, "/vector/namespaces", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorListNamespaces_WithShardID(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/namespaces?shard_id=1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)
}

func TestHandleVectorListNamespaces_InvalidShardID(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/namespaces?shard_id=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorListNamespaces_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		listNamespacesFn: func(_ context.Context, _ *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
			return nil, errors.New("list failed")
		},
	}
	srv := newVectorServer(vecSvc)
	req := httptest.NewRequest(http.MethodGet, "/vector/namespaces", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// Vector Get Namespace Info
// ---------------------------------------------------------------------------

func TestHandleVectorGetNamespaceInfo_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/namespace/info?namespace=test-ns", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorNamespaceInfoResponse
	decodeJSON(t, rec, &resp)
	if resp.Namespace != "test-ns" {
		t.Fatalf("namespace = %q, want %q", resp.Namespace, "test-ns")
	}
}

func TestHandleVectorGetNamespaceInfo_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodGet, "/vector/namespace/info?namespace=test-ns", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorGetNamespaceInfo_MissingNamespace(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/namespace/info", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorGetNamespaceInfo_InvalidShardID(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/namespace/info?namespace=ns&shard_id=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorGetNamespaceInfo_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		getNamespaceInfoFn: func(_ context.Context, _ *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
			return nil, &service.NamespaceNotFoundError{Namespace: "test-ns"}
		},
	}
	srv := newVectorServer(vecSvc)
	req := httptest.NewRequest(http.MethodGet, "/vector/namespace/info?namespace=test-ns", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// Vector Upsert
// ---------------------------------------------------------------------------

func TestHandleVectorUpsert_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	body := `{"namespace":"ns","id":1,"vector":[1.0,2.0,3.0]}`
	req := httptest.NewRequest(http.MethodPost, "/vector/upsert", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorUpsertResponse
	decodeJSON(t, rec, &resp)
	if !resp.Success {
		t.Fatal("expected success = true")
	}
}

func TestHandleVectorUpsert_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	body := `{"namespace":"ns","id":1,"vector":[1.0]}`
	req := httptest.NewRequest(http.MethodPost, "/vector/upsert", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorUpsert_BadJSON(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodPost, "/vector/upsert", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorUpsert_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		upsertVectorFn: func(_ context.Context, _ *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
			return nil, &service.DimensionMismatchError{Expected: 128, Actual: 3}
		},
	}
	srv := newVectorServer(vecSvc)
	body := `{"namespace":"ns","id":1,"vector":[1.0,2.0,3.0]}`
	req := httptest.NewRequest(http.MethodPost, "/vector/upsert", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// Vector Get
// ---------------------------------------------------------------------------

func TestHandleVectorGet_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/get?namespace=ns&id=1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorDataJSON
	decodeJSON(t, rec, &resp)
	if resp.ID != 1 {
		t.Fatalf("id = %d, want 1", resp.ID)
	}
}

func TestHandleVectorGet_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodGet, "/vector/get?namespace=ns&id=1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorGet_MissingNamespace(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/get?id=1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorGet_MissingID(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/get?namespace=ns", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorGet_InvalidID(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/get?namespace=ns&id=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorGet_InvalidConsistency(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/get?namespace=ns&id=1&consistency=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorGet_InvalidShardID(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/get?namespace=ns&id=1&shard_id=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorGet_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		getVectorFn: func(_ context.Context, _ *service.GetVectorRequest) (*service.GetVectorResponse, error) {
			return nil, &service.VectorNotFoundError{ID: "1"}
		},
	}
	srv := newVectorServer(vecSvc)
	req := httptest.NewRequest(http.MethodGet, "/vector/get?namespace=ns&id=1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// Vector Delete
// ---------------------------------------------------------------------------

func TestHandleVectorDelete_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	body := `{"namespace":"ns","id":1}`
	req := httptest.NewRequest(http.MethodPost, "/vector/delete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorDeleteResponse
	decodeJSON(t, rec, &resp)
	if !resp.Success {
		t.Fatal("expected success = true")
	}
}

func TestHandleVectorDelete_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	body := `{"namespace":"ns","id":1}`
	req := httptest.NewRequest(http.MethodPost, "/vector/delete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorDelete_BadJSON(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodPost, "/vector/delete", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorDelete_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		deleteVectorFn: func(_ context.Context, _ *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
			return nil, &service.VectorNotFoundError{ID: "1"}
		},
	}
	srv := newVectorServer(vecSvc)
	body := `{"namespace":"ns","id":1}`
	req := httptest.NewRequest(http.MethodPost, "/vector/delete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// Vector Batch Upsert
// ---------------------------------------------------------------------------

func TestHandleVectorBatchUpsert_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	body := `{"namespace":"ns","vectors":[{"id":1,"vector":[1.0]},{"id":2,"vector":[2.0]}]}`
	req := httptest.NewRequest(http.MethodPost, "/vector/batch/upsert", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorBatchUpsertResponse
	decodeJSON(t, rec, &resp)
	if !resp.Success {
		t.Fatal("expected success = true")
	}
	if resp.InsertedCount != 2 {
		t.Fatalf("inserted_count = %d, want 2", resp.InsertedCount)
	}
}

func TestHandleVectorBatchUpsert_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	body := `{"namespace":"ns","vectors":[]}`
	req := httptest.NewRequest(http.MethodPost, "/vector/batch/upsert", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorBatchUpsert_BadJSON(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodPost, "/vector/batch/upsert", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorBatchUpsert_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		batchUpsertFn: func(_ context.Context, _ *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
			return nil, &service.NamespaceNotFoundError{Namespace: "ns"}
		},
	}
	srv := newVectorServer(vecSvc)
	body := `{"namespace":"ns","vectors":[{"id":1,"vector":[1.0]}]}`
	req := httptest.NewRequest(http.MethodPost, "/vector/batch/upsert", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// Vector Batch Delete
// ---------------------------------------------------------------------------

func TestHandleVectorBatchDelete_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	body := `{"namespace":"ns","ids":[1,2,3]}`
	req := httptest.NewRequest(http.MethodPost, "/vector/batch/delete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorBatchDeleteResponse
	decodeJSON(t, rec, &resp)
	if !resp.Success {
		t.Fatal("expected success = true")
	}
}

func TestHandleVectorBatchDelete_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	body := `{"namespace":"ns","ids":[1]}`
	req := httptest.NewRequest(http.MethodPost, "/vector/batch/delete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorBatchDelete_BadJSON(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodPost, "/vector/batch/delete", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorBatchDelete_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		batchDeleteVectorsFn: func(_ context.Context, _ *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
			return nil, errors.New("batch delete failed")
		},
	}
	srv := newVectorServer(vecSvc)
	body := `{"namespace":"ns","ids":[1]}`
	req := httptest.NewRequest(http.MethodPost, "/vector/batch/delete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// Vector List Vectors
// ---------------------------------------------------------------------------

func TestHandleVectorListVectors_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/list?namespace=ns", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorListResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Vectors) != 1 {
		t.Fatalf("vectors count = %d, want 1", len(resp.Vectors))
	}
}

func TestHandleVectorListVectors_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodGet, "/vector/list?namespace=ns", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorListVectors_MissingNamespace(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/list", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorListVectors_InvalidLimit(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/list?namespace=ns&limit=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorListVectors_InvalidShardID(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/list?namespace=ns&shard_id=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorListVectors_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		listVectorsFn: func(_ context.Context, _ *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
			return nil, errors.New("list failed")
		},
	}
	srv := newVectorServer(vecSvc)
	req := httptest.NewRequest(http.MethodGet, "/vector/list?namespace=ns", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// Vector Update Metadata
// ---------------------------------------------------------------------------

func TestHandleVectorUpdateMetadata_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	body := `{"namespace":"ns","id":1,"metadata":{"key":"val"}}`
	req := httptest.NewRequest(http.MethodPost, "/vector/metadata", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorUpdateMetadataResponse
	decodeJSON(t, rec, &resp)
	if !resp.Success {
		t.Fatal("expected success = true")
	}
}

func TestHandleVectorUpdateMetadata_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	body := `{"namespace":"ns","id":1,"metadata":{}}`
	req := httptest.NewRequest(http.MethodPost, "/vector/metadata", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorUpdateMetadata_BadJSON(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodPost, "/vector/metadata", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorUpdateMetadata_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		updateMetadataFn: func(_ context.Context, _ *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
			return nil, &service.VectorNotFoundError{ID: "1"}
		},
	}
	srv := newVectorServer(vecSvc)
	body := `{"namespace":"ns","id":1,"metadata":{"key":"val"}}`
	req := httptest.NewRequest(http.MethodPost, "/vector/metadata", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// Vector Search Similar
// ---------------------------------------------------------------------------

func TestHandleVectorSearchSimilar_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	body := `{"namespace":"ns","query_vector":[1.0,2.0],"k":5}`
	req := httptest.NewRequest(http.MethodPost, "/vector/search/similar", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorSearchSimilarResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Results) != 1 {
		t.Fatalf("results count = %d, want 1", len(resp.Results))
	}
}

func TestHandleVectorSearchSimilar_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	body := `{"namespace":"ns","query_vector":[1.0],"k":5}`
	req := httptest.NewRequest(http.MethodPost, "/vector/search/similar", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorSearchSimilar_BadJSON(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodPost, "/vector/search/similar", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorSearchSimilar_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		searchSimilarFn: func(_ context.Context, _ *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
			return nil, &service.InvalidVectorError{Reason: "empty"}
		},
	}
	srv := newVectorServer(vecSvc)
	body := `{"namespace":"ns","query_vector":[],"k":5}`
	req := httptest.NewRequest(http.MethodPost, "/vector/search/similar", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// Vector Search By Metadata
// ---------------------------------------------------------------------------

func TestHandleVectorSearchByMetadata_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	body := `{"namespace":"ns","filters":{"color":"red"}}`
	req := httptest.NewRequest(http.MethodPost, "/vector/search/metadata", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorSearchByMetadataResponse
	decodeJSON(t, rec, &resp)
	if resp.TotalCount != 1 {
		t.Fatalf("total_count = %d, want 1", resp.TotalCount)
	}
}

func TestHandleVectorSearchByMetadata_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	body := `{"namespace":"ns","filters":{}}`
	req := httptest.NewRequest(http.MethodPost, "/vector/search/metadata", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorSearchByMetadata_BadJSON(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodPost, "/vector/search/metadata", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorSearchByMetadata_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		searchByMetadataFn: func(_ context.Context, _ *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
			return nil, &service.NamespaceNotFoundError{Namespace: "ns"}
		},
	}
	srv := newVectorServer(vecSvc)
	body := `{"namespace":"ns","filters":{}}`
	req := httptest.NewRequest(http.MethodPost, "/vector/search/metadata", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// Vector Hybrid Search
// ---------------------------------------------------------------------------

func TestHandleVectorHybridSearch_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	body := `{"namespace":"ns","query_vector":[1.0],"k":5,"metadata_filter":{"color":"red"}}`
	req := httptest.NewRequest(http.MethodPost, "/vector/search/hybrid", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorHybridSearchResponse
	decodeJSON(t, rec, &resp)
	if resp.TotalCandidates != 10 {
		t.Fatalf("total_candidates = %d, want 10", resp.TotalCandidates)
	}
}

func TestHandleVectorHybridSearch_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	body := `{"namespace":"ns","query_vector":[1.0],"k":5,"metadata_filter":{}}`
	req := httptest.NewRequest(http.MethodPost, "/vector/search/hybrid", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorHybridSearch_BadJSON(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodPost, "/vector/search/hybrid", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorHybridSearch_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		hybridSearchFn: func(_ context.Context, _ *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
			return nil, errors.New("hybrid search failed")
		},
	}
	srv := newVectorServer(vecSvc)
	body := `{"namespace":"ns","query_vector":[1.0],"k":5,"metadata_filter":{}}`
	req := httptest.NewRequest(http.MethodPost, "/vector/search/hybrid", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// Vector Search By ID
// ---------------------------------------------------------------------------

func TestHandleVectorSearchByID_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	body := `{"namespace":"ns","id":1,"k":5}`
	req := httptest.NewRequest(http.MethodPost, "/vector/search/by-id", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorSearchSimilarResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Results) != 1 {
		t.Fatalf("results count = %d, want 1", len(resp.Results))
	}
}

func TestHandleVectorSearchByID_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	body := `{"namespace":"ns","id":1,"k":5}`
	req := httptest.NewRequest(http.MethodPost, "/vector/search/by-id", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorSearchByID_BadJSON(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodPost, "/vector/search/by-id", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorSearchByID_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		searchByIDFn: func(_ context.Context, _ *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
			return nil, &service.VectorNotFoundError{ID: "1"}
		},
	}
	srv := newVectorServer(vecSvc)
	body := `{"namespace":"ns","id":1,"k":5}`
	req := httptest.NewRequest(http.MethodPost, "/vector/search/by-id", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// Vector Rebuild Index
// ---------------------------------------------------------------------------

func TestHandleVectorRebuildIndex_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	body := `{"namespace":"ns"}`
	req := httptest.NewRequest(http.MethodPost, "/vector/index/rebuild", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorRebuildIndexResponse
	decodeJSON(t, rec, &resp)
	if resp.JobID != "job-1" {
		t.Fatalf("job_id = %q, want %q", resp.JobID, "job-1")
	}
}

func TestHandleVectorRebuildIndex_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	body := `{"namespace":"ns"}`
	req := httptest.NewRequest(http.MethodPost, "/vector/index/rebuild", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorRebuildIndex_BadJSON(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodPost, "/vector/index/rebuild", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorRebuildIndex_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		rebuildIndexFn: func(_ context.Context, _ *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
			return nil, &service.NamespaceNotFoundError{Namespace: "ns"}
		},
	}
	srv := newVectorServer(vecSvc)
	body := `{"namespace":"ns"}`
	req := httptest.NewRequest(http.MethodPost, "/vector/index/rebuild", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// Vector Get Rebuild Progress
// ---------------------------------------------------------------------------

func TestHandleVectorGetRebuildProgress_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/index/rebuild/progress?namespace=ns&job_id=j1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorRebuildProgressResponse
	decodeJSON(t, rec, &resp)
	if resp.Status != "running" {
		t.Fatalf("status = %q, want %q", resp.Status, "running")
	}
}

func TestHandleVectorGetRebuildProgress_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodGet, "/vector/index/rebuild/progress?namespace=ns&job_id=j1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorGetRebuildProgress_MissingNamespace(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/index/rebuild/progress?job_id=j1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorGetRebuildProgress_MissingJobID(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/index/rebuild/progress?namespace=ns", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorGetRebuildProgress_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		getRebuildProgressFn: func(_ context.Context, _ *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
			return nil, errors.New("not found")
		},
	}
	srv := newVectorServer(vecSvc)
	req := httptest.NewRequest(http.MethodGet, "/vector/index/rebuild/progress?namespace=ns&job_id=j1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// Vector Get Index Stats
// ---------------------------------------------------------------------------

func TestHandleVectorGetIndexStats_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/index/stats?namespace=ns", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorIndexStatsResponse
	decodeJSON(t, rec, &resp)
	if resp.VectorCount != 100 {
		t.Fatalf("vector_count = %d, want 100", resp.VectorCount)
	}
}

func TestHandleVectorGetIndexStats_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodGet, "/vector/index/stats?namespace=ns", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorGetIndexStats_MissingNamespace(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/index/stats", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorGetIndexStats_InvalidShardID(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/index/stats?namespace=ns&shard_id=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorGetIndexStats_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		getIndexStatsFn: func(_ context.Context, _ *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
			return nil, &service.NamespaceNotFoundError{Namespace: "ns"}
		},
	}
	srv := newVectorServer(vecSvc)
	req := httptest.NewRequest(http.MethodGet, "/vector/index/stats?namespace=ns", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// Vector Compact Namespace
// ---------------------------------------------------------------------------

func TestHandleVectorCompactNamespace_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	body := `{"namespace":"ns"}`
	req := httptest.NewRequest(http.MethodPost, "/vector/compact", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorCompactResponse
	decodeJSON(t, rec, &resp)
	if resp.OperationID != "op-1" {
		t.Fatalf("operation_id = %q, want %q", resp.OperationID, "op-1")
	}
}

func TestHandleVectorCompactNamespace_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	body := `{"namespace":"ns"}`
	req := httptest.NewRequest(http.MethodPost, "/vector/compact", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorCompactNamespace_BadJSON(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodPost, "/vector/compact", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorCompactNamespace_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		compactNamespaceFn: func(_ context.Context, _ *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
			return nil, errors.New("compact failed")
		},
	}
	srv := newVectorServer(vecSvc)
	body := `{"namespace":"ns"}`
	req := httptest.NewRequest(http.MethodPost, "/vector/compact", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// Vector Get Compaction Progress
// ---------------------------------------------------------------------------

func TestHandleVectorGetCompactionProgress_Success(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/compact/progress?namespace=ns&operation_id=op-1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp vectorCompactionProgressResponse
	decodeJSON(t, rec, &resp)
	if !resp.InProgress {
		t.Fatal("expected in_progress = true")
	}
}

func TestHandleVectorGetCompactionProgress_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodGet, "/vector/compact/progress?namespace=ns&operation_id=op-1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleVectorGetCompactionProgress_MissingNamespace(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/compact/progress?operation_id=op-1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorGetCompactionProgress_MissingOperationID(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/compact/progress?namespace=ns", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorGetCompactionProgress_InvalidShardID(t *testing.T) {
	srv := newVectorServer(&mockVectorService{})
	req := httptest.NewRequest(http.MethodGet, "/vector/compact/progress?namespace=ns&operation_id=op-1&shard_id=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleVectorGetCompactionProgress_ServiceError(t *testing.T) {
	vecSvc := &mockVectorService{
		getCompactionProgressFn: func(_ context.Context, _ *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
			return nil, errors.New("not found")
		},
	}
	srv := newVectorServer(vecSvc)
	req := httptest.NewRequest(http.MethodGet, "/vector/compact/progress?namespace=ns&operation_id=op-1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// Vector JSON conversion helpers
// ---------------------------------------------------------------------------

func TestVectorDataPtrSliceToJSON_WithNil(t *testing.T) {
	data := []*service.VectorData{
		{ID: 1, Vector: []float32{1.0, 2.0}, Version: 1},
		nil,
		{ID: 3, Vector: []float32{3.0, 4.0}, Version: 2},
	}
	result := vectorDataPtrSliceToJSON(data)
	if len(result) != 3 {
		t.Fatalf("result length = %d, want 3", len(result))
	}
	if result[0].ID != 1 {
		t.Fatalf("result[0].ID = %d, want 1", result[0].ID)
	}
	if result[1].ID != 0 {
		t.Fatalf("result[1].ID = %d, want 0 (nil entry)", result[1].ID)
	}
	if result[2].ID != 3 {
		t.Fatalf("result[2].ID = %d, want 3", result[2].ID)
	}
}

func TestVectorDataPtrSliceToJSON_Empty(t *testing.T) {
	result := vectorDataPtrSliceToJSON(nil)
	if len(result) != 0 {
		t.Fatalf("result length = %d, want 0", len(result))
	}
}

func TestSearchResultPtrsToJSON_WithNil(t *testing.T) {
	results := []*service.VectorSearchResult{
		{ID: 1, Score: 0.9, Distance: 0.1, Rank: 1},
		nil,
	}
	resp := searchResultPtrsToJSON(results)
	if len(resp) != 2 {
		t.Fatalf("result length = %d, want 2", len(resp))
	}
	if resp[0].ID != 1 {
		t.Fatalf("resp[0].ID = %d, want 1", resp[0].ID)
	}
	if resp[1].ID != 0 {
		t.Fatalf("resp[1].ID = %d, want 0 (nil entry)", resp[1].ID)
	}
}

func TestSearchResultPtrsToJSON_Empty(t *testing.T) {
	resp := searchResultPtrsToJSON(nil)
	if len(resp) != 0 {
		t.Fatalf("result length = %d, want 0", len(resp))
	}
}

func TestNamespaceInfoSliceToJSON_WithNil(t *testing.T) {
	infos := []*service.NamespaceInfo{
		{Namespace: "ns1", Dimension: 128},
		nil,
	}
	result := namespaceInfoSliceToJSON(infos)
	if len(result) != 2 {
		t.Fatalf("result length = %d, want 2", len(result))
	}
	if result[0].Namespace != "ns1" {
		t.Fatalf("result[0].Namespace = %q, want %q", result[0].Namespace, "ns1")
	}
}

func TestNamespaceInfoSliceToJSON_Empty(t *testing.T) {
	result := namespaceInfoSliceToJSON(nil)
	if len(result) != 0 {
		t.Fatalf("result length = %d, want 0", len(result))
	}
}

// ---------------------------------------------------------------------------
// handleCapabilities
// ---------------------------------------------------------------------------

func TestHandleCapabilities_Success(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodGet, "/capabilities", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp capabilitiesResponse
	decodeJSON(t, rec, &resp)
	if !resp.KV {
		t.Fatal("expected KV = true")
	}
}

func TestHandleCapabilities_ServiceError(t *testing.T) {
	adminSvc := &mockAdminService{
		capabilitiesFn: func(_ context.Context) (*capabilities.Capabilities, error) {
			return nil, errors.New("caps failed")
		},
	}
	srv := NewServer(newDefaultKVSvc(), adminSvc, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodGet, "/capabilities", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// handleKVScan
// ---------------------------------------------------------------------------

func TestHandleKVScan_Success(t *testing.T) {
	kvSvc := &scanKVService{
		mockKVService: mockKVService{},
		scanFn: func(_ context.Context, prefix string) (map[string][]byte, error) {
			return map[string][]byte{"test/a": []byte("1"), "test/b": []byte("2")}, nil
		},
	}
	srv := NewServer(kvSvc, newDefaultAdminSvc(), "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodGet, "/kv/scan?prefix=test", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp kvScanResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Pairs) != 2 {
		t.Fatalf("pairs count = %d, want 2", len(resp.Pairs))
	}
}

func TestHandleKVScan_EmptyPrefix(t *testing.T) {
	kvSvc := &scanKVService{
		mockKVService: mockKVService{},
		scanFn: func(_ context.Context, _ string) (map[string][]byte, error) {
			return nil, nil
		},
	}
	srv := NewServer(kvSvc, newDefaultAdminSvc(), "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodGet, "/kv/scan", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)
}

func TestHandleKVScan_ServiceError(t *testing.T) {
	kvSvc := &scanKVService{
		mockKVService: mockKVService{},
		scanFn: func(_ context.Context, _ string) (map[string][]byte, error) {
			return nil, errors.New("scan failed")
		},
	}
	srv := NewServer(kvSvc, newDefaultAdminSvc(), "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodGet, "/kv/scan?prefix=test", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// scanKVService wraps mockKVService with a customizable Scan function.
type scanKVService struct {
	mockKVService
	scanFn func(ctx context.Context, prefix string) (map[string][]byte, error)
}

func (s *scanKVService) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	if s.scanFn != nil {
		return s.scanFn(ctx, prefix)
	}
	return nil, nil
}

func (s *scanKVService) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return s.Scan(ctx, prefix)
}

// ---------------------------------------------------------------------------
// mapHTTPError: cover remaining vector error types
// ---------------------------------------------------------------------------

func TestMapHTTPError_VectorErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"VectorNotFoundError", &service.VectorNotFoundError{ID: "1"}, http.StatusNotFound},
		{"NamespaceNotFoundError", &service.NamespaceNotFoundError{Namespace: "ns"}, http.StatusNotFound},
		{"NamespaceExistsError", &service.NamespaceExistsError{Namespace: "ns"}, http.StatusConflict},
		{"DimensionMismatchError", &service.DimensionMismatchError{Expected: 128, Actual: 64}, http.StatusBadRequest},
		{"InvalidVectorError", &service.InvalidVectorError{Reason: "empty"}, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := mapHTTPError(tt.err)
			if code != tt.wantCode {
				t.Fatalf("mapHTTPError(%T) = %d, want %d", tt.err, code, tt.wantCode)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// WithAuthMiddleware
// ---------------------------------------------------------------------------

func TestWithAuthMiddleware_Nil(t *testing.T) {
	// Verify the option sets the field (passing nil).
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithAuthMiddleware(nil))
	if srv.authMW != nil {
		t.Fatal("authMW should be nil when nil is passed")
	}
}

// ---------------------------------------------------------------------------
// FetchJSONWithMTLS
// ---------------------------------------------------------------------------

func TestFetchJSONWithMTLS_ConnectionRefused(t *testing.T) {
	// The function should return an error for a non-existent server.
	var dst json.RawMessage
	err := FetchJSONWithMTLS("https://127.0.0.1:1/test", &dst, nil, tlsNopCert())
	if err == nil {
		t.Fatal("expected error for connection to non-existent server")
	}
}

func TestFetchJSONWithMTLS_InvalidURL(t *testing.T) {
	var dst json.RawMessage
	err := FetchJSONWithMTLS("https://invalid-host-that-does-not-exist:1/test", &dst, nil, tlsNopCert())
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

// tlsNopCert returns a zero-value tls.Certificate for testing the mTLS
// path. The certificate is intentionally invalid -- the test verifies that
// the function returns a connection error, not that TLS negotiation succeeds.
func tlsNopCert() tls.Certificate {
	return tls.Certificate{}
}
