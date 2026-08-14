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

package mvcc

import (
	"bytes"
	"errors"
	"testing"

	pebblestorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// collectChanges drains ScanChanges into a slice for assertions.
func collectChanges(t *testing.T, s *Store, startRev, endRev int64) []Change {
	t.Helper()
	var got []Change
	if err := s.ScanChanges(startRev, endRev, func(c Change) bool {
		got = append(got, c)
		return true
	}); err != nil {
		t.Fatalf("ScanChanges(%d,%d): %v", startRev, endRev, err)
	}
	return got
}

// ---------------------------------------------------------------------------
// Encoding round-trips
// ---------------------------------------------------------------------------

func TestRevIndexKey_RoundTrip(t *testing.T) {
	for _, tc := range []struct {
		rev int64
		key []byte
	}{
		{1, []byte("a")},
		{1 << 40, []byte("some/longer/key")},
		{7, []byte{}}, // empty key is legal in the encoding
	} {
		enc := EncodeRevIndexKey(tc.rev, tc.key)
		rev, key, err := DecodeRevIndexKey(enc)
		if err != nil {
			t.Fatalf("DecodeRevIndexKey(%d,%q): %v", tc.rev, tc.key, err)
		}
		if rev != tc.rev || !bytes.Equal(key, tc.key) {
			t.Errorf("round-trip = (%d,%q), want (%d,%q)", rev, key, tc.rev, tc.key)
		}
	}
}

func TestRevIndexKey_AscendingRevisionOrder(t *testing.T) {
	// Lower revision must sort before higher revision so a forward scan yields
	// revision order.
	lo := EncodeRevIndexKey(5, []byte("z"))
	hi := EncodeRevIndexKey(6, []byte("a"))
	if bytes.Compare(lo, hi) >= 0 {
		t.Errorf("rev 5 key must sort before rev 6 key, got compare >= 0")
	}
}

func TestChangeRecord_RoundTrip(t *testing.T) {
	rec := &ChangeRecord{EventType: changePut, CreateRevision: 3, Version: 4, LeaseID: 9}
	dec, err := DecodeChangeRecord(EncodeChangeRecord(rec))
	if err != nil {
		t.Fatalf("DecodeChangeRecord: %v", err)
	}
	if dec.EventType != changePut || dec.CreateRevision != 3 || dec.Version != 4 || dec.LeaseID != 9 {
		t.Errorf("round-trip = %+v, want %+v", dec, rec)
	}
	if _, err := DecodeChangeRecord([]byte{0x09}); err == nil {
		t.Error("DecodeChangeRecord accepted a short buffer")
	}
	if _, err := DecodeChangeRecord(EncodeChangeRecord(&ChangeRecord{EventType: 0x7f})); err == nil {
		t.Error("DecodeChangeRecord accepted an unknown event type")
	}
}

// ---------------------------------------------------------------------------
// ScanChanges
// ---------------------------------------------------------------------------

func TestScanChanges_PutsAndDeleteOrdered(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Put([]byte("a"), []byte("1"), 0); err != nil { // rev 1
		t.Fatal(err)
	}
	if _, err := s.Put([]byte("b"), []byte("2"), 0); err != nil { // rev 2
		t.Fatal(err)
	}
	if _, err := s.Delete([]byte("a")); err != nil { // rev 3
		t.Fatal(err)
	}

	got := collectChanges(t, s, 1, s.CurrentRevision())
	if len(got) != 3 {
		t.Fatalf("got %d changes, want 3: %+v", len(got), got)
	}
	// Ascending revision order.
	if got[0].Revision != 1 || string(got[0].Key) != "a" || got[0].IsDelete || string(got[0].Value) != "1" {
		t.Errorf("change[0] = %+v", got[0])
	}
	if got[1].Revision != 2 || string(got[1].Key) != "b" || string(got[1].Value) != "2" {
		t.Errorf("change[1] = %+v", got[1])
	}
	if got[2].Revision != 3 || string(got[2].Key) != "a" || !got[2].IsDelete || got[2].Value != nil {
		t.Errorf("change[2] = %+v (want delete of a at rev 3 with nil value)", got[2])
	}
}

func TestScanChanges_StartAndEndBounds(t *testing.T) {
	s := newTestStore(t)
	for _, k := range []string{"a", "b", "c", "d"} { // revs 1..4
		if _, err := s.Put([]byte(k), []byte(k), 0); err != nil {
			t.Fatal(err)
		}
	}
	// Start bound: only revs >= 3.
	got := collectChanges(t, s, 3, 100)
	if len(got) != 2 || got[0].Revision != 3 || got[1].Revision != 4 {
		t.Fatalf("start-bound scan = %+v, want revs [3,4]", got)
	}
	// End bound: only revs <= 2.
	got = collectChanges(t, s, 1, 2)
	if len(got) != 2 || got[0].Revision != 1 || got[1].Revision != 2 {
		t.Fatalf("end-bound scan = %+v, want revs [1,2]", got)
	}
	// Empty range.
	if got = collectChanges(t, s, 5, 2); len(got) != 0 {
		t.Fatalf("inverted range returned %d changes, want 0", len(got))
	}
}

func TestScanChanges_MultipleRevisionsSameKey(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 3; i++ { // revs 1,2,3 all on key "k"
		if _, err := s.Put([]byte("k"), []byte{byte('A' + i)}, 0); err != nil {
			t.Fatal(err)
		}
	}
	got := collectChanges(t, s, 1, s.CurrentRevision())
	if len(got) != 3 {
		t.Fatalf("got %d changes, want 3", len(got))
	}
	for i, c := range got {
		if c.Revision != int64(i+1) || string(c.Value) != string([]byte{byte('A' + i)}) {
			t.Errorf("change[%d] = %+v", i, c)
		}
		if c.Version != int64(i+1) { // version increments per modification
			t.Errorf("change[%d] version = %d, want %d", i, c.Version, i+1)
		}
	}
}

func TestScanChanges_EarlyStop(t *testing.T) {
	s := newTestStore(t)
	for _, k := range []string{"a", "b", "c"} {
		if _, err := s.Put([]byte(k), []byte(k), 0); err != nil {
			t.Fatal(err)
		}
	}
	var seen int
	if err := s.ScanChanges(1, 100, func(Change) bool {
		seen++
		return seen < 2 // stop after the second change
	}); err != nil {
		t.Fatal(err)
	}
	if seen != 2 {
		t.Errorf("early stop visited %d changes, want 2", seen)
	}
}

func TestScanChanges_TxnSharedRevisionOrdersByKey(t *testing.T) {
	// A single Raft entry assigns ONE revision to every key it mutates; the
	// index disambiguates by key and replays them together, ordered by key.
	s := newTestStore(t)
	batch := s.Storage().Batch()
	if _, err := s.PutInBatch(batch, []byte("b"), []byte("2"), 0, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutInBatch(batch, []byte("a"), []byte("1"), 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = batch.Close()
	s.SetRevision(1)

	got := collectChanges(t, s, 1, 1)
	if len(got) != 2 {
		t.Fatalf("got %d changes, want 2", len(got))
	}
	if string(got[0].Key) != "a" || string(got[1].Key) != "b" {
		t.Errorf("changes at shared revision not key-ordered: %q, %q", got[0].Key, got[1].Key)
	}
	if got[0].Revision != 1 || got[1].Revision != 1 {
		t.Errorf("both changes must share revision 1, got %d and %d", got[0].Revision, got[1].Revision)
	}
}

func TestScanChanges_PutWithCompactedValueFailsClosed(t *testing.T) {
	// M1 regression. If a concurrent Compact removes a put's data entry between
	// the (snapshot) rev-index iterator and the fresh value read, ScanChanges
	// must fail closed with a compaction error rather than yield a put with a
	// silently empty value. Reproduce the inconsistent state directly: keep the
	// rev-index entry but drop the data entry.
	s := newTestStore(t)
	if _, err := s.Put([]byte("k"), []byte("v"), 0); err != nil { // rev 1
		t.Fatal(err)
	}
	batch := s.Storage().Batch()
	if err := batch.Delete(EncodeDataKey([]byte("k"), 1)); err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = batch.Close()

	err := s.ScanChanges(1, 1, func(Change) bool { return true })
	var rce *RevisionCompactedError
	if !errors.As(err, &rce) {
		t.Fatalf("ScanChanges over a put whose value was compacted away = %v, want RevisionCompactedError", err)
	}
}

// ---------------------------------------------------------------------------
// Compaction GC
// ---------------------------------------------------------------------------

func TestCompact_DropsRevIndexAtOrBelowFloor(t *testing.T) {
	s := newTestStore(t)
	for _, k := range []string{"a", "b", "c", "d"} { // revs 1..4
		if _, err := s.Put([]byte(k), []byte(k), 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Compact(2); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Changes at or below the compaction floor are gone; 3 and 4 remain.
	got := collectChanges(t, s, 1, 100)
	if len(got) != 2 || got[0].Revision != 3 || got[1].Revision != 4 {
		t.Fatalf("post-compaction scan = %+v, want revs [3,4]", got)
	}
}

// ---------------------------------------------------------------------------
// Durability: the whole point of a storage-backed index
// ---------------------------------------------------------------------------

func TestScanChanges_SurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	store, err := pebblestorage.New(pebblestorage.TestConfig(dir))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	s, err := New(store)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := s.Put([]byte("a"), []byte("1"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put([]byte("b"), []byte("2"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	store2, err := pebblestorage.New(pebblestorage.TestConfig(dir))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = store2.Close() })
	s2, err := New(store2)
	if err != nil {
		t.Fatalf("new store 2: %v", err)
	}

	got := collectChanges(t, s2, 1, s2.CurrentRevision())
	if len(got) != 3 {
		t.Fatalf("post-reopen scan = %d changes, want 3 (index must be durable): %+v", len(got), got)
	}
	if got[2].Revision != 3 || !got[2].IsDelete {
		t.Errorf("post-reopen final change = %+v, want delete at rev 3", got[2])
	}
}
