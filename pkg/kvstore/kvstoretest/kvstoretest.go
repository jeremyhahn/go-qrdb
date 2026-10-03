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
// in-memory store. Batches serialise on an internal mutex against each
// other and against the plain KVStore methods it inherits, so a test
// driving both at once (a batch write racing an ordinary Put) still sees a
// consistent store.
type GuardedMemoryStore struct {
	*kvstore.MemoryKVStore
	batchMu sync.Mutex
}

// Compile-time interface check.
var _ kvstore.MultiGuardedBatchKVStore = (*GuardedMemoryStore)(nil)

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
	for _, w := range writes {
		if err := s.Put(ctx, w.Key, w.Value); err != nil {
			return false, err
		}
	}
	return true, nil
}
