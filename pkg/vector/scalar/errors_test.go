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
