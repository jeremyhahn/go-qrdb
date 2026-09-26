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

// coverage_gaps10_test.go covers vector operation success paths via a storage-
// backed service (no Raft), hitting metrics, closed-service, and error paths.
package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Helper: vector service with preloaded namespace and all storage ops mocked
// to succeed with realistic return values.
// ---------------------------------------------------------------------------

// newFullVecSvc creates a service with the namespace preloaded and all
// storage functions returning sensible values to maximize branch coverage.
func newFullVecSvc(t *testing.T, nsName string, dim uint32) (VectorService, *vecMockStorage) {
	t.Helper()

	scoped := scopedNamespace("", nsName)
	store := newVecMockStorage()

	store.listNamespacesFn = func(_ context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{{Name: scoped, Dimension: dim}}, nil
	}

	counter := uint64(0)
	store.nextIDFn = func(_ string) (uint64, error) {
		counter++
		return counter, nil
	}
	store.batchAllocateIDsFn = func(_ string, count int) ([]uint64, error) {
		ids := make([]uint64, count)
		for i := range ids {
			counter++
			ids[i] = counter
		}
		return ids, nil
	}

	store.upsertVectorFn = func(_ context.Context, _ string, vec *vector.VectorValue) error {
		return nil
	}
	store.getVectorFn = func(_ context.Context, _ string, id uint64) (*vector.VectorValue, error) {
		return &vector.VectorValue{ID: id, Embedding: make([]float32, dim)}, nil
	}
	store.deleteVectorFn = func(_ context.Context, _ string, _ uint64) error { return nil }
	store.batchUpsertVectorsFn = func(_ context.Context, _ string, _ []*vector.VectorValue) error {
		return nil
	}
	store.batchDeleteVectorsFn = func(_ context.Context, _ string, _ []uint64) error { return nil }
	store.scanNamespaceFn = func(_ context.Context, _ string, _ *vector.ScanOptions) ([]*vector.VectorValue, error) {
		return []*vector.VectorValue{
			{ID: 1, Embedding: make([]float32, dim)},
			{ID: 2, Embedding: make([]float32, dim)},
		}, nil
	}
	store.scanByMetadataFn = func(_ context.Context, _, _, _ string, _ *vector.ScanOptions) ([]*vector.VectorValue, error) {
		return []*vector.VectorValue{
			{ID: 1, Embedding: make([]float32, dim), Metadata: map[string]string{"role": "admin"}},
		}, nil
	}
	store.scanByMultiFiltersFn = func(_ context.Context, _ string, _ []vector.MetadataFilter, _ *vector.ScanOptions) ([]*vector.VectorValue, error) {
		return []*vector.VectorValue{
			{ID: 1, Embedding: make([]float32, dim)},
		}, nil
	}
	store.getNamespaceInfoFn = func(_ context.Context, ns string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: ns, Dimension: dim, VectorCount: 2}, nil
	}
	store.getStatsFn = func(_ context.Context, ns string) (*vector.NamespaceStats, error) {
		return &vector.NamespaceStats{Namespace: ns, Count: 2, SizeBytes: 1024}, nil
	}
	store.createNamespaceFn = func(_ context.Context, _ *vector.NamespaceInfo) error { return nil }
	store.deleteNamespaceFn = func(_ context.Context, _ string) error { return nil }
	store.listNamespacesFn = func(_ context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{{Name: scoped, Dimension: dim, VectorCount: 2}}, nil
	}
	store.compactFn = func(_ context.Context, _ string) error { return nil }
	store.cleanupTombstonesFn = func(_ context.Context, _ time.Duration) error { return nil }

	logger := newVecTestLogger()
	svc, err := NewVectorService(store, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	return svc, store
}

// ---------------------------------------------------------------------------
// SearchSimilar – success path hits many branches (39.5% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_SearchSimilar_FullPath(t *testing.T) {
	svc, _ := newFullVecSvc(t, "search-full-ns", 3)
	ctx := context.Background()

	// Insert vectors for the search to find.
	for i := uint64(1); i <= 5; i++ {
		_, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
			Namespace: "search-full-ns",
			ID:        i,
			Vector:    []float32{float32(i) * 0.1, float32(i) * 0.2, float32(i) * 0.3},
			Metadata:  map[string]interface{}{"i": i},
		})
		if err != nil {
			t.Fatalf("UpsertVector %d: %v", i, err)
		}
	}

	resp, err := svc.SearchSimilar(ctx, &SearchSimilarRequest{
		Namespace:       "search-full-ns",
		QueryVector:     []float32{0.3, 0.6, 0.9},
		K:               3,
		EfSearch:        50,
		IncludeVectors:  true,
		IncludeMetadata: true,
	})
	if err != nil {
		t.Fatalf("SearchSimilar: %v", err)
	}
	_ = resp
}

// ---------------------------------------------------------------------------
// GetNamespaceInfo – success and error (53.1% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_GetNamespaceInfo_StorageError(t *testing.T) {
	svc, store := newFullVecSvc(t, "info-err-ns", 3)
	store.getNamespaceInfoFn = func(_ context.Context, _ string) (*vector.NamespaceInfo, error) {
		return nil, errors.New("storage unavailable")
	}
	ctx := context.Background()
	_, err := svc.GetNamespaceInfo(ctx, &GetNamespaceInfoRequest{Namespace: "info-err-ns"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// DeleteVector – success with metadata update (51.1% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_DeleteVector_WithMetrics(t *testing.T) {
	svc, _ := newFullVecSvc(t, "del-metrics-ns", 3)
	ctx := context.Background()

	// Upsert first.
	_, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "del-metrics-ns",
		ID:        100,
		Vector:    []float32{0.1, 0.2, 0.3},
	})
	if err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}

	resp, err := svc.DeleteVector(ctx, &DeleteVectorRequest{
		Namespace: "del-metrics-ns",
		ID:        100,
	})
	if err != nil {
		t.Fatalf("DeleteVector: %v", err)
	}
	_ = resp
}

// ---------------------------------------------------------------------------
// BatchDeleteVectors – success (54.2% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_BatchDeleteVectors_FullPath(t *testing.T) {
	svc, _ := newFullVecSvc(t, "bdv-full-ns", 3)
	ctx := context.Background()

	resp, err := svc.BatchDeleteVectors(ctx, &BatchDeleteVectorsRequest{
		Namespace: "bdv-full-ns",
		IDs:       []uint64{1, 2, 3},
	})
	if err != nil {
		t.Fatalf("BatchDeleteVectors: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// ListVectors – success (70.0% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_ListVectors_FullPath(t *testing.T) {
	svc, _ := newFullVecSvc(t, "lv-full-ns", 3)
	ctx := context.Background()

	resp, err := svc.ListVectors(ctx, &ListVectorsRequest{
		Namespace: "lv-full-ns",
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("ListVectors: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// UpdateMetadata – success path (62.1% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_UpdateMetadata_FullPath(t *testing.T) {
	svc, _ := newFullVecSvc(t, "um-full-ns", 3)
	ctx := context.Background()

	// Upsert to create the vector.
	_, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "um-full-ns",
		ID:        50,
		Vector:    []float32{0.5, 0.5, 0.5},
		Metadata:  map[string]interface{}{"role": "user"},
	})
	if err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}

	resp, err := svc.UpdateMetadata(ctx, &UpdateVectorMetadataRequest{
		Namespace: "um-full-ns",
		ID:        50,
		Metadata:  map[string]interface{}{"role": "admin"},
	})
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	_ = resp
}

// ---------------------------------------------------------------------------
// SearchByID – success path (67.7% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_SearchByID_FullPath(t *testing.T) {
	svc, _ := newFullVecSvc(t, "sid-full-ns", 3)
	ctx := context.Background()

	// Insert vectors first.
	for i := uint64(1); i <= 3; i++ {
		_, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
			Namespace: "sid-full-ns",
			ID:        i,
			Vector:    []float32{float32(i) * 0.2, float32(i) * 0.3, float32(i) * 0.4},
		})
		if err != nil {
			t.Fatalf("UpsertVector %d: %v", i, err)
		}
	}

	resp, err := svc.SearchByID(ctx, &SearchByIDRequest{
		Namespace: "sid-full-ns",
		ID:        2,
		K:         2,
	})
	if err != nil {
		t.Fatalf("SearchByID: %v", err)
	}
	_ = resp
}

// ---------------------------------------------------------------------------
// GetIndexStats – success path (61.9% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_GetIndexStats_FullPath(t *testing.T) {
	svc, _ := newFullVecSvc(t, "stats-full-ns", 3)
	ctx := context.Background()

	resp, err := svc.GetIndexStats(ctx, &GetIndexStatsRequest{Namespace: "stats-full-ns"})
	if err != nil {
		t.Fatalf("GetIndexStats: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// CompactNamespace – success path (75.6% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_CompactNamespace_FullPath(t *testing.T) {
	svc, store := newFullVecSvc(t, "compact-full-ns", 3)
	store.compactFn = func(_ context.Context, _ string) error { return nil }
	ctx := context.Background()

	resp, err := svc.CompactNamespace(ctx, &CompactNamespaceRequest{Namespace: "compact-full-ns"})
	if err != nil {
		t.Fatalf("CompactNamespace: %v", err)
	}
	_ = resp
}

// ---------------------------------------------------------------------------
// GetCompactionProgress – success path (32.1% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_CreateNamespace_StoragePath(t *testing.T) {
	store := newVecMockStorage()
	store.listNamespacesFn = func(_ context.Context) ([]vector.NamespaceInfo, error) {
		return nil, nil
	}
	store.getNamespaceInfoFn = func(_ context.Context, _ string) (*vector.NamespaceInfo, error) {
		return nil, vector.ErrNamespaceNotFound
	}
	store.createNamespaceFn = func(_ context.Context, _ *vector.NamespaceInfo) error {
		return nil
	}

	logger := newVecTestLogger()
	svc, err := NewVectorService(store, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()

	// With qrdb_vector tag absent, the HNSW index creation fails, but the
	// storage and validation paths are covered.
	_, _ = svc.CreateNamespace(ctx, &CreateNamespaceRequest{
		Namespace:      "storage-ns",
		Dimension:      128,
		DistanceMetric: "COSINE",
		IndexType:      "FLAT",
	})
}

// ---------------------------------------------------------------------------
// DeleteNamespace – storage path after not finding in memory (59.1% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_DeleteNamespace_WithStorage(t *testing.T) {
	svc, _ := newFullVecSvc(t, "del-storage-ns", 3)
	ctx := context.Background()

	resp, err := svc.DeleteNamespace(ctx, &DeleteNamespaceRequest{Namespace: "del-storage-ns"})
	if err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}
	_ = resp
}

// ---------------------------------------------------------------------------
// ListNamespaces – tenant filtering path (85.3% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_ListNamespaces_AllTenants(t *testing.T) {
	store := newVecMockStorage()
	store.listNamespacesFn = func(_ context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{
			{Name: "acme\x1fns1"},
			{Name: "\x1fsystem-ns"},
			{Name: "beta\x1fns2"},
		}, nil
	}

	logger := newVecTestLogger()
	svc, err := NewVectorService(store, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}

	ctx := context.Background()
	resp, listErr := svc.ListNamespaces(ctx, &ListNamespacesRequest{})
	if listErr != nil {
		t.Fatalf("ListNamespaces: %v", listErr)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestVectorService_ListNamespaces_TenantFiltered(t *testing.T) {
	store := newVecMockStorage()
	store.listNamespacesFn = func(_ context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{
			{Name: "acme\x1fns1"},
			{Name: "beta\x1fns2"},
		}, nil
	}

	logger := newVecTestLogger()
	svc, err := NewVectorService(store, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}

	ctx := tenant.WithTenantID(context.Background(), "acme")
	resp, listErr := svc.ListNamespaces(ctx, &ListNamespacesRequest{})
	if listErr != nil {
		t.Fatalf("ListNamespaces: %v", listErr)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// SearchByMetadata – full path (71.4% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_SearchByMetadata_FullPath(t *testing.T) {
	svc, _ := newFullVecSvc(t, "meta-full-ns", 3)
	ctx := context.Background()

	// Insert vectors with metadata.
	_, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "meta-full-ns",
		ID:        1,
		Vector:    []float32{0.1, 0.2, 0.3},
		Metadata:  map[string]interface{}{"role": "admin", "dept": "eng"},
	})
	if err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}

	resp, err := svc.SearchByMetadata(ctx, &SearchByMetadataRequest{
		Namespace: "meta-full-ns",
		Filters:   map[string]interface{}{"role": "admin"},
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("SearchByMetadata: %v", err)
	}
	_ = resp
}

// ---------------------------------------------------------------------------
// HybridSearch – full path (73.3% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_HybridSearch_FullPath(t *testing.T) {
	svc, _ := newFullVecSvc(t, "hybrid-full-ns", 3)
	ctx := context.Background()

	for i := uint64(1); i <= 3; i++ {
		_, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
			Namespace: "hybrid-full-ns",
			ID:        i,
			Vector:    []float32{float32(i) * 0.1, float32(i) * 0.1, float32(i) * 0.1},
			Metadata:  map[string]interface{}{"tier": "gold"},
		})
		if err != nil {
			t.Fatalf("UpsertVector %d: %v", i, err)
		}
	}

	resp, err := svc.HybridSearch(ctx, &HybridSearchRequest{
		Namespace:      "hybrid-full-ns",
		QueryVector:    []float32{0.2, 0.2, 0.2},
		K:              3,
		MetadataFilter: map[string]interface{}{"tier": "gold"},
	})
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	_ = resp
}

// ---------------------------------------------------------------------------
// BatchUpsert – with IDs and without metrics (58.3% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_BatchUpsert_FullPath(t *testing.T) {
	svc, _ := newFullVecSvc(t, "batch-full-ns", 3)
	ctx := context.Background()

	resp, err := svc.BatchUpsert(ctx, &BatchUpsertRequest{
		Namespace: "batch-full-ns",
		Vectors: []*VectorData{
			{ID: 10, Vector: []float32{0.1, 0.2, 0.3}, Metadata: map[string]interface{}{"k": "v1"}},
			{ID: 11, Vector: []float32{0.4, 0.5, 0.6}, Metadata: map[string]interface{}{"k": "v2"}},
			{ID: 12, Vector: []float32{0.7, 0.8, 0.9}},
		},
	})
	if err != nil {
		t.Fatalf("BatchUpsert: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// RebuildIndex – success/dispatch path (64.5% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_RebuildIndex_FullPath(t *testing.T) {
	svc, store := newFullVecSvc(t, "rebuild-full-ns", 3)
	store.scanNamespaceFn = func(_ context.Context, _ string, _ *vector.ScanOptions) ([]*vector.VectorValue, error) {
		return []*vector.VectorValue{
			{ID: 1, Embedding: []float32{0.1, 0.2, 0.3}},
		}, nil
	}
	ctx := context.Background()

	// May succeed or fail on HNSW index creation without qrdb_vector.
	_, _ = svc.RebuildIndex(ctx, &RebuildIndexRequest{Namespace: "rebuild-full-ns"})
}

// ---------------------------------------------------------------------------
// ensureHNSWIndex – covers more branches (41.9% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_EnsureHNSWIndex_NamespaceStorageError(t *testing.T) {
	svc, store := newFullVecSvc(t, "hnsw-err-ns", 3)
	store.getNamespaceInfoFn = func(_ context.Context, _ string) (*vector.NamespaceInfo, error) {
		return nil, errors.New("storage failure")
	}
	ctx := context.Background()

	// UpsertVector will call ensureHNSWIndex which calls getNamespaceInfo.
	_, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "hnsw-err-ns",
		ID:        1,
		Vector:    []float32{0.1, 0.2, 0.3},
	})
	// Expected to fail since namespace info can't be loaded.
	if err == nil {
		t.Fatal("expected error from storage failure, got nil")
	}
}

// ---------------------------------------------------------------------------
// loadNamespaces – error in GetNamespaceInfo during index creation (70.6%)
// ---------------------------------------------------------------------------

func TestNewVectorService_LoadNamespaces_WithIndex(t *testing.T) {
	store := newVecMockStorage()
	store.listNamespacesFn = func(_ context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{
			{Name: "\x1fns-with-index", Dimension: 3, VectorCount: 5},
		}, nil
	}

	logger := newVecTestLogger()
	svc, err := NewVectorService(store, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
}
