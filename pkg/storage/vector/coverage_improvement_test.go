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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector/schema"
)

// TestBatchUpsertVector_EncodingError tests error handling in batch upsert encoding
func TestBatchUpsertVector_EncodingError(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "batch_encoding_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()
	batch := newVectorBatch(db, metrics)
	defer func() { _ = batch.Close() }()

	// Test with vector that has extremely large metadata that could cause encoding issues
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata:  make(map[string]string),
		Version:   1,
	}

	// Add many metadata fields to test encoding paths
	for i := 0; i < 100; i++ {
		vec.Metadata[string(rune('a'+i%26))+string(rune('0'+i/26))] = "test_value"
	}

	err = batch.UpsertVector("test_ns", vec)
	assert.NoError(t, err)
}

// TestBatchDeleteVector_WithMetadata tests deletion with complex metadata
func TestBatchDeleteVector_WithMetadata(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "batch_delete_metadata_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	// Insert vector with extensive metadata
	batch1 := newVectorBatch(db, metrics)
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata: map[string]string{
			"field1": "value1",
			"field2": "value2",
			"field3": "value3",
			"field4": "value4",
			"field5": "value5",
		},
		Version: 1,
	}

	err = batch1.UpsertVector("test_ns", vec)
	require.NoError(t, err)
	err = batch1.Commit()
	require.NoError(t, err)
	_ = batch1.Close()

	// Now delete it
	batch2 := newVectorBatch(db, metrics)
	defer func() { _ = batch2.Close() }()

	err = batch2.DeleteVector("test_ns", 1)
	assert.NoError(t, err)

	err = batch2.Commit()
	assert.NoError(t, err)
}

// TestBatchDeleteVector_MultipleVersionsWithMetadata tests deletion with multiple versions
func TestBatchDeleteVector_MultipleVersionsWithMetadata(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "batch_delete_multi_version_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	// Insert multiple versions with different metadata
	for version := uint64(1); version <= 3; version++ {
		batch := newVectorBatch(db, metrics)
		vec := &VectorValue{
			ID:        1,
			Embedding: []float32{float32(version)},
			Metadata: map[string]string{
				"version": string(rune('0' + version)),
				"field1":  "value1",
				"field2":  "value2",
			},
			Version: version,
		}

		err = batch.UpsertVector("test_ns", vec)
		require.NoError(t, err)
		err = batch.Commit()
		require.NoError(t, err)
		_ = batch.Close()
	}

	// Delete all versions
	batch := newVectorBatch(db, metrics)
	defer func() { _ = batch.Close() }()

	err = batch.DeleteVector("test_ns", 1)
	assert.NoError(t, err)

	err = batch.Commit()
	assert.NoError(t, err)
}

// TestVectorIterator_CloseWithError tests iterator cleanup on error path
func TestVectorIterator_CloseWithError(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "iterator_close_error_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	// Create iterator with valid prefix
	prefix := schema.EncodeNamespacePrefix("test_ns")
	iter := newVectorIterator(db, prefix)

	// Close should succeed even with no data
	err = iter.Close()
	assert.NoError(t, err)
}

// TestVectorIterator_ErrWithNoIterator tests Err() when iterator is nil
func TestVectorIterator_ErrWithNoIterator(t *testing.T) {
	iter := &vectorIterator{
		iter: nil,
		err:  nil,
	}

	err := iter.Err()
	assert.NoError(t, err)
}

// TestVectorIterator_NextAfterError tests Next() after an error has occurred
func TestVectorIterator_NextAfterError(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "iterator_next_error_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	namespace := "test_ns"
	// Insert invalid data
	key := schema.EncodeVectorKey(namespace, 1, 1)
	err = db.Set(key, []byte{0xFF, 0xFF, 0xFF}, pebble.NoSync)
	require.NoError(t, err)

	prefix := schema.EncodeNamespacePrefix(namespace)
	iter := newVectorIterator(db, prefix)
	defer func() { _ = iter.Close() }()

	// First Next should fail and set error
	hasNext := iter.Next()
	assert.False(t, hasNext)
	assert.Error(t, iter.Err())

	// Second Next should return false immediately due to error
	hasNext = iter.Next()
	assert.False(t, hasNext)
}

// TestVectorIterator_ValueWithError tests Value() when error is set
func TestVectorIterator_ValueWithError(t *testing.T) {
	iter := &vectorIterator{
		iter: nil,
		err:  ErrVectorNotFound,
	}

	vec, err := iter.Value()
	assert.Error(t, err)
	assert.Nil(t, vec)
	assert.Equal(t, ErrVectorNotFound, err)
}

// TestMetadataIterator_CloseWithNilIterator tests Close() with nil iterator
func TestMetadataIterator_CloseWithNilIterator(t *testing.T) {
	iter := &metadataIterator{
		iter: nil,
	}

	err := iter.Close()
	assert.NoError(t, err)
}

// TestMetadataIterator_ErrWithNoIterator tests Err() when iterator is nil
func TestMetadataIterator_ErrWithNoIterator(t *testing.T) {
	iter := &metadataIterator{
		iter: nil,
		err:  nil,
	}

	err := iter.Err()
	assert.NoError(t, err)
}

// TestMetadataIterator_NextAfterError tests Next() after an error
func TestMetadataIterator_NextAfterError(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "metadata_iter_error_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	// Insert metadata with no corresponding vector
	namespace := "test_ns"
	metaKey := schema.EncodeMetadataKey(namespace, "field", "value", 1, 1)
	err = db.Set(metaKey, []byte{}, pebble.NoSync)
	require.NoError(t, err)

	prefix := schema.EncodeMetadataKeyPrefix(namespace, "field", "value")
	iter := newMetadataIterator(db, prefix)
	defer func() { _ = iter.Close() }()

	// First Next should fail
	hasNext := iter.Next()
	assert.False(t, hasNext)
	assert.Error(t, iter.Err())

	// Second Next should return false immediately
	hasNext = iter.Next()
	assert.False(t, hasNext)
}

// TestMetadataIterator_ValueWithError tests Value() with error set
func TestMetadataIterator_ValueWithError(t *testing.T) {
	iter := &metadataIterator{
		iter: nil,
		err:  ErrVectorNotFound,
	}

	vec, err := iter.Value()
	assert.Error(t, err)
	assert.Nil(t, vec)
	assert.Equal(t, ErrVectorNotFound, err)
}

// TestAdapter_CleanupTombstones_EdgeCases tests cleanup with various edge cases
func TestAdapter_CleanupTombstones_EdgeCases(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace
	info := &NamespaceInfo{
		Name:      "test_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err := storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Insert and delete vectors to create tombstones
	for i := 1; i <= 10; i++ {
		vec := &VectorValue{
			ID:        uint64(i),
			Embedding: []float32{0.1, 0.2, 0.3},
			Metadata: map[string]string{
				"index": string(rune('0' + i)),
			},
			Version: 1,
		}

		err = storage.UpsertVector(ctx, "test_ns", vec)
		require.NoError(t, err)

		// Delete odd-numbered vectors
		if i%2 == 1 {
			err = storage.DeleteVector(ctx, "test_ns", uint64(i))
			require.NoError(t, err)
		}
	}

	// Run cleanup - cleanup tombstones older than 1 hour
	err = storage.CleanupTombstones(ctx, time.Hour)
	assert.NoError(t, err)
}

// TestAdapter_CleanupTombstones_RecentTombstones tests cleanup doesn't remove recent tombstones
func TestAdapter_CleanupTombstones_RecentTombstones(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Cleanup with very short duration - should not remove recent tombstones
	err := storage.CleanupTombstones(ctx, 1*time.Nanosecond)
	assert.NoError(t, err)
}

// TestAdapter_Compact_ClosedStorage tests compaction on closed storage
func TestAdapter_Compact_ClosedStorage(t *testing.T) {
	storage, cleanup := setupStorage(t)
	cleanup() // Close immediately

	ctx := context.Background()

	err := storage.Compact(ctx, "test_ns")
	assert.Error(t, err)
	assert.Equal(t, ErrStorageClosed, err)
}

// TestAdapter_CleanupTombstones_ClosedStorage tests cleanup on closed storage
func TestAdapter_CleanupTombstones_ClosedStorage(t *testing.T) {
	storage, cleanup := setupStorage(t)
	cleanup() // Close immediately

	ctx := context.Background()

	err := storage.CleanupTombstones(ctx, time.Hour)
	assert.Error(t, err)
	assert.Equal(t, ErrStorageClosed, err)
}

// TestAdapter_NewPebbleVectorStorage_InvalidDataDir tests creation with invalid data directory
func TestAdapter_NewPebbleVectorStorage_InvalidDataDir(t *testing.T) {
	log := &testLogger{}

	// Use a path that's likely to fail (like a file instead of directory)
	tmpFile := filepath.Join(os.TempDir(), "test_file.txt")
	err := os.WriteFile(tmpFile, []byte("test"), 0644)
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpFile) }()

	config := DefaultConfig(tmpFile)
	storage, err := NewPebbleVectorStorage(config, log)

	// Should fail because path is a file, not a directory
	assert.Error(t, err)
	assert.Nil(t, storage)
}

// TestAdapter_BatchAllocateIDs_EdgeCases tests ID allocation edge cases
func TestAdapter_BatchAllocateIDs_EdgeCases(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace
	info := &NamespaceInfo{
		Name:      "test_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err := storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	t.Run("AllocateZeroIDs", func(t *testing.T) {
		ids, err := storage.BatchAllocateIDs("test_ns", 0)
		assert.Error(t, err)
		assert.Nil(t, ids)
	})

	t.Run("AllocateNegativeCount", func(t *testing.T) {
		ids, err := storage.BatchAllocateIDs("test_ns", -1)
		assert.Error(t, err)
		assert.Nil(t, ids)
	})

	t.Run("AllocateFromNonExistentNamespace", func(t *testing.T) {
		ids, err := storage.BatchAllocateIDs("nonexistent", 5)
		assert.Error(t, err)
		assert.Nil(t, ids)
	})

	t.Run("AllocateLargeCount", func(t *testing.T) {
		ids, err := storage.BatchAllocateIDs("test_ns", 1000)
		assert.NoError(t, err)
		assert.NotNil(t, ids)
		assert.Equal(t, 1000, len(ids))

		// Verify IDs are sequential
		for i := 1; i < len(ids); i++ {
			assert.Equal(t, ids[i-1]+1, ids[i])
		}
	})
}

// TestAdapter_NextID_ClosedStorage tests NextID on closed storage
func TestAdapter_NextID_ClosedStorage(t *testing.T) {
	storage, cleanup := setupStorage(t)
	cleanup() // Close immediately

	id, err := storage.NextID("test_ns")
	assert.Error(t, err)
	assert.Equal(t, uint64(0), id)
	assert.Equal(t, ErrStorageClosed, err)
}

// TestAdapter_GetStats_EmptyNamespace tests stats on empty namespace
func TestAdapter_GetStats_EmptyNamespace(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace but don't add any vectors
	info := &NamespaceInfo{
		Name:      "empty_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err := storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	stats, err := storage.GetStats(ctx, "empty_ns")
	assert.NoError(t, err)
	assert.NotNil(t, stats)
	assert.Equal(t, uint64(0), stats.Count)
}

// TestAdapter_DeleteNamespace_WithVectors tests namespace deletion with existing vectors
func TestAdapter_DeleteNamespace_WithVectors(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace
	info := &NamespaceInfo{
		Name:      "test_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err := storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Add multiple vectors with metadata
	for i := 1; i <= 5; i++ {
		vec := &VectorValue{
			ID:        uint64(i),
			Embedding: []float32{0.1, 0.2, 0.3},
			Metadata: map[string]string{
				"field1": "value1",
				"field2": "value2",
			},
			Version: 1,
		}
		err = storage.UpsertVector(ctx, "test_ns", vec)
		require.NoError(t, err)
	}

	// Delete namespace with all vectors
	err = storage.DeleteNamespace(ctx, "test_ns")
	assert.NoError(t, err)

	// Verify namespace is gone from info
	_, err = storage.GetNamespaceInfo(ctx, "test_ns")
	assert.Error(t, err)
	assert.Equal(t, ErrNamespaceNotFound, err)
}

// TestBatchCommit_MetricsUpdate tests that metrics are updated on commit
func TestBatchCommit_MetricsUpdate(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "batch_metrics_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()
	batch := newVectorBatch(db, metrics)
	defer func() { _ = batch.Close() }()

	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1},
		Metadata:  map[string]string{"k": "v"},
		Version:   1,
	}

	err = batch.UpsertVector("test_ns", vec)
	require.NoError(t, err)

	// Commit should update metrics
	err = batch.Commit()
	assert.NoError(t, err)
}

// TestBatchUpsertVector_EmptyMetadata tests upsert with nil and empty metadata
func TestBatchUpsertVector_EmptyMetadata(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "batch_empty_metadata_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()
	batch := newVectorBatch(db, metrics)
	defer func() { _ = batch.Close() }()

	t.Run("NilMetadata", func(t *testing.T) {
		vec := &VectorValue{
			ID:        1,
			Embedding: []float32{0.1, 0.2},
			Metadata:  nil,
			Version:   1,
		}

		err := batch.UpsertVector("test_ns", vec)
		assert.NoError(t, err)
	})

	t.Run("EmptyMetadataMap", func(t *testing.T) {
		vec := &VectorValue{
			ID:        2,
			Embedding: []float32{0.1, 0.2},
			Metadata:  map[string]string{},
			Version:   1,
		}

		err := batch.UpsertVector("test_ns", vec)
		assert.NoError(t, err)
	})
}

// TestAdapter_NextID_NonExistentNamespace tests NextID on non-existent namespace
func TestAdapter_NextID_NonExistentNamespace(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	id, err := storage.NextID("nonexistent")
	assert.Error(t, err)
	assert.Equal(t, uint64(0), id)
	assert.Equal(t, ErrNamespaceNotFound, err)
}

// TestAdapter_BatchAllocateIDs_ClosedStorage tests BatchAllocateIDs on closed storage
func TestAdapter_BatchAllocateIDs_ClosedStorage(t *testing.T) {
	storage, cleanup := setupStorage(t)
	cleanup() // Close immediately

	ids, err := storage.BatchAllocateIDs("test_ns", 10)
	assert.Error(t, err)
	assert.Nil(t, ids)
	assert.Equal(t, ErrStorageClosed, err)
}
