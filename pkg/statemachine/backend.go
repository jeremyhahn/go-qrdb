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
//
// There are TWO write contracts here, and the difference is the whole reason
// the interface carries four write methods instead of two.
//
// Put and Delete are the UNGUARDED pair. Their promise is "your write is
// queued": an implementation may buffer them and persist in the background,
// and a storage failure may be logged rather than returned. FastBackend exists
// to provide exactly that, and nothing in this change alters it.
//
// PutDurable and DeleteDurable are the GUARDED pair, and every implementation
// must satisfy three properties:
//
//   - DURABLE ON RETURN. A nil return means the write has taken effect, so a
//     caller told "you won the claim" or "you released it" is not contradicted
//     by a restart.
//   - FAILURE RETURNED. A storage fault comes back as an error rather than
//     reaching a log line nobody reads, so the guard can refuse instead of
//     reporting a success that did not happen.
//   - ORDERED. The write lands after every earlier write to the same key, so
//     no pending older write can overwrite it.
//
// They are METHODS ON Backend rather than an optional interface a backend may
// or may not implement (owner ruling 2026-09-03, go-qrdb#216, option 2). An
// optional interface is the idiomatic Go shape and it is wrong here: a backend
// that does not implement it falls back to the asynchronous path SILENTLY, and
// silently degrading is the exact thing the guarded plane exists to refuse. An
// opt-in guarantee is not a guarantee. On this interface a new backend — in
// this repository or outside it — cannot compile without answering, and the
// answer is visible in its source rather than inferred from a type assertion.
//
// A backend with no durability to promise satisfies the guarded pair by
// delegating: for a pure in-memory map the write has landed when Put returns,
// so the two contracts coincide. The pair is not redundant there — it is
// trivially satisfied there, which is a different statement, and the one
// FastBackend does not get to make.
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

	// PutDurable stores a key-value pair and returns only once the write
	// has taken effect, reporting a failure instead of logging it. See the
	// contract note above the interface.
	PutDurable(key, value []byte) error

	// DeleteDurable removes a key and returns only once the removal has
	// taken effect, reporting a failure instead of logging it. See the
	// contract note above the interface.
	DeleteDurable(key []byte) error

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
