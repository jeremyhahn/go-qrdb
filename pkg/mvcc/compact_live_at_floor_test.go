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
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// liveAtFloorFixture writes, at consecutive revisions starting at 1:
//
//	rev 1  k = v1
//	rev 2  other = o1   (an unrelated key, so revisions interleave)
//	rev 3  k = v3
//	rev 4  gone = g1
//	rev 5  k = v5
//	rev 6  gone deleted (tombstone)
//	rev 7  gone = g7
//	rev 8  dead = d8
//	rev 9  dead deleted (tombstone, its newest entry)
//
// every key under prefix. Compacting to any floor must keep, per key, the
// entry live at the floor -- compacting to 3 or 4 keeps k's v3 though v5 is
// newer; compacting to 6 drops gone's tombstone, live at 6, because g7 is
// newer -- so every read at a revision above the floor answers exactly as it
// did before.
func liveAtFloorFixture(t *testing.T, s *Store, prefix string) {
	t.Helper()
	k, other, gone, dead := []byte(prefix+"k"), []byte(prefix+"other"), []byte(prefix+"gone"), []byte(prefix+"dead")
	steps := []func() error{
		func() error { _, err := s.Put(k, []byte("v1"), 0); return err },
		func() error { _, err := s.Put(other, []byte("o1"), 0); return err },
		func() error { _, err := s.Put(k, []byte("v3"), 0); return err },
		func() error { _, err := s.Put(gone, []byte("g1"), 0); return err },
		func() error { _, err := s.Put(k, []byte("v5"), 0); return err },
		func() error { _, err := s.Delete(gone); return err },
		func() error { _, err := s.Put(gone, []byte("g7"), 0); return err },
		func() error { _, err := s.Put(dead, []byte("d8"), 0); return err },
		func() error { _, err := s.Delete(dead); return err },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i+1, err)
		}
	}
	if got := s.CurrentRevision(); got != 9 {
		t.Fatalf("fixture current revision = %d, want 9", got)
	}
}

// readsAt is every as-of answer the fixture's keys give at rev: the value per
// key, or "" when the key does not exist at rev.
func readsAt(t *testing.T, s *Store, prefix string, rev int64) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, name := range []string{"k", "other", "gone", "dead"} {
		kv, err := s.GetAtRevision([]byte(prefix+name), rev)
		var nf *RevisionNotFoundError
		var knf *KeyNotFoundError
		switch {
		case err == nil:
			out[name] = string(kv.Value)
		case errors.As(err, &nf), errors.As(err, &knf):
			out[name] = ""
		default:
			t.Fatalf("GetAtRevision(%s, %d): %v", name, rev, err)
		}
	}
	return out
}

// rangeAt is the prefix's Range at rev as key -> value.
func rangeAt(t *testing.T, s *Store, prefix string, rev int64) map[string]string {
	t.Helper()
	kvs, _, _, err := s.Range([]byte(prefix), prefixEnd([]byte(prefix)), service.WithRevision(rev))
	if err != nil {
		t.Fatalf("Range at %d: %v", rev, err)
	}
	out := map[string]string{}
	for _, kv := range kvs {
		out[string(kv.Key)[len(prefix):]] = string(kv.Value)
	}
	return out
}

// TestCompaction_KeepsTheValueLiveAtTheFloor: compaction to a revision keeps
// every key's entry that was live at it, so a read at any revision above the
// floor answers exactly as before -- etcd's contract, which a client listing
// at a revision above the compaction point relies on. Both compaction forms.
func TestCompaction_KeepsTheValueLiveAtTheFloor(t *testing.T) {
	compactions := map[string]struct {
		prefix  string
		compact func(*Store, int64) error
	}{
		"prefix": {"acme/app/", func(s *Store, rev int64) error { return s.CompactPrefix([]byte("acme/app/"), rev) }},
		"prefix in batch": {"acme/app/", func(s *Store, rev int64) error {
			return inBatch(s, func(b storage.Batch) error { return s.CompactPrefixInBatch(b, []byte("acme/app/"), rev) })
		}},
		"node keyspace": {"node-", func(s *Store, rev int64) error { return s.CompactNodeKeyspace(rev) }},
		"node keyspace in batch": {"node-", func(s *Store, rev int64) error {
			return inBatch(s, func(b storage.Batch) error { return s.CompactNodeKeyspaceInBatch(b, rev) })
		}},
	}
	for name, c := range compactions {
		for floor := int64(1); floor <= 8; floor++ {
			t.Run(fmt.Sprintf("%s/floor %d", name, floor), func(t *testing.T) {
				s := newTestStore(t)
				liveAtFloorFixture(t, s, c.prefix)

				before := map[int64][2]map[string]string{}
				for rev := floor + 1; rev <= 9; rev++ {
					before[rev] = [2]map[string]string{readsAt(t, s, c.prefix, rev), rangeAt(t, s, c.prefix, rev)}
				}

				if err := c.compact(s, floor); err != nil {
					t.Fatalf("compact to %d: %v", floor, err)
				}

				for rev := floor + 1; rev <= 9; rev++ {
					if got := readsAt(t, s, c.prefix, rev); !sameReads(got, before[rev][0]) {
						t.Fatalf("GetAtRevision at %d after compacting to %d = %v, want %v", rev, floor, got, before[rev][0])
					}
					if got := rangeAt(t, s, c.prefix, rev); !sameReads(got, before[rev][1]) {
						t.Fatalf("Range at %d after compacting to %d = %v, want %v", rev, floor, got, before[rev][1])
					}
				}
			})
		}
	}
}

// TestCompaction_ReadAboveFloorBeforeNewestWrite is the review's repro: put k
// at 1, 3 and 5, compact to 3, read at 4 -- the value live at the floor, v3.
func TestCompaction_ReadAboveFloorBeforeNewestWrite(t *testing.T) {
	s := newTestStore(t)
	k := []byte("a/b/k")
	for _, v := range []string{"v1", "x", "v3", "x", "v5"} {
		key := k
		if v == "x" {
			key = []byte("a/b/x")
		}
		if _, err := s.Put(key, []byte(v), 0); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := s.CompactPrefix([]byte("a/b/"), 3); err != nil {
		t.Fatalf("CompactPrefix: %v", err)
	}
	kv, err := s.GetAtRevision(k, 4)
	if err != nil || string(kv.Value) != "v3" {
		t.Fatalf("GetAtRevision(k, 4) after compacting to 3 = %v, %v; want v3", kv, err)
	}
	kvs, _, _, err := s.Range([]byte("a/b/"), []byte("a/b0"), service.WithRevision(4))
	if err != nil || len(kvs) != 2 {
		t.Fatalf("Range at 4 after compacting to 3 = %v, %v; want k and x", kvs, err)
	}
	// The entry below the kept one is gone.
	if n := dataEntries(t, s, k); n != 2 {
		t.Fatalf("k has %d data entries after compaction, want 2 (v3 kept, v5)", n)
	}
}

// TestCompaction_DropsATombstoneLiveAtTheFloor: a tombstone live at the floor
// is no history anyone can read -- the key did not exist -- so it goes,
// exactly as etcd drops it: whether the key was written again later or the
// tombstone is the key's newest entry. No read above the floor can reach it:
// a read at any revision above the floor finds no entry, which is the same
// answer the tombstone gave.
func TestCompaction_DropsATombstoneLiveAtTheFloor(t *testing.T) {
	s := newTestStore(t)
	liveAtFloorFixture(t, s, "acme/app/")
	if err := s.CompactPrefix([]byte("acme/app/"), 9); err != nil {
		t.Fatalf("CompactPrefix: %v", err)
	}
	// gone: tombstone at 6 had g7 above it at compaction time 6, but compacting
	// to 9 keeps g7 (live at 9) and drops everything older.
	if n := dataEntries(t, s, []byte("acme/app/gone")); n != 1 {
		t.Fatalf("gone has %d data entries, want 1 (g7)", n)
	}
	// dead: its tombstone at 9 is its newest entry and live at the floor, so
	// it goes with d8 below it; the key is simply absent.
	if n := dataEntries(t, s, []byte("acme/app/dead")); n != 0 {
		t.Fatalf("dead has %d data entries, want 0 (its tombstone dropped)", n)
	}
	if kv, err := s.GetAtRevision([]byte("acme/app/dead"), 10); err == nil {
		t.Fatalf("dead reads %v above the floor; it was deleted", kv)
	}

	s2 := newTestStore(t)
	liveAtFloorFixture(t, s2, "acme/app/")
	if err := s2.CompactPrefix([]byte("acme/app/"), 6); err != nil {
		t.Fatalf("CompactPrefix: %v", err)
	}
	// gone at 6: the tombstone is live at 6 and g7 is newer, so it goes with
	// g1 below it; only g7 remains, and a read at 6 is refused by the floor.
	if n := dataEntries(t, s2, []byte("acme/app/gone")); n != 1 {
		t.Fatalf("gone has %d data entries after compacting to 6, want 1 (g7)", n)
	}
}

// sameReads reports whether two key -> value maps are equal.
func sameReads(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// inBatch runs fn against a fresh batch of s and commits it.
func inBatch(s *Store, fn func(storage.Batch) error) error {
	b := s.storage.Batch()
	defer func() { _ = b.Close() }()
	if err := fn(b); err != nil {
		return err
	}
	return b.Commit()
}

// dataEntries counts key's data entries in storage.
func dataEntries(t *testing.T, s *Store, key []byte) int {
	t.Helper()
	iter := s.storage.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte{prefixData},
		UpperBound: []byte{prefixData + 1},
	})
	defer func() { _ = iter.Close() }()
	n := 0
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		if k, ok := dataUserKey(iter.Key()); ok && string(k) == string(key) {
			n++
		}
	}
	return n
}
