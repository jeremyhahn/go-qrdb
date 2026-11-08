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
	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/gpu/remote"
)

// GPUIndexFactory creates GPU-accelerated HNSW-compatible indexes. The
// priority order is:
//  1. Remote GPU — when RemoteGPUConfig.Enabled is true, all index operations
//     are forwarded to a VectorIndexService running on a dedicated GPU host.
//  2. Local GPU — when GPUConfig.Enabled is true and CUDA hardware is present.
//  3. CPU fallback — delegated to the provided fallback factory.
//
// It implements interfaces.HNSWIndexFactory.
type GPUIndexFactory struct {
	config       *GPUConfig
	remoteConfig *RemoteGPUConfig
	gpuAvailable bool
	fallback     interfaces.HNSWIndexFactory
}

// NewGPUIndexFactory returns a factory that creates GPU indexes when
// config.Enabled is true and GPU hardware is available, otherwise delegating
// to fallback. Pass nil as fallback only when GPU is guaranteed to be present.
//
// remoteConfig may be nil; when non-nil and Enabled it takes priority over the
// local GPU path.
func NewGPUIndexFactory(config *GPUConfig, remoteConfig *RemoteGPUConfig, fallback interfaces.HNSWIndexFactory) *GPUIndexFactory {
	return &GPUIndexFactory{
		config:       config,
		remoteConfig: remoteConfig,
		gpuAvailable: GPUAvailable(),
		fallback:     fallback,
	}
}

// CreateIndex selects the appropriate index backend using the priority order
// described on GPUIndexFactory. It wraps local GPU results in GPUIndexAdapter
// and remote results in RemoteGPUIndex, both satisfying interfaces.HNSWIndex.
func (f *GPUIndexFactory) CreateIndex(config *interfaces.HNSWConfig) (interfaces.HNSWIndex, error) {
	if f.remoteConfig != nil && f.remoteConfig.Enabled {
		return f.createRemoteIndex()
	}
	if f.config.Enabled && f.gpuAvailable {
		return f.createGPUIndex(config)
	}
	return f.createFallbackIndex(config)
}

// createRemoteIndex builds a RemoteGPUIndex connected to the configured
// VectorIndexService endpoint.
func (f *GPUIndexFactory) createRemoteIndex() (interfaces.HNSWIndex, error) {
	idx, err := remote.NewRemoteGPUIndex(remoteConfigToConnConfig(f.remoteConfig))
	if err != nil {
		return nil, &FactoryError{Op: "gpu factory: create remote index", Cause: err}
	}
	return idx, nil
}

// createGPUIndex selects the appropriate GPU index type based on the configured
// GPUIndexType and wraps it in a GPUIndexAdapter.
func (f *GPUIndexFactory) createGPUIndex(config *interfaces.HNSWConfig) (interfaces.HNSWIndex, error) {
	// A GPU index is allocated for one fixed dimension, so it is built for the
	// namespace's dimension and refuses a config that does not carry one.
	if config.Dimensions <= 0 {
		return nil, &FactoryError{Op: "gpu factory: create gpu index", Cause: ErrInvalidDimension}
	}
	// Default to CAGRA as the highest-throughput GPU index.
	idx, err := NewCAGRAIndex(f.config, config.Dimensions, config.DistanceMetric)
	if err != nil {
		return nil, err
	}
	return NewGPUIndexAdapter(idx, config.Dimensions), nil
}

// createFallbackIndex delegates index creation to the CPU fallback factory.
func (f *GPUIndexFactory) createFallbackIndex(config *interfaces.HNSWConfig) (interfaces.HNSWIndex, error) {
	if f.fallback == nil {
		return nil, &FactoryError{Op: "gpu factory", Cause: ErrNoFallbackFactory}
	}
	return f.fallback.CreateIndex(config)
}
