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
	"sync"
)

// HNSWNode represents a node in the HNSW graph.
type HNSWNode struct {
	// ID is the unique identifier for this node.
	ID uint64

	// Vector is the feature vector for this node.
	Vector []float32

	// Layer is the highest layer this node appears in.
	Layer int

	// Connections maps layer number to a list of neighbor node IDs.
	// Layer 0 is the base layer containing all nodes.
	Connections map[int][]uint64

	mu sync.RWMutex
}

// NewNode creates a new HNSW node.
func NewNode(id uint64, vector []float32, layer int) *HNSWNode {
	return &HNSWNode{
		ID:          id,
		Vector:      vector,
		Layer:       layer,
		Connections: make(map[int][]uint64),
	}
}

// AddConnection adds a bidirectional connection between this node and another.
func (n *HNSWNode) AddConnection(layer int, targetID uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()

	// Check if connection already exists
	for _, id := range n.Connections[layer] {
		if id == targetID {
			return
		}
	}

	n.Connections[layer] = append(n.Connections[layer], targetID)
}

// GetConnections returns the connections for a specific layer.
func (n *HNSWNode) GetConnections(layer int) []uint64 {
	n.mu.RLock()
	defer n.mu.RUnlock()

	connections := n.Connections[layer]
	result := make([]uint64, len(connections))
	copy(result, connections)
	return result
}

// RemoveConnection removes a connection at a specific layer.
func (n *HNSWNode) RemoveConnection(layer int, targetID uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()

	connections := n.Connections[layer]
	for i, id := range connections {
		if id == targetID {
			n.Connections[layer] = append(connections[:i], connections[i+1:]...)
			return
		}
	}
}

// SetConnections replaces all connections at a specific layer.
func (n *HNSWNode) SetConnections(layer int, connections []uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.Connections[layer] = make([]uint64, len(connections))
	copy(n.Connections[layer], connections)
}

// ConnectionCount returns the number of connections at a specific layer.
func (n *HNSWNode) ConnectionCount(layer int) int {
	n.mu.RLock()
	defer n.mu.RUnlock()

	return len(n.Connections[layer])
}

// Copy creates a deep copy of the node.
func (n *HNSWNode) Copy() *HNSWNode {
	n.mu.RLock()
	defer n.mu.RUnlock()

	vectorCopy := make([]float32, len(n.Vector))
	copy(vectorCopy, n.Vector)

	connCopy := make(map[int][]uint64)
	for layer, conns := range n.Connections {
		connCopy[layer] = make([]uint64, len(conns))
		copy(connCopy[layer], conns)
	}

	return &HNSWNode{
		ID:          n.ID,
		Vector:      vectorCopy,
		Layer:       n.Layer,
		Connections: connCopy,
	}
}
