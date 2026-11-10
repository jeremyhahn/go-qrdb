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
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// ---------------------------------------------------------------------------
// Batch reads
//
// storage.Batch promises read-your-own-writes, which is what lets a Raft apply
// loop pack a variable number of entries into one batch without the packing
// showing up in the resulting state. PebbleDB supplies the semantics natively
// through an indexed batch; these tests pin that the wrapper uses one and maps
// its outcomes onto the storage contract.
// ---------------------------------------------------------------------------

// TestBatch_GetSeesPendingWrite requires a buffered write to be readable from
// the batch before it commits.
func TestBatch_GetSeesPendingWrite(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	defer func() { _ = b.Close() }()

	if err := b.Set([]byte("pending"), []byte("value")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := b.Get([]byte("pending"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "value" {
		t.Fatalf("Get = %q, want %q", got, "value")
	}

	// The database itself must not see it yet.
	committed, err := s.Get([]byte("pending"))
	if err != nil {
		t.Fatalf("storage Get: %v", err)
	}
	if committed != nil {
		t.Fatalf("uncommitted write leaked into the database: %q", committed)
	}
}

// TestBatch_GetShadowsCommittedValue requires a pending write to take
// precedence over the committed one.
func TestBatch_GetShadowsCommittedValue(t *testing.T) {
	s := openTestStorage(t)
	if err := s.Set([]byte("k"), []byte("committed")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	b := s.Batch()
	defer func() { _ = b.Close() }()

	if err := b.Set([]byte("k"), []byte("pending")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := b.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "pending" {
		t.Fatalf("Get = %q, want %q", got, "pending")
	}
}

// TestBatch_GetFallsThroughToDatabase requires an untouched key to read its
// committed value through the batch.
func TestBatch_GetFallsThroughToDatabase(t *testing.T) {
	s := openTestStorage(t)
	if err := s.Set([]byte("k"), []byte("committed")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	b := s.Batch()
	defer func() { _ = b.Close() }()

	got, err := b.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "committed" {
		t.Fatalf("Get = %q, want %q", got, "committed")
	}
}

// TestBatch_GetHidesPendingDelete requires a buffered delete to make the key
// read as absent.
func TestBatch_GetHidesPendingDelete(t *testing.T) {
	s := openTestStorage(t)
	if err := s.Set([]byte("k"), []byte("committed")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	b := s.Batch()
	defer func() { _ = b.Close() }()

	if err := b.Delete([]byte("k")); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, err := b.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("Get after pending delete = %q, want nil", got)
	}
}

// TestBatch_GetMissingKey requires an absent key to read as nil, nil rather
// than as an error.
func TestBatch_GetMissingKey(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	defer func() { _ = b.Close() }()

	got, err := b.Get([]byte("nowhere"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("Get = %q, want nil", got)
	}
}

// TestBatch_GetEmptyKey requires the empty key to be refused.
func TestBatch_GetEmptyKey(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	defer func() { _ = b.Close() }()

	_, err := b.Get(nil)
	if !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Get(nil) = %v, want ErrEmptyKey", err)
	}
}

// TestBatch_GetAfterClose requires a read on a closed batch to fail rather
// than serve a value.
func TestBatch_GetAfterClose(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := b.Get([]byte("k"))
	if err == nil {
		t.Fatal("Get on a closed batch returned no error")
	}
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Get error = %T (%v), want a wrapped *storage.ClosedError", err, err)
	}
}

// TestBatch_GetReturnsDefensiveCopy requires the returned bytes not to alias
// PebbleDB's internal buffers.
func TestBatch_GetReturnsDefensiveCopy(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	defer func() { _ = b.Close() }()

	if err := b.Set([]byte("k"), []byte("original")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	first, err := b.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	first[0] = 'X'

	second, err := b.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(second) != "original" {
		t.Fatalf("Get returned an aliased slice: %q", second)
	}
}

// TestBatch_IteratorMergesPendingWrites requires a scan through the batch to
// interleave its pending writes with the committed keys.
func TestBatch_IteratorMergesPendingWrites(t *testing.T) {
	s := openTestStorage(t)
	for _, k := range []string{"a", "c", "e"} {
		if err := s.Set([]byte(k), []byte("committed-"+k)); err != nil {
			t.Fatalf("Set %s: %v", k, err)
		}
	}

	b := s.Batch()
	defer func() { _ = b.Close() }()

	for _, k := range []string{"b", "d"} {
		if err := b.Set([]byte(k), []byte("pending-"+k)); err != nil {
			t.Fatalf("Set %s: %v", k, err)
		}
	}
	if err := b.Delete([]byte("e")); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	it := b.NewIterator(nil)
	defer func() { _ = it.Close() }()

	var pairs []string
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		pairs = append(pairs, string(it.Key())+"="+string(it.Value()))
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterate: %v", err)
	}

	want := "[a=committed-a b=pending-b c=committed-c d=pending-d]"
	if got := fmt.Sprint(pairs); got != want {
		t.Fatalf("merged scan = %s, want %s", got, want)
	}
}

// TestBatch_IteratorHonoursBounds requires the batch iterator to apply the
// caller's range to both the pending and the committed side.
func TestBatch_IteratorHonoursBounds(t *testing.T) {
	s := openTestStorage(t)
	if err := s.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Set([]byte("m"), []byte("2")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	b := s.Batch()
	defer func() { _ = b.Close() }()

	for _, k := range []string{"b", "z"} {
		if err := b.Set([]byte(k), []byte("pending")); err != nil {
			t.Fatalf("Set %s: %v", k, err)
		}
	}

	it := b.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte("b"),
		UpperBound: []byte("n"),
	})
	defer func() { _ = it.Close() }()

	var keys []string
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		keys = append(keys, string(it.Key()))
	}
	want := "[b m]"
	if got := fmt.Sprint(keys); got != want {
		t.Fatalf("bounded scan = %s, want %s", got, want)
	}
}

// TestBatch_IteratorAfterClose requires an iterator requested from a closed
// batch to be invalid and to report why.
func TestBatch_IteratorAfterClose(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	it := b.NewIterator(nil)
	defer func() { _ = it.Close() }()

	if it.SeekToFirst() {
		t.Error("iterator on a closed batch positioned")
	}
	if it.Error() == nil {
		t.Error("iterator on a closed batch reported no error")
	}
}

// TestBatch_IteratorAfterStoreClose verifies an open batch whose store has
// closed hands out an iterator that never positions and reports a
// BatchError wrapping ClosedError.
func TestBatch_IteratorAfterStoreClose(t *testing.T) {
	s := openTestStorage(t)
	b := s.Batch()
	defer func() { _ = b.Close() }()
	if err := b.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("batch Set: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	it := b.NewIterator(nil)
	defer func() { _ = it.Close() }()
	if it.SeekToFirst() {
		t.Error("batch iterator after store close positioned")
	}
	err := it.Error()
	var batchErr *storage.BatchError
	if !errors.As(err, &batchErr) || batchErr.Op != "iterate" {
		t.Fatalf("Error() = %v, want *storage.BatchError{Op: iterate}", err)
	}
	var closed *storage.ClosedError
	if !errors.As(err, &closed) {
		t.Errorf("Error() = %v, want a wrapped ClosedError", err)
	}
}
