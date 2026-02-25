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

package index

import (
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2"
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

// newPebbleIdx creates a PebbleIndex backed by a temp PebbleDB. It
// fails the test immediately on error.
func newPebbleIdx(t *testing.T, name string, unique bool) (*PebbleIndex, *pebble.DB) {
	t.Helper()
	db := openPebble(t)
	idx, err := NewPebbleIndex(db, name, unique)
	if err != nil {
		t.Fatalf("NewPebbleIndex: %v", err)
	}
	return idx, db
}

// ---------------------------------------------------------------------------
// NewPebbleIndex constructor
// ---------------------------------------------------------------------------

func TestNewPebbleIndex_NilDB(t *testing.T) {
	_, err := NewPebbleIndex(nil, "test", false)
	if err == nil {
		t.Fatal("NewPebbleIndex(nil db): expected NilDBError, got nil")
	}
	var nde *NilDBError
	if !errors.As(err, &nde) {
		t.Fatalf("NewPebbleIndex(nil db): expected NilDBError, got %T: %v", err, err)
	}
}

func TestNewPebbleIndex_EmptyName(t *testing.T) {
	db := openPebble(t)
	_, err := NewPebbleIndex(db, "", false)
	if err == nil {
		t.Fatal("NewPebbleIndex(empty name): expected EmptyNameError, got nil")
	}
	var ene *EmptyNameError
	if !errors.As(err, &ene) {
		t.Fatalf("NewPebbleIndex(empty name): expected EmptyNameError, got %T: %v", err, err)
	}
}

func TestNewPebbleIndex_ValidParams(t *testing.T) {
	db := openPebble(t)
	idx, err := NewPebbleIndex(db, "test", false)
	if err != nil {
		t.Fatalf("NewPebbleIndex: unexpected error: %v", err)
	}
	if idx == nil {
		t.Fatal("NewPebbleIndex: returned nil index")
	}
}

func TestNewPebbleIndex_ValidParamsUnique(t *testing.T) {
	db := openPebble(t)
	idx, err := NewPebbleIndex(db, "email", true)
	if err != nil {
		t.Fatalf("NewPebbleIndex: unexpected error: %v", err)
	}
	if !idx.IsUnique() {
		t.Error("IsUnique: expected true")
	}
}

// ---------------------------------------------------------------------------
// Name / IsUnique accessors
// ---------------------------------------------------------------------------

func TestPebbleIndex_Name_ReturnsConfiguredName(t *testing.T) {
	idx, _ := newPebbleIdx(t, "users", false)
	if got := idx.Name(); got != "users" {
		t.Errorf("Name: got %q, want %q", got, "users")
	}
}

func TestPebbleIndex_IsUnique_True(t *testing.T) {
	idx, _ := newPebbleIdx(t, "email", true)
	if !idx.IsUnique() {
		t.Error("IsUnique: expected true for unique index")
	}
}

func TestPebbleIndex_IsUnique_False(t *testing.T) {
	idx, _ := newPebbleIdx(t, "tags", false)
	if idx.IsUnique() {
		t.Error("IsUnique: expected false for non-unique index")
	}
}

// ---------------------------------------------------------------------------
// Put
// ---------------------------------------------------------------------------

func TestPebbleIndex_Put_Normal(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	if err := idx.Put(1, []byte("alice")); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}
	ids, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Errorf("Get: got %v, want [1]", ids)
	}
}

func TestPebbleIndex_Put_MultipleEntitiesSameValue(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	if err := idx.Put(1, []byte("shared")); err != nil {
		t.Fatalf("Put(1): unexpected error: %v", err)
	}
	if err := idx.Put(2, []byte("shared")); err != nil {
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

func TestPebbleIndex_Put_DuplicateEntityIDIdempotent(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	if err := idx.Put(1, []byte("alice")); err != nil {
		t.Fatalf("Put(first): unexpected error: %v", err)
	}
	// PebbleDB Set is idempotent for the same key.
	if err := idx.Put(1, []byte("alice")); err != nil {
		t.Fatalf("Put(duplicate): unexpected error: %v", err)
	}
	ids, _ := idx.Get([]byte("alice"))
	if len(ids) != 1 {
		t.Errorf("Get: got %d entries, want 1", len(ids))
	}
}

func TestPebbleIndex_Put_UniqueConstraintViolation(t *testing.T) {
	idx, _ := newPebbleIdx(t, "email", true)
	if err := idx.Put(1, []byte("alice@test.com")); err != nil {
		t.Fatalf("Put(first): unexpected error: %v", err)
	}
	err := idx.Put(2, []byte("alice@test.com"))
	if err == nil {
		t.Fatal("Put(second): expected UniqueViolationError, got nil")
	}
	var uv *UniqueViolationError
	if !errors.As(err, &uv) {
		t.Fatalf("Put(second): expected UniqueViolationError, got %T: %v", err, err)
	}
	if uv.ExistingID != 1 || uv.NewID != 2 {
		t.Errorf("UniqueViolation: ExistingID=%d NewID=%d, want 1, 2", uv.ExistingID, uv.NewID)
	}
}

func TestPebbleIndex_Put_UniqueSameEntityIDAllowed(t *testing.T) {
	idx, _ := newPebbleIdx(t, "email", true)
	if err := idx.Put(1, []byte("alice@test.com")); err != nil {
		t.Fatalf("Put(first): unexpected error: %v", err)
	}
	if err := idx.Put(1, []byte("alice@test.com")); err != nil {
		t.Fatalf("Put(same entity): unexpected error: %v", err)
	}
}

func TestPebbleIndex_Put_NilFieldValue(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	err := idx.Put(1, nil)
	if err == nil {
		t.Fatal("Put(nil): expected NilFieldValueError, got nil")
	}
	var nfv *NilFieldValueError
	if !errors.As(err, &nfv) {
		t.Fatalf("Put(nil): expected NilFieldValueError, got %T: %v", err, err)
	}
}

func TestPebbleIndex_Put_AfterClose(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	err := idx.Put(1, []byte("value"))
	if err == nil {
		t.Fatal("Put(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Put(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

func TestPebbleIndex_Get_ExistingValue(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put(42, []byte("alice"))
	ids, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if len(ids) != 1 || ids[0] != 42 {
		t.Errorf("Get: got %v, want [42]", ids)
	}
}

func TestPebbleIndex_Get_NonExistent(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	ids, err := idx.Get([]byte("nonexistent"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("Get: got %v, want empty slice", ids)
	}
}

func TestPebbleIndex_Get_NilFieldValue(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_, err := idx.Get(nil)
	if err == nil {
		t.Fatal("Get(nil): expected NilFieldValueError, got nil")
	}
	var nfv *NilFieldValueError
	if !errors.As(err, &nfv) {
		t.Fatalf("Get(nil): expected NilFieldValueError, got %T: %v", err, err)
	}
}

func TestPebbleIndex_Get_AfterClose(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	_, err := idx.Get([]byte("value"))
	if err == nil {
		t.Fatal("Get(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Get(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestPebbleIndex_Delete_Existing(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put(1, []byte("alice"))
	if err := idx.Delete(1, []byte("alice")); err != nil {
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

func TestPebbleIndex_Delete_NonExistent(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	err := idx.Delete(999, []byte("nonexistent"))
	if err != nil {
		t.Fatalf("Delete(nonexistent): unexpected error: %v", err)
	}
}

func TestPebbleIndex_Delete_LeavesOtherEntities(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put(1, []byte("shared"))
	_ = idx.Put(2, []byte("shared"))
	if err := idx.Delete(1, []byte("shared")); err != nil {
		t.Fatalf("Delete: unexpected error: %v", err)
	}
	ids, _ := idx.Get([]byte("shared"))
	if len(ids) != 1 || ids[0] != 2 {
		t.Errorf("Get after partial delete: got %v, want [2]", ids)
	}
}

func TestPebbleIndex_Delete_NilFieldValue(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	err := idx.Delete(1, nil)
	if err == nil {
		t.Fatal("Delete(nil): expected NilFieldValueError, got nil")
	}
	var nfv *NilFieldValueError
	if !errors.As(err, &nfv) {
		t.Fatalf("Delete(nil): expected NilFieldValueError, got %T: %v", err, err)
	}
}

func TestPebbleIndex_Delete_AfterClose(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	err := idx.Delete(1, []byte("value"))
	if err == nil {
		t.Fatal("Delete(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Delete(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Scan
// ---------------------------------------------------------------------------

func TestPebbleIndex_Scan_FullRange(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put(1, []byte("a"))
	_ = idx.Put(2, []byte("b"))
	_ = idx.Put(3, []byte("c"))

	result, err := idx.Scan([]byte("a"), []byte("d"))
	if err != nil {
		t.Fatalf("Scan: unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Errorf("Scan: got %d entries, want 3", len(result))
	}
}

func TestPebbleIndex_Scan_PartialRange(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put(1, []byte("apple"))
	_ = idx.Put(2, []byte("banana"))
	_ = idx.Put(3, []byte("cherry"))

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

func TestPebbleIndex_Scan_NilStart(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_, err := idx.Scan(nil, []byte("end"))
	if err == nil {
		t.Fatal("Scan(nil start): expected NilFieldValueError, got nil")
	}
	var nfv *NilFieldValueError
	if !errors.As(err, &nfv) {
		t.Fatalf("Scan(nil start): expected NilFieldValueError, got %T: %v", err, err)
	}
}

func TestPebbleIndex_Scan_NilEnd(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_, err := idx.Scan([]byte("start"), nil)
	if err == nil {
		t.Fatal("Scan(nil end): expected NilFieldValueError, got nil")
	}
	var nfv *NilFieldValueError
	if !errors.As(err, &nfv) {
		t.Fatalf("Scan(nil end): expected NilFieldValueError, got %T: %v", err, err)
	}
}

func TestPebbleIndex_Scan_EmptyIndex(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	result, err := idx.Scan([]byte("a"), []byte("z"))
	if err != nil {
		t.Fatalf("Scan(empty): unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("Scan(empty): got %d entries, want 0", len(result))
	}
}

func TestPebbleIndex_Scan_AfterClose(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	_, err := idx.Scan([]byte("a"), []byte("z"))
	if err == nil {
		t.Fatal("Scan(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Scan(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// GetAll
// ---------------------------------------------------------------------------

func TestPebbleIndex_GetAll_MultipleValues(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put(3, []byte("c"))
	_ = idx.Put(1, []byte("a"))
	_ = idx.Put(2, []byte("b"))

	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: unexpected error: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("GetAll: got %d IDs, want 3", len(ids))
	}
	// Result should be sorted.
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Errorf("GetAll: result not sorted at index %d: %v", i, ids)
		}
	}
}

func TestPebbleIndex_GetAll_Deduplicated(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put(1, []byte("a"))
	_ = idx.Put(1, []byte("b")) // Same entity ID, different field value.

	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: unexpected error: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("GetAll: got %d IDs, want 1 (deduplicated)", len(ids))
	}
}

func TestPebbleIndex_GetAll_EmptyIndex(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll(empty): unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("GetAll(empty): got %v, want empty", ids)
	}
}

func TestPebbleIndex_GetAll_AfterClose(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	_, err := idx.GetAll()
	if err == nil {
		t.Fatal("GetAll(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("GetAll(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Clear
// ---------------------------------------------------------------------------

func TestPebbleIndex_Clear_RemovesAll(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put(1, []byte("a"))
	_ = idx.Put(2, []byte("b"))

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

func TestPebbleIndex_Clear_CanPutAfterClear(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Put(1, []byte("a"))
	_ = idx.Clear()

	if err := idx.Put(2, []byte("b")); err != nil {
		t.Fatalf("Put after clear: unexpected error: %v", err)
	}
	ids, _ := idx.Get([]byte("b"))
	if len(ids) != 1 || ids[0] != 2 {
		t.Errorf("Get after clear+put: got %v, want [2]", ids)
	}
}

func TestPebbleIndex_Clear_AfterClose(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	err := idx.Clear()
	if err == nil {
		t.Fatal("Clear(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Clear(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestPebbleIndex_Close_SubsequentPutFails(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	if err := idx.Close(); err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}
	err := idx.Put(1, []byte("val"))
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Put(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestPebbleIndex_Close_SubsequentGetFails(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	_, err := idx.Get([]byte("val"))
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Get(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestPebbleIndex_Close_SubsequentDeleteFails(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	err := idx.Delete(1, []byte("val"))
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Delete(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestPebbleIndex_Close_SubsequentScanFails(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	_, err := idx.Scan([]byte("a"), []byte("z"))
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Scan(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestPebbleIndex_Close_SubsequentGetAllFails(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	_, err := idx.GetAll()
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("GetAll(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestPebbleIndex_Close_SubsequentClearFails(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	_ = idx.Close()
	err := idx.Clear()
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Clear(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Concurrent access
// ---------------------------------------------------------------------------

func TestPebbleIndex_ConcurrentPutGet(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	const goroutines = 20
	const opsPerGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Writers.
	for g := 0; g < goroutines; g++ {
		go func(base uint64) {
			defer wg.Done()
			for i := uint64(0); i < opsPerGoroutine; i++ {
				_ = idx.Put(base+i, []byte("shared-value"))
			}
		}(uint64(g) * opsPerGoroutine)
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
		t.Errorf("GetAll: got %d IDs, want %d", len(ids), expected)
	}
}

func TestPebbleIndex_ConcurrentPutDelete(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	const count = 100

	// Populate.
	for i := uint64(0); i < count; i++ {
		_ = idx.Put(i, []byte("val"))
	}

	var wg sync.WaitGroup
	wg.Add(count)
	for i := uint64(0); i < count; i++ {
		go func(id uint64) {
			defer wg.Done()
			_ = idx.Delete(id, []byte("val"))
		}(i)
	}
	wg.Wait()

	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("GetAll after concurrent delete: got %d IDs, want 0", len(ids))
	}
}

// ---------------------------------------------------------------------------
// Large number of entries
// ---------------------------------------------------------------------------

func TestPebbleIndex_LargeEntryCount(t *testing.T) {
	idx, _ := newPebbleIdx(t, "large", false)
	const count = 1000

	for i := uint64(0); i < count; i++ {
		if err := idx.Put(i, []byte("field-"+strconv.FormatUint(i, 10))); err != nil {
			t.Fatalf("Put(%d): unexpected error: %v", i, err)
		}
	}

	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: unexpected error: %v", err)
	}
	if len(ids) != count {
		t.Errorf("GetAll: got %d IDs, want %d", len(ids), count)
	}
}

func TestPebbleIndex_LargeEntryCount_Scan(t *testing.T) {
	idx, _ := newPebbleIdx(t, "large", false)
	const count = 500

	for i := 0; i < count; i++ {
		fieldVal := []byte("f" + strconv.Itoa(i))
		if err := idx.Put(uint64(i), fieldVal); err != nil {
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

func TestPebbleIndex_Isolation_DifferentNames(t *testing.T) {
	db := openPebble(t)

	idx1, err := NewPebbleIndex(db, "users", false)
	if err != nil {
		t.Fatalf("NewPebbleIndex(users): %v", err)
	}
	idx2, err := NewPebbleIndex(db, "orders", false)
	if err != nil {
		t.Fatalf("NewPebbleIndex(orders): %v", err)
	}

	_ = idx1.Put(1, []byte("alice"))
	_ = idx2.Put(2, []byte("order-1"))

	ids1, _ := idx1.GetAll()
	ids2, _ := idx2.GetAll()

	if len(ids1) != 1 || ids1[0] != 1 {
		t.Errorf("users index: got %v, want [1]", ids1)
	}
	if len(ids2) != 1 || ids2[0] != 2 {
		t.Errorf("orders index: got %v, want [2]", ids2)
	}
}

func TestPebbleIndex_Isolation_ClearDoesNotAffectOther(t *testing.T) {
	db := openPebble(t)

	idx1, _ := NewPebbleIndex(db, "first", false)
	idx2, _ := NewPebbleIndex(db, "second", false)

	_ = idx1.Put(1, []byte("a"))
	_ = idx2.Put(2, []byte("b"))

	_ = idx1.Clear()

	ids1, _ := idx1.GetAll()
	ids2, _ := idx2.GetAll()

	if len(ids1) != 0 {
		t.Errorf("first index after clear: got %v, want empty", ids1)
	}
	if len(ids2) != 1 {
		t.Errorf("second index after first's clear: got %v, want [2]", ids2)
	}
}

// ---------------------------------------------------------------------------
// Interface compliance
// ---------------------------------------------------------------------------

func TestPebbleIndex_ImplementsIndex(t *testing.T) {
	var _ Index = (*PebbleIndex)(nil)
}

func TestPebbleIndex_ImplementsIndex_ViaConstructor(t *testing.T) {
	idx, _ := newPebbleIdx(t, "test", false)
	var _ Index = idx
}

// ---------------------------------------------------------------------------
// incrementBytes helper
// ---------------------------------------------------------------------------

func TestIncrementBytes_NormalByte(t *testing.T) {
	input := []byte{0x00, 0x01}
	got := incrementBytes(input)
	if len(got) != 2 || got[0] != 0x00 || got[1] != 0x02 {
		t.Errorf("incrementBytes: got %v, want [0x00, 0x02]", got)
	}
}

func TestIncrementBytes_AllFF(t *testing.T) {
	input := []byte{0xFF, 0xFF}
	got := incrementBytes(input)
	// When all bytes are 0xFF, a 0x00 byte is appended.
	if len(got) != 3 {
		t.Errorf("incrementBytes(all 0xFF): got len %d, want 3", len(got))
	}
}

func TestIncrementBytes_SingleByte(t *testing.T) {
	input := []byte{0x41}
	got := incrementBytes(input)
	if len(got) != 1 || got[0] != 0x42 {
		t.Errorf("incrementBytes: got %v, want [0x42]", got)
	}
}

func TestIncrementBytes_CarryOver(t *testing.T) {
	input := []byte{0x01, 0xFF}
	got := incrementBytes(input)
	// 0xFF increments carry to produce [0x02].
	if len(got) != 1 || got[0] != 0x02 {
		t.Errorf("incrementBytes(carry): got %v, want [0x02]", got)
	}
}
