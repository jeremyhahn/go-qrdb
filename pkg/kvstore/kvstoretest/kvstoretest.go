// Copyright 2026 Jeremy Hahn
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

// Package kvstoretest holds helpers for tests that need a
// kvstore.MultiGuardedBatchKVStore without a real transaction plane.
//
// A plain *kvstore.MemoryKVStore deliberately does not implement
// PutAllIfUnchanged/PutAllIfAllUnchanged -- its atomicity is a mutex, not
// consensus, and the guarded-batch capability is reserved for stores a real
// transaction plane backs (see
// kvstore.TestNewNamespacedKVStore_AtomicOnlyStillWorks, a regression guard
// for exactly that boundary). Production callers that need the capability
// get it from lifecycle.NewGuardedKVStoreAdapter, backed by the real
// transaction service. A unit test that only needs SOME store offering the
// capability -- cakey.NewSoftwareProvider's split key/certificate records,
// in particular -- gets one from GuardedMemoryStore instead of hand-rolling
// the same wrapper in every package that needs it.
//
// It is imported by tests only; a production import of it is a defect.
package kvstoretest

import (
	"bytes"
	"context"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// GuardedMemoryStore adds kvstore.MultiGuardedBatchKVStore to a real
// in-memory store. Batches serialise against EACH OTHER on an internal
// mutex, so two batches never interleave their guard checks, writes, or
// rollbacks. That mutex is NOT held by the plain KVStore methods this type
// inherits from *kvstore.MemoryKVStore (Put, Get, Delete, ...): each of
// those is independently atomic under MemoryKVStore's own lock, but a
// batch's guard check, its writes, and a possible rollback are several
// such calls in sequence, and an ordinary Put racing between two of them
// is not excluded. A test that needs a batch's net effect to be atomic
// with respect to a concurrent ordinary Put on the same key must
// serialise that itself; this type only guarantees batch-vs-batch
// ordering.
//
// A batch is all-or-nothing even when a WRITE fails partway through, not
// only when a guard refuses up front: PutAllIfAllUnchanged snapshots every
// write key's prior state before applying any of them, and rolls every
// already-applied write back to that snapshot the moment one write fails --
// a later write's unique-index violation, or a cancelled ctx discovered
// mid-batch. A caller sees either every write or none, never a prefix of
// the batch left in the store.
type GuardedMemoryStore struct {
	*kvstore.MemoryKVStore
	batchMu sync.Mutex
}

// Compile-time interface check.
var _ kvstore.MultiGuardedBatchKVStore = (*GuardedMemoryStore)(nil)

// batchPriorState is a write key's value immediately before a batch
// touched it, captured so a mid-batch failure can restore exactly that
// state rather than leave the key at whatever the batch's own partial
// application left behind.
type batchPriorState struct {
	existed bool
	value   []byte
}

// NewGuardedMemoryStore returns a fresh, empty GuardedMemoryStore.
func NewGuardedMemoryStore() *GuardedMemoryStore {
	return &GuardedMemoryStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
}

// PutAllIfUnchanged implements kvstore.GuardedBatchKVStore through
// PutAllIfAllUnchanged, as the production adapters do.
func (s *GuardedMemoryStore) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	return s.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: guardKey, Value: guardValue}}, writes)
}

// PutAllIfAllUnchanged applies writes only while every guard holds: a nil
// Guard.Value requires the key to be absent, any other value requires it to
// be present and equal.
//
// The writes themselves are then all-or-nothing too. Put can fail for a
// reason that has nothing to do with ctx -- a unique-index violation on
// the SECOND write of a batch whose first write already landed, in
// particular -- so this snapshots every write key's prior value before
// applying any of them and rolls every already-applied write back the
// moment one fails, rather than leaving a prefix of the batch in the store
// with the rest refused.
func (s *GuardedMemoryStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	s.batchMu.Lock()
	defer s.batchMu.Unlock()
	for _, g := range guards {
		exists, err := s.Exists(ctx, g.Key)
		if err != nil {
			return false, err
		}
		if g.Value == nil {
			if exists {
				return false, nil
			}
			continue
		}
		v, err := s.Get(ctx, g.Key)
		if err != nil {
			return false, err
		}
		if !exists || !bytes.Equal(v, g.Value) {
			return false, nil
		}
	}

	snapshot := make(map[string]batchPriorState, len(writes))
	for _, w := range writes {
		if _, seen := snapshot[w.Key]; seen {
			continue
		}
		existing, err := s.Get(ctx, w.Key)
		if err != nil {
			return false, err
		}
		snapshot[w.Key] = batchPriorState{existed: existing != nil, value: existing}
	}

	applied := make([]string, 0, len(writes))
	for _, w := range writes {
		if err := s.Put(ctx, w.Key, w.Value); err != nil {
			s.rollback(applied, snapshot)
			return false, err
		}
		applied = append(applied, w.Key)
	}
	return true, nil
}

// rollback restores every key in appliedKeys to the batchPriorState
// recorded before this batch touched it, undoing the most recently applied
// write first so a key written more than once in the same batch ends up
// back at its ORIGINAL value rather than an intermediate one.
//
// It runs with batchMu already held by the caller and uses a background
// context: a rollback is this store's own cleanup after ITS write failed,
// not a new operation subject to the caller's ctx, and an already-expired
// ctx must not be allowed to block the very cleanup it triggered.
func (s *GuardedMemoryStore) rollback(appliedKeys []string, snapshot map[string]batchPriorState) {
	ctx := context.Background()
	for i := len(appliedKeys) - 1; i >= 0; i-- {
		key := appliedKeys[i]
		prior := snapshot[key]
		if prior.existed {
			_ = s.Put(ctx, key, prior.value)
			continue
		}
		_ = s.Delete(ctx, key)
	}
}
