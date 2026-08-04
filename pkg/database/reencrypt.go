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

package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb/waldb"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// encfsMagic is the 4-byte sentinel that prefixes every encfs-encrypted file
// header. Files lacking it (Pebble's LOCK, and any plaintext artifacts) are
// copied verbatim during a raw-file recodec rather than run through the
// encrypting FS, which would otherwise prepend a spurious header.
var encfsMagic = []byte("QENC")

// reencryptBatchBytes bounds the in-memory size of a copy batch before it is
// committed. Large stores are copied in bounded chunks so peak memory stays
// flat regardless of dataset size.
const reencryptBatchBytes = 16 << 20 // 16 MiB

// ReencryptOpenError wraps a failure to open a Pebble store directory during
// an at-rest re-encryption.
type ReencryptOpenError struct {
	Dir string
	Err error
}

func (e *ReencryptOpenError) Error() string {
	return "database: reencrypt: open store " + e.Dir + ": " + e.Err.Error()
}

func (e *ReencryptOpenError) Unwrap() error { return e.Err }

// ReencryptCopyError wraps a failure while copying key/value pairs between the
// source and destination stores.
type ReencryptCopyError struct {
	Err error
}

func (e *ReencryptCopyError) Error() string {
	return "database: reencrypt: copy: " + e.Err.Error()
}

func (e *ReencryptCopyError) Unwrap() error { return e.Err }

// ReencryptVerifyMismatchError is returned when post-copy verification finds
// the destination store does not faithfully reproduce the source.
type ReencryptVerifyMismatchError struct {
	// Reason describes the first mismatch found.
	Reason string
}

func (e *ReencryptVerifyMismatchError) Error() string {
	return "database: reencrypt: verification failed: " + e.Reason
}

// ReencryptPebbleStore performs a logical key/value copy of every record in the
// Pebble store at srcDir (decrypting through srcEnc) into a freshly-opened
// store at dstDir (encrypting through dstEnc). It returns the number of records
// copied.
//
// This is the engine-faithful "swap" re-encryption primitive: because qrdb's
// stores use Pebble's default comparer/merger, iterating the full keyspace and
// rewriting it into a default-options store reproduces the visible state
// exactly while changing only the at-rest encryption (srcEnc may be a none /
// plaintext encryptor and dstEnc a keyed one, or vice versa). The source is
// opened read-only so a copy never mutates it; the caller performs the atomic
// directory swap once this returns and verification passes.
//
// srcEnc or dstEnc may be a disabled (nil-barrier) Encryptor, which models the
// seal-subsystem-disabled raw-plaintext source state.
func ReencryptPebbleStore(ctx context.Context, srcDir string, srcEnc *atrest.Encryptor, dstDir string, dstEnc *atrest.Encryptor) (int64, error) {
	srcOpts := &pebble.Options{ReadOnly: true}
	srcEnc.Apply(srcOpts)
	srcOpts.Logger = pebblestore.NewLogger(nil)
	src, err := pebble.Open(srcDir, srcOpts)
	if err != nil {
		return 0, &ReencryptOpenError{Dir: srcDir, Err: err}
	}
	defer func() { _ = src.Close() }()

	dstOpts := &pebble.Options{}
	dstEnc.Apply(dstOpts)
	dstOpts.Logger = pebblestore.NewLogger(nil)
	dst, err := pebble.Open(dstDir, dstOpts)
	if err != nil {
		return 0, &ReencryptOpenError{Dir: dstDir, Err: err}
	}
	defer func() { _ = dst.Close() }()

	iter, err := src.NewIter(nil)
	if err != nil {
		return 0, &ReencryptCopyError{Err: err}
	}
	defer func() { _ = iter.Close() }()

	var (
		count int64
		batch = dst.NewBatch()
	)
	defer func() { _ = batch.Close() }()

	for valid := iter.First(); valid; valid = iter.Next() {
		select {
		case <-ctx.Done():
			return count, &ReencryptCopyError{Err: ctx.Err()}
		default:
		}
		// Batch.Set copies key/value internally, so the iterator-owned slices
		// (valid only until Next) are safe to pass directly.
		if err := batch.Set(iter.Key(), iter.Value(), nil); err != nil {
			return count, &ReencryptCopyError{Err: err}
		}
		count++
		if batch.Len() >= reencryptBatchBytes {
			if err := batch.Commit(pebble.Sync); err != nil {
				return count, &ReencryptCopyError{Err: err}
			}
			_ = batch.Close()
			batch = dst.NewBatch()
		}
	}
	if err := iter.Error(); err != nil {
		return count, &ReencryptCopyError{Err: err}
	}
	if batch.Len() > 0 {
		if err := batch.Commit(pebble.Sync); err != nil {
			return count, &ReencryptCopyError{Err: err}
		}
	}
	if err := dst.Flush(); err != nil {
		return count, &ReencryptCopyError{Err: err}
	}
	return count, nil
}

// ReencryptStoreFiles re-encrypts a Pebble store by copying every file in
// srcDir into dstDir, recoding encfs-encrypted files through the source and
// destination encrypting filesystems (decrypt with srcEnc, re-encrypt with
// dstEnc) while copying non-encfs files (e.g. Pebble's LOCK) verbatim. Unlike
// ReencryptPebbleStore — which performs a logical key/value copy and lets
// Pebble rebuild its file layout — this preserves Pebble's on-disk structure
// byte-for-byte at the logical level, only changing the at-rest encryption.
//
// It is the "in-place" mechanism's recodec: lighter on the engine (no
// compaction/rewrite) than the swap mechanism's logical copy. dstDir is a fresh
// sibling directory; the caller verifies it and performs the crash-safe atomic
// swap, so a partial run never touches the original store.
func ReencryptStoreFiles(ctx context.Context, srcDir string, srcEnc *atrest.Encryptor, dstDir string, dstEnc *atrest.Encryptor) (int64, error) {
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return 0, &ReencryptOpenError{Dir: dstDir, Err: err}
	}
	srcFS := srcEnc.FS()
	dstFS := dstEnc.FS()

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return 0, &ReencryptOpenError{Dir: srcDir, Err: err}
	}
	var count int64
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return count, &ReencryptCopyError{Err: ctx.Err()}
		default:
		}
		name := entry.Name()
		srcPath := filepath.Join(srcDir, name)
		dstPath := filepath.Join(dstDir, name)

		if entry.IsDir() {
			// Pebble stores are flat, but recurse defensively for any nested
			// artifacts so nothing is silently dropped.
			n, rerr := ReencryptStoreFiles(ctx, srcPath, srcEnc, dstPath, dstEnc)
			if rerr != nil {
				return count, rerr
			}
			count += n
			continue
		}
		// Pebble re-creates its own LOCK on open; never copy it.
		if name == "LOCK" {
			continue
		}
		encrypted, derr := isEncfsFile(srcPath)
		if derr != nil {
			return count, &ReencryptCopyError{Err: derr}
		}
		if encrypted {
			if cerr := recodeFile(srcFS, srcPath, dstFS, dstPath); cerr != nil {
				return count, &ReencryptCopyError{Err: cerr}
			}
		} else {
			if cerr := copyFileRaw(srcPath, dstPath); cerr != nil {
				return count, &ReencryptCopyError{Err: cerr}
			}
		}
		count++
	}
	return count, nil
}

// isEncfsFile reports whether the file at path begins with the encfs magic.
func isEncfsFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	hdr := make([]byte, len(encfsMagic))
	n, err := io.ReadFull(f, hdr)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return false, nil // too short to be encfs
	}
	if err != nil {
		return false, err
	}
	return n == len(encfsMagic) && bytes.Equal(hdr, encfsMagic), nil
}

// recodeFile decrypts an encfs file through srcFS and re-encrypts it through
// dstFS, writing the logically-identical content under a new (destination)
// barrier key.
func recodeFile(srcFS vfs.FS, srcPath string, dstFS vfs.FS, dstPath string) error {
	in, err := srcFS.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := dstFS.Create(dstPath, vfs.WriteCategoryUnspecified)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// copyFileRaw copies a non-encfs file verbatim.
func copyFileRaw(srcPath, dstPath string) error {
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// SnapshotDataFile is the fixed name of a snapshot's data file inside the
// snapshot directory recorded in go-quicraft LogDB metadata. Callers building a
// snapDataDest path join it onto the destination snapshot directory.
const SnapshotDataFile = "snapshot.dat"

// snapshotKeyedSuffix is appended to a snapshot data file's path to stage its
// re-encrypted (destination-barrier) copy beside the original until the
// migration commits. It never collides with go-quicraft's own snapshot files.
const snapshotKeyedSuffix = ".migrate-keyed"

// StagedSnapshot is a snapshot data file that ReencryptWALStore re-encrypted
// under the destination barrier into a sibling staging file, awaiting the
// migration commit. CommitStagedSnapshots renames KeyedPath over FinalPath once
// the WAL/SM swaps are durable; RollbackStagedSnapshots discards KeyedPath on
// failure, leaving the live snapshot untouched. It aliases the seal journal's
// record type so a migrator can checkpoint the staged set directly into its
// MigrationJournal for crash recovery.
type StagedSnapshot = seal.StagedSnapshot

// SnapshotStageError is returned when committing a re-encrypted snapshot data
// file into place fails.
type SnapshotStageError struct {
	Op   string
	Path string
	Err  error
}

// Error implements the error interface.
func (e *SnapshotStageError) Error() string {
	return "database: snapshot stage " + e.Op + " for " + e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *SnapshotStageError) Unwrap() error { return e.Err }

// SnapshotKeyedSuffix is the suffix a caller appends to stage a re-encrypted
// snapshot data file beside the original when the snapshot directory is NOT part
// of a directory that the migration swaps (the IsolationRaft case, whose
// snapshots live in the shared host tree). It is committed by
// CommitStagedSnapshots. Callers whose snapshot directory rides a directory swap
// (IsolationVolume) instead map the destination into the keyed staging dir.
const SnapshotKeyedSuffix = snapshotKeyedSuffix

// ReencryptWALStore re-encrypts a Raft WAL/LogDB at srcDir — read under the
// source barrier — into a freshly-opened LogDB at dstDir written under the
// destination barrier, returning the number of Raft nodes copied. It is the
// WAL counterpart to ReencryptPebbleStore, used for barriers that back a Raft
// host (per-tenant IsolationVolume/IsolationRaft) whose replication log is
// encrypted by go-quicraft (not encfs). A nil barrier denotes a plaintext side.
//
// Each node's snapshot DATA FILE lives outside the WAL — in the host snapshot
// directory recorded in the LogDB metadata, framed under the host barrier — so
// it must be re-keyed alongside the WAL. For every snapshot that has a data
// file, ReencryptWALStore re-encrypts it from src to dst via
// quicraft.ReencryptSnapshotFile, writing the keyed copy to the path returned by
// snapDataDest(snapDir), and records it in the returned []StagedSnapshot. The
// caller chooses snapDataDest by how its migration commits:
//
//   - IsolationVolume: the snapshot directory sits under the SM dir that the
//     migration swaps, so snapDataDest maps it into the keyed staging dir
//     (smDir.keyed). The directory swap then makes the keyed snapshot live and a
//     rolled-back migration discards it with the staging dir; no per-snapshot
//     commit is needed and the returned slice can be ignored.
//   - IsolationRaft: the snapshot lives in the shared host tree the migration
//     does not swap, so snapDataDest returns a sibling staging file
//     (snapDir/snapshot.dat + SnapshotKeyedSuffix). The caller renames it into
//     place with CommitStagedSnapshots once the WAL/SM swaps are durable, or
//     discards it with RollbackStagedSnapshots on failure.
//
// A metadata-only snapshot (e.g. an on-disk state machine whose state is the
// separately-recoded store, fileSize 0) has no data file and is recorded with
// the destination barrier's current epoch only.
// ErrEmptySnapshotData is returned when a snapshot's metadata points at a data
// file that exists but is unexpectedly zero-length. A genuine metadata-only
// snapshot has NO data file at all; a present-but-empty file indicates a
// corrupt or interrupted snapshot write, so re-encryption fails closed rather
// than silently recording the destination epoch over un-recoded data.
var ErrEmptySnapshotData = errors.New("database: snapshot data file is empty")

// ErrSnapshotDataNotRegular is returned when a snapshot's data-file path exists
// but is not a regular file (e.g. a directory). A framed snapshot.dat is always
// a regular file, so anything else is treated as a malformed snapshot.
var ErrSnapshotDataNotRegular = errors.New("database: snapshot data path is not a regular file")

// snapshotDataAction classifies a snapshot's data-file path for re-encryption.
// It distinguishes a genuine metadata-only snapshot (an on-disk state machine
// whose state is the separately-recoded store, with NO data file) — which is
// correctly skipped — from an unreadable or malformed data file, which must
// abort the migration fail-closed. Returning (false, nil) skips re-encryption;
// (true, nil) requests it; a non-nil error fails the migration.
//
// The old behavior treated ANY stat error or a zero-length file as
// "metadata-only" and skipped re-encryption, which silently recorded the
// destination epoch over a source-encrypted (or missing) snapshot — leaving
// recovery pointing at data the destination barrier cannot decrypt.
func snapshotDataAction(srcDat string) (reencrypt bool, err error) {
	fi, statErr := os.Stat(srcDat)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			// Genuine metadata-only snapshot: no separate framed data file.
			return false, nil
		}
		// Any other stat failure (permission, I/O) is indeterminate; do not
		// silently skip re-encryption.
		return false, &SnapshotStageError{Op: "stat", Path: srcDat, Err: statErr}
	}
	if fi.IsDir() {
		return false, &SnapshotStageError{Op: "stat", Path: srcDat, Err: ErrSnapshotDataNotRegular}
	}
	if fi.Size() == 0 {
		return false, &SnapshotStageError{Op: "stat", Path: srcDat, Err: ErrEmptySnapshotData}
	}
	return true, nil
}

// keyLocator returns a non-reversible locator for a KV key suitable for logs
// and error messages: a short SHA-256 prefix plus the key length. Raw KV keys
// can be tenant-sensitive, so they are never embedded verbatim in errors that
// may be persisted or logged.
func keyLocator(k []byte) string {
	sum := sha256.Sum256(k)
	return hex.EncodeToString(sum[:8]) + " (len " + strconv.Itoa(len(k)) + ")"
}

func ReencryptWALStore(srcDir string, src *seal.StorageBarrier, dstDir string, dst *seal.StorageBarrier, snapDataDest func(snapDir string) string) (int, []StagedSnapshot, error) {
	var srcWAL, dstWAL waldb.WALBarrier
	var srcQ, dstQ quicraft.Barrier
	if src != nil {
		eb := src.EpochBarrier()
		srcWAL, srcQ = eb, eb
	}
	if dst != nil {
		eb := dst.EpochBarrier()
		dstWAL, dstQ = eb, eb
	}

	var staged []StagedSnapshot
	snapFn := func(_, _ uint64, snap logdb.Snapshot) (uint64, error) {
		// The migrated metadata records the destination barrier's current epoch
		// (0 for a plaintext destination); for a data snapshot this is replaced
		// by the actual re-encryption epoch below.
		var dstEpoch uint64
		if dstQ != nil {
			dstEpoch = dstQ.CurrentEpoch()
		}
		if snap.Filepath == "" {
			return dstEpoch, nil
		}
		srcDat := filepath.Join(snap.Filepath, SnapshotDataFile)
		reenc, aerr := snapshotDataAction(srcDat)
		if aerr != nil {
			return 0, aerr
		}
		if !reenc {
			// Genuine metadata-only snapshot: no separate framed data file to re-key.
			return dstEpoch, nil
		}
		dstDat := snapDataDest(snap.Filepath)
		_ = os.Remove(dstDat) // clear stale staging from an interrupted run
		epoch, rerr := quicraft.ReencryptSnapshotFile(srcDat, dstDat, srcQ, dstQ)
		if rerr != nil {
			return 0, rerr
		}
		staged = append(staged, StagedSnapshot{FinalPath: srcDat, KeyedPath: dstDat})
		return epoch, nil
	}

	n, err := waldb.Reencrypt(srcDir, srcWAL, dstDir, dstWAL, snapFn)
	if err != nil {
		RollbackStagedSnapshots(staged)
		return n, nil, err
	}
	return n, staged, nil
}

// CommitStagedSnapshots atomically renames each staged keyed snapshot over its
// live counterpart so the post-migration host reads the destination-barrier
// copy. It is idempotent for crash recovery: a missing keyed file (already
// committed) is skipped.
func CommitStagedSnapshots(staged []StagedSnapshot) error {
	for _, s := range staged {
		if _, err := os.Stat(s.KeyedPath); err != nil {
			if os.IsNotExist(err) {
				continue // already committed
			}
			return &SnapshotStageError{Op: "stat", Path: s.KeyedPath, Err: err}
		}
		if err := os.Rename(s.KeyedPath, s.FinalPath); err != nil {
			return &SnapshotStageError{Op: "commit", Path: s.FinalPath, Err: err}
		}
	}
	return nil
}

// CommitStagedSnapshotsWithRetry commits staged snapshots past the migration
// point of no return, retrying transient filesystem errors with bounded,
// context-aware backoff. After the SM/WAL directory swaps are durable the live
// snapshots MUST be advanced to their keyed copies — otherwise the
// post-migration host reads source-encrypted snapshot data under the
// destination barrier. Aborting on a transient stat/rename error would leave
// exactly that inconsistency, so this rolls forward instead of failing.
// CommitStagedSnapshots is idempotent (an already-committed sibling is
// skipped), so every retry is safe. The final attempt's error is returned if
// all attempts fail, leaving the staged siblings on disk for recovery.
func CommitStagedSnapshotsWithRetry(ctx context.Context, staged []StagedSnapshot, attempts int, backoff time.Duration) error {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return &SnapshotStageError{Op: "commit-retry", Path: "", Err: ctx.Err()}
			case <-time.After(backoff):
			}
			backoff *= 2
		}
		if err = CommitStagedSnapshots(staged); err == nil {
			return nil
		}
	}
	return err
}

// RollbackStagedSnapshots removes staged keyed snapshots, leaving the live
// snapshot data files untouched. Safe to call multiple times and on nil.
func RollbackStagedSnapshots(staged []StagedSnapshot) {
	for _, s := range staged {
		_ = os.Remove(s.KeyedPath)
	}
}

// SwapStoreDir atomically replaces dir with dir+keyedSuffix, moving the
// original to dir+backupSuffix. It is the exported swap used by orchestrators
// outside this package (e.g. the per-tenant host recycle in pkg/server) and is
// idempotent for crash recovery (a no-op when the keyed copy is absent).
func SwapStoreDir(dir string) error { return swapDir(dir) }

// RemoveStoreBackup removes the pre-migration backup left by SwapStoreDir once
// the commit is durable.
func RemoveStoreBackup(dir string) { _ = os.RemoveAll(dir + backupSuffix) }

// KeyedDirSuffix / BackupDirSuffix expose the staging/backup suffixes so
// out-of-package orchestrators can clean up transient artifacts.
const (
	KeyedDirSuffix  = keyedSuffix
	BackupDirSuffix = backupSuffix
)

// VerifyReencryptedStore confirms that the destination store at dstDir (read
// through dstEnc) reproduces every key/value of the source store at srcDir
// (read through srcEnc), and that neither contains records the other lacks. It
// is the post-copy gate that must pass before the caller deletes the source
// and commits the swap — guaranteeing no data loss across re-encryption.
func VerifyReencryptedStore(ctx context.Context, srcDir string, srcEnc *atrest.Encryptor, dstDir string, dstEnc *atrest.Encryptor) error {
	srcOpts := &pebble.Options{ReadOnly: true}
	srcEnc.Apply(srcOpts)
	srcOpts.Logger = pebblestore.NewLogger(nil)
	src, err := pebble.Open(srcDir, srcOpts)
	if err != nil {
		return &ReencryptOpenError{Dir: srcDir, Err: err}
	}
	defer func() { _ = src.Close() }()

	dstOpts := &pebble.Options{ReadOnly: true}
	dstEnc.Apply(dstOpts)
	dstOpts.Logger = pebblestore.NewLogger(nil)
	dst, err := pebble.Open(dstDir, dstOpts)
	if err != nil {
		return &ReencryptOpenError{Dir: dstDir, Err: err}
	}
	defer func() { _ = dst.Close() }()

	sIt, err := src.NewIter(nil)
	if err != nil {
		return &ReencryptCopyError{Err: err}
	}
	defer func() { _ = sIt.Close() }()
	dIt, err := dst.NewIter(nil)
	if err != nil {
		return &ReencryptCopyError{Err: err}
	}
	defer func() { _ = dIt.Close() }()

	sValid := sIt.First()
	dValid := dIt.First()
	for sValid && dValid {
		select {
		case <-ctx.Done():
			return &ReencryptCopyError{Err: ctx.Err()}
		default:
		}
		if !bytes.Equal(sIt.Key(), dIt.Key()) {
			return &ReencryptVerifyMismatchError{Reason: "key mismatch at " + keyLocator(sIt.Key()) + " vs " + keyLocator(dIt.Key())}
		}
		if !bytes.Equal(sIt.Value(), dIt.Value()) {
			return &ReencryptVerifyMismatchError{Reason: "value mismatch for key " + keyLocator(sIt.Key())}
		}
		sValid = sIt.Next()
		dValid = dIt.Next()
	}
	if sValid != dValid {
		return &ReencryptVerifyMismatchError{Reason: "record count mismatch (one store has extra keys)"}
	}
	if err := sIt.Error(); err != nil {
		return &ReencryptCopyError{Err: err}
	}
	if err := dIt.Error(); err != nil {
		return &ReencryptCopyError{Err: err}
	}
	return nil
}
