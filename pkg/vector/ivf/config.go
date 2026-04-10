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

package ivf

import (
	"fmt"
	"math"

	"github.com/jeremyhahn/go-qrdb/pkg/api"
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
		NumCentroids:    256, // Good default for medium datasets
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

// AutoConfig automatically determines optimal configuration based on dataset size.
func AutoConfig(dimensions, datasetSize int) *Config {
	// Use sqrt(dataset size) as a heuristic for number of centroids
	numCentroids := int(math.Sqrt(float64(datasetSize)))
	if numCentroids < 16 {
		numCentroids = 16
	}
	if numCentroids > 4096 {
		numCentroids = 4096
	}

	// Adjust nprobe based on centroids (search ~5% of clusters)
	nprobe := numCentroids / 20
	if nprobe < 1 {
		nprobe = 1
	}
	if nprobe > 100 {
		nprobe = 100
	}

	return &Config{
		NumCentroids:    numCentroids,
		Dimensions:      dimensions,
		MaxIterations:   25,
		Convergence:     0.0001,
		InitMethod:      "kmeans++",
		DefaultNProbe:   nprobe,
		UseQuantization: datasetSize > 100000, // Enable PQ for large datasets
		NumSubvectors:   8,
		NumBits:         8,
		DistanceMetric:  L2Distance,
	}
}

// Validate checks if the configuration is valid.
func (c *Config) Validate() error {
	if c.NumCentroids <= 0 {
		return api.NewIVFConfigValidationError("ivf.Config.Validate",
			fmt.Sprintf("num_centroids must be positive, got %d", c.NumCentroids))
	}
	if c.Dimensions <= 0 {
		return api.NewIVFConfigValidationError("ivf.Config.Validate",
			fmt.Sprintf("dimensions must be positive, got %d", c.Dimensions))
	}
	if c.MaxIterations <= 0 {
		return api.NewIVFConfigValidationError("ivf.Config.Validate",
			fmt.Sprintf("max_iterations must be positive, got %d", c.MaxIterations))
	}
	if c.Convergence <= 0 {
		return api.NewIVFConfigValidationError("ivf.Config.Validate",
			fmt.Sprintf("convergence must be positive, got %f", c.Convergence))
	}
	if c.InitMethod != "kmeans++" && c.InitMethod != "random" {
		return api.NewIVFConfigValidationError("ivf.Config.Validate",
			fmt.Sprintf("init_method must be 'kmeans++' or 'random', got '%s'", c.InitMethod))
	}
	if c.DefaultNProbe <= 0 {
		return api.NewIVFConfigValidationError("ivf.Config.Validate",
			fmt.Sprintf("default_nprobe must be positive, got %d", c.DefaultNProbe))
	}
	if c.DefaultNProbe > c.NumCentroids {
		return api.NewIVFConfigValidationError("ivf.Config.Validate",
			fmt.Sprintf("default_nprobe (%d) cannot exceed num_centroids (%d)", c.DefaultNProbe, c.NumCentroids))
	}

	// Validate quantization parameters if enabled
	if c.UseQuantization {
		if c.NumSubvectors <= 0 {
			return api.NewIVFConfigValidationError("ivf.Config.Validate",
				"num_subvectors must be positive when quantization is enabled")
		}
		if c.Dimensions%c.NumSubvectors != 0 {
			return api.NewIVFConfigValidationError("ivf.Config.Validate",
				fmt.Sprintf("dimensions (%d) must be divisible by num_subvectors (%d)", c.Dimensions, c.NumSubvectors))
		}
		if c.NumBits <= 0 || c.NumBits > 16 {
			return api.NewIVFConfigValidationError("ivf.Config.Validate",
				fmt.Sprintf("num_bits must be between 1 and 16, got %d", c.NumBits))
		}
	}

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
