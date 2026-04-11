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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector/schema"
)

// TestBatch_UpsertVector_WithLargeMetadata tests batch operations with large metadata sets
func TestBatch_UpsertVector_WithLargeMetadata(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "batch_large_meta")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()
	batch := newVectorBatch(db, metrics)
	defer func() { _ = batch.Close() }()

	// Create vector with many metadata fields to exercise all code paths
	metadata := make(map[string]string)
	for i := 0; i < 50; i++ {
		metadata[string(rune('a'+i%26))+string(rune(i))] = "value" + string(rune(i))
	}

	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata:  metadata,
		Version:   1,
	}

	err = batch.UpsertVector("test", vec)
	assert.NoError(t, err)
	assert.Equal(t, 1, batch.Count())

	err = batch.Commit()
	assert.NoError(t, err)
}

// TestBatch_DeleteVector_WithMultipleVersions tests delete with multiple versions
func TestBatch_DeleteVector_WithMultipleVersions(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "batch_multi_version")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	// Insert multiple versions with different metadata
	for v := uint64(1); v <= 5; v++ {
		batch := newVectorBatch(db, metrics)
		vec := &VectorValue{
			ID:        1,
			Embedding: []float32{float32(v)},
			Metadata: map[string]string{
				"version": string(rune('0' + v)),
				"field":   "value" + string(rune(v)),
			},
			Version: v,
		}
		require.NoError(t, batch.UpsertVector("test", vec))
		require.NoError(t, batch.Commit())
		_ = batch.Close()
	}

	// Delete should handle all versions and all their metadata/reverse indexes
	batch := newVectorBatch(db, metrics)
	defer func() { _ = batch.Close() }()

	err = batch.DeleteVector("test", 1)
	assert.NoError(t, err)

	err = batch.Commit()
	assert.NoError(t, err)
}

// TestBatch_DeleteVector_WithCorruptedKeys tests delete with corrupted keys
func TestBatch_DeleteVector_WithCorruptedKeys(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "batch_corrupt_keys")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	// Insert a valid vector first
	batch1 := newVectorBatch(db, metrics)
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1},
		Metadata: map[string]string{
			"field": "value",
		},
		Version: 1,
	}
	require.NoError(t, batch1.UpsertVector("test", vec))
	require.NoError(t, batch1.Commit())
	_ = batch1.Close()

	// Insert corrupted keys
	prefix := schema.EncodeVectorKeyPrefix("test", 1)
	corruptKey1 := append(prefix, []byte{0xFF}...)
	require.NoError(t, db.Set(corruptKey1, []byte{}, pebble.NoSync))

	reversePrefix := schema.EncodeReverseKeyPrefix("test", 1)
	corruptKey2 := append(reversePrefix, []byte{0xFF}...)
	require.NoError(t, db.Set(corruptKey2, []byte{}, pebble.NoSync))

	// Delete should skip corrupted keys gracefully
	batch2 := newVectorBatch(db, metrics)
	defer func() { _ = batch2.Close() }()

	err = batch2.DeleteVector("test", 1)
	assert.NoError(t, err)

	err = batch2.Commit()
	assert.NoError(t, err)
}

// TestIterator_ErrorCreation tests iterator creation with errors
func TestIterator_ErrorCreation(t *testing.T) {
	t.Run("VectorIterator_WithEmptyDB", func(t *testing.T) {
		tmpDir := filepath.Join(os.TempDir(), "iter_empty")
		defer func() { _ = os.RemoveAll(tmpDir) }()

		db, err := pebble.Open(tmpDir, &pebble.Options{})
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		prefix := schema.EncodeNamespacePrefix("empty")
		iter := newVectorIterator(db, prefix)
		defer func() { _ = iter.Close() }()

		// Should handle empty database
		result := iter.Next()
		assert.False(t, result)
		assert.NoError(t, iter.Err())
	})

	t.Run("MetadataIterator_WithEmptyDB", func(t *testing.T) {
		tmpDir := filepath.Join(os.TempDir(), "meta_iter_empty")
		defer func() { _ = os.RemoveAll(tmpDir) }()

		db, err := pebble.Open(tmpDir, &pebble.Options{})
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		prefix := schema.EncodeMetadataKeyPrefix("empty", "field", "value")
		iter := newMetadataIterator(db, prefix)
		defer func() { _ = iter.Close() }()

		// Should handle empty database
		result := iter.Next()
		assert.False(t, result)
		assert.NoError(t, iter.Err())
	})
}

// TestIterator_NextPathCoverage tests different paths in Next()
func TestIterator_NextPathCoverage(t *testing.T) {
	t.Run("VectorIterator_SingleItem", func(t *testing.T) {
		tmpDir := filepath.Join(os.TempDir(), "iter_single")
		defer func() { _ = os.RemoveAll(tmpDir) }()

		db, err := pebble.Open(tmpDir, &pebble.Options{})
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		// Insert single item
		vec := &schema.VectorValue{
			ID:        1,
			Embedding: []float32{0.1},
			Metadata:  map[string]string{},
			Version:   1,
		}
		key := schema.EncodeVectorKey("test", 1, 1)
		value, err := schema.EncodeVectorValue(vec)
		require.NoError(t, err)
		require.NoError(t, db.Set(key, value, pebble.NoSync))

		prefix := schema.EncodeNamespacePrefix("test")
		iter := newVectorIterator(db, prefix)
		defer func() { _ = iter.Close() }()

		// First Next should succeed
		result := iter.Next()
		assert.True(t, result)

		v, err := iter.Value()
		assert.NoError(t, err)
		assert.NotNil(t, v)

		// Second Next should return false (no more items)
		result = iter.Next()
		assert.False(t, result)
	})

	t.Run("MetadataIterator_SingleItem", func(t *testing.T) {
		tmpDir := filepath.Join(os.TempDir(), "meta_iter_single")
		defer func() { _ = os.RemoveAll(tmpDir) }()

		db, err := pebble.Open(tmpDir, &pebble.Options{})
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		ns := "test"
		field := "category"
		value := "tech"

		// Insert vector and metadata
		vec := &schema.VectorValue{
			ID:        1,
			Embedding: []float32{0.1},
			Metadata: map[string]string{
				field: value,
			},
			Version: 1,
		}

		vectorKey := schema.EncodeVectorKey(ns, 1, 1)
		vectorValue, err := schema.EncodeVectorValue(vec)
		require.NoError(t, err)
		require.NoError(t, db.Set(vectorKey, vectorValue, pebble.NoSync))

		metaKey := schema.EncodeMetadataKey(ns, field, value, 1, 1)
		require.NoError(t, db.Set(metaKey, []byte{}, pebble.NoSync))

		prefix := schema.EncodeMetadataKeyPrefix(ns, field, value)
		iter := newMetadataIterator(db, prefix)
		defer func() { _ = iter.Close() }()

		// First Next should succeed
		result := iter.Next()
		assert.True(t, result)

		v, err := iter.Value()
		assert.NoError(t, err)
		assert.NotNil(t, v)

		// Second Next should return false
		result = iter.Next()
		assert.False(t, result)
	})
}

// TestCleanupTombstones_AdditionalEdgeCases tests more edge cases
func TestCleanupTombstones_AdditionalEdgeCases(t *testing.T) {
	t.Run("WithValidTombstones", func(t *testing.T) {
		storage, cleanup := setupStorage(t)
		defer cleanup()

		ctx := context.Background()

		ns := "tombstone_valid"
		nsInfo := &NamespaceInfo{
			Name:      ns,
			Dimension: 128,
			CreatedAt: time.Now().Unix(),
		}
		require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

		// Add old tombstones that should be cleaned
		oldTime := uint64(time.Now().Add(-48 * time.Hour).Unix())
		for i := uint64(1); i <= 5; i++ {
			tombstoneKey := schema.EncodeTombstoneKey(ns, i, oldTime)
			require.NoError(t, storage.db.Set(tombstoneKey, []byte{}, pebble.NoSync))
		}

		// Cleanup should succeed and remove old tombstones
		err := storage.CleanupTombstones(ctx, 24*time.Hour)
		assert.NoError(t, err)
	})

	t.Run("WithRecentTombstones", func(t *testing.T) {
		storage, cleanup := setupStorage(t)
		defer cleanup()

		ctx := context.Background()

		ns := "tombstone_recent"
		nsInfo := &NamespaceInfo{
			Name:      ns,
			Dimension: 128,
			CreatedAt: time.Now().Unix(),
		}
		require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

		// Add recent tombstones that should NOT be cleaned
		recentTime := uint64(time.Now().Add(-1 * time.Hour).Unix())
		for i := uint64(1); i <= 3; i++ {
			tombstoneKey := schema.EncodeTombstoneKey(ns, i, recentTime)
			require.NoError(t, storage.db.Set(tombstoneKey, []byte{}, pebble.NoSync))
		}

		// Cleanup should succeed but not remove recent tombstones
		err := storage.CleanupTombstones(ctx, 24*time.Hour)
		assert.NoError(t, err)
	})

	t.Run("WithMixedTombstones", func(t *testing.T) {
		storage, cleanup := setupStorage(t)
		defer cleanup()

		ctx := context.Background()

		ns := "tombstone_mixed"
		nsInfo := &NamespaceInfo{
			Name:      ns,
			Dimension: 128,
			CreatedAt: time.Now().Unix(),
		}
		require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

		// Add both old and recent tombstones
		oldTime := uint64(time.Now().Add(-48 * time.Hour).Unix())
		recentTime := uint64(time.Now().Add(-1 * time.Hour).Unix())

		tombstoneKey1 := schema.EncodeTombstoneKey(ns, 1, oldTime)
		require.NoError(t, storage.db.Set(tombstoneKey1, []byte{}, pebble.NoSync))

		tombstoneKey2 := schema.EncodeTombstoneKey(ns, 2, recentTime)
		require.NoError(t, storage.db.Set(tombstoneKey2, []byte{}, pebble.NoSync))

		// Cleanup should remove old but keep recent
		err := storage.CleanupTombstones(ctx, 24*time.Hour)
		assert.NoError(t, err)
	})

	t.Run("WithSyncWrites", func(t *testing.T) {
		tmpDir := filepath.Join(os.TempDir(), "tombstone_sync")
		defer func() { _ = os.RemoveAll(tmpDir) }()

		config := DefaultConfig(tmpDir)
		config.SyncWrites = true // Enable sync writes

		log := &testLogger{}
		storage, err := NewPebbleVectorStorage(config, log)
		require.NoError(t, err)
		defer func() { _ = storage.Close() }()

		ctx := context.Background()

		ns := "tombstone_sync"
		nsInfo := &NamespaceInfo{
			Name:      ns,
			Dimension: 128,
			CreatedAt: time.Now().Unix(),
		}
		require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

		// Add old tombstones
		oldTime := uint64(time.Now().Add(-48 * time.Hour).Unix())
		tombstoneKey := schema.EncodeTombstoneKey(ns, 1, oldTime)
		require.NoError(t, storage.db.Set(tombstoneKey, []byte{}, pebble.NoSync))

		// Cleanup should use sync writes path
		err = storage.CleanupTombstones(ctx, 24*time.Hour)
		assert.NoError(t, err)
	})
}

// TestCompact_AdditionalCases tests more compact scenarios
func TestCompact_AdditionalCases(t *testing.T) {
	t.Run("CompactAfterMultipleOps", func(t *testing.T) {
		storage, cleanup := setupStorage(t)
		defer cleanup()

		ctx := context.Background()

		ns := "compact_multi"
		nsInfo := &NamespaceInfo{
			Name:      ns,
			Dimension: 128,
			CreatedAt: time.Now().Unix(),
		}
		require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

		// Insert, update, and delete vectors to create fragmentation
		for i := uint64(1); i <= 20; i++ {
			vec := &VectorValue{
				ID:        i,
				Embedding: make([]float32, 128),
				Metadata:  map[string]string{"index": string(rune('0' + i%10))},
				Version:   1,
			}
			require.NoError(t, storage.UpsertVector(ctx, ns, vec))
		}

		// Delete half of them
		for i := uint64(1); i <= 10; i++ {
			require.NoError(t, storage.DeleteVector(ctx, ns, i))
		}

		// Compact should succeed
		err := storage.Compact(ctx, ns)
		assert.NoError(t, err)
	})
}

// TestAdapter_ClosedStorageComprehensive tests all operations on closed storage
func TestAdapter_ClosedStorageComprehensive(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace first
	ns := "test"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Close storage
	_ = storage.Close()

	// Test all operations fail gracefully when storage is closed
	t.Run("UpsertVector", func(t *testing.T) {
		vec := &VectorValue{ID: 1, Embedding: make([]float32, 128), Version: 1}
		err := storage.UpsertVector(ctx, ns, vec)
		assert.Error(t, err)
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("GetVector", func(t *testing.T) {
		_, err := storage.GetVector(ctx, ns, 1)
		assert.Error(t, err)
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("DeleteVector", func(t *testing.T) {
		err := storage.DeleteVector(ctx, ns, 1)
		assert.Error(t, err)
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("BatchUpsertVectors", func(t *testing.T) {
		vecs := []*VectorValue{{ID: 1, Embedding: make([]float32, 128), Version: 1}}
		err := storage.BatchUpsertVectors(ctx, ns, vecs)
		assert.Error(t, err)
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("BatchDeleteVectors", func(t *testing.T) {
		err := storage.BatchDeleteVectors(ctx, ns, []uint64{1})
		assert.Error(t, err)
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("ScanByMetadata", func(t *testing.T) {
		opts := &ScanOptions{Limit: 10}
		_, err := storage.ScanByMetadata(ctx, ns, "field", "value", opts)
		assert.Error(t, err)
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("ScanNamespace", func(t *testing.T) {
		opts := &ScanOptions{Limit: 10}
		_, err := storage.ScanNamespace(ctx, ns, opts)
		assert.Error(t, err)
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("CreateNamespace", func(t *testing.T) {
		ns2 := &NamespaceInfo{Name: "test2", Dimension: 128}
		err := storage.CreateNamespace(ctx, ns2)
		assert.Error(t, err)
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("DeleteNamespace", func(t *testing.T) {
		err := storage.DeleteNamespace(ctx, ns)
		assert.Error(t, err)
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("GetStats", func(t *testing.T) {
		_, err := storage.GetStats(ctx, ns)
		assert.Error(t, err)
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("Compact", func(t *testing.T) {
		err := storage.Compact(ctx, ns)
		assert.Error(t, err)
		assert.Equal(t, ErrStorageClosed, err)
	})

	t.Run("CleanupTombstones", func(t *testing.T) {
		err := storage.CleanupTombstones(ctx, 24*time.Hour)
		assert.Error(t, err)
		assert.Equal(t, ErrStorageClosed, err)
	})
}

// TestClose_MultipleCloses tests calling Close multiple times
func TestClose_MultipleCloses(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	// First close
	err := storage.Close()
	assert.NoError(t, err)

	// Second close should also succeed (idempotent)
	err = storage.Close()
	assert.NoError(t, err)

	// Third close to be extra sure
	err = storage.Close()
	assert.NoError(t, err)
}
