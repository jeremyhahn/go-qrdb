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

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

func TestCompactionFloors_ForKey(t *testing.T) {
	f := emptyFloors.with([]byte("acme/app/"), 10).with([]byte("acme/"), 4).with([]byte("beta/app/"), 20)
	tests := []struct {
		key  string
		want int64
	}{
		{"acme/app/k", 10},  // the deepest covering floor is not the only one; the highest wins
		{"acme/other/k", 4}, // only the tenant-wide floor covers it
		{"beta/app/k", 20},
		{"beta/other/k", 0},
		{"acme", 0}, // shorter than every prefix that could cover it
		{"", 0},
	}
	for _, tt := range tests {
		if got := f.forKey([]byte(tt.key)); got != tt.want {
			t.Errorf("forKey(%q) = %d, want %d", tt.key, got, tt.want)
		}
	}
	if got := emptyFloors.with(nil, 7).forKey([]byte("anything")); got != 7 {
		t.Errorf("the empty prefix covers every key; forKey = %d, want 7", got)
	}
}

func TestCompactionFloors_ForRange(t *testing.T) {
	f := emptyFloors.with([]byte("acme/app/"), 10).with([]byte("beta/app/"), 20)
	tests := []struct {
		name       string
		start, end []byte
		want       int64
	}{
		{"inside one database", []byte("acme/app/"), []byte("acme/app0"), 10},
		{"another database", []byte("gamma/"), []byte("gamma0"), 0},
		{"spanning both", []byte("a"), []byte("c"), 20},
		{"ends where acme's begins", []byte("a"), []byte("acme/app/"), 0},
		{"starts where acme's ends", []byte("acme/app0"), []byte("b"), 0},
		{"unbounded above from before beta", []byte("b"), nil, 20},
	}
	for _, tt := range tests {
		if got := f.forRange(tt.start, tt.end); got != tt.want {
			t.Errorf("%s: forRange(%q, %q) = %d, want %d", tt.name, tt.start, tt.end, got, tt.want)
		}
	}
}

func TestCompactionFloors_WithDoesNotMutateTheSnapshot(t *testing.T) {
	a := emptyFloors.with([]byte("p/"), 3)
	b := a.with([]byte("q/"), 5)
	if a.forKey([]byte("q/k")) != 0 {
		t.Fatal("with mutated the snapshot it was called on")
	}
	if b.forKey([]byte("p/k")) != 3 || b.forKey([]byte("q/k")) != 5 {
		t.Fatal("with lost a floor")
	}
	if len(emptyFloors.byPrefix) != 0 {
		t.Fatal("emptyFloors was mutated")
	}
}

func TestCompactionFloorKey_RoundTrip(t *testing.T) {
	for _, p := range [][]byte{nil, []byte("acme/app/")} {
		got, err := DecodeCompactionFloorKey(CompactionFloorKey(p))
		if err != nil || !bytes.Equal(got, p) {
			t.Fatalf("round trip of %q = %q, %v", p, got, err)
		}
	}
	if _, err := DecodeCompactionFloorKey([]byte{prefixData, 'x'}); err == nil {
		t.Fatal("a key outside the floor namespace decoded")
	}
	if _, err := DecodeCompactionFloorKey(nil); err == nil {
		t.Fatal("an empty key decoded")
	}
}

func TestNew_RefusesACorruptFloor(t *testing.T) {
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Set(CompactionFloorKey([]byte("p/")), []byte{1, 2}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := New(store); err == nil {
		t.Fatal("New loaded a floor that does not decode")
	}
}

// TestRange_SingleKeyAndRangeUseTheirOwnFloors pins Range's floor selection:
// the single-key form checks the key's floor, the range form the floors it
// overlaps.
func TestRange_SingleKeyAndRangeUseTheirOwnFloors(t *testing.T) {
	s := newTestStore(t)
	for _, k := range []string{"a/k", "b/k", "a/k", "b/k"} {
		if _, err := s.Put([]byte(k), []byte("v"), 0); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := s.CompactPrefix([]byte("a/"), 3); err != nil {
		t.Fatalf("CompactPrefix: %v", err)
	}
	var compacted *RevisionCompactedError
	if _, _, _, err := s.Range([]byte("a/k"), nil, service.WithRevision(2)); !errors.As(err, &compacted) {
		t.Fatalf("single-key Range below a/'s floor = %v", err)
	}
	if _, _, _, err := s.Range([]byte("b/k"), nil, service.WithRevision(2)); err != nil {
		t.Fatalf("single-key Range of b/ below a/'s floor = %v", err)
	}
	if kvs, _, _, err := s.Range([]byte("b/"), []byte("b0"), service.WithRevision(2)); err != nil || len(kvs) != 1 {
		t.Fatalf("b/'s range below a/'s floor = %v, %v", kvs, err)
	}
	if _, _, _, err := s.Range([]byte("a/"), []byte("b0"), service.WithRevision(2)); !errors.As(err, &compacted) {
		t.Fatalf("a range overlapping a/ below its floor = %v", err)
	}
	if got := s.CompactedRevisionForPrefix([]byte("a/")); got != 3 {
		t.Fatalf("CompactedRevisionForPrefix(a/) = %d, want 3", got)
	}
	if got := s.CompactedRevisionForPrefix(nil); got != 3 {
		t.Fatalf("CompactedRevisionForPrefix(everything) = %d, want 3", got)
	}
}

func TestCompactPrefixInBatch_IsAtomicWithTheBatch(t *testing.T) {
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	s, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Put([]byte("p/k"), []byte{byte(i)}, 0); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	batch := store.Batch()
	if err := s.CompactPrefixInBatch(batch, []byte("p/"), 2); err != nil {
		t.Fatalf("CompactPrefixInBatch: %v", err)
	}
	// The data entries are not deleted until the batch commits.
	if raw, _ := store.Get(EncodeDataKey([]byte("p/k"), 1)); raw == nil {
		t.Fatal("the compaction reached storage before its batch committed")
	}
	if err := batch.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	_ = batch.Close()
	if raw, _ := store.Get(EncodeDataKey([]byte("p/k"), 1)); raw != nil {
		t.Fatal("the committed compaction left the old entry")
	}
	if v, _ := store.Get(CompactionFloorKey([]byte("p/"))); v == nil {
		t.Fatal("the floor was not persisted with the batch")
	}
}
