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

//go:build codec_json && raft

package statemachine

import (
	"bytes"
	"testing"
)

// ---------------------------------------------------------------------------
// FastBackend.Scan (0% coverage)
// ---------------------------------------------------------------------------

func TestFastBackend_Scan_CacheOnly(t *testing.T) {
	store := newMemStorage()
	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()

	_ = b.Put([]byte("scan/a"), []byte("va"))
	_ = b.Put([]byte("scan/b"), []byte("vb"))
	_ = b.Put([]byte("other/x"), []byte("vx"))
	b.wg.Wait()

	m, err := b.Scan([]byte("scan/"), 0)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(m) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(m))
	}
	if string(m["scan/a"]) != "va" {
		t.Fatalf("expected 'va', got %q", m["scan/a"])
	}
	if string(m["scan/b"]) != "vb" {
		t.Fatalf("expected 'vb', got %q", m["scan/b"])
	}
}

func TestFastBackend_Scan_StorageFallback(t *testing.T) {
	store := newMemStorage()
	// Pre-populate storage directly (simulates data that was written before the cache).
	_ = store.Set([]byte("stored/c"), []byte("vc"))
	_ = store.Set([]byte("stored/d"), []byte("vd"))

	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()

	// Also add one key via cache.
	_ = b.Put([]byte("stored/e"), []byte("ve"))
	b.wg.Wait()

	m, err := b.Scan([]byte("stored/"), 0)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(m) != 3 {
		t.Fatalf("expected 3 entries (2 from storage + 1 from cache), got %d", len(m))
	}
}

func TestFastBackend_Scan_WithLimit(t *testing.T) {
	store := newMemStorage()
	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()

	for i := 0; i < 10; i++ {
		key := []byte("limited/" + string(rune('a'+i)))
		_ = b.Put(key, []byte("v"))
	}
	b.wg.Wait()

	m, err := b.Scan([]byte("limited/"), 4)
	if err != nil {
		t.Fatalf("Scan with limit failed: %v", err)
	}
	if len(m) > 4 {
		t.Fatalf("expected at most 4 entries, got %d", len(m))
	}
}

func TestFastBackend_Scan_TombstonedKeysExcluded(t *testing.T) {
	store := newMemStorage()
	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()

	_ = b.Put([]byte("ts/a"), []byte("va"))
	_ = b.Put([]byte("ts/b"), []byte("vb"))
	b.wg.Wait()

	// Delete ts/a via cache tombstone.
	_ = b.Delete([]byte("ts/a"))
	b.wg.Wait()

	m, err := b.Scan([]byte("ts/"), 0)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if _, ok := m["ts/a"]; ok {
		t.Fatal("expected tombstoned key ts/a to be excluded from scan")
	}
	if string(m["ts/b"]) != "vb" {
		t.Fatalf("expected 'vb', got %q", m["ts/b"])
	}
}

func TestFastBackend_Scan_EmptyPrefix(t *testing.T) {
	store := newMemStorage()
	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()

	_ = b.Put([]byte("a"), []byte("va"))
	_ = b.Put([]byte("b"), []byte("vb"))
	b.wg.Wait()

	// Empty prefix should return all keys.
	m, err := b.Scan([]byte(""), 0)
	if err != nil {
		t.Fatalf("Scan with empty prefix failed: %v", err)
	}
	if len(m) < 2 {
		t.Fatalf("expected at least 2 entries, got %d", len(m))
	}
}

// ---------------------------------------------------------------------------
// FastBackend.clearStorage is exercised indirectly via RecoverSnapshot.
// The following test ensures the recovery path (which calls clearStorage)
// is covered when the backend has pre-existing storage keys.
// ---------------------------------------------------------------------------

func TestFastBackend_RecoverSnapshot_ClearsStorage(t *testing.T) {
	// Source backend: write some data and snapshot it.
	srcStore := newMemStorage()
	src := NewFastBackend(1, 1, srcStore, nil)
	defer func() { _ = src.Close() }()

	_ = src.Put([]byte("clear/x"), []byte("vx"))
	_ = src.Put([]byte("clear/y"), []byte("vy"))
	src.wg.Wait()

	var buf bytes.Buffer
	stopper := make(chan struct{})
	if err := src.SaveSnapshot(&buf, stopper); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	// Target backend: pre-populate storage with a key that should be cleared.
	tgtStore := newMemStorage()
	_ = tgtStore.Set([]byte("stale/z"), []byte("stale"))
	tgt := NewFastBackend(2, 2, tgtStore, nil)
	defer func() { _ = tgt.Close() }()

	if err := tgt.RecoverSnapshot(&buf, stopper); err != nil {
		t.Fatalf("RecoverSnapshot failed: %v", err)
	}
	tgt.wg.Wait()

	// The stale key should have been removed by clearStorage.
	val, err := tgt.Get([]byte("stale/z"))
	if err != nil {
		t.Fatalf("Get stale key failed: %v", err)
	}
	if val != nil {
		t.Fatalf("expected stale key to be cleared, got %q", val)
	}

	// Recovered keys should be present.
	for _, key := range []string{"clear/x", "clear/y"} {
		v, err := tgt.Get([]byte(key))
		if err != nil {
			t.Fatalf("Get(%s) failed: %v", key, err)
		}
		if v == nil {
			t.Fatalf("expected key %s to be present after recovery", key)
		}
	}
}
