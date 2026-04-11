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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHNSWIndex_insert(t *testing.T) {
	t.Run("insert creates connections", func(t *testing.T) {
		idx, err := NewHNSWIndex(nil)
		require.NoError(t, err)

		// Insert first node
		vector1 := []float32{1, 0, 0}
		err = idx.Insert(1, vector1)
		require.NoError(t, err)

		// Insert second node - should connect to first
		vector2 := []float32{0.9, 0.1, 0}
		node2 := NewNode(2, vector2, 0)
		idx.insert(node2)
		idx.nodes.Store(2, node2)

		// Verify connections
		connections := node2.GetConnections(0)
		assert.NotEmpty(t, connections)
	})

	t.Run("insert maintains max connections", func(t *testing.T) {
		config := &Config{
			M:              2, // Small M for testing
			MMax:           4,
			EfConstruction: 10,
			EfSearch:       10,
			Ml:             1.0,
			DistanceMetric: L2Distance,
		}

		idx, err := NewHNSWIndex(config)
		require.NoError(t, err)

		// Insert many nodes
		for i := uint64(1); i <= 20; i++ {
			vector := []float32{float32(i) / 20.0, 0, 0}
			err = idx.Insert(i, vector)
			require.NoError(t, err)
		}

		// Check that nodes don't exceed 2x max connections (our pruning threshold)
		idx.nodes.Range(func(key, value interface{}) bool {
			node := value.(*HNSWNode)
			for layer := 0; layer <= node.Layer; layer++ {
				// We allow 2x connections before pruning
				maxConns := config.M * 2
				if layer == 0 {
					maxConns = config.MMax * 2
				}
				assert.LessOrEqual(t, node.ConnectionCount(layer), maxConns,
					"Node %d layer %d has too many connections", node.ID, layer)
			}
			return true
		})
	})
}

func TestSelectNeighborsHeuristic(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Insert some nodes
	vectors := [][]float32{
		{1, 0, 0},
		{0.9, 0, 0},
		{0.8, 0, 0},
		{0.7, 0, 0},
		{0, 1, 0},
	}

	for i, v := range vectors {
		err = idx.Insert(uint64(i+1), v)
		require.NoError(t, err)
	}

	// Create candidates
	query := []float32{0.95, 0, 0}
	candidates := make([]*candidate, len(vectors))
	for i, v := range vectors {
		dist := idx.distanceFunc(query, v)
		candidates[i] = &candidate{
			id:       uint64(i + 1),
			distance: dist,
		}
	}

	// Select 3 neighbors
	selected := idx.selectNeighborsHeuristic(query, candidates, 3)

	assert.Len(t, selected, 3)
	// Should select closest ones
	assert.Equal(t, uint64(1), selected[0].id)
}

func TestPruneConnections(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Insert enough nodes to test pruning
	numNodes := 100
	for i := 1; i <= numNodes; i++ {
		vector := []float32{float32(i), 0, 0}
		err = idx.Insert(uint64(i), vector)
		require.NoError(t, err)
	}

	// Get a node with connections
	node, err := idx.Get(50)
	require.NoError(t, err)

	// Force add many connections to exceed the 2x threshold
	pruneThreshold := idx.config.MMax * 2
	for i := 1; i <= numNodes; i++ {
		if uint64(i) != node.ID {
			node.AddConnection(0, uint64(i))
		}
	}

	initialCount := node.ConnectionCount(0)
	assert.Greater(t, initialCount, pruneThreshold,
		"Need more than %d connections to test pruning, got %d", pruneThreshold, initialCount)

	// Prune connections
	idx.pruneConnections(node, 0, idx.config.MMax)

	// Verify pruned to MMax
	finalCount := node.ConnectionCount(0)
	assert.LessOrEqual(t, finalCount, idx.config.MMax,
		"After pruning, should have at most MMax=%d connections, got %d", idx.config.MMax, finalCount)
}

func TestGetNeighbors(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Insert nodes
	for i := uint64(1); i <= 5; i++ {
		vector := []float32{float32(i), 0, 0}
		err = idx.Insert(i, vector)
		require.NoError(t, err)
	}

	// Get neighbors
	neighbors := idx.getNeighbors(3, 0)
	assert.NotNil(t, neighbors)
}

func TestPriorityQueue(t *testing.T) {
	t.Run("priority queue min heap", func(t *testing.T) {
		pq := &priorityQueue{
			{id: 1, distance: 0.5},
			{id: 2, distance: 0.1},
			{id: 3, distance: 0.9},
		}

		// Verify it works like a min heap
		assert.Equal(t, 3, pq.Len())
		assert.True(t, pq.Less(1, 0))  // 0.1 < 0.5
		assert.False(t, pq.Less(0, 1)) // 0.5 > 0.1
	})

	t.Run("max priority queue", func(t *testing.T) {
		pq := &maxPriorityQueue{
			{id: 1, distance: 0.5},
			{id: 2, distance: 0.1},
			{id: 3, distance: 0.9},
		}

		// Verify it works like a max heap
		assert.Equal(t, 3, pq.Len())
		assert.False(t, pq.Less(1, 0)) // 0.1 < 0.5 but reversed
		assert.True(t, pq.Less(0, 1))  // 0.5 > 0.1 but reversed
	})
}

func TestBuildCandidateHeap(t *testing.T) {
	candidates := []*candidate{
		{id: 1, distance: 0.5},
		{id: 2, distance: 0.1},
		{id: 3, distance: 0.9},
		{id: 4, distance: 0.3},
		{id: 5, distance: 0.7},
	}

	heap := buildCandidateHeap(candidates, 3)

	assert.Equal(t, 3, heap.Len())

	// Should contain the 3 closest (smallest distance)
	distances := make(map[float32]bool)
	for _, c := range *heap {
		distances[c.distance] = true
	}

	assert.True(t, distances[0.1])
	assert.True(t, distances[0.3])
	assert.True(t, distances[0.5])
	assert.False(t, distances[0.9])
}

func TestLayerSelection(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Test multiple selections
	layerCounts := make(map[int]int)
	for i := 0; i < 1000; i++ {
		layer := idx.selectLayer()
		layerCounts[layer]++
		assert.GreaterOrEqual(t, layer, 0)
	}

	// Verify exponential decay distribution
	// Layer 0 should be most common
	for i := 0; i < 5; i++ {
		if layerCounts[i+1] == 0 {
			break
		}
		assert.Greater(t, layerCounts[i], layerCounts[i+1],
			"Layer %d should have more nodes than layer %d", i, i+1)
	}
}

func TestBidirectionalConnections(t *testing.T) {
	idx, err := NewHNSWIndex(nil)
	require.NoError(t, err)

	// Insert multiple nodes
	for i := uint64(1); i <= 5; i++ {
		vector := []float32{float32(i), 0, 0}
		err = idx.Insert(i, vector)
		require.NoError(t, err)
	}

	// Check bidirectional connections
	idx.nodes.Range(func(key, value interface{}) bool {
		node := value.(*HNSWNode)
		for layer := 0; layer <= node.Layer; layer++ {
			connections := node.GetConnections(layer)
			for _, neighborID := range connections {
				// Get neighbor
				neighborNode, ok := idx.nodes.Load(neighborID)
				if !ok {
					t.Errorf("Neighbor %d not found", neighborID)
					return false
				}

				neighbor := neighborNode.(*HNSWNode)
				neighborConns := neighbor.GetConnections(layer)

				// Check reverse connection exists
				found := false
				for _, id := range neighborConns {
					if id == node.ID {
						found = true
						break
					}
				}

				assert.True(t, found,
					"Bidirectional connection missing: %d -> %d at layer %d",
					node.ID, neighborID, layer)
			}
		}
		return true
	})
}

func BenchmarkInsert_Sequential(b *testing.B) {
	idx, _ := NewHNSWIndex(nil)
	vector := []float32{1, 2, 3, 4, 5, 6, 7, 8}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = idx.Insert(uint64(i+1), vector)
	}
}

func BenchmarkSelectNeighborsHeuristic(b *testing.B) {
	idx, _ := NewHNSWIndex(nil)

	// Create candidates
	candidates := make([]*candidate, 100)
	for i := 0; i < 100; i++ {
		candidates[i] = &candidate{
			id:       uint64(i + 1),
			distance: float32(i),
		}
	}

	query := []float32{1, 2, 3}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.selectNeighborsHeuristic(query, candidates, 16)
	}
}

func BenchmarkPruneConnections(b *testing.B) {
	idx, _ := NewHNSWIndex(nil)

	// Insert nodes
	for i := uint64(1); i <= 100; i++ {
		vector := []float32{float32(i), 0, 0}
		_ = idx.Insert(i, vector)
	}

	node, _ := idx.Get(50)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.pruneConnections(node, 0, idx.config.MMax)
	}
}
