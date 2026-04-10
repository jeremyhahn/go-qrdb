//go:build qrdb_vector

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

package hnsw

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHNSWIndex_Save(t *testing.T) {
	// Create temporary directory for test
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "hnsw_test.db")

	// Open database
	db, err := pebble.Open(dbPath, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	// Create and populate index
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	vectors := [][]float32{
		{1, 0, 0},
		{0, 1, 0},
		{0, 0, 1},
	}

	for i, v := range vectors {
		err = idx.Insert(uint64(i+1), v)
		require.NoError(t, err)
	}

	// Save index
	err = idx.Save(db)
	require.NoError(t, err)

	// Verify data was written
	iter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefixNode),
		UpperBound: []byte(prefixNode + "\xff"),
	})
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	nodeCount := 0
	for iter.First(); iter.Valid(); iter.Next() {
		nodeCount++
	}

	assert.Equal(t, 3, nodeCount)
}

func TestHNSWIndex_Load(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "hnsw_test.db")

	// Create and save index
	db, err := pebble.Open(dbPath, &pebble.Options{})
	require.NoError(t, err)

	idx1, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	vectors := [][]float32{
		{1, 2, 3},
		{4, 5, 6},
		{7, 8, 9},
	}

	for i, v := range vectors {
		err = idx1.Insert(uint64(i+1), v)
		require.NoError(t, err)
	}

	err = idx1.Save(db)
	require.NoError(t, err)
	_ = db.Close()

	// Load index
	db, err = pebble.Open(dbPath, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	idx2, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	err = idx2.Load(db)
	require.NoError(t, err)

	// Verify loaded data
	assert.Equal(t, idx1.Count(), idx2.Count())
	assert.Equal(t, idx1.dimensions.Load(), idx2.dimensions.Load())
	assert.Equal(t, idx1.entryPoint.Load(), idx2.entryPoint.Load())
	assert.Equal(t, idx1.maxLayer.Load(), idx2.maxLayer.Load())

	// Verify nodes
	for i := uint64(1); i <= 3; i++ {
		node1, err1 := idx1.Get(i)
		node2, err2 := idx2.Get(i)

		require.NoError(t, err1)
		require.NoError(t, err2)

		assert.Equal(t, node1.ID, node2.ID)
		assert.Equal(t, node1.Vector, node2.Vector)
		assert.Equal(t, node1.Layer, node2.Layer)
	}
}

func TestHNSWIndex_SaveLoad_LargeIndex(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "hnsw_large.db")

	db, err := pebble.Open(dbPath, &pebble.Options{})
	require.NoError(t, err)

	// Create large index
	idx1, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	const numVectors = 1000
	for i := uint64(1); i <= numVectors; i++ {
		vector := []float32{float32(i), float32(i * 2), float32(i * 3)}
		err = idx1.Insert(i, vector)
		require.NoError(t, err)
	}

	// Save
	err = idx1.Save(db)
	require.NoError(t, err)
	_ = db.Close()

	// Load
	db, err = pebble.Open(dbPath, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	idx2, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	err = idx2.Load(db)
	require.NoError(t, err)

	// Verify
	assert.Equal(t, int64(numVectors), idx2.Count())

	// Test search still works
	query := []float32{500, 1000, 1500}
	results, err := idx2.Search(query, 10)
	require.NoError(t, err)
	assert.Len(t, results, 10)
}

func TestSnapshot(t *testing.T) {
	t.Run("snapshot and restore", func(t *testing.T) {
		// Create index
		idx1, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		vectors := [][]float32{
			{1, 2, 3},
			{4, 5, 6},
			{7, 8, 9},
		}

		for i, v := range vectors {
			err = idx1.Insert(uint64(i+1), v)
			require.NoError(t, err)
		}

		// Create snapshot
		var buf bytes.Buffer
		err = idx1.Snapshot(&buf)
		require.NoError(t, err)

		// Restore to new index
		idx2, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		err = idx2.Restore(&buf)
		require.NoError(t, err)

		// Verify
		assert.Equal(t, idx1.Count(), idx2.Count())
		assert.Equal(t, idx1.dimensions.Load(), idx2.dimensions.Load())

		for i := uint64(1); i <= 3; i++ {
			node1, _ := idx1.Get(i)
			node2, _ := idx2.Get(i)

			assert.Equal(t, node1.ID, node2.ID)
			assert.Equal(t, node1.Vector, node2.Vector)
		}
	})

	t.Run("empty index snapshot", func(t *testing.T) {
		idx1, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		var buf bytes.Buffer
		err = idx1.Snapshot(&buf)
		require.NoError(t, err)

		idx2, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		err = idx2.Restore(&buf)
		require.NoError(t, err)

		assert.Equal(t, int64(0), idx2.Count())
	})
}

func TestSerializeNode(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Create node
	node := NewNode(1, []float32{1, 2, 3}, 2)
	node.AddConnection(0, 2)
	node.AddConnection(0, 3)
	node.AddConnection(1, 4)

	// Serialize
	data, err := idx.serializeNode(node)
	require.NoError(t, err)
	assert.NotEmpty(t, data)

	// Deserialize
	restored, err := idx.deserializeNode(data)
	require.NoError(t, err)

	// Verify
	assert.Equal(t, node.ID, restored.ID)
	assert.Equal(t, node.Vector, restored.Vector)
	assert.Equal(t, node.Layer, restored.Layer)
	assert.Equal(t, len(node.Connections), len(restored.Connections))
}

func TestMetadataSerialization(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "metadata_test.db")

	db, err := pebble.Open(dbPath, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Set metadata
	idx.entryPoint.Store(42)
	idx.maxLayer.Store(5)
	idx.dimensions.Store(128)

	// Save metadata
	batch := db.NewBatch()
	err = idx.saveMetadata(batch)
	require.NoError(t, err)
	err = batch.Commit(pebble.Sync)
	require.NoError(t, err)
	_ = batch.Close()

	// Create new index and load metadata
	idx2, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	err = idx2.loadMetadata(db)
	require.NoError(t, err)

	// Verify
	assert.Equal(t, uint64(42), idx2.entryPoint.Load())
	assert.Equal(t, int32(5), idx2.maxLayer.Load())
	assert.Equal(t, int32(128), idx2.dimensions.Load())
}

func TestByteConversion(t *testing.T) {
	t.Run("uint64 conversion", func(t *testing.T) {
		original := uint64(12345678901234)
		bytes := uint64ToBytes(original)
		restored := bytesToUint64(bytes)
		assert.Equal(t, original, restored)
	})

	t.Run("int32 conversion", func(t *testing.T) {
		original := int32(123456)
		bytes := int32ToBytes(original)
		restored := bytesToInt32(bytes)
		assert.Equal(t, original, restored)
	})

	t.Run("int conversion", func(t *testing.T) {
		original := 12345
		bytes := intToBytes(original)
		restored := bytesToInt(bytes)
		assert.Equal(t, original, restored)
	})

	t.Run("negative int32", func(t *testing.T) {
		original := int32(-123)
		bytes := int32ToBytes(original)
		restored := bytesToInt32(bytes)
		assert.Equal(t, original, restored)
	})
}

func TestPersistence_WithConnections(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "connections_test.db")

	db, err := pebble.Open(dbPath, &pebble.Options{})
	require.NoError(t, err)

	// Create index with connections
	idx1, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	for i := uint64(1); i <= 20; i++ {
		vector := []float32{float32(i), float32(i * 2)}
		err = idx1.Insert(i, vector)
		require.NoError(t, err)
	}

	// Save
	err = idx1.Save(db)
	require.NoError(t, err)
	_ = db.Close()

	// Load
	db, err = pebble.Open(dbPath, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	idx2, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	err = idx2.Load(db)
	require.NoError(t, err)

	// Verify connections preserved
	for i := uint64(1); i <= 20; i++ {
		node1, _ := idx1.Get(i)
		node2, _ := idx2.Get(i)

		for layer := 0; layer <= node1.Layer; layer++ {
			conns1 := node1.GetConnections(layer)
			conns2 := node2.GetConnections(layer)

			assert.Equal(t, len(conns1), len(conns2),
				"Node %d layer %d connection count mismatch", i, layer)
		}
	}

	// Test search works after load
	query := []float32{10, 20}
	results, err := idx2.Search(query, 5)
	require.NoError(t, err)
	assert.Len(t, results, 5)
}

func TestPersistence_IncrementalUpdates(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "incremental_test.db")

	db, err := pebble.Open(dbPath, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Insert and save multiple times
	for batch := 0; batch < 3; batch++ {
		start := uint64(batch*10 + 1)
		end := start + 9

		for i := start; i <= end; i++ {
			vector := []float32{float32(i), float32(i * 2)}
			err = idx.Insert(i, vector)
			require.NoError(t, err)
		}

		err = idx.Save(db)
		require.NoError(t, err)
	}

	// Load and verify
	idx2, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	err = idx2.Load(db)
	require.NoError(t, err)

	assert.Equal(t, int64(30), idx2.Count())
}

func TestPersistence_EmptyIndex(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "empty_test.db")

	db, err := pebble.Open(dbPath, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	// Save empty index
	idx1, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	err = idx1.Save(db)
	require.NoError(t, err)

	// Load empty index
	idx2, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	err = idx2.Load(db)
	require.NoError(t, err)

	assert.Equal(t, int64(0), idx2.Count())
}

func BenchmarkSave(b *testing.B) {
	tmpDir := b.TempDir()

	idx, _ := NewHNSWIndex(nil)

	// Insert vectors
	for i := uint64(1); i <= 1000; i++ {
		vector := []float32{float32(i), float32(i * 2), float32(i * 3)}
		_ = idx.Insert(i, vector)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		dbPath := filepath.Join(tmpDir, "bench.db")
		_ = os.RemoveAll(dbPath)
		db, _ := pebble.Open(dbPath, &pebble.Options{})
		b.StartTimer()

		_ = idx.Save(db)

		b.StopTimer()
		_ = db.Close()
		b.StartTimer()
	}
}

func BenchmarkLoad(b *testing.B) {
	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench_load.db")

	// Create and save index once
	db, _ := pebble.Open(dbPath, &pebble.Options{})
	idx, _ := NewHNSWIndex(nil)

	for i := uint64(1); i <= 1000; i++ {
		vector := []float32{float32(i), float32(i * 2), float32(i * 3)}
		_ = idx.Insert(i, vector)
	}

	_ = idx.Save(db)
	_ = db.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db, _ := pebble.Open(dbPath, &pebble.Options{})
		idx2, _ := NewHNSWIndex(nil)

		_ = idx2.Load(db)

		_ = db.Close()
	}
}

func BenchmarkSnapshot(b *testing.B) {
	idx, _ := NewHNSWIndex(nil)

	for i := uint64(1); i <= 1000; i++ {
		vector := []float32{float32(i), float32(i * 2), float32(i * 3)}
		_ = idx.Insert(i, vector)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		_ = idx.Snapshot(&buf)
	}
}

func BenchmarkRestore(b *testing.B) {
	idx, _ := NewHNSWIndex(nil)

	for i := uint64(1); i <= 1000; i++ {
		vector := []float32{float32(i), float32(i * 2), float32(i * 3)}
		_ = idx.Insert(i, vector)
	}

	var buf bytes.Buffer
	_ = idx.Snapshot(&buf)
	data := buf.Bytes()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx2, _ := NewHNSWIndex(nil)
		buf := bytes.NewBuffer(data)
		_ = idx2.Restore(buf)
	}
}
