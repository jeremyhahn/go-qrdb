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

// Engine is a file-based storage engine that stores each key-value pair
// as a separate file under a base directory. Keys containing "/" are
// mapped to subdirectories.
type Engine struct {
	dir    string
	closed atomic.Bool

	// journalMu serializes batch commits: the commit journal is a single
	// file in the engine root, so two concurrent commits must not
	// interleave publish/apply/clear.
	journalMu sync.Mutex
}

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

func (e *Engine) keyPath(key []byte) string {
	return filepath.Join(e.dir, filepath.FromSlash(encodeKeyToPath(key)))
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
	data, err := os.ReadFile(e.keyPath(key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// Set durably stores a key-value pair as a file on disk: the value is
// written to a temp file in the destination directory, fsynced, and
// atomically renamed into place, then the parent directory is fsynced. A
// crash therefore leaves either the old value or the new value, never a
// torn file — the durability contract the raft state-machine storage
// seam (raft.sm_storage: file) relies on.
func (e *Engine) Set(key, value []byte) error {
	if e.closed.Load() {
		return &storage.ClosedError{}
	}
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}
	path := e.keyPath(key)
	dir := filepath.Dir(path)
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
	if err := fsync.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// Delete removes a key from storage.
func (e *Engine) Delete(key []byte) error {
	if e.closed.Load() {
		return &storage.ClosedError{}
	}
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}
	path := e.keyPath(key)
	err := os.Remove(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// Make the removal durable: fsync the parent directory so a crash
	// cannot resurrect the deleted key.
	return fsync.Dir(filepath.Dir(path))
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
		if info.IsDir() {
			return nil
		}
		// Skip the commit journal and temp files (dot-prefixed; no
		// encoded key segment can start with '.').
		if strings.HasPrefix(info.Name(), ".") {
			return nil
		}
		rel, relErr := filepath.Rel(e.dir, path)
		if relErr != nil {
			return relErr
		}
		key := []byte(filepath.ToSlash(rel))
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
	_, err := os.Stat(e.keyPath(key))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Batch creates a new write batch for atomic multi-key operations.
func (e *Engine) Batch() storage.Batch {
	return newBatch(e)
}

// NewIterator creates an iterator for scanning key ranges.
func (e *Engine) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
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
