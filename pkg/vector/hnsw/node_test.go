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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewNode(t *testing.T) {
	vector := []float32{1, 2, 3}
	node := NewNode(1, vector, 2)

	assert.Equal(t, uint64(1), node.ID)
	assert.Equal(t, vector, node.Vector)
	assert.Equal(t, 2, node.Layer)
	assert.NotNil(t, node.Connections)
	assert.Empty(t, node.Connections)
}

func TestHNSWNode_AddConnection(t *testing.T) {
	node := NewNode(1, []float32{1, 2, 3}, 2)

	// Add connection
	node.AddConnection(0, 2)
	connections := node.GetConnections(0)
	assert.Len(t, connections, 1)
	assert.Equal(t, uint64(2), connections[0])

	// Add another connection
	node.AddConnection(0, 3)
	connections = node.GetConnections(0)
	assert.Len(t, connections, 2)

	// Add duplicate connection (should be ignored)
	node.AddConnection(0, 2)
	connections = node.GetConnections(0)
	assert.Len(t, connections, 2)
}

func TestHNSWNode_GetConnections(t *testing.T) {
	node := NewNode(1, []float32{1, 2, 3}, 2)

	// Empty layer
	connections := node.GetConnections(0)
	assert.Empty(t, connections)

	// Add connections
	node.AddConnection(0, 2)
	node.AddConnection(0, 3)

	connections = node.GetConnections(0)
	assert.Len(t, connections, 2)

	// Modify returned slice should not affect node
	connections[0] = 999
	actualConnections := node.GetConnections(0)
	assert.NotEqual(t, uint64(999), actualConnections[0])
}

func TestHNSWNode_RemoveConnection(t *testing.T) {
	node := NewNode(1, []float32{1, 2, 3}, 2)

	// Add connections
	node.AddConnection(0, 2)
	node.AddConnection(0, 3)
	node.AddConnection(0, 4)

	// Remove middle connection
	node.RemoveConnection(0, 3)
	connections := node.GetConnections(0)
	assert.Len(t, connections, 2)
	assert.Contains(t, connections, uint64(2))
	assert.Contains(t, connections, uint64(4))
	assert.NotContains(t, connections, uint64(3))

	// Remove non-existent connection (should be no-op)
	node.RemoveConnection(0, 999)
	connections = node.GetConnections(0)
	assert.Len(t, connections, 2)
}

func TestHNSWNode_SetConnections(t *testing.T) {
	node := NewNode(1, []float32{1, 2, 3}, 2)

	// Set connections
	newConnections := []uint64{5, 6, 7}
	node.SetConnections(0, newConnections)

	connections := node.GetConnections(0)
	assert.Equal(t, newConnections, connections)

	// Modify source slice should not affect node
	newConnections[0] = 999
	actualConnections := node.GetConnections(0)
	assert.Equal(t, uint64(5), actualConnections[0])
}

func TestHNSWNode_ConnectionCount(t *testing.T) {
	node := NewNode(1, []float32{1, 2, 3}, 2)

	// Empty layer
	assert.Equal(t, 0, node.ConnectionCount(0))

	// Add connections
	node.AddConnection(0, 2)
	assert.Equal(t, 1, node.ConnectionCount(0))

	node.AddConnection(0, 3)
	assert.Equal(t, 2, node.ConnectionCount(0))

	// Different layer
	assert.Equal(t, 0, node.ConnectionCount(1))
	node.AddConnection(1, 4)
	assert.Equal(t, 1, node.ConnectionCount(1))
}

func TestHNSWNode_Copy(t *testing.T) {
	original := NewNode(1, []float32{1, 2, 3}, 2)
	original.AddConnection(0, 2)
	original.AddConnection(0, 3)
	original.AddConnection(1, 4)

	copy := original.Copy()

	// Verify copy
	assert.Equal(t, original.ID, copy.ID)
	assert.Equal(t, original.Vector, copy.Vector)
	assert.Equal(t, original.Layer, copy.Layer)

	// Verify connections
	for layer := 0; layer <= original.Layer; layer++ {
		assert.Equal(t, original.GetConnections(layer), copy.GetConnections(layer))
	}

	// Modify copy should not affect original
	copy.Vector[0] = 999
	assert.Equal(t, float32(1), original.Vector[0])

	copy.AddConnection(0, 999)
	assert.NotContains(t, original.GetConnections(0), uint64(999))
}

func TestHNSWNode_MultiLayer(t *testing.T) {
	node := NewNode(1, []float32{1, 2, 3}, 3)

	// Add connections to different layers
	node.AddConnection(0, 2)
	node.AddConnection(0, 3)
	node.AddConnection(1, 4)
	node.AddConnection(1, 5)
	node.AddConnection(2, 6)

	assert.Equal(t, 2, node.ConnectionCount(0))
	assert.Equal(t, 2, node.ConnectionCount(1))
	assert.Equal(t, 1, node.ConnectionCount(2))
	assert.Equal(t, 0, node.ConnectionCount(3))
}

func TestHNSWNode_ConcurrentAccess(t *testing.T) {
	node := NewNode(1, []float32{1, 2, 3}, 2)

	const numGoroutines = 10
	const operationsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines * 3)

	// Concurrent adds
	for g := 0; g < numGoroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < operationsPerGoroutine; i++ {
				node.AddConnection(0, uint64(id*1000+i))
			}
		}(g)
	}

	// Concurrent reads
	for g := 0; g < numGoroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < operationsPerGoroutine; i++ {
				_ = node.GetConnections(0)
			}
		}()
	}

	// Concurrent counts
	for g := 0; g < numGoroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < operationsPerGoroutine; i++ {
				_ = node.ConnectionCount(0)
			}
		}()
	}

	wg.Wait()

	// Verify no race conditions occurred
	connections := node.GetConnections(0)
	assert.NotEmpty(t, connections)
}

func TestHNSWNode_EdgeCases(t *testing.T) {
	t.Run("layer 0 node", func(t *testing.T) {
		node := NewNode(1, []float32{1}, 0)
		assert.Equal(t, 0, node.Layer)

		node.AddConnection(0, 2)
		assert.Equal(t, 1, node.ConnectionCount(0))
	})

	t.Run("high layer node", func(t *testing.T) {
		node := NewNode(1, []float32{1}, 10)
		assert.Equal(t, 10, node.Layer)

		// Can add connections to any layer <= node.Layer
		for layer := 0; layer <= 10; layer++ {
			node.AddConnection(layer, uint64(layer+100))
			assert.Equal(t, 1, node.ConnectionCount(layer))
		}
	})

	t.Run("empty vector", func(t *testing.T) {
		node := NewNode(1, []float32{}, 0)
		assert.Empty(t, node.Vector)
	})

	t.Run("single dimension vector", func(t *testing.T) {
		node := NewNode(1, []float32{42}, 0)
		assert.Len(t, node.Vector, 1)
		assert.Equal(t, float32(42), node.Vector[0])
	})
}

func TestHNSWNode_RemoveAllConnections(t *testing.T) {
	node := NewNode(1, []float32{1, 2, 3}, 2)

	// Add multiple connections
	node.AddConnection(0, 2)
	node.AddConnection(0, 3)
	node.AddConnection(0, 4)

	// Remove all
	node.RemoveConnection(0, 2)
	node.RemoveConnection(0, 3)
	node.RemoveConnection(0, 4)

	connections := node.GetConnections(0)
	assert.Empty(t, connections)
}

func TestHNSWNode_SetEmptyConnections(t *testing.T) {
	node := NewNode(1, []float32{1, 2, 3}, 2)

	// Add connections
	node.AddConnection(0, 2)
	node.AddConnection(0, 3)

	// Set to empty
	node.SetConnections(0, []uint64{})

	connections := node.GetConnections(0)
	assert.Empty(t, connections)
}

func BenchmarkNode_AddConnection(b *testing.B) {
	node := NewNode(1, []float32{1, 2, 3}, 2)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		node.AddConnection(0, uint64(i))
	}
}

func BenchmarkNode_GetConnections(b *testing.B) {
	node := NewNode(1, []float32{1, 2, 3}, 2)

	// Add some connections
	for i := 0; i < 32; i++ {
		node.AddConnection(0, uint64(i))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = node.GetConnections(0)
	}
}

func BenchmarkNode_RemoveConnection(b *testing.B) {
	b.StopTimer()

	for n := 0; n < b.N; n++ {
		node := NewNode(1, []float32{1, 2, 3}, 2)
		for i := 0; i < 100; i++ {
			node.AddConnection(0, uint64(i))
		}

		b.StartTimer()
		node.RemoveConnection(0, 50)
		b.StopTimer()
	}
}

func BenchmarkNode_Copy(b *testing.B) {
	node := NewNode(1, []float32{1, 2, 3, 4, 5, 6, 7, 8}, 3)

	// Add connections to multiple layers
	for layer := 0; layer <= 3; layer++ {
		for i := 0; i < 16; i++ {
			node.AddConnection(layer, uint64(layer*100+i))
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = node.Copy()
	}
}

func BenchmarkNode_ConcurrentRead(b *testing.B) {
	node := NewNode(1, []float32{1, 2, 3}, 2)

	for i := 0; i < 32; i++ {
		node.AddConnection(0, uint64(i))
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = node.GetConnections(0)
		}
	})
}
