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

// Package fsync makes a rename crash-durable.
//
// Atomic visibility and crash durability are different properties, and rename
// gives only the first. A concurrent reader sees either the old entry or the
// new one and never a mix -- but the entry itself lives in the containing
// DIRECTORY's metadata, and that metadata is not on stable storage until the
// directory is fsynced. Without the sync a power loss can recover a set of
// names in an order the writer never wrote them in: a file whose contents were
// synced can come back without the directory entry that publishes it, and a
// staged file renamed over its live counterpart can come back as either name,
// or both.
//
// That ordering is load-bearing wherever a caller uses rename as a commit
// point -- an audit log rotating to an archive name, a migration swapping a
// store directory into place, a re-encryption committing a keyed snapshot over
// the original. Each of those means "this step is done" and none of them are,
// until the directory is durable.
//
// This package is the single implementation. It exists because the operation
// had been hand-rolled: two independent, correct copies of the directory fsync
// (pkg/clustertls, pkg/storage/file) and eight rename sites with none at all
// (go-qrdb#268). One copy of a durability primitive is how it stays right;
// several is how one of them drifts.
package fsync

import (
	"os"
	"path/filepath"
)

// Dir fsyncs dir, so an entry created, renamed, or removed inside it survives
// a crash.
//
// It is the operation callers reach for after os.Remove as well as after
// os.Rename: unlinking a name is a directory-metadata change like any other,
// and a "deleted" file can come back if the directory is never synced.
func Dir(dir string) error { return syncDirFn(dir) }

// Rename renames oldpath to newpath and then fsyncs the containing
// directories, so both halves of the operation -- the new name appearing and
// the old one disappearing -- are durable when it returns nil.
//
// When the two paths share a parent, that one directory is synced once. When
// they do not, BOTH are synced: the destination publishes the new name and the
// source records the removal of the old one, and a crash that keeps one
// without the other recovers the entry under two names or under none.
//
// The error from the rename itself is returned unwrapped, so callers keep
// their existing os.IsNotExist / errors.Is handling.
func Rename(oldpath, newpath string) error {
	if err := os.Rename(oldpath, newpath); err != nil {
		return err
	}
	newDir := filepath.Dir(newpath)
	if err := syncDirFn(newDir); err != nil {
		return err
	}
	if oldDir := filepath.Dir(oldpath); oldDir != newDir {
		return syncDirFn(oldDir)
	}
	return nil
}

// syncDirFn is indirected so a test can fail the directory sync on a
// filesystem that would otherwise always succeed. The post-state of a
// successful sync is byte-identical to that of a skipped one, so a test has to
// observe the OPERATION rather than the result -- see SetForTest.
var syncDirFn = fsyncDir

// fsyncDir opens dir and fsyncs it. The close error is reported when the sync
// itself succeeded, because a failed close can mean the sync did not reach
// stable storage.
func fsyncDir(dir string) error {
	d, err := os.Open(dir) // #nosec G304 -- caller-owned data directory
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}
