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

package user

import (
	"bytes"
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// memKVStore's MultiGuardedBatchKVStore capability, split into its own file
// so the plain/atomic double in store_test.go stays focused. It reuses
// atomicMu — checking one or more keys and writing several needs the same
// single-step atomicity the three AtomicKVStore verbs already share, for
// the same reason: a Load-then-Store-many here would reproduce the exact
// race go-qrdb#555's atomic CreateUser/CreateRole path exists to close, and
// the concurrency tests would then pass against broken code.
var _ kvstore.MultiGuardedBatchKVStore = (*memKVStore)(nil)

// PutAllIfUnchanged implements kvstore.GuardedBatchKVStore in terms of
// PutAllIfAllUnchanged, exactly as the production adapters do.
func (m *memKVStore) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	return m.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: guardKey, Value: guardValue}}, writes)
}

// PutAllIfAllUnchanged implements kvstore.MultiGuardedBatchKVStore.
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
	m.atomicMu.Lock()
	defer m.atomicMu.Unlock()
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
