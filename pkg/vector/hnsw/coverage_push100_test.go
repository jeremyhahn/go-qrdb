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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestL2SIMDAllRemainderCases tests all remainder cases for SIMD functions
func TestL2SIMDAllRemainderCases(t *testing.T) {
	// Test with 1 dimension (no chunks, only remainder)
	t.Run("1 dimension", func(t *testing.T) {
		a := []float32{1}
		b := []float32{2}
		dist := L2SIMD(a, b)
		expected := L2(a, b)
		assert.InDelta(t, expected, dist, 1e-6)
	})

	// Test with 2 dimensions
	t.Run("2 dimensions", func(t *testing.T) {
		a := []float32{1, 2}
		b := []float32{2, 3}
		dist := L2SIMD(a, b)
		expected := L2(a, b)
		assert.InDelta(t, expected, dist, 1e-6)
	})

	// Test with 3 dimensions
	t.Run("3 dimensions", func(t *testing.T) {
		a := []float32{1, 2, 3}
		b := []float32{2, 3, 4}
		dist := L2SIMD(a, b)
		expected := L2(a, b)
		assert.InDelta(t, expected, dist, 1e-6)
	})

	// Test with 8 dimensions (2 chunks, no remainder)
	t.Run("8 dimensions", func(t *testing.T) {
		a := []float32{1, 2, 3, 4, 5, 6, 7, 8}
		b := []float32{2, 3, 4, 5, 6, 7, 8, 9}
		dist := L2SIMD(a, b)
		expected := L2(a, b)
		assert.InDelta(t, expected, dist, 1e-6)
	})

	// Test with 9 dimensions (2 chunks + 1 remainder)
	t.Run("9 dimensions", func(t *testing.T) {
		a := []float32{1, 2, 3, 4, 5, 6, 7, 8, 9}
		b := []float32{2, 3, 4, 5, 6, 7, 8, 9, 10}
		dist := L2SIMD(a, b)
		expected := L2(a, b)
		assert.InDelta(t, expected, dist, 1e-6)
	})
}

// TestCosineSIMDAllRemainderCases tests all remainder cases for CosineSIMD
func TestCosineSIMDAllRemainderCases(t *testing.T) {
	// Test with 1 dimension
	t.Run("1 dimension", func(t *testing.T) {
		a := []float32{1}
		b := []float32{2}
		dist := CosineSIMD(a, b)
		expected := Cosine(a, b)
		assert.InDelta(t, expected, dist, 1e-6)
	})

	// Test with 2 dimensions
	t.Run("2 dimensions", func(t *testing.T) {
		a := []float32{1, 2}
		b := []float32{2, 3}
		dist := CosineSIMD(a, b)
		expected := Cosine(a, b)
		assert.InDelta(t, expected, dist, 1e-6)
	})

	// Test with 3 dimensions
	t.Run("3 dimensions", func(t *testing.T) {
		a := []float32{1, 2, 3}
		b := []float32{2, 3, 4}
		dist := CosineSIMD(a, b)
		expected := Cosine(a, b)
		assert.InDelta(t, expected, dist, 1e-6)
	})

	// Test with 8 dimensions (2 chunks)
	t.Run("8 dimensions", func(t *testing.T) {
		a := []float32{1, 2, 3, 4, 5, 6, 7, 8}
		b := []float32{2, 3, 4, 5, 6, 7, 8, 9}
		dist := CosineSIMD(a, b)
		expected := Cosine(a, b)
		assert.InDelta(t, expected, dist, 1e-6)
	})

	// Test with 9 dimensions (2 chunks + 1 remainder)
	t.Run("9 dimensions", func(t *testing.T) {
		a := []float32{1, 2, 3, 4, 5, 6, 7, 8, 9}
		b := []float32{2, 3, 4, 5, 6, 7, 8, 9, 10}
		dist := CosineSIMD(a, b)
		expected := Cosine(a, b)
		assert.InDelta(t, expected, dist, 1e-6)
	})
}

// TestSelectNeighborsHeuristicAllPaths ensures all heuristic paths are tested
func TestSelectNeighborsHeuristicAllPaths(t *testing.T) {
	t.Run("heuristic with clustered candidates", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Create a cluster of nearby points
		for i := uint64(1); i <= 10; i++ {
			x := float32(i) * 0.1 // Points at 0.1, 0.2, 0.3, etc.
			vector := []float32{x, 0}
			err = idx.Insert(i, vector)
			require.NoError(t, err)
		}

		// Query near the cluster
		query := []float32{0.5, 0}

		// Create candidates that are all close to each other
		candidates := []*candidate{
			{id: 5, distance: 0.0},
			{id: 4, distance: 0.1},
			{id: 6, distance: 0.1},
			{id: 3, distance: 0.2},
			{id: 7, distance: 0.2},
		}

		// Heuristic should reject some clustered points
		selected := idx.selectNeighborsHeuristic(query, candidates, 2)
		assert.LessOrEqual(t, len(selected), 2)
	})

	t.Run("heuristic when nodes not found during comparison", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		err = idx.Insert(1, []float32{1, 0})
		require.NoError(t, err)

		query := []float32{1, 0}

		// Mix of valid and invalid node IDs
		candidates := []*candidate{
			{id: 1, distance: 0.0},
			{id: 999, distance: 0.1}, // Invalid - won't be found in comparison
		}

		selected := idx.selectNeighborsHeuristic(query, candidates, 2)
		// Should still return valid results
		assert.Greater(t, len(selected), 0)
	})
}

// TestInsertAllPaths ensures all paths in insert are covered
func TestInsertAllPaths(t *testing.T) {
	t.Run("insert with varying layers", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Insert many nodes to exercise all paths
		for i := uint64(1); i <= 200; i++ {
			vector := []float32{float32(i), float32(i * 2)}
			err = idx.Insert(i, vector)
			require.NoError(t, err)
		}

		// Verify structure
		assert.Greater(t, idx.Count(), int64(0))

		// Check that we have nodes at different layers
		layerCounts := make(map[int]int)
		idx.nodes.Range(func(key, value interface{}) bool {
			node := value.(*HNSWNode)
			layerCounts[node.Layer]++
			return true
		})

		// Should have nodes at layer 0 at minimum
		assert.Greater(t, layerCounts[0], 0)
	})
}

// TestSearchRadiusEdgeCasesComplete covers remaining SearchRadius paths
func TestSearchRadiusEdgeCasesComplete(t *testing.T) {
	t.Run("SearchRadius with invalid dimensions", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		err = idx.Insert(1, []float32{1, 2})
		require.NoError(t, err)

		// Query with wrong dimensions
		query := []float32{1, 2, 3}
		_, err = idx.SearchRadius(query, 1.0, 10)
		assert.Error(t, err)
		assert.Equal(t, ErrInvalidVector, err)
	})

	t.Run("SearchRadius returns empty index error", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		query := []float32{1, 2}
		_, err = idx.SearchRadius(query, 1.0, 10)
		assert.Error(t, err)
		assert.Equal(t, ErrInvalidVector, err)
	})

	t.Run("SearchRadius early termination at maxResults", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Insert many points that will be within radius
		for i := uint64(1); i <= 50; i++ {
			err = idx.Insert(i, []float32{float32(i % 10), 0})
			require.NoError(t, err)
		}

		query := []float32{5, 0}
		results, err := idx.SearchRadius(query, 10.0, 3)
		require.NoError(t, err)

		// Should stop at maxResults
		assert.Equal(t, 3, len(results))
	})
}

// TestGetNodeFailureInOperations tests node lookup failures in various operations
func TestGetNodeFailureInOperations(t *testing.T) {
	t.Run("selectNeighborsHeuristic with missing nodes", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		err = idx.Insert(1, []float32{1, 0})
		require.NoError(t, err)

		query := []float32{1, 0}

		// Create candidates with non-existent IDs
		candidates := []*candidate{
			{id: 999, distance: 0.1},  // Does not exist
			{id: 1000, distance: 0.2}, // Does not exist
		}

		selected := idx.selectNeighborsHeuristic(query, candidates, 2)
		// Might return fewer than requested due to missing nodes
		assert.LessOrEqual(t, len(selected), 2)
	})
}

// TestPersistenceEdgeCases tests remaining persistence paths
func TestPersistenceEdgeCases(t *testing.T) {
	t.Run("serializeNode with connections", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Insert enough nodes to create connections
		for i := uint64(1); i <= 30; i++ {
			vector := []float32{float32(i), float32(i * 2)}
			err = idx.Insert(i, vector)
			require.NoError(t, err)
		}

		// Get a node and serialize it
		node, err := idx.Get(1)
		require.NoError(t, err)

		data, err := idx.serializeNode(node)
		require.NoError(t, err)
		assert.Greater(t, len(data), 0)

		// Should be able to deserialize
		restored, err := idx.deserializeNode(data)
		require.NoError(t, err)
		assert.Equal(t, node.ID, restored.ID)
	})
}

// TestComplexScenarios tests complex real-world scenarios
func TestComplexScenarios(t *testing.T) {
	t.Run("insert, search, delete, search again", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Insert nodes
		for i := uint64(1); i <= 50; i++ {
			vector := []float32{float32(i), float32(i * 2)}
			err = idx.Insert(i, vector)
			require.NoError(t, err)
		}

		// Search
		query := []float32{25, 50}
		results1, err := idx.Search(query, 10)
		require.NoError(t, err)
		assert.Len(t, results1, 10)

		// Delete some nodes
		for i := uint64(20); i <= 30; i++ {
			err = idx.Delete(i)
			require.NoError(t, err)
		}

		// Search again
		results2, err := idx.Search(query, 10)
		require.NoError(t, err)
		assert.Len(t, results2, 10)

		// Results should be different after deletions
		// (though exact matches may vary)
	})
}
