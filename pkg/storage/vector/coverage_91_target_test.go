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

//go:build !integration

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

// TestUpsertVector_WriteSemaphoreTimeout verifies that UpsertVector returns
// ErrContextCanceled when the write semaphore is full and the context expires.
func TestUpsertVector_WriteSemaphoreTimeout(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "vector_test_"+t.Name())
	_ = os.RemoveAll(tmpDir)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	config.MaxConcurrentWrites = 1
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()
	require.NoError(t, storage.CreateNamespace(ctx, &NamespaceInfo{
		Name: "ns-sem", Dimension: 3, CreatedAt: time.Now().UnixNano(),
	}))

	// Fill the write semaphore (capacity 1)
	storage.writeSem <- struct{}{}

	// Attempt upsert with a deadline that expires while waiting for semaphore
	deadlineCtx, cancel := context.WithTimeout(ctx, 5*time.Millisecond)
	defer cancel()

	err = storage.UpsertVector(deadlineCtx, "ns-sem", &VectorValue{
		ID: 1, Embedding: []float32{1.0, 2.0, 3.0}, Version: 1,
		CreatedAt: time.Now().UnixNano(),
	})
	assert.ErrorIs(t, err, ErrContextCanceled)

	// Drain semaphore
	<-storage.writeSem
}

// TestGetVector_ReadSemaphoreTimeout verifies that GetVector returns
// ErrContextCanceled when the read semaphore is full and the context expires.
func TestGetVector_ReadSemaphoreTimeout(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "vector_test_"+t.Name())
	_ = os.RemoveAll(tmpDir)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	config.MaxConcurrentReads = 1
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	// Fill the read semaphore (capacity 1)
	storage.readSem <- struct{}{}

	deadlineCtx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	_, err = storage.GetVector(deadlineCtx, "ns", 1)
	assert.ErrorIs(t, err, ErrContextCanceled)

	<-storage.readSem
}

// TestDeleteVector_WriteSemaphoreTimeout verifies that DeleteVector returns
// ErrContextCanceled when the write semaphore is full and the context expires.
func TestDeleteVector_WriteSemaphoreTimeout(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "vector_test_"+t.Name())
	_ = os.RemoveAll(tmpDir)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	config.MaxConcurrentWrites = 1
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	storage.writeSem <- struct{}{}

	deadlineCtx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	err = storage.DeleteVector(deadlineCtx, "ns", 1)
	assert.ErrorIs(t, err, ErrContextCanceled)

	<-storage.writeSem
}

// TestBatchUpsertVectors_WriteSemaphoreTimeout verifies that BatchUpsertVectors
// returns ErrContextCanceled when the write semaphore is full and the context expires.
func TestBatchUpsertVectors_WriteSemaphoreTimeout(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "vector_test_"+t.Name())
	_ = os.RemoveAll(tmpDir)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	config.MaxConcurrentWrites = 1
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	storage.writeSem <- struct{}{}

	deadlineCtx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	err = storage.BatchUpsertVectors(deadlineCtx, "ns", []*VectorValue{
		{ID: 1, Embedding: []float32{1.0}, Version: 1},
	})
	assert.ErrorIs(t, err, ErrContextCanceled)

	<-storage.writeSem
}

// TestBatchDeleteVectors_WriteSemaphoreTimeout verifies that BatchDeleteVectors
// returns ErrContextCanceled when the write semaphore is full and the context expires.
func TestBatchDeleteVectors_WriteSemaphoreTimeout(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "vector_test_"+t.Name())
	_ = os.RemoveAll(tmpDir)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	config := DefaultConfig(tmpDir)
	config.MaxConcurrentWrites = 1
	storage, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	storage.writeSem <- struct{}{}

	deadlineCtx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	err = storage.BatchDeleteVectors(deadlineCtx, "ns", []uint64{1})
	assert.ErrorIs(t, err, ErrContextCanceled)

	<-storage.writeSem
}

// TestLoadNamespaces_CorruptNamespaceData verifies that loadNamespaces
// gracefully skips corrupted namespace entries during startup.
func TestLoadNamespaces_CorruptNamespaceData(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "vector_test_"+t.Name())
	_ = os.RemoveAll(tmpDir)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Phase 1: Create a valid storage with a namespace, then close it.
	config := DefaultConfig(tmpDir)
	storage1, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, storage1.CreateNamespace(ctx, &NamespaceInfo{
		Name: "valid-ns", Dimension: 128, CreatedAt: time.Now().UnixNano(),
	}))
	require.NoError(t, storage1.Close())

	// Phase 2: Open the raw PebbleDB and write corrupt data as a namespace key.
	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)

	corruptKey := schema.EncodeNamespaceMetaKey("corrupt-ns")
	require.NoError(t, db.Set(corruptKey, []byte("not-valid-msgpack"), pebble.NoSync))
	require.NoError(t, db.Close())

	// Phase 3: Reopen storage. loadNamespaces should skip the corrupt entry
	// and still load the valid namespace.
	storage2, err := NewPebbleVectorStorage(config, &testLogger{})
	require.NoError(t, err)
	defer func() { _ = storage2.Close() }()

	namespaces, err := storage2.ListNamespaces(ctx)
	require.NoError(t, err)

	// The valid namespace should be loaded; the corrupt one skipped.
	assert.Len(t, namespaces, 1)
	assert.Equal(t, "valid-ns", namespaces[0].Name)
}
