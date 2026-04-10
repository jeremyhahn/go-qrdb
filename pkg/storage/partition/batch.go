// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package partition

import (
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Compile-time interface assertion.
var _ storage.Batch = (*partitionBatch)(nil)

// partitionBatch implements storage.Batch by grouping operations by partition.
// Each partition's batch is committed independently. The batch lazily creates
// per-partition batches on first write to avoid unnecessary allocations.
type partitionBatch struct {
	manager *Manager
	batches map[uint32]storage.Batch
	mu      sync.Mutex
	closed  atomic.Bool
}

// newPartitionBatch creates a new multi-partition batch.
func newPartitionBatch(manager *Manager) storage.Batch {
	return &partitionBatch{
		manager: manager,
		batches: make(map[uint32]storage.Batch),
	}
}

// getBatch returns the batch for the given key's partition, creating it lazily.
// Caller must hold mu.
func (b *partitionBatch) getBatch(key []byte) storage.Batch {
	id := b.manager.router.Route(key)
	batch, ok := b.batches[id]
	if !ok {
		batch = b.manager.partitions[id].Batch()
		b.batches[id] = batch
	}
	return batch
}

// Set adds a key-value pair to the appropriate partition's batch.
func (b *partitionBatch) Set(key, value []byte) error {
	if b.closed.Load() {
		return &storage.BatchError{Op: "set", Err: &storage.ClosedError{}}
	}

	b.mu.Lock()
	batch := b.getBatch(key)
	b.mu.Unlock()

	return batch.Set(key, value)
}

// Delete adds a key deletion to the appropriate partition's batch.
func (b *partitionBatch) Delete(key []byte) error {
	if b.closed.Load() {
		return &storage.BatchError{Op: "delete", Err: &storage.ClosedError{}}
	}

	b.mu.Lock()
	batch := b.getBatch(key)
	b.mu.Unlock()

	return batch.Delete(key)
}

// Commit commits all partition batches. If any partition's batch fails to
// commit, all errors are collected and returned as a BatchCommitError.
// This is best-effort: each partition commits independently.
func (b *partitionBatch) Commit() error {
	if b.closed.Load() {
		return &storage.BatchError{Op: "commit", Err: &storage.ClosedError{}}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	var errs []error
	for _, batch := range b.batches {
		if err := batch.Commit(); err != nil {
			errs = append(errs, err)
		}
	}

	b.closed.Store(true)

	if len(errs) > 0 {
		return &BatchCommitError{Errors: errs}
	}
	return nil
}

// Rollback discards all partition batches without applying them.
func (b *partitionBatch) Rollback() error {
	if b.closed.Load() {
		return &storage.BatchError{Op: "rollback", Err: &storage.ClosedError{}}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	var firstErr error
	for _, batch := range b.batches {
		if err := batch.Rollback(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	b.closed.Store(true)
	return firstErr
}

// Close releases resources held by all partition batches.
func (b *partitionBatch) Close() error {
	if !b.closed.CompareAndSwap(false, true) {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	var firstErr error
	for _, batch := range b.batches {
		if err := batch.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	b.batches = nil
	return firstErr
}
