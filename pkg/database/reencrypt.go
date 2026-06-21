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
	"io"
	"os"
	"path/filepath"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

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

// ReencryptWALStore re-encrypts a Raft WAL/LogDB at srcDir — read under the
// source barrier — into a freshly-opened LogDB at dstDir written under the
// destination barrier, returning the number of Raft nodes copied. It is the
// WAL counterpart to ReencryptPebbleStore, used for barriers that back a Raft
// host (per-tenant IsolationVolume/IsolationRaft) whose replication log is
// encrypted by go-quicraft (not encfs). A nil barrier denotes a plaintext side.
func ReencryptWALStore(srcDir string, src *seal.StorageBarrier, dstDir string, dst *seal.StorageBarrier) (int, error) {
	var srcWAL, dstWAL waldb.WALBarrier
	if src != nil {
		srcWAL = src.EpochBarrier()
	}
	if dst != nil {
		dstWAL = dst.EpochBarrier()
	}
	return waldb.Reencrypt(srcDir, srcWAL, dstDir, dstWAL)
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
			return &ReencryptVerifyMismatchError{Reason: "key mismatch at " + string(sIt.Key()) + " vs " + string(dIt.Key())}
		}
		if !bytes.Equal(sIt.Value(), dIt.Value()) {
			return &ReencryptVerifyMismatchError{Reason: "value mismatch for key " + string(sIt.Key())}
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
