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

// Failure-path tests for the per-tenant key manager.
//
// A DEK failure must surface as an error, never as a zero key or a silently
// regenerated one: a tenant whose DEK "regenerated" because the wrapped copy
// could not be read would come up with a key that decrypts none of its
// existing data.

package seal

import (
	"context"
	"errors"
	"testing"
)

// newFailableKeyManager wires a key manager over a real AEAD test barrier and
// a store whose failures the caller controls.
func newFailableKeyManager(t *testing.T, barrier *testBarrier, store *failingKVStore) TenantKeyManager {
	t.Helper()
	m, err := NewTenantKeyManager(barrier, store)
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}
	return m
}

// newFailableStore returns a store wrapper backed by the package's in-memory
// KV store.
func newFailableStore() *failingKVStore {
	return &failingKVStore{inner: newMemoryKVStore()}
}

// TestGetTenantDEKRefusesWhenTheBarrierIsSealed covers the first guard. The
// barrier holds the key that unwraps every DEK, so a sealed barrier means the
// DEK is UNAVAILABLE -- not absent, which would trigger regeneration.
func TestGetTenantDEKRefusesWhenTheBarrierIsSealed(t *testing.T) {
	barrier := newUnsealedBarrier(t)
	barrier.sealed.Store(true)
	m := newFailableKeyManager(t, barrier, newFailableStore())

	dek, err := m.GetTenantDEK(context.Background(), "acme")
	if err == nil {
		t.Fatal("a sealed barrier must refuse to produce a DEK")
	}
	var want *BarrierSealedError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *BarrierSealedError", err, err)
	}
	if dek != nil {
		t.Error("a refused request must return no key material")
	}
}

// TestGetTenantDEKServesTheCachedKeyOnASecondCall covers the cache-hit branch
// and pins that the caller gets a COPY. A cache that handed out its own buffer
// would let one caller's mutation corrupt every later reader's key.
func TestGetTenantDEKServesTheCachedKeyOnASecondCall(t *testing.T) {
	ctx := context.Background()
	m := newFailableKeyManager(t, newUnsealedBarrier(t), newFailableStore())

	first, err := m.GetTenantDEK(ctx, "acme")
	if err != nil {
		t.Fatalf("first GetTenantDEK: %v", err)
	}
	second, err := m.GetTenantDEK(ctx, "acme")
	if err != nil {
		t.Fatalf("second GetTenantDEK: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("the cached DEK differs from the one first returned")
	}

	first[0] ^= 0xFF
	third, err := m.GetTenantDEK(ctx, "acme")
	if err != nil {
		t.Fatalf("third GetTenantDEK: %v", err)
	}
	if string(third) == string(first) {
		t.Fatal("mutating a returned DEK changed the cached value")
	}
}

// TestGetTenantDEKReportsAStoreReadFailure covers the wrapped-DEK read error.
// This is the branch that must NOT fall through to generating a fresh DEK.
func TestGetTenantDEKReportsAStoreReadFailure(t *testing.T) {
	store := newFailableStore()
	store.getErr = errors.New("store unavailable")
	m := newFailableKeyManager(t, newUnsealedBarrier(t), store)

	dek, err := m.GetTenantDEK(context.Background(), "acme")
	if err == nil {
		t.Fatal("an unreadable store must fail rather than regenerate the DEK")
	}
	if dek != nil {
		t.Error("a failed read must return no key material")
	}
}

// TestGetTenantDEKReportsAnUnwrapFailure covers the barrier-decrypt error: the
// wrapped DEK is present but will not unwrap, which is corruption or the wrong
// barrier. Either way it must not be papered over with a new key.
func TestGetTenantDEKReportsAnUnwrapFailure(t *testing.T) {
	ctx := context.Background()
	store := newFailableStore()
	m := newFailableKeyManager(t, newUnsealedBarrier(t), store)

	if _, err := m.GetTenantDEK(ctx, "acme"); err != nil {
		t.Fatalf("seed GetTenantDEK: %v", err)
	}
	m.ClearCache()

	// Replace the stored value with one this barrier cannot open. The cache
	// was just dropped, so the next read must go through the store.
	if err := store.inner.Put(ctx, tenantDEKStoreKey("acme"), []byte("not wrapped by this barrier")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if _, err := m.GetTenantDEK(ctx, "acme"); err == nil {
		t.Fatal("an unwrappable DEK must be reported, not replaced")
	}
}

// TestGetTenantDEKReportsAStoreWriteFailureOnGeneration covers the persist
// error on the generate path. A DEK generated but never stored would work
// until the process restarted and then decrypt nothing.
func TestGetTenantDEKReportsAStoreWriteFailureOnGeneration(t *testing.T) {
	store := newFailableStore()
	store.putErr = errors.New("store full")
	m := newFailableKeyManager(t, newUnsealedBarrier(t), store)

	if _, err := m.GetTenantDEK(context.Background(), "acme"); err == nil {
		t.Fatal("a failed persist must fail the DEK request")
	}
}

// TestDestroyTenantDEKReportsADeleteFailure covers the eviction error path.
// Reporting success while the wrapped DEK is still on disk would leave a
// deleted tenant's key recoverable.
func TestDestroyTenantDEKReportsADeleteFailure(t *testing.T) {
	ctx := context.Background()
	store := newFailableStore()
	m := newFailableKeyManager(t, newUnsealedBarrier(t), store)

	if _, err := m.GetTenantDEK(ctx, "acme"); err != nil {
		t.Fatalf("seed GetTenantDEK: %v", err)
	}

	store.deleteErr = errors.New("delete failed")
	if err := m.DestroyTenantDEK(ctx, "acme"); err == nil {
		t.Fatal("a failed delete must be reported")
	}
}

// TestRotateTenantDEKReportsAPersistFailure covers rotation's store error.
// Rotation that reported success without persisting would leave the cache
// holding a key that no longer matches what is on disk, and the next restart
// would silently revert to the old one.
func TestRotateTenantDEKReportsAPersistFailure(t *testing.T) {
	ctx := context.Background()
	store := newFailableStore()
	m := newFailableKeyManager(t, newUnsealedBarrier(t), store)

	if _, err := m.GetTenantDEK(ctx, "acme"); err != nil {
		t.Fatalf("seed GetTenantDEK: %v", err)
	}

	store.putErr = errors.New("store full")
	if err := m.RotateTenantDEK(ctx, "acme"); err == nil {
		t.Fatal("a failed persist must fail the rotation")
	}
}

// TestRotateTenantDEKRefusesWhenSealed and the empty-tenant guard cover the
// two preconditions, both of which run before any key material is generated.
func TestRotateTenantDEKRefusesOnBadPreconditions(t *testing.T) {
	ctx := context.Background()

	t.Run("empty tenant id", func(t *testing.T) {
		m := newFailableKeyManager(t, newUnsealedBarrier(t), newFailableStore())
		if err := m.RotateTenantDEK(ctx, ""); err == nil {
			t.Fatal("an empty tenant id must be refused")
		}
	})

	t.Run("sealed barrier", func(t *testing.T) {
		barrier := newUnsealedBarrier(t)
		barrier.sealed.Store(true)
		m := newFailableKeyManager(t, barrier, newFailableStore())

		err := m.RotateTenantDEK(ctx, "acme")
		var want *BarrierSealedError
		if !errors.As(err, &want) {
			t.Fatalf("error = %v (%T), want *BarrierSealedError", err, err)
		}
	})
}

// TestImportTenantKeyReportsAPersistFailure covers the import path's wrap and
// store step. An import that failed silently would leave the tenant using a
// generated key instead of the one the operator supplied -- and the supplied
// key is usually the only thing that can read migrated data.
func TestImportTenantKeyReportsAPersistFailure(t *testing.T) {
	store := newFailableStore()
	store.putErr = errors.New("store full")
	m := newFailableKeyManager(t, newUnsealedBarrier(t), store)

	keyMaterial := make([]byte, 32)
	for i := range keyMaterial {
		keyMaterial[i] = 0x5A
	}

	if err := m.ImportTenantKey(context.Background(), "acme", keyMaterial); err == nil {
		t.Fatal("a failed persist must fail the import")
	}
}
