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
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Compile-time interface check.
var _ storage.Engine = (*Engine)(nil)

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

func (e *Engine) keyPath(key string) string {
	return filepath.Join(e.dir, filepath.FromSlash(key))
}

// Get retrieves the value for the given key.
func (e *Engine) Get(key string) ([]byte, error) {
	if e.closed.Load() {
		return nil, storage.ErrStorageClosed
	}
	if key == "" {
		return nil, storage.ErrEmptyKey
	}
	data, err := os.ReadFile(e.keyPath(key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, storage.ErrKeyNotFound
		}
		return nil, err
	}
	return data, nil
}

// Put stores a key-value pair as a file on disk.
func (e *Engine) Put(key string, data []byte) error {
	if e.closed.Load() {
		return storage.ErrStorageClosed
	}
	if key == "" {
		return storage.ErrEmptyKey
	}
	path := e.keyPath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Delete removes a key from storage.
func (e *Engine) Delete(key string) error {
	if e.closed.Load() {
		return storage.ErrStorageClosed
	}
	if key == "" {
		return storage.ErrEmptyKey
	}
	err := os.Remove(e.keyPath(key))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// List returns all keys matching the given prefix.
func (e *Engine) List(prefix string) ([]string, error) {
	if e.closed.Load() {
		return nil, storage.ErrStorageClosed
	}
	var keys []string
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
		key := filepath.ToSlash(rel)
		if strings.HasPrefix(key, prefix) {
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
func (e *Engine) Exists(key string) (bool, error) {
	if e.closed.Load() {
		return false, storage.ErrStorageClosed
	}
	if key == "" {
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

// Close marks the engine as closed. Subsequent operations return ErrStorageClosed.
func (e *Engine) Close() error {
	e.closed.Store(true)
	return nil
}
