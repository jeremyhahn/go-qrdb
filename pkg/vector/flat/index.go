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

// Package flat provides a brute-force vector index that computes distances
// to all stored vectors, guaranteeing 100% recall. It uses SIMD-accelerated
// batch distance computation from the hnsw package for performance.
package flat

import (
	"container/heap"
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
)

// SearchResult represents a single search result from the flat index.
type SearchResult struct {
	// ID is the unique identifier of the matched vector.
	ID uint64

	// Distance is the computed distance between the query and this result.
	Distance float32
}

// Index is a brute-force vector index that provides 100% recall
// by computing distances to all stored vectors. It uses SIMD-accelerated
// batch distance computation for performance on large datasets.
type Index struct {
	vectors    sync.Map
	count      atomic.Int64
	dimensions atomic.Int32
	distMetric hnsw.DistanceMetric
}

// NewIndex creates a new flat brute-force index with the given distance metric.
func NewIndex(distMetric hnsw.DistanceMetric) *Index {
	return &Index{
		distMetric: distMetric,
	}
}

// Insert adds a vector with the given ID to the index.
// The first inserted vector establishes the dimensionality for the index.
// Subsequent vectors must match that dimensionality.
// If a vector with the same ID already exists, it is overwritten (upsert).
func (idx *Index) Insert(id uint64, vector []float32) error {
	if len(vector) == 0 {
		return ErrInvalidVector
	}

	// Set dimensions on first insert, validate on subsequent inserts.
	dims := idx.dimensions.Load()
	if dims == 0 {
		// First vector sets the dimensions. Use CompareAndSwap to handle
		// concurrent first inserts.
		if idx.dimensions.CompareAndSwap(0, int32(len(vector))) {
			dims = int32(len(vector))
		} else {
			dims = idx.dimensions.Load()
		}
	}

	if int32(len(vector)) != dims {
		return &DimensionMismatchError{
			Got:      len(vector),
			Expected: int(dims),
		}
	}

	// Copy the vector to prevent external mutation.
	copied := make([]float32, len(vector))
	copy(copied, vector)

	// Upsert: if key already exists, overwrite without changing count.
	_, loaded := idx.vectors.Swap(id, copied)
	if !loaded {
		idx.count.Add(1)
	}

	return nil
}

// Delete removes a vector from the index by ID.
func (idx *Index) Delete(id uint64) error {
	_, loaded := idx.vectors.LoadAndDelete(id)
	if !loaded {
		return ErrNotFound
	}
	idx.count.Add(-1)
	return nil
}

// Search performs k-nearest neighbor search by computing distances to all
// stored vectors. Uses SIMD-accelerated batch distance computation from
// the hnsw package, then selects the top-k results using a max-heap.
// Returns results sorted by distance ascending (nearest first).
func (idx *Index) Search(query []float32, k int) ([]SearchResult, error) {
	if k <= 0 {
		return nil, ErrInvalidK
	}

	dims := idx.dimensions.Load()
	if dims == 0 {
		return nil, ErrEmptyIndex
	}

	if int32(len(query)) != dims {
		return nil, &DimensionMismatchError{
			Got:      len(query),
			Expected: int(dims),
		}
	}

	currentCount := idx.count.Load()
	if currentCount == 0 {
		return nil, ErrEmptyIndex
	}

	// Collect all vectors and their IDs for batch distance computation.
	ids := make([]uint64, 0, currentCount)
	vectors := make([][]float32, 0, currentCount)

	idx.vectors.Range(func(key, value any) bool {
		id, ok := key.(uint64)
		if !ok {
			return true
		}
		vec, ok := value.([]float32)
		if !ok {
			return true
		}
		ids = append(ids, id)
		vectors = append(vectors, vec)
		return true
	})

	if len(vectors) == 0 {
		return nil, ErrEmptyIndex
	}

	// Compute all distances using SIMD-accelerated batch computation.
	distances, err := hnsw.CalcDistanceBatch(idx.distMetric, query, vectors)
	if err != nil {
		return nil, err
	}

	// If k >= number of vectors, return all sorted by distance.
	if k >= len(distances) {
		return idx.sortAllResults(ids, distances), nil
	}

	// Use a max-heap to maintain top-k nearest results.
	h := &resultMaxHeap{}
	heap.Init(h)

	for i, dist := range distances {
		if h.Len() < k {
			heap.Push(h, &heapItem{id: ids[i], distance: dist})
		} else if dist < (*h)[0].distance {
			(*h)[0] = &heapItem{id: ids[i], distance: dist}
			heap.Fix(h, 0)
		}
	}

	// Extract results from heap and reverse to get ascending distance order.
	results := make([]SearchResult, h.Len())
	for i := h.Len() - 1; i >= 0; i-- {
		item := heap.Pop(h).(*heapItem)
		results[i] = SearchResult{
			ID:       item.id,
			Distance: item.distance,
		}
	}

	return results, nil
}

// sortAllResults returns all results sorted by distance ascending.
// Used when k >= total vector count.
func (idx *Index) sortAllResults(ids []uint64, distances []float32) []SearchResult {
	// Use insertion sort for small datasets, which is common when k >= n.
	results := make([]SearchResult, len(ids))
	for i := range ids {
		results[i] = SearchResult{ID: ids[i], Distance: distances[i]}
	}

	// Simple insertion sort - efficient for the typical small-n case.
	for i := 1; i < len(results); i++ {
		key := results[i]
		j := i - 1
		for j >= 0 && results[j].Distance > key.Distance {
			results[j+1] = results[j]
			j--
		}
		results[j+1] = key
	}

	return results
}

// Count returns the number of vectors currently in the index.
func (idx *Index) Count() int64 {
	return idx.count.Load()
}

// Dimensions returns the dimensionality of vectors in the index.
// Returns 0 if no vectors have been inserted.
func (idx *Index) Dimensions() int {
	return int(idx.dimensions.Load())
}

// Close releases index resources. After Close, the index should not be used.
func (idx *Index) Close() error {
	// Clear all vectors from the sync.Map.
	idx.vectors.Range(func(key, value any) bool {
		idx.vectors.Delete(key)
		return true
	})
	idx.count.Store(0)
	idx.dimensions.Store(0)
	return nil
}

// heapItem is an element in the max-heap used for top-k selection.
type heapItem struct {
	id       uint64
	distance float32
}

// resultMaxHeap is a max-heap of search results.
// The largest distance is at the root, so we can efficiently evict
// the worst result when a better candidate is found.
type resultMaxHeap []*heapItem

func (h resultMaxHeap) Len() int            { return len(h) }
func (h resultMaxHeap) Less(i, j int) bool  { return h[i].distance > h[j].distance }
func (h resultMaxHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *resultMaxHeap) Push(x interface{}) { *h = append(*h, x.(*heapItem)) }
func (h *resultMaxHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil // avoid memory leak
	*h = old[:n-1]
	return item
}
