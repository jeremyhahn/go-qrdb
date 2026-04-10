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
	"container/heap"
	"sort"
)

// insert adds a node to the HNSW graph.
func (idx *HNSWIndex) insert(node *HNSWNode) {
	// Get entry point
	entryPointID := idx.entryPoint.Load()
	entryPointNode, ok := idx.nodes.Load(entryPointID)
	if !ok {
		return
	}

	ep, ok := entryPointNode.(*HNSWNode)
	if !ok {
		return
	}
	topLayer := int(idx.maxLayer.Load())

	// Find nearest neighbors from top to target layer
	nearest := []*candidate{
		{id: ep.ID, distance: idx.distanceFunc(node.Vector, ep.Vector)},
	}

	// Search from top layer down to node.Layer + 1
	for layer := topLayer; layer > node.Layer; layer-- {
		nearest = idx.searchLayer(node.Vector, nearest, 1, layer)
	}

	// Insert node from node.Layer down to 0
	for layer := node.Layer; layer >= 0; layer-- {
		// Find M nearest neighbors
		m := idx.config.M
		if layer == 0 {
			m = idx.config.MMax
		}

		candidates := idx.searchLayer(node.Vector, nearest, idx.config.EfConstruction, layer)

		// Select M neighbors - use simple selection for better connectivity
		neighbors := idx.selectNeighborsSimple(candidates, m)

		// Add bidirectional connections
		for _, neighbor := range neighbors {
			node.AddConnection(layer, neighbor.id)

			// Add reverse connection
			neighborNode, ok := idx.nodes.Load(neighbor.id)
			if !ok {
				continue
			}

			n, ok := neighborNode.(*HNSWNode)
			if !ok {
				continue
			}
			n.AddConnection(layer, node.ID)

			// Prune neighbors if necessary
			// Use 2x the limit to allow for temporary over-connection
			maxConnections := idx.config.M * 2
			if layer == 0 {
				maxConnections = idx.config.MMax * 2
			}

			if n.ConnectionCount(layer) > maxConnections {
				// Prune back to the configured maximum
				pruneLimit := idx.config.M
				if layer == 0 {
					pruneLimit = idx.config.MMax
				}
				idx.pruneConnections(n, layer, pruneLimit)
			}
		}

		nearest = candidates
	}
}

// selectNeighborsSimple selects the M nearest neighbors by distance.
// This is simpler than the heuristic and ensures better connectivity.
func (idx *HNSWIndex) selectNeighborsSimple(candidates []*candidate, m int) []*candidate {
	if len(candidates) <= m {
		return candidates
	}

	// Sort candidates by distance
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].distance < candidates[j].distance
	})

	return candidates[:m]
}

// selectNeighborsHeuristic selects neighbors using a heuristic to maintain graph quality.
// This implements the algorithm from the HNSW paper.
func (idx *HNSWIndex) selectNeighborsHeuristic(query []float32, candidates []*candidate, m int) []*candidate {
	if len(candidates) <= m {
		return candidates
	}

	// Sort candidates by distance
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].distance < candidates[j].distance
	})

	result := make([]*candidate, 0, m)
	resultSet := make(map[uint64]bool)

	for _, c := range candidates {
		if len(result) >= m {
			break
		}

		// For better recall, be less aggressive with the heuristic
		// Only reject if the candidate is significantly farther from query
		// than from existing results
		good := true
		if len(result) > 0 {
			for _, r := range result {
				rNode, ok := idx.nodes.Load(r.id)
				if !ok {
					continue
				}

				cNode, ok := idx.nodes.Load(c.id)
				if !ok {
					continue
				}

				cN, cOK := cNode.(*HNSWNode)
				rN, rOK := rNode.(*HNSWNode)
				if !cOK || !rOK {
					continue
				}
				distToResult := idx.distanceFunc(cN.Vector, rN.Vector)
				// Only reject if distance to result is much smaller than distance to query
				// This ensures we don't reject good neighbors
				if distToResult < c.distance*0.8 {
					good = false
					break
				}
			}
		}

		if good && !resultSet[c.id] {
			result = append(result, c)
			resultSet[c.id] = true
		}
	}

	// If we don't have enough neighbors, add the closest ones
	if len(result) < m {
		for _, c := range candidates {
			if len(result) >= m {
				break
			}
			if !resultSet[c.id] {
				result = append(result, c)
				resultSet[c.id] = true
			}
		}
	}

	return result
}

// pruneConnections removes the worst connections to maintain the maximum number.
func (idx *HNSWIndex) pruneConnections(node *HNSWNode, layer, maxConnections int) {
	connections := node.GetConnections(layer)
	if len(connections) <= maxConnections {
		return
	}

	// Build candidates from connections
	candidates := make([]*candidate, 0, len(connections))
	for _, connID := range connections {
		connNode, ok := idx.nodes.Load(connID)
		if !ok {
			continue
		}

		cn, ok := connNode.(*HNSWNode)
		if !ok {
			continue
		}
		dist := idx.distanceFunc(node.Vector, cn.Vector)
		candidates = append(candidates, &candidate{
			id:       connID,
			distance: dist,
		})
	}

	// Select best neighbors using simple selection for pruning
	selected := idx.selectNeighborsSimple(candidates, maxConnections)

	// Update connections
	newConnections := make([]uint64, len(selected))
	for i, c := range selected {
		newConnections[i] = c.id
	}
	node.SetConnections(layer, newConnections)
}

// getNeighbors returns all neighbors of a node at a specific layer.
func (idx *HNSWIndex) getNeighbors(nodeID uint64, layer int) []*candidate {
	node, ok := idx.nodes.Load(nodeID)
	if !ok {
		return nil
	}

	n, ok := node.(*HNSWNode)
	if !ok {
		return nil
	}
	connections := n.GetConnections(layer)

	neighbors := make([]*candidate, 0, len(connections))
	for _, connID := range connections {
		connNode, ok := idx.nodes.Load(connID)
		if !ok {
			continue
		}

		cn, ok := connNode.(*HNSWNode)
		if !ok {
			continue
		}
		dist := idx.distanceFunc(n.Vector, cn.Vector)
		neighbors = append(neighbors, &candidate{
			id:       connID,
			distance: dist,
		})
	}

	return neighbors
}

// priorityQueue implements a min-heap for candidates.
type priorityQueue []*candidate

func (pq priorityQueue) Len() int { return len(pq) }

func (pq priorityQueue) Less(i, j int) bool {
	return pq[i].distance < pq[j].distance
}

func (pq priorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
}

func (pq *priorityQueue) Push(x interface{}) {
	if item, ok := x.(*candidate); ok {
		*pq = append(*pq, item)
	}
}

func (pq *priorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	*pq = old[0 : n-1]
	return item
}

// maxPriorityQueue implements a max-heap for candidates.
type maxPriorityQueue []*candidate

func (pq maxPriorityQueue) Len() int { return len(pq) }

func (pq maxPriorityQueue) Less(i, j int) bool {
	return pq[i].distance > pq[j].distance
}

func (pq maxPriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
}

func (pq *maxPriorityQueue) Push(x interface{}) {
	if item, ok := x.(*candidate); ok {
		*pq = append(*pq, item)
	}
}

func (pq *maxPriorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	*pq = old[0 : n-1]
	return item
}

// buildCandidateHeap creates a max-heap from a slice of candidates.
func buildCandidateHeap(candidates []*candidate, maxSize int) *maxPriorityQueue {
	h := &maxPriorityQueue{}
	heap.Init(h)

	for _, c := range candidates {
		if h.Len() < maxSize {
			heap.Push(h, c)
		} else if c.distance < (*h)[0].distance {
			heap.Pop(h)
			heap.Push(h, c)
		}
	}

	return h
}
