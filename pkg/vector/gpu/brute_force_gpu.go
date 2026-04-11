//go:build qrdb_vector && !gpu

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

// BruteForceGPU wraps NVIDIA cuVS GPU brute-force index.
// Brute-force on GPU provides exact nearest neighbor search
// with GPU-accelerated distance computation.
type BruteForceGPU struct{}

// NewBruteForceGPU creates a new GPU brute-force index.
// Returns ErrGPUNotCompiled when GPU support is not compiled in.
func NewBruteForceGPU(_ *GPUConfig, _ int, _ string) (*BruteForceGPU, error) {
	return nil, ErrGPUNotCompiled
}

// Insert adds a vector with the given ID to the index.
// Returns ErrGPUNotCompiled when GPU support is not compiled in.
func (idx *BruteForceGPU) Insert(_ uint64, _ []float32) error {
	return ErrGPUNotCompiled
}

// Delete removes a vector by ID from the index.
// Returns ErrGPUNotCompiled when GPU support is not compiled in.
func (idx *BruteForceGPU) Delete(_ uint64) error {
	return ErrGPUNotCompiled
}

// Search performs exact k-nearest neighbor search on the GPU.
// Returns ErrGPUNotCompiled when GPU support is not compiled in.
func (idx *BruteForceGPU) Search(_ []float32, _ int) ([]SearchResult, error) {
	return nil, ErrGPUNotCompiled
}

// Build builds the GPU index from the provided vectors.
// Returns ErrGPUNotCompiled when GPU support is not compiled in.
func (idx *BruteForceGPU) Build(_ map[uint64][]float32) error {
	return ErrGPUNotCompiled
}

// Count returns the number of indexed vectors.
// Returns 0 when GPU support is not compiled in.
func (idx *BruteForceGPU) Count() int64 {
	return 0
}

// Close releases GPU index resources.
// Returns ErrGPUNotCompiled when GPU support is not compiled in.
func (idx *BruteForceGPU) Close() error {
	return ErrGPUNotCompiled
}
