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
	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
)

// Compile-time assertion that HNSWIndex implements interfaces.HNSWIndex.
var _ interfaces.HNSWIndex = (*IndexAdapter)(nil)

// IndexAdapter wraps a concrete HNSWIndex to implement the interfaces.HNSWIndex interface.
// This adapter provides a clean abstraction layer between the vector service and
// the underlying HNSW implementation.
type IndexAdapter struct {
	index *HNSWIndex
}

// NewIndexAdapter creates a new IndexAdapter wrapping the given HNSWIndex.
func NewIndexAdapter(index *HNSWIndex) *IndexAdapter {
	return &IndexAdapter{index: index}
}

// Insert adds a new vector to the index with the given ID.
func (a *IndexAdapter) Insert(id uint64, vector []float32) error {
	return a.index.Insert(id, vector)
}

// Delete removes a vector from the index by ID.
func (a *IndexAdapter) Delete(id uint64) error {
	return a.index.Delete(id)
}

// Search performs k-nearest neighbor search for the given query vector.
func (a *IndexAdapter) Search(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
	results, err := a.index.Search(query, k)
	if err != nil {
		return nil, err
	}

	// Convert internal SearchResult to interface type
	converted := make([]*interfaces.HNSWSearchResult, len(results))
	for i, r := range results {
		converted[i] = &interfaces.HNSWSearchResult{
			ID:       r.ID,
			Distance: r.Distance,
		}
	}
	return converted, nil
}

// Count returns the number of vectors currently in the index.
func (a *IndexAdapter) Count() int64 {
	return a.index.Count()
}

// GetStats returns statistics about the index.
func (a *IndexAdapter) GetStats() *interfaces.HNSWStats {
	stats := a.index.GetStats()
	if stats == nil {
		return nil
	}
	return &interfaces.HNSWStats{
		NodeCount:      stats.NodeCount,
		MaxLayer:       stats.MaxLayer,
		AvgConnections: stats.AvgConnections,
		Dimensions:     stats.Dimensions,
	}
}

// Unwrap returns the underlying concrete HNSWIndex.
// This is useful when direct access to the concrete type is needed
// for operations not exposed through the interface.
func (a *IndexAdapter) Unwrap() *HNSWIndex {
	return a.index
}

// IndexFactory implements interfaces.HNSWIndexFactory for creating HNSW indexes.
type IndexFactory struct{}

// NewIndexFactory creates a new IndexFactory.
func NewIndexFactory() *IndexFactory {
	return &IndexFactory{}
}

// CreateIndex creates a new HNSW index with the specified configuration.
func (f *IndexFactory) CreateIndex(config *interfaces.HNSWConfig) (interfaces.HNSWIndex, error) {
	// Convert interface config to internal config
	hnswConfig := &Config{
		M:              config.M,
		MMax:           config.MMax,
		EfConstruction: config.EfConstruction,
		EfSearch:       config.EfSearch,
		DistanceMetric: parseDistanceMetric(config.DistanceMetric),
	}

	index, err := NewHNSWIndex(hnswConfig)
	if err != nil {
		return nil, err
	}

	return NewIndexAdapter(index), nil
}

// parseDistanceMetric converts a string distance metric to the internal type.
func parseDistanceMetric(metric string) DistanceMetric {
	switch metric {
	case "cosine", "COSINE":
		return CosineDistance
	case "dot_product", "DOT_PRODUCT":
		return DotProductDistance
	case "l2", "L2", "euclidean", "EUCLIDEAN":
		return L2Distance
	default:
		return L2Distance
	}
}
