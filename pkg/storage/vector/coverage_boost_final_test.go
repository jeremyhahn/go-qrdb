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

// TestUpsertVector_BoostCoverage tests UpsertVector edge cases
func TestUpsertVector_BoostCoverage(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace
	nsInfo := &NamespaceInfo{
		Name:      "boost_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err := storage.CreateNamespace(ctx, nsInfo)
	require.NoError(t, err)

	t.Run("UpsertWithMultipleMetadataFields", func(t *testing.T) {
		vec := &VectorValue{
			ID:        1,
			Embedding: []float32{0.1, 0.2, 0.3},
			Metadata: map[string]string{
				"author":   "alice",
				"category": "test",
				"tags":     "important",
				"status":   "active",
			},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
			Version:   1,
		}

		err := storage.UpsertVector(ctx, "boost_ns", vec)
		assert.NoError(t, err)
	})

	t.Run("UpsertErrorOnEncode", func(t *testing.T) {
		// Close storage to trigger error
		oldClosed := storage.closed.Load()
		storage.closed.Store(true)

		vec := &VectorValue{
			ID:        2,
			Embedding: []float32{0.1},
			Version:   1,
		}

		err := storage.UpsertVector(ctx, "boost_ns", vec)
		assert.Error(t, err)

		storage.closed.Store(oldClosed)
	})
}

// TestBatchUpsertVectors_BoostCoverage tests BatchUpsertVectors
func TestBatchUpsertVectors_BoostCoverage(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace
	nsInfo := &NamespaceInfo{
		Name:      "batch_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err := storage.CreateNamespace(ctx, nsInfo)
	require.NoError(t, err)

	t.Run("BatchWithMixedVectors", func(t *testing.T) {
		vectors := []*VectorValue{
			{
				ID:        10,
				Embedding: []float32{0.1, 0.2, 0.3},
				Metadata: map[string]string{
					"type": "type1",
				},
				Version: 1,
			},
			{
				ID:        11,
				Embedding: []float32{0.4, 0.5, 0.6},
				Metadata: map[string]string{
					"type": "type2",
				},
				Version: 1,
			},
		}

		err := storage.BatchUpsertVectors(ctx, "batch_ns", vectors)
		assert.NoError(t, err)
	})
}

// TestBatchDeleteVectors_BoostCoverage tests BatchDeleteVectors
func TestBatchDeleteVectors_BoostCoverage(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace
	nsInfo := &NamespaceInfo{
		Name:      "delete_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err := storage.CreateNamespace(ctx, nsInfo)
	require.NoError(t, err)

	// Insert vectors first
	vectors := []*VectorValue{
		{
			ID:        20,
			Embedding: []float32{0.1, 0.2, 0.3},
			Metadata:  map[string]string{"key": "val1"},
			Version:   1,
		},
		{
			ID:        21,
			Embedding: []float32{0.4, 0.5, 0.6},
			Metadata:  map[string]string{"key": "val2"},
			Version:   1,
		},
	}

	err = storage.BatchUpsertVectors(ctx, "delete_ns", vectors)
	require.NoError(t, err)

	t.Run("BatchDeleteMultiple", func(t *testing.T) {
		ids := []uint64{20, 21}
		err := storage.BatchDeleteVectors(ctx, "delete_ns", ids)
		assert.NoError(t, err)
	})
}

// TestCleanupTombstones_BoostCoverage tests CleanupTombstones thoroughly
func TestCleanupTombstones_BoostCoverage(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace
	nsInfo := &NamespaceInfo{
		Name:      "tomb_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err := storage.CreateNamespace(ctx, nsInfo)
	require.NoError(t, err)

	t.Run("CleanupWithSyncWrites", func(t *testing.T) {
		// Enable sync writes
		oldSync := storage.config.SyncWrites
		storage.config.SyncWrites = true

		// Create old tombstone
		batch := storage.db.NewBatch()
		oldTime := uint64(time.Now().Add(-30 * time.Minute).Unix())
		tombstone := schema.EncodeTombstoneKey("tomb_ns", 100, oldTime)
		err := batch.Set(tombstone, []byte{}, pebble.NoSync)
		require.NoError(t, err)
		err = batch.Commit(pebble.NoSync)
		require.NoError(t, err)
		_ = batch.Close()

		// Cleanup
		err = storage.CleanupTombstones(ctx, 10*time.Minute)
		assert.NoError(t, err)

		storage.config.SyncWrites = oldSync
	})

	t.Run("CleanupWithShortKeys", func(t *testing.T) {
		// Insert a malformed tombstone with a short key
		batch := storage.db.NewBatch()
		prefix := schema.EncodeTombstonePrefix("tomb_ns")
		shortKey := append(prefix, []byte{1, 2, 3}...) // Too short for timestamp
		err := batch.Set(shortKey, []byte{}, pebble.NoSync)
		require.NoError(t, err)
		err = batch.Commit(pebble.NoSync)
		require.NoError(t, err)
		_ = batch.Close()

		// Should not error even with malformed keys
		err = storage.CleanupTombstones(ctx, 1*time.Hour)
		assert.NoError(t, err)
	})
}

// TestCompact_BoostCoverage tests Compact edge cases
func TestCompact_BoostCoverage(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace
	nsInfo := &NamespaceInfo{
		Name:      "compact_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err := storage.CreateNamespace(ctx, nsInfo)
	require.NoError(t, err)

	// Add some data
	for i := 0; i < 10; i++ {
		vec := &VectorValue{
			ID:        uint64(i + 1),
			Embedding: []float32{float32(i), float32(i + 1), float32(i + 2)},
			Metadata:  map[string]string{"index": string(rune(i + '0'))},
			Version:   1,
		}
		err = storage.UpsertVector(ctx, "compact_ns", vec)
		require.NoError(t, err)
	}

	t.Run("CompactWithData", func(t *testing.T) {
		err := storage.Compact(ctx, "compact_ns")
		assert.NoError(t, err)
	})
}

// TestBatch_UpsertVectorErrorPaths tests batch upsert error paths
func TestBatch_UpsertVectorErrorPaths(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "test_batch_upsert_errors")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()
	batch := newVectorBatch(db, metrics)
	defer func() { _ = batch.Close() }()

	t.Run("UpsertWithEmptyMetadata", func(t *testing.T) {
		vec := &VectorValue{
			ID:        1,
			Embedding: []float32{0.1, 0.2},
			Metadata:  map[string]string{}, // Empty metadata
			Version:   1,
		}

		err := batch.UpsertVector("test_ns", vec)
		assert.NoError(t, err)
	})

	t.Run("UpsertWithNilMetadata", func(t *testing.T) {
		vec := &VectorValue{
			ID:        2,
			Embedding: []float32{0.1, 0.2},
			Metadata:  nil, // Nil metadata
			Version:   1,
		}

		err := batch.UpsertVector("test_ns", vec)
		assert.NoError(t, err)
	})
}

// TestBatch_DeleteVectorErrorPaths tests batch delete error paths
func TestBatch_DeleteVectorErrorPaths(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "test_batch_delete_errors")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	// Insert a vector with multiple metadata fields
	insertBatch := newVectorBatch(db, metrics)
	vec := &VectorValue{
		ID:        100,
		Embedding: []float32{0.1, 0.2},
		Metadata: map[string]string{
			"field1": "value1",
			"field2": "value2",
			"field3": "value3",
		},
		Version: 1,
	}
	err = insertBatch.UpsertVector("test_ns", vec)
	require.NoError(t, err)
	err = insertBatch.Commit()
	require.NoError(t, err)
	_ = insertBatch.Close()

	t.Run("DeleteVectorWithMetadata", func(t *testing.T) {
		deleteBatch := newVectorBatch(db, metrics)
		defer func() { _ = deleteBatch.Close() }()

		err := deleteBatch.DeleteVector("test_ns", 100)
		assert.NoError(t, err)

		err = deleteBatch.Commit()
		assert.NoError(t, err)
	})
}
