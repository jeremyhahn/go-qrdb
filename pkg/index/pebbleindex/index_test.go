// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pebbleindex

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// openPebble creates a PebbleDB in a temporary directory and registers
// cleanup. It fails the test immediately on error.
func openPebble(t *testing.T) *pebble.DB {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("pebble.Close: %v", err)
		}
	})
	return db
}

// newPebbleIdx creates a pebble Index backed by a temp PebbleDB. It
// fails the test immediately on error.
func newPebbleIdx(t *testing.T, name string, unique bool) (*Index, *pebble.DB) {
	t.Helper()
	db := openPebble(t)
	idx, err := New(db, name, unique, index.IndexValueTypeString)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return idx, db
}

// ---------------------------------------------------------------------------
// New constructor
// ---------------------------------------------------------------------------

func TestNew_NilDB(t *testing.T) {
	_, err := New(nil, "test", false, index.IndexValueTypeString)
	if err == nil {
		t.Fatal("New(nil db): expected NilDBError, got nil")
	}
	var nde *index.NilDBError
	if !errors.As(err, &nde) {
		t.Fatalf("New(nil db): expected index.NilDBError, got %T: %v", err, err)
	}
}

func TestNew_EmptyName(t *testing.T) {
	db := openPebble(t)
	_, err := New(db, "", false, index.IndexValueTypeString)
	if err == nil {
		t.Fatal("New(empty name): expected EmptyNameError, got nil")
	}
	var ene *index.EmptyNameError
	if !errors.As(err, &ene) {
		t.Fatalf("New(empty name): expected index.EmptyNameError, got %T: %v", err, err)
	}
}

func TestNew_ValidParams(t *testing.T) {
	db := openPebble(t)
	idx, err := New(db, "test", false, index.IndexValueTypeString)
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	if idx == nil {
		t.Fatal("New: returned nil index")
	}
}

func TestNew_ValidParamsUnique(t *testing.T) {
	db := openPebble(t)
	idx, err := New(db, "email", true, index.IndexValueTypeString)
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	if !idx.IsUnique() {
		t.Error("IsUnique: expected true")
	}
}

// ---------------------------------------------------------------------------
// Name / IsUnique accessors
// ---------------------------------------------------------------------------

func TestIndex_Name_ReturnsConfiguredName(t *testing.T) {
	idx, _ := newPebbleIdx(t, "users", false)
	if got := idx.Name(); got != "users" {
		t.Errorf("Name: got %q, want %q", got, "users")
	}
}

func TestIndex_IsUnique_True(t *testing.T) {
	idx, _ := newPebbleIdx(t, "email", true)
	if !idx.IsUnique() {
		t.Error("IsUnique: expected true for unique index")
	}
}

func TestIndex_IsUnique_False(t *testing.T) {
	idx, _ := newPebbleIdx(t, "tags", false)
	if idx.IsUnique() {
		t.Error("IsUnique: expected false for non-unique index")
	}
}

// ---------------------------------------------------------------------------
// Put
// ---------------------------------------------------------------------------

func TestIndex_Put_Normal(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	if err := idx.Put([]byte("1"), []byte("alice")); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}
	ids, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if len(ids) != 1 || !bytes.Equal(ids[0], []byte("1")) {
		t.Errorf("Get: got %v, want [[1]]", ids)
	}
}

func TestIndex_Put_MultipleEntitiesSameValue(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	if err := idx.Put([]byte("1"), []byte("shared")); err != nil {
		t.Fatalf("Put(1): unexpected error: %v", err)
	}
	if err := idx.Put([]byte("2"), []byte("shared")); err != nil {
		t.Fatalf("Put(2): unexpected error: %v", err)
	}
	ids, err := idx.Get([]byte("shared"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("Get: got %d entities, want 2", len(ids))
	}
}

func TestIndex_Put_DuplicateEntityKeyIdempotent(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	if err := idx.Put([]byte("1"), []byte("alice")); err != nil {
		t.Fatalf("Put(first): unexpected error: %v", err)
	}
	// PebbleDB Set is idempotent for the same key.
	if err := idx.Put([]byte("1"), []byte("alice")); err != nil {
		t.Fatalf("Put(duplicate): unexpected error: %v", err)
	}
	ids, _ := idx.Get([]byte("alice"))
	if len(ids) != 1 {
		t.Errorf("Get: got %d entries, want 1", len(ids))
	}
}

func TestIndex_Put_UniqueConstraintViolation(t *testing.T) {
	idx, _ := newPebbleIdx(t, "email", true)
	if err := idx.Put([]byte("1"), []byte("alice@test.com")); err != nil {
		t.Fatalf("Put(first): unexpected error: %v", err)
	}
	err := idx.Put([]byte("2"), []byte("alice@test.com"))
	if err == nil {
		t.Fatal("Put(second): expected UniqueViolationError, got nil")
	}
	var uv *index.UniqueViolationError
	if !errors.As(err, &uv) {
		t.Fatalf("Put(second): expected index.UniqueViolationError, got %T: %v", err, err)
	}
	if !bytes.Equal(uv.ExistingKey, []byte("1")) || !bytes.Equal(uv.NewKey, []byte("2")) {
		t.Errorf("UniqueViolation: ExistingKey=%s NewKey=%s, want 1, 2", uv.ExistingKey, uv.NewKey)
	}
}

func TestIndex_Put_UniqueSameEntityKeyAllowed(t *testing.T) {
	idx, _ := newPebbleIdx(t, "email", true)
	if err := idx.Put([]byte("1"), []byte("alice@test.com")); err != nil {
		t.Fatalf("Put(first): unexpected error: %v", err)
	}
	if err := idx.Put([]byte("1"), []byte("alice@test.com")); err != nil {
		t.Fatalf("Put(same entity): unexpected error: %v", err)
	}
}

func TestIndex_Put_NilFieldValue(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	err := idx.Put([]byte("1"), nil)
	if err == nil {
		t.Fatal("Put(nil): expected NilFieldValueError, got nil")
	}
	var nfv *index.NilFieldValueError
	if !errors.As(err, &nfv) {
		t.Fatalf("Put(nil): expected index.NilFieldValueError, got %T: %v", err, err)
	}
}

func TestIndex_Put_AfterClose(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	err := idx.Put([]byte("1"), []byte("value"))
	if err == nil {
		t.Fatal("Put(after close): expected ClosedError, got nil")
	}
	var ce *index.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Put(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

func TestIndex_Get_ExistingValue(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put([]byte("42"), []byte("alice"))
	ids, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if len(ids) != 1 || !bytes.Equal(ids[0], []byte("42")) {
		t.Errorf("Get: got %v, want [[42]]", ids)
	}
}

func TestIndex_Get_NonExistent(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	ids, err := idx.Get([]byte("nonexistent"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("Get: got %v, want empty slice", ids)
	}
}

func TestIndex_Get_NilFieldValue(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_, err := idx.Get(nil)
	if err == nil {
		t.Fatal("Get(nil): expected NilFieldValueError, got nil")
	}
	var nfv *index.NilFieldValueError
	if !errors.As(err, &nfv) {
		t.Fatalf("Get(nil): expected index.NilFieldValueError, got %T: %v", err, err)
	}
}

func TestIndex_Get_AfterClose(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	_, err := idx.Get([]byte("value"))
	if err == nil {
		t.Fatal("Get(after close): expected ClosedError, got nil")
	}
	var ce *index.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Get(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestIndex_Delete_Existing(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put([]byte("1"), []byte("alice"))
	if err := idx.Delete([]byte("1"), []byte("alice")); err != nil {
		t.Fatalf("Delete: unexpected error: %v", err)
	}
	ids, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get after delete: unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("Get after delete: got %v, want empty", ids)
	}
}

func TestIndex_Delete_NonExistent(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	err := idx.Delete([]byte("999"), []byte("nonexistent"))
	if err != nil {
		t.Fatalf("Delete(nonexistent): unexpected error: %v", err)
	}
}

func TestIndex_Delete_LeavesOtherEntities(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put([]byte("1"), []byte("shared"))
	_ = idx.Put([]byte("2"), []byte("shared"))
	if err := idx.Delete([]byte("1"), []byte("shared")); err != nil {
		t.Fatalf("Delete: unexpected error: %v", err)
	}
	ids, _ := idx.Get([]byte("shared"))
	if len(ids) != 1 || !bytes.Equal(ids[0], []byte("2")) {
		t.Errorf("Get after partial delete: got %v, want [[2]]", ids)
	}
}

func TestIndex_Delete_NilFieldValue(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	err := idx.Delete([]byte("1"), nil)
	if err == nil {
		t.Fatal("Delete(nil): expected NilFieldValueError, got nil")
	}
	var nfv *index.NilFieldValueError
	if !errors.As(err, &nfv) {
		t.Fatalf("Delete(nil): expected index.NilFieldValueError, got %T: %v", err, err)
	}
}

func TestIndex_Delete_AfterClose(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	err := idx.Delete([]byte("1"), []byte("value"))
	if err == nil {
		t.Fatal("Delete(after close): expected ClosedError, got nil")
	}
	var ce *index.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Delete(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Scan
// ---------------------------------------------------------------------------

func TestIndex_Scan_FullRange(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put([]byte("1"), []byte("a"))
	_ = idx.Put([]byte("2"), []byte("b"))
	_ = idx.Put([]byte("3"), []byte("c"))

	result, err := idx.Scan([]byte("a"), []byte("d"))
	if err != nil {
		t.Fatalf("Scan: unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Errorf("Scan: got %d entries, want 3", len(result))
	}
}

func TestIndex_Scan_PartialRange(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put([]byte("1"), []byte("apple"))
	_ = idx.Put([]byte("2"), []byte("banana"))
	_ = idx.Put([]byte("3"), []byte("cherry"))

	result, err := idx.Scan([]byte("banana"), []byte("cherry"))
	if err != nil {
		t.Fatalf("Scan: unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("Scan: got %d entries, want 1 (banana only, end is exclusive)", len(result))
	}
	if _, ok := result["banana"]; !ok {
		t.Error("Scan: expected 'banana' in result")
	}
}

func TestIndex_Scan_NilStart(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put([]byte("1"), []byte("apple"))
	_ = idx.Put([]byte("2"), []byte("cherry"))

	// A nil start means unbounded below: [-inf, "cherry").
	result, err := idx.Scan(nil, []byte("cherry"))
	if err != nil {
		t.Fatalf("Scan(nil start): unexpected error: %v", err)
	}
	if _, ok := result["apple"]; !ok {
		t.Error("Scan(nil start): expected 'apple' in result")
	}
	if _, ok := result["cherry"]; ok {
		t.Error("Scan(nil start): 'cherry' should be excluded (exclusive end)")
	}
}

func TestIndex_Scan_NilEnd(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put([]byte("1"), []byte("apple"))
	_ = idx.Put([]byte("2"), []byte("cherry"))

	// A nil end means unbounded above: ["banana", +inf).
	result, err := idx.Scan([]byte("banana"), nil)
	if err != nil {
		t.Fatalf("Scan(nil end): unexpected error: %v", err)
	}
	if _, ok := result["cherry"]; !ok {
		t.Error("Scan(nil end): expected 'cherry' in result")
	}
	if _, ok := result["apple"]; ok {
		t.Error("Scan(nil end): 'apple' should be excluded (below start)")
	}
}

func TestIndex_Scan_NilStartAndEnd(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put([]byte("1"), []byte("apple"))
	_ = idx.Put([]byte("2"), []byte("cherry"))

	result, err := idx.Scan(nil, nil)
	if err != nil {
		t.Fatalf("Scan(nil, nil): unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Scan(nil, nil): got %d entries, want 2", len(result))
	}
}

func TestIndex_Scan_EmptyIndex(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	result, err := idx.Scan([]byte("a"), []byte("z"))
	if err != nil {
		t.Fatalf("Scan(empty): unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("Scan(empty): got %d entries, want 0", len(result))
	}
}

func TestIndex_Scan_AfterClose(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	_, err := idx.Scan([]byte("a"), []byte("z"))
	if err == nil {
		t.Fatal("Scan(after close): expected ClosedError, got nil")
	}
	var ce *index.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Scan(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// GetAll
// ---------------------------------------------------------------------------

func TestIndex_GetAll_MultipleValues(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put([]byte("3"), []byte("c"))
	_ = idx.Put([]byte("1"), []byte("a"))
	_ = idx.Put([]byte("2"), []byte("b"))

	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: unexpected error: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("GetAll: got %d keys, want 3", len(ids))
	}
	// Result should be sorted.
	for i := 1; i < len(ids); i++ {
		if bytes.Compare(ids[i], ids[i-1]) <= 0 {
			t.Errorf("GetAll: result not sorted at index %d: %v", i, ids)
		}
	}
}

func TestIndex_GetAll_Deduplicated(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put([]byte("1"), []byte("a"))
	_ = idx.Put([]byte("1"), []byte("b")) // Same entity key, different field value.

	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: unexpected error: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("GetAll: got %d keys, want 1 (deduplicated)", len(ids))
	}
}

func TestIndex_GetAll_EmptyIndex(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll(empty): unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("GetAll(empty): got %v, want empty", ids)
	}
}

func TestIndex_GetAll_AfterClose(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	_, err := idx.GetAll()
	if err == nil {
		t.Fatal("GetAll(after close): expected ClosedError, got nil")
	}
	var ce *index.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("GetAll(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Clear
// ---------------------------------------------------------------------------

func TestIndex_Clear_RemovesAll(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put([]byte("1"), []byte("a"))
	_ = idx.Put([]byte("2"), []byte("b"))

	if err := idx.Clear(); err != nil {
		t.Fatalf("Clear: unexpected error: %v", err)
	}

	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll after clear: unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("GetAll after clear: got %v, want empty", ids)
	}
}

func TestIndex_Clear_CanPutAfterClear(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put([]byte("1"), []byte("a"))
	_ = idx.Clear()

	if err := idx.Put([]byte("2"), []byte("b")); err != nil {
		t.Fatalf("Put after clear: unexpected error: %v", err)
	}
	ids, _ := idx.Get([]byte("b"))
	if len(ids) != 1 || !bytes.Equal(ids[0], []byte("2")) {
		t.Errorf("Get after clear+put: got %v, want [[2]]", ids)
	}
}

func TestIndex_Clear_AfterClose(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	err := idx.Clear()
	if err == nil {
		t.Fatal("Clear(after close): expected ClosedError, got nil")
	}
	var ce *index.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Clear(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestIndex_Close_SubsequentPutFails(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	if err := idx.Close(); err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}
	err := idx.Put([]byte("1"), []byte("val"))
	var ce *index.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Put(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestIndex_Close_SubsequentGetFails(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	_, err := idx.Get([]byte("val"))
	var ce *index.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Get(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestIndex_Close_SubsequentDeleteFails(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	err := idx.Delete([]byte("1"), []byte("val"))
	var ce *index.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Delete(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestIndex_Close_SubsequentScanFails(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	_, err := idx.Scan([]byte("a"), []byte("z"))
	var ce *index.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Scan(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestIndex_Close_SubsequentGetAllFails(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	_, err := idx.GetAll()
	var ce *index.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("GetAll(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestIndex_Close_SubsequentClearFails(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	err := idx.Clear()
	var ce *index.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Clear(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Concurrent access
// ---------------------------------------------------------------------------

func TestIndex_ConcurrentPutGet(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	const goroutines = 20
	const opsPerGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Writers.
	for g := 0; g < goroutines; g++ {
		go func(base int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				_ = idx.Put([]byte(fmt.Sprintf("%d", base+i)), []byte("shared-value"))
			}
		}(g * opsPerGoroutine)
	}

	// Readers.
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				_, _ = idx.Get([]byte("shared-value"))
			}
		}()
	}

	wg.Wait()

	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: unexpected error: %v", err)
	}
	expected := goroutines * opsPerGoroutine
	if len(ids) != expected {
		t.Errorf("GetAll: got %d keys, want %d", len(ids), expected)
	}
}

func TestIndex_ConcurrentPutDelete(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	const count = 100

	// Populate.
	for i := 0; i < count; i++ {
		_ = idx.Put([]byte(fmt.Sprintf("%d", i)), []byte("val"))
	}

	var wg sync.WaitGroup
	wg.Add(count)
	for i := 0; i < count; i++ {
		go func(id int) {
			defer wg.Done()
			_ = idx.Delete([]byte(fmt.Sprintf("%d", id)), []byte("val"))
		}(i)
	}
	wg.Wait()

	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("GetAll after concurrent delete: got %d keys, want 0", len(ids))
	}
}

// ---------------------------------------------------------------------------
// Large number of entries
// ---------------------------------------------------------------------------

func TestIndex_LargeEntryCount(t *testing.T) {
	idx, _ := newPebbleIdx(t, "large", false)
	const count = 1000

	for i := 0; i < count; i++ {
		key := []byte(strconv.Itoa(i))
		if err := idx.Put(key, []byte("field-"+strconv.Itoa(i))); err != nil {
			t.Fatalf("Put(%d): unexpected error: %v", i, err)
		}
	}

	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: unexpected error: %v", err)
	}
	if len(ids) != count {
		t.Errorf("GetAll: got %d keys, want %d", len(ids), count)
	}
}

func TestIndex_LargeEntryCount_Scan(t *testing.T) {
	idx, _ := newPebbleIdx(t, "large", false)
	const count = 500

	for i := 0; i < count; i++ {
		fieldVal := []byte("f" + strconv.Itoa(i))
		if err := idx.Put([]byte(strconv.Itoa(i)), fieldVal); err != nil {
			t.Fatalf("Put(%d): unexpected error: %v", i, err)
		}
	}

	// Scan should return entries in range.
	result, err := idx.Scan([]byte("f1"), []byte("f2"))
	if err != nil {
		t.Fatalf("Scan: unexpected error: %v", err)
	}
	// f1, f10-f19, f100-f199 all fall in [f1, f2).
	if len(result) == 0 {
		t.Error("Scan: expected non-empty result for large index")
	}
}

// ---------------------------------------------------------------------------
// Index isolation (different named indexes in same DB)
// ---------------------------------------------------------------------------

func TestIndex_Isolation_DifferentNames(t *testing.T) {
	db := openPebble(t)

	idx1, err := New(db, "users", false, index.IndexValueTypeString)
	if err != nil {
		t.Fatalf("New(users): %v", err)
	}
	idx2, err := New(db, "orders", false, index.IndexValueTypeString)
	if err != nil {
		t.Fatalf("New(orders): %v", err)
	}

	_ = idx1.Put([]byte("1"), []byte("alice"))
	_ = idx2.Put([]byte("2"), []byte("order-1"))

	ids1, _ := idx1.GetAll()
	ids2, _ := idx2.GetAll()

	if len(ids1) != 1 || !bytes.Equal(ids1[0], []byte("1")) {
		t.Errorf("users index: got %v, want [[1]]", ids1)
	}
	if len(ids2) != 1 || !bytes.Equal(ids2[0], []byte("2")) {
		t.Errorf("orders index: got %v, want [[2]]", ids2)
	}
}

func TestIndex_Isolation_ClearDoesNotAffectOther(t *testing.T) {
	db := openPebble(t)

	idx1, _ := New(db, "first", false, index.IndexValueTypeString)
	idx2, _ := New(db, "second", false, index.IndexValueTypeString)

	_ = idx1.Put([]byte("1"), []byte("a"))
	_ = idx2.Put([]byte("2"), []byte("b"))

	_ = idx1.Clear()

	ids1, _ := idx1.GetAll()
	ids2, _ := idx2.GetAll()

	if len(ids1) != 0 {
		t.Errorf("first index after clear: got %v, want empty", ids1)
	}
	if len(ids2) != 1 {
		t.Errorf("second index after first's clear: got %v, want [[2]]", ids2)
	}
}

// ---------------------------------------------------------------------------
// Interface compliance
// ---------------------------------------------------------------------------

func TestIndex_ImplementsIndex(t *testing.T) {
	var _ index.Index = (*Index)(nil)
}

func TestIndex_ImplementsIndex_ViaConstructor(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	var _ index.Index = idx
}

// ---------------------------------------------------------------------------
// Numeric index: correct byte-order comparison for range scans
// ---------------------------------------------------------------------------

// newNumericIdx opens a new PebbleDB and creates a numeric-type pebble Index
// for the given name. The test is registered for cleanup.
func newNumericIdx(t *testing.T, name string) *Index {
	t.Helper()
	db := openPebble(t)
	idx, err := New(db, name, false, index.IndexValueTypeNumeric)
	if err != nil {
		t.Fatalf("New(numeric): %v", err)
	}
	return idx
}

// TestIndex_Numeric_PutGet verifies that an exact-match Get on a
// numeric index returns the correct entity keys.
func TestIndex_Numeric_PutGet(t *testing.T) {
	idx := newNumericIdx(t, "score")

	if err := idx.Put([]byte("doc-a"), []byte("123")); err != nil {
		t.Fatalf("Put(123): %v", err)
	}
	if err := idx.Put([]byte("doc-b"), []byte("5")); err != nil {
		t.Fatalf("Put(5): %v", err)
	}

	keys, err := idx.Get([]byte("123"))
	if err != nil {
		t.Fatalf("Get(123): %v", err)
	}
	if len(keys) != 1 || !bytes.Equal(keys[0], []byte("doc-a")) {
		t.Errorf("Get(123) = %v, want [[doc-a]]", keys)
	}

	// Value not present.
	missing, err := idx.Get([]byte("999"))
	if err != nil {
		t.Fatalf("Get(999): %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("Get(999) = %v, want empty", missing)
	}
}

// TestIndex_Numeric_Get_NonNumeric verifies that querying a numeric
// index with a non-numeric string returns an empty result (skip, not error).
func TestIndex_Numeric_Get_NonNumeric(t *testing.T) {
	idx := newNumericIdx(t, "score2")

	keys, err := idx.Get([]byte("not-a-number"))
	if err != nil {
		t.Fatalf("Get(non-numeric) returned unexpected error: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("Get(non-numeric) = %v, want empty", keys)
	}
}

// TestIndex_Numeric_ScanRange verifies that a numeric range scan
// [1, 222) includes 5 and 123 but excludes 1234, which a lexicographic
// scan would incorrectly include.
func TestIndex_Numeric_ScanRange(t *testing.T) {
	idx := newNumericIdx(t, "f2")

	docs := map[string]string{
		"doc-1234": "1234",
		"doc-123":  "123",
		"doc-5":    "5",
	}
	for key, val := range docs {
		if err := idx.Put([]byte(key), []byte(val)); err != nil {
			t.Fatalf("Put(%s=%s): %v", key, val, err)
		}
	}

	// Scan [1, 222) — must include 5 and 123, must exclude 1234.
	result, err := idx.Scan([]byte("1"), []byte("222"))
	if err != nil {
		t.Fatalf("Scan[1,222): %v", err)
	}

	if _, has1234 := result["1234"]; has1234 {
		t.Error("Scan[1,222) incorrectly included 1234")
	}
	if _, has123 := result["123"]; !has123 {
		t.Error("Scan[1,222) missing 123")
	}
	if _, has5 := result["5"]; !has5 {
		t.Error("Scan[1,222) missing 5")
	}
	if len(result) != 2 {
		t.Errorf("Scan[1,222) returned %d entries, want 2: %v", len(result), result)
	}
}

// TestIndex_Numeric_ScanWideRange verifies a wide numeric range [1, 2000)
// includes all three entries: 5, 123, and 1234.
func TestIndex_Numeric_ScanWideRange(t *testing.T) {
	idx := newNumericIdx(t, "f2wide")

	docs := map[string]string{
		"doc-1234": "1234",
		"doc-123":  "123",
		"doc-5":    "5",
	}
	for key, val := range docs {
		if err := idx.Put([]byte(key), []byte(val)); err != nil {
			t.Fatalf("Put(%s=%s): %v", key, val, err)
		}
	}

	result, err := idx.Scan([]byte("1"), []byte("2000"))
	if err != nil {
		t.Fatalf("Scan[1,2000): %v", err)
	}

	for _, v := range []string{"5", "123", "1234"} {
		if _, ok := result[v]; !ok {
			t.Errorf("Scan[1,2000) missing %s; result: %v", v, result)
		}
	}
	if len(result) != 3 {
		t.Errorf("Scan[1,2000) returned %d entries, want 3: %v", len(result), result)
	}
}

// TestIndex_Numeric_Put_NonNumericSkipped verifies that inserting
// a non-numeric value into a numeric index is silently skipped.
func TestIndex_Numeric_Put_NonNumericSkipped(t *testing.T) {
	idx := newNumericIdx(t, "skiptest")

	// Non-numeric value must not return an error.
	if err := idx.Put([]byte("doc-x"), []byte("abc")); err != nil {
		t.Fatalf("Put(non-numeric) should be silently skipped, got error: %v", err)
	}

	// The index must remain empty.
	all, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("GetAll after skipped put = %v, want empty", all)
	}
}

// TestIndex_Numeric_Delete verifies that deleting a numeric value
// removes the corresponding entry.
func TestIndex_Numeric_Delete(t *testing.T) {
	idx := newNumericIdx(t, "deltest")

	if err := idx.Put([]byte("doc-a"), []byte("42")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := idx.Delete([]byte("doc-a"), []byte("42")); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	keys, err := idx.Get([]byte("42"))
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("Get after delete = %v, want empty", keys)
	}
}

// TestIndex_Numeric_NegativeValues verifies that negative numbers
// sort correctly — all negative values must be below positive values.
func TestIndex_Numeric_NegativeValues(t *testing.T) {
	idx := newNumericIdx(t, "negtest")

	_ = idx.Put([]byte("doc-pos"), []byte("10"))
	_ = idx.Put([]byte("doc-neg"), []byte("-5"))

	// Scan [0, 100) must include 10 but not -5.
	result, err := idx.Scan([]byte("0"), []byte("100"))
	if err != nil {
		t.Fatalf("Scan[0,100): %v", err)
	}
	if _, hasNeg := result["-5"]; hasNeg {
		t.Error("Scan[0,100) should not include -5")
	}
	if _, hasPos := result["10"]; !hasPos {
		t.Error("Scan[0,100) missing 10")
	}
}

// TestNew_NumericValueType verifies that constructing with
// index.IndexValueTypeNumeric sets the field correctly.
func TestNew_NumericValueType(t *testing.T) {
	db := openPebble(t)
	idx, err := New(db, "numeric-idx", false, index.IndexValueTypeNumeric)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if idx.valueType != index.IndexValueTypeNumeric {
		t.Errorf("valueType = %v, want IndexValueTypeNumeric", idx.valueType)
	}
}
