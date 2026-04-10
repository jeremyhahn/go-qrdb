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
	"sync"
	"sync/atomic"

	"github.com/RoaringBitmap/roaring/v2/roaring64"
)

// nullKey is the sentinel key used to track null/empty values in the bitmap.
const nullKey = ""

// BitmapIndex is a roaring bitmap-based scalar index optimized for
// low-cardinality fields (e.g., booleans, enums, categories). Each unique
// value maps to a roaring64.Bitmap of vector IDs, enabling O(1) lookups
// and fast set operations (AND, OR, AND NOT) for filter evaluation.
type BitmapIndex struct {
	bitmaps sync.Map // map[string]*roaring64.Bitmap
	ids     sync.Map // map[uint64]string (reverse mapping: vectorID -> value)
	count   atomic.Int64
	closed  atomic.Bool
}

// bitmapQueryFunc is the function signature for bitmap query handlers.
type bitmapQueryFunc func(b *BitmapIndex, value interface{}) ([]uint64, error)

// bitmapQueryHandlers maps QueryOp to its bitmap query implementation.
var bitmapQueryHandlers = map[QueryOp]bitmapQueryFunc{
	QueryEqual:     bitmapQueryEqual,
	QueryNotEqual:  bitmapQueryNotEqual,
	QueryIn:        bitmapQueryIn,
	QueryNotIn:     bitmapQueryNotIn,
	QueryIsNull:    bitmapQueryIsNull,
	QueryIsNotNull: bitmapQueryIsNotNull,
}

// NewBitmapIndex creates a new BitmapIndex.
func NewBitmapIndex() *BitmapIndex {
	return &BitmapIndex{}
}

// Add indexes a value for the given vector ID. If the vector ID already exists,
// its previous value mapping is removed before adding the new one.
func (b *BitmapIndex) Add(vectorID uint64, value interface{}) error {
	if b.closed.Load() {
		return &IndexClosedError{IndexType: ScalarIndexBitmap}
	}
	key := toString(value)

	// Remove previous mapping if it exists.
	if prev, loaded := b.ids.LoadAndDelete(vectorID); loaded {
		prevKey := prev.(string)
		if prevKey != key {
			if bm, ok := b.bitmaps.Load(prevKey); ok {
				bm.(*roaring64.Bitmap).Remove(vectorID)
			}
		} else {
			// Same value; re-store the reverse mapping and return.
			b.ids.Store(vectorID, key)
			return nil
		}
	} else {
		b.count.Add(1)
	}

	// Get or create bitmap for this value.
	actual, _ := b.bitmaps.LoadOrStore(key, roaring64.New())
	actual.(*roaring64.Bitmap).Add(vectorID)
	b.ids.Store(vectorID, key)
	return nil
}

// Remove removes the index entry for the given vector ID.
func (b *BitmapIndex) Remove(vectorID uint64) error {
	if b.closed.Load() {
		return &IndexClosedError{IndexType: ScalarIndexBitmap}
	}
	prev, loaded := b.ids.LoadAndDelete(vectorID)
	if !loaded {
		return nil
	}
	key := prev.(string)
	if bm, ok := b.bitmaps.Load(key); ok {
		bm.(*roaring64.Bitmap).Remove(vectorID)
	}
	b.count.Add(-1)
	return nil
}

// Query returns vector IDs matching the given operator and value.
func (b *BitmapIndex) Query(op QueryOp, value interface{}) ([]uint64, error) {
	if b.closed.Load() {
		return nil, &IndexClosedError{IndexType: ScalarIndexBitmap}
	}
	handler, ok := bitmapQueryHandlers[op]
	if !ok {
		return nil, &UnsupportedQueryOpError{Op: op, IndexType: ScalarIndexBitmap}
	}
	return handler(b, value)
}

// QueryRange returns vector IDs with values between min and max (inclusive).
func (b *BitmapIndex) QueryRange(min, max interface{}) ([]uint64, error) {
	if b.closed.Load() {
		return nil, &IndexClosedError{IndexType: ScalarIndexBitmap}
	}
	minStr := toString(min)
	maxStr := toString(max)
	result := roaring64.New()
	b.bitmaps.Range(func(key, val interface{}) bool {
		k := key.(string)
		if compareStrings(k, minStr) >= 0 && compareStrings(k, maxStr) <= 0 {
			result.Or(val.(*roaring64.Bitmap))
		}
		return true
	})
	return result.ToArray(), nil
}

// Type returns ScalarIndexBitmap.
func (b *BitmapIndex) Type() ScalarIndexType {
	return ScalarIndexBitmap
}

// Count returns the number of indexed entries.
func (b *BitmapIndex) Count() int64 {
	return b.count.Load()
}

// Close releases index resources.
func (b *BitmapIndex) Close() error {
	b.closed.Store(true)
	return nil
}

// allBitmaps returns the union of all bitmaps in the index.
func (b *BitmapIndex) allBitmaps() *roaring64.Bitmap {
	result := roaring64.New()
	b.bitmaps.Range(func(_, val interface{}) bool {
		result.Or(val.(*roaring64.Bitmap))
		return true
	})
	return result
}

func bitmapQueryEqual(b *BitmapIndex, value interface{}) ([]uint64, error) {
	key := toString(value)
	bm, ok := b.bitmaps.Load(key)
	if !ok {
		return nil, nil
	}
	return bm.(*roaring64.Bitmap).ToArray(), nil
}

func bitmapQueryNotEqual(b *BitmapIndex, value interface{}) ([]uint64, error) {
	key := toString(value)
	all := b.allBitmaps()
	if bm, ok := b.bitmaps.Load(key); ok {
		all.AndNot(bm.(*roaring64.Bitmap))
	}
	return all.ToArray(), nil
}

func bitmapQueryIn(b *BitmapIndex, value interface{}) ([]uint64, error) {
	values, ok := toStringSlice(value)
	if !ok {
		return nil, &InvalidValueTypeError{Value: value, Expected: "[]string or []interface{}"}
	}
	result := roaring64.New()
	for _, v := range values {
		if bm, ok := b.bitmaps.Load(v); ok {
			result.Or(bm.(*roaring64.Bitmap))
		}
	}
	return result.ToArray(), nil
}

func bitmapQueryNotIn(b *BitmapIndex, value interface{}) ([]uint64, error) {
	values, ok := toStringSlice(value)
	if !ok {
		return nil, &InvalidValueTypeError{Value: value, Expected: "[]string or []interface{}"}
	}
	all := b.allBitmaps()
	exclude := roaring64.New()
	for _, v := range values {
		if bm, ok := b.bitmaps.Load(v); ok {
			exclude.Or(bm.(*roaring64.Bitmap))
		}
	}
	all.AndNot(exclude)
	return all.ToArray(), nil
}

func bitmapQueryIsNull(b *BitmapIndex, _ interface{}) ([]uint64, error) {
	bm, ok := b.bitmaps.Load(nullKey)
	if !ok {
		return nil, nil
	}
	return bm.(*roaring64.Bitmap).ToArray(), nil
}

func bitmapQueryIsNotNull(b *BitmapIndex, _ interface{}) ([]uint64, error) {
	all := b.allBitmaps()
	if bm, ok := b.bitmaps.Load(nullKey); ok {
		all.AndNot(bm.(*roaring64.Bitmap))
	}
	return all.ToArray(), nil
}
