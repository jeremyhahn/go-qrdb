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

package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// priorContractKVStore is a KV store implementing the atomic contract as it
// stood before CompareAndSwap: Get, PutIfAbsent and CompareAndDelete over a
// real in-memory store. Embedding the kvstore.KVStore INTERFACE promotes only
// its methods, so the in-memory store's CompareAndSwap stays withheld.
type priorContractKVStore struct {
	kvstore.KVStore
	mem *kvstore.MemoryKVStore
}

func (s priorContractKVStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	return s.mem.PutIfAbsent(ctx, key, value)
}

func (s priorContractKVStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return s.mem.CompareAndDelete(ctx, key, expected)
}

// TestCreateNamespace_PriorContractStrictFloorRefusesSystemCreate pins that
// a strict cluster floor written to a store without CompareAndSwap (the
// pre-upgrade contract) still refuses a system-context namespace create.
func TestCreateNamespace_PriorContractStrictFloorRefusesSystemCreate(t *testing.T) {
	ctx := context.Background()
	mem := kvstore.NewMemoryKVStore()
	store := priorContractKVStore{KVStore: mem, mem: mem}
	tenants, err := tenant.NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	// The floor's storage key is pkg/tenant's, so tighten it through a
	// service over the full in-memory store; the record is the same bytes a
	// cluster on the prior contract persisted before the upgrade.
	seeder, err := tenant.NewKVTenantService(mem, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService(seeder): %v", err)
	}
	if err := seeder.SetClusterIsolationPolicy(ctx, tenant.ClusterIsolationPolicyStrict); err != nil {
		t.Fatalf("seed strict floor: %v", err)
	}

	svc, err := NewVectorService(newVecMockStorage(), newVecTestLogger(), WithClusterIsolationPolicyReader(tenants))
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	_, err = svc.CreateNamespace(ctx, &CreateNamespaceRequest{Namespace: "test-ns", Dimension: 128})
	var required *TenantRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("CreateNamespace under a stored strict floor = %v, want *TenantRequiredError", err)
	}
}
