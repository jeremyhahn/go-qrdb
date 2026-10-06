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

	// Simulate a crash on an already-initialised data directory: the format
	// marker was committed in an earlier (crash-free) Open, and a batch
	// commit got as far as publishing its journal before the process died.
	if err := os.WriteFile(filepath.Join(dir, formatMarkerFileName), []byte(formatVersion), 0o600); err != nil {
		t.Fatal(err)
	}

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
	// As in TestOpen_RecoversPublishedJournal: the directory was already
	// initialised before the crash that left this corrupt journal behind.
	if err := os.WriteFile(filepath.Join(dir, formatMarkerFileName), []byte(formatVersion), 0o600); err != nil {
		t.Fatal(err)
	}
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

// TestSet_LeavesNoTempFiles verifies the atomic-rename write path cleans up
// after itself and Sync succeeds. The format marker (a permanent,
// legitimate dot-prefixed file, not a leftover) and the record itself
// (named by its key's hash, not dot-prefixed) are the only two files this
// should find; anything else dot-prefixed is a stray ".tmp-set-*" or
// ".tmp-journal-*" artifact.
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

	walkErr := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		name := info.Name()
		if name == formatMarkerFileName || isHashFileName(name) {
			return nil
		}
		t.Errorf("temp artifact %q left behind", path)
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
}

// TestWriteJournal_CreateTempErrorPropagates covers writeJournal's first
// error path directly: the engine root cannot be written into.
func TestWriteJournal_CreateTempErrorPropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission denial cannot be exercised")
	}
	dir := t.TempDir()
	e, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	if err := os.Chmod(dir, 0o500); err != nil { // #nosec G302 -- r-x: the directory's own permission bits are the fault this negative-permission test injects, not a secret file's mode
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // #nosec G302 -- restores the mode t.TempDir() needs to remove the directory; not a secret file's mode

	err = e.writeJournal([]byte("x"))
	var jwe *JournalWriteError
	if !errors.As(err, &jwe) {
		t.Fatalf("writeJournal = %v, want *JournalWriteError", err)
	}
}

// TestClearJournal_RemoveErrorPropagates covers clearJournal's error path:
// a committed journal is present, but the engine root denies write, so
// removing it fails for a reason other than "already gone".
func TestClearJournal_RemoveErrorPropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission denial cannot be exercised")
	}
	dir := t.TempDir()
	e, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	if err := os.WriteFile(filepath.Join(dir, journalFileName), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { // #nosec G302 -- r-x: the directory's own permission bits are the fault this negative-permission test injects, not a secret file's mode
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // #nosec G302 -- restores the mode t.TempDir() needs to remove the directory; not a secret file's mode

	err = e.clearJournal()
	var jwe *JournalWriteError
	if !errors.As(err, &jwe) {
		t.Fatalf("clearJournal = %v, want *JournalWriteError", err)
	}
}
