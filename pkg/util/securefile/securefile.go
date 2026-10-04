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

// Package securefile writes files the way a secret or a user-requested
// output is written: crash-durable, mode-enforcing, and never through a
// symlink an attacker (or a stale deployment artifact) planted at the
// target path.
//
// This is the single implementation (go-qrdb#580). It used to be
// pkg/clustertls.WriteFileAtomic, reachable only from pkg/*; go-xkms and
// quicbaas consume go-qrdb exclusively through sdk/go (the ownership
// constitution), so the writer moved to this neutral, dependency-free
// package and is re-exported from sdk/go. pkg/clustertls calls it like any
// other caller now; it does not keep its own copy.
package securefile

import (
	"os"
	"path/filepath"

	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
)

// chmodFn, writeFn, syncFn and closeFn perform the four steps every open file
// in this package goes through before it is either renamed into place
// (WriteFileAtomic) or left exactly where O_EXCL created it (CreateExclusive).
// Each defaults to the real *os.File method. A same-package test overrides
// one to deterministically exercise an error-return path no portable,
// non-root syscall fault reaches (a full disk for Write/Sync, a concurrent
// unlink for Close) -- the same fault-injection shape
// pkg/util/fsync.SetForTest uses for the directory sync.
var (
	chmodFn = func(f *os.File, perm os.FileMode) error { return f.Chmod(perm) }
	writeFn = func(f *os.File, data []byte) error { _, err := f.Write(data); return err }
	syncFn  = func(f *os.File) error { return f.Sync() }
	closeFn = func(f *os.File) error { return f.Close() }
)

// writeChmodSyncClose chmods f to perm, writes data, fsyncs it and closes it,
// in that order -- the common body of WriteFileAtomic and CreateExclusive
// once each has its own *os.File open. It does not close f on an error that
// already came from closeFn (closing twice is itself an error on some
// platforms); every other error path closes f before returning so the
// caller's cleanup only has to consider the file as a name to remove, never
// as a descriptor to release.
func writeChmodSyncClose(f *os.File, perm os.FileMode, data []byte) error {
	if err := chmodFn(f, perm); err != nil {
		_ = f.Close()
		return err
	}
	if err := writeFn(f, data); err != nil {
		_ = f.Close()
		return err
	}
	if err := syncFn(f); err != nil {
		_ = f.Close()
		return err
	}
	return closeFn(f)
}

// refuseSymlink reports a typed error when path already exists and is a
// symlink, so neither WriteFileAtomic's rename nor CreateExclusive's open
// ever resolves a symlink an attacker or a stale deployment artifact planted
// at the target -- not because either operation would write THROUGH that
// symlink (os.Rename replaces the directory entry itself, and O_EXCL fails
// closed on one of its own accord), but so a caller relying on this writer
// for a security-sensitive path gets a loud, typed refusal instead of a
// silent replace of something that was never a plain file.
func refuseSymlink(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return &SymlinkRefusedError{Path: path}
	}
	return nil
}

// WriteFileAtomic writes data to a uniquely named temporary file in the same
// directory as path, fsyncs it, chmods it to perm, renames it into place, and
// then fsyncs the containing directory. The temporary file is removed on
// every error path, including the symlink refusal, which runs BEFORE the
// temporary file is created so there is nothing to clean up.
//
// The directory sync is not belt-and-braces. Atomic visibility to a
// concurrent reader and durability across a crash are different properties,
// and rename gives only the first: a reader sees either the old file or the
// new one and never a mix, but the new DIRECTORY ENTRY lives in the
// directory's own metadata and is not on stable storage until the directory
// is synced. Without it, a power loss can recover a set of files in an order
// the writer never wrote them in.
//
// The rename and its directory sync are one operation, pkg/util/fsync.Rename.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := refuseSymlink(path); err != nil {
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we return before the rename succeeds.
	defer func() { _ = os.Remove(tmpName) }()

	if err := writeChmodSyncClose(tmp, perm, data); err != nil {
		return err
	}
	// The temp file and the target share dir, so this syncs it exactly once.
	return fsync.Rename(tmpName, path)
}

// CreateExclusive writes data to path, refusing to create it if anything --
// a plain file, a directory, or a symlink of any kind, dangling or not --
// already occupies that name. It is for outputs that must never silently
// overwrite an existing file: a share export, a cert export, a generated
// root token. The open itself refuses a symlink (O_EXCL on a symlinked
// pathname fails EEXIST whether or not the link resolves), so there is
// nothing for refuseSymlink to add here.
//
// On success the file is fsynced, chmoded to exactly perm (O_CREAT's mode
// argument is subject to umask; this call is not), and the containing
// directory is fsynced so the new entry survives a crash. A failure before
// the file is fully written and closed removes the partial file, so a
// chmod, write or file-sync error never leaves a truncated file at path.
// A failure of the trailing DIRECTORY sync is reported the same way
// WriteFileAtomic reports one: the file is already complete and closed at
// that point, so it is left in place rather than removed -- the error means
// "not yet guaranteed durable across a crash", not "not written".
func CreateExclusive(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm) // #nosec G304 -- caller-owned destination path
	if err != nil {
		if os.IsExist(err) {
			return &FileExistsError{Path: path}
		}
		return err
	}

	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(path)
		}
	}()

	if err := writeChmodSyncClose(f, perm, data); err != nil {
		return err
	}
	ok = true

	return fsync.Dir(filepath.Dir(path))
}
