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
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
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

// fakeResolver implements tenant.StoreResolver, mapping tenant IDs to fixed
// KVStores and surfacing tenant.ErrTenantSealed for sealed tenants.
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

// (a) A barrier tenant's user round-trips through its own store and does
// NOT leak into the shared store.
func TestRoutingUserService_BarrierTenantUsesOwnStore(t *testing.T) {
	shared := newFakeKVStore()
	tenantStore := newFakeKVStore()
	resolver := &fakeResolver{
		shared:  shared,
		tenants: map[string]kvstore.KVStore{"secure": tenantStore},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := context.Background()
	created, err := svc.CreateUser(ctx, &CreateUserRequest{Username: "alice", TenantID: "secure"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// The tenant store holds the record; the shared store does not.
	if n := len(tenantStore.m); n == 0 {
		t.Fatal("tenant store has no records; user was not routed to it")
	}
	if n := len(shared.m); n != 0 {
		t.Fatalf("shared store unexpectedly holds %d records (leak)", n)
	}

	// GetUser within the tenant context resolves the record.
	tctx := tenant.WithTenantID(ctx, "secure")
	got, err := svc.GetUser(tctx, created.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.Username != "alice" {
		t.Fatalf("Username = %q, want alice", got.Username)
	}
}

// (b) A prefix tenant's user goes to the shared store.
func TestRoutingUserService_PrefixTenantUsesSharedStore(t *testing.T) {
	shared := newFakeKVStore()
	resolver := &fakeResolver{shared: shared, tenants: map[string]kvstore.KVStore{}}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := context.Background()
	if _, err := svc.CreateUser(ctx, &CreateUserRequest{Username: "bob", TenantID: "acme"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if len(shared.m) == 0 {
		t.Fatal("prefix tenant user was not routed to the shared store")
	}
}

// (c) Two barrier tenants are isolated: tenant A's user is not resolvable
// as tenant B.
func TestRoutingUserService_TwoBarrierTenantsIsolated(t *testing.T) {
	storeA := newFakeKVStore()
	storeB := newFakeKVStore()
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"a": storeA, "b": storeB},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := context.Background()
	ua, err := svc.CreateUser(ctx, &CreateUserRequest{Username: "carol", TenantID: "a"})
	if err != nil {
		t.Fatalf("CreateUser(a): %v", err)
	}

	// Looking up tenant A's user ID in tenant B's context must not find it.
	bctx := tenant.WithTenantID(ctx, "b")
	_, err = svc.GetUser(bctx, ua.ID)
	var nf *UserNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("cross-tenant GetUser = %v, want UserNotFoundError", err)
	}

	// And it resolves correctly in tenant A's context.
	actx := tenant.WithTenantID(ctx, "a")
	if _, err := svc.GetUser(actx, ua.ID); err != nil {
		t.Fatalf("same-tenant GetUser(a): %v", err)
	}
}

// (d) A sealed barrier tenant fails closed: routing methods return
// ErrTenantSealed and never touch the shared store.
func TestRoutingUserService_SealedTenantFailsClosed(t *testing.T) {
	shared := newFakeKVStore()
	resolver := &fakeResolver{
		shared:  shared,
		tenants: map[string]kvstore.KVStore{"vault": newFakeKVStore()},
		sealed:  map[string]bool{"vault": true},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := context.Background()

	if _, err := svc.CreateUser(ctx, &CreateUserRequest{Username: "dan", TenantID: "vault"}); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("CreateUser(sealed) = %v, want ErrTenantSealed", err)
	}

	vctx := tenant.WithTenantID(ctx, "vault")
	if _, err := svc.GetUser(vctx, 12345); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("GetUser(sealed) = %v, want ErrTenantSealed", err)
	}
	if _, err := svc.ListUsers(ctx, &ListUsersRequest{TenantID: "vault"}); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("ListUsers(sealed) = %v, want ErrTenantSealed", err)
	}

	// No fallback: the shared store must remain untouched.
	if len(shared.m) != 0 {
		t.Fatal("sealed-tenant write leaked into the shared store")
	}
}

func TestNewRoutingService_NilArgs(t *testing.T) {
	if _, err := NewRoutingService(nil, newTestIDGen(t)); err == nil {
		t.Error("expected error for nil resolver")
	}
	resolver := &fakeResolver{shared: newFakeKVStore(), tenants: map[string]kvstore.KVStore{}}
	if _, err := NewRoutingService(resolver, nil); err == nil {
		t.Error("expected error for nil id generator")
	}
}
