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

func TestConfig_Validate_AllNegativeFields(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
	}{
		{"negative MemTableSize", &Config{DataDir: "/tmp/test", MemTableSize: -1}},
		{"negative CacheSize", &Config{DataDir: "/tmp/test", CacheSize: -1}},
		{"negative MaxConcurrentCompactions", &Config{DataDir: "/tmp/test", MaxConcurrentCompactions: -1}},
		{"negative WALBytesPerSync", &Config{DataDir: "/tmp/test", WALBytesPerSync: -1}},
		{"negative TombstoneRetention", &Config{DataDir: "/tmp/test", TombstoneRetention: -1 * time.Second}},
		{"negative BloomFilterBitsPerKey", &Config{DataDir: "/tmp/test", BloomFilterBitsPerKey: -1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.ErrorIs(t, tc.config.Validate(), ErrInvalidConfig)
		})
	}
}

func TestConfig_Validate_DefaultsConcurrencyLimits(t *testing.T) {
	cfg := &Config{DataDir: "/tmp/test", MaxConcurrentReads: 0, MaxConcurrentWrites: 0}
	require.NoError(t, cfg.Validate())
	assert.Equal(t, 1000, cfg.MaxConcurrentReads)
	assert.Equal(t, 100, cfg.MaxConcurrentWrites)
}

func TestConfig_WithDefaults_AllZeroFields(t *testing.T) {
	result := (&Config{DataDir: "/tmp/test"}).WithDefaults()
	assert.Equal(t, int64(64<<20), result.MemTableSize)
	assert.Equal(t, int64(512<<20), result.CacheSize)
	assert.Equal(t, 2, result.MaxConcurrentCompactions)
	assert.Equal(t, int64(1<<20), result.WALBytesPerSync)
	assert.Equal(t, 24*time.Hour, result.TombstoneRetention)
	assert.Equal(t, 10, result.BloomFilterBitsPerKey)
	assert.Equal(t, 1000, result.MaxConcurrentReads)
	assert.Equal(t, 100, result.MaxConcurrentWrites)
}

func TestConfig_WithDefaults_NonZeroFieldsPreserved(t *testing.T) {
	cfg := &Config{
		DataDir: "/tmp/test", MemTableSize: 1, CacheSize: 2, MaxConcurrentCompactions: 3,
		WALBytesPerSync: 4, TombstoneRetention: 5 * time.Second, BloomFilterBitsPerKey: 6,
		MaxConcurrentReads: 7, MaxConcurrentWrites: 8,
	}
	result := cfg.WithDefaults()
	assert.Equal(t, int64(1), result.MemTableSize)
	assert.Equal(t, int64(2), result.CacheSize)
	assert.Equal(t, 3, result.MaxConcurrentCompactions)
	assert.Equal(t, int64(4), result.WALBytesPerSync)
	assert.Equal(t, 5*time.Second, result.TombstoneRetention)
	assert.Equal(t, 6, result.BloomFilterBitsPerKey)
	assert.Equal(t, 7, result.MaxConcurrentReads)
	assert.Equal(t, 8, result.MaxConcurrentWrites)
}

func TestConvert_NilInputs(t *testing.T) {
	assert.Nil(t, toSchemaVectorValue(nil))
	assert.Nil(t, fromSchemaVectorValue(nil))
	assert.Nil(t, toSchemaNamespaceInfo(nil))
	assert.Nil(t, fromSchemaNamespaceInfo(nil))
}

func TestListNamespaces_Success(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, storage.CreateNamespace(ctx, &NamespaceInfo{Name: "ns-list-1", Dimension: 128, CreatedAt: time.Now().UnixNano()}))
	require.NoError(t, storage.CreateNamespace(ctx, &NamespaceInfo{Name: "ns-list-2", Dimension: 256, CreatedAt: time.Now().UnixNano()}))
	require.NoError(t, storage.CreateNamespace(ctx, &NamespaceInfo{Name: "ns-list-3", Dimension: 64, CreatedAt: time.Now().UnixNano()}))

	results, err := storage.ListNamespaces(ctx)
	require.NoError(t, err)
	assert.Len(t, results, 3)
}

func TestListNamespaces_Empty(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	results, err := storage.ListNamespaces(context.Background())
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestListNamespaces_Closed(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	require.NoError(t, storage.Close())
	_, err := storage.ListNamespaces(context.Background())
	assert.ErrorIs(t, err, ErrStorageClosed)
}

func TestUpsertVector_UpdateWithOldMetadata(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, storage.CreateNamespace(ctx, &NamespaceInfo{Name: "ns-update", Dimension: 3, CreatedAt: time.Now().UnixNano()}))

	vec1 := &VectorValue{ID: 1, Embedding: []float32{1.0, 2.0, 3.0}, Metadata: map[string]string{"color": "red", "size": "large"}, Version: 1, CreatedAt: time.Now().UnixNano(), UpdatedAt: time.Now().UnixNano()}
	require.NoError(t, storage.UpsertVector(ctx, "ns-update", vec1))

	vec2 := &VectorValue{ID: 1, Embedding: []float32{4.0, 5.0, 6.0}, Metadata: map[string]string{"color": "blue", "shape": "round"}, Version: 2, CreatedAt: vec1.CreatedAt, UpdatedAt: time.Now().UnixNano()}
	require.NoError(t, storage.UpsertVector(ctx, "ns-update", vec2))

	got, err := storage.GetVector(ctx, "ns-update", 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), got.Version)
	assert.Equal(t, "blue", got.Metadata["color"])
}

func TestUpsertVector_SyncWrites(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = true
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()
	require.NoError(t, storage.CreateNamespace(ctx, &NamespaceInfo{Name: "ns-sync", Dimension: 3, CreatedAt: time.Now().UnixNano()}))
	require.NoError(t, storage.UpsertVector(ctx, "ns-sync", &VectorValue{ID: 1, Embedding: []float32{1.0, 2.0, 3.0}, Version: 1, CreatedAt: time.Now().UnixNano()}))

	got, err := storage.GetVector(ctx, "ns-sync", 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), got.ID)
}

func TestCompact_SuccessPath(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, storage.CreateNamespace(ctx, &NamespaceInfo{Name: "ns-compact", Dimension: 3, CreatedAt: time.Now().UnixNano()}))
	for i := uint64(1); i <= 5; i++ {
		require.NoError(t, storage.UpsertVector(ctx, "ns-compact", &VectorValue{ID: i, Embedding: []float32{float32(i), float32(i + 1), float32(i + 2)}, Version: 1, CreatedAt: time.Now().UnixNano()}))
	}
	require.NoError(t, storage.Compact(ctx, "ns-compact"))
}

func TestUpsertVector_CancelledContext(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Error(t, storage.UpsertVector(ctx, "ns", &VectorValue{ID: 1, Embedding: []float32{1.0, 2.0}, Version: 1}))
}

func TestGetVector_CancelledContext(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := storage.GetVector(ctx, "ns", 1)
	assert.Error(t, err)
}

func TestDeleteVector_WithMetadataIndexes(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, storage.CreateNamespace(ctx, &NamespaceInfo{Name: "ns-del-meta", Dimension: 3, CreatedAt: time.Now().UnixNano()}))
	require.NoError(t, storage.UpsertVector(ctx, "ns-del-meta", &VectorValue{ID: 42, Embedding: []float32{1.0, 2.0, 3.0}, Metadata: map[string]string{"env": "prod", "region": "us-east"}, Version: 1, CreatedAt: time.Now().UnixNano(), UpdatedAt: time.Now().UnixNano()}))
	require.NoError(t, storage.DeleteVector(ctx, "ns-del-meta", 42))
	_, err := storage.GetVector(ctx, "ns-del-meta", 42)
	assert.ErrorIs(t, err, ErrVectorNotFound)
}

func TestDeleteVector_SyncWritesPath(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = true
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()
	require.NoError(t, storage.CreateNamespace(ctx, &NamespaceInfo{Name: "ns-sync-del", Dimension: 3, CreatedAt: time.Now().UnixNano()}))
	require.NoError(t, storage.UpsertVector(ctx, "ns-sync-del", &VectorValue{ID: 1, Embedding: []float32{1.0, 2.0, 3.0}, Metadata: map[string]string{"key": "value"}, Version: 1, CreatedAt: time.Now().UnixNano()}))
	require.NoError(t, storage.DeleteVector(ctx, "ns-sync-del", 1))
	_, err = storage.GetVector(ctx, "ns-sync-del", 1)
	assert.ErrorIs(t, err, ErrVectorNotFound)
}

func TestDeleteVector_CancelledContext(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Error(t, storage.DeleteVector(ctx, "ns", 1))
}

func TestBatchUpsertVectors_CancelledContext(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Error(t, storage.BatchUpsertVectors(ctx, "ns", []*VectorValue{{ID: 1, Embedding: []float32{1.0}, Version: 1}}))
}

func TestBatchDeleteVectors_CancelledContext(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Error(t, storage.BatchDeleteVectors(ctx, "ns", []uint64{1}))
}

func TestNewPebbleVectorStorage_InvalidConfig(t *testing.T) {
	_, err := NewPebbleVectorStorage(&Config{DataDir: ""}, &testLogger{})
	assert.ErrorIs(t, err, ErrInvalidConfig)
}

func TestCreateNamespace_SyncWritesPath(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = true
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()
	require.NoError(t, storage.CreateNamespace(ctx, &NamespaceInfo{Name: "ns-sync-create", Dimension: 128, CreatedAt: time.Now().UnixNano()}))
	info, err := storage.GetNamespaceInfo(ctx, "ns-sync-create")
	require.NoError(t, err)
	assert.Equal(t, "ns-sync-create", info.Name)
}

func TestDeleteNamespace_SyncWritesPath(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = true
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()
	require.NoError(t, storage.CreateNamespace(ctx, &NamespaceInfo{Name: "ns-sync-delns", Dimension: 64, CreatedAt: time.Now().UnixNano()}))
	require.NoError(t, storage.UpsertVector(ctx, "ns-sync-delns", &VectorValue{ID: 1, Embedding: []float32{1.0, 2.0}, Version: 1, CreatedAt: time.Now().UnixNano()}))
	require.NoError(t, storage.DeleteNamespace(ctx, "ns-sync-delns"))
	_, err = storage.GetNamespaceInfo(ctx, "ns-sync-delns")
	assert.ErrorIs(t, err, ErrNamespaceNotFound)
}

func TestBatchUpsertVectors_SyncWritesPath(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = true
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()
	require.NoError(t, storage.CreateNamespace(ctx, &NamespaceInfo{Name: "ns-batch-sync", Dimension: 3, CreatedAt: time.Now().UnixNano()}))
	require.NoError(t, storage.BatchUpsertVectors(ctx, "ns-batch-sync", []*VectorValue{
		{ID: 1, Embedding: []float32{1.0, 2.0, 3.0}, Version: 1, CreatedAt: time.Now().UnixNano()},
		{ID: 2, Embedding: []float32{4.0, 5.0, 6.0}, Version: 1, CreatedAt: time.Now().UnixNano()},
	}))
	got, err := storage.GetVector(ctx, "ns-batch-sync", 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), got.ID)
}

func TestBatchDeleteVectors_SyncWritesPath(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultConfig(tmpDir)
	config.SyncWrites = true
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()
	require.NoError(t, storage.CreateNamespace(ctx, &NamespaceInfo{Name: "ns-batchdel-sync", Dimension: 3, CreatedAt: time.Now().UnixNano()}))
	require.NoError(t, storage.UpsertVector(ctx, "ns-batchdel-sync", &VectorValue{ID: 1, Embedding: []float32{1.0, 2.0, 3.0}, Version: 1, CreatedAt: time.Now().UnixNano()}))
	require.NoError(t, storage.BatchDeleteVectors(ctx, "ns-batchdel-sync", []uint64{1}))
	_, err = storage.GetVector(ctx, "ns-batchdel-sync", 1)
	assert.ErrorIs(t, err, ErrVectorNotFound)
}

func TestUpsertVector_UpdateVersionNoOldData(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, storage.CreateNamespace(ctx, &NamespaceInfo{Name: "ns-upd-noold", Dimension: 3, CreatedAt: time.Now().UnixNano()}))
	require.NoError(t, storage.UpsertVector(ctx, "ns-upd-noold", &VectorValue{ID: 1, Embedding: []float32{1.0, 2.0, 3.0}, Metadata: map[string]string{"key": "val"}, Version: 2, CreatedAt: time.Now().UnixNano(), UpdatedAt: time.Now().UnixNano()}))

	got, err := storage.GetVector(ctx, "ns-upd-noold", 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), got.Version)
}

// TestStorageReopen verifies namespace data persists and loads on reopen.
func TestStorageReopen(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultConfig(tmpDir)

	// First open: create namespace and insert data
	storage1, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, storage1.CreateNamespace(ctx, &NamespaceInfo{Name: "persist-ns", Dimension: 3, CreatedAt: time.Now().UnixNano()}))
	require.NoError(t, storage1.UpsertVector(ctx, "persist-ns", &VectorValue{ID: 1, Embedding: []float32{1.0, 2.0, 3.0}, Version: 1, CreatedAt: time.Now().UnixNano()}))
	require.NoError(t, storage1.Close())

	// Second open: verify namespace loaded from disk
	storage2, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage2.Close() }()

	namespaces, err := storage2.ListNamespaces(ctx)
	require.NoError(t, err)
	assert.Len(t, namespaces, 1)
	assert.Equal(t, "persist-ns", namespaces[0].Name)

	got, err := storage2.GetVector(ctx, "persist-ns", 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), got.ID)
}
