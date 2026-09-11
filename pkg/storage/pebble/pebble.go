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
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Compile-time interface assertion.
var _ storage.Storage = (*PebbleStorage)(nil)

// PebbleStorage implements storage.Storage backed by PebbleDB.
// It provides persistent key-value storage with batch writes, iterators,
// and point-in-time snapshots for the Raft state machine layer.
//
// Thread safety: PebbleDB handles its own internal synchronization.
// Concurrent read and write capacity is bounded by semaphore channels.
//
// # Why Close is ordered against every operation
//
// PebbleDB answers a use-after-close with panic(pebble.ErrClosed), not an
// error — deliberately, to make illegal concurrent use during Close loud
// (pebble/db.go, applyInternal). A bare `if s.closed.Load()` guard is
// therefore not enough: the check and the DB call are two steps, and a Close
// that lands between them turns an ordinary shutdown race into a panic with
// no typed value to match (go-qrdb#371).
//
// gate closes that window. Every method that touches s.db holds gate.RLock
// across BOTH its closed check and its DB call; Close holds gate.Lock, so it
// cannot release the DB while an operation is inside that window and no
// operation can enter after it. That makes the check binding rather than
// advisory, which is what lets this layer return a typed *storage.ClosedError
// instead of recovering a panic and classifying it by text.
//
// gate is a reader-counter, which is precisely what the lock-free form of this
// would be: sync.RWMutex.RLock is a single atomic add on the uncontended path
// and it already carries the drain-on-close wait, so hand-rolling it with
// atomics would reproduce sync.RWMutex with more code and no fast path.
type PebbleStorage struct {
	db       *pebble.DB
	config   *Config
	gate     sync.RWMutex
	closed   atomic.Bool
	readSem  chan struct{}
	writeSem chan struct{}
}

// New creates a new PebbleDB storage instance. The config is validated,
// defaults applied, and the database opened at config.DataDir.
func New(config *Config) (*PebbleStorage, error) {
	if config == nil {
		return nil, &ConfigError{Field: "config", Message: "is required"}
	}

	if err := config.Validate(); err != nil {
		return nil, err
	}
	config = config.WithDefaults()

	opts := config.ToPebbleOptions()

	db, err := pebble.Open(config.DataDir, opts)
	if err != nil {
		return nil, &storage.OpError{Op: "open", Err: err}
	}

	return &PebbleStorage{
		db:       db,
		config:   config,
		readSem:  make(chan struct{}, config.MaxConcurrentReads),
		writeSem: make(chan struct{}, config.MaxConcurrentWrites),
	}, nil
}

// NewFromDB wraps an existing PebbleDB instance as a storage.Storage.
// The caller retains ownership of the DB lifecycle. Calling Close() on the
// returned PebbleStorage will close the underlying DB, so coordinate
// accordingly. SyncWrites controls whether batch commits use pebble.Sync
// or pebble.NoSync.
func NewFromDB(db *pebble.DB, syncWrites bool) *PebbleStorage {
	return &PebbleStorage{
		db: db,
		config: &Config{
			SyncWrites:          syncWrites,
			MaxConcurrentReads:  100,
			MaxConcurrentWrites: 100,
		},
		readSem:  make(chan struct{}, 100),
		writeSem: make(chan struct{}, 100),
	}
}

// DB returns the underlying PebbleDB instance. This is used by subsystems
// like index.NewPebbleIndex and timeseries.New that require direct PebbleDB
// access.
//
// The returned handle is OUTSIDE the close gate: a caller that keeps it past
// Close gets PebbleDB's raw semantics back, panic included. Subsystems built
// on it own their own lifecycle against this store's.
func (s *PebbleStorage) DB() *pebble.DB {
	return s.db
}

// Get retrieves the value for the given key. Returns a copy that is safe
// to retain. Returns nil, nil when the key does not exist per the
// storage.Storage contract.
func (s *PebbleStorage) Get(key []byte) ([]byte, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if s.closed.Load() {
		return nil, &storage.ClosedError{}
	}
	if len(key) == 0 {
		return nil, storage.ErrEmptyKey
	}

	s.readSem <- struct{}{}
	defer func() { <-s.readSem }()

	value, closer, err := s.db.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, nil
		}
		return nil, &storage.OpError{Op: "get", Err: err}
	}
	defer func() { _ = closer.Close() }()

	result := make([]byte, len(value))
	copy(result, value)
	return result, nil
}

// Set stores a key-value pair. Uses pebble.NoSync by default since
// the Raft WAL provides durability. Honors Config.SyncWrites when set.
func (s *PebbleStorage) Set(key, value []byte) error {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if s.closed.Load() {
		return &storage.ClosedError{}
	}
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}

	s.writeSem <- struct{}{}
	defer func() { <-s.writeSem }()

	writeOpts := pebble.NoSync
	if s.config.SyncWrites {
		writeOpts = pebble.Sync
	}

	if err := s.db.Set(key, value, writeOpts); err != nil {
		return &storage.OpError{Op: "set", Err: err}
	}
	return nil
}

// Delete removes a key-value pair. Deleting a non-existent key is a no-op.
func (s *PebbleStorage) Delete(key []byte) error {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if s.closed.Load() {
		return &storage.ClosedError{}
	}
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}

	s.writeSem <- struct{}{}
	defer func() { <-s.writeSem }()

	writeOpts := pebble.NoSync
	if s.config.SyncWrites {
		writeOpts = pebble.Sync
	}

	if err := s.db.Delete(key, writeOpts); err != nil {
		return &storage.OpError{Op: "delete", Err: err}
	}
	return nil
}

// List returns all keys matching the given prefix. An empty or nil prefix
// returns all keys. Uses a PebbleDB prefix iterator bounded by the computed
// upper key for efficient range scanning.
func (s *PebbleStorage) List(prefix []byte) ([][]byte, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if s.closed.Load() {
		return nil, &storage.ClosedError{}
	}

	s.readSem <- struct{}{}
	defer func() { <-s.readSem }()

	iterOpts := &pebble.IterOptions{}
	if len(prefix) > 0 {
		iterOpts.LowerBound = prefix
		upper := make([]byte, len(prefix))
		copy(upper, prefix)
		for i := len(upper) - 1; i >= 0; i-- {
			if upper[i] < 0xff {
				upper[i]++
				upper = upper[:i+1]
				iterOpts.UpperBound = upper
				break
			}
		}
	}

	iter, err := s.db.NewIter(iterOpts)
	if err != nil {
		return nil, &storage.OpError{Op: "list", Err: err}
	}
	defer func() { _ = iter.Close() }()

	var keys [][]byte
	for iter.First(); iter.Valid(); iter.Next() {
		if len(prefix) > 0 && !bytes.HasPrefix(iter.Key(), prefix) {
			break
		}
		cp := make([]byte, len(iter.Key()))
		copy(cp, iter.Key())
		keys = append(keys, cp)
	}
	if err := iter.Error(); err != nil {
		return nil, &storage.OpError{Op: "list", Err: err}
	}
	return keys, nil
}

// Exists reports whether the given key exists in storage. Returns false
// without error when the key is not found.
func (s *PebbleStorage) Exists(key []byte) (bool, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if s.closed.Load() {
		return false, &storage.ClosedError{}
	}
	if len(key) == 0 {
		return false, storage.ErrEmptyKey
	}

	s.readSem <- struct{}{}
	defer func() { <-s.readSem }()

	_, closer, err := s.db.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return false, nil
		}
		return false, &storage.OpError{Op: "exists", Err: err}
	}
	_ = closer.Close()
	return true, nil
}

// Batch creates a new write batch for atomic multi-key operations. On a
// closed store it returns a batch that reports the closure from every method
// rather than one wired to a dead DB: pebble.DB.NewIndexedBatch succeeds after
// Close and the panic then lands in Commit or Get, several frames from the
// mistake. That is the shape go-qrdb#371 was reported as — the MVCC engine's
// apply path is batch-then-commit, so its Put panicked out of Commit.
func (s *PebbleStorage) Batch() storage.Batch {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if s.closed.Load() {
		return storage.NewClosedBatch()
	}
	return newBatch(s)
}

// NewIterator creates an iterator for scanning key ranges.
// A nil opts creates an unbounded forward iterator. On a closed store the
// returned iterator is invalid and reports ErrStorageClosed from Error().
func (s *PebbleStorage) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if s.closed.Load() {
		return storage.NewClosedIterator()
	}
	return newIterator(s.db, opts)
}

// Snapshot creates a point-in-time snapshot for consistent reads.
func (s *PebbleStorage) Snapshot() (storage.Snapshot, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if s.closed.Load() {
		return nil, &storage.ClosedError{}
	}
	return newSnapshot(s, s.db.NewSnapshot()), nil
}

// Close releases all resources held by the storage. Subsequent operations
// return ClosedError. Calling Close on an already-closed storage is a no-op.
//
// It waits for every in-flight operation to leave the gate before releasing
// the DB, so an operation that has already passed its closed check always
// completes against a live DB.
//
// The closed flag is set BEFORE db.Close and is never rolled back if db.Close
// reports an error: PebbleDB closes the database regardless and returns the
// leaked-handle report as a value, so the store really is closed either way.
func (s *PebbleStorage) Close() error {
	s.gate.Lock()
	defer s.gate.Unlock()

	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return &storage.OpError{Op: "close", Err: err}
	}
	return nil
}

// Compact triggers a manual compaction over the given key range [start, end].
// Pass nil for both to compact the entire database.
func (s *PebbleStorage) Compact(start, end []byte) error {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if s.closed.Load() {
		return &storage.ClosedError{}
	}
	if err := s.db.Compact(context.Background(), start, end, true); err != nil {
		return &storage.OpError{Op: "compact", Err: err}
	}
	return nil
}

// Flush forces a flush of all memtables to sstables on disk.
func (s *PebbleStorage) Flush() error {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if s.closed.Load() {
		return &storage.ClosedError{}
	}
	if err := s.db.Flush(); err != nil {
		return &storage.OpError{Op: "flush", Err: err}
	}
	return nil
}

// Metrics returns the PebbleDB metrics snapshot for observability. It returns
// nil on a closed store: the metrics belong to a DB that no longer exists, and
// a zero-valued snapshot would be indistinguishable from a genuinely idle one.
func (s *PebbleStorage) Metrics() *pebble.Metrics {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if s.closed.Load() {
		return nil
	}
	return s.db.Metrics()
}
