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

// Test90Plus_CleanupTombstones tests specific uncovered lines
func Test90Plus_CleanupTombstones(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace
	nsInfo := &NamespaceInfo{
		Name:      "tomb90_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err := storage.CreateNamespace(ctx, nsInfo)
	require.NoError(t, err)

	t.Run("WithOldTombstones", func(t *testing.T) {
		// Create old tombstone with proper 8-byte timestamp
		batch := storage.db.NewBatch()
		oldTime := uint64(time.Now().Add(-30 * time.Minute).Unix())
		tombstone := schema.EncodeTombstoneKey("tomb90_ns", 42, oldTime)

		err := batch.Set(tombstone, []byte{}, pebble.NoSync)
		require.NoError(t, err)
		err = batch.Commit(pebble.NoSync)
		require.NoError(t, err)
		_ = batch.Close()

		// Cleanup - this should find and delete the old tombstone
		err = storage.CleanupTombstones(ctx, 10*time.Minute)
		assert.NoError(t, err)
	})

	t.Run("WithSyncWrites", func(t *testing.T) {
		// Save old config
		oldSync := storage.config.SyncWrites
		storage.config.SyncWrites = true
		defer func() { storage.config.SyncWrites = oldSync }()

		// Create another old tombstone
		batch := storage.db.NewBatch()
		oldTime := uint64(time.Now().Add(-25 * time.Minute).Unix())
		tombstone := schema.EncodeTombstoneKey("tomb90_ns", 43, oldTime)

		err := batch.Set(tombstone, []byte{}, pebble.NoSync)
		require.NoError(t, err)
		err = batch.Commit(pebble.NoSync)
		require.NoError(t, err)
		_ = batch.Close()

		// Cleanup with sync writes enabled
		err = storage.CleanupTombstones(ctx, 10*time.Minute)
		assert.NoError(t, err)
	})

	t.Run("WithShortKeySkip", func(t *testing.T) {
		// Insert a key with less than 8 bytes after the prefix
		batch := storage.db.NewBatch()
		prefix := schema.EncodeTombstonePrefix("tomb90_ns")
		shortKey := append(prefix, []byte{1, 2, 3}...) // Only 3 bytes, needs 8

		err := batch.Set(shortKey, []byte{}, pebble.NoSync)
		require.NoError(t, err)
		err = batch.Commit(pebble.NoSync)
		require.NoError(t, err)
		_ = batch.Close()

		// Should skip short keys without error
		err = storage.CleanupTombstones(ctx, 1*time.Hour)
		assert.NoError(t, err)
	})
}

// Test90Plus_BatchUpsertMetadata tests batch operations with metadata
func Test90Plus_BatchUpsertMetadata(t *testing.T) {
	tmpDir := t.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	t.Run("UpsertWithReverseIndexError", func(t *testing.T) {
		// Create batch and insert vector with metadata
		batch := newVectorBatch(db, metrics)
		defer func() { _ = batch.Close() }()

		vec := &VectorValue{
			ID:        100,
			Embedding: []float32{0.1, 0.2, 0.3},
			Metadata: map[string]string{
				"field1": "value1",
				"field2": "value2",
			},
			Version: 1,
		}

		err := batch.UpsertVector("meta_ns", vec)
		assert.NoError(t, err)
	})
}

// Test90Plus_DeleteVectorPaths tests delete vector code paths
func Test90Plus_DeleteVectorPaths(t *testing.T) {
	tmpDir := t.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	// First insert a vector with metadata
	insertBatch := newVectorBatch(db, metrics)
	vec := &VectorValue{
		ID:        200,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata: map[string]string{
			"author":   "alice",
			"category": "test",
		},
		Version: 1,
	}
	err = insertBatch.UpsertVector("del_ns", vec)
	require.NoError(t, err)
	err = insertBatch.Commit()
	require.NoError(t, err)
	_ = insertBatch.Close()

	t.Run("DeleteWithReverseIndexCleanup", func(t *testing.T) {
		deleteBatch := newVectorBatch(db, metrics)
		defer func() { _ = deleteBatch.Close() }()

		// Delete should clean up reverse indexes
		err := deleteBatch.DeleteVector("del_ns", 200)
		assert.NoError(t, err)

		err = deleteBatch.Commit()
		assert.NoError(t, err)
	})
}

// Test90Plus_CompactPath tests Compact function
func Test90Plus_CompactPath(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create namespace
	nsInfo := &NamespaceInfo{
		Name:      "compact90_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err := storage.CreateNamespace(ctx, nsInfo)
	require.NoError(t, err)

	// Insert some data to compact
	for i := 0; i < 5; i++ {
		vec := &VectorValue{
			ID:        uint64(i + 1),
			Embedding: []float32{float32(i), float32(i + 1), float32(i + 2)},
			Metadata:  map[string]string{"idx": string(rune(i + '0'))},
			Version:   1,
		}
		err = storage.UpsertVector(ctx, "compact90_ns", vec)
		require.NoError(t, err)
	}

	t.Run("CompactSuccess", func(t *testing.T) {
		err := storage.Compact(ctx, "compact90_ns")
		assert.NoError(t, err)
	})
}

// Test90Plus_ClosePath tests Close function edge cases
func Test90Plus_ClosePath(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = false
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)

	// Create some data
	ctx := context.Background()
	nsInfo := &NamespaceInfo{
		Name:      "close_ns",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, nsInfo)
	require.NoError(t, err)

	t.Run("CloseNormally", func(t *testing.T) {
		err := storage.Close()
		assert.NoError(t, err)
	})
}
