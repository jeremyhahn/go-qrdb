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

package statemachine

import (
	"bytes"
	"testing"
)

// ---------------------------------------------------------------------------
// Backend contract tests: run the same suite against each implementation.
// ---------------------------------------------------------------------------

func TestStandardBackend_PutGetDelete(t *testing.T) {
	b := NewStandardBackend()
	defer func() { _ = b.Close() }()
	testBackendPutGetDelete(t, b)
}

func TestStandardBackend_GetNotFound(t *testing.T) {
	b := NewStandardBackend()
	defer func() { _ = b.Close() }()
	testBackendGetNotFound(t, b)
}

func TestStandardBackend_ListAndScan(t *testing.T) {
	b := NewStandardBackend()
	defer func() { _ = b.Close() }()
	testBackendListAndScan(t, b)
}

func TestStandardBackend_Snapshot(t *testing.T) {
	b := NewStandardBackend()
	defer func() { _ = b.Close() }()
	testBackendSnapshot(t, b, func() Backend { return NewStandardBackend() })
}

func TestStandardBackend_Hash(t *testing.T) {
	b := NewStandardBackend()
	defer func() { _ = b.Close() }()
	testBackendHash(t, b)
}

func TestStandardBackend_DeleteNonExistent(t *testing.T) {
	b := NewStandardBackend()
	defer func() { _ = b.Close() }()
	// Deleting a non-existent key should not error.
	if err := b.Delete([]byte("ghost")); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

func TestStandardBackend_ScanLimit(t *testing.T) {
	b := NewStandardBackend()
	defer func() { _ = b.Close() }()
	testBackendScanLimit(t, b)
}

func TestStandardBackend_ListEmpty(t *testing.T) {
	b := NewStandardBackend()
	defer func() { _ = b.Close() }()
	keys, err := b.List([]byte("prefix"))
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys, got %d", len(keys))
	}
}

func TestConcurrentBackend_PutGetDelete(t *testing.T) {
	b := NewConcurrentBackend()
	defer func() { _ = b.Close() }()
	testBackendPutGetDelete(t, b)
}

func TestConcurrentBackend_GetNotFound(t *testing.T) {
	b := NewConcurrentBackend()
	defer func() { _ = b.Close() }()
	testBackendGetNotFound(t, b)
}

func TestConcurrentBackend_ListAndScan(t *testing.T) {
	b := NewConcurrentBackend()
	defer func() { _ = b.Close() }()
	testBackendListAndScan(t, b)
}

func TestConcurrentBackend_Snapshot(t *testing.T) {
	b := NewConcurrentBackend()
	defer func() { _ = b.Close() }()
	testBackendSnapshot(t, b, func() Backend { return NewConcurrentBackend() })
}

func TestConcurrentBackend_Hash(t *testing.T) {
	b := NewConcurrentBackend()
	defer func() { _ = b.Close() }()
	testBackendHash(t, b)
}

func TestConcurrentBackend_DeleteNonExistent(t *testing.T) {
	b := NewConcurrentBackend()
	defer func() { _ = b.Close() }()
	if err := b.Delete([]byte("ghost")); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

func TestConcurrentBackend_ScanLimit(t *testing.T) {
	b := NewConcurrentBackend()
	defer func() { _ = b.Close() }()
	testBackendScanLimit(t, b)
}

func TestFastBackend_PutGetDelete(t *testing.T) {
	store := newMemStorage()
	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()
	testBackendPutGetDelete(t, b)
	b.wg.Wait()
}

func TestFastBackend_GetNotFound(t *testing.T) {
	store := newMemStorage()
	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()
	testBackendGetNotFound(t, b)
}

func TestFastBackend_StorageFallback(t *testing.T) {
	store := newMemStorage()
	// Put directly into storage (bypassing cache).
	_ = store.Set([]byte("disk-key"), []byte("disk-value"))

	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()

	val, err := b.Get([]byte("disk-key"))
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if string(val) != "disk-value" {
		t.Fatalf("expected 'disk-value', got %q", val)
	}
}

func TestFastBackend_Tombstone(t *testing.T) {
	store := newMemStorage()
	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()

	_ = b.Put([]byte("k"), []byte("v"))
	b.wg.Wait()

	_ = b.Delete([]byte("k"))
	b.wg.Wait()

	val, err := b.Get([]byte("k"))
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if val != nil {
		t.Fatalf("expected nil after delete, got %q", val)
	}
}

func TestFastBackend_Snapshot(t *testing.T) {
	store := newMemStorage()
	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()

	testBackendSnapshot(t, b, func() Backend {
		return NewFastBackend(2, 2, newMemStorage(), nil)
	})
	b.wg.Wait()
}

func TestFastBackend_Hash(t *testing.T) {
	store := newMemStorage()
	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()
	testBackendHash(t, b)
	b.wg.Wait()
}

func TestFastBackend_ListMergesStorageAndCache(t *testing.T) {
	store := newMemStorage()
	_ = store.Set([]byte("ns/a"), []byte("from-storage"))
	_ = store.Set([]byte("ns/b"), []byte("from-storage"))

	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()

	_ = b.Put([]byte("ns/c"), []byte("from-cache"))
	b.wg.Wait()

	keys, err := b.List([]byte("ns/"))
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(keys))
	}
}

func TestDiskBackend_PutGetDelete(t *testing.T) {
	store := newMemStorage()
	b := NewDiskBackend(store)
	defer func() { _ = b.Close() }()
	testBackendPutGetDelete(t, b)
}

func TestDiskBackend_GetNotFound(t *testing.T) {
	store := newMemStorage()
	b := NewDiskBackend(store)
	defer func() { _ = b.Close() }()
	testBackendGetNotFound(t, b)
}

func TestDiskBackend_ListAndScan(t *testing.T) {
	store := newMemStorage()
	b := NewDiskBackend(store)
	defer func() { _ = b.Close() }()
	testBackendListAndScan(t, b)
}

func TestDiskBackend_Snapshot(t *testing.T) {
	store := newMemStorage()
	b := NewDiskBackend(store)
	defer func() { _ = b.Close() }()
	testBackendSnapshot(t, b, func() Backend {
		return NewDiskBackend(newMemStorage())
	})
}

func TestDiskBackend_Hash(t *testing.T) {
	store := newMemStorage()
	b := NewDiskBackend(store)
	defer func() { _ = b.Close() }()
	testBackendHash(t, b)
}

func TestDiskBackend_DeleteNonExistent(t *testing.T) {
	store := newMemStorage()
	b := NewDiskBackend(store)
	defer func() { _ = b.Close() }()
	if err := b.Delete([]byte("ghost")); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

func TestDiskBackend_ScanLimit(t *testing.T) {
	store := newMemStorage()
	b := NewDiskBackend(store)
	defer func() { _ = b.Close() }()
	testBackendScanLimit(t, b)
}

// ---------------------------------------------------------------------------
// Shared backend test helpers
// ---------------------------------------------------------------------------

func testBackendPutGetDelete(t *testing.T, b Backend) {
	t.Helper()

	if err := b.Put([]byte("key1"), []byte("value1")); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	val, err := b.Get([]byte("key1"))
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(val) != "value1" {
		t.Fatalf("expected 'value1', got %q", val)
	}

	if err := b.Delete([]byte("key1")); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	val, err = b.Get([]byte("key1"))
	if err != nil {
		t.Fatalf("Get after delete failed: %v", err)
	}
	if val != nil {
		t.Fatalf("expected nil after delete, got %q", val)
	}
}

func testBackendGetNotFound(t *testing.T, b Backend) {
	t.Helper()

	val, err := b.Get([]byte("nonexistent"))
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if val != nil {
		t.Fatalf("expected nil for missing key, got %q", val)
	}
}

func testBackendListAndScan(t *testing.T, b Backend) {
	t.Helper()

	_ = b.Put([]byte("ns/a"), []byte("va"))
	_ = b.Put([]byte("ns/b"), []byte("vb"))
	_ = b.Put([]byte("other/x"), []byte("vx"))

	// List with prefix
	keys, err := b.List([]byte("ns/"))
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}

	// Scan with prefix
	m, err := b.Scan([]byte("ns/"), 0)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(m) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(m))
	}
	if string(m["ns/a"]) != "va" {
		t.Fatalf("expected 'va', got %q", m["ns/a"])
	}

	// List all
	allKeys, err := b.List(nil)
	if err != nil {
		t.Fatalf("List all failed: %v", err)
	}
	if len(allKeys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(allKeys))
	}
}

func testBackendSnapshot(t *testing.T, source Backend, newTarget func() Backend) {
	t.Helper()

	_ = source.Put([]byte("snap-a"), []byte("val-a"))
	_ = source.Put([]byte("snap-b"), []byte("val-b"))

	// If source is FastBackend, wait for async writes.
	if fb, ok := source.(*FastBackend); ok {
		fb.wg.Wait()
	}

	// Save snapshot.
	var buf bytes.Buffer
	stopper := make(chan struct{})
	if err := source.SaveSnapshot(&buf, stopper); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	// Recover into target.
	target := newTarget()
	defer func() { _ = target.Close() }()
	if err := target.RecoverSnapshot(&buf, stopper); err != nil {
		t.Fatalf("RecoverSnapshot failed: %v", err)
	}

	// If target is FastBackend, wait for async writes.
	if fb, ok := target.(*FastBackend); ok {
		fb.wg.Wait()
	}

	// Verify data.
	for _, key := range []string{"snap-a", "snap-b"} {
		val, err := target.Get([]byte(key))
		if err != nil {
			t.Fatalf("Get(%s) after recovery: %v", key, err)
		}
		if val == nil {
			t.Fatalf("expected non-nil value for %s after recovery", key)
		}
	}

	// Verify hashes match.
	sourceHash, _ := source.GetHash()
	targetHash, _ := target.GetHash()
	if sourceHash != targetHash {
		t.Fatalf("hash mismatch: source=%d target=%d", sourceHash, targetHash)
	}
}

func testBackendHash(t *testing.T, b Backend) {
	t.Helper()

	// Empty backend has a deterministic hash.
	h1, err := b.GetHash()
	if err != nil {
		t.Fatalf("GetHash failed: %v", err)
	}

	_ = b.Put([]byte("hk"), []byte("hv"))

	// If FastBackend, wait for async.
	if fb, ok := b.(*FastBackend); ok {
		fb.wg.Wait()
	}

	h2, err := b.GetHash()
	if err != nil {
		t.Fatalf("GetHash after put: %v", err)
	}

	if h1 == h2 {
		t.Fatal("hash should change after put")
	}
}

func testBackendScanLimit(t *testing.T, b Backend) {
	t.Helper()

	for i := 0; i < 10; i++ {
		key := []byte("limit/" + string(rune('a'+i)))
		_ = b.Put(key, []byte("v"))
	}

	m, err := b.Scan([]byte("limit/"), 3)
	if err != nil {
		t.Fatalf("Scan with limit failed: %v", err)
	}
	if len(m) > 3 {
		t.Fatalf("expected at most 3 entries, got %d", len(m))
	}
}
