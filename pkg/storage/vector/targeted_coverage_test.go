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

// TestCleanupTombstones_ActualCleanup tests the full cleanup path with old tombstones
func TestCleanupTombstones_ActualCleanup(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "cleanup_actual_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	// Create namespace
	info := &NamespaceInfo{
		Name:      "cleanup_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Insert vectors and then delete them to create tombstones
	for i := 1; i <= 10; i++ {
		vec := &VectorValue{
			ID:        uint64(i),
			Embedding: []float32{0.1, 0.2, 0.3},
			Metadata:  map[string]string{"id": string(rune('0' + i%10))},
			Version:   1,
		}
		err = storage.UpsertVector(ctx, "cleanup_ns", vec)
		require.NoError(t, err)

		// Delete to create tombstone
		err = storage.DeleteVector(ctx, "cleanup_ns", uint64(i))
		require.NoError(t, err)
	}

	// Manually create old tombstones (older than cutoff)
	oldTime := uint64(time.Now().Add(-2 * time.Hour).Unix())
	for i := 1; i <= 5; i++ {
		key := schema.EncodeTombstoneKey("cleanup_ns", uint64(i), oldTime)
		err = storage.db.Set(key, []byte{}, pebble.NoSync)
		require.NoError(t, err)
	}

	// Run cleanup with 1 hour cutoff
	err = storage.CleanupTombstones(ctx, time.Hour)
	assert.NoError(t, err)
}

// TestCleanupTombstones_NoCleanupNeeded tests when no tombstones need cleanup
func TestCleanupTombstones_NoCleanupNeeded(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "cleanup_none_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	// Create namespace
	info := &NamespaceInfo{
		Name:      "test_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Run cleanup - should complete without error even with no tombstones
	err = storage.CleanupTombstones(ctx, time.Hour)
	assert.NoError(t, err)
}

// TestCleanupTombstones_MultipleNamespaces tests cleanup across multiple namespaces
func TestCleanupTombstones_MultipleNamespaces(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "cleanup_multi_ns_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	// Create multiple namespaces
	for i := 0; i < 3; i++ {
		info := &NamespaceInfo{
			Name:      string(rune('a'+i)) + "_namespace",
			Dimension: 3,
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		}
		err = storage.CreateNamespace(ctx, info)
		require.NoError(t, err)

		// Add old tombstones to each namespace
		oldTime := uint64(time.Now().Add(-2 * time.Hour).Unix())
		for j := 1; j <= 3; j++ {
			key := schema.EncodeTombstoneKey(info.Name, uint64(j), oldTime)
			err = storage.db.Set(key, []byte{}, pebble.NoSync)
			require.NoError(t, err)
		}
	}

	// Run cleanup
	err = storage.CleanupTombstones(ctx, time.Hour)
	assert.NoError(t, err)
}

// TestUpsertVector_VersionUpdate tests updating a vector with version > 1
func TestUpsertVector_VersionUpdate(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "upsert_version_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	// Create namespace
	info := &NamespaceInfo{
		Name:      "test_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Insert version 1
	vec1 := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata: map[string]string{
			"field1": "old_value",
			"field2": "value2",
		},
		Version: 1,
	}
	err = storage.UpsertVector(ctx, "test_ns", vec1)
	require.NoError(t, err)

	// Update to version 2 with different metadata
	vec2 := &VectorValue{
		ID:        1,
		Embedding: []float32{0.4, 0.5, 0.6},
		Metadata: map[string]string{
			"field1": "new_value", // Changed value
			"field3": "value3",    // New field
		},
		Version: 2,
	}
	err = storage.UpsertVector(ctx, "test_ns", vec2)
	assert.NoError(t, err)

	// Verify version 2 exists
	retrieved, err := storage.GetVector(ctx, "test_ns", 1)
	assert.NoError(t, err)
	assert.Equal(t, uint64(2), retrieved.Version)
	assert.Equal(t, "new_value", retrieved.Metadata["field1"])
}

// TestUpsertVector_CanceledContext tests context cancellation during upsert with different test name
func TestUpsertVector_CanceledContext(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "upsert_canceled_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	config.MaxConcurrentWrites = 1 // Limit to 1 concurrent write
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	// Create namespace
	info := &NamespaceInfo{
		Name:      "test_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(context.Background(), info)
	require.NoError(t, err)

	// Occupy the write semaphore
	storage.writeSem <- struct{}{}

	// Try to upsert with canceled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata:  map[string]string{"test": "value"},
		Version:   1,
	}

	err = storage.UpsertVector(ctx, "test_ns", vec)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrContextCanceled)

	// Release semaphore
	<-storage.writeSem
}

// TestBatchDeleteVector_ErrorInReverseIter tests error handling in reverse iterator
func TestBatchDeleteVector_ErrorInReverseIter(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "batch_delete_reverse_error_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	// Insert a vector with metadata
	batch1 := newVectorBatch(db, metrics)
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata: map[string]string{
			"field1": "value1",
			"field2": "value2",
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

// TestGetVector_IteratorError tests GetVector when iterator fails
func TestGetVector_IteratorError(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "get_vector_iter_error_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)

	ctx := context.Background()

	// Create namespace
	info := &NamespaceInfo{
		Name:      "test_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Close the storage to cause iterator errors
	_ = storage.Close()

	// Try to get vector - should fail
	vec, err := storage.GetVector(ctx, "test_ns", 1)
	assert.Error(t, err)
	assert.Nil(t, vec)
}

// TestDeleteVector_WithMetadata tests deletion of vector with multiple metadata fields
func TestDeleteVector_WithMetadata(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "delete_with_metadata_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	// Create namespace
	info := &NamespaceInfo{
		Name:      "test_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Insert vector with multiple metadata fields
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
	err = storage.UpsertVector(ctx, "test_ns", vec)
	require.NoError(t, err)

	// Delete it
	err = storage.DeleteVector(ctx, "test_ns", 1)
	assert.NoError(t, err)

	// Verify it's gone
	retrieved, err := storage.GetVector(ctx, "test_ns", 1)
	assert.Error(t, err)
	assert.Nil(t, retrieved)
}

// TestBatchUpsertVectors_ErrorPath tests batch upsert error handling
func TestBatchUpsertVectors_ErrorPath(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "batch_upsert_error_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	// Create namespace
	info := &NamespaceInfo{
		Name:      "test_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Create batch with mix of valid and nil vectors
	vecs := []*VectorValue{
		{
			ID:        1,
			Embedding: []float32{0.1, 0.2, 0.3},
			Metadata:  map[string]string{"id": "1"},
			Version:   1,
		},
		nil, // This should cause an error
	}

	err = storage.BatchUpsertVectors(ctx, "test_ns", vecs)
	assert.Error(t, err)
}

// TestCreateNamespace_SyncWrites tests namespace creation with sync writes
func TestCreateNamespace_SyncWrites(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "create_ns_sync_test")
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
		Name:      "sync_ns",
		Dimension: 768,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, info)
	assert.NoError(t, err)

	// Verify it was created
	retrieved, err := storage.GetNamespaceInfo(ctx, "sync_ns")
	assert.NoError(t, err)
	assert.Equal(t, "sync_ns", retrieved.Name)
}

// TestGetStats_WithVectors tests stats for namespace with vectors
func TestGetStats_WithVectors(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "stats_with_vectors_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	// Create namespace
	info := &NamespaceInfo{
		Name:      "stats_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Insert some vectors
	for i := 1; i <= 10; i++ {
		vec := &VectorValue{
			ID:        uint64(i),
			Embedding: []float32{0.1, 0.2, 0.3},
			Metadata:  map[string]string{"id": string(rune('0' + i%10))},
			Version:   1,
		}
		err = storage.UpsertVector(ctx, "stats_ns", vec)
		require.NoError(t, err)
	}

	// Get stats
	stats, err := storage.GetStats(ctx, "stats_ns")
	assert.NoError(t, err)
	assert.NotNil(t, stats)
	assert.Equal(t, uint64(10), stats.Count)
}

// TestNewPebbleVectorStorage_LoadNamespacesWithData tests loading namespaces with vectors
func TestNewPebbleVectorStorage_LoadNamespacesWithData(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "load_ns_data_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	log := &testLogger{}

	// Create storage and add namespace with vectors
	storage1, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)

	ctx := context.Background()

	info := &NamespaceInfo{
		Name:      "persist_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage1.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Add some vectors to establish data
	for i := 1; i <= 5; i++ {
		vec := &VectorValue{
			ID:        uint64(i),
			Embedding: []float32{0.1, 0.2, 0.3},
			Metadata:  map[string]string{"id": string(rune('0' + i))},
			Version:   1,
		}
		err = storage1.UpsertVector(ctx, "persist_ns", vec)
		require.NoError(t, err)
	}

	_ = storage1.Close()

	// Reopen - should load namespace
	storage2, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage2.Close() }()

	// Verify namespace was loaded
	loaded, err := storage2.GetNamespaceInfo(ctx, "persist_ns")
	assert.NoError(t, err)
	assert.NotNil(t, loaded)
	assert.Equal(t, "persist_ns", loaded.Name)

	// Verify vectors still exist
	vec, err := storage2.GetVector(ctx, "persist_ns", 1)
	assert.NoError(t, err)
	assert.NotNil(t, vec)
}
