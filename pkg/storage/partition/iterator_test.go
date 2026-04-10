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
	"fmt"
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

func TestIterator_SeekToFirst_SortedOrder(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	// Insert keys that will be spread across partitions.
	keys := []string{"delta", "alpha", "echo", "bravo", "charlie"}
	for _, k := range keys {
		if err := m.Set([]byte(k), []byte("v")); err != nil {
			t.Fatalf("Set(%q) failed: %v", k, err)
		}
	}

	iter := m.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		collected = append(collected, string(iter.Key()))
	}

	// Should be sorted.
	sorted := make([]string, len(keys))
	copy(sorted, keys)
	sort.Strings(sorted)

	if len(collected) != len(sorted) {
		t.Fatalf("iterator returned %d keys, want %d", len(collected), len(sorted))
	}
	for i := range sorted {
		if collected[i] != sorted[i] {
			t.Errorf("key[%d] = %q, want %q", i, collected[i], sorted[i])
		}
	}
}

func TestIterator_SeekToLast(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	keys := []string{"alpha", "bravo", "charlie", "delta"}
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
	lastKey := keys[len(keys)-1]
	if got := string(iter.Key()); got != lastKey {
		t.Errorf("SeekToLast key = %q, want %q", got, lastKey)
	}
}

func TestIterator_Seek(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	for i := 0; i < 20; i++ {
		key := []byte(fmt.Sprintf("key-%02d", i))
		if err := m.Set(key, []byte("v")); err != nil {
			t.Fatalf("Set failed: %v", err)
		}
	}

	iter := m.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	// Seek to key-10.
	if !iter.Seek([]byte("key-10")) {
		t.Fatal("Seek returned false")
	}

	got := string(iter.Key())
	if got != "key-10" {
		t.Errorf("Seek key = %q, want %q", got, "key-10")
	}

	// Continue iterating and verify order.
	var remaining []string
	remaining = append(remaining, got)
	for iter.Next() {
		remaining = append(remaining, string(iter.Key()))
	}

	if !sort.StringsAreSorted(remaining) {
		t.Errorf("keys after Seek are not sorted: %v", remaining)
	}
}

func TestIterator_WithBounds(t *testing.T) {
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
	}

	iter := m.NewIterator(opts)
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		collected = append(collected, string(iter.Key()))
	}

	// Should include key-03, key-04, key-05, key-06 (upper bound is exclusive).
	expected := []string{"key-03", "key-04", "key-05", "key-06"}
	if len(collected) != len(expected) {
		t.Fatalf("bounded iterator returned %d keys, want %d: %v", len(collected), len(expected), collected)
	}
	for i := range expected {
		if collected[i] != expected[i] {
			t.Errorf("key[%d] = %q, want %q", i, collected[i], expected[i])
		}
	}
}

func TestIterator_EmptyStorage(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	iter := m.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	if iter.SeekToFirst() {
		t.Error("SeekToFirst on empty storage should return false")
	}
	if iter.Valid() {
		t.Error("Valid on empty storage should return false")
	}
	if iter.Key() != nil {
		t.Error("Key on empty storage should return nil")
	}
	if iter.Value() != nil {
		t.Error("Value on empty storage should return nil")
	}
}

func TestIterator_SeekToLast_Empty(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	iter := m.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	if iter.SeekToLast() {
		t.Error("SeekToLast on empty storage should return false")
	}
}

func TestIterator_Close(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	if err := m.Set([]byte("key"), []byte("val")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	iter := m.NewIterator(nil)
	if err := iter.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Operations after close should be safe.
	if iter.SeekToFirst() {
		t.Error("SeekToFirst after close should return false")
	}
	if iter.Valid() {
		t.Error("Valid after close should return false")
	}
}

func TestIterator_DoubleClose(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	iter := m.NewIterator(nil)
	if err := iter.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	if err := iter.Close(); err != nil {
		t.Fatalf("second Close should be no-op: %v", err)
	}
}

func TestIterator_Error(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	iter := m.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	// Memory-backed iterators don't produce errors.
	if err := iter.Error(); err != nil {
		t.Errorf("Error() = %v, want nil", err)
	}
}

func TestIterator_Prev_NotSupported(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	if err := m.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	iter := m.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	iter.SeekToFirst()
	if iter.Prev() {
		t.Error("Prev on merge iterator should return false")
	}
}

func TestIterator_Values(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	if err := m.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	if err := m.Set([]byte("k2"), []byte("v2")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	iter := m.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	kvMap := make(map[string]string)
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		kvMap[string(iter.Key())] = string(iter.Value())
	}

	if kvMap["k1"] != "v1" {
		t.Errorf("kvMap[k1] = %q, want %q", kvMap["k1"], "v1")
	}
	if kvMap["k2"] != "v2" {
		t.Errorf("kvMap[k2] = %q, want %q", kvMap["k2"], "v2")
	}
}

func TestIterator_ManyKeys_Sorted(t *testing.T) {
	m := newTestManager(t, 8)
	defer func() { _ = m.Close() }()

	const count = 200
	var expected []string
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("key-%04d", i)
		expected = append(expected, key)
		if err := m.Set([]byte(key), []byte("v")); err != nil {
			t.Fatalf("Set failed: %v", err)
		}
	}
	sort.Strings(expected)

	iter := m.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		collected = append(collected, string(iter.Key()))
	}

	if len(collected) != len(expected) {
		t.Fatalf("collected %d keys, want %d", len(collected), len(expected))
	}
	for i := range expected {
		if collected[i] != expected[i] {
			t.Errorf("key[%d] = %q, want %q", i, collected[i], expected[i])
		}
	}
}

func TestClosedMergeIterator(t *testing.T) {
	iter := newClosedMergeIterator()

	if iter.SeekToFirst() {
		t.Error("SeekToFirst on closed should return false")
	}
	if iter.SeekToLast() {
		t.Error("SeekToLast on closed should return false")
	}
	if iter.Seek([]byte("a")) {
		t.Error("Seek on closed should return false")
	}
	if iter.Next() {
		t.Error("Next on closed should return false")
	}
	if iter.Valid() {
		t.Error("Valid on closed should return false")
	}
	if iter.Key() != nil {
		t.Error("Key on closed should return nil")
	}
	if iter.Value() != nil {
		t.Error("Value on closed should return nil")
	}
}

func TestIterator_Next_BeforeSeek(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	if err := m.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	iter := m.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	// Next without Seek should return false.
	if iter.Next() {
		t.Error("Next before Seek should return false")
	}
}
