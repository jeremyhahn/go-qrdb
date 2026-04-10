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

package namespace

import (
	"bytes"
	"errors"
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

func TestNamespaceEngine_SetGet(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("tenant1"))

	if err := ns.Set([]byte("key1"), []byte("value1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := ns.Get([]byte("key1"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "value1" {
		t.Fatalf("Get: got %q, want %q", got, "value1")
	}

	// Verify key is stored with prefix in inner storage.
	innerVal, err := inner.Get([]byte("tenant1/key1"))
	if err != nil {
		t.Fatalf("inner.Get: %v", err)
	}
	if string(innerVal) != "value1" {
		t.Fatalf("inner.Get: got %q, want %q", innerVal, "value1")
	}
}

func TestNamespaceEngine_Delete(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("tenant1"))

	if err := ns.Set([]byte("key1"), []byte("value1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := ns.Delete([]byte("key1")); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// MemoryStorage returns nil, nil for missing keys.
	got, err := ns.Get([]byte("key1"))
	if err != nil {
		t.Fatalf("Get after Delete: unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("Get after Delete: got %q, want nil", got)
	}

	// Verify gone from inner storage as well.
	innerVal, err := inner.Get([]byte("tenant1/key1"))
	if err != nil {
		t.Fatalf("inner.Get after Delete: unexpected error: %v", err)
	}
	if innerVal != nil {
		t.Fatalf("inner.Get after Delete: got %q, want nil", innerVal)
	}
}

func TestNamespaceEngine_List(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("tenant1"))

	if err := ns.Set([]byte("users/alice"), []byte("a")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := ns.Set([]byte("users/bob"), []byte("b")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := ns.Set([]byte("config/db"), []byte("c")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	keys, err := ns.List([]byte("users/"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sort.Slice(keys, func(i, j int) bool {
		return bytes.Compare(keys[i], keys[j]) < 0
	})

	if len(keys) != 2 {
		t.Fatalf("List: got %d keys, want 2", len(keys))
	}
	if !bytes.Equal(keys[0], []byte("users/alice")) || !bytes.Equal(keys[1], []byte("users/bob")) {
		t.Fatalf("List: got %v, want [users/alice users/bob]", keys)
	}

	// Empty prefix returns all keys within namespace.
	allKeys, err := ns.List(nil)
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(allKeys) != 3 {
		t.Fatalf("List all: got %d keys, want 3", len(allKeys))
	}
}

func TestNamespaceEngine_Exists(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("tenant1"))

	if err := ns.Set([]byte("key1"), []byte("value1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	exists, err := ns.Exists([]byte("key1"))
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("Exists: got false, want true")
	}

	exists, err = ns.Exists([]byte("nonexistent"))
	if err != nil {
		t.Fatalf("Exists nonexistent: %v", err)
	}
	if exists {
		t.Fatal("Exists nonexistent: got true, want false")
	}
}

func TestNamespaceEngine_Isolation(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns1 := New(inner, []byte("tenant1"))
	ns2 := New(inner, []byte("tenant2"))

	if err := ns1.Set([]byte("shared-key"), []byte("tenant1-value")); err != nil {
		t.Fatalf("ns1 Set: %v", err)
	}
	if err := ns2.Set([]byte("shared-key"), []byte("tenant2-value")); err != nil {
		t.Fatalf("ns2 Set: %v", err)
	}

	got1, err := ns1.Get([]byte("shared-key"))
	if err != nil {
		t.Fatalf("ns1 Get: %v", err)
	}
	if string(got1) != "tenant1-value" {
		t.Fatalf("ns1 Get: got %q, want %q", got1, "tenant1-value")
	}

	got2, err := ns2.Get([]byte("shared-key"))
	if err != nil {
		t.Fatalf("ns2 Get: %v", err)
	}
	if string(got2) != "tenant2-value" {
		t.Fatalf("ns2 Get: got %q, want %q", got2, "tenant2-value")
	}

	// List in ns1 should not show ns2 keys.
	keys1, err := ns1.List(nil)
	if err != nil {
		t.Fatalf("ns1 List: %v", err)
	}
	if len(keys1) != 1 || !bytes.Equal(keys1[0], []byte("shared-key")) {
		t.Fatalf("ns1 List: got %v, want [shared-key]", keys1)
	}

	keys2, err := ns2.List(nil)
	if err != nil {
		t.Fatalf("ns2 List: %v", err)
	}
	if len(keys2) != 1 || !bytes.Equal(keys2[0], []byte("shared-key")) {
		t.Fatalf("ns2 List: got %v, want [shared-key]", keys2)
	}
}

func TestNamespaceEngine_EmptyKey(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("tenant1"))

	if _, err := ns.Get(nil); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Get empty key: got %v, want %v", err, storage.ErrEmptyKey)
	}
	if err := ns.Set(nil, []byte("data")); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Set empty key: got %v, want %v", err, storage.ErrEmptyKey)
	}
	if err := ns.Delete(nil); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Delete empty key: got %v, want %v", err, storage.ErrEmptyKey)
	}
	if _, err := ns.Exists(nil); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Exists empty key: got %v, want %v", err, storage.ErrEmptyKey)
	}

	// Also test with zero-length byte slice.
	if _, err := ns.Get([]byte{}); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Get zero-length key: got %v, want %v", err, storage.ErrEmptyKey)
	}
	if err := ns.Set([]byte{}, []byte("data")); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Set zero-length key: got %v, want %v", err, storage.ErrEmptyKey)
	}
	if err := ns.Delete([]byte{}); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Delete zero-length key: got %v, want %v", err, storage.ErrEmptyKey)
	}
	if _, err := ns.Exists([]byte{}); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Exists zero-length key: got %v, want %v", err, storage.ErrEmptyKey)
	}
}

func TestNamespaceEngine_Namespace(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("my-tenant"))
	if !bytes.Equal(ns.Namespace(), []byte("my-tenant")) {
		t.Fatalf("Namespace: got %q, want %q", ns.Namespace(), "my-tenant")
	}
}

func TestNamespaceEngine_Batch(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("tenant1"))

	batch := ns.Batch()
	if err := batch.Set([]byte("bk1"), []byte("bv1")); err != nil {
		t.Fatalf("batch Set: %v", err)
	}
	if err := batch.Set([]byte("bk2"), []byte("bv2")); err != nil {
		t.Fatalf("batch Set: %v", err)
	}
	if err := batch.Commit(); err != nil {
		t.Fatalf("batch Commit: %v", err)
	}
	if err := batch.Close(); err != nil {
		t.Fatalf("batch Close: %v", err)
	}

	// Verify keys are accessible through namespace.
	got, err := ns.Get([]byte("bk1"))
	if err != nil {
		t.Fatalf("Get bk1: %v", err)
	}
	if string(got) != "bv1" {
		t.Fatalf("Get bk1: got %q, want %q", got, "bv1")
	}

	// Verify keys are stored with prefix in inner storage.
	innerVal, err := inner.Get([]byte("tenant1/bk1"))
	if err != nil {
		t.Fatalf("inner.Get: %v", err)
	}
	if string(innerVal) != "bv1" {
		t.Fatalf("inner.Get: got %q, want %q", innerVal, "bv1")
	}
}

func TestNamespaceEngine_BatchDelete(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("tenant1"))

	if err := ns.Set([]byte("dk1"), []byte("dv1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	batch := ns.Batch()
	if err := batch.Delete([]byte("dk1")); err != nil {
		t.Fatalf("batch Delete: %v", err)
	}
	if err := batch.Commit(); err != nil {
		t.Fatalf("batch Commit: %v", err)
	}
	if err := batch.Close(); err != nil {
		t.Fatalf("batch Close: %v", err)
	}

	got, err := ns.Get([]byte("dk1"))
	if err != nil {
		t.Fatalf("Get after batch Delete: unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("Get after batch Delete: got %q, want nil", got)
	}
}

func TestNamespaceEngine_Iterator(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("tenant1"))

	if err := ns.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := ns.Set([]byte("b"), []byte("2")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := ns.Set([]byte("c"), []byte("3")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Also insert a key under a different namespace to verify isolation.
	ns2 := New(inner, []byte("tenant2"))
	if err := ns2.Set([]byte("x"), []byte("9")); err != nil {
		t.Fatalf("ns2 Set: %v", err)
	}

	iter := ns.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	var keys []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		keys = append(keys, string(iter.Key()))
	}
	if err := iter.Error(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}

	if len(keys) != 3 {
		t.Fatalf("iterator: got %d keys, want 3: %v", len(keys), keys)
	}
	if keys[0] != "a" || keys[1] != "b" || keys[2] != "c" {
		t.Fatalf("iterator: got %v, want [a b c]", keys)
	}
}

func TestNamespaceEngine_IteratorSeek(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("tenant1"))

	if err := ns.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := ns.Set([]byte("b"), []byte("2")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := ns.Set([]byte("c"), []byte("3")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := ns.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	if !iter.Seek([]byte("b")) {
		t.Fatal("Seek to 'b' should find a key")
	}
	if string(iter.Key()) != "b" {
		t.Fatalf("Seek: got key %q, want %q", iter.Key(), "b")
	}
	if string(iter.Value()) != "2" {
		t.Fatalf("Seek: got value %q, want %q", iter.Value(), "2")
	}
}

func TestNamespaceEngine_Snapshot(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("tenant1"))

	if err := ns.Set([]byte("sk1"), []byte("sv1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	snap, err := ns.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	// Modify after snapshot.
	if err := ns.Set([]byte("sk1"), []byte("sv2")); err != nil {
		t.Fatalf("Set after snapshot: %v", err)
	}

	// Snapshot should return the original value.
	got, err := snap.Get([]byte("sk1"))
	if err != nil {
		t.Fatalf("snap.Get: %v", err)
	}
	if string(got) != "sv1" {
		t.Fatalf("snap.Get: got %q, want %q", got, "sv1")
	}

	// Current storage should return the updated value.
	current, err := ns.Get([]byte("sk1"))
	if err != nil {
		t.Fatalf("ns.Get: %v", err)
	}
	if string(current) != "sv2" {
		t.Fatalf("ns.Get: got %q, want %q", current, "sv2")
	}
}

func TestNamespaceEngine_SnapshotIterator(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("tenant1"))

	if err := ns.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := ns.Set([]byte("b"), []byte("2")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	snap, err := ns.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	iter := snap.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	var keys []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		keys = append(keys, string(iter.Key()))
	}
	if err := iter.Error(); err != nil {
		t.Fatalf("snapshot iterator error: %v", err)
	}

	if len(keys) != 2 {
		t.Fatalf("snapshot iterator: got %d keys, want 2: %v", len(keys), keys)
	}
	if keys[0] != "a" || keys[1] != "b" {
		t.Fatalf("snapshot iterator: got %v, want [a b]", keys)
	}
}

func TestNamespaceEngine_Close(t *testing.T) {
	inner := memory.NewStorage()
	ns := New(inner, []byte("tenant1"))

	if err := ns.Set([]byte("key1"), []byte("value1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := ns.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// After closing the namespace engine (which closes inner), operations
	// on the inner storage should fail with ClosedError.
	var closedErr *storage.ClosedError
	_, err := inner.Get([]byte("anything"))
	if !errors.As(err, &closedErr) {
		t.Fatalf("inner.Get after Close: got %v, want ClosedError", err)
	}
}

func TestNamespaceEngine_CloseIdempotent(t *testing.T) {
	inner := memory.NewStorage()
	ns := New(inner, []byte("tenant1"))

	if err := ns.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Second close should be a no-op (inner memory storage is idempotent).
	if err := ns.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestNamespaceEngine_BatchRollback(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("tenant1"))

	// Write initial key.
	if err := ns.Set([]byte("existing"), []byte("original")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Create a batch with pending operations.
	batch := ns.Batch()
	if err := batch.Set([]byte("new-key"), []byte("new-value")); err != nil {
		t.Fatalf("batch Set: %v", err)
	}
	if err := batch.Delete([]byte("existing")); err != nil {
		t.Fatalf("batch Delete: %v", err)
	}

	// Rollback should discard all pending operations.
	if err := batch.Rollback(); err != nil {
		t.Fatalf("batch Rollback: %v", err)
	}
	if err := batch.Close(); err != nil {
		t.Fatalf("batch Close: %v", err)
	}

	// Verify the new key was NOT written.
	got, err := ns.Get([]byte("new-key"))
	if err != nil {
		t.Fatalf("Get new-key: %v", err)
	}
	if got != nil {
		t.Fatalf("Get new-key after Rollback: got %q, want nil", got)
	}

	// Verify the existing key was NOT deleted.
	got, err = ns.Get([]byte("existing"))
	if err != nil {
		t.Fatalf("Get existing: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("Get existing after Rollback: got %q, want %q", got, "original")
	}
}

func TestNamespaceEngine_IteratorSeekToLast(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("tenant1"))

	if err := ns.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := ns.Set([]byte("b"), []byte("2")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := ns.Set([]byte("c"), []byte("3")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Insert key under different namespace to verify isolation.
	ns2 := New(inner, []byte("tenant2"))
	if err := ns2.Set([]byte("z"), []byte("99")); err != nil {
		t.Fatalf("ns2 Set: %v", err)
	}

	iter := ns.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	if !iter.SeekToLast() {
		t.Fatal("SeekToLast should find a key")
	}
	if string(iter.Key()) != "c" {
		t.Fatalf("SeekToLast: got key %q, want %q", iter.Key(), "c")
	}
	if string(iter.Value()) != "3" {
		t.Fatalf("SeekToLast: got value %q, want %q", iter.Value(), "3")
	}
}

func TestNamespaceEngine_IteratorPrev(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("tenant1"))

	if err := ns.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := ns.Set([]byte("b"), []byte("2")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := ns.Set([]byte("c"), []byte("3")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := ns.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	// Position at the last key, then walk backward.
	if !iter.SeekToLast() {
		t.Fatal("SeekToLast should find a key")
	}

	var keys []string
	keys = append(keys, string(iter.Key()))
	for iter.Prev() {
		keys = append(keys, string(iter.Key()))
	}

	if len(keys) != 3 {
		t.Fatalf("Prev walk: got %d keys, want 3: %v", len(keys), keys)
	}
	if keys[0] != "c" || keys[1] != "b" || keys[2] != "a" {
		t.Fatalf("Prev walk: got %v, want [c b a]", keys)
	}
}

func TestNamespaceEngine_IteratorValid(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("tenant1"))

	if err := ns.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := ns.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	// Before any seek, Valid should return false.
	if iter.Valid() {
		t.Fatal("Valid before seek should be false")
	}

	// After seeking, Valid should return true.
	if !iter.SeekToFirst() {
		t.Fatal("SeekToFirst should find a key")
	}
	if !iter.Valid() {
		t.Fatal("Valid after SeekToFirst should be true")
	}

	// Advance past the last key.
	if iter.Next() {
		t.Fatal("Next past last key should return false")
	}
	if iter.Valid() {
		t.Fatal("Valid after exhausting iterator should be false")
	}
}

func TestNamespaceEngine_IteratorWithBounds(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("tenant1"))

	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if err := ns.Set([]byte(k), []byte(k+"-val")); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	// Create iterator with LowerBound="b" and UpperBound="d" (exclusive).
	iter := ns.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte("b"),
		UpperBound: []byte("d"),
	})
	defer func() { _ = iter.Close() }()

	var keys []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		keys = append(keys, string(iter.Key()))
	}

	if len(keys) != 2 {
		t.Fatalf("bounded iterator: got %d keys, want 2: %v", len(keys), keys)
	}
	if keys[0] != "b" || keys[1] != "c" {
		t.Fatalf("bounded iterator: got %v, want [b c]", keys)
	}
}

func TestNamespaceEngine_IteratorWithReverseAndOnlyKeys(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("tenant1"))

	for _, k := range []string{"a", "b", "c"} {
		if err := ns.Set([]byte(k), []byte(k+"-val")); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	// Create iterator with Reverse and OnlyKeys options.
	iter := ns.NewIterator(&storage.IteratorOptions{
		Reverse:  true,
		OnlyKeys: true,
	})
	defer func() { _ = iter.Close() }()

	// With OnlyKeys, Value() should return nil.
	if !iter.SeekToFirst() {
		t.Fatal("SeekToFirst should find a key")
	}
	if iter.Value() != nil {
		t.Fatalf("Value with OnlyKeys: got %q, want nil", iter.Value())
	}
	if string(iter.Key()) != "a" {
		t.Fatalf("Key: got %q, want %q", iter.Key(), "a")
	}
}

func TestNamespaceEngine_SnapshotIteratorWithBounds(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("tenant1"))

	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if err := ns.Set([]byte(k), []byte(k+"-val")); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	snap, err := ns.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	// Create snapshot iterator with bounds.
	iter := snap.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte("b"),
		UpperBound: []byte("d"),
	})
	defer func() { _ = iter.Close() }()

	var keys []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		keys = append(keys, string(iter.Key()))
	}

	if len(keys) != 2 {
		t.Fatalf("snapshot bounded iterator: got %d keys, want 2: %v", len(keys), keys)
	}
	if keys[0] != "b" || keys[1] != "c" {
		t.Fatalf("snapshot bounded iterator: got %v, want [b c]", keys)
	}
}

func TestNamespaceEngine_SnapshotIteratorWithReverseAndOnlyKeys(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	ns := New(inner, []byte("tenant1"))

	if err := ns.Set([]byte("x"), []byte("xv")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	snap, err := ns.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	// Pass Reverse and OnlyKeys through snapshot iterator.
	iter := snap.NewIterator(&storage.IteratorOptions{
		Reverse:  true,
		OnlyKeys: true,
	})
	defer func() { _ = iter.Close() }()

	if !iter.SeekToFirst() {
		t.Fatal("SeekToFirst should find a key")
	}
	if iter.Value() != nil {
		t.Fatalf("Value with OnlyKeys: got %q, want nil", iter.Value())
	}
}

// errorStorage is a minimal storage mock that returns errors from List and
// Snapshot, enabling tests for the error propagation paths in the namespace
// Engine.
type errorStorage struct {
	storage.Storage
	listErr     error
	snapshotErr error
}

func (s *errorStorage) List(prefix []byte) ([][]byte, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return nil, nil
}

func (s *errorStorage) Snapshot() (storage.Snapshot, error) {
	if s.snapshotErr != nil {
		return nil, s.snapshotErr
	}
	return nil, nil
}

func TestNamespaceEngine_ListError(t *testing.T) {
	listErr := &storage.OpError{Op: "list", Err: &storage.ClosedError{}}
	inner := &errorStorage{listErr: listErr}

	ns := New(inner, []byte("tenant1"))

	_, err := ns.List([]byte("prefix"))
	if err == nil {
		t.Fatal("List: expected error, got nil")
	}
	var opErr *storage.OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("List: expected OpError, got %T: %v", err, err)
	}
	if opErr.Op != "list" {
		t.Fatalf("List: expected Op %q, got %q", "list", opErr.Op)
	}
}

func TestNamespaceEngine_SnapshotError(t *testing.T) {
	snapErr := &storage.SnapshotError{Op: "create", Err: &storage.ClosedError{}}
	inner := &errorStorage{snapshotErr: snapErr}

	ns := New(inner, []byte("tenant1"))

	_, err := ns.Snapshot()
	if err == nil {
		t.Fatal("Snapshot: expected error, got nil")
	}
	var snapshotErr *storage.SnapshotError
	if !errors.As(err, &snapshotErr) {
		t.Fatalf("Snapshot: expected SnapshotError, got %T: %v", err, err)
	}
	if snapshotErr.Op != "create" {
		t.Fatalf("Snapshot: expected Op %q, got %q", "create", snapshotErr.Op)
	}
}
