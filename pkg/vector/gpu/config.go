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

// Package gpu provides GPU-accelerated vector index implementations
// powered by NVIDIA cuVS. When built without the "gpu" build tag,
// all operations return ErrGPUNotCompiled.
package gpu

// GPUConfig configures GPU acceleration settings.
type GPUConfig struct {
	// DeviceID is the CUDA device ordinal to use.
	DeviceID int

	// MemoryLimitBytes is the maximum GPU memory to use (0 = unlimited).
	MemoryLimitBytes int64

	// BuildBatchSize is the number of vectors to transfer per batch during index build.
	BuildBatchSize int

	// Enabled indicates whether GPU acceleration is active.
	Enabled bool
}

// DefaultGPUConfig returns sensible defaults for GPU acceleration.
func DefaultGPUConfig() *GPUConfig {
	return &GPUConfig{
		DeviceID:         0,
		MemoryLimitBytes: 0, // unlimited
		BuildBatchSize:   100000,
		Enabled:          false,
	}
}

// GPUIndexType represents GPU-specific index types.
type GPUIndexType int

const (
	// GPUIndexCAGRA is a graph-based GPU index using NVIDIA cuVS CAGRA.
	GPUIndexCAGRA GPUIndexType = iota

	// GPUIndexIVFFlat is a GPU IVF-Flat index.
	GPUIndexIVFFlat

	// GPUIndexIVFPQ is a GPU IVF index with Product Quantization.
	GPUIndexIVFPQ

	// GPUIndexBruteForce is a GPU brute-force exact search index.
	GPUIndexBruteForce
)

// gpuIndexTypeNames provides O(1) lookup from GPUIndexType to string representation.
var gpuIndexTypeNames = map[GPUIndexType]string{
	GPUIndexCAGRA:      "gpu_cagra",
	GPUIndexIVFFlat:    "gpu_ivf_flat",
	GPUIndexIVFPQ:      "gpu_ivf_pq",
	GPUIndexBruteForce: "gpu_brute_force",
}

// String returns the string representation of the GPUIndexType.
func (t GPUIndexType) String() string {
	if name, ok := gpuIndexTypeNames[t]; ok {
		return name
	}
	return "unknown"
}
