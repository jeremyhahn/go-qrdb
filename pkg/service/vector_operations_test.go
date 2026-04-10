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

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dragonErrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// =============================================================================
// Test Helper: Create a VectorService with a pre-populated namespace
// =============================================================================

func newVecSvcWithNamespace(t *testing.T, namespace string, dimension uint32) (VectorService, *vecMockStorage) {
	t.Helper()
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{
			{
				Name:           namespace,
				Dimension:      dimension,
				VectorCount:    0,
				DistanceMetric: "L2",
				IndexType:      "HNSW",
				CreatedAt:      time.Now().UnixNano(),
				UpdatedAt:      time.Now().UnixNano(),
			},
		}, nil
	}

	storage.getNamespaceInfoFn = func(ctx context.Context, ns string) (*vector.NamespaceInfo, error) {
		if ns == namespace {
			return &vector.NamespaceInfo{
				Name:           namespace,
				Dimension:      dimension,
				VectorCount:    0,
				DistanceMetric: "L2",
				IndexType:      "HNSW",
				CreatedAt:      time.Now().UnixNano(),
				UpdatedAt:      time.Now().UnixNano(),
			}, nil
		}
		return nil, errors.New("namespace not found")
	}

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)
	return svc, storage
}

func newBasicVecSvc(t *testing.T) (VectorService, *vecMockStorage) {
	t.Helper()
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)
	return svc, storage
}

// =============================================================================
// CreateNamespace - Coverage paths
// These tests exercise the full CreateNamespace code path including storage
// creation, metadata conversion, distance metric defaulting, index type
// defaulting, and index config parsing. Without the vector build tag,
// HNSW index creation fails after storage write, which is expected.
// =============================================================================

func TestVecCreateNamespace_MetadataConversionAndStorageWrite(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	var createdInfo *vector.NamespaceInfo
	storage.createNamespaceFn = func(ctx context.Context, info *vector.NamespaceInfo) error {
		createdInfo = info
		return nil
	}

	// This exercises: validation, closed check, already-exists check (miss),
	// metadata conversion, distance metric defaulting, index type defaulting,
	// storage.CreateNamespace call, then HNSW creation (which may fail without tag)
	_, _ = svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace:      "meta-ns",
		Dimension:      256,
		DistanceMetric: "COSINE",
		IndexType:      "HNSW",
		Metadata:       map[string]interface{}{"description": "test namespace"},
	})

	// Verify storage was called with converted metadata
	require.NotNil(t, createdInfo)
	assert.Equal(t, "meta-ns", createdInfo.Name)
	assert.Equal(t, uint32(256), createdInfo.Dimension)
	assert.Equal(t, "COSINE", createdInfo.DistanceMetric)
	assert.Equal(t, "HNSW", createdInfo.IndexType)
	assert.Equal(t, "test namespace", createdInfo.Metadata["description"])
}

func TestVecCreateNamespace_IndexConfigParsing(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	var createdInfo *vector.NamespaceInfo
	storage.createNamespaceFn = func(ctx context.Context, info *vector.NamespaceInfo) error {
		createdInfo = info
		return nil
	}

	// Exercise index config parsing with valid and invalid values
	_, _ = svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "idx-ns",
		Dimension: 128,
		IndexConfig: map[string]string{
			"m":               "32",
			"ef_construction": "400",
			"ef_search":       "100",
		},
	})

	require.NotNil(t, createdInfo)
	assert.Equal(t, "32", createdInfo.IndexConfig["m"])
	assert.Equal(t, "400", createdInfo.IndexConfig["ef_construction"])
}

func TestVecCreateNamespace_InvalidIndexConfigParsing(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	var createdInfo *vector.NamespaceInfo
	storage.createNamespaceFn = func(ctx context.Context, info *vector.NamespaceInfo) error {
		createdInfo = info
		return nil
	}

	// Exercise invalid index config parsing (logs warnings but continues)
	_, _ = svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "bad-idx-ns",
		Dimension: 128,
		IndexConfig: map[string]string{
			"m":               "not-a-number",
			"ef_construction": "also-bad",
			"ef_search":       "nope",
		},
	})

	require.NotNil(t, createdInfo)
	assert.Equal(t, "not-a-number", createdInfo.IndexConfig["m"])
}

func TestVecCreateNamespace_DefaultDistanceMetricAndIndexType(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	var createdInfo *vector.NamespaceInfo
	storage.createNamespaceFn = func(ctx context.Context, info *vector.NamespaceInfo) error {
		createdInfo = info
		return nil
	}

	// Empty DistanceMetric and IndexType should default to "L2" and "HNSW"
	_, _ = svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace:      "default-ns",
		Dimension:      64,
		DistanceMetric: "",
		IndexType:      "",
	})

	require.NotNil(t, createdInfo)
	assert.Equal(t, "L2", createdInfo.DistanceMetric)
	assert.Equal(t, "HNSW", createdInfo.IndexType)
}

func TestVecCreateNamespace_ServiceClosed(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	concreteSvc := svc.(*vectorService)
	concreteSvc.mu.Lock()
	concreteSvc.closed = true
	concreteSvc.mu.Unlock()

	resp, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "closed-ns",
		Dimension: 128,
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecCreateNamespace_StorageCreateError(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	storage.createNamespaceFn = func(ctx context.Context, info *vector.NamespaceInfo) error {
		return errors.New("disk full")
	}

	resp, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "fail-ns",
		Dimension: 128,
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	var dragonErr *dragonErrors.DragonError
	require.True(t, errors.As(err, &dragonErr))
	assert.Equal(t, dragonErrors.ErrStorage, dragonErr.Code)
}

// =============================================================================
// DeleteNamespace - New coverage paths
// =============================================================================

func TestVecDeleteNamespace_StorageError(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "del-fail", 128)

	storage.deleteNamespaceFn = func(ctx context.Context, ns string) error {
		return errors.New("storage error")
	}

	resp, err := svc.DeleteNamespace(context.Background(), &DeleteNamespaceRequest{
		Namespace: "del-fail",
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecDeleteNamespace_ServiceClosed(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	concreteSvc := svc.(*vectorService)
	concreteSvc.mu.Lock()
	concreteSvc.closed = true
	concreteSvc.mu.Unlock()

	resp, err := svc.DeleteNamespace(context.Background(), &DeleteNamespaceRequest{
		Namespace: "ns",
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecDeleteNamespace_WithVectorCount(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "del-ns", 128)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{
			Name:        "del-ns",
			Dimension:   128,
			VectorCount: 42,
		}, nil
	}
	storage.deleteNamespaceFn = func(ctx context.Context, ns string) error {
		return nil
	}

	resp, err := svc.DeleteNamespace(context.Background(), &DeleteNamespaceRequest{
		Namespace: "del-ns",
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, uint64(42), resp.VectorsDeleted)
}

// =============================================================================
// ListNamespaces - New coverage paths
// =============================================================================

func TestVecListNamespaces_WithPrefix(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{
			{Name: "prod-ns-1", Dimension: 128},
			{Name: "prod-ns-2", Dimension: 128},
			{Name: "dev-ns-1", Dimension: 128},
		}, nil
	}

	resp, err := svc.ListNamespaces(context.Background(), &ListNamespacesRequest{
		Prefix: "prod-",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, resp.TotalCount)
}

func TestVecListNamespaces_Pagination(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		nsList := make([]vector.NamespaceInfo, 5)
		for i := range nsList {
			nsList[i] = vector.NamespaceInfo{Name: "ns-" + string(rune('a'+i)), Dimension: 128}
		}
		return nsList, nil
	}

	resp, err := svc.ListNamespaces(context.Background(), &ListNamespacesRequest{
		Limit:  2,
		Offset: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, 5, resp.TotalCount)
	assert.Len(t, resp.Namespaces, 2)
}

func TestVecListNamespaces_LimitCapping(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.ListNamespaces(context.Background(), &ListNamespacesRequest{
		Limit: 5000,
	})
	require.NoError(t, err)
	assert.NotNil(t, resp)
}

func TestVecListNamespaces_ServiceClosed(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	concreteSvc := svc.(*vectorService)
	concreteSvc.mu.Lock()
	concreteSvc.closed = true
	concreteSvc.mu.Unlock()

	resp, err := svc.ListNamespaces(context.Background(), &ListNamespacesRequest{})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecListNamespaces_OffsetBeyondCount(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{
			{Name: "ns-a", Dimension: 128},
		}, nil
	}

	resp, err := svc.ListNamespaces(context.Background(), &ListNamespacesRequest{
		Offset: 100,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, resp.TotalCount)
	assert.Empty(t, resp.Namespaces)
}

func TestVecListNamespaces_TenantFiltering(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{
			{Name: "ns-tenant-a", TenantID: "tenant-a"},
			{Name: "ns-tenant-b", TenantID: "tenant-b"},
			{Name: "ns-shared", TenantID: ""},
		}, nil
	}

	ctx := tenant.WithTenantID(context.Background(), "tenant-a")
	resp, err := svc.ListNamespaces(ctx, &ListNamespacesRequest{})
	require.NoError(t, err)
	assert.Equal(t, 2, resp.TotalCount)
}

// =============================================================================
// GetNamespaceInfo - New coverage paths
// =============================================================================

func TestVecGetNamespaceInfo_DefaultValues(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{
			Name:      "bare-ns",
			Dimension: 64,
		}, nil
	}

	resp, err := svc.GetNamespaceInfo(context.Background(), &GetNamespaceInfoRequest{
		Namespace: "bare-ns",
	})
	require.NoError(t, err)
	assert.Equal(t, "L2", resp.DistanceMetric)
	assert.Equal(t, "HNSW", resp.IndexType)
	assert.NotNil(t, resp.IndexConfig)
	assert.NotNil(t, resp.Metadata)
}

func TestVecGetNamespaceInfo_NotFound(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.GetNamespaceInfo(context.Background(), &GetNamespaceInfoRequest{
		Namespace: "nonexistent",
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	var dragonErr *dragonErrors.DragonError
	require.True(t, errors.As(err, &dragonErr))
	assert.Equal(t, dragonErrors.ErrNotFound, dragonErr.Code)
}

func TestVecGetNamespaceInfo_ServiceClosed(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	concreteSvc := svc.(*vectorService)
	concreteSvc.mu.Lock()
	concreteSvc.closed = true
	concreteSvc.mu.Unlock()

	resp, err := svc.GetNamespaceInfo(context.Background(), &GetNamespaceInfoRequest{
		Namespace: "ns",
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecGetNamespaceInfo_WithFullData(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{
			Name:           "full-ns",
			Dimension:      128,
			VectorCount:    42,
			DistanceMetric: "COSINE",
			IndexType:      "HNSW",
			Metadata:       map[string]string{"key": "value"},
			IndexConfig:    map[string]string{"m": "16"},
			CreatedAt:      1000,
			UpdatedAt:      2000,
		}, nil
	}

	resp, err := svc.GetNamespaceInfo(context.Background(), &GetNamespaceInfoRequest{
		Namespace: "full-ns",
	})
	require.NoError(t, err)
	assert.Equal(t, "full-ns", resp.Namespace)
	assert.Equal(t, uint32(128), resp.Dimension)
	assert.Equal(t, uint64(42), resp.VectorCount)
	assert.Equal(t, "COSINE", resp.DistanceMetric)
	assert.Equal(t, "value", resp.Metadata["key"])
	assert.Equal(t, "16", resp.IndexConfig["m"])
}

// =============================================================================
// UpsertVector - New coverage paths
// =============================================================================

func TestVecUpsert_WithMetadata(t *testing.T) {
	svc, _ := newVecSvcWithNamespace(t, "vectors", 3)

	resp, err := svc.UpsertVector(context.Background(), &UpsertVectorRequest{
		Namespace: "vectors",
		ID:        2,
		Vector:    []float32{1.0, 2.0, 3.0},
		Metadata:  map[string]interface{}{"label": "test", "score": 42},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestVecUpsert_NamespaceNotFound(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.UpsertVector(context.Background(), &UpsertVectorRequest{
		Namespace: "nonexistent",
		ID:        1,
		Vector:    []float32{1.0, 2.0, 3.0},
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	var dragonErr *dragonErrors.DragonError
	require.True(t, errors.As(err, &dragonErr))
	assert.Equal(t, dragonErrors.ErrNotFound, dragonErr.Code)
}

func TestVecUpsert_DimensionMismatch(t *testing.T) {
	svc, _ := newVecSvcWithNamespace(t, "vectors", 3)

	resp, err := svc.UpsertVector(context.Background(), &UpsertVectorRequest{
		Namespace: "vectors",
		ID:        1,
		Vector:    []float32{1.0, 2.0},
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	var dragonErr *dragonErrors.DragonError
	require.True(t, errors.As(err, &dragonErr))
	assert.Equal(t, dragonErrors.ErrInvalidInput, dragonErr.Code)
}

func TestVecUpsert_StorageError(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "vectors", 3)

	storage.upsertVectorFn = func(ctx context.Context, namespace string, vec *vector.VectorValue) error {
		return errors.New("disk full")
	}

	resp, err := svc.UpsertVector(context.Background(), &UpsertVectorRequest{
		Namespace: "vectors",
		ID:        1,
		Vector:    []float32{1.0, 2.0, 3.0},
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	var dragonErr *dragonErrors.DragonError
	require.True(t, errors.As(err, &dragonErr))
	assert.Equal(t, dragonErrors.ErrStorage, dragonErr.Code)
}

func TestVecUpsert_ServiceClosed(t *testing.T) {
	svc, _ := newVecSvcWithNamespace(t, "vectors", 3)

	concreteSvc := svc.(*vectorService)
	concreteSvc.mu.Lock()
	concreteSvc.closed = true
	concreteSvc.mu.Unlock()

	resp, err := svc.UpsertVector(context.Background(), &UpsertVectorRequest{
		Namespace: "vectors",
		ID:        1,
		Vector:    []float32{1.0, 2.0, 3.0},
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecUpsert_TenantForbidden(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "tenant-ns", 3)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{
			Name:      "tenant-ns",
			Dimension: 3,
			TenantID:  "owner-tenant",
		}, nil
	}

	ctx := tenant.WithTenantID(context.Background(), "other-tenant")
	resp, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "tenant-ns",
		ID:        1,
		Vector:    []float32{1.0, 2.0, 3.0},
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	var dragonErr *dragonErrors.DragonError
	require.True(t, errors.As(err, &dragonErr))
	assert.Equal(t, dragonErrors.ErrForbidden, dragonErr.Code)
}

// =============================================================================
// GetVector - New coverage paths
// =============================================================================

func TestVecGetVector_WithoutVectorData(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "vectors", 3)

	storage.getVectorFn = func(ctx context.Context, namespace string, id uint64) (*vector.VectorValue, error) {
		return &vector.VectorValue{
			ID:        1,
			Embedding: []float32{1.0, 2.0, 3.0},
			Metadata:  map[string]string{"key": "val"},
		}, nil
	}

	resp, err := svc.GetVector(context.Background(), &GetVectorRequest{
		Namespace:       "vectors",
		ID:              1,
		IncludeVector:   false,
		IncludeMetadata: false,
	})
	require.NoError(t, err)
	assert.True(t, resp.Found)
	assert.Nil(t, resp.Vector)
	assert.Nil(t, resp.Metadata)
}

func TestVecGetVector_ServiceClosed(t *testing.T) {
	svc, _ := newVecSvcWithNamespace(t, "vectors", 3)

	concreteSvc := svc.(*vectorService)
	concreteSvc.mu.Lock()
	concreteSvc.closed = true
	concreteSvc.mu.Unlock()

	resp, err := svc.GetVector(context.Background(), &GetVectorRequest{
		Namespace: "vectors",
		ID:        1,
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecGetVector_TenantForbidden(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "tenant-ns", 3)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{
			Name:      "tenant-ns",
			Dimension: 3,
			TenantID:  "owner",
		}, nil
	}

	ctx := tenant.WithTenantID(context.Background(), "attacker")
	resp, err := svc.GetVector(ctx, &GetVectorRequest{
		Namespace: "tenant-ns",
		ID:        1,
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

// =============================================================================
// DeleteVector - New coverage paths
// =============================================================================

func TestVecDeleteVector_StorageError(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "vectors", 3)

	storage.deleteVectorFn = func(ctx context.Context, namespace string, id uint64) error {
		return errors.New("delete error")
	}

	resp, err := svc.DeleteVector(context.Background(), &DeleteVectorRequest{
		Namespace: "vectors",
		ID:        1,
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	var dragonErr *dragonErrors.DragonError
	require.True(t, errors.As(err, &dragonErr))
	assert.Equal(t, dragonErrors.ErrStorage, dragonErr.Code)
}

func TestVecDeleteVector_ServiceClosed(t *testing.T) {
	svc, _ := newVecSvcWithNamespace(t, "vectors", 3)

	concreteSvc := svc.(*vectorService)
	concreteSvc.mu.Lock()
	concreteSvc.closed = true
	concreteSvc.mu.Unlock()

	resp, err := svc.DeleteVector(context.Background(), &DeleteVectorRequest{
		Namespace: "vectors",
		ID:        1,
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecDeleteVector_TenantForbidden(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "tenant-ns", 3)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{
			Name:      "tenant-ns",
			Dimension: 3,
			TenantID:  "owner-tenant",
		}, nil
	}

	ctx := tenant.WithTenantID(context.Background(), "attacker-tenant")
	resp, err := svc.DeleteVector(ctx, &DeleteVectorRequest{
		Namespace: "tenant-ns",
		ID:        1,
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

// =============================================================================
// BatchUpsert - New coverage paths
// =============================================================================

func TestVecBatchUpsert_StorageError(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "batch-ns", 3)

	storage.batchUpsertVectorsFn = func(ctx context.Context, namespace string, vecs []*vector.VectorValue) error {
		return errors.New("batch write failed")
	}

	resp, err := svc.BatchUpsert(context.Background(), &BatchUpsertRequest{
		Namespace: "batch-ns",
		Vectors: []*VectorData{
			{ID: 1, Vector: []float32{1.0, 2.0, 3.0}},
		},
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecBatchUpsert_ServiceClosed(t *testing.T) {
	svc, _ := newVecSvcWithNamespace(t, "batch-ns", 3)

	concreteSvc := svc.(*vectorService)
	concreteSvc.mu.Lock()
	concreteSvc.closed = true
	concreteSvc.mu.Unlock()

	resp, err := svc.BatchUpsert(context.Background(), &BatchUpsertRequest{
		Namespace: "batch-ns",
		Vectors: []*VectorData{
			{ID: 1, Vector: []float32{1.0, 2.0, 3.0}},
		},
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecBatchUpsert_TenantForbidden(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "tenant-ns", 3)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{
			Name:      "tenant-ns",
			Dimension: 3,
			TenantID:  "owner",
		}, nil
	}

	ctx := tenant.WithTenantID(context.Background(), "attacker")
	resp, err := svc.BatchUpsert(ctx, &BatchUpsertRequest{
		Namespace: "tenant-ns",
		Vectors:   []*VectorData{{ID: 1, Vector: []float32{1.0, 2.0, 3.0}}},
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecBatchUpsert_MultipleVectors(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "batch-ns", 3)

	var capturedCount int
	storage.batchUpsertVectorsFn = func(ctx context.Context, namespace string, vecs []*vector.VectorValue) error {
		capturedCount = len(vecs)
		return nil
	}

	resp, err := svc.BatchUpsert(context.Background(), &BatchUpsertRequest{
		Namespace: "batch-ns",
		Vectors: []*VectorData{
			{ID: 1, Vector: []float32{1.0, 2.0, 3.0}},
			{ID: 2, Vector: []float32{4.0, 5.0, 6.0}},
			{ID: 3, Vector: []float32{7.0, 8.0, 9.0}},
		},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, 3, resp.InsertedCount)
	assert.Equal(t, 3, capturedCount)
}

// =============================================================================
// SearchSimilar - New coverage paths
// =============================================================================

func TestVecSearchSimilar_NamespaceNotFoundReturnsEmpty(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.SearchSimilar(context.Background(), &SearchSimilarRequest{
		Namespace:   "nonexistent",
		QueryVector: []float32{1.0, 2.0, 3.0},
		K:           5,
	})
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Empty(t, resp.Results)
}

func TestVecSearchSimilar_ServiceClosed(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	concreteSvc := svc.(*vectorService)
	concreteSvc.mu.Lock()
	concreteSvc.closed = true
	concreteSvc.mu.Unlock()

	resp, err := svc.SearchSimilar(context.Background(), &SearchSimilarRequest{
		Namespace:   "ns",
		QueryVector: []float32{1.0},
		K:           5,
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecSearchSimilar_TenantForbidden(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "tenant-ns", 3)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{
			Name:      "tenant-ns",
			Dimension: 3,
			TenantID:  "owner",
		}, nil
	}

	ctx := tenant.WithTenantID(context.Background(), "attacker")
	resp, err := svc.SearchSimilar(ctx, &SearchSimilarRequest{
		Namespace:   "tenant-ns",
		QueryVector: []float32{1.0, 2.0, 3.0},
		K:           5,
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

// =============================================================================
// SearchByMetadata - New coverage paths
// =============================================================================

func TestVecSearchByMetadata_SuccessWithVectorsAndMetadata(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "meta-ns", 3)

	storage.scanByMultiFiltersFn = func(ctx context.Context, namespace string, filters []vector.MetadataFilter, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		return []*vector.VectorValue{
			{
				ID:        1,
				Embedding: []float32{1.0, 2.0, 3.0},
				Metadata:  map[string]string{"category": "test"},
				Version:   1,
			},
		}, nil
	}

	resp, err := svc.SearchByMetadata(context.Background(), &SearchByMetadataRequest{
		Namespace:       "meta-ns",
		Filters:         map[string]interface{}{"category": "test"},
		IncludeVectors:  true,
		IncludeMetadata: true,
	})
	require.NoError(t, err)
	assert.Len(t, resp.Results, 1)
	assert.Equal(t, []float32{1.0, 2.0, 3.0}, resp.Results[0].Vector)
	assert.Equal(t, "test", resp.Results[0].Metadata["category"])
}

func TestVecSearchByMetadata_StorageError(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "meta-ns", 3)

	storage.scanByMultiFiltersFn = func(ctx context.Context, namespace string, filters []vector.MetadataFilter, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		return nil, errors.New("scan failed")
	}

	resp, err := svc.SearchByMetadata(context.Background(), &SearchByMetadataRequest{
		Namespace: "meta-ns",
		Filters:   map[string]interface{}{"key": "val"},
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecSearchByMetadata_LimitCapping(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "meta-ns", 3)

	var capturedOpts *vector.ScanOptions
	storage.scanByMultiFiltersFn = func(ctx context.Context, namespace string, filters []vector.MetadataFilter, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		capturedOpts = opts
		return nil, nil
	}

	_, err := svc.SearchByMetadata(context.Background(), &SearchByMetadataRequest{
		Namespace: "meta-ns",
		Filters:   map[string]interface{}{"key": "val"},
		Limit:     5000,
	})
	require.NoError(t, err)
	assert.Equal(t, 1000, capturedOpts.Limit)
}

func TestVecSearchByMetadata_DefaultLimit(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "meta-ns", 3)

	var capturedOpts *vector.ScanOptions
	storage.scanByMultiFiltersFn = func(ctx context.Context, namespace string, filters []vector.MetadataFilter, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		capturedOpts = opts
		return nil, nil
	}

	_, err := svc.SearchByMetadata(context.Background(), &SearchByMetadataRequest{
		Namespace: "meta-ns",
		Filters:   map[string]interface{}{"key": "val"},
		Limit:     0,
	})
	require.NoError(t, err)
	assert.Equal(t, 100, capturedOpts.Limit)
}

func TestVecSearchByMetadata_ServiceClosed(t *testing.T) {
	svc, _ := newVecSvcWithNamespace(t, "meta-ns", 3)

	concreteSvc := svc.(*vectorService)
	concreteSvc.mu.Lock()
	concreteSvc.closed = true
	concreteSvc.mu.Unlock()

	resp, err := svc.SearchByMetadata(context.Background(), &SearchByMetadataRequest{
		Namespace: "meta-ns",
		Filters:   map[string]interface{}{"key": "val"},
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecSearchByMetadata_TenantForbidden(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "tenant-ns", 3)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{
			Name:      "tenant-ns",
			Dimension: 3,
			TenantID:  "owner",
		}, nil
	}

	ctx := tenant.WithTenantID(context.Background(), "attacker")
	resp, err := svc.SearchByMetadata(ctx, &SearchByMetadataRequest{
		Namespace: "tenant-ns",
		Filters:   map[string]interface{}{"key": "val"},
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

// =============================================================================
// ListVectors - New coverage paths
// =============================================================================

func TestVecListVectors_Success(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "list-ns", 3)

	storage.scanNamespaceFn = func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		return []*vector.VectorValue{
			{ID: 1, Embedding: []float32{1.0, 2.0, 3.0}, Metadata: map[string]string{"k": "v"}, Version: 1},
			{ID: 2, Embedding: []float32{4.0, 5.0, 6.0}, Version: 2},
		}, nil
	}

	resp, err := svc.ListVectors(context.Background(), &ListVectorsRequest{
		Namespace:       "list-ns",
		IncludeVectors:  true,
		IncludeMetadata: true,
	})
	require.NoError(t, err)
	assert.Len(t, resp.Vectors, 2)
	assert.Equal(t, uint64(1), resp.Vectors[0].ID)
	assert.Equal(t, []float32{1.0, 2.0, 3.0}, resp.Vectors[0].Vector)
	assert.Equal(t, "v", resp.Vectors[0].Metadata["k"])
}

func TestVecListVectors_NamespaceNotFound(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.ListVectors(context.Background(), &ListVectorsRequest{
		Namespace: "nonexistent",
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecListVectors_WithCursor(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "list-ns", 3)

	var capturedOpts *vector.ScanOptions
	storage.scanNamespaceFn = func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		capturedOpts = opts
		return []*vector.VectorValue{{ID: 3, Version: 1}}, nil
	}

	resp, err := svc.ListVectors(context.Background(), &ListVectorsRequest{
		Namespace: "list-ns",
		Cursor:    "10",
		Limit:     5,
	})
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, 10, capturedOpts.Offset)
}

func TestVecListVectors_InvalidCursor(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "list-ns", 3)

	var capturedOpts *vector.ScanOptions
	storage.scanNamespaceFn = func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		capturedOpts = opts
		return nil, nil
	}

	_, err := svc.ListVectors(context.Background(), &ListVectorsRequest{
		Namespace: "list-ns",
		Cursor:    "invalid",
	})
	require.NoError(t, err)
	assert.Equal(t, 0, capturedOpts.Offset)
}

func TestVecListVectors_LimitDefaults(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "list-ns", 3)

	var capturedOpts *vector.ScanOptions
	storage.scanNamespaceFn = func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		capturedOpts = opts
		return nil, nil
	}

	_, err := svc.ListVectors(context.Background(), &ListVectorsRequest{
		Namespace: "list-ns",
		Limit:     0,
	})
	require.NoError(t, err)
	assert.Equal(t, 101, capturedOpts.Limit)
}

func TestVecListVectors_NextCursor(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "list-ns", 3)

	storage.scanNamespaceFn = func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		result := make([]*vector.VectorValue, opts.Limit)
		for i := range result {
			result[i] = &vector.VectorValue{ID: uint64(i + 1), Version: 1}
		}
		return result, nil
	}

	resp, err := svc.ListVectors(context.Background(), &ListVectorsRequest{
		Namespace: "list-ns",
		Limit:     2,
	})
	require.NoError(t, err)
	assert.Len(t, resp.Vectors, 2)
	assert.Equal(t, "2", resp.NextCursor)
}

func TestVecListVectors_ScanError(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "list-ns", 3)

	storage.scanNamespaceFn = func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		return nil, errors.New("scan failed")
	}

	resp, err := svc.ListVectors(context.Background(), &ListVectorsRequest{
		Namespace: "list-ns",
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecListVectors_TenantForbidden(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "tenant-ns", 3)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{
			Name:      "tenant-ns",
			Dimension: 3,
			TenantID:  "owner",
		}, nil
	}

	ctx := tenant.WithTenantID(context.Background(), "attacker")
	resp, err := svc.ListVectors(ctx, &ListVectorsRequest{
		Namespace: "tenant-ns",
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

// =============================================================================
// UpdateMetadata - New coverage paths
// =============================================================================

func TestVecUpdateMetadata_ReplaceSuccess(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "meta-ns", 3)

	storage.getVectorFn = func(ctx context.Context, namespace string, id uint64) (*vector.VectorValue, error) {
		return &vector.VectorValue{
			ID:        1,
			Embedding: []float32{1.0, 2.0, 3.0},
			Metadata:  map[string]string{"old_key": "old_val"},
			Version:   1,
		}, nil
	}

	resp, err := svc.UpdateMetadata(context.Background(), &UpdateVectorMetadataRequest{
		Namespace:     "meta-ns",
		ID:            1,
		Metadata:      map[string]interface{}{"new_key": "new_val"},
		MergeMetadata: false,
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestVecUpdateMetadata_MergeWithDeleteKeys(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "meta-ns", 3)

	storage.getVectorFn = func(ctx context.Context, namespace string, id uint64) (*vector.VectorValue, error) {
		return &vector.VectorValue{
			ID:        1,
			Embedding: []float32{1.0, 2.0, 3.0},
			Metadata:  map[string]string{"existing": "value", "to_delete": "gone"},
			Version:   1,
		}, nil
	}

	resp, err := svc.UpdateMetadata(context.Background(), &UpdateVectorMetadataRequest{
		Namespace:     "meta-ns",
		ID:            1,
		Metadata:      map[string]interface{}{"new_key": "new_val"},
		MergeMetadata: true,
		DeleteKeys:    []string{"to_delete"},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestVecUpdateMetadata_ZeroID(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.UpdateMetadata(context.Background(), &UpdateVectorMetadataRequest{
		Namespace: "ns",
		ID:        0,
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecUpdateMetadata_VectorNotFound(t *testing.T) {
	svc, _ := newVecSvcWithNamespace(t, "meta-ns", 3)

	resp, err := svc.UpdateMetadata(context.Background(), &UpdateVectorMetadataRequest{
		Namespace: "meta-ns",
		ID:        999,
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

// =============================================================================
// SearchByID - New coverage paths
// =============================================================================

func TestVecSearchByID_ZeroID(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.SearchByID(context.Background(), &SearchByIDRequest{
		Namespace: "ns",
		ID:        0,
		K:         5,
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecSearchByID_InvalidK(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.SearchByID(context.Background(), &SearchByIDRequest{
		Namespace: "ns",
		ID:        1,
		K:         0,
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecSearchByID_VectorNotFound(t *testing.T) {
	svc, _ := newVecSvcWithNamespace(t, "ns", 3)

	resp, err := svc.SearchByID(context.Background(), &SearchByIDRequest{
		Namespace: "ns",
		ID:        999,
		K:         5,
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecSearchByID_TenantForbidden(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "tenant-ns", 3)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{
			Name:      "tenant-ns",
			Dimension: 3,
			TenantID:  "owner",
		}, nil
	}

	ctx := tenant.WithTenantID(context.Background(), "attacker")
	resp, err := svc.SearchByID(ctx, &SearchByIDRequest{
		Namespace: "tenant-ns",
		ID:        1,
		K:         5,
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

// =============================================================================
// BatchDeleteVectors - New coverage paths
// =============================================================================

func TestVecBatchDelete_Success(t *testing.T) {
	svc, _ := newVecSvcWithNamespace(t, "batch-del", 3)

	resp, err := svc.BatchDeleteVectors(context.Background(), &BatchDeleteVectorsRequest{
		Namespace: "batch-del",
		IDs:       []uint64{1, 2, 3},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, 3, resp.DeletedCount)
}

func TestVecBatchDelete_FallbackToIndividual(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "batch-del", 3)

	storage.batchDeleteVectorsFn = func(ctx context.Context, namespace string, ids []uint64) error {
		return errors.New("batch not supported")
	}

	resp, err := svc.BatchDeleteVectors(context.Background(), &BatchDeleteVectorsRequest{
		Namespace: "batch-del",
		IDs:       []uint64{1, 2},
	})
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, 2, resp.DeletedCount)
}

func TestVecBatchDelete_FallbackPartialFailure(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "batch-del", 3)

	storage.batchDeleteVectorsFn = func(ctx context.Context, namespace string, ids []uint64) error {
		return errors.New("batch not supported")
	}
	storage.deleteVectorFn = func(ctx context.Context, namespace string, id uint64) error {
		if id == 2 {
			return errors.New("delete failed for id 2")
		}
		return nil
	}

	resp, err := svc.BatchDeleteVectors(context.Background(), &BatchDeleteVectorsRequest{
		Namespace: "batch-del",
		IDs:       []uint64{1, 2, 3},
	})
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Equal(t, 2, resp.DeletedCount)
	assert.Contains(t, resp.FailedIDs, uint64(2))
}

func TestVecBatchDelete_TenantForbidden(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "tenant-ns", 3)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{
			Name:      "tenant-ns",
			Dimension: 3,
			TenantID:  "owner",
		}, nil
	}

	ctx := tenant.WithTenantID(context.Background(), "attacker")
	resp, err := svc.BatchDeleteVectors(ctx, &BatchDeleteVectorsRequest{
		Namespace: "tenant-ns",
		IDs:       []uint64{1},
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

// =============================================================================
// GetIndexStats - New coverage paths
// =============================================================================

func TestVecGetIndexStats_Success(t *testing.T) {
	svc, _ := newVecSvcWithNamespace(t, "stats-ns", 128)

	resp, err := svc.GetIndexStats(context.Background(), &GetIndexStatsRequest{
		Namespace: "stats-ns",
	})
	require.NoError(t, err)
	assert.Equal(t, "stats-ns", resp.Namespace)
	assert.Equal(t, "HNSW", resp.IndexType)
	assert.Equal(t, uint32(128), resp.Dimension)
}

func TestVecGetIndexStats_NotFound(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.GetIndexStats(context.Background(), &GetIndexStatsRequest{
		Namespace: "nonexistent",
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecGetIndexStats_ServiceClosed(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	concreteSvc := svc.(*vectorService)
	concreteSvc.mu.Lock()
	concreteSvc.closed = true
	concreteSvc.mu.Unlock()

	resp, err := svc.GetIndexStats(context.Background(), &GetIndexStatsRequest{
		Namespace: "ns",
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

// =============================================================================
// RebuildIndex - New coverage paths
// =============================================================================

func TestVecRebuildIndex_ServiceClosed(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	concreteSvc := svc.(*vectorService)
	concreteSvc.mu.Lock()
	concreteSvc.closed = true
	concreteSvc.mu.Unlock()

	resp, err := svc.RebuildIndex(context.Background(), &RebuildIndexRequest{
		Namespace: "ns",
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecGetRebuildProgress_NotFound(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.GetRebuildProgress(context.Background(), &GetRebuildProgressRequest{
		Namespace: "nonexistent",
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

// =============================================================================
// CompactNamespace - New coverage paths
// =============================================================================

func TestVecCompactNamespace_ServiceClosed(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	concreteSvc := svc.(*vectorService)
	concreteSvc.mu.Lock()
	concreteSvc.closed = true
	concreteSvc.mu.Unlock()

	resp, err := svc.CompactNamespace(context.Background(), &CompactNamespaceRequest{
		Namespace: "ns",
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVecGetCompactionProgress_NotFound(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.GetCompactionProgress(context.Background(), &GetCompactionProgressRequest{
		Namespace: "nonexistent",
	})
	require.Error(t, err)
	assert.Nil(t, resp)
}

// =============================================================================
// Tenant Access Validation
// =============================================================================

func TestVecTenantAccess_DefaultTenantDeniedCrossTenant(t *testing.T) {
	// In qrdb, tenant.FromContext always returns a non-empty string ("default")
	// when no tenant is explicitly set. The default tenant cannot access
	// a namespace owned by a different tenant.
	svc, storage := newVecSvcWithNamespace(t, "ns", 3)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: "ns", Dimension: 3, TenantID: "some-tenant"}, nil
	}

	resp, err := svc.GetNamespaceInfo(context.Background(), &GetNamespaceInfoRequest{Namespace: "ns"})
	require.Error(t, err)
	assert.Nil(t, resp)

	var dragonErr *dragonErrors.DragonError
	require.True(t, errors.As(err, &dragonErr))
	assert.Equal(t, dragonErrors.ErrForbidden, dragonErr.Code)
}

func TestVecTenantAccess_MatchingTenant(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "ns", 3)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: "ns", Dimension: 3, TenantID: "my-tenant"}, nil
	}

	ctx := tenant.WithTenantID(context.Background(), "my-tenant")
	resp, err := svc.GetNamespaceInfo(ctx, &GetNamespaceInfoRequest{Namespace: "ns"})
	require.NoError(t, err)
	assert.NotNil(t, resp)
}

func TestVecTenantAccess_SharedNamespace(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "ns", 3)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: "ns", Dimension: 3, TenantID: ""}, nil
	}

	ctx := tenant.WithTenantID(context.Background(), "any-tenant")
	resp, err := svc.GetNamespaceInfo(ctx, &GetNamespaceInfoRequest{Namespace: "ns"})
	require.NoError(t, err)
	assert.NotNil(t, resp)
}

// =============================================================================
// HybridSearch - New coverage paths
// =============================================================================

func TestVecHybridSearch_NamespaceNotFoundReturnsEmpty(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.HybridSearch(context.Background(), &HybridSearchRequest{
		Namespace:      "nonexistent",
		QueryVector:    []float32{1.0, 2.0, 3.0},
		K:              5,
		MetadataFilter: map[string]interface{}{"category": "test"},
	})
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Empty(t, resp.Results)
}

// =============================================================================
// WithHNSWIndexFactory Test
// =============================================================================

func TestVecWithHNSWIndexFactory(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger, WithHNSWIndexFactory(nil))
	require.NoError(t, err)
	assert.NotNil(t, svc)
}

// =============================================================================
// Closed State Error Type Validation
// =============================================================================

func TestVecClosedState_AllOperationsReturnProperError(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	concreteSvc := svc.(*vectorService)
	concreteSvc.mu.Lock()
	concreteSvc.closed = true
	concreteSvc.mu.Unlock()

	// Verify multiple operations all return errors when closed
	_, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "ns", Dimension: 128,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vector service not available")

	_, err = svc.UpsertVector(context.Background(), &UpsertVectorRequest{
		Namespace: "ns", ID: 1, Vector: []float32{1.0},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vector service not available")

	_, err = svc.GetVector(context.Background(), &GetVectorRequest{
		Namespace: "ns", ID: 1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vector service not available")

	_, err = svc.DeleteVector(context.Background(), &DeleteVectorRequest{
		Namespace: "ns", ID: 1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vector service not available")

	_, err = svc.BatchUpsert(context.Background(), &BatchUpsertRequest{
		Namespace: "ns",
		Vectors:   []*VectorData{{ID: 1, Vector: []float32{1.0}}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vector service not available")
}
