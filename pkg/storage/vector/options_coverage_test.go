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

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
)

// TestWithEncryptor_NilBarrier verifies that WithEncryptor with a no-op encryptor
// (nil barrier) produces a valid StorageOption and that NewPebbleVectorStorage
// accepts and exercises that option without error.
func TestWithEncryptor_NilBarrier(t *testing.T) {
	tmpDir := t.TempDir()

	enc := atrest.New(nil)
	opt := WithEncryptor(enc)
	require.NotNil(t, opt)

	config := DefaultConfig(tmpDir)
	storage, err := NewPebbleVectorStorage(config, &testLogger{}, opt)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()
	require.NoError(t, storage.CreateNamespace(ctx, &NamespaceInfo{
		Name:      "enc-ns",
		Dimension: 3,
		CreatedAt: time.Now().UnixNano(),
	}))

	id, err := storage.NextID("enc-ns")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), id)
}

// TestWithEncryptor_NilBarrierUpsert verifies that WithEncryptor with a
// nil-barrier encryptor allows plaintext operation through a full
// create-namespace/upsert/get round trip.
func TestWithEncryptor_NilBarrierUpsert(t *testing.T) {
	tmpDir := t.TempDir()

	opt := WithEncryptor(atrest.New(nil))
	require.NotNil(t, opt)

	config := DefaultConfig(tmpDir)
	storage, err := NewPebbleVectorStorage(config, &testLogger{}, opt)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()
	require.NoError(t, storage.CreateNamespace(ctx, &NamespaceInfo{
		Name:      "barrier-ns",
		Dimension: 4,
		CreatedAt: time.Now().UnixNano(),
	}))

	require.NoError(t, storage.UpsertVector(ctx, "barrier-ns", &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3, 0.4},
		Version:   1,
		CreatedAt: time.Now().UnixNano(),
	}))

	got, err := storage.GetVector(ctx, "barrier-ns", 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), got.ID)
}

// TestWithEncryptor_MultipleOptions verifies that multiple StorageOptions are all
// applied when passed to NewPebbleVectorStorage (exercises the opts loop).
func TestWithEncryptor_MultipleOptions(t *testing.T) {
	tmpDir := t.TempDir()

	enc1 := atrest.New(nil)
	enc2 := atrest.New(nil)
	opt1 := WithEncryptor(enc1)
	opt2 := WithEncryptor(enc2)

	config := DefaultConfig(tmpDir)
	storage, err := NewPebbleVectorStorage(config, &testLogger{}, opt1, opt2)
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()
	require.NoError(t, storage.CreateNamespace(ctx, &NamespaceInfo{
		Name:      "multi-opt-ns",
		Dimension: 2,
		CreatedAt: time.Now().UnixNano(),
	}))

	ids, err := storage.BatchAllocateIDs("multi-opt-ns", 3)
	require.NoError(t, err)
	assert.Len(t, ids, 3)
	assert.Equal(t, []uint64{1, 2, 3}, ids)
}
