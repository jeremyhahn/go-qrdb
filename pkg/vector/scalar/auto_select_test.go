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
	"errors"
	"testing"
)

func TestAutoSelectBoolField(t *testing.T) {
	stats := &FieldStats{
		Cardinality: 2,
		TotalCount:  1000,
		IsNumeric:   false,
		IsBool:      true,
	}
	result := AutoSelectIndexType(stats)
	if result != ScalarIndexBitmap {
		t.Errorf("AutoSelectIndexType(bool) = %v, want ScalarIndexBitmap", result)
	}
}

func TestAutoSelectBoolField_HighCardinality(t *testing.T) {
	// Even with high cardinality, bool fields should use BITMAP.
	stats := &FieldStats{
		Cardinality: 500,
		TotalCount:  1000,
		IsNumeric:   false,
		IsBool:      true,
	}
	result := AutoSelectIndexType(stats)
	if result != ScalarIndexBitmap {
		t.Errorf("AutoSelectIndexType(bool, high-cardinality) = %v, want ScalarIndexBitmap", result)
	}
}

func TestAutoSelectLowCardinality(t *testing.T) {
	stats := &FieldStats{
		Cardinality: 50,
		TotalCount:  10000,
		IsNumeric:   false,
		IsBool:      false,
	}
	result := AutoSelectIndexType(stats)
	if result != ScalarIndexBitmap {
		t.Errorf("AutoSelectIndexType(low-cardinality) = %v, want ScalarIndexBitmap", result)
	}
}

func TestAutoSelectLowCardinality_Boundary(t *testing.T) {
	// Exactly at threshold (100) should still use BITMAP.
	stats := &FieldStats{
		Cardinality: 100,
		TotalCount:  10000,
		IsNumeric:   false,
		IsBool:      false,
	}
	result := AutoSelectIndexType(stats)
	if result != ScalarIndexBitmap {
		t.Errorf("AutoSelectIndexType(cardinality=100) = %v, want ScalarIndexBitmap", result)
	}
}

func TestAutoSelectNumericField(t *testing.T) {
	stats := &FieldStats{
		Cardinality: 5000,
		TotalCount:  100000,
		IsNumeric:   true,
		IsBool:      false,
	}
	result := AutoSelectIndexType(stats)
	if result != ScalarIndexSorted {
		t.Errorf("AutoSelectIndexType(numeric) = %v, want ScalarIndexSorted", result)
	}
}

func TestAutoSelectNumericField_LowCardinality(t *testing.T) {
	// Low-cardinality numeric should use BITMAP (cardinality check comes first).
	stats := &FieldStats{
		Cardinality: 10,
		TotalCount:  1000,
		IsNumeric:   true,
		IsBool:      false,
	}
	result := AutoSelectIndexType(stats)
	if result != ScalarIndexBitmap {
		t.Errorf("AutoSelectIndexType(numeric, low-cardinality) = %v, want ScalarIndexBitmap", result)
	}
}

func TestAutoSelectHighCardinalityString(t *testing.T) {
	stats := &FieldStats{
		Cardinality: 50000,
		TotalCount:  100000,
		IsNumeric:   false,
		IsBool:      false,
	}
	result := AutoSelectIndexType(stats)
	if result != ScalarIndexInverted {
		t.Errorf("AutoSelectIndexType(high-cardinality string) = %v, want ScalarIndexInverted", result)
	}
}

func TestAutoSelectHighCardinalityString_JustAboveThreshold(t *testing.T) {
	stats := &FieldStats{
		Cardinality: 101,
		TotalCount:  10000,
		IsNumeric:   false,
		IsBool:      false,
	}
	result := AutoSelectIndexType(stats)
	if result != ScalarIndexInverted {
		t.Errorf("AutoSelectIndexType(cardinality=101) = %v, want ScalarIndexInverted", result)
	}
}

func TestNewScalarIndex_Bitmap(t *testing.T) {
	idx, err := NewScalarIndex(ScalarIndexBitmap)
	if err != nil {
		t.Fatalf("NewScalarIndex(Bitmap) returned error: %v", err)
	}
	defer idx.Close()
	if idx.Type() != ScalarIndexBitmap {
		t.Errorf("Type() = %v, want ScalarIndexBitmap", idx.Type())
	}
}

func TestNewScalarIndex_Inverted(t *testing.T) {
	idx, err := NewScalarIndex(ScalarIndexInverted)
	if err != nil {
		t.Fatalf("NewScalarIndex(Inverted) returned error: %v", err)
	}
	defer idx.Close()
	if idx.Type() != ScalarIndexInverted {
		t.Errorf("Type() = %v, want ScalarIndexInverted", idx.Type())
	}
}

func TestNewScalarIndex_Sorted(t *testing.T) {
	idx, err := NewScalarIndex(ScalarIndexSorted)
	if err != nil {
		t.Fatalf("NewScalarIndex(Sorted) returned error: %v", err)
	}
	defer idx.Close()
	if idx.Type() != ScalarIndexSorted {
		t.Errorf("Type() = %v, want ScalarIndexSorted", idx.Type())
	}
}

func TestNewScalarIndex_UnsupportedType(t *testing.T) {
	_, err := NewScalarIndex(ScalarIndexType(99))
	if err == nil {
		t.Fatal("NewScalarIndex(99) should return error")
	}
	var typeErr *UnsupportedScalarIndexTypeError
	if !errors.As(err, &typeErr) {
		t.Errorf("expected UnsupportedScalarIndexTypeError, got %T: %v", err, err)
	}
}
