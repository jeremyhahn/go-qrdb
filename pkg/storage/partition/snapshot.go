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

package partition

import (
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Compile-time interface assertion.
var _ storage.Snapshot = (*partitionSnapshot)(nil)

// partitionSnapshot implements storage.Snapshot across multiple partition snapshots.
// Reads are routed to the correct partition snapshot using the router.
type partitionSnapshot struct {
	router    Router
	snapshots []storage.Snapshot
	closed    atomic.Bool
}

// newPartitionSnapshot creates a new composite snapshot.
func newPartitionSnapshot(router Router, snapshots []storage.Snapshot) *partitionSnapshot {
	return &partitionSnapshot{
		router:    router,
		snapshots: snapshots,
	}
}

// Get retrieves a value by key from the appropriate partition snapshot.
func (s *partitionSnapshot) Get(key []byte) ([]byte, error) {
	if s.closed.Load() {
		return nil, &storage.SnapshotError{Op: "get", Err: &storage.ClosedError{}}
	}
	id := s.router.Route(key)
	return s.snapshots[id].Get(key)
}

// NewIterator creates a merge iterator over all partition snapshots.
func (s *partitionSnapshot) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	if s.closed.Load() {
		return newClosedMergeIterator()
	}

	iters := make([]storage.Iterator, len(s.snapshots))
	for i, snap := range s.snapshots {
		iters[i] = snap.NewIterator(opts)
	}
	return newMergeIterator(iters, opts)
}

// Close releases all partition snapshots.
func (s *partitionSnapshot) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}

	var firstErr error
	for _, snap := range s.snapshots {
		if err := snap.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
