// Copyright 2026 Jeremy Hahn
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

package mvcc

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

// openObjstoreStore opens an MVCC store over objstore's local provider rooted
// at dir. Opening the same dir again is a reopen: everything the store wrote
// must come back.
func openObjstoreStore(t *testing.T, dir string) (*Store, storage.Storage) {
	t.Helper()
	backend, err := objstore.New("local", map[string]string{"path": dir})
	if err != nil {
		t.Fatalf("objstore.New: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	s, err := New(backend)
	if err != nil {
		t.Fatalf("mvcc.New over objstore: %v", err)
	}
	return s, backend
}

// deleteRangeOneRevision tombstones every live key in [start, end) at a
// single revision, the way a range delete is applied by the state machine.
func deleteRangeOneRevision(t *testing.T, s *Store, start, end []byte) int64 {
	t.Helper()
	kvs, _, _, err := s.Range(start, end)
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	rev := s.CurrentRevision() + 1
	batch := s.Storage().Batch()
	defer func() { _ = batch.Close() }()
	for _, kv := range kvs {
		if _, err := s.DeleteInBatch(batch, kv.Key, rev); err != nil {
			t.Fatalf("delete %q: %v", kv.Key, err)
		}
	}
	if err := batch.Set(RevisionKey(), EncodeRevision(rev)); err != nil {
		t.Fatalf("persist revision: %v", err)
	}
	if err := batch.Commit(); err != nil {
		t.Fatalf("commit range delete: %v", err)
	}
	s.SetRevision(rev)
	return rev
}

func TestObjstoreBackend_PutGetDelete(t *testing.T) {
	s, _ := openObjstoreStore(t, t.TempDir())

	kv, err := s.Put([]byte("a"), []byte("1"), 0)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if kv.ModRevision != 1 || kv.CreateRevision != 1 || kv.Version != 1 {
		t.Fatalf("first put = %+v, want mod=create=version=1", kv)
	}
	if kv, err = s.Put([]byte("a"), []byte("2"), 0); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if kv.ModRevision != 2 || kv.CreateRevision != 1 || kv.Version != 2 {
		t.Fatalf("overwrite = %+v, want mod=2 create=1 version=2", kv)
	}

	got, err := s.Get([]byte("a"))
	if err != nil || string(got.Value) != "2" {
		t.Fatalf("get = %+v, %v; want value 2", got, err)
	}
	old, err := s.GetAtRevision([]byte("a"), 1)
	if err != nil || string(old.Value) != "1" {
		t.Fatalf("get at 1 = %+v, %v; want value 1", old, err)
	}

	prev, err := s.Delete([]byte("a"))
	if err != nil || prev == nil || string(prev.Value) != "2" {
		t.Fatalf("delete = %+v, %v; want previous value 2", prev, err)
	}
	if _, err := s.Get([]byte("a")); err == nil {
		t.Fatal("get after delete: want an error, got none")
	}
	if s.CurrentRevision() != 3 {
		t.Fatalf("revision = %d, want 3", s.CurrentRevision())
	}
}

func TestObjstoreBackend_DeleteRange(t *testing.T) {
	s, _ := openObjstoreStore(t, t.TempDir())
	for _, k := range []string{"r/a", "r/b", "r/c", "s/keep"} {
		if _, err := s.Put([]byte(k), []byte("v-"+k), 0); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	before := s.CurrentRevision()

	rev := deleteRangeOneRevision(t, s, []byte("r/"), []byte("r0"))
	if rev != before+1 {
		t.Fatalf("range delete revision = %d, want %d", rev, before+1)
	}

	live, count, _, err := s.Range([]byte("r/"), []byte("r0"))
	if err != nil {
		t.Fatalf("range after delete: %v", err)
	}
	if len(live) != 0 || count != 0 {
		t.Fatalf("range after delete = %d keys (count %d), want none", len(live), count)
	}
	for _, k := range []string{"r/a", "r/b", "r/c"} {
		kv, err := s.GetAtRevision([]byte(k), before)
		if err != nil || string(kv.Value) != "v-"+k {
			t.Fatalf("%s at %d = %+v, %v; want the pre-delete value", k, before, kv, err)
		}
	}
	keep, err := s.Get([]byte("s/keep"))
	if err != nil || string(keep.Value) != "v-s/keep" {
		t.Fatalf("key outside the range = %+v, %v; want it untouched", keep, err)
	}
}

func TestObjstoreBackend_RevisionSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, backend := openObjstoreStore(t, dir)
	for i, v := range []string{"1", "2", "3"} {
		if _, err := s.Put([]byte("k"), []byte(v), 0); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	want := s.CurrentRevision()
	if err := backend.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, _ := openObjstoreStore(t, dir)
	if got := reopened.CurrentRevision(); got != want {
		t.Fatalf("revision after reopen = %d, want %d", got, want)
	}
	kv, err := reopened.Get([]byte("k"))
	if err != nil || string(kv.Value) != "3" || kv.Version != 3 {
		t.Fatalf("get after reopen = %+v, %v; want value 3 version 3", kv, err)
	}
	if old, err := reopened.GetAtRevision([]byte("k"), 1); err != nil || string(old.Value) != "1" {
		t.Fatalf("history after reopen = %+v, %v; want value 1", old, err)
	}
	next, err := reopened.Put([]byte("k"), []byte("4"), 0)
	if err != nil {
		t.Fatalf("put after reopen: %v", err)
	}
	if next.ModRevision != want+1 {
		t.Fatalf("revision after reopen put = %d, want %d", next.ModRevision, want+1)
	}
}

func TestObjstoreBackend_Compaction(t *testing.T) {
	dir := t.TempDir()
	s, backend := openObjstoreStore(t, dir)
	// k at 1, 2 and 3, x at 4, k at 5: compacting to 3 keeps k's v3, the
	// entry live at the floor, and v5.
	for _, v := range []string{"1", "2", "3", "x", "5"} {
		key := []byte("c/k")
		if v == "x" {
			key = []byte("c/x")
		}
		if _, err := s.Put(key, []byte(v), 0); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	versions := func(st storage.Storage) int {
		keys, err := st.List(DataKeyPrefix([]byte("c/k")))
		if err != nil {
			t.Fatalf("list versions: %v", err)
		}
		return len(keys)
	}
	if n := versions(backend); n != 4 {
		t.Fatalf("versions before compaction = %d, want 4 (revs 1, 2, 3, 5)", n)
	}

	if err := s.CompactPrefix([]byte("c/"), 3); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if got := s.CompactionFloor([]byte("c/")); got != 3 {
		t.Fatalf("floor = %d, want 3", got)
	}
	if n := versions(backend); n != 2 {
		t.Fatalf("versions after compaction = %d, want 2 (rev 3 and rev 5)", n)
	}
	var compacted *RevisionCompactedError
	if _, err := s.GetAtRevision([]byte("c/k"), 3); !errors.As(err, &compacted) {
		t.Fatalf("read at the floor: err = %v, want RevisionCompactedError", err)
	}
	if kv, err := s.GetAtRevision([]byte("c/k"), 4); err != nil || string(kv.Value) != "3" {
		t.Fatalf("read above the floor = %+v, %v; want value 3, live at the floor", kv, err)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, rbackend := openObjstoreStore(t, dir)
	if got := reopened.CompactionFloor([]byte("c/")); got != 3 {
		t.Fatalf("floor after reopen = %d, want 3", got)
	}
	if n := versions(rbackend); n != 2 {
		t.Fatalf("versions after reopen = %d, want 2", n)
	}
	if _, err := reopened.GetAtRevision([]byte("c/k"), 2); !errors.As(err, &compacted) {
		t.Fatalf("read below the floor after reopen: err = %v, want RevisionCompactedError", err)
	}
	if kv, err := reopened.GetAtRevision([]byte("c/k"), 4); err != nil || string(kv.Value) != "3" {
		t.Fatalf("read above the floor after reopen = %+v, %v; want value 3", kv, err)
	}
	if kv, err := reopened.Get([]byte("c/k")); err != nil || string(kv.Value) != "5" {
		t.Fatalf("latest after reopen = %+v, %v; want value 5", kv, err)
	}
}
