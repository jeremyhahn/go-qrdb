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

func TestInvertedIndex_AddAndQueryEqual(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	if err := idx.Add(1, "alice@example.com"); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}
	if err := idx.Add(2, "bob@example.com"); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}
	if err := idx.Add(3, "alice@example.com"); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	result, err := idx.Query(QueryEqual, "alice@example.com")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 3 {
		t.Errorf("Query(Equal) = %v, want [1, 3]", result)
	}
}

func TestInvertedIndex_AddError_ClosedIndex(t *testing.T) {
	idx := NewInvertedIndex()
	idx.Close()

	err := idx.Add(1, "test")
	if err == nil {
		t.Fatal("Add on closed index should return error")
	}
	var closedErr *IndexClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected IndexClosedError, got %T: %v", err, err)
	}
}

func TestInvertedIndex_QueryNotEqual(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	idx.Add(1, "alice")
	idx.Add(2, "bob")
	idx.Add(3, "charlie")

	result, err := idx.Query(QueryNotEqual, "bob")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 3 {
		t.Errorf("Query(NotEqual, bob) = %v, want [1, 3]", result)
	}
}

func TestInvertedIndex_QueryNotEqual_Nonexistent(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	idx.Add(1, "alice")
	idx.Add(2, "bob")

	result, err := idx.Query(QueryNotEqual, "charlie")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 2 {
		t.Errorf("Query(NotEqual, charlie) = %v, want [1, 2]", result)
	}
}

func TestInvertedIndex_QueryIn(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	idx.Add(1, "alice")
	idx.Add(2, "bob")
	idx.Add(3, "charlie")
	idx.Add(4, "diana")

	result, err := idx.Query(QueryIn, []string{"alice", "charlie"})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 3 {
		t.Errorf("Query(In) = %v, want [1, 3]", result)
	}
}

func TestInvertedIndex_QueryIn_InvalidType(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	_, err := idx.Query(QueryIn, 42)
	if err == nil {
		t.Fatal("Query(In, int) should return error")
	}
	var typeErr *InvalidValueTypeError
	if !errors.As(err, &typeErr) {
		t.Errorf("expected InvalidValueTypeError, got %T: %v", err, err)
	}
}

func TestInvertedIndex_QueryNotIn(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	idx.Add(1, "alice")
	idx.Add(2, "bob")
	idx.Add(3, "charlie")

	result, err := idx.Query(QueryNotIn, []string{"bob"})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 3 {
		t.Errorf("Query(NotIn) = %v, want [1, 3]", result)
	}
}

func TestInvertedIndex_QueryNotIn_InvalidType(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	_, err := idx.Query(QueryNotIn, true)
	if err == nil {
		t.Fatal("Query(NotIn, bool) should return error")
	}
}

func TestInvertedIndex_QueryIsNull(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	idx.Add(1, "alice")
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

func TestInvertedIndex_QueryIsNotNull(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	idx.Add(1, "alice")
	idx.Add(2, "")
	idx.Add(3, "bob")

	result, err := idx.Query(QueryIsNotNull, nil)
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 3 {
		t.Errorf("Query(IsNotNull) = %v, want [1, 3]", result)
	}
}

func TestInvertedIndex_Remove(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	idx.Add(1, "alice")
	idx.Add(2, "alice")
	idx.Add(3, "bob")

	if err := idx.Remove(1); err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}

	result, err := idx.Query(QueryEqual, "alice")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if len(result) != 1 || result[0] != 2 {
		t.Errorf("after Remove(1), Query(Equal, alice) = %v, want [2]", result)
	}
	if idx.Count() != 2 {
		t.Errorf("after Remove, Count() = %d, want 2", idx.Count())
	}
}

func TestInvertedIndex_Remove_ClosedIndex(t *testing.T) {
	idx := NewInvertedIndex()
	idx.Add(1, "test")
	idx.Close()

	err := idx.Remove(1)
	if err == nil {
		t.Fatal("Remove on closed index should return error")
	}
}

func TestInvertedIndex_Remove_Nonexistent(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	err := idx.Remove(999)
	if err != nil {
		t.Errorf("Remove(999) returned error for nonexistent ID: %v", err)
	}
}

func TestInvertedIndex_DuplicateAdd(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	idx.Add(1, "alice")
	idx.Add(1, "bob")

	result, err := idx.Query(QueryEqual, "alice")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("after update, Query(Equal, alice) = %v, want []", result)
	}

	result, err = idx.Query(QueryEqual, "bob")
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if len(result) != 1 || result[0] != 1 {
		t.Errorf("after update, Query(Equal, bob) = %v, want [1]", result)
	}
	if idx.Count() != 1 {
		t.Errorf("Count() = %d, want 1", idx.Count())
	}
}

func TestInvertedIndex_DuplicateAdd_SameValue(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	idx.Add(1, "alice")
	idx.Add(1, "alice")

	if idx.Count() != 1 {
		t.Errorf("Count() = %d, want 1", idx.Count())
	}
}

func TestInvertedIndex_Count(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	if idx.Count() != 0 {
		t.Errorf("empty Count() = %d, want 0", idx.Count())
	}

	idx.Add(1, "a")
	idx.Add(2, "b")
	if idx.Count() != 2 {
		t.Errorf("Count() = %d, want 2", idx.Count())
	}
}

func TestInvertedIndex_EmptyIndex(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	result, err := idx.Query(QueryEqual, "anything")
	if err != nil {
		t.Fatalf("Query on empty index returned error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("Query on empty index = %v, want []", result)
	}
}

func TestInvertedIndex_Close(t *testing.T) {
	idx := NewInvertedIndex()
	idx.Add(1, "test")

	if err := idx.Close(); err != nil {
		t.Fatalf("Close() returned error: %v", err)
	}

	_, err := idx.Query(QueryEqual, "test")
	if err == nil {
		t.Fatal("Query on closed index should return error")
	}
}

func TestInvertedIndex_Type(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	if idx.Type() != ScalarIndexInverted {
		t.Errorf("Type() = %v, want ScalarIndexInverted", idx.Type())
	}
}

func TestInvertedIndex_UnsupportedOp(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	_, err := idx.Query(QueryGreaterThan, "5")
	if err == nil {
		t.Fatal("Query(GreaterThan) on inverted index should return error")
	}
	var opErr *UnsupportedQueryOpError
	if !errors.As(err, &opErr) {
		t.Errorf("expected UnsupportedQueryOpError, got %T: %v", err, err)
	}
}

func TestInvertedIndex_QueryContains(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	idx.Add(1, "hello world")
	idx.Add(2, "foo bar")
	idx.Add(3, "hello foo")

	result, err := idx.QueryContains("hello")
	if err != nil {
		t.Fatalf("QueryContains returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 3 {
		t.Errorf("QueryContains(hello) = %v, want [1, 3]", result)
	}
}

func TestInvertedIndex_QueryContains_ClosedIndex(t *testing.T) {
	idx := NewInvertedIndex()
	idx.Close()

	_, err := idx.QueryContains("test")
	if err == nil {
		t.Fatal("QueryContains on closed index should return error")
	}
}

func TestInvertedIndex_QueryContains_NoMatch(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	idx.Add(1, "hello")

	result, err := idx.QueryContains("xyz")
	if err != nil {
		t.Fatalf("QueryContains returned error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("QueryContains(xyz) = %v, want []", result)
	}
}

func TestInvertedIndex_QueryPrefixMatch(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	idx.Add(1, "prefix_alpha")
	idx.Add(2, "prefix_beta")
	idx.Add(3, "other_gamma")

	result, err := idx.QueryPrefixMatch("prefix_")
	if err != nil {
		t.Fatalf("QueryPrefixMatch returned error: %v", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) != 2 || result[0] != 1 || result[1] != 2 {
		t.Errorf("QueryPrefixMatch(prefix_) = %v, want [1, 2]", result)
	}
}

func TestInvertedIndex_QueryPrefixMatch_ClosedIndex(t *testing.T) {
	idx := NewInvertedIndex()
	idx.Close()

	_, err := idx.QueryPrefixMatch("test")
	if err == nil {
		t.Fatal("QueryPrefixMatch on closed index should return error")
	}
}

func TestInvertedIndex_QueryPrefixMatch_NoMatch(t *testing.T) {
	idx := NewInvertedIndex()
	defer idx.Close()

	idx.Add(1, "hello")

	result, err := idx.QueryPrefixMatch("xyz")
	if err != nil {
		t.Fatalf("QueryPrefixMatch returned error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("QueryPrefixMatch(xyz) = %v, want []", result)
	}
}

func TestInvertedIndex_QueryRange(t *testing.T) {
	idx := NewInvertedIndex()
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

func TestInvertedIndex_QueryRange_ClosedIndex(t *testing.T) {
	idx := NewInvertedIndex()
	idx.Close()

	_, err := idx.QueryRange("0", "100")
	if err == nil {
		t.Fatal("QueryRange on closed index should return error")
	}
}
