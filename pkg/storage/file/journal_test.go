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
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// TestBatchCommit_AppliesAllAndClearsJournal pins the happy path of the
// journaled commit: every op lands and no journal or temp file survives.
func TestBatchCommit_AppliesAllAndClearsJournal(t *testing.T) {
	dir := t.TempDir()
	e, err := New(dir)
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
	if _, err := os.Stat(filepath.Join(dir, journalFileName)); !os.IsNotExist(err) {
		t.Fatal("journal not cleared after a successful commit")
	}

	// No dot-prefixed artifacts leak into key listings.
	keys, err := e.List(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("List = %d keys (%q), want 2", len(keys), keys)
	}
}

// TestOpen_RecoversPublishedJournal simulates a crash between journal
// publish and apply: a complete journal exists but none of its files do.
// Open must replay the whole batch and clear the journal.
func TestOpen_RecoversPublishedJournal(t *testing.T) {
	dir := t.TempDir()

	ops := []storage.BufferedOp{
		{Key: []byte("r1"), Value: []byte("v1")},
		{Key: []byte("dir/r2"), Value: []byte("v2")},
		{Key: []byte("gone"), Deleted: true},
	}
	if err := os.WriteFile(filepath.Join(dir, journalFileName), encodeJournal(ops), 0o600); err != nil {
		t.Fatal(err)
	}
	// A stray temp journal from a never-published attempt must be swept.
	if err := os.WriteFile(filepath.Join(dir, ".tmp-journal-123"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}

	e, err := New(dir)
	if err != nil {
		t.Fatalf("New with published journal: %v", err)
	}
	defer func() { _ = e.Close() }()

	for key, want := range map[string]string{"r1": "v1", "dir/r2": "v2"} {
		got, err := e.Get([]byte(key))
		if err != nil || string(got) != want {
			t.Fatalf("recovered Get(%q) = %q, %v; want %q", key, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, journalFileName)); !os.IsNotExist(err) {
		t.Fatal("journal not cleared after recovery")
	}
	if _, err := os.Stat(filepath.Join(dir, ".tmp-journal-123")); !os.IsNotExist(err) {
		t.Fatal("stray temp journal not swept at open")
	}
}

// TestOpen_CorruptJournalFailsClosed pins the fail-closed contract: a
// checksum-failing journal means the last batch is in an unknown state,
// and the open must refuse rather than serve it.
func TestOpen_CorruptJournalFailsClosed(t *testing.T) {
	dir := t.TempDir()
	image := encodeJournal([]storage.BufferedOp{{Key: []byte("k"), Value: []byte("v")}})
	image[len(image)-1] ^= 0xff // break the CRC
	if err := os.WriteFile(filepath.Join(dir, journalFileName), image, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := New(dir)
	var corrupt *JournalCorruptError
	if !errors.As(err, &corrupt) {
		t.Fatalf("New = %v, want *JournalCorruptError", err)
	}
}

// TestJournalRoundTrip pins the encode/decode bijection including empty
// values and delete records.
func TestJournalRoundTrip(t *testing.T) {
	ops := []storage.BufferedOp{
		{Key: []byte("a"), Value: []byte("1")},
		{Key: []byte("empty"), Value: []byte{}},
		{Key: []byte{0x00, 0xff, '/'}, Value: []byte("bin")},
		{Key: []byte("del"), Deleted: true},
	}
	got, err := decodeJournal(encodeJournal(ops))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(ops) {
		t.Fatalf("round trip length %d, want %d", len(got), len(ops))
	}
	for i := range ops {
		if string(got[i].Key) != string(ops[i].Key) ||
			string(got[i].Value) != string(ops[i].Value) ||
			got[i].Deleted != ops[i].Deleted {
			t.Fatalf("op %d round trip mismatch: %+v vs %+v", i, got[i], ops[i])
		}
	}
}

// TestDecodeJournal_RejectsGarbage covers the header and truncation
// paths.
func TestDecodeJournal_RejectsGarbage(t *testing.T) {
	for name, data := range map[string][]byte{
		"empty":      {},
		"bad-magic":  []byte("NOTAJRNL\x00\x00\x00\x00"),
		"only-magic": []byte(journalMagic),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeJournal(data); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

// TestSet_LeavesNoTempFiles verifies the atomic-rename write path cleans
// up after itself and Sync succeeds.
func TestSet_LeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	e, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	if err := e.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := e.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range entries {
		if ent.Name()[0] == '.' {
			t.Fatalf("temp artifact %q left behind", ent.Name())
		}
	}
}
