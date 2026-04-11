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

package scalar

import (
	"strings"
	"testing"
)

func TestScalarIndexType_String(t *testing.T) {
	tests := []struct {
		name      string
		indexType ScalarIndexType
		want      string
	}{
		{name: "bitmap", indexType: ScalarIndexBitmap, want: "BITMAP"},
		{name: "inverted", indexType: ScalarIndexInverted, want: "INVERTED"},
		{name: "sorted", indexType: ScalarIndexSorted, want: "SORTED"},
		{name: "unknown", indexType: ScalarIndexType(99), want: "UNKNOWN(99)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.indexType.String()
			if got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestScalarIndexType_String_Unknown(t *testing.T) {
	result := ScalarIndexType(-1).String()
	if !strings.HasPrefix(result, "UNKNOWN") {
		t.Errorf("String() = %q, should start with UNKNOWN", result)
	}
}

func TestQueryOp_String(t *testing.T) {
	tests := []struct {
		name string
		op   QueryOp
		want string
	}{
		{name: "equal", op: QueryEqual, want: "="},
		{name: "not_equal", op: QueryNotEqual, want: "!="},
		{name: "greater_than", op: QueryGreaterThan, want: ">"},
		{name: "greater_equal", op: QueryGreaterEqual, want: ">="},
		{name: "less_than", op: QueryLessThan, want: "<"},
		{name: "less_equal", op: QueryLessEqual, want: "<="},
		{name: "in", op: QueryIn, want: "IN"},
		{name: "not_in", op: QueryNotIn, want: "NOT IN"},
		{name: "range", op: QueryRange, want: "RANGE"},
		{name: "is_null", op: QueryIsNull, want: "IS NULL"},
		{name: "is_not_null", op: QueryIsNotNull, want: "IS NOT NULL"},
		{name: "unknown", op: QueryOp(99), want: "UNKNOWN(99)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.op.String()
			if got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestQueryOp_String_Unknown(t *testing.T) {
	result := QueryOp(-1).String()
	if !strings.HasPrefix(result, "UNKNOWN") {
		t.Errorf("String() = %q, should start with UNKNOWN", result)
	}
}

func TestScalarIndex_Interface(t *testing.T) {
	// Compile-time verification that all index types implement ScalarIndex.
	var _ ScalarIndex = (*BitmapIndex)(nil)
	var _ ScalarIndex = (*InvertedIndex)(nil)
	var _ ScalarIndex = (*SortedIndex)(nil)
}
