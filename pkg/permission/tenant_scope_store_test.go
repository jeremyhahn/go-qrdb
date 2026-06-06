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

package permission

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Test doubles: an in-memory kvstore.KVStore and a StoreResolver that mirrors
// sub-phase A's isolation contract — distinct stores per tenant, and a
// configurable per-tenant fault (used to simulate a sealed barrier tenant
// returning tenant.ErrTenantSealed).
// ---------------------------------------------------------------------------

type memStore struct {
	data map[string][]byte
}

func newMemStore() *memStore { return &memStore{data: make(map[string][]byte)} }

func (m *memStore) Put(_ context.Context, key string, value []byte) error {
	cp := make([]byte, len(value))
	copy(cp, value)
	m.data[key] = cp
	return nil
}

func (m *memStore) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := m.data[key]
	if !ok {
		return nil, nil
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (m *memStore) Delete(_ context.Context, key string) error {
	delete(m.data, key)
	return nil
}

func (m *memStore) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for k := range m.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *memStore) Exists(_ context.Context, key string) (bool, error) {
	_, ok := m.data[key]
	return ok, nil
}

func (m *memStore) Scan(_ context.Context, prefix string, fn func(string, []byte) error) error {
	for k, v := range m.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			if err := fn(k, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *memStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error { return nil }
func (m *memStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}
func (m *memStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}
func (m *memStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}
func (m *memStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

var _ kvstore.KVStore = (*memStore)(nil)

// fakeResolver mirrors the StoreResolver isolation contract: each tenant ID
// maps to its OWN in-memory store, so writes under one tenant never appear
// under another (the property a barrier tenant gets for real). A tenant ID
// listed in sealed fails closed with tenant.ErrTenantSealed, mirroring a
// sealed barrier tenant.
type fakeResolver struct {
	stores map[string]*memStore
	sealed map[string]bool
}

func newFakeResolver() *fakeResolver {
	return &fakeResolver{
		stores: make(map[string]*memStore),
		sealed: make(map[string]bool),
	}
}

func (f *fakeResolver) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	if f.sealed[tenantID] {
		return nil, tenant.ErrTenantSealed
	}
	s, ok := f.stores[tenantID]
	if !ok {
		s = newMemStore()
		f.stores[tenantID] = s
	}
	return s, nil
}

var _ tenant.StoreResolver = (*fakeResolver)(nil)

func newTenantScopeStore(t *testing.T) (*TenantScopeStore, *fakeResolver) {
	t.Helper()
	fr := newFakeResolver()
	store, err := NewTenantScopeStore(fr)
	if err != nil {
		t.Fatalf("NewTenantScopeStore: %v", err)
	}
	return store, fr
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

func TestNewTenantScopeStore_NilResolver(t *testing.T) {
	if _, err := NewTenantScopeStore(nil); !errors.Is(err, ErrNilStoreResolver) {
		t.Fatalf("NewTenantScopeStore(nil) = %v, want ErrNilStoreResolver", err)
	}
}

// ---------------------------------------------------------------------------
// (a) Two tenants set the SAME permission name to DIFFERENT scopes with no
// collision: they resolve independently.
// ---------------------------------------------------------------------------

func TestTenantScopeStore_SameNameDifferentScopesNoCollision(t *testing.T) {
	store, _ := newTenantScopeStore(t)
	ctx := context.Background()
	const perm Permission = "billing:export"

	if err := store.SetScope(ctx, "tenant-a", perm, ScopeCross); err != nil {
		t.Fatalf("SetScope(tenant-a): %v", err)
	}
	if err := store.SetScope(ctx, "tenant-b", perm, ScopeTenantOnly); err != nil {
		t.Fatalf("SetScope(tenant-b): %v", err)
	}

	gotA, okA, err := store.GetScope(ctx, "tenant-a", perm)
	if err != nil || !okA {
		t.Fatalf("GetScope(tenant-a) = (%v, %v, %v)", gotA, okA, err)
	}
	if gotA != ScopeCross {
		t.Fatalf("tenant-a scope = %v, want ScopeCross", gotA)
	}

	gotB, okB, err := store.GetScope(ctx, "tenant-b", perm)
	if err != nil || !okB {
		t.Fatalf("GetScope(tenant-b) = (%v, %v, %v)", gotB, okB, err)
	}
	if gotB != ScopeTenantOnly {
		t.Fatalf("tenant-b scope = %v, want ScopeTenantOnly", gotB)
	}

	// ResolveScope honors the same independence.
	rA, err := store.ResolveScope(ctx, "tenant-a", perm)
	if err != nil {
		t.Fatalf("ResolveScope(tenant-a): %v", err)
	}
	rB, err := store.ResolveScope(ctx, "tenant-b", perm)
	if err != nil {
		t.Fatalf("ResolveScope(tenant-b): %v", err)
	}
	if rA != ScopeCross || rB != ScopeTenantOnly {
		t.Fatalf("ResolveScope independence broken: a=%v b=%v", rA, rB)
	}
}

// ---------------------------------------------------------------------------
// (c) Per-tenant scope defs land in the tenant-specific store the resolver
// selected — proving storage routes through the StoreResolver and inherits
// its isolation. tenant-a's record never appears in tenant-b's store, and it
// is stored under the namespaced "__sys:perm_scope/" key.
// ---------------------------------------------------------------------------

func TestTenantScopeStore_DefsStoredInTenantStore(t *testing.T) {
	store, fr := newTenantScopeStore(t)
	ctx := context.Background()
	const perm Permission = "billing:export"

	if err := store.SetScope(ctx, "tenant-a", perm, ScopeCross); err != nil {
		t.Fatalf("SetScope(tenant-a): %v", err)
	}

	// The definition exists in tenant-a's store under the namespaced key.
	storeA := fr.stores["tenant-a"]
	if storeA == nil {
		t.Fatal("tenant-a store was never created by the resolver")
	}
	raw, ok := storeA.data[scopeKeyPrefix+string(perm)]
	if !ok {
		t.Fatalf("scope def not stored under %q in tenant-a store", scopeKeyPrefix+string(perm))
	}
	if string(raw) != ScopeCross.String() {
		t.Fatalf("stored scope value = %q, want %q", raw, ScopeCross.String())
	}

	// tenant-b's store must NOT contain tenant-a's record (isolation).
	if storeB := fr.stores["tenant-b"]; storeB != nil {
		if _, leaked := storeB.data[scopeKeyPrefix+string(perm)]; leaked {
			t.Fatal("tenant-a scope def leaked into tenant-b store")
		}
	}
}

// ---------------------------------------------------------------------------
// (b) Fallback precedence: a tenant with no override falls back to the global
// built-in scope; absent that, ScopeSelf.
// ---------------------------------------------------------------------------

func TestTenantScopeStore_FallbackPrecedence(t *testing.T) {
	resetScopeRegistryForTesting()
	t.Cleanup(resetScopeRegistryForTesting)

	store, _ := newTenantScopeStore(t)
	ctx := context.Background()

	// A built-in cross-scope permission with no per-tenant override falls
	// back to the global registry value.
	const builtin Permission = "platform:audit"
	RegisterScope(ScopeCross, builtin)
	got, err := store.ResolveScope(ctx, "tenant-a", builtin)
	if err != nil {
		t.Fatalf("ResolveScope(builtin): %v", err)
	}
	if got != ScopeCross {
		t.Fatalf("builtin fallback = %v, want ScopeCross", got)
	}

	// A wholly unknown permission with no override and no registry entry
	// defaults to ScopeSelf (the safe, tenant-confined default).
	const unknown Permission = "wholly:unknown"
	got, err = store.ResolveScope(ctx, "tenant-a", unknown)
	if err != nil {
		t.Fatalf("ResolveScope(unknown): %v", err)
	}
	if got != ScopeSelf {
		t.Fatalf("unknown fallback = %v, want ScopeSelf", got)
	}

	// A per-tenant override beats the global built-in.
	if err := store.SetScope(ctx, "tenant-a", builtin, ScopeTenantOnly); err != nil {
		t.Fatalf("SetScope override: %v", err)
	}
	got, err = store.ResolveScope(ctx, "tenant-a", builtin)
	if err != nil {
		t.Fatalf("ResolveScope(override): %v", err)
	}
	if got != ScopeTenantOnly {
		t.Fatalf("override precedence = %v, want ScopeTenantOnly", got)
	}
}

// ---------------------------------------------------------------------------
// (e) A sealed barrier tenant fails closed: read returns tenant.ErrTenantSealed
// and ResolveScope propagates it (so the caller denies).
// ---------------------------------------------------------------------------

func TestTenantScopeStore_SealedTenantFailsClosed(t *testing.T) {
	store, fr := newTenantScopeStore(t)
	ctx := context.Background()
	fr.sealed["vault"] = true

	const perm Permission = "billing:export"

	if _, _, err := store.GetScope(ctx, "vault", perm); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("GetScope(sealed) = %v, want ErrTenantSealed", err)
	}
	if _, err := store.ResolveScope(ctx, "vault", perm); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("ResolveScope(sealed) = %v, want ErrTenantSealed", err)
	}
	if err := store.SetScope(ctx, "vault", perm, ScopeSelf); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("SetScope(sealed) = %v, want ErrTenantSealed", err)
	}
	if _, err := store.ListScopes(ctx, "vault"); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("ListScopes(sealed) = %v, want ErrTenantSealed", err)
	}
}

// ---------------------------------------------------------------------------
// ListScopes returns all defs for a tenant and only that tenant.
// ---------------------------------------------------------------------------

func TestTenantScopeStore_ListScopes(t *testing.T) {
	store, _ := newTenantScopeStore(t)
	ctx := context.Background()

	if err := store.SetScope(ctx, "tenant-a", "a:one", ScopeCross); err != nil {
		t.Fatalf("SetScope a:one: %v", err)
	}
	if err := store.SetScope(ctx, "tenant-a", "a:two", ScopeTenantOnly); err != nil {
		t.Fatalf("SetScope a:two: %v", err)
	}
	if err := store.SetScope(ctx, "tenant-b", "b:one", ScopeSelf); err != nil {
		t.Fatalf("SetScope b:one: %v", err)
	}

	got, err := store.ListScopes(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("ListScopes(tenant-a): %v", err)
	}
	want := map[Permission]Scope{"a:one": ScopeCross, "a:two": ScopeTenantOnly}
	if len(got) != len(want) {
		t.Fatalf("ListScopes len = %d, want %d (%v)", len(got), len(want), got)
	}
	for p, s := range want {
		if got[p] != s {
			t.Fatalf("ListScopes[%q] = %v, want %v", p, got[p], s)
		}
	}
	if _, leaked := got["b:one"]; leaked {
		t.Fatal("ListScopes(tenant-a) leaked tenant-b's def")
	}
}

// ---------------------------------------------------------------------------
// Input validation and decode-failure (fail-closed) paths.
// ---------------------------------------------------------------------------

func TestTenantScopeStore_EmptyPermission(t *testing.T) {
	store, _ := newTenantScopeStore(t)
	ctx := context.Background()

	var empty *EmptyPermissionError
	if err := store.SetScope(ctx, "tenant-a", "", ScopeSelf); !errors.As(err, &empty) {
		t.Fatalf("SetScope(empty) = %v, want *EmptyPermissionError", err)
	}
	if _, _, err := store.GetScope(ctx, "tenant-a", ""); !errors.As(err, &empty) {
		t.Fatalf("GetScope(empty) = %v, want *EmptyPermissionError", err)
	}
}

func TestTenantScopeStore_InvalidScopeRejectedOnWrite(t *testing.T) {
	store, _ := newTenantScopeStore(t)
	ctx := context.Background()

	var invalid *InvalidScopeError
	if err := store.SetScope(ctx, "tenant-a", "a:one", Scope(99)); !errors.As(err, &invalid) {
		t.Fatalf("SetScope(invalid) = %v, want *InvalidScopeError", err)
	}
}

func TestTenantScopeStore_DecodeFailureFailsClosed(t *testing.T) {
	store, fr := newTenantScopeStore(t)
	ctx := context.Background()
	const perm Permission = "billing:export"

	// Plant a corrupt value directly in the tenant store.
	s, err := fr.Resolve(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := s.Put(ctx, scopeKeyPrefix+string(perm), []byte("garbage")); err != nil {
		t.Fatalf("plant corrupt value: %v", err)
	}

	var decErr *ScopeDecodeError
	gotScope, ok, gerr := store.GetScope(ctx, "tenant-a", perm)
	if !errors.As(gerr, &decErr) {
		t.Fatalf("GetScope(corrupt) err = %v, want *ScopeDecodeError", gerr)
	}
	if ok || gotScope != ScopeSelf {
		t.Fatalf("GetScope(corrupt) = (%v, %v), want (ScopeSelf, false)", gotScope, ok)
	}

	// ResolveScope must also fail closed (return the error, not admit).
	if _, rerr := store.ResolveScope(ctx, "tenant-a", perm); !errors.As(rerr, &decErr) {
		t.Fatalf("ResolveScope(corrupt) err = %v, want *ScopeDecodeError", rerr)
	}

	// ListScopes fails the whole listing closed on a corrupt record.
	if _, lerr := store.ListScopes(ctx, "tenant-a"); !errors.As(lerr, &decErr) {
		t.Fatalf("ListScopes(corrupt) err = %v, want *ScopeDecodeError", lerr)
	}
}

func TestTenantScopeStore_GetScopeMissingReturnsDefault(t *testing.T) {
	store, _ := newTenantScopeStore(t)
	ctx := context.Background()

	got, ok, err := store.GetScope(ctx, "tenant-a", "never:set")
	if err != nil {
		t.Fatalf("GetScope(missing): %v", err)
	}
	if ok {
		t.Fatal("GetScope(missing) reported a definition exists")
	}
	if got != ScopeSelf {
		t.Fatalf("GetScope(missing) = %v, want ScopeSelf", got)
	}
}
