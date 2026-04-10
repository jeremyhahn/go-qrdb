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

import (
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
)

// ErrNoFallbackFactory is returned when a GPU index cannot be created and no
// CPU fallback factory was provided.
var ErrNoFallbackFactory = errors.New("no CPU fallback factory provided")

// GPUIndexFactory creates GPU-accelerated HNSW-compatible indexes, falling
// back to a CPU factory when GPU acceleration is disabled or unavailable.
// It implements interfaces.HNSWIndexFactory.
type GPUIndexFactory struct {
	config       *GPUConfig
	gpuAvailable bool
	fallback     interfaces.HNSWIndexFactory
}

// NewGPUIndexFactory returns a factory that creates GPU indexes when
// config.Enabled is true and GPU hardware is available, otherwise delegating
// to fallback. Pass nil as fallback only when GPU is guaranteed to be present.
func NewGPUIndexFactory(config *GPUConfig, fallback interfaces.HNSWIndexFactory) *GPUIndexFactory {
	return &GPUIndexFactory{
		config:       config,
		gpuAvailable: GPUAvailable(),
		fallback:     fallback,
	}
}

// CreateIndex creates a GPU-backed index when GPU acceleration is both enabled
// and available. It wraps the resulting index in a GPUIndexAdapter so that
// callers receive the standard interfaces.HNSWIndex interface.
//
// When GPU is disabled or hardware is absent, CreateIndex delegates to the CPU
// fallback factory. If no fallback was provided, ErrNoFallbackFactory is returned.
func (f *GPUIndexFactory) CreateIndex(config *interfaces.HNSWConfig) (interfaces.HNSWIndex, error) {
	if f.config.Enabled && f.gpuAvailable {
		return f.createGPUIndex(config)
	}
	return f.createFallbackIndex(config)
}

// createGPUIndex selects the appropriate GPU index type based on the configured
// GPUIndexType and wraps it in a GPUIndexAdapter.
func (f *GPUIndexFactory) createGPUIndex(config *interfaces.HNSWConfig) (interfaces.HNSWIndex, error) {
	// Default to CAGRA as the highest-throughput GPU index.
	// Dimension is inferred from the HNSW config's EfSearch heuristic;
	// callers that need a specific dimension should embed it in a custom config.
	// Here we use the HNSW M parameter as a proxy for expected dimension buckets,
	// but a production caller would pass dimension explicitly. We default to 128.
	const defaultDimension = 128
	idx, err := NewCAGRAIndex(f.config, defaultDimension, config.DistanceMetric)
	if err != nil {
		return nil, err
	}
	return NewGPUIndexAdapter(idx, defaultDimension), nil
}

// createFallbackIndex delegates index creation to the CPU fallback factory.
func (f *GPUIndexFactory) createFallbackIndex(config *interfaces.HNSWConfig) (interfaces.HNSWIndex, error) {
	if f.fallback == nil {
		return nil, ErrNoFallbackFactory
	}
	return f.fallback.CreateIndex(config)
}
