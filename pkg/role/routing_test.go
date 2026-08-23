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
	"errors"
	"strings"
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

func (s *fakeKVStore) Put(ctx context.Context, k string, v []byte) error {
	// Honour cancellation, as any real store does. A fake that ignores it
	// makes compensation-on-a-dead-context bugs invisible.
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(v))
	copy(cp, v)
	s.m[k] = cp
	return nil
}

// PutIfAbsent stores only when the key is absent, serialising the
// check-then-write under the same lock a real store resolves it under. The
// identity index REFUSES a store without it (tenant.ErrIdentityStoreNotAtomic),
// because a claim that reads and then writes is not a claim — so a fake that
// omitted it would model a store this code deliberately rejects.
func (s *fakeKVStore) PutIfAbsent(ctx context.Context, k string, v []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.m[k]; exists {
		return false, nil
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	s.m[k] = cp
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (s *fakeKVStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.m[key]
	if !ok {
		return false, nil
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	delete(s.m, key)
	return true, nil
}

func (s *fakeKVStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}

func (s *fakeKVStore) Get(ctx context.Context, k string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[k]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (s *fakeKVStore) Delete(ctx context.Context, k string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
	// The shared store may hold exactly one thing: the identity index entry
	// (id -> tenant), which is what lets a caller holding only the id be
	// routed here. Assert key by key so a real leak of a role record still
	// fails.
	const identityIndexPrefix = "__sys:identity_idx/tenant/"
	for k, v := range shared.m {
		if !strings.HasPrefix(k, identityIndexPrefix) {
			t.Fatalf("shared store holds non-index key %q (leak)", k)
		}
		// The value is the tenant plus an opaque generation token — the token
		// is what lets a release tell one claim of an id from the next. It
		// still carries nothing about the RECORD.
		if !strings.HasPrefix(string(v), "secure\x00") {
			t.Fatalf("index entry %q = %q, want tenant \"secure\" and a token", k, v)
		}
		if strings.Contains(string(v), "editor") {
			t.Fatalf("index entry %q carries the role name; it must carry id and tenant only", k)
		}
	}
	if want := identityIndexPrefix + "role/" + created.ID; shared.m[want] == nil {
		t.Fatalf("shared store has no identity index entry at %q; keys: %v", want, shared.m)
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
	// A non-reserved name: this test is about cross-tenant routing, and a
	// canonical system role name would now be refused unless created with its
	// canonical id and permission set.
	ra, err := svc.CreateRole(ctx, &CreateRoleRequest{Name: "manager", TenantID: "a"})
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

// crossTenantSetup builds a router over two barrier tenants "a" and "b".
func crossTenantSetup(t *testing.T) (RoleServicer, *fakeKVStore, *fakeKVStore) {
	t.Helper()
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
	return svc, storeA, storeB
}

// A non-system caller in tenant A supplying request tenant B is rejected with
// CrossTenantDeniedError on every routing method that carries a request
// tenant, and tenant B's store is never touched.
func TestRoutingRoleService_CrossTenantDenied(t *testing.T) {
	svc, _, storeB := crossTenantSetup(t)
	actx := tenant.WithTenantID(context.Background(), "a")

	assertDenied := func(name string, err error) {
		t.Helper()
		var xt *tenant.CrossTenantDeniedError
		if !errors.As(err, &xt) {
			t.Fatalf("%s cross-tenant = %v, want CrossTenantDeniedError", name, err)
		}
		if xt.IdentityTenantID != "a" || xt.RequestedTenantID != "b" {
			t.Fatalf("%s error tenants = (%q,%q), want (a,b)", name, xt.IdentityTenantID, xt.RequestedTenantID)
		}
	}

	_, err := svc.CreateRole(actx, &CreateRoleRequest{Name: "sneaky", TenantID: "b"})
	assertDenied("CreateRole", err)

	_, err = svc.ListRoles(actx, &ListRolesRequest{TenantID: "b"})
	assertDenied("ListRoles", err)

	_, err = svc.GetRoleByName(actx, "b", "admin")
	assertDenied("GetRoleByName", err)

	if n := len(storeB.m); n != 0 {
		t.Fatalf("tenant B store unexpectedly holds %d records after denied cross-tenant access", n)
	}
}

// A non-system caller in tenant A with a matching or empty request tenant is
// routed to tenant A's own store, and the stored record's tenant is A.
func TestRoutingRoleService_SameTenantRoutesToSelf(t *testing.T) {
	svc, storeA, storeB := crossTenantSetup(t)
	actx := tenant.WithTenantID(context.Background(), "a")

	if _, err := svc.CreateRole(actx, &CreateRoleRequest{Name: "editor", TenantID: "a"}); err != nil {
		t.Fatalf("CreateRole(match): %v", err)
	}
	created, err := svc.CreateRole(actx, &CreateRoleRequest{Name: "viewer", TenantID: ""})
	if err != nil {
		t.Fatalf("CreateRole(empty): %v", err)
	}
	if created.TenantID != "a" {
		t.Fatalf("stored TenantID = %q, want a", created.TenantID)
	}
	if len(storeA.m) == 0 {
		t.Fatal("same-tenant roles were not routed to tenant A's store")
	}
	if len(storeB.m) != 0 {
		t.Fatalf("tenant B store unexpectedly holds %d records", len(storeB.m))
	}

	byName, err := svc.GetRoleByName(actx, "", "viewer")
	if err != nil {
		t.Fatalf("GetRoleByName(empty): %v", err)
	}
	if byName.TenantID != "a" {
		t.Fatalf("GetRoleByName TenantID = %q, want a", byName.TenantID)
	}
}

// A system-tenant caller MAY target another tenant.
func TestRoutingRoleService_SystemCallerMayTargetTenant(t *testing.T) {
	svc, _, storeB := crossTenantSetup(t)
	sctx := tenant.WithTenantID(context.Background(), tenant.SystemTenantID)

	created, err := svc.CreateRole(sctx, &CreateRoleRequest{Name: "provisioned", TenantID: "b"})
	if err != nil {
		t.Fatalf("CreateRole(system→b): %v", err)
	}
	if created.TenantID != "b" {
		t.Fatalf("stored TenantID = %q, want b", created.TenantID)
	}
	if len(storeB.m) == 0 {
		t.Fatal("system-caller role was not routed to tenant B's store")
	}
	if _, err := svc.GetRoleByName(sctx, "b", "provisioned"); err != nil {
		t.Fatalf("GetRoleByName(system→b): %v", err)
	}
	if _, err := svc.ListRoles(sctx, &ListRolesRequest{TenantID: "b"}); err != nil {
		t.Fatalf("ListRoles(system→b): %v", err)
	}
}

// Completeness guard: every routingRoleService method that accepts a
// request-supplied tenant must reconcile it against the caller's identity.
func TestRoutingRoleService_AllRequestTenantMethodsReconciled(t *testing.T) {
	svc, _, _ := crossTenantSetup(t)
	actx := tenant.WithTenantID(context.Background(), "a")

	cases := []struct {
		name string
		call func() error
	}{
		{"CreateRole", func() error {
			_, err := svc.CreateRole(actx, &CreateRoleRequest{Name: "x", TenantID: "b"})
			return err
		}},
		{"ListRoles", func() error {
			_, err := svc.ListRoles(actx, &ListRolesRequest{TenantID: "b"})
			return err
		}},
		{"GetRoleByName", func() error {
			_, err := svc.GetRoleByName(actx, "b", "x")
			return err
		}},
	}
	for _, tc := range cases {
		var xt *tenant.CrossTenantDeniedError
		if err := tc.call(); !errors.As(err, &xt) {
			t.Errorf("%s: cross-tenant call = %v, want CrossTenantDeniedError", tc.name, err)
		}
	}
}

func TestNewRoutingRoleService_NilResolver(t *testing.T) {
	if _, err := NewRoutingService(nil); err == nil {
		t.Error("expected error for nil resolver")
	}
}

// The role half of the regression #41 opened. `roles create --tenant-id X`
// followed by `roles delete <uuid>` failed with "role: not found" on all five
// transports, because the delete routed on the ADMIN's tenant while the
// record lived in X. `roles assign --role <uuid>` failed the same way through
// GetRole. The caller here carries no tenant, exactly as the admin CLI does.
func TestRoutingRoleService_ByIDReachesTheTenantThatHoldsIt(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": newFakeKVStore()},
	}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := context.Background()
	created, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name: "db-admin", TenantID: "acme",
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	got, err := svc.GetRole(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetRole by id from the admin context: %v — this is the "+
			"`roles assign` failure", err)
	}
	if got.Name != "db-admin" || got.TenantID != "acme" {
		t.Fatalf("GetRole = %+v, want db-admin in acme", got)
	}

	if _, err := svc.UpdateRole(ctx, &UpdateRoleRequest{ID: created.ID, Name: "db-admin-2"}); err != nil {
		t.Fatalf("UpdateRole by id: %v", err)
	}
	if err := svc.DeleteRole(ctx, created.ID); err != nil {
		t.Fatalf("DeleteRole by id: %v — this is the `roles delete` failure", err)
	}

	index, err := tenant.NewResolvedIdentityIndex(resolver)
	if err != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", err)
	}
	if _, ok, lerr := index.Lookup(ctx, tenant.IdentityRole, created.ID); lerr != nil || ok {
		t.Fatalf("index entry after delete = (ok=%v, err=%v); a stale entry keeps "+
			"routing a dead id at a tenant", ok, lerr)
	}
}

// A by-id role call fails closed when the owning tenant's barrier is sealed.
func TestRoutingRoleService_ByIDFailsClosedWhenOwnerSealed(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"vault": newFakeKVStore()},
		sealed:  map[string]bool{},
	}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := context.Background()
	created, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name: "keeper", TenantID: "vault",
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	resolver.sealed["vault"] = true

	if _, err := svc.GetRole(ctx, created.ID); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("GetRole = %v, want ErrTenantSealed", err)
	}
	if _, err := svc.UpdateRole(ctx, &UpdateRoleRequest{ID: created.ID, Name: "x"}); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("UpdateRole = %v, want ErrTenantSealed", err)
	}
	if err := svc.DeleteRole(ctx, created.ID); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("DeleteRole = %v, want ErrTenantSealed", err)
	}
}

// A foreign caller learns nothing from the index: not-found, never a
// cross-tenant denial that would confirm the id exists elsewhere.
func TestRoutingRoleService_ByIDTellsAForeignCallerNothing(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": newFakeKVStore(), "globex": newFakeKVStore()},
	}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := context.Background()
	created, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name: "secret", TenantID: "acme",
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	foreign := tenant.WithTenantID(ctx, "globex")
	_, err = svc.GetRole(foreign, created.ID)

	var notFound *RoleNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("foreign GetRole = %v, want RoleNotFoundError", err)
	}
	var denied *tenant.CrossTenantDeniedError
	if errors.As(err, &denied) {
		t.Fatal("foreign GetRole returned CrossTenantDeniedError; that answer " +
			"distinguishes a real id in another tenant from a fabricated one")
	}
	if _, err := svc.GetRole(ctx, created.ID); err != nil {
		t.Fatalf("the record was disturbed by a foreign caller: %v", err)
	}
}

// ResolvePermissions is deliberately NOT placement-routed: it resolves the
// caller's OWN bindings during authorization, so a binding naming another
// tenant's role must not import that role's permissions.
func TestRoutingRoleService_ResolvePermissionsStaysInTheCallersTenant(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": newFakeKVStore(), "globex": newFakeKVStore()},
	}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := context.Background()
	privileged, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name: "root", TenantID: "acme",
		Permissions: []permission.Permission{permission.KVGet, permission.KVPut},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	// A caller in globex holding a binding that names acme's role id must
	// resolve NO permissions from it.
	foreign := tenant.WithTenantID(ctx, "globex")
	perms, err := svc.ResolvePermissions(foreign, []string{privileged.ID})
	if len(perms) != 0 {
		t.Fatalf("ResolvePermissions = %v; a binding naming another tenant's role "+
			"imported that role's permissions", perms)
	}
	var notFound *RoleNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("ResolvePermissions err = %v, want RoleNotFoundError — the role is "+
			"not resolvable from globex at all", err)
	}
}

// THE HIJACK, end to end. Role IDs are caller-supplied: CreateRoleRequest.ID
// travels from the REST and gRPC surfaces straight into the store. Without a
// global claim, tenant globex could create a role carrying acme's role id,
// take over that id's identity-index entry, and silently redirect every by-id
// operation an administrator performs on acme's role to globex's.
func TestRoutingRoleService_ForeignTenantCannotCaptureARoleID(t *testing.T) {
	acmeStore := newFakeKVStore()
	globexStore := newFakeKVStore()
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": acmeStore, "globex": globexStore},
	}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := context.Background()
	victim, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name: "acme-admin", TenantID: "acme",
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole(acme): %v", err)
	}

	// globex names acme's id explicitly.
	_, err = svc.CreateRole(ctx, &CreateRoleRequest{
		ID: victim.ID, Name: "impostor", TenantID: "globex",
		Permissions: []permission.Permission{permission.KVPut},
	})
	var exists *RoleIDExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("capturing another tenant's role id = %v, want RoleIDExistsError", err)
	}
	if exists.TenantID != "globex" {
		t.Fatalf("RoleIDExistsError.TenantID = %q, want the CALLER's tenant %q — "+
			"naming the holder discloses where the id lives", exists.TenantID, "globex")
	}

	// Nothing was written into globex's store: the claim is refused BEFORE
	// the record, so a failed capture leaves no orphan behind.
	if len(globexStore.m) != 0 {
		t.Fatalf("globex store holds %d keys after a refused capture: %v",
			len(globexStore.m), globexStore.m)
	}

	// And the id still routes to acme's role, unchanged.
	got, err := svc.GetRole(ctx, victim.ID)
	if err != nil {
		t.Fatalf("GetRole after the refused capture: %v", err)
	}
	if got.Name != "acme-admin" || got.TenantID != "acme" {
		t.Fatalf("GetRole = %+v; the id was redirected to the impostor", got)
	}
}

// A duplicate caller-supplied id WITHIN one tenant must be refused too —
// otherwise CreateRole silently overwrites the record it collides with.
func TestRoutingRoleService_DuplicateIDInTheSameTenantIsRefused(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": newFakeKVStore()},
	}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := context.Background()
	first, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name: "one", TenantID: "acme",
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	_, err = svc.CreateRole(ctx, &CreateRoleRequest{
		ID: first.ID, Name: "two", TenantID: "acme",
		Permissions: []permission.Permission{permission.KVPut},
	})
	var exists *RoleIDExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("duplicate id in the same tenant = %v, want RoleIDExistsError", err)
	}
	got, gerr := svc.GetRole(ctx, first.ID)
	if gerr != nil {
		t.Fatalf("GetRole: %v", gerr)
	}
	if got.Name != "one" {
		t.Fatalf("the original record was overwritten: %+v", got)
	}
}

// errInjected is the fault a faultStore raises.
var errInjected = errors.New("injected storage fault")

// faultStore fails a chosen operation on a chosen key a chosen number of
// times, then behaves normally. It embeds *fakeKVStore so PutIfAbsent is
// promoted — the identity index refuses a store that cannot claim atomically,
// so a wrapper that hid it would model a store this code rejects.
type faultStore struct {
	*fakeKVStore
	failPut    map[string]int
	failDelete map[string]int
	// failCompareAndDelete injects on the GUARDED delete, which is the one
	// the identity index's release actually uses.
	failCompareAndDelete map[string]int
	// onPut runs before each Put, so a test can interleave another writer at
	// the moment this one is mid-create.
	onPut func(key string)
}

func newFaultStore() *faultStore {
	return &faultStore{
		fakeKVStore:          newFakeKVStore(),
		failPut:              map[string]int{},
		failDelete:           map[string]int{},
		failCompareAndDelete: map[string]int{},
	}
}

func (f *faultStore) Put(ctx context.Context, k string, v []byte) error {
	if f.onPut != nil {
		f.onPut(k)
	}
	if n := f.failPut[k]; n > 0 {
		f.failPut[k] = n - 1
		return errInjected
	}
	return f.fakeKVStore.Put(ctx, k, v)
}

func (f *faultStore) Delete(ctx context.Context, k string) error {
	if n := f.failDelete[k]; n > 0 {
		f.failDelete[k] = n - 1
		return errInjected
	}
	return f.fakeKVStore.Delete(ctx, k)
}

// CompareAndDelete injects on the guarded delete. The identity index releases
// a claim through this, not through Delete, so a test that only injected on
// Delete would exercise nothing.
func (f *faultStore) CompareAndDelete(ctx context.Context, k string, expected []byte) (bool, error) {
	if n := f.failCompareAndDelete[k]; n > 0 {
		f.failCompareAndDelete[k] = n - 1
		return false, errInjected
	}
	return f.fakeKVStore.CompareAndDelete(ctx, k, expected)
}

// A caller-supplied role id must survive a transient write failure. Role ids
// are chosen names, not generated ones: a claim left behind by a failed
// create would burn that id permanently, and the record does not exist for
// DeleteRole to reach and release it.
//
// Each subtest fails ONE of the writes that follow the claim, proving the
// compensation covers every path rather than only the first.
func TestRoutingRoleService_AFailedCreateReleasesItsClaim(t *testing.T) {
	const chosenID = "chosen-role-id"

	for _, tc := range []struct {
		name string
		key  func(roleID string) string
	}{
		{"primary record", func(id string) string { return "__sys:roles/id/" + id }},
		{"tenant index", func(id string) string { return "__sys:roles/tenant/acme/" + id }},
		{"name index", func(string) string { return "__sys:role_idx/name/acme/chosen" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tenantStore := newFaultStore()
			resolver := &fakeResolver{
				shared:  newFakeKVStore(),
				tenants: map[string]kvstore.KVStore{"acme": tenantStore},
			}
			svc, err := NewRoutingService(resolver)
			if err != nil {
				t.Fatalf("NewRoutingService: %v", err)
			}
			ctx := context.Background()
			tenantStore.failPut[tc.key(chosenID)] = 1

			req := &CreateRoleRequest{
				ID: chosenID, Name: "chosen", TenantID: "acme",
				Permissions: []permission.Permission{permission.KVGet},
			}
			if _, err := svc.CreateRole(ctx, req); !errors.Is(err, errInjected) {
				t.Fatalf("CreateRole = %v, want the injected fault", err)
			}

			// The claim must be gone, or the caller's chosen id is burned.
			index, ierr := tenant.NewResolvedIdentityIndex(resolver)
			if ierr != nil {
				t.Fatalf("NewResolvedIdentityIndex: %v", ierr)
			}
			if _, held, lerr := index.Lookup(ctx, tenant.IdentityRole, chosenID); lerr != nil || held {
				t.Fatalf("claim after a failed create = (held=%v, err=%v); the "+
					"caller-supplied id is burned", held, lerr)
			}

			// And the retry — the whole point — succeeds with the same id.
			retried, rerr := svc.CreateRole(ctx, req)
			if rerr != nil {
				t.Fatalf("retry after a released claim: %v", rerr)
			}
			if retried.ID != chosenID {
				t.Fatalf("retry produced id %q, want %q", retried.ID, chosenID)
			}
			if got, gerr := svc.GetRole(ctx, chosenID); gerr != nil || got.Name != "chosen" {
				t.Fatalf("GetRole after retry = (%+v, %v)", got, gerr)
			}
		})
	}
}

// A delete interrupted after the record is gone but before the claim is
// released must be completable by a plain retry. Without the tolerant path
// the retry short-circuits on the missing record and the claim survives
// forever: the id routes at a tenant that does not hold it, and nothing can
// remove the entry.
func TestRoutingRoleService_AnInterruptedDeleteIsCompletedByRetry(t *testing.T) {
	shared := newFaultStore()
	resolver := &fakeResolver{
		shared:  shared,
		tenants: map[string]kvstore.KVStore{"acme": newFakeKVStore()},
	}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := context.Background()

	created, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name: "doomed", TenantID: "acme",
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	// Fail the claim release once: the record is removed, the entry is not.
	shared.failCompareAndDelete["__sys:identity_idx/tenant/role/"+created.ID] = 1
	if derr := svc.DeleteRole(ctx, created.ID); !errors.Is(derr, errInjected) {
		t.Fatalf("first delete = %v, want the injected fault", derr)
	}

	index, ierr := tenant.NewResolvedIdentityIndex(resolver)
	if ierr != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", ierr)
	}
	if _, held, lerr := index.Lookup(ctx, tenant.IdentityRole, created.ID); lerr != nil || !held {
		t.Fatalf("precondition: the interrupted delete should have left the claim "+
			"(held=%v, err=%v)", held, lerr)
	}

	// The retry sees no record. It must finish the job, not report not-found.
	if derr := svc.DeleteRole(ctx, created.ID); derr != nil {
		t.Fatalf("retry of an interrupted delete = %v, want nil — the claim is "+
			"unreachable once the record is gone, so a short-circuit strands it", derr)
	}
	if _, held, lerr := index.Lookup(ctx, tenant.IdentityRole, created.ID); lerr != nil || held {
		t.Fatalf("claim after the retry = (held=%v, err=%v), want released", held, lerr)
	}
}

// Deleting an id that never existed still reports not-found. The tolerant
// retry must complete an interrupted delete, not turn every miss into a
// success.
func TestRoutingRoleService_DeletingAnUnknownIDStillReportsNotFound(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": newFakeKVStore()},
	}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	var notFound *RoleNotFoundError
	if err := svc.DeleteRole(context.Background(), "never-existed"); !errors.As(err, &notFound) {
		t.Fatalf("DeleteRole(unknown) = %v, want RoleNotFoundError", err)
	}
}

// THE RECLAIM WINDOW, which is why the claim is released LAST rather than
// first. Another tenant holds the id while the original tenant's record still
// exists — the state a release-first ordering would produce, and one that no
// supported sequence of operations can otherwise reach, which is why it has
// to be seeded by hand here.
//
// The original tenant may delete its OWN record; what it must never do is
// free a claim another tenant holds, putting the id back up for capture. It
// reports the refusal rather than declaring the delete complete.
func TestRoutingRoleService_ARetryNeverFreesAnotherTenantsClaim(t *testing.T) {
	acmeStore := newFakeKVStore()
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": acmeStore, "globex": newFakeKVStore()},
	}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := context.Background()

	const contested = "contested-role-id"

	// Reproduce the state a release-first delete ordering would produce:
	// acme's RECORD still exists while globex holds the claim for its id.
	// Seeded directly, because no supported sequence of operations can reach
	// it — which is the point of refusing to open the window.
	record := `{"id":"` + contested + `","name":"acme-role","tenantID":"acme"}`
	if err := acmeStore.Put(ctx, "__sys:roles/id/"+contested, []byte(record)); err != nil {
		t.Fatalf("seed acme record: %v", err)
	}
	if err := acmeStore.Put(ctx, "__sys:roles/tenant/acme/"+contested, []byte(contested)); err != nil {
		t.Fatalf("seed acme tenant index: %v", err)
	}

	index, err := tenant.NewResolvedIdentityIndex(resolver)
	if err != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", err)
	}
	if _, err := index.Record(ctx, tenant.IdentityRole, contested, "globex"); err != nil {
		t.Fatalf("globex claim: %v", err)
	}

	// acme retries its delete for that id. It must not report success, and
	// must not free a claim it does not hold.
	acmeCtx := tenant.WithTenantID(ctx, "acme")
	if derr := svc.DeleteRole(acmeCtx, contested); derr == nil {
		t.Fatal("acme's retry reported the delete complete while globex holds the claim")
	}

	// acme's OWN record is removed — it owns that record and deleting it is
	// legitimate. What acme must not do is touch globex's claim, and the
	// error it gets is precisely the refusal to.
	if raw, rerr := acmeStore.Get(ctx, "__sys:roles/id/"+contested); rerr != nil || raw != nil {
		t.Fatalf("acme's own record after its delete = (%v, %v), want it removed",
			raw, rerr)
	}

	holderClaim, held, lerr := index.Lookup(ctx, tenant.IdentityRole, contested)
	if lerr != nil || !held || holderClaim.Tenant != "globex" {
		t.Fatalf("claim after acme's retry = (%q, %v, %v); acme freed a claim it "+
			"does not hold, putting the id back up for capture", holderClaim.Tenant, held, lerr)
	}
}

// THE ORDERING ITSELF. The primary record is deleted LAST of the store's
// writes so that "record absent" implies every index entry is already gone —
// which is the precondition that makes the tolerant retry sound. Delete the
// primary FIRST and a failure on a later index delete leaves the record gone
// with index entries behind; the retry then takes the tolerant path, releases
// the claim and reports SUCCESS over the leak.
//
// The name index is the observable one: a leaked entry makes the name
// permanently unusable in that tenant.
func TestRoutingRoleService_AnInterruptedDeleteLeaksNoIndexEntry(t *testing.T) {
	tenantStore := newFaultStore()
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": tenantStore},
	}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := context.Background()

	created, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name: "reusable", TenantID: "acme",
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	// Fail the NAME-INDEX delete once, mid-delete.
	tenantStore.failDelete["__sys:role_idx/name/acme/reusable"] = 1
	if derr := svc.DeleteRole(ctx, created.ID); !errors.Is(derr, errInjected) {
		t.Fatalf("first delete = %v, want the injected fault", derr)
	}

	// The retry must COMPLETE the delete, not declare victory over a leak.
	if derr := svc.DeleteRole(ctx, created.ID); derr != nil {
		t.Fatalf("retry of an interrupted delete = %v, want nil", derr)
	}

	// The proof: the name is usable again. A leaked index entry would make
	// this fail with RoleNameExistsError.
	if _, rerr := svc.CreateRole(ctx, &CreateRoleRequest{
		Name: "reusable", TenantID: "acme",
		Permissions: []permission.Permission{permission.KVGet},
	}); rerr != nil {
		t.Fatalf("recreating the name after a completed delete = %v; the "+
			"interrupted delete leaked an index entry and the retry reported "+
			"success over it", rerr)
	}
}

// The rollback must delete only what THIS call wrote. The name key is shared
// BY VALUE, so two concurrent creates of one name in one tenant can both pass
// the uniqueness check — and the one that fails, rolling back the full
// possible key set, would delete the WINNER's name-index entry: the winner's
// role becomes unfindable by name and its name free to be taken again.
//
// The interleaving is reproduced rather than raced: the loser's uniqueness
// check runs first and passes, then the winner's name-index write lands while
// the loser is mid-create, then the loser fails. That is the same order a
// real interleaving produces, deterministically.
func TestRoutingRoleService_RollbackDeletesOnlyWhatThisCallWrote(t *testing.T) {
	tenantStore := newFaultStore()
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": tenantStore},
	}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := context.Background()

	const nameKey = "__sys:role_idx/name/acme/shared-name"
	const winnerID = "winner-id"

	// The winner's name-index write lands while the loser is between its own
	// uniqueness check and its failure.
	tenantStore.onPut = func(key string) {
		if key == "__sys:roles/id/loser-id" {
			tenantStore.onPut = nil
			if perr := tenantStore.fakeKVStore.Put(ctx, nameKey, []byte(winnerID)); perr != nil {
				t.Errorf("seed the winner's name index: %v", perr)
			}
		}
	}

	// The loser fails on its NAME-index write, so `written` holds its primary
	// and tenant index but NOT the name key.
	tenantStore.failPut[nameKey] = 1
	if _, lerr := svc.CreateRole(ctx, &CreateRoleRequest{
		ID: "loser-id", Name: "shared-name", TenantID: "acme",
		Permissions: []permission.Permission{permission.KVPut},
	}); !errors.Is(lerr, errInjected) {
		t.Fatalf("loser create = %v, want the injected fault", lerr)
	}

	// The winner's name index survives the loser's rollback.
	raw, gerr := tenantStore.Get(ctx, nameKey)
	if gerr != nil {
		t.Fatalf("Get(name index): %v", gerr)
	}
	if string(raw) != winnerID {
		t.Fatalf("name index after the loser rolled back = %q, want the winner's "+
			"%q — the rollback deleted a key it never wrote", raw, winnerID)
	}

	// And the loser's own keys are gone.
	if r, rerr := tenantStore.Get(ctx, "__sys:roles/id/loser-id"); rerr != nil || r != nil {
		t.Fatalf("loser's primary after rollback = (%v, %v), want removed", r, rerr)
	}
}

// When the record cleanup itself fails, the claim must be KEPT. Releasing it
// over a partial record makes the id claimable by another tenant while this
// tenant's leftovers sit there — the same window the delete path refuses to
// open.
func TestRoutingRoleService_RollbackKeepsTheClaimWhenCleanupFails(t *testing.T) {
	tenantStore := newFaultStore()
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": tenantStore},
	}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := context.Background()

	// Fail the NAME-index write, so the primary and tenant index are written
	// and must be rolled back — then fail the primary's cleanup delete.
	tenantStore.failPut["__sys:role_idx/name/acme/doomed"] = 1
	tenantStore.failDelete["__sys:roles/id/stuck-id"] = 1

	if _, cerr := svc.CreateRole(ctx, &CreateRoleRequest{
		ID: "stuck-id", Name: "doomed", TenantID: "acme",
		Permissions: []permission.Permission{permission.KVGet},
	}); !errors.Is(cerr, errInjected) {
		t.Fatalf("create = %v, want the injected fault", cerr)
	}

	index, ierr := tenant.NewResolvedIdentityIndex(resolver)
	if ierr != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", ierr)
	}
	holderClaim, held, lerr := index.Lookup(ctx, tenant.IdentityRole, "stuck-id")
	if lerr != nil || !held || holderClaim.Tenant != "acme" {
		t.Fatalf("claim after a FAILED cleanup = (%q, %v, %v); the id was freed "+
			"while this tenant's leftovers are still in its store", holderClaim.Tenant, held, lerr)
	}
}

// sliceKVStore is a contract-compliant KVStore whose dynamic type is NOT
// comparable — see the equivalent in pkg/user. A routing cache keyed by the
// store panics on it before any role operation runs.
type sliceKVStore []struct{}

func (sliceKVStore) Put(context.Context, string, []byte) error      { return nil }
func (sliceKVStore) Get(context.Context, string) ([]byte, error)    { return nil, nil }
func (sliceKVStore) Delete(context.Context, string) error           { return nil }
func (sliceKVStore) List(context.Context, string) ([]string, error) { return nil, nil }
func (sliceKVStore) Exists(context.Context, string) (bool, error)   { return false, nil }
func (sliceKVStore) Scan(context.Context, string, func(string, []byte) error) error {
	return nil
}
func (sliceKVStore) RegisterEntityIndexes(string, []kvstore.EntityIndex) error { return nil }
func (sliceKVStore) QueryIndex(context.Context, string, []byte) ([][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}
func (sliceKVStore) ScanIndex(context.Context, string, []byte, []byte) (map[string][][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}
func (sliceKVStore) QueryCompoundIndex(context.Context, string, [][]byte) ([][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}
func (sliceKVStore) ScanCompoundIndex(context.Context, string, [][]byte, []byte, []byte) (map[string][][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

func TestRoutingRoleService_NonComparableStoreDoesNotPanic(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"weird": sliceKVStore{}},
	}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := tenant.WithTenantID(context.Background(), "weird")
	if _, err := svc.GetRoleByName(ctx, "weird", "anything"); err == nil {
		t.Fatal("expected a not-found from an empty store")
	}
	if _, err := svc.GetRoleByName(ctx, "weird", "anything"); err == nil {
		t.Fatal("expected a not-found on the second call too")
	}
}
