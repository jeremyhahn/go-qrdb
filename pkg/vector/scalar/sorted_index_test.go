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

func TestSortedIndex_AddAndQueryEqual(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	if err := idx.Add(1, "100"); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}
	if err := idx.Add(2, "200"); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}
	if err := idx.Add(3, "100"); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	result, err := idx.Query(QueryEqual, "100")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 3 {
		t.Errorf("Query(Equal, 100) = %v, want [1, 3]", result)
	}
}

func TestSortedIndex_AddError_ClosedIndex(t *testing.T) {
	idx := NewSortedIndex()
	idx.Close()

	err := idx.Add(1, "100")
	if err == nil {
		t.Fatal("Add on closed index should return error")
	}
	var closedErr *IndexClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected IndexClosedError, got %T: %v", err, err)
	}
}

func TestSortedIndex_QueryNotEqual(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "10")
	idx.Add(2, "20")
	idx.Add(3, "30")

	result, err := idx.Query(QueryNotEqual, "20")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 3 {
		t.Errorf("Query(NotEqual, 20) = %v, want [1, 3]", result)
	}
}

func TestSortedIndex_QueryNotEqual_Nonexistent(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "10")
	idx.Add(2, "20")

	result, err := idx.Query(QueryNotEqual, "99")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 {
		t.Errorf("Query(NotEqual, 99) returned %d results, want 2", len(result))
	}
}

func TestSortedIndex_QueryGreaterThan(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "10")
	idx.Add(2, "20")
	idx.Add(3, "30")
	idx.Add(4, "5")

	result, err := idx.Query(QueryGreaterThan, "10")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 2 || result[1] != 3 {
		t.Errorf("Query(GreaterThan, 10) = %v, want [2, 3]", result)
	}
}

func TestSortedIndex_QueryGreaterThan_NoMatch(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "10")
	idx.Add(2, "20")

	result, err := idx.Query(QueryGreaterThan, "100")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("Query(GreaterThan, 100) = %v, want []", result)
	}
}

func TestSortedIndex_QueryGreaterEqual(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "10")
	idx.Add(2, "20")
	idx.Add(3, "30")

	result, err := idx.Query(QueryGreaterEqual, "20")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 2 || result[1] != 3 {
		t.Errorf("Query(GreaterEqual, 20) = %v, want [2, 3]", result)
	}
}

func TestSortedIndex_QueryGreaterEqual_ExactMatch(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "10")

	result, err := idx.Query(QueryGreaterEqual, "10")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if len(result) != 1 || result[0] != 1 {
		t.Errorf("Query(GreaterEqual, 10) = %v, want [1]", result)
	}
}

func TestSortedIndex_QueryLessThan(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "10")
	idx.Add(2, "20")
	idx.Add(3, "30")

	result, err := idx.Query(QueryLessThan, "20")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if len(result) != 1 || result[0] != 1 {
		t.Errorf("Query(LessThan, 20) = %v, want [1]", result)
	}
}

func TestSortedIndex_QueryLessThan_NoMatch(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "10")

	result, err := idx.Query(QueryLessThan, "5")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("Query(LessThan, 5) = %v, want []", result)
	}
}

func TestSortedIndex_QueryLessEqual(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "10")
	idx.Add(2, "20")
	idx.Add(3, "30")

	result, err := idx.Query(QueryLessEqual, "20")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 2 {
		t.Errorf("Query(LessEqual, 20) = %v, want [1, 2]", result)
	}
}

func TestSortedIndex_QueryLessEqual_ExactMatch(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "10")

	result, err := idx.Query(QueryLessEqual, "10")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if len(result) != 1 || result[0] != 1 {
		t.Errorf("Query(LessEqual, 10) = %v, want [1]", result)
	}
}

func TestSortedIndex_QueryRange(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "10")
	idx.Add(2, "20")
	idx.Add(3, "30")
	idx.Add(4, "40")
	idx.Add(5, "5")

	result, err := idx.QueryRange("10", "30")
	if err != nil {
		t.Fatalf("QueryRange returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 3 || result[0] != 1 || result[1] != 2 || result[2] != 3 {
		t.Errorf("QueryRange(10, 30) = %v, want [1, 2, 3]", result)
	}
}

func TestSortedIndex_QueryRange_ClosedIndex(t *testing.T) {
	idx := NewSortedIndex()
	idx.Close()

	_, err := idx.QueryRange("0", "100")
	if err == nil {
		t.Fatal("QueryRange on closed index should return error")
	}
}

func TestSortedIndex_QueryRange_Empty(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "10")
	idx.Add(2, "20")

	result, err := idx.QueryRange("50", "100")
	if err != nil {
		t.Fatalf("QueryRange returned error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("QueryRange(50, 100) = %v, want []", result)
	}
}

func TestSortedIndex_NumericOrdering(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	// Verify that "10" > "9" numerically, not lexicographically
	idx.Add(1, "9")
	idx.Add(2, "10")
	idx.Add(3, "100")
	idx.Add(4, "2")

	result, err := idx.Query(QueryGreaterThan, "9")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 2 || result[1] != 3 {
		t.Errorf("Query(GreaterThan, 9) = %v, want [2, 3] (10, 100)", result)
	}
}

func TestSortedIndex_NumericOrdering_LessThan(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "9")
	idx.Add(2, "10")
	idx.Add(3, "2")

	result, err := idx.Query(QueryLessThan, "10")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 3 {
		t.Errorf("Query(LessThan, 10) = %v, want [1, 3] (9, 2)", result)
	}
}

func TestSortedIndex_QueryIn(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "10")
	idx.Add(2, "20")
	idx.Add(3, "30")

	result, err := idx.Query(QueryIn, []string{"10", "30"})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 3 {
		t.Errorf("Query(In) = %v, want [1, 3]", result)
	}
}

func TestSortedIndex_QueryIn_InvalidType(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	_, err := idx.Query(QueryIn, 42)
	if err == nil {
		t.Fatal("Query(In, int) should return error")
	}
}

func TestSortedIndex_QueryNotIn(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "10")
	idx.Add(2, "20")
	idx.Add(3, "30")

	result, err := idx.Query(QueryNotIn, []string{"20"})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 3 {
		t.Errorf("Query(NotIn) = %v, want [1, 3]", result)
	}
}

func TestSortedIndex_QueryNotIn_InvalidType(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	_, err := idx.Query(QueryNotIn, true)
	if err == nil {
		t.Fatal("Query(NotIn, bool) should return error")
	}
}

func TestSortedIndex_QueryIsNull(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "10")
	idx.Add(2, "")
	idx.Add(3, nil)

	result, err := idx.Query(QueryIsNull, nil)
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 2 || result[1] != 3 {
		t.Errorf("Query(IsNull) = %v, want [2, 3]", result)
	}
}

func TestSortedIndex_QueryIsNotNull(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "10")
	idx.Add(2, "")
	idx.Add(3, "20")

	result, err := idx.Query(QueryIsNotNull, nil)
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 3 {
		t.Errorf("Query(IsNotNull) = %v, want [1, 3]", result)
	}
}

func TestSortedIndex_Remove(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "10")
	idx.Add(2, "20")
	idx.Add(3, "10")

	if err := idx.Remove(1); err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}

	result, err := idx.Query(QueryEqual, "10")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if len(result) != 1 || result[0] != 3 {
		t.Errorf("after Remove(1), Query(Equal, 10) = %v, want [3]", result)
	}
	if idx.Count() != 2 {
		t.Errorf("after Remove, Count() = %d, want 2", idx.Count())
	}
}

func TestSortedIndex_Remove_ClosedIndex(t *testing.T) {
	idx := NewSortedIndex()
	idx.Add(1, "10")
	idx.Close()

	err := idx.Remove(1)
	if err == nil {
		t.Fatal("Remove on closed index should return error")
	}
}

func TestSortedIndex_Remove_Nonexistent(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	err := idx.Remove(999)
	if err != nil {
		t.Errorf("Remove(999) returned error: %v", err)
	}
}

func TestSortedIndex_DuplicateAdd(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "10")
	idx.Add(1, "20")

	result, err := idx.Query(QueryEqual, "10")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("after update, Query(Equal, 10) = %v, want []", result)
	}

	result, err = idx.Query(QueryEqual, "20")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if len(result) != 1 || result[0] != 1 {
		t.Errorf("after update, Query(Equal, 20) = %v, want [1]", result)
	}
	if idx.Count() != 1 {
		t.Errorf("Count() = %d, want 1", idx.Count())
	}
}

func TestSortedIndex_DuplicateAdd_SameValue(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	idx.Add(1, "10")
	idx.Add(1, "10")

	if idx.Count() != 1 {
		t.Errorf("Count() = %d, want 1", idx.Count())
	}
}

func TestSortedIndex_Count(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	if idx.Count() != 0 {
		t.Errorf("empty Count() = %d, want 0", idx.Count())
	}

	idx.Add(1, "10")
	idx.Add(2, "20")
	if idx.Count() != 2 {
		t.Errorf("Count() = %d, want 2", idx.Count())
	}
}

func TestSortedIndex_EmptyIndex(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	result, err := idx.Query(QueryEqual, "anything")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("Query on empty index = %v, want []", result)
	}

	result, err = idx.QueryRange("0", "100")
	if err != nil {
		t.Fatalf("QueryRange returned error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("QueryRange on empty index = %v, want []", result)
	}
}

func TestSortedIndex_Close(t *testing.T) {
	idx := NewSortedIndex()
	idx.Add(1, "10")

	if err := idx.Close(); err != nil {
		t.Fatalf("Close() returned error: %v", err)
	}

	_, err := idx.Query(QueryEqual, "10")
	if err == nil {
		t.Fatal("Query on closed index should return error")
	}
}

func TestSortedIndex_Type(t *testing.T) {
	idx := NewSortedIndex()
	defer idx.Close()

	if idx.Type() != ScalarIndexSorted {
		t.Errorf("Type() = %v, want ScalarIndexSorted", idx.Type())
	}
}

func TestSortedIndex_QueryClosed(t *testing.T) {
	idx := NewSortedIndex()
	idx.Close()

	_, err := idx.Query(QueryEqual, "10")
	if err == nil {
		t.Fatal("Query on closed index should return error")
	}
	var closedErr *IndexClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected IndexClosedError, got %T: %v", err, err)
	}
}
