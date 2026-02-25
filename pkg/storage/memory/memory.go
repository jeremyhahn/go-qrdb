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

// Package memory provides an in-memory storage engine for testing and ephemeral use.
package memory

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Compile-time interface check.
var _ storage.Engine = (*Engine)(nil)

// Engine is an in-memory storage engine backed by sync.Map.
type Engine struct {
	data   sync.Map
	closed atomic.Bool
}

// New creates a new in-memory storage engine.
func New() *Engine {
	return &Engine{}
}

// Get retrieves the value for the given key.
func (e *Engine) Get(key string) ([]byte, error) {
	if e.closed.Load() {
		return nil, storage.ErrStorageClosed
	}
	if key == "" {
		return nil, storage.ErrEmptyKey
	}
	val, ok := e.data.Load(key)
	if !ok {
		return nil, storage.ErrKeyNotFound
	}
	// Return a copy to prevent aliasing.
	src := val.([]byte)
	out := make([]byte, len(src))
	copy(out, src)
	return out, nil
}

// Put stores a key-value pair.
func (e *Engine) Put(key string, data []byte) error {
	if e.closed.Load() {
		return storage.ErrStorageClosed
	}
	if key == "" {
		return storage.ErrEmptyKey
	}
	// Store a copy to prevent aliasing.
	cp := make([]byte, len(data))
	copy(cp, data)
	e.data.Store(key, cp)
	return nil
}

// Delete removes a key.
func (e *Engine) Delete(key string) error {
	if e.closed.Load() {
		return storage.ErrStorageClosed
	}
	if key == "" {
		return storage.ErrEmptyKey
	}
	e.data.Delete(key)
	return nil
}

// List returns all keys matching the given prefix.
func (e *Engine) List(prefix string) ([]string, error) {
	if e.closed.Load() {
		return nil, storage.ErrStorageClosed
	}
	var keys []string
	e.data.Range(func(key, _ any) bool {
		k := key.(string)
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
		return true
	})
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
	_, ok := e.data.Load(key)
	return ok, nil
}

// Close marks the engine as closed. Subsequent operations return ErrStorageClosed.
func (e *Engine) Close() error {
	e.closed.Store(true)
	return nil
}
