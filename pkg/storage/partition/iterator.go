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
	"bytes"
	"container/heap"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Compile-time interface assertion.
var _ storage.Iterator = (*mergeIterator)(nil)

// heapItem holds a reference to an iterator and its current position in the min-heap.
type heapItem struct {
	iter  storage.Iterator
	index int // index within the iterators slice, for stable ordering
}

// minHeap implements container/heap.Interface for forward iteration (ascending keys).
type minHeap []heapItem

func (h minHeap) Len() int { return len(h) }

func (h minHeap) Less(i, j int) bool {
	cmp := bytes.Compare(h[i].iter.Key(), h[j].iter.Key())
	if cmp == 0 {
		return h[i].index < h[j].index // stable ordering
	}
	return cmp < 0
}

func (h minHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *minHeap) Push(x any) {
	*h = append(*h, x.(heapItem))
}

func (h *minHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// maxHeap implements container/heap.Interface for reverse iteration (descending keys).
type maxHeap []heapItem

func (h maxHeap) Len() int { return len(h) }

func (h maxHeap) Less(i, j int) bool {
	cmp := bytes.Compare(h[i].iter.Key(), h[j].iter.Key())
	if cmp == 0 {
		return h[i].index < h[j].index
	}
	return cmp > 0 // reverse: larger keys first
}

func (h maxHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *maxHeap) Push(x any) {
	*h = append(*h, x.(heapItem))
}

func (h *maxHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// mergeIterator merges sorted iterators from multiple partitions using a heap.
// It produces globally sorted output from partition-local sorted iterators.
type mergeIterator struct {
	iters   []storage.Iterator
	opts    *storage.IteratorOptions
	fwdHeap minHeap
	revHeap maxHeap
	current storage.Iterator // the iterator that produced the current key/value
	reverse bool
	closed  atomic.Bool
	err     error
}

// newMergeIterator creates a new merge iterator over the given partition iterators.
func newMergeIterator(iters []storage.Iterator, opts *storage.IteratorOptions) *mergeIterator {
	if opts == nil {
		opts = &storage.IteratorOptions{}
	}
	return &mergeIterator{
		iters:   iters,
		opts:    opts,
		reverse: opts.Reverse,
	}
}

// newClosedMergeIterator returns a merge iterator in the closed state.
func newClosedMergeIterator() *mergeIterator {
	it := &mergeIterator{
		opts: &storage.IteratorOptions{},
	}
	it.closed.Store(true)
	return it
}

// SeekToFirst positions the iterator at the first key across all partitions.
func (m *mergeIterator) SeekToFirst() bool {
	if m.closed.Load() {
		return false
	}

	if m.reverse {
		m.revHeap = m.revHeap[:0]
		for i, it := range m.iters {
			if it.SeekToFirst() {
				m.revHeap = append(m.revHeap, heapItem{iter: it, index: i})
			}
		}
		heap.Init(&m.revHeap)
		return m.advanceReverse()
	}

	m.fwdHeap = m.fwdHeap[:0]
	for i, it := range m.iters {
		if it.SeekToFirst() {
			m.fwdHeap = append(m.fwdHeap, heapItem{iter: it, index: i})
		}
	}
	heap.Init(&m.fwdHeap)
	return m.advanceForward()
}

// SeekToLast positions the iterator at the last key across all partitions.
func (m *mergeIterator) SeekToLast() bool {
	if m.closed.Load() {
		return false
	}

	if m.reverse {
		m.revHeap = m.revHeap[:0]
		for i, it := range m.iters {
			if it.SeekToLast() {
				m.revHeap = append(m.revHeap, heapItem{iter: it, index: i})
			}
		}
		heap.Init(&m.revHeap)
		return m.advanceReverse()
	}

	// For forward mode, SeekToLast positions at the largest key.
	// We build a max-heap to find the largest, then rebuild as min-heap for Next().
	m.fwdHeap = m.fwdHeap[:0]
	var best storage.Iterator
	var bestKey []byte
	for _, it := range m.iters {
		if it.SeekToLast() {
			k := it.Key()
			if best == nil || bytes.Compare(k, bestKey) > 0 {
				best = it
				bestKey = k
			}
		}
	}

	if best == nil {
		m.current = nil
		return false
	}

	m.current = best
	return true
}

// Seek positions the iterator at the first key >= target across all partitions.
func (m *mergeIterator) Seek(target []byte) bool {
	if m.closed.Load() {
		return false
	}

	if m.reverse {
		m.revHeap = m.revHeap[:0]
		for i, it := range m.iters {
			if it.Seek(target) {
				m.revHeap = append(m.revHeap, heapItem{iter: it, index: i})
			}
		}
		heap.Init(&m.revHeap)
		return m.advanceReverse()
	}

	m.fwdHeap = m.fwdHeap[:0]
	for i, it := range m.iters {
		if it.Seek(target) {
			m.fwdHeap = append(m.fwdHeap, heapItem{iter: it, index: i})
		}
	}
	heap.Init(&m.fwdHeap)
	return m.advanceForward()
}

// Next advances the iterator to the next key in sorted order.
func (m *mergeIterator) Next() bool {
	if m.closed.Load() || m.current == nil {
		return false
	}

	if m.reverse {
		return m.nextReverse()
	}
	return m.nextForward()
}

// Prev moves the iterator to the previous key. Not supported by merge iterator.
func (m *mergeIterator) Prev() bool {
	// Prev across a merge of hash-partitioned iterators is not meaningful
	// in the general case because each partition iterator only sees a subset
	// of keys. We return false to indicate no previous entry.
	return false
}

// Valid reports whether the iterator is positioned at a valid entry.
func (m *mergeIterator) Valid() bool {
	if m.closed.Load() {
		return false
	}
	return m.current != nil && m.current.Valid()
}

// Key returns the current key.
func (m *mergeIterator) Key() []byte {
	if m.closed.Load() || m.current == nil {
		return nil
	}
	return m.current.Key()
}

// Value returns the current value.
func (m *mergeIterator) Value() []byte {
	if m.closed.Load() || m.current == nil {
		return nil
	}
	return m.current.Value()
}

// Error returns any accumulated error from the underlying iterators.
func (m *mergeIterator) Error() error {
	if m.err != nil {
		return m.err
	}
	for _, it := range m.iters {
		if err := it.Error(); err != nil {
			return &MergeIteratorError{Message: "underlying iterator error", Err: err}
		}
	}
	return nil
}

// Close releases all underlying iterators.
func (m *mergeIterator) Close() error {
	if !m.closed.CompareAndSwap(false, true) {
		return nil
	}

	var firstErr error
	for _, it := range m.iters {
		if err := it.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	m.current = nil
	return firstErr
}

// advanceForward sets current to the minimum key from the forward heap.
func (m *mergeIterator) advanceForward() bool {
	if m.fwdHeap.Len() == 0 {
		m.current = nil
		return false
	}
	m.current = m.fwdHeap[0].iter
	return true
}

// nextForward advances past the current key and re-heapifies.
func (m *mergeIterator) nextForward() bool {
	if m.fwdHeap.Len() == 0 {
		m.current = nil
		return false
	}

	// Advance the current minimum iterator.
	top := m.fwdHeap[0]
	if top.iter.Next() {
		heap.Fix(&m.fwdHeap, 0)
	} else {
		heap.Pop(&m.fwdHeap)
	}

	return m.advanceForward()
}

// advanceReverse sets current to the maximum key from the reverse heap.
func (m *mergeIterator) advanceReverse() bool {
	if m.revHeap.Len() == 0 {
		m.current = nil
		return false
	}
	m.current = m.revHeap[0].iter
	return true
}

// nextReverse advances past the current key in reverse direction.
func (m *mergeIterator) nextReverse() bool {
	if m.revHeap.Len() == 0 {
		m.current = nil
		return false
	}

	top := m.revHeap[0]
	if top.iter.Next() {
		heap.Fix(&m.revHeap, 0)
	} else {
		heap.Pop(&m.revHeap)
	}

	return m.advanceReverse()
}
