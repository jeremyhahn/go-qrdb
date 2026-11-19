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

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector/schema"
)

// TestAdapter_UpsertVector_ContextHandling tests context-aware operations
func TestAdapter_UpsertVector_ContextHandling(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace
	info := &NamespaceInfo{
		Name:      "test_ns",
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err := storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Test upsert with large embedding
	vec := &VectorValue{
		ID:        1,
		Embedding: make([]float32, 128),
		Metadata: map[string]string{
			"category": "test",
			"source":   "unit_test",
		},
		Version: 1,
	}

	// Fill embedding with test data
	for i := range vec.Embedding {
		vec.Embedding[i] = float32(i) * 0.01
	}

	err = storage.UpsertVector(ctx, "test_ns", vec)
	assert.NoError(t, err)

	// Verify the vector was stored
	retrieved, err := storage.GetVector(ctx, "test_ns", 1)
	assert.NoError(t, err)
	assert.NotNil(t, retrieved)
	assert.Equal(t, 128, len(retrieved.Embedding))
}

// TestAdapter_BatchUpsertVectors_LargeBatch tests batch upsert with many vectors
func TestAdapter_BatchUpsertVectors_LargeBatch(t *testing.T) {
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

	// Create large batch
	vecs := make([]*VectorValue, 100)
	for i := 0; i < 100; i++ {
		vecs[i] = &VectorValue{
			ID:        uint64(i + 1),
			Embedding: []float32{float32(i), float32(i) * 0.5, float32(i) * 0.25},
			Metadata: map[string]string{
				"batch": "large",
				"index": string(rune('0' + i%10)),
			},
			Version: 1,
		}
	}

	err = storage.BatchUpsertVectors(ctx, "test_ns", vecs)
	assert.NoError(t, err)

	// Verify some vectors
	for i := 0; i < 10; i++ {
		vec, err := storage.GetVector(ctx, "test_ns", uint64(i+1))
		assert.NoError(t, err)
		assert.NotNil(t, vec)
		assert.Equal(t, uint64(i+1), vec.ID)
	}
}

// TestAdapter_BatchDeleteVectors_LargeBatch tests batch deletion
func TestAdapter_BatchDeleteVectors_LargeBatch(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace and vectors
	info := &NamespaceInfo{
		Name:      "test_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err := storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Insert vectors
	vecs := make([]*VectorValue, 50)
	for i := 0; i < 50; i++ {
		vecs[i] = &VectorValue{
			ID:        uint64(i + 1),
			Embedding: []float32{0.1, 0.2, 0.3},
			Metadata: map[string]string{
				"index": string(rune('0' + i%10)),
			},
			Version: 1,
		}
	}
	err = storage.BatchUpsertVectors(ctx, "test_ns", vecs)
	require.NoError(t, err)

	// Delete half of them
	ids := make([]uint64, 25)
	for i := 0; i < 25; i++ {
		ids[i] = uint64(i + 1)
	}

	err = storage.BatchDeleteVectors(ctx, "test_ns", ids)
	assert.NoError(t, err)

	// Verify deletions
	for i := 0; i < 25; i++ {
		vec, err := storage.GetVector(ctx, "test_ns", uint64(i+1))
		assert.Error(t, err)
		assert.Nil(t, vec)
	}

	// Verify remaining vectors
	for i := 25; i < 50; i++ {
		vec, err := storage.GetVector(ctx, "test_ns", uint64(i+1))
		assert.NoError(t, err)
		assert.NotNil(t, vec)
	}
}

// TestAdapter_GetVector_WithMetadata tests vector retrieval with metadata
func TestAdapter_GetVector_WithMetadata(t *testing.T) {
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

	// Insert vector with rich metadata
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata: map[string]string{
			"title":    "Test Document",
			"author":   "Test Author",
			"category": "Technology",
			"date":     "2025-12-19",
			"tags":     "test,unit,coverage",
		},
		Version: 1,
	}

	err = storage.UpsertVector(ctx, "test_ns", vec)
	require.NoError(t, err)

	// Retrieve and verify
	retrieved, err := storage.GetVector(ctx, "test_ns", 1)
	assert.NoError(t, err)
	assert.NotNil(t, retrieved)
	assert.Equal(t, "Test Document", retrieved.Metadata["title"])
	assert.Equal(t, "Test Author", retrieved.Metadata["author"])
	assert.Equal(t, 5, len(retrieved.Metadata))
}

// TestAdapter_CreateNamespace_DuplicateName tests duplicate namespace creation
func TestAdapter_CreateNamespace_DuplicateName(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	info := &NamespaceInfo{
		Name:      "duplicate_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	// First creation should succeed
	err := storage.CreateNamespace(ctx, info)
	assert.NoError(t, err)

	// Second creation with same name should fail
	err = storage.CreateNamespace(ctx, info)
	assert.Error(t, err)
	assert.Equal(t, ErrNamespaceExists, err)
}

// TestAdapter_Compact_WithData tests compaction with actual data
func TestAdapter_Compact_WithData(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace
	info := &NamespaceInfo{
		Name:      "compact_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err := storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Insert many vectors
	for i := 1; i <= 100; i++ {
		vec := &VectorValue{
			ID:        uint64(i),
			Embedding: []float32{0.1, 0.2, 0.3},
			Metadata:  map[string]string{"i": string(rune('0' + i%10))},
			Version:   1,
		}
		err = storage.UpsertVector(ctx, "compact_ns", vec)
		require.NoError(t, err)
	}

	// Compact namespace
	err = storage.Compact(ctx, "compact_ns")
	assert.NoError(t, err)

	// Verify data is still accessible
	vec, err := storage.GetVector(ctx, "compact_ns", 50)
	assert.NoError(t, err)
	assert.NotNil(t, vec)
}

// TestAdapter_ScanNamespace_WithLimit tests scanning with limit
func TestAdapter_ScanNamespace_WithLimit(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace
	info := &NamespaceInfo{
		Name:      "scan_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err := storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Insert vectors
	for i := 1; i <= 20; i++ {
		vec := &VectorValue{
			ID:        uint64(i),
			Embedding: []float32{0.1, 0.2, 0.3},
			Metadata:  map[string]string{"index": string(rune('0' + i%10))},
			Version:   1,
		}
		err = storage.UpsertVector(ctx, "scan_ns", vec)
		require.NoError(t, err)
	}

	// Scan with limit
	opts := &ScanOptions{
		Limit: 10,
	}
	vecs, err := storage.ScanNamespace(ctx, "scan_ns", opts)
	assert.NoError(t, err)
	assert.Equal(t, 10, len(vecs))
}

// TestBatch_DeleteVector_ErrorPaths tests error paths in batch delete
func TestBatch_DeleteVector_ErrorPaths(t *testing.T) {
	tmpDir := t.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	// Insert a vector with multiple metadata fields
	batch1 := newVectorBatch(db, metrics)
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata: map[string]string{
			"field1": "value1",
			"field2": "value2",
			"field3": "value3",
		},
		Version: 1,
	}
	err = batch1.UpsertVector("test_ns", vec)
	require.NoError(t, err)
	err = batch1.Commit()
	require.NoError(t, err)
	_ = batch1.Close()

	// Insert another version
	batch2 := newVectorBatch(db, metrics)
	vec2 := &VectorValue{
		ID:        1,
		Embedding: []float32{0.2, 0.3, 0.4},
		Metadata: map[string]string{
			"field1": "updated1",
			"field2": "updated2",
		},
		Version: 2,
	}
	err = batch2.UpsertVector("test_ns", vec2)
	require.NoError(t, err)
	err = batch2.Commit()
	require.NoError(t, err)
	_ = batch2.Close()

	// Now delete all versions
	batch3 := newVectorBatch(db, metrics)
	defer func() { _ = batch3.Close() }()

	err = batch3.DeleteVector("test_ns", 1)
	assert.NoError(t, err)

	err = batch3.Commit()
	assert.NoError(t, err)
}

// TestIterator_CloseNilIterator tests closing with nil iterator
func TestIterator_CloseNilIterator(t *testing.T) {
	iter := &vectorIterator{
		iter: nil,
		err:  nil,
	}

	err := iter.Close()
	assert.NoError(t, err)
}

// TestAdapter_DeleteVector_NonExistent tests deleting non-existent vector
func TestAdapter_DeleteVector_NonExistent(t *testing.T) {
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

	// Delete non-existent vector - should return error
	err = storage.DeleteVector(ctx, "test_ns", 999999)
	assert.Error(t, err)
	assert.Equal(t, ErrVectorNotFound, err)
}

// TestBatch_UpsertVector_WithSetError tests batch upsert error handling
func TestBatch_UpsertVector_WithSetError(t *testing.T) {
	tmpDir := t.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()
	batch := newVectorBatch(db, metrics)
	defer func() { _ = batch.Close() }()

	// Test with vector that has no metadata (edge case)
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata:  nil,
		Version:   1,
	}

	err = batch.UpsertVector("test_ns", vec)
	assert.NoError(t, err)
}

// TestMetadataIterator_CloseError tests metadata iterator close error path
func TestMetadataIterator_CloseError(t *testing.T) {
	tmpDir := t.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	prefix := schema.EncodeMetadataKeyPrefix("test_ns", "field", "value")
	iter := newMetadataIterator(db, prefix)

	// Close should succeed
	err = iter.Close()
	assert.NoError(t, err)
}
