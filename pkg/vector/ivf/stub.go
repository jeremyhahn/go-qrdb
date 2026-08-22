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

//go:build !qrdb_vector

// Package ivf provides an IVF (Inverted File) vector index. This stub
// version is compiled when vector support is disabled and returns errors
// for all operations.
package ivf

import (
	"errors"
	"fmt"
)

// Stub errors for when vector support is not compiled in.
var (
	ErrNotCompiled = errors.New("vector support not compiled: build with -tags qrdb_vector")
)

// Config holds IVF index configuration parameters.
type Config struct {
	// Clustering parameters
	NumCentroids  int     // Number of clusters (default: sqrt of dataset size)
	Dimensions    int     // Vector dimensionality
	MaxIterations int     // Maximum k-means iterations (default: 25)
	Convergence   float32 // Convergence threshold (default: 0.0001)
	InitMethod    string  // Initialization method: "kmeans++" or "random"

	// Search parameters
	DefaultNProbe int // Default number of clusters to search (default: 10)

	// Quantization parameters (optional)
	UseQuantization bool // Enable product quantization
	NumSubvectors   int  // Number of subvectors for PQ
	NumBits         int  // Bits per subvector code

	// Distance metric
	DistanceMetric DistanceMetric

	// Seed fixes the randomness used to INITIALISE the clusters. Zero means
	// seed from the clock, which is the production default.
	//
	// Set it only when clustering must be reproducible. Recall is a property
	// of the centroid set, and Cluster produces one set shared by every
	// subsequent query, so a test measuring recall against a threshold is
	// measuring one draw from a distribution unless this is fixed. Averaging
	// over queries does not help: an unlucky initialisation moves every
	// query's result together (go-qrdb#92).
	//
	// A fixed seed is wrong for production. Every node in a fleet clustering
	// the same data would make the same unlucky choice at the same time.
	Seed int64
}

// DistanceMetric represents the distance function type.
type DistanceMetric int

const (
	// L2Distance represents Euclidean distance.
	L2Distance DistanceMetric = iota
	// CosineDistance represents cosine distance.
	CosineDistance
	// DotProductDistance represents dot product distance.
	DotProductDistance
)

// DefaultConfig returns a default IVF configuration.
func DefaultConfig(dimensions int) *Config {
	return &Config{
		NumCentroids:    256,
		Dimensions:      dimensions,
		MaxIterations:   25,
		Convergence:     0.0001,
		InitMethod:      "kmeans++",
		DefaultNProbe:   10,
		UseQuantization: false,
		NumSubvectors:   8,
		NumBits:         8,
		DistanceMetric:  L2Distance,
	}
}

// Validate checks if the configuration is valid.
func (c *Config) Validate() error {
	return nil
}

// String returns a string representation of the configuration.
func (c *Config) String() string {
	return fmt.Sprintf("IVF(centroids=%d, dims=%d, nprobe=%d, metric=%s)",
		c.NumCentroids, c.Dimensions, c.DefaultNProbe, c.DistanceMetric)
}

func (m DistanceMetric) String() string {
	switch m {
	case L2Distance:
		return "L2"
	case CosineDistance:
		return "Cosine"
	case DotProductDistance:
		return "DotProduct"
	default:
		return "Unknown"
	}
}

// Index represents an IVF (Inverted File) index for fast approximate nearest neighbor search.
type Index interface {
	// Train trains the index on a set of vectors to learn cluster centroids
	Train(vectors [][]float32) error

	// Add adds a vector with ID and metadata to the index
	Add(id string, vector []float32, metadata map[string]interface{}) error

	// Search searches for k nearest neighbors, searching nprobe clusters
	Search(query []float32, k int, nprobe int) ([]Result, error)

	// Remove removes a vector from the index by ID
	Remove(id string) error

	// Save saves the index to disk
	Save(path string) error

	// Load loads the index from disk
	Load(path string) error

	// Size returns the number of vectors in the index
	Size() int

	// IsTrained returns whether the index has been trained
	IsTrained() bool
}

// Result represents a search result with ID, distance, and metadata.
type Result struct {
	ID       string                 `json:"id"`
	Distance float32                `json:"distance"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// NewIndex creates a new IVF index with the given configuration.
func NewIndex(config *Config) (Index, error) {
	return nil, ErrNotCompiled
}
