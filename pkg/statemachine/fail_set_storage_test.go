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
	"bytes"
	"errors"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// This double lives in its own UNCONSTRAINED file because its callers do not
// share one build constraint: guarded_persist_test.go builds only under
// codec_json && raft && !integration, backend_atomic_test.go under every tag
// set. A helper's constraint must be a superset of every caller's.

// failSetStorage fails Set on demand while behaving normally otherwise.
//
// The failure is switchable rather than fixed because these tests need the
// state machine to reach a given state through WORKING writes and only then
// meet the fault -- a storage that failed every Set could never be seeded.
type failSetStorage struct {
	*memStorage
	failing atomic.Bool
	// failDelete makes Delete fail too. It exists so a test can leave a
	// RETIRED VALUE in storage under a cache tombstone -- the state that makes
	// the tombstone load-bearing. Delete is asynchronous and swallows its
	// error, so this is a state a real deployment reaches, not a contrivance.
	failDelete atomic.Bool
	// failKey, when non-nil, narrows the Set failure to a single key so a
	// multi-entry batch can commit an earlier entry and fault on a later one
	// (go-qrdb#94 batch replay). It is written before the apply that reads it,
	// on the same goroutine, since the guarded persist is synchronous.
	failKey []byte
	err     error

	// holdSet parks the next Set until it is closed; setHeld announces that
	// the Set is parked. Both are touched only by the test goroutine and the
	// single parked writer, in that order.
	holdSet chan struct{}
	setHeld chan struct{}
}

func newFailSetStorage(err error) *failSetStorage {
	return &failSetStorage{memStorage: newMemStorage(), err: err}
}

func (s *failSetStorage) Set(key, value []byte) error {
	if s.holdSet != nil {
		close(s.setHeld)
		<-s.holdSet
		s.holdSet = nil
	}
	if s.failing.Load() && (s.failKey == nil || bytes.Equal(key, s.failKey)) {
		return s.err
	}
	return s.memStorage.Set(key, value)
}

// holdFirstSet makes the next Set block until the returned release function is
// called, announcing on the returned channel that it is blocked.
//
// It is what makes the failure interleaving DETERMINISTIC: the guarded write
// is parked inside storage, with the shard lock released, which is precisely
// the window a later write has to land in. Racing two goroutines and hoping
// would test the interleaving only some of the time.
func (s *failSetStorage) holdFirstSet() (held <-chan struct{}, release func()) {
	s.setHeld = make(chan struct{})
	hold := make(chan struct{})
	s.holdSet = hold
	return s.setHeld, func() { close(hold) }
}

func (s *failSetStorage) Delete(key []byte) error {
	if s.failDelete.Load() {
		return s.err
	}
	return s.memStorage.Delete(key)
}

// Batch returns a batch that meets the same faults a direct write would: a
// batched Set fails exactly when Set would, and a batched Delete when Delete
// would. A storage whose writes fail does not start succeeding because the
// writes were grouped — a double that let them would pass a backend that
// commits through a batch against faults it never saw.
func (s *failSetStorage) Batch() storage.Batch {
	return &failSetBatch{memBatch: &memBatch{store: s.memStorage}, owner: s}
}

type failSetBatch struct {
	*memBatch
	owner *failSetStorage
}

func (b *failSetBatch) Set(key, value []byte) error {
	if b.owner.failing.Load() && (b.owner.failKey == nil || bytes.Equal(key, b.owner.failKey)) {
		return b.owner.err
	}
	return b.memBatch.Set(key, value)
}

func (b *failSetBatch) Delete(key []byte) error {
	if b.owner.failDelete.Load() {
		return b.owner.err
	}
	return b.memBatch.Delete(key)
}

var _ storage.Storage = (*failSetStorage)(nil)

// markerCrashStorage simulates the process dying between a storage batch and
// the disk state machine's trailing applied-index write: while crashing is
// set, a DIRECT Set of the marker key fails, and every batch commits. It is
// what lets a test ask which log index survives a crash at exactly that point.
type markerCrashStorage struct {
	*memStorage
	crashing atomic.Bool
}

func (s *markerCrashStorage) Set(key, value []byte) error {
	if s.crashing.Load() && string(key) == diskMetaKeyLastApplied {
		return errCrash
	}
	return s.memStorage.Set(key, value)
}

var errCrash = errors.New("crash before the trailing applied-index write")

var _ storage.Storage = (*markerCrashStorage)(nil)
