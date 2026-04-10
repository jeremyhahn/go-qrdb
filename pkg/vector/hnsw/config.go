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

import "math"

// Config holds HNSW index configuration parameters.
type Config struct {
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

	// Ml is the normalization factor for level generation.
	// Default: 1.0 / ln(M)
	Ml float64

	// DistanceMetric specifies the distance function to use.
	DistanceMetric DistanceMetric
}

// DefaultConfig returns the default HNSW configuration.
func DefaultConfig() *Config {
	m := 16
	return &Config{
		M:              m,
		MMax:           m * 2,
		EfConstruction: 200,
		EfSearch:       50,
		Ml:             1.0 / math.Log(float64(m)),
		DistanceMetric: L2Distance,
	}
}

// Validate checks if the configuration is valid.
func (c *Config) Validate() error {
	if c.M <= 0 {
		return ErrInvalidConfig
	}
	if c.MMax <= 0 {
		c.MMax = c.M * 2
	}
	if c.EfConstruction <= 0 {
		return ErrInvalidConfig
	}
	if c.EfSearch <= 0 {
		return ErrInvalidConfig
	}
	if c.Ml <= 0 {
		c.Ml = 1.0 / math.Log(float64(c.M))
	}
	return nil
}
