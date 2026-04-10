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
)

// searchLayer performs greedy search at a specific layer.
func (idx *HNSWIndex) searchLayer(query []float32, entryPoints []*candidate, ef int, layer int) []*candidate {
	// Visited set to avoid cycles
	visited := make(map[uint64]bool)

	// Candidate pool (min-heap by distance)
	candidates := &priorityQueue{}
	heap.Init(candidates)

	// Working set (max-heap by distance) - keeps the ef closest points
	working := &maxPriorityQueue{}
	heap.Init(working)

	// Initialize with entry points
	for _, ep := range entryPoints {
		if visited[ep.id] {
			continue
		}

		visited[ep.id] = true
		heap.Push(candidates, ep)
		heap.Push(working, ep)
	}

	// Greedy search
	for candidates.Len() > 0 {
		// Get closest candidate
		c, ok := heap.Pop(candidates).(*candidate)
		if !ok {
			continue
		}
		current := c

		// Check if we should continue
		if working.Len() > 0 && current.distance > (*working)[0].distance {
			break
		}

		// Get node
		node, ok := idx.nodes.Load(current.id)
		if !ok {
			continue
		}

		n, ok := node.(*HNSWNode)
		if !ok {
			continue
		}

		// Check all neighbors at this layer
		connections := n.GetConnections(layer)
		for _, neighborID := range connections {
			if visited[neighborID] {
				continue
			}

			visited[neighborID] = true

			// Get neighbor node
			neighborNode, ok := idx.nodes.Load(neighborID)
			if !ok {
				continue
			}

			neighbor, ok := neighborNode.(*HNSWNode)
			if !ok {
				continue
			}
			dist := idx.distanceFunc(query, neighbor.Vector)

			// Check if this neighbor is closer than the furthest in working set
			if working.Len() < ef || dist < (*working)[0].distance {
				c := &candidate{
					id:       neighborID,
					distance: dist,
				}

				heap.Push(candidates, c)
				heap.Push(working, c)

				// Remove furthest if we exceed ef
				if working.Len() > ef {
					heap.Pop(working)
				}
			}
		}
	}

	// Convert working set to sorted slice
	result := make([]*candidate, working.Len())
	for i := len(result) - 1; i >= 0; i-- {
		c, ok := heap.Pop(working).(*candidate)
		if !ok {
			continue
		}
		result[i] = c
	}

	return result
}

// SearchBatch performs batch k-NN search for multiple queries.
func (idx *HNSWIndex) SearchBatch(queries [][]float32, k int) ([][]*SearchResult, error) {
	results := make([][]*SearchResult, len(queries))
	errors := make([]error, len(queries))

	// Parallel search
	done := make(chan struct{})
	for i, query := range queries {
		go func(index int, q []float32) {
			results[index], errors[index] = idx.Search(q, k)
			done <- struct{}{}
		}(i, query)
	}

	// Wait for all searches to complete
	for range queries {
		<-done
	}

	// Check for errors
	for _, err := range errors {
		if err != nil {
			return nil, err
		}
	}

	return results, nil
}

// SearchRadius returns all points within a given radius of the query.
func (idx *HNSWIndex) SearchRadius(query []float32, radius float32, maxResults int) ([]*SearchResult, error) {
	if len(query) != int(idx.dimensions.Load()) {
		return nil, ErrInvalidVector
	}

	if idx.nodeCount.Load() == 0 {
		return nil, ErrEmptyIndex
	}

	// Use a large ef to ensure we find all points within radius
	oldEf := idx.config.EfSearch
	idx.config.EfSearch = 500
	defer func() { idx.config.EfSearch = oldEf }()

	// Perform regular search with large k
	k := maxResults
	if k <= 0 || k > int(idx.nodeCount.Load()) {
		k = int(idx.nodeCount.Load())
	}

	results, err := idx.Search(query, k)
	if err != nil {
		return nil, err
	}

	// Filter by radius
	filtered := make([]*SearchResult, 0, len(results))
	for _, r := range results {
		if r.Distance <= radius {
			filtered = append(filtered, r)
			if maxResults > 0 && len(filtered) >= maxResults {
				break
			}
		}
	}

	return filtered, nil
}

// Stats returns statistics about the index.
type Stats struct {
	NodeCount      int64
	MaxLayer       int
	AvgConnections float64
	Dimensions     int
}

// GetStats returns statistics about the index.
func (idx *HNSWIndex) GetStats() *Stats {
	stats := &Stats{
		NodeCount:  idx.nodeCount.Load(),
		MaxLayer:   int(idx.maxLayer.Load()),
		Dimensions: int(idx.dimensions.Load()),
	}

	if stats.NodeCount == 0 {
		return stats
	}

	// Calculate average connections
	totalConnections := 0
	idx.nodes.Range(func(key, value interface{}) bool {
		node, ok := value.(*HNSWNode)
		if !ok {
			return true
		}
		for layer := 0; layer <= node.Layer; layer++ {
			totalConnections += node.ConnectionCount(layer)
		}
		return true
	})

	stats.AvgConnections = float64(totalConnections) / float64(stats.NodeCount)

	return stats
}

// CalculateRecall calculates the recall@k for the index using a ground truth set.
func CalculateRecall(results, groundTruth []*SearchResult, k int) float64 {
	if len(groundTruth) == 0 {
		return 0.0
	}

	if k > len(results) {
		k = len(results)
	}
	if k > len(groundTruth) {
		k = len(groundTruth)
	}

	// Build set of ground truth IDs
	truthSet := make(map[uint64]bool)
	for i := 0; i < k && i < len(groundTruth); i++ {
		truthSet[groundTruth[i].ID] = true
	}

	// Count matches in results
	matches := 0
	for i := 0; i < k && i < len(results); i++ {
		if truthSet[results[i].ID] {
			matches++
		}
	}

	return float64(matches) / float64(k)
}
