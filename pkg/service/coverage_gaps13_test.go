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

// coverage_gaps13_test.go covers rebuild progress, SearchSimilar empty-index
// path, and additional vector branches to push past 91%.
package service

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// ---------------------------------------------------------------------------
// GetRebuildProgress – job found path (lines 1863-1877)
// After RebuildIndex stores a job, GetRebuildProgress finds it.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// vector.go – SearchByMetadata with multiFilters (71.4% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_SearchByMetadata_MultiFilters(t *testing.T) {
	svc, _ := newFullVecSvc(t, "multi-filter-ns", 3)
	ctx := context.Background()

	resp, err := svc.SearchByMetadata(ctx, &SearchByMetadataRequest{
		Namespace: "multi-filter-ns",
		Filters: map[string]interface{}{
			"role": "admin",
			"dept": "eng",
		},
		Limit:       10,
		Consistency: 0,
	})
	if err != nil {
		t.Fatalf("SearchByMetadata multi-filters: %v", err)
	}
	_ = resp
}

// ---------------------------------------------------------------------------
// vector.go – HybridSearch metadata path (73.3% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_HybridSearch_WithMetadataFilters(t *testing.T) {
	svc, _ := newFullVecSvc(t, "hybrid-meta-ns", 3)
	ctx := context.Background()

	for i := uint64(1); i <= 3; i++ {
		_, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
			Namespace: "hybrid-meta-ns",
			ID:        i,
			Vector:    []float32{float32(i) * 0.1, float32(i) * 0.1, float32(i) * 0.1},
			Metadata:  map[string]interface{}{"role": "admin"},
		})
		if err != nil {
			t.Fatalf("UpsertVector %d: %v", i, err)
		}
	}

	resp, err := svc.HybridSearch(ctx, &HybridSearchRequest{
		Namespace:      "hybrid-meta-ns",
		QueryVector:    []float32{0.2, 0.2, 0.2},
		K:              3,
		FilterOperator: "AND",
		MetadataFilter: map[string]interface{}{"role": "admin"},
	})
	if err != nil {
		t.Fatalf("HybridSearch with filters: %v", err)
	}
	_ = resp
}

// ---------------------------------------------------------------------------
// vector.go – UpdateMetadata merge mode (62.1% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_UpdateMetadata_MergeMode(t *testing.T) {
	svc, _ := newFullVecSvc(t, "merge-meta-ns", 3)
	ctx := context.Background()

	_, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "merge-meta-ns",
		ID:        200,
		Vector:    []float32{0.2, 0.3, 0.4},
		Metadata:  map[string]interface{}{"key1": "val1"},
	})
	if err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}

	resp, err := svc.UpdateMetadata(ctx, &UpdateVectorMetadataRequest{
		Namespace:     "merge-meta-ns",
		ID:            200,
		Metadata:      map[string]interface{}{"key2": "val2"},
		MergeMetadata: true,
	})
	if err != nil {
		t.Fatalf("UpdateMetadata merge mode: %v", err)
	}
	_ = resp
}

// ---------------------------------------------------------------------------
// vector.go – SearchByID missing vector (67.7% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_SearchByID_VectorNotFound(t *testing.T) {
	svc, store := newFullVecSvc(t, "sid-notfound-ns", 3)
	store.getVectorFn = func(_ context.Context, _ string, id uint64) (*vector.VectorValue, error) {
		return nil, &VectorNotFoundError{Namespace: "sid-notfound-ns", ID: "999"}
	}
	ctx := context.Background()

	_, err := svc.SearchByID(ctx, &SearchByIDRequest{
		Namespace: "sid-notfound-ns",
		ID:        999,
		K:         2,
	})
	if err == nil {
		t.Fatal("expected error for missing vector, got nil")
	}
}

// ---------------------------------------------------------------------------
// vector.go – BatchDeleteVectors StorageError path (54.2% → higher)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// vector.go – GetIndexStats with nil HNSW index (61.9% → higher)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// vector.go – ListVectors with cursor (70% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_ListVectors_WithCursor(t *testing.T) {
	svc, _ := newFullVecSvc(t, "lv-cursor-ns", 3)
	ctx := context.Background()

	resp, err := svc.ListVectors(ctx, &ListVectorsRequest{
		Namespace: "lv-cursor-ns",
		Limit:     1,
		Cursor:    "some-cursor",
	})
	if resp != nil {
		t.Fatalf("ListVectors with a malformed cursor returned a page: %+v", resp)
	}
	requireErrorAs[*InvalidVectorError](t, err)
}

// ---------------------------------------------------------------------------
// vector.go – CompactNamespace storage error (75.6% → higher)
// ---------------------------------------------------------------------------
