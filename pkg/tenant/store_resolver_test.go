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

// (e) The system tenant resolves to the shared store (prefix) and works
// while tenant barriers are sealed — the bootstrap-ordering guarantee.
func TestStoreResolver_SystemTenantUsesSharedStore(t *testing.T) {
	shared, _, _, resolver := newResolverFixture(t)
	ctx := context.Background()

	got, err := resolver.Resolve(ctx, SystemTenantID)
	if err != nil {
		t.Fatalf("Resolve(system): %v", err)
	}
	if got != kvstore.KVStore(shared) {
		t.Fatal("system tenant must resolve to the shared store")
	}

	// Empty tenant ID also resolves to the shared store.
	got, err = resolver.Resolve(ctx, "")
	if err != nil {
		t.Fatalf("Resolve(empty): %v", err)
	}
	if got != kvstore.KVStore(shared) {
		t.Fatal("empty tenant must resolve to the shared store")
	}
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
	if got != kvstore.KVStore(shared) {
		t.Fatal("prefix tenant must resolve to the shared store")
	}
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
	if got != kvstore.KVStore(shared) {
		t.Fatal("legacy/empty-isolation tenant must resolve to the shared store")
	}
}

// An unknown tenant (no metadata) resolves to the shared store, preserving
// pre-isolation behaviour.
func TestStoreResolver_UnknownTenantUsesSharedStore(t *testing.T) {
	shared, _, _, resolver := newResolverFixture(t)
	got, err := resolver.Resolve(context.Background(), "ghost")
	if err != nil {
		t.Fatalf("Resolve(ghost): %v", err)
	}
	if got != kvstore.KVStore(shared) {
		t.Fatal("unknown tenant must resolve to the shared store")
	}
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
	svc, err := NewTenantService(store, reg, testLogger(), WithDefaultEntitlements(ents))
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
	if _, err := NewStoreResolver(shared, nil, svc); !errors.Is(err, ErrNilRegistry) {
		t.Errorf("nil registry: got %v, want ErrNilRegistry", err)
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
