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
	"os"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector/schema"
)

func setupTestDB(t *testing.T) (*pebble.DB, func()) {
	tmpDir := t.TempDir()
	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)

	cleanup := func() {
		_ = db.Close()
		_ = os.RemoveAll(tmpDir)
	}

	return db, cleanup
}

func TestVectorIterator_Success(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Insert test data
	namespace := "test_ns"
	for i := 1; i <= 5; i++ {
		vec := &schema.VectorValue{
			ID:        uint64(i),
			Embedding: []float32{float32(i) * 0.1},
			Metadata: map[string]string{
				"index": string(rune('0' + i)),
			},
			Version: 1,
		}

		key := schema.EncodeVectorKey(namespace, uint64(i), 1)
		value, err := schema.EncodeVectorValue(vec)
		require.NoError(t, err)

		err = db.Set(key, value, pebble.NoSync)
		require.NoError(t, err)
	}

	prefix := schema.EncodeNamespacePrefix(namespace)
	iter := newVectorIterator(db, prefix)
	defer func() { _ = iter.Close() }()

	count := 0
	for iter.Next() {
		count++

		vec, err := iter.Value()
		assert.NoError(t, err)
		assert.NotNil(t, vec)

		ns, id, version := iter.Key()
		assert.Equal(t, namespace, ns)
		assert.Greater(t, id, uint64(0))
		assert.Equal(t, uint64(1), version)
	}

	assert.NoError(t, iter.Err())
	assert.Equal(t, 5, count)
}

func TestVectorIterator_Empty(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	prefix := schema.EncodeNamespacePrefix("empty_ns")
	iter := newVectorIterator(db, prefix)
	defer func() { _ = iter.Close() }()

	count := 0
	for iter.Next() {
		count++
	}

	assert.Equal(t, 0, count)
	assert.NoError(t, iter.Err())
}

func TestVectorIterator_InvalidValue(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	namespace := "test_ns"
	key := schema.EncodeVectorKey(namespace, 1, 1)

	// Set invalid msgpack data
	err := db.Set(key, []byte{0xFF, 0xFF, 0xFF}, pebble.NoSync)
	require.NoError(t, err)

	prefix := schema.EncodeNamespacePrefix(namespace)
	iter := newVectorIterator(db, prefix)
	defer func() { _ = iter.Close() }()

	hasNext := iter.Next()
	assert.False(t, hasNext)
	assert.Error(t, iter.Err())
}

func TestVectorIterator_ValueBeforeNext(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	prefix := schema.EncodeNamespacePrefix("test_ns")
	iter := newVectorIterator(db, prefix)
	defer func() { _ = iter.Close() }()

	// Call Value before Next
	vec, err := iter.Value()
	assert.Error(t, err)
	assert.Nil(t, vec)
	assert.Equal(t, ErrVectorNotFound, err)
}

func TestVectorIterator_MultipleVersions(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	namespace := "test_ns"
	id := uint64(1)

	// Insert multiple versions
	for version := uint64(1); version <= 3; version++ {
		vec := &schema.VectorValue{
			ID:        id,
			Embedding: []float32{float32(version)},
			Metadata: map[string]string{
				"version": string(rune('0' + version)),
			},
			Version: version,
		}

		key := schema.EncodeVectorKey(namespace, id, version)
		value, err := schema.EncodeVectorValue(vec)
		require.NoError(t, err)

		err = db.Set(key, value, pebble.NoSync)
		require.NoError(t, err)
	}

	// Iterate over all versions
	prefix := schema.EncodeVectorKeyPrefix(namespace, id)
	iter := newVectorIterator(db, prefix)
	defer func() { _ = iter.Close() }()

	count := 0
	for iter.Next() {
		count++
		_, iterID, version := iter.Key()
		assert.Equal(t, id, iterID)
		assert.Equal(t, uint64(count), version)
	}

	assert.Equal(t, 3, count)
	assert.NoError(t, iter.Err())
}

func TestMetadataIterator_Success(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	namespace := "test_ns"
	field := "category"
	value := "tech"

	// Insert test data
	for i := 1; i <= 5; i++ {
		vec := &schema.VectorValue{
			ID:        uint64(i),
			Embedding: []float32{float32(i) * 0.1},
			Metadata: map[string]string{
				field: value,
			},
			Version: 1,
		}

		// Store vector
		vectorKey := schema.EncodeVectorKey(namespace, uint64(i), 1)
		vectorValue, err := schema.EncodeVectorValue(vec)
		require.NoError(t, err)
		err = db.Set(vectorKey, vectorValue, pebble.NoSync)
		require.NoError(t, err)

		// Store metadata index
		metaKey := schema.EncodeMetadataKey(namespace, field, value, uint64(i), 1)
		err = db.Set(metaKey, []byte{}, pebble.NoSync)
		require.NoError(t, err)
	}

	prefix := schema.EncodeMetadataKeyPrefix(namespace, field, value)
	iter := newMetadataIterator(db, prefix)
	defer func() { _ = iter.Close() }()

	count := 0
	for iter.Next() {
		count++

		vec, err := iter.Value()
		assert.NoError(t, err)
		assert.NotNil(t, vec)
		assert.Equal(t, value, vec.Metadata[field])

		ns, id, version := iter.Key()
		assert.Equal(t, namespace, ns)
		assert.Greater(t, id, uint64(0))
		assert.Equal(t, uint64(1), version)
	}

	assert.NoError(t, iter.Err())
	assert.Equal(t, 5, count)
}

func TestMetadataIterator_Empty(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	prefix := schema.EncodeMetadataKeyPrefix("empty_ns", "field", "value")
	iter := newMetadataIterator(db, prefix)
	defer func() { _ = iter.Close() }()

	count := 0
	for iter.Next() {
		count++
	}

	assert.Equal(t, 0, count)
	assert.NoError(t, iter.Err())
}

func TestMetadataIterator_InvalidMetadataKey(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Insert invalid metadata key
	invalidKey := []byte{schema.PrefixMetadata, schema.Separator, 'i', 'n', 'v'}
	err := db.Set(invalidKey, []byte{}, pebble.NoSync)
	require.NoError(t, err)

	prefix := []byte{schema.PrefixMetadata, schema.Separator}
	iter := newMetadataIterator(db, prefix)
	defer func() { _ = iter.Close() }()

	hasNext := iter.Next()
	assert.False(t, hasNext)
	assert.Error(t, iter.Err())
}

func TestMetadataIterator_VectorNotFound(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	namespace := "test_ns"

	// Insert metadata index without corresponding vector
	metaKey := schema.EncodeMetadataKey(namespace, "field", "value", 1, 1)
	err := db.Set(metaKey, []byte{}, pebble.NoSync)
	require.NoError(t, err)

	prefix := schema.EncodeMetadataKeyPrefix(namespace, "field", "value")
	iter := newMetadataIterator(db, prefix)
	defer func() { _ = iter.Close() }()

	hasNext := iter.Next()
	assert.False(t, hasNext)
	assert.Error(t, iter.Err())
}

func TestMetadataIterator_InvalidVectorValue(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	namespace := "test_ns"

	// Insert metadata index
	metaKey := schema.EncodeMetadataKey(namespace, "field", "value", 1, 1)
	err := db.Set(metaKey, []byte{}, pebble.NoSync)
	require.NoError(t, err)

	// Insert invalid vector value
	vectorKey := schema.EncodeVectorKey(namespace, 1, 1)
	err = db.Set(vectorKey, []byte{0xFF, 0xFF}, pebble.NoSync)
	require.NoError(t, err)

	prefix := schema.EncodeMetadataKeyPrefix(namespace, "field", "value")
	iter := newMetadataIterator(db, prefix)
	defer func() { _ = iter.Close() }()

	hasNext := iter.Next()
	assert.False(t, hasNext)
	assert.Error(t, iter.Err())
}

func TestMetadataIterator_ValueBeforeNext(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	prefix := schema.EncodeMetadataKeyPrefix("test_ns", "field", "value")
	iter := newMetadataIterator(db, prefix)
	defer func() { _ = iter.Close() }()

	vec, err := iter.Value()
	assert.Error(t, err)
	assert.Nil(t, vec)
	assert.Equal(t, ErrVectorNotFound, err)
}

func TestMetadataIterator_MultipleFields(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	namespace := "test_ns"

	// Insert vectors with multiple metadata fields
	for i := 1; i <= 3; i++ {
		vec := &schema.VectorValue{
			ID:        uint64(i),
			Embedding: []float32{float32(i) * 0.1},
			Metadata: map[string]string{
				"category": "tech",
				"author":   "alice",
				"status":   "published",
			},
			Version: 1,
		}

		// Store vector
		vectorKey := schema.EncodeVectorKey(namespace, uint64(i), 1)
		vectorValue, err := schema.EncodeVectorValue(vec)
		require.NoError(t, err)
		err = db.Set(vectorKey, vectorValue, pebble.NoSync)
		require.NoError(t, err)

		// Store all metadata indexes
		for field, val := range vec.Metadata {
			metaKey := schema.EncodeMetadataKey(namespace, field, val, uint64(i), 1)
			err = db.Set(metaKey, []byte{}, pebble.NoSync)
			require.NoError(t, err)
		}
	}

	// Query by specific field
	prefix := schema.EncodeMetadataKeyPrefix(namespace, "author", "alice")
	iter := newMetadataIterator(db, prefix)
	defer func() { _ = iter.Close() }()

	count := 0
	for iter.Next() {
		count++
		vec, err := iter.Value()
		assert.NoError(t, err)
		assert.Equal(t, "alice", vec.Metadata["author"])
	}

	assert.Equal(t, 3, count)
}

func BenchmarkVectorIterator(b *testing.B) {
	tmpDir := b.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(b, err)
	defer func() { _ = db.Close() }()

	namespace := "bench_ns"

	// Insert test data
	for i := 1; i <= 1000; i++ {
		vec := &schema.VectorValue{
			ID:        uint64(i),
			Embedding: []float32{float32(i) * 0.1},
			Metadata: map[string]string{
				"index": string(rune(i)),
			},
			Version: 1,
		}

		key := schema.EncodeVectorKey(namespace, uint64(i), 1)
		value, _ := schema.EncodeVectorValue(vec)
		_ = db.Set(key, value, pebble.NoSync)
	}

	prefix := schema.EncodeNamespacePrefix(namespace)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter := newVectorIterator(db, prefix)
		for iter.Next() {
			_, _ = iter.Value()
		}
		_ = iter.Close()
	}
}

func BenchmarkMetadataIterator(b *testing.B) {
	tmpDir := b.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(b, err)
	defer func() { _ = db.Close() }()

	namespace := "bench_ns"
	field := "category"
	value := "tech"

	// Insert test data
	for i := 1; i <= 1000; i++ {
		vec := &schema.VectorValue{
			ID:        uint64(i),
			Embedding: []float32{float32(i) * 0.1},
			Metadata: map[string]string{
				field: value,
			},
			Version: 1,
		}

		vectorKey := schema.EncodeVectorKey(namespace, uint64(i), 1)
		vectorValue, _ := schema.EncodeVectorValue(vec)
		_ = db.Set(vectorKey, vectorValue, pebble.NoSync)

		metaKey := schema.EncodeMetadataKey(namespace, field, value, uint64(i), 1)
		_ = db.Set(metaKey, []byte{}, pebble.NoSync)
	}

	prefix := schema.EncodeMetadataKeyPrefix(namespace, field, value)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter := newMetadataIterator(db, prefix)
		for iter.Next() {
			_, _ = iter.Value()
		}
		_ = iter.Close()
	}
}
