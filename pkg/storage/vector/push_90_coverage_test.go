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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUpsertVector_VersionUpdatePath tests updating vectors (version > 1)
func TestUpsertVector_VersionUpdatePath(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	ns := "update_path"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Insert initial vector with metadata
	vec1 := &VectorValue{
		ID:        1,
		Embedding: make([]float32, 128),
		Metadata: map[string]string{
			"field1": "value1",
			"field2": "value2",
		},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
		Version:   1,
	}
	require.NoError(t, storage.UpsertVector(ctx, ns, vec1))

	// Update with version 2 - should delete old metadata indexes
	vec2 := &VectorValue{
		ID:        1,
		Embedding: make([]float32, 128),
		Metadata: map[string]string{
			"field1": "value1_updated",
			"field3": "value3",
		},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
		Version:   2,
	}
	err := storage.UpsertVector(ctx, ns, vec2)
	assert.NoError(t, err)

	// Update with version 3
	vec3 := &VectorValue{
		ID:        1,
		Embedding: make([]float32, 128),
		Metadata: map[string]string{
			"field4": "value4",
		},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
		Version:   3,
	}
	err = storage.UpsertVector(ctx, ns, vec3)
	assert.NoError(t, err)

	// Verify latest version
	retrieved, err := storage.GetVector(ctx, ns, 1)
	assert.NoError(t, err)
	assert.NotNil(t, retrieved)
	assert.Equal(t, uint64(3), retrieved.Version)
}

// TestGetVector_Metrics tests GetVector to ensure metrics paths are covered
func TestGetVector_Metrics(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	ns := "get_metrics"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Insert vector
	vec := &VectorValue{
		ID:        1,
		Embedding: make([]float32, 128),
		Metadata:  map[string]string{"key": "value"},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
		Version:   1,
	}
	require.NoError(t, storage.UpsertVector(ctx, ns, vec))

	// Get vector multiple times to exercise metrics
	for i := 0; i < 5; i++ {
		retrieved, err := storage.GetVector(ctx, ns, 1)
		assert.NoError(t, err)
		assert.NotNil(t, retrieved)
	}
}

// TestDeleteVector_MetadataCleanup tests DeleteVector with metadata cleanup
func TestDeleteVector_MetadataCleanup(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	ns := "delete_meta_cleanup"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Insert vector with extensive metadata
	vec := &VectorValue{
		ID:        1,
		Embedding: make([]float32, 128),
		Metadata: map[string]string{
			"field1": "value1",
			"field2": "value2",
			"field3": "value3",
			"field4": "value4",
			"field5": "value5",
		},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
		Version:   1,
	}
	require.NoError(t, storage.UpsertVector(ctx, ns, vec))

	// Delete should clean up all metadata indexes
	err := storage.DeleteVector(ctx, ns, 1)
	assert.NoError(t, err)

	// Verify deletion
	_, err = storage.GetVector(ctx, ns, 1)
	assert.Error(t, err)
}

// TestBatchUpsertVectors_MixedOps tests batch upsert
func TestBatchUpsertVectors_MixedOps(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	ns := "batch_upsert_mixed"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Create batch of vectors
	vecs := make([]*VectorValue, 10)
	for i := 0; i < 10; i++ {
		vecs[i] = &VectorValue{
			ID:        uint64(i + 1),
			Embedding: make([]float32, 128),
			Metadata: map[string]string{
				"index": string(rune('0' + i)),
				"batch": "true",
			},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
			Version:   1,
		}
	}

	// Batch upsert
	err := storage.BatchUpsertVectors(ctx, ns, vecs)
	assert.NoError(t, err)

	// Verify all vectors were inserted
	for i := 0; i < 10; i++ {
		retrieved, err := storage.GetVector(ctx, ns, uint64(i+1))
		assert.NoError(t, err)
		assert.NotNil(t, retrieved)
	}
}

// TestBatchDeleteVectors_MultipleVecs tests batch delete
func TestBatchDeleteVectors_MultipleVecs(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	ns := "batch_delete_multi"
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
			Metadata:  map[string]string{"index": string(rune('0' + i))},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
			Version:   1,
		}
		require.NoError(t, storage.UpsertVector(ctx, ns, vec))
	}

	// Batch delete half
	ids := []uint64{1, 2, 3, 4, 5}
	err := storage.BatchDeleteVectors(ctx, ns, ids)
	assert.NoError(t, err)

	// Verify deletion
	for _, id := range ids {
		_, err := storage.GetVector(ctx, ns, id)
		assert.Error(t, err)
	}

	// Verify remaining vectors
	for i := uint64(6); i <= 10; i++ {
		retrieved, err := storage.GetVector(ctx, ns, i)
		assert.NoError(t, err)
		assert.NotNil(t, retrieved)
	}
}

// TestScanByMetadata_PaginationPaths tests scanning with limit and offset
func TestScanByMetadata_PaginationPaths(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	ns := "scan_pagination_paths"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Insert vectors with same metadata field
	for i := uint64(1); i <= 20; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: make([]float32, 128),
			Metadata: map[string]string{
				"category": "test",
				"index":    string(rune('0' + i%10)),
			},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
			Version:   1,
		}
		require.NoError(t, storage.UpsertVector(ctx, ns, vec))
	}

	// Scan with limit
	opts := &ScanOptions{Limit: 5}
	results, err := storage.ScanByMetadata(ctx, ns, "category", "test", opts)
	assert.NoError(t, err)
	assert.Len(t, results, 5)

	// Scan with offset
	opts2 := &ScanOptions{Limit: 5, Offset: 5}
	results2, err := storage.ScanByMetadata(ctx, ns, "category", "test", opts2)
	assert.NoError(t, err)
	assert.Len(t, results2, 5)

	// Scan all
	opts3 := &ScanOptions{Limit: -1}
	results3, err := storage.ScanByMetadata(ctx, ns, "category", "test", opts3)
	assert.NoError(t, err)
	assert.Equal(t, 20, len(results3))
}

// TestScanNamespace_PaginationPaths tests namespace scanning
func TestScanNamespace_PaginationPaths(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	ns := "scan_ns_pages"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Insert vectors
	for i := uint64(1); i <= 15; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: make([]float32, 128),
			Metadata:  map[string]string{"id": string(rune('0' + i%10))},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
			Version:   1,
		}
		require.NoError(t, storage.UpsertVector(ctx, ns, vec))
	}

	// Scan with limit
	opts := &ScanOptions{Limit: 10}
	results, err := storage.ScanNamespace(ctx, ns, opts)
	assert.NoError(t, err)
	assert.Len(t, results, 10)

	// Scan with offset
	opts2 := &ScanOptions{Limit: 5, Offset: 10}
	results2, err := storage.ScanNamespace(ctx, ns, opts2)
	assert.NoError(t, err)
	assert.Len(t, results2, 5)
}

// TestCreateNamespace_Paths tests namespace creation paths
func TestCreateNamespace_Paths(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	t.Run("CreateAndVerify", func(t *testing.T) {
		ns := "create_verify"
		nsInfo := &NamespaceInfo{
			Name:      ns,
			Dimension: 256,
			CreatedAt: time.Now().Unix(),
		}
		err := storage.CreateNamespace(ctx, nsInfo)
		assert.NoError(t, err)

		// Verify it's in cache
		storage.nsMu.RLock()
		_, exists := storage.nsCache[ns]
		storage.nsMu.RUnlock()
		assert.True(t, exists)
	})

	t.Run("CreateDuplicateError", func(t *testing.T) {
		ns := "duplicate_ns"
		nsInfo := &NamespaceInfo{
			Name:      ns,
			Dimension: 128,
			CreatedAt: time.Now().Unix(),
		}
		err := storage.CreateNamespace(ctx, nsInfo)
		assert.NoError(t, err)

		// Try to create again
		err = storage.CreateNamespace(ctx, nsInfo)
		assert.Error(t, err)
		assert.Equal(t, ErrNamespaceExists, err)
	})
}

// TestDeleteNamespace_WithVectorsData tests deleting namespace with data
func TestDeleteNamespace_WithVectorsData(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	ns := "delete_with_vecs"
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
			Metadata:  map[string]string{"key": "value"},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
			Version:   1,
		}
		require.NoError(t, storage.UpsertVector(ctx, ns, vec))
	}

	// Delete namespace
	err := storage.DeleteNamespace(ctx, ns)
	assert.NoError(t, err)

	// Verify namespace is gone
	storage.nsMu.RLock()
	_, exists := storage.nsCache[ns]
	storage.nsMu.RUnlock()
	assert.False(t, exists)
}

// TestGetStats_CompletePath tests statistics gathering
func TestGetStats_CompletePath(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	ns := "stats_complete"
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
			Metadata:  map[string]string{"key": string(rune('0' + i))},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
			Version:   1,
		}
		require.NoError(t, storage.UpsertVector(ctx, ns, vec))
	}

	// Get stats
	stats, err := storage.GetStats(ctx, ns)
	assert.NoError(t, err)
	assert.NotNil(t, stats)
	assert.Equal(t, ns, stats.Namespace)
	assert.Greater(t, stats.Count, uint64(0))
}
