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

// Package storage provides a canonical storage engine interface and implementations
// for go-qrdb. The Engine interface is designed for general-purpose key-value storage
// with support for prefix listing and existence checks.
package storage

// Engine defines the canonical storage interface. Implementations include
// memory-backed, file-backed, and PebbleDB-backed engines.
// The interface is compatible with go-quicraft's seal.StorageBackend
// via Go duck typing.
type Engine interface {
	// Get retrieves the value for the given key.
	// Returns ErrKeyNotFound if the key does not exist.
	Get(key string) ([]byte, error)

	// Put stores a key-value pair. Overwrites any existing value.
	Put(key string, data []byte) error

	// Delete removes a key. Returns nil if the key does not exist.
	Delete(key string) error

	// List returns all keys matching the given prefix.
	// An empty prefix returns all keys.
	List(prefix string) ([]string, error)

	// Exists reports whether the key exists in storage.
	Exists(key string) (bool, error)

	// Close releases resources held by the engine.
	Close() error
}
