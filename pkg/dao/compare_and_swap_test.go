//go:build codec_json

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

package dao

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// TestGenericDAO_CompareAndSwap_Succeeds proves a CAS against the exact
// bytes currently stored replaces them, in one guarded store operation
// rather than a read followed by a write (go-qrdb#589).
func TestGenericDAO_CompareAndSwap_Succeeds(t *testing.T) {
	ctx := context.Background()
	store := newMemoryKVStore()
	d, err := New(store, "entities", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	original := &TestEntity{ID: 1, Name: "alice", Email: "a@example.com"}
	if err := d.Save(ctx, original); err != nil {
		t.Fatalf("Save: %v", err)
	}

	next := &TestEntity{ID: 1, Name: "alice", Email: "alice@example.com"}
	swapped, err := d.CompareAndSwap(ctx, original, next)
	if err != nil {
		t.Fatalf("CompareAndSwap: %v", err)
	}
	if !swapped {
		t.Fatal("swapped = false, want true")
	}

	got, err := d.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Email != "alice@example.com" {
		t.Fatalf("Email = %q, want the swapped value", got.Email)
	}
}

// TestGenericDAO_CompareAndSwap_FailsOnStaleExpected proves a CAS against
// bytes that no longer match the stored value is refused, and the store is
// left unchanged -- the lost-update window SaveWithCondition's read-then-write
// cannot close.
func TestGenericDAO_CompareAndSwap_FailsOnStaleExpected(t *testing.T) {
	ctx := context.Background()
	store := newMemoryKVStore()
	d, err := New(store, "entities", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	original := &TestEntity{ID: 1, Name: "alice", Email: "a@example.com"}
	if err := d.Save(ctx, original); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// A concurrent writer already changed the record.
	concurrent := &TestEntity{ID: 1, Name: "alice", Email: "concurrent@example.com"}
	if err := d.Save(ctx, concurrent); err != nil {
		t.Fatalf("Save (concurrent): %v", err)
	}

	// This caller still believes `original` is current.
	next := &TestEntity{ID: 1, Name: "alice", Email: "mine@example.com"}
	swapped, err := d.CompareAndSwap(ctx, original, next)
	if err != nil {
		t.Fatalf("CompareAndSwap: %v", err)
	}
	if swapped {
		t.Fatal("swapped = true, want false: expected no longer matches stored bytes")
	}

	got, err := d.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Email != "concurrent@example.com" {
		t.Fatalf("Email = %q, want the concurrent writer's value preserved", got.Email)
	}
}

// TestGenericDAO_CompareAndSwap_MismatchedIDsRefused verifies CompareAndSwap
// refuses when expected and next name different entities, rather than
// comparing one key and writing another.
func TestGenericDAO_CompareAndSwap_MismatchedIDsRefused(t *testing.T) {
	ctx := context.Background()
	store := newMemoryKVStore()
	d, err := New(store, "entities", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	expected := &TestEntity{ID: 1, Name: "a"}
	next := &TestEntity{ID: 2, Name: "b"}

	var idErr InvalidEntityIDError
	if _, err := d.CompareAndSwap(ctx, expected, next); !errors.As(err, &idErr) {
		t.Fatalf("CompareAndSwap(mismatched IDs) = %v, want InvalidEntityIDError", err)
	}
}

// TestGenericDAO_CompareAndSwap_ZeroIDRefused verifies CompareAndSwap
// refuses an entity with no ID, the same way Delete refuses one.
func TestGenericDAO_CompareAndSwap_ZeroIDRefused(t *testing.T) {
	ctx := context.Background()
	store := newMemoryKVStore()
	d, err := New(store, "entities", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	zero := &TestEntity{ID: 0}
	var idErr InvalidEntityIDError
	if _, err := d.CompareAndSwap(ctx, zero, zero); !errors.As(err, &idErr) {
		t.Fatalf("CompareAndSwap(id=0) = %v, want InvalidEntityIDError", err)
	}
}

// TestGenericDAO_CompareAndSwap_NonAtomicStoreIsRefused mirrors
// TestSaveIfAbsent_NonAtomicStoreIsRefused: a store with no atomic write is
// refused, never emulated with Get-then-Put.
func TestGenericDAO_CompareAndSwap_NonAtomicStoreIsRefused(t *testing.T) {
	ctx := context.Background()
	inner := newMemoryKVStore()
	d, err := New(plainKVStore{inner}, "entities", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e := &TestEntity{ID: 1, Name: "a"}
	var atomicErr *AtomicWriteUnsupportedError
	_, casErr := d.CompareAndSwap(ctx, e, e)
	if !errors.As(casErr, &atomicErr) {
		t.Fatalf("CompareAndSwap over a non-atomic store = %v, want *AtomicWriteUnsupportedError", casErr)
	}
	if !errors.Is(casErr, kvstore.ErrAtomicWriteUnsupported) {
		t.Fatalf("err = %v, want it to match kvstore.ErrAtomicWriteUnsupported", casErr)
	}
}

// TestGenericDAO_DeleteIfMatch_Succeeds proves DeleteIfMatch removes the
// entity when the stored bytes still equal its own.
func TestGenericDAO_DeleteIfMatch_Succeeds(t *testing.T) {
	ctx := context.Background()
	store := newMemoryKVStore()
	d, err := New(store, "entities", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e := &TestEntity{ID: 1, Name: "alice"}
	if err := d.Save(ctx, e); err != nil {
		t.Fatalf("Save: %v", err)
	}

	deleted, err := d.DeleteIfMatch(ctx, e)
	if err != nil {
		t.Fatalf("DeleteIfMatch: %v", err)
	}
	if !deleted {
		t.Fatal("deleted = false, want true")
	}

	var notFoundErr NotFoundError
	if _, err := d.Get(ctx, 1); !errors.As(err, &notFoundErr) {
		t.Fatalf("Get after DeleteIfMatch = %v, want NotFoundError", err)
	}
}

// TestGenericDAO_DeleteIfMatch_FailsWhenRecordChanged proves DeleteIfMatch
// refuses to delete a record a concurrent writer has since replaced, so the
// caller's stale read cannot destroy the winner's write.
func TestGenericDAO_DeleteIfMatch_FailsWhenRecordChanged(t *testing.T) {
	ctx := context.Background()
	store := newMemoryKVStore()
	d, err := New(store, "entities", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	stale := &TestEntity{ID: 1, Name: "alice"}
	if err := d.Save(ctx, stale); err != nil {
		t.Fatalf("Save: %v", err)
	}
	concurrent := &TestEntity{ID: 1, Name: "bob"}
	if err := d.Save(ctx, concurrent); err != nil {
		t.Fatalf("Save (concurrent): %v", err)
	}

	deleted, err := d.DeleteIfMatch(ctx, stale)
	if err != nil {
		t.Fatalf("DeleteIfMatch: %v", err)
	}
	if deleted {
		t.Fatal("deleted = true, want false: record changed since the caller's read")
	}

	got, err := d.Get(ctx, 1)
	if err != nil || got.Name != "bob" {
		t.Fatalf("Get after refused DeleteIfMatch = (%+v, %v), want bob preserved", got, err)
	}
}

// TestGenericDAO_DeleteIfMatch_ZeroIDRefused verifies DeleteIfMatch refuses
// an entity with no ID, mirroring Delete.
func TestGenericDAO_DeleteIfMatch_ZeroIDRefused(t *testing.T) {
	ctx := context.Background()
	store := newMemoryKVStore()
	d, err := New(store, "entities", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	zero := &TestEntity{ID: 0}
	var idErr InvalidEntityIDError
	if _, err := d.DeleteIfMatch(ctx, zero); !errors.As(err, &idErr) {
		t.Fatalf("DeleteIfMatch(id=0) = %v, want InvalidEntityIDError", err)
	}
}

// TestGenericDAO_DeleteIfMatch_NonAtomicStoreIsRefused mirrors the
// CompareAndSwap case.
func TestGenericDAO_DeleteIfMatch_NonAtomicStoreIsRefused(t *testing.T) {
	ctx := context.Background()
	inner := newMemoryKVStore()
	d, err := New(plainKVStore{inner}, "entities", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e := &TestEntity{ID: 1, Name: "a"}
	var atomicErr *AtomicWriteUnsupportedError
	if _, err := d.DeleteIfMatch(ctx, e); !errors.As(err, &atomicErr) {
		t.Fatalf("DeleteIfMatch over a non-atomic store = %v, want *AtomicWriteUnsupportedError", err)
	}
}
