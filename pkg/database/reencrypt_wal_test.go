// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb/waldb"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// newNoneBarrier returns an initialized+unsealed none-strategy StorageBarrier.
func newNoneBarrier(t *testing.T, rootKeyPath string) *seal.StorageBarrier {
	t.Helper()
	b, err := seal.NewStorageBarrier(reencTestLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: rootKeyPath, DeploymentID: 1}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier(none): %v", err)
	}
	if err := b.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("Initialize(none): %v", err)
	}
	return b
}

// newKeyedBarrier returns an initialized+unsealed passphrase StorageBarrier.
func newKeyedBarrier(t *testing.T, rootKeyPath, passphrase string) *seal.StorageBarrier {
	t.Helper()
	strategy, err := seal.NewSoftwareStrategy([]byte(passphrase))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	b, err := seal.NewStorageBarrier(reencTestLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: rootKeyPath, DeploymentID: 1}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier(keyed): %v", err)
	}
	if err := b.Initialize(context.Background(), seal.MustCredentials([]byte(passphrase))); err != nil {
		t.Fatalf("Initialize(keyed): %v", err)
	}
	return b
}

// writeWALNode opens a waldb at dir through the barrier and writes one node's
// bootstrap + hard state + a run of log entries.
func writeWALNode(t *testing.T, dir string, b *seal.StorageBarrier, shardID, replicaID uint64, n int) {
	t.Helper()
	db, err := waldb.Open(dir, waldb.WithBarrier(b.EpochBarrier()))
	if err != nil {
		t.Fatalf("waldb.Open(%s): %v", dir, err)
	}
	if err := db.SaveBootstrap(shardID, replicaID, logdb.Bootstrap{
		Addresses: map[uint64]string{replicaID: "127.0.0.1:1"}, Join: false, Type: 7,
	}); err != nil {
		t.Fatalf("SaveBootstrap: %v", err)
	}
	entries := make([]logdb.Entry, 0, n)
	for i := 0; i < n; i++ {
		entries = append(entries, logdb.Entry{Index: uint64(i + 1), Term: 1, Cmd: []byte("wal-payload")})
	}
	if err := db.SaveState([]logdb.Update{{
		ShardID: shardID, ReplicaID: replicaID,
		State:   logdb.State{Term: 1, Vote: replicaID, Commit: uint64(n)},
		Entries: entries,
	}}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("waldb.Close: %v", err)
	}
}

// TestReencryptWALStore_NoneToKeyed proves the WAL re-encryption wrapper
// round-trips a Raft write-ahead log from a none barrier into a keyed barrier
// with every entry preserved, and that the result is genuinely encrypted.
func TestReencryptWALStore_NoneToKeyed(t *testing.T) {
	const shardID, replicaID = uint64(1), uint64(1)
	none := newNoneBarrier(t, "wal-none/root-key")
	keyed := newKeyedBarrier(t, "wal-keyed/root-key", "wal-pass")

	srcDir := filepath.Join(t.TempDir(), "wal")
	dstDir := filepath.Join(t.TempDir(), "wal.keyed")

	writeWALNode(t, srcDir, none, shardID, replicaID, 12)

	count, _, err := ReencryptWALStore(srcDir, none, dstDir, keyed, snapSibling)
	if err != nil {
		t.Fatalf("ReencryptWALStore: %v", err)
	}
	if count != 1 {
		t.Fatalf("re-encrypted %d nodes, want 1", count)
	}

	// Read back through the keyed barrier — entries must match.
	dst, err := waldb.Open(dstDir, waldb.WithBarrier(keyed.EpochBarrier()))
	if err != nil {
		t.Fatalf("open keyed WAL: %v", err)
	}
	first, length, _ := dst.EntryRange(shardID, replicaID)
	if length != 12 {
		_ = dst.Close()
		t.Fatalf("entry count = %d, want 12", length)
	}
	got, _, eerr := dst.IterateEntries(nil, 0, shardID, replicaID, first, first+length, 1<<30)
	_ = dst.Close()
	if eerr != nil {
		t.Fatalf("IterateEntries: %v", eerr)
	}
	if len(got) != 12 {
		t.Fatalf("iterated %d entries, want 12", len(got))
	}

	// Genuinely encrypted: the none barrier cannot open the keyed WAL.
	if probe, perr := waldb.Open(dstDir, waldb.WithBarrier(none.EpochBarrier())); perr == nil {
		_ = probe.Close()
		t.Fatal("none barrier opened the keyed WAL — not encrypted")
	}
}

// TestReencryptWALStore_PlaintextSides covers the nil-barrier (plaintext) paths
// of the wrapper: a nil source and/or nil destination barrier.
func TestReencryptWALStore_PlaintextSides(t *testing.T) {
	const shardID, replicaID = uint64(2), uint64(1)
	srcDir := filepath.Join(t.TempDir(), "wal")
	dstDir := filepath.Join(t.TempDir(), "wal.plain")

	// Write a plaintext WAL (nil barrier) by opening with no barrier option.
	db, err := waldb.Open(srcDir)
	if err != nil {
		t.Fatalf("waldb.Open(plaintext): %v", err)
	}
	if err := db.SaveState([]logdb.Update{{
		ShardID: shardID, ReplicaID: replicaID,
		State:   logdb.State{Term: 1, Vote: replicaID, Commit: 3},
		Entries: []logdb.Entry{{Index: 1, Term: 1, Cmd: []byte("a")}, {Index: 2, Term: 1, Cmd: []byte("b")}, {Index: 3, Term: 1, Cmd: []byte("c")}},
	}}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	_ = db.Close()

	// nil source + nil destination barrier = plaintext-to-plaintext copy.
	count, _, err := ReencryptWALStore(srcDir, nil, dstDir, nil, snapSibling)
	if err != nil {
		t.Fatalf("ReencryptWALStore(nil,nil): %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

// snapSibling stages a re-encrypted snapshot data file beside its original.
func snapSibling(snapDir string) string {
	return filepath.Join(snapDir, SnapshotDataFile) + snapshotKeyedSuffix
}

// TestReencryptWALStore_ReencryptsSnapshotData proves the WAL re-encryption
// re-keys a node's snapshot DATA FILE: a snapshot.dat framed under barrier A is
// staged re-encrypted under barrier B, committed into place, and then recovers
// the original bytes under B (and not under A) — with the source left intact
// until commit. This is the snapshot half of an enable-encryption migration.
func TestReencryptWALStore_ReencryptsSnapshotData(t *testing.T) {
	const shardID, replicaID = uint64(3), uint64(1)
	barA := newKeyedBarrier(t, "snapA/root-key", "pass-A")
	barB := newKeyedBarrier(t, "snapB/root-key", "pass-B")

	base := t.TempDir()
	srcDir := filepath.Join(base, "wal")
	dstDir := filepath.Join(base, "wal.keyed")

	// A snapshot directory with snapshot.dat framed under barrier A (seeded from
	// plaintext via the public re-encrypt primitive: plaintext -> A).
	snapDir := filepath.Join(base, "snapshots", "shard-3", "replica-1", "snapshot-1")
	if err := os.MkdirAll(snapDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// snapshotIndex is the log index this snapshot is taken at, recorded by the
	// SaveSnapshot call below. It is part of the frame AAD, so every re-encrypt
	// of this file — seeding it and reading it back — must present the same
	// (shard, replica, index) triple or the frames will not authenticate.
	const snapshotIndex = uint64(5)

	plain := []byte("the-serialized-state-machine-bytes-0123456789-abcdef")
	plainPath := filepath.Join(base, "plain.bin")
	if err := os.WriteFile(plainPath, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	snapDat := filepath.Join(snapDir, SnapshotDataFile)
	if _, err := quicraft.ReencryptSnapshotFile(plainPath, snapDat,
		shardID, replicaID, snapshotIndex, nil, barA.EpochBarrier()); err != nil {
		t.Fatalf("seed snapshot.dat under A: %v", err)
	}
	fi, _ := os.Stat(snapDat)

	// A WAL node under A whose snapshot metadata points at snapDir.
	db, err := waldb.Open(srcDir, waldb.WithBarrier(barA.EpochBarrier()))
	if err != nil {
		t.Fatalf("open src wal: %v", err)
	}
	if err := db.SaveBootstrap(shardID, replicaID, logdb.Bootstrap{
		Addresses: map[uint64]string{replicaID: "127.0.0.1:1"}, Type: 7,
	}); err != nil {
		t.Fatalf("SaveBootstrap: %v", err)
	}
	if err := db.SaveSnapshot(shardID, replicaID, logdb.Snapshot{
		Index: snapshotIndex, Term: 1, Filepath: snapDir, FileSize: uint64(fi.Size()),
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := db.SaveState([]logdb.Update{{
		ShardID: shardID, ReplicaID: replicaID,
		State:   logdb.State{Term: 1, Vote: replicaID, Commit: 5},
		Entries: []logdb.Entry{{Index: 6, Term: 1, Cmd: []byte("x")}},
	}}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	_ = db.Close()

	// Re-encrypt the WAL A -> B; the snapshot.dat is staged as a sibling.
	n, staged, err := ReencryptWALStore(srcDir, barA, dstDir, barB, snapSibling)
	if err != nil {
		t.Fatalf("ReencryptWALStore: %v", err)
	}
	if n != 1 {
		t.Fatalf("nodes = %d, want 1", n)
	}
	if len(staged) != 1 {
		t.Fatalf("staged snapshots = %d, want 1", len(staged))
	}
	if _, serr := os.Stat(staged[0].KeyedPath); serr != nil {
		t.Fatalf("staged keyed snapshot missing: %v", serr)
	}
	// Source untouched before commit.
	if _, serr := os.Stat(snapDat); serr != nil {
		t.Fatalf("source snapshot.dat removed before commit: %v", serr)
	}

	// Commit: the keyed sibling replaces the original snapshot.dat.
	if cerr := CommitStagedSnapshots(staged); cerr != nil {
		t.Fatalf("CommitStagedSnapshots: %v", cerr)
	}
	if _, serr := os.Stat(staged[0].KeyedPath); !os.IsNotExist(serr) {
		t.Fatal("staged sibling not consumed by commit")
	}

	// The committed snapshot.dat recovers the original under B (decrypt B -> plaintext).
	recovered := filepath.Join(base, "recovered.bin")
	if _, derr := quicraft.ReencryptSnapshotFile(snapDat, recovered,
		shardID, replicaID, snapshotIndex, barB.EpochBarrier(), nil); derr != nil {
		t.Fatalf("committed snapshot not readable under B: %v", derr)
	}
	if got := mustReadFile(t, recovered); got != string(plain) {
		t.Fatal("recovered snapshot data does not match original (DATA LOSS)")
	}

	// Barrier A must NOT recover B's committed snapshot.
	bad := filepath.Join(base, "bad.bin")
	if _, derr := quicraft.ReencryptSnapshotFile(snapDat, bad,
		shardID, replicaID, snapshotIndex, barA.EpochBarrier(), nil); derr == nil {
		if got, _ := os.ReadFile(bad); string(got) == string(plain) {
			t.Fatal("barrier A decrypted barrier B's committed snapshot — keys not isolated")
		}
	}

	// Commit is idempotent for crash recovery: a second call is a no-op.
	if cerr := CommitStagedSnapshots(staged); cerr != nil {
		t.Fatalf("idempotent CommitStagedSnapshots: %v", cerr)
	}
}

// TestSwapStoreDir_And_RemoveStoreBackup exercises the exported atomic swap and
// backup-cleanup helpers, including the idempotent recovery no-op.
func TestSwapStoreDir_And_RemoveStoreBackup(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "store")
	keyed := dir + KeyedDirSuffix
	backup := dir + BackupDirSuffix

	mustWriteFile(t, filepath.Join(mustMkdir(t, dir), "marker"), "original")
	mustWriteFile(t, filepath.Join(mustMkdir(t, keyed), "marker"), "keyed")

	// Swap: keyed -> dir, original -> backup.
	if err := SwapStoreDir(dir); err != nil {
		t.Fatalf("SwapStoreDir: %v", err)
	}
	if got := mustReadFile(t, filepath.Join(dir, "marker")); got != "keyed" {
		t.Fatalf("after swap dir/marker = %q, want keyed", got)
	}
	if got := mustReadFile(t, filepath.Join(backup, "marker")); got != "original" {
		t.Fatalf("backup/marker = %q, want original", got)
	}
	if _, err := os.Stat(keyed); !os.IsNotExist(err) {
		t.Fatalf("keyed dir should be gone after swap")
	}

	// Idempotent: a second swap is a no-op (keyed copy absent), data intact.
	if err := SwapStoreDir(dir); err != nil {
		t.Fatalf("SwapStoreDir (idempotent): %v", err)
	}
	if got := mustReadFile(t, filepath.Join(dir, "marker")); got != "keyed" {
		t.Fatalf("after idempotent swap dir/marker = %q, want keyed", got)
	}

	// RemoveStoreBackup clears the backup.
	RemoveStoreBackup(dir)
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("backup dir should be removed")
	}
}

func mustMkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	return dir
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestSnapshotDataAction covers the classifier that decides whether a
// snapshot's data file is re-encrypted, skipped (genuine metadata-only), or
// must abort the migration (unreadable/malformed). It is the unit guard for the
// fail-closed fix: a missing data file is the ONLY skip condition.
func TestSnapshotDataAction(t *testing.T) {
	dir := t.TempDir()

	// Absent file → genuine metadata-only snapshot: skip, no error.
	absent := filepath.Join(dir, "absent", SnapshotDataFile)
	if reenc, err := snapshotDataAction(absent); reenc || err != nil {
		t.Fatalf("absent: got (%v,%v), want (false,nil)", reenc, err)
	}

	// Present, non-empty → re-encrypt.
	good := filepath.Join(dir, "good.dat")
	mustWriteFile(t, good, "framed-snapshot-bytes")
	if reenc, err := snapshotDataAction(good); !reenc || err != nil {
		t.Fatalf("regular: got (%v,%v), want (true,nil)", reenc, err)
	}

	// Present, empty → abort (corrupt/interrupted write), fail-closed.
	empty := filepath.Join(dir, "empty.dat")
	mustWriteFile(t, empty, "")
	reenc, err := snapshotDataAction(empty)
	if reenc {
		t.Fatal("empty data file must not be re-encrypted")
	}
	if !errors.Is(err, ErrEmptySnapshotData) {
		t.Fatalf("empty: err = %v, want ErrEmptySnapshotData", err)
	}
	var se *SnapshotStageError
	if !errors.As(err, &se) {
		t.Fatalf("empty: err = %v, want *SnapshotStageError", err)
	}

	// Present, but a directory → abort (not a regular framed file).
	asDir := filepath.Join(dir, "asdir.dat")
	mustMkdir(t, asDir)
	if reenc, err := snapshotDataAction(asDir); reenc || !errors.Is(err, ErrSnapshotDataNotRegular) {
		t.Fatalf("dir: got (%v,%v), want (false, ErrSnapshotDataNotRegular)", reenc, err)
	}
}

// TestReencryptWALStore_MetadataOnlySnapshot_Skipped proves a snapshot whose
// metadata exists but has NO data file (an on-disk state machine) is handled
// without error and produces no staged snapshot — the legitimate skip path.
func TestReencryptWALStore_MetadataOnlySnapshot_Skipped(t *testing.T) {
	const shardID, replicaID = uint64(7), uint64(1)
	barA := newKeyedBarrier(t, "metaA/root-key", "pass-A")
	barB := newKeyedBarrier(t, "metaB/root-key", "pass-B")

	base := t.TempDir()
	srcDir := filepath.Join(base, "wal")
	dstDir := filepath.Join(base, "wal.keyed")

	// Snapshot directory exists but has NO snapshot.dat (metadata-only).
	snapDir := filepath.Join(base, "snapshots", "shard-7", "replica-1", "snapshot-1")
	if err := os.MkdirAll(snapDir, 0o750); err != nil {
		t.Fatal(err)
	}

	db, err := waldb.Open(srcDir, waldb.WithBarrier(barA.EpochBarrier()))
	if err != nil {
		t.Fatalf("open src wal: %v", err)
	}
	if err := db.SaveBootstrap(shardID, replicaID, logdb.Bootstrap{
		Addresses: map[uint64]string{replicaID: "127.0.0.1:1"}, Type: 7,
	}); err != nil {
		t.Fatalf("SaveBootstrap: %v", err)
	}
	if err := db.SaveSnapshot(shardID, replicaID, logdb.Snapshot{
		Index: 5, Term: 1, Filepath: snapDir, FileSize: 0,
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := db.SaveState([]logdb.Update{{
		ShardID: shardID, ReplicaID: replicaID,
		State:   logdb.State{Term: 1, Vote: replicaID, Commit: 5},
		Entries: []logdb.Entry{{Index: 6, Term: 1, Cmd: []byte("x")}},
	}}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	_ = db.Close()

	n, staged, err := ReencryptWALStore(srcDir, barA, dstDir, barB, snapSibling)
	if err != nil {
		t.Fatalf("ReencryptWALStore (metadata-only): %v", err)
	}
	if n != 1 {
		t.Fatalf("nodes = %d, want 1", n)
	}
	if len(staged) != 0 {
		t.Fatalf("staged = %d, want 0 (metadata-only has no data file)", len(staged))
	}
}

// TestReencryptWALStore_EmptySnapshotData_Aborts proves the fix: a snapshot
// whose metadata points at a present-but-empty data file aborts the migration
// fail-closed (the old code silently skipped it), and leaves the source intact.
func TestReencryptWALStore_EmptySnapshotData_Aborts(t *testing.T) {
	const shardID, replicaID = uint64(8), uint64(1)
	barA := newKeyedBarrier(t, "emptyA/root-key", "pass-A")
	barB := newKeyedBarrier(t, "emptyB/root-key", "pass-B")

	base := t.TempDir()
	srcDir := filepath.Join(base, "wal")
	dstDir := filepath.Join(base, "wal.keyed")

	snapDir := filepath.Join(base, "snapshots", "shard-8", "replica-1", "snapshot-1")
	if err := os.MkdirAll(snapDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// A present but zero-length snapshot.dat (corrupt/interrupted write).
	snapDat := filepath.Join(snapDir, SnapshotDataFile)
	if err := os.WriteFile(snapDat, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := waldb.Open(srcDir, waldb.WithBarrier(barA.EpochBarrier()))
	if err != nil {
		t.Fatalf("open src wal: %v", err)
	}
	if err := db.SaveBootstrap(shardID, replicaID, logdb.Bootstrap{
		Addresses: map[uint64]string{replicaID: "127.0.0.1:1"}, Type: 7,
	}); err != nil {
		t.Fatalf("SaveBootstrap: %v", err)
	}
	if err := db.SaveSnapshot(shardID, replicaID, logdb.Snapshot{
		Index: 5, Term: 1, Filepath: snapDir, FileSize: 0,
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := db.SaveState([]logdb.Update{{
		ShardID: shardID, ReplicaID: replicaID,
		State:   logdb.State{Term: 1, Vote: replicaID, Commit: 5},
		Entries: []logdb.Entry{{Index: 6, Term: 1, Cmd: []byte("x")}},
	}}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	_ = db.Close()

	_, staged, err := ReencryptWALStore(srcDir, barA, dstDir, barB, snapSibling)
	if err == nil {
		t.Fatal("ReencryptWALStore must abort on a present-but-empty snapshot.dat")
	}
	if !errors.Is(err, ErrEmptySnapshotData) {
		t.Fatalf("err = %v, want ErrEmptySnapshotData", err)
	}
	if len(staged) != 0 {
		t.Fatalf("staged = %d, want 0 on abort", len(staged))
	}
	// Source snapshot.dat left intact on abort.
	if _, serr := os.Stat(snapDat); serr != nil {
		t.Fatalf("source snapshot.dat disturbed on abort: %v", serr)
	}
}

// TestCommitStagedSnapshotsWithRetry covers the past-point-of-no-return commit:
// it succeeds (and is idempotent), surfaces a persistent failure after the
// bounded attempts, and honors context cancellation between attempts.
func TestCommitStagedSnapshotsWithRetry(t *testing.T) {
	t.Run("commits and is idempotent", func(t *testing.T) {
		base := t.TempDir()
		final := filepath.Join(base, SnapshotDataFile)
		keyed := final + snapshotKeyedSuffix
		mustWriteFile(t, final, "source-encrypted")
		mustWriteFile(t, keyed, "dest-encrypted")
		staged := []StagedSnapshot{{FinalPath: final, KeyedPath: keyed}}

		if err := CommitStagedSnapshotsWithRetry(context.Background(), staged, 3, time.Millisecond); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if got := mustReadFile(t, final); got != "dest-encrypted" {
			t.Fatalf("final = %q, want dest-encrypted", got)
		}
		if _, err := os.Stat(keyed); !os.IsNotExist(err) {
			t.Fatal("keyed sibling not consumed by commit")
		}
		// Idempotent for crash recovery: a second call is a no-op.
		if err := CommitStagedSnapshotsWithRetry(context.Background(), staged, 3, time.Millisecond); err != nil {
			t.Fatalf("idempotent commit: %v", err)
		}
	})

	t.Run("returns error after exhausting attempts", func(t *testing.T) {
		base := t.TempDir()
		// FinalPath is a non-empty directory, so renaming a file onto it always
		// fails — a deterministic persistent commit failure.
		final := mustMkdir(t, filepath.Join(base, "final-dir"))
		mustWriteFile(t, filepath.Join(final, "occupant"), "x")
		keyed := filepath.Join(base, "keyed.dat")
		mustWriteFile(t, keyed, "dest-encrypted")
		staged := []StagedSnapshot{{FinalPath: final, KeyedPath: keyed}}

		err := CommitStagedSnapshotsWithRetry(context.Background(), staged, 3, time.Millisecond)
		if err == nil {
			t.Fatal("want error on persistent commit failure")
		}
		var se *SnapshotStageError
		if !errors.As(err, &se) || se.Op != "commit" {
			t.Fatalf("err = %v, want *SnapshotStageError{Op: commit}", err)
		}
		// The staged sibling is preserved for recovery (not consumed).
		if _, serr := os.Stat(keyed); serr != nil {
			t.Fatalf("keyed sibling lost on failure: %v", serr)
		}
	})

	t.Run("honors context cancellation", func(t *testing.T) {
		base := t.TempDir()
		final := mustMkdir(t, filepath.Join(base, "final-dir"))
		mustWriteFile(t, filepath.Join(final, "occupant"), "x")
		keyed := filepath.Join(base, "keyed.dat")
		mustWriteFile(t, keyed, "dest-encrypted")
		staged := []StagedSnapshot{{FinalPath: final, KeyedPath: keyed}}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := CommitStagedSnapshotsWithRetry(ctx, staged, 5, time.Second)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}

// TestKeyLocator proves the KV-key locator never embeds the raw key, is stable,
// and distinguishes distinct keys — so re-encryption mismatch errors stay safe
// to log in a multi-tenant deployment.
func TestKeyLocator(t *testing.T) {
	key := []byte("tenant-42/secret-credential-name")
	loc := keyLocator(key)
	if strings.Contains(loc, string(key)) {
		t.Fatalf("keyLocator leaked the raw key: %q", loc)
	}
	if !strings.Contains(loc, "len "+strconv.Itoa(len(key))) {
		t.Fatalf("keyLocator = %q, want it to include the key length", loc)
	}
	if keyLocator(key) != loc {
		t.Fatal("keyLocator must be deterministic")
	}
	if keyLocator([]byte("different")) == loc {
		t.Fatal("keyLocator collided across distinct keys")
	}
}
