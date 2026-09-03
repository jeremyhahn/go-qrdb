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

import (
	"io"
	"reflect"
)

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

// GuardedWriteBackend is the OPT-IN declaration that a Backend's Put and
// Delete can carry what a guarded verb's result promises:
//
//   - a write that returns nil has taken effect and is durable, so a caller
//     told "you won the claim" or "you released it" is not contradicted by a
//     restart;
//   - a write that fails RETURNS its error rather than logging it, so the
//     guard can refuse instead of reporting a success that did not happen;
//   - a write is ordered after every earlier write to the same key, so no
//     pending older write can overwrite it.
//
// It is a separate optional interface rather than a method on Backend so that
// adding it breaks no existing implementation — including any outside this
// repository — and it is OPT-IN rather than opt-out for the reason that
// matters: a backend that does not declare it is REFUSED, so a new backend
// whose author has not thought about ordering fails closed. Getting this
// backwards would make the next FastBackend silently unsafe instead of
// silently unavailable.
//
// The third clause is the one with history. FastBackend used to satisfy
// NEITHER the first nor the third: it writes its cache synchronously and then
// persisted on an unordered background goroutine whose error it swallows.
// Measured deterministically on that backend with ordinary writes: Put(k,"old")
// followed by Put(k,"new") left storage holding "old", and Put(k,"v") followed
// by Delete(k) left the key PRESENT — the value resurrected after its own
// delete. go-qrdb#224 fixed the ordering by giving it the same persistOrder
// discipline FastStateMachine and ConcurrentStateMachine have had since
// go-qrdb#86.
//
// It still does NOT implement this interface, because the first two clauses
// are untouched: Put returns before the value is durable, and a storage
// failure is logged rather than returned. The guarded verbs therefore continue
// to refuse on it with a typed GuardedWriteUnsupportedError. Supplying the
// remaining two promises needs a way for a Backend to express a synchronous
// write, which is an interface change and the owner's call: go-qrdb#216.
type GuardedWriteBackend interface {
	Backend

	// SupportsGuardedWrites reports the three promises above. It is a method
	// rather than a bare marker so a backend whose guarantee depends on how it
	// was constructed can answer for the instance rather than for the type.
	SupportsGuardedWrites() bool
}

// backendSupportsGuardedWrites reports whether b can carry a guarded verb's
// result. A backend that does not implement GuardedWriteBackend answers no.
func backendSupportsGuardedWrites(b Backend) bool {
	gw, ok := b.(GuardedWriteBackend)
	return ok && gw.SupportsGuardedWrites()
}

// backendName is the backend's type name without its package qualifier, for
// naming it in a refusal an operator has to act on.
//
// Reflection rather than a Name() method on Backend: this is a diagnostic
// string and nothing branches on it, so it does not justify widening the
// interface every implementation — including any outside this repository —
// would then have to satisfy.
func backendName(b Backend) string {
	if b == nil {
		return "nil"
	}
	t := reflect.TypeOf(b)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Name()
}
