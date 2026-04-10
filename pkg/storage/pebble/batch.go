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
	"sync"
	"sync/atomic"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// pebbleBatch implements storage.Batch backed by a PebbleDB batch.
// Thread-safe: all mutation methods are guarded by a mutex.
type pebbleBatch struct {
	batch      *pebble.Batch
	mu         sync.Mutex
	closed     atomic.Bool
	syncWrites bool
}

// newBatch creates a new PebbleDB batch.
func newBatch(db *pebble.DB, syncWrites bool) storage.Batch {
	return &pebbleBatch{
		batch:      db.NewBatch(),
		syncWrites: syncWrites,
	}
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

// Commit atomically applies all batched operations. The batch is
// considered closed after a successful commit.
func (b *pebbleBatch) Commit() error {
	if b.closed.Load() {
		return &storage.BatchError{Op: "commit", Err: &storage.ClosedError{}}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	writeOpts := pebble.NoSync
	if b.syncWrites {
		writeOpts = pebble.Sync
	}

	if err := b.batch.Commit(writeOpts); err != nil {
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
