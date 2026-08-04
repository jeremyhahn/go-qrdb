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

// IVFPQGPU wraps the NVIDIA cuVS GPU IVF-PQ nearest neighbor index.
// IVF-PQ combines inverted file indexing with product quantization to achieve
// high recall at greatly reduced memory footprint compared to IVF-Flat.
//
// Train must be called with a representative sample before Build.
// nLists controls clustering granularity; pqDim and pqBits control compression.
type IVFPQGPU struct {
	idx       *C.qrdb_ivf_pq_index
	dimension int
	nLists    int
	pqDim     int
	pqBits    int
	nProbes   int
	metric    string
	vectors   map[uint64][]float32
	trained   bool
	built     bool
	mu        sync.RWMutex
}

// NewIVFPQGPU creates a GPU IVF-PQ index.
// nLists is the number of IVF clusters (typically sqrt(n) for corpus size n).
// pqDim is the number of product quantizer sub-spaces; must evenly divide dimension.
// pqBits is the number of bits per sub-space code (4 or 8).
func NewIVFPQGPU(config *GPUConfig, dimension, nLists, pqDim, pqBits int, metric string) (*IVFPQGPU, error) {
	idx, err := ivfPQCreate(dimension, nLists, pqDim, pqBits, metric)
	if err != nil {
		return nil, &GPUDeviceError{
			DeviceID: config.DeviceID,
			Message:  "failed to create IVF-PQ GPU index",
			Err:      err,
		}
	}
	nProbes := defaultNProbes
	if nProbes > nLists {
		nProbes = nLists
	}
	return &IVFPQGPU{
		idx:       idx,
		dimension: dimension,
		nLists:    nLists,
		pqDim:     pqDim,
		pqBits:    pqBits,
		nProbes:   nProbes,
		metric:    metric,
		vectors:   make(map[uint64][]float32),
	}, nil
}

// Insert buffers a vector for the given ID. Inserting a duplicate ID overwrites
// the existing vector. An automatic rebuild is triggered at rebuildThreshold inserts.
func (idx *IVFPQGPU) Insert(id uint64, vector []float32) error {
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

// Delete removes a vector by ID and invalidates the GPU index.
func (idx *IVFPQGPU) Delete(id uint64) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.vectors, id)
	idx.built = false
	return nil
}

// Search performs approximate k-nearest neighbor search using nProbes clusters.
// Rebuilds from the buffer when pending inserts exist.
// Returns an empty slice when no vectors have been indexed.
func (idx *IVFPQGPU) Search(query []float32, k int) ([]SearchResult, error) {
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

	resultIDs, resultDists, err := ivfPQSearch(idx.idx, query, k, idx.nProbes)
	if err != nil {
		return nil, err
	}

	results := make([]SearchResult, len(resultIDs))
	for i, id := range resultIDs {
		results[i] = SearchResult{ID: id, Distance: resultDists[i]}
	}
	return results, nil
}

// Build replaces the current buffer with the provided vectors and rebuilds the GPU index.
// Train must have been called before Build.
func (idx *IVFPQGPU) Build(vectors map[uint64][]float32) error {
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

// Train trains the product quantizer codebooks from the provided training vectors.
// Each element of trainingVectors must have idx.dimension elements.
// Training must complete before Build or Search.
func (idx *IVFPQGPU) Train(trainingVectors [][]float32) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if len(trainingVectors) == 0 {
		return nil
	}
	flat := make([]float32, 0, len(trainingVectors)*idx.dimension)
	for _, v := range trainingVectors {
		flat = append(flat, v...)
	}
	if err := ivfPQTrain(idx.idx, flat, len(trainingVectors)); err != nil {
		return err
	}
	idx.trained = true
	return nil
}

// Count returns the number of vectors in the index.
func (idx *IVFPQGPU) Count() int64 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if idx.built {
		return ivfPQCount(idx.idx)
	}
	return int64(len(idx.vectors))
}

// Close destroys the underlying GPU index and releases all CUDA resources.
func (idx *IVFPQGPU) Close() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.idx != nil {
		ivfPQDestroy(idx.idx)
		idx.idx = nil
	}
	return nil
}

// buildLocked sends the buffered vectors to the GPU and builds the IVF-PQ index.
// Must be called with idx.mu held for writing.
func (idx *IVFPQGPU) buildLocked() error {
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
	if err := ivfPQBuild(idx.idx, flat, ids); err != nil {
		return err
	}
	idx.built = true
	return nil
}
