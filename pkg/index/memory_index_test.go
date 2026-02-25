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
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Name / IsUnique accessors
// ---------------------------------------------------------------------------

func TestMemoryIndex_Name_ReturnsConfiguredName(t *testing.T) {
	idx := NewMemoryIndex("users", false)
	if got := idx.Name(); got != "users" {
		t.Errorf("Name: got %q, want %q", got, "users")
	}
}

func TestMemoryIndex_Name_EmptyString(t *testing.T) {
	idx := NewMemoryIndex("", false)
	if got := idx.Name(); got != "" {
		t.Errorf("Name: got %q, want empty", got)
	}
}

func TestMemoryIndex_IsUnique_True(t *testing.T) {
	idx := NewMemoryIndex("email", true)
	if !idx.IsUnique() {
		t.Error("IsUnique: expected true for unique index")
	}
}

func TestMemoryIndex_IsUnique_False(t *testing.T) {
	idx := NewMemoryIndex("tags", false)
	if idx.IsUnique() {
		t.Error("IsUnique: expected false for non-unique index")
	}
}

// ---------------------------------------------------------------------------
// Put
// ---------------------------------------------------------------------------

func TestMemoryIndex_Put_Normal(t *testing.T) {
	idx := NewMemoryIndex("test", false)
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

func TestMemoryIndex_Put_MultipleEntitiesSameValue(t *testing.T) {
	idx := NewMemoryIndex("test", false)
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

func TestMemoryIndex_Put_DuplicateEntityIDSilentlyIgnored(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	if err := idx.Put(1, []byte("alice")); err != nil {
		t.Fatalf("Put(first): unexpected error: %v", err)
	}
	if err := idx.Put(1, []byte("alice")); err != nil {
		t.Fatalf("Put(duplicate): unexpected error: %v", err)
	}
	ids, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("Get: got %d entries, want 1 (duplicate should be ignored)", len(ids))
	}
}

func TestMemoryIndex_Put_UniqueConstraintViolation(t *testing.T) {
	idx := NewMemoryIndex("email", true)
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

func TestMemoryIndex_Put_UniqueSameEntityIDAllowed(t *testing.T) {
	idx := NewMemoryIndex("email", true)
	if err := idx.Put(1, []byte("alice@test.com")); err != nil {
		t.Fatalf("Put(first): unexpected error: %v", err)
	}
	if err := idx.Put(1, []byte("alice@test.com")); err != nil {
		t.Fatalf("Put(same entity): unexpected error: %v", err)
	}
}

func TestMemoryIndex_Put_NilFieldValue(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	err := idx.Put(1, nil)
	if err == nil {
		t.Fatal("Put(nil): expected NilFieldValueError, got nil")
	}
	var nfv *NilFieldValueError
	if !errors.As(err, &nfv) {
		t.Fatalf("Put(nil): expected NilFieldValueError, got %T: %v", err, err)
	}
}

func TestMemoryIndex_Put_AfterClose(t *testing.T) {
	idx := NewMemoryIndex("test", false)
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

func TestMemoryIndex_Get_ExistingValue(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	_ = idx.Put(42, []byte("alice"))
	ids, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if len(ids) != 1 || ids[0] != 42 {
		t.Errorf("Get: got %v, want [42]", ids)
	}
}

func TestMemoryIndex_Get_NonExistent(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	ids, err := idx.Get([]byte("nonexistent"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("Get: got %v, want empty slice", ids)
	}
}

func TestMemoryIndex_Get_ReturnsCopy(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	_ = idx.Put(1, []byte("alice"))
	ids, _ := idx.Get([]byte("alice"))
	ids[0] = 999
	ids2, _ := idx.Get([]byte("alice"))
	if ids2[0] != 1 {
		t.Errorf("Get: mutating returned slice should not affect index, got %v", ids2)
	}
}

func TestMemoryIndex_Get_NilFieldValue(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	_, err := idx.Get(nil)
	if err == nil {
		t.Fatal("Get(nil): expected NilFieldValueError, got nil")
	}
	var nfv *NilFieldValueError
	if !errors.As(err, &nfv) {
		t.Fatalf("Get(nil): expected NilFieldValueError, got %T: %v", err, err)
	}
}

func TestMemoryIndex_Get_AfterClose(t *testing.T) {
	idx := NewMemoryIndex("test", false)
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

func TestMemoryIndex_Delete_Existing(t *testing.T) {
	idx := NewMemoryIndex("test", false)
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

func TestMemoryIndex_Delete_NonExistent(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	err := idx.Delete(999, []byte("nonexistent"))
	if err != nil {
		t.Fatalf("Delete(nonexistent): unexpected error: %v", err)
	}
}

func TestMemoryIndex_Delete_LeavesOtherEntities(t *testing.T) {
	idx := NewMemoryIndex("test", false)
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

func TestMemoryIndex_Delete_RemovesEmptyKeyFromMap(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	_ = idx.Put(1, []byte("alice"))
	_ = idx.Delete(1, []byte("alice"))

	// After deleting the only entity, the key should be gone from the map.
	idx.mu.RLock()
	_, exists := idx.data["alice"]
	idx.mu.RUnlock()
	if exists {
		t.Error("Delete: expected key to be removed from internal map when list becomes empty")
	}
}

func TestMemoryIndex_Delete_NilFieldValue(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	err := idx.Delete(1, nil)
	if err == nil {
		t.Fatal("Delete(nil): expected NilFieldValueError, got nil")
	}
	var nfv *NilFieldValueError
	if !errors.As(err, &nfv) {
		t.Fatalf("Delete(nil): expected NilFieldValueError, got %T: %v", err, err)
	}
}

func TestMemoryIndex_Delete_AfterClose(t *testing.T) {
	idx := NewMemoryIndex("test", false)
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

func TestMemoryIndex_Scan_FullRange(t *testing.T) {
	idx := NewMemoryIndex("test", false)
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

func TestMemoryIndex_Scan_PartialRange(t *testing.T) {
	idx := NewMemoryIndex("test", false)
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

func TestMemoryIndex_Scan_ReturnsCopies(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	_ = idx.Put(1, []byte("key"))

	result, _ := idx.Scan([]byte("a"), []byte("z"))
	result["key"][0] = 999
	ids, _ := idx.Get([]byte("key"))
	if ids[0] != 1 {
		t.Error("Scan: mutating returned slice should not affect index")
	}
}

func TestMemoryIndex_Scan_NilStart(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	_, err := idx.Scan(nil, []byte("end"))
	if err == nil {
		t.Fatal("Scan(nil start): expected NilFieldValueError, got nil")
	}
	var nfv *NilFieldValueError
	if !errors.As(err, &nfv) {
		t.Fatalf("Scan(nil start): expected NilFieldValueError, got %T: %v", err, err)
	}
}

func TestMemoryIndex_Scan_NilEnd(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	_, err := idx.Scan([]byte("start"), nil)
	if err == nil {
		t.Fatal("Scan(nil end): expected NilFieldValueError, got nil")
	}
	var nfv *NilFieldValueError
	if !errors.As(err, &nfv) {
		t.Fatalf("Scan(nil end): expected NilFieldValueError, got %T: %v", err, err)
	}
}

func TestMemoryIndex_Scan_EmptyIndex(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	result, err := idx.Scan([]byte("a"), []byte("z"))
	if err != nil {
		t.Fatalf("Scan(empty): unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("Scan(empty): got %d entries, want 0", len(result))
	}
}

func TestMemoryIndex_Scan_AfterClose(t *testing.T) {
	idx := NewMemoryIndex("test", false)
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

func TestMemoryIndex_GetAll_MultipleValues(t *testing.T) {
	idx := NewMemoryIndex("test", false)
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

func TestMemoryIndex_GetAll_Deduplicated(t *testing.T) {
	idx := NewMemoryIndex("test", false)
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

func TestMemoryIndex_GetAll_EmptyIndex(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll(empty): unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("GetAll(empty): got %v, want empty", ids)
	}
}

func TestMemoryIndex_GetAll_AfterClose(t *testing.T) {
	idx := NewMemoryIndex("test", false)
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

func TestMemoryIndex_Clear_RemovesAll(t *testing.T) {
	idx := NewMemoryIndex("test", false)
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

func TestMemoryIndex_Clear_CanPutAfterClear(t *testing.T) {
	idx := NewMemoryIndex("test", false)
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

func TestMemoryIndex_Clear_AfterClose(t *testing.T) {
	idx := NewMemoryIndex("test", false)
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

func TestMemoryIndex_Close_SubsequentPutFails(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	if err := idx.Close(); err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}
	err := idx.Put(1, []byte("val"))
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Put(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestMemoryIndex_Close_SubsequentGetFails(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	_ = idx.Close()
	_, err := idx.Get([]byte("val"))
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Get(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestMemoryIndex_Close_SubsequentDeleteFails(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	_ = idx.Close()
	err := idx.Delete(1, []byte("val"))
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Delete(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestMemoryIndex_Close_SubsequentScanFails(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	_ = idx.Close()
	_, err := idx.Scan([]byte("a"), []byte("z"))
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Scan(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestMemoryIndex_Close_SubsequentGetAllFails(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	_ = idx.Close()
	_, err := idx.GetAll()
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("GetAll(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestMemoryIndex_Close_SubsequentClearFails(t *testing.T) {
	idx := NewMemoryIndex("test", false)
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

func TestMemoryIndex_ConcurrentPutGet(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	const goroutines = 50
	const opsPerGoroutine = 100

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

func TestMemoryIndex_ConcurrentPutDelete(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	const count = 200

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
// Interface compliance
// ---------------------------------------------------------------------------

func TestMemoryIndex_ImplementsIndex(t *testing.T) {
	var _ Index = (*MemoryIndex)(nil)
}

func TestMemoryIndex_ImplementsIndex_ViaNewMemoryIndex(t *testing.T) {
	var idx Index = NewMemoryIndex("test", false)
	if idx == nil {
		t.Fatal("NewMemoryIndex returned nil")
	}
}
