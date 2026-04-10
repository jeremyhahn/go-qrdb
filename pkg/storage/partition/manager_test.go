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
	"bytes"
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// newTestManager creates a memory-backed partition manager for testing.
func newTestManager(t *testing.T, partitions uint32) *Manager {
	t.Helper()
	router := NewHashRouter(partitions)
	factory := NewMemoryFactory()
	cfg := &Config{PartitionCount: partitions, BackendType: BackendMemory}
	m, err := NewManager(router, factory, cfg)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	return m
}

func TestNewManager_NilConfig(t *testing.T) {
	_, err := NewManager(NewHashRouter(4), NewMemoryFactory(), nil)
	if err == nil {
		t.Fatal("expected error for nil config")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected ConfigError, got %T: %v", err, err)
	}
}

func TestNewManager_InvalidConfig(t *testing.T) {
	cfg := &Config{PartitionCount: 0, BackendType: BackendMemory}
	_, err := NewManager(NewHashRouter(4), NewMemoryFactory(), cfg)
	if err == nil {
		t.Fatal("expected error for invalid config")
	}
}

func TestNewManager_NilRouter(t *testing.T) {
	cfg := &Config{PartitionCount: 4, BackendType: BackendMemory}
	_, err := NewManager(nil, NewMemoryFactory(), cfg)
	if err == nil {
		t.Fatal("expected error for nil router")
	}
}

func TestNewManager_NilFactory(t *testing.T) {
	cfg := &Config{PartitionCount: 4, BackendType: BackendMemory}
	_, err := NewManager(NewHashRouter(4), nil, cfg)
	if err == nil {
		t.Fatal("expected error for nil factory")
	}
}

func TestManager_SetGetDelete(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	key := []byte("test-key")
	val := []byte("test-value")

	// Set
	if err := m.Set(key, val); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Get
	got, err := m.Get(key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !bytes.Equal(got, val) {
		t.Errorf("Get = %q, want %q", got, val)
	}

	// Exists
	exists, err := m.Exists(key)
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Error("Exists = false, want true")
	}

	// Delete
	if err := m.Delete(key); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Get after delete
	got, err = m.Get(key)
	if err != nil {
		t.Fatalf("Get after delete failed: %v", err)
	}
	if got != nil {
		t.Errorf("Get after delete = %q, want nil", got)
	}

	// Exists after delete
	exists, err = m.Exists(key)
	if err != nil {
		t.Fatalf("Exists after delete failed: %v", err)
	}
	if exists {
		t.Error("Exists after delete = true, want false")
	}
}

func TestManager_List(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	// Insert keys across multiple partitions.
	keys := []string{"user/alice", "user/bob", "user/charlie", "order/1", "order/2"}
	for _, k := range keys {
		if err := m.Set([]byte(k), []byte("val")); err != nil {
			t.Fatalf("Set(%q) failed: %v", k, err)
		}
	}

	// List with prefix should collect from all partitions.
	userKeys, err := m.List([]byte("user/"))
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(userKeys) != 3 {
		t.Errorf("List(user/) returned %d keys, want 3", len(userKeys))
	}

	// List all keys.
	allKeys, err := m.List(nil)
	if err != nil {
		t.Fatalf("List(nil) failed: %v", err)
	}
	if len(allKeys) != 5 {
		t.Errorf("List(nil) returned %d keys, want 5", len(allKeys))
	}
}

func TestManager_ManyKeys(t *testing.T) {
	m := newTestManager(t, 8)
	defer func() { _ = m.Close() }()

	const count = 1000
	for i := 0; i < count; i++ {
		key := []byte(fmt.Sprintf("key-%04d", i))
		val := []byte(fmt.Sprintf("val-%04d", i))
		if err := m.Set(key, val); err != nil {
			t.Fatalf("Set(%q) failed: %v", key, err)
		}
	}

	// Verify all keys are retrievable.
	for i := 0; i < count; i++ {
		key := []byte(fmt.Sprintf("key-%04d", i))
		expected := []byte(fmt.Sprintf("val-%04d", i))
		got, err := m.Get(key)
		if err != nil {
			t.Fatalf("Get(%q) failed: %v", key, err)
		}
		if !bytes.Equal(got, expected) {
			t.Errorf("Get(%q) = %q, want %q", key, got, expected)
		}
	}

	// List all keys.
	allKeys, err := m.List(nil)
	if err != nil {
		t.Fatalf("List(nil) failed: %v", err)
	}
	if len(allKeys) != count {
		t.Errorf("List(nil) returned %d keys, want %d", len(allKeys), count)
	}
}

func TestManager_ClosedOperations(t *testing.T) {
	m := newTestManager(t, 2)
	_ = m.Close()

	var closedErr *storage.ClosedError

	if _, err := m.Get([]byte("key")); !errors.As(err, &closedErr) {
		t.Errorf("Get on closed: got %v, want ClosedError", err)
	}
	if err := m.Set([]byte("key"), []byte("val")); !errors.As(err, &closedErr) {
		t.Errorf("Set on closed: got %v, want ClosedError", err)
	}
	if err := m.Delete([]byte("key")); !errors.As(err, &closedErr) {
		t.Errorf("Delete on closed: got %v, want ClosedError", err)
	}
	if _, err := m.List(nil); !errors.As(err, &closedErr) {
		t.Errorf("List on closed: got %v, want ClosedError", err)
	}
	if _, err := m.Exists([]byte("key")); !errors.As(err, &closedErr) {
		t.Errorf("Exists on closed: got %v, want ClosedError", err)
	}
	if _, err := m.Snapshot(); !errors.As(err, &closedErr) {
		t.Errorf("Snapshot on closed: got %v, want ClosedError", err)
	}
}

func TestManager_DoubleClose(t *testing.T) {
	m := newTestManager(t, 2)
	if err := m.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	// Second close should be a no-op.
	if err := m.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
}

func TestManager_PartitionCount(t *testing.T) {
	m := newTestManager(t, 7)
	defer func() { _ = m.Close() }()

	if got := m.PartitionCount(); got != 7 {
		t.Errorf("PartitionCount() = %d, want 7", got)
	}
}

func TestManager_SinglePartition(t *testing.T) {
	m := newTestManager(t, 1)
	defer func() { _ = m.Close() }()

	if err := m.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	if err := m.Set([]byte("b"), []byte("2")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	got, err := m.Get([]byte("a"))
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(got) != "1" {
		t.Errorf("Get(a) = %q, want %q", got, "1")
	}
}

func TestManager_Snapshot(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	// Write data.
	if err := m.Set([]byte("key1"), []byte("val1")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Take snapshot.
	snap, err := m.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
	defer func() { _ = snap.Close() }()

	// Write more data after snapshot.
	if err := m.Set([]byte("key2"), []byte("val2")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Snapshot should see key1 but not key2.
	got, err := snap.Get([]byte("key1"))
	if err != nil {
		t.Fatalf("snap.Get(key1) failed: %v", err)
	}
	if string(got) != "val1" {
		t.Errorf("snap.Get(key1) = %q, want %q", got, "val1")
	}

	got, err = snap.Get([]byte("key2"))
	if err != nil {
		t.Fatalf("snap.Get(key2) failed: %v", err)
	}
	if got != nil {
		t.Errorf("snap.Get(key2) = %q, want nil (not in snapshot)", got)
	}
}

func TestManager_SnapshotClose(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	if err := m.Set([]byte("key"), []byte("val")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	snap, err := m.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	// Close snapshot.
	if err := snap.Close(); err != nil {
		t.Fatalf("snap.Close failed: %v", err)
	}

	// Operations on closed snapshot should error.
	_, err = snap.Get([]byte("key"))
	if err == nil {
		t.Error("expected error on Get after snapshot Close")
	}
}

func TestManager_SnapshotDoubleClose(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	snap, err := m.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	if err := snap.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	if err := snap.Close(); err != nil {
		t.Fatalf("second Close should be no-op: %v", err)
	}
}

func TestManager_ClosedIterator(t *testing.T) {
	m := newTestManager(t, 2)
	_ = m.Close()

	iter := m.NewIterator(nil)
	if iter.SeekToFirst() {
		t.Error("SeekToFirst on closed manager should return false")
	}
	if iter.Valid() {
		t.Error("Valid on closed manager iterator should return false")
	}
}

func TestManager_PartitionByID(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	// Valid partition.
	_, err := m.partitionByID(0)
	if err != nil {
		t.Errorf("partitionByID(0) failed: %v", err)
	}

	// Invalid partition.
	_, err = m.partitionByID(10)
	if err == nil {
		t.Fatal("expected error for out-of-range partition ID")
	}
	var pnfErr *PartitionNotFoundError
	if !errors.As(err, &pnfErr) {
		t.Errorf("expected PartitionNotFoundError, got %T: %v", err, err)
	}
}

func TestManager_SnapshotIterator(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	// Write data spread across partitions.
	keys := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	for _, k := range keys {
		if err := m.Set([]byte(k), []byte("val-"+k)); err != nil {
			t.Fatalf("Set(%q) failed: %v", k, err)
		}
	}

	snap, err := m.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
	defer func() { _ = snap.Close() }()

	// Iterate over snapshot and collect keys.
	iter := snap.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		collected = append(collected, string(iter.Key()))
	}

	sort.Strings(keys)
	sort.Strings(collected)

	if len(collected) != len(keys) {
		t.Fatalf("snapshot iterator returned %d keys, want %d", len(collected), len(keys))
	}
	for i := range keys {
		if collected[i] != keys[i] {
			t.Errorf("key[%d] = %q, want %q", i, collected[i], keys[i])
		}
	}
}

func TestManager_ClosedSnapshotIterator(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	snap, err := m.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
	_ = snap.Close()

	iter := snap.NewIterator(nil)
	if iter.SeekToFirst() {
		t.Error("SeekToFirst on closed snapshot should return false")
	}
}
