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

// coverage_gaps11_test.go covers vector Raft-path code by using a
// mockRaftNode. Most uncovered vector.go blocks are behind raftNode != nil.
package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// ---------------------------------------------------------------------------
// Helper: Raft-enabled vector service with preloaded namespace.
// ---------------------------------------------------------------------------

func newRaftVecSvc(t *testing.T, nsName string, dim uint32) (VectorService, *vecMockStorage) {
	t.Helper()

	scoped := scopedNamespace("", nsName)
	store := newVecMockStorage()
	store.listNamespacesFn = func(_ context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{{Name: scoped, Dimension: dim}}, nil
	}

	// All storage ops succeed.
	store.upsertVectorFn = func(_ context.Context, _ string, _ *vector.VectorValue) error { return nil }
	store.getVectorFn = func(_ context.Context, _ string, id uint64) (*vector.VectorValue, error) {
		return &vector.VectorValue{ID: id, Embedding: make([]float32, dim)}, nil
	}
	store.deleteVectorFn = func(_ context.Context, _ string, _ uint64) error { return nil }
	store.batchUpsertVectorsFn = func(_ context.Context, _ string, _ []*vector.VectorValue) error { return nil }
	store.batchDeleteVectorsFn = func(_ context.Context, _ string, _ []uint64) error { return nil }
	store.scanNamespaceFn = func(_ context.Context, _ string, _ *vector.ScanOptions) ([]*vector.VectorValue, error) {
		return []*vector.VectorValue{{ID: 1, Embedding: make([]float32, dim)}}, nil
	}
	store.scanByMetadataFn = func(_ context.Context, _, _, _ string, _ *vector.ScanOptions) ([]*vector.VectorValue, error) {
		return []*vector.VectorValue{}, nil
	}
	store.scanByMultiFiltersFn = func(_ context.Context, _ string, _ []vector.MetadataFilter, _ *vector.ScanOptions) ([]*vector.VectorValue, error) {
		return []*vector.VectorValue{}, nil
	}
	store.getNamespaceInfoFn = func(_ context.Context, ns string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: ns, Dimension: dim}, nil
	}
	store.getStatsFn = func(_ context.Context, ns string) (*vector.NamespaceStats, error) {
		return &vector.NamespaceStats{Namespace: ns, Count: 1, SizeBytes: 100}, nil
	}
	store.createNamespaceFn = func(_ context.Context, _ *vector.NamespaceInfo) error { return nil }
	store.deleteNamespaceFn = func(_ context.Context, _ string) error { return nil }
	store.compactFn = func(_ context.Context, _ string) error { return nil }
	store.cleanupTombstonesFn = func(_ context.Context, _ time.Duration) error { return nil }

	// Raft node succeeds by default.
	raftNode := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return nil
		},
	}

	logger := newVecTestLogger()
	svc, err := NewVectorService(store, logger,
		WithRaftNode(raftNode),
		WithNodeID("test-node"),
	)
	if err != nil {
		t.Fatalf("NewVectorService with Raft: %v", err)
	}
	return svc, store
}

// ---------------------------------------------------------------------------
// CreateNamespace – Raft path (most uncovered in CreateNamespace at 47.1%)
// ---------------------------------------------------------------------------

func TestVectorService_CreateNamespace_RaftPath(t *testing.T) {
	store := newVecMockStorage()
	store.listNamespacesFn = func(_ context.Context) ([]vector.NamespaceInfo, error) {
		return nil, nil
	}
	store.getNamespaceInfoFn = func(_ context.Context, _ string) (*vector.NamespaceInfo, error) {
		return nil, vector.ErrNamespaceNotFound
	}

	raftNode := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return nil
		},
	}

	logger := newVecTestLogger()
	svc, err := NewVectorService(store, logger, WithRaftNode(raftNode), WithNodeID("node-1"))
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()

	resp, createErr := svc.CreateNamespace(ctx, &CreateNamespaceRequest{
		Namespace:      "raft-create-ns",
		Dimension:      128,
		DistanceMetric: "COSINE",
		IndexType:      "HNSW",
		Metadata:       map[string]interface{}{"owner": "test"},
	})
	if createErr != nil {
		t.Fatalf("CreateNamespace Raft path: %v", createErr)
	}
	if resp == nil || !resp.Success {
		t.Fatal("expected successful response")
	}
}

func TestVectorService_CreateNamespace_RaftError(t *testing.T) {
	store := newVecMockStorage()
	store.listNamespacesFn = func(_ context.Context) ([]vector.NamespaceInfo, error) {
		return nil, nil
	}
	store.getNamespaceInfoFn = func(_ context.Context, _ string) (*vector.NamespaceInfo, error) {
		return nil, vector.ErrNamespaceNotFound
	}

	raftNode := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return errors.New("raft unavailable")
		},
	}

	logger := newVecTestLogger()
	svc, err := NewVectorService(store, logger, WithRaftNode(raftNode), WithNodeID("node-1"))
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()

	_, createErr := svc.CreateNamespace(ctx, &CreateNamespaceRequest{
		Namespace: "raft-err-ns",
		Dimension: 64,
	})
	if createErr == nil {
		t.Fatal("expected error from Raft failure, got nil")
	}
}

// ---------------------------------------------------------------------------
// DeleteNamespace – Raft path (59.1% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_DeleteNamespace_RaftPath(t *testing.T) {
	svc, _ := newRaftVecSvc(t, "raft-del-ns", 3)
	ctx := context.Background()

	resp, err := svc.DeleteNamespace(ctx, &DeleteNamespaceRequest{Namespace: "raft-del-ns"})
	if err != nil {
		t.Fatalf("DeleteNamespace Raft path: %v", err)
	}
	_ = resp
}

func TestVectorService_DeleteNamespace_RaftError(t *testing.T) {
	store := newVecMockStorage()
	scoped := scopedNamespace("", "raft-del-err-ns")
	store.listNamespacesFn = func(_ context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{{Name: scoped, Dimension: 3}}, nil
	}
	store.getNamespaceInfoFn = func(_ context.Context, ns string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: ns, Dimension: 3}, nil
	}

	raftNode := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return errors.New("raft error")
		},
	}

	logger := newVecTestLogger()
	svc, err := NewVectorService(store, logger, WithRaftNode(raftNode), WithNodeID("node-1"))
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()

	_, deleteErr := svc.DeleteNamespace(ctx, &DeleteNamespaceRequest{Namespace: "raft-del-err-ns"})
	if deleteErr == nil {
		t.Fatal("expected error from Raft failure, got nil")
	}
}

// ---------------------------------------------------------------------------
// UpsertVector – Raft path (56.4% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_UpsertVector_RaftPath(t *testing.T) {
	svc, _ := newRaftVecSvc(t, "raft-upsert-ns", 3)
	ctx := context.Background()

	resp, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "raft-upsert-ns",
		ID:        1,
		Vector:    []float32{0.1, 0.2, 0.3},
		Metadata:  map[string]interface{}{"tag": "raft-test"},
	})
	if err != nil {
		t.Fatalf("UpsertVector Raft path: %v", err)
	}
	if resp.ID != 1 {
		t.Fatalf("expected ID=1, got %d", resp.ID)
	}
}

func TestVectorService_UpsertVector_RaftError(t *testing.T) {
	store := newVecMockStorage()
	scoped := scopedNamespace("", "raft-upsert-err-ns")
	store.listNamespacesFn = func(_ context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{{Name: scoped, Dimension: 3}}, nil
	}
	store.getNamespaceInfoFn = func(_ context.Context, ns string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: ns, Dimension: 3}, nil
	}

	raftNode := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return errors.New("raft propose failed")
		},
	}

	logger := newVecTestLogger()
	svc, err := NewVectorService(store, logger, WithRaftNode(raftNode), WithNodeID("node-1"))
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()

	_, upsertErr := svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "raft-upsert-err-ns",
		ID:        1,
		Vector:    []float32{0.1, 0.2, 0.3},
	})
	if upsertErr == nil {
		t.Fatal("expected error from Raft failure, got nil")
	}
}

// ---------------------------------------------------------------------------
// DeleteVector – Raft path (51.1% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_DeleteVector_RaftPath(t *testing.T) {
	svc, _ := newRaftVecSvc(t, "raft-del-vec-ns", 3)
	ctx := context.Background()

	resp, err := svc.DeleteVector(ctx, &DeleteVectorRequest{
		Namespace: "raft-del-vec-ns",
		ID:        1,
	})
	if err != nil {
		t.Fatalf("DeleteVector Raft path: %v", err)
	}
	_ = resp
}

// ---------------------------------------------------------------------------
// BatchUpsert – Raft path (58.3% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_BatchUpsert_RaftPath(t *testing.T) {
	svc, _ := newRaftVecSvc(t, "raft-batch-ns", 3)
	ctx := context.Background()

	resp, err := svc.BatchUpsert(ctx, &BatchUpsertRequest{
		Namespace: "raft-batch-ns",
		Vectors: []*VectorData{
			{ID: 1, Vector: []float32{0.1, 0.2, 0.3}},
			{ID: 2, Vector: []float32{0.4, 0.5, 0.6}},
		},
	})
	if err != nil {
		t.Fatalf("BatchUpsert Raft path: %v", err)
	}
	_ = resp
}

func TestVectorService_BatchUpsert_RaftError(t *testing.T) {
	store := newVecMockStorage()
	scoped := scopedNamespace("", "raft-batch-err-ns")
	store.listNamespacesFn = func(_ context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{{Name: scoped, Dimension: 3}}, nil
	}
	store.getNamespaceInfoFn = func(_ context.Context, ns string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{Name: ns, Dimension: 3}, nil
	}

	raftNode := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return errors.New("raft batch error")
		},
	}

	logger := newVecTestLogger()
	svc, err := NewVectorService(store, logger, WithRaftNode(raftNode), WithNodeID("node-1"))
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()

	_, batchErr := svc.BatchUpsert(ctx, &BatchUpsertRequest{
		Namespace: "raft-batch-err-ns",
		Vectors: []*VectorData{
			{ID: 1, Vector: []float32{0.1, 0.2, 0.3}},
		},
	})
	if batchErr == nil {
		t.Fatal("expected error from Raft failure, got nil")
	}
}

// ---------------------------------------------------------------------------
// BatchDeleteVectors – Raft path (54.2% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_BatchDeleteVectors_RaftPath(t *testing.T) {
	svc, _ := newRaftVecSvc(t, "raft-bdv-ns", 3)
	ctx := context.Background()

	resp, err := svc.BatchDeleteVectors(ctx, &BatchDeleteVectorsRequest{
		Namespace: "raft-bdv-ns",
		IDs:       []uint64{1, 2, 3},
	})
	if err != nil {
		t.Fatalf("BatchDeleteVectors Raft path: %v", err)
	}
	_ = resp
}

// ---------------------------------------------------------------------------
// GetRebuildProgress – non-nil job lookup path (22.9% → higher)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// GetIndexStats – storage path covering uncovered branches (61.9% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_GetIndexStats_WithRaft(t *testing.T) {
	svc, _ := newRaftVecSvc(t, "stats-raft-ns", 3)
	ctx := context.Background()

	resp, err := svc.GetIndexStats(ctx, &GetIndexStatsRequest{Namespace: "stats-raft-ns"})
	if err != nil {
		t.Fatalf("GetIndexStats Raft path: %v", err)
	}
	_ = resp
}

// ---------------------------------------------------------------------------
// UpdateMetadata – Raft-backed path (62.1% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_UpdateMetadata_RaftPath(t *testing.T) {
	svc, _ := newRaftVecSvc(t, "um-raft-ns", 3)
	ctx := context.Background()

	_, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "um-raft-ns",
		ID:        77,
		Vector:    []float32{0.7, 0.8, 0.9},
	})
	if err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}

	resp, err := svc.UpdateMetadata(ctx, &UpdateVectorMetadataRequest{
		Namespace: "um-raft-ns",
		ID:        77,
		Metadata:  map[string]interface{}{"status": "active"},
	})
	if err != nil {
		t.Fatalf("UpdateMetadata Raft path: %v", err)
	}
	_ = resp
}

// ---------------------------------------------------------------------------
// CompactNamespace – Raft path (75.6% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_CompactNamespace_RaftPath(t *testing.T) {
	svc, store := newRaftVecSvc(t, "compact-raft-ns", 3)
	store.compactFn = func(_ context.Context, _ string) error { return nil }
	store.cleanupTombstonesFn = func(_ context.Context, _ time.Duration) error { return nil }
	ctx := context.Background()

	resp, err := svc.CompactNamespace(ctx, &CompactNamespaceRequest{Namespace: "compact-raft-ns"})
	if err != nil {
		t.Fatalf("CompactNamespace Raft path: %v", err)
	}
	_ = resp
}

// ---------------------------------------------------------------------------
// ListVectors – coverage via full storage (70.0% → higher)
// ---------------------------------------------------------------------------

func TestVectorService_ListVectors_RaftPath(t *testing.T) {
	svc, _ := newRaftVecSvc(t, "lv-raft-ns", 3)
	ctx := context.Background()

	resp, err := svc.ListVectors(ctx, &ListVectorsRequest{
		Namespace: "lv-raft-ns",
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("ListVectors Raft path: %v", err)
	}
	_ = resp
}
