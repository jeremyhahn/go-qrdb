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

// defaultNProbes is the default number of IVF clusters probed during search.
// Higher values increase recall at the cost of latency.
const defaultNProbes = 32

// IVFFlatGPU wraps the NVIDIA cuVS GPU IVF-Flat nearest neighbor index.
// IVF-Flat partitions the vector space into nLists clusters. At search time
// nProbes clusters are examined, providing a recall/speed trade-off.
//
// Train must be called before Build when creating the index from scratch.
// Build sends the full vector set to the GPU after training.
type IVFFlatGPU struct {
	idx       *C.qrdb_ivf_flat_index
	dimension int
	nLists    int
	nProbes   int
	metric    string
	vectors   map[uint64][]float32
	trained   bool
	built     bool
	mu        sync.RWMutex
}

// NewIVFFlatGPU creates a GPU IVF-Flat index. nLists is the number of Voronoi
// clusters; a typical value is sqrt(n) where n is the expected corpus size.
func NewIVFFlatGPU(config *GPUConfig, dimension, nLists int, metric string) (*IVFFlatGPU, error) {
	idx, err := ivfFlatCreate(dimension, nLists, metric)
	if err != nil {
		return nil, &GPUDeviceError{
			DeviceID: config.DeviceID,
			Message:  "failed to create IVF-Flat GPU index",
			Err:      err,
		}
	}
	nProbes := defaultNProbes
	if nProbes > nLists {
		nProbes = nLists
	}
	return &IVFFlatGPU{
		idx:       idx,
		dimension: dimension,
		nLists:    nLists,
		nProbes:   nProbes,
		metric:    metric,
		vectors:   make(map[uint64][]float32),
	}, nil
}

// Insert buffers a vector for the given ID. Inserting a duplicate ID overwrites
// the existing vector. An automatic rebuild is triggered at rebuildThreshold inserts.
func (idx *IVFFlatGPU) Insert(id uint64, vector []float32) error {
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
func (idx *IVFFlatGPU) Delete(id uint64) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.vectors, id)
	idx.built = false
	return nil
}

// Search performs approximate k-nearest neighbor search using nProbes clusters.
// Rebuilds from the buffer when pending inserts exist.
// Returns an empty slice when no vectors have been indexed.
func (idx *IVFFlatGPU) Search(query []float32, k int) ([]SearchResult, error) {
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

	resultIDs, resultDists, err := ivfFlatSearch(idx.idx, query, k, idx.nProbes)
	if err != nil {
		return nil, err
	}

	results := make([]SearchResult, len(resultIDs))
	for i, id := range resultIDs {
		results[i] = SearchResult{ID: id, Distance: resultDists[i]}
	}
	return results, nil
}

// Build replaces the current buffer with the provided vectors and immediately
// rebuilds the GPU index. Train must have been called at least once before Build.
func (idx *IVFFlatGPU) Build(vectors map[uint64][]float32) error {
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

// Train trains the IVF cluster centroids from the provided training vectors.
// Each element of trainingVectors must have idx.dimension elements.
// Training must be completed before Build or Search are called.
func (idx *IVFFlatGPU) Train(trainingVectors [][]float32) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if len(trainingVectors) == 0 {
		return nil
	}
	flat := make([]float32, 0, len(trainingVectors)*idx.dimension)
	for _, v := range trainingVectors {
		flat = append(flat, v...)
	}
	if err := ivfFlatTrain(idx.idx, flat, len(trainingVectors)); err != nil {
		return err
	}
	idx.trained = true
	return nil
}

// Count returns the number of vectors in the index.
func (idx *IVFFlatGPU) Count() int64 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if idx.built {
		return ivfFlatCount(idx.idx)
	}
	return int64(len(idx.vectors))
}

// Close destroys the underlying GPU index and releases all CUDA resources.
func (idx *IVFFlatGPU) Close() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.idx != nil {
		ivfFlatDestroy(idx.idx)
		idx.idx = nil
	}
	return nil
}

// buildLocked sends the buffered vectors to the GPU and builds the IVF-Flat index.
// Must be called with idx.mu held for writing.
func (idx *IVFFlatGPU) buildLocked() error {
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
	if err := ivfFlatBuild(idx.idx, flat, ids); err != nil {
		return err
	}
	idx.built = true
	return nil
}
