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

package statemachine

import "io"

// Backend abstracts the underlying key-value storage for state machines.
// Implementations handle get/put/delete/list/scan operations with their
// own concurrency model (single-threaded, sharded, disk-backed, etc.).
type Backend interface {
	// Get retrieves the value for the given key. Returns nil, nil when
	// the key does not exist.
	Get(key []byte) ([]byte, error)

	// Put stores a key-value pair. Implementations may buffer writes
	// and persist asynchronously.
	Put(key, value []byte) error

	// Delete removes a key. Implementations may buffer deletes and
	// persist asynchronously.
	Delete(key []byte) error

	// List returns all keys matching the given prefix. An empty prefix
	// returns all keys.
	List(prefix []byte) ([][]byte, error)

	// Scan returns all key-value pairs matching the given prefix as a
	// map. When limit > 0, the scan stops after collecting that many
	// entries.
	Scan(prefix []byte, limit int) (map[string][]byte, error)

	// SaveSnapshot serializes all backend state to the writer. The
	// stopper channel signals cancellation.
	SaveSnapshot(w io.Writer, stopper <-chan struct{}) error

	// RecoverSnapshot replaces all backend state with data read from
	// the reader. The stopper channel signals cancellation.
	RecoverSnapshot(r io.Reader, stopper <-chan struct{}) error

	// GetHash computes a deterministic hash over all key-value pairs
	// for cross-replica consistency verification.
	GetHash() (uint64, error)

	// Close releases all resources held by the backend. It must wait
	// for any in-flight asynchronous operations to complete.
	Close() error
}
