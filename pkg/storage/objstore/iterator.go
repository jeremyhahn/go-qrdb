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

package objstore

import (
	"bytes"
	"sort"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// kvEntry holds a key-value pair loaded from the object store.
type kvEntry struct {
	key   []byte
	value []byte
}

// kvIterator implements storage.Iterator by eagerly loading all matching keys
// (and optionally their values) from the object store at creation time, sorting
// them, and iterating in order. This trades memory and up-front cost for a
// simple, provider-agnostic scan — appropriate for the blob/backup workloads
// this backend targets.
type kvIterator struct {
	entries     []kvEntry
	pos         int
	valid       bool
	opts        *storage.IteratorOptions
	err         error
	initialized bool
}

// loadEntries lists keys within the iterator bounds, optionally reading values,
// and returns them sorted ascending. The caller applies Reverse.
func loadEntries(e *Engine, opts *storage.IteratorOptions) ([]kvEntry, error) {
	all, err := e.scan(nil, !opts.OnlyKeys)
	if err != nil {
		return nil, err
	}
	entries := all[:0]
	for _, entry := range all {
		if len(opts.LowerBound) > 0 && bytes.Compare(entry.key, opts.LowerBound) < 0 {
			continue
		}
		if len(opts.UpperBound) > 0 && bytes.Compare(entry.key, opts.UpperBound) >= 0 {
			continue
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare(entries[i].key, entries[j].key) < 0
	})
	return entries, nil
}

// newIterator creates a new iterator over the engine, eagerly loading matching
// entries into memory for sorted iteration.
func newIterator(e *Engine, opts *storage.IteratorOptions) *kvIterator {
	if opts == nil {
		opts = &storage.IteratorOptions{}
	}
	it := &kvIterator{pos: -1, opts: opts}
	entries, err := loadEntries(e, opts)
	if err != nil {
		it.err = err
		return it
	}
	if opts.Reverse {
		reverse(entries)
	}
	it.entries = entries
	return it
}

// newIteratorFromEntries creates an iterator from pre-loaded entries, used by
// snapshots to avoid re-reading the object store.
func newIteratorFromEntries(entries []kvEntry, opts *storage.IteratorOptions) *kvIterator {
	if opts == nil {
		opts = &storage.IteratorOptions{}
	}
	return &kvIterator{entries: entries, pos: -1, opts: opts}
}

func reverse(entries []kvEntry) {
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
}

// SeekToFirst positions the iterator at the first entry.
func (it *kvIterator) SeekToFirst() bool {
	it.initialized = true
	if len(it.entries) == 0 {
		it.valid = false
		return false
	}
	it.pos = 0
	it.valid = true
	return true
}

// SeekToLast positions the iterator at the last entry.
func (it *kvIterator) SeekToLast() bool {
	it.initialized = true
	if len(it.entries) == 0 {
		it.valid = false
		return false
	}
	it.pos = len(it.entries) - 1
	it.valid = true
	return true
}

// Seek positions the iterator at the first key >= target.
func (it *kvIterator) Seek(target []byte) bool {
	it.initialized = true
	idx := sort.Search(len(it.entries), func(i int) bool {
		return bytes.Compare(it.entries[i].key, target) >= 0
	})
	if idx >= len(it.entries) {
		it.valid = false
		return false
	}
	it.pos = idx
	it.valid = true
	return true
}

// Next advances the iterator to the next entry.
func (it *kvIterator) Next() bool {
	if !it.initialized {
		return false
	}
	it.pos++
	if it.pos >= len(it.entries) {
		it.valid = false
		return false
	}
	it.valid = true
	return true
}

// Prev moves the iterator to the previous entry.
func (it *kvIterator) Prev() bool {
	if !it.initialized {
		return false
	}
	it.pos--
	if it.pos < 0 {
		it.valid = false
		return false
	}
	it.valid = true
	return true
}

// Valid reports whether the iterator is positioned at a valid entry.
func (it *kvIterator) Valid() bool { return it.valid }

// Key returns the current key, or nil when invalid.
func (it *kvIterator) Key() []byte {
	if !it.valid {
		return nil
	}
	return it.entries[it.pos].key
}

// Value returns the current value, or nil when invalid or OnlyKeys is set.
func (it *kvIterator) Value() []byte {
	if !it.valid {
		return nil
	}
	return it.entries[it.pos].value
}

// Error returns any accumulated error encountered during iteration.
func (it *kvIterator) Error() error { return it.err }

// Close releases resources held by the iterator.
func (it *kvIterator) Close() error { return nil }

// kvSnapshot implements storage.Snapshot by capturing all key-value pairs from
// the engine at creation time, serving subsequent reads from the in-memory copy.
type kvSnapshot struct {
	entries []kvEntry
	closed  atomic.Bool
}

// newSnapshot reads all entries from the engine into a point-in-time snapshot.
func newSnapshot(e *Engine) (*kvSnapshot, error) {
	entries, err := loadEntries(e, &storage.IteratorOptions{})
	if err != nil {
		return nil, err
	}
	return &kvSnapshot{entries: entries}, nil
}

// Get retrieves the value for key from the snapshot. Returns nil, nil when the
// key does not exist.
func (s *kvSnapshot) Get(key []byte) ([]byte, error) {
	if s.closed.Load() {
		return nil, &storage.SnapshotError{Op: "get", Err: &storage.ClosedError{}}
	}
	if len(key) == 0 {
		return nil, &storage.SnapshotError{Op: "get", Err: storage.ErrEmptyKey}
	}
	idx := sort.Search(len(s.entries), func(i int) bool {
		return bytes.Compare(s.entries[i].key, key) >= 0
	})
	if idx < len(s.entries) && bytes.Equal(s.entries[idx].key, key) {
		cp := make([]byte, len(s.entries[idx].value))
		copy(cp, s.entries[idx].value)
		return cp, nil
	}
	return nil, nil
}

// NewIterator creates an iterator over the snapshot entries filtered by opts.
func (s *kvSnapshot) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	if s.closed.Load() {
		return newIteratorFromEntries(nil, opts)
	}
	if opts == nil {
		opts = &storage.IteratorOptions{}
	}
	var filtered []kvEntry
	for _, e := range s.entries {
		if len(opts.LowerBound) > 0 && bytes.Compare(e.key, opts.LowerBound) < 0 {
			continue
		}
		if len(opts.UpperBound) > 0 && bytes.Compare(e.key, opts.UpperBound) >= 0 {
			continue
		}
		filtered = append(filtered, e)
	}
	if opts.Reverse {
		reverse(filtered)
	}
	return newIteratorFromEntries(filtered, opts)
}

// Close releases resources held by the snapshot. Calling Close on an
// already-closed snapshot is a no-op.
func (s *kvSnapshot) Close() error {
	s.closed.CompareAndSwap(false, true)
	return nil
}
