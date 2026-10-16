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
	"bytes"
	"context"
	"sync"
	"sync/atomic"

	"github.com/tidwall/btree"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Compile-time interface assertion.
var _ storage.Storage = (*MemoryStorage)(nil)

// entry is a key-value pair stored in the btree.
type entry struct {
	key   []byte
	value []byte
}

// entryLess defines the ordering of entries by key using bytes.Compare.
func entryLess(a, b entry) bool {
	return bytes.Compare(a.key, b.key) < 0
}

// MemoryStorage implements storage.Storage backed by an ordered btree.
// It provides an in-memory key-value store with batch writes, iterators,
// and point-in-time snapshots for the Raft state machine layer.
//
// Thread safety: reads are guarded by RLock, writes by Lock on the RWMutex.
// The closed flag uses atomic.Bool for lock-free state checking.
type MemoryStorage struct {
	tree   *btree.BTreeG[entry]
	mu     sync.RWMutex
	closed atomic.Bool
}

// NewStorage creates a new in-memory storage instance backed by an ordered btree.
func NewStorage() *MemoryStorage {
	return &MemoryStorage{
		tree: btree.NewBTreeGOptions(entryLess, btree.Options{
			NoLocks: true,
		}),
	}
}

// Get retrieves the value for the given key. Returns a defensive copy that is
// safe to retain. Returns nil, nil when the key does not exist per the
// storage.Storage contract.
func (s *MemoryStorage) Get(key []byte) ([]byte, error) {
	if s.closed.Load() {
		return nil, &storage.ClosedError{}
	}
	if len(key) == 0 {
		return nil, storage.ErrEmptyKey
	}

	s.mu.RLock()
	item, ok := s.tree.Get(entry{key: key})
	s.mu.RUnlock()

	if !ok {
		return nil, nil
	}

	result := make([]byte, len(item.value))
	copy(result, item.value)
	return result, nil
}

// Set stores a key-value pair. Keys and values are defensively copied to
// prevent aliasing with the caller's byte slices.
func (s *MemoryStorage) Set(key, value []byte) error {
	if s.closed.Load() {
		return &storage.ClosedError{}
	}
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}

	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)

	valCopy := make([]byte, len(value))
	copy(valCopy, value)

	s.mu.Lock()
	s.tree.Set(entry{key: keyCopy, value: valCopy})
	s.mu.Unlock()

	return nil
}

// CompareAndSwap implements storage.GuardedStorage. The compare and the write
// happen under one write lock, so concurrent swaps on a key serialise.
func (s *MemoryStorage) CompareAndSwap(ctx context.Context, key, expected, next []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if s.closed.Load() {
		return false, &storage.ClosedError{}
	}
	if len(key) == 0 {
		return false, storage.ErrEmptyKey
	}

	nextCopy := make([]byte, len(next))
	copy(nextCopy, next)

	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.tree.Get(entry{key: key})
	if !ok || !bytes.Equal(item.value, expected) {
		return false, nil
	}
	s.tree.Set(entry{key: item.key, value: nextCopy})
	return true, nil
}

// Delete removes a key-value pair. Deleting a non-existent key is a no-op.
func (s *MemoryStorage) Delete(key []byte) error {
	if s.closed.Load() {
		return &storage.ClosedError{}
	}
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}

	s.mu.Lock()
	s.tree.Delete(entry{key: key})
	s.mu.Unlock()

	return nil
}

// DeleteRange removes every key in [start, end); a nil end reaches the end of
// the keyspace. Deleting an empty range is a no-op.
func (s *MemoryStorage) DeleteRange(start, end []byte) error {
	if s.closed.Load() {
		return &storage.ClosedError{}
	}
	if err := storage.ValidateRange(start, end); err != nil {
		return err
	}

	s.mu.Lock()
	s.deleteRangeLocked(start, end)
	s.mu.Unlock()
	return nil
}

// deleteRangeLocked removes every key in [start, end) from the tree; an empty
// end is unbounded. The caller holds s.mu for writing.
func (s *MemoryStorage) deleteRangeLocked(start, end []byte) {
	var keys [][]byte
	s.tree.Ascend(entry{key: start}, func(item entry) bool {
		if len(end) > 0 && bytes.Compare(item.key, end) >= 0 {
			return false
		}
		keys = append(keys, item.key)
		return true
	})
	for _, key := range keys {
		s.tree.Delete(entry{key: key})
	}
}

// List returns all keys matching the given prefix. An empty or nil prefix
// returns all keys. Keys are returned in sorted order as defensive copies.
func (s *MemoryStorage) List(prefix []byte) ([][]byte, error) {
	if s.closed.Load() {
		return nil, &storage.ClosedError{}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var keys [][]byte
	s.tree.Scan(func(item entry) bool {
		if bytes.HasPrefix(item.key, prefix) {
			cp := make([]byte, len(item.key))
			copy(cp, item.key)
			keys = append(keys, cp)
		}
		return true
	})
	return keys, nil
}

// Exists reports whether the key exists in storage. Returns false for
// non-existent keys without error.
func (s *MemoryStorage) Exists(key []byte) (bool, error) {
	if s.closed.Load() {
		return false, &storage.ClosedError{}
	}
	if len(key) == 0 {
		return false, storage.ErrEmptyKey
	}

	s.mu.RLock()
	_, ok := s.tree.Get(entry{key: key})
	s.mu.RUnlock()

	return ok, nil
}

// Batch creates a new write batch for atomic multi-key operations. A closed
// store returns a batch that reports the closure from every operation.
func (s *MemoryStorage) Batch() storage.Batch {
	if s.closed.Load() {
		return storage.NewClosedBatch()
	}
	return newBatch(s)
}

// NewIterator creates an iterator for scanning key ranges.
// The iterator operates on a CoW snapshot of the btree taken at creation time,
// providing isolation from concurrent mutations. A nil opts creates an
// unbounded forward iterator. A closed store returns an iterator that reports
// the closure through Error.
//
// Copy() requires a write lock because btree.IsoCopy mutates the source
// tree's isolation ID even with NoLocks: true.
func (s *MemoryStorage) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	if s.closed.Load() {
		return storage.NewClosedIterator()
	}

	s.mu.Lock()
	snapshot := s.tree.Copy()
	s.mu.Unlock()

	return newIterator(snapshot, opts)
}

// Snapshot creates a point-in-time snapshot for consistent reads.
//
// Copy() requires a write lock because btree.IsoCopy mutates the source
// tree's isolation ID even with NoLocks: true.
func (s *MemoryStorage) Snapshot() (storage.Snapshot, error) {
	if s.closed.Load() {
		return nil, &storage.ClosedError{}
	}

	s.mu.Lock()
	snapshot := s.tree.Copy()
	s.mu.Unlock()

	return newSnapshot(snapshot), nil
}

// Close releases all resources held by the storage. Subsequent operations
// return ClosedError. Calling Close on an already-closed storage is a no-op.
func (s *MemoryStorage) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	return nil
}
