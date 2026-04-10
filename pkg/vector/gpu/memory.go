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

import "sync/atomic"

// MemoryPool manages a bounded GPU memory budget for vector index allocations.
// It tracks logical allocation totals in Go and delegates physical pool
// management to the CUDA memory pool in the cuVS bridge.
type MemoryPool struct {
	pool      *C.qrdb_memory_pool
	limit     int64
	allocated atomic.Int64
}

// NewMemoryPool creates a GPU memory pool capped at limitBytes.
// Pass 0 for limitBytes to create an unbounded pool.
func NewMemoryPool(limitBytes int64) (*MemoryPool, error) {
	pool, err := memoryPoolCreate(limitBytes)
	if err != nil {
		return nil, err
	}
	return &MemoryPool{
		pool:  pool,
		limit: limitBytes,
	}, nil
}

// Allocate reserves bytes from the pool, returning GPUMemoryError when the
// budget would be exceeded. A limit of 0 means unlimited.
func (p *MemoryPool) Allocate(bytes int64) error {
	if p.limit > 0 {
		current := p.allocated.Load()
		if current+bytes > p.limit {
			return &GPUMemoryError{
				RequestedBytes: bytes,
				AvailableBytes: p.limit - current,
			}
		}
	}
	p.allocated.Add(bytes)
	return nil
}

// Release returns bytes to the pool budget. Calling Release with more bytes
// than currently allocated is safe; the counter clamps to zero.
func (p *MemoryPool) Release(bytes int64) {
	for {
		current := p.allocated.Load()
		next := current - bytes
		if next < 0 {
			next = 0
		}
		if p.allocated.CompareAndSwap(current, next) {
			return
		}
	}
}

// Allocated returns the number of bytes currently allocated from the pool.
func (p *MemoryPool) Allocated() int64 {
	return p.allocated.Load()
}

// Available returns the number of bytes still available in the pool.
// Returns math.MaxInt64 when the pool is unbounded.
func (p *MemoryPool) Available() int64 {
	if p.limit == 0 {
		return memoryPoolAvailable(p.pool)
	}
	avail := p.limit - p.allocated.Load()
	if avail < 0 {
		return 0
	}
	return avail
}

// Close destroys the underlying GPU memory pool and releases all GPU resources.
func (p *MemoryPool) Close() error {
	memoryPoolDestroy(p.pool)
	return nil
}
