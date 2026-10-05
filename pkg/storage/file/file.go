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

// Package file provides a file-backed storage engine that maps keys to filesystem paths.
package file

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
)

// Compile-time interface checks.
var (
	_ storage.Storage = (*Engine)(nil)
	_ storage.Syncer  = (*Engine)(nil)
)

// Engine is a file-based storage engine that maps each key to a directory
// under a base directory, holding the key's value in a sentinel file
// (valueFileName) inside it. Keys containing "/" are mapped to nested
// subdirectories, so a key and every key for which it is a '/' prefix share a
// directory tree without colliding — see keyenc.go for why that mapping is
// collision-free for ANY two distinct keys, regardless of their '/'
// structure or the order they are written in (go-qrdb#592).
type Engine struct {
	dir    string
	closed atomic.Bool

	// journalMu serializes batch commits: the commit journal is a single
	// file in the engine root, so two concurrent commits must not
	// interleave publish/apply/clear.
	journalMu sync.Mutex
}

// valueFileName is the sentinel file holding a key's value, inside the
// directory encodeKeyToPath maps the key to. No encoded key segment can ever
// be named this: encodeKeyToPath escapes a leading '.' in every segment it
// emits (see keyenc.go), so a real key segment never starts with '.' and
// this name can never collide with one.
const valueFileName = ".kv"

// maxEncodedSegmentLen bounds a single '/'-delimited segment of an encoded
// key to this filesystem's path-component limit. 255 bytes (NAME_MAX) is the
// floor across ext4, xfs, btrfs and APFS; 200 leaves margin without
// depending on exactly which filesystem backs raft.data_dir.
const maxEncodedSegmentLen = 200

// New creates a new file-based storage engine rooted at dir. The
// directory is created if it does not exist. An interrupted batch commit
// from a previous process is completed here (see recoverJournal); a
// corrupt journal fails the open rather than silently serving a
// half-applied batch.
func New(dir string) (*Engine, error) {
	if dir == "" {
		return nil, storage.ErrInvalidPath
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	e := &Engine{dir: dir}
	if err := e.recoverJournal(); err != nil {
		return nil, err
	}
	return e, nil
}

// keyDir returns the directory key maps to: key's own value lives in
// valueFileName inside it, and any key for which this one is a '/' prefix
// lives in a subdirectory beside that file. The two never collide — see the
// keyenc.go package doc.
//
// Every encoded segment is checked against this filesystem's path-component
// limit here, before any path derived from it is opened, so an oversized
// segment is refused as KeySegmentTooLongError (a declared refusal, not a
// storage fault) rather than reaching the OS as ENAMETOOLONG.
func (e *Engine) keyDir(key []byte) (string, error) {
	encoded := encodeKeyToPath(key)
	for _, seg := range strings.Split(encoded, "/") {
		if len(seg) > maxEncodedSegmentLen {
			return "", &KeySegmentTooLongError{Segment: seg}
		}
	}
	return filepath.Join(e.dir, filepath.FromSlash(encoded)), nil
}

// valuePath returns the path of the sentinel file holding key's value.
func (e *Engine) valuePath(key []byte) (string, error) {
	dir, err := e.keyDir(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, valueFileName), nil
}

// Get retrieves the value for the given key. Returns nil, nil when the
// key does not exist per the storage.Storage contract.
func (e *Engine) Get(key []byte) ([]byte, error) {
	if e.closed.Load() {
		return nil, &storage.ClosedError{}
	}
	if len(key) == 0 {
		return nil, storage.ErrEmptyKey
	}
	path, err := e.valuePath(key)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// Set durably stores a key-value pair as a file on disk: the value is
// written to a temp file in the key's own directory (created if absent),
// fsynced, and atomically renamed into place as valueFileName, then that
// directory is fsynced. A crash therefore leaves either the old value or the
// new value, never a torn file — the durability contract the raft
// state-machine storage seam (raft.sm_storage: file) relies on.
//
// The directory MkdirAll creates here is exactly the one keyDir names: it
// may already exist, either because this key was set before or because an
// unrelated key for which this one is a '/' prefix created it first. Either
// way the directory is only ever a container for this key's value file and
// its children's subdirectories, never a file itself, so the two kinds of
// key can never contend for the same path (go-qrdb#592).
func (e *Engine) Set(key, value []byte) error {
	if e.closed.Load() {
		return &storage.ClosedError{}
	}
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}
	dir, err := e.keyDir(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-set-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(value); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	// fsync.Rename is the rename and its directory sync as one operation. The
	// temp file and the target share dir, so it is synced exactly once -- the
	// same work the separate os.Rename + directory-fsync pair did, with no second
	// place for the pairing to be got wrong (go-qrdb#268).
	path := filepath.Join(dir, valueFileName)
	if err := fsync.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// Delete removes a key from storage.
//
// Only the value sentinel is removed; the key's directory is left in place,
// deliberately, even when nothing else is left in it. Two things depend on
// that:
//
//   - A key for which this one is a '/' prefix may have its own subdirectory
//     there, and removing the directory would delete that key's data too.
//   - storage.Storage carries no promise that Set and Delete on DIFFERENT
//     keys are serialized against each other — FastBackend's write-through
//     deliberately issues them from concurrent per-shard workers, ordered
//     only per key — so two keys that share a directory because one is a
//     '/' prefix of the other can have their Set and Delete in flight at
//     once. Removing a directory on the strength of a single point-in-time
//     "is it empty" check would race a concurrent Set for the other key
//     between that check and its own MkdirAll, which depends on the
//     directory continuing to exist. Never removing it trades a harmless
//     leftover empty directory for not having that race at all.
func (e *Engine) Delete(key []byte) error {
	if e.closed.Load() {
		return &storage.ClosedError{}
	}
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}
	dir, err := e.keyDir(key)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, valueFileName)
	if rmErr := os.Remove(path); rmErr != nil {
		if os.IsNotExist(rmErr) {
			return nil
		}
		return rmErr
	}
	// Make the removal durable: fsync the directory so a crash cannot
	// resurrect the deleted value.
	return fsync.Dir(dir)
}

// List returns all keys matching the given prefix.
func (e *Engine) List(prefix []byte) ([][]byte, error) {
	if e.closed.Load() {
		return nil, &storage.ClosedError{}
	}
	var keys [][]byte
	err := filepath.Walk(e.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		key, isValue := decodeValueFilePath(e.dir, path, info)
		if !isValue {
			return nil
		}
		if bytes.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}

// Exists reports whether the key exists in storage.
func (e *Engine) Exists(key []byte) (bool, error) {
	if e.closed.Load() {
		return false, &storage.ClosedError{}
	}
	if len(key) == 0 {
		return false, storage.ErrEmptyKey
	}
	path, err := e.valuePath(key)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// decodeValueFilePath reports whether path (walked under root, described by
// info) is a key's value sentinel, and when it is, the key it decodes to.
//
// Every other file under root -- the commit journal, its temp files, and any
// in-flight ".tmp-set-*" write -- is dot-prefixed and is not named
// valueFileName, so excluding everything but an exact valueFileName match
// also excludes all of them without needing to enumerate them here. A
// directory is never a key by itself; only the sentinel inside it is.
func decodeValueFilePath(root, path string, info os.FileInfo) ([]byte, bool) {
	if info.IsDir() || info.Name() != valueFileName {
		return nil, false
	}
	rel, err := filepath.Rel(root, filepath.Dir(path))
	if err != nil || rel == "." {
		// rel == "." means the sentinel sits directly in the engine root,
		// which only an empty-key Set could produce -- refused before it
		// reaches here (see Engine.Set) -- so this is unreachable from this
		// engine's own writes. Treated as "not a key" rather than panicking
		// on a hand-placed file.
		return nil, false
	}
	return decodePathToKey(filepath.ToSlash(rel)), true
}

// Batch creates a new write batch for atomic multi-key operations. A closed
// engine returns a batch that reports the closure from every operation.
func (e *Engine) Batch() storage.Batch {
	if e.closed.Load() {
		return storage.NewClosedBatch()
	}
	return newBatch(e)
}

// NewIterator creates an iterator for scanning key ranges. A closed engine
// returns an iterator that reports the closure through Error.
func (e *Engine) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	if e.closed.Load() {
		return storage.NewClosedIterator()
	}
	return newIterator(e, opts)
}

// Snapshot creates a point-in-time snapshot for consistent reads.
func (e *Engine) Snapshot() (storage.Snapshot, error) {
	if e.closed.Load() {
		return nil, &storage.ClosedError{}
	}
	return newSnapshot(e)
}

// Sync fsyncs the engine root directory. Individual Set/Delete/Batch
// operations are already durable on their own; Sync exists to satisfy the
// storage.Syncer seam for callers that checkpoint explicitly.
func (e *Engine) Sync() error {
	if e.closed.Load() {
		return &storage.ClosedError{}
	}
	return fsync.Dir(e.dir)
}

// Close marks the engine as closed. Subsequent operations return ClosedError.
func (e *Engine) Close() error {
	e.closed.Store(true)
	return nil
}
