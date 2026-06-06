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

package seal_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// newInitializedTenantBarrier builds a tenant barrier over a shared base
// MemoryBackend and initializes (unseals) it. It returns the tenant
// barrier and the shared base so tests can inspect the on-disk bytes.
func newInitializedTenantBarrier(t *testing.T, tenantID string, base seal.StorageBackend) *seal.TenantBarrier {
	t.Helper()
	tb, err := seal.NewTenantBarrier(tenantID, poolTestLogger(), base, seal.BarrierConfig{}, newPoolSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewTenantBarrier(%s): %v", tenantID, err)
	}
	if err := tb.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("Initialize(%s): %v", tenantID, err)
	}
	return tb
}

func TestTenantBarrierKVStore_NilBarrier(t *testing.T) {
	if _, err := seal.NewTenantBarrierKVStore(nil); !errors.Is(err, seal.ErrNilTenantBarrier) {
		t.Fatalf("NewTenantBarrierKVStore(nil) = %v, want ErrNilTenantBarrier", err)
	}
}

// TestTenantBarrierKVStore_RoundTripAndCryptoSeparation proves that values
// written through the wrapper round-trip, are stored under the tenant
// namespace in the base backend, and are stored as CIPHERTEXT (the
// plaintext never appears in the base backend's stored bytes).
func TestTenantBarrierKVStore_RoundTripAndCryptoSeparation(t *testing.T) {
	base := seal.NewMemoryBackend()
	tb := newInitializedTenantBarrier(t, "alpha", base)

	store, err := seal.NewTenantBarrierKVStore(tb)
	if err != nil {
		t.Fatalf("NewTenantBarrierKVStore: %v", err)
	}

	ctx := context.Background()
	const key = "__sys:users/id/record"
	plaintext := []byte("super-secret-iam-value")

	if err := store.Put(ctx, key, plaintext); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plaintext)
	}

	// The base backend stores the value under the "{tenantID}/" namespace.
	raw, err := base.Get(ctx, "alpha/"+key)
	if err != nil {
		t.Fatalf("base.Get(namespaced): %v", err)
	}
	if raw == nil {
		t.Fatal("value not stored under the tenant namespace in the base backend")
	}
	// And the stored bytes are ciphertext: the plaintext must not appear.
	if bytes.Contains(raw, plaintext) {
		t.Fatal("plaintext found in base backend: value was not encrypted with the tenant DEK")
	}

	// Exists reports presence; a missing key reports absence (not an error).
	ok, err := store.Exists(ctx, key)
	if err != nil || !ok {
		t.Fatalf("Exists(present) = (%v, %v), want (true, nil)", ok, err)
	}
	ok, err = store.Exists(ctx, "missing")
	if err != nil || ok {
		t.Fatalf("Exists(missing) = (%v, %v), want (false, nil)", ok, err)
	}

	// Get on a missing key returns (nil, nil) per the kvstore contract.
	mv, err := store.Get(ctx, "missing")
	if err != nil || mv != nil {
		t.Fatalf("Get(missing) = (%v, %v), want (nil, nil)", mv, err)
	}
}

// TestTenantBarrierKVStore_SealedFailsClosed proves Put/Get/Exists surface
// ErrSealed once the tenant barrier is sealed — they do NOT silently treat
// a sealed barrier as an empty store.
func TestTenantBarrierKVStore_SealedFailsClosed(t *testing.T) {
	base := seal.NewMemoryBackend()
	tb := newInitializedTenantBarrier(t, "beta", base)

	store, err := seal.NewTenantBarrierKVStore(tb)
	if err != nil {
		t.Fatalf("NewTenantBarrierKVStore: %v", err)
	}

	ctx := context.Background()
	if err := store.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put (unsealed): %v", err)
	}

	if err := tb.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if err := store.Put(ctx, "k2", []byte("v2")); !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("Put(sealed) = %v, want ErrSealed", err)
	}
	if _, err := store.Get(ctx, "k"); !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("Get(sealed) = %v, want ErrSealed", err)
	}
	if _, err := store.Exists(ctx, "k"); !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("Exists(sealed) = %v, want ErrSealed", err)
	}
}

// TestTenantBarrierKVStore_TwoTenantsIsolated proves two tenant barriers
// over the SAME base backend cannot read each other's records (namespace
// isolation) and decrypt with their own DEK.
func TestTenantBarrierKVStore_TwoTenantsIsolated(t *testing.T) {
	base := seal.NewMemoryBackend()
	tbA := newInitializedTenantBarrier(t, "a", base)
	tbB := newInitializedTenantBarrier(t, "b", base)

	storeA, err := seal.NewTenantBarrierKVStore(tbA)
	if err != nil {
		t.Fatalf("wrap A: %v", err)
	}
	storeB, err := seal.NewTenantBarrierKVStore(tbB)
	if err != nil {
		t.Fatalf("wrap B: %v", err)
	}

	ctx := context.Background()
	if err := storeA.Put(ctx, "shared-key", []byte("a-value")); err != nil {
		t.Fatalf("storeA.Put: %v", err)
	}

	// Tenant B must not see tenant A's record under the same logical key.
	bv, err := storeB.Get(ctx, "shared-key")
	if err != nil {
		t.Fatalf("storeB.Get: %v", err)
	}
	if bv != nil {
		t.Fatalf("tenant B read tenant A's record: %q", bv)
	}
}

// TestRegisterTenantPooled_DurableIndependentBackend proves
// RegisterTenantPooled creates a durable, independent per-tenant Pebble via
// the volume pool, inheriting the system strategies.
func TestRegisterTenantPooled_DurableIndependentBackend(t *testing.T) {
	reg, _, perTenantReg := newPooledRegistry(t)
	ctx := context.Background()

	tb, err := reg.RegisterTenantPooled(ctx, "pooled")
	if err != nil {
		t.Fatalf("RegisterTenantPooled: %v", err)
	}
	if tb.TenantID() != "pooled" {
		t.Fatalf("TenantID = %q, want pooled", tb.TenantID())
	}

	// A dedicated per-tenant Pebble was opened.
	if _, err := perTenantReg.Get("pooled"); err != nil {
		t.Fatalf("perTenantReg.Get(pooled): %v", err)
	}

	// Initialize then round-trip a value to prove the backend is usable.
	if err := reg.InitializeTenant(ctx, "pooled", seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}
	if err := tb.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := tb.Get(ctx, "k")
	if err != nil || !bytes.Equal(got, []byte("v")) {
		t.Fatalf("Get = (%q, %v), want (v, nil)", got, err)
	}
}

// TestRegisterTenantPooled_NoPoolConfigured proves the pooled flow fails
// closed when no volume pool is configured.
func TestRegisterTenantPooled_NoPoolConfigured(t *testing.T) {
	system := newPoolSystemBarrier(t)
	reg, err := seal.NewBarrierRegistry(poolTestLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	if _, err := reg.RegisterTenantPooled(context.Background(), "x"); !errors.Is(err, seal.ErrPoolNotConfigured) {
		t.Fatalf("RegisterTenantPooled without pool = %v, want ErrPoolNotConfigured", err)
	}
}
