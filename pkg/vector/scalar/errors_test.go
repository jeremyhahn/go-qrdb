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
	"errors"
	"strings"
	"testing"
)

func TestScalarIndexNotFoundError(t *testing.T) {
	err := &ScalarIndexNotFoundError{Field: "category"}
	expected := "scalar index not found for field: category"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestScalarIndexNotFoundError_EmptyField(t *testing.T) {
	err := &ScalarIndexNotFoundError{Field: ""}
	if !strings.Contains(err.Error(), "scalar index not found") {
		t.Errorf("Error() = %q, should contain 'scalar index not found'", err.Error())
	}
}

func TestUnsupportedScalarIndexTypeError(t *testing.T) {
	err := &UnsupportedScalarIndexTypeError{Type: ScalarIndexType(99)}
	if !strings.Contains(err.Error(), "unsupported scalar index type") {
		t.Errorf("Error() = %q, should contain 'unsupported scalar index type'", err.Error())
	}
}

func TestUnsupportedScalarIndexTypeError_KnownType(t *testing.T) {
	err := &UnsupportedScalarIndexTypeError{Type: ScalarIndexBitmap}
	expected := "unsupported scalar index type: BITMAP"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestScalarQueryError(t *testing.T) {
	cause := &NilValueError{Op: "test"}
	err := &ScalarQueryError{Op: QueryEqual, Value: "test", Err: cause}
	if !strings.Contains(err.Error(), "scalar query failed") {
		t.Errorf("Error() = %q, should contain 'scalar query failed'", err.Error())
	}
}

func TestScalarQueryError_Unwrap(t *testing.T) {
	cause := &NilValueError{Op: "test"}
	err := &ScalarQueryError{Op: QueryEqual, Value: "test", Err: cause}
	if !errors.Is(err, cause) {
		t.Error("Unwrap() should return cause")
	}
}

func TestUnsupportedQueryOpError(t *testing.T) {
	err := &UnsupportedQueryOpError{Op: QueryGreaterThan, IndexType: ScalarIndexBitmap}
	expected := "operator > not supported by BITMAP index"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestUnsupportedQueryOpError_DifferentIndex(t *testing.T) {
	err := &UnsupportedQueryOpError{Op: QueryRange, IndexType: ScalarIndexInverted}
	if !strings.Contains(err.Error(), "INVERTED") {
		t.Errorf("Error() = %q, should contain 'INVERTED'", err.Error())
	}
}

func TestNilValueError(t *testing.T) {
	err := &NilValueError{Op: "Add"}
	expected := "nil value not allowed for operation: Add"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestNilValueError_EmptyOp(t *testing.T) {
	err := &NilValueError{Op: ""}
	if !strings.Contains(err.Error(), "nil value not allowed") {
		t.Errorf("Error() = %q, should contain 'nil value not allowed'", err.Error())
	}
}

func TestInvalidRangeError(t *testing.T) {
	err := &InvalidRangeError{Min: "100", Max: "50"}
	expected := "invalid range: min=100 max=50"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestInvalidRangeError_EmptyBounds(t *testing.T) {
	err := &InvalidRangeError{Min: "", Max: ""}
	if !strings.Contains(err.Error(), "invalid range") {
		t.Errorf("Error() = %q, should contain 'invalid range'", err.Error())
	}
}

func TestInvalidValueTypeError(t *testing.T) {
	err := &InvalidValueTypeError{Value: 42, Expected: "string"}
	if !strings.Contains(err.Error(), "expected string") {
		t.Errorf("Error() = %q, should contain 'expected string'", err.Error())
	}
}

func TestInvalidValueTypeError_NilValue(t *testing.T) {
	err := &InvalidValueTypeError{Value: nil, Expected: "string"}
	if !strings.Contains(err.Error(), "nil") {
		t.Errorf("Error() = %q, should contain 'nil'", err.Error())
	}
}

func TestIndexClosedError(t *testing.T) {
	err := &IndexClosedError{IndexType: ScalarIndexBitmap}
	expected := "BITMAP index is closed"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestIndexClosedError_SortedType(t *testing.T) {
	err := &IndexClosedError{IndexType: ScalarIndexSorted}
	expected := "SORTED index is closed"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestDuplicateValueError(t *testing.T) {
	err := &DuplicateValueError{VectorID: 42}
	expected := "duplicate entry for vector ID 42"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestDuplicateValueError_ZeroID(t *testing.T) {
	err := &DuplicateValueError{VectorID: 0}
	expected := "duplicate entry for vector ID 0"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}
