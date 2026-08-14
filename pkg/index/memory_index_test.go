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
	"bytes"
	"errors"
	"fmt"
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
	if err := idx.Put([]byte("entity-1"), []byte("alice")); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}
	keys, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if len(keys) != 1 || !bytes.Equal(keys[0], []byte("entity-1")) {
		t.Errorf("Get: got %v, want [entity-1]", keys)
	}
}

func TestMemoryIndex_Put_MultipleEntitiesSameValue(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	if err := idx.Put([]byte("entity-1"), []byte("shared")); err != nil {
		t.Fatalf("Put(1): unexpected error: %v", err)
	}
	if err := idx.Put([]byte("entity-2"), []byte("shared")); err != nil {
		t.Fatalf("Put(2): unexpected error: %v", err)
	}
	keys, err := idx.Get([]byte("shared"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("Get: got %d entities, want 2", len(keys))
	}
}

func TestMemoryIndex_Put_DuplicateEntityKeySilentlyIgnored(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	if err := idx.Put([]byte("entity-1"), []byte("alice")); err != nil {
		t.Fatalf("Put(first): unexpected error: %v", err)
	}
	if err := idx.Put([]byte("entity-1"), []byte("alice")); err != nil {
		t.Fatalf("Put(duplicate): unexpected error: %v", err)
	}
	keys, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("Get: got %d entries, want 1 (duplicate should be ignored)", len(keys))
	}
}

func TestMemoryIndex_Put_UniqueConstraintViolation(t *testing.T) {
	idx := NewMemoryIndex("email", true)
	if err := idx.Put([]byte("entity-1"), []byte("alice@test.com")); err != nil {
		t.Fatalf("Put(first): unexpected error: %v", err)
	}
	err := idx.Put([]byte("entity-2"), []byte("alice@test.com"))
	if err == nil {
		t.Fatal("Put(second): expected UniqueViolationError, got nil")
	}
	var uv *UniqueViolationError
	if !errors.As(err, &uv) {
		t.Fatalf("Put(second): expected UniqueViolationError, got %T: %v", err, err)
	}
	if !bytes.Equal(uv.ExistingKey, []byte("entity-1")) || !bytes.Equal(uv.NewKey, []byte("entity-2")) {
		t.Errorf("UniqueViolation: ExistingKey=%s NewKey=%s, want entity-1, entity-2", uv.ExistingKey, uv.NewKey)
	}
}

func TestMemoryIndex_Put_UniqueSameEntityKeyAllowed(t *testing.T) {
	idx := NewMemoryIndex("email", true)
	if err := idx.Put([]byte("entity-1"), []byte("alice@test.com")); err != nil {
		t.Fatalf("Put(first): unexpected error: %v", err)
	}
	if err := idx.Put([]byte("entity-1"), []byte("alice@test.com")); err != nil {
		t.Fatalf("Put(same entity): unexpected error: %v", err)
	}
}

func TestMemoryIndex_Put_NilFieldValue(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	err := idx.Put([]byte("entity-1"), nil)
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
	err := idx.Put([]byte("entity-1"), []byte("value"))
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
	_ = idx.Put([]byte("entity-42"), []byte("alice"))
	keys, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if len(keys) != 1 || !bytes.Equal(keys[0], []byte("entity-42")) {
		t.Errorf("Get: got %v, want [entity-42]", keys)
	}
}

func TestMemoryIndex_Get_NonExistent(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	keys, err := idx.Get([]byte("nonexistent"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("Get: got %v, want empty slice", keys)
	}
}

func TestMemoryIndex_Get_ReturnsCopy(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	_ = idx.Put([]byte("entity-1"), []byte("alice"))
	keys, _ := idx.Get([]byte("alice"))
	keys[0] = []byte("mutated")
	keys2, _ := idx.Get([]byte("alice"))
	if !bytes.Equal(keys2[0], []byte("entity-1")) {
		t.Errorf("Get: mutating returned slice should not affect index, got %v", keys2)
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
	_ = idx.Put([]byte("entity-1"), []byte("alice"))
	if err := idx.Delete([]byte("entity-1"), []byte("alice")); err != nil {
		t.Fatalf("Delete: unexpected error: %v", err)
	}
	keys, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get after delete: unexpected error: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("Get after delete: got %v, want empty", keys)
	}
}

func TestMemoryIndex_Delete_NonExistent(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	err := idx.Delete([]byte("entity-999"), []byte("nonexistent"))
	if err != nil {
		t.Fatalf("Delete(nonexistent): unexpected error: %v", err)
	}
}

func TestMemoryIndex_Delete_LeavesOtherEntities(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	_ = idx.Put([]byte("entity-1"), []byte("shared"))
	_ = idx.Put([]byte("entity-2"), []byte("shared"))
	if err := idx.Delete([]byte("entity-1"), []byte("shared")); err != nil {
		t.Fatalf("Delete: unexpected error: %v", err)
	}
	keys, _ := idx.Get([]byte("shared"))
	if len(keys) != 1 || !bytes.Equal(keys[0], []byte("entity-2")) {
		t.Errorf("Get after partial delete: got %v, want [entity-2]", keys)
	}
}

func TestMemoryIndex_Delete_RemovesEmptyKeyFromMap(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	_ = idx.Put([]byte("entity-1"), []byte("alice"))
	_ = idx.Delete([]byte("entity-1"), []byte("alice"))

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
	err := idx.Delete([]byte("entity-1"), nil)
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
	err := idx.Delete([]byte("entity-1"), []byte("value"))
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
	_ = idx.Put([]byte("entity-1"), []byte("a"))
	_ = idx.Put([]byte("entity-2"), []byte("b"))
	_ = idx.Put([]byte("entity-3"), []byte("c"))

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
	_ = idx.Put([]byte("entity-1"), []byte("apple"))
	_ = idx.Put([]byte("entity-2"), []byte("banana"))
	_ = idx.Put([]byte("entity-3"), []byte("cherry"))

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
	_ = idx.Put([]byte("entity-1"), []byte("key"))

	result, _ := idx.Scan([]byte("a"), []byte("z"))
	result["key"][0] = []byte("mutated")
	keys, _ := idx.Get([]byte("key"))
	if !bytes.Equal(keys[0], []byte("entity-1")) {
		t.Error("Scan: mutating returned slice should not affect index")
	}
}

func TestMemoryIndex_Scan_NilStart(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	_ = idx.Put([]byte("entity-1"), []byte("apple"))
	_ = idx.Put([]byte("entity-2"), []byte("cherry"))

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

func TestMemoryIndex_Scan_NilEnd(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	_ = idx.Put([]byte("entity-1"), []byte("apple"))
	_ = idx.Put([]byte("entity-2"), []byte("cherry"))

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

func TestMemoryIndex_Scan_NilStartAndEnd(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	_ = idx.Put([]byte("entity-1"), []byte("apple"))
	_ = idx.Put([]byte("entity-2"), []byte("cherry"))

	// Both bounds nil means the full index.
	result, err := idx.Scan(nil, nil)
	if err != nil {
		t.Fatalf("Scan(nil, nil): unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Scan(nil, nil): got %d entries, want 2", len(result))
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
	_ = idx.Put([]byte("entity-3"), []byte("c"))
	_ = idx.Put([]byte("entity-1"), []byte("a"))
	_ = idx.Put([]byte("entity-2"), []byte("b"))

	keys, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: unexpected error: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("GetAll: got %d keys, want 3", len(keys))
	}
	// Result should be sorted.
	for i := 1; i < len(keys); i++ {
		if bytes.Compare(keys[i], keys[i-1]) <= 0 {
			t.Errorf("GetAll: result not sorted at index %d: %v", i, keys)
		}
	}
}

func TestMemoryIndex_GetAll_Deduplicated(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	_ = idx.Put([]byte("entity-1"), []byte("a"))
	_ = idx.Put([]byte("entity-1"), []byte("b")) // Same entity key, different field value.

	keys, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: unexpected error: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("GetAll: got %d keys, want 1 (deduplicated)", len(keys))
	}
}

func TestMemoryIndex_GetAll_EmptyIndex(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	keys, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll(empty): unexpected error: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("GetAll(empty): got %v, want empty", keys)
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
	_ = idx.Put([]byte("entity-1"), []byte("a"))
	_ = idx.Put([]byte("entity-2"), []byte("b"))

	if err := idx.Clear(); err != nil {
		t.Fatalf("Clear: unexpected error: %v", err)
	}

	keys, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll after clear: unexpected error: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("GetAll after clear: got %v, want empty", keys)
	}
}

func TestMemoryIndex_Clear_CanPutAfterClear(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	_ = idx.Put([]byte("entity-1"), []byte("a"))
	_ = idx.Clear()

	if err := idx.Put([]byte("entity-2"), []byte("b")); err != nil {
		t.Fatalf("Put after clear: unexpected error: %v", err)
	}
	keys, _ := idx.Get([]byte("b"))
	if len(keys) != 1 || !bytes.Equal(keys[0], []byte("entity-2")) {
		t.Errorf("Get after clear+put: got %v, want [entity-2]", keys)
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
	err := idx.Put([]byte("entity-1"), []byte("val"))
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
	err := idx.Delete([]byte("entity-1"), []byte("val"))
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
		go func(base int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				entityKey := []byte(fmt.Sprintf("entity-%d", base+i))
				_ = idx.Put(entityKey, []byte("shared-value"))
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

	keys, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: unexpected error: %v", err)
	}
	expected := goroutines * opsPerGoroutine
	if len(keys) != expected {
		t.Errorf("GetAll: got %d keys, want %d", len(keys), expected)
	}
}

func TestMemoryIndex_ConcurrentPutDelete(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	const count = 200

	// Populate.
	for i := 0; i < count; i++ {
		entityKey := []byte(fmt.Sprintf("entity-%d", i))
		_ = idx.Put(entityKey, []byte("val"))
	}

	var wg sync.WaitGroup
	wg.Add(count)
	for i := 0; i < count; i++ {
		go func(id int) {
			defer wg.Done()
			entityKey := []byte(fmt.Sprintf("entity-%d", id))
			_ = idx.Delete(entityKey, []byte("val"))
		}(i)
	}
	wg.Wait()

	keys, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: unexpected error: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("GetAll after concurrent delete: got %d keys, want 0", len(keys))
	}
}

// ---------------------------------------------------------------------------
// Interface compliance
// ---------------------------------------------------------------------------

func TestMemoryIndex_ImplementsIndex(t *testing.T) {
	var _ Index = (*MemoryIndex)(nil)
}

func TestMemoryIndex_ImplementsIndex_ViaNewMemoryIndex(t *testing.T) {
	idx := NewMemoryIndex("test", false)
	var _ Index = idx // compile-time interface assertion
	_ = idx
}
