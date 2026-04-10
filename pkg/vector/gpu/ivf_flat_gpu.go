//go:build qrdb_vector && !gpu

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

package gpu

// IVFFlatGPU wraps NVIDIA cuVS GPU IVF-FLAT index.
// IVF-Flat on GPU provides accelerated inverted file search
// with flat (uncompressed) vectors in each cluster.
type IVFFlatGPU struct{}

// NewIVFFlatGPU creates a new GPU IVF-Flat index.
// Returns ErrGPUNotCompiled when GPU support is not compiled in.
func NewIVFFlatGPU(_ *GPUConfig, _, _ int, _ string) (*IVFFlatGPU, error) {
	return nil, ErrGPUNotCompiled
}

// Insert adds a vector with the given ID to the index.
// Returns ErrGPUNotCompiled when GPU support is not compiled in.
func (idx *IVFFlatGPU) Insert(_ uint64, _ []float32) error {
	return ErrGPUNotCompiled
}

// Delete removes a vector by ID from the index.
// Returns ErrGPUNotCompiled when GPU support is not compiled in.
func (idx *IVFFlatGPU) Delete(_ uint64) error {
	return ErrGPUNotCompiled
}

// Search performs k-nearest neighbor search on the GPU.
// Returns ErrGPUNotCompiled when GPU support is not compiled in.
func (idx *IVFFlatGPU) Search(_ []float32, _ int) ([]SearchResult, error) {
	return nil, ErrGPUNotCompiled
}

// Build builds the GPU index from the provided vectors.
// Returns ErrGPUNotCompiled when GPU support is not compiled in.
func (idx *IVFFlatGPU) Build(_ map[uint64][]float32) error {
	return ErrGPUNotCompiled
}

// Train trains the IVF index on the provided training vectors.
// Returns ErrGPUNotCompiled when GPU support is not compiled in.
func (idx *IVFFlatGPU) Train(_ [][]float32) error {
	return ErrGPUNotCompiled
}

// Count returns the number of indexed vectors.
// Returns 0 when GPU support is not compiled in.
func (idx *IVFFlatGPU) Count() int64 {
	return 0
}

// Close releases GPU index resources.
// Returns ErrGPUNotCompiled when GPU support is not compiled in.
func (idx *IVFFlatGPU) Close() error {
	return ErrGPUNotCompiled
}
