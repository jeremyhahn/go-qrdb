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

package interfaces

// HNSWIndex defines the interface for a Hierarchical Navigable Small World index.
// This interface abstracts the HNSW index operations used by the vector service,
// allowing for dependency injection and easier testing.
type HNSWIndex interface {
	// Insert adds a new vector to the index with the given ID.
	// Returns an error if the ID already exists or the vector dimensions are invalid.
	Insert(id uint64, vector []float32) error

	// Delete removes a vector from the index by ID.
	// Returns an error if the ID does not exist.
	Delete(id uint64) error

	// Search performs k-nearest neighbor search for the given query vector.
	// Returns up to k results sorted by distance (ascending).
	Search(query []float32, k int) ([]*HNSWSearchResult, error)

	// Count returns the number of vectors currently in the index.
	Count() int64

	// GetStats returns statistics about the index such as node count,
	// maximum layer, and average connections.
	GetStats() *HNSWStats
}

// HNSWSearchResult represents a single search result from the HNSW index.
type HNSWSearchResult struct {
	// ID is the unique identifier of the matched vector.
	ID uint64

	// Distance is the computed distance between the query and this result.
	Distance float32
}

// HNSWStats contains statistics about an HNSW index.
type HNSWStats struct {
	// NodeCount is the total number of vectors in the index.
	NodeCount int64

	// MaxLayer is the highest layer in the HNSW graph.
	MaxLayer int

	// AvgConnections is the average number of connections per node.
	AvgConnections float64

	// Dimensions is the dimensionality of vectors in the index.
	Dimensions int
}

// HNSWIndexFactory creates new HNSW index instances.
// This interface enables dependency injection for index creation,
// supporting different implementations (real, mock, stub) based on context.
type HNSWIndexFactory interface {
	// CreateIndex creates a new HNSW index with the specified configuration.
	// The config parameter contains HNSW-specific parameters like M, EfConstruction, etc.
	CreateIndex(config *HNSWConfig) (HNSWIndex, error)
}

// HNSWConfig holds configuration parameters for creating an HNSW index.
// This is a simplified view of the configuration used by the factory interface.
type HNSWConfig struct {
	// M is the maximum number of bidirectional connections per layer.
	// Higher values improve recall but increase memory usage.
	// Typical range: 8-32, default: 16
	M int

	// MMax is the maximum number of connections for layer 0.
	// Usually set to M * 2.
	MMax int

	// EfConstruction is the size of the dynamic candidate list during construction.
	// Higher values improve recall but slow down index building.
	// Typical range: 100-400, default: 200
	EfConstruction int

	// EfSearch is the size of the dynamic candidate list during search.
	// Higher values improve recall but slow down search.
	// Typical range: 10-100, default: 50
	EfSearch int

	// DistanceMetric specifies the distance function to use.
	// Common values: "l2", "cosine", "dot_product"
	DistanceMetric string
}

// DefaultHNSWConfig returns the default HNSW configuration.
func DefaultHNSWConfig() *HNSWConfig {
	return &HNSWConfig{
		M:              16,
		MMax:           32,
		EfConstruction: 200,
		EfSearch:       50,
		DistanceMetric: "l2",
	}
}
