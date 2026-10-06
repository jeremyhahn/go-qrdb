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
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// newResolverFixture builds a TenantService (with a pooled barrier
// registry) and a StoreResolver over a fresh shared in-memory store.
func newResolverFixture(t *testing.T) (*memKVStore, *TenantService, *seal.BarrierRegistry, StoreResolver) {
	t.Helper()
	svc, reg := newTestService(t)
	shared := newMemKVStore()
	resolver, err := NewStoreResolver(shared, reg, svc)
	if err != nil {
		t.Fatalf("NewStoreResolver: %v", err)
	}
	return shared, svc, reg, resolver
}

// requireBackedByShared asserts that a resolved store writes THROUGH to the
// shared store under the tenant's namespace, and that it is not the shared
// store itself.
//
// These four tests used to assert `got == shared` — literal identity with the
// raw shared store. That passed, and it is exactly what made every prefix
// tenant share one undifferentiated keyspace (#41): a dao.GenericDAO keys
// entities "{entityType}/{id}" and has no tenant concept, so one tenant read
// another's rows and one tenant's index answered another's query. The
// assertion has been inverted rather than deleted, because "resolves to the
// shared store" is still the right routing — what changed is that it now
// arrives CONFINED.
func requireBackedByShared(t *testing.T, shared *memKVStore, got kvstore.KVStore, tenantID string) {
	t.Helper()
	if got == kvstore.KVStore(shared) {
		t.Fatalf("%s resolved to the raw shared store; it must be confined to its own keyspace", tenantID)
	}
	ctx := context.Background()
	if err := got.Put(ctx, "probe/1", []byte("v")); err != nil {
		t.Fatalf("Put through the resolved store: %v", err)
	}
	want := tenantID + "/probe/1"
	if _, ok := shared.data[want]; !ok {
		t.Fatalf("write did not land at %q in the shared store; keys present: %v", want, sharedKeys(shared))
	}
}

// sharedKeys lists the physical keys in the shared store, for failure output.
func sharedKeys(shared *memKVStore) []string {
	out := make([]string, 0, len(shared.data))
	for k := range shared.data {
		out = append(out, k)
	}
	return out
}

// (e) The system tenant resolves to the shared store (prefix) and works
// while tenant barriers are sealed — the bootstrap-ordering guarantee.
func TestStoreResolver_SystemTenantUsesSharedStore(t *testing.T) {
	shared, _, _, resolver := newResolverFixture(t)
	ctx := context.Background()

	got, err := resolver.Resolve(ctx, tenantid.System)
	if err != nil {
		t.Fatalf("Resolve(system): %v", err)
	}
	requireBackedByShared(t, shared, got, tenantid.System)

	// Empty tenant ID also resolves to the shared store.
	got, err = resolver.Resolve(ctx, "")
	if err != nil {
		t.Fatalf("Resolve(empty): %v", err)
	}
	// An empty tenant ID resolves to the system tenant's namespace.
	requireBackedByShared(t, shared, got, tenantid.System)
}

// (b) A prefix tenant's data goes to the shared system store.
func TestStoreResolver_PrefixTenantUsesSharedStore(t *testing.T) {
	shared, svc, _, resolver := newResolverFixture(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant(prefix): %v", err)
	}

	got, err := resolver.Resolve(ctx, "acme")
	if err != nil {
		t.Fatalf("Resolve(acme): %v", err)
	}
	requireBackedByShared(t, shared, got, "acme")
}

// (f) Migration: a tenant with empty/legacy IsolationType resolves to the
// shared store.
func TestStoreResolver_LegacyEmptyIsolationUsesSharedStore(t *testing.T) {
	shared, svc, _, resolver := newResolverFixture(t)
	ctx := context.Background()

	// Persist a tenant record with an empty isolation type, simulating a
	// legacy tenant created before the field existed. We write the metadata
	// directly so CreateTenant's default does not coerce the value.
	info := &TenantInfo{ID: "legacy", Name: "Legacy"}
	if err := putTenantMeta(ctx, svc, info); err != nil {
		t.Fatalf("persist legacy tenant: %v", err)
	}

	got, err := resolver.Resolve(ctx, "legacy")
	if err != nil {
		t.Fatalf("Resolve(legacy): %v", err)
	}
	requireBackedByShared(t, shared, got, "legacy")
}

// An unknown tenant (no metadata) resolves to the shared store, preserving
// pre-isolation behaviour.
func TestStoreResolver_UnknownTenantUsesSharedStore(t *testing.T) {
	shared, _, _, resolver := newResolverFixture(t)
	got, err := resolver.Resolve(context.Background(), "ghost")
	if err != nil {
		t.Fatalf("Resolve(ghost): %v", err)
	}
	requireBackedByShared(t, shared, got, "ghost")
}

// (a) A barrier tenant resolves to a per-tenant barrier-backed store that
// is NOT the shared store, and a round-tripped value is encrypted in the
// underlying backend (proving crypto separation).
func TestStoreResolver_BarrierTenantUsesOwnBarrier(t *testing.T) {
	shared, svc, reg, resolver := newResolverFixture(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "secure", "Secure", IsolationBarrier); err != nil {
		t.Fatalf("CreateTenant(barrier): %v", err)
	}
	// Self-managed is the default: the barrier comes up sealed. Initialize it
	// (the tenant-admin step) so the resolver can route to an unsealed store.
	if err := reg.InitializeTenant(ctx, "secure", seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant(secure): %v", err)
	}

	store, err := resolver.Resolve(ctx, "secure")
	if err != nil {
		t.Fatalf("Resolve(secure): %v", err)
	}
	if store == kvstore.KVStore(shared) {
		t.Fatal("barrier tenant must NOT resolve to the shared store")
	}

	const key = "__sys:users/id/secret-record"
	plaintext := []byte("top-secret-iam-payload")
	if err := store.Put(ctx, key, plaintext); err != nil {
		t.Fatalf("Put through barrier store: %v", err)
	}

	// Round-trips through the barrier store.
	got, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get through barrier store: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plaintext)
	}

	// The shared store must NOT contain the record.
	sv, err := shared.Get(ctx, key)
	if err != nil {
		t.Fatalf("shared.Get: %v", err)
	}
	if sv != nil {
		t.Fatal("barrier tenant record leaked into the shared store")
	}
}

// (c) Two barrier tenants are isolated: tenant A's record is not resolvable
// through tenant B's store, and the resolver hands back distinct stores.
func TestStoreResolver_TwoBarrierTenantsAreIsolated(t *testing.T) {
	_, svc, reg, resolver := newResolverFixture(t)
	ctx := context.Background()

	for _, id := range []string{"tenant-a", "tenant-b"} {
		if _, err := svc.CreateTenant(ctx, id, id, IsolationBarrier); err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
		// Self-managed default: initialize the barrier (tenant-admin step).
		if err := reg.InitializeTenant(ctx, id, seal.Credentials{}); err != nil {
			t.Fatalf("InitializeTenant(%s): %v", id, err)
		}
	}

	storeA, err := resolver.Resolve(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("Resolve(tenant-a): %v", err)
	}
	storeB, err := resolver.Resolve(ctx, "tenant-b")
	if err != nil {
		t.Fatalf("Resolve(tenant-b): %v", err)
	}
	if storeA == storeB {
		t.Fatal("two barrier tenants must resolve to distinct stores")
	}

	const key = "__sys:users/id/shared-key-name"
	if err := storeA.Put(ctx, key, []byte("a-only")); err != nil {
		t.Fatalf("storeA.Put: %v", err)
	}

	// tenant B must not see tenant A's value under the same key.
	bv, err := storeB.Get(ctx, key)
	if err != nil {
		t.Fatalf("storeB.Get: %v", err)
	}
	if bv != nil {
		t.Fatalf("tenant B resolved tenant A's record: %q", bv)
	}
}

// (d) A sealed barrier tenant resolves to ErrTenantSealed and NEVER falls
// back to the shared store (fail closed).
func TestStoreResolver_SealedBarrierTenantFailsClosed(t *testing.T) {
	_, svc, reg, resolver := newResolverFixture(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "vault", "Vault", IsolationBarrier); err != nil {
		t.Fatalf("CreateTenant(barrier): %v", err)
	}
	// Self-managed default: initialize the barrier so it is unsealed before the
	// test seals it again below.
	if err := reg.InitializeTenant(ctx, "vault", seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant(vault): %v", err)
	}

	// Resolve once while unsealed so a wrapper is cached, proving the cached
	// wrapper does not mask a subsequent seal.
	if _, err := resolver.Resolve(ctx, "vault"); err != nil {
		t.Fatalf("Resolve(vault) while unsealed: %v", err)
	}

	if err := reg.SealTenant("vault"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}

	store, err := resolver.Resolve(ctx, "vault")
	if !errors.Is(err, ErrTenantSealed) {
		t.Fatalf("Resolve(sealed) = (%v, %v), want ErrTenantSealed", store, err)
	}
	if store != nil {
		t.Fatal("sealed barrier tenant must not return a store (no shared-store fallback)")
	}
}

// newResolverFixtureFor builds a StoreResolver over a service whose default
// entitlements permit the given dedicated-isolation mode, so volume- and
// raft-isolated tenants can be created in-process. It mirrors
// newResolverFixture but lets the caller pick the entitlement posture.
func newResolverFixtureFor(t *testing.T, ents *quota.Entitlements) (*memKVStore, *TenantService, *seal.BarrierRegistry, StoreResolver) {
	t.Helper()
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger(),
		WithDefaultEntitlements(ents), WithVolumeAllocator(volumeAllocatorFor(t, reg)))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	shared := newMemKVStore()
	resolver, err := NewStoreResolver(shared, reg, svc)
	if err != nil {
		t.Fatalf("NewStoreResolver: %v", err)
	}
	return shared, svc, reg, resolver
}

// A volume-isolated tenant routes IAM through its own barrier (NOT the shared
// store), fails closed with ErrTenantSealed while sealed, and round-trips once
// unsealed — proving volume IAM is gated by the tenant's data seal.
func TestStoreResolver_VolumeTenantRoutesThroughBarrier(t *testing.T) {
	shared, svc, reg, resolver := newResolverFixtureFor(t, volumeEntitlements())
	ctx := context.Background()

	// RegisterVolumeTenant (reached via CreateTenant) refuses with ErrSealed
	// while the system barrier is sealed; this test is about the tenant's
	// own barrier sealing IAM, not the system barrier's state.
	if err := reg.SystemBarrier().Initialize(ctx, seal.Credentials{}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	if _, err := svc.CreateTenant(ctx, "vault-vol", "Volume", IsolationVolume); err != nil {
		t.Fatalf("CreateTenant(volume): %v", err)
	}

	// Self-managed default: the barrier comes up SEALED. IAM must fail closed.
	store, err := resolver.Resolve(ctx, "vault-vol")
	if !errors.Is(err, ErrTenantSealed) {
		t.Fatalf("Resolve(sealed volume) = (%v, %v), want ErrTenantSealed", store, err)
	}
	if store != nil {
		t.Fatal("sealed volume tenant must not return a store (no shared-store fallback)")
	}

	// Initialize (tenant-admin step) so the barrier is unsealed.
	if err := reg.InitializeTenant(ctx, "vault-vol", seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant(vault-vol): %v", err)
	}

	store, err = resolver.Resolve(ctx, "vault-vol")
	if err != nil {
		t.Fatalf("Resolve(unsealed volume): %v", err)
	}
	if store == kvstore.KVStore(shared) {
		t.Fatal("volume tenant must NOT resolve to the shared store")
	}

	const key = "__sys:users/id/vol-record"
	if err := store.Put(ctx, key, []byte("vol-secret")); err != nil {
		t.Fatalf("Put through volume barrier store: %v", err)
	}
	sv, err := shared.Get(ctx, key)
	if err != nil {
		t.Fatalf("shared.Get: %v", err)
	}
	if sv != nil {
		t.Fatal("volume tenant record leaked into the shared store")
	}

	// A cached wrapper must not mask a subsequent seal: fail closed again.
	if err := reg.SealTenant("vault-vol"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	store, err = resolver.Resolve(ctx, "vault-vol")
	if !errors.Is(err, ErrTenantSealed) {
		t.Fatalf("Resolve(re-sealed volume) = (%v, %v), want ErrTenantSealed", store, err)
	}
}

// A raft-isolated tenant routes IAM through its own barrier and fails closed
// while sealed, identical to volume/barrier isolation.
func TestStoreResolver_RaftTenantRoutesThroughBarrier(t *testing.T) {
	shared, svc, reg, resolver := newResolverFixtureFor(t, raftEntitlements())
	ctx := context.Background()

	// RegisterVolumeTenant (reached via CreateTenant) refuses with ErrSealed
	// while the system barrier is sealed; this test is about the tenant's
	// own barrier sealing IAM, not the system barrier's state.
	if err := reg.SystemBarrier().Initialize(ctx, seal.Credentials{}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	if _, err := svc.CreateTenant(ctx, "vault-raft", "Raft", IsolationRaft); err != nil {
		t.Fatalf("CreateTenant(raft): %v", err)
	}

	// Sealed by default: IAM fails closed.
	store, err := resolver.Resolve(ctx, "vault-raft")
	if !errors.Is(err, ErrTenantSealed) {
		t.Fatalf("Resolve(sealed raft) = (%v, %v), want ErrTenantSealed", store, err)
	}
	if store != nil {
		t.Fatal("sealed raft tenant must not return a store (no shared-store fallback)")
	}

	if err := reg.InitializeTenant(ctx, "vault-raft", seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant(vault-raft): %v", err)
	}
	store, err = resolver.Resolve(ctx, "vault-raft")
	if err != nil {
		t.Fatalf("Resolve(unsealed raft): %v", err)
	}
	if store == kvstore.KVStore(shared) {
		t.Fatal("raft tenant must NOT resolve to the shared store")
	}
}

func TestStoreResolver_NilArguments(t *testing.T) {
	svc, reg := newTestService(t)
	shared := newMemKVStore()

	if _, err := NewStoreResolver(nil, reg, svc); !errors.Is(err, ErrNilStore) {
		t.Errorf("nil shared: got %v, want ErrNilStore", err)
	}
	// The registry is OPTIONAL. It used to be required, and the consequence
	// was that a node with the seal subsystem disabled got no resolver at all
	// — its IAM stayed on the raw shared store and every prefix tenant shared
	// one keyspace, the exact exposure #41 closes. Keyspace isolation is not
	// a property of encryption.
	if _, err := NewStoreResolver(shared, nil, svc); err != nil {
		t.Errorf("nil registry: got %v, want a working resolver — isolation "+
			"must not depend on the seal subsystem being enabled", err)
	}
	if _, err := NewStoreResolver(shared, reg, nil); !errors.Is(err, ErrNilTenantServicer) {
		t.Errorf("nil servicer: got %v, want ErrNilTenantServicer", err)
	}
}

// putTenantMeta writes a TenantInfo record straight to the service store,
// bypassing CreateTenant's isolation defaulting so legacy/empty-isolation
// records can be simulated.
func putTenantMeta(ctx context.Context, svc *TenantService, info *TenantInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return svc.store.Put(ctx, metadataPrefix+info.ID, data)
}

// TestStoreResolver_RejectsANestingTenantID closes a path that bypasses tenant
// creation.
//
// Resolve accepts an id it has never seen — an unknown tenant deliberately
// falls through to the shared store — so the charset rule enforced at creation
// does not protect it. An id containing '/' would build a prefix nested INSIDE
// another tenant's namespace: "acme/system" resolves to "acme/system/system/",
// which sits under tenant "acme"'s own "acme/system/", so a scan by acme would
// list the crafted tenant's keys.
func TestStoreResolver_RejectsANestingTenantID(t *testing.T) {
	_, _, _, resolver := newResolverFixture(t)
	ctx := context.Background()

	for _, id := range []string{"acme/system", "acme\\system", "acme\x01evil"} {
		got, err := resolver.Resolve(ctx, id)
		var charErr *InvalidTenantIDCharError
		if !errors.As(err, &charErr) {
			t.Fatalf("Resolve(%q) = (%v, %v); want *InvalidTenantIDCharError", id, got, err)
		}
		if got != nil {
			t.Errorf("Resolve(%q) returned a store alongside a refusal", id)
		}
	}
}

// TestStoreResolver_AdjacentTenantIDsDoNotCollide guards the prefix boundary:
// "acme" must not be able to see "acme2"'s records just because its namespace
// is a string prefix of the other's. The trailing separator in
// ComposeNamespace is what prevents it, so this asserts the property rather
// than the formula.
func TestStoreResolver_AdjacentTenantIDsDoNotCollide(t *testing.T) {
	shared, svc, _, resolver := newResolverFixture(t)
	ctx := context.Background()

	for _, id := range []string{"acme", "acme2"} {
		if _, err := svc.CreateTenant(ctx, id, id, IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
	}

	acme, err := resolver.Resolve(ctx, "acme")
	if err != nil {
		t.Fatalf("Resolve(acme): %v", err)
	}
	acme2, err := resolver.Resolve(ctx, "acme2")
	if err != nil {
		t.Fatalf("Resolve(acme2): %v", err)
	}

	if err := acme2.Put(ctx, "probe/1", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	keys, err := acme.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("tenant acme listed %v; acme2's keys must not be visible to it", keys)
	}
	_ = shared
}

// TestStoreResolver_RejectsANestingDatabaseName covers the SECOND segment of
// the keyspace prefix, which is as injectable as the first.
//

// TestStoreResolver_RejectsAReservedPrefixTenantID answers a review finding.
//
// With full namespacing a tenant id of "__sys:" can no longer ESCAPE — its
// namespace "__sys:/system/" reaches nothing another tenant owns, which
// TestNamespacedKVStore_ReservedPrefixTenantIDCannotEscape pins. What it would
// still do is bury that tenant's own records under a prefix the reserved-key
// discovery guard filters out of user-facing enumeration, so the tenant would
// silently fail to list its own data. Refusing the id is cheaper than
// explaining that.
func TestStoreResolver_RejectsAReservedPrefixTenantID(t *testing.T) {
	_, _, _, resolver := newResolverFixture(t)
	ctx := context.Background()

	got, err := resolver.Resolve(ctx, kvstore.ReservedPrefixSystem+"evil")
	var idErr *InvalidTenantIDError
	if !errors.As(err, &idErr) {
		t.Fatalf("Resolve = (%v, %v); want *InvalidTenantIDError", got, err)
	}
	if got != nil {
		t.Error("a refused tenant id must not return a store")
	}
}

// TestStoreResolver_IdentityIsTenantWideNotPerDatabase pins the constraint
// that decides the namespace shape, and it is a hard one rather than a
// preference.
//
// This store serves a tenant's IDENTITY — users, roles, permission scopes,
// invitations — and identity belongs to a tenant, not to one of its
// databases. An earlier draft namespaced at ComposeNamespace's
// "{tenant}/{db}/" and broke login outright: firstboot writes the admin user
// with no database bound to its context, landing it under "{tenant}/system/",
// while any request carrying a database resolved "{tenant}/{db}/" and found
// nothing. Single-database unit tests could not see it; the integration
// stack, which authenticates for real, failed every CLI suite.
func TestStoreResolver_IdentityIsTenantWideNotPerDatabase(t *testing.T) {
	_, svc, _, resolver := newResolverFixture(t)
	base := context.Background()

	if _, err := svc.CreateTenant(base, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Written with NO database bound, as firstboot does.
	bootStore, err := resolver.Resolve(base, "acme")
	if err != nil {
		t.Fatalf("Resolve(no database): %v", err)
	}
	if err := bootStore.Put(base, "__sys:users/id/admin", []byte("admin")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Read through contexts naming different databases, as requests do.
	for _, db := range []string{"default", "alpha", DefaultDatabase} {
		ctx := WithDatabase(base, db)
		store, err := resolver.Resolve(ctx, "acme")
		if err != nil {
			t.Fatalf("Resolve(database %q): %v", db, err)
		}
		got, err := store.Get(ctx, "__sys:users/id/admin")
		if err != nil {
			t.Fatalf("Get(database %q): %v", db, err)
		}
		if got == nil {
			t.Errorf("the admin user is invisible to a request bound to database %q; "+
				"identity is tenant-wide", db)
		}
	}
}

// TestStoreResolver_EmptyAndSystemTenantShareOneNamespace covers a review
// finding, and it is the same split-brain class as per-database identity
// reached through a different door.
//
// IsSystemTenant treats "" and tenantid.System as the same tenant, so both
// route through sharedFor. Building the prefix from the RAW input gave them
// two namespaces — "/" and "system/" — and system IAM written through one
// representation was invisible through the other. tenant.ComposeNamespace
// performed this normalization; hand-rolling the prefix dropped it, which is
// the hazard in re-deriving a formula that already exists.
func TestStoreResolver_EmptyAndSystemTenantShareOneNamespace(t *testing.T) {
	shared, _, _, resolver := newResolverFixture(t)
	ctx := context.Background()

	empty, err := resolver.Resolve(ctx, "")
	if err != nil {
		t.Fatalf("Resolve(empty): %v", err)
	}
	system, err := resolver.Resolve(ctx, tenantid.System)
	if err != nil {
		t.Fatalf("Resolve(system): %v", err)
	}

	// Same store instance: they are one tenant, so they share one cache entry.
	if empty != system {
		t.Error("the empty id and the system id must resolve to the SAME store")
	}

	// Written through one representation, visible through the other.
	if err := empty.Put(ctx, "__sys:users/id/admin", []byte("admin")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := system.Get(ctx, "__sys:users/id/admin")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Error("system IAM written through the empty tenant id is invisible through " +
			"the canonical id: the two representations built different namespaces")
	}

	// And it lands under the canonical prefix, not "/".
	if _, ok := shared.data[tenantid.System+"/__sys:users/id/admin"]; !ok {
		keys := make([]string, 0, len(shared.data))
		for k := range shared.data {
			keys = append(keys, k)
		}
		t.Errorf("write landed at %v; want it under %q", keys, tenantid.System+"/")
	}
}

// TestIdentityNamespace_ResolverAndDirectCallersAgree pins the invariant whose
// violation de-authorized the admin.
//
// Two callers reach the same tenant's identity records by different routes:
// firstboot seeds them through a store it builds ITSELF, before any
// StoreResolver exists, while the RBAC authorizer reads them back through a
// resolver-routed service afterwards. If those two compose the keyspace prefix
// differently, the admin has no resolvable role bindings and EVERY
// authorization check denies — while the data plane keeps working, because it
// never looks a role up.
//
// That is exactly what the integration suite caught: 1,048 failures, all
// "permission denied: admin cannot <verb> on admin", with kv put/del passing.
//
// tenant.IdentityNamespace is the single formula both sides call, and this
// test is the proof they cannot drift apart again.
func TestIdentityNamespace_ResolverAndDirectCallersAgree(t *testing.T) {
	shared, _, _, resolver := newResolverFixture(t)
	ctx := context.Background()

	// The firstboot side: a store built directly, confined by the shared
	// formula, with no resolver in existence yet.
	direct, err := kvstore.NewNamespacedKVStore(shared, IdentityNamespace(tenantid.System))
	if err != nil {
		t.Fatalf("NewNamespacedKVStore: %v", err)
	}
	const roleKey = "__sys:roles/id/admin"
	if err := direct.Put(ctx, roleKey, []byte("admin-role")); err != nil {
		t.Fatalf("seed through the direct store: %v", err)
	}

	// The authorizer side: the resolver-routed store for the same tenant.
	for _, id := range []string{tenantid.System, ""} {
		routed, rerr := resolver.Resolve(ctx, id)
		if rerr != nil {
			t.Fatalf("Resolve(%q): %v", id, rerr)
		}
		got, gerr := routed.Get(ctx, roleKey)
		if gerr != nil {
			t.Fatalf("Get through the resolver (%q): %v", id, gerr)
		}
		if got == nil {
			t.Fatalf("a role seeded before the resolver existed is INVISIBLE through "+
				"Resolve(%q): the writer and the authorizer compose different "+
				"namespaces, so the admin would have no role bindings and every "+
				"authorization check would deny", id)
		}
	}

	// And the reverse direction, since either side may write first.
	routed, err := resolver.Resolve(ctx, tenantid.System)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := routed.Put(ctx, "__sys:roles/id/second", []byte("x")); err != nil {
		t.Fatalf("Put through the resolver: %v", err)
	}
	back, err := direct.Get(ctx, "__sys:roles/id/second")
	if err != nil {
		t.Fatalf("Get through the direct store: %v", err)
	}
	if back == nil {
		t.Error("a record written through the resolver is invisible to the direct store")
	}
}

// A resolver built WITHOUT a barrier registry still confines prefix tenants,
// and still fails closed for tenants that asked for cryptographic isolation.
//
// This is the seal-disabled embedded node: pkg/server/lifecycle supports
// SealBarrier == nil, and in that configuration IAM used to stay on the raw
// shared store, so two prefix tenants shared one keyspace.
func TestStoreResolver_WithoutRegistry(t *testing.T) {
	svc, _ := newTestService(t)
	shared := newMemKVStore()
	ctx := context.Background()

	resolver, err := NewStoreResolver(shared, nil, svc)
	if err != nil {
		t.Fatalf("NewStoreResolver without a registry: %v", err)
	}

	// Two prefix tenants are confined to their own keyspaces.
	acme, err := resolver.Resolve(ctx, "acme")
	if err != nil {
		t.Fatalf("Resolve(acme): %v", err)
	}
	globex, err := resolver.Resolve(ctx, "globex")
	if err != nil {
		t.Fatalf("Resolve(globex): %v", err)
	}
	if err := acme.Put(ctx, "__sys:users/id/1", []byte("acme-user")); err != nil {
		t.Fatalf("Put through acme: %v", err)
	}
	crossed, err := globex.Get(ctx, "__sys:users/id/1")
	if err != nil {
		t.Fatalf("Get through globex: %v", err)
	}
	if crossed != nil {
		t.Fatalf("globex read acme's user (%q) — a seal-disabled node still "+
			"shares one IAM keyspace across prefix tenants", crossed)
	}
	own, err := acme.Get(ctx, "__sys:users/id/1")
	if err != nil || own == nil {
		t.Fatalf("acme cannot read its own record: (%v, %v)", own, err)
	}

	// A dedicated-isolation tenant has no barrier to route through here, so
	// it fails CLOSED rather than being served from the shared store.
	if _, cerr := svc.CreateTenant(ctx, "vault", "Vault", IsolationBarrier); cerr != nil {
		t.Fatalf("CreateTenant(barrier): %v", cerr)
	}
	if _, rerr := resolver.Resolve(ctx, "vault"); !errors.Is(rerr, ErrTenantSealed) {
		t.Fatalf("Resolve(barrier tenant) without a registry = %v, want "+
			"ErrTenantSealed — serving it from the shared store would hand a "+
			"tenant that asked for cryptographic isolation an unencrypted "+
			"keyspace", rerr)
	}
}
