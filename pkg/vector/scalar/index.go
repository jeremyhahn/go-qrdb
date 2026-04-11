//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

// Package scalar provides scalar field indexes for accelerating metadata
// filtering during vector search operations. It supports bitmap, inverted,
// and sorted (B-tree) index types with automatic selection based on field
// characteristics.
package scalar

import "strconv"

// ScalarIndexType represents the type of scalar index.
type ScalarIndexType int

const (
	// ScalarIndexBitmap uses roaring bitmaps for low-cardinality fields.
	ScalarIndexBitmap ScalarIndexType = iota
	// ScalarIndexInverted uses hash-based inverted posting lists for high-cardinality string fields.
	ScalarIndexInverted
	// ScalarIndexSorted uses a B-tree for numeric range queries.
	ScalarIndexSorted
)

// scalarIndexTypeNames maps index types to display names.
var scalarIndexTypeNames = map[ScalarIndexType]string{
	ScalarIndexBitmap:   "BITMAP",
	ScalarIndexInverted: "INVERTED",
	ScalarIndexSorted:   "SORTED",
}

// String returns a human-readable name for the scalar index type.
func (t ScalarIndexType) String() string {
	name, ok := scalarIndexTypeNames[t]
	if !ok {
		return "UNKNOWN(" + strconv.Itoa(int(t)) + ")"
	}
	return name
}

// QueryOp represents query operators for scalar index lookups.
type QueryOp int

const (
	// QueryEqual matches exact value equality.
	QueryEqual QueryOp = iota
	// QueryNotEqual matches when values are not equal.
	QueryNotEqual
	// QueryGreaterThan matches when indexed value is greater than the query value.
	QueryGreaterThan
	// QueryGreaterEqual matches when indexed value is greater than or equal to the query value.
	QueryGreaterEqual
	// QueryLessThan matches when indexed value is less than the query value.
	QueryLessThan
	// QueryLessEqual matches when indexed value is less than or equal to the query value.
	QueryLessEqual
	// QueryIn matches when indexed value is in a set of values.
	QueryIn
	// QueryNotIn matches when indexed value is not in a set of values.
	QueryNotIn
	// QueryRange matches when indexed value is between min and max.
	QueryRange
	// QueryIsNull matches when the indexed value is empty or absent.
	QueryIsNull
	// QueryIsNotNull matches when the indexed value is non-empty.
	QueryIsNotNull
)

// queryOpNames maps query operators to display names.
var queryOpNames = map[QueryOp]string{
	QueryEqual:        "=",
	QueryNotEqual:     "!=",
	QueryGreaterThan:  ">",
	QueryGreaterEqual: ">=",
	QueryLessThan:     "<",
	QueryLessEqual:    "<=",
	QueryIn:           "IN",
	QueryNotIn:        "NOT IN",
	QueryRange:        "RANGE",
	QueryIsNull:       "IS NULL",
	QueryIsNotNull:    "IS NOT NULL",
}

// String returns a human-readable name for the query operator.
func (op QueryOp) String() string {
	name, ok := queryOpNames[op]
	if !ok {
		return "UNKNOWN(" + strconv.Itoa(int(op)) + ")"
	}
	return name
}

// ScalarIndex defines the interface for scalar field indexes used to accelerate
// metadata filtering during vector search.
type ScalarIndex interface {
	// Add indexes a value for the given vector ID.
	Add(vectorID uint64, value interface{}) error
	// Remove removes the index entry for the given vector ID.
	Remove(vectorID uint64) error
	// Query returns vector IDs matching the given operator and value.
	Query(op QueryOp, value interface{}) ([]uint64, error)
	// QueryRange returns vector IDs with values between min and max (inclusive).
	QueryRange(min, max interface{}) ([]uint64, error)
	// Type returns the scalar index type.
	Type() ScalarIndexType
	// Count returns the number of indexed entries.
	Count() int64
	// Close releases index resources.
	Close() error
}
