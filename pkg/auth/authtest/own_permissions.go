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

package authtest

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TenantRoutedRoles is a real role service routed per tenant exactly as a node
// routes it (role.NewRoutingService), over one in-memory store per tenant,
// each holding that tenant's reconciled built-in roles. Every tenant the
// router resolves is recorded, so a test proves which tenant's IAM a call
// actually read rather than inferring it from the shape of a key.
type TenantRoutedRoles struct {
	role.RoleServicer

	mu       sync.Mutex
	stores   map[string]kvstore.KVStore
	resolved []string
}

var _ tenant.StoreResolver = (*TenantRoutedRoles)(nil)

// NewTenantRoutedRoles returns a TenantRoutedRoles serving tenantIDs. A
// tenant not in tenantIDs resolves to tenant.ErrTenantNotFound.
func NewTenantRoutedRoles(t testing.TB, tenantIDs ...string) *TenantRoutedRoles {
	t.Helper()
	r := &TenantRoutedRoles{stores: make(map[string]kvstore.KVStore, len(tenantIDs))}
	for _, id := range tenantIDs {
		kv := kvstore.NewMemoryKVStore()
		store, err := role.NewStore(kv)
		if err != nil {
			t.Fatalf("authtest: role store for %q: %v", id, err)
		}
		if _, err := store.ReconcileBuiltInRoles(context.Background(), id); err != nil {
			t.Fatalf("authtest: reconcile built-in roles for %q: %v", id, err)
		}
		r.stores[id] = kv
	}
	svc, err := role.NewRoutingService(r)
	if err != nil {
		t.Fatalf("authtest: routing role service: %v", err)
	}
	r.RoleServicer = svc
	return r
}

// Resolve implements tenant.StoreResolver and records tenantID.
func (r *TenantRoutedRoles) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolved = append(r.resolved, tenantID)
	kv, ok := r.stores[tenantID]
	if !ok {
		return nil, tenant.ErrTenantNotFound
	}
	return kv, nil
}

// Resolved returns every tenant resolved so far, in order.
func (r *TenantRoutedRoles) Resolved() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.resolved)
}
