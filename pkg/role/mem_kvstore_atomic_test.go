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

package role

import (
	"bytes"
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// memKVStore's AtomicKVStore and GuardedBatchKVStore capabilities, split
// into its own file so the plain KVStore double in store_test.go stays
// focused. sync.Map's own LoadOrStore/CompareAndSwap/CompareAndDelete give
// each single-key verb genuine atomicity; PutAllIfUnchanged additionally
// takes memKVStore.mu, because checking one key and writing several is not
// something per-key atomicity alone can make atomic. That is exactly the
// capability go-qrdb#555's atomic CreateRole path needs, and go-qrdb#555's
// tenant.IdentityIndex needs AtomicKVStore, so any test wiring either up
// needs a double that has both.
var (
	_ kvstore.AtomicKVStore            = (*memKVStore)(nil)
	_ kvstore.MultiGuardedBatchKVStore = (*memKVStore)(nil)
)

// PutIfAbsent implements kvstore.AtomicKVStore.
func (m *memKVStore) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	_, loaded := m.data.LoadOrStore(key, append([]byte(nil), value...))
	return !loaded, nil
}

// CompareAndDelete implements kvstore.AtomicKVStore.
//
// It compares under m.mu rather than through sync.Map.CompareAndDelete,
// which compares the stored values with == and panics on a []byte.
func (m *memKVStore) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.data.Load(key)
	if !ok || !bytes.Equal(current.([]byte), expected) {
		return false, nil
	}
	m.data.Delete(key)
	return true, nil
}

// CompareAndSwap implements kvstore.AtomicKVStore, under m.mu for the
// reason CompareAndDelete gives.
func (m *memKVStore) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.data.Load(key)
	if !ok || !bytes.Equal(current.([]byte), expected) {
		return false, nil
	}
	m.data.Store(key, append([]byte(nil), next...))
	return true, nil
}

// PutAllIfUnchanged implements kvstore.GuardedBatchKVStore in terms of
// PutAllIfAllUnchanged, exactly as the production adapters do.
func (m *memKVStore) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	return m.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: guardKey, Value: guardValue}}, writes)
}

// PutAllIfAllUnchanged implements kvstore.MultiGuardedBatchKVStore: it
// evaluates every guard and applies every write while holding m.mu, so no
// other operation on this store can observe — or produce — a state where
// only some of the writes landed. That is the property the real Raft-backed
// implementation gets from evaluating the compares and the writes inside one
// state-machine Apply; this is the in-process equivalent, sufficient for a
// single-process unit test.
//
// Guards are evaluated on EXISTENCE first, exactly as the production
// encoding (pkg/server/lifecycle's guardCompares, behind
// ExecuteGuardedBatch) does: a nil Guard.Value requires the key to be
// absent, and a non-nil one requires it to be present AND hold exactly that
// value. bytes.Equal(nil, []byte{}) is true in Go, so value equality alone
// would confuse an absent key with an empty stored value in both directions
// — the empty-vs-absent hole go-qrdb#555's review found in the adapters. A
// double that got this wrong would make that regression invisible here.
func (m *memKVStore) PutAllIfAllUnchanged(_ context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, g := range guards {
		v, exists := m.data.Load(g.Key)
		if g.Value == nil {
			if exists {
				return false, nil
			}
			continue
		}
		if !exists || !bytes.Equal(v.([]byte), g.Value) {
			return false, nil
		}
	}
	for _, w := range writes {
		m.data.Store(w.Key, append([]byte(nil), w.Value...))
	}
	return true, nil
}
