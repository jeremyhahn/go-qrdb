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
	"sync/atomic"

	"github.com/tidwall/btree"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// memoryIterator implements storage.Iterator over a btree snapshot.
// The iterator operates on a CoW copy of the btree taken at creation time,
// providing isolation from concurrent mutations. Key() and Value() return
// defensive copies to prevent aliasing.
type memoryIterator struct {
	tree        *btree.BTreeG[entry]
	iter        btree.IterG[entry]
	opts        *storage.IteratorOptions
	valid       bool
	closed      atomic.Bool
	initialized bool
}

// newIterator creates a new iterator over the given btree snapshot.
// A nil opts creates an unbounded forward iterator.
func newIterator(tree *btree.BTreeG[entry], opts *storage.IteratorOptions) storage.Iterator {
	if opts == nil {
		opts = &storage.IteratorOptions{}
	}
	iter := tree.Iter()
	return &memoryIterator{
		tree: tree,
		iter: iter,
		opts: opts,
	}
}

// SeekToFirst positions the iterator at the first key within bounds.
func (it *memoryIterator) SeekToFirst() bool {
	if it.closed.Load() {
		return false
	}
	it.initialized = true

	if it.opts.LowerBound != nil {
		it.valid = it.iter.Seek(entry{key: it.opts.LowerBound})
	} else {
		it.valid = it.iter.First()
	}

	if it.valid && !it.inUpperBound() {
		it.valid = false
	}
	return it.valid
}

// SeekToLast positions the iterator at the last key within bounds.
func (it *memoryIterator) SeekToLast() bool {
	if it.closed.Load() {
		return false
	}
	it.initialized = true

	if it.opts.UpperBound != nil {
		// Seek to UpperBound, which is exclusive. If we land exactly on it
		// or past it, move backward.
		found := it.iter.Seek(entry{key: it.opts.UpperBound})
		if found {
			// We are at or past UpperBound. Since UpperBound is exclusive,
			// the last valid entry is the one before this position.
			it.valid = it.iter.Prev()
		} else {
			// Seek went past the end, so last item in tree is the candidate.
			it.valid = it.iter.Last()
		}
	} else {
		it.valid = it.iter.Last()
	}

	if it.valid && !it.inLowerBound() {
		it.valid = false
	}
	// Also re-check upper bound for safety.
	if it.valid && !it.inUpperBound() {
		it.valid = false
	}
	return it.valid
}

// Seek positions the iterator at the first key >= target within bounds.
func (it *memoryIterator) Seek(target []byte) bool {
	if it.closed.Load() {
		return false
	}
	it.initialized = true

	it.valid = it.iter.Seek(entry{key: target})

	// If the result is below our LowerBound, advance.
	if it.valid && !it.inLowerBound() {
		it.valid = it.iter.Seek(entry{key: it.opts.LowerBound})
	}

	if it.valid && !it.inUpperBound() {
		it.valid = false
	}
	return it.valid
}

// Next advances the iterator to the next key within bounds.
func (it *memoryIterator) Next() bool {
	if it.closed.Load() || !it.initialized {
		return false
	}

	it.valid = it.iter.Next()
	if it.valid && !it.inUpperBound() {
		it.valid = false
	}
	return it.valid
}

// Prev moves the iterator to the previous key within bounds.
func (it *memoryIterator) Prev() bool {
	if it.closed.Load() || !it.initialized {
		return false
	}

	it.valid = it.iter.Prev()
	if it.valid && !it.inLowerBound() {
		it.valid = false
	}
	return it.valid
}

// Valid reports whether the iterator is positioned at a valid entry.
func (it *memoryIterator) Valid() bool {
	if it.closed.Load() {
		return false
	}
	return it.valid
}

// Key returns a defensive copy of the current key. Returns nil when the
// iterator is not positioned at a valid entry.
func (it *memoryIterator) Key() []byte {
	if it.closed.Load() || !it.valid {
		return nil
	}
	item := it.iter.Item()
	out := make([]byte, len(item.key))
	copy(out, item.key)
	return out
}

// Value returns a defensive copy of the current value. Returns nil when the
// iterator is not positioned at a valid entry or when OnlyKeys is set.
func (it *memoryIterator) Value() []byte {
	if it.closed.Load() || !it.valid {
		return nil
	}
	if it.opts.OnlyKeys {
		return nil
	}
	item := it.iter.Item()
	out := make([]byte, len(item.value))
	copy(out, item.value)
	return out
}

// Error returns any accumulated error encountered during iteration.
// The memory-backed iterator does not produce errors during normal operation.
func (it *memoryIterator) Error() error {
	return nil
}

// Close releases resources held by the iterator. Calling Close on an
// already-closed iterator is a no-op.
func (it *memoryIterator) Close() error {
	if !it.closed.CompareAndSwap(false, true) {
		return nil
	}
	it.iter.Release()
	it.valid = false
	return nil
}

// inUpperBound checks whether the current item's key is below the exclusive
// upper bound. Returns true when no upper bound is set.
func (it *memoryIterator) inUpperBound() bool {
	if it.opts.UpperBound == nil {
		return true
	}
	item := it.iter.Item()
	return bytes.Compare(item.key, it.opts.UpperBound) < 0
}

// inLowerBound checks whether the current item's key is at or above the
// inclusive lower bound. Returns true when no lower bound is set.
func (it *memoryIterator) inLowerBound() bool {
	if it.opts.LowerBound == nil {
		return true
	}
	item := it.iter.Item()
	return bytes.Compare(item.key, it.opts.LowerBound) >= 0
}
