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
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector/schema"
)

// TestBatch_UpsertErrors tests batch upsert error paths
func TestBatch_UpsertErrors(t *testing.T) {
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

	// Test batch upsert with invalid vector
	batch := newVectorBatch(storage.db, storage.metrics)
	defer func() { _ = batch.Close() }()

	err := batch.UpsertVector(ns, nil)
	assert.Equal(t, ErrInvalidVector, err)
}

// TestBatch_DeleteErrors tests batch delete error paths
func TestBatch_DeleteErrors(t *testing.T) {
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

	// Test batch delete with invalid ID
	batch := newVectorBatch(storage.db, storage.metrics)
	defer func() { _ = batch.Close() }()

	err := batch.DeleteVector(ns, 0)
	assert.Equal(t, ErrInvalidID, err)
}

// TestBatch_CommitEmpty tests committing empty batch
func TestBatch_CommitEmpty(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	batch := newVectorBatch(storage.db, storage.metrics)
	defer func() { _ = batch.Close() }()

	err := batch.Commit()
	assert.Equal(t, ErrEmptyBatch, err)
}

// TestCleanupTombstones_NoTombstones tests cleanup when no tombstones exist
func TestCleanupTombstones_NoTombstones(t *testing.T) {
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

	// Insert vectors but don't delete them
	for i := uint64(1); i <= 3; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: make([]float32, 128),
			Version:   1,
		}
		err := storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)
	}

	// Clean up tombstones (should not find any)
	err := storage.CleanupTombstones(ctx, 1*time.Hour)
	assert.NoError(t, err)
}

// TestUpsertVector_UpdateExistingVersions tests update with old vector decode failure
func TestUpsertVector_UpdateExistingVersions(t *testing.T) {
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

	// Insert vector v1
	vec := &VectorValue{
		ID:        1,
		Embedding: make([]float32, 128),
		Metadata: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
		Version: 1,
	}
	err := storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	// Update to v2 with different metadata
	vec.Version = 2
	vec.Metadata = map[string]string{
		"key1": "new_value1",
		"key3": "value3",
	}
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	// Verify v2 is retrieved
	retrieved, err := storage.GetVector(ctx, ns, 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), retrieved.Version)
	assert.Equal(t, "new_value1", retrieved.Metadata["key1"])
	assert.Equal(t, "value3", retrieved.Metadata["key3"])
	assert.NotContains(t, retrieved.Metadata, "key2")
}

// TestDeleteVector_WithMultipleVersionsAndMetadata tests deletion with multiple versions
func TestDeleteVector_WithMultipleVersionsAndMetadata(t *testing.T) {
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

	// Insert vector with v1
	vec := &VectorValue{
		ID:        1,
		Embedding: make([]float32, 128),
		Metadata: map[string]string{
			"category": "v1",
			"type":     "test",
		},
		Version: 1,
	}
	err := storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	// Update to v2
	vec.Version = 2
	vec.Metadata["category"] = "v2"
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	// Update to v3
	vec.Version = 3
	vec.Metadata["category"] = "v3"
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	// Delete all versions
	err = storage.DeleteVector(ctx, ns, 1)
	require.NoError(t, err)

	// Verify vector is gone
	_, err = storage.GetVector(ctx, ns, 1)
	assert.Equal(t, ErrVectorNotFound, err)
}

// TestBatchDelete_WithMetadata tests batch delete with metadata cleanup
func TestBatchDelete_WithMetadata(t *testing.T) {
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
				"id":       fmt.Sprintf("%d", i),
			},
			Version: 1,
		}
		err := storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)
	}

	// Batch delete
	batch := newVectorBatch(storage.db, storage.metrics)
	defer func() { _ = batch.Close() }()

	for i := uint64(1); i <= 5; i++ {
		err := batch.DeleteVector(ns, i)
		require.NoError(t, err)
	}

	err := batch.Commit()
	require.NoError(t, err)

	// Verify vectors are gone
	for i := uint64(1); i <= 5; i++ {
		_, err := storage.GetVector(ctx, ns, i)
		assert.Equal(t, ErrVectorNotFound, err)
	}
}

// TestScanByMetadata_OffsetLimitCombinations tests scan with various offset/limit combinations
func TestScanByMetadata_OffsetLimitCombinations(t *testing.T) {
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

	// Insert 20 vectors
	for i := uint64(1); i <= 20; i++ {
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

	// Test offset without limit
	results, err := storage.ScanByMetadata(ctx, ns, "category", "test", &ScanOptions{Offset: 10})
	require.NoError(t, err)
	assert.Len(t, results, 10)

	// Test limit without offset
	results, err = storage.ScanByMetadata(ctx, ns, "category", "test", &ScanOptions{Limit: 5})
	require.NoError(t, err)
	assert.Len(t, results, 5)

	// Test offset + limit
	results, err = storage.ScanByMetadata(ctx, ns, "category", "test", &ScanOptions{Offset: 15, Limit: 10})
	require.NoError(t, err)
	assert.Len(t, results, 5) // Only 5 left after offset 15

	// Test no options
	results, err = storage.ScanByMetadata(ctx, ns, "category", "test", nil)
	require.NoError(t, err)
	assert.Len(t, results, 20)
}

// TestGetStats_WithData tests getting stats with actual data
func TestGetStats_WithData(t *testing.T) {
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

	// Insert vectors
	for i := uint64(1); i <= 10; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: make([]float32, 128),
			Metadata: map[string]string{
				"id": fmt.Sprintf("%d", i),
			},
			Version: 1,
		}
		err := storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)
	}

	// Get stats
	stats, err := storage.GetStats(ctx, ns)
	require.NoError(t, err)
	assert.NotNil(t, stats)
	assert.Equal(t, ns, stats.Namespace)
	assert.Greater(t, stats.Count, uint64(0))
	assert.Greater(t, stats.SizeBytes, uint64(0))
}

// TestCleanupTombstones_SyncWritePath tests cleanup with sync writes
func TestCleanupTombstones_SyncWritePath(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = true
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	// Create namespace
	info := &NamespaceInfo{
		Name:      "sync_cleanup",
		Dimension: 3,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Add old tombstones
	oldTime := uint64(time.Now().Add(-2 * time.Hour).Unix())
	for i := 1; i <= 10; i++ {
		key := schema.EncodeTombstoneKey("sync_cleanup", uint64(i), oldTime)
		err = storage.db.Set(key, []byte{}, pebble.NoSync)
		require.NoError(t, err)
	}

	// Run cleanup with sync writes
	err = storage.CleanupTombstones(ctx, time.Hour)
	assert.NoError(t, err)
}

// TestBatch_UpsertVector_EmptyNamespace tests upsert with empty namespace
func TestBatch_UpsertVector_EmptyNamespace(t *testing.T) {
	tmpDir := t.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()
	batch := newVectorBatch(db, metrics)
	defer func() { _ = batch.Close() }()

	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata:  map[string]string{"k": "v"},
		Version:   1,
	}

	err = batch.UpsertVector("", vec)
	assert.NoError(t, err)
}
