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
	"log/slog"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// NewKVStandardCreateFunc returns an sm.CreateFunc that creates
// KVStateMachine instances backed by StandardBackend. The shardID and
// replicaID parameters from QuicRaft are not used by this backend.
func NewKVStandardCreateFunc(opts ...KVOption) sm.CreateFunc {
	return sm.NewCreateFunc(func(_, _ uint64) sm.StateMachine {
		backend := NewStandardBackend()
		kvsm := NewKVStateMachine(backend, opts...)
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
		return &ConcurrentAdapter{KVStateMachine: kvsm}
	})
}

// NewKVFastCreateFunc returns an sm.CreateFunc that creates
// KVStateMachine instances backed by FastBackend. The storageFactory
// provides the backing storage.Storage for each shard/replica pair.
func NewKVFastCreateFunc(storageFactory func(shardID, replicaID uint64) storage.Storage, opts ...KVOption) sm.CreateFunc {
	return sm.NewCreateFunc(func(shardID, replicaID uint64) sm.StateMachine {
		store := storageFactory(shardID, replicaID)
		// Extract logger from options if provided, otherwise use default.
		var logger *slog.Logger
		probe := &KVStateMachine{logger: slog.Default()}
		for _, opt := range opts {
			opt(probe)
		}
		logger = probe.logger

		backend := NewFastBackend(shardID, replicaID, store, logger)
		kvsm := NewKVStateMachine(backend, opts...)
		return &StandardAdapter{KVStateMachine: kvsm}
	})
}

// NewKVDiskCreateFunc returns an sm.CreateFunc that creates
// KVStateMachine instances backed by DiskBackend. The storageFactory
// provides the backing storage.Storage for each shard/replica pair.
func NewKVDiskCreateFunc(storageFactory func(shardID, replicaID uint64) storage.Storage, opts ...KVOption) sm.CreateFunc {
	return sm.NewDiskCreateFunc(func(shardID, replicaID uint64) sm.DiskStateMachine {
		// The shard/replica identity MUST reach the storage factory: every
		// replica needs its own storage path, and a restarted replica must
		// reopen the SAME path (DiskAdapter.Open resumes from the persisted
		// applied index there). The previous hardcoded (0, 0) gave every
		// shard on a node one shared, unreusable location.
		store := storageFactory(shardID, replicaID)
		backend := NewDiskBackend(store)
		kvsm := NewKVStateMachine(backend, opts...)
		return &DiskAdapter{KVStateMachine: kvsm, store: store}
	})
}
