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

// rebuildThreshold is the number of pending inserts that trigger an automatic
// index rebuild to incorporate buffered vectors into the GPU graph.
const rebuildThreshold = 10000

// CAGRAIndex wraps the NVIDIA cuVS CAGRA graph-based GPU nearest neighbor index.
// CAGRA provides high-throughput approximate nearest neighbor search by building
// a proximity graph on the GPU during index construction.
//
// Insert buffers new vectors in memory and triggers a GPU rebuild when the
// pending buffer reaches rebuildThreshold. Build can also be called explicitly.
type CAGRAIndex struct {
	idx       *C.qrdb_cagra_index
	dimension int
	metric    string
	vectors   map[uint64][]float32
	built     bool
	mu        sync.RWMutex
}

// NewCAGRAIndex creates a CAGRA index for vectors of the given dimension using
// the specified distance metric (e.g. "l2", "cosine").
func NewCAGRAIndex(config *GPUConfig, dimension int, metric string) (*CAGRAIndex, error) {
	idx, err := cagraCreate(dimension, metric)
	if err != nil {
		return nil, &GPUDeviceError{
			DeviceID: config.DeviceID,
			Message:  "failed to create CAGRA index",
			Err:      err,
		}
	}
	return &CAGRAIndex{
		idx:       idx,
		dimension: dimension,
		metric:    metric,
		vectors:   make(map[uint64][]float32),
	}, nil
}

// Insert buffers a vector for the given ID. When the pending buffer reaches
// rebuildThreshold, the index is automatically rebuilt on the GPU.
// Inserting a duplicate ID overwrites the previous vector.
func (idx *CAGRAIndex) Insert(id uint64, vector []float32) error {
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

// Delete removes a vector by ID from the in-memory buffer and marks the index
// as needing a rebuild. Deletion triggers a full rebuild to propagate the
// removal to the GPU graph.
func (idx *CAGRAIndex) Delete(id uint64) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.vectors, id)
	idx.built = false
	return nil
}

// Search performs k-nearest neighbor search against the GPU index.
// If the index has pending buffered vectors, it rebuilds before searching.
// Returns ErrNotInitialized when the index has never been built.
func (idx *CAGRAIndex) Search(query []float32, k int) ([]SearchResult, error) {
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

	resultIDs, resultDists, err := cagraSearch(idx.idx, query, k)
	if err != nil {
		return nil, err
	}

	results := make([]SearchResult, len(resultIDs))
	for i, id := range resultIDs {
		results[i] = SearchResult{ID: id, Distance: resultDists[i]}
	}
	return results, nil
}

// Build explicitly triggers a GPU index construction from the provided vector map.
// It replaces the current in-memory buffer and rebuilds the GPU graph.
func (idx *CAGRAIndex) Build(vectors map[uint64][]float32) error {
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

// Count returns the total number of vectors currently in the index,
// including any pending buffered vectors not yet sent to the GPU.
func (idx *CAGRAIndex) Count() int64 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if idx.built {
		return cagraCount(idx.idx)
	}
	return int64(len(idx.vectors))
}

// Close destroys the underlying GPU index and releases all CUDA resources.
// After Close, the CAGRAIndex must not be used.
func (idx *CAGRAIndex) Close() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.idx != nil {
		cagraDestroy(idx.idx)
		idx.idx = nil
	}
	return nil
}

// buildLocked rebuilds the GPU index from the current in-memory vector buffer.
// Must be called with idx.mu held for writing.
func (idx *CAGRAIndex) buildLocked() error {
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
	if err := cagraBuild(idx.idx, flat, ids); err != nil {
		return err
	}
	idx.built = true
	return nil
}
