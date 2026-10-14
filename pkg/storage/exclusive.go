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

package storage

// ReadWriter is the point-lookup, point-write view of a store that Exclusive
// hands to its callback: exactly what a guarded write (compare, then write)
// needs, and nothing that could take the exclusion a second time.
type ReadWriter interface {
	// Get retrieves a value by key. Returns nil, nil when key does not exist.
	Get(key []byte) ([]byte, error)

	// Set stores a key-value pair.
	Set(key, value []byte) error

	// Delete removes a key.
	Delete(key []byte) error
}

// Exclusive is optionally implemented by a backend whose data may be open in
// more than one process at once, and which can therefore exclude every other
// writer -- in this process and in the others -- for the duration of a
// read-check-write. A caller that makes a guarded write indivisible type-asserts
// for it:
//
//	if x, ok := stg.(storage.Exclusive); ok { err = x.Exclusive(fn) }
//
// and falls back to its own in-process mutex when the backend is not shared. It
// is deliberately not part of Storage: an in-memory or single-owner backend has
// nothing to add, and an in-process mutex is the whole of its atomicity.
type Exclusive interface {
	// Exclusive runs fn while holding the backend's exclusive write lock. Every
	// other writer on the same data blocks until fn returns, whichever process
	// it is in, so a value fn reads cannot change before fn writes. The
	// ReadWriter passed to fn is valid only for the duration of the call and
	// must not be retained; calling the backend's own Set, Delete or Batch from
	// inside fn would wait on the lock fn already holds, so fn writes through
	// the ReadWriter instead. The lock is released on every return path,
	// including when fn returns an error or panics.
	Exclusive(fn func(ReadWriter) error) error
}
