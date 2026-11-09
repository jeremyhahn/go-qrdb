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
//
// A backend whose type implements Exclusive but cannot keep that promise for
// the store it was opened on (an object store whose provider cannot exclude a
// second opener) returns *ExclusiveUnsupportedError without calling fn, and
// the caller falls back exactly as for a backend that does not implement it.
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

// Guarded is optionally implemented by a backend whose guarded writes need
// more than the caller's own mutex even within one process: an object store
// settles a half-applied batch commit before fn reads, so a read-check-write
// never compares against it. It promises nothing about other processes; a
// caller uses it where Exclusive is not implemented or refuses with
// ExclusiveUnsupportedError:
//
//	if g, ok := stg.(storage.Guarded); ok { err = g.Guarded(fn) }
type Guarded interface {
	// Guarded runs fn while holding the backend's write lock for this
	// handle, so no write through the handle lands between fn's read and its
	// write. The ReadWriter rules of Exclusive apply to fn.
	Guarded(fn func(ReadWriter) error) error
}

// ExclusiveUnsupportedError is returned by Exclusive, before fn runs, when the
// backend cannot exclude writers in other processes for the store it was
// opened on. Backend names the backend, such as an object-store provider.
type ExclusiveUnsupportedError struct {
	Backend string
}

// Error returns a human-readable description of the refusal.
func (e *ExclusiveUnsupportedError) Error() string {
	return "storage: " + e.Backend + " cannot exclude writers in other processes, so it offers no exclusive write lock"
}
