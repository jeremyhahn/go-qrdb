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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
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

// CompareAndSwap completes the trio, replacing the value only when it still
// equals expected. It was a stub returning (false, nil) — a double asserting
// "the swap never happens", which is not a thing any real store does. That went
// unnoticed while nothing called it; UpdateUser now commits through it, and the
// stub would have failed every routed update with a spurious
// ConcurrentUpdateError.
func (s *fakeKVStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.m[key]
	if !ok || !bytes.Equal(current, expected) {
		return false, nil
	}
	cp := make([]byte, len(next))
	copy(cp, next)
	s.m[key] = cp
	return true, nil
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
	created, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "alice", TenantID: "secure"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// The tenant store holds the record; the shared store holds NO part of
	// it. The one key the shared store is allowed to hold is the identity
	// index entry — id and tenant, by design, so a caller holding only the
	// id can be routed here at all (tenant.IdentityIndex). Assert the split
	// key by key rather than by count, so a genuine leak of a user record
	// into the shared store still fails this test.
	if n := len(tenantStore.m); n == 0 {
		t.Fatal("tenant store has no records; user was not routed to it")
	}
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
		if strings.Contains(string(v), "alice") {
			t.Fatalf("index entry %q carries the username; it must carry id and tenant only", k)
		}
	}
	if want := identityIndexPrefix + "user/" + strconv.FormatUint(created.ID, 10); shared.m[want] == nil {
		t.Fatalf("shared store has no identity index entry at %q; keys: %v", want, shared.m)
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
	if _, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "bob", TenantID: "acme"}); err != nil {
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
	ua, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "carol", TenantID: "a"})
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

	if _, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "dan", TenantID: "vault"}); !errors.Is(err, tenant.ErrTenantSealed) {
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

// crossTenantSetup builds a router over two barrier tenants "a" and "b" and
// returns it alongside the two backing stores.
func crossTenantSetup(t *testing.T) (UserServicer, *fakeKVStore, *fakeKVStore) {
	t.Helper()
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
	return svc, storeA, storeB
}

// A non-system caller in tenant A that supplies request tenant B is rejected
// with CrossTenantDeniedError on every IAM routing method that carries a
// request tenant, and tenant B's store is never touched.
func TestRoutingUserService_CrossTenantDenied(t *testing.T) {
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

	_, err := svc.CreateUser(actx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "mallory", TenantID: "b"})
	assertDenied("CreateUser", err)

	_, err = svc.ListUsers(actx, &ListUsersRequest{TenantID: "b"})
	assertDenied("ListUsers", err)

	_, err = svc.GetUserByUsername(actx, "b", "victim")
	assertDenied("GetUserByUsername", err)

	if n := len(storeB.m); n != 0 {
		t.Fatalf("tenant B store unexpectedly holds %d records after denied cross-tenant access", n)
	}
}

// A non-system caller in tenant A with a matching or empty request tenant is
// routed to tenant A's own store, and the stored record's tenant is A (never
// a client-supplied value).
func TestRoutingUserService_SameTenantRoutesToSelf(t *testing.T) {
	svc, storeA, storeB := crossTenantSetup(t)
	actx := tenant.WithTenantID(context.Background(), "a")

	// Explicit matching tenant.
	if _, err := svc.CreateUser(actx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "alice", TenantID: "a"}); err != nil {
		t.Fatalf("CreateUser(match): %v", err)
	}
	// Empty request tenant resolves to the identity tenant, not the system
	// tenant — the record must land in tenant A's store.
	if _, err := svc.CreateUser(actx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "annie", TenantID: ""}); err != nil {
		t.Fatalf("CreateUser(empty): %v", err)
	}
	if len(storeA.m) == 0 {
		t.Fatal("same-tenant users were not routed to tenant A's store")
	}
	if len(storeB.m) != 0 {
		t.Fatalf("tenant B store unexpectedly holds %d records", len(storeB.m))
	}

	// The stored record's tenant is A even for the empty-request case.
	got, err := svc.GetUserByUsername(actx, "", "annie")
	if err != nil {
		t.Fatalf("GetUserByUsername(empty): %v", err)
	}
	if got.TenantID != "a" {
		t.Fatalf("stored TenantID = %q, want a", got.TenantID)
	}
}

// A system-tenant caller MAY target another tenant: the request tenant is
// honored and the record lands in that tenant's store.
func TestRoutingUserService_SystemCallerMayTargetTenant(t *testing.T) {
	svc, _, storeB := crossTenantSetup(t)
	sctx := tenant.WithTenantID(context.Background(), tenant.SystemTenantID)

	created, err := svc.CreateUser(sctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "provisioned", TenantID: "b"})
	if err != nil {
		t.Fatalf("CreateUser(system→b): %v", err)
	}
	if created.TenantID != "b" {
		t.Fatalf("stored TenantID = %q, want b", created.TenantID)
	}
	if len(storeB.m) == 0 {
		t.Fatal("system-caller user was not routed to tenant B's store")
	}

	// System caller can also read/enumerate tenant B.
	if _, err := svc.GetUserByUsername(sctx, "b", "provisioned"); err != nil {
		t.Fatalf("GetUserByUsername(system→b): %v", err)
	}
	if _, err := svc.ListUsers(sctx, &ListUsersRequest{TenantID: "b"}); err != nil {
		t.Fatalf("ListUsers(system→b): %v", err)
	}
}

// Completeness guard: every routingUserService method that accepts a
// request-supplied tenant must reconcile it against the caller's identity, so
// a future method cannot silently reintroduce the cross-tenant leak. Each
// entry invokes the method as a non-system caller in tenant A targeting
// tenant B and asserts a CrossTenantDeniedError.
func TestRoutingUserService_AllRequestTenantMethodsReconciled(t *testing.T) {
	svc, _, _ := crossTenantSetup(t)
	actx := tenant.WithTenantID(context.Background(), "a")

	cases := []struct {
		name string
		call func() error
	}{
		{"CreateUser", func() error {
			_, err := svc.CreateUser(actx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "x", TenantID: "b"})
			return err
		}},
		{"ListUsers", func() error {
			_, err := svc.ListUsers(actx, &ListUsersRequest{TenantID: "b"})
			return err
		}},
		{"GetUserByUsername", func() error {
			_, err := svc.GetUserByUsername(actx, "b", "x")
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

func TestNewRoutingService_NilArgs(t *testing.T) {
	if _, err := NewRoutingService(nil, newTestIDGen(t)); err == nil {
		t.Error("expected error for nil resolver")
	}
	resolver := &fakeResolver{shared: newFakeKVStore(), tenants: map[string]kvstore.KVStore{}}
	if _, err := NewRoutingService(resolver, nil); err == nil {
		t.Error("expected error for nil id generator")
	}
}

// The regression this branch had to close. Confining each tenant to its own
// keyspace (#41) broke every operation that names a user by its globally
// unique id: `users delete <id>` issued by the admin routed to the SYSTEM
// store, where a tenant's user does not live, and returned "user not found"
// across all five transports. The identity index makes the by-id route
// deliberate instead of accidental.
//
// The caller here carries NO tenant in context — exactly the admin CLI's
// situation — and every by-id method must still reach the tenant that holds
// the record.
func TestRoutingUserService_ByIDReachesTheTenantThatHoldsIt(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": newFakeKVStore()},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	// The admin: no tenant bound to the context, so FromContext is "" and
	// the caller is the system tenant.
	ctx := context.Background()
	created, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "dana", TenantID: "acme"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	got, err := svc.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetUser by id from the admin context: %v — the by-id route did "+
			"not reach the tenant holding the record", err)
	}
	if got.Username != "dana" || got.TenantID != "acme" {
		t.Fatalf("GetUser = %+v, want dana in acme", got)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	thumbprint := ComputeSPKIThumbprint(makeCertForKey(t, key, "dana", 1))
	if _, err := svc.AddCertBinding(ctx, created.ID, thumbprint, 0); err != nil {
		t.Fatalf("AddCertBinding by id: %v", err)
	}
	if _, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: created.ID, Username: "dana2"}); err != nil {
		t.Fatalf("UpdateUser by id: %v", err)
	}
	if err := svc.DisableUser(ctx, created.ID); err != nil {
		t.Fatalf("DisableUser by id: %v", err)
	}
	if err := svc.EnableUser(ctx, created.ID); err != nil {
		t.Fatalf("EnableUser by id: %v", err)
	}
	if err := svc.DeleteUser(ctx, created.ID); err != nil {
		t.Fatalf("DeleteUser by id: %v — this is the exact CLI failure", err)
	}

	// Deleting the user removes its index entry, so the id cannot keep
	// routing to a tenant that no longer holds it.
	if _, err := svc.GetUser(ctx, created.ID); err == nil {
		t.Fatal("GetUser after delete succeeded; the record or its index entry survived")
	}
	index, err := tenant.NewResolvedIdentityIndex(resolver)
	if err != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", err)
	}
	if _, ok, lerr := index.Lookup(ctx, tenant.IdentityUser, strconv.FormatUint(created.ID, 10)); lerr != nil || ok {
		t.Fatalf("index entry after delete = (ok=%v, err=%v); a stale entry keeps "+
			"routing a dead id at a tenant", ok, lerr)
	}
}

// Routing by id must fail CLOSED when the tenant that holds the record has a
// sealed barrier. The index says where the record lives; it must never become
// a reason to read somewhere the record is not encrypted.
func TestRoutingUserService_ByIDFailsClosedWhenOwnerSealed(t *testing.T) {
	shared := newFakeKVStore()
	resolver := &fakeResolver{
		shared:  shared,
		tenants: map[string]kvstore.KVStore{"vault": newFakeKVStore()},
		sealed:  map[string]bool{},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := context.Background()
	created, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "erin", TenantID: "vault"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	resolver.sealed["vault"] = true

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"GetUser", func() error { _, e := svc.GetUser(ctx, created.ID); return e }},
		{"UpdateUser", func() error {
			_, e := svc.UpdateUser(ctx, &UpdateUserRequest{ID: created.ID, Username: "x"})
			return e
		}},
		{"AddCertBinding", func() error {
			_, e := svc.AddCertBinding(ctx, created.ID, sealedThumbprint(t), 0)
			return e
		}},
		{"EnableUser", func() error { return svc.EnableUser(ctx, created.ID) }},
		{"DisableUser", func() error { return svc.DisableUser(ctx, created.ID) }},
		{"DeleteUser", func() error { return svc.DeleteUser(ctx, created.ID) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, tenant.ErrTenantSealed) {
				t.Fatalf("%s = %v, want ErrTenantSealed — a by-id call must not fall "+
					"back to the shared store when the owning barrier is sealed", tc.name, err)
			}
		})
	}
}

// A caller in another tenant must learn NOTHING from the index. It supplied
// only an id; a cross-tenant denial would confirm both that the record exists
// and which tenant holds it, which is an enumeration oracle the shared
// keyspace never had.
func TestRoutingUserService_ByIDTellsAForeignCallerNothing(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": newFakeKVStore(), "globex": newFakeKVStore()},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := context.Background()
	created, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "frank", TenantID: "acme"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	foreign := tenant.WithTenantID(ctx, "globex")
	_, err = svc.GetUser(foreign, created.ID)

	var notFound *UserNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("foreign GetUser = %v, want UserNotFoundError", err)
	}
	var denied *tenant.CrossTenantDeniedError
	if errors.As(err, &denied) {
		t.Fatal("foreign GetUser returned CrossTenantDeniedError; that answer " +
			"distinguishes a real id in another tenant from a fabricated one")
	}
	derr := svc.DeleteUser(foreign, created.ID)
	if errors.As(derr, &denied) {
		t.Fatal("foreign DeleteUser returned CrossTenantDeniedError; same oracle")
	}
	if !errors.As(derr, &notFound) {
		t.Fatalf("foreign DeleteUser = %v, want UserNotFoundError — the answer must "+
			"be identical to the one a fabricated id gets", derr)
	}
	// And the record survives the foreign attempt.
	if _, err := svc.GetUser(ctx, created.ID); err != nil {
		t.Fatalf("the record was disturbed by a foreign caller: %v", err)
	}
}

// sealedThumbprint returns a syntactically valid SPKI thumbprint, so a
// sealed-tenant test fails on the seal rather than on validation.
func sealedThumbprint(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return ComputeSPKIThumbprint(makeCertForKey(t, key, "erin", 1))
}

// fixedIDGen hands out the same Snowflake every time, which is what a
// duplicated node seed would do across two nodes. It makes the id-collision
// path reachable in a test; nothing else can reach it, because user ids are
// generated inside the store.
type fixedIDGen struct{ id uint64 }

func (f *fixedIDGen) NextID(_ dao.Entity) uint64 { return f.id }

// A refused identity claim must leave NO record behind. The claim is taken
// before the primary write for exactly this reason: a user record whose id is
// claimed by another tenant is unroutable by id, and writing it first would
// mean a failed create still consumed a username and left a row only a tenant
// scan could ever find.
func TestRoutingUserService_ARefusedClaimWritesNothing(t *testing.T) {
	acmeStore := newFakeKVStore()
	globexStore := newFakeKVStore()
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": acmeStore, "globex": globexStore},
	}
	svc, err := NewRoutingService(resolver, &fixedIDGen{id: 4242})
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := context.Background()
	if _, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "first", TenantID: "acme"}); err != nil {
		t.Fatalf("CreateUser(acme): %v", err)
	}

	// The same id, generated again, for a different tenant.
	_, err = svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "second", TenantID: "globex"})
	var conflict *tenant.IdentityClaimConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("colliding id across tenants = %v, want *tenant.IdentityClaimConflictError", err)
	}
	if len(globexStore.m) != 0 {
		t.Fatalf("globex store holds %d keys after a refused claim: %v — the record "+
			"was written before the claim was settled", len(globexStore.m), globexStore.m)
	}

	// acme's record is untouched and still routes by id.
	got, gerr := svc.GetUser(ctx, 4242)
	if gerr != nil {
		t.Fatalf("GetUser after the refused claim: %v", gerr)
	}
	if got.Username != "first" || got.TenantID != "acme" {
		t.Fatalf("GetUser = %+v; the id was redirected", got)
	}
}

// errInjected is the fault a faultStore raises.
var errInjected = errors.New("injected storage fault")

// faultStore fails a chosen operation on a chosen key a chosen number of
// times. It embeds *fakeKVStore so PutIfAbsent is promoted: the identity index
// refuses a store that cannot claim atomically.
type faultStore struct {
	*fakeKVStore
	failPut    map[string]int
	failDelete map[string]int
	// failCompareAndDelete injects on the GUARDED delete, which is the one
	// the identity index's release actually uses.
	failCompareAndDelete map[string]int
	// onPut runs before each Put, so a test can cancel the caller's context
	// at the moment a write is attempted.
	onPut func()
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
		f.onPut()
	}
	if n := f.failPut[k]; n > 0 {
		f.failPut[k] = n - 1
		return errInjected
	}
	return f.fakeKVStore.Put(ctx, k, v)
}

// PutIfAbsent honours failPut too. A test that says "the write of this key
// fails" is describing the OUTCOME, not the verb, and the user store now claims
// its username/email index entries with PutIfAbsent rather than Put (#190). If
// the injection point did not follow the verb, the fault would silently stop
// firing and the rollback assertions below would pass without ever exercising a
// failure.
func (f *faultStore) PutIfAbsent(ctx context.Context, k string, v []byte) (bool, error) {
	if f.onPut != nil {
		f.onPut()
	}
	if n := f.failPut[k]; n > 0 {
		f.failPut[k] = n - 1
		return false, errInjected
	}
	return f.fakeKVStore.PutIfAbsent(ctx, k, v)
}

func (f *faultStore) Delete(ctx context.Context, k string) error {
	// Honour cancellation, as any real store does. Without this the
	// detached-context compensation would look correct even if it used the
	// caller's dead context.
	if err := ctx.Err(); err != nil {
		return err
	}
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

// A create that fails partway must leave NOTHING behind — no claim, and no
// half-written record. A leaked claim blocks the id; a leaked primary record
// is worse, because the username index the retry keys on was never written,
// so neither a retry nor a delete can ever reach it.
func TestRoutingUserService_AFailedCreateRollsBackCompletely(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
	}{
		{"primary record", "__sys:users/id/"},
		{"tenant index", "__sys:users/tenant/acme/"},
		{"username index", "__sys:user_idx/username/acme/gwen"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tenantStore := newFaultStore()
			shared := newFakeKVStore()
			resolver := &fakeResolver{
				shared:  shared,
				tenants: map[string]kvstore.KVStore{"acme": tenantStore},
			}
			// A fixed id makes the key of every write predictable.
			svc, err := NewRoutingService(resolver, &fixedIDGen{id: 7777})
			if err != nil {
				t.Fatalf("NewRoutingService: %v", err)
			}
			ctx := context.Background()

			key := tc.key
			if key != "__sys:user_idx/username/acme/gwen" {
				key += string(encodeID(7777))
			}
			tenantStore.failPut[key] = 1

			req := &CreateUserRequest{WithoutEmailLoginKey: true, Username: "gwen", TenantID: "acme"}
			if _, err := svc.CreateUser(ctx, req); !errors.Is(err, errInjected) {
				t.Fatalf("CreateUser = %v, want the injected fault", err)
			}

			if n := len(tenantStore.m); n != 0 {
				t.Fatalf("tenant store holds %d keys after a failed create: %v — a "+
					"half-written record no retry and no delete can reach",
					n, tenantStore.m)
			}
			index, ierr := tenant.NewResolvedIdentityIndex(resolver)
			if ierr != nil {
				t.Fatalf("NewResolvedIdentityIndex: %v", ierr)
			}
			if _, held, lerr := index.Lookup(ctx, tenant.IdentityUser, "7777"); lerr != nil || held {
				t.Fatalf("claim after a failed create = (held=%v, err=%v), want released",
					held, lerr)
			}

			// The retry succeeds and the record is reachable by id.
			if _, rerr := svc.CreateUser(ctx, req); rerr != nil {
				t.Fatalf("retry after rollback: %v", rerr)
			}
			if got, gerr := svc.GetUser(ctx, 7777); gerr != nil || got.Username != "gwen" {
				t.Fatalf("GetUser after retry = (%+v, %v)", got, gerr)
			}
		})
	}
}

// A delete interrupted after the record is gone but before the claim is
// released is completed by a plain retry, rather than short-circuiting on the
// missing record and stranding the claim forever.
func TestRoutingUserService_AnInterruptedDeleteIsCompletedByRetry(t *testing.T) {
	shared := newFaultStore()
	resolver := &fakeResolver{
		shared:  shared,
		tenants: map[string]kvstore.KVStore{"acme": newFakeKVStore()},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := context.Background()

	created, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "hank", TenantID: "acme"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	claimKey := "__sys:identity_idx/tenant/user/" + strconv.FormatUint(created.ID, 10)
	shared.failCompareAndDelete[claimKey] = 1
	if derr := svc.DeleteUser(ctx, created.ID); !errors.Is(derr, errInjected) {
		t.Fatalf("first delete = %v, want the injected fault", derr)
	}

	index, ierr := tenant.NewResolvedIdentityIndex(resolver)
	if ierr != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", ierr)
	}
	idStr := strconv.FormatUint(created.ID, 10)
	if _, held, lerr := index.Lookup(ctx, tenant.IdentityUser, idStr); lerr != nil || !held {
		t.Fatalf("precondition: the interrupted delete should have left the claim "+
			"(held=%v, err=%v)", held, lerr)
	}

	if derr := svc.DeleteUser(ctx, created.ID); derr != nil {
		t.Fatalf("retry of an interrupted delete = %v, want nil", derr)
	}
	if _, held, lerr := index.Lookup(ctx, tenant.IdentityUser, idStr); lerr != nil || held {
		t.Fatalf("claim after the retry = (held=%v, err=%v), want released", held, lerr)
	}
}

// The tolerant retry must not turn every miss into a success.
func TestRoutingUserService_DeletingAnUnknownIDStillReportsNotFound(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": newFakeKVStore()},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	var notFound *UserNotFoundError
	if err := svc.DeleteUser(context.Background(), 999999); !errors.As(err, &notFound) {
		t.Fatalf("DeleteUser(unknown) = %v, want UserNotFoundError", err)
	}
}

// Compensation must survive the cancellation that caused the failure. When a
// create fails because the request context was cancelled or timed out,
// cleanup issued on that same context fails at every step — leaving exactly
// the claim the compensation exists to release.
func TestRoutingUserService_RollbackSurvivesACancelledContext(t *testing.T) {
	tenantStore := newFaultStore()
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": tenantStore},
	}
	svc, err := NewRoutingService(resolver, &fixedIDGen{id: 5150})
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Fail the primary write, then cancel: the create fails and the
	// compensation runs under a context whose parent is already dead.
	tenantStore.failPut["__sys:users/id/"+string(encodeID(5150))] = 1
	tenantStore.onPut = func() { cancel() }

	if _, cerr := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "ivan", TenantID: "acme"}); cerr == nil {
		t.Fatal("CreateUser succeeded; the injected fault did not fire")
	}
	tenantStore.onPut = nil

	index, ierr := tenant.NewResolvedIdentityIndex(resolver)
	if ierr != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", ierr)
	}
	if _, held, lerr := index.Lookup(context.Background(), tenant.IdentityUser, "5150"); lerr != nil || held {
		t.Fatalf("claim after a create that failed on a CANCELLED context = "+
			"(held=%v, err=%v); compensation ran on the dead context and could "+
			"not release it", held, lerr)
	}
}

// sliceKVStore is a contract-compliant KVStore whose dynamic type is NOT
// comparable: a named slice type cannot be a map key. Nothing in the KVStore
// interface forbids this, so a resolver returning one is valid — and a
// routing cache keyed by the store panics with "hash of unhashable type" on
// the FIRST operation, before any tenant work runs.
type sliceKVStore []struct{}

func (sliceKVStore) Put(context.Context, string, []byte) error   { return nil }
func (sliceKVStore) Get(context.Context, string) ([]byte, error) { return nil, nil }
func (sliceKVStore) Delete(context.Context, string) error        { return nil }
func (sliceKVStore) List(context.Context, string) ([]string, error) {
	return nil, nil
}
func (sliceKVStore) Exists(context.Context, string) (bool, error) { return false, nil }
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

// A resolver returning a non-comparable store must not crash the router.
// pkg/invite guarded this and the two routers its cache was modelled on did
// not, so a contract-compliant resolver panicked them before doing any work.
func TestRoutingUserService_NonComparableStoreDoesNotPanic(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"weird": sliceKVStore{}},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := tenant.WithTenantID(context.Background(), "weird")
	// The store answers everything with nils, so the lookup simply finds
	// nothing. What matters is that it RETURNS rather than panicking.
	if _, err := svc.GetUserByUsername(ctx, "weird", "anyone"); err == nil {
		t.Fatal("expected a not-found from an empty store")
	}
	if _, err := svc.GetUserByUsername(ctx, "weird", "anyone"); err == nil {
		t.Fatal("expected a not-found on the second call too")
	}
}
