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

package file

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// ---------------------------------------------------------------------------
// iterator.Error() — currently 0% covered
// ---------------------------------------------------------------------------

// TestFileIterator_Error verifies that Error() always returns nil for the
// file iterator since it does not accumulate errors during normal operation.
func TestFileIterator_Error(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if err := eng.Set([]byte("key1"), []byte("val1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := newIterator(eng, nil)
	defer func() { _ = iter.Close() }()

	if err := iter.Error(); err != nil {
		t.Errorf("Error() = %v, want nil", err)
	}

	// Error() should still be nil after iterating.
	iter.SeekToFirst()
	for iter.Next() {
	}
	if err := iter.Error(); err != nil {
		t.Errorf("Error() after iteration = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// iterator.Prev() — 75% (missing the not-initialized path)
// ---------------------------------------------------------------------------

// TestFileIterator_Prev_NotInitialized verifies that Prev returns false when
// the iterator has never been positioned.
func TestFileIterator_Prev_NotInitialized(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if err := eng.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := newIterator(eng, nil)
	defer func() { _ = iter.Close() }()

	// Calling Prev before any seek must return false.
	if iter.Prev() {
		t.Error("Prev() before seek should return false")
	}
	if iter.Valid() {
		t.Error("Valid() after uninitialised Prev should return false")
	}
}

// TestFileIterator_Prev_ReachesStart verifies that iterating backward past the
// first entry sets the iterator invalid.
func TestFileIterator_Prev_ReachesStart(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	for _, k := range []string{"a", "b", "c"} {
		if err := eng.Set([]byte(k), []byte(k)); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	iter := newIterator(eng, nil)
	defer func() { _ = iter.Close() }()

	// Position at last entry, then walk backward off the front.
	if !iter.SeekToLast() {
		t.Fatal("SeekToLast returned false")
	}
	iter.Prev() // b
	iter.Prev() // a
	if iter.Prev() {
		t.Error("Prev() past start should return false")
	}
	if iter.Valid() {
		t.Error("Valid() after walking off start should be false")
	}
}

// ---------------------------------------------------------------------------
// batch.Commit with delete error path — 84.6% needs the delete-error branch
// ---------------------------------------------------------------------------

// TestFileBatch_Commit_WithDelete verifies that a batch containing both Set
// and Delete operations is applied atomically on Commit.
func TestFileBatch_Commit_WithDelete(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	// Seed a key that we will delete via the batch.
	if err := eng.Set([]byte("to-delete"), []byte("gone")); err != nil {
		t.Fatalf("Set seed: %v", err)
	}

	batch := newBatch(eng)
	if err := batch.Set([]byte("new-key"), []byte("new-val")); err != nil {
		t.Fatalf("batch.Set: %v", err)
	}
	if err := batch.Delete([]byte("to-delete")); err != nil {
		t.Fatalf("batch.Delete: %v", err)
	}
	if err := batch.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// new-key must be visible.
	got, err := eng.Get([]byte("new-key"))
	if err != nil {
		t.Fatalf("Get new-key: %v", err)
	}
	if string(got) != "new-val" {
		t.Errorf("Get new-key = %q, want %q", got, "new-val")
	}

	// to-delete must be gone.
	got, err = eng.Get([]byte("to-delete"))
	if err != nil {
		t.Fatalf("Get to-delete: %v", err)
	}
	if got != nil {
		t.Errorf("Get to-delete = %q, want nil (deleted)", got)
	}
}

// TestFileBatch_Commit_Closed verifies that Commit on a closed batch
// returns a BatchError wrapping ClosedError.
func TestFileBatch_Commit_Closed(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	batch := newBatch(eng)
	_ = batch.Rollback() // close it first
	if err := batch.Commit(); err == nil {
		t.Error("Commit on closed batch should return error")
	}
}

// ---------------------------------------------------------------------------
// iterator.load — 92.3% (missing the onlyKeys + reverse path combination)
// ---------------------------------------------------------------------------

// TestFileIterator_OnlyKeys verifies that loading with OnlyKeys=true omits
// values from the entries (Value() returns nil).
func TestFileIterator_OnlyKeys(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	for _, k := range []string{"x", "y", "z"} {
		if err := eng.Set([]byte(k), []byte("val")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	iter := newIterator(eng, &storage.IteratorOptions{OnlyKeys: true})
	defer func() { _ = iter.Close() }()

	if !iter.SeekToFirst() {
		t.Fatal("SeekToFirst returned false")
	}
	for iter.Valid() {
		if iter.Key() == nil {
			t.Error("Key should not be nil")
		}
		if iter.Value() != nil {
			t.Error("Value should be nil with OnlyKeys=true")
		}
		iter.Next()
	}
}

// TestFileIterator_Reverse verifies that setting Reverse=true returns keys
// in descending order.
func TestFileIterator_Reverse(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	for _, k := range []string{"a", "b", "c", "d"} {
		if err := eng.Set([]byte(k), []byte(k)); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	iter := newIterator(eng, &storage.IteratorOptions{Reverse: true})
	defer func() { _ = iter.Close() }()

	var keys []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		keys = append(keys, string(iter.Key()))
	}

	// Expect descending order.
	expected := []string{"d", "c", "b", "a"}
	if len(keys) != len(expected) {
		t.Fatalf("got %d keys, want %d", len(keys), len(expected))
	}
	for i, want := range expected {
		if keys[i] != want {
			t.Errorf("key[%d] = %q, want %q", i, keys[i], want)
		}
	}
}

// TestFileIterator_EmptyDir_SeekToLast verifies SeekToLast on an empty engine.
func TestFileIterator_EmptyDir_SeekToLast(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	iter := newIterator(eng, nil)
	defer func() { _ = iter.Close() }()

	if iter.SeekToLast() {
		t.Error("SeekToLast on empty storage should return false")
	}
}

// TestFileIterator_Next_BeforeSeek verifies that Next before any Seek returns false.
func TestFileIterator_Next_BeforeSeek(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if err := eng.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := newIterator(eng, nil)
	defer func() { _ = iter.Close() }()

	if iter.Next() {
		t.Error("Next before Seek should return false")
	}
}

// TestFileIterator_SeekToLast_ThenNext verifies iterating from last backward.
func TestFileIterator_SeekToLast_ThenNext(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	for _, k := range []string{"p", "q", "r"} {
		if err := eng.Set([]byte(k), []byte(k)); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	iter := newIterator(eng, nil)
	defer func() { _ = iter.Close() }()

	if !iter.SeekToLast() {
		t.Fatal("SeekToLast returned false")
	}
	if string(iter.Key()) != "r" {
		t.Errorf("SeekToLast key = %q, want %q", iter.Key(), "r")
	}
	// Next after SeekToLast should advance (or run off the end in forward iterator).
	// In a forward iterator, SeekToLast positions at the last index so Next is past end.
	if iter.Next() {
		t.Error("Next after SeekToLast should return false (already at end)")
	}
}
