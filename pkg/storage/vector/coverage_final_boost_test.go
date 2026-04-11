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

// TestBatch_UpsertVector_WithMultipleMetadata tests batch upsert with multiple metadata fields
func TestBatch_UpsertVector_WithMultipleMetadata(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "batch_multi_meta_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()
	batch := newVectorBatch(db, metrics)
	defer func() { _ = batch.Close() }()

	// Test with multiple metadata fields to exercise all indexing paths
	vec := &VectorValue{
		ID:        100,
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

	err = batch.UpsertVector("metadata_test", vec)
	assert.NoError(t, err)
	assert.Equal(t, 1, batch.Count())

	err = batch.Commit()
	assert.NoError(t, err)
}

// TestBatch_DeleteVector_AllVersionPaths tests deletion with all version iteration paths
func TestBatch_DeleteVector_AllVersionPaths(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "batch_delete_all_versions_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	// Insert multiple versions with different metadata counts
	for version := uint64(1); version <= 5; version++ {
		batch := newVectorBatch(db, metrics)
		metadataCount := int(version)
		metadata := make(map[string]string)
		for i := 0; i < metadataCount; i++ {
			metadata[string(rune('a'+i))] = string(rune('A' + i))
		}

		vec := &VectorValue{
			ID:        1,
			Embedding: []float32{float32(version)},
			Metadata:  metadata,
			Version:   version,
		}

		err = batch.UpsertVector("multi_version_test", vec)
		require.NoError(t, err)

		err = batch.Commit()
		require.NoError(t, err)
		_ = batch.Close()
	}

	// Delete all versions - exercises reverse index cleanup loop
	batch := newVectorBatch(db, metrics)
	defer func() { _ = batch.Close() }()

	err = batch.DeleteVector("multi_version_test", 1)
	assert.NoError(t, err)

	err = batch.Commit()
	assert.NoError(t, err)
}

// TestIterator_DecodeErrors tests iterator decode error paths
func TestIterator_DecodeErrors(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "iterator_decode_error_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() {
		_ = db.Close()
		_ = os.RemoveAll(tmpDir)
	}()

	t.Run("VectorIterator_InvalidKey", func(t *testing.T) {
		// Insert a key that will fail to decode
		invalidKey := []byte{schema.PrefixVector, schema.Separator, 0xFF, 0xFE}
		err := db.Set(invalidKey, []byte{0x01}, pebble.NoSync)
		require.NoError(t, err)

		prefix := []byte{schema.PrefixVector, schema.Separator}
		iter := newVectorIterator(db, prefix)
		defer func() { _ = iter.Close() }()

		// Should fail on decode
		hasNext := iter.Next()
		assert.False(t, hasNext)
		assert.Error(t, iter.Err())
	})

	t.Run("MetadataIterator_InvalidKey", func(t *testing.T) {
		// Clean DB
		_ = db.Close()
		_ = os.RemoveAll(tmpDir)

		db, err = pebble.Open(tmpDir, &pebble.Options{})
		require.NoError(t, err)

		// Insert invalid metadata key
		invalidKey := []byte{schema.PrefixMetadata, schema.Separator, 0xFF, 0xFE}
		err := db.Set(invalidKey, []byte{}, pebble.NoSync)
		require.NoError(t, err)

		prefix := []byte{schema.PrefixMetadata, schema.Separator}
		iter := newMetadataIterator(db, prefix)
		defer func() { _ = iter.Close() }()

		hasNext := iter.Next()
		assert.False(t, hasNext)
		assert.Error(t, iter.Err())
	})
}

// TestAdapter_CleanupTombstones_Coverage tests cleanup paths
func TestAdapter_CleanupTombstones_Coverage(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "cleanup_coverage_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	// Create multiple namespaces
	for i := 0; i < 3; i++ {
		nsInfo := &NamespaceInfo{
			Name:      string(rune('a' + i)),
			CreatedAt: time.Now().Unix(),
		}
		err = storage.CreateNamespace(ctx, nsInfo)
		require.NoError(t, err)

		// Insert and delete vectors to create tombstones
		for j := uint64(1); j <= 3; j++ {
			vec := &VectorValue{
				ID:        j,
				Embedding: []float32{float32(j)},
				Metadata:  map[string]string{"key": "value"},
				Version:   1,
			}

			err = storage.UpsertVector(ctx, string(rune('a'+i)), vec)
			require.NoError(t, err)

			err = storage.DeleteVector(ctx, string(rune('a'+i)), j)
			require.NoError(t, err)
		}
	}

	// Wait for tombstones to age
	time.Sleep(10 * time.Millisecond)

	// Cleanup - exercises namespace iteration and tombstone cleanup
	err = storage.CleanupTombstones(ctx, 1*time.Millisecond)
	assert.NoError(t, err)
}

// TestAdapter_Compact_Coverage tests compaction paths
func TestAdapter_Compact_Coverage(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "compact_coverage_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	nsInfo := &NamespaceInfo{
		Name:      "compact_test",
		CreatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, nsInfo)
	require.NoError(t, err)

	// Insert data to compact
	for i := uint64(1); i <= 20; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: []float32{float32(i)},
			Metadata:  map[string]string{"key": "value"},
			Version:   1,
		}
		err = storage.UpsertVector(ctx, "compact_test", vec)
		require.NoError(t, err)
	}

	// Compact
	err = storage.Compact(ctx, "compact_test")
	assert.NoError(t, err)
}

// TestIterator_NextCoverage tests all Next() path combinations
func TestIterator_NextCoverage(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "iterator_next_coverage")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	namespace := "next_test"

	// Insert test data
	for i := 1; i <= 3; i++ {
		vec := &schema.VectorValue{
			ID:        uint64(i),
			Embedding: []float32{float32(i)},
			Metadata:  map[string]string{"key": "val"},
			Version:   1,
		}

		key := schema.EncodeVectorKey(namespace, uint64(i), 1)
		value, err := schema.EncodeVectorValue(vec)
		require.NoError(t, err)

		err = db.Set(key, value, pebble.NoSync)
		require.NoError(t, err)
	}

	t.Run("VectorIterator_MultipleNext", func(t *testing.T) {
		prefix := schema.EncodeNamespacePrefix(namespace)
		iter := newVectorIterator(db, prefix)
		defer func() { _ = iter.Close() }()

		count := 0
		// First Next() - !iter.Valid() path, calls First()
		if iter.Next() {
			count++
			// Second Next() - iter.Valid() path, calls Next()
			if iter.Next() {
				count++
				// Third Next() - iter.Valid() path
				if iter.Next() {
					count++
				}
			}
		}

		assert.Equal(t, 3, count)
		assert.NoError(t, iter.Err())
	})

	t.Run("MetadataIterator_MultipleNext", func(t *testing.T) {
		// Setup metadata indexes
		for i := 1; i <= 3; i++ {
			metaKey := schema.EncodeMetadataKey(namespace, "key", "val", uint64(i), 1)
			err = db.Set(metaKey, []byte{}, pebble.NoSync)
			require.NoError(t, err)
		}

		prefix := schema.EncodeMetadataKeyPrefix(namespace, "key", "val")
		iter := newMetadataIterator(db, prefix)
		defer func() { _ = iter.Close() }()

		count := 0
		// Exercise both First() and Next() paths
		if iter.Next() {
			count++
			if iter.Next() {
				count++
				if iter.Next() {
					count++
				}
			}
		}

		assert.Equal(t, 3, count)
		assert.NoError(t, iter.Err())
	})
}

// TestAdapter_ScanNamespace_NilOptions tests scan with nil options
func TestAdapter_ScanNamespace_NilOptions(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "scan_nil_opts_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	nsInfo := &NamespaceInfo{
		Name:      "scan_test",
		CreatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, nsInfo)
	require.NoError(t, err)

	// Insert vectors
	for i := uint64(1); i <= 5; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: []float32{float32(i)},
			Metadata:  map[string]string{"key": "value"},
			Version:   1,
		}
		err = storage.UpsertVector(ctx, "scan_test", vec)
		require.NoError(t, err)
	}

	// Scan with nil options - exercises default path
	results, err := storage.ScanNamespace(ctx, "scan_test", nil)
	assert.NoError(t, err)
	assert.NotEmpty(t, results)
}

// TestAdapter_Close_Coverage tests close with cleanup
func TestAdapter_Close_Coverage(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "close_coverage_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)

	ctx := context.Background()

	// Create namespace
	nsInfo := &NamespaceInfo{
		Name:      "close_test",
		CreatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, nsInfo)
	require.NoError(t, err)

	// Insert data
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2},
		Metadata:  map[string]string{"key": "value"},
		Version:   1,
	}
	err = storage.UpsertVector(ctx, "close_test", vec)
	require.NoError(t, err)

	// Close - exercises cleanup paths
	err = storage.Close()
	assert.NoError(t, err)

	// Verify storage is closed
	_, err = storage.GetVector(ctx, "close_test", 1)
	assert.Equal(t, ErrStorageClosed, err)
}
