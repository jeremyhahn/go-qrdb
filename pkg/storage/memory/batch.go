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

package memory

import (
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

const (
	opSet    byte = 1
	opDelete byte = 2
)

// batchOp represents a single buffered operation in a batch.
type batchOp struct {
	op    byte
	key   []byte
	value []byte
}

// memoryBatch implements storage.Batch with buffered operations.
// Operations are applied atomically under the parent's write lock on Commit.
// Thread-safe: all mutation methods are guarded by a mutex.
type memoryBatch struct {
	parent *MemoryStorage
	ops    []batchOp
	mu     sync.Mutex
	closed atomic.Bool
}

// newBatch creates a new batch for the given MemoryStorage.
func newBatch(parent *MemoryStorage) storage.Batch {
	return &memoryBatch{
		parent: parent,
	}
}

// Set adds a key-value pair to the batch. Keys and values are defensively
// copied to prevent aliasing with the caller's byte slices.
func (b *memoryBatch) Set(key, value []byte) error {
	if b.closed.Load() {
		return &storage.BatchError{Op: "set", Err: &storage.ClosedError{}}
	}
	if len(key) == 0 {
		return &storage.BatchError{Op: "set", Err: storage.ErrEmptyKey}
	}

	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)

	valCopy := make([]byte, len(value))
	copy(valCopy, value)

	b.mu.Lock()
	b.ops = append(b.ops, batchOp{op: opSet, key: keyCopy, value: valCopy})
	b.mu.Unlock()

	return nil
}

// Delete adds a key deletion to the batch. The key is defensively copied.
func (b *memoryBatch) Delete(key []byte) error {
	if b.closed.Load() {
		return &storage.BatchError{Op: "delete", Err: &storage.ClosedError{}}
	}
	if len(key) == 0 {
		return &storage.BatchError{Op: "delete", Err: storage.ErrEmptyKey}
	}

	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)

	b.mu.Lock()
	b.ops = append(b.ops, batchOp{op: opDelete, key: keyCopy})
	b.mu.Unlock()

	return nil
}

// Commit atomically applies all batched operations to the parent storage.
// The batch is considered closed after a successful commit.
func (b *memoryBatch) Commit() error {
	if b.closed.Load() {
		return &storage.BatchError{Op: "commit", Err: &storage.ClosedError{}}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.parent.mu.Lock()
	for _, op := range b.ops {
		switch op.op {
		case opSet:
			b.parent.tree.Set(entry{key: op.key, value: op.value})
		case opDelete:
			b.parent.tree.Delete(entry{key: op.key})
		}
	}
	b.parent.mu.Unlock()

	b.ops = nil
	b.closed.Store(true)
	return nil
}

// Rollback discards all batched operations without applying them.
// The batch is considered closed after rollback.
func (b *memoryBatch) Rollback() error {
	if b.closed.Load() {
		return &storage.BatchError{Op: "rollback", Err: &storage.ClosedError{}}
	}

	b.mu.Lock()
	b.ops = nil
	b.mu.Unlock()

	b.closed.Store(true)
	return nil
}

// Close releases resources held by the batch. If the batch was neither
// committed nor rolled back, it performs an implicit rollback.
// Calling Close on an already-closed batch is a no-op.
func (b *memoryBatch) Close() error {
	if !b.closed.CompareAndSwap(false, true) {
		return nil
	}

	b.mu.Lock()
	b.ops = nil
	b.mu.Unlock()

	return nil
}
