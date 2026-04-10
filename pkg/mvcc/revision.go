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

package mvcc

import (
	"context"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// revisionManager manages the global revision counter for MVCC.
// It ensures atomic increments and persistent storage of the revision.
type revisionManager struct {
	current  int64 // Atomic counter for current revision
	store    storage.Storage
	dirty    atomic.Bool // Indicates if revision needs to be persisted
	lastSync int64       // Last persisted revision
}

// newRevisionManager creates a new revision manager.
// It loads the current revision from storage or starts at 1.
func newRevisionManager(store storage.Storage) (*revisionManager, error) {
	rm := &revisionManager{
		store:    store,
		current:  0,
		lastSync: 0,
	}

	// Load current revision from storage
	data, err := store.Get(RevisionKey())
	if err != nil {
		// If not found, start at revision 1
		if errors.IsNotFound(err) {
			rm.current = 1
			rm.lastSync = 1
			// Persist initial revision
			if err := rm.persist(); err != nil {
				return nil, err
			}
			return rm, nil
		}
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"revisionManager.newRevisionManager",
			"failed to load current revision",
		)
	}

	rev, err := DecodeRevision(data)
	if err != nil {
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"revisionManager.newRevisionManager",
			"failed to decode revision",
		)
	}

	rm.current = rev
	rm.lastSync = rev

	return rm, nil
}

// next atomically increments the revision and returns the new value.
// The revision is not persisted immediately for performance.
func (rm *revisionManager) next() int64 {
	rev := atomic.AddInt64(&rm.current, 1)
	rm.dirty.Store(true)
	return rev
}

// getCurrent returns the current revision without incrementing.
func (rm *revisionManager) getCurrent() int64 {
	return atomic.LoadInt64(&rm.current)
}

// persist writes the current revision to storage.
// This should be called periodically or during critical operations.
func (rm *revisionManager) persist() error {
	current := rm.getCurrent()

	// Only persist if revision has changed
	if current == rm.lastSync {
		rm.dirty.Store(false)
		return nil
	}

	data := EncodeRevision(current)
	if err := rm.store.Set(RevisionKey(), data); err != nil {
		return errors.Wrapf(
			err,
			errors.ErrStorage,
			"revisionManager.persist",
			"failed to persist revision",
		)
	}

	rm.lastSync = current
	rm.dirty.Store(false)

	return nil
}

// persistInBatch adds the current revision to a batch.
// This allows atomic persistence with other operations.
func (rm *revisionManager) persistInBatch(batch storage.Batch) error {
	current := rm.getCurrent()

	data := EncodeRevision(current)
	if err := batch.Set(RevisionKey(), data); err != nil {
		return errors.Wrapf(
			err,
			errors.ErrStorage,
			"revisionManager.persistInBatch",
			"failed to add revision to batch",
		)
	}

	return nil
}

// isDirty returns true if the revision needs to be persisted.
func (rm *revisionManager) isDirty() bool {
	return rm.dirty.Load()
}

// sync ensures the current revision is persisted to storage.
// This is called during shutdown or before critical operations.
func (rm *revisionManager) sync(ctx context.Context) error {
	if !rm.isDirty() {
		return nil
	}

	// Check context before expensive I/O
	if err := ctx.Err(); err != nil {
		return errors.Wrap(
			err,
			errors.ErrCanceled,
			"revisionManager.sync",
		)
	}

	return rm.persist()
}

// setIfGreater sets the revision to the given value if it's greater than current.
// This is used during recovery or replication.
func (rm *revisionManager) setIfGreater(rev int64) {
	for {
		current := rm.getCurrent()
		if rev <= current {
			return
		}
		if atomic.CompareAndSwapInt64(&rm.current, current, rev) {
			rm.dirty.Store(true)
			return
		}
	}
}

// reset sets the revision to a specific value (for testing).
func (rm *revisionManager) reset(rev int64) error {
	atomic.StoreInt64(&rm.current, rev)
	rm.dirty.Store(true)
	return rm.persist()
}
