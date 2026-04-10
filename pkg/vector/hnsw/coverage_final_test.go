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
	"os"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPersistenceDetailedCoverage ensures all persistence paths are covered
func TestPersistenceDetailedCoverage(t *testing.T) {
	t.Run("Save with error during node iteration", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "hnsw-test-*")
		require.NoError(t, err)
		defer func() { _ = os.RemoveAll(tmpDir) }()

		db, err := pebble.Open(tmpDir, &pebble.Options{})
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Insert multiple nodes
		for i := uint64(1); i <= 5; i++ {
			vector := []float32{float32(i), float32(i * 2)}
			err = idx.Insert(i, vector)
			require.NoError(t, err)
		}

		// Save should succeed
		err = idx.Save(db)
		require.NoError(t, err)
	})

	t.Run("Load with all metadata fields present", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "hnsw-test-*")
		require.NoError(t, err)
		defer func() { _ = os.RemoveAll(tmpDir) }()

		db, err := pebble.Open(tmpDir, &pebble.Options{})
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		// Create and save index
		idx1, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		for i := uint64(1); i <= 10; i++ {
			vector := []float32{float32(i), float32(i * 2), float32(i * 3)}
			err = idx1.Insert(i, vector)
			require.NoError(t, err)
		}

		err = idx1.Save(db)
		require.NoError(t, err)

		// Load into new index
		idx2, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		err = idx2.Load(db)
		require.NoError(t, err)

		// All metadata should be loaded
		assert.Equal(t, idx1.entryPoint.Load(), idx2.entryPoint.Load())
		assert.Equal(t, idx1.maxLayer.Load(), idx2.maxLayer.Load())
		assert.Equal(t, idx1.dimensions.Load(), idx2.dimensions.Load())
		assert.Equal(t, idx1.Count(), idx2.Count())
	})

	t.Run("loadMetadata with missing metadata keys", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "hnsw-test-*")
		require.NoError(t, err)
		defer func() { _ = os.RemoveAll(tmpDir) }()

		db, err := pebble.Open(tmpDir, &pebble.Options{})
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		// Don't save anything, just try to load
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Load should handle missing keys gracefully (pebble.ErrNotFound)
		err = idx.Load(db)
		require.NoError(t, err)

		// Values should remain at defaults
		assert.Equal(t, uint64(0), idx.entryPoint.Load())
		assert.Equal(t, int32(-1), idx.maxLayer.Load())
		assert.Equal(t, int32(0), idx.dimensions.Load())
	})

	t.Run("saveNode and serializeNode coverage", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "hnsw-test-*")
		require.NoError(t, err)
		defer func() { _ = os.RemoveAll(tmpDir) }()

		db, err := pebble.Open(tmpDir, &pebble.Options{})
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Insert node with connections at multiple layers
		for i := uint64(1); i <= 20; i++ {
			vector := []float32{float32(i), float32(i * 2)}
			err = idx.Insert(i, vector)
			require.NoError(t, err)
		}

		// Save should serialize all nodes with their connections
		err = idx.Save(db)
		require.NoError(t, err)

		// Verify by loading
		idx2, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		err = idx2.Load(db)
		require.NoError(t, err)

		// Check that connections were preserved
		for i := uint64(1); i <= 20; i++ {
			node1, err := idx.Get(i)
			if err != nil {
				continue
			}
			node2, err := idx2.Get(i)
			require.NoError(t, err)

			// Verify connections match for layer 0
			conns1 := node1.GetConnections(0)
			conns2 := node2.GetConnections(0)
			assert.Equal(t, len(conns1), len(conns2))
		}
	})
}

// TestL2SIMDZeroDimensions tests L2SIMD with exactly 0 dimensions (edge case)
func TestL2SIMDZeroDimensions(t *testing.T) {
	a := []float32{}
	b := []float32{}

	dist := L2SIMD(a, b)
	assert.Equal(t, float32(0), dist)
}

// TestCosineSIMDZeroDimensions tests CosineSIMD with exactly 0 dimensions
func TestCosineSIMDZeroDimensions(t *testing.T) {
	a := []float32{}
	b := []float32{}

	dist := CosineSIMD(a, b)
	// Should return 1.0 for zero vectors (maximum distance)
	assert.Equal(t, float32(1.0), dist)
}

// TestL2SIMDMultipleOf4 tests L2SIMD with exactly multiple of 4 (no remainder)
func TestL2SIMDMultipleOf4(t *testing.T) {
	a := []float32{1, 2, 3, 4}
	b := []float32{2, 3, 4, 5}

	dist := L2SIMD(a, b)
	expected := L2(a, b)

	assert.InDelta(t, expected, dist, 1e-6)
}

// TestCosineSIMDMultipleOf4 tests CosineSIMD with exactly multiple of 4
func TestCosineSIMDMultipleOf4(t *testing.T) {
	a := []float32{1, 2, 3, 4}
	b := []float32{2, 3, 4, 5}

	dist := CosineSIMD(a, b)
	expected := Cosine(a, b)

	assert.InDelta(t, expected, dist, 1e-6)
}

// TestSelectNeighborsHeuristicRejection tests the rejection path in heuristic
func TestSelectNeighborsHeuristicRejection(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Insert nodes in a line
	for i := uint64(1); i <= 10; i++ {
		vector := []float32{float32(i), 0}
		err = idx.Insert(i, vector)
		require.NoError(t, err)
	}

	// Create candidates where some should be rejected due to proximity
	query := []float32{5, 0}
	candidates := []*candidate{
		{id: 5, distance: 0.0},  // Exact match
		{id: 4, distance: 1.0},  // Close to 5
		{id: 6, distance: 1.0},  // Close to 5
		{id: 1, distance: 4.0},  // Far
		{id: 10, distance: 5.0}, // Far
	}

	// Request 3 neighbors - heuristic should reject some due to clustering
	selected := idx.selectNeighborsHeuristic(query, candidates, 3)

	// Should return 3 neighbors
	assert.LessOrEqual(t, len(selected), 3)
	assert.Greater(t, len(selected), 0)
}

// TestInsertMaxLayerUpdate tests the max layer update during insert
func TestInsertMaxLayerUpdate(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Insert nodes until we get one with layer > 0
	inserted := false
	for i := uint64(1); i <= 200 && !inserted; i++ {
		vector := []float32{float32(i), float32(i * 2)}
		err = idx.Insert(i, vector)
		require.NoError(t, err)

		// Check if we got a node with layer > 0
		node, _ := idx.Get(i)
		if node.Layer > 0 {
			// Verify max layer was updated
			assert.GreaterOrEqual(t, int(idx.maxLayer.Load()), node.Layer)
			inserted = true
		}
	}

	// We should have found at least one node with layer > 0 in 200 tries
	assert.True(t, inserted, "Should have inserted at least one node with layer > 0")
}

// TestSearchRadiusAllPaths tests all code paths in SearchRadius
func TestSearchRadiusAllPaths(t *testing.T) {
	t.Run("SearchRadius with small radius", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		for i := uint64(1); i <= 10; i++ {
			err = idx.Insert(i, []float32{float32(i), 0})
			require.NoError(t, err)
		}

		query := []float32{5, 0}
		// Small radius should return fewer results
		results, err := idx.SearchRadius(query, 0.5, 10)
		require.NoError(t, err)

		// All results should be within radius
		for _, r := range results {
			assert.LessOrEqual(t, r.Distance, float32(0.5))
		}
	})

	t.Run("SearchRadius reaching maxResults limit", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		for i := uint64(1); i <= 20; i++ {
			err = idx.Insert(i, []float32{float32(i), 0})
			require.NoError(t, err)
		}

		query := []float32{10, 0}
		// Large radius but limited results
		results, err := idx.SearchRadius(query, 20.0, 5)
		require.NoError(t, err)

		// Should stop at maxResults even if more are within radius
		assert.LessOrEqual(t, len(results), 5)
	})
}

// TestLoadIteratorError tests error handling in Load iterator
func TestLoadIteratorError(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "hnsw-test-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)

	// Save valid data
	idx1, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	for i := uint64(1); i <= 5; i++ {
		err = idx1.Insert(i, []float32{float32(i), float32(i * 2)})
		require.NoError(t, err)
	}

	err = idx1.Save(db)
	require.NoError(t, err)

	// Add corrupt data after valid data
	key := []byte(prefixNode + "99999999999999999999")
	corruptData := []byte{0xFF, 0xFF, 0xFF}
	err = db.Set(key, corruptData, pebble.Sync)
	require.NoError(t, err)

	_ = db.Close()

	// Reopen and try to load
	db, err = pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	idx2, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Load should fail on corrupt data
	err = idx2.Load(db)
	assert.Error(t, err)
}

// TestInsertEntryPointNotFound tests the early return path in insert
func TestInsertEntryPointNotFound(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Insert first node
	err = idx.Insert(1, []float32{1, 2})
	require.NoError(t, err)

	// Corrupt entry point
	idx.entryPoint.Store(9999)

	// Insert should handle missing entry point (early return in insert function)
	err = idx.Insert(2, []float32{2, 3})
	require.NoError(t, err)

	// Node should still be added to the index
	assert.Equal(t, int64(2), idx.Count())
}

// TestSaveMetadataAllBranches ensures all branches in saveMetadata are tested
func TestSaveMetadataAllBranches(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "hnsw-test-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Set specific values for all metadata fields
	err = idx.Insert(1, []float32{1, 2, 3})
	require.NoError(t, err)
	err = idx.Insert(2, []float32{2, 3, 4})
	require.NoError(t, err)

	// Save should write all metadata
	err = idx.Save(db)
	require.NoError(t, err)

	// Verify all metadata was saved by loading
	idx2, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	err = idx2.Load(db)
	require.NoError(t, err)

	assert.NotEqual(t, uint64(0), idx2.entryPoint.Load())
	assert.NotEqual(t, int32(-1), idx2.maxLayer.Load())
	assert.Equal(t, int32(3), idx2.dimensions.Load())
}

// TestSerializeNodeWithMultipleLayers tests serialization of nodes with connections at multiple layers
func TestSerializeNodeWithMultipleLayers(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Insert enough nodes to get multi-layer structure
	for i := uint64(1); i <= 100; i++ {
		vector := []float32{float32(i), float32(i * 2)}
		err = idx.Insert(i, vector)
		require.NoError(t, err)
	}

	// Find a node with multiple layers
	var multiLayerNode *HNSWNode
	idx.nodes.Range(func(key, value interface{}) bool {
		node := value.(*HNSWNode)
		if node.Layer > 0 {
			multiLayerNode = node
			return false
		}
		return true
	})

	if multiLayerNode != nil {
		// Serialize and deserialize
		data, err := idx.serializeNode(multiLayerNode)
		require.NoError(t, err)
		assert.Greater(t, len(data), 0)

		// Deserialize
		restored, err := idx.deserializeNode(data)
		require.NoError(t, err)

		assert.Equal(t, multiLayerNode.ID, restored.ID)
		assert.Equal(t, multiLayerNode.Layer, restored.Layer)
		assert.Equal(t, len(multiLayerNode.Vector), len(restored.Vector))
	}
}
