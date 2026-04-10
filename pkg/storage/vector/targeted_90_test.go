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

// TestCleanupTombstones_CommitPath tests the commit success path in cleanup
func TestCleanupTombstones_CommitPath(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("vector_test_%d", time.Now().UnixNano()))
	require.NoError(t, os.MkdirAll(tmpDir, 0755))
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = true // Test the SyncWrites path
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	// Create namespace
	ns := "test_cleanup_commit"
	err = storage.CreateNamespace(ctx, &NamespaceInfo{
		Name:      ns,
		Dimension: 3,
	})
	require.NoError(t, err)

	// Create and immediately delete a vector to create a tombstone
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

	err = storage.DeleteVector(ctx, ns, 1)
	require.NoError(t, err)

	// Wait to ensure tombstone is old enough
	time.Sleep(100 * time.Millisecond)

	// Cleanup tombstones - should trigger count > 0 and commit path
	err = storage.CleanupTombstones(ctx, 50*time.Millisecond)
	require.NoError(t, err)
}

// TestBatchUpsert_MetadataIndexing tests all paths in batch upsert
func TestBatchUpsert_MetadataIndexing(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	batch := newVectorBatch(storage.db, storage.metrics)
	defer func() { _ = batch.Close() }()

	// Test with vector having multiple metadata fields
	vec := &VectorValue{
		ID:        1,
		Version:   1,
		Embedding: []float32{1.0, 2.0, 3.0},
		Metadata: map[string]string{
			"key1": "value1",
			"key2": "value2",
			"key3": "value3",
		},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	err := batch.UpsertVector("test_ns", vec)
	assert.NoError(t, err)

	// Commit the batch to test the metrics path
	err = batch.Commit()
	assert.NoError(t, err)
}

// TestBatchDelete_ReverseIndexIteration tests reverse index iteration in batch delete
func TestBatchDelete_ReverseIndexIteration(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace
	ns := "test_batch_del_rev"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{
		Name:      ns,
		Dimension: 3,
	})
	require.NoError(t, err)

	// Insert a vector with metadata
	vec := &VectorValue{
		ID:        1,
		Version:   1,
		Embedding: []float32{1.0, 2.0, 3.0},
		Metadata: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	// Use batch to delete - this tests the reverse index iteration
	batch := newVectorBatch(storage.db, storage.metrics)
	defer func() { _ = batch.Close() }()

	err = batch.DeleteVector(ns, 1)
	assert.NoError(t, err)

	err = batch.Commit()
	assert.NoError(t, err)
}

// TestUpsertVector_ContextTimeout tests context cancellation path
func TestUpsertVector_ContextTimeout(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	// Create namespace first
	ctx := context.Background()
	ns := "test_ctx_timeout"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{
		Name:      ns,
		Dimension: 3,
	})
	require.NoError(t, err)

	// Create a context with very short timeout
	ctx2, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()

	// Wait for context to expire
	time.Sleep(10 * time.Millisecond)

	vec := &VectorValue{
		ID:        1,
		Version:   1,
		Embedding: []float32{1.0, 2.0, 3.0},
		Metadata:  map[string]string{"key": "value"},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	// This might fail with context deadline or succeed depending on timing
	_ = storage.UpsertVector(ctx2, ns, vec)
}

// TestIteratorNext_FirstCallPath tests the first call path in Next()
func TestIteratorNext_FirstCallPath(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace with vectors
	ns := "test_iter_first"
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

	// Create iterator
	prefix := schema.EncodeNamespacePrefix(ns)
	iter := newVectorIterator(storage.db, prefix)
	defer func() { _ = iter.Close() }()

	// First Next() call takes the !Valid() -> First() path
	hasNext := iter.Next()
	assert.True(t, hasNext)

	val, err := iter.Value()
	assert.NoError(t, err)
	assert.NotNil(t, val)

	// Second Next() call takes the Valid() -> Next() path
	hasNext = iter.Next()
	assert.False(t, hasNext) // No more items
}

// TestUpsertVector_UpdateWithDifferentMetadata tests version update path with metadata changes
func TestUpsertVector_UpdateWithDifferentMetadata(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace
	ns := "test_update_meta"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{
		Name:      ns,
		Dimension: 3,
	})
	require.NoError(t, err)

	// Insert version 1
	vec1 := &VectorValue{
		ID:        1,
		Version:   1,
		Embedding: []float32{1.0, 2.0, 3.0},
		Metadata: map[string]string{
			"key1": "old_value1",
			"key2": "old_value2",
		},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.UpsertVector(ctx, ns, vec1)
	require.NoError(t, err)

	// Insert version 2 with different metadata - triggers version > 1 path
	vec2 := &VectorValue{
		ID:        1,
		Version:   2,
		Embedding: []float32{4.0, 5.0, 6.0},
		Metadata: map[string]string{
			"key1": "new_value1",
			"key3": "new_value3",
		},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.UpsertVector(ctx, ns, vec2)
	require.NoError(t, err)

	// Verify update
	retrieved, err := storage.GetVector(ctx, ns, 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), retrieved.Version)
}

// TestCleanupTombstones_ShortKeyPath tests cleanup with keys that are too short
func TestCleanupTombstones_ShortKeyPath(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace
	ns := "test_cleanup_short"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{
		Name:      ns,
		Dimension: 3,
	})
	require.NoError(t, err)

	// Create and delete vectors to generate tombstones
	for i := uint64(1); i <= 3; i++ {
		vec := &VectorValue{
			ID:        i,
			Version:   1,
			Embedding: []float32{1.0, 2.0, 3.0},
			Metadata:  map[string]string{"key": fmt.Sprintf("val%d", i)},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		}
		err = storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)

		err = storage.DeleteVector(ctx, ns, i)
		require.NoError(t, err)
	}

	time.Sleep(100 * time.Millisecond)

	// Cleanup - tests the len(key) >= 8 check and timestamp extraction
	err = storage.CleanupTombstones(ctx, 50*time.Millisecond)
	require.NoError(t, err)
}

// TestMetadataIterator_FirstPath tests metadata iterator first call path
func TestMetadataIterator_FirstPath(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace
	ns := "test_meta_iter_first"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{
		Name:      ns,
		Dimension: 3,
	})
	require.NoError(t, err)

	// Add vectors with metadata
	for i := uint64(1); i <= 2; i++ {
		vec := &VectorValue{
			ID:        i,
			Version:   1,
			Embedding: []float32{1.0, 2.0, 3.0},
			Metadata:  map[string]string{"category": "test"},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		}
		err = storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)
	}

	// Create metadata iterator
	prefix := schema.EncodeMetadataKeyPrefix(ns, "category", "test")
	iter := newMetadataIterator(storage.db, prefix)
	defer func() { _ = iter.Close() }()

	// First Next() call
	hasNext := iter.Next()
	assert.True(t, hasNext)

	// Second Next() call
	hasNext = iter.Next()
	assert.True(t, hasNext)

	// Third Next() call should be false
	hasNext = iter.Next()
	assert.False(t, hasNext)
}
