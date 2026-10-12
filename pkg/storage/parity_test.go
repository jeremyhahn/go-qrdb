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

// parity_test.go is a differential-oracle test: it drives the file, pebble,
// memory, and objstore (local provider) storage backends through an identical
// sequence of operations and
// asserts every observable result is byte-for-byte identical. This guards the
// cross-backend consistency that lets a filesystem-backed app (e.g. xkey
// during development) serve as a reference implementation for the pebble
// backend used in production.
package storage_test

import (
	"fmt"
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// namedBackend pairs a backend label with its storage.Storage instance.
type namedBackend struct {
	name string
	s    storage.Storage
}

// newParityBackends constructs one fresh instance of each backend. Pebble and
// file are rooted in per-test temp dirs; all are closed on cleanup.
func newParityBackends(t *testing.T) []namedBackend {
	t.Helper()

	pdb, err := pebble.New(&pebble.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("pebble.New: %v", err)
	}
	fdb, err := file.New(t.TempDir())
	if err != nil {
		t.Fatalf("file.New: %v", err)
	}
	mem := memory.NewStorage()
	odb, err := objstore.New("local", map[string]string{"path": t.TempDir()})
	if err != nil {
		t.Fatalf("objstore.New: %v", err)
	}
	omem, err := objstore.New("memory", map[string]string{})
	if err != nil {
		t.Fatalf("objstore.New(memory): %v", err)
	}

	backends := []namedBackend{
		{"memory", mem},
		{"pebble", pdb},
		{"file", fdb},
		{"objstore", odb},
		{"objstore-memory", omem},
	}
	t.Cleanup(func() {
		for _, b := range backends {
			_ = b.s.Close()
		}
	})
	return backends
}

// TestBackendParity drives every backend through the same scenario and asserts
// the resulting transcripts are identical. Any divergence between the file and
// pebble (or memory) backends fails the test with a per-line diff.
func TestBackendParity(t *testing.T) {
	backends := newParityBackends(t)

	transcripts := make(map[string][]string, len(backends))
	for _, b := range backends {
		transcripts[b.name] = runParityScenario(b.s)
	}

	ref := backends[0]
	refLines := transcripts[ref.name]
	for _, b := range backends[1:] {
		got := transcripts[b.name]
		if len(got) != len(refLines) {
			t.Fatalf("%s produced %d transcript lines, %s produced %d",
				b.name, len(got), ref.name, len(refLines))
		}
		for i := range refLines {
			if got[i] != refLines[i] {
				t.Errorf("parity divergence at step %d:\n  %s: %q\n  %s: %q",
					i, ref.name, refLines[i], b.name, got[i])
			}
		}
	}
}

// runParityScenario executes the shared operation sequence and returns a
// transcript of observable outcomes. The outcomes are normalized (values as
// strings, missing keys as "<nil>", errors as "ERR") so they are directly
// comparable across backends regardless of internal representation.
func runParityScenario(s storage.Storage) []string {
	var tr []string
	rec := func(format string, args ...any) { tr = append(tr, fmt.Sprintf(format, args...)) }

	getStr := func(key string) string {
		v, err := s.Get([]byte(key))
		if err != nil {
			return "ERR"
		}
		if v == nil {
			return "<nil>"
		}
		return string(v)
	}
	existsStr := func(key string) string {
		ok, err := s.Exists([]byte(key))
		if err != nil {
			return "ERR"
		}
		return fmt.Sprintf("%t", ok)
	}
	listStr := func(prefix string) string {
		keys, err := s.List([]byte(prefix))
		if err != nil {
			return "ERR"
		}
		out := make([]string, len(keys))
		for i, k := range keys {
			out[i] = string(k)
		}
		sort.Strings(out)
		return fmt.Sprintf("%v", out)
	}
	iterStr := func(opts *storage.IteratorOptions, backward bool) string {
		it := s.NewIterator(opts)
		defer func() { _ = it.Close() }()
		var pairs []string
		var ok bool
		if backward {
			ok = it.SeekToLast()
		} else {
			ok = it.SeekToFirst()
		}
		for ; ok && it.Valid(); ok = step(it, backward) {
			pairs = append(pairs, string(it.Key())+"="+string(it.Value()))
		}
		if err := it.Error(); err != nil {
			return "ERR"
		}
		return fmt.Sprintf("%v", pairs)
	}

	// 1. Populate k01..k08.
	for i := 1; i <= 8; i++ {
		k := fmt.Sprintf("k%02d", i)
		if err := s.Set([]byte(k), []byte(fmt.Sprintf("val-%02d", i))); err != nil {
			rec("set %s ERR", k)
		}
	}

	// 2. Point reads (present + absent).
	for i := 1; i <= 8; i++ {
		k := fmt.Sprintf("k%02d", i)
		rec("get %s=%s", k, getStr(k))
	}
	rec("get absent=%s", getStr("absent"))

	// 3. Existence.
	rec("exists k01=%s", existsStr("k01"))
	rec("exists absent=%s", existsStr("absent"))

	// 4. Overwrite.
	_ = s.Set([]byte("k01"), []byte("NEW"))
	rec("overwrite k01=%s", getStr("k01"))

	// 5. Delete.
	_ = s.Delete([]byte("k02"))
	rec("after del k02 get=%s exists=%s", getStr("k02"), existsStr("k02"))

	// 6. Prefix list.
	rec("list k0=%s", listStr("k0"))

	// 7. Full list.
	rec("list all=%s", listStr(""))

	// 8. Atomic batch commit (set + delete).
	b := s.Batch()
	_ = b.Set([]byte("k09"), []byte("val-09"))
	_ = b.Delete([]byte("k03"))
	if err := b.Commit(); err != nil {
		rec("batch commit ERR")
	}
	_ = b.Close()
	rec("after batch k09=%s k03=%s", getStr("k09"), getStr("k03"))

	// 9. Batch rollback (must not apply).
	b2 := s.Batch()
	_ = b2.Set([]byte("k10"), []byte("rolled-back"))
	_ = b2.Rollback()
	_ = b2.Close()
	rec("after rollback k10=%s", getStr("k10"))

	// 10. Forward iteration over the whole keyspace.
	rec("iter forward=%s", iterStr(&storage.IteratorOptions{}, false))

	// 11. Backward iteration (SeekToLast + Prev).
	rec("iter backward=%s", iterStr(&storage.IteratorOptions{}, true))

	// 12. Bounded forward iteration [k04, k07).
	rec("iter bounds=%s", iterStr(&storage.IteratorOptions{
		LowerBound: []byte("k04"),
		UpperBound: []byte("k07"),
	}, false))

	// 13. Seek positioning to the first key >= target.
	it := s.NewIterator(&storage.IteratorOptions{})
	if it.Seek([]byte("k055")) && it.Valid() {
		rec("seek k055=%s", string(it.Key()))
	} else {
		rec("seek k055=<none>")
	}
	_ = it.Close()

	// 14. Snapshot isolation: a snapshot must not observe writes made after it.
	snap, err := s.Snapshot()
	if err != nil {
		rec("snapshot ERR")
	} else {
		_ = s.Set([]byte("k06"), []byte("MUTATED-AFTER-SNAPSHOT"))
		sv, serr := snap.Get([]byte("k06"))
		if serr != nil {
			rec("snapshot k06=ERR")
		} else {
			rec("snapshot k06=%s live k06=%s", string(sv), getStr("k06"))
		}
		_ = snap.Close()
	}

	return tr
}

// step advances an iterator in the requested direction.
func step(it storage.Iterator, backward bool) bool {
	if backward {
		return it.Prev()
	}
	return it.Next()
}
