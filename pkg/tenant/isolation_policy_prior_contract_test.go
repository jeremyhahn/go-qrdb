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

package tenant

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// priorContractKVStore is a KV store implementing the atomic contract as it
// stood before CompareAndSwap: Get, PutIfAbsent and CompareAndDelete over a
// real in-memory store. Embedding the kvstore.KVStore INTERFACE promotes only
// its methods, so the in-memory store's CompareAndSwap stays withheld.
type priorContractKVStore struct {
	kvstore.KVStore
	mem *kvstore.MemoryKVStore
}

func newPriorContractKVStore() priorContractKVStore {
	mem := kvstore.NewMemoryKVStore()
	return priorContractKVStore{KVStore: mem, mem: mem}
}

func (s priorContractKVStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	return s.mem.PutIfAbsent(ctx, key, value)
}

func (s priorContractKVStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return s.mem.CompareAndDelete(ctx, key, expected)
}

// TestKVTenantService_PriorContractStoreKeepsStrictFloor pins that a strict
// cluster floor persisted on a store without CompareAndSwap is still read and
// enforced after an upgrade, and that a write there is refused, typed, with
// the record left as it was.
func TestKVTenantService_PriorContractStoreKeepsStrictFloor(t *testing.T) {
	ctx := context.Background()
	store := newPriorContractKVStore()
	if _, swaps := any(store).(interface {
		CompareAndSwap(context.Context, string, []byte, []byte) (bool, error)
	}); swaps {
		t.Fatal("double must not carry CompareAndSwap")
	}
	if err := store.Put(ctx, clusterIsolationPolicyKey, []byte(ClusterIsolationPolicyStrict)); err != nil {
		t.Fatalf("seed strict floor: %v", err)
	}
	svc, err := NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	got, err := svc.ClusterIsolationPolicy(ctx)
	if err != nil || got != ClusterIsolationPolicyStrict {
		t.Fatalf("ClusterIsolationPolicy = %q, %v; want the stored strict floor", got, err)
	}

	for _, next := range []ClusterIsolationPolicy{ClusterIsolationPolicyOperatorAccess, ClusterIsolationPolicyStrict} {
		if err := svc.SetClusterIsolationPolicy(ctx, next); !errors.Is(err, ErrTenantStoreNotAtomic) {
			t.Fatalf("SetClusterIsolationPolicy(%q) = %v, want ErrTenantStoreNotAtomic", next, err)
		}
		raw, err := store.Get(ctx, clusterIsolationPolicyKey)
		if err != nil || string(raw) != string(ClusterIsolationPolicyStrict) {
			t.Fatalf("after SetClusterIsolationPolicy(%q) record = %q, %v; want strict unchanged", next, raw, err)
		}
	}
}
