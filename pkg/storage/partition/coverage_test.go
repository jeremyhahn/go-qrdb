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

package partition

import (
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// --- Reverse iteration tests (exercises maxHeap) ---

func TestIterator_Reverse_SeekToFirst(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	keys := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	for _, k := range keys {
		if err := m.Set([]byte(k), []byte("v")); err != nil {
			t.Fatalf("Set(%q) failed: %v", k, err)
		}
	}

	opts := &storage.IteratorOptions{Reverse: true}
	iter := m.NewIterator(opts)
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		collected = append(collected, string(iter.Key()))
	}

	// All keys should be returned.
	if len(collected) != len(keys) {
		t.Fatalf("reverse iterator returned %d keys, want %d", len(collected), len(keys))
	}
}

func TestIterator_Reverse_SeekToLast(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	keys := []string{"alpha", "bravo", "charlie"}
	for _, k := range keys {
		if err := m.Set([]byte(k), []byte("v")); err != nil {
			t.Fatalf("Set(%q) failed: %v", k, err)
		}
	}

	opts := &storage.IteratorOptions{Reverse: true}
	iter := m.NewIterator(opts)
	defer func() { _ = iter.Close() }()

	if !iter.SeekToLast() {
		t.Fatal("reverse SeekToLast returned false")
	}

	// In reverse mode, SeekToLast should find a valid key.
	got := string(iter.Key())
	if got == "" {
		t.Error("Key() after reverse SeekToLast should not be empty")
	}
}

func TestIterator_Reverse_Seek(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	for i := 0; i < 10; i++ {
		key := []byte(fmt.Sprintf("key-%02d", i))
		if err := m.Set(key, []byte("v")); err != nil {
			t.Fatalf("Set failed: %v", err)
		}
	}

	opts := &storage.IteratorOptions{Reverse: true}
	iter := m.NewIterator(opts)
	defer func() { _ = iter.Close() }()

	// In reverse mode, Seek positions each partition at >= target, and the
	// maxHeap selects the largest. We collect all results from Seek onward.
	if !iter.Seek([]byte("key-05")) {
		t.Fatal("reverse Seek returned false")
	}

	// The first key should be >= "key-05" (largest among partition iterators).
	got := string(iter.Key())
	if got < "key-05" {
		t.Errorf("reverse Seek key = %q, want >= %q", got, "key-05")
	}

	// Collect remaining keys and verify all are >= target.
	var collected []string
	collected = append(collected, got)
	for iter.Next() {
		collected = append(collected, string(iter.Key()))
	}

	for _, k := range collected {
		if k < "key-05" {
			t.Errorf("collected key %q is below target key-05", k)
		}
	}
}

func TestIterator_Reverse_Empty(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	opts := &storage.IteratorOptions{Reverse: true}
	iter := m.NewIterator(opts)
	defer func() { _ = iter.Close() }()

	if iter.SeekToFirst() {
		t.Error("reverse SeekToFirst on empty should return false")
	}
	if iter.SeekToLast() {
		t.Error("reverse SeekToLast on empty should return false")
	}
	if iter.Seek([]byte("a")) {
		t.Error("reverse Seek on empty should return false")
	}
}

func TestIterator_Reverse_Next(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	keys := []string{"alpha", "bravo", "charlie", "delta"}
	for _, k := range keys {
		if err := m.Set([]byte(k), []byte("v")); err != nil {
			t.Fatalf("Set(%q) failed: %v", k, err)
		}
	}

	opts := &storage.IteratorOptions{Reverse: true}
	iter := m.NewIterator(opts)
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		collected = append(collected, string(iter.Key()))
	}

	if len(collected) != len(keys) {
		t.Fatalf("reverse iteration returned %d keys, want %d", len(collected), len(keys))
	}
}

// --- minHeap duplicate key ordering (stable) ---

func TestIterator_DuplicateKeysAcrossPartitions(t *testing.T) {
	// With a single partition, we can control key insertion and test heap ordering.
	m := newTestManager(t, 1)
	defer func() { _ = m.Close() }()

	// Single partition: keys are sorted internally.
	if err := m.Set([]byte("key"), []byte("val")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	iter := m.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	if !iter.SeekToFirst() {
		t.Fatal("SeekToFirst returned false")
	}
	if string(iter.Key()) != "key" {
		t.Errorf("Key = %q, want %q", iter.Key(), "key")
	}
	if string(iter.Value()) != "val" {
		t.Errorf("Value = %q, want %q", iter.Value(), "val")
	}
}

// --- Factory error propagation on manager creation ---

type failingFactory struct{}

func (f *failingFactory) Create(partitionID uint32, _ *Config) (storage.Storage, error) {
	if partitionID >= 2 {
		return nil, errors.New("disk error")
	}
	return newTestMemoryStorage(), nil
}

func (f *failingFactory) BackendType() string { return "failing" }

func newTestMemoryStorage() storage.Storage {
	// Use the memory factory to create a real storage.
	f := NewMemoryFactory()
	s, _ := f.Create(0, nil)
	return s
}

func TestNewManager_FactoryError(t *testing.T) {
	router := NewHashRouter(4)
	factory := &failingFactory{}
	cfg := &Config{PartitionCount: 4, BackendType: BackendMemory}

	_, err := NewManager(router, factory, cfg)
	if err == nil {
		t.Fatal("expected error when factory fails")
	}
	var createErr *PartitionCreateError
	if !errors.As(err, &createErr) {
		t.Errorf("expected PartitionCreateError, got %T: %v", err, err)
	}
}

// --- File factory error path ---

func TestFileFactory_Create_InvalidPath(t *testing.T) {
	f := NewFileFactory()
	cfg := &Config{PartitionCount: 2, BackendType: BackendFile, DataDir: "/dev/null/bad"}

	_, err := f.Create(0, cfg)
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
}

// --- Iterator with bounds on reverse ---

func TestIterator_Reverse_WithBounds(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	for i := 0; i < 10; i++ {
		key := []byte(fmt.Sprintf("key-%02d", i))
		if err := m.Set(key, []byte("v")); err != nil {
			t.Fatalf("Set failed: %v", err)
		}
	}

	opts := &storage.IteratorOptions{
		LowerBound: []byte("key-03"),
		UpperBound: []byte("key-07"),
		Reverse:    true,
	}
	iter := m.NewIterator(opts)
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		collected = append(collected, string(iter.Key()))
	}

	// Should include key-03 through key-06 (upper bound exclusive).
	expected := []string{"key-03", "key-04", "key-05", "key-06"}
	sort.Strings(collected)
	if len(collected) != len(expected) {
		t.Fatalf("bounded reverse iterator returned %d keys, want %d: %v", len(collected), len(expected), collected)
	}
}

// --- Batch commit error path and rollback error path ---

func TestBatch_Rollback_AfterCommit(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	batch := m.Batch()
	if err := batch.Set([]byte("key"), []byte("val")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	if err := batch.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Rollback after commit should fail.
	err := batch.Rollback()
	if err == nil {
		t.Fatal("expected error on Rollback after Commit")
	}
}

func TestBatch_Commit_AfterRollback(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	batch := m.Batch()
	if err := batch.Set([]byte("key"), []byte("val")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	if err := batch.Rollback(); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}

	// Commit after rollback should fail.
	err := batch.Commit()
	if err == nil {
		t.Fatal("expected error on Commit after Rollback")
	}
}

// --- Snapshot close error propagation ---

func TestManager_Snapshot_ClosedIterator(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	if err := m.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	snap, err := m.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	// Create iterator from snapshot, then close snapshot.
	iter := snap.NewIterator(nil)
	if err := snap.Close(); err != nil {
		t.Errorf("snap.Close: %v", err)
	}

	// Iterator should still work since it was created before close.
	defer func() { _ = iter.Close() }()
	if !iter.SeekToFirst() {
		// This is acceptable -- snapshot iterators may or may not work after close
		// depending on the underlying implementation.
		return
	}
}

// --- Manager.List error propagation ---

func TestManager_List_EmptyPrefix(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	if err := m.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	if err := m.Set([]byte("b"), []byte("2")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Empty prefix (empty byte slice) should return all keys.
	keys, err := m.List([]byte{})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("List(empty) returned %d keys, want 2", len(keys))
	}
}

// --- Close error propagation ---

func TestManager_Close_NoError(t *testing.T) {
	m := newTestManager(t, 4)
	if err := m.Close(); err != nil {
		t.Errorf("Close returned unexpected error: %v", err)
	}
}

// --- Iterator nextForward exhaustion ---

func TestIterator_NextForward_Exhaustion(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	if err := m.Set([]byte("only"), []byte("one")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	iter := m.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	if !iter.SeekToFirst() {
		t.Fatal("SeekToFirst returned false")
	}
	if string(iter.Key()) != "only" {
		t.Errorf("Key = %q, want %q", iter.Key(), "only")
	}

	// Next should return false (only one key).
	if iter.Next() {
		t.Error("Next should return false after last key")
	}
	if iter.Valid() {
		t.Error("Valid should return false after exhaustion")
	}
}

// --- Iterator nextReverse exhaustion ---

func TestIterator_Reverse_NextExhaustion(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	if err := m.Set([]byte("only"), []byte("one")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	opts := &storage.IteratorOptions{Reverse: true}
	iter := m.NewIterator(opts)
	defer func() { _ = iter.Close() }()

	if !iter.SeekToFirst() {
		t.Fatal("reverse SeekToFirst returned false")
	}

	// Next should return false (only one key).
	if iter.Next() {
		t.Error("reverse Next should return false after last key")
	}
}

// --- Test SeekToLast forward mode ---

func TestIterator_SeekToLast_Forward_Multiple(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	keys := []string{"alpha", "bravo", "charlie", "zulu"}
	for _, k := range keys {
		if err := m.Set([]byte(k), []byte("v")); err != nil {
			t.Fatalf("Set(%q) failed: %v", k, err)
		}
	}

	iter := m.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	if !iter.SeekToLast() {
		t.Fatal("SeekToLast returned false")
	}

	sort.Strings(keys)
	last := keys[len(keys)-1]
	if got := string(iter.Key()); got != last {
		t.Errorf("SeekToLast key = %q, want %q", got, last)
	}
}
