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
	"sync"
	"sync/atomic"

	"github.com/tidwall/btree"
)

// sortedEntry represents a single indexed entry in the B-tree.
type sortedEntry struct {
	value    string
	vectorID uint64
}

// SortedIndex is a B-tree based scalar index optimized for numeric range
// queries. It stores entries sorted by value (numerically when possible,
// otherwise lexicographically), enabling O(log n) lookups and efficient
// range scans via the B-tree's Ascend/Descend operations.
type SortedIndex struct {
	tree   *btree.BTreeG[sortedEntry]
	ids    sync.Map // map[uint64]string (reverse mapping: vectorID -> value)
	count  atomic.Int64
	closed atomic.Bool
	mu     sync.RWMutex // protects tree mutations
}

// sortedQueryFunc is the function signature for sorted index query handlers.
type sortedQueryFunc func(idx *SortedIndex, value interface{}) ([]uint64, error)

// sortedQueryHandlers maps QueryOp to its sorted index query implementation.
var sortedQueryHandlers = map[QueryOp]sortedQueryFunc{
	QueryEqual:        sortedQueryEqual,
	QueryNotEqual:     sortedQueryNotEqual,
	QueryGreaterThan:  sortedQueryGreaterThan,
	QueryGreaterEqual: sortedQueryGreaterEqual,
	QueryLessThan:     sortedQueryLessThan,
	QueryLessEqual:    sortedQueryLessEqual,
	QueryIn:           sortedQueryIn,
	QueryNotIn:        sortedQueryNotIn,
	QueryIsNull:       sortedQueryIsNull,
	QueryIsNotNull:    sortedQueryIsNotNull,
}

// NewSortedIndex creates a new SortedIndex backed by a B-tree.
func NewSortedIndex() *SortedIndex {
	tree := btree.NewBTreeG[sortedEntry](func(a, b sortedEntry) bool {
		cmp := compareStrings(a.value, b.value)
		if cmp != 0 {
			return cmp < 0
		}
		return a.vectorID < b.vectorID
	})
	return &SortedIndex{tree: tree}
}

// Add indexes a value for the given vector ID.
func (idx *SortedIndex) Add(vectorID uint64, value interface{}) error {
	if idx.closed.Load() {
		return &IndexClosedError{IndexType: ScalarIndexSorted}
	}
	key := toString(value)

	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Remove previous entry if it exists.
	if prev, loaded := idx.ids.LoadAndDelete(vectorID); loaded {
		prevKey := prev.(string)
		if prevKey != key {
			idx.tree.Delete(sortedEntry{value: prevKey, vectorID: vectorID})
		} else {
			idx.ids.Store(vectorID, key)
			return nil
		}
	} else {
		idx.count.Add(1)
	}

	idx.tree.Set(sortedEntry{value: key, vectorID: vectorID})
	idx.ids.Store(vectorID, key)
	return nil
}

// Remove removes the index entry for the given vector ID.
func (idx *SortedIndex) Remove(vectorID uint64) error {
	if idx.closed.Load() {
		return &IndexClosedError{IndexType: ScalarIndexSorted}
	}
	prev, loaded := idx.ids.LoadAndDelete(vectorID)
	if !loaded {
		return nil
	}
	key := prev.(string)

	idx.mu.Lock()
	idx.tree.Delete(sortedEntry{value: key, vectorID: vectorID})
	idx.mu.Unlock()

	idx.count.Add(-1)
	return nil
}

// Query returns vector IDs matching the given operator and value.
func (idx *SortedIndex) Query(op QueryOp, value interface{}) ([]uint64, error) {
	if idx.closed.Load() {
		return nil, &IndexClosedError{IndexType: ScalarIndexSorted}
	}
	handler, ok := sortedQueryHandlers[op]
	if !ok {
		return nil, &UnsupportedQueryOpError{Op: op, IndexType: ScalarIndexSorted}
	}
	return handler(idx, value)
}

// QueryRange returns vector IDs with values between min and max (inclusive).
func (idx *SortedIndex) QueryRange(min, max interface{}) ([]uint64, error) {
	if idx.closed.Load() {
		return nil, &IndexClosedError{IndexType: ScalarIndexSorted}
	}
	minStr := toString(min)
	maxStr := toString(max)

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var result []uint64
	pivot := sortedEntry{value: minStr, vectorID: 0}
	idx.tree.Ascend(pivot, func(entry sortedEntry) bool {
		if compareStrings(entry.value, maxStr) > 0 {
			return false
		}
		result = append(result, entry.vectorID)
		return true
	})
	return result, nil
}

// Type returns ScalarIndexSorted.
func (idx *SortedIndex) Type() ScalarIndexType {
	return ScalarIndexSorted
}

// Count returns the number of indexed entries.
func (idx *SortedIndex) Count() int64 {
	return idx.count.Load()
}

// Close releases index resources.
func (idx *SortedIndex) Close() error {
	idx.closed.Store(true)
	return nil
}

func sortedQueryEqual(idx *SortedIndex, value interface{}) ([]uint64, error) {
	key := toString(value)
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var result []uint64
	pivot := sortedEntry{value: key, vectorID: 0}
	idx.tree.Ascend(pivot, func(entry sortedEntry) bool {
		if entry.value != key {
			return false
		}
		result = append(result, entry.vectorID)
		return true
	})
	return result, nil
}

func sortedQueryNotEqual(idx *SortedIndex, value interface{}) ([]uint64, error) {
	key := toString(value)
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var result []uint64
	idx.tree.Ascend(sortedEntry{}, func(entry sortedEntry) bool {
		if entry.value != key {
			result = append(result, entry.vectorID)
		}
		return true
	})
	return result, nil
}

func sortedQueryGreaterThan(idx *SortedIndex, value interface{}) ([]uint64, error) {
	key := toString(value)
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var result []uint64
	pivot := sortedEntry{value: key, vectorID: 0}
	idx.tree.Ascend(pivot, func(entry sortedEntry) bool {
		if compareStrings(entry.value, key) > 0 {
			result = append(result, entry.vectorID)
		}
		return true
	})
	return result, nil
}

func sortedQueryGreaterEqual(idx *SortedIndex, value interface{}) ([]uint64, error) {
	key := toString(value)
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var result []uint64
	pivot := sortedEntry{value: key, vectorID: 0}
	idx.tree.Ascend(pivot, func(entry sortedEntry) bool {
		result = append(result, entry.vectorID)
		return true
	})
	return result, nil
}

func sortedQueryLessThan(idx *SortedIndex, value interface{}) ([]uint64, error) {
	key := toString(value)
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var result []uint64
	idx.tree.Ascend(sortedEntry{}, func(entry sortedEntry) bool {
		if compareStrings(entry.value, key) >= 0 {
			return false
		}
		result = append(result, entry.vectorID)
		return true
	})
	return result, nil
}

func sortedQueryLessEqual(idx *SortedIndex, value interface{}) ([]uint64, error) {
	key := toString(value)
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var result []uint64
	idx.tree.Ascend(sortedEntry{}, func(entry sortedEntry) bool {
		if compareStrings(entry.value, key) > 0 {
			return false
		}
		result = append(result, entry.vectorID)
		return true
	})
	return result, nil
}

func sortedQueryIn(idx *SortedIndex, value interface{}) ([]uint64, error) {
	values, ok := toStringSlice(value)
	if !ok {
		return nil, &InvalidValueTypeError{Value: value, Expected: "[]string or []interface{}"}
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	seen := make(map[uint64]struct{})
	var result []uint64
	for _, v := range values {
		pivot := sortedEntry{value: v, vectorID: 0}
		idx.tree.Ascend(pivot, func(entry sortedEntry) bool {
			if entry.value != v {
				return false
			}
			if _, exists := seen[entry.vectorID]; !exists {
				seen[entry.vectorID] = struct{}{}
				result = append(result, entry.vectorID)
			}
			return true
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func sortedQueryNotIn(idx *SortedIndex, value interface{}) ([]uint64, error) {
	values, ok := toStringSlice(value)
	if !ok {
		return nil, &InvalidValueTypeError{Value: value, Expected: "[]string or []interface{}"}
	}
	exclude := make(map[string]struct{}, len(values))
	for _, v := range values {
		exclude[v] = struct{}{}
	}

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var result []uint64
	idx.tree.Ascend(sortedEntry{}, func(entry sortedEntry) bool {
		if _, excluded := exclude[entry.value]; !excluded {
			result = append(result, entry.vectorID)
		}
		return true
	})
	return result, nil
}

func sortedQueryIsNull(idx *SortedIndex, _ interface{}) ([]uint64, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var result []uint64
	pivot := sortedEntry{value: nullKey, vectorID: 0}
	idx.tree.Ascend(pivot, func(entry sortedEntry) bool {
		if entry.value != nullKey {
			return false
		}
		result = append(result, entry.vectorID)
		return true
	})
	return result, nil
}

func sortedQueryIsNotNull(idx *SortedIndex, _ interface{}) ([]uint64, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var result []uint64
	idx.tree.Ascend(sortedEntry{}, func(entry sortedEntry) bool {
		if entry.value != nullKey {
			result = append(result, entry.vectorID)
		}
		return true
	})
	return result, nil
}
