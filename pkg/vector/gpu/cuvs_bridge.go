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

// Package gpu provides CGO bindings to the NVIDIA cuVS GPU vector index library.
// Building with -tags "qrdb_vector gpu" requires:
//   - CUDA Toolkit 12.x or later
//   - NVIDIA cuVS library (libcuvs)
//   - RAFT library (libraft)
//   - CGO_LDFLAGS=-lcuvs -lcudart -lraft
//   - CGO_CFLAGS=-I/usr/local/cuda/include -I/path/to/cuvs/include
package gpu

// #cgo LDFLAGS: -lcuvs -lcudart -lraft
// #include "cuvs_bridge.h"
// #include <stdlib.h>
import "C"

import (
	"unsafe"
)

// cudaInit initializes the CUDA device with the given device ordinal.
// Returns a non-nil error if initialization fails.
func cudaInit(deviceID int) error {
	rc := C.qrdb_cuda_init(C.int(deviceID))
	if rc != 0 {
		return &GPUDeviceError{
			DeviceID: deviceID,
			Message:  "CUDA initialization failed",
			Err:      ErrNotInitialized,
		}
	}
	return nil
}

// cudaCleanup releases all CUDA resources acquired by cudaInit.
func cudaCleanup() {
	C.qrdb_cuda_cleanup()
}

// cudaGetMemoryInfo queries GPU memory statistics for the active device.
func cudaGetMemoryInfo() (total, used, available int64, err error) {
	var cTotal, cUsed, cAvailable C.int64_t
	rc := C.qrdb_cuda_get_memory_info(&cTotal, &cUsed, &cAvailable)
	if rc != 0 {
		return 0, 0, 0, ErrKernelFailed
	}
	return int64(cTotal), int64(cUsed), int64(cAvailable), nil
}

// cudaHealthCheck verifies the GPU device is responsive.
func cudaHealthCheck() error {
	rc := C.qrdb_cuda_health_check()
	if rc != 0 {
		return ErrKernelFailed
	}
	return nil
}

// memoryPoolCreate allocates a new GPU memory pool capped at limitBytes.
// A limitBytes of 0 means unlimited.
func memoryPoolCreate(limitBytes int64) (*C.qrdb_memory_pool, error) {
	pool := C.qrdb_memory_pool_create(C.int64_t(limitBytes))
	if pool == nil {
		return nil, &GPUMemoryError{
			RequestedBytes: limitBytes,
			AvailableBytes: 0,
		}
	}
	return pool, nil
}

// memoryPoolDestroy destroys the memory pool and releases GPU resources.
func memoryPoolDestroy(pool *C.qrdb_memory_pool) {
	C.qrdb_memory_pool_destroy(pool)
}

// memoryPoolAllocated returns the number of bytes currently allocated in pool.
func memoryPoolAllocated(pool *C.qrdb_memory_pool) int64 {
	return int64(C.qrdb_memory_pool_allocated(pool))
}

// memoryPoolAvailable returns the number of bytes available in pool.
func memoryPoolAvailable(pool *C.qrdb_memory_pool) int64 {
	return int64(C.qrdb_memory_pool_available(pool))
}

// cagraCreate creates a new CAGRA index for vectors of given dimension and metric.
func cagraCreate(dimension int, metric string) (*C.qrdb_cagra_index, error) {
	cMetric := C.CString(metric)
	defer C.free(unsafe.Pointer(cMetric))
	idx := C.qrdb_cagra_create(C.int(dimension), cMetric)
	if idx == nil {
		return nil, ErrKernelFailed
	}
	return idx, nil
}

// cagraDestroy releases a CAGRA index.
func cagraDestroy(idx *C.qrdb_cagra_index) {
	C.qrdb_cagra_destroy(idx)
}

// cagraBuild sends a batch of vectors and their IDs to the GPU for index construction.
// vectors must have length count * dimension float32 values in row-major order.
// ids must have length count.
func cagraBuild(idx *C.qrdb_cagra_index, vectors []float32, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	rc := C.qrdb_cagra_build(
		idx,
		(*C.float)(unsafe.Pointer(&vectors[0])),
		(*C.uint64_t)(unsafe.Pointer(&ids[0])),
		C.size_t(len(ids)),
	)
	if rc != 0 {
		return ErrKernelFailed
	}
	return nil
}

// cagraSearch performs k-nearest neighbor search on the GPU CAGRA index.
// Returns slices of result IDs and distances of length k.
func cagraSearch(idx *C.qrdb_cagra_index, query []float32, k int) ([]uint64, []float32, error) {
	resultIDs := make([]uint64, k)
	resultDists := make([]float32, k)
	rc := C.qrdb_cagra_search(
		idx,
		(*C.float)(unsafe.Pointer(&query[0])),
		C.int(k),
		(*C.uint64_t)(unsafe.Pointer(&resultIDs[0])),
		(*C.float)(unsafe.Pointer(&resultDists[0])),
	)
	if rc != 0 {
		return nil, nil, ErrKernelFailed
	}
	return resultIDs, resultDists, nil
}

// cagraCount returns the number of vectors stored in the CAGRA index.
func cagraCount(idx *C.qrdb_cagra_index) int64 {
	return int64(C.qrdb_cagra_count(idx))
}

// bruteForceCreate creates a new GPU brute-force index.
func bruteForceCreate(dimension int, metric string) (*C.qrdb_brute_force_index, error) {
	cMetric := C.CString(metric)
	defer C.free(unsafe.Pointer(cMetric))
	idx := C.qrdb_brute_force_create(C.int(dimension), cMetric)
	if idx == nil {
		return nil, ErrKernelFailed
	}
	return idx, nil
}

// bruteForceDestroy releases a brute-force index.
func bruteForceDestroy(idx *C.qrdb_brute_force_index) {
	C.qrdb_brute_force_destroy(idx)
}

// bruteForceBuild sends vectors to the GPU for brute-force index construction.
func bruteForceBuild(idx *C.qrdb_brute_force_index, vectors []float32, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	rc := C.qrdb_brute_force_build(
		idx,
		(*C.float)(unsafe.Pointer(&vectors[0])),
		(*C.uint64_t)(unsafe.Pointer(&ids[0])),
		C.size_t(len(ids)),
	)
	if rc != 0 {
		return ErrKernelFailed
	}
	return nil
}

// bruteForceSearch performs exact k-nearest neighbor search on the GPU.
func bruteForceSearch(idx *C.qrdb_brute_force_index, query []float32, k int) ([]uint64, []float32, error) {
	resultIDs := make([]uint64, k)
	resultDists := make([]float32, k)
	rc := C.qrdb_brute_force_search(
		idx,
		(*C.float)(unsafe.Pointer(&query[0])),
		C.int(k),
		(*C.uint64_t)(unsafe.Pointer(&resultIDs[0])),
		(*C.float)(unsafe.Pointer(&resultDists[0])),
	)
	if rc != 0 {
		return nil, nil, ErrKernelFailed
	}
	return resultIDs, resultDists, nil
}

// bruteForceCount returns the number of vectors in the brute-force index.
func bruteForceCount(idx *C.qrdb_brute_force_index) int64 {
	return int64(C.qrdb_brute_force_count(idx))
}

// ivfFlatCreate creates a new GPU IVF-Flat index.
func ivfFlatCreate(dimension, nLists int, metric string) (*C.qrdb_ivf_flat_index, error) {
	cMetric := C.CString(metric)
	defer C.free(unsafe.Pointer(cMetric))
	idx := C.qrdb_ivf_flat_create(C.int(dimension), C.int(nLists), cMetric)
	if idx == nil {
		return nil, ErrKernelFailed
	}
	return idx, nil
}

// ivfFlatDestroy releases an IVF-Flat index.
func ivfFlatDestroy(idx *C.qrdb_ivf_flat_index) {
	C.qrdb_ivf_flat_destroy(idx)
}

// ivfFlatTrain trains the IVF-Flat index using the provided flat vector buffer.
// vectors must have length count * dimension float32 values in row-major order.
func ivfFlatTrain(idx *C.qrdb_ivf_flat_index, vectors []float32, count int) error {
	if count == 0 {
		return nil
	}
	rc := C.qrdb_ivf_flat_train(
		idx,
		(*C.float)(unsafe.Pointer(&vectors[0])),
		C.size_t(count),
	)
	if rc != 0 {
		return ErrKernelFailed
	}
	return nil
}

// ivfFlatBuild builds the IVF-Flat index from the provided vector batch.
func ivfFlatBuild(idx *C.qrdb_ivf_flat_index, vectors []float32, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	rc := C.qrdb_ivf_flat_build(
		idx,
		(*C.float)(unsafe.Pointer(&vectors[0])),
		(*C.uint64_t)(unsafe.Pointer(&ids[0])),
		C.size_t(len(ids)),
	)
	if rc != 0 {
		return ErrKernelFailed
	}
	return nil
}

// ivfFlatSearch performs k-nearest neighbor search using the IVF-Flat index.
// nProbes controls the trade-off between recall and speed.
func ivfFlatSearch(idx *C.qrdb_ivf_flat_index, query []float32, k, nProbes int) ([]uint64, []float32, error) {
	resultIDs := make([]uint64, k)
	resultDists := make([]float32, k)
	rc := C.qrdb_ivf_flat_search(
		idx,
		(*C.float)(unsafe.Pointer(&query[0])),
		C.int(k),
		C.int(nProbes),
		(*C.uint64_t)(unsafe.Pointer(&resultIDs[0])),
		(*C.float)(unsafe.Pointer(&resultDists[0])),
	)
	if rc != 0 {
		return nil, nil, ErrKernelFailed
	}
	return resultIDs, resultDists, nil
}

// ivfFlatCount returns the number of vectors indexed in the IVF-Flat index.
func ivfFlatCount(idx *C.qrdb_ivf_flat_index) int64 {
	return int64(C.qrdb_ivf_flat_count(idx))
}

// ivfPQCreate creates a new GPU IVF-PQ index.
func ivfPQCreate(dimension, nLists, pqDim, pqBits int, metric string) (*C.qrdb_ivf_pq_index, error) {
	cMetric := C.CString(metric)
	defer C.free(unsafe.Pointer(cMetric))
	idx := C.qrdb_ivf_pq_create(C.int(dimension), C.int(nLists), C.int(pqDim), C.int(pqBits), cMetric)
	if idx == nil {
		return nil, ErrKernelFailed
	}
	return idx, nil
}

// ivfPQDestroy releases an IVF-PQ index.
func ivfPQDestroy(idx *C.qrdb_ivf_pq_index) {
	C.qrdb_ivf_pq_destroy(idx)
}

// ivfPQTrain trains the IVF-PQ index on the provided flat vector buffer.
func ivfPQTrain(idx *C.qrdb_ivf_pq_index, vectors []float32, count int) error {
	if count == 0 {
		return nil
	}
	rc := C.qrdb_ivf_pq_train(
		idx,
		(*C.float)(unsafe.Pointer(&vectors[0])),
		C.size_t(count),
	)
	if rc != 0 {
		return ErrKernelFailed
	}
	return nil
}

// ivfPQBuild builds the IVF-PQ index from a batch of vectors.
func ivfPQBuild(idx *C.qrdb_ivf_pq_index, vectors []float32, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	rc := C.qrdb_ivf_pq_build(
		idx,
		(*C.float)(unsafe.Pointer(&vectors[0])),
		(*C.uint64_t)(unsafe.Pointer(&ids[0])),
		C.size_t(len(ids)),
	)
	if rc != 0 {
		return ErrKernelFailed
	}
	return nil
}

// ivfPQSearch performs k-nearest neighbor search using the IVF-PQ index.
// nProbes controls the accuracy/speed trade-off.
func ivfPQSearch(idx *C.qrdb_ivf_pq_index, query []float32, k, nProbes int) ([]uint64, []float32, error) {
	resultIDs := make([]uint64, k)
	resultDists := make([]float32, k)
	rc := C.qrdb_ivf_pq_search(
		idx,
		(*C.float)(unsafe.Pointer(&query[0])),
		C.int(k),
		C.int(nProbes),
		(*C.uint64_t)(unsafe.Pointer(&resultIDs[0])),
		(*C.float)(unsafe.Pointer(&resultDists[0])),
	)
	if rc != 0 {
		return nil, nil, ErrKernelFailed
	}
	return resultIDs, resultDists, nil
}

// ivfPQCount returns the number of vectors indexed in the IVF-PQ index.
func ivfPQCount(idx *C.qrdb_ivf_pq_index) int64 {
	return int64(C.qrdb_ivf_pq_count(idx))
}
