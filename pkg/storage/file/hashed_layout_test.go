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
	"github.com/jeremyhahn/go-qrdb/pkg/testutil/memdir"
)

// TestNew_RefusesOldLayoutInPlace reproduces a review probe finding: a
// data directory written by the previous (pre-go-qrdb#592) per-key-file
// layout has real files in it but no format marker. New must refuse it
// rather than silently treating it as this format's empty store — opening
// it in place would misread stale files as nothing and let Set write
// records this build's own readers would not recognise either.
func TestNew_RefusesOldLayoutInPlace(t *testing.T) {
	dir := memdir.TempDir(t)
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
	dir := memdir.TempDir(t)
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
			base := memdir.TempDir(t)
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
			if want := formatMarker(atRestPlaintext); string(data) != want {
				t.Errorf("marker = %q, want %q", data, want)
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
		e, err := New(memdir.TempDir(t))
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
	dir := memdir.TempDir(t)
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
	e, err := New(memdir.TempDir(t))
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
	e, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	rng := rand.New(rand.NewSource(0xC0FFEE)) // #nosec G404 -- fixed-seed property-test fixture, not a cryptographic use
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
		e, err := New(memdir.TempDir(t))
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
	e, err := New(memdir.TempDir(t))
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
	dir := memdir.TempDir(t)
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
	dir := memdir.TempDir(t)
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

// TestWalks_NeverDescendIntoAnUnreadableRootEntry is the real-world shape
// F3 is about: lost+found on a genuine mount is root-owned, mode 0700, so a
// non-root process cannot even LIST its contents, let alone read them.
// ensureFormatMarker's os.ReadDir(e.dir) only lists the ROOT (never
// descends), so it was never at risk; New's OWN cleanupTempSetFiles, List,
// and the iterator (and so Snapshot, which loads through the same
// iterator) all walk the tree with filepath.Walk and must never try to
// open this directory's contents at all -- SkipDir on sight, not a
// permission error caught and ignored after the fact. Denies this
// process's own access (mode 0) rather than relying on root ownership,
// which a non-root test cannot create; the remote gate (per the task that
// added this test) runs it as a non-root user for the identical reason a
// real mount would deny a non-root qrdbd.
func TestWalks_NeverDescendIntoAnUnreadableRootEntry(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a 0-mode directory is still readable by root, so this test cannot exercise the permission denial it is about")
	}

	dir := memdir.TempDir(t)
	lostAndFound := filepath.Join(dir, "lost+found")
	if err := os.Mkdir(lostAndFound, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lostAndFound, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(lostAndFound, 0o700) }) // #nosec G302 -- restores the mode memdir.TempDir(t) needs to remove the directory; not a secret file's mode

	// New itself must succeed: it runs cleanupTempSetFiles, which walks the
	// tree exactly like List and the iterator do.
	e, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = e.Close() }()

	if err := e.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	keys, err := e.List(nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 || string(keys[0]) != "k" {
		t.Fatalf("List = %q, want [\"k\"]", keys)
	}

	it := e.NewIterator(nil)
	n := 0
	for it.SeekToFirst(); it.Valid(); it.Next() {
		n++
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}
	if n != 1 {
		t.Fatalf("iterator saw %d entries, want 1", n)
	}
	_ = it.Close()

	snap, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()
	v, err := snap.Get([]byte("k"))
	if err != nil || string(v) != "v" {
		t.Fatalf("Snapshot.Get = (%q, %v), want (\"v\", nil)", v, err)
	}

	// Reopening over the same, still-unreadable lost+found must also
	// succeed: New's own cleanupTempSetFiles walk is exercised again.
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	e2, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = e2.Close()
}

// ---------------------------------------------------------------------------
// go-qrdb#592 performance: a shard directory confirmed durable once must
// not pay mkdirAllDurable's parent fsync again on every later Set into it.
// ---------------------------------------------------------------------------

// TestEnsureShardDirDurable_CachesPerDirectory pins the mechanism directly:
// the first Set into a shard directory records it in durableShardDirs, a
// repeat Set to the SAME key (same shard directory) is a cache hit and
// leaves the cache unchanged, and a key that hashes into a DIFFERENT shard
// directory gets its own, separate entry.
func TestEnsureShardDirDurable_CachesPerDirectory(t *testing.T) {
	e, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	countCached := func() int {
		n := 0
		e.durableShardDirs.Range(func(_, _ any) bool {
			n++
			return true
		})
		return n
	}

	if n := countCached(); n != 0 {
		t.Fatalf("fresh engine: %d shard directories already cached, want 0", n)
	}

	if err := e.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	dirA, _ := e.recordPaths([]byte("a"))
	if _, ok := e.durableShardDirs.Load(dirA); !ok {
		t.Fatal("Set did not record its shard directory as durable")
	}
	if n := countCached(); n != 1 {
		t.Fatalf("after one key: %d shard directories cached, want 1", n)
	}

	// A repeat Set to the IDENTICAL key hashes into the identical shard
	// directory: the cache must not grow.
	if err := e.Set([]byte("a"), []byte("2")); err != nil {
		t.Fatal(err)
	}
	if n := countCached(); n != 1 {
		t.Fatalf("after repeating the same key: %d shard directories cached, want 1", n)
	}
	got, err := e.Get([]byte("a"))
	if err != nil || string(got) != "2" {
		t.Fatalf("Get after the repeated Set = (%q, %v), want (\"2\", nil)", got, err)
	}

	// A key that is EXTREMELY unlikely to share "a"'s shard directory (the
	// hash is content-addressed and uniform) gets its own cache entry.
	if err := e.Set([]byte("a wildly different key, not sharing a's shard"), []byte("3")); err != nil {
		t.Fatal(err)
	}
	if n := countCached(); n != 2 {
		t.Fatalf("after a second, differently-hashing key: %d shard directories cached, want 2", n)
	}
}

// TestEnsureShardDirDurable_SkipsMkdirAllDurableOnCacheHit confirms the
// cache is actually consulted BEFORE mkdirAllDurable would run, not merely
// populated alongside it: removing the shard directory out from under a
// cached entry (something Set itself never does -- shard directories are
// permanent) and then calling Set again must still succeed by trusting the
// cache rather than re-verifying the directory on disk, which is exactly
// the work the cache exists to skip.
func TestEnsureShardDirDurable_SkipsMkdirAllDurableOnCacheHit(t *testing.T) {
	e, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	key := []byte("cache-hit-key")
	if err := e.Set(key, []byte("v")); err != nil {
		t.Fatal(err)
	}
	dir, _ := e.recordPaths(key)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	// mkdirAllDurable would recreate dir here; a cache hit skips it
	// entirely, so dir stays gone and the write below fails with ENOENT --
	// proof the cache path, not mkdirAllDurable, decided the outcome.
	err = e.ensureShardDirDurable(dir)
	if err != nil {
		t.Fatalf("ensureShardDirDurable returned an error on a cache hit: %v", err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("cache hit recreated the removed directory: stat = %v", statErr)
	}
}

// TestApplyBufferedOps_SetErrorPropagates covers applyBufferedOps's Set
// branch failing mid-batch: the journal is already durable (by design, for
// a transient fault recovery can retry), and the error reaches the caller.
func TestApplyBufferedOps_SetErrorPropagates(t *testing.T) {
	e, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	key := []byte("blocked-in-a-batch")
	shardDir, _ := e.recordPaths(key)
	obstacle := filepath.Dir(shardDir)
	if err := os.RemoveAll(obstacle); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(obstacle, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := e.commitBufferedOps([]storage.BufferedOp{{Key: key, Value: []byte("v")}}); err == nil {
		t.Fatal("expected Set to fail inside the batch")
	}
}

// TestApplyBufferedOps_DeleteErrorPropagates is the Delete-branch twin.
func TestApplyBufferedOps_DeleteErrorPropagates(t *testing.T) {
	e, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	key := []byte("blocked-delete-in-a-batch")
	_, recordPath := e.recordPaths(key)
	if err := os.MkdirAll(recordPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recordPath, "occupied"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := e.commitBufferedOps([]storage.BufferedOp{{Key: key, Deleted: true}}); err == nil {
		t.Fatal("expected Delete to fail inside the batch")
	}
}
