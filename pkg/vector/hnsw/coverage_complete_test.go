//go:build qrdb_vector

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

package hnsw

import (
	"bytes"
	"container/heap"
	"io"
	"os"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCandidateHeap covers the candidateHeap implementation that has 0% coverage
func TestCandidateHeap(t *testing.T) {
	t.Run("basic heap operations", func(t *testing.T) {
		h := &candidateHeap{}

		// Test Len on empty heap
		assert.Equal(t, 0, h.Len())

		// Push some candidates
		c1 := &candidate{id: 1, distance: 0.5}
		c2 := &candidate{id: 2, distance: 0.3}
		c3 := &candidate{id: 3, distance: 0.7}

		h.Push(c1)
		h.Push(c2)
		h.Push(c3)

		// Test Len
		assert.Equal(t, 3, h.Len())

		// Test Less - candidateHeap is max-heap (distance > comparison)
		assert.True(t, h.Less(2, 0))  // 0.7 > 0.5
		assert.False(t, h.Less(0, 2)) // 0.5 < 0.7

		// Test Swap
		h.Swap(0, 1)
		assert.Equal(t, uint64(2), (*h)[0].id)
		assert.Equal(t, uint64(1), (*h)[1].id)

		// Test Pop
		popped := h.Pop().(*candidate)
		assert.Equal(t, uint64(3), popped.id)
		assert.Equal(t, 2, h.Len())
	})

	t.Run("heap.Interface compatibility", func(t *testing.T) {
		h := &candidateHeap{}
		heap.Init(h)

		// Use heap package functions
		heap.Push(h, &candidate{id: 1, distance: 0.5})
		heap.Push(h, &candidate{id: 2, distance: 0.3})
		heap.Push(h, &candidate{id: 3, distance: 0.7})

		assert.Equal(t, 3, h.Len())

		// Pop from max-heap should give largest distance first
		c1 := heap.Pop(h).(*candidate)
		assert.Equal(t, float32(0.7), c1.distance)

		c2 := heap.Pop(h).(*candidate)
		assert.Equal(t, float32(0.5), c2.distance)

		c3 := heap.Pop(h).(*candidate)
		assert.Equal(t, float32(0.3), c3.distance)

		assert.Equal(t, 0, h.Len())
	})
}

// TestUpdateMaxLayer covers the updateMaxLayer function that has 0% coverage
func TestUpdateMaxLayer(t *testing.T) {
	t.Run("update max layer after deletion", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Insert nodes at different layers
		// We need to insert enough nodes to ensure we get nodes at different layers
		for i := uint64(1); i <= 100; i++ {
			vector := []float32{float32(i), float32(i * 2)}
			err = idx.Insert(i, vector)
			require.NoError(t, err)
		}

		// Find the node with the highest layer
		var maxLayerNode *HNSWNode
		idx.nodes.Range(func(key, value interface{}) bool {
			node := value.(*HNSWNode)
			if maxLayerNode == nil || node.Layer > maxLayerNode.Layer {
				maxLayerNode = node
			}
			return true
		})

		require.NotNil(t, maxLayerNode)
		originalMaxLayer := int32(maxLayerNode.Layer)
		assert.Equal(t, originalMaxLayer, idx.maxLayer.Load())

		// Delete the node with the highest layer
		err = idx.Delete(maxLayerNode.ID)
		require.NoError(t, err)

		// updateMaxLayer should have been called
		// The new max layer should be <= original
		newMaxLayer := idx.maxLayer.Load()
		assert.LessOrEqual(t, newMaxLayer, originalMaxLayer)

		// Verify by checking all remaining nodes
		var actualMaxLayer int32 = -1
		idx.nodes.Range(func(key, value interface{}) bool {
			node := value.(*HNSWNode)
			if int32(node.Layer) > actualMaxLayer {
				actualMaxLayer = int32(node.Layer)
			}
			return true
		})

		assert.Equal(t, actualMaxLayer, newMaxLayer)
	})

	t.Run("max layer reset when all nodes deleted", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Insert and delete all nodes
		for i := uint64(1); i <= 5; i++ {
			vector := []float32{float32(i), float32(i * 2)}
			err = idx.Insert(i, vector)
			require.NoError(t, err)
		}

		for i := uint64(1); i <= 5; i++ {
			err = idx.Delete(i)
			require.NoError(t, err)
		}

		// Max layer should be -1 for empty index
		assert.Equal(t, int32(-1), idx.maxLayer.Load())
	})
}

// TestDistanceSIMDRemainder covers the remainder loops in SIMD functions
func TestDistanceSIMDRemainder(t *testing.T) {
	t.Run("L2SIMD with non-multiple of 4 dimensions", func(t *testing.T) {
		// Test with 5 dimensions (4 + 1 remainder)
		a := []float32{1, 2, 3, 4, 5}
		b := []float32{2, 3, 4, 5, 6}

		dist := L2SIMD(a, b)
		expected := L2(a, b)

		assert.InDelta(t, expected, dist, 1e-6)
	})

	t.Run("L2SIMD with 6 dimensions (4 + 2 remainder)", func(t *testing.T) {
		a := []float32{1, 2, 3, 4, 5, 6}
		b := []float32{2, 3, 4, 5, 6, 7}

		dist := L2SIMD(a, b)
		expected := L2(a, b)

		assert.InDelta(t, expected, dist, 1e-6)
	})

	t.Run("L2SIMD with 7 dimensions (4 + 3 remainder)", func(t *testing.T) {
		a := []float32{1, 2, 3, 4, 5, 6, 7}
		b := []float32{2, 3, 4, 5, 6, 7, 8}

		dist := L2SIMD(a, b)
		expected := L2(a, b)

		assert.InDelta(t, expected, dist, 1e-6)
	})

	t.Run("CosineSIMD with non-multiple of 4 dimensions", func(t *testing.T) {
		// Test with 5 dimensions (4 + 1 remainder)
		a := []float32{1, 2, 3, 4, 5}
		b := []float32{2, 3, 4, 5, 6}

		dist := CosineSIMD(a, b)
		expected := Cosine(a, b)

		assert.InDelta(t, expected, dist, 1e-6)
	})

	t.Run("CosineSIMD with 6 dimensions (4 + 2 remainder)", func(t *testing.T) {
		a := []float32{1, 2, 3, 4, 5, 6}
		b := []float32{2, 3, 4, 5, 6, 7}

		dist := CosineSIMD(a, b)
		expected := Cosine(a, b)

		assert.InDelta(t, expected, dist, 1e-6)
	})

	t.Run("CosineSIMD with 7 dimensions (4 + 3 remainder)", func(t *testing.T) {
		a := []float32{1, 2, 3, 4, 5, 6, 7}
		b := []float32{2, 3, 4, 5, 6, 7, 8}

		dist := CosineSIMD(a, b)
		expected := Cosine(a, b)

		assert.InDelta(t, expected, dist, 1e-6)
	})
}

// TestPersistenceErrors covers error paths in persistence functions
func TestPersistenceErrors(t *testing.T) {
	t.Run("Load with corrupt node data", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "hnsw-test-*")
		require.NoError(t, err)
		defer func() { _ = os.RemoveAll(tmpDir) }()

		db, err := pebble.Open(tmpDir, &pebble.Options{})
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		// Write corrupt data
		key := []byte(prefixNode + "00000000000000000001")
		corruptData := []byte{0x00, 0x01, 0x02} // Invalid gob data
		err = db.Set(key, corruptData, pebble.Sync)
		require.NoError(t, err)

		// Load should fail
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		err = idx.Load(db)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "deserialize")
	})

	t.Run("loadMetadata success path", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "hnsw-test-*")
		require.NoError(t, err)
		defer func() { _ = os.RemoveAll(tmpDir) }()

		db, err := pebble.Open(tmpDir, &pebble.Options{})
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		// Write metadata
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)
		err = idx.Insert(1, []float32{1, 2, 3})
		require.NoError(t, err)
		err = idx.Save(db)
		require.NoError(t, err)

		// Load should succeed with valid data
		idx2, err := NewHNSWIndex(nil)
		require.NoError(t, err)
		err = idx2.Load(db)
		assert.NoError(t, err)
		assert.Equal(t, int32(3), idx2.dimensions.Load())
	})
}

// TestSelectNeighborsHeuristicExtended covers the heuristic neighbor selection
func TestSelectNeighborsHeuristicExtended(t *testing.T) {
	t.Run("heuristic with diverse candidates", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Insert nodes to create a graph
		vectors := [][]float32{
			{0, 0},
			{1, 0},
			{2, 0},
			{3, 0},
			{4, 0},
			{0, 1},
			{0, 2},
		}

		for i, v := range vectors {
			err = idx.Insert(uint64(i+1), v)
			require.NoError(t, err)
		}

		// Create candidates
		query := []float32{0.5, 0}
		candidates := []*candidate{
			{id: 1, distance: 0.5},
			{id: 2, distance: 0.5},
			{id: 3, distance: 1.5},
			{id: 4, distance: 2.5},
			{id: 5, distance: 3.5},
		}

		// Select neighbors using heuristic
		selected := idx.selectNeighborsHeuristic(query, candidates, 3)

		// Should select diverse neighbors
		assert.LessOrEqual(t, len(selected), 3)
		assert.Greater(t, len(selected), 0)
	})

	t.Run("heuristic fills up to M when needed", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Insert nodes
		for i := uint64(1); i <= 10; i++ {
			vector := []float32{float32(i), 0}
			err = idx.Insert(i, vector)
			require.NoError(t, err)
		}

		// Create many candidates
		query := []float32{5, 0}
		candidates := make([]*candidate, 10)
		for i := 0; i < 10; i++ {
			candidates[i] = &candidate{
				id:       uint64(i + 1),
				distance: float32(i),
			}
		}

		// Request 5 neighbors
		selected := idx.selectNeighborsHeuristic(query, candidates, 5)

		// Should return exactly 5
		assert.Equal(t, 5, len(selected))
	})

	t.Run("heuristic with node lookup failures", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Insert a few nodes
		err = idx.Insert(1, []float32{1, 0})
		require.NoError(t, err)
		err = idx.Insert(2, []float32{2, 0})
		require.NoError(t, err)

		// Create candidates including non-existent nodes
		query := []float32{1.5, 0}
		candidates := []*candidate{
			{id: 1, distance: 0.5},
			{id: 999, distance: 0.6}, // Non-existent node
			{id: 2, distance: 0.5},
		}

		selected := idx.selectNeighborsHeuristic(query, candidates, 2)

		// Should handle missing nodes gracefully
		assert.LessOrEqual(t, len(selected), 2)
	})
}

// TestGetNeighborsEdgeCases covers getNeighbors error paths
func TestGetNeighborsEdgeCases(t *testing.T) {
	t.Run("getNeighbors with missing node", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		neighbors := idx.getNeighbors(999, 0)
		assert.Nil(t, neighbors)
	})

	t.Run("getNeighbors with missing connection", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Insert nodes
		err = idx.Insert(1, []float32{1, 0})
		require.NoError(t, err)
		err = idx.Insert(2, []float32{2, 0})
		require.NoError(t, err)

		// Manually corrupt connections to reference non-existent node
		node1, _ := idx.Get(1)
		node1.AddConnection(0, 999) // Non-existent node

		neighbors := idx.getNeighbors(1, 0)

		// Should skip the non-existent connection
		assert.NotNil(t, neighbors)
		// All returned neighbors should be valid
		for _, n := range neighbors {
			assert.NotEqual(t, uint64(999), n.id)
		}
	})
}

// TestSearchLayerEdgeCases covers searchLayer edge cases
func TestSearchLayerEdgeCases(t *testing.T) {
	t.Run("searchLayer with missing entry point node", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Insert and then manually create invalid state
		err = idx.Insert(1, []float32{1, 0})
		require.NoError(t, err)

		// Create entry points with invalid node
		entryPoints := []*candidate{
			{id: 999, distance: 0.5}, // Non-existent
		}

		query := []float32{1, 0}
		results := idx.searchLayer(query, entryPoints, 1, 0)

		// Should handle gracefully and return empty or valid results
		assert.NotNil(t, results)
	})

	t.Run("searchLayer with visited entry points", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		err = idx.Insert(1, []float32{1, 0})
		require.NoError(t, err)

		// Same node in entry points twice
		entryPoints := []*candidate{
			{id: 1, distance: 0.0},
			{id: 1, distance: 0.0}, // Duplicate
		}

		query := []float32{1, 0}
		results := idx.searchLayer(query, entryPoints, 1, 0)

		assert.NotNil(t, results)
		assert.Greater(t, len(results), 0)
	})
}

// TestSearchRadiusEdgeCases covers SearchRadius edge cases
func TestSearchRadiusEdgeCases(t *testing.T) {
	t.Run("SearchRadius with maxResults = 0", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		for i := uint64(1); i <= 10; i++ {
			err = idx.Insert(i, []float32{float32(i), 0})
			require.NoError(t, err)
		}

		query := []float32{5, 0}
		results, err := idx.SearchRadius(query, 10.0, 0)

		require.NoError(t, err)
		// Should return all within radius when maxResults = 0
		assert.Greater(t, len(results), 0)
	})

	t.Run("SearchRadius with maxResults > nodeCount", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		for i := uint64(1); i <= 5; i++ {
			err = idx.Insert(i, []float32{float32(i), 0})
			require.NoError(t, err)
		}

		query := []float32{3, 0}
		results, err := idx.SearchRadius(query, 10.0, 100)

		require.NoError(t, err)
		assert.LessOrEqual(t, len(results), 5)
	})
}

// TestCalculateRecallEdgeCases covers CalculateRecall edge cases
func TestCalculateRecallEdgeCases(t *testing.T) {
	t.Run("k larger than results", func(t *testing.T) {
		results := []*SearchResult{
			{ID: 1, Distance: 0.1},
		}
		groundTruth := []*SearchResult{
			{ID: 1, Distance: 0.1},
			{ID: 2, Distance: 0.2},
		}

		recall := CalculateRecall(results, groundTruth, 10)
		assert.Equal(t, 1.0, recall) // 1/1 match
	})

	t.Run("k larger than ground truth", func(t *testing.T) {
		results := []*SearchResult{
			{ID: 1, Distance: 0.1},
			{ID: 2, Distance: 0.2},
		}
		groundTruth := []*SearchResult{
			{ID: 1, Distance: 0.1},
		}

		recall := CalculateRecall(results, groundTruth, 10)
		assert.Equal(t, 1.0, recall) // 1/1 match
	})
}

// TestSnapshotRestore tests snapshot and restore functionality
func TestSnapshotRestore(t *testing.T) {
	t.Run("snapshot and restore complete index", func(t *testing.T) {
		idx1, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Insert data
		for i := uint64(1); i <= 10; i++ {
			vector := []float32{float32(i), float32(i * 2)}
			err = idx1.Insert(i, vector)
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

		// Verify restored index
		assert.Equal(t, idx1.Count(), idx2.Count())
		assert.Equal(t, idx1.dimensions.Load(), idx2.dimensions.Load())
		assert.Equal(t, idx1.maxLayer.Load(), idx2.maxLayer.Load())

		// Verify we can search the restored index
		query := []float32{5, 10}
		results, err := idx2.Search(query, 3)
		require.NoError(t, err)
		assert.Len(t, results, 3)
	})

	t.Run("restore with corrupt data", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Try to restore from corrupt data
		buf := bytes.NewBufferString("corrupt data")
		err = idx.Restore(buf)
		assert.Error(t, err)
	})

	t.Run("snapshot empty index", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		var buf bytes.Buffer
		err = idx.Snapshot(&buf)
		require.NoError(t, err)

		// Should be able to restore empty snapshot
		idx2, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		err = idx2.Restore(&buf)
		require.NoError(t, err)
		assert.Equal(t, int64(0), idx2.Count())
	})
}

// TestRestoreDimensionsSet ensures dimensions are set during restore
func TestRestoreDimensionsSet(t *testing.T) {
	idx1, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Insert vectors with specific dimensions
	err = idx1.Insert(1, []float32{1, 2, 3, 4})
	require.NoError(t, err)

	var buf bytes.Buffer
	err = idx1.Snapshot(&buf)
	require.NoError(t, err)

	// Create new index with no dimensions set
	idx2, err := NewHNSWIndex(nil)
	require.NoError(t, err)
	assert.Equal(t, int32(0), idx2.dimensions.Load())

	// Restore should set dimensions
	err = idx2.Restore(&buf)
	require.NoError(t, err)
	assert.Equal(t, int32(4), idx2.dimensions.Load())
}

// TestSearchWithKGreaterThanEf ensures ef is adjusted when k > ef
func TestSearchWithKGreaterThanEf(t *testing.T) {
	config := DefaultConfig()
	config.EfSearch = 10 // Small ef

	idx, err := NewHNSWIndex(config)
	require.NoError(t, err)

	// Insert vectors
	for i := uint64(1); i <= 50; i++ {
		vector := []float32{float32(i), float32(i * 2)}
		err = idx.Insert(i, vector)
		require.NoError(t, err)
	}

	// Search with k > ef
	query := []float32{25, 50}
	results, err := idx.Search(query, 20) // k=20 > ef=10
	require.NoError(t, err)

	// Should still return k results
	assert.Equal(t, 20, len(results))
}

// TestSearchWithZeroK ensures k=0 is handled
func TestSearchWithZeroK(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	err = idx.Insert(1, []float32{1, 2})
	require.NoError(t, err)

	query := []float32{1, 2}
	results, err := idx.Search(query, 0) // k=0
	require.NoError(t, err)

	// Should return at least 1 result (k is set to 1 internally)
	assert.GreaterOrEqual(t, len(results), 1)
}

// TestInsertErrorHandling ensures insert handles edge cases
func TestInsertErrorHandling(t *testing.T) {
	t.Run("insert when entry point lookup fails", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Insert first node
		err = idx.Insert(1, []float32{1, 2})
		require.NoError(t, err)

		// Manually corrupt the entry point
		idx.entryPoint.Store(999) // Non-existent node

		// Next insert should handle missing entry point
		err = idx.Insert(2, []float32{2, 3})
		// The insert function returns early if entry point is not found
		require.NoError(t, err)
	})
}

// TestByteConversionFunctions tests byte conversion helpers
func TestByteConversionFunctions(t *testing.T) {
	t.Run("uint64 conversion", func(t *testing.T) {
		original := uint64(12345678901234)
		bytes := uint64ToBytes(original)
		assert.Len(t, bytes, 8)

		converted := bytesToUint64(bytes)
		assert.Equal(t, original, converted)
	})

	t.Run("int32 conversion", func(t *testing.T) {
		original := int32(-123456)
		bytes := int32ToBytes(original)
		assert.Len(t, bytes, 4)

		converted := bytesToInt32(bytes)
		assert.Equal(t, original, converted)
	})

	t.Run("int conversion", func(t *testing.T) {
		original := 123456
		bytes := intToBytes(original)
		assert.Len(t, bytes, 4)

		converted := bytesToInt(bytes)
		assert.Equal(t, original, converted)
	})
}

// TestPruneConnectionsEdgeCases tests pruning logic
func TestPruneConnectionsEdgeCases(t *testing.T) {
	t.Run("prune with exactly maxConnections", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Insert enough nodes to test pruning
		for i := uint64(1); i <= 50; i++ {
			vector := []float32{float32(i), float32(i * 2)}
			err = idx.Insert(i, vector)
			require.NoError(t, err)
		}

		// Get a node and check its connections
		node, err := idx.Get(1)
		require.NoError(t, err)

		connCount := node.ConnectionCount(0)
		maxConn := idx.config.MMax

		// If already at or below max, pruning shouldn't change anything
		if connCount <= maxConn {
			originalConns := node.GetConnections(0)
			idx.pruneConnections(node, 0, maxConn)
			newConns := node.GetConnections(0)
			assert.Equal(t, len(originalConns), len(newConns))
		}
	})

	t.Run("prune with missing connection nodes", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		err = idx.Insert(1, []float32{1, 0})
		require.NoError(t, err)
		err = idx.Insert(2, []float32{2, 0})
		require.NoError(t, err)

		node, _ := idx.Get(1)

		// Add connection to non-existent node
		node.AddConnection(0, 999)

		// Pruning should handle missing nodes
		idx.pruneConnections(node, 0, 5)

		// Should complete without error
		conns := node.GetConnections(0)
		assert.NotNil(t, conns)
	})
}

// TestSearchBatchErrors tests SearchBatch error handling
func TestSearchBatchErrors(t *testing.T) {
	t.Run("batch search with invalid query", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		err = idx.Insert(1, []float32{1, 2, 3})
		require.NoError(t, err)

		// Include an invalid query
		queries := [][]float32{
			{1, 2},    // Wrong dimensions
			{1, 2, 3}, // Correct
		}

		_, err = idx.SearchBatch(queries, 1)
		assert.Error(t, err)
	})

	t.Run("batch search on empty index", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		queries := [][]float32{
			{1, 2, 3},
		}

		_, err = idx.SearchBatch(queries, 1)
		assert.Error(t, err)
	})
}

// TestPersistenceRoundTrip tests full save/load cycle
func TestPersistenceRoundTrip(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "hnsw-test-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	// Create and populate index
	idx1, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	for i := uint64(1); i <= 20; i++ {
		vector := []float32{float32(i), float32(i * 2), float32(i * 3)}
		err = idx1.Insert(i, vector)
		require.NoError(t, err)
	}

	// Save to database
	err = idx1.Save(db)
	require.NoError(t, err)

	// Load into new index
	idx2, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	err = idx2.Load(db)
	require.NoError(t, err)

	// Verify indices match
	assert.Equal(t, idx1.Count(), idx2.Count())
	assert.Equal(t, idx1.dimensions.Load(), idx2.dimensions.Load())
	assert.Equal(t, idx1.maxLayer.Load(), idx2.maxLayer.Load())
	assert.Equal(t, idx1.entryPoint.Load(), idx2.entryPoint.Load())

	// Verify search results match
	query := []float32{10, 20, 30}
	results1, err := idx1.Search(query, 5)
	require.NoError(t, err)

	results2, err := idx2.Search(query, 5)
	require.NoError(t, err)

	assert.Equal(t, len(results1), len(results2))
	for i := range results1 {
		assert.Equal(t, results1[i].ID, results2[i].ID)
		assert.InDelta(t, results1[i].Distance, results2[i].Distance, 1e-6)
	}
}

// TestLoadMetadataEmptyDatabase tests loading from empty database
func TestLoadMetadataEmptyDatabase(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "hnsw-test-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := pebble.Open(tmpDir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Load from empty database should not error
	err = idx.Load(db)
	require.NoError(t, err)

	// Index should remain empty
	assert.Equal(t, int64(0), idx.Count())
}

// TestWriteError tests io.Writer errors in Snapshot
func TestWriteError(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	err = idx.Insert(1, []float32{1, 2, 3})
	require.NoError(t, err)

	// Use error writer
	errWriter := &errorWriter{}
	err = idx.Snapshot(errWriter)
	assert.Error(t, err)
}

// errorWriter always returns an error
type errorWriter struct{}

func (e *errorWriter) Write(p []byte) (n int, err error) {
	return 0, io.ErrUnexpectedEOF
}

// TestSearchNotFoundEntryPoint tests when entry point is not found
func TestSearchNotFoundEntryPoint(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Insert a node to set dimensions
	err = idx.Insert(1, []float32{1, 2})
	require.NoError(t, err)

	// Corrupt entry point to invalid ID
	idx.entryPoint.Store(9999)

	query := []float32{1, 2}
	_, err = idx.Search(query, 1)
	assert.Error(t, err)
	assert.Equal(t, ErrNotFound, err)
}

// TestInsertUpdateMaxLayer tests the max layer update path during insert
func TestInsertUpdateMaxLayer(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Insert many nodes to get different layers
	for i := uint64(1); i <= 100; i++ {
		vector := []float32{float32(i), float32(i * 2)}
		err = idx.Insert(i, vector)
		require.NoError(t, err)
	}

	// Verify max layer is set correctly
	var actualMaxLayer int32 = -1
	idx.nodes.Range(func(key, value interface{}) bool {
		node := value.(*HNSWNode)
		if int32(node.Layer) > actualMaxLayer {
			actualMaxLayer = int32(node.Layer)
		}
		return true
	})

	assert.Equal(t, actualMaxLayer, idx.maxLayer.Load())
}
