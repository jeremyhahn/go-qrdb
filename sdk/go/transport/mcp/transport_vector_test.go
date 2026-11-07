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
	"errors"
	"sync"
	"testing"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockVectorSvc implements service.VectorServicer for transport tests.
// ---------------------------------------------------------------------------

type mockVectorSvc struct {
	mu sync.Mutex

	createNSErr    error
	updateNSErr    error
	lastUpdateNS   *service.UpdateNamespaceRequest
	deleteNSErr    error
	listNSErr      error
	getNSInfoErr   error
	upsertErr      error
	getVecErr      error
	deleteVecErr   error
	batchUpsertErr error
	batchDelErr    error
	listVecErr     error
	updateMetaErr  error
	searchSimErr   error
	searchMetaErr  error
	hybridErr      error
	searchByIDErr  error
	rebuildErr     error
	rebuildProgErr error
	indexStatsErr  error
	compactErr     error
	compactProgErr error

	// nsInfoMetric, when set, is the distance metric GetNamespaceInfo
	// reports in place of L2.
	nsInfoMetric string
}

var _ service.VectorServicer = (*mockVectorSvc)(nil)

func newMockVectorSvc() *mockVectorSvc {
	return &mockVectorSvc{}
}

func (m *mockVectorSvc) CreateNamespace(_ context.Context, req *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
	if m.createNSErr != nil {
		return nil, m.createNSErr
	}
	return &service.CreateNamespaceResponse{
		Namespace: req.Namespace,
		Dimension: req.Dimension,
		Success:   true,
		Timestamp: 1700000000,
	}, nil
}

func (m *mockVectorSvc) UpdateNamespace(_ context.Context, req *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error) {
	m.lastUpdateNS = req
	if m.updateNSErr != nil {
		return nil, m.updateNSErr
	}
	return &service.UpdateNamespaceResponse{
		Namespace:       req.Namespace,
		RequiresRebuild: len(req.IndexConfig) > 0,
		Success:         true,
		Timestamp:       1700000000,
	}, nil
}

func (m *mockVectorSvc) DeleteNamespace(_ context.Context, _ *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
	if m.deleteNSErr != nil {
		return nil, m.deleteNSErr
	}
	return &service.DeleteNamespaceResponse{Success: true, VectorsDeleted: 10}, nil
}

func (m *mockVectorSvc) ListNamespaces(_ context.Context, _ *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
	if m.listNSErr != nil {
		return nil, m.listNSErr
	}
	return &service.ListNamespacesResponse{
		Namespaces: []*service.NamespaceInfo{
			{Namespace: "ns1", Dimension: 128, VectorCount: 100, DistanceMetric: "L2", IndexType: "HNSW"},
		},
		TotalCount: 1,
	}, nil
}

func (m *mockVectorSvc) GetNamespaceInfo(_ context.Context, _ *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
	if m.getNSInfoErr != nil {
		return nil, m.getNSInfoErr
	}
	metric := "L2"
	if m.nsInfoMetric != "" {
		metric = m.nsInfoMetric
	}
	return &service.GetNamespaceInfoResponse{
		Namespace:      "ns1",
		Dimension:      128,
		VectorCount:    100,
		DistanceMetric: metric,
		IndexType:      "HNSW",
	}, nil
}

func (m *mockVectorSvc) UpsertVector(_ context.Context, req *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	if m.upsertErr != nil {
		return nil, m.upsertErr
	}
	return &service.UpsertVectorResponse{ID: req.ID, Version: 1, Success: true}, nil
}

func (m *mockVectorSvc) GetVector(_ context.Context, req *service.GetVectorRequest) (*service.GetVectorResponse, error) {
	if m.getVecErr != nil {
		return nil, m.getVecErr
	}
	return &service.GetVectorResponse{
		ID:     req.ID,
		Vector: []float32{0.1, 0.2, 0.3},
		Found:  true,
	}, nil
}

func (m *mockVectorSvc) DeleteVector(_ context.Context, _ *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
	if m.deleteVecErr != nil {
		return nil, m.deleteVecErr
	}
	return &service.DeleteVectorResponse{Success: true}, nil
}

func (m *mockVectorSvc) BatchUpsert(_ context.Context, req *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
	if m.batchUpsertErr != nil {
		return nil, m.batchUpsertErr
	}
	ids := make([]uint64, len(req.Vectors))
	versions := make([]uint64, len(req.Vectors))
	for i, v := range req.Vectors {
		ids[i] = v.ID
		versions[i] = 1
	}
	return &service.BatchUpsertResponse{
		IDs:           ids,
		Versions:      versions,
		Success:       true,
		InsertedCount: len(req.Vectors),
	}, nil
}

func (m *mockVectorSvc) BatchDeleteVectors(_ context.Context, req *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
	if m.batchDelErr != nil {
		return nil, m.batchDelErr
	}
	return &service.BatchDeleteVectorsResponse{DeletedCount: len(req.IDs), Success: true}, nil
}

func (m *mockVectorSvc) ListVectors(_ context.Context, _ *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
	if m.listVecErr != nil {
		return nil, m.listVecErr
	}
	return &service.ListVectorsResponse{
		Vectors: []*service.VectorData{
			{ID: 1, Vector: []float32{0.1, 0.2}, Version: 1},
			{ID: 2, Vector: []float32{0.3, 0.4}, Version: 1},
		},
		TotalCount: 2,
	}, nil
}

func (m *mockVectorSvc) UpdateMetadata(_ context.Context, _ *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
	if m.updateMetaErr != nil {
		return nil, m.updateMetaErr
	}
	return &service.UpdateVectorMetadataResponse{ID: 1, Version: 2, Success: true}, nil
}

func (m *mockVectorSvc) SearchSimilar(_ context.Context, _ *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
	if m.searchSimErr != nil {
		return nil, m.searchSimErr
	}
	return &service.SearchSimilarResponse{
		Results: []*service.VectorSearchResult{
			{ID: 1, Score: 0.95, Rank: 1, Distance: 0.05},
		},
	}, nil
}

func (m *mockVectorSvc) SearchByMetadata(_ context.Context, _ *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
	if m.searchMetaErr != nil {
		return nil, m.searchMetaErr
	}
	return &service.SearchByMetadataResponse{
		Results:    []*service.VectorData{{ID: 1, Vector: []float32{0.1}}},
		TotalCount: 1,
	}, nil
}

func (m *mockVectorSvc) HybridSearch(_ context.Context, _ *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
	if m.hybridErr != nil {
		return nil, m.hybridErr
	}
	return &service.HybridSearchResponse{
		Results:         []*service.VectorSearchResult{{ID: 1, Score: 0.9, Rank: 1}},
		TotalCandidates: 100,
		TotalFiltered:   1,
	}, nil
}

func (m *mockVectorSvc) SearchByID(_ context.Context, req *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
	if m.searchByIDErr != nil {
		return nil, m.searchByIDErr
	}
	return &service.SearchByIDResponse{
		SourceID: req.ID,
		Results:  []*service.VectorSearchResult{{ID: 2, Score: 0.85, Rank: 1}},
	}, nil
}

func (m *mockVectorSvc) RebuildIndex(_ context.Context, _ *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
	if m.rebuildErr != nil {
		return nil, m.rebuildErr
	}
	return &service.RebuildIndexResponse{JobID: "job-1", Status: "started", Success: true}, nil
}

func (m *mockVectorSvc) GetRebuildProgress(_ context.Context, _ *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
	if m.rebuildProgErr != nil {
		return nil, m.rebuildProgErr
	}
	return &service.GetRebuildProgressResponse{
		JobID:           "job-1",
		Namespace:       "ns1",
		Status:          "running",
		PercentComplete: 50.0,
	}, nil
}

func (m *mockVectorSvc) GetIndexStats(_ context.Context, _ *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
	if m.indexStatsErr != nil {
		return nil, m.indexStatsErr
	}
	return &service.GetIndexStatsResponse{
		Namespace:   "ns1",
		IndexType:   "HNSW",
		VectorCount: 100,
		Dimension:   128,
	}, nil
}

func (m *mockVectorSvc) CompactNamespace(_ context.Context, _ *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
	if m.compactErr != nil {
		return nil, m.compactErr
	}
	return &service.CompactNamespaceResponse{OperationID: "op-1", Success: true}, nil
}

func (m *mockVectorSvc) GetCompactionProgress(_ context.Context, _ *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
	if m.compactProgErr != nil {
		return nil, m.compactProgErr
	}
	return &service.GetCompactionProgressResponse{
		OperationID:     "op-1",
		Namespace:       "ns1",
		PercentComplete: 75.0,
	}, nil
}

func connectVectorInProcess(t *testing.T, svc service.VectorServicer) (*Transport, func()) {
	t.Helper()
	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		mcpapi.WithVector(svc),
	)
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if connErr := tr.Connect(context.Background()); connErr != nil {
		t.Fatalf("Connect: %v", connErr)
	}
	return tr, func() { _ = tr.Close() }
}

// ---------------------------------------------------------------------------
// CreateNamespace / DeleteNamespace / ListNamespaces / GetNamespaceInfo
// ---------------------------------------------------------------------------

func TestCreateNamespace_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.CreateNamespace(context.Background(), &transport.CreateNamespaceRequest{
		Namespace:      "ns1",
		Dimension:      128,
		DistanceMetric: "L2",
		IndexType:      "HNSW",
	})
	if err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if resp.Namespace != "ns1" {
		t.Errorf("namespace: %q", resp.Namespace)
	}
}

func TestCreateNamespace_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.createNSErr = errors.New("already exists")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.CreateNamespace(context.Background(), &transport.CreateNamespaceRequest{Namespace: "x", Dimension: 1}); err == nil {
		t.Fatal("expected error")
	}
}

func TestUpdateNamespace_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.UpdateNamespace(context.Background(), &transport.UpdateNamespaceRequest{
		Namespace:   "ns",
		Dimension:   4,
		IndexConfig: map[string]string{"ef_search": "128"},
		Metadata:    map[string]interface{}{"owner": "search"},
	})
	if err != nil {
		t.Fatalf("UpdateNamespace: %v", err)
	}
	if !resp.Success || !resp.RequiresRebuild || resp.Namespace != "ns" || resp.Timestamp != 1700000000 {
		t.Fatalf("resp = %+v", resp)
	}
	got := svc.lastUpdateNS
	if got == nil || got.Dimension != 4 || got.IndexConfig["ef_search"] != "128" || got.Metadata["owner"] != "search" {
		t.Fatalf("service saw %+v", got)
	}
}

func TestUpdateNamespace_TypedRefusals(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	svc.updateNSErr = &service.NamespaceNotFoundError{Namespace: "missing"}
	_, err := tr.UpdateNamespace(context.Background(), &transport.UpdateNamespaceRequest{Namespace: "missing"})
	if nf, ok := errors.AsType[*transport.NamespaceNotFoundError](err); !ok || nf.Namespace != "missing" {
		t.Fatalf("err = %v (%T), want *NamespaceNotFoundError", err, err)
	}

	svc.updateNSErr = &service.DimensionMismatchError{Expected: 4, Actual: 8}
	_, err = tr.UpdateNamespace(context.Background(), &transport.UpdateNamespaceRequest{Namespace: "ns", Dimension: 8})
	if dm, ok := errors.AsType[*transport.DimensionMismatchError](err); !ok || dm.Expected != 4 || dm.Got != 8 {
		t.Fatalf("err = %v (%T), want *DimensionMismatchError 4/8", err, err)
	}
}

func TestDeleteNamespace_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.DeleteNamespace(context.Background(), &transport.DeleteNamespaceRequest{Namespace: "ns1"})
	if err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}
	if !resp.Success {
		t.Error("expected success")
	}
}

func TestListNamespaces_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.ListNamespaces(context.Background(), &transport.ListNamespacesRequest{})
	if err != nil {
		t.Fatalf("ListNamespaces: %v", err)
	}
	if len(resp.Namespaces) != 1 {
		t.Fatalf("namespaces: %d", len(resp.Namespaces))
	}
	if resp.Namespaces[0].IndexType != transport.IndexHNSW {
		t.Errorf("index type: %q, want %q", resp.Namespaces[0].IndexType, transport.IndexHNSW)
	}
}

func TestGetNamespaceInfo_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.GetNamespaceInfo(context.Background(), &transport.GetNamespaceInfoRequest{Namespace: "ns1"})
	if err != nil {
		t.Fatalf("GetNamespaceInfo: %v", err)
	}
	if resp.Namespace != "ns1" {
		t.Errorf("namespace: %q", resp.Namespace)
	}
	if resp.IndexType != transport.IndexHNSW {
		t.Errorf("index type: %q, want %q", resp.IndexType, transport.IndexHNSW)
	}
}

// TestGetNamespaceInfo_DistanceMetricNormalized proves a metric the server
// reports in either case comes back as the SDK constant every other
// transport returns.
func TestGetNamespaceInfo_DistanceMetricNormalized(t *testing.T) {
	for reported, want := range map[string]transport.DistanceMetric{
		"L2":        transport.DistanceEuclidean,
		"MANHATTAN": transport.DistanceManhattan,
		"manhattan": transport.DistanceManhattan,
	} {
		svc := newMockVectorSvc()
		svc.nsInfoMetric = reported
		tr, cleanup := connectVectorInProcess(t, svc)
		resp, err := tr.GetNamespaceInfo(context.Background(), &transport.GetNamespaceInfoRequest{Namespace: "ns1"})
		cleanup()
		if err != nil {
			t.Fatalf("GetNamespaceInfo with %q: %v", reported, err)
		}
		if resp.DistanceMetric != want {
			t.Errorf("GetNamespaceInfo with %q: metric %q, want %q", reported, resp.DistanceMetric, want)
		}
	}
}

// TestGetNamespaceInfo_UnknownDistanceMetricRefused proves a metric the SDK
// does not know is refused with the typed error, never read as cosine.
func TestGetNamespaceInfo_UnknownDistanceMetricRefused(t *testing.T) {
	svc := newMockVectorSvc()
	svc.nsInfoMetric = "CHEBYSHEV"
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.GetNamespaceInfo(context.Background(), &transport.GetNamespaceInfoRequest{Namespace: "ns1"})
	var unknown *transport.UnknownDistanceMetricError
	if !errors.As(err, &unknown) || unknown.Metric != "CHEBYSHEV" {
		t.Fatalf("GetNamespaceInfo = %+v, %v; want *UnknownDistanceMetricError{CHEBYSHEV}", resp, err)
	}
}

// ---------------------------------------------------------------------------
// UpsertVector / GetVector / DeleteVector
// ---------------------------------------------------------------------------

func TestUpsertVector_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.UpsertVector(context.Background(), &transport.UpsertVectorRequest{
		Namespace: "ns1",
		ID:        42,
		Vector:    []float32{0.1, 0.2, 0.3},
	})
	if err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	if resp.ID != 42 {
		t.Errorf("id: %d", resp.ID)
	}
}

func TestGetVector_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.GetVector(context.Background(), &transport.GetVectorRequest{
		Namespace: "ns1",
		ID:        42,
	})
	if err != nil {
		t.Fatalf("GetVector: %v", err)
	}
	if !resp.Found {
		t.Error("expected found=true")
	}
}

func TestDeleteVector_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.DeleteVector(context.Background(), &transport.DeleteVectorRequest{
		Namespace: "ns1",
		ID:        42,
	})
	if err != nil {
		t.Fatalf("DeleteVector: %v", err)
	}
	if !resp.Success {
		t.Error("expected success")
	}
}

// ---------------------------------------------------------------------------
// BatchUpsert / BatchDeleteVectors / ListVectors
// ---------------------------------------------------------------------------

func TestBatchUpsert_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.BatchUpsert(context.Background(), &transport.BatchUpsertRequest{
		Namespace: "ns1",
		Vectors: []*transport.VectorData{
			{ID: 1, Vector: []float32{0.1}},
			{ID: 2, Vector: []float32{0.2}},
		},
	})
	if err != nil {
		t.Fatalf("BatchUpsert: %v", err)
	}
	if resp.InsertedCount != 2 {
		t.Errorf("inserted: %d", resp.InsertedCount)
	}
}

func TestBatchDeleteVectors_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.BatchDeleteVectors(context.Background(), &transport.BatchDeleteVectorsRequest{
		Namespace: "ns1",
		IDs:       []uint64{1, 2, 3},
	})
	if err != nil {
		t.Fatalf("BatchDeleteVectors: %v", err)
	}
	if resp.DeletedCount != 3 {
		t.Errorf("deleted: %d", resp.DeletedCount)
	}
}

func TestListVectors_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.ListVectors(context.Background(), &transport.ListVectorsRequest{Namespace: "ns1"})
	if err != nil {
		t.Fatalf("ListVectors: %v", err)
	}
	if len(resp.Vectors) != 2 {
		t.Errorf("vectors: %d", len(resp.Vectors))
	}
}

// ---------------------------------------------------------------------------
// SearchSimilar / SearchByMetadata / HybridSearch
// ---------------------------------------------------------------------------

func TestSearchSimilar_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.SearchSimilar(context.Background(), &transport.SearchSimilarRequest{
		Namespace:   "ns1",
		QueryVector: []float32{0.1, 0.2},
		K:           10,
	})
	if err != nil {
		t.Fatalf("SearchSimilar: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Errorf("results: %d", len(resp.Results))
	}
}

func TestSearchByMetadata_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.SearchByMetadata(context.Background(), &transport.SearchByMetadataRequest{
		Namespace: "ns1",
		Filters:   map[string]interface{}{"tag": "foo"},
	})
	if err != nil {
		t.Fatalf("SearchByMetadata: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Errorf("results: %d", len(resp.Results))
	}
}

func TestHybridSearch_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.HybridSearch(context.Background(), &transport.HybridSearchRequest{
		Namespace:      "ns1",
		QueryVector:    []float32{0.1},
		K:              10,
		MetadataFilter: map[string]interface{}{"k": "v"},
	})
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Errorf("results: %d", len(resp.Results))
	}
}

// ---------------------------------------------------------------------------
// SearchByID / UpdateMetadata
// ---------------------------------------------------------------------------

func TestSearchByID_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.SearchByID(context.Background(), &transport.SearchByIDRequest{
		Namespace: "ns1",
		ID:        42,
		K:         5,
	})
	if err != nil {
		t.Fatalf("SearchByID: %v", err)
	}
	if resp.SourceID != 42 {
		t.Errorf("source id: %d", resp.SourceID)
	}
}

func TestUpdateMetadata_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.UpdateMetadata(context.Background(), &transport.UpdateVectorMetadataRequest{
		Namespace: "ns1",
		ID:        1,
		Metadata:  map[string]interface{}{"k": "v"},
	})
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if !resp.Success {
		t.Error("expected success")
	}
}

// ---------------------------------------------------------------------------
// RebuildIndex / GetRebuildProgress / GetIndexStats
// ---------------------------------------------------------------------------

func TestRebuildIndex_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.RebuildIndex(context.Background(), &transport.RebuildIndexRequest{Namespace: "ns1"})
	if err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	if resp.JobID == "" {
		t.Error("expected non-empty job id")
	}
}

func TestGetRebuildProgress_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.GetRebuildProgress(context.Background(), &transport.GetRebuildProgressRequest{Namespace: "ns1", JobID: "job-1"})
	if err != nil {
		t.Fatalf("GetRebuildProgress: %v", err)
	}
	if resp.JobID != "job-1" {
		t.Errorf("job id: %q", resp.JobID)
	}
}

func TestGetIndexStats_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.GetIndexStats(context.Background(), &transport.GetIndexStatsRequest{Namespace: "ns1"})
	if err != nil {
		t.Fatalf("GetIndexStats: %v", err)
	}
	if resp.Namespace != "ns1" {
		t.Errorf("namespace: %q", resp.Namespace)
	}
	if resp.IndexType != transport.IndexHNSW {
		t.Errorf("index type: %q, want %q", resp.IndexType, transport.IndexHNSW)
	}
}

// ---------------------------------------------------------------------------
// CompactNamespace / GetCompactionProgress
// ---------------------------------------------------------------------------

func TestCompactNamespace_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.CompactNamespace(context.Background(), &transport.CompactNamespaceRequest{Namespace: "ns1"})
	if err != nil {
		t.Fatalf("CompactNamespace: %v", err)
	}
	if resp.OperationID == "" {
		t.Error("expected non-empty operation id")
	}
}

func TestGetCompactionProgress_Success(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.GetCompactionProgress(context.Background(), &transport.GetCompactionProgressRequest{Namespace: "ns1", OperationID: "op-1"})
	if err != nil {
		t.Fatalf("GetCompactionProgress: %v", err)
	}
	if resp.OperationID != "op-1" {
		t.Errorf("operation id: %q", resp.OperationID)
	}
}

// ---------------------------------------------------------------------------
// Conversion helpers
// ---------------------------------------------------------------------------

func TestServerVectorSearchResultToTransport(t *testing.T) {
	in := &serverVectorSearchResult{
		ID:       42,
		Score:    0.9,
		Vector:   []float32{0.1, 0.2},
		Metadata: map[string]interface{}{"k": "v"},
		Distance: 0.1,
		Rank:     1,
	}
	out := serverVectorSearchResultToTransport(in)
	if out.ID != 42 || out.Score != 0.9 || out.Distance != 0.1 || out.Rank != 1 {
		t.Errorf("conversion: %+v", out)
	}
}

func TestServerVectorDataToTransport(t *testing.T) {
	in := &serverVectorData{
		ID:        10,
		Vector:    []float32{0.5},
		Metadata:  map[string]interface{}{"k": "v"},
		Version:   2,
		CreatedAt: 100,
		UpdatedAt: 200,
	}
	out := serverVectorDataToTransport(in)
	if out.ID != 10 || out.Version != 2 || out.CreatedAt != 100 || out.UpdatedAt != 200 {
		t.Errorf("conversion: %+v", out)
	}
}

func TestServerNamespaceInfoToTransport(t *testing.T) {
	in := &serverNamespaceInfo{
		Namespace:      "ns1",
		Dimension:      128,
		VectorCount:    100,
		DistanceMetric: "L2",
		IndexType:      "HNSW",
		IndexConfig:    map[string]string{"M": "16"},
		Metadata:       map[string]string{"k": "v"},
		CreatedAt:      100,
		UpdatedAt:      200,
	}
	out := serverNamespaceInfoToTransport(in)
	if out.Namespace != "ns1" || out.Dimension != 128 || out.VectorCount != 100 {
		t.Errorf("conversion: %+v", out)
	}
}

// ---------------------------------------------------------------------------
// Vector service error tests for additional coverage
// ---------------------------------------------------------------------------

func TestDeleteNamespace_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.deleteNSErr = errors.New("not found")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.DeleteNamespace(context.Background(), &transport.DeleteNamespaceRequest{Namespace: "x"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestListNamespaces_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.listNSErr = errors.New("list failed")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.ListNamespaces(context.Background(), &transport.ListNamespacesRequest{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestGetNamespaceInfo_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.getNSInfoErr = errors.New("not found")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.GetNamespaceInfo(context.Background(), &transport.GetNamespaceInfoRequest{Namespace: "x"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestUpsertVector_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.upsertErr = errors.New("upsert failed")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.UpsertVector(context.Background(), &transport.UpsertVectorRequest{
		Namespace: "x", ID: 1, Vector: []float32{0.1},
	}); err == nil {
		t.Fatal("expected error")
	}
}

func TestGetVector_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.getVecErr = errors.New("internal error")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.GetVector(context.Background(), &transport.GetVectorRequest{Namespace: "x", ID: 1}); err == nil {
		t.Fatal("expected error")
	}
}

func TestDeleteVector_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.deleteVecErr = errors.New("not found")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.DeleteVector(context.Background(), &transport.DeleteVectorRequest{Namespace: "x", ID: 1}); err == nil {
		t.Fatal("expected error")
	}
}

func TestBatchUpsert_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.batchUpsertErr = errors.New("batch failed")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.BatchUpsert(context.Background(), &transport.BatchUpsertRequest{
		Namespace: "x", Vectors: []*transport.VectorData{{ID: 1, Vector: []float32{0.1}}},
	}); err == nil {
		t.Fatal("expected error")
	}
}

func TestBatchDeleteVectors_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.batchDelErr = errors.New("batch failed")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.BatchDeleteVectors(context.Background(), &transport.BatchDeleteVectorsRequest{
		Namespace: "x", IDs: []uint64{1},
	}); err == nil {
		t.Fatal("expected error")
	}
}

func TestListVectors_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.listVecErr = errors.New("list failed")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.ListVectors(context.Background(), &transport.ListVectorsRequest{Namespace: "x"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestSearchSimilar_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.searchSimErr = errors.New("search failed")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.SearchSimilar(context.Background(), &transport.SearchSimilarRequest{
		Namespace: "x", QueryVector: []float32{0.1}, K: 5,
	}); err == nil {
		t.Fatal("expected error")
	}
}

func TestSearchByMetadata_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.searchMetaErr = errors.New("search failed")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.SearchByMetadata(context.Background(), &transport.SearchByMetadataRequest{
		Namespace: "x", Filters: map[string]interface{}{"k": "v"},
	}); err == nil {
		t.Fatal("expected error")
	}
}

func TestHybridSearch_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.hybridErr = errors.New("search failed")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.HybridSearch(context.Background(), &transport.HybridSearchRequest{
		Namespace: "x", QueryVector: []float32{0.1}, K: 5,
		MetadataFilter: map[string]interface{}{"k": "v"},
	}); err == nil {
		t.Fatal("expected error")
	}
}

func TestSearchByID_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.searchByIDErr = errors.New("search failed")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.SearchByID(context.Background(), &transport.SearchByIDRequest{
		Namespace: "x", ID: 1, K: 5,
	}); err == nil {
		t.Fatal("expected error")
	}
}

func TestUpdateMetadata_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.updateMetaErr = errors.New("update failed")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.UpdateMetadata(context.Background(), &transport.UpdateVectorMetadataRequest{
		Namespace: "x", ID: 1, Metadata: map[string]interface{}{"k": "v"},
	}); err == nil {
		t.Fatal("expected error")
	}
}

func TestRebuildIndex_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.rebuildErr = errors.New("rebuild failed")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.RebuildIndex(context.Background(), &transport.RebuildIndexRequest{Namespace: "x"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestGetRebuildProgress_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.rebuildProgErr = errors.New("progress failed")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.GetRebuildProgress(context.Background(), &transport.GetRebuildProgressRequest{Namespace: "x", JobID: "j"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestGetIndexStats_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.indexStatsErr = errors.New("stats failed")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.GetIndexStats(context.Background(), &transport.GetIndexStatsRequest{Namespace: "x"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCompactNamespace_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.compactErr = errors.New("compact failed")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.CompactNamespace(context.Background(), &transport.CompactNamespaceRequest{Namespace: "x"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestGetCompactionProgress_ServiceError(t *testing.T) {
	svc := newMockVectorSvc()
	svc.compactProgErr = errors.New("progress failed")
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.GetCompactionProgress(context.Background(), &transport.GetCompactionProgressRequest{Namespace: "x", OperationID: "op"}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// Vector tests with metadata and shard ID to exercise optional branches
// ---------------------------------------------------------------------------

func TestUpsertVector_WithMetadataAndShardID(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	_, err := tr.UpsertVector(context.Background(), &transport.UpsertVectorRequest{
		Namespace: "ns1",
		ID:        42,
		Vector:    []float32{0.1},
		Metadata:  map[string]interface{}{"k": "v"},
		ShardID:   7,
	})
	if err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
}

func TestGetVector_WithShardID(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	_, err := tr.GetVector(context.Background(), &transport.GetVectorRequest{
		Namespace: "ns1",
		ID:        42,
		ShardID:   7,
	})
	if err != nil {
		t.Fatalf("GetVector: %v", err)
	}
}

func TestDeleteVector_WithShardID(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	_, err := tr.DeleteVector(context.Background(), &transport.DeleteVectorRequest{
		Namespace: "ns1",
		ID:        42,
		ShardID:   7,
	})
	if err != nil {
		t.Fatalf("DeleteVector: %v", err)
	}
}

func TestCreateNamespace_WithMetadata(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	_, err := tr.CreateNamespace(context.Background(), &transport.CreateNamespaceRequest{
		Namespace:      "ns1",
		Dimension:      128,
		DistanceMetric: "L2",
		IndexType:      "HNSW",
		IndexConfig:    map[string]string{"M": "16"},
		Metadata:       map[string]interface{}{"k": "v"},
		ShardID:        7,
	})
	if err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
}

func TestSearchSimilar_WithFilters(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	_, err := tr.SearchSimilar(context.Background(), &transport.SearchSimilarRequest{
		Namespace:       "ns1",
		QueryVector:     []float32{0.1, 0.2},
		K:               10,
		EfSearch:        100,
		MinScore:        0.5,
		MetadataFilter:  map[string]interface{}{"k": "v"},
		IncludeVectors:  true,
		IncludeMetadata: true,
	})
	if err != nil {
		t.Fatalf("SearchSimilar: %v", err)
	}
}

func TestHybridSearch_WithReranking(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	_, err := tr.HybridSearch(context.Background(), &transport.HybridSearchRequest{
		Namespace:          "ns1",
		QueryVector:        []float32{0.1},
		K:                  10,
		EfSearch:           100,
		MinScore:           0.5,
		MetadataFilter:     map[string]interface{}{"k": "v"},
		FilterOperator:     "AND",
		RerankingAlgorithm: "weighted",
		IncludeVectors:     true,
		IncludeMetadata:    true,
	})
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
}

func TestBatchUpsert_WithMetadata(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	_, err := tr.BatchUpsert(context.Background(), &transport.BatchUpsertRequest{
		Namespace: "ns1",
		Vectors: []*transport.VectorData{
			{ID: 1, Vector: []float32{0.1}, Metadata: map[string]interface{}{"k": "v"}},
		},
	})
	if err != nil {
		t.Fatalf("BatchUpsert: %v", err)
	}
}

func TestListVectors_WithOptions(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	_, err := tr.ListVectors(context.Background(), &transport.ListVectorsRequest{
		Namespace:       "ns1",
		Limit:           50,
		Cursor:          "abc",
		IDPrefix:        "pre",
		IncludeVectors:  true,
		IncludeMetadata: true,
	})
	if err != nil {
		t.Fatalf("ListVectors: %v", err)
	}
}

func TestRebuildIndex_WithConfig(t *testing.T) {
	svc := newMockVectorSvc()
	tr, cleanup := connectVectorInProcess(t, svc)
	defer cleanup()

	_, err := tr.RebuildIndex(context.Background(), &transport.RebuildIndexRequest{
		Namespace:   "ns1",
		IndexConfig: map[string]string{"M": "32"},
	})
	if err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
}
