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
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector/schema"
)

// TestNewPebbleVectorStorage_ErrorPaths tests error handling in constructor
func TestNewPebbleVectorStorage_ErrorPaths(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("NilConfig", func(t *testing.T) {
		_, err := NewPebbleVectorStorage(nil, &testLogger{})
		assert.Equal(t, ErrInvalidConfig, err)
	})

	t.Run("NilLogger", func(t *testing.T) {
		config := DefaultConfig(tmpDir)
		_, err := NewPebbleVectorStorage(config, nil)
		assert.Equal(t, ErrInvalidConfig, err)
	})

	t.Run("InvalidDataDir", func(t *testing.T) {
		config := &Config{DataDir: ""}
		_, err := NewPebbleVectorStorage(config, &testLogger{})
		assert.Equal(t, ErrInvalidConfig, err)
	})
}

// TestBatch_DeleteVector_ReverseIndexPaths tests reverse index iteration paths
func TestBatch_DeleteVector_ReverseIndexPaths(t *testing.T) {
	tmpDir := t.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	// Insert vector with metadata
	batch := newVectorBatch(db, metrics)
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2},
		Metadata: map[string]string{
			"field1": "value1",
			"field2": "value2",
		},
		Version: 1,
	}

	err = batch.UpsertVector("delete_reverse_test", vec)
	require.NoError(t, err)

	err = batch.Commit()
	require.NoError(t, err)
	_ = batch.Close()

	// Now delete - exercises reverse index cleanup
	batch2 := newVectorBatch(db, metrics)
	defer func() { _ = batch2.Close() }()

	err = batch2.DeleteVector("delete_reverse_test", 1)
	assert.NoError(t, err)

	err = batch2.Commit()
	assert.NoError(t, err)
}

// TestIterator_NextWithError tests Next() error handling path
func TestIterator_NextWithError(t *testing.T) {
	tmpDir := t.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	// Insert invalid vector value
	key := schema.EncodeVectorKey("test", 1, 1)
	err = db.Set(key, []byte{0xFF, 0xFE, 0xFD}, pebble.NoSync)
	require.NoError(t, err)

	prefix := schema.EncodeNamespacePrefix("test")
	iter := newVectorIterator(db, prefix)
	defer func() { _ = iter.Close() }()

	// Next should fail due to invalid value
	hasNext := iter.Next()
	assert.False(t, hasNext)
	assert.Error(t, iter.Err())
}

// TestMetadataIterator_DecodeValueError tests metadata iterator value decode error
func TestMetadataIterator_DecodeValueError(t *testing.T) {
	tmpDir := t.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	namespace := "meta_error_test"

	// Insert valid metadata key
	metaKey := schema.EncodeMetadataKey(namespace, "field", "value", 1, 1)
	err = db.Set(metaKey, []byte{}, pebble.NoSync)
	require.NoError(t, err)

	// Insert invalid vector value
	vectorKey := schema.EncodeVectorKey(namespace, 1, 1)
	err = db.Set(vectorKey, []byte{0xFF, 0xFE}, pebble.NoSync)
	require.NoError(t, err)

	prefix := schema.EncodeMetadataKeyPrefix(namespace, "field", "value")
	iter := newMetadataIterator(db, prefix)
	defer func() { _ = iter.Close() }()

	// Next should fail when trying to decode vector
	hasNext := iter.Next()
	assert.False(t, hasNext)
	assert.Error(t, iter.Err())
}
