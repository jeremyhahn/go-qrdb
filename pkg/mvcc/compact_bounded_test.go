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
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// recordingBatch is a storage.Batch that counts the keys its iterators visit
// and records whether any iterator was opened over the change log.
type recordingBatch struct {
	storage.Batch
	visited      int
	changeLogged bool
}

func (b *recordingBatch) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	if opts != nil && len(opts.LowerBound) > 0 && opts.LowerBound[0] == prefixRevIndex {
		b.changeLogged = true
	}
	return &recordingIterator{Iterator: b.Batch.NewIterator(opts), b: b}
}

type recordingIterator struct {
	storage.Iterator
	b *recordingBatch
}

func (it *recordingIterator) Valid() bool {
	ok := it.Iterator.Valid()
	if ok {
		it.b.visited++
	}
	return ok
}

// TestCompactPrefix_WorkIsBoundedByTheCompactedDatabase: a database's
// compaction -- data and change log -- costs the entries under its own
// prefix, however much history other databases on the store hold. It never
// walks the revision window, which holds every database's changes.
func TestCompactPrefix_WorkIsBoundedByTheCompactedDatabase(t *testing.T) {
	s := newTestStore(t)
	const otherWrites = 2000
	for i := range otherWrites {
		if _, err := s.Put([]byte(fmt.Sprintf("beta/app/k%04d", i%50)), []byte("b"), 0); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	for i := range 6 {
		if _, err := s.Put([]byte("acme/app/k"), []byte(fmt.Sprintf("a%d", i)), 0); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	cur := s.CurrentRevision()

	b := &recordingBatch{Batch: s.storage.Batch()}
	defer func() { _ = b.Close() }()
	if err := s.CompactPrefixInBatch(b, []byte("acme/app/"), cur); err != nil {
		t.Fatalf("CompactPrefixInBatch: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if b.changeLogged {
		t.Fatal("the compaction walked the change log's revision window, which holds every database's changes")
	}
	// acme's 6 entries plus at most a seek landing per key length.
	if b.visited > 16 {
		t.Fatalf("compacting acme/app/ visited %d keys; it holds 6 entries beside %d of another database's", b.visited, otherWrites)
	}

	// And the collection is complete: acme's change log at or below the floor
	// is gone, beta's is whole.
	var acme, beta int
	if err := s.ScanChanges(1, cur, nil, func(c Change) bool {
		switch string(c.Key[:5]) {
		case "acme/":
			acme++
		case "beta/":
			beta++
		}
		return true
	}); err != nil {
		t.Fatalf("ScanChanges: %v", err)
	}
	if acme != 0 || beta != otherWrites {
		t.Fatalf("change log after compacting acme: acme %d (want 0), beta %d (want %d)", acme, beta, otherWrites)
	}
	if kv, err := s.Get([]byte("acme/app/k")); err != nil || string(kv.Value) != "a5" {
		t.Fatalf("acme's current value = %v, %v", kv, err)
	}
	if _, _, _, err := s.Range([]byte("beta/app/"), []byte("beta/app0"), service.WithRevision(1)); err != nil {
		t.Fatalf("beta's history at 1 = %v", err)
	}
}

// TestScanChanges_AnotherDatabasesCompactionDoesNotFailTheScan: a change scan
// of one database -- the engine's MVCCScanChangesQuery, a watch replay --
// reads nothing of another's. That database compacting mid-scan removes
// entries the scan walks past in the change log, and must not fail it.
func TestScanChanges_AnotherDatabasesCompactionDoesNotFailTheScan(t *testing.T) {
	s := newTestStore(t)
	for i := range 10 {
		for _, k := range []string{"acme/app/k", "beta/app/k"} {
			if _, err := s.Put([]byte(k), []byte{byte('a' + i)}, 0); err != nil {
				t.Fatalf("Put: %v", err)
			}
		}
	}
	cur := s.CurrentRevision()
	inAcme := func(k []byte) bool { return string(k[:len("acme/app/")]) == "acme/app/" }

	seen, compacted := 0, false
	err := s.ScanChanges(1, cur, inAcme, func(c Change) bool {
		if string(c.Key) != "acme/app/k" {
			t.Fatalf("the scan yielded another database's key %q", c.Key)
		}
		seen++
		if !compacted {
			if cerr := s.CompactPrefix([]byte("beta/app/"), cur); cerr != nil {
				t.Fatalf("CompactPrefix(beta): %v", cerr)
			}
			compacted = true
		}
		return true
	})
	if err != nil || seen != 10 {
		t.Fatalf("acme's scan after beta compacted = %d changes, %v; want all 10", seen, err)
	}
}

// TestCompaction_ChangeLogCollectedOncePerWindow: a second compaction
// collects only the change log of (previous floor, revision] -- the kept
// entry's record went with the first -- and a key's change log above the
// floor stays.
func TestCompaction_ChangeLogCollectedOncePerWindow(t *testing.T) {
	s := newTestStore(t)
	for _, v := range []string{"v1", "v2", "v3", "v4"} {
		if _, err := s.Put([]byte("acme/app/k"), []byte(v), 0); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := s.CompactPrefix([]byte("acme/app/"), 2); err != nil {
		t.Fatalf("CompactPrefix 2: %v", err)
	}
	revs := func() []int64 {
		var out []int64
		if err := s.ScanChanges(1, s.CurrentRevision(), nil, func(c Change) bool {
			out = append(out, c.Revision)
			return true
		}); err != nil {
			t.Fatalf("ScanChanges: %v", err)
		}
		return out
	}
	if got := revs(); fmt.Sprint(got) != "[3 4]" {
		t.Fatalf("change log after compacting to 2 = %v, want [3 4]", got)
	}
	if err := s.CompactPrefix([]byte("acme/app/"), 3); err != nil {
		t.Fatalf("CompactPrefix 3: %v", err)
	}
	if got := revs(); fmt.Sprint(got) != "[4]" {
		t.Fatalf("change log after compacting to 3 = %v, want [4]", got)
	}
}
