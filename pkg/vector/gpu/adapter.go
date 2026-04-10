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

package gpu

import "github.com/jeremyhahn/go-qrdb/pkg/interfaces"

// GPUIndex is the internal interface satisfied by all GPU index implementations.
// It operates on GPU-native SearchResult types and is adapted to the public
// interfaces.HNSWIndex by GPUIndexAdapter.
type GPUIndex interface {
	// Insert adds a vector with the given ID to the index.
	Insert(id uint64, vector []float32) error

	// Delete removes a vector by ID from the index.
	Delete(id uint64) error

	// Search performs k-nearest neighbor search for the query vector.
	Search(query []float32, k int) ([]SearchResult, error)

	// Count returns the number of indexed vectors.
	Count() int64

	// Close releases GPU resources held by the index.
	Close() error
}

// GPUIndexAdapter bridges a GPUIndex to the interfaces.HNSWIndex contract,
// translating GPU-native SearchResult values to HNSWSearchResult pointers.
type GPUIndexAdapter struct {
	index     GPUIndex
	dimension int
}

// NewGPUIndexAdapter wraps a GPUIndex with the interfaces.HNSWIndex interface.
// dimension is the vector dimensionality, reported in GetStats.
func NewGPUIndexAdapter(index GPUIndex, dimension int) *GPUIndexAdapter {
	return &GPUIndexAdapter{
		index:     index,
		dimension: dimension,
	}
}

// Insert adds a vector to the underlying GPU index.
func (a *GPUIndexAdapter) Insert(id uint64, vector []float32) error {
	return a.index.Insert(id, vector)
}

// Delete removes a vector by ID from the underlying GPU index.
func (a *GPUIndexAdapter) Delete(id uint64) error {
	return a.index.Delete(id)
}

// Search performs k-nearest neighbor search and returns results as
// []*interfaces.HNSWSearchResult, converting from GPU-native SearchResult values.
func (a *GPUIndexAdapter) Search(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
	gpuResults, err := a.index.Search(query, k)
	if err != nil {
		return nil, err
	}
	results := make([]*interfaces.HNSWSearchResult, len(gpuResults))
	for i, r := range gpuResults {
		results[i] = &interfaces.HNSWSearchResult{
			ID:       r.ID,
			Distance: r.Distance,
		}
	}
	return results, nil
}

// Count returns the number of vectors currently in the GPU index.
func (a *GPUIndexAdapter) Count() int64 {
	return a.index.Count()
}

// GetStats returns index statistics derived from the underlying GPU index.
// GPU indexes do not maintain HNSW graph metadata (MaxLayer, AvgConnections),
// so those fields are always zero.
func (a *GPUIndexAdapter) GetStats() *interfaces.HNSWStats {
	return &interfaces.HNSWStats{
		NodeCount:      a.index.Count(),
		MaxLayer:       0,
		AvgConnections: 0,
		Dimensions:     a.dimension,
	}
}
