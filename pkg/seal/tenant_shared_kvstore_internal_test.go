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
	"strings"
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
	if err := tb.Initialize(context.Background(), MustCredentials(context.Background(), []byte("pw"))); err != nil {
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
	if err := tb.Initialize(context.Background(), MustCredentials(context.Background(), []byte("pw"))); err != nil {
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

// ---------------------------------------------------------------------------
// The read/enumerate verbs, and the guarded writes
//
// Two properties run through all of them and neither is visible from a return
// value alone:
//
//   - the NAMESPACE is applied on the way down and stripped on the way back,
//     so a tenant addresses its own keyspace by logical name and never sees
//     another tenant's records;
//   - values cross the boundary as CIPHERTEXT, encrypted under this tenant's
//     DEK, so the shared replicated store beneath holds nothing readable.
//
// A wrapper that forgot the prefix would pass any test that only round-trips
// through itself -- it would write and read back its own unprefixed keys
// perfectly happily, while colliding with every other tenant in the shared
// store. These therefore seed a SECOND tenant's record directly and assert it
// is neither returned nor disturbed.
// ---------------------------------------------------------------------------

// seedForeignRecord writes a record belonging to a different tenant directly
// into the shared store, at the physical key that tenant's wrapper would use.
func seedForeignRecord(t *testing.T, shared kvstore.KVStore, key string) string {
	t.Helper()
	physical := tenantIAMSharedPrefix + "other-tenant/" + key
	if err := shared.Put(context.Background(), physical, []byte("not yours")); err != nil {
		t.Fatalf("seed foreign record: %v", err)
	}
	return physical
}

func TestTenantSharedKVStore_ListAndScanAreScopedAndDecrypted(t *testing.T) {
	ctx := context.Background()
	shared := kvstore.NewMemoryKVStore()
	s := newTenantSharedStore(t, shared)
	foreign := seedForeignRecord(t, shared, "users/1")

	for k, v := range map[string]string{"users/1": "alice", "users/2": "bob"} {
		if err := s.Put(ctx, k, []byte(v)); err != nil {
			t.Fatalf("Put %q: %v", k, err)
		}
	}

	keys, err := s.List(ctx, "users/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("List returned %d keys (%v), want 2: another tenant's records "+
			"must not be enumerable", len(keys), keys)
	}
	for _, k := range keys {
		if strings.HasPrefix(k, tenantIAMSharedPrefix) {
			t.Errorf("List returned a PHYSICAL key %q: the namespace must be stripped", k)
		}
	}

	seen := map[string]string{}
	if err := s.Scan(ctx, "users/", func(k string, v []byte) error {
		seen[k] = string(v)
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(seen) != 2 || seen["users/1"] != "alice" || seen["users/2"] != "bob" {
		t.Errorf("Scan = %v, want the two decrypted records keyed by logical name", seen)
	}

	// The foreign record is untouched and was never decrypted.
	got, err := shared.Get(ctx, foreign)
	if err != nil || string(got) != "not yours" {
		t.Errorf("the other tenant's record changed: (%q, %v)", got, err)
	}
}

func TestTenantSharedKVStore_ExistsAndDeleteAreNamespaced(t *testing.T) {
	ctx := context.Background()
	shared := kvstore.NewMemoryKVStore()
	s := newTenantSharedStore(t, shared)
	foreign := seedForeignRecord(t, shared, "token")

	if err := s.Put(ctx, "token", []byte("mine")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	ok, err := s.Exists(ctx, "token")
	if err != nil || !ok {
		t.Fatalf("Exists = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := s.Exists(ctx, "absent"); err != nil || ok {
		t.Errorf("Exists(absent) = (%v, %v), want (false, nil)", ok, err)
	}

	if err := s.Delete(ctx, "token"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ok, _ := s.Exists(ctx, "token"); ok {
		t.Error("the key survived Delete")
	}

	// Deleting this tenant's "token" must not have touched the other's.
	if got, err := shared.Get(ctx, foreign); err != nil || string(got) != "not yours" {
		t.Errorf("Delete crossed the namespace boundary: (%q, %v)", got, err)
	}
}

// TestTenantSharedKVStore_GuardedWritesCompareDecryptedAndGuardOnCiphertext
// covers the two guarded verbs on the atomic wrapper.
//
// Same split as StorageBarrier: the caller's expectation is compared as
// plaintext, while the guard handed to the shared store is the ciphertext just
// read. A success therefore proves both halves -- an implementation that
// re-encrypted `expected` to build the guard would decline every time, because
// the AEAD is nonce-randomised.
func TestTenantSharedKVStore_GuardedWritesCompareDecryptedAndGuardOnCiphertext(t *testing.T) {
	ctx := context.Background()

	t.Run("CompareAndSwap on a match", func(t *testing.T) {
		shared := kvstore.NewMemoryKVStore()
		s := newTenantSharedStoreIface(t, shared)
		atomic, ok := s.(kvstore.AtomicKVStore)
		if !ok {
			t.Fatal("a memory-backed shared store must yield an AtomicKVStore")
		}
		if err := s.Put(ctx, "k", []byte("old")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		swapped, err := atomic.CompareAndSwap(ctx, "k", []byte("old"), []byte("new"))
		if err != nil || !swapped {
			t.Fatalf("CompareAndSwap = (%v, %v), want (true, nil)", swapped, err)
		}
		got, gerr := s.Get(ctx, "k")
		if gerr != nil || string(got) != "new" {
			t.Errorf("value = (%q, %v), want %q", got, gerr, "new")
		}
	})

	t.Run("CompareAndDelete on a match", func(t *testing.T) {
		shared := kvstore.NewMemoryKVStore()
		s := newTenantSharedStoreIface(t, shared)
		atomic := s.(kvstore.AtomicKVStore)
		if err := s.Put(ctx, "k", []byte("v")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		deleted, err := atomic.CompareAndDelete(ctx, "k", []byte("v"))
		if err != nil || !deleted {
			t.Fatalf("CompareAndDelete = (%v, %v), want (true, nil)", deleted, err)
		}
		if ok, _ := s.Exists(ctx, "k"); ok {
			t.Error("the key survived a successful guarded delete")
		}
	})

	t.Run("both decline on a stale plaintext and change nothing", func(t *testing.T) {
		shared := kvstore.NewMemoryKVStore()
		s := newTenantSharedStoreIface(t, shared)
		atomic := s.(kvstore.AtomicKVStore)
		if err := s.Put(ctx, "k", []byte("actual")); err != nil {
			t.Fatalf("Put: %v", err)
		}

		if swapped, err := atomic.CompareAndSwap(ctx, "k", []byte("stale"), []byte("new")); err != nil || swapped {
			t.Errorf("CompareAndSwap = (%v, %v), want (false, nil)", swapped, err)
		}
		if deleted, err := atomic.CompareAndDelete(ctx, "k", []byte("stale")); err != nil || deleted {
			t.Errorf("CompareAndDelete = (%v, %v), want (false, nil)", deleted, err)
		}
		got, _ := s.Get(ctx, "k")
		if string(got) != "actual" {
			t.Errorf("a declined guarded write changed the value to %q", got)
		}
	})

	t.Run("absent is unchanged, and a swap does not create", func(t *testing.T) {
		shared := kvstore.NewMemoryKVStore()
		s := newTenantSharedStoreIface(t, shared)
		atomic := s.(kvstore.AtomicKVStore)

		if swapped, err := atomic.CompareAndSwap(ctx, "missing", []byte("v"), []byte("next")); err != nil || swapped {
			t.Errorf("CompareAndSwap = (%v, %v), want (false, nil)", swapped, err)
		}
		if ok, _ := s.Exists(ctx, "missing"); ok {
			t.Error("CompareAndSwap created an absent key")
		}
		if deleted, err := atomic.CompareAndDelete(ctx, "missing", []byte("v")); err != nil || deleted {
			t.Errorf("CompareAndDelete = (%v, %v), want (false, nil)", deleted, err)
		}
	})
}

// TestTenantSharedKVStore_GuardedWritesAreNamespaced: the guarded verbs must
// carry the prefix like every other verb. Without it, one tenant's
// compare-and-swap would evaluate against -- and overwrite -- another
// tenant's identically-named key whenever the plaintexts happened to match.
func TestTenantSharedKVStore_GuardedWritesAreNamespaced(t *testing.T) {
	ctx := context.Background()
	shared := kvstore.NewMemoryKVStore()
	s := newTenantSharedStoreIface(t, shared)
	atomic := s.(kvstore.AtomicKVStore)
	foreign := seedForeignRecord(t, shared, "k")

	if err := s.Put(ctx, "k", []byte("mine")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if swapped, err := atomic.CompareAndSwap(ctx, "k", []byte("mine"), []byte("updated")); err != nil || !swapped {
		t.Fatalf("CompareAndSwap = (%v, %v), want (true, nil)", swapped, err)
	}

	got, err := shared.Get(ctx, foreign)
	if err != nil || string(got) != "not yours" {
		t.Errorf("the other tenant's record was disturbed: (%q, %v)", got, err)
	}
}

// failingSharedKVStore fails every Get, standing in for a shared store that is
// unreachable rather than merely empty.
type failingSharedKVStore struct {
	kvstore.KVStore
	err error
}

func (s *failingSharedKVStore) Get(context.Context, string) ([]byte, error) { return nil, s.err }

// TestTenantSharedKVStore_GetDistinguishesAbsentFromBroken pins the contract
// the doc comment states: Get returns (nil, nil) for a key that does not
// exist, and propagates anything else.
//
// Collapsing the two would be a security-relevant mistake rather than a
// cosmetic one. This store backs tenant IAM: a caller that reads a role
// binding or a token and is told "absent" when the shared store is actually
// unreachable would proceed as though the tenant genuinely has no such
// grant -- turning an outage into a silent authorization answer.
func TestTenantSharedKVStore_GetDistinguishesAbsentFromBroken(t *testing.T) {
	ctx := context.Background()

	t.Run("absent is (nil, nil)", func(t *testing.T) {
		s := newTenantSharedStore(t, kvstore.NewMemoryKVStore())
		got, err := s.Get(ctx, "never-written")
		if err != nil {
			t.Fatalf("Get on an absent key = %v, want no error", err)
		}
		if got != nil {
			t.Errorf("Get on an absent key = %q, want nil", got)
		}
	})

	t.Run("a broken store propagates", func(t *testing.T) {
		boom := errors.New("shared store unreachable")
		s := newTenantSharedStore(t, &failingSharedKVStore{
			KVStore: kvstore.NewMemoryKVStore(), err: boom,
		})
		got, err := s.Get(ctx, "k")
		if !errors.Is(err, boom) {
			t.Errorf("Get = (%q, %v), want the store's error -- reporting this as "+
				"absent would turn an outage into a silent authorization answer", got, err)
		}
		if got != nil {
			t.Errorf("Get returned %q alongside an error", got)
		}
	})
}
