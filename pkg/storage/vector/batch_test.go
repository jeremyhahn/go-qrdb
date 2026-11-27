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
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector/schema"
)

func TestVectorBatch_UpsertVector(t *testing.T) {
	tmpDir := t.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()
	batch := newVectorBatch(db, metrics)
	defer func() { _ = batch.Close() }()

	t.Run("Success", func(t *testing.T) {
		vec := &VectorValue{
			ID:        1,
			Embedding: []float32{0.1, 0.2, 0.3},
			Metadata: map[string]string{
				"category": "test",
				"author":   "alice",
			},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
			Version:   1,
		}

		err := batch.UpsertVector("test_ns", vec)
		assert.NoError(t, err)
		assert.Equal(t, 1, batch.Count())
	})

	t.Run("NilVector", func(t *testing.T) {
		err := batch.UpsertVector("test_ns", nil)
		assert.Error(t, err)
		assert.Equal(t, ErrInvalidVector, err)
	})

	t.Run("EmptyNamespace", func(t *testing.T) {
		vec := &VectorValue{
			ID:        2,
			Embedding: []float32{0.1},
			Metadata:  map[string]string{},
			Version:   1,
		}

		err := batch.UpsertVector("", vec)
		assert.NoError(t, err)
	})

	t.Run("MultipleMetadataFields", func(t *testing.T) {
		vec := &VectorValue{
			ID:        3,
			Embedding: []float32{0.1, 0.2},
			Metadata: map[string]string{
				"field1": "value1",
				"field2": "value2",
				"field3": "value3",
			},
			Version: 1,
		}

		err := batch.UpsertVector("test_ns", vec)
		assert.NoError(t, err)
	})

	t.Run("NoMetadata", func(t *testing.T) {
		vec := &VectorValue{
			ID:        4,
			Embedding: []float32{0.5},
			Metadata:  nil,
			Version:   1,
		}

		err := batch.UpsertVector("test_ns", vec)
		assert.NoError(t, err)
	})

	t.Run("LargeEmbedding", func(t *testing.T) {
		vec := &VectorValue{
			ID:        5,
			Embedding: make([]float32, 1536),
			Metadata: map[string]string{
				"model": "ada-002",
			},
			Version: 1,
		}

		err := batch.UpsertVector("test_ns", vec)
		assert.NoError(t, err)
	})
}

func TestVectorBatch_DeleteVector(t *testing.T) {
	tmpDir := t.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	// First, insert a vector to delete
	batch1 := newVectorBatch(db, metrics)
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata: map[string]string{
			"category": "test",
			"author":   "alice",
		},
		Version: 1,
	}

	err = batch1.UpsertVector("test_ns", vec)
	require.NoError(t, err)
	err = batch1.Commit()
	require.NoError(t, err)
	_ = batch1.Close()

	t.Run("Success", func(t *testing.T) {
		batch := newVectorBatch(db, metrics)
		defer func() { _ = batch.Close() }()

		err := batch.DeleteVector("test_ns", 1)
		assert.NoError(t, err)
		assert.Equal(t, 1, batch.Count())
	})

	t.Run("InvalidID", func(t *testing.T) {
		batch := newVectorBatch(db, metrics)
		defer func() { _ = batch.Close() }()

		err := batch.DeleteVector("test_ns", 0)
		assert.Error(t, err)
		assert.Equal(t, ErrInvalidID, err)
	})

	t.Run("NonExistentVector", func(t *testing.T) {
		batch := newVectorBatch(db, metrics)
		defer func() { _ = batch.Close() }()

		err := batch.DeleteVector("test_ns", 999999)
		assert.NoError(t, err) // Should not error on non-existent
	})

	t.Run("EmptyNamespace", func(t *testing.T) {
		batch := newVectorBatch(db, metrics)
		defer func() { _ = batch.Close() }()

		err := batch.DeleteVector("", 1)
		assert.NoError(t, err)
	})
}

func TestVectorBatch_Commit(t *testing.T) {
	tmpDir := t.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	t.Run("Success", func(t *testing.T) {
		batch := newVectorBatch(db, metrics)
		defer func() { _ = batch.Close() }()

		vec := &VectorValue{
			ID:        1,
			Embedding: []float32{0.1},
			Metadata:  map[string]string{"k": "v"},
			Version:   1,
		}

		err := batch.UpsertVector("test_ns", vec)
		require.NoError(t, err)

		err = batch.Commit()
		assert.NoError(t, err)
	})

	t.Run("EmptyBatch", func(t *testing.T) {
		batch := newVectorBatch(db, metrics)
		defer func() { _ = batch.Close() }()

		err := batch.Commit()
		assert.Error(t, err)
		assert.Equal(t, ErrEmptyBatch, err)
	})

	t.Run("MultipleOperations", func(t *testing.T) {
		batch := newVectorBatch(db, metrics)
		defer func() { _ = batch.Close() }()

		for i := 0; i < 10; i++ {
			vec := &VectorValue{
				ID:        uint64(i + 1),
				Embedding: []float32{float32(i)},
				Metadata:  map[string]string{"index": string(rune(i))},
				Version:   1,
			}
			err := batch.UpsertVector("test_ns", vec)
			require.NoError(t, err)
		}

		err := batch.Commit()
		assert.NoError(t, err)
		assert.Equal(t, 10, batch.count)
	})
}

func TestVectorBatch_Reset(t *testing.T) {
	tmpDir := t.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()
	batch := newVectorBatch(db, metrics)
	defer func() { _ = batch.Close() }()

	// Add operations
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1},
		Metadata:  map[string]string{"k": "v"},
		Version:   1,
	}

	err = batch.UpsertVector("test_ns", vec)
	require.NoError(t, err)
	assert.Equal(t, 1, batch.Count())

	// Reset
	batch.Reset()
	assert.Equal(t, 0, batch.Count())

	// Should be able to add new operations after reset
	err = batch.UpsertVector("test_ns", vec)
	require.NoError(t, err)
	assert.Equal(t, 1, batch.Count())
}

func TestVectorBatch_Close(t *testing.T) {
	tmpDir := t.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()
	batch := newVectorBatch(db, metrics)

	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1},
		Metadata:  map[string]string{"k": "v"},
		Version:   1,
	}

	err = batch.UpsertVector("test_ns", vec)
	require.NoError(t, err)

	err = batch.Close()
	assert.NoError(t, err)
}

func TestVectorBatch_MultipleVersions(t *testing.T) {
	tmpDir := t.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	// Insert multiple versions of the same vector
	for version := uint64(1); version <= 3; version++ {
		batch := newVectorBatch(db, metrics)

		vec := &VectorValue{
			ID:        1,
			Embedding: []float32{float32(version)},
			Metadata: map[string]string{
				"version": string(rune('0' + version)),
			},
			Version: version,
		}

		err = batch.UpsertVector("test_ns", vec)
		require.NoError(t, err)

		err = batch.Commit()
		require.NoError(t, err)
		_ = batch.Close()
	}

	// Delete should remove all versions
	batch := newVectorBatch(db, metrics)
	defer func() { _ = batch.Close() }()

	err = batch.DeleteVector("test_ns", 1)
	assert.NoError(t, err)

	err = batch.Commit()
	assert.NoError(t, err)
}

// countPrefix reports how many keys the DB holds under prefix.
func countPrefix(t *testing.T, db *pebble.DB, prefix []byte) int {
	t.Helper()
	iter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: schema.PrefixSuccessor(prefix),
	})
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()
	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		n++
	}
	require.NoError(t, iter.Error())
	return n
}

// TestVectorBatch_RepeatedIDKeepsLastIndex pins Codex round 2 P1 on #1478:
// a batch that upserts one ID twice at the same version (the Raft apply
// writes Version 1 for every vector) keeps only the last write's metadata
// index, and an upsert then a delete of one ID leaves no index key.
func TestVectorBatch_RepeatedIDKeepsLastIndex(t *testing.T) {
	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	metrics := newVectorMetrics()
	const ns = "test_ns"

	t.Run("upsert twice", func(t *testing.T) {
		batch := newVectorBatch(db, metrics)
		defer func() { _ = batch.Close() }()
		require.NoError(t, batch.UpsertVector(ns, &VectorValue{ID: 7, Embedding: []float32{1}, Metadata: map[string]string{"tag": "red"}, Version: 1}))
		require.NoError(t, batch.UpsertVector(ns, &VectorValue{ID: 7, Embedding: []float32{2}, Metadata: map[string]string{"tag": "blue"}, Version: 1}))
		require.NoError(t, batch.Commit())

		assert.Zero(t, countPrefix(t, db, schema.EncodeMetadataKeyPrefix(ns, "tag", "red")), "the first write's metadata key remains")
		assert.Equal(t, 1, countPrefix(t, db, schema.EncodeMetadataKeyPrefix(ns, "tag", "blue")))
		value, closer, err := db.Get(schema.EncodeReverseKey(ns, 7, "tag", 1))
		require.NoError(t, err)
		assert.Equal(t, "blue", string(value))
		require.NoError(t, closer.Close())
		assert.Equal(t, 1, countPrefix(t, db, schema.EncodeReverseKeyPrefix(ns, 7)))
	})

	t.Run("upsert then delete", func(t *testing.T) {
		batch := newVectorBatch(db, metrics)
		defer func() { _ = batch.Close() }()
		require.NoError(t, batch.UpsertVector(ns, &VectorValue{ID: 9, Embedding: []float32{1}, Metadata: map[string]string{"tag": "green"}, Version: 1}))
		require.NoError(t, batch.DeleteVector(ns, 9))
		require.NoError(t, batch.Commit())

		assert.Zero(t, countPrefix(t, db, schema.EncodeMetadataKeyPrefix(ns, "tag", "green")))
		assert.Zero(t, countPrefix(t, db, schema.EncodeReverseKeyPrefix(ns, 9)))
		assert.Zero(t, countPrefix(t, db, schema.EncodeVectorKeyPrefix(ns, 9)))
	})

	t.Run("reset keeps the batch indexed", func(t *testing.T) {
		batch := newVectorBatch(db, metrics)
		defer func() { _ = batch.Close() }()
		require.NoError(t, batch.UpsertVector(ns, &VectorValue{ID: 11, Embedding: []float32{1}, Metadata: map[string]string{"tag": "x"}, Version: 1}))
		batch.Reset()
		require.NoError(t, batch.UpsertVector(ns, &VectorValue{ID: 11, Embedding: []float32{1}, Metadata: map[string]string{"tag": "y"}, Version: 1}))
		require.NoError(t, batch.UpsertVector(ns, &VectorValue{ID: 11, Embedding: []float32{1}, Metadata: map[string]string{"tag": "z"}, Version: 1}))
		require.NoError(t, batch.Commit())

		assert.Zero(t, countPrefix(t, db, schema.EncodeMetadataKeyPrefix(ns, "tag", "x")))
		assert.Zero(t, countPrefix(t, db, schema.EncodeMetadataKeyPrefix(ns, "tag", "y")))
		assert.Equal(t, 1, countPrefix(t, db, schema.EncodeMetadataKeyPrefix(ns, "tag", "z")))
	})
}

func TestVectorBatch_ConcurrentOperations(t *testing.T) {
	tmpDir := t.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	// Multiple batches can be created and used concurrently
	batch1 := newVectorBatch(db, metrics)
	batch2 := newVectorBatch(db, metrics)
	defer func() { _ = batch1.Close() }()
	defer func() { _ = batch2.Close() }()

	vec1 := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1},
		Metadata:  map[string]string{"batch": "1"},
		Version:   1,
	}

	vec2 := &VectorValue{
		ID:        2,
		Embedding: []float32{0.2},
		Metadata:  map[string]string{"batch": "2"},
		Version:   1,
	}

	err = batch1.UpsertVector("test_ns", vec1)
	assert.NoError(t, err)

	err = batch2.UpsertVector("test_ns", vec2)
	assert.NoError(t, err)

	err = batch1.Commit()
	assert.NoError(t, err)

	err = batch2.Commit()
	assert.NoError(t, err)
}

func BenchmarkVectorBatch_UpsertVector(b *testing.B) {
	tmpDir := b.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(b, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()
	batch := newVectorBatch(db, metrics)
	defer func() { _ = batch.Close() }()

	vec := &VectorValue{
		ID:        1,
		Embedding: make([]float32, 768),
		Metadata: map[string]string{
			"category": "test",
			"author":   "alice",
		},
		Version: 1,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vec.ID = uint64(i)
		_ = batch.UpsertVector("test_ns", vec)

		// Reset periodically to avoid memory issues
		if i%1000 == 0 {
			batch.Reset()
		}
	}
}

func BenchmarkVectorBatch_Commit(b *testing.B) {
	tmpDir := b.TempDir()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(b, err)
	defer func() { _ = db.Close() }()

	metrics := newVectorMetrics()

	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata: map[string]string{
			"category": "test",
		},
		Version: 1,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		batch := newVectorBatch(db, metrics)
		vec.ID = uint64(i)
		_ = batch.UpsertVector("test_ns", vec)
		_ = batch.Commit()
		_ = batch.Close()
	}
}
