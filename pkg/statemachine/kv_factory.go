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
	"context"
	"io"
	"log/slog"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// NewKVStandardCreateFunc returns an sm.CreateFunc that creates
// KVStateMachine instances backed by StandardBackend. The shardID and
// replicaID parameters from QuicRaft are not used by this backend.
func NewKVStandardCreateFunc(opts ...KVOption) sm.CreateFunc {
	return sm.NewCreateFunc(func(_, _ uint64) sm.StateMachine {
		backend := NewStandardBackend()
		kvsm := NewKVStateMachine(backend, opts...)
		kvsm.resetLessor()
		return &StandardAdapter{KVStateMachine: kvsm}
	})
}

// NewKVConcurrentCreateFunc returns an sm.CreateFunc that creates
// KVStateMachine instances backed by ConcurrentBackend. The returned
// function produces ConcurrentAdapter instances that support concurrent
// Lookups during Update.
func NewKVConcurrentCreateFunc(opts ...KVOption) sm.CreateFunc {
	return sm.NewConcurrentCreateFunc(func(_, _ uint64) sm.ConcurrentStateMachine {
		backend := NewConcurrentBackend()
		kvsm := NewKVStateMachine(backend, opts...)
		kvsm.resetLessor()
		return &ConcurrentAdapter{KVStateMachine: kvsm}
	})
}

// NewKVFastCreateFunc returns an sm.CreateFunc that creates
// KVStateMachine instances backed by FastBackend. The storageFactory
// provides the backing storage.Storage for each shard/replica pair; when
// it fails, the replica's Open returns that error and the shard does not
// start.
func NewKVFastCreateFunc(storageFactory StorageFactory, opts ...KVOption) sm.CreateFunc {
	return func(shardID, replicaID uint64) interface{} {
		store, err := storageFactory(shardID, replicaID)
		if err != nil {
			return &unopenedReplica{err: err}
		}
		// Extract logger from options if provided, otherwise use default.
		var logger *slog.Logger
		probe := &KVStateMachine{logger: slog.Default()}
		for _, opt := range opts {
			opt(probe)
		}
		logger = probe.logger

		backend := NewFastBackend(shardID, replicaID, store, logger)
		// The replica starts from an EMPTY keyspace, whatever its storage
		// holds from a previous run. This state machine is an in-memory one to
		// Raft: on every start Raft rebuilds it from the latest snapshot
		// (RecoverSnapshot clears and repopulates storage) or, with none, by
		// replaying the log from the beginning. Left in place, the old
		// contents are the state AFTER entries that are about to be replayed,
		// and a cache miss reads through to them: a transaction replayed over
		// its own result can decide its compares the other way and apply the
		// other branch, so this replica diverges from the log every other
		// replica applied once.
		if err := backend.clearStorage(); err != nil {
			_ = store.Close()
			return &unopenedReplica{err: &ReplicaStorageOpenError{ShardID: shardID, ReplicaID: replicaID, Err: err}}
		}
		kvsm := NewKVStateMachine(backend, opts...)
		kvsm.resetLessor()
		return &StandardAdapter{KVStateMachine: kvsm}
	}
}

// unopenedReplica stands in for a replica whose storage could not be opened.
// sm.CreateFunc cannot return an error, so the create function returns this
// instead: the host's Open of it returns err and the shard fails to start
// with that error, rather than the replica panicking. Nothing else is called
// on a replica that did not open; each method returns err all the same.
type unopenedReplica struct {
	err error
}

var _ sm.DiskStateMachine = (*unopenedReplica)(nil)

// Open returns the storage failure.
func (r *unopenedReplica) Open(context.Context, string, <-chan struct{}) (uint64, error) {
	return 0, r.err
}

// Update returns the storage failure.
func (r *unopenedReplica) Update(context.Context, []sm.Entry, []sm.Result) error {
	return r.err
}

// Lookup returns the storage failure.
func (r *unopenedReplica) Lookup(context.Context, interface{}) (interface{}, error) {
	return nil, r.err
}

// Sync returns the storage failure.
func (r *unopenedReplica) Sync() error {
	return r.err
}

// PrepareSnapshot returns the storage failure.
func (r *unopenedReplica) PrepareSnapshot() (interface{}, error) {
	return nil, r.err
}

// SaveSnapshot returns the storage failure.
func (r *unopenedReplica) SaveSnapshot(context.Context, interface{}, io.Writer, <-chan struct{}) error {
	return r.err
}

// RecoverFromSnapshot returns the storage failure.
func (r *unopenedReplica) RecoverFromSnapshot(context.Context, io.Reader, <-chan struct{}) error {
	return r.err
}

// Close has nothing to release: no storage was opened.
func (r *unopenedReplica) Close(context.Context) error {
	return nil
}

// NewKVDiskCreateFunc returns an sm.CreateFunc that creates
// KVStateMachine instances backed by DiskBackend. The storageFactory
// provides the backing storage.Storage for each shard/replica pair; when
// it fails, the replica's Open returns that error and the shard does not
// start.
func NewKVDiskCreateFunc(storageFactory StorageFactory, opts ...KVOption) sm.CreateFunc {
	return sm.NewDiskCreateFunc(func(shardID, replicaID uint64) sm.DiskStateMachine {
		// The shard/replica identity MUST reach the storage factory: every
		// replica needs its own storage path, and a restarted replica must
		// reopen the SAME path (DiskAdapter.Open resumes from the persisted
		// applied index there). The previous hardcoded (0, 0) gave every
		// shard on a node one shared, unreusable location.
		stg, err := storageFactory(shardID, replicaID)
		if err != nil {
			return &unopenedReplica{err: err}
		}
		return newDiskAdapter(stg, opts...)
	})
}
