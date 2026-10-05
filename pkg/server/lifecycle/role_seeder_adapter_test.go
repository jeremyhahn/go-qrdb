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

package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// TestRoleSeederAdapter_ReconcileBuiltInRoles_Succeeds proves the adapter
// namespaces the shared store to the tenant's identity namespace and
// seeds the built-in roles there -- the same namespace tenant.StoreResolver
// confines every other prefix-tenant IAM read/write to.
//
// shared must be a kvstore.MultiGuardedBatchKVStore: ReconcileBuiltInRoles
// builds its role.Store WithIdentityIndex/WithAtomicIdentity (the firstboot
// role race fix), which role.NewStore refuses to construct over a store
// that cannot honour a guarded batch. A plain kvstore.NewMemoryKVStore
// cannot; guardedMemKVStore (below) can, and — unlike a disconnected pair
// of mocks — keeps reads and guarded writes on the SAME backing map, which
// this test's own read-back after ReconcileBuiltInRoles depends on.
func TestRoleSeederAdapter_ReconcileBuiltInRoles_Succeeds(t *testing.T) {
	shared := newGuardedMemKVStore()
	a := roleSeederAdapter{shared: shared}

	if err := a.ReconcileBuiltInRoles(context.Background(), "acme"); err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}

	keys, err := shared.List(context.Background(), "acme/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) == 0 {
		t.Error("ReconcileBuiltInRoles wrote nothing under the tenant's identity namespace")
	}
}

// TestRoleSeederAdapter_ReconcileBuiltInRoles_NilSharedStorePropagatesError
// proves a nil shared store is reported as the namespaced store's own
// typed refusal, rather than panicking.
func TestRoleSeederAdapter_ReconcileBuiltInRoles_NilSharedStorePropagatesError(t *testing.T) {
	a := roleSeederAdapter{shared: nil}

	err := a.ReconcileBuiltInRoles(context.Background(), "acme")
	var nilInner *kvstore.NilInnerStoreError
	if !errors.As(err, &nilInner) {
		t.Fatalf("ReconcileBuiltInRoles with a nil shared store = %v (%T), want *kvstore.NilInnerStoreError", err, err)
	}
}

// ---------------------------------------------------------------------------
// guardedMemKVStore: an in-memory kvstore.KVStore that also satisfies
// kvstore.AtomicKVStore and kvstore.MultiGuardedBatchKVStore, mirroring
// pkg/role's own memKVStore test double (store_test.go,
// mem_kvstore_atomic_test.go) field-for-field. Needed here, not reused from
// there, because memKVStore is unexported to pkg/role: this package needs
// the identical capability to exercise roleSeederAdapter's own
// WithAtomicIdentity construction.
// ---------------------------------------------------------------------------

type guardedMemKVStore struct {
	data sync.Map
	// mu guards PutAllIfAllUnchanged: sync.Map gives per-key atomicity, not
	// the check-many-then-write-many atomicity a guarded batch needs.
	mu sync.Mutex
}

func newGuardedMemKVStore() *guardedMemKVStore {
	return &guardedMemKVStore{}
}

var (
	_ kvstore.KVStore                  = (*guardedMemKVStore)(nil)
	_ kvstore.AtomicKVStore            = (*guardedMemKVStore)(nil)
	_ kvstore.MultiGuardedBatchKVStore = (*guardedMemKVStore)(nil)
)

func (m *guardedMemKVStore) Put(_ context.Context, key string, value []byte) error {
	m.data.Store(key, append([]byte(nil), value...))
	return nil
}

func (m *guardedMemKVStore) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := m.data.Load(key)
	if !ok {
		return nil, nil
	}
	return v.([]byte), nil
}

func (m *guardedMemKVStore) Delete(_ context.Context, key string) error {
	m.data.Delete(key)
	return nil
}

func (m *guardedMemKVStore) Scan(_ context.Context, prefix string, fn func(key string, value []byte) error) error {
	var scanErr error
	m.data.Range(func(k, v any) bool {
		key := k.(string)
		if strings.HasPrefix(key, prefix) {
			if err := fn(key, v.([]byte)); err != nil {
				scanErr = err
				return false
			}
		}
		return true
	})
	return scanErr
}

func (m *guardedMemKVStore) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	m.data.Range(func(k, _ any) bool {
		key := k.(string)
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
		return true
	})
	sort.Strings(keys)
	return keys, nil
}

func (m *guardedMemKVStore) Exists(_ context.Context, key string) (bool, error) {
	_, ok := m.data.Load(key)
	return ok, nil
}

func (m *guardedMemKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return nil
}

func (m *guardedMemKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}

func (m *guardedMemKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

func (m *guardedMemKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

func (m *guardedMemKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

// PutIfAbsent implements kvstore.AtomicKVStore.
func (m *guardedMemKVStore) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	_, loaded := m.data.LoadOrStore(key, append([]byte(nil), value...))
	return !loaded, nil
}

// CompareAndDelete implements kvstore.AtomicKVStore.
func (m *guardedMemKVStore) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	current, ok := m.data.Load(key)
	if !ok || !bytes.Equal(current.([]byte), expected) {
		return false, nil
	}
	return m.data.CompareAndDelete(key, current), nil
}

// CompareAndSwap implements kvstore.AtomicKVStore.
func (m *guardedMemKVStore) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	current, ok := m.data.Load(key)
	if !ok || !bytes.Equal(current.([]byte), expected) {
		return false, nil
	}
	return m.data.CompareAndSwap(key, current, append([]byte(nil), next...)), nil
}

// PutAllIfUnchanged implements kvstore.GuardedBatchKVStore in terms of
// PutAllIfAllUnchanged, exactly as the production adapters do.
func (m *guardedMemKVStore) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	return m.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: guardKey, Value: guardValue}}, writes)
}

// PutAllIfAllUnchanged implements kvstore.MultiGuardedBatchKVStore: it
// evaluates every guard and applies every write while holding m.mu, so no
// other operation on this store can observe -- or produce -- a state where
// only some of the writes landed. Guards are evaluated on EXISTENCE first,
// exactly as the production encoding does: a nil Guard.Value requires the
// key to be absent, and a non-nil one requires it to be present AND hold
// exactly that value.
func (m *guardedMemKVStore) PutAllIfAllUnchanged(_ context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
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
