// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
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
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector/schema"
)

// TestCleanupTombstones_EdgeCases tests the CleanupTombstones function with various edge cases
func TestCleanupTombstones_EdgeCases(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	t.Run("CleanupTombstones_Success", func(t *testing.T) {
		// Create a namespace
		ns := "test_tombstone"
		err := storage.CreateNamespace(ctx, &NamespaceInfo{
			Name:      ns,
			Dimension: 128,
		})
		require.NoError(t, err)

		// Create and delete some vectors
		vec1 := &VectorValue{
			ID:        1,
			Version:   1,
			Embedding: make([]float32, 128),
			Metadata:  map[string]string{"key": "value1"},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		}

		err = storage.UpsertVector(ctx, ns, vec1)
		require.NoError(t, err)

		err = storage.DeleteVector(ctx, ns, 1)
		require.NoError(t, err)

		// Wait a bit to ensure tombstone is created
		time.Sleep(10 * time.Millisecond)

		// Cleanup old tombstones (should clean up our tombstone)
		err = storage.CleanupTombstones(ctx, 1*time.Millisecond)
		require.NoError(t, err)
	})

	t.Run("CleanupTombstones_NoTombstones", func(t *testing.T) {
		// Cleanup when there are no tombstones
		err := storage.CleanupTombstones(ctx, 24*time.Hour)
		require.NoError(t, err)
	})

	t.Run("CleanupTombstones_FutureTime", func(t *testing.T) {
		// Cleanup with future time (should not delete any)
		err := storage.CleanupTombstones(ctx, -24*time.Hour)
		require.NoError(t, err)
	})

	t.Run("CleanupTombstones_Closed", func(t *testing.T) {
		storage2, cleanup2 := setupStorage(t)
		defer cleanup2()

		// Close storage
		err := storage2.Close()
		require.NoError(t, err)

		// Try to cleanup tombstones on closed storage
		err = storage2.CleanupTombstones(ctx, time.Hour)
		assert.ErrorIs(t, err, ErrStorageClosed)
	})

	t.Run("CleanupTombstones_MultipleTombstones", func(t *testing.T) {
		// Create a namespace
		ns := "test_multi_tombstone"
		err := storage.CreateNamespace(ctx, &NamespaceInfo{
			Name:      ns,
			Dimension: 128,
		})
		require.NoError(t, err)

		// Create and delete multiple vectors
		for i := uint64(100); i < 110; i++ {
			vec := &VectorValue{
				ID:        i,
				Version:   1,
				Embedding: make([]float32, 128),
				Metadata:  map[string]string{"key": fmt.Sprintf("value%d", i)},
				CreatedAt: time.Now().Unix(),
				UpdatedAt: time.Now().Unix(),
			}

			err = storage.UpsertVector(ctx, ns, vec)
			require.NoError(t, err)

			err = storage.DeleteVector(ctx, ns, i)
			require.NoError(t, err)
		}

		// Wait a bit
		time.Sleep(10 * time.Millisecond)

		// Cleanup old tombstones
		err = storage.CleanupTombstones(ctx, 1*time.Millisecond)
		require.NoError(t, err)
	})
}

// TestCompact_EdgeCases tests the Compact function with various edge cases
func TestCompact_EdgeCases(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	t.Run("Compact_Success", func(t *testing.T) {
		// Create a namespace
		ns := "test_compact"
		err := storage.CreateNamespace(ctx, &NamespaceInfo{
			Name:      ns,
			Dimension: 128,
		})
		require.NoError(t, err)

		// Add some vectors
		for i := uint64(1); i <= 10; i++ {
			vec := &VectorValue{
				ID:        i,
				Version:   1,
				Embedding: make([]float32, 128),
				Metadata:  map[string]string{"key": fmt.Sprintf("value%d", i)},
				CreatedAt: time.Now().Unix(),
				UpdatedAt: time.Now().Unix(),
			}
			err = storage.UpsertVector(ctx, ns, vec)
			require.NoError(t, err)
		}

		// Compact the namespace
		err = storage.Compact(ctx, ns)
		require.NoError(t, err)
	})

	t.Run("Compact_EmptyNamespace", func(t *testing.T) {
		// Create an empty namespace
		ns := "test_compact_empty"
		err := storage.CreateNamespace(ctx, &NamespaceInfo{
			Name:      ns,
			Dimension: 128,
		})
		require.NoError(t, err)

		// Compact empty namespace (should succeed)
		err = storage.Compact(ctx, ns)
		require.NoError(t, err)
	})

	t.Run("Compact_NonexistentNamespace", func(t *testing.T) {
		// Compact a namespace that doesn't exist (should succeed - pebble doesn't care)
		err := storage.Compact(ctx, "nonexistent")
		require.NoError(t, err)
	})

	t.Run("Compact_Closed", func(t *testing.T) {
		storage2, cleanup2 := setupStorage(t)
		defer cleanup2()

		// Close storage
		err := storage2.Close()
		require.NoError(t, err)

		// Try to compact on closed storage
		err = storage2.Compact(ctx, "test")
		assert.ErrorIs(t, err, ErrStorageClosed)
	})
}

// TestBatchUpsertVector_ErrorCases tests error cases in batch upsert
func TestBatchUpsertVector_ErrorCases(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	t.Run("UpsertVector_NilVector", func(t *testing.T) {
		batch := newVectorBatch(storage.db, storage.metrics)
		defer func() { _ = batch.Close() }()

		err := batch.UpsertVector("test", nil)
		assert.ErrorIs(t, err, ErrInvalidVector)
	})

	t.Run("UpsertVector_WithMetadata", func(t *testing.T) {
		batch := newVectorBatch(storage.db, storage.metrics)
		defer func() { _ = batch.Close() }()

		vec := &VectorValue{
			ID:        1,
			Version:   1,
			Embedding: []float32{1.0, 2.0, 3.0},
			Metadata:  map[string]string{"key1": "value1", "key2": "value2", "key3": "value3"},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		}

		err := batch.UpsertVector("test", vec)
		assert.NoError(t, err)
		assert.Equal(t, 1, batch.Count())
	})

	t.Run("UpsertVector_EmptyMetadata", func(t *testing.T) {
		batch := newVectorBatch(storage.db, storage.metrics)
		defer func() { _ = batch.Close() }()

		vec := &VectorValue{
			ID:        2,
			Version:   1,
			Embedding: []float32{1.0, 2.0, 3.0},
			Metadata:  map[string]string{},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		}

		err := batch.UpsertVector("test", vec)
		assert.NoError(t, err)
	})
}

// TestBatchDeleteVector_ErrorCases tests error cases in batch delete
func TestBatchDeleteVector_ErrorCases(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	t.Run("DeleteVector_InvalidID", func(t *testing.T) {
		batch := newVectorBatch(storage.db, storage.metrics)
		defer func() { _ = batch.Close() }()

		err := batch.DeleteVector("test", 0)
		assert.ErrorIs(t, err, ErrInvalidID)
	})

	t.Run("DeleteVector_NonexistentVector", func(t *testing.T) {
		batch := newVectorBatch(storage.db, storage.metrics)
		defer func() { _ = batch.Close() }()

		err := batch.DeleteVector("test", 99999)
		assert.NoError(t, err)
	})

	t.Run("DeleteVector_WithMetadata", func(t *testing.T) {
		// Create a namespace and vector first
		ns := "test_batch_delete"
		err := storage.CreateNamespace(ctx, &NamespaceInfo{
			Name:      ns,
			Dimension: 3,
		})
		require.NoError(t, err)

		vec := &VectorValue{
			ID:        100,
			Version:   1,
			Embedding: []float32{1.0, 2.0, 3.0},
			Metadata:  map[string]string{"key1": "value1", "key2": "value2"},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		}
		err = storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)

		// Now delete it in a batch
		batch := newVectorBatch(storage.db, storage.metrics)
		defer func() { _ = batch.Close() }()

		err = batch.DeleteVector(ns, 100)
		assert.NoError(t, err)

		err = batch.Commit()
		assert.NoError(t, err)
	})

	t.Run("DeleteVector_MultipleVersions", func(t *testing.T) {
		// Create a namespace and vector with multiple versions
		ns := "test_batch_delete_versions"
		err := storage.CreateNamespace(ctx, &NamespaceInfo{
			Name:      ns,
			Dimension: 3,
		})
		require.NoError(t, err)

		// Insert multiple versions
		for v := uint64(1); v <= 3; v++ {
			vec := &VectorValue{
				ID:        200,
				Version:   v,
				Embedding: []float32{1.0, 2.0, 3.0},
				Metadata:  map[string]string{"key": fmt.Sprintf("version%d", v)},
				CreatedAt: time.Now().Unix(),
				UpdatedAt: time.Now().Unix(),
			}
			err = storage.UpsertVector(ctx, ns, vec)
			require.NoError(t, err)
		}

		// Delete all versions in a batch
		batch := newVectorBatch(storage.db, storage.metrics)
		defer func() { _ = batch.Close() }()

		err = batch.DeleteVector(ns, 200)
		assert.NoError(t, err)

		err = batch.Commit()
		assert.NoError(t, err)
	})
}

// TestIterator_ErrorCases tests iterator error cases
func TestIterator_ErrorCases(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	t.Run("VectorIterator_EmptyResult", func(t *testing.T) {
		ns := "test_iter_empty"
		err := storage.CreateNamespace(ctx, &NamespaceInfo{
			Name:      ns,
			Dimension: 3,
		})
		require.NoError(t, err)

		prefix := schema.EncodeNamespacePrefix(ns)
		iter := newVectorIterator(storage.db, prefix)
		defer func() { _ = iter.Close() }()

		// Should return false when there are no vectors
		hasNext := iter.Next()
		assert.False(t, hasNext)

		// Value should return error
		val, err := iter.Value()
		assert.Error(t, err)
		assert.Nil(t, val)
	})

	t.Run("VectorIterator_WithData", func(t *testing.T) {
		ns := "test_iter_data"
		err := storage.CreateNamespace(ctx, &NamespaceInfo{
			Name:      ns,
			Dimension: 3,
		})
		require.NoError(t, err)

		// Add a vector
		vec := &VectorValue{
			ID:        1,
			Version:   1,
			Embedding: []float32{1.0, 2.0, 3.0},
			Metadata:  map[string]string{"key": "value"},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		}
		err = storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)

		prefix := schema.EncodeNamespacePrefix(ns)
		iter := newVectorIterator(storage.db, prefix)
		defer func() { _ = iter.Close() }()

		// Should return true for first element
		hasNext := iter.Next()
		assert.True(t, hasNext)

		// Should be able to get value
		val, err := iter.Value()
		assert.NoError(t, err)
		assert.NotNil(t, val)

		// Should be able to get key
		namespace, id, version := iter.Key()
		assert.Equal(t, ns, namespace)
		assert.Equal(t, uint64(1), id)
		assert.Equal(t, uint64(1), version)

		// Should return false for next (no more elements)
		hasNext = iter.Next()
		assert.False(t, hasNext)
	})

	t.Run("MetadataIterator_EmptyResult", func(t *testing.T) {
		ns := "test_meta_iter_empty"
		err := storage.CreateNamespace(ctx, &NamespaceInfo{
			Name:      ns,
			Dimension: 3,
		})
		require.NoError(t, err)

		prefix := schema.EncodeMetadataKeyPrefix(ns, "nonexistent", "")
		iter := newMetadataIterator(storage.db, prefix)
		defer func() { _ = iter.Close() }()

		// Should return false when there are no results
		hasNext := iter.Next()
		assert.False(t, hasNext)

		// Value should return error
		val, err := iter.Value()
		assert.Error(t, err)
		assert.Nil(t, val)
	})

	t.Run("MetadataIterator_WithData", func(t *testing.T) {
		ns := "test_meta_iter_data"
		err := storage.CreateNamespace(ctx, &NamespaceInfo{
			Name:      ns,
			Dimension: 3,
		})
		require.NoError(t, err)

		// Add a vector with metadata
		vec := &VectorValue{
			ID:        1,
			Version:   1,
			Embedding: []float32{1.0, 2.0, 3.0},
			Metadata:  map[string]string{"category": "test"},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		}
		err = storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)

		prefix := schema.EncodeMetadataKeyPrefix(ns, "category", "test")
		iter := newMetadataIterator(storage.db, prefix)
		defer func() { _ = iter.Close() }()

		// Should return true for first element
		hasNext := iter.Next()
		assert.True(t, hasNext)

		// Should be able to get value
		val, err := iter.Value()
		assert.NoError(t, err)
		assert.NotNil(t, val)

		// Should be able to get key
		namespace, id, version := iter.Key()
		assert.Equal(t, ns, namespace)
		assert.Equal(t, uint64(1), id)
		assert.Equal(t, uint64(1), version)

		// Check error
		assert.NoError(t, iter.Err())

		// Should return false for next (no more elements)
		hasNext = iter.Next()
		assert.False(t, hasNext)
	})
}

// TestAdapter_AdditionalCoverage tests additional adapter edge cases
func TestAdapter_AdditionalCoverage(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	t.Run("UpsertVector_UpdateExisting", func(t *testing.T) {
		ns := "test_update"
		err := storage.CreateNamespace(ctx, &NamespaceInfo{
			Name:      ns,
			Dimension: 3,
		})
		require.NoError(t, err)

		// Insert initial vector
		vec1 := &VectorValue{
			ID:        1,
			Version:   1,
			Embedding: []float32{1.0, 2.0, 3.0},
			Metadata:  map[string]string{"key": "value1"},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		}
		err = storage.UpsertVector(ctx, ns, vec1)
		require.NoError(t, err)

		// Update with new version
		vec2 := &VectorValue{
			ID:        1,
			Version:   2,
			Embedding: []float32{4.0, 5.0, 6.0},
			Metadata:  map[string]string{"key": "value2"},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		}
		err = storage.UpsertVector(ctx, ns, vec2)
		require.NoError(t, err)

		// Retrieve and verify latest version
		retrieved, err := storage.GetVector(ctx, ns, 1)
		require.NoError(t, err)
		assert.Equal(t, uint64(2), retrieved.Version)
	})

	t.Run("GetStats_AfterOperations", func(t *testing.T) {
		ns := "test_stats"
		err := storage.CreateNamespace(ctx, &NamespaceInfo{
			Name:      ns,
			Dimension: 3,
		})
		require.NoError(t, err)

		// Add some vectors
		for i := uint64(1); i <= 5; i++ {
			vec := &VectorValue{
				ID:        i,
				Version:   1,
				Embedding: []float32{1.0, 2.0, 3.0},
				Metadata:  map[string]string{"key": fmt.Sprintf("value%d", i)},
				CreatedAt: time.Now().Unix(),
				UpdatedAt: time.Now().Unix(),
			}
			err = storage.UpsertVector(ctx, ns, vec)
			require.NoError(t, err)
		}

		// Get stats
		stats, err := storage.GetStats(ctx, ns)
		require.NoError(t, err)
		assert.NotNil(t, stats)
	})

	t.Run("BatchDeleteVectors_EmptyList", func(t *testing.T) {
		ns := "test_batch_delete_empty"
		err := storage.CreateNamespace(ctx, &NamespaceInfo{
			Name:      ns,
			Dimension: 3,
		})
		require.NoError(t, err)

		// Try to delete empty list
		err = storage.BatchDeleteVectors(ctx, ns, []uint64{})
		assert.Error(t, err)
	})

	t.Run("DeleteVector_WithMultipleMetadataFields", func(t *testing.T) {
		ns := "test_delete_multi_meta"
		err := storage.CreateNamespace(ctx, &NamespaceInfo{
			Name:      ns,
			Dimension: 3,
		})
		require.NoError(t, err)

		// Insert vector with multiple metadata fields
		vec := &VectorValue{
			ID:        1,
			Version:   1,
			Embedding: []float32{1.0, 2.0, 3.0},
			Metadata: map[string]string{
				"field1": "value1",
				"field2": "value2",
				"field3": "value3",
			},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		}
		err = storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)

		// Delete it
		err = storage.DeleteVector(ctx, ns, 1)
		require.NoError(t, err)

		// Verify it's deleted
		_, err = storage.GetVector(ctx, ns, 1)
		assert.Error(t, err)
	})

	t.Run("CreateNamespace_InvalidConfig", func(t *testing.T) {
		// Try to create namespace with nil info
		err := storage.CreateNamespace(ctx, nil)
		assert.ErrorIs(t, err, ErrInvalidNamespace)

		// Try to create namespace with empty name
		err = storage.CreateNamespace(ctx, &NamespaceInfo{
			Name:      "",
			Dimension: 3,
		})
		assert.ErrorIs(t, err, ErrInvalidNamespace)
	})

	t.Run("NextID_MultipleAllocation", func(t *testing.T) {
		ns := "test_nextid"
		err := storage.CreateNamespace(ctx, &NamespaceInfo{
			Name:      ns,
			Dimension: 3,
		})
		require.NoError(t, err)

		// Allocate multiple IDs
		id1, err := storage.NextID(ns)
		require.NoError(t, err)
		assert.Greater(t, id1, uint64(0))

		id2, err := storage.NextID(ns)
		require.NoError(t, err)
		assert.Greater(t, id2, id1)

		id3, err := storage.NextID(ns)
		require.NoError(t, err)
		assert.Greater(t, id3, id2)
	})

	t.Run("BatchAllocateIDs_Success", func(t *testing.T) {
		ns := "test_batch_ids"
		err := storage.CreateNamespace(ctx, &NamespaceInfo{
			Name:      ns,
			Dimension: 3,
		})
		require.NoError(t, err)

		// Allocate batch of IDs
		ids, err := storage.BatchAllocateIDs(ns, 10)
		require.NoError(t, err)
		assert.Len(t, ids, 10)
	})

	t.Run("GetVector_Closed", func(t *testing.T) {
		tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("vector_test_%d", time.Now().UnixNano()))
		require.NoError(t, os.MkdirAll(tmpDir, 0755))
		defer func() { _ = os.RemoveAll(tmpDir) }()

		config := DefaultConfig(tmpDir)
		config.SyncWrites = false
		log := &testLogger{}

		storage2, err := NewPebbleVectorStorage(config, log)
		require.NoError(t, err)

		err = storage2.Close()
		require.NoError(t, err)

		_, err = storage2.GetVector(ctx, "test", 1)
		assert.ErrorIs(t, err, ErrStorageClosed)
	})

	t.Run("DeleteVector_Closed", func(t *testing.T) {
		tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("vector_test_%d", time.Now().UnixNano()))
		require.NoError(t, os.MkdirAll(tmpDir, 0755))
		defer func() { _ = os.RemoveAll(tmpDir) }()

		config := DefaultConfig(tmpDir)
		config.SyncWrites = false
		log := &testLogger{}

		storage2, err := NewPebbleVectorStorage(config, log)
		require.NoError(t, err)

		err = storage2.Close()
		require.NoError(t, err)

		err = storage2.DeleteVector(ctx, "test", 1)
		assert.ErrorIs(t, err, ErrStorageClosed)
	})

	t.Run("ScanByMetadata_Closed", func(t *testing.T) {
		tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("vector_test_%d", time.Now().UnixNano()))
		require.NoError(t, os.MkdirAll(tmpDir, 0755))
		defer func() { _ = os.RemoveAll(tmpDir) }()

		config := DefaultConfig(tmpDir)
		config.SyncWrites = false
		log := &testLogger{}

		storage2, err := NewPebbleVectorStorage(config, log)
		require.NoError(t, err)

		err = storage2.Close()
		require.NoError(t, err)

		_, err = storage2.ScanByMetadata(ctx, "test", "key", "value", nil)
		assert.ErrorIs(t, err, ErrStorageClosed)
	})

	t.Run("ScanNamespace_Closed", func(t *testing.T) {
		tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("vector_test_%d", time.Now().UnixNano()))
		require.NoError(t, os.MkdirAll(tmpDir, 0755))
		defer func() { _ = os.RemoveAll(tmpDir) }()

		config := DefaultConfig(tmpDir)
		config.SyncWrites = false
		log := &testLogger{}

		storage2, err := NewPebbleVectorStorage(config, log)
		require.NoError(t, err)

		err = storage2.Close()
		require.NoError(t, err)

		_, err = storage2.ScanNamespace(ctx, "test", nil)
		assert.ErrorIs(t, err, ErrStorageClosed)
	})

	t.Run("CreateNamespace_Closed", func(t *testing.T) {
		tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("vector_test_%d", time.Now().UnixNano()))
		require.NoError(t, os.MkdirAll(tmpDir, 0755))
		defer func() { _ = os.RemoveAll(tmpDir) }()

		config := DefaultConfig(tmpDir)
		config.SyncWrites = false
		log := &testLogger{}

		storage2, err := NewPebbleVectorStorage(config, log)
		require.NoError(t, err)

		err = storage2.Close()
		require.NoError(t, err)

		err = storage2.CreateNamespace(ctx, &NamespaceInfo{Name: "test", Dimension: 3})
		assert.ErrorIs(t, err, ErrStorageClosed)
	})

	t.Run("DeleteNamespace_Closed", func(t *testing.T) {
		tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("vector_test_%d", time.Now().UnixNano()))
		require.NoError(t, os.MkdirAll(tmpDir, 0755))
		defer func() { _ = os.RemoveAll(tmpDir) }()

		config := DefaultConfig(tmpDir)
		config.SyncWrites = false
		log := &testLogger{}

		storage2, err := NewPebbleVectorStorage(config, log)
		require.NoError(t, err)

		err = storage2.Close()
		require.NoError(t, err)

		err = storage2.DeleteNamespace(ctx, "test")
		assert.ErrorIs(t, err, ErrStorageClosed)
	})

	t.Run("NextID_Closed", func(t *testing.T) {
		tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("vector_test_%d", time.Now().UnixNano()))
		require.NoError(t, os.MkdirAll(tmpDir, 0755))
		defer func() { _ = os.RemoveAll(tmpDir) }()

		config := DefaultConfig(tmpDir)
		config.SyncWrites = false
		log := &testLogger{}

		storage2, err := NewPebbleVectorStorage(config, log)
		require.NoError(t, err)

		err = storage2.Close()
		require.NoError(t, err)

		_, err = storage2.NextID("test")
		assert.ErrorIs(t, err, ErrStorageClosed)
	})

	t.Run("BatchAllocateIDs_Closed", func(t *testing.T) {
		tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("vector_test_%d", time.Now().UnixNano()))
		require.NoError(t, os.MkdirAll(tmpDir, 0755))
		defer func() { _ = os.RemoveAll(tmpDir) }()

		config := DefaultConfig(tmpDir)
		config.SyncWrites = false
		log := &testLogger{}

		storage2, err := NewPebbleVectorStorage(config, log)
		require.NoError(t, err)

		err = storage2.Close()
		require.NoError(t, err)

		_, err = storage2.BatchAllocateIDs("test", 10)
		assert.ErrorIs(t, err, ErrStorageClosed)
	})
}

// TestBatch_ResetAndReuse tests batch reset and reuse
func TestBatch_ResetAndReuse(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	batch := newVectorBatch(storage.db, storage.metrics)
	defer func() { _ = batch.Close() }()

	// Add an operation
	vec := &VectorValue{
		ID:        1,
		Version:   1,
		Embedding: []float32{1.0, 2.0, 3.0},
		Metadata:  map[string]string{"key": "value"},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err := batch.UpsertVector("test", vec)
	require.NoError(t, err)
	assert.Equal(t, 1, batch.Count())

	// Reset the batch
	batch.Reset()
	assert.Equal(t, 0, batch.Count())

	// Reuse the batch
	vec2 := &VectorValue{
		ID:        2,
		Version:   1,
		Embedding: []float32{4.0, 5.0, 6.0},
		Metadata:  map[string]string{"key": "value2"},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = batch.UpsertVector("test", vec2)
	require.NoError(t, err)
	assert.Equal(t, 1, batch.Count())
}

// TestClose_DoubleClose tests calling Close multiple times
func TestClose_DoubleClose(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("vector_test_%d", time.Now().UnixNano()))
	require.NoError(t, os.MkdirAll(tmpDir, 0755))
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = false
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)

	// First close
	err = storage.Close()
	require.NoError(t, err)

	// Second close (should not error)
	err = storage.Close()
	assert.NoError(t, err)
}

// TestUpsertVector_InvalidNamespace tests upsert with invalid namespace
func TestUpsertVector_InvalidNamespace(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Test with nil vector
	err := storage.UpsertVector(ctx, "test", nil)
	assert.ErrorIs(t, err, ErrInvalidVector)
}

// TestGetVector_InvalidNamespace tests get with invalid namespace
func TestGetVector_InvalidNamespace(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	_, err := storage.GetVector(ctx, "nonexistent", 1)
	assert.Error(t, err)
}

// TestDeleteNamespace_WithData tests deleting a namespace that contains data
func TestDeleteNamespace_WithData(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace
	ns := "test_delete_ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{
		Name:      ns,
		Dimension: 3,
	})
	require.NoError(t, err)

	// Add vectors
	for i := uint64(1); i <= 5; i++ {
		vec := &VectorValue{
			ID:        i,
			Version:   1,
			Embedding: []float32{1.0, 2.0, 3.0},
			Metadata:  map[string]string{"key": fmt.Sprintf("value%d", i)},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		}
		err = storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)
	}

	// Delete namespace (should delete all data)
	err = storage.DeleteNamespace(ctx, ns)
	require.NoError(t, err)

	// Verify namespace is gone
	_, err = storage.GetNamespaceInfo(ctx, ns)
	assert.Error(t, err)
}

// TestScanByMultipleFilters_ComplexFilters tests scanning with complex filters
func TestScanByMultipleFilters_ComplexFilters(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace
	ns := "test_multi_filter"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{
		Name:      ns,
		Dimension: 3,
	})
	require.NoError(t, err)

	// Add vectors with different metadata
	vectors := []*VectorValue{
		{
			ID:        1,
			Version:   1,
			Embedding: []float32{1.0, 2.0, 3.0},
			Metadata:  map[string]string{"category": "A", "status": "active"},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		},
		{
			ID:        2,
			Version:   1,
			Embedding: []float32{2.0, 3.0, 4.0},
			Metadata:  map[string]string{"category": "A", "status": "inactive"},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		},
		{
			ID:        3,
			Version:   1,
			Embedding: []float32{3.0, 4.0, 5.0},
			Metadata:  map[string]string{"category": "B", "status": "active"},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		},
	}

	for _, vec := range vectors {
		err = storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)
	}

	// Scan with multiple filters
	filters := []MetadataFilter{
		{Field: "category", Value: "A", Operator: FilterEqual},
		{Field: "status", Value: "active", Operator: FilterEqual},
	}

	results, err := storage.ScanByMultipleFilters(ctx, ns, filters, nil)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, uint64(1), results[0].ID)
}

// TestCleanupTombstones_WithSyncWrites tests cleanup with sync writes enabled
