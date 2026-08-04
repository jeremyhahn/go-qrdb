//go:build qrdb_vector && gpu

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

// #include "cuvs_bridge.h"
import "C"

import "sync"

// BruteForceGPU wraps the NVIDIA cuVS GPU brute-force exact nearest neighbor index.
// Brute force computes distances between the query and every stored vector on the GPU,
// guaranteeing exact results at the cost of O(n) work per query.
//
// Insert buffers vectors in memory. Build sends the full buffer to the GPU.
// A rebuild is triggered automatically when the pending buffer reaches rebuildThreshold.
type BruteForceGPU struct {
	idx       *C.qrdb_brute_force_index
	dimension int
	metric    string
	vectors   map[uint64][]float32
	built     bool
	mu        sync.RWMutex
}

// NewBruteForceGPU creates a GPU brute-force index for vectors of the given
// dimension using the specified distance metric (e.g. "l2", "cosine").
func NewBruteForceGPU(config *GPUConfig, dimension int, metric string) (*BruteForceGPU, error) {
	idx, err := bruteForceCreate(dimension, metric)
	if err != nil {
		return nil, &GPUDeviceError{
			DeviceID: config.DeviceID,
			Message:  "failed to create brute-force GPU index",
			Err:      err,
		}
	}
	return &BruteForceGPU{
		idx:       idx,
		dimension: dimension,
		metric:    metric,
		vectors:   make(map[uint64][]float32),
	}, nil
}

// Insert buffers a vector for the given ID. Inserting a duplicate ID overwrites
// the existing vector. An automatic rebuild is triggered at rebuildThreshold inserts.
func (idx *BruteForceGPU) Insert(id uint64, vector []float32) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	cp := make([]float32, len(vector))
	copy(cp, vector)
	idx.vectors[id] = cp
	if len(idx.vectors) >= rebuildThreshold {
		return idx.buildLocked()
	}
	return nil
}

// Delete removes a vector by ID from the buffer and invalidates the GPU index.
// The GPU index is rebuilt on the next Search call.
func (idx *BruteForceGPU) Delete(id uint64) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.vectors, id)
	idx.built = false
	return nil
}

// Search performs exact k-nearest neighbor search on the GPU.
// Rebuilds the GPU index if there are pending buffered vectors.
// Returns an empty slice when no vectors have been indexed.
func (idx *BruteForceGPU) Search(query []float32, k int) ([]SearchResult, error) {
	idx.mu.Lock()
	if !idx.built && len(idx.vectors) > 0 {
		if err := idx.buildLocked(); err != nil {
			idx.mu.Unlock()
			return nil, err
		}
	}
	idx.mu.Unlock()

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if !idx.built {
		return []SearchResult{}, nil
	}

	resultIDs, resultDists, err := bruteForceSearch(idx.idx, query, k)
	if err != nil {
		return nil, err
	}

	results := make([]SearchResult, len(resultIDs))
	for i, id := range resultIDs {
		results[i] = SearchResult{ID: id, Distance: resultDists[i]}
	}
	return results, nil
}

// Build replaces the current buffer with the provided vector map and rebuilds
// the GPU index immediately.
func (idx *BruteForceGPU) Build(vectors map[uint64][]float32) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.vectors = make(map[uint64][]float32, len(vectors))
	for id, v := range vectors {
		cp := make([]float32, len(v))
		copy(cp, v)
		idx.vectors[id] = cp
	}
	return idx.buildLocked()
}

// Count returns the number of vectors in the index. When the GPU index has been
// built, the count is read from the GPU. Otherwise the pending buffer length is returned.
func (idx *BruteForceGPU) Count() int64 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if idx.built {
		return bruteForceCount(idx.idx)
	}
	return int64(len(idx.vectors))
}

// Close destroys the underlying GPU index and releases all CUDA resources.
// After Close, the BruteForceGPU must not be used.
func (idx *BruteForceGPU) Close() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.idx != nil {
		bruteForceDestroy(idx.idx)
		idx.idx = nil
	}
	return nil
}

// buildLocked sends the in-memory vector buffer to the GPU.
// Must be called with idx.mu held for writing.
func (idx *BruteForceGPU) buildLocked() error {
	n := len(idx.vectors)
	if n == 0 {
		return nil
	}
	flat := make([]float32, 0, n*idx.dimension)
	ids := make([]uint64, 0, n)
	for id, v := range idx.vectors {
		ids = append(ids, id)
		flat = append(flat, v...)
	}
	if err := bruteForceBuild(idx.idx, flat, ids); err != nil {
		return err
	}
	idx.built = true
	return nil
}
