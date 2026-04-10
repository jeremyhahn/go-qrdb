#ifndef QRDB_CUVS_BRIDGE_H
#define QRDB_CUVS_BRIDGE_H

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

#include <stdint.h>
#include <stddef.h>

// Device management
int qrdb_cuda_init(int device_id);
void qrdb_cuda_cleanup(void);
int qrdb_cuda_get_memory_info(int64_t* total, int64_t* used, int64_t* available);
int qrdb_cuda_health_check(void);

// Memory pool
typedef struct qrdb_memory_pool qrdb_memory_pool;
qrdb_memory_pool* qrdb_memory_pool_create(int64_t limit_bytes);
void qrdb_memory_pool_destroy(qrdb_memory_pool* pool);
int64_t qrdb_memory_pool_allocated(qrdb_memory_pool* pool);
int64_t qrdb_memory_pool_available(qrdb_memory_pool* pool);

// CAGRA index
typedef struct qrdb_cagra_index qrdb_cagra_index;
qrdb_cagra_index* qrdb_cagra_create(int dimension, const char* metric);
void qrdb_cagra_destroy(qrdb_cagra_index* idx);
int qrdb_cagra_build(qrdb_cagra_index* idx, const float* vectors, const uint64_t* ids, size_t count);
int qrdb_cagra_search(qrdb_cagra_index* idx, const float* query, int k, uint64_t* result_ids, float* result_distances);
int64_t qrdb_cagra_count(qrdb_cagra_index* idx);

// Brute force index
typedef struct qrdb_brute_force_index qrdb_brute_force_index;
qrdb_brute_force_index* qrdb_brute_force_create(int dimension, const char* metric);
void qrdb_brute_force_destroy(qrdb_brute_force_index* idx);
int qrdb_brute_force_build(qrdb_brute_force_index* idx, const float* vectors, const uint64_t* ids, size_t count);
int qrdb_brute_force_search(qrdb_brute_force_index* idx, const float* query, int k, uint64_t* result_ids, float* result_distances);
int64_t qrdb_brute_force_count(qrdb_brute_force_index* idx);

// IVF-Flat index
typedef struct qrdb_ivf_flat_index qrdb_ivf_flat_index;
qrdb_ivf_flat_index* qrdb_ivf_flat_create(int dimension, int n_lists, const char* metric);
void qrdb_ivf_flat_destroy(qrdb_ivf_flat_index* idx);
int qrdb_ivf_flat_train(qrdb_ivf_flat_index* idx, const float* vectors, size_t count);
int qrdb_ivf_flat_build(qrdb_ivf_flat_index* idx, const float* vectors, const uint64_t* ids, size_t count);
int qrdb_ivf_flat_search(qrdb_ivf_flat_index* idx, const float* query, int k, int n_probes, uint64_t* result_ids, float* result_distances);
int64_t qrdb_ivf_flat_count(qrdb_ivf_flat_index* idx);

// IVF-PQ index
typedef struct qrdb_ivf_pq_index qrdb_ivf_pq_index;
qrdb_ivf_pq_index* qrdb_ivf_pq_create(int dimension, int n_lists, int pq_dim, int pq_bits, const char* metric);
void qrdb_ivf_pq_destroy(qrdb_ivf_pq_index* idx);
int qrdb_ivf_pq_train(qrdb_ivf_pq_index* idx, const float* vectors, size_t count);
int qrdb_ivf_pq_build(qrdb_ivf_pq_index* idx, const float* vectors, const uint64_t* ids, size_t count);
int qrdb_ivf_pq_search(qrdb_ivf_pq_index* idx, const float* query, int k, int n_probes, uint64_t* result_ids, float* result_distances);
int64_t qrdb_ivf_pq_count(qrdb_ivf_pq_index* idx);

#endif // QRDB_CUVS_BRIDGE_H
