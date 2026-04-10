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

import (
	"runtime"
	"sync"
)

// batchSequentialThreshold is the maximum number of vectors to compute
// sequentially before switching to concurrent processing.
const batchSequentialThreshold = 256

// CalcDistanceBatch computes distances between a query vector and multiple
// target vectors. For batches larger than batchSequentialThreshold, the work
// is distributed across runtime.NumCPU() goroutines for parallel execution.
// Returns a slice of distances in the same order as the input vectors.
func CalcDistanceBatch(metric DistanceMetric, query []float32, vectors [][]float32) ([]float32, error) {
	if len(query) == 0 {
		return nil, &BatchError{
			Op:  "CalcDistanceBatch",
			Err: ErrInvalidVector,
		}
	}
	if len(vectors) == 0 {
		return nil, &BatchError{
			Op:  "CalcDistanceBatch",
			Err: ErrEmptyBatch,
		}
	}

	queryDim := len(query)
	for i, v := range vectors {
		if len(v) != queryDim {
			return nil, &DimensionMismatchError{
				Index:    i,
				Got:      len(v),
				Expected: queryDim,
			}
		}
	}

	distFn := GetDistanceFunc(metric)
	results := make([]float32, len(vectors))

	if len(vectors) <= batchSequentialThreshold {
		for i, v := range vectors {
			results[i] = distFn(query, v)
		}
		return results, nil
	}

	numWorkers := runtime.NumCPU()
	if numWorkers > len(vectors) {
		numWorkers = len(vectors)
	}

	chunkSize := len(vectors) / numWorkers
	remainder := len(vectors) % numWorkers

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	start := 0
	for w := 0; w < numWorkers; w++ {
		end := start + chunkSize
		if w < remainder {
			end++
		}

		go func(from, to int) {
			defer wg.Done()
			for i := from; i < to; i++ {
				results[i] = distFn(query, vectors[i])
			}
		}(start, end)

		start = end
	}

	wg.Wait()
	return results, nil
}
