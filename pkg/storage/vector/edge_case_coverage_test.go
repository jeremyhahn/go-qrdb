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
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewPebbleVectorStorage_DBOpenError tests database open failure
func TestNewPebbleVectorStorage_DBOpenError(t *testing.T) {
	// Create a file where a directory should be
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("vector_file_%d", time.Now().UnixNano()))
	file, err := os.Create(tmpFile)
	require.NoError(t, err)
	_ = file.Close()
	defer func() { _ = os.Remove(tmpFile) }()

	// Try to use the file path as a directory (should fail)
	config := DefaultConfig(tmpFile)
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	assert.Error(t, err)
	assert.Nil(t, storage)
}

// TestLoadNamespaces_ShortSequenceBytes tests loading with short sequence bytes
func TestLoadNamespaces_ShortSequenceBytes(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("vector_test_%d", time.Now().UnixNano()))
	require.NoError(t, os.MkdirAll(tmpDir, 0755))
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	log := &testLogger{}

	// Create storage
	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)

	ctx := context.Background()
	nsInfo := &NamespaceInfo{
		Name:      "test_ns",
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Write short sequence bytes (less than 8 bytes) directly
	_ = storage.db.Set([]byte("seq\x00test_ns"), []byte{0x01, 0x02}, nil)

	_ = storage.Close()

	// Reopen - should handle short sequence bytes gracefully
	storage2, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage2.Close() }()

	// Should still be able to use the namespace
	id, err := storage2.NextID("test_ns")
	require.NoError(t, err)
	assert.Greater(t, id, uint64(0))
}

// TestUpsertVector_NilVector tests upserting nil vector
func TestUpsertVector_NilVector(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	err := storage.UpsertVector(ctx, ns, nil)
	assert.Equal(t, ErrInvalidVector, err)
}

// TestGetVector_InvalidID tests getting vector with invalid ID
func TestGetVector_InvalidID(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	_, err := storage.GetVector(ctx, ns, 0)
	assert.Equal(t, ErrInvalidID, err)
}

// TestDeleteVector_InvalidID tests deleting vector with invalid ID
func TestDeleteVector_InvalidID(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	err := storage.DeleteVector(ctx, ns, 0)
	assert.Equal(t, ErrInvalidID, err)
}

// TestBatchUpsertVectors_EmptyBatch tests batch upsert with empty slice
func TestBatchUpsertVectors_EmptyBatch(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	err := storage.BatchUpsertVectors(ctx, ns, []*VectorValue{})
	assert.Equal(t, ErrEmptyBatch, err)
}

// TestBatchDeleteVectors_EmptyBatch tests batch delete with empty slice
func TestBatchDeleteVectors_EmptyBatch(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	err := storage.BatchDeleteVectors(ctx, ns, []uint64{})
	assert.Equal(t, ErrEmptyBatch, err)
}

// TestCreateNamespace_WithSyncWrites tests namespace creation with sync writes enabled
func TestCreateNamespace_WithSyncWrites(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("vector_test_%d", time.Now().UnixNano()))
	require.NoError(t, os.MkdirAll(tmpDir, 0755))
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = true
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()
	nsInfo := &NamespaceInfo{
		Name:      "sync_test",
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}

	err = storage.CreateNamespace(ctx, nsInfo)
	assert.NoError(t, err)

	// Verify namespace was created
	info, err := storage.GetNamespaceInfo(ctx, "sync_test")
	require.NoError(t, err)
	assert.Equal(t, "sync_test", info.Name)
}

// TestUpsertVector_WithSyncWrites tests upsert with sync writes enabled
func TestUpsertVector_WithSyncWrites(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("vector_test_%d", time.Now().UnixNano()))
	require.NoError(t, os.MkdirAll(tmpDir, 0755))
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = true
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	vec := &VectorValue{
		ID:        1,
		Embedding: make([]float32, 128),
		Version:   1,
	}

	err = storage.UpsertVector(ctx, ns, vec)
	assert.NoError(t, err)

	// Verify vector was stored
	retrieved, err := storage.GetVector(ctx, ns, 1)
	require.NoError(t, err)
	assert.Equal(t, vec.ID, retrieved.ID)
}

// TestDeleteVector_NonexistentVector tests deleting a vector that doesn't exist
func TestDeleteVector_NonexistentVector(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	err := storage.DeleteVector(ctx, ns, 999)
	assert.Equal(t, ErrVectorNotFound, err)
}

// TestDeleteNamespace_WithSyncWrites tests namespace deletion with sync writes enabled
func TestDeleteNamespace_WithSyncWrites(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("vector_test_%d", time.Now().UnixNano()))
	require.NoError(t, os.MkdirAll(tmpDir, 0755))
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = true
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Insert some vectors
	for i := uint64(1); i <= 3; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: make([]float32, 128),
			Version:   1,
		}
		err := storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)
	}

	// Delete namespace
	err = storage.DeleteNamespace(ctx, ns)
	assert.NoError(t, err)
}

// TestDeleteNamespace_NonexistentNamespace tests deleting non-existent namespace
func TestDeleteNamespace_NonexistentNamespace(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	err := storage.DeleteNamespace(ctx, "nonexistent")
	assert.Equal(t, ErrNamespaceNotFound, err)
}

// TestNextID_NonexistentNamespace tests ID allocation for non-existent namespace
func TestNextID_NonexistentNamespace(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	_, err := storage.NextID("nonexistent")
	assert.Equal(t, ErrNamespaceNotFound, err)
}

// TestBatchAllocateIDs_InvalidCount tests batch ID allocation with invalid count
func TestBatchAllocateIDs_InvalidCount(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Test with zero count
	_, err := storage.BatchAllocateIDs(ns, 0)
	assert.Equal(t, ErrInvalidID, err)

	// Test with negative count
	_, err = storage.BatchAllocateIDs(ns, -1)
	assert.Equal(t, ErrInvalidID, err)
}

// TestBatchAllocateIDs_NonexistentNamespace tests batch ID allocation for non-existent namespace
func TestBatchAllocateIDs_NonexistentNamespace(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	_, err := storage.BatchAllocateIDs("nonexistent", 5)
	assert.Equal(t, ErrNamespaceNotFound, err)
}

// TestCleanupTombstones_WithShortKey tests tombstone cleanup with short keys
func TestCleanupTombstones_WithShortKey(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Insert and delete a vector to create a tombstone
	vec := &VectorValue{
		ID:        1,
		Embedding: make([]float32, 128),
		Version:   1,
	}
	err := storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	err = storage.DeleteVector(ctx, ns, 1)
	require.NoError(t, err)

	// Clean up tombstones
	err = storage.CleanupTombstones(ctx, 0)
	assert.NoError(t, err)
}

// TestCleanupTombstones_WithSyncWrites tests tombstone cleanup with sync writes
func TestCleanupTombstones_WithSyncWrites(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("vector_test_%d", time.Now().UnixNano()))
	require.NoError(t, os.MkdirAll(tmpDir, 0755))
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = true
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Insert and delete vectors
	for i := uint64(1); i <= 3; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: make([]float32, 128),
			Version:   1,
		}
		err := storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)

		err = storage.DeleteVector(ctx, ns, i)
		require.NoError(t, err)
	}

	// Clean up tombstones with sync writes
	err = storage.CleanupTombstones(ctx, 0)
	assert.NoError(t, err)
}

// TestScanByMetadata_NoResults tests scanning with no matching results
func TestScanByMetadata_NoResults(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Scan for non-existent metadata
	results, err := storage.ScanByMetadata(ctx, ns, "nonexistent", "value", nil)
	require.NoError(t, err)
	assert.Empty(t, results)
}

// TestScanByMultipleFilters_MetadataNotFound tests filtering with non-matching metadata
func TestScanByMultipleFilters_MetadataNotFound(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "vectors"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	// Insert vectors with metadata
	for i := uint64(1); i <= 5; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: make([]float32, 128),
			Metadata: map[string]string{
				"category": "test",
			},
			Version: 1,
		}
		err := storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)
	}

	// Scan with filters that require a missing metadata field
	filters := []MetadataFilter{
		{Field: "category", Value: "test", Operator: FilterEqual},
		{Field: "missing_field", Value: "value", Operator: FilterEqual},
	}
	results, err := storage.ScanByMultipleFilters(ctx, ns, filters, nil)
	require.NoError(t, err)
	assert.Empty(t, results) // Should be empty because missing_field doesn't exist
}

// TestGetStats_EmptyNamespace tests getting stats for empty namespace
func TestGetStats_EmptyNamespace(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	ns := "empty"
	nsInfo := &NamespaceInfo{
		Name:      ns,
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, storage.CreateNamespace(ctx, nsInfo))

	stats, err := storage.GetStats(ctx, ns)
	require.NoError(t, err)
	assert.NotNil(t, stats)
	assert.Equal(t, ns, stats.Namespace)
	assert.Equal(t, uint64(0), stats.Count)
	assert.Equal(t, uint64(0), stats.SizeBytes)
}
