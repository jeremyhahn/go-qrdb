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

package file

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/testutil/memdir"
	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
)

// commitKeys commits one batch setting every key to value.
func commitKeys(t *testing.T, e *Engine, keys [][]byte, value []byte) error {
	t.Helper()
	b := e.Batch()
	defer func() { _ = b.Close() }()
	for _, k := range keys {
		if err := b.Set(k, value); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	return b.Commit()
}

func distinctKeys(n int) [][]byte {
	keys := make([][]byte, n)
	for i := range keys {
		keys[i] = []byte("parallel/" + strconv.Itoa(i))
	}
	return keys
}

// TestCommit_RecordSyncsOverlap: a commit's records are written together, so
// the commit waits about one directory sync for all of them rather than one
// per key in a row.
func TestCommit_RecordSyncsOverlap(t *testing.T) {
	// The delay dominates the commit's own CPU so the bound below measures
	// sync depth, not instrumentation: under -race with GOFIPS140 every
	// record's temp name draws on a FIPS DRBG that sync.Pool's race-mode
	// eviction keeps reseeding from the jitter entropy source, which at
	// 25ms pushed a fully overlapped commit past the bound (go-qrdb#20).
	const n, delay = 32, 100 * time.Millisecond
	e, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	keys := distinctKeys(n)
	// The first commit creates the shard directories; the timed one only
	// replaces records, so every directory sync it waits on is a record's.
	if err := commitKeys(t, e, keys, []byte("v1")); err != nil {
		t.Fatalf("warm-up commit: %v", err)
	}

	var syncs atomic.Int32
	t.Cleanup(fsync.SetForTest(func(string) error {
		syncs.Add(1)
		time.Sleep(delay)
		return nil
	}))
	began := time.Now()
	if err := commitKeys(t, e, keys, []byte("v2")); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	elapsed := time.Since(began)

	if got := syncs.Load(); got < n {
		t.Fatalf("commit issued %d directory syncs, want at least one per record (%d)", got, n)
	}
	// The journal publish, the records and the journal clear: three syncs
	// deep. One record after another is n+2 deep.
	if limit := 8 * delay; elapsed > limit {
		t.Fatalf("a %d-key commit took %s under a %s directory sync, want under %s (serial is about %s)",
			n, elapsed, delay, limit, time.Duration(n+2)*delay)
	}
	for _, k := range keys {
		if v, err := e.Get(k); err != nil || !bytes.Equal(v, []byte("v2")) {
			t.Fatalf("Get(%s) = %q, %v; want v2", k, v, err)
		}
	}
}

// TestCommit_FailedRecordSyncIsRecoveredFromTheJournal: when one record's
// sync fails while the others are in flight, Commit reports the failure and
// leaves the journal, and the next open replays the whole batch.
func TestCommit_FailedRecordSyncIsRecoveredFromTheJournal(t *testing.T) {
	dir := memdir.TempDir(t)
	e, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	keys := distinctKeys(16)
	if err := commitKeys(t, e, keys, []byte("v1")); err != nil {
		t.Fatalf("warm-up commit: %v", err)
	}

	failDir, _ := mustRecordPaths(t, e, keys[7])
	sentinel := errors.New("record directory sync failed")
	restore := fsync.SetForTest(func(d string) error {
		if d == failDir {
			return sentinel
		}
		return nil
	})
	err = commitKeys(t, e, keys, []byte("v2"))
	restore()
	if !errors.Is(err, sentinel) {
		t.Fatalf("Commit = %v, want the record's sync failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, journalFileName)); statErr != nil {
		t.Fatalf("the journal is gone after a failed apply, so recovery has nothing to replay: %v", statErr)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	for _, k := range keys {
		if v, err := reopened.Get(k); err != nil || !bytes.Equal(v, []byte("v2")) {
			t.Fatalf("after recovery Get(%s) = %q, %v; want v2", k, v, err)
		}
	}
}

// TestCommit_LastOperationOnAKeyWins: applying the distinct keys together
// leaves the same state as applying every operation in order.
func TestCommit_LastOperationOnAKeyWins(t *testing.T) {
	e, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if err := e.Set([]byte("gone"), []byte("old")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	b := e.Batch()
	defer func() { _ = b.Close() }()
	steps := []func() error{
		func() error { return b.Set([]byte("kept"), []byte("first")) },
		func() error { return b.Delete([]byte("kept")) },
		func() error { return b.Set([]byte("kept"), []byte("last")) },
		func() error { return b.Set([]byte("gone"), []byte("new")) },
		func() error { return b.Delete([]byte("gone")) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("batch op: %v", err)
		}
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if v, err := e.Get([]byte("kept")); err != nil || !bytes.Equal(v, []byte("last")) {
		t.Fatalf("Get(kept) = %q, %v; want last", v, err)
	}
	if v, err := e.Get([]byte("gone")); err != nil || v != nil {
		t.Fatalf("Get(gone) = %q, %v; want deleted", v, err)
	}
}

func TestLastOpPerKey(t *testing.T) {
	op := func(k, v string, del bool) storage.BufferedOp {
		return storage.BufferedOp{Key: []byte(k), Value: []byte(v), Deleted: del}
	}
	t.Run("distinct keys are returned as is", func(t *testing.T) {
		ops := []storage.BufferedOp{op("a", "1", false), op("b", "", true)}
		got := lastOpPerKey(ops)
		if len(got) != 2 || &got[0] != &ops[0] {
			t.Fatalf("lastOpPerKey copied or changed a batch with no repeated key: %v", got)
		}
	})
	t.Run("a repeated key keeps only its last operation", func(t *testing.T) {
		ops := []storage.BufferedOp{
			op("a", "1", false), op("b", "1", false), op("a", "", true), op("c", "1", false), op("b", "2", false),
		}
		got := lastOpPerKey(ops)
		want := []storage.BufferedOp{op("a", "", true), op("c", "1", false), op("b", "2", false)}
		if len(got) != len(want) {
			t.Fatalf("lastOpPerKey = %v, want %v", got, want)
		}
		for i := range want {
			if !bytes.Equal(got[i].Key, want[i].Key) || !bytes.Equal(got[i].Value, want[i].Value) || got[i].Deleted != want[i].Deleted {
				t.Fatalf("lastOpPerKey[%d] = %+v, want %+v", i, got[i], want[i])
			}
		}
	})
}
