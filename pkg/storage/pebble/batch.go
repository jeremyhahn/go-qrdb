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
	"sync"
	"sync/atomic"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Compile-time interface assertion.
var _ storage.Batch = (*pebbleBatch)(nil)

// pebbleBatch implements storage.Batch backed by a PebbleDB batch.
// Thread-safe: all mutation methods are guarded by a mutex.
//
// store is the batch's parent. A batch's own closed flag says nothing about
// whether the DB underneath it is still open — an open batch does NOT keep
// PebbleDB from closing — so every method that reaches through to the DB
// (Get, NewIterator, Commit) re-enters the store's close gate and re-checks.
// Set and Delete stay out of it: an indexed batch buffers them in its own
// skiplist and touches no DB state, which is why a closed store's batch still
// accepts writes and refuses only at Commit, matching the memory, file and
// objstore backends.
//
// Lock order is store.gate then mu, everywhere, and PebbleStorage.Close never
// takes mu.
type pebbleBatch struct {
	store      *PebbleStorage
	batch      *pebble.Batch
	mu         sync.Mutex
	closed     atomic.Bool
	syncWrites bool
}

// newBatch creates a new PebbleDB batch.
//
// The batch is INDEXED because storage.Batch requires read-your-own-writes:
// PebbleDB's indexed batch reads from the batch's pending writes layered over
// the DB, which is exactly the semantics the interface promises and is what
// keeps a Raft apply loop's entry grouping invisible in the resulting state. An
// indexed batch maintains a skiplist over its own contents, so writes cost more
// than a write-only batch; that cost buys the determinism and is paid only on
// the apply path, which is the one place that reads what it is writing.
func newBatch(s *PebbleStorage) storage.Batch {
	return &pebbleBatch{
		store:      s,
		batch:      s.db.NewIndexedBatch(),
		syncWrites: s.config.SyncWrites,
	}
}

// storeClosed reports whether the parent store has been closed. It is only
// reached from a batch that is itself still open, and every such batch came
// from newBatch, so store is set.
func (b *pebbleBatch) storeClosed() bool {
	return b.store.closed.Load()
}

// Get returns the value for key as the batch sees it: the batch's own pending
// write when it has one, otherwise the committed value from the database.
// Returns nil, nil when the key exists in neither, and nil, nil when the batch
// holds a pending delete for it.
func (b *pebbleBatch) Get(key []byte) ([]byte, error) {
	if b.closed.Load() {
		return nil, &storage.BatchError{Op: "get", Err: &storage.ClosedError{}}
	}
	if len(key) == 0 {
		return nil, &storage.BatchError{Op: "get", Err: storage.ErrEmptyKey}
	}

	b.store.gate.RLock()
	defer b.store.gate.RUnlock()
	if b.storeClosed() {
		return nil, &storage.BatchError{Op: "get", Err: &storage.ClosedError{}}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	value, closer, err := b.batch.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, nil
		}
		return nil, &storage.BatchError{Op: "get", Err: err}
	}
	defer func() { _ = closer.Close() }()

	result := make([]byte, len(value))
	copy(result, value)
	return result, nil
}

// NewIterator returns an iterator over the batch's pending writes layered on
// the database's committed contents.
func (b *pebbleBatch) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	if b.closed.Load() {
		return &pebbleIterator{err: &storage.BatchError{Op: "iterate", Err: &storage.ClosedError{}}}
	}

	b.store.gate.RLock()
	defer b.store.gate.RUnlock()
	if b.storeClosed() {
		return &pebbleIterator{err: &storage.BatchError{Op: "iterate", Err: &storage.ClosedError{}}}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	iter, err := b.batch.NewIter(buildIterOptions(opts))
	if err != nil {
		return &pebbleIterator{err: err}
	}
	return &pebbleIterator{iter: iter}
}

// Set adds a key-value pair to the batch.
func (b *pebbleBatch) Set(key, value []byte) error {
	if b.closed.Load() {
		return &storage.BatchError{Op: "set", Err: &storage.ClosedError{}}
	}
	if len(key) == 0 {
		return &storage.BatchError{Op: "set", Err: storage.ErrEmptyKey}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.batch.Set(key, value, pebble.NoSync); err != nil {
		return &storage.BatchError{Op: "set", Err: err}
	}
	return nil
}

// Delete adds a key deletion to the batch.
func (b *pebbleBatch) Delete(key []byte) error {
	if b.closed.Load() {
		return &storage.BatchError{Op: "delete", Err: &storage.ClosedError{}}
	}
	if len(key) == 0 {
		return &storage.BatchError{Op: "delete", Err: storage.ErrEmptyKey}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.batch.Delete(key, pebble.NoSync); err != nil {
		return &storage.BatchError{Op: "delete", Err: err}
	}
	return nil
}

// DeleteRange adds a range tombstone over [start, end) to the batch. An empty
// end is unbounded, and is closed past the last key the batch sees, pending
// writes included; finding that key reads the database, so the unbounded form
// enters the store's close gate where a bounded one, like Set, does not.
func (b *pebbleBatch) DeleteRange(start, end []byte) error {
	if b.closed.Load() {
		return &storage.BatchError{Op: "delete_range", Err: &storage.ClosedError{}}
	}
	if err := storage.ValidateRange(start, end); err != nil {
		return &storage.BatchError{Op: "delete_range", Err: err}
	}

	if len(end) == 0 {
		b.store.gate.RLock()
		defer b.store.gate.RUnlock()
		if b.storeClosed() {
			return &storage.BatchError{Op: "delete_range", Err: &storage.ClosedError{}}
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if len(end) == 0 {
		var err error
		if end, err = unboundedRangeEnd(b.batch, start); err != nil {
			return &storage.BatchError{Op: "delete_range", Err: err}
		}
		if end == nil {
			return nil
		}
	}
	if err := b.batch.DeleteRange(start, end, pebble.NoSync); err != nil {
		return &storage.BatchError{Op: "delete_range", Err: err}
	}
	return nil
}

// Commit atomically applies all batched operations. The batch is
// considered closed after a successful commit.
func (b *pebbleBatch) Commit() error {
	if b.closed.Load() {
		return &storage.BatchError{Op: "commit", Err: &storage.ClosedError{}}
	}

	b.store.gate.RLock()
	defer b.store.gate.RUnlock()
	if b.storeClosed() {
		return &storage.BatchError{Op: "commit", Err: &storage.ClosedError{}}
	}

	b.store.casMu.RLock()
	defer b.store.casMu.RUnlock()

	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.store.durably(b.syncWrites, b.batch.Commit); err != nil {
		return &storage.BatchError{Op: "commit", Err: err}
	}
	b.closed.Store(true)
	return nil
}

// Rollback discards all batched operations without applying them.
// The batch is considered closed after rollback.
func (b *pebbleBatch) Rollback() error {
	if b.closed.Load() {
		return &storage.BatchError{Op: "rollback", Err: &storage.ClosedError{}}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.batch.Close(); err != nil {
		return &storage.BatchError{Op: "rollback", Err: err}
	}
	b.closed.Store(true)
	return nil
}

// Close releases resources held by the batch. If the batch was neither
// committed nor rolled back, it performs an implicit rollback.
// Calling Close on an already-closed batch is a no-op.
func (b *pebbleBatch) Close() error {
	if !b.closed.CompareAndSwap(false, true) {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.batch.Close(); err != nil {
		return &storage.BatchError{Op: "close", Err: err}
	}
	return nil
}
