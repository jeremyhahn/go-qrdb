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
	"sort"
	"testing"
)

func TestBitmapIndex_AddAndQueryEqual(t *testing.T) {
	idx := NewBitmapIndex()
	defer idx.Close()

	if err := idx.Add(1, "red"); err != nil {
		t.Fatalf("Add(1, red) returned error: %v", err)
	}
	if err := idx.Add(2, "blue"); err != nil {
		t.Fatalf("Add(2, blue) returned error: %v", err)
	}
	if err := idx.Add(3, "red"); err != nil {
		t.Fatalf("Add(3, red) returned error: %v", err)
	}

	result, err := idx.Query(QueryEqual, "red")
	if err != nil {
		t.Fatalf("Query(Equal, red) returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 3 {
		t.Errorf("Query(Equal, red) = %v, want [1, 3]", result)
	}

	result, err = idx.Query(QueryEqual, "green")
	if err != nil {
		t.Fatalf("Query(Equal, green) returned error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("Query(Equal, green) = %v, want []", result)
	}
}

func TestBitmapIndex_AddError_ClosedIndex(t *testing.T) {
	idx := NewBitmapIndex()
	idx.Close()

	err := idx.Add(1, "red")
	if err == nil {
		t.Fatal("Add on closed index should return error")
	}
	var closedErr *IndexClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected IndexClosedError, got %T: %v", err, err)
	}
}

func TestBitmapIndex_QueryNotEqual(t *testing.T) {
	idx := NewBitmapIndex()
	defer idx.Close()

	idx.Add(1, "red")
	idx.Add(2, "blue")
	idx.Add(3, "red")
	idx.Add(4, "green")

	result, err := idx.Query(QueryNotEqual, "red")
	if err != nil {
		t.Fatalf("Query(NotEqual, red) returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 2 || result[1] != 4 {
		t.Errorf("Query(NotEqual, red) = %v, want [2, 4]", result)
	}
}

func TestBitmapIndex_QueryNotEqual_NonexistentValue(t *testing.T) {
	idx := NewBitmapIndex()
	defer idx.Close()

	idx.Add(1, "red")
	idx.Add(2, "blue")

	result, err := idx.Query(QueryNotEqual, "green")
	if err != nil {
		t.Fatalf("Query(NotEqual, green) returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 2 {
		t.Errorf("Query(NotEqual, green) = %v, want [1, 2]", result)
	}
}

func TestBitmapIndex_QueryIn(t *testing.T) {
	idx := NewBitmapIndex()
	defer idx.Close()

	idx.Add(1, "red")
	idx.Add(2, "blue")
	idx.Add(3, "green")
	idx.Add(4, "yellow")

	result, err := idx.Query(QueryIn, []string{"red", "green"})
	if err != nil {
		t.Fatalf("Query(In, [red, green]) returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 3 {
		t.Errorf("Query(In, [red, green]) = %v, want [1, 3]", result)
	}
}

func TestBitmapIndex_QueryIn_InvalidType(t *testing.T) {
	idx := NewBitmapIndex()
	defer idx.Close()

	_, err := idx.Query(QueryIn, "not-a-slice")
	if err == nil {
		t.Fatal("Query(In, string) should return error")
	}
	var typeErr *InvalidValueTypeError
	if !errors.As(err, &typeErr) {
		t.Errorf("expected InvalidValueTypeError, got %T: %v", err, err)
	}
}

func TestBitmapIndex_QueryNotIn(t *testing.T) {
	idx := NewBitmapIndex()
	defer idx.Close()

	idx.Add(1, "red")
	idx.Add(2, "blue")
	idx.Add(3, "green")
	idx.Add(4, "yellow")

	result, err := idx.Query(QueryNotIn, []string{"red", "green"})
	if err != nil {
		t.Fatalf("Query(NotIn, [red, green]) returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 2 || result[1] != 4 {
		t.Errorf("Query(NotIn, [red, green]) = %v, want [2, 4]", result)
	}
}

func TestBitmapIndex_QueryNotIn_InvalidType(t *testing.T) {
	idx := NewBitmapIndex()
	defer idx.Close()

	_, err := idx.Query(QueryNotIn, 42)
	if err == nil {
		t.Fatal("Query(NotIn, int) should return error")
	}
	var typeErr *InvalidValueTypeError
	if !errors.As(err, &typeErr) {
		t.Errorf("expected InvalidValueTypeError, got %T: %v", err, err)
	}
}

func TestBitmapIndex_QueryIsNull(t *testing.T) {
	idx := NewBitmapIndex()
	defer idx.Close()

	idx.Add(1, "red")
	idx.Add(2, "")
	idx.Add(3, nil)

	result, err := idx.Query(QueryIsNull, nil)
	if err != nil {
		t.Fatalf("Query(IsNull) returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 2 || result[1] != 3 {
		t.Errorf("Query(IsNull) = %v, want [2, 3]", result)
	}
}

func TestBitmapIndex_QueryIsNotNull(t *testing.T) {
	idx := NewBitmapIndex()
	defer idx.Close()

	idx.Add(1, "red")
	idx.Add(2, "")
	idx.Add(3, "blue")

	result, err := idx.Query(QueryIsNotNull, nil)
	if err != nil {
		t.Fatalf("Query(IsNotNull) returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 3 {
		t.Errorf("Query(IsNotNull) = %v, want [1, 3]", result)
	}
}

func TestBitmapIndex_Remove(t *testing.T) {
	idx := NewBitmapIndex()
	defer idx.Close()

	idx.Add(1, "red")
	idx.Add(2, "red")
	idx.Add(3, "blue")

	if err := idx.Remove(1); err != nil {
		t.Fatalf("Remove(1) returned error: %v", err)
	}

	result, err := idx.Query(QueryEqual, "red")
	if err != nil {
		t.Fatalf("Query(Equal, red) returned error: %v", err)
	}
	if len(result) != 1 || result[0] != 2 {
		t.Errorf("after Remove(1), Query(Equal, red) = %v, want [2]", result)
	}
	if idx.Count() != 2 {
		t.Errorf("after Remove(1), Count() = %d, want 2", idx.Count())
	}
}

func TestBitmapIndex_Remove_ClosedIndex(t *testing.T) {
	idx := NewBitmapIndex()
	idx.Add(1, "red")
	idx.Close()

	err := idx.Remove(1)
	if err == nil {
		t.Fatal("Remove on closed index should return error")
	}
	var closedErr *IndexClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected IndexClosedError, got %T: %v", err, err)
	}
}

func TestBitmapIndex_Remove_Nonexistent(t *testing.T) {
	idx := NewBitmapIndex()
	defer idx.Close()

	err := idx.Remove(999)
	if err != nil {
		t.Errorf("Remove(999) returned error for nonexistent ID: %v", err)
	}
}

func TestBitmapIndex_DuplicateAdd(t *testing.T) {
	idx := NewBitmapIndex()
	defer idx.Close()

	idx.Add(1, "red")
	idx.Add(1, "blue") // update existing

	result, err := idx.Query(QueryEqual, "red")
	if err != nil {
		t.Fatalf("Query(Equal, red) returned error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("after update, Query(Equal, red) = %v, want []", result)
	}

	result, err = idx.Query(QueryEqual, "blue")
	if err != nil {
		t.Fatalf("Query(Equal, blue) returned error: %v", err)
	}
	if len(result) != 1 || result[0] != 1 {
		t.Errorf("after update, Query(Equal, blue) = %v, want [1]", result)
	}

	if idx.Count() != 1 {
		t.Errorf("after duplicate add, Count() = %d, want 1", idx.Count())
	}
}

func TestBitmapIndex_DuplicateAdd_SameValue(t *testing.T) {
	idx := NewBitmapIndex()
	defer idx.Close()

	idx.Add(1, "red")
	idx.Add(1, "red") // same value, should be no-op

	result, err := idx.Query(QueryEqual, "red")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if len(result) != 1 || result[0] != 1 {
		t.Errorf("Query(Equal, red) = %v, want [1]", result)
	}
	if idx.Count() != 1 {
		t.Errorf("Count() = %d, want 1", idx.Count())
	}
}

func TestBitmapIndex_Count(t *testing.T) {
	idx := NewBitmapIndex()
	defer idx.Close()

	if idx.Count() != 0 {
		t.Errorf("empty index Count() = %d, want 0", idx.Count())
	}

	idx.Add(1, "a")
	idx.Add(2, "b")
	idx.Add(3, "c")

	if idx.Count() != 3 {
		t.Errorf("Count() = %d, want 3", idx.Count())
	}

	idx.Remove(2)
	if idx.Count() != 2 {
		t.Errorf("after Remove, Count() = %d, want 2", idx.Count())
	}
}

func TestBitmapIndex_EmptyIndex(t *testing.T) {
	idx := NewBitmapIndex()
	defer idx.Close()

	result, err := idx.Query(QueryEqual, "anything")
	if err != nil {
		t.Fatalf("Query on empty index returned error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("Query on empty index = %v, want []", result)
	}

	result, err = idx.QueryRange("0", "100")
	if err != nil {
		t.Fatalf("QueryRange on empty index returned error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("QueryRange on empty index = %v, want []", result)
	}
}

func TestBitmapIndex_Close(t *testing.T) {
	idx := NewBitmapIndex()
	idx.Add(1, "red")

	if err := idx.Close(); err != nil {
		t.Fatalf("Close() returned error: %v", err)
	}

	_, err := idx.Query(QueryEqual, "red")
	if err == nil {
		t.Fatal("Query on closed index should return error")
	}
	var closedErr *IndexClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected IndexClosedError, got %T: %v", err, err)
	}
}

func TestBitmapIndex_Type(t *testing.T) {
	idx := NewBitmapIndex()
	defer idx.Close()

	if idx.Type() != ScalarIndexBitmap {
		t.Errorf("Type() = %v, want ScalarIndexBitmap", idx.Type())
	}
}

func TestBitmapIndex_UnsupportedOp(t *testing.T) {
	idx := NewBitmapIndex()
	defer idx.Close()

	_, err := idx.Query(QueryGreaterThan, "5")
	if err == nil {
		t.Fatal("Query(GreaterThan) on bitmap should return error")
	}
	var opErr *UnsupportedQueryOpError
	if !errors.As(err, &opErr) {
		t.Errorf("expected UnsupportedQueryOpError, got %T: %v", err, err)
	}
}

func TestBitmapIndex_QueryRange(t *testing.T) {
	idx := NewBitmapIndex()
	defer idx.Close()

	idx.Add(1, "10")
	idx.Add(2, "20")
	idx.Add(3, "30")
	idx.Add(4, "5")

	result, err := idx.QueryRange("10", "25")
	if err != nil {
		t.Fatalf("QueryRange returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 2 {
		t.Errorf("QueryRange(10, 25) = %v, want [1, 2]", result)
	}
}

func TestBitmapIndex_QueryRange_ClosedIndex(t *testing.T) {
	idx := NewBitmapIndex()
	idx.Close()

	_, err := idx.QueryRange("0", "100")
	if err == nil {
		t.Fatal("QueryRange on closed index should return error")
	}
}
