// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package vector

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewPebbleVectorStorage_LoadNamespacesSuccess tests successful namespace loading
func TestNewPebbleVectorStorage_LoadNamespacesSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(tmpDir, 0o750))
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	log := &testLogger{}

	// Create storage and add namespaces
	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	require.NotNil(t, storage)

	ctx := context.Background()
	nsInfo := &NamespaceInfo{
		Name:      "test_ns",
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, nsInfo)
	require.NoError(t, err)

	// Allocate some IDs
	id1, err := storage.NextID("test_ns")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), id1)

	id2, err := storage.NextID("test_ns")
	require.NoError(t, err)
	assert.Equal(t, uint64(2), id2)

	_ = storage.Close()

	// Reopen storage to trigger loadNamespaces with existing data
	storage2, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	require.NotNil(t, storage2)
	defer func() { _ = storage2.Close() }()

	// Verify namespace was loaded
	info, err := storage2.GetNamespaceInfo(ctx, "test_ns")
	require.NoError(t, err)
	require.Equal(t, "test_ns", info.Name)

	// Verify ID sequence was loaded
	id3, err := storage2.NextID("test_ns")
	require.NoError(t, err)
	assert.Greater(t, id3, uint64(2))
}

// TestUpsertVector_ContextCancellation tests context cancellation during upsert
func TestUpsertVector_ContextCancellation(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	ctx := context.Background()
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Create a cancelled context
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	vec := &VectorValue{
		ID:        1,
		Embedding: make([]float32, 128),
		Version:   1,
	}

	// Fill all write semaphore slots to force context check
	for i := 0; i < storage.config.MaxConcurrentWrites; i++ {
		storage.writeSem <- struct{}{}
	}

	// Try to upsert with cancelled context
	err := storage.UpsertVector(cancelCtx, ns, vec)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrContextCanceled)

	// Clear semaphore
	for i := 0; i < storage.config.MaxConcurrentWrites; i++ {
		<-storage.writeSem
	}
}

// TestUpsertVector_UpdateWithMetadataCleanup tests metadata index cleanup on update
func TestUpsertVector_UpdateWithMetadataCleanup(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Insert vector with metadata
	vec := &VectorValue{
		ID:        1,
		Embedding: make([]float32, 128),
		Metadata: map[string]string{
			"category": "old",
			"color":    "red",
		},
		Version: 1,
	}
	err := storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	// Update vector with different metadata (version 2)
	vec.Version = 2
	vec.Metadata = map[string]string{
		"category": "new",
		"color":    "blue",
	}
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	// Verify new metadata
	retrieved, err := storage.GetVector(ctx, ns, 1)
	require.NoError(t, err)
	assert.Equal(t, "new", retrieved.Metadata["category"])
	assert.Equal(t, "blue", retrieved.Metadata["color"])
}

// TestGetVector_ContextCancellation tests context cancellation during get
func TestGetVector_ContextCancellation(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	ctx := context.Background()
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Create a cancelled context
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// Fill all read semaphore slots
	for i := 0; i < storage.config.MaxConcurrentReads; i++ {
		storage.readSem <- struct{}{}
	}

	// Try to get with cancelled context
	_, err := storage.GetVector(cancelCtx, ns, 1)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrContextCanceled)

	// Clear semaphore
	for i := 0; i < storage.config.MaxConcurrentReads; i++ {
		<-storage.readSem
	}
}

// TestDeleteVector_ContextCancellation tests context cancellation during delete
func TestDeleteVector_ContextCancellation(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	ctx := context.Background()
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Create a cancelled context
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// Fill all write semaphore slots
	for i := 0; i < storage.config.MaxConcurrentWrites; i++ {
		storage.writeSem <- struct{}{}
	}

	// Try to delete with cancelled context
	err := storage.DeleteVector(cancelCtx, ns, 1)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrContextCanceled)

	// Clear semaphore
	for i := 0; i < storage.config.MaxConcurrentWrites; i++ {
		<-storage.writeSem
	}
}

// TestBatchUpsertVectors_ContextCancellation tests context cancellation during batch upsert
func TestBatchUpsertVectors_ContextCancellation(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	ctx := context.Background()
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	vecs := []*VectorValue{
		{ID: 1, Embedding: make([]float32, 128), Version: 1},
	}

	// Fill all write semaphore slots
	for i := 0; i < storage.config.MaxConcurrentWrites; i++ {
		storage.writeSem <- struct{}{}
	}

	err := storage.BatchUpsertVectors(cancelCtx, ns, vecs)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrContextCanceled)

	// Clear semaphore
	for i := 0; i < storage.config.MaxConcurrentWrites; i++ {
		<-storage.writeSem
	}
}

// TestBatchDeleteVectors_ContextCancellation tests context cancellation during batch delete
func TestBatchDeleteVectors_ContextCancellation(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	ctx := context.Background()
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	ids := []uint64{1, 2, 3}

	// Fill all write semaphore slots
	for i := 0; i < storage.config.MaxConcurrentWrites; i++ {
		storage.writeSem <- struct{}{}
	}

	err := storage.BatchDeleteVectors(cancelCtx, ns, ids)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrContextCanceled)

	// Clear semaphore
	for i := 0; i < storage.config.MaxConcurrentWrites; i++ {
		<-storage.writeSem
	}
}

// TestCleanupTombstones_Comprehensive tests tombstone cleanup
func TestCleanupTombstones_Comprehensive(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Insert and delete vectors to create tombstones
	for i := uint64(1); i <= 5; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: make([]float32, 128),
			Version:   1,
		}
		err := storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)

		err = storage.DeleteVector(ctx, ns, i)
		require.NoError(t, err)
	}

	// Clean up old tombstones
	err := storage.CleanupTombstones(ctx, 0)
	assert.NoError(t, err)

	// Clean up tombstones older than 1 hour (should not clean recent ones)
	err = storage.CleanupTombstones(ctx, 1*time.Hour)
	assert.NoError(t, err)
}

// TestCleanupTombstones_NoNamespaces tests cleanup with no namespaces
func TestCleanupTombstones_NoNamespaces(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	err := storage.CleanupTombstones(ctx, 1*time.Hour)
	assert.NoError(t, err)
}

// TestCompact_Success tests manual compaction
func TestCompact_Success(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Insert vectors
	for i := uint64(1); i <= 10; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: make([]float32, 128),
			Version:   1,
		}
		err := storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)
	}

	// Compact the namespace
	err := storage.Compact(ctx, ns)
	assert.NoError(t, err)
}

// TestBatchAllocateIDs_Success tests batch ID allocation
func TestBatchAllocateIDs_Success(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Allocate batch of IDs
	ids, err := storage.BatchAllocateIDs(ns, 5)
	require.NoError(t, err)
	assert.Len(t, ids, 5)
	assert.Equal(t, uint64(1), ids[0])
	assert.Equal(t, uint64(5), ids[4])

	// Allocate more IDs
	ids2, err := storage.BatchAllocateIDs(ns, 3)
	require.NoError(t, err)
	assert.Len(t, ids2, 3)
	assert.Equal(t, uint64(6), ids2[0])
	assert.Equal(t, uint64(8), ids2[2])
}

// TestScanNamespace_WithOptions tests scan with various options
func TestScanNamespace_WithOptions(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Insert 10 vectors
	for i := uint64(1); i <= 10; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: make([]float32, 128),
			Version:   1,
		}
		err := storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)
	}

	// Test limit beyond results
	results, err := storage.ScanNamespace(ctx, ns, &ScanOptions{Limit: 100})
	require.NoError(t, err)
	assert.Len(t, results, 10)

	// Test offset + limit within bounds
	results, err = storage.ScanNamespace(ctx, ns, &ScanOptions{Offset: 5, Limit: 3})
	require.NoError(t, err)
	assert.Len(t, results, 3)

	// Test offset at boundary
	results, err = storage.ScanNamespace(ctx, ns, &ScanOptions{Offset: 9})
	require.NoError(t, err)
	assert.Len(t, results, 1)
}

// TestScanByMultipleFilters_WithOptions tests multiple filter scan with options
func TestScanByMultipleFilters_WithOptions(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Insert vectors with metadata
	for i := uint64(1); i <= 10; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: make([]float32, 128),
			Metadata: map[string]string{
				"category": "test",
				"id":       fmt.Sprintf("%d", i),
			},
			Version: 1,
		}
		err := storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)
	}

	// Test with limit beyond results
	filters := []MetadataFilter{
		{Field: "category", Value: "test", Operator: FilterEqual},
	}
	results, err := storage.ScanByMultipleFilters(ctx, ns, filters, &ScanOptions{Limit: 100})
	require.NoError(t, err)
	assert.Len(t, results, 10)

	// Test FilterNotEqual operator
	filters = []MetadataFilter{
		{Field: "category", Value: "test", Operator: FilterEqual},
		{Field: "id", Value: "5", Operator: FilterNotEqual},
	}
	results, err = storage.ScanByMultipleFilters(ctx, ns, filters, nil)
	require.NoError(t, err)
	assert.Len(t, results, 9)

	// Test with offset and limit
	filters = []MetadataFilter{
		{Field: "category", Value: "test", Operator: FilterEqual},
	}
	results, err = storage.ScanByMultipleFilters(ctx, ns, filters, &ScanOptions{Offset: 3, Limit: 4})
	require.NoError(t, err)
	assert.Len(t, results, 4)
}

// TestScanByMultipleFilters_NoFilters tests scan with no filters
func TestScanByMultipleFilters_NoFilters(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Insert vectors
	for i := uint64(1); i <= 5; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: make([]float32, 128),
			Version:   1,
		}
		err := storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)
	}

	// Scan with no filters (should call ScanNamespace)
	results, err := storage.ScanByMultipleFilters(ctx, ns, []MetadataFilter{}, nil)
	require.NoError(t, err)
	assert.Len(t, results, 5)
}

// TestClose_MultipleCalls tests multiple close calls
func TestClose_MultipleCalls(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(tmpDir, 0o750))
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)

	// Close once
	err = storage.Close()
	assert.NoError(t, err)

	// Close again (should be no-op)
	err = storage.Close()
	assert.NoError(t, err)

	// Verify storage is closed
	assert.True(t, storage.closed.Load())
}

// TestOperationsOnClosedStorage tests all operations on closed storage
func TestOperationsOnClosedStorage(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(tmpDir, 0o750))
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)

	// Close storage
	err = storage.Close()
	require.NoError(t, err)

	ctx := context.Background()

	// Test all operations on closed storage
	t.Run("UpsertVector", func(t *testing.T) {
		err := storage.UpsertVector(ctx, "ns", &VectorValue{ID: 1})
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("GetVector", func(t *testing.T) {
		_, err := storage.GetVector(ctx, "ns", 1)
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("DeleteVector", func(t *testing.T) {
		err := storage.DeleteVector(ctx, "ns", 1)
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("BatchUpsertVectors", func(t *testing.T) {
		err := storage.BatchUpsertVectors(ctx, "ns", []*VectorValue{{ID: 1}})
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("BatchDeleteVectors", func(t *testing.T) {
		err := storage.BatchDeleteVectors(ctx, "ns", []uint64{1})
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("ScanByMetadata", func(t *testing.T) {
		_, err := storage.ScanByMetadata(ctx, "ns", "field", "value", nil)
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("ScanByMultipleFilters", func(t *testing.T) {
		_, err := storage.ScanByMultipleFilters(ctx, "ns", []MetadataFilter{}, nil)
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("ScanNamespace", func(t *testing.T) {
		_, err := storage.ScanNamespace(ctx, "ns", nil)
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("CreateNamespace", func(t *testing.T) {
		err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: "ns"})
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("DeleteNamespace", func(t *testing.T) {
		err := storage.DeleteNamespace(ctx, "ns")
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("ListNamespaces", func(t *testing.T) {
		_, err := storage.ListNamespaces(ctx)
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("GetNamespaceInfo", func(t *testing.T) {
		_, err := storage.GetNamespaceInfo(ctx, "ns")
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("GetStats", func(t *testing.T) {
		_, err := storage.GetStats(ctx, "ns")
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("NextID", func(t *testing.T) {
		_, err := storage.NextID("ns")
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("BatchAllocateIDs", func(t *testing.T) {
		_, err := storage.BatchAllocateIDs("ns", 5)
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("Compact", func(t *testing.T) {
		err := storage.Compact(ctx, "ns")
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("CleanupTombstones", func(t *testing.T) {
		err := storage.CleanupTombstones(ctx, 1*time.Hour)
		assert.Equal(t, ErrStorageClosed, err)
	})
}

// TestDeleteNamespace_WithVectors tests deleting namespace with vectors
func TestDeleteNamespace_WithVectors(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Insert vectors with metadata
	for i := uint64(1); i <= 5; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: make([]float32, 128),
			Metadata: map[string]string{
				"category": "test",
			},
			Version: 1,
		}
		err := storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)
	}

	// Delete namespace
	err := storage.DeleteNamespace(ctx, ns)
	assert.NoError(t, err)

	// Verify namespace is gone
	_, err = storage.GetNamespaceInfo(ctx, ns)
	assert.Equal(t, ErrNamespaceNotFound, err)
}

// TestScanByMetadata_WithPagination tests scan with pagination
func TestScanByMetadata_WithPagination(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Insert vectors with metadata
	for i := uint64(1); i <= 10; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: make([]float32, 128),
			Metadata: map[string]string{
				"category": "test",
			},
			Version: 1,
		}
		err := storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)
	}

	// Scan with offset and limit
	results, err := storage.ScanByMetadata(ctx, ns, "category", "test", &ScanOptions{Offset: 3, Limit: 4})
	require.NoError(t, err)
	assert.Len(t, results, 4)
}

// TestScanNamespace_MultipleVersions tests scanning with multiple versions
func TestScanNamespace_MultipleVersions(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Insert vector with version 1
	vec := &VectorValue{
		ID:        1,
		Embedding: make([]float32, 128),
		Metadata: map[string]string{
			"version": "1",
		},
		Version: 1,
	}
	err := storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	// Update to version 2
	vec.Version = 2
	vec.Metadata["version"] = "2"
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	// Update to version 3
	vec.Version = 3
	vec.Metadata["version"] = "3"
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	// Scan namespace (should only return latest version)
	results, err := storage.ScanNamespace(ctx, ns, nil)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, uint64(3), results[0].Version)
	assert.Equal(t, "3", results[0].Metadata["version"])
}
