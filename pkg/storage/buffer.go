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

package storage

import (
	"bytes"
	"sort"
	"sync"
	"sync/atomic"
)

// BufferedOp is a single pending write held by a BufferedBatch. Deleted marks
// a pending removal, in which case Value is nil.
type BufferedOp struct {
	Key     []byte
	Value   []byte
	Deleted bool
}

// ApplyFunc commits a batch's ordered operations to a backend. Implementations
// must apply the operations in the given order; the backend decides how much
// atomicity it can offer.
type ApplyFunc func(ops []BufferedOp) error

// BufferedBatch implements Batch for backends that have no engine-native
// batch. It accumulates writes in memory, serves reads through them, and hands
// the ordered operation list to the backend's ApplyFunc on Commit.
//
// It exists so the read-your-own-writes contract of Batch has exactly one
// implementation shared by every buffering backend. Re-deriving overlay
// semantics per backend is how a backend ends up quietly serving committed-only
// reads, which is precisely what makes a Raft apply loop's entry grouping
// observable in the resulting state.
//
// Reads materialize the requested range: NewIterator merges the pending writes
// with a full forward pass of the base Reader over the same bounds and
// iterates the result. That mirrors what the file and object-store backends
// already do for ordinary iteration, and it keeps the merge obviously correct.
// Backends with an engine-native readable batch (PebbleDB) use that instead and
// never pay for this.
//
// Thread safety: all methods are safe for concurrent use.
type BufferedBatch struct {
	base   Reader
	apply  ApplyFunc
	mu     sync.Mutex
	ops    []BufferedOp
	latest map[string]int
	closed atomic.Bool
}

// Compile-time interface assertion.
var _ Batch = (*BufferedBatch)(nil)

// NewBufferedBatch creates a batch that reads through base and commits by
// calling apply with its ordered operations.
func NewBufferedBatch(base Reader, apply ApplyFunc) *BufferedBatch {
	return &BufferedBatch{
		base:   base,
		apply:  apply,
		latest: make(map[string]int),
	}
}

// Set records a pending write. The key and value are defensively copied so the
// batch never aliases the caller's slices.
func (b *BufferedBatch) Set(key, value []byte) error {
	if b.closed.Load() {
		return &BatchError{Op: "set", Err: &ClosedError{}}
	}
	if len(key) == 0 {
		return &BatchError{Op: "set", Err: ErrEmptyKey}
	}
	b.record(BufferedOp{Key: cloneBytes(key), Value: cloneBytes(value)})
	return nil
}

// Delete records a pending removal. The key is defensively copied.
func (b *BufferedBatch) Delete(key []byte) error {
	if b.closed.Load() {
		return &BatchError{Op: "delete", Err: &ClosedError{}}
	}
	if len(key) == 0 {
		return &BatchError{Op: "delete", Err: ErrEmptyKey}
	}
	b.record(BufferedOp{Key: cloneBytes(key), Deleted: true})
	return nil
}

// record appends op and remembers it as the key's latest pending write.
func (b *BufferedBatch) record(op BufferedOp) {
	b.mu.Lock()
	b.latest[string(op.Key)] = len(b.ops)
	b.ops = append(b.ops, op)
	b.mu.Unlock()
}

// Get returns the value for key as the batch sees it: its own latest pending
// write when it has one (nil for a pending delete), otherwise the committed
// value from the base reader.
func (b *BufferedBatch) Get(key []byte) ([]byte, error) {
	if b.closed.Load() {
		return nil, &BatchError{Op: "get", Err: &ClosedError{}}
	}

	b.mu.Lock()
	idx, pending := b.latest[string(key)]
	var op BufferedOp
	if pending {
		op = b.ops[idx]
	}
	b.mu.Unlock()

	if pending {
		if op.Deleted {
			return nil, nil
		}
		return cloneBytes(op.Value), nil
	}
	return b.base.Get(key)
}

// Len returns the number of buffered operations. It is used by backends whose
// Commit needs to know whether there is anything to do.
func (b *BufferedBatch) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.ops)
}

// NewIterator returns an iterator over the batch's pending writes layered on
// the base reader's committed contents. A pending delete hides the committed
// value; a pending write shadows it.
func (b *BufferedBatch) NewIterator(opts *IteratorOptions) Iterator {
	if b.closed.Load() {
		return &sliceIterator{err: &BatchError{Op: "iterate", Err: &ClosedError{}}}
	}
	return newOverlayIterator(b.pendingSnapshot(), b.base, opts)
}

// pendingSnapshot returns the batch's effective pending state, one entry per
// key, ordered by key.
func (b *BufferedBatch) pendingSnapshot() []BufferedOp {
	b.mu.Lock()
	effective := make([]BufferedOp, 0, len(b.latest))
	for _, idx := range b.latest {
		effective = append(effective, b.ops[idx])
	}
	b.mu.Unlock()

	sort.Slice(effective, func(i, j int) bool {
		return bytes.Compare(effective[i].Key, effective[j].Key) < 0
	})
	return effective
}

// Commit applies the buffered operations through the backend's ApplyFunc. The
// batch is closed afterwards whether or not the apply succeeded, since a
// partially applied backend must not be replayed.
func (b *BufferedBatch) Commit() error {
	if b.closed.Load() {
		return &BatchError{Op: "commit", Err: &ClosedError{}}
	}

	b.mu.Lock()
	ops := b.ops
	b.ops = nil
	b.latest = make(map[string]int)
	b.mu.Unlock()

	b.closed.Store(true)

	if err := b.apply(ops); err != nil {
		return &BatchError{Op: "commit", Err: err}
	}
	return nil
}

// Rollback discards the buffered operations without applying them.
func (b *BufferedBatch) Rollback() error {
	if b.closed.Load() {
		return &BatchError{Op: "rollback", Err: &ClosedError{}}
	}
	b.discard()
	b.closed.Store(true)
	return nil
}

// Close releases the batch, discarding any operations that were neither
// committed nor rolled back. Closing an already-closed batch is a no-op.
func (b *BufferedBatch) Close() error {
	if !b.closed.CompareAndSwap(false, true) {
		return nil
	}
	b.discard()
	return nil
}

// discard drops all buffered operations.
func (b *BufferedBatch) discard() {
	b.mu.Lock()
	b.ops = nil
	b.latest = make(map[string]int)
	b.mu.Unlock()
}

// cloneBytes returns a defensive copy of b. The result is always non-nil, so a
// zero-length value stays distinguishable from a missing one.
func cloneBytes(b []byte) []byte {
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp
}

// ---------------------------------------------------------------------------
// Overlay iteration
// ---------------------------------------------------------------------------

// sliceEntry is one materialized key-value pair.
type sliceEntry struct {
	key   []byte
	value []byte
}

// newOverlayIterator merges pending (sorted by key, one entry per key) with a
// forward pass of base over the same bounds and returns an iterator over the
// result. Entries whose pending op is a delete are omitted.
func newOverlayIterator(pending []BufferedOp, base Reader, opts *IteratorOptions) Iterator {
	if opts == nil {
		opts = &IteratorOptions{}
	}

	shadowed := make(map[string]struct{}, len(pending))
	for _, op := range pending {
		shadowed[string(op.Key)] = struct{}{}
	}

	entries := make([]sliceEntry, 0, len(pending))

	// The base pass is always forward; ordering is applied to the merged
	// result so both sides agree on it.
	baseOpts := *opts
	baseOpts.Reverse = false
	it := base.NewIterator(&baseOpts)
	defer func() { _ = it.Close() }()

	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		if _, hidden := shadowed[string(it.Key())]; hidden {
			continue
		}
		e := sliceEntry{key: cloneBytes(it.Key())}
		if v := it.Value(); v != nil {
			e.value = cloneBytes(v)
		}
		entries = append(entries, e)
	}
	if err := it.Error(); err != nil {
		return &sliceIterator{err: err}
	}

	for _, op := range pending {
		if op.Deleted || !withinBounds(op.Key, opts) {
			continue
		}
		e := sliceEntry{key: op.Key}
		if !opts.OnlyKeys {
			e.value = op.Value
		}
		entries = append(entries, e)
	}

	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare(entries[i].key, entries[j].key) < 0
	})
	if opts.Reverse {
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
	}

	return &sliceIterator{entries: entries, pos: -1}
}

// withinBounds reports whether key falls inside the iterator's
// [LowerBound, UpperBound) range. An empty bound is unbounded on that side.
func withinBounds(key []byte, opts *IteratorOptions) bool {
	if len(opts.LowerBound) > 0 && bytes.Compare(key, opts.LowerBound) < 0 {
		return false
	}
	if len(opts.UpperBound) > 0 && bytes.Compare(key, opts.UpperBound) >= 0 {
		return false
	}
	return true
}

// sliceIterator iterates a materialized, pre-ordered slice of entries. Its
// positioning semantics match the file backend's iterator, which is also
// slice-backed, so backends that share it stay behaviourally identical.
//
// A non-nil err makes the iterator permanently invalid and reports the failure
// through Error, so a construction failure surfaces rather than reading as an
// empty range.
type sliceIterator struct {
	entries     []sliceEntry
	pos         int
	valid       bool
	initialized bool
	err         error
}

// SeekToFirst positions the iterator at the first entry.
func (it *sliceIterator) SeekToFirst() bool {
	it.initialized = true
	if it.err != nil || len(it.entries) == 0 {
		it.valid = false
		return false
	}
	it.pos = 0
	it.valid = true
	return true
}

// SeekToLast positions the iterator at the last entry.
func (it *sliceIterator) SeekToLast() bool {
	it.initialized = true
	if it.err != nil || len(it.entries) == 0 {
		it.valid = false
		return false
	}
	it.pos = len(it.entries) - 1
	it.valid = true
	return true
}

// Seek positions the iterator at the first entry whose key is at or after
// target in the slice's order.
func (it *sliceIterator) Seek(target []byte) bool {
	it.initialized = true
	if it.err != nil {
		it.valid = false
		return false
	}
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

// Next advances the iterator.
func (it *sliceIterator) Next() bool {
	if !it.initialized || it.err != nil {
		it.valid = false
		return false
	}
	it.pos++
	it.valid = it.pos >= 0 && it.pos < len(it.entries)
	return it.valid
}

// Prev moves the iterator back one entry.
func (it *sliceIterator) Prev() bool {
	if !it.initialized || it.err != nil {
		it.valid = false
		return false
	}
	it.pos--
	it.valid = it.pos >= 0 && it.pos < len(it.entries)
	return it.valid
}

// Valid reports whether the iterator is positioned at an entry.
func (it *sliceIterator) Valid() bool { return it.valid }

// Key returns the current key, or nil when the iterator is not positioned.
func (it *sliceIterator) Key() []byte {
	if !it.valid {
		return nil
	}
	return it.entries[it.pos].key
}

// Value returns the current value, or nil when the iterator is not positioned.
func (it *sliceIterator) Value() []byte {
	if !it.valid {
		return nil
	}
	return it.entries[it.pos].value
}

// Error returns the failure that invalidated the iterator, if any.
func (it *sliceIterator) Error() error { return it.err }

// Close releases the iterator. Materialized iterators hold no resources.
func (it *sliceIterator) Close() error { return nil }
