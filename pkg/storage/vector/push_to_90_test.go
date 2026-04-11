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
)

// TestAdapter_UpsertVector_FullCodePath tests the complete upsert path including sync writes
func TestAdapter_UpsertVector_FullCodePath(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "upsert_full_path_test")
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
		Name:      "test_ns",
		Dimension: 5,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Test upsert with all paths
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3, 0.4, 0.5},
		Metadata: map[string]string{
			"field1": "value1",
			"field2": "value2",
			"field3": "value3",
		},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
		Version:   1,
	}

	err = storage.UpsertVector(ctx, "test_ns", vec)
	assert.NoError(t, err)

	// Verify it was stored
	retrieved, err := storage.GetVector(ctx, "test_ns", 1)
	assert.NoError(t, err)
	assert.NotNil(t, retrieved)
	assert.Equal(t, uint64(1), retrieved.ID)
}

// TestAdapter_BatchUpsertVectors_FullPath tests batch upsert with full code paths
func TestAdapter_BatchUpsertVectors_FullPath(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "batch_upsert_full_test")
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
		Name:      "test_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Create batch of vectors
	vecs := make([]*VectorValue, 50)
	for i := 0; i < 50; i++ {
		vecs[i] = &VectorValue{
			ID:        uint64(i + 1),
			Embedding: []float32{float32(i) * 0.1, float32(i) * 0.2, float32(i) * 0.3},
			Metadata: map[string]string{
				"batch":  "test",
				"number": string(rune('0' + i%10)),
			},
			Version: 1,
		}
	}

	err = storage.BatchUpsertVectors(ctx, "test_ns", vecs)
	assert.NoError(t, err)

	// Verify storage
	for i := 0; i < 10; i++ {
		vec, err := storage.GetVector(ctx, "test_ns", uint64(i+1))
		assert.NoError(t, err)
		assert.NotNil(t, vec)
	}
}

// TestAdapter_BatchDeleteVectors_FullPath tests batch delete with full code paths
func TestAdapter_BatchDeleteVectors_FullPath(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "batch_delete_full_test")
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
		Name:      "test_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Insert vectors
	for i := 1; i <= 30; i++ {
		vec := &VectorValue{
			ID:        uint64(i),
			Embedding: []float32{0.1, 0.2, 0.3},
			Metadata: map[string]string{
				"index": string(rune('0' + i%10)),
			},
			Version: 1,
		}
		err = storage.UpsertVector(ctx, "test_ns", vec)
		require.NoError(t, err)
	}

	// Batch delete
	ids := make([]uint64, 15)
	for i := 0; i < 15; i++ {
		ids[i] = uint64(i + 1)
	}

	err = storage.BatchDeleteVectors(ctx, "test_ns", ids)
	assert.NoError(t, err)

	// Verify deletions
	for i := 0; i < 15; i++ {
		vec, err := storage.GetVector(ctx, "test_ns", uint64(i+1))
		assert.Error(t, err)
		assert.Nil(t, vec)
	}
}

// TestAdapter_CreateNamespace_AllPaths tests namespace creation edge cases
func TestAdapter_CreateNamespace_AllPaths(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "create_namespace_full_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = true
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	// Create namespace with all fields
	info := &NamespaceInfo{
		Name:        "full_ns",
		VectorCount: 0,
		Dimension:   768,
		CreatedAt:   time.Now().Unix(),
		UpdatedAt:   time.Now().Unix(),
	}

	err = storage.CreateNamespace(ctx, info)
	assert.NoError(t, err)

	// Verify it exists
	retrieved, err := storage.GetNamespaceInfo(ctx, "full_ns")
	assert.NoError(t, err)
	assert.NotNil(t, retrieved)
	assert.Equal(t, "full_ns", retrieved.Name)
	assert.Equal(t, uint32(768), retrieved.Dimension)
}

// TestAdapter_GetVector_AllPaths tests GetVector with various scenarios
func TestAdapter_GetVector_AllPaths(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "get_vector_full_test")
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
		Dimension: 4,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Insert vector with all metadata
	vec := &VectorValue{
		ID:        100,
		Embedding: []float32{0.1, 0.2, 0.3, 0.4},
		Metadata: map[string]string{
			"title":    "Test Document",
			"author":   "John Doe",
			"category": "Tech",
			"tags":     "golang,database",
		},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
		Version:   1,
	}

	err = storage.UpsertVector(ctx, "test_ns", vec)
	require.NoError(t, err)

	// Retrieve and verify all fields
	retrieved, err := storage.GetVector(ctx, "test_ns", 100)
	assert.NoError(t, err)
	assert.NotNil(t, retrieved)
	assert.Equal(t, uint64(100), retrieved.ID)
	assert.Equal(t, 4, len(retrieved.Embedding))
	assert.Equal(t, 4, len(retrieved.Metadata))
	assert.Equal(t, "Test Document", retrieved.Metadata["title"])
}

// TestAdapter_CleanupTombstones_IteratorError tests cleanup with edge cases
func TestAdapter_CleanupTombstones_IteratorError(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "cleanup_iterator_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	// Create multiple namespaces
	for i := 1; i <= 3; i++ {
		info := &NamespaceInfo{
			Name:      string(rune('a'+i-1)) + "_ns",
			Dimension: 3,
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		}
		err = storage.CreateNamespace(ctx, info)
		require.NoError(t, err)
	}

	// Run cleanup on multiple namespaces
	err = storage.CleanupTombstones(ctx, time.Hour)
	assert.NoError(t, err)
}

// TestAdapter_NewPebbleVectorStorage_LoadNamespaces tests loading existing namespaces
func TestAdapter_NewPebbleVectorStorage_LoadNamespaces(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "load_namespaces_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	log := &testLogger{}

	// Create storage and add namespaces
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

	// Add a vector to establish ID sequence
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata:  map[string]string{"test": "data"},
		Version:   1,
	}
	err = storage1.UpsertVector(ctx, "persist_ns", vec)
	require.NoError(t, err)

	_ = storage1.Close()

	// Reopen storage - should load existing namespace
	storage2, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage2.Close() }()

	// Verify namespace was loaded
	loaded, err := storage2.GetNamespaceInfo(ctx, "persist_ns")
	assert.NoError(t, err)
	assert.NotNil(t, loaded)
	assert.Equal(t, "persist_ns", loaded.Name)
}

// TestBatch_UpsertAndDeleteCombined tests combined batch operations
func TestBatch_UpsertAndDeleteCombined(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "batch_combined_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	// First batch: Insert multiple vectors
	batch1 := newVectorBatch(db, metrics)
	for i := 1; i <= 10; i++ {
		vec := &VectorValue{
			ID:        uint64(i),
			Embedding: []float32{0.1, 0.2, 0.3},
			Metadata: map[string]string{
				"index": string(rune('0' + i%10)),
			},
			Version: 1,
		}
		err = batch1.UpsertVector("test_ns", vec)
		require.NoError(t, err)
	}
	err = batch1.Commit()
	require.NoError(t, err)
	_ = batch1.Close()

	// Second batch: Delete some of them
	batch2 := newVectorBatch(db, metrics)
	for i := 1; i <= 5; i++ {
		err = batch2.DeleteVector("test_ns", uint64(i))
		require.NoError(t, err)
	}
	err = batch2.Commit()
	require.NoError(t, err)
	_ = batch2.Close()
}
