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
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// fakeKVStore is a minimal in-memory kvstore.KVStore for routing tests.
type fakeKVStore struct {
	mu sync.RWMutex
	m  map[string][]byte
}

func newFakeKVStore() *fakeKVStore { return &fakeKVStore{m: make(map[string][]byte)} }

func (s *fakeKVStore) Put(_ context.Context, k string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(v))
	copy(cp, v)
	s.m[k] = cp
	return nil
}

func (s *fakeKVStore) Get(_ context.Context, k string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[k]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (s *fakeKVStore) Delete(_ context.Context, k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, k)
	return nil
}

func (s *fakeKVStore) Scan(_ context.Context, prefix string, fn func(string, []byte) error) error {
	s.mu.RLock()
	snap := make(map[string][]byte, len(s.m))
	for k, v := range s.m {
		snap[k] = v
	}
	s.mu.RUnlock()
	for k, v := range snap {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			if err := fn(k, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *fakeKVStore) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var keys []string
	for k := range s.m {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (s *fakeKVStore) Exists(_ context.Context, k string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.m[k]
	return ok, nil
}

func (s *fakeKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error { return nil }
func (s *fakeKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}
func (s *fakeKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}
func (s *fakeKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}
func (s *fakeKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

// fakeResolver implements tenant.StoreResolver.
type fakeResolver struct {
	shared  kvstore.KVStore
	tenants map[string]kvstore.KVStore
	sealed  map[string]bool
}

func (r *fakeResolver) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	if r.sealed[tenantID] {
		return nil, tenant.ErrTenantSealed
	}
	if kv, ok := r.tenants[tenantID]; ok {
		return kv, nil
	}
	return r.shared, nil
}

// (a) A barrier tenant's role round-trips through its own store and does
// NOT leak into the shared store.
func TestRoutingRoleService_BarrierTenantUsesOwnStore(t *testing.T) {
	shared := newFakeKVStore()
	tenantStore := newFakeKVStore()
	resolver := &fakeResolver{shared: shared, tenants: map[string]kvstore.KVStore{"secure": tenantStore}}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := context.Background()
	created, err := svc.CreateRole(ctx, &CreateRoleRequest{Name: "editor", TenantID: "secure"})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if len(tenantStore.m) == 0 {
		t.Fatal("role was not routed to the tenant store")
	}
	if len(shared.m) != 0 {
		t.Fatalf("shared store leaked %d records", len(shared.m))
	}

	tctx := tenant.WithTenantID(ctx, "secure")
	got, err := svc.GetRole(tctx, created.ID)
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if got.Name != "editor" {
		t.Fatalf("Name = %q, want editor", got.Name)
	}
}

// (b) A prefix tenant's role goes to the shared store.
func TestRoutingRoleService_PrefixTenantUsesSharedStore(t *testing.T) {
	shared := newFakeKVStore()
	resolver := &fakeResolver{shared: shared, tenants: map[string]kvstore.KVStore{}}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	if _, err := svc.CreateRole(context.Background(), &CreateRoleRequest{Name: "viewer", TenantID: "acme"}); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if len(shared.m) == 0 {
		t.Fatal("prefix tenant role was not routed to the shared store")
	}
}

// (c) Two barrier tenants are isolated.
func TestRoutingRoleService_TwoBarrierTenantsIsolated(t *testing.T) {
	storeA := newFakeKVStore()
	storeB := newFakeKVStore()
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"a": storeA, "b": storeB},
	}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := context.Background()
	ra, err := svc.CreateRole(ctx, &CreateRoleRequest{Name: "admin", TenantID: "a"})
	if err != nil {
		t.Fatalf("CreateRole(a): %v", err)
	}

	bctx := tenant.WithTenantID(ctx, "b")
	_, err = svc.GetRole(bctx, ra.ID)
	var nf *RoleNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("cross-tenant GetRole = %v, want RoleNotFoundError", err)
	}
}

// (d) A sealed barrier tenant fails closed.
func TestRoutingRoleService_SealedTenantFailsClosed(t *testing.T) {
	shared := newFakeKVStore()
	resolver := &fakeResolver{
		shared:  shared,
		tenants: map[string]kvstore.KVStore{"vault": newFakeKVStore()},
		sealed:  map[string]bool{"vault": true},
	}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := context.Background()
	if _, err := svc.CreateRole(ctx, &CreateRoleRequest{Name: "x", TenantID: "vault"}); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("CreateRole(sealed) = %v, want ErrTenantSealed", err)
	}
	vctx := tenant.WithTenantID(ctx, "vault")
	if _, err := svc.GetRole(vctx, "any-id"); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("GetRole(sealed) = %v, want ErrTenantSealed", err)
	}
	if len(shared.m) != 0 {
		t.Fatal("sealed-tenant write leaked into the shared store")
	}
}

// TestRoutingRoleService_DelegatesFullLifecycle exercises the remaining
// routed RoleServicer methods end-to-end through the per-tenant router. It is
// the path that resolves a tenant-admin's effective permissions, so it also
// asserts that broker data-plane wildcard grants survive a create -> resolve
// round-trip through the tenant store.
func TestRoutingRoleService_DelegatesFullLifecycle(t *testing.T) {
	store := newFakeKVStore()
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": store},
	}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := context.Background()
	tctx := tenant.WithTenantID(ctx, "acme")

	created, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:     "tenant-admin",
		TenantID: "acme",
		Permissions: []permission.Permission{
			permission.ObjStoreAll,
			permission.SchedulerAll,
			permission.ParametersRead,
		},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	// GetRoleByName routes on the explicit tenant argument.
	byName, err := svc.GetRoleByName(ctx, "acme", "tenant-admin")
	if err != nil {
		t.Fatalf("GetRoleByName: %v", err)
	}
	if byName.ID != created.ID {
		t.Fatalf("GetRoleByName ID = %q, want %q", byName.ID, created.ID)
	}

	// UpdateRole routes on the context tenant.
	updated, err := svc.UpdateRole(tctx, &UpdateRoleRequest{
		ID: created.ID,
		Permissions: []permission.Permission{
			permission.ObjStoreAll,
			permission.MessagingAll,
		},
	})
	if err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if len(updated.Permissions) != 2 {
		t.Fatalf("UpdateRole perms = %d, want 2", len(updated.Permissions))
	}

	// ListRoles routes on the request's TenantID.
	list, err := svc.ListRoles(ctx, &ListRolesRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if list.Total != 1 {
		t.Fatalf("ListRoles Total = %d, want 1", list.Total)
	}

	// ResolvePermissions routes on the context tenant and returns the
	// (sorted, deduped) wildcard grants.
	perms, err := svc.ResolvePermissions(tctx, []string{created.ID})
	if err != nil {
		t.Fatalf("ResolvePermissions: %v", err)
	}
	got := make(map[permission.Permission]struct{}, len(perms))
	for _, p := range perms {
		got[p] = struct{}{}
	}
	for _, want := range []permission.Permission{permission.ObjStoreAll, permission.MessagingAll} {
		if _, ok := got[want]; !ok {
			t.Errorf("ResolvePermissions missing %q", want)
		}
	}

	// DeleteRole routes on the context tenant.
	if err := svc.DeleteRole(tctx, created.ID); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}
	if _, err := svc.GetRole(tctx, created.ID); err == nil {
		t.Fatal("GetRole after delete: expected error")
	}
}

func TestNewRoutingRoleService_NilResolver(t *testing.T) {
	if _, err := NewRoutingService(nil); err == nil {
		t.Error("expected error for nil resolver")
	}
}
