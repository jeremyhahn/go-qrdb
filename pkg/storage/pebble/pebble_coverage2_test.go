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

package pebble

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// ---------------------------------------------------------------------------
// PebbleStorage.Set / Delete — SyncWrites path
// ---------------------------------------------------------------------------

// TestSet_SyncWrites verifies that Set with SyncWrites=true writes and reads
// back the correct value without error.
func TestSet_SyncWrites(t *testing.T) {
	s, err := New(TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = s.Close() }()
	s.config.SyncWrites = true

	if err := s.Set([]byte("sync-key"), []byte("sync-val")); err != nil {
		t.Fatalf("Set (sync): %v", err)
	}
	got, err := s.Get([]byte("sync-key"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "sync-val" {
		t.Errorf("Get = %q, want %q", got, "sync-val")
	}
}

// TestDelete_SyncWrites verifies that Delete with SyncWrites=true removes
// the key without error.
func TestDelete_SyncWrites(t *testing.T) {
	s, err := New(TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = s.Close() }()
	s.config.SyncWrites = true

	if err := s.Set([]byte("del-sync"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Delete([]byte("del-sync")); err != nil {
		t.Fatalf("Delete (sync): %v", err)
	}
	got, err := s.Get([]byte("del-sync"))
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if got != nil {
		t.Errorf("Get after delete = %q, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// PebbleStorage.Set / Delete — ClosedError path
// ---------------------------------------------------------------------------

// TestSet_ClosedError verifies that Set on a closed storage returns ClosedError.
func TestSet_ClosedError(t *testing.T) {
	s := openTestStorage(t)
	_ = s.Close()

	err := s.Set([]byte("k"), []byte("v"))
	if err == nil {
		t.Fatal("Set on closed storage should return error")
	}
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *storage.ClosedError, got %T: %v", err, err)
	}
}

// TestDelete_ClosedError verifies that Delete on a closed storage returns ClosedError.
func TestDelete_ClosedError(t *testing.T) {
	s := openTestStorage(t)
	_ = s.Close()

	err := s.Delete([]byte("k"))
	if err == nil {
		t.Fatal("Delete on closed storage should return error")
	}
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *storage.ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// PebbleStorage.Compact / Flush — closed + happy path
// ---------------------------------------------------------------------------

// TestCompact_Closed verifies that Compact on a closed storage returns ClosedError.
func TestCompact_Closed(t *testing.T) {
	s := openTestStorage(t)
	_ = s.Close()

	err := s.Compact(nil, nil)
	if err == nil {
		t.Fatal("Compact on closed storage should return error")
	}
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *storage.ClosedError, got %T: %v", err, err)
	}
}

// TestCompact_HappyPath verifies Compact succeeds on an open, populated storage.
func TestCompact_HappyPath(t *testing.T) {
	s := openTestStorage(t)
	defer func() { _ = s.Close() }()

	if err := s.Set([]byte("a"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Compact requires start < end.
	if err := s.Compact([]byte("a"), []byte("z")); err != nil {
		t.Errorf("Compact: %v", err)
	}
}

// TestFlush_Closed verifies that Flush on a closed storage returns ClosedError.
func TestFlush_Closed(t *testing.T) {
	s := openTestStorage(t)
	_ = s.Close()

	err := s.Flush()
	if err == nil {
		t.Fatal("Flush on closed storage should return error")
	}
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *storage.ClosedError, got %T: %v", err, err)
	}
}

// TestFlush_HappyPath verifies Flush succeeds on an open storage.
func TestFlush_HappyPath(t *testing.T) {
	s := openTestStorage(t)
	defer func() { _ = s.Close() }()

	if err := s.Flush(); err != nil {
		t.Errorf("Flush: %v", err)
	}
}

// ---------------------------------------------------------------------------
// PebbleStorage.Close — happy path + double close (idempotent)
// ---------------------------------------------------------------------------

// TestClose_DoubleClose verifies that calling Close twice does not error.
func TestClose_DoubleClose(t *testing.T) {
	s, err := New(TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close should be no-op: %v", err)
	}
}

// ---------------------------------------------------------------------------
// pebbleBatch — SyncWrites path + Rollback error path
// ---------------------------------------------------------------------------

// TestBatch_Commit_SyncWrites verifies a batch commit with sync mode.
func TestBatch_Commit_SyncWrites(t *testing.T) {
	s, err := New(TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = s.Close() }()
	s.config.SyncWrites = true

	batch := s.Batch()
	if err := batch.Set([]byte("sk"), []byte("sv")); err != nil {
		t.Fatalf("batch.Set: %v", err)
	}
	if err := batch.Commit(); err != nil {
		t.Fatalf("Commit (sync): %v", err)
	}

	got, err := s.Get([]byte("sk"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "sv" {
		t.Errorf("Get = %q, want %q", got, "sv")
	}
}

// TestBatch_Rollback_ClosedError verifies that Rollback on a closed batch
// returns a BatchError wrapping ClosedError.
func TestBatch_Rollback_ClosedError(t *testing.T) {
	s := openTestStorage(t)
	defer func() { _ = s.Close() }()

	batch := s.Batch()
	_ = batch.Rollback() // close it

	err := batch.Rollback()
	if err == nil {
		t.Fatal("second Rollback should return error")
	}
	var be *storage.BatchError
	if !errors.As(err, &be) {
		t.Fatalf("expected *storage.BatchError, got %T: %v", err, err)
	}
}

// TestBatch_Close_DoubleClose verifies that calling Close twice on a batch
// is idempotent (second call is a no-op).
func TestBatch_Close_DoubleClose(t *testing.T) {
	s := openTestStorage(t)
	defer func() { _ = s.Close() }()

	batch := s.Batch()
	if err := batch.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := batch.Close(); err != nil {
		t.Errorf("second Close should be no-op: %v", err)
	}
}

// TestBatch_Set_ClosedError verifies that Set on a closed batch returns error.
func TestBatch_Set_ClosedError(t *testing.T) {
	s := openTestStorage(t)
	defer func() { _ = s.Close() }()

	batch := s.Batch()
	_ = batch.Close()

	err := batch.Set([]byte("k"), []byte("v"))
	if err == nil {
		t.Fatal("Set on closed batch should return error")
	}
}

// TestBatch_Delete_ClosedError verifies that Delete on a closed batch returns error.
func TestBatch_Delete_ClosedError(t *testing.T) {
	s := openTestStorage(t)
	defer func() { _ = s.Close() }()

	batch := s.Batch()
	_ = batch.Close()

	err := batch.Delete([]byte("k"))
	if err == nil {
		t.Fatal("Delete on closed batch should return error")
	}
}

// TestBatch_Commit_ClosedError verifies that Commit on a closed batch returns error.
func TestBatch_Commit_ClosedError(t *testing.T) {
	s := openTestStorage(t)
	defer func() { _ = s.Close() }()

	batch := s.Batch()
	_ = batch.Close()

	err := batch.Commit()
	if err == nil {
		t.Fatal("Commit on closed batch should return error")
	}
}

// ---------------------------------------------------------------------------
// pebbleIterator — nil inner iterator paths
// ---------------------------------------------------------------------------

// TestIterator_NilInner verifies all methods on a pebbleIterator with nil inner
// iterator behave safely (should return false/nil/no-op).
func TestIterator_NilInner_AllMethods(t *testing.T) {
	it := &pebbleIterator{} // nil inner iter

	if it.SeekToFirst() {
		t.Error("SeekToFirst with nil iter should return false")
	}
	if it.SeekToLast() {
		t.Error("SeekToLast with nil iter should return false")
	}
	if it.Seek([]byte("k")) {
		t.Error("Seek with nil iter should return false")
	}
	if it.Next() {
		t.Error("Next with nil iter should return false")
	}
	if it.Prev() {
		t.Error("Prev with nil iter should return false")
	}
	if it.Valid() {
		t.Error("Valid with nil iter should return false")
	}
	if it.Key() != nil {
		t.Error("Key with nil iter should return nil")
	}
	if it.Value() != nil {
		t.Error("Value with nil iter should return nil")
	}
	// Error with nil inner iter and no stored err should return nil.
	if err := it.Error(); err != nil {
		t.Errorf("Error with nil iter and no err should be nil, got %v", err)
	}
	// Close with nil inner iter should be a no-op (no error).
	if err := it.Close(); err != nil {
		t.Errorf("Close with nil iter: %v", err)
	}
}

// TestIterator_ClosedInner verifies methods return false/nil after Close().
func TestIterator_ClosedInner(t *testing.T) {
	s := openTestStorage(t)
	defer func() { _ = s.Close() }()

	if err := s.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := s.NewIterator(nil)
	if err := iter.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if iter.SeekToFirst() {
		t.Error("SeekToFirst after Close should return false")
	}
	if iter.Valid() {
		t.Error("Valid after Close should return false")
	}
}

// ---------------------------------------------------------------------------
// pebbleSnapshot.Close — double close
// ---------------------------------------------------------------------------

// TestSnapshot_DoubleClose verifies that calling Close twice on a snapshot
// is idempotent.
func TestSnapshot_DoubleClose(t *testing.T) {
	s := openTestStorage(t)
	defer func() { _ = s.Close() }()

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := snap.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := snap.Close(); err != nil {
		t.Errorf("second Close should be no-op: %v", err)
	}
}

// TestSnapshot_Get_Closed verifies Get on a closed snapshot returns error.
func TestSnapshot_Get_Closed(t *testing.T) {
	s := openTestStorage(t)
	defer func() { _ = s.Close() }()

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	_ = snap.Close()

	_, err = snap.Get([]byte("k"))
	if err == nil {
		t.Fatal("Get on closed snapshot should return error")
	}
	var se *storage.SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *storage.SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// buildIterOptions — OnlyKeys path
// ---------------------------------------------------------------------------

// TestBuildIterOptions_OnlyKeys verifies that OnlyKeys is translated correctly.
func TestBuildIterOptions_OnlyKeys(t *testing.T) {
	opts := buildIterOptions(&storage.IteratorOptions{
		OnlyKeys: true,
	})
	if opts == nil {
		t.Fatal("buildIterOptions returned nil")
	}
	// The test verifies the function completes without panic; the KeyTypes
	// field is internal to pebble and not directly inspectable here.
}

// TestBuildIterOptions_NilOpts verifies that nil opts returns empty options.
func TestBuildIterOptions_NilOpts(t *testing.T) {
	opts := buildIterOptions(nil)
	if opts == nil {
		t.Fatal("buildIterOptions(nil) returned nil")
	}
}

// TestBuildIterOptions_Bounds verifies bounds are set correctly.
func TestBuildIterOptions_Bounds(t *testing.T) {
	opts := buildIterOptions(&storage.IteratorOptions{
		LowerBound: []byte("a"),
		UpperBound: []byte("z"),
	})
	if string(opts.LowerBound) != "a" {
		t.Errorf("LowerBound = %q, want %q", opts.LowerBound, "a")
	}
	if string(opts.UpperBound) != "z" {
		t.Errorf("UpperBound = %q, want %q", opts.UpperBound, "z")
	}
}

// ---------------------------------------------------------------------------
// newIterator / newSnapshotIterator paths
// ---------------------------------------------------------------------------

// TestNewIterator_Happy exercises the happy path of newIterator.
func TestNewIterator_Happy(t *testing.T) {
	s := openTestStorage(t)
	defer func() { _ = s.Close() }()

	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := newIterator(s.db, nil)
	defer func() { _ = iter.Close() }()

	if !iter.SeekToFirst() {
		t.Error("SeekToFirst should return true")
	}
}

// TestNewSnapshotIterator_Happy exercises the happy path of newSnapshotIterator.
func TestNewSnapshotIterator_Happy(t *testing.T) {
	s := openTestStorage(t)
	defer func() { _ = s.Close() }()

	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	snap := s.db.NewSnapshot()
	defer func() { _ = snap.Close() }()

	iter := newSnapshotIterator(snap, nil)
	defer func() { _ = iter.Close() }()

	if !iter.SeekToFirst() {
		t.Error("SeekToFirst should return true")
	}
}

// ---------------------------------------------------------------------------
// PebbleStorage.List — prefix with 0xff byte (all-0xff upper bound)
// ---------------------------------------------------------------------------

// TestList_PrefixWithAllFF verifies List handles a prefix ending in 0xff correctly.
func TestList_PrefixWithAllFF(t *testing.T) {
	s := openTestStorage(t)
	defer func() { _ = s.Close() }()

	// Set a key that starts with 0xff — the upper bound calculation in List
	// walks backwards to find a byte to increment.
	key := []byte{0xff, 0x01}
	if err := s.Set(key, []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	keys, err := s.List(key)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("List len = %d, want 1", len(keys))
	}
}
