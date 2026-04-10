//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package scalar

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// postingList holds a sorted list of vector IDs for a given value.
type postingList struct {
	mu  sync.RWMutex
	ids []uint64 // sorted ascending
}

// add inserts a vector ID into the sorted posting list using binary search.
func (p *postingList) add(id uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	idx := sort.Search(len(p.ids), func(i int) bool { return p.ids[i] >= id })
	if idx < len(p.ids) && p.ids[idx] == id {
		return // already present
	}
	p.ids = append(p.ids, 0)
	copy(p.ids[idx+1:], p.ids[idx:])
	p.ids[idx] = id
}

// remove deletes a vector ID from the sorted posting list using binary search.
func (p *postingList) remove(id uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	idx := sort.Search(len(p.ids), func(i int) bool { return p.ids[i] >= id })
	if idx < len(p.ids) && p.ids[idx] == id {
		p.ids = append(p.ids[:idx], p.ids[idx+1:]...)
	}
}

// toArray returns a copy of the sorted IDs.
func (p *postingList) toArray() []uint64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]uint64, len(p.ids))
	copy(result, p.ids)
	return result
}

// InvertedIndex is a hash-based inverted index with sorted posting lists,
// optimized for high-cardinality string fields. Each unique value maps to a
// sorted posting list of vector IDs, enabling O(1) exact lookups and
// efficient iteration for substring/prefix operations.
type InvertedIndex struct {
	postings sync.Map // map[string]*postingList
	ids      sync.Map // map[uint64]string (reverse mapping)
	count    atomic.Int64
	closed   atomic.Bool
}

// invertedQueryFunc is the function signature for inverted index query handlers.
type invertedQueryFunc func(idx *InvertedIndex, value interface{}) ([]uint64, error)

// invertedQueryHandlers maps QueryOp to its inverted index query implementation.
var invertedQueryHandlers = map[QueryOp]invertedQueryFunc{
	QueryEqual:     invertedQueryEqual,
	QueryNotEqual:  invertedQueryNotEqual,
	QueryIn:        invertedQueryIn,
	QueryNotIn:     invertedQueryNotIn,
	QueryIsNull:    invertedQueryIsNull,
	QueryIsNotNull: invertedQueryIsNotNull,
}

// NewInvertedIndex creates a new InvertedIndex.
func NewInvertedIndex() *InvertedIndex {
	return &InvertedIndex{}
}

// Add indexes a value for the given vector ID.
func (idx *InvertedIndex) Add(vectorID uint64, value interface{}) error {
	if idx.closed.Load() {
		return &IndexClosedError{IndexType: ScalarIndexInverted}
	}
	key := toString(value)

	// Remove previous mapping if it exists.
	if prev, loaded := idx.ids.LoadAndDelete(vectorID); loaded {
		prevKey := prev.(string)
		if prevKey != key {
			if pl, ok := idx.postings.Load(prevKey); ok {
				pl.(*postingList).remove(vectorID)
			}
		} else {
			idx.ids.Store(vectorID, key)
			return nil
		}
	} else {
		idx.count.Add(1)
	}

	// Get or create posting list for this value.
	actual, _ := idx.postings.LoadOrStore(key, &postingList{})
	actual.(*postingList).add(vectorID)
	idx.ids.Store(vectorID, key)
	return nil
}

// Remove removes the index entry for the given vector ID.
func (idx *InvertedIndex) Remove(vectorID uint64) error {
	if idx.closed.Load() {
		return &IndexClosedError{IndexType: ScalarIndexInverted}
	}
	prev, loaded := idx.ids.LoadAndDelete(vectorID)
	if !loaded {
		return nil
	}
	key := prev.(string)
	if pl, ok := idx.postings.Load(key); ok {
		pl.(*postingList).remove(vectorID)
	}
	idx.count.Add(-1)
	return nil
}

// Query returns vector IDs matching the given operator and value.
func (idx *InvertedIndex) Query(op QueryOp, value interface{}) ([]uint64, error) {
	if idx.closed.Load() {
		return nil, &IndexClosedError{IndexType: ScalarIndexInverted}
	}
	handler, ok := invertedQueryHandlers[op]
	if !ok {
		return nil, &UnsupportedQueryOpError{Op: op, IndexType: ScalarIndexInverted}
	}
	return handler(idx, value)
}

// QueryRange returns vector IDs with values between min and max (inclusive).
func (idx *InvertedIndex) QueryRange(min, max interface{}) ([]uint64, error) {
	if idx.closed.Load() {
		return nil, &IndexClosedError{IndexType: ScalarIndexInverted}
	}
	minStr := toString(min)
	maxStr := toString(max)
	seen := make(map[uint64]struct{})
	var result []uint64
	idx.postings.Range(func(key, val interface{}) bool {
		k := key.(string)
		if compareStrings(k, minStr) >= 0 && compareStrings(k, maxStr) <= 0 {
			for _, id := range val.(*postingList).toArray() {
				if _, exists := seen[id]; !exists {
					seen[id] = struct{}{}
					result = append(result, id)
				}
			}
		}
		return true
	})
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

// Type returns ScalarIndexInverted.
func (idx *InvertedIndex) Type() ScalarIndexType {
	return ScalarIndexInverted
}

// Count returns the number of indexed entries.
func (idx *InvertedIndex) Count() int64 {
	return idx.count.Load()
}

// Close releases index resources.
func (idx *InvertedIndex) Close() error {
	idx.closed.Store(true)
	return nil
}

// collectAllIDs returns a deduplicated, sorted slice of all indexed vector IDs.
func (idx *InvertedIndex) collectAllIDs() []uint64 {
	seen := make(map[uint64]struct{})
	var result []uint64
	idx.ids.Range(func(key, _ interface{}) bool {
		id := key.(uint64)
		seen[id] = struct{}{}
		result = append(result, id)
		return true
	})
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func invertedQueryEqual(idx *InvertedIndex, value interface{}) ([]uint64, error) {
	key := toString(value)
	pl, ok := idx.postings.Load(key)
	if !ok {
		return nil, nil
	}
	return pl.(*postingList).toArray(), nil
}

func invertedQueryNotEqual(idx *InvertedIndex, value interface{}) ([]uint64, error) {
	key := toString(value)
	exclude := make(map[uint64]struct{})
	if pl, ok := idx.postings.Load(key); ok {
		for _, id := range pl.(*postingList).toArray() {
			exclude[id] = struct{}{}
		}
	}
	all := idx.collectAllIDs()
	var result []uint64
	for _, id := range all {
		if _, excluded := exclude[id]; !excluded {
			result = append(result, id)
		}
	}
	return result, nil
}

func invertedQueryIn(idx *InvertedIndex, value interface{}) ([]uint64, error) {
	values, ok := toStringSlice(value)
	if !ok {
		return nil, &InvalidValueTypeError{Value: value, Expected: "[]string or []interface{}"}
	}
	seen := make(map[uint64]struct{})
	var result []uint64
	for _, v := range values {
		if pl, ok := idx.postings.Load(v); ok {
			for _, id := range pl.(*postingList).toArray() {
				if _, exists := seen[id]; !exists {
					seen[id] = struct{}{}
					result = append(result, id)
				}
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func invertedQueryNotIn(idx *InvertedIndex, value interface{}) ([]uint64, error) {
	values, ok := toStringSlice(value)
	if !ok {
		return nil, &InvalidValueTypeError{Value: value, Expected: "[]string or []interface{}"}
	}
	exclude := make(map[uint64]struct{})
	for _, v := range values {
		if pl, ok := idx.postings.Load(v); ok {
			for _, id := range pl.(*postingList).toArray() {
				exclude[id] = struct{}{}
			}
		}
	}
	all := idx.collectAllIDs()
	var result []uint64
	for _, id := range all {
		if _, excluded := exclude[id]; !excluded {
			result = append(result, id)
		}
	}
	return result, nil
}

func invertedQueryIsNull(idx *InvertedIndex, _ interface{}) ([]uint64, error) {
	pl, ok := idx.postings.Load(nullKey)
	if !ok {
		return nil, nil
	}
	return pl.(*postingList).toArray(), nil
}

func invertedQueryIsNotNull(idx *InvertedIndex, _ interface{}) ([]uint64, error) {
	nullIDs := make(map[uint64]struct{})
	if pl, ok := idx.postings.Load(nullKey); ok {
		for _, id := range pl.(*postingList).toArray() {
			nullIDs[id] = struct{}{}
		}
	}
	all := idx.collectAllIDs()
	var result []uint64
	for _, id := range all {
		if _, isNull := nullIDs[id]; !isNull {
			result = append(result, id)
		}
	}
	return result, nil
}

// QueryContains returns vector IDs whose indexed values contain the given
// substring. This requires iterating all keys.
func (idx *InvertedIndex) QueryContains(substr string) ([]uint64, error) {
	if idx.closed.Load() {
		return nil, &IndexClosedError{IndexType: ScalarIndexInverted}
	}
	seen := make(map[uint64]struct{})
	var result []uint64
	idx.postings.Range(func(key, val interface{}) bool {
		k := key.(string)
		if strings.Contains(k, substr) {
			for _, id := range val.(*postingList).toArray() {
				if _, exists := seen[id]; !exists {
					seen[id] = struct{}{}
					result = append(result, id)
				}
			}
		}
		return true
	})
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

// QueryPrefixMatch returns vector IDs whose indexed values start with the
// given prefix. This requires iterating all keys.
func (idx *InvertedIndex) QueryPrefixMatch(prefix string) ([]uint64, error) {
	if idx.closed.Load() {
		return nil, &IndexClosedError{IndexType: ScalarIndexInverted}
	}
	seen := make(map[uint64]struct{})
	var result []uint64
	idx.postings.Range(func(key, val interface{}) bool {
		k := key.(string)
		if strings.HasPrefix(k, prefix) {
			for _, id := range val.(*postingList).toArray() {
				if _, exists := seen[id]; !exists {
					seen[id] = struct{}{}
					result = append(result, id)
				}
			}
		}
		return true
	})
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}
