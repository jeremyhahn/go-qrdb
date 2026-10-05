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
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// TestNew_RefusesOldLayoutInPlace reproduces a review probe finding: a
// data directory written by the previous (pre-go-qrdb#592) per-key-file
// layout has real files in it but no format marker. New must refuse it
// rather than silently treating it as this format's empty store — opening
// it in place would misread stale files as nothing and let Set write
// records this build's own readers would not recognise either.
func TestNew_RefusesOldLayoutInPlace(t *testing.T) {
	dir := t.TempDir()
	// Old layout: key "foo" was the file <dir>/foo.
	if err := os.WriteFile(filepath.Join(dir, "foo"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := New(dir)
	var missing *FormatMarkerMissingError
	if !errors.As(err, &missing) {
		t.Fatalf("New(old-layout dir) = %v, want *FormatMarkerMissingError", err)
	}
}

// TestNew_RefusesMismatchedFormatVersion pins the other half of the format
// guard: a marker naming a version other than this build's is refused by
// name, not silently accepted or misread.
func TestNew_RefusesMismatchedFormatVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, formatMarkerFileName), []byte("999"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := New(dir)
	var mismatch *FormatVersionMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("New(mismatched version) = %v, want *FormatVersionMismatchError", err)
	}
	if mismatch.Found != "999" || mismatch.Want != formatVersion {
		t.Errorf("mismatch = %+v, want Found=999 Want=%s", mismatch, formatVersion)
	}
}

// TestNew_FreshAndEmptyDirectoriesWriteTheMarker proves a directory New has
// never touched before — whether it does not exist yet or exists but is
// empty — is accepted and comes back marked, so a SECOND Open of the exact
// same directory succeeds too.
func TestNew_FreshAndEmptyDirectoriesWriteTheMarker(t *testing.T) {
	for _, name := range []string{"does-not-exist-yet", "already-empty"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "data")
			if name == "already-empty" {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}

			e, err := New(dir)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_ = e.Close()

			data, err := os.ReadFile(filepath.Join(dir, formatMarkerFileName))
			if err != nil {
				t.Fatalf("read marker: %v", err)
			}
			if string(data) != formatVersion {
				t.Errorf("marker = %q, want %q", data, formatVersion)
			}

			// Reopening the now-marked directory must succeed.
			e2, err := New(dir)
			if err != nil {
				t.Fatalf("second New: %v", err)
			}
			_ = e2.Close()
		})
	}
}

// TestDeepNesting_NeverFails reproduces a review probe finding against the
// PREVIOUS per-segment on-disk layout: a key built from thousands of short
// '/'-delimited segments produced a path longer than PATH_MAX, and
// os.MkdirAll returned ENAMETOOLONG. The hashed, flat layout has no
// path-length dependency on the key at all, so every depth here must
// succeed identically.
func TestDeepNesting_NeverFails(t *testing.T) {
	for _, n := range []int{100, 1000, 2100, 4000} {
		key := []byte(strings.Repeat("a/", n) + "a")
		e, err := New(t.TempDir())
		if err != nil {
			t.Fatalf("depth %d: New: %v", n, err)
		}
		if err := e.Set(key, []byte("v")); err != nil {
			t.Errorf("depth %d (key len %d): Set: %v", n, len(key), err)
		}
		got, err := e.Get(key)
		if err != nil || string(got) != "v" {
			t.Errorf("depth %d: Get = (%q, %v), want (v, nil)", n, got, err)
		}
		_ = e.Close()
	}
}

// TestCommitBufferedOps_RefusalWritesNoJournalAndAppliesNothing reproduces a
// review probe finding (go-qrdb#592 B2/B3): a refused op used to leave a
// published commit journal behind — commitBufferedOps wrote it before
// validating any op — so a deterministic refusal left a journal that
// recoverJournal would replay, hit the identical refusal, and fail the NEXT
// Open permanently. Refusing before the journal is written means the batch
// leaves neither a journal nor a partial write, and the engine reopens
// cleanly afterward.
//
// commitBufferedOps is called directly, with a hand-built op set, rather
// than through Batch(): storage.NewBufferedBatch already refuses an empty
// key at Set/Delete time, before Commit, so the public Batch API can no
// longer get an invalid op as far as the journal at all (an independent,
// earlier line of defence — the one go-qrdb#592's probe caught was
// specifically commitBufferedOps's OWN handling once an op reaches it, and
// that is what this test exercises directly).
func TestCommitBufferedOps_RefusalWritesNoJournalAndAppliesNothing(t *testing.T) {
	dir := t.TempDir()
	e, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	commitErr := e.commitBufferedOps([]storage.BufferedOp{
		{Key: []byte("good"), Value: []byte("v")},
		{Key: nil, Value: []byte("v")},
	})
	if !errors.Is(commitErr, storage.ErrEmptyKey) {
		t.Fatalf("commitBufferedOps = %v, want ErrEmptyKey", commitErr)
	}

	// Nothing applied: not even the good op in the same batch.
	if got, _ := e.Get([]byte("good")); got != nil {
		t.Fatalf("Get(good) = %q, want nil — the refused batch must apply nothing", got)
	}
	// No journal left behind.
	if _, statErr := os.Stat(filepath.Join(dir, journalFileName)); !os.IsNotExist(statErr) {
		t.Fatal("commit journal left behind after a refused batch")
	}
	_ = e.Close()

	// The engine reopens cleanly: no journal for recoverJournal to replay
	// (and fail on, forever) at the next Open.
	e2, err := New(dir)
	if err != nil {
		t.Fatalf("reopen after refused batch: %v", err)
	}
	_ = e2.Close()
}

// TestCommitBufferedOps_PartialRefusalNeverPartiallyApplies covers the
// second op in a batch being the refused one, proving the pre-check runs
// over the WHOLE op set before anything touches storage, not just the
// first op. See TestCommitBufferedOps_RefusalWritesNoJournalAndAppliesNothing
// for why this calls commitBufferedOps directly.
func TestCommitBufferedOps_PartialRefusalNeverPartiallyApplies(t *testing.T) {
	e, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	err = e.commitBufferedOps([]storage.BufferedOp{
		{Key: []byte("first"), Value: []byte("v")},
		{Key: nil, Deleted: true},
	})
	if !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("commitBufferedOps = %v, want ErrEmptyKey", err)
	}
	if got, _ := e.Get([]byte("first")); got != nil {
		t.Fatalf("Get(first) = %q, want nil", got)
	}
}

// TestPropertyPrefixNestingNeverCollides runs a seeded, deterministic
// random sequence of Put/Delete over keys with arbitrary '/' nesting and
// prefix relationships (the shape go-qrdb#592 was about) directly against
// the engine, proving the hashed layout never fails on key SHAPE — only a
// genuine storage fault could make Set/Delete/Get return an error here.
func TestPropertyPrefixNestingNeverCollides(t *testing.T) {
	e, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	rng := rand.New(rand.NewSource(0xC0FFEE))
	alphabet := "abc"
	randKey := func() []byte {
		segments := rng.Intn(4) + 1
		var b strings.Builder
		for i := 0; i < segments; i++ {
			if i > 0 {
				b.WriteByte('/')
			}
			segLen := rng.Intn(3) + 1
			for j := 0; j < segLen; j++ {
				b.WriteByte(alphabet[rng.Intn(len(alphabet))])
			}
		}
		return []byte(b.String())
	}

	live := map[string][]byte{}
	for i := 0; i < 500; i++ {
		key := randKey()
		if rng.Intn(4) == 0 {
			if err := e.Delete(key); err != nil {
				t.Fatalf("entry %d: Delete(%q): %v", i, key, err)
			}
			delete(live, string(key))
			continue
		}
		val := []byte{byte('0' + i%10)}
		if err := e.Set(key, val); err != nil {
			t.Fatalf("entry %d: Set(%q): %v", i, key, err)
		}
		live[string(key)] = val
	}

	for k, want := range live {
		got, err := e.Get([]byte(k))
		if err != nil {
			t.Fatalf("Get(%q): %v", k, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("Get(%q) = %q, want %q", k, got, want)
		}
	}
	keys, err := e.List(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != len(live) {
		t.Errorf("List returned %d keys, want %d", len(keys), len(live))
	}
	if !sort.SliceIsSorted(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 }) {
		t.Error("List is not byte-ordered")
	}
}

// FuzzEngine_SetGetDeleteRoundTrip fuzzes arbitrary key/value pairs through
// Set/Get/Delete, proving the hashed record format round-trips every byte
// string a key or value can be, with no dependence on '/' placement, NUL
// bytes, or length.
func FuzzEngine_SetGetDeleteRoundTrip(f *testing.F) {
	f.Add([]byte("dir/child"), []byte("v1"))
	f.Add([]byte("dir"), []byte("v2"))
	f.Add([]byte{0x00, '/', 0xff}, []byte{})
	f.Add([]byte(strings.Repeat("a/", 50)+"a"), []byte("deep"))

	f.Fuzz(func(t *testing.T, key, value []byte) {
		if len(key) == 0 {
			t.Skip()
		}
		e, err := New(t.TempDir())
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer func() { _ = e.Close() }()

		if err := e.Set(key, value); err != nil {
			t.Fatalf("Set(%q): %v", key, err)
		}
		got, err := e.Get(key)
		if err != nil {
			t.Fatalf("Get(%q): %v", key, err)
		}
		if !bytes.Equal(got, value) && !(len(got) == 0 && len(value) == 0) {
			t.Fatalf("Get(%q) = %q, want %q", key, got, value)
		}
		if err := e.Delete(key); err != nil {
			t.Fatalf("Delete(%q): %v", key, err)
		}
		if got, err := e.Get(key); err != nil || got != nil {
			t.Fatalf("Get(%q) after delete = (%q, %v), want (nil, nil)", key, got, err)
		}
	})
}

// ---------------------------------------------------------------------------
// go-qrdb#592 F2: scans must not abort, or silently truncate, on a
// concurrent Set's rename-into-place or a concurrent Delete.
// ---------------------------------------------------------------------------

// TestConcurrentScan_StableUnderChurn keeps 2,000 keys untouched while four
// goroutines continuously Set and Delete an UNRELATED keyspace, and asserts
// that every iterator scan, List call and Snapshot sees all 2,000 stable
// keys with no error, every round. Before the F2 fix, Walk's own per-entry
// ENOENT -- a renamed-away ".tmp-set-*" file, or a record a concurrent
// Delete removed between Walk reading its parent directory and stat'ing it
// -- aborted the whole scan (iterator and List) or was silently absorbed
// into a truncated Snapshot with a nil error.
func TestConcurrentScan_StableUnderChurn(t *testing.T) {
	const stableKeys = 2000
	e, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	for i := 0; i < stableKeys; i++ {
		if err := e.Set([]byte(fmt.Sprintf("stable/%05d", i)), []byte("v")); err != nil {
			t.Fatalf("seed Set: %v", err)
		}
	}

	var stop atomic.Bool
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			i := 0
			for !stop.Load() {
				k := []byte(fmt.Sprintf("churn/%d/%d", w, i%200))
				_ = e.Set(k, []byte("v"))
				_ = e.Delete(k)
				i++
			}
		}(w)
	}
	defer func() {
		stop.Store(true)
		wg.Wait()
	}()

	opts := &storage.IteratorOptions{LowerBound: []byte("stable/"), UpperBound: []byte("stable0")}
	for round := 0; round < 15; round++ {
		it := e.NewIterator(opts)
		n := 0
		for it.SeekToFirst(); it.Valid(); it.Next() {
			n++
		}
		if err := it.Error(); err != nil {
			t.Fatalf("round %d: iterator error: %v", round, err)
		}
		if n != stableKeys {
			t.Fatalf("round %d: iterator saw %d of %d stable keys", round, n, stableKeys)
		}
		_ = it.Close()

		keys, err := e.List([]byte("stable/"))
		if err != nil {
			t.Fatalf("round %d: List error: %v", round, err)
		}
		if len(keys) != stableKeys {
			t.Fatalf("round %d: List returned %d of %d stable keys", round, len(keys), stableKeys)
		}

		snap, err := e.Snapshot()
		if err != nil {
			t.Fatalf("round %d: Snapshot error: %v", round, err)
		}
		sit := snap.NewIterator(opts)
		m := 0
		for sit.SeekToFirst(); sit.Valid(); sit.Next() {
			m++
		}
		if m != stableKeys {
			t.Fatalf("round %d: FINDING snapshot silently holds %d of %d stable keys with a nil error", round, m, stableKeys)
		}
		_ = snap.Close()
	}
}

// ---------------------------------------------------------------------------
// go-qrdb#592 F3: the format-marker check must ignore lost+found, the
// directory mkfs/fsck creates at a filesystem mount root, rather than treat
// it as legacy qrdb content and refuse to initialize.
// ---------------------------------------------------------------------------

func TestNew_IgnoresLostAndFoundWhenDecidingFreshness(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "lost+found"), 0o700); err != nil {
		t.Fatal(err)
	}

	e, err := New(dir)
	if err != nil {
		t.Fatalf("New refused a mount root containing only lost+found: %v", err)
	}
	defer func() { _ = e.Close() }()

	if err := e.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set after opening over lost+found: %v", err)
	}
	got, err := e.Get([]byte("k"))
	if err != nil || string(got) != "v" {
		t.Fatalf("Get after opening over lost+found = (%q, %v), want (\"v\", nil)", got, err)
	}

	if _, err := os.Stat(filepath.Join(dir, formatMarkerFileName)); err != nil {
		t.Errorf("format marker was not written: %v", err)
	}
}

// A directory holding lost+found AND genuine old-layout content is still
// refused: lost+found is ignored, not a license to ignore everything else.
func TestNew_LostAndFoundDoesNotMaskOldLayoutContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "lost+found"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "foo"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := New(dir)
	var m *FormatMarkerMissingError
	if !errors.As(err, &m) {
		t.Fatalf("New(dir with lost+found AND old-layout content) = %v, want *FormatMarkerMissingError", err)
	}
}
