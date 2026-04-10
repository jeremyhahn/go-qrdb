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

// lowCardinalityThreshold is the maximum cardinality for which a bitmap
// index is preferred over an inverted index.
const lowCardinalityThreshold = 100

// FieldStats holds statistics about a metadata field for auto-index selection.
type FieldStats struct {
	// Cardinality is the number of unique values observed for this field.
	Cardinality int64
	// TotalCount is the total number of entries for this field.
	TotalCount int64
	// IsNumeric indicates whether all observed values are numeric.
	IsNumeric bool
	// IsBool indicates whether all observed values are boolean.
	IsBool bool
}

// AutoSelectIndexType chooses the optimal scalar index type based on field statistics.
//
// Selection rules (inspired by Milvus autoindex_param.go):
//   - Boolean fields: BITMAP (only 2 unique values, bitmap ops are ideal)
//   - Cardinality <= 100 (low-cardinality): BITMAP (space-efficient, fast AND/OR)
//   - Numeric fields: SORTED (B-tree enables O(log n) range queries)
//   - High-cardinality strings: INVERTED (hash-based O(1) lookups)
func AutoSelectIndexType(stats *FieldStats) ScalarIndexType {
	if stats.IsBool {
		return ScalarIndexBitmap
	}
	if stats.Cardinality <= lowCardinalityThreshold {
		return ScalarIndexBitmap
	}
	if stats.IsNumeric {
		return ScalarIndexSorted
	}
	return ScalarIndexInverted
}

// scalarIndexFactory maps index types to their constructor functions.
type scalarIndexConstructor func() ScalarIndex

// scalarIndexFactory provides O(1) dispatch for index creation.
var scalarIndexFactory = map[ScalarIndexType]scalarIndexConstructor{
	ScalarIndexBitmap:   func() ScalarIndex { return NewBitmapIndex() },
	ScalarIndexInverted: func() ScalarIndex { return NewInvertedIndex() },
	ScalarIndexSorted:   func() ScalarIndex { return NewSortedIndex() },
}

// NewScalarIndex creates a new ScalarIndex of the given type.
func NewScalarIndex(indexType ScalarIndexType) (ScalarIndex, error) {
	constructor, ok := scalarIndexFactory[indexType]
	if !ok {
		return nil, &UnsupportedScalarIndexTypeError{Type: indexType}
	}
	return constructor(), nil
}
