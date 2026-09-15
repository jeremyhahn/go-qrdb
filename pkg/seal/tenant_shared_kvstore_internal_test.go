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

package seal

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// nonAtomicKVStore is a KVStore that deliberately does NOT implement
// AtomicKVStore, standing in for a shared store whose backend has no
// compare-and-set. It embeds the interface rather than the concrete memory
// store so the PutIfAbsent method is not promoted.
type nonAtomicKVStore struct {
	kvstore.KVStore
}

// newTenantSharedStore returns a tenantSharedKVStore over the given shared
// backing store, with an initialized tenant barrier.
func newTenantSharedStore(t *testing.T, shared kvstore.KVStore) *tenantSharedKVStore {
	t.Helper()
	backend := NewMemoryBackend()
	tb, err := NewTenantBarrier("tshared", testLogger(), backend, BarrierConfig{}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewTenantBarrier: %v", err)
	}
	if err := tb.Initialize(context.Background(), MustCredentials([]byte("pw"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	store, err := NewTenantSharedKVStore(tb, shared)
	if err != nil {
		t.Fatalf("NewTenantSharedKVStore: %v", err)
	}
	switch w := store.(type) {
	case *atomicTenantSharedKVStore:
		return w.tenantSharedKVStore
	case *tenantSharedKVStore:
		return w
	default:
		t.Fatalf("NewTenantSharedKVStore returned %T", store)
		return nil
	}
}

// newTenantSharedStoreIface returns the wrapper as the interface a caller
// actually holds, so a test can perform the capability assertion for real.
func newTenantSharedStoreIface(t *testing.T, shared kvstore.KVStore) kvstore.KVStore {
	t.Helper()
	backend := NewMemoryBackend()
	tb, err := NewTenantBarrier("tshared", testLogger(), backend, BarrierConfig{}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewTenantBarrier: %v", err)
	}
	if err := tb.Initialize(context.Background(), MustCredentials([]byte("pw"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	store, err := NewTenantSharedKVStore(tb, shared)
	if err != nil {
		t.Fatalf("NewTenantSharedKVStore: %v", err)
	}
	return store
}

// TestTenantSharedKVStore_IsNotAtomicWithoutAtomicBacking is the #30
// regression, in the form a caller actually checks it.
//
// kvstore.AtomicKVStore is optional and discovered by type assertion, so a
// caller that needs exactly-once semantics fails closed at CONSTRUCTION:
//
//	atomic, ok := store.(kvstore.AtomicKVStore)
//	if !ok { return ErrAtomicWriteUnsupported }
//
// The wrapper used to define PutIfAbsent unconditionally and fall back to
// Exists-then-Put — the exact TOCTOU the capability exists to eliminate,
// delivered while reporting success. Returning a typed error instead fixed
// the write but not the assertion, which still passed unconditionally. A
// guard that always passes is not a guard, so the wrapper is now two types
// and only the atomic one carries the method.
func TestTenantSharedKVStore_IsNotAtomicWithoutAtomicBacking(t *testing.T) {
	store := newTenantSharedStoreIface(t, &nonAtomicKVStore{KVStore: kvstore.NewMemoryKVStore()})

	if _, ok := store.(kvstore.AtomicKVStore); ok {
		t.Fatal("the wrapper must NOT satisfy kvstore.AtomicKVStore over a " +
			"non-atomic shared store; a construction-time guard depends on it")
	}
}

// TestTenantSharedKVStore_IsAtomicWithAtomicBacking is the positive control:
// the capability must be exposed when it genuinely exists, or the split above
// would just be a blanket removal.
func TestTenantSharedKVStore_IsAtomicWithAtomicBacking(t *testing.T) {
	store := newTenantSharedStoreIface(t, kvstore.NewMemoryKVStore())

	atomicStore, ok := store.(kvstore.AtomicKVStore)
	if !ok {
		t.Fatal("the wrapper must satisfy kvstore.AtomicKVStore over an atomic shared store")
	}
	stored, err := atomicStore.PutIfAbsent(context.Background(), "claim", []byte("v"))
	if err != nil {
		t.Fatalf("PutIfAbsent: %v", err)
	}
	if !stored {
		t.Error("the first PutIfAbsent must report stored=true")
	}
}

// TestTenantSharedKVStore_PutIfAbsentDelegatesToAtomicBacking proves the
// happy path still works — the refusal above is a capability check, not a
// blanket disabling of the method.
func TestTenantSharedKVStore_PutIfAbsentDelegatesToAtomicBacking(t *testing.T) {
	store := newTenantSharedStoreIface(t, kvstore.NewMemoryKVStore()).(kvstore.AtomicKVStore)
	ctx := context.Background()

	stored, err := store.PutIfAbsent(ctx, "claim", []byte("first"))
	if err != nil {
		t.Fatalf("PutIfAbsent: %v", err)
	}
	if !stored {
		t.Fatal("the first PutIfAbsent must report stored=true")
	}

	stored, err = store.PutIfAbsent(ctx, "claim", []byte("second"))
	if err != nil {
		t.Fatalf("PutIfAbsent (second): %v", err)
	}
	if stored {
		t.Error("the second PutIfAbsent must report stored=false")
	}

	got, err := store.Get(ctx, "claim")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "first" {
		t.Errorf("value = %q; want %q — the loser must not overwrite", got, "first")
	}
}

// TestTenantSharedKVStore_PutIfAbsentHasOneWinnerUnderRace exercises the
// property the typed refusal protects: when the backing store IS atomic,
// exactly one concurrent caller may win.
func TestTenantSharedKVStore_PutIfAbsentHasOneWinnerUnderRace(t *testing.T) {
	store := newTenantSharedStoreIface(t, kvstore.NewMemoryKVStore()).(kvstore.AtomicKVStore)
	ctx := context.Background()

	const writers = 24
	var winners atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			<-start
			if stored, err := store.PutIfAbsent(ctx, "claim", []byte("v")); err == nil && stored {
				winners.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := winners.Load(); got != 1 {
		t.Errorf("winners = %d; want exactly 1 — more than one means the claim is not a claim", got)
	}
}

// TestTenantSharedKVStore_IndexOperationsRefuse pins the #31 half for this
// store: it cannot index ciphertext, so every index method returns the typed
// error rather than an empty result that reads as "no matches".
func TestTenantSharedKVStore_IndexOperationsRefuse(t *testing.T) {
	store := newTenantSharedStore(t, kvstore.NewMemoryKVStore())
	ctx := context.Background()

	if err := store.RegisterEntityIndexes("member", []kvstore.EntityIndex{
		{FieldName: "Team", JSONFieldName: "team"},
	}); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("RegisterEntityIndexes = %v; want kvstore.ErrIndexUnsupported", err)
	}

	keys, err := store.QueryIndex(ctx, "member.Team", []byte("alpha"))
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("QueryIndex error = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if keys != nil {
		t.Errorf("QueryIndex returned %v alongside a refusal", keys)
	}

	entries, err := store.ScanIndex(ctx, "member.Team", []byte("a"), []byte("z"))
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("ScanIndex error = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if entries != nil {
		t.Errorf("ScanIndex returned %v alongside a refusal", entries)
	}

	if _, err := store.QueryCompoundIndex(ctx, "member.Team_Name", [][]byte{[]byte("a")}); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("QueryCompoundIndex error = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if _, err := store.ScanCompoundIndex(ctx, "member.Team_Name", [][]byte{[]byte("a")}, nil, nil); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("ScanCompoundIndex error = %v; want kvstore.ErrIndexUnsupported", err)
	}
}

// TestTenantSharedKVStore_ValuesAreCiphertextInTheSharedStore is the reason
// this store cannot index: the shared store holds only ciphertext, so nothing
// underneath it can read a field value to index. Pinning the property keeps
// the refusal's justification from silently becoming false.
func TestTenantSharedKVStore_ValuesAreCiphertextInTheSharedStore(t *testing.T) {
	shared := kvstore.NewMemoryKVStore()
	store := newTenantSharedStore(t, shared)
	ctx := context.Background()

	plaintext := []byte(`{"team":"alpha"}`)
	if err := store.Put(ctx, "member/1", plaintext); err != nil {
		t.Fatalf("Put: %v", err)
	}

	keys, err := shared.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("shared store holds %d keys; want 1", len(keys))
	}
	raw, err := shared.Get(ctx, keys[0])
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(raw) == string(plaintext) {
		t.Error("the shared store holds plaintext; the tenant DEK encryption is not applied")
	}

	// It still round-trips through the wrapper.
	got, err := store.Get(ctx, "member/1")
	if err != nil {
		t.Fatalf("Get through wrapper: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("round-trip = %q; want %q", got, plaintext)
	}
}
