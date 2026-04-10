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
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Compile-time interface check.
var _ storage.Storage = (*Engine)(nil)

// Engine is a file-based storage engine that stores each key-value pair
// as a separate file under a base directory. Keys containing "/" are
// mapped to subdirectories.
type Engine struct {
	dir    string
	closed atomic.Bool
}

// New creates a new file-based storage engine rooted at dir.
// The directory is created if it does not exist.
func New(dir string) (*Engine, error) {
	if dir == "" {
		return nil, storage.ErrInvalidPath
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Engine{dir: dir}, nil
}

func (e *Engine) keyPath(key []byte) string {
	return filepath.Join(e.dir, filepath.FromSlash(string(key)))
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

// Set stores a key-value pair as a file on disk.
func (e *Engine) Set(key, value []byte) error {
	if e.closed.Load() {
		return &storage.ClosedError{}
	}
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}
	path := e.keyPath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, value, 0o600)
}

// Delete removes a key from storage.
func (e *Engine) Delete(key []byte) error {
	if e.closed.Load() {
		return &storage.ClosedError{}
	}
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}
	err := os.Remove(e.keyPath(key))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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

// Close marks the engine as closed. Subsequent operations return ClosedError.
func (e *Engine) Close() error {
	e.closed.Store(true)
	return nil
}
