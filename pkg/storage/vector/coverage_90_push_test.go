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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUpsertVector_NilVectorCoverage tests UpsertVector with nil vector value.
func TestUpsertVector_NilVectorCoverage(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	err := storage.UpsertVector(context.Background(), "ns", nil)
	assert.ErrorIs(t, err, ErrInvalidVector)
}

// TestBatchDeleteVectors_EmptyBatchCoverage tests BatchDeleteVectors with empty IDs.
func TestBatchDeleteVectors_EmptyBatchCoverage(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	err := storage.BatchDeleteVectors(context.Background(), "ns", []uint64{})
	assert.ErrorIs(t, err, ErrEmptyBatch)
}

// TestBatchDeleteVectors_InvalidID tests BatchDeleteVectors with zero ID.
func TestBatchDeleteVectors_InvalidID(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	err := storage.BatchDeleteVectors(context.Background(), "ns", []uint64{0})
	assert.Error(t, err)
}

// TestCreateNamespace_NilInfo tests CreateNamespace with nil NamespaceInfo.
func TestCreateNamespace_NilInfo(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	err := storage.CreateNamespace(context.Background(), nil)
	assert.ErrorIs(t, err, ErrInvalidNamespace)
}

// TestCreateNamespace_EmptyName tests CreateNamespace with empty name.
func TestCreateNamespace_EmptyName(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	err := storage.CreateNamespace(context.Background(), &NamespaceInfo{Name: ""})
	assert.ErrorIs(t, err, ErrInvalidNamespace)
}

// TestBatchUpsert_WithMetadata tests batch upsert with metadata fields.
func TestBatchUpsert_WithMetadata(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: "meta-ns", Dimension: 3})
	require.NoError(t, err)

	vectors := []*VectorValue{
		{
			ID:        1,
			Embedding: []float32{1.0, 2.0, 3.0},
			Version:   1,
			Metadata:  map[string]string{"category": "a", "type": "test"},
		},
		{
			ID:        2,
			Embedding: []float32{4.0, 5.0, 6.0},
			Version:   1,
			Metadata:  map[string]string{"category": "b", "type": "test"},
		},
	}

	err = storage.BatchUpsertVectors(ctx, "meta-ns", vectors)
	require.NoError(t, err)

	// Scan by metadata to verify indexes
	results, err := storage.ScanByMetadata(ctx, "meta-ns", "type", "test", nil)
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

// TestBatchDelete_WithMetadataCleanup tests batch delete cleans up metadata.
func TestBatchDelete_WithMetadataCleanup(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: "del-ns", Dimension: 2})
	require.NoError(t, err)

	// Insert vectors
	for i := uint64(1); i <= 3; i++ {
		err = storage.UpsertVector(ctx, "del-ns", &VectorValue{
			ID:        i,
			Embedding: []float32{float32(i), float32(i + 1)},
			Version:   1,
			Metadata:  map[string]string{"tag": "delete-me"},
		})
		require.NoError(t, err)
	}

	// Batch delete
	err = storage.BatchDeleteVectors(ctx, "del-ns", []uint64{1, 2})
	require.NoError(t, err)

	// Verify only vector 3 remains
	_, err = storage.GetVector(ctx, "del-ns", 1)
	assert.Error(t, err)

	result, err := storage.GetVector(ctx, "del-ns", 3)
	require.NoError(t, err)
	assert.Equal(t, uint64(3), result.ID)
}

// TestCleanupTombstones_WithDataCoverage tests tombstone cleanup with actual data.
func TestCleanupTombstones_WithDataCoverage(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: "tomb-ns", Dimension: 2})
	require.NoError(t, err)

	// Insert and delete vectors to create tombstones
	for i := uint64(1); i <= 5; i++ {
		err = storage.UpsertVector(ctx, "tomb-ns", &VectorValue{
			ID:        i,
			Embedding: []float32{float32(i), float32(i)},
			Version:   1,
		})
		require.NoError(t, err)
	}

	for i := uint64(1); i <= 3; i++ {
		err = storage.DeleteVector(ctx, "tomb-ns", i)
		require.NoError(t, err)
	}

	// Cleanup with a very large duration (should not remove recent tombstones)
	err = storage.CleanupTombstones(ctx, 24*time.Hour)
	require.NoError(t, err)

	// Verify remaining vectors still exist
	result, err := storage.GetVector(ctx, "tomb-ns", 4)
	require.NoError(t, err)
	assert.Equal(t, uint64(4), result.ID)
}

// TestCompact_WithDataCoverage tests compaction with actual data.
func TestCompact_WithDataCoverage(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: "compact-ns", Dimension: 2})
	require.NoError(t, err)

	// Insert some data
	for i := uint64(1); i <= 10; i++ {
		err = storage.UpsertVector(ctx, "compact-ns", &VectorValue{
			ID:        i,
			Embedding: []float32{float32(i), float32(i)},
			Version:   1,
		})
		require.NoError(t, err)
	}

	// Compact
	err = storage.Compact(ctx, "compact-ns")
	require.NoError(t, err)

	// Verify data is still accessible
	result, err := storage.GetVector(ctx, "compact-ns", 5)
	require.NoError(t, err)
	assert.Equal(t, uint64(5), result.ID)
}

// TestUpsertVector_UpdateVersionCoverage tests updating a vector with version increment.
func TestUpsertVector_UpdateVersionCoverage(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: "ver-ns", Dimension: 2})
	require.NoError(t, err)

	// Insert version 1
	err = storage.UpsertVector(ctx, "ver-ns", &VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0},
		Version:   1,
		Metadata:  map[string]string{"tag": "v1"},
	})
	require.NoError(t, err)

	// Update to version 2 with different metadata
	err = storage.UpsertVector(ctx, "ver-ns", &VectorValue{
		ID:        1,
		Embedding: []float32{3.0, 4.0},
		Version:   2,
		Metadata:  map[string]string{"tag": "v2"},
	})
	require.NoError(t, err)

	// Verify latest version
	result, err := storage.GetVector(ctx, "ver-ns", 1)
	require.NoError(t, err)
	assert.Equal(t, []float32{3.0, 4.0}, result.Embedding)
}

// TestDeleteVector_ZeroID tests DeleteVector with ID 0.
func TestDeleteVector_ZeroID(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	err := storage.DeleteVector(context.Background(), "ns", 0)
	assert.ErrorIs(t, err, ErrInvalidID)
}

// TestGetVector_NonExistentCoverage tests GetVector for non-existent vector.
func TestGetVector_NonExistentCoverage(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: "get-ns", Dimension: 2})
	require.NoError(t, err)

	_, err = storage.GetVector(ctx, "get-ns", 999)
	assert.Error(t, err)
}
