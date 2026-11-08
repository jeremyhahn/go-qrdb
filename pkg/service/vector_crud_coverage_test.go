//go:build qrdb_vector

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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// =============================================================================
// CreateNamespace - Raft Mode Coverage
// =============================================================================

func TestVectorCRUD_CreateNamespace_RaftSuccess(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{}, nil
	}

	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return nil
		},
	}

	svc, err := NewVectorService(storage, logger, WithRaftNode(mockRaft), WithNodeID("node-1"))
	require.NoError(t, err)

	// Namespace does not exist yet (GetNamespaceInfo returns error)
	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return nil, vector.ErrNamespaceNotFound
	}

	resp, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace:      "raft-ns",
		Dimension:      128,
		DistanceMetric: "COSINE",
		IndexType:      "HNSW",
		Metadata:       map[string]interface{}{"owner": "test"},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, "raft-ns", resp.Namespace)
	assert.Equal(t, uint32(128), resp.Dimension)
	assert.NotZero(t, resp.Timestamp)
}

func TestVectorCRUD_CreateNamespace_RaftDefaultMetricAndType(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{}, nil
	}

	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return nil
		},
	}

	svc, err := NewVectorService(storage, logger, WithRaftNode(mockRaft), WithNodeID("node-1"))
	require.NoError(t, err)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return nil, vector.ErrNamespaceNotFound
	}

	// Empty DistanceMetric and IndexType should use defaults
	resp, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "default-ns",
		Dimension: 64,
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestVectorCRUD_CreateNamespace_RaftProposalError(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{}, nil
	}

	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return errors.New("raft unavailable")
		},
	}

	svc, err := NewVectorService(storage, logger, WithRaftNode(mockRaft), WithNodeID("node-1"))
	require.NoError(t, err)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return nil, vector.ErrNamespaceNotFound
	}

	resp, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "fail-ns",
		Dimension: 64,
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*ProposeError](t, err)
}

func TestVectorCRUD_CreateNamespace_AlreadyExistsReturnsError(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "existing-ns", 128)

	// GetNamespaceInfo returns success (namespace exists)
	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{
			Name:      "existing-ns",
			Dimension: 128,
		}, nil
	}

	resp, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "existing-ns",
		Dimension: 128,
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*NamespaceExistsError](t, err)
}

func TestVectorCRUD_CreateNamespace_RaftWithNilMetadata(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{}, nil
	}

	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return nil
		},
	}

	svc, err := NewVectorService(storage, logger, WithRaftNode(mockRaft), WithNodeID("node-1"))
	require.NoError(t, err)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return nil, vector.ErrNamespaceNotFound
	}

	// Nil metadata should not panic
	resp, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "no-meta-ns",
		Dimension: 32,
		Metadata:  nil,
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

// =============================================================================
// DeleteNamespace - Raft Mode and Error Coverage
// =============================================================================

func TestVectorCRUD_DeleteNamespace_RaftSuccess(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	scoped := scopedNamespace(tenantid.System, "to-delete")

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{
			{Name: scoped, Dimension: 64, VectorCount: 10},
		}, nil
	}

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		if namespace == scoped {
			return &vector.NamespaceInfo{
				Name:        scoped,
				Dimension:   64,
				VectorCount: 10,
			}, nil
		}
		return nil, vector.ErrNamespaceNotFound
	}

	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return nil
		},
	}

	svc, err := NewVectorService(storage, logger, WithRaftNode(mockRaft), WithNodeID("node-1"))
	require.NoError(t, err)

	resp, err := svc.DeleteNamespace(context.Background(), &DeleteNamespaceRequest{
		Namespace: "to-delete",
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, uint64(10), resp.VectorsDeleted)
	assert.NotZero(t, resp.Timestamp)
}

func TestVectorCRUD_DeleteNamespace_RaftProposalError(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{}, nil
	}

	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return errors.New("raft down")
		},
	}

	svc, err := NewVectorService(storage, logger, WithRaftNode(mockRaft), WithNodeID("node-1"))
	require.NoError(t, err)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: "ns", VectorCount: 5}, nil
	}

	resp, err := svc.DeleteNamespace(context.Background(), &DeleteNamespaceRequest{
		Namespace: "ns",
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*ProposeError](t, err)
}

func TestVectorCRUD_DeleteNamespace_GetNamespaceInfoFails(t *testing.T) {
	// When GetNamespaceInfo fails, vectorsDeleted should be 0
	svc, storage := newBasicVecSvc(t)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return nil, errors.New("storage error")
	}

	storage.deleteNamespaceFn = func(ctx context.Context, namespace string) error {
		return nil
	}

	resp, err := svc.DeleteNamespace(context.Background(), &DeleteNamespaceRequest{
		Namespace: "unknown-ns",
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, uint64(0), resp.VectorsDeleted)
}

func TestVectorCRUD_DeleteNamespace_StorageDeleteError(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: "ns", VectorCount: 3}, nil
	}

	storage.deleteNamespaceFn = func(ctx context.Context, namespace string) error {
		return errors.New("permission denied")
	}

	resp, err := svc.DeleteNamespace(context.Background(), &DeleteNamespaceRequest{
		Namespace: "ns",
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*VectorOperationError](t, err)
}

// =============================================================================
// UpsertVector - Raft Mode and HNSW Insert Error Coverage
// =============================================================================

func TestVectorCRUD_UpsertVector_RaftSuccess(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	scopedUpsert := scopedNamespace(tenantid.System, "upsert-ns")

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{
			{Name: scopedUpsert, Dimension: 3, DistanceMetric: "L2", IndexType: "HNSW"},
		}, nil
	}

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		if namespace == scopedUpsert {
			return &vector.NamespaceInfo{Name: scopedUpsert, Dimension: 3}, nil
		}
		return nil, vector.ErrNamespaceNotFound
	}

	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return nil
		},
	}

	svc, err := NewVectorService(storage, logger, WithRaftNode(mockRaft), WithNodeID("node-1"))
	require.NoError(t, err)

	resp, err := svc.UpsertVector(context.Background(), &UpsertVectorRequest{
		Namespace: "upsert-ns",
		ID:        42,
		Vector:    []float32{1.0, 2.0, 3.0},
		Metadata:  map[string]interface{}{"tag": "test"},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, uint64(42), resp.ID)
	assert.Equal(t, uint64(1), resp.Version)
}

func TestVectorCRUD_UpsertVector_RaftProposalError(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{}, nil
	}

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: "ns", Dimension: 3}, nil
	}

	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return errors.New("raft timeout")
		},
	}

	svc, err := NewVectorService(storage, logger, WithRaftNode(mockRaft), WithNodeID("node-1"))
	require.NoError(t, err)

	resp, err := svc.UpsertVector(context.Background(), &UpsertVectorRequest{
		Namespace: "ns",
		ID:        1,
		Vector:    []float32{1.0, 2.0, 3.0},
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*ProposeError](t, err)
}

func TestVectorCRUD_UpsertVector_HNSWInsertErrorNonRaft(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "hnsw-ns", 3)

	storage.upsertVectorFn = func(ctx context.Context, namespace string, vec *vector.VectorValue) error {
		return nil
	}

	// Inject a mock HNSW index that fails on insert
	concreteSvc := svc.(*vectorService)
	concreteSvc.indexMu.Lock()
	concreteSvc.hnswIndexes["hnsw-ns"] = &coverageMockHNSWIndex{
		insertFn: func(id uint64, vec []float32) error {
			return errors.New("hnsw insert failed")
		},
	}
	concreteSvc.indexMu.Unlock()

	// Should still succeed - HNSW insert error is logged as warning, not returned
	resp, err := svc.UpsertVector(context.Background(), &UpsertVectorRequest{
		Namespace: "hnsw-ns",
		ID:        1,
		Vector:    []float32{1.0, 2.0, 3.0},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestVectorCRUD_UpsertVector_DimensionZeroAllowsAnyLength(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: "flex-ns", Dimension: 0}, nil
	}

	storage.upsertVectorFn = func(ctx context.Context, namespace string, vec *vector.VectorValue) error {
		return nil
	}

	resp, err := svc.UpsertVector(context.Background(), &UpsertVectorRequest{
		Namespace: "flex-ns",
		ID:        1,
		Vector:    []float32{1.0, 2.0, 3.0, 4.0, 5.0},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

// =============================================================================
// DeleteVector - Raft Mode and HNSW Delete Error Coverage
// =============================================================================

func TestVectorCRUD_DeleteVector_RaftSuccess(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{
			{Name: "del-ns", Dimension: 3},
		}, nil
	}

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: "del-ns", Dimension: 3}, nil
	}

	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return nil
		},
	}

	svc, err := NewVectorService(storage, logger, WithRaftNode(mockRaft), WithNodeID("node-1"))
	require.NoError(t, err)

	resp, err := svc.DeleteVector(context.Background(), &DeleteVectorRequest{
		Namespace: "del-ns",
		ID:        99,
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.NotZero(t, resp.Timestamp)
}

func TestVectorCRUD_DeleteVector_RaftProposalError(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{}, nil
	}

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: "ns", Dimension: 3}, nil
	}

	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return errors.New("raft leader lost")
		},
	}

	svc, err := NewVectorService(storage, logger, WithRaftNode(mockRaft), WithNodeID("node-1"))
	require.NoError(t, err)

	resp, err := svc.DeleteVector(context.Background(), &DeleteVectorRequest{
		Namespace: "ns",
		ID:        1,
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*ProposeError](t, err)
}

func TestVectorCRUD_DeleteVector_RaftWithHNSWDeleteError(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{}, nil
	}

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: "ns", Dimension: 3}, nil
	}

	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return nil
		},
	}

	svc, err := NewVectorService(storage, logger, WithRaftNode(mockRaft), WithNodeID("node-1"))
	require.NoError(t, err)

	concreteSvc := svc.(*vectorService)
	concreteSvc.indexMu.Lock()
	concreteSvc.hnswIndexes["ns"] = &coverageMockHNSWIndex{
		deleteFn: func(id uint64) error {
			return errors.New("hnsw delete failed")
		},
	}
	concreteSvc.indexMu.Unlock()

	// Should still succeed - HNSW delete error is only a warning
	resp, err := svc.DeleteVector(context.Background(), &DeleteVectorRequest{
		Namespace: "ns",
		ID:        1,
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestVectorCRUD_DeleteVector_NonRaftWithHNSWDeleteError(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "del-ns", 3)

	storage.deleteVectorFn = func(ctx context.Context, namespace string, id uint64) error {
		return nil
	}

	concreteSvc := svc.(*vectorService)
	concreteSvc.indexMu.Lock()
	concreteSvc.hnswIndexes["del-ns"] = &coverageMockHNSWIndex{
		deleteFn: func(id uint64) error {
			return errors.New("hnsw internal error")
		},
	}
	concreteSvc.indexMu.Unlock()

	// Should still succeed - HNSW delete error is logged as warning
	resp, err := svc.DeleteVector(context.Background(), &DeleteVectorRequest{
		Namespace: "del-ns",
		ID:        1,
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestVectorCRUD_DeleteVector_NonRaftSuccessWithIndex(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "del-ns", 3)

	storage.deleteVectorFn = func(ctx context.Context, namespace string, id uint64) error {
		return nil
	}

	concreteSvc := svc.(*vectorService)
	concreteSvc.indexMu.Lock()
	concreteSvc.hnswIndexes["del-ns"] = &coverageMockHNSWIndex{
		deleteFn: func(id uint64) error {
			return nil
		},
	}
	concreteSvc.indexMu.Unlock()

	resp, err := svc.DeleteVector(context.Background(), &DeleteVectorRequest{
		Namespace: "del-ns",
		ID:        5,
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

// =============================================================================
// BatchUpsert - Raft Mode and HNSW Insert Error Coverage
// =============================================================================

func TestVectorCRUD_BatchUpsert_RaftSuccess(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{
			{Name: "batch-ns", Dimension: 3},
		}, nil
	}

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: "batch-ns", Dimension: 3}, nil
	}

	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return nil
		},
	}

	svc, err := NewVectorService(storage, logger, WithRaftNode(mockRaft), WithNodeID("node-1"))
	require.NoError(t, err)

	resp, err := svc.BatchUpsert(context.Background(), &BatchUpsertRequest{
		Namespace: "batch-ns",
		Vectors: []*VectorData{
			{ID: 1, Vector: []float32{1.0, 2.0, 3.0}},
			{ID: 2, Vector: []float32{4.0, 5.0, 6.0}},
		},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, 2, resp.InsertedCount)
	assert.Len(t, resp.IDs, 2)
	assert.Len(t, resp.Versions, 2)
}

func TestVectorCRUD_BatchUpsert_RaftProposalError(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{}, nil
	}

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: "ns", Dimension: 3}, nil
	}

	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return errors.New("raft quorum lost")
		},
	}

	svc, err := NewVectorService(storage, logger, WithRaftNode(mockRaft), WithNodeID("node-1"))
	require.NoError(t, err)

	resp, err := svc.BatchUpsert(context.Background(), &BatchUpsertRequest{
		Namespace: "ns",
		Vectors:   []*VectorData{{ID: 1, Vector: []float32{1.0, 2.0, 3.0}}},
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*ProposeError](t, err)
}

func TestVectorCRUD_BatchUpsert_RaftWithHNSWInsertError(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{}, nil
	}

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: "ns", Dimension: 3}, nil
	}

	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return nil
		},
	}

	svc, err := NewVectorService(storage, logger, WithRaftNode(mockRaft), WithNodeID("node-1"))
	require.NoError(t, err)

	concreteSvc := svc.(*vectorService)
	concreteSvc.indexMu.Lock()
	concreteSvc.hnswIndexes["ns"] = &coverageMockHNSWIndex{
		insertFn: func(id uint64, vec []float32) error {
			return errors.New("hnsw insert capacity exceeded")
		},
	}
	concreteSvc.indexMu.Unlock()

	// Should still succeed - HNSW insert errors are just warnings in raft mode
	resp, err := svc.BatchUpsert(context.Background(), &BatchUpsertRequest{
		Namespace: "ns",
		Vectors: []*VectorData{
			{ID: 1, Vector: []float32{1.0, 2.0, 3.0}},
			{ID: 2, Vector: []float32{4.0, 5.0, 6.0}},
		},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, 2, resp.InsertedCount)
}

func TestVectorCRUD_BatchUpsert_NonRaftWithHNSWInsertError(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "batch-ns", 3)

	storage.batchUpsertVectorsFn = func(ctx context.Context, namespace string, vecs []*vector.VectorValue) error {
		return nil
	}

	concreteSvc := svc.(*vectorService)
	concreteSvc.indexMu.Lock()
	concreteSvc.hnswIndexes["batch-ns"] = &coverageMockHNSWIndex{
		insertFn: func(id uint64, vec []float32) error {
			return errors.New("hnsw dimension mismatch")
		},
	}
	concreteSvc.indexMu.Unlock()

	// Should still succeed - HNSW insert errors are just warnings
	resp, err := svc.BatchUpsert(context.Background(), &BatchUpsertRequest{
		Namespace: "batch-ns",
		Vectors: []*VectorData{
			{ID: 1, Vector: []float32{1.0, 2.0, 3.0}},
		},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestVectorCRUD_BatchUpsert_NonRaftWithHNSWInsertSuccess(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "batch-ns", 3)

	storage.batchUpsertVectorsFn = func(ctx context.Context, namespace string, vecs []*vector.VectorValue) error {
		return nil
	}

	concreteSvc := svc.(*vectorService)
	concreteSvc.indexMu.Lock()
	concreteSvc.hnswIndexes["batch-ns"] = &coverageMockHNSWIndex{
		insertFn: func(id uint64, vec []float32) error {
			return nil
		},
	}
	concreteSvc.indexMu.Unlock()

	resp, err := svc.BatchUpsert(context.Background(), &BatchUpsertRequest{
		Namespace: "batch-ns",
		Vectors: []*VectorData{
			{ID: 10, Vector: []float32{1.0, 2.0, 3.0}},
			{ID: 20, Vector: []float32{4.0, 5.0, 6.0}},
		},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, 2, resp.InsertedCount)
}

// =============================================================================
// loadNamespaces - Index Creation Error Coverage
// =============================================================================

func TestVectorCRUD_LoadNamespaces_MultipleNamespacesSuccess(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{
			{Name: "ns-1", Dimension: 3, VectorCount: 5},
			{Name: "ns-2", Dimension: 128, VectorCount: 10},
		}, nil
	}
	// loadNamespaces now builds each namespace's real index through
	// ensureHNSWIndex -- reading its geometry back and scanning its
	// vectors -- rather than pre-registering a blank placeholder, so the
	// mock must answer both calls for the index to be created at all.
	storage.getNamespaceInfoFn = func(_ context.Context, ns string) (*vector.NamespaceInfo, error) {
		switch ns {
		case "ns-1":
			return &vector.NamespaceInfo{Name: "ns-1", Dimension: 3, VectorCount: 5}, nil
		case "ns-2":
			return &vector.NamespaceInfo{Name: "ns-2", Dimension: 128, VectorCount: 10}, nil
		default:
			return nil, vector.ErrNamespaceNotFound
		}
	}
	storage.scanNamespaceFn = func(_ context.Context, _ string, _ *vector.ScanOptions) ([]*vector.VectorValue, error) {
		return nil, nil
	}

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	concreteSvc := svc.(*vectorService)
	concreteSvc.indexMu.RLock()
	assert.Equal(t, 2, len(concreteSvc.hnswIndexes))
	concreteSvc.indexMu.RUnlock()
}

func TestVectorCRUD_LoadNamespaces_ListNamespacesError(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return nil, errors.New("storage unavailable")
	}

	svc, err := NewVectorService(storage, logger)
	require.Error(t, err)
	assert.Nil(t, svc)

	requireErrorAs[*VectorOperationError](t, err)
}

func TestVectorCRUD_LoadNamespaces_EmptyNamespaceList(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{}, nil
	}

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	concreteSvc := svc.(*vectorService)
	concreteSvc.indexMu.RLock()
	assert.Equal(t, 0, len(concreteSvc.hnswIndexes))
	concreteSvc.indexMu.RUnlock()
}

// =============================================================================
// Validate Functions - Uncovered Branches
// =============================================================================

func TestVectorCRUD_ValidateCreateNamespaceRequest_NilRequest(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.CreateNamespace(context.Background(), nil)
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorCRUD_ValidateCreateNamespaceRequest_EmptyNamespace(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "",
		Dimension: 128,
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorCRUD_ValidateCreateNamespaceRequest_ZeroDimension(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "valid-ns",
		Dimension: 0,
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorCRUD_ValidateUpsertVectorRequest_NilRequest(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.UpsertVector(context.Background(), nil)
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorCRUD_ValidateUpsertVectorRequest_EmptyNamespace(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.UpsertVector(context.Background(), &UpsertVectorRequest{
		Namespace: "",
		ID:        1,
		Vector:    []float32{1.0},
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorCRUD_ValidateUpsertVectorRequest_ZeroID(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.UpsertVector(context.Background(), &UpsertVectorRequest{
		Namespace: "ns",
		ID:        0,
		Vector:    []float32{1.0},
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorCRUD_ValidateUpsertVectorRequest_EmptyVector(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.UpsertVector(context.Background(), &UpsertVectorRequest{
		Namespace: "ns",
		ID:        1,
		Vector:    []float32{},
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorCRUD_ValidateGetVectorRequest_NilRequest(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.GetVector(context.Background(), nil)
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorCRUD_ValidateGetVectorRequest_EmptyNamespace(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.GetVector(context.Background(), &GetVectorRequest{
		Namespace: "",
		ID:        1,
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorCRUD_ValidateGetVectorRequest_ZeroID(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.GetVector(context.Background(), &GetVectorRequest{
		Namespace: "ns",
		ID:        0,
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorCRUD_ValidateDeleteVectorRequest_NilRequest(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.DeleteVector(context.Background(), nil)
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorCRUD_ValidateDeleteVectorRequest_EmptyNamespace(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.DeleteVector(context.Background(), &DeleteVectorRequest{
		Namespace: "",
		ID:        1,
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorCRUD_ValidateDeleteVectorRequest_ZeroID(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.DeleteVector(context.Background(), &DeleteVectorRequest{
		Namespace: "ns",
		ID:        0,
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorCRUD_ValidateBatchUpsertRequest_NilRequest(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.BatchUpsert(context.Background(), nil)
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorCRUD_ValidateBatchUpsertRequest_EmptyNamespace(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.BatchUpsert(context.Background(), &BatchUpsertRequest{
		Namespace: "",
		Vectors:   []*VectorData{{ID: 1, Vector: []float32{1.0}}},
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorCRUD_ValidateBatchUpsertRequest_EmptyVectors(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.BatchUpsert(context.Background(), &BatchUpsertRequest{
		Namespace: "ns",
		Vectors:   []*VectorData{},
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorCRUD_ValidateCompactNamespaceRequest_NilRequest(t *testing.T) {
	svc, _ := newVecSvcWithNamespace(t, "compact-ns", 3)

	resp, err := svc.CompactNamespace(context.Background(), nil)
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorCRUD_ValidateCompactNamespaceRequest_EmptyNamespace(t *testing.T) {
	svc, _ := newVecSvcWithNamespace(t, "compact-ns", 3)

	resp, err := svc.CompactNamespace(context.Background(), &CompactNamespaceRequest{
		Namespace: "",
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

// =============================================================================
// DeleteNamespace - Validation Coverage
// =============================================================================

func TestVectorCRUD_ValidateDeleteNamespaceRequest_NilRequest(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.DeleteNamespace(context.Background(), nil)
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorCRUD_ValidateDeleteNamespaceRequest_EmptyNamespace(t *testing.T) {
	svc, _ := newBasicVecSvc(t)

	resp, err := svc.DeleteNamespace(context.Background(), &DeleteNamespaceRequest{
		Namespace: "",
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

// =============================================================================
// Raft Mode - UpsertVector with ensureHNSWIndex Failure
// =============================================================================

func TestVectorCRUD_UpsertVector_RaftEnsureHNSWIndexFailure(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{}, nil
	}

	// GetNamespaceInfo returns an error for the ensureHNSWIndex call
	// but succeeds for the dimension check call
	callCount := 0
	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		callCount++
		if callCount <= 2 {
			return &vector.NamespaceInfo{Name: "ns", Dimension: 3}, nil
		}
		return nil, errors.New("namespace info unavailable")
	}

	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return nil
		},
	}

	svc, err := NewVectorService(storage, logger, WithRaftNode(mockRaft), WithNodeID("node-1"))
	require.NoError(t, err)

	// Should still succeed - ensureHNSWIndex failure only logs a warning
	resp, err := svc.UpsertVector(context.Background(), &UpsertVectorRequest{
		Namespace: "ns",
		ID:        1,
		Vector:    []float32{1.0, 2.0, 3.0},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

// =============================================================================
// Raft Mode - BatchUpsert with ensureHNSWIndex Failure
// =============================================================================

func TestVectorCRUD_BatchUpsert_RaftEnsureHNSWIndexFailure(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{}, nil
	}

	// The batch's own dimension check reads the namespace; the index the
	// write then ensures cannot read it.
	infoCalls := 0
	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		infoCalls++
		if infoCalls == 1 {
			return &vector.NamespaceInfo{Name: namespace, Dimension: 3}, nil
		}
		return nil, errors.New("namespace info unavailable")
	}

	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return nil
		},
	}

	svc, err := NewVectorService(storage, logger, WithRaftNode(mockRaft), WithNodeID("node-1"))
	require.NoError(t, err)

	// Should still succeed - ensureHNSWIndex failure only logs a warning
	resp, err := svc.BatchUpsert(context.Background(), &BatchUpsertRequest{
		Namespace: "ns",
		Vectors: []*VectorData{
			{ID: 1, Vector: []float32{1.0, 2.0, 3.0}},
		},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

// =============================================================================
// Tenant Access - Coverage for CRUD operations with tenant context
// =============================================================================

func TestVectorCRUD_CreateNamespace_TenantAccessAllowed(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return nil, vector.ErrNamespaceNotFound
	}

	storage.createNamespaceFn = func(ctx context.Context, info *vector.NamespaceInfo) error {
		return nil
	}

	resp, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "system-ns",
		Dimension: 64,
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestVectorCRUD_DeleteNamespace_TenantContextNoRestriction(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{
			Name:     "tenant-ns",
			TenantID: "owner-tenant",
		}, nil
	}

	storage.deleteNamespaceFn = func(ctx context.Context, namespace string) error {
		return nil
	}

	resp, err := svc.DeleteNamespace(context.Background(), &DeleteNamespaceRequest{
		Namespace: "tenant-ns",
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

// =============================================================================
// CreateNamespace - Non-Raft with metadata and nil metadata
// =============================================================================

func TestVectorCRUD_CreateNamespace_NonRaftNilMetadata(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return nil, vector.ErrNamespaceNotFound
	}

	storage.createNamespaceFn = func(ctx context.Context, info *vector.NamespaceInfo) error {
		return nil
	}

	resp, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "no-meta",
		Dimension: 32,
		Metadata:  nil,
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestVectorCRUD_CreateNamespace_NonRaftWithMetadata(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return nil, vector.ErrNamespaceNotFound
	}

	var capturedInfo *vector.NamespaceInfo
	storage.createNamespaceFn = func(ctx context.Context, info *vector.NamespaceInfo) error {
		capturedInfo = info
		return nil
	}

	resp, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "meta-ns",
		Dimension: 32,
		Metadata:  map[string]interface{}{"env": "test", "version": 2},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	require.NotNil(t, capturedInfo)
	assert.Equal(t, "test", capturedInfo.Metadata["env"])
	assert.Equal(t, "2", capturedInfo.Metadata["version"])
}

// =============================================================================
// Raft UpsertVector - HNSW Index Insert Error After Raft Success
// =============================================================================

func TestVectorCRUD_UpsertVector_RaftHNSWIndexInsertError(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{
			{Name: "upsert-ns", Dimension: 3},
		}, nil
	}

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: "upsert-ns", Dimension: 3}, nil
	}

	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return nil
		},
	}

	svc, err := NewVectorService(storage, logger, WithRaftNode(mockRaft), WithNodeID("node-1"))
	require.NoError(t, err)

	concreteSvc := svc.(*vectorService)
	concreteSvc.indexMu.Lock()
	concreteSvc.hnswIndexes["upsert-ns"] = &coverageMockHNSWIndex{
		insertFn: func(id uint64, vec []float32) error {
			return errors.New("hnsw capacity exceeded")
		},
	}
	concreteSvc.indexMu.Unlock()

	resp, err := svc.UpsertVector(context.Background(), &UpsertVectorRequest{
		Namespace: "upsert-ns",
		ID:        1,
		Vector:    []float32{1.0, 2.0, 3.0},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

// =============================================================================
// Raft DeleteVector - With HNSW Index Present (Success Path)
// =============================================================================

func TestVectorCRUD_DeleteVector_RaftWithHNSWIndexSuccess(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{}, nil
	}

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: "ns", Dimension: 3}, nil
	}

	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return nil
		},
	}

	svc, err := NewVectorService(storage, logger, WithRaftNode(mockRaft), WithNodeID("node-1"))
	require.NoError(t, err)

	concreteSvc := svc.(*vectorService)
	concreteSvc.indexMu.Lock()
	concreteSvc.hnswIndexes["ns"] = &coverageMockHNSWIndex{
		deleteFn: func(id uint64) error {
			return nil
		},
	}
	concreteSvc.indexMu.Unlock()

	resp, err := svc.DeleteVector(context.Background(), &DeleteVectorRequest{
		Namespace: "ns",
		ID:        42,
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

// =============================================================================
// Tenant context for operations that use validateTenantAccess
// =============================================================================

func TestVectorCRUD_UpsertVector_TenantMatchAllowed(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "tenant-ns", 3)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{
			Name:      "tenant-ns",
			Dimension: 3,
			TenantID:  "my-tenant",
		}, nil
	}

	storage.upsertVectorFn = func(ctx context.Context, namespace string, vec *vector.VectorValue) error {
		return nil
	}

	ctx := tenant.WithTenantID(context.Background(), "my-tenant")
	resp, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "tenant-ns",
		ID:        1,
		Vector:    []float32{1.0, 2.0, 3.0},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestVectorCRUD_DeleteVector_TenantMatchAllowed(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "tenant-ns", 3)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{
			Name:      "tenant-ns",
			Dimension: 3,
			TenantID:  "my-tenant",
		}, nil
	}

	storage.deleteVectorFn = func(ctx context.Context, namespace string, id uint64) error {
		return nil
	}

	ctx := tenant.WithTenantID(context.Background(), "my-tenant")
	resp, err := svc.DeleteVector(ctx, &DeleteVectorRequest{
		Namespace: "tenant-ns",
		ID:        1,
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestVectorCRUD_BatchUpsert_TenantMatchAllowed(t *testing.T) {
	svc, storage := newVecSvcWithNamespace(t, "tenant-ns", 3)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{
			Name:      "tenant-ns",
			Dimension: 3,
			TenantID:  "my-tenant",
		}, nil
	}

	storage.batchUpsertVectorsFn = func(ctx context.Context, namespace string, vecs []*vector.VectorValue) error {
		return nil
	}

	ctx := tenant.WithTenantID(context.Background(), "my-tenant")
	resp, err := svc.BatchUpsert(ctx, &BatchUpsertRequest{
		Namespace: "tenant-ns",
		Vectors:   []*VectorData{{ID: 1, Vector: []float32{1.0, 2.0, 3.0}}},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

// =============================================================================
// Non-Raft HNSW Index Interaction Coverage (when no index exists)
// =============================================================================

func TestVectorCRUD_UpsertVector_NonRaftNoIndex(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: "no-idx", Dimension: 2}, nil
	}

	storage.upsertVectorFn = func(ctx context.Context, namespace string, vec *vector.VectorValue) error {
		return nil
	}

	resp, err := svc.UpsertVector(context.Background(), &UpsertVectorRequest{
		Namespace: "no-idx",
		ID:        1,
		Vector:    []float32{1.0, 2.0},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestVectorCRUD_DeleteVector_NonRaftNoIndex(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: "no-idx", Dimension: 2}, nil
	}

	storage.deleteVectorFn = func(ctx context.Context, namespace string, id uint64) error {
		return nil
	}

	resp, err := svc.DeleteVector(context.Background(), &DeleteVectorRequest{
		Namespace: "no-idx",
		ID:        1,
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestVectorCRUD_BatchUpsert_NonRaftNoIndex(t *testing.T) {
	svc, storage := newBasicVecSvc(t)

	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: "no-idx", Dimension: 2}, nil
	}

	storage.batchUpsertVectorsFn = func(ctx context.Context, namespace string, vecs []*vector.VectorValue) error {
		return nil
	}

	resp, err := svc.BatchUpsert(context.Background(), &BatchUpsertRequest{
		Namespace: "no-idx",
		Vectors:   []*VectorData{{ID: 1, Vector: []float32{1.0, 2.0}}},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}
