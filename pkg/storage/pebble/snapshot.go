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
type pebbleSnapshot struct {
	snap   *pebble.Snapshot
	closed atomic.Bool
}

// newSnapshot wraps a PebbleDB snapshot.
func newSnapshot(snap *pebble.Snapshot) storage.Snapshot {
	return &pebbleSnapshot{snap: snap}
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
		return &pebbleIterator{err: &storage.ClosedError{}}
	}
	return newSnapshotIterator(s.snap, opts)
}

// Close releases resources held by the snapshot. Calling Close on an
// already-closed snapshot is a no-op.
func (s *pebbleSnapshot) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	if err := s.snap.Close(); err != nil {
		return &storage.SnapshotError{Op: "close", Err: err}
	}
	return nil
}
