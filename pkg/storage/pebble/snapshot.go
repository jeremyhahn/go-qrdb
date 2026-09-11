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

package pebble

import (
	"errors"
	"sync/atomic"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// pebbleSnapshot implements storage.Snapshot backed by a PebbleDB snapshot.
//
// store is the snapshot's parent. Closing the store closes the DB the snapshot
// reads through — PebbleDB reports the open snapshot as leaked but closes
// anyway — so the snapshot's own closed flag is not enough and every read
// re-enters the store's close gate. See PebbleStorage for why the gate exists.
type pebbleSnapshot struct {
	store  *PebbleStorage
	snap   *pebble.Snapshot
	closed atomic.Bool
}

// newSnapshot wraps a PebbleDB snapshot taken from s.
func newSnapshot(s *PebbleStorage, snap *pebble.Snapshot) storage.Snapshot {
	return &pebbleSnapshot{store: s, snap: snap}
}

// Get retrieves the value for the given key from the snapshot. Returns a
// defensive copy. Returns nil, nil when the key does not exist.
func (s *pebbleSnapshot) Get(key []byte) ([]byte, error) {
	if s.closed.Load() {
		return nil, &storage.SnapshotError{Op: "get", Err: &storage.ClosedError{}}
	}
	if len(key) == 0 {
		return nil, &storage.SnapshotError{Op: "get", Err: storage.ErrEmptyKey}
	}

	s.store.gate.RLock()
	defer s.store.gate.RUnlock()
	if s.store.closed.Load() {
		return nil, &storage.SnapshotError{Op: "get", Err: &storage.ClosedError{}}
	}

	value, closer, err := s.snap.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, nil
		}
		return nil, &storage.SnapshotError{Op: "get", Err: err}
	}
	defer func() { _ = closer.Close() }()

	result := make([]byte, len(value))
	copy(result, value)
	return result, nil
}

// NewIterator creates an iterator over the snapshot. A nil opts creates
// an unbounded forward iterator.
func (s *pebbleSnapshot) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	if s.closed.Load() {
		return storage.NewClosedIterator()
	}

	s.store.gate.RLock()
	defer s.store.gate.RUnlock()
	if s.store.closed.Load() {
		return storage.NewClosedIterator()
	}
	return newSnapshotIterator(s.snap, opts)
}

// Close releases resources held by the snapshot. Calling Close on an
// already-closed snapshot is a no-op, and so is closing one whose store has
// already been closed: the DB that owned the snapshot is gone, so there is
// nothing left to release.
func (s *pebbleSnapshot) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}

	s.store.gate.RLock()
	defer s.store.gate.RUnlock()
	if s.store.closed.Load() {
		return nil
	}
	if err := s.snap.Close(); err != nil {
		return &storage.SnapshotError{Op: "close", Err: err}
	}
	return nil
}
