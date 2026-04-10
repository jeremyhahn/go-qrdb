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
)

// TestCleanupTombstones_SyncWrites tests cleanup with sync writes enabled
func TestCleanupTombstones_SyncWrites(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "cleanup_sync_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = true // Enable sync writes to test that path
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	// Create namespace
	nsInfo := &NamespaceInfo{
		Name:      "sync_test",
		CreatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, nsInfo)
	require.NoError(t, err)

	// Insert and delete vectors to create tombstones
	for i := uint64(1); i <= 5; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: []float32{float32(i)},
			Metadata:  map[string]string{"key": "value"},
			Version:   1,
		}

		err = storage.UpsertVector(ctx, "sync_test", vec)
		require.NoError(t, err)

		err = storage.DeleteVector(ctx, "sync_test", i)
		require.NoError(t, err)
	}

	// Wait for tombstones to age
	time.Sleep(10 * time.Millisecond)

	// Cleanup - exercises sync write path
	err = storage.CleanupTombstones(ctx, 1*time.Millisecond)
	assert.NoError(t, err)
}

// TestBatch_UpsertVector_SetErrors tests Set error paths
func TestBatch_UpsertVector_SetErrors(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "batch_set_errors")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	// Create batch and insert vector
	batch := newVectorBatch(db, metrics)
	defer func() { _ = batch.Close() }()

	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata: map[string]string{
			"field1": "value1",
			"field2": "value2",
		},
		Version: 1,
	}

	err = batch.UpsertVector("test", vec)
	assert.NoError(t, err)

	err = batch.Commit()
	assert.NoError(t, err)
}

// TestAdapter_UpsertVector_WithSyncWrites tests upsert with sync writes
func TestAdapter_UpsertVector_WithSyncWrites(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "upsert_sync_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = true
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	nsInfo := &NamespaceInfo{
		Name:      "sync_upsert",
		CreatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, nsInfo)
	require.NoError(t, err)

	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata: map[string]string{
			"field1": "value1",
			"field2": "value2",
		},
		Version: 1,
	}

	err = storage.UpsertVector(ctx, "sync_upsert", vec)
	assert.NoError(t, err)
}

// TestAdapter_DeleteVector_WithSyncWrites tests delete with sync writes
func TestAdapter_DeleteVector_WithSyncWrites(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "delete_sync_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = true
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	nsInfo := &NamespaceInfo{
		Name:      "sync_delete",
		CreatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, nsInfo)
	require.NoError(t, err)

	// Insert vector
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata:  map[string]string{"key": "value"},
		Version:   1,
	}
	err = storage.UpsertVector(ctx, "sync_delete", vec)
	require.NoError(t, err)

	// Delete with sync writes
	err = storage.DeleteVector(ctx, "sync_delete", 1)
	assert.NoError(t, err)
}

// TestAdapter_GetVector_AfterClose tests get after storage is closed
func TestAdapter_GetVector_AfterClose(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "get_after_close")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)

	ctx := context.Background()

	nsInfo := &NamespaceInfo{
		Name:      "close_test",
		CreatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, nsInfo)
	require.NoError(t, err)

	// Close storage
	err = storage.Close()
	require.NoError(t, err)

	// Try to get vector - should return ErrStorageClosed
	_, err = storage.GetVector(ctx, "close_test", 1)
	assert.Equal(t, ErrStorageClosed, err)
}

// TestAdapter_CreateNamespace_AfterClose tests create after storage is closed
func TestAdapter_CreateNamespace_AfterClose(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "create_after_close")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)

	ctx := context.Background()

	// Close storage
	err = storage.Close()
	require.NoError(t, err)

	// Try to create namespace - should return ErrStorageClosed
	nsInfo := &NamespaceInfo{
		Name:      "test",
		CreatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, nsInfo)
	assert.Equal(t, ErrStorageClosed, err)
}

// TestAdapter_GetStats_AfterClose tests get stats after storage is closed
func TestAdapter_GetStats_AfterClose(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "stats_after_close")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)

	// Close storage
	err = storage.Close()
	require.NoError(t, err)

	ctx := context.Background()

	// Try to get stats - should return ErrStorageClosed
	_, err = storage.GetStats(ctx, "test")
	assert.Equal(t, ErrStorageClosed, err)
}

// TestAdapter_NextID_AfterClose tests ID allocation after storage is closed
func TestAdapter_NextID_AfterClose(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "nextid_after_close")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)

	// Close storage
	err = storage.Close()
	require.NoError(t, err)

	// Try to allocate ID - should return ErrStorageClosed
	_, err = storage.NextID("test")
	assert.Equal(t, ErrStorageClosed, err)
}

// TestAdapter_ScanNamespace_NilAndEmptyOptions tests scan with various options
func TestAdapter_ScanNamespace_NilAndEmptyOptions(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "scan_options_test")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()

	nsInfo := &NamespaceInfo{
		Name:      "scan_opts",
		CreatedAt: time.Now().Unix(),
	}
	err = storage.CreateNamespace(ctx, nsInfo)
	require.NoError(t, err)

	// Insert vectors
	for i := uint64(1); i <= 10; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: []float32{float32(i)},
			Metadata:  map[string]string{"key": "value"},
			Version:   1,
		}
		err = storage.UpsertVector(ctx, "scan_opts", vec)
		require.NoError(t, err)
	}

	t.Run("NilOptions", func(t *testing.T) {
		results, err := storage.ScanNamespace(ctx, "scan_opts", nil)
		assert.NoError(t, err)
		assert.NotEmpty(t, results)
	})

	t.Run("WithLimit", func(t *testing.T) {
		results, err := storage.ScanNamespace(ctx, "scan_opts", &ScanOptions{Limit: 5})
		assert.NoError(t, err)
		assert.Len(t, results, 5)
	})

	t.Run("WithOffsetAndLimit", func(t *testing.T) {
		results, err := storage.ScanNamespace(ctx, "scan_opts", &ScanOptions{Limit: 3, Offset: 2})
		assert.NoError(t, err)
		assert.Len(t, results, 3)
	})
}
