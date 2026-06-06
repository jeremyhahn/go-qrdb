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

// Package pool provides qrdb-side wiring for the go-objstore volume
// pool primitive. It supplies a pool.StateStore implementation backed
// by a dedicated qrdb-owned PebbleStorage so the round-robin cursor
// survives daemon restarts.
//
// The pool primitive itself (Strategy, Manager, Hint) lives in
// github.com/jeremyhahn/go-objstore/pkg/pool and is consumed by the
// seal.BarrierRegistry to assign per-tenant on-disk paths.
package pool

import (
	"context"

	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// keyNamespace is the qrdb-scoped prefix prepended to every key the
// volume pool persists. It keeps the cursor blob isolated from
// application keys in the same Pebble instance and makes pool state
// trivially identifiable in operator dumps.
const keyNamespace = "__sys:volumepool/"

// PebbleStateStore implements objpool.StateStore on top of a qrdb
// *PebbleStorage. The store is intended for the round-robin cursor
// (a small, infrequently written 8-byte payload). It does not buffer
// or cache: every Get/Put round-trips to Pebble so cursors survive
// process restart with at-most-once-write durability semantics.
type PebbleStateStore struct {
	storage *pebblestore.PebbleStorage
}

// Compile-time interface assertion.
var _ objpool.StateStore = (*PebbleStateStore)(nil)

// NewPebbleStateStore constructs a PebbleStateStore backed by the
// supplied storage. The caller retains ownership of the storage's
// lifecycle; closing the StateStore is a no-op (this avoids
// double-close when the same Pebble is shared with other subsystems).
//
// Returns NilStorageError if storage is nil.
func NewPebbleStateStore(storage *pebblestore.PebbleStorage) (*PebbleStateStore, error) {
	if storage == nil {
		return nil, &NilStorageError{}
	}
	return &PebbleStateStore{storage: storage}, nil
}

// Get implements objpool.StateStore. A missing key returns
// found=false and a nil error so callers can apply a zero default.
func (s *PebbleStateStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if key == "" {
		return nil, false, &EmptyKeyError{}
	}

	value, err := s.storage.Get([]byte(namespacedKey(key)))
	if err != nil {
		return nil, false, &GetError{Key: key, Err: err}
	}
	if value == nil {
		return nil, false, nil
	}
	return value, true, nil
}

// Put implements objpool.StateStore. The supplied value bytes are
// copied by the underlying PebbleDB; the caller may reuse the buffer.
func (s *PebbleStateStore) Put(ctx context.Context, key string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if key == "" {
		return &EmptyKeyError{}
	}

	if err := s.storage.Set([]byte(namespacedKey(key)), value); err != nil {
		return &PutError{Key: key, Err: err}
	}
	return nil
}

// namespacedKey prepends the qrdb-scoped pool namespace to a logical
// pool key. The strategy-supplied key (e.g.
// "pool/<name>/round_robin/cursor") is preserved verbatim after the
// prefix so operators reading raw Pebble dumps can identify which
// strategy persisted which cursor.
func namespacedKey(key string) string {
	return keyNamespace + key
}
