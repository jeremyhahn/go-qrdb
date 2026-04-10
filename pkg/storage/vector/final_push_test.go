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

// TestBatch_UpsertVector_MetadataIndexing tests metadata index creation
func TestBatch_UpsertVector_MetadataIndexing(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "batch_metadata_index_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()
	batch := newVectorBatch(db, metrics)
	defer func() { _ = batch.Close() }()

	// Test with vector that has large metadata
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata: map[string]string{
			"field1": "value1",
			"field2": "value2",
			"field3": "value3",
			"field4": "value4",
			"field5": "value5",
			"field6": "value6",
			"field7": "value7",
			"field8": "value8",
		},
		Version: 1,
	}

	err = batch.UpsertVector("test_ns", vec)
	assert.NoError(t, err)

	// Test commit
	err = batch.Commit()
	assert.NoError(t, err)

	// Verify metadata was indexed by checking reverse indexes exist
	reversePrefix := schema.EncodeReverseKeyPrefix("test_ns", 1)
	iter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: reversePrefix,
		UpperBound: schema.PrefixSuccessor(reversePrefix),
	})
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		count++
	}
	assert.Equal(t, 8, count) // Should have 8 reverse index entries
}

// TestBatch_DeleteVector_ReverseIndexCleanup tests reverse index cleanup during deletion
func TestBatch_DeleteVector_ReverseIndexCleanup(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "batch_delete_reverse_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	// First insert a vector with metadata
	batch1 := newVectorBatch(db, metrics)
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata: map[string]string{
			"field1": "value1",
			"field2": "value2",
			"field3": "value3",
			"field4": "value4",
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

	// Verify reverse indexes were deleted
	reversePrefix := schema.EncodeReverseKeyPrefix("test_ns", 1)
	iter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: reversePrefix,
		UpperBound: schema.PrefixSuccessor(reversePrefix),
	})
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		count++
	}
	assert.Equal(t, 0, count) // All reverse indexes should be deleted
}

// TestAdapter_CleanupTombstones_WithSyncWrites tests cleanup with sync writes enabled
func TestAdapter_CleanupTombstones_WithSyncWrites(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "tombstone_sync_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = true // Enable sync writes
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	// Create namespace
	info := &NamespaceInfo{
		Name:      "tomb_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Insert and delete vectors to create tombstones
	for i := 1; i <= 5; i++ {
		vec := &VectorValue{
			ID:        uint64(i),
			Embedding: []float32{0.1, 0.2, 0.3},
			Metadata:  map[string]string{"test": "data"},
			Version:   1,
		}
		err = storage.UpsertVector(ctx, "tomb_ns", vec)
		require.NoError(t, err)

		err = storage.DeleteVector(ctx, "tomb_ns", uint64(i))
		require.NoError(t, err)
	}

	// Wait a bit
	time.Sleep(10 * time.Millisecond)

	// Cleanup tombstones with sync writes
	err = storage.CleanupTombstones(ctx, 1*time.Nanosecond)
	assert.NoError(t, err)
}

// TestAdapter_Compact_WithSyncWrites tests compaction with sync writes
func TestAdapter_Compact_WithSyncWrites(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "compact_sync_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = true
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	// Create namespace
	info := &NamespaceInfo{
		Name:      "compact_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Insert vectors
	for i := 1; i <= 20; i++ {
		vec := &VectorValue{
			ID:        uint64(i),
			Embedding: []float32{0.1, 0.2, 0.3},
			Metadata:  map[string]string{"i": string(rune('0' + i%10))},
			Version:   1,
		}
		err = storage.UpsertVector(ctx, "compact_ns", vec)
		require.NoError(t, err)
	}

	// Compact
	err = storage.Compact(ctx, "compact_ns")
	assert.NoError(t, err)
}

// TestAdapter_CleanupTombstones_NoTombstones tests cleanup when no tombstones exist
func TestAdapter_CleanupTombstones_NoTombstones(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace without any vectors
	info := &NamespaceInfo{
		Name:      "empty_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err := storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Cleanup should succeed with no tombstones
	err = storage.CleanupTombstones(ctx, time.Hour)
	assert.NoError(t, err)
}

// TestBatch_DeleteVector_NoMetadata tests deletion of vector without metadata
func TestBatch_DeleteVector_NoMetadata(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "batch_delete_no_metadata_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	// Insert vector without metadata
	batch1 := newVectorBatch(db, metrics)
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata:  nil, // No metadata
		Version:   1,
	}
	err = batch1.UpsertVector("test_ns", vec)
	require.NoError(t, err)
	err = batch1.Commit()
	require.NoError(t, err)
	_ = batch1.Close()

	// Delete it
	batch2 := newVectorBatch(db, metrics)
	defer func() { _ = batch2.Close() }()

	err = batch2.DeleteVector("test_ns", 1)
	assert.NoError(t, err)

	err = batch2.Commit()
	assert.NoError(t, err)
}
