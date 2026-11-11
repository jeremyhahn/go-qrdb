// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package memory

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// openTestStorage creates a MemoryStorage for testing.
func openTestStorage(t *testing.T) *MemoryStorage {
	t.Helper()
	s := NewStorage()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// ---------------------------------------------------------------------------
// Get / Set / Delete
// ---------------------------------------------------------------------------

func TestGetSet(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("key1"), []byte("value1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := s.Get([]byte("key1"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "value1" {
		t.Fatalf("Get: got %q, want %q", got, "value1")
	}
}

func TestGetSet_MultipleKeys(t *testing.T) {
	s := openTestStorage(t)

	keys := []string{"alpha", "beta", "gamma"}
	for _, k := range keys {
		if err := s.Set([]byte(k), []byte("val-"+k)); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	for _, k := range keys {
		got, err := s.Get([]byte(k))
		if err != nil {
			t.Fatalf("Get %q: %v", k, err)
		}
		if string(got) != "val-"+k {
			t.Fatalf("Get %q: got %q, want %q", k, got, "val-"+k)
		}
	}
}

func TestGet_NotFound(t *testing.T) {
	s := openTestStorage(t)

	got, err := s.Get([]byte("missing"))
	if err != nil {
		t.Fatalf("Get missing: unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("Get missing: got %v, want nil", got)
	}
}

func TestGet_NotFoundAfterOtherSets(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("exists"), []byte("val")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := s.Get([]byte("missing"))
	if err != nil {
		t.Fatalf("Get missing: unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("Get missing: got %v, want nil", got)
	}
}

func TestGet_EmptyKey(t *testing.T) {
	s := openTestStorage(t)

	_, err := s.Get([]byte{})
	if !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Get empty key: got %v, want ErrEmptyKey", err)
	}
}

func TestGet_NilKey(t *testing.T) {
	s := openTestStorage(t)

	_, err := s.Get(nil)
	if !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Get nil key: got %v, want ErrEmptyKey", err)
	}
}

func TestSet_EmptyKey(t *testing.T) {
	s := openTestStorage(t)

	err := s.Set([]byte{}, []byte("value"))
	if !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Set empty key: got %v, want ErrEmptyKey", err)
	}
}

func TestSet_NilKey(t *testing.T) {
	s := openTestStorage(t)

	err := s.Set(nil, []byte("value"))
	if !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Set nil key: got %v, want ErrEmptyKey", err)
	}
}

func TestSet_Overwrite(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("key"), []byte("v1")); err != nil {
		t.Fatalf("Set v1: %v", err)
	}
	if err := s.Set([]byte("key"), []byte("v2")); err != nil {
		t.Fatalf("Set v2: %v", err)
	}

	got, err := s.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "v2" {
		t.Fatalf("Get: got %q, want %q", got, "v2")
	}
}

func TestSet_EmptyValue(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("key"), []byte{}); err != nil {
		t.Fatalf("Set empty value: %v", err)
	}

	got, err := s.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Get: got %v, want empty", got)
	}
}

func TestSet_NilValue(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("key"), nil); err != nil {
		t.Fatalf("Set nil value: %v", err)
	}

	got, err := s.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Get: got %v, want empty", got)
	}
}

func TestDelete(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("key1"), []byte("value1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Delete([]byte("key1")); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, err := s.Get([]byte("key1"))
	if err != nil {
		t.Fatalf("Get after Delete: %v", err)
	}
	if got != nil {
		t.Fatalf("Get after Delete: got %v, want nil", got)
	}
}

func TestDelete_NonExistent(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Delete([]byte("nonexistent")); err != nil {
		t.Fatalf("Delete nonexistent: unexpected error: %v", err)
	}
}

func TestDelete_EmptyKey(t *testing.T) {
	s := openTestStorage(t)

	err := s.Delete([]byte{})
	if !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Delete empty key: got %v, want ErrEmptyKey", err)
	}
}

func TestDelete_NilKey(t *testing.T) {
	s := openTestStorage(t)

	err := s.Delete(nil)
	if !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Delete nil key: got %v, want ErrEmptyKey", err)
	}
}

func TestGet_ReturnsCopy(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("key"), []byte("original")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got1, err := s.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get1: %v", err)
	}

	// Mutate the returned slice.
	got1[0] = 'X'

	got2, err := s.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get2: %v", err)
	}
	if string(got2) != "original" {
		t.Fatalf("data isolation: stored data was mutated, got %q", got2)
	}
}

func TestSet_DefensiveCopy(t *testing.T) {
	s := openTestStorage(t)

	key := []byte("key")
	value := []byte("original")
	if err := s.Set(key, value); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Mutate the original slices after Set.
	key[0] = 'X'
	value[0] = 'X'

	got, err := s.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("Set defensive copy: stored data was mutated, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Closed storage errors
// ---------------------------------------------------------------------------

func TestClosed_Get(t *testing.T) {
	s := openTestStorage(t)
	_ = s.Close()

	_, err := s.Get([]byte("key"))
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Get after close: got %T, want *storage.ClosedError", err)
	}
}

func TestClosed_Set(t *testing.T) {
	s := openTestStorage(t)
	_ = s.Close()

	err := s.Set([]byte("key"), []byte("val"))
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Set after close: got %T, want *storage.ClosedError", err)
	}
}

func TestClosed_Delete(t *testing.T) {
	s := openTestStorage(t)
	_ = s.Close()

	err := s.Delete([]byte("key"))
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Delete after close: got %T, want *storage.ClosedError", err)
	}
}

func TestClosed_Snapshot(t *testing.T) {
	s := openTestStorage(t)
	_ = s.Close()

	_, err := s.Snapshot()
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Snapshot after close: got %T, want *storage.ClosedError", err)
	}
}

func TestClose_Idempotent(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close 2: %v", err)
	}
}

// ---------------------------------------------------------------------------
// List tests
// ---------------------------------------------------------------------------

func TestMemoryStorage_List(t *testing.T) {
	s := openTestStorage(t)

	// Populate keys with different prefixes.
	for _, k := range []string{"app/config", "app/data", "app/log", "sys/info", "sys/version"} {
		if err := s.Set([]byte(k), []byte("val")); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	// List with prefix "app/" should return 3 keys.
	keys, err := s.List([]byte("app/"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("List app/: got %d keys, want 3", len(keys))
	}
	expected := []string{"app/config", "app/data", "app/log"}
	for i, k := range keys {
		if string(k) != expected[i] {
			t.Fatalf("List app/[%d]: got %q, want %q", i, k, expected[i])
		}
	}

	// List with prefix "sys/" should return 2 keys.
	keys, err = s.List([]byte("sys/"))
	if err != nil {
		t.Fatalf("List sys/: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("List sys/: got %d keys, want 2", len(keys))
	}

	// Empty prefix returns all keys.
	keys, err = s.List([]byte{})
	if err != nil {
		t.Fatalf("List empty prefix: %v", err)
	}
	if len(keys) != 5 {
		t.Fatalf("List empty prefix: got %d keys, want 5", len(keys))
	}

	// Nil prefix returns all keys.
	keys, err = s.List(nil)
	if err != nil {
		t.Fatalf("List nil prefix: %v", err)
	}
	if len(keys) != 5 {
		t.Fatalf("List nil prefix: got %d keys, want 5", len(keys))
	}

	// Prefix with no matches returns empty slice.
	keys, err = s.List([]byte("nonexistent/"))
	if err != nil {
		t.Fatalf("List nonexistent/: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("List nonexistent/: got %d keys, want 0", len(keys))
	}

	// Returned keys are defensive copies.
	keys, err = s.List([]byte("app/"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	keys[0][0] = 'X'
	keys2, err := s.List([]byte("app/"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if string(keys2[0]) != "app/config" {
		t.Fatalf("List defensive copy: stored key was mutated, got %q", keys2[0])
	}
}

func TestMemoryStorage_List_Closed(t *testing.T) {
	s := openTestStorage(t)
	_ = s.Close()

	_, err := s.List([]byte("prefix"))
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("List after close: got %T, want *storage.ClosedError", err)
	}
}

// ---------------------------------------------------------------------------
// Exists tests
// ---------------------------------------------------------------------------

func TestMemoryStorage_Exists(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("present"), []byte("value")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Existing key returns true.
	ok, err := s.Exists([]byte("present"))
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !ok {
		t.Fatal("Exists: got false, want true for existing key")
	}

	// Non-existent key returns false.
	ok, err = s.Exists([]byte("missing"))
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if ok {
		t.Fatal("Exists: got true, want false for non-existent key")
	}
}

func TestMemoryStorage_Exists_EmptyKey(t *testing.T) {
	s := openTestStorage(t)

	_, err := s.Exists([]byte{})
	if err == nil {
		t.Fatal("expected error for empty key")
	}
	if err != storage.ErrEmptyKey {
		t.Fatalf("Exists empty key: got %v, want %v", err, storage.ErrEmptyKey)
	}
}

func TestMemoryStorage_Exists_Closed(t *testing.T) {
	s := openTestStorage(t)
	_ = s.Close()

	_, err := s.Exists([]byte("key"))
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Exists after close: got %T, want *storage.ClosedError", err)
	}
}

// ---------------------------------------------------------------------------
// Batch tests
// ---------------------------------------------------------------------------

func TestBatch_SetCommit(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	if err := b.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("Batch.Set: %v", err)
	}
	if err := b.Set([]byte("k2"), []byte("v2")); err != nil {
		t.Fatalf("Batch.Set: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Batch.Commit: %v", err)
	}

	got, err := s.Get([]byte("k1"))
	if err != nil {
		t.Fatalf("Get k1: %v", err)
	}
	if string(got) != "v1" {
		t.Fatalf("Get k1: got %q, want %q", got, "v1")
	}

	got, err = s.Get([]byte("k2"))
	if err != nil {
		t.Fatalf("Get k2: %v", err)
	}
	if string(got) != "v2" {
		t.Fatalf("Get k2: got %q, want %q", got, "v2")
	}
}

func TestBatch_DeleteCommit(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	b := s.Batch()
	if err := b.Delete([]byte("k1")); err != nil {
		t.Fatalf("Batch.Delete: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Batch.Commit: %v", err)
	}

	got, err := s.Get([]byte("k1"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("Get after batch delete: got %v, want nil", got)
	}
}

func TestBatch_Rollback(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	if err := b.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("Batch.Set: %v", err)
	}
	if err := b.Rollback(); err != nil {
		t.Fatalf("Batch.Rollback: %v", err)
	}

	got, err := s.Get([]byte("k1"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("Get after rollback: got %v, want nil", got)
	}
}

func TestBatch_Close_ImplicitRollback(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	if err := b.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("Batch.Set: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Batch.Close: %v", err)
	}

	got, err := s.Get([]byte("k1"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("Get after close (implicit rollback): got %v, want nil", got)
	}
}

func TestBatch_Close_Idempotent(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	if err := b.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close 2: %v", err)
	}
}

func TestBatch_SetAfterCommit(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	err := b.Set([]byte("k1"), []byte("v1"))
	if err == nil {
		t.Fatal("expected error for Set after Commit")
	}
	var be *storage.BatchError
	if !errors.As(err, &be) {
		t.Fatalf("expected *storage.BatchError, got %T", err)
	}
}

func TestBatch_CommitAfterCommit(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit 1: %v", err)
	}

	err := b.Commit()
	if err == nil {
		t.Fatal("expected error for double Commit")
	}
	var be *storage.BatchError
	if !errors.As(err, &be) {
		t.Fatalf("expected *storage.BatchError, got %T", err)
	}
}

func TestBatch_RollbackAfterCommit(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	err := b.Rollback()
	if err == nil {
		t.Fatal("expected error for Rollback after Commit")
	}
	var be *storage.BatchError
	if !errors.As(err, &be) {
		t.Fatalf("expected *storage.BatchError, got %T", err)
	}
}

func TestBatch_DeleteAfterRollback(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	if err := b.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	err := b.Delete([]byte("k1"))
	if err == nil {
		t.Fatal("expected error for Delete after Rollback")
	}
	var be *storage.BatchError
	if !errors.As(err, &be) {
		t.Fatalf("expected *storage.BatchError, got %T", err)
	}
}

func TestBatch_Set_EmptyKey(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	defer func() { _ = b.Close() }()

	err := b.Set([]byte{}, []byte("val"))
	if err == nil {
		t.Fatal("expected error for empty key")
	}
	var be *storage.BatchError
	if !errors.As(err, &be) {
		t.Fatalf("expected *storage.BatchError, got %T", err)
	}
}

func TestBatch_Delete_EmptyKey(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	defer func() { _ = b.Close() }()

	err := b.Delete([]byte{})
	if err == nil {
		t.Fatal("expected error for empty key")
	}
	var be *storage.BatchError
	if !errors.As(err, &be) {
		t.Fatalf("expected *storage.BatchError, got %T", err)
	}
}

func TestBatch_Atomicity(t *testing.T) {
	s := openTestStorage(t)

	// Pre-populate.
	if err := s.Set([]byte("existing"), []byte("old")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	b := s.Batch()
	if err := b.Set([]byte("new1"), []byte("val1")); err != nil {
		t.Fatalf("Batch.Set: %v", err)
	}
	if err := b.Set([]byte("new2"), []byte("val2")); err != nil {
		t.Fatalf("Batch.Set: %v", err)
	}
	if err := b.Delete([]byte("existing")); err != nil {
		t.Fatalf("Batch.Delete: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Batch.Commit: %v", err)
	}

	// Verify all operations applied.
	if got, _ := s.Get([]byte("new1")); string(got) != "val1" {
		t.Fatalf("new1: got %q, want %q", got, "val1")
	}
	if got, _ := s.Get([]byte("new2")); string(got) != "val2" {
		t.Fatalf("new2: got %q, want %q", got, "val2")
	}
	if got, _ := s.Get([]byte("existing")); got != nil {
		t.Fatalf("existing: got %v, want nil", got)
	}
}

func TestBatch_SetAndDeleteSameKey(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	if err := b.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("Batch.Set: %v", err)
	}
	if err := b.Delete([]byte("k1")); err != nil {
		t.Fatalf("Batch.Delete: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Batch.Commit: %v", err)
	}

	// The delete should win since it comes after the set.
	got, err := s.Get([]byte("k1"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("Get after set+delete in batch: got %v, want nil", got)
	}
}

func TestBatch_DefensiveCopy(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	key := []byte("key")
	value := []byte("value")
	if err := b.Set(key, value); err != nil {
		t.Fatalf("Batch.Set: %v", err)
	}

	// Mutate the original slices.
	key[0] = 'X'
	value[0] = 'X'

	if err := b.Commit(); err != nil {
		t.Fatalf("Batch.Commit: %v", err)
	}

	got, err := s.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "value" {
		t.Fatalf("Batch defensive copy: stored data was mutated, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Iterator tests
// ---------------------------------------------------------------------------

func TestIterator_ForwardScan(t *testing.T) {
	s := openTestStorage(t)

	keys := []string{"a", "b", "c", "d", "e"}
	for _, k := range keys {
		if err := s.Set([]byte(k), []byte("val-"+k)); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		collected = append(collected, string(iter.Key()))
	}
	if err := iter.Error(); err != nil {
		t.Fatalf("Iterator error: %v", err)
	}
	if len(collected) != len(keys) {
		t.Fatalf("forward scan: got %d keys, want %d", len(collected), len(keys))
	}
	for i, k := range keys {
		if collected[i] != k {
			t.Fatalf("forward scan[%d]: got %q, want %q", i, collected[i], k)
		}
	}
}

func TestIterator_ReverseScan(t *testing.T) {
	s := openTestStorage(t)

	keys := []string{"a", "b", "c"}
	for _, k := range keys {
		if err := s.Set([]byte(k), []byte("val")); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToLast(); ok; ok = iter.Prev() {
		collected = append(collected, string(iter.Key()))
	}
	if err := iter.Error(); err != nil {
		t.Fatalf("Iterator error: %v", err)
	}
	if len(collected) != 3 {
		t.Fatalf("reverse scan: got %d keys, want 3", len(collected))
	}
	if collected[0] != "c" || collected[1] != "b" || collected[2] != "a" {
		t.Fatalf("reverse scan: got %v, want [c b a]", collected)
	}
}

func TestIterator_Seek(t *testing.T) {
	s := openTestStorage(t)

	for _, k := range []string{"a", "c", "e"} {
		if err := s.Set([]byte(k), []byte("val")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	// Seek to "b" should land on "c" (first key >= "b").
	if !iter.Seek([]byte("b")) {
		t.Fatal("Seek to b: expected valid position")
	}
	if string(iter.Key()) != "c" {
		t.Fatalf("Seek to b: got %q, want %q", iter.Key(), "c")
	}
}

func TestIterator_SeekExact(t *testing.T) {
	s := openTestStorage(t)

	for _, k := range []string{"a", "b", "c"} {
		if err := s.Set([]byte(k), []byte("val-"+k)); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	if !iter.Seek([]byte("b")) {
		t.Fatal("Seek to b: expected valid position")
	}
	if string(iter.Key()) != "b" {
		t.Fatalf("Seek exact: got %q, want %q", iter.Key(), "b")
	}
	if string(iter.Value()) != "val-b" {
		t.Fatalf("Seek exact value: got %q, want %q", iter.Value(), "val-b")
	}
}

func TestIterator_SeekPastEnd(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("a"), []byte("val")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	if iter.Seek([]byte("z")) {
		t.Fatal("Seek past end: expected false")
	}
}

func TestIterator_Bounds(t *testing.T) {
	s := openTestStorage(t)

	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if err := s.Set([]byte(k), []byte("val")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	iter := s.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte("b"),
		UpperBound: []byte("d"),
	})
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		collected = append(collected, string(iter.Key()))
	}
	// "b" and "c" (UpperBound "d" is exclusive).
	if len(collected) != 2 {
		t.Fatalf("bounded scan: got %v, want [b c]", collected)
	}
	if collected[0] != "b" || collected[1] != "c" {
		t.Fatalf("bounded scan: got %v, want [b c]", collected)
	}
}

func TestIterator_BoundsReverse(t *testing.T) {
	s := openTestStorage(t)

	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if err := s.Set([]byte(k), []byte("val")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	iter := s.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte("b"),
		UpperBound: []byte("d"),
	})
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToLast(); ok; ok = iter.Prev() {
		collected = append(collected, string(iter.Key()))
	}
	// "c" and "b" in reverse.
	if len(collected) != 2 {
		t.Fatalf("bounded reverse scan: got %v, want [c b]", collected)
	}
	if collected[0] != "c" || collected[1] != "b" {
		t.Fatalf("bounded reverse scan: got %v, want [c b]", collected)
	}
}

func TestIterator_LowerBoundOnly(t *testing.T) {
	s := openTestStorage(t)

	for _, k := range []string{"a", "b", "c", "d"} {
		if err := s.Set([]byte(k), []byte("val")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	iter := s.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte("c"),
	})
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		collected = append(collected, string(iter.Key()))
	}
	if len(collected) != 2 || collected[0] != "c" || collected[1] != "d" {
		t.Fatalf("lower bound scan: got %v, want [c d]", collected)
	}
}

func TestIterator_UpperBoundOnly(t *testing.T) {
	s := openTestStorage(t)

	for _, k := range []string{"a", "b", "c", "d"} {
		if err := s.Set([]byte(k), []byte("val")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	iter := s.NewIterator(&storage.IteratorOptions{
		UpperBound: []byte("c"),
	})
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		collected = append(collected, string(iter.Key()))
	}
	if len(collected) != 2 || collected[0] != "a" || collected[1] != "b" {
		t.Fatalf("upper bound scan: got %v, want [a b]", collected)
	}
}

func TestIterator_Empty(t *testing.T) {
	s := openTestStorage(t)

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	if iter.SeekToFirst() {
		t.Fatal("SeekToFirst on empty: expected false")
	}
	if iter.SeekToLast() {
		t.Fatal("SeekToLast on empty: expected false")
	}
	if iter.Valid() {
		t.Fatal("Valid on empty: expected false")
	}
	if iter.Key() != nil {
		t.Fatal("Key on empty: expected nil")
	}
	if iter.Value() != nil {
		t.Fatal("Value on empty: expected nil")
	}
}

func TestIterator_NilOpts(t *testing.T) {
	s := openTestStorage(t)
	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	if !iter.SeekToFirst() {
		t.Fatal("SeekToFirst: expected true")
	}
	if string(iter.Key()) != "k" {
		t.Fatalf("Key: got %q, want %q", iter.Key(), "k")
	}
}

func TestIterator_Value(t *testing.T) {
	s := openTestStorage(t)
	if err := s.Set([]byte("k"), []byte("myvalue")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	if !iter.SeekToFirst() {
		t.Fatal("SeekToFirst: expected true")
	}
	if string(iter.Value()) != "myvalue" {
		t.Fatalf("Value: got %q, want %q", iter.Value(), "myvalue")
	}
}

func TestIterator_OnlyKeysMultiple(t *testing.T) {
	s := openTestStorage(t)
	for _, k := range []string{"a", "b", "c"} {
		if err := s.Set([]byte(k), []byte("val-"+k)); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	iter := s.NewIterator(&storage.IteratorOptions{OnlyKeys: true})
	defer func() { _ = iter.Close() }()

	var keys []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		keys = append(keys, string(iter.Key()))
		if iter.Value() != nil {
			t.Fatalf("Value with OnlyKeys: got %v, want nil", iter.Value())
		}
	}
	if len(keys) != 3 {
		t.Fatalf("OnlyKeys scan: got %d keys, want 3", len(keys))
	}
}

func TestIterator_KeyValueCopy(t *testing.T) {
	s := openTestStorage(t)
	if err := s.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Set([]byte("k2"), []byte("v2")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	iter.SeekToFirst()
	key1 := iter.Key()
	val1 := iter.Value()

	// Move to next entry.
	iter.Next()

	// key1 and val1 should still hold the original values.
	if string(key1) != "k1" {
		t.Fatalf("key copy: got %q, want %q", key1, "k1")
	}
	if string(val1) != "v1" {
		t.Fatalf("value copy: got %q, want %q", val1, "v1")
	}
}

func TestIterator_NextPrevNavigation(t *testing.T) {
	s := openTestStorage(t)

	for _, k := range []string{"a", "b", "c"} {
		if err := s.Set([]byte(k), []byte("val")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	// Navigate: First -> Next -> Prev should land back on first item.
	if !iter.SeekToFirst() {
		t.Fatal("SeekToFirst: expected true")
	}
	if string(iter.Key()) != "a" {
		t.Fatalf("First: got %q, want %q", iter.Key(), "a")
	}
	if !iter.Next() {
		t.Fatal("Next: expected true")
	}
	if string(iter.Key()) != "b" {
		t.Fatalf("Next: got %q, want %q", iter.Key(), "b")
	}
	if !iter.Prev() {
		t.Fatal("Prev: expected true")
	}
	if string(iter.Key()) != "a" {
		t.Fatalf("Prev: got %q, want %q", iter.Key(), "a")
	}
}

func TestIterator_SeekWithBounds(t *testing.T) {
	s := openTestStorage(t)

	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if err := s.Set([]byte(k), []byte("val")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	iter := s.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte("b"),
		UpperBound: []byte("d"),
	})
	defer func() { _ = iter.Close() }()

	// Seek to "a" should land on "b" (respecting lower bound).
	if !iter.Seek([]byte("a")) {
		t.Fatal("Seek to a with LowerBound b: expected valid position")
	}
	if string(iter.Key()) != "b" {
		t.Fatalf("Seek to a with LowerBound b: got %q, want %q", iter.Key(), "b")
	}

	// Seek to "d" should fail (UpperBound is exclusive).
	if iter.Seek([]byte("d")) {
		t.Fatal("Seek to d with UpperBound d: expected false")
	}
}

func TestIterator_Close_Idempotent(t *testing.T) {
	s := openTestStorage(t)

	iter := s.NewIterator(nil)
	if err := iter.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	if err := iter.Close(); err != nil {
		t.Fatalf("Close 2: %v", err)
	}
}

func TestIterator_OperationsAfterClose(t *testing.T) {
	s := openTestStorage(t)
	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := s.NewIterator(nil)
	_ = iter.Close()

	if iter.SeekToFirst() {
		t.Fatal("SeekToFirst after Close: expected false")
	}
	if iter.SeekToLast() {
		t.Fatal("SeekToLast after Close: expected false")
	}
	if iter.Seek([]byte("k")) {
		t.Fatal("Seek after Close: expected false")
	}
	if iter.Next() {
		t.Fatal("Next after Close: expected false")
	}
	if iter.Prev() {
		t.Fatal("Prev after Close: expected false")
	}
	if iter.Valid() {
		t.Fatal("Valid after Close: expected false")
	}
	if iter.Key() != nil {
		t.Fatal("Key after Close: expected nil")
	}
	if iter.Value() != nil {
		t.Fatal("Value after Close: expected nil")
	}
}

func TestIterator_Error_NoError(t *testing.T) {
	s := openTestStorage(t)

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	if err := iter.Error(); err != nil {
		t.Fatalf("Error on fresh iterator: %v", err)
	}
}

func TestIterator_Isolation(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	// Write after iterator creation.
	if err := s.Set([]byte("b"), []byte("2")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	var collected []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		collected = append(collected, string(iter.Key()))
	}

	// Iterator should only see "a" (snapshot at creation time).
	if len(collected) != 1 || collected[0] != "a" {
		t.Fatalf("iterator isolation: got %v, want [a]", collected)
	}
}

// ---------------------------------------------------------------------------
// Snapshot tests
// ---------------------------------------------------------------------------

func TestSnapshot_Isolation(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("key"), []byte("before")); err != nil {
		t.Fatalf("Set before: %v", err)
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	// Write after snapshot.
	if err := s.Set([]byte("key"), []byte("after")); err != nil {
		t.Fatalf("Set after: %v", err)
	}

	// Snapshot sees old value.
	got, err := snap.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Snapshot.Get: %v", err)
	}
	if string(got) != "before" {
		t.Fatalf("Snapshot.Get: got %q, want %q", got, "before")
	}

	// Live storage sees new value.
	got, err = s.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Storage.Get: %v", err)
	}
	if string(got) != "after" {
		t.Fatalf("Storage.Get: got %q, want %q", got, "after")
	}
}

func TestSnapshot_IsolationDelete(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("key"), []byte("value")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	// Delete after snapshot.
	if err := s.Delete([]byte("key")); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Snapshot still sees the value.
	got, err := snap.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Snapshot.Get: %v", err)
	}
	if string(got) != "value" {
		t.Fatalf("Snapshot.Get: got %q, want %q", got, "value")
	}

	// Live storage reports nil.
	got, err = s.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Storage.Get: %v", err)
	}
	if got != nil {
		t.Fatalf("Storage.Get: got %v, want nil", got)
	}
}

func TestSnapshot_NotFound(t *testing.T) {
	s := openTestStorage(t)

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	got, err := snap.Get([]byte("missing"))
	if err != nil {
		t.Fatalf("Snapshot.Get missing: unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("Snapshot.Get missing: got %v, want nil", got)
	}
}

func TestSnapshot_EmptyKey(t *testing.T) {
	s := openTestStorage(t)

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	_, err = snap.Get([]byte{})
	if err == nil {
		t.Fatal("expected error for empty key")
	}
	var se *storage.SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *storage.SnapshotError, got %T", err)
	}
}

func TestSnapshot_Iterator(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Set([]byte("b"), []byte("2")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	// Add more data after snapshot.
	if err := s.Set([]byte("c"), []byte("3")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := snap.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		collected = append(collected, string(iter.Key()))
	}

	// Snapshot should only see "a" and "b".
	if len(collected) != 2 {
		t.Fatalf("snapshot iterator: got %v, want [a b]", collected)
	}
	if collected[0] != "a" || collected[1] != "b" {
		t.Fatalf("snapshot iterator: got %v, want [a b]", collected)
	}
}

func TestSnapshot_IteratorWithBounds(t *testing.T) {
	s := openTestStorage(t)

	for _, k := range []string{"a", "b", "c", "d"} {
		if err := s.Set([]byte(k), []byte("v")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	iter := snap.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte("b"),
		UpperBound: []byte("d"),
	})
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		collected = append(collected, string(iter.Key()))
	}
	if len(collected) != 2 || collected[0] != "b" || collected[1] != "c" {
		t.Fatalf("bounded snapshot iterator: got %v, want [b c]", collected)
	}
}

func TestSnapshot_Close_Idempotent(t *testing.T) {
	s := openTestStorage(t)

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if err := snap.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	if err := snap.Close(); err != nil {
		t.Fatalf("Close 2: %v", err)
	}
}

func TestSnapshot_GetAfterClose(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	_ = snap.Close()

	_, err = snap.Get([]byte("k"))
	if err == nil {
		t.Fatal("expected error for Get after Close")
	}
	var se *storage.SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *storage.SnapshotError, got %T", err)
	}
}

func TestSnapshot_IteratorAfterClose(t *testing.T) {
	s := openTestStorage(t)

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	_ = snap.Close()

	iter := snap.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	// Iterator created on closed snapshot should not yield results.
	if iter.SeekToFirst() {
		t.Fatal("expected false on closed snapshot iterator")
	}
}

func TestSnapshot_GetReturnsCopy(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("key"), []byte("original")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	got1, err := snap.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got1[0] = 'X'

	got2, err := snap.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get2: %v", err)
	}
	if string(got2) != "original" {
		t.Fatalf("snapshot data isolation: got %q, want %q", got2, "original")
	}
}

// ---------------------------------------------------------------------------
// Large value round-trip
// ---------------------------------------------------------------------------

func TestLargeValue(t *testing.T) {
	s := openTestStorage(t)

	bigVal := bytes.Repeat([]byte("x"), 1024*1024) // 1 MB
	if err := s.Set([]byte("big"), bigVal); err != nil {
		t.Fatalf("Set big: %v", err)
	}

	got, err := s.Get([]byte("big"))
	if err != nil {
		t.Fatalf("Get big: %v", err)
	}
	if !bytes.Equal(got, bigVal) {
		t.Fatalf("Get big: length %d, want %d", len(got), len(bigVal))
	}
}

func TestLargeValue_ViaIterator(t *testing.T) {
	s := openTestStorage(t)

	bigVal := bytes.Repeat([]byte("y"), 512*1024) // 512 KB
	if err := s.Set([]byte("big"), bigVal); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	if !iter.SeekToFirst() {
		t.Fatal("SeekToFirst: expected true")
	}
	if !bytes.Equal(iter.Value(), bigVal) {
		t.Fatalf("Iterator value: length %d, want %d", len(iter.Value()), len(bigVal))
	}
}

// ---------------------------------------------------------------------------
// Concurrent access
// ---------------------------------------------------------------------------

func TestConcurrent_ReadsAndWrites(t *testing.T) {
	s := openTestStorage(t)

	const numGoroutines = 50
	const numOps = 100

	var wg sync.WaitGroup

	// Concurrent writers.
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := []byte{byte(id)}
			for j := 0; j < numOps; j++ {
				val := []byte{byte(j)}
				if err := s.Set(key, val); err != nil {
					t.Errorf("writer %d Set: %v", id, err)
					return
				}
			}
		}(i)
	}

	// Concurrent readers.
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := []byte{byte(id)}
			for j := 0; j < numOps; j++ {
				_, err := s.Get(key)
				if err != nil {
					t.Errorf("reader %d Get: %v", id, err)
					return
				}
			}
		}(i)
	}

	wg.Wait()
}

func TestConcurrent_SnapshotsAndWrites(t *testing.T) {
	s := openTestStorage(t)

	const numGoroutines = 20
	const numOps = 50

	var wg sync.WaitGroup

	// Concurrent writers.
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := []byte{byte(id)}
			for j := 0; j < numOps; j++ {
				if err := s.Set(key, []byte{byte(j)}); err != nil {
					t.Errorf("writer %d: %v", id, err)
					return
				}
			}
		}(i)
	}

	// Concurrent snapshot takers.
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numOps; j++ {
				snap, err := s.Snapshot()
				if err != nil {
					t.Errorf("Snapshot: %v", err)
					return
				}
				_ = snap.Close()
			}
		}()
	}

	wg.Wait()
}

func TestConcurrent_BatchCommits(t *testing.T) {
	s := openTestStorage(t)

	const numGoroutines = 20

	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			b := s.Batch()
			key := []byte{byte(id)}
			if err := b.Set(key, []byte{byte(id)}); err != nil {
				t.Errorf("Batch.Set %d: %v", id, err)
				return
			}
			if err := b.Commit(); err != nil {
				t.Errorf("Batch.Commit %d: %v", id, err)
				return
			}
		}(i)
	}

	wg.Wait()

	// Verify all keys are present.
	for i := 0; i < numGoroutines; i++ {
		got, err := s.Get([]byte{byte(i)})
		if err != nil {
			t.Fatalf("Get %d: %v", i, err)
		}
		if len(got) != 1 || got[0] != byte(i) {
			t.Fatalf("Get %d: got %v, want [%d]", i, got, i)
		}
	}
}

// ---------------------------------------------------------------------------
// Compile-time interface check
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// NewStorage constructor
// ---------------------------------------------------------------------------

func TestIterator_SeekToFirst_LowerBoundExceedsUpperBound(t *testing.T) {
	s := openTestStorage(t)

	// Only key "a" exists, but bounds [d, e) exclude it.
	if err := s.Set([]byte("a"), []byte("val")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := s.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte("d"),
		UpperBound: []byte("e"),
	})
	defer func() { _ = iter.Close() }()

	// LowerBound seek finds nothing in [d, e).
	if iter.SeekToFirst() {
		t.Fatal("SeekToFirst: expected false when no keys in bounds")
	}
}

func TestIterator_SeekToFirst_LowerBoundHitsUpperBound(t *testing.T) {
	s := openTestStorage(t)

	// Keys: a, d, e. Bounds [d, d) is an empty range.
	for _, k := range []string{"a", "d", "e"} {
		if err := s.Set([]byte(k), []byte("val")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	iter := s.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte("d"),
		UpperBound: []byte("d"),
	})
	defer func() { _ = iter.Close() }()

	// Seek to "d" succeeds but "d" == UpperBound (exclusive), so invalid.
	if iter.SeekToFirst() {
		t.Fatal("SeekToFirst: expected false when LowerBound == UpperBound")
	}
}

func TestIterator_SeekToLast_ResultBelowLowerBound(t *testing.T) {
	s := openTestStorage(t)

	// Key "a" is below LowerBound "c". UpperBound "z" seek goes past end,
	// so Last() returns "a", but "a" < "c" (LowerBound), so invalid.
	if err := s.Set([]byte("a"), []byte("val")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := s.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte("c"),
		UpperBound: []byte("z"),
	})
	defer func() { _ = iter.Close() }()

	if iter.SeekToLast() {
		t.Fatal("SeekToLast: expected false when result is below LowerBound")
	}
}

func TestNewStorage_Separate(t *testing.T) {
	s1 := NewStorage()
	s2 := NewStorage()
	defer func() { _ = s1.Close() }()
	defer func() { _ = s2.Close() }()

	if err := s1.Set([]byte("key"), []byte("v1")); err != nil {
		t.Fatalf("s1.Set: %v", err)
	}

	// s2 should not see s1's data.
	got, err := s2.Get([]byte("key"))
	if err != nil {
		t.Fatalf("s2.Get: %v", err)
	}
	if got != nil {
		t.Fatalf("s2 should be independent from s1, got %v", got)
	}
}
