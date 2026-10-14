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

package objstore

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// TestBatchCommit_AppliesAllAndClearsJournal pins the happy path of the
// journaled commit: every op lands and no journal object survives.
func TestBatchCommit_AppliesAllAndClearsJournal(t *testing.T) {
	settings := map[string]string{"path": t.TempDir()}
	e, err := New("local", settings)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	if err := e.Set([]byte("stale"), []byte("x")); err != nil {
		t.Fatal(err)
	}

	b := e.Batch()
	if err := b.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := b.Set([]byte("nested/b"), []byte("2")); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete([]byte("stale")); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	for key, want := range map[string]string{"a": "1", "nested/b": "2"} {
		got, err := e.Get([]byte(key))
		if err != nil || string(got) != want {
			t.Fatalf("Get(%q) = %q, %v; want %q", key, got, err, want)
		}
	}
	if v, _ := e.Get([]byte("stale")); v != nil {
		t.Fatal("deleted key survived the batch")
	}
	if _, err := e.journal.Get(journalName); !notFound(err) {
		t.Fatalf("journal not cleared after a successful commit (err=%v)", err)
	}
}

// TestOpen_RecoversPublishedJournal is the first half of Lane 1A's
// acceptance criterion 3 (docs/databases/engineering.md, "Target: phased
// implementation plan"): a commit interrupted AFTER the journal object is
// durable is replayed in full at open. It simulates a crash between journal
// publish and apply by writing the journal object directly, bypassing
// commitBufferedOps's apply step entirely, then reopening.
func TestOpen_RecoversPublishedJournal(t *testing.T) {
	settings := map[string]string{"path": t.TempDir()}

	// Open once to establish the format marker on an empty store.
	e, err := New("local", settings)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ops := []storage.BufferedOp{
		{Key: []byte("r1"), Value: []byte("v1")},
		{Key: []byte("dir/r2"), Value: []byte("v2")},
	}
	if err := e.writeJournal(encodeJournal(ops)); err != nil {
		t.Fatalf("writeJournal: %v", err)
	}
	_ = e.Close() // this Engine's in-process view is now stale; discard it.

	// Reopen against the same provider settings: Open-time recovery must
	// replay the whole batch and clear the journal.
	e2, err := New("local", settings)
	if err != nil {
		t.Fatalf("New with a published journal: %v", err)
	}
	defer func() { _ = e2.Close() }()

	for key, want := range map[string]string{"r1": "v1", "dir/r2": "v2"} {
		got, err := e2.Get([]byte(key))
		if err != nil || string(got) != want {
			t.Fatalf("recovered Get(%q) = %q, %v; want %q", key, got, err, want)
		}
	}
	if _, err := e2.journal.Get(journalName); !notFound(err) {
		t.Fatalf("journal not cleared after recovery (err=%v)", err)
	}
}

// TestOpen_JournalCorruptFailsOpen: a journal object that exists but does
// not decode must fail Open rather than silently serve a half-applied
// batch -- the store's last commit is in an unknown state.
func TestOpen_JournalCorruptFailsOpen(t *testing.T) {
	settings := map[string]string{"path": t.TempDir()}
	e, err := New("local", settings)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := e.writeJournal([]byte("not a journal image")); err != nil {
		t.Fatalf("writeJournal: %v", err)
	}
	_ = e.Close()

	_, err = New("local", settings)
	var corrupt *JournalCorruptError
	if !errors.As(err, &corrupt) {
		t.Fatalf("New over a corrupt journal: err = %v (%T), want *JournalCorruptError", err, err)
	}
}

// TestOpen_NoJournalMeansBatchNeverHappened is the second half of
// acceptance criterion 3: a commit interrupted BEFORE the journal object is
// durable -- so the process died with the batch only buffered in memory,
// never published -- leaves no trace: at reopen, every op in that batch is
// absent.
func TestOpen_NoJournalMeansBatchNeverHappened(t *testing.T) {
	settings := map[string]string{"path": t.TempDir()}
	e, err := New("local", settings)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	b := e.Batch()
	if err := b.Set([]byte("never-committed"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	// The process "dies" here: Commit is never called, so writeJournal never
	// ran and nothing was published.
	_ = b.Close()
	_ = e.Close()

	e2, err := New("local", settings)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = e2.Close() }()

	if v, _ := e2.Get([]byte("never-committed")); v != nil {
		t.Fatalf("a never-committed batch's key is present after reopen: %q", v)
	}
	if _, err := e2.journal.Get(journalName); !notFound(err) {
		t.Fatalf("no journal was ever published, but one is present at reopen (err=%v)", err)
	}
}
