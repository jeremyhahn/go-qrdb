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
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// ---------------------------------------------------------------------------
// Set — the second Set into an already-durable shard directory skips
// mkdirAllDurable (ensureShardDirDurable's memoization) and reaches
// os.CreateTemp directly, so this is the only way to fail CreateTemp without
// also failing the earlier mkdirAllDurable check.
// ---------------------------------------------------------------------------

// TestSet_CreateTempErrorPropagates covers Set's own os.CreateTemp error
// path, distinct from TestSet_MkdirAllError which fails one step earlier.
func TestSet_CreateTempErrorPropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission denial cannot be exercised")
	}
	e, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	key := []byte("k")
	if err := e.Set(key, []byte("v1")); err != nil {
		t.Fatalf("seed Set: %v", err)
	}
	shardDir, _ := e.recordPaths(key)
	if err := os.Chmod(shardDir, 0o500); err != nil { // #nosec G302 -- r-x: the directory's own permission bits are the fault this negative-permission test injects, not a secret file's mode
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(shardDir, 0o700) }) // #nosec G302 -- restores the mode t.TempDir() needs to remove the directory; not a secret file's mode

	if err := e.Set(key, []byte("v2")); err == nil {
		t.Fatal("expected Set to fail creating a temp file in a read-only shard directory")
	}
}

// ---------------------------------------------------------------------------
// Get/List/iterator — all three decode a record file they found by name,
// and all three must propagate a non-ENOENT read failure or a corrupt
// record the same way.
// ---------------------------------------------------------------------------

// TestGet_DecodeRecordErrorPropagates covers Get's decodeRecord error path:
// a file sitting at the exact hash-named path a key resolves to, but whose
// content is too short to be a valid record.
func TestGet_DecodeRecordErrorPropagates(t *testing.T) {
	e, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	key := []byte("corrupt")
	dir, path := e.recordPaths(key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{0, 0}, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := e.Get(key); err == nil {
		t.Fatal("expected Get to fail decoding a corrupt record")
	}
}

// TestList_ReadFileErrorPropagates covers List's non-ENOENT os.ReadFile
// branch: the shard directory is listable, but the record file itself
// denies read.
func TestList_ReadFileErrorPropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission denial cannot be exercised")
	}
	e, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	key := []byte("k")
	if err := e.Set(key, []byte("v")); err != nil {
		t.Fatal(err)
	}
	_, path := e.recordPaths(key)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	if _, err := e.List(nil); err == nil {
		t.Fatal("expected List to fail reading a permission-denied record")
	}
}

// TestList_DecodeRecordErrorPropagates covers List's decodeRecord error
// path via a corrupt record discovered during the shard walk.
func TestList_DecodeRecordErrorPropagates(t *testing.T) {
	e, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	dir, path := e.recordPaths([]byte("corrupt"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{0, 0}, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := e.List(nil); err == nil {
		t.Fatal("expected List to fail decoding a corrupt record")
	}
}

// TestIterator_ReadFileErrorPropagates is TestList_ReadFileErrorPropagates
// for the iterator's own load, surfaced through Error().
func TestIterator_ReadFileErrorPropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission denial cannot be exercised")
	}
	e, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	key := []byte("k")
	if err := e.Set(key, []byte("v")); err != nil {
		t.Fatal(err)
	}
	_, path := e.recordPaths(key)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	it := e.NewIterator(nil)
	defer func() { _ = it.Close() }()
	for it.SeekToFirst(); it.Valid(); it.Next() {
	}
	if err := it.Error(); err == nil {
		t.Fatal("expected the iterator to report a permission error reading the record")
	}
}

// TestIterator_DecodeRecordErrorPropagates is TestList_DecodeRecordErrorPropagates
// for the iterator's own load, surfaced through Error().
func TestIterator_DecodeRecordErrorPropagates(t *testing.T) {
	e, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	dir, path := e.recordPaths([]byte("corrupt"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{0, 0}, 0o600); err != nil {
		t.Fatal(err)
	}

	it := e.NewIterator(nil)
	defer func() { _ = it.Close() }()
	for it.SeekToFirst(); it.Valid(); it.Next() {
	}
	if err := it.Error(); err == nil {
		t.Fatal("expected the iterator to report a corrupt-record error")
	}
}

// ---------------------------------------------------------------------------
// New — cleanupTempSetFiles's own error must propagate through New's wrapping
// check, not merely through a direct call to cleanupTempSetFiles.
// ---------------------------------------------------------------------------

// TestNew_CleanupTempSetFilesErrorPropagates covers New's own error check
// around cleanupTempSetFiles, on a SECOND Open of an already-initialised
// directory whose shard directory has since become unreadable.
func TestNew_CleanupTempSetFilesErrorPropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission denial cannot be exercised")
	}
	dir := t.TempDir()
	e, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	shardDir, _ := e.recordPaths([]byte("k"))
	if err := os.MkdirAll(shardDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(shardDir, ".tmp-set-stray")
	if err := os.WriteFile(stray, []byte("leftover"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shardDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(shardDir, 0o700) }) // #nosec G302 -- restores the mode t.TempDir() needs to remove the directory; not a secret file's mode

	if _, err := New(dir); err == nil {
		t.Fatal("expected New to fail cleaning up temp set files in an unreadable shard directory")
	}
}

// ---------------------------------------------------------------------------
// ensureFormatMarker/recoverJournal — called directly on a bare *Engine, the
// same white-box pattern the package's other direct-call tests already use,
// to reach their OWN os.ReadDir failure distinctly from New's earlier calls
// that would otherwise fail first.
// ---------------------------------------------------------------------------

// TestEnsureFormatMarker_ReadDirErrorPropagates covers ensureFormatMarker's
// first os.ReadDir error path.
func TestEnsureFormatMarker_ReadDirErrorPropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission denial cannot be exercised")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // #nosec G302 -- restores the mode t.TempDir() needs to remove the directory; not a secret file's mode

	e := &Engine{dir: dir}
	if err := e.ensureFormatMarker(); err == nil {
		t.Fatal("expected a permission error reading the engine directory")
	}
}

// TestRecoverJournal_ReadDirErrorPropagates covers recoverJournal's own
// os.ReadDir error path.
func TestRecoverJournal_ReadDirErrorPropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission denial cannot be exercised")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // #nosec G302 -- restores the mode t.TempDir() needs to remove the directory; not a secret file's mode

	e := &Engine{dir: dir}
	if err := e.recoverJournal(); err == nil {
		t.Fatal("expected a permission error reading the engine directory")
	}
}

// TestRecoverJournal_ReadFileErrorPropagates covers recoverJournal's
// non-ENOENT os.ReadFile branch: the engine root is listable, but the
// committed journal file itself denies read.
func TestRecoverJournal_ReadFileErrorPropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission denial cannot be exercised")
	}
	dir := t.TempDir()
	journalPath := filepath.Join(dir, journalFileName)
	if err := os.WriteFile(journalPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(journalPath, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(journalPath, 0o600) })

	e := &Engine{dir: dir}
	if err := e.recoverJournal(); err == nil {
		t.Fatal("expected a permission error reading the journal file")
	}
}

// TestRecoverJournal_ApplyBufferedOpsErrorPropagates covers recoverJournal's
// applyBufferedOps error path: a committed journal whose single op is a Set
// with an empty key, which checkKey refuses deterministically.
func TestRecoverJournal_ApplyBufferedOpsErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	ops := []storage.BufferedOp{{Key: []byte{}, Value: []byte("v")}}
	if err := os.WriteFile(filepath.Join(dir, journalFileName), encodeJournal(ops), 0o600); err != nil {
		t.Fatal(err)
	}

	e := &Engine{dir: dir}
	if err := e.recoverJournal(); err == nil {
		t.Fatal("expected the empty-key refusal from applyBufferedOps to propagate")
	}
}

// ---------------------------------------------------------------------------
// decodeJournal — TestDecodeJournal_RejectsGarbage only reaches the header
// check (missing/short magic). None of those cases are long enough to enter
// the per-record loop, so every truncation INSIDE a record, and the
// unknown-tag branch, were unexercised.
// ---------------------------------------------------------------------------

func sealedJournalBody(body []byte) []byte {
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc32.Checksum(body, castagnoli))
	return append(append([]byte{}, body...), sum[:]...)
}

func TestDecodeJournal_RejectsTruncatedRecords(t *testing.T) {
	be32 := func(v uint32) []byte {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], v)
		return b[:]
	}

	cases := map[string][]byte{
		"truncated key length": append(
			append([]byte(journalMagic), journalOpSet),
			0, 0, // 2 bytes, short of the 4-byte key-length field
		),
		"truncated key": append(
			append(append([]byte(journalMagic), journalOpSet), be32(10)...),
			[]byte("abc")..., // 3 bytes, short of the declared 10-byte key
		),
		"truncated value length": append(
			append(append(append([]byte(journalMagic), journalOpSet), be32(1)...), 'k'),
			0, 0, // 2 bytes, short of the 4-byte value-length field
		),
		"truncated value": append(
			append(append(append([]byte(journalMagic), journalOpSet), be32(1)...), 'k'),
			append(be32(5), []byte("ab")...)..., // 2 bytes, short of the declared 5-byte value
		),
		"unknown record tag": append(
			append(append([]byte(journalMagic), byte(99)), be32(1)...),
			'k',
		),
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeJournal(sealedJournalBody(body)); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// commitBufferedOps — the journal-write failure path, reached only through a
// real Batch.Commit against a read-only engine root.
// ---------------------------------------------------------------------------

// TestCommitBufferedOps_WriteJournalErrorPropagates covers commitBufferedOps'
// own error return when writeJournal fails, distinct from
// TestWriteJournal_CreateTempErrorPropagates which calls writeJournal
// directly rather than through a batch commit.
func TestCommitBufferedOps_WriteJournalErrorPropagates(t *testing.T) {
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
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // #nosec G302 -- restores the mode t.TempDir() needs to remove the directory; not a secret file's mode // #nosec G302 -- restores the mode t.TempDir() needs to remove the directory; not a secret file's mode

	b := e.Batch()
	if err := b.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(); err == nil {
		t.Fatal("expected Commit to fail writing the journal into a read-only engine root")
	}
}
