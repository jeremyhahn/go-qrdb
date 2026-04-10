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
	"sync/atomic"

	"github.com/tidwall/btree"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// memorySnapshot implements storage.Snapshot using a CoW btree clone.
// The snapshot provides a consistent point-in-time view of the storage
// that is isolated from subsequent mutations.
type memorySnapshot struct {
	tree   *btree.BTreeG[entry]
	closed atomic.Bool
}

// newSnapshot wraps a CoW btree clone as a storage.Snapshot.
func newSnapshot(tree *btree.BTreeG[entry]) storage.Snapshot {
	return &memorySnapshot{tree: tree}
}

// Get retrieves the value for the given key from the snapshot. Returns a
// defensive copy. Returns nil, nil when the key does not exist.
func (s *memorySnapshot) Get(key []byte) ([]byte, error) {
	if s.closed.Load() {
		return nil, &storage.SnapshotError{Op: "get", Err: &storage.ClosedError{}}
	}
	if len(key) == 0 {
		return nil, &storage.SnapshotError{Op: "get", Err: storage.ErrEmptyKey}
	}

	item, ok := s.tree.Get(entry{key: key})
	if !ok {
		return nil, nil
	}

	result := make([]byte, len(item.value))
	copy(result, item.value)
	return result, nil
}

// NewIterator creates an iterator over the snapshot. A nil opts creates
// an unbounded forward iterator. The iterator uses a Copy of the snapshot's
// tree to ensure iterator isolation.
func (s *memorySnapshot) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	if s.closed.Load() {
		return newClosedIterator()
	}
	// The snapshot tree is never mutated, but we Copy() for iterator safety
	// since the btree iterator requires exclusive ownership semantics.
	snapshot := s.tree.Copy()
	return newIterator(snapshot, opts)
}

// Close releases resources held by the snapshot. Calling Close on an
// already-closed snapshot is a no-op.
func (s *memorySnapshot) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	return nil
}

// newClosedIterator returns an iterator that reports closed state.
// Used when creating an iterator on a closed snapshot.
func newClosedIterator() storage.Iterator {
	it := &memoryIterator{
		opts: &storage.IteratorOptions{},
	}
	it.closed.Store(true)
	return it
}
