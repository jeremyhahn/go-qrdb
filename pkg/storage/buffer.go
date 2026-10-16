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
// a pending removal, in which case Value is nil. Range marks a range delete of
// [Key, RangeEnd), with Deleted also set; an empty RangeEnd is unbounded.
type BufferedOp struct {
	Key      []byte
	Value    []byte
	RangeEnd []byte
	Deleted  bool
	Range    bool
}

// ApplyFunc commits a batch's ordered operations to a backend. Implementations
// must leave the state applying the operations in the given order would: a
// later operation on a key wins over an earlier one. Operations on distinct
// keys may be applied in any order or concurrently. The backend decides how
// much atomicity it can offer.
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
// Reads merge rather than copy: NewIterator resolves the pending writes into a
// sorted list of the keys the batch wrote and the union of its range deletes,
// and merges that with a live iterator over the base Reader (overlayIterator),
// so a seek or a short scan inside a large keyspace costs what it costs on the
// base. Backends with an engine-native readable batch (PebbleDB) use that
// instead.
//
// Thread safety: all methods are safe for concurrent use.
type BufferedBatch struct {
	base   Reader
	apply  ApplyFunc
	mu     sync.Mutex
	ops    []BufferedOp
	latest map[string]int
	// ranges holds the index in ops of every pending range delete, in
	// buffered order, so Get checks only those rather than every op.
	ranges []int
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

// DeleteRange buffers the removal of every key in [start, end); a nil end
// reaches the end of the keyspace. It hides committed keys and keys written
// earlier in the batch, but not keys written after it.
func (b *BufferedBatch) DeleteRange(start, end []byte) error {
	if b.closed.Load() {
		return &BatchError{Op: "delete_range", Err: &ClosedError{}}
	}
	if err := ValidateRange(start, end); err != nil {
		return &BatchError{Op: "delete_range", Err: err}
	}
	b.record(BufferedOp{Key: cloneBytes(start), RangeEnd: cloneBytes(end), Deleted: true, Range: true})
	return nil
}

// record appends op and, for a single-key op, remembers it as the key's
// latest pending write.
func (b *BufferedBatch) record(op BufferedOp) {
	b.mu.Lock()
	if op.Range {
		b.ranges = append(b.ranges, len(b.ops))
	} else {
		b.latest[string(op.Key)] = len(b.ops)
	}
	b.ops = append(b.ops, op)
	b.mu.Unlock()
}

// Get returns the value for key as the batch sees it: its own latest pending
// write when it has one (nil for a pending delete or a pending range delete
// covering key), otherwise the committed value from the base reader.
//
// It costs one map lookup plus one check per pending range delete, never a
// pass over every pending op: an apply that reads each key it is about to
// write -- a batch, a range delete, an MVCC delete reading the key's meta --
// would otherwise be quadratic in the batch's size.
func (b *BufferedBatch) Get(key []byte) ([]byte, error) {
	if b.closed.Load() {
		return nil, &BatchError{Op: "get", Err: &ClosedError{}}
	}

	b.mu.Lock()
	value, decided := b.pendingGetLocked(key)
	b.mu.Unlock()
	if decided {
		return value, nil
	}
	return b.base.Get(key)
}

// pendingGetLocked resolves key against the pending ops: decided is false
// when no pending op touches key, and otherwise value is what the newest one
// left (nil when it deleted key). b.mu must be held.
func (b *BufferedBatch) pendingGetLocked(key []byte) (value []byte, decided bool) {
	latest, written := b.latest[string(key)]
	if !written {
		return nil, b.rangeDeletedLocked(key, -1)
	}
	if op := b.ops[latest]; !op.Deleted && !b.rangeDeletedLocked(key, latest) {
		return cloneBytes(op.Value), true
	}
	return nil, true
}

// rangeDeletedLocked reports whether a range delete buffered after the op at
// index after covers key; after is -1 to ask about every range delete. b.mu
// must be held.
func (b *BufferedBatch) rangeDeletedLocked(key []byte, after int) bool {
	for i := len(b.ranges) - 1; i >= 0 && b.ranges[i] > after; i-- {
		if op := b.ops[b.ranges[i]]; inRange(key, op.Key, op.RangeEnd) {
			return true
		}
	}
	return false
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
// value; a pending write shadows it; a pending range delete hides every
// committed key it covers and every key written before it in the batch.
//
// The pending writes are captured when the iterator is created; the base is
// read lazily through its own iterator (see overlayIterator).
func (b *BufferedBatch) NewIterator(opts *IteratorOptions) Iterator {
	if b.closed.Load() {
		return &sliceIterator{err: &BatchError{Op: "iterate", Err: &ClosedError{}}}
	}
	if opts == nil {
		opts = &IteratorOptions{}
	}
	b.mu.Lock()
	points, spans := b.pendingViewLocked(opts)
	b.mu.Unlock()
	return newOverlayIterator(points, spans, b.base, opts)
}

// pendingViewLocked resolves the pending ops into what an iterator merges with
// the base: the batch's final word on each key it wrote inside opts' bounds,
// in key order, and the union of its range deletes. It costs a sort of the
// keys the batch wrote, never a pass over the base. b.mu must be held.
func (b *BufferedBatch) pendingViewLocked(opts *IteratorOptions) ([]pendingPoint, []tombstoneSpan) {
	points := make([]pendingPoint, 0, len(b.latest))
	for _, idx := range b.latest {
		op := b.ops[idx]
		if !withinBounds(op.Key, opts) {
			continue
		}
		p := pendingPoint{key: op.Key, deleted: op.Deleted || b.rangeDeletedLocked(op.Key, idx)}
		if !p.deleted && !opts.OnlyKeys {
			p.value = op.Value
		}
		points = append(points, p)
	}
	sort.Slice(points, func(i, j int) bool {
		return bytes.Compare(points[i].key, points[j].key) < 0
	})

	spans := make([]tombstoneSpan, 0, len(b.ranges))
	for _, r := range b.ranges {
		spans = append(spans, tombstoneSpan{start: b.ops[r].Key, end: b.ops[r].RangeEnd})
	}
	return points, mergeSpans(spans)
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
	b.ranges = nil
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
	b.ranges = nil
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

// inRange reports whether key falls in [start, end); an empty end is
// unbounded.
func inRange(key, start, end []byte) bool {
	if bytes.Compare(key, start) < 0 {
		return false
	}
	return len(end) == 0 || bytes.Compare(key, end) < 0
}

// pendingPoint is the batch's final word on one key it wrote: the value of its
// latest write, or deleted when that write was a delete or a range delete
// buffered after it covers the key.
type pendingPoint struct {
	key     []byte
	value   []byte
	deleted bool
}

// tombstoneSpan is a union of pending range deletes, [start, end); an empty end
// is unbounded. Every pending range delete hides every committed key it covers,
// since the base predates all of the batch's ops.
type tombstoneSpan struct {
	start []byte
	end   []byte
}

// mergeSpans sorts spans by start and coalesces those that overlap or touch, so
// a key is covered by at most one span and a scan skips each union with one
// seek. It reorders spans in place.
func mergeSpans(spans []tombstoneSpan) []tombstoneSpan {
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(i, j int) bool {
		return bytes.Compare(spans[i].start, spans[j].start) < 0
	})
	merged := spans[:1]
	for _, s := range spans[1:] {
		last := &merged[len(merged)-1]
		if len(last.end) == 0 {
			// An unbounded span covers everything after it.
			break
		}
		if bytes.Compare(s.start, last.end) > 0 {
			merged = append(merged, s)
			continue
		}
		if len(s.end) == 0 || bytes.Compare(s.end, last.end) > 0 {
			last.end = s.end
		}
	}
	return merged
}

// coveringSpan returns the span of spans, which mergeSpans produced, that
// covers key.
func coveringSpan(spans []tombstoneSpan, key []byte) (tombstoneSpan, bool) {
	i := sort.Search(len(spans), func(i int) bool {
		return bytes.Compare(spans[i].start, key) > 0
	}) - 1
	if i < 0 || !inRange(key, spans[i].start, spans[i].end) {
		return tombstoneSpan{}, false
	}
	return spans[i], true
}

// overlayIterator merges a BufferedBatch's pending view with a live iterator
// over the base, as an LSM merges a memtable with the levels below it: the
// base is read lazily, a pending point shadows the base entry with its key, a
// deleted point hides it, and a base key inside a tombstone span is skipped
// with one seek past the span. A scan costs what the same scan of the base
// costs plus the pending keys it meets -- never a pass over every committed key
// in the bounds.
//
// The base iterator is always opened ascending; the merge walks it in either
// direction and maps the caller's order onto that walk. Under Reverse,
// SeekToFirst lands on the largest key, Next moves to smaller keys, and Seek
// lands on the largest key at or below its target, as the object-store
// backend's iterator does.
//
// Next and Prev on an iterator that is not positioned return false.
type overlayIterator struct {
	base    Iterator
	points  []pendingPoint
	spans   []tombstoneSpan
	reverse bool

	// baseValid and baseKey describe where base is positioned. pi indexes
	// points. When forward is true both sit at the smallest entry at or
	// after the current key; otherwise at the largest at or before it.
	baseValid bool
	baseKey   []byte
	pi        int
	forward   bool

	valid    bool
	key      []byte
	value    []byte
	fromBase bool
	err      error
	closed   bool
}

// newOverlayIterator returns an iterator over points and spans layered on
// base, within opts' bounds.
func newOverlayIterator(points []pendingPoint, spans []tombstoneSpan, base Reader, opts *IteratorOptions) Iterator {
	baseOpts := *opts
	baseOpts.Reverse = false
	return &overlayIterator{
		base:    base.NewIterator(&baseOpts),
		points:  points,
		spans:   spans,
		reverse: opts.Reverse,
	}
}

// SeekToFirst positions the iterator at the first entry in iteration order.
func (it *overlayIterator) SeekToFirst() bool {
	if it.reverse {
		return it.last()
	}
	return it.first()
}

// SeekToLast positions the iterator at the last entry in iteration order.
func (it *overlayIterator) SeekToLast() bool {
	if it.reverse {
		return it.first()
	}
	return it.last()
}

// Seek positions the iterator at the first entry at or after target in
// iteration order: the smallest key >= target, or under Reverse the largest
// key <= target.
func (it *overlayIterator) Seek(target []byte) bool {
	if it.reverse {
		return it.seekLE(target)
	}
	return it.seekGE(target)
}

// Next advances the iterator in iteration order.
func (it *overlayIterator) Next() bool {
	if it.reverse {
		return it.prev()
	}
	return it.next()
}

// Prev moves the iterator back in iteration order.
func (it *overlayIterator) Prev() bool {
	if it.reverse {
		return it.next()
	}
	return it.prev()
}

// Valid reports whether the iterator is positioned at an entry.
func (it *overlayIterator) Valid() bool { return it.valid && !it.closed }

// Key returns the current key, or nil when the iterator is not positioned.
func (it *overlayIterator) Key() []byte {
	if !it.Valid() {
		return nil
	}
	return it.key
}

// Value returns the current value, or nil when the iterator is not positioned
// or was opened with OnlyKeys.
func (it *overlayIterator) Value() []byte {
	if !it.Valid() {
		return nil
	}
	if it.fromBase {
		return it.base.Value()
	}
	return it.value
}

// Error returns the base's failure, if any. A failure makes every later
// positioning report false, so it never reads as the end of the range.
func (it *overlayIterator) Error() error {
	if it.err != nil {
		return it.err
	}
	return it.base.Error()
}

// Close releases the base iterator. Closing twice is a no-op.
func (it *overlayIterator) Close() error {
	if it.closed {
		return nil
	}
	it.closed = true
	it.valid = false
	return it.base.Close()
}

// first positions at the smallest visible key.
func (it *overlayIterator) first() bool {
	if it.closed {
		return false
	}
	it.setBase(it.base.SeekToFirst())
	it.pi = 0
	it.forward = true
	return it.settleForward()
}

// last positions at the largest visible key.
func (it *overlayIterator) last() bool {
	if it.closed {
		return false
	}
	it.setBase(it.base.SeekToLast())
	it.pi = len(it.points) - 1
	it.forward = false
	return it.settleBackward()
}

// seekGE positions at the smallest visible key >= target.
func (it *overlayIterator) seekGE(target []byte) bool {
	if it.closed {
		return false
	}
	it.setBase(it.base.Seek(target))
	it.pi = it.pointAtOrAfter(target)
	it.forward = true
	return it.settleForward()
}

// seekLE positions at the largest visible key <= target.
func (it *overlayIterator) seekLE(target []byte) bool {
	if it.closed {
		return false
	}
	it.seekBaseBackward(target, true)
	it.pi = it.pointAfter(target) - 1
	it.forward = false
	return it.settleBackward()
}

// next moves to the smallest visible key above the current one.
func (it *overlayIterator) next() bool {
	if it.closed || !it.valid {
		return false
	}
	cur := it.key
	if !it.forward {
		// The base may reuse the current key's buffer once it moves.
		cur = cloneBytes(cur)
		it.setBase(it.base.Seek(cur))
		it.pi = it.pointAtOrAfter(cur)
		it.forward = true
	}
	onPoint := it.pi < len(it.points) && bytes.Equal(it.points[it.pi].key, cur)
	if it.baseValid && bytes.Equal(it.baseKey, cur) {
		it.setBase(it.base.Next())
	}
	if onPoint {
		it.pi++
	}
	return it.settleForward()
}

// prev moves to the largest visible key below the current one.
func (it *overlayIterator) prev() bool {
	if it.closed || !it.valid {
		return false
	}
	cur := it.key
	if it.forward {
		cur = cloneBytes(cur)
		it.seekBaseBackward(cur, true)
		it.pi = it.pointAfter(cur) - 1
		it.forward = false
	}
	onPoint := it.pi >= 0 && bytes.Equal(it.points[it.pi].key, cur)
	if it.baseValid && bytes.Equal(it.baseKey, cur) {
		it.setBase(it.base.Prev())
	}
	if onPoint {
		it.pi--
	}
	return it.settleBackward()
}

// settleForward advances past hidden entries to the smallest visible one at
// or after where base and points sit.
func (it *overlayIterator) settleForward() bool {
	for it.err == nil {
		havePoint := it.pi < len(it.points)
		if !havePoint && !it.baseValid {
			break
		}
		if havePoint {
			p := it.points[it.pi]
			cmp := -1
			if it.baseValid {
				cmp = bytes.Compare(p.key, it.baseKey)
			}
			if cmp <= 0 {
				if !p.deleted {
					return it.emitPoint(p)
				}
				it.pi++
				if cmp == 0 {
					it.setBase(it.base.Next())
				}
				continue
			}
		}
		if s, covered := coveringSpan(it.spans, it.baseKey); covered {
			if len(s.end) == 0 {
				it.baseValid = false
				continue
			}
			it.setBase(it.base.Seek(s.end))
			continue
		}
		return it.emitBase()
	}
	it.valid = false
	return false
}

// settleBackward retreats past hidden entries to the largest visible one at or
// before where base and points sit.
func (it *overlayIterator) settleBackward() bool {
	for it.err == nil {
		havePoint := it.pi >= 0
		if !havePoint && !it.baseValid {
			break
		}
		if havePoint {
			p := it.points[it.pi]
			cmp := 1
			if it.baseValid {
				cmp = bytes.Compare(p.key, it.baseKey)
			}
			if cmp >= 0 {
				if !p.deleted {
					return it.emitPoint(p)
				}
				it.pi--
				if cmp == 0 {
					it.setBase(it.base.Prev())
				}
				continue
			}
		}
		if s, covered := coveringSpan(it.spans, it.baseKey); covered {
			it.seekBaseBackward(s.start, false)
			continue
		}
		return it.emitBase()
	}
	it.valid = false
	return false
}

// seekBaseBackward positions base at its largest key below limit, or at limit
// itself when inclusive and present.
func (it *overlayIterator) seekBaseBackward(limit []byte, inclusive bool) {
	if !it.setBase(it.base.Seek(limit)) {
		if it.err == nil {
			// Nothing at or above limit: the last key is the largest below.
			it.setBase(it.base.SeekToLast())
		}
		return
	}
	if cmp := bytes.Compare(it.baseKey, limit); cmp > 0 || (cmp == 0 && !inclusive) {
		it.setBase(it.base.Prev())
	}
}

// setBase records where base landed after a move that reported ok, and
// captures a base failure. It returns whether base is on an entry.
func (it *overlayIterator) setBase(ok bool) bool {
	it.baseValid = ok && it.base.Valid()
	it.baseKey = nil
	if it.baseValid {
		it.baseKey = it.base.Key()
	}
	if err := it.base.Error(); err != nil && it.err == nil {
		it.err = err
		it.baseValid = false
	}
	return it.baseValid
}

// pointAtOrAfter returns the index of the first point whose key is >= key.
func (it *overlayIterator) pointAtOrAfter(key []byte) int {
	return sort.Search(len(it.points), func(i int) bool {
		return bytes.Compare(it.points[i].key, key) >= 0
	})
}

// pointAfter returns the index of the first point whose key is > key.
func (it *overlayIterator) pointAfter(key []byte) int {
	return sort.Search(len(it.points), func(i int) bool {
		return bytes.Compare(it.points[i].key, key) > 0
	})
}

func (it *overlayIterator) emitPoint(p pendingPoint) bool {
	it.valid, it.fromBase = true, false
	it.key, it.value = p.key, p.value
	return true
}

func (it *overlayIterator) emitBase() bool {
	it.valid, it.fromBase = true, true
	it.key, it.value = it.baseKey, nil
	return true
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
