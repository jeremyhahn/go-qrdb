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

package kvstore

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeGuardedStore adds an in-memory PutAllIfUnchanged to a plain KVStore,
// for testing namespacedKVStore's guarded wrapper without a real
// transaction-backed adapter (cmd/qrdbd's guardedKVServiceAdapter and
// pkg/server/lifecycle's guarded kvStoreAdapter, both outside this
// package). It deliberately embeds the KVStore interface, not
// AtomicKVStore, so it exercises the GUARDED-ONLY branch of
// NewNamespacedKVStore's capability switch.
type fakeGuardedStore struct {
	KVStore
	mu sync.Mutex
}

func (f *fakeGuardedStore) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []KV) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, err := f.Get(ctx, guardKey)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(current, guardValue) {
		return false, nil
	}
	for _, w := range writes {
		if err := f.Put(ctx, w.Key, w.Value); err != nil {
			return false, err
		}
	}
	return true, nil
}

// fakeGuardedAtomicStore is fakeGuardedStore's sibling for the combined
// capability branch: it embeds AtomicKVStore (NewMemoryKVStore(), which
// already implements it) so BOTH the Atomic trio and PutAllIfUnchanged are
// present on one type, matching every production guarded adapter.
type fakeGuardedAtomicStore struct {
	AtomicKVStore
	mu sync.Mutex
}

func (f *fakeGuardedAtomicStore) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []KV) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, err := f.Get(ctx, guardKey)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(current, guardValue) {
		return false, nil
	}
	for _, w := range writes {
		if err := f.Put(ctx, w.Key, w.Value); err != nil {
			return false, err
		}
	}
	return true, nil
}

// TestNewNamespacedKVStore_GuardedOnlyCapability covers the isGuarded &&
// !isAtomic branch: the returned store implements GuardedBatchKVStore but
// not AtomicKVStore, matching the inner store exactly.
func TestNewNamespacedKVStore_GuardedOnlyCapability(t *testing.T) {
	inner := &fakeGuardedStore{KVStore: NewMemoryKVStore()}
	store, err := NewNamespacedKVStore(inner, "acme/system/")
	require.NoError(t, err)

	if _, ok := store.(AtomicKVStore); ok {
		t.Fatal("expected AtomicKVStore assertion to fail: inner does not implement it")
	}
	guarded, ok := store.(GuardedBatchKVStore)
	if !ok {
		t.Fatal("expected GuardedBatchKVStore assertion to succeed: inner implements it")
	}

	ctx := context.Background()
	applied, err := guarded.PutAllIfUnchanged(ctx, "guard", nil, []KV{
		{Key: "a", Value: []byte("1")},
		{Key: "b", Value: []byte("2")},
	})
	require.NoError(t, err)
	if !applied {
		t.Fatal("expected the batch to apply: guard key was absent")
	}

	// Every write, and the guard read, must have gone through the
	// namespace, not to the inner store's root.
	got, err := inner.Get(ctx, "acme/system/a")
	require.NoError(t, err)
	if string(got) != "1" {
		t.Errorf("inner key acme/system/a = %q, want %q", got, "1")
	}
	got, err = store.Get(ctx, "a")
	require.NoError(t, err)
	if string(got) != "1" {
		t.Errorf("namespaced Get(a) = %q, want %q", got, "1")
	}
	if raw, _ := inner.Get(ctx, "a"); raw != nil {
		t.Errorf("unnamespaced key %q exists on the inner store; the write escaped confinement", "a")
	}
}

// TestNewNamespacedKVStore_GuardedOnlyRefusesOnMismatch confirms the guard
// itself is evaluated inside the namespace: two different namespaces over
// one physical store must not see each other's guard key.
func TestNewNamespacedKVStore_GuardedOnlyRefusesOnMismatch(t *testing.T) {
	shared := &fakeGuardedStore{KVStore: NewMemoryKVStore()}
	a, err := NewNamespacedKVStore(shared, "acme/system/")
	require.NoError(t, err)
	b, err := NewNamespacedKVStore(shared, "globex/system/")
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, a.Put(ctx, "guard", []byte("acme-owns-it")))

	// b's guard key is a DIFFERENT physical key ("globex/system/guard"), so
	// it is absent from b's point of view even though a's "guard" exists.
	applied, err := b.(GuardedBatchKVStore).PutAllIfUnchanged(ctx, "guard", nil, []KV{{Key: "x", Value: []byte("1")}})
	require.NoError(t, err)
	if !applied {
		t.Fatal("expected b's batch to apply: b's own namespaced guard key was absent")
	}

	// a's guard key is unaffected by b's write.
	applied, err = a.(GuardedBatchKVStore).PutAllIfUnchanged(ctx, "guard", nil, []KV{{Key: "y", Value: []byte("2")}})
	require.NoError(t, err)
	if applied {
		t.Fatal("expected a's batch to be refused: a's own namespaced guard key was already set")
	}
}

// TestNewNamespacedKVStore_GuardedAtomicCapability covers the isGuarded &&
// isAtomic branch: BOTH capabilities are present and confined together,
// which is what every real boot-time composer's store looks like once
// go-qrdb#555's atomic role/user creation is wired in.
func TestNewNamespacedKVStore_GuardedAtomicCapability(t *testing.T) {
	inner := &fakeGuardedAtomicStore{AtomicKVStore: NewMemoryKVStore()}
	store, err := NewNamespacedKVStore(inner, "acme/system/")
	require.NoError(t, err)

	atomicStore, ok := store.(AtomicKVStore)
	if !ok {
		t.Fatal("expected AtomicKVStore assertion to succeed")
	}
	guarded, ok := store.(GuardedBatchKVStore)
	if !ok {
		t.Fatal("expected GuardedBatchKVStore assertion to succeed")
	}
	ctx := context.Background()

	stored, err := atomicStore.PutIfAbsent(ctx, "claim", []byte("v1"))
	require.NoError(t, err)
	if !stored {
		t.Fatal("expected PutIfAbsent to succeed on an absent key")
	}
	swapped, err := atomicStore.CompareAndSwap(ctx, "claim", []byte("v1"), []byte("v2"))
	require.NoError(t, err)
	if !swapped {
		t.Fatal("expected CompareAndSwap to succeed against the value just stored")
	}
	deleted, err := atomicStore.CompareAndDelete(ctx, "claim", []byte("v2"))
	require.NoError(t, err)
	if !deleted {
		t.Fatal("expected CompareAndDelete to succeed against the value just stored")
	}

	applied, err := guarded.PutAllIfUnchanged(ctx, "batch-guard", nil, []KV{
		{Key: "p", Value: []byte("primary")},
		{Key: "t", Value: []byte("tenant-index")},
	})
	require.NoError(t, err)
	if !applied {
		t.Fatal("expected the batch to apply: guard key was absent")
	}

	// Confinement: every key this test touched lives under the namespace on
	// the shared inner store, and nowhere else.
	for _, k := range []string{"acme/system/p", "acme/system/t"} {
		v, err := inner.Get(ctx, k)
		require.NoError(t, err)
		if v == nil {
			t.Errorf("expected inner key %q to exist", k)
		}
	}
	if v, _ := inner.Get(ctx, "claim"); v != nil {
		t.Error("expected the deleted claim to be gone from the inner store")
	}
}

// TestNewNamespacedKVStore_AtomicOnlyStillWorks is a regression guard for
// the pre-existing capability branch: an inner store that is Atomic but NOT
// Guarded must still get exactly the atomic-only wrapper, unaffected by the
// two new branches added alongside it.
func TestNewNamespacedKVStore_AtomicOnlyStillWorks(t *testing.T) {
	store, err := NewNamespacedKVStore(NewMemoryKVStore(), "acme/system/")
	require.NoError(t, err)
	if _, ok := store.(AtomicKVStore); !ok {
		t.Fatal("expected AtomicKVStore assertion to succeed: NewMemoryKVStore implements it")
	}
	if _, ok := store.(GuardedBatchKVStore); ok {
		t.Fatal("expected GuardedBatchKVStore assertion to fail: NewMemoryKVStore does not implement it")
	}
}

// fakeMultiGuardedStore adds an in-memory PutAllIfAllUnchanged (and, in terms
// of it, PutAllIfUnchanged) to a plain KVStore, for testing
// multiGuardedNamespacedKVStore without a real transaction-backed adapter.
// It deliberately embeds the KVStore interface, not AtomicKVStore, so it
// exercises the MULTI-GUARDED-ONLY branch of NewNamespacedKVStore's
// capability switch.
type fakeMultiGuardedStore struct {
	KVStore
	mu sync.Mutex
}

func (f *fakeMultiGuardedStore) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []KV) (bool, error) {
	return f.PutAllIfAllUnchanged(ctx, []Guard{{Key: guardKey, Value: guardValue}}, writes)
}

func (f *fakeMultiGuardedStore) PutAllIfAllUnchanged(ctx context.Context, guards []Guard, writes []KV) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, g := range guards {
		current, err := f.Get(ctx, g.Key)
		if err != nil {
			return false, err
		}
		if !bytes.Equal(current, g.Value) {
			return false, nil
		}
	}
	for _, w := range writes {
		if err := f.Put(ctx, w.Key, w.Value); err != nil {
			return false, err
		}
	}
	return true, nil
}

// fakeMultiGuardedAtomicStore is fakeMultiGuardedStore's sibling for the
// combined capability branch: it embeds AtomicKVStore so the Atomic trio,
// PutAllIfUnchanged, AND PutAllIfAllUnchanged are all present on one type,
// matching every production guarded adapter this codebase constructs today.
type fakeMultiGuardedAtomicStore struct {
	AtomicKVStore
	mu sync.Mutex
}

func (f *fakeMultiGuardedAtomicStore) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []KV) (bool, error) {
	return f.PutAllIfAllUnchanged(ctx, []Guard{{Key: guardKey, Value: guardValue}}, writes)
}

func (f *fakeMultiGuardedAtomicStore) PutAllIfAllUnchanged(ctx context.Context, guards []Guard, writes []KV) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, g := range guards {
		current, err := f.Get(ctx, g.Key)
		if err != nil {
			return false, err
		}
		if !bytes.Equal(current, g.Value) {
			return false, nil
		}
	}
	for _, w := range writes {
		if err := f.Put(ctx, w.Key, w.Value); err != nil {
			return false, err
		}
	}
	return true, nil
}

// TestNewNamespacedKVStore_MultiGuardedOnlyCapability covers the isMulti &&
// !isAtomic branch: the returned store implements MultiGuardedBatchKVStore
// (and, through it, GuardedBatchKVStore), confined to the namespace exactly
// like the single-guard wrapper.
func TestNewNamespacedKVStore_MultiGuardedOnlyCapability(t *testing.T) {
	inner := &fakeMultiGuardedStore{KVStore: NewMemoryKVStore()}
	store, err := NewNamespacedKVStore(inner, "acme/system/")
	require.NoError(t, err)

	if _, ok := store.(AtomicKVStore); ok {
		t.Fatal("expected AtomicKVStore assertion to fail: inner does not implement it")
	}
	multi, ok := store.(MultiGuardedBatchKVStore)
	if !ok {
		t.Fatal("expected MultiGuardedBatchKVStore assertion to succeed: inner implements it")
	}
	if _, ok := store.(GuardedBatchKVStore); !ok {
		t.Fatal("expected GuardedBatchKVStore assertion to also succeed: MultiGuardedBatchKVStore embeds it")
	}

	ctx := context.Background()
	applied, err := multi.PutAllIfAllUnchanged(ctx, []Guard{
		{Key: "claim"}, {Key: "username"},
	}, []KV{
		{Key: "a", Value: []byte("1")},
		{Key: "b", Value: []byte("2")},
	})
	require.NoError(t, err)
	if !applied {
		t.Fatal("expected the batch to apply: both guard keys were absent")
	}

	// Every write, and every guard read, must have gone through the
	// namespace, not to the inner store's root.
	got, err := inner.Get(ctx, "acme/system/a")
	require.NoError(t, err)
	if string(got) != "1" {
		t.Errorf("inner key acme/system/a = %q, want %q", got, "1")
	}
	if raw, _ := inner.Get(ctx, "a"); raw != nil {
		t.Errorf("unnamespaced key %q exists on the inner store; the write escaped confinement", "a")
	}

	// A second guard already holding a value refuses the whole batch, even
	// though the first guard is still absent.
	require.NoError(t, store.Put(ctx, "username", []byte("taken")))
	applied, err = multi.PutAllIfAllUnchanged(ctx, []Guard{
		{Key: "claim"}, {Key: "username"},
	}, []KV{{Key: "c", Value: []byte("3")}})
	require.NoError(t, err)
	if applied {
		t.Fatal("expected the batch to be refused: the username guard already held a value")
	}
	if v, _ := store.Get(ctx, "c"); v != nil {
		t.Error("expected no write from a refused multi-guard batch")
	}
}

// TestNewNamespacedKVStore_MultiGuardedAtomicCapability covers the isMulti &&
// isAtomic branch: all three capability tiers are present and confined
// together, matching every production guarded adapter (cmd/qrdbd's
// guardedKVServiceAdapter and pkg/server/lifecycle's guardedKVStoreAdapter)
// once go-qrdb#555's multi-guard user identity path is wired in.
func TestNewNamespacedKVStore_MultiGuardedAtomicCapability(t *testing.T) {
	inner := &fakeMultiGuardedAtomicStore{AtomicKVStore: NewMemoryKVStore()}
	store, err := NewNamespacedKVStore(inner, "acme/system/")
	require.NoError(t, err)

	atomicStore, ok := store.(AtomicKVStore)
	if !ok {
		t.Fatal("expected AtomicKVStore assertion to succeed")
	}
	multi, ok := store.(MultiGuardedBatchKVStore)
	if !ok {
		t.Fatal("expected MultiGuardedBatchKVStore assertion to succeed")
	}
	ctx := context.Background()

	stored, err := atomicStore.PutIfAbsent(ctx, "claim", []byte("v1"))
	require.NoError(t, err)
	if !stored {
		t.Fatal("expected PutIfAbsent to succeed on an absent key")
	}

	applied, err := multi.PutAllIfAllUnchanged(ctx, []Guard{
		{Key: "id-claim"}, {Key: "username"}, {Key: "email"},
	}, []KV{
		{Key: "p", Value: []byte("primary")},
		{Key: "t", Value: []byte("tenant-index")},
	})
	require.NoError(t, err)
	if !applied {
		t.Fatal("expected the batch to apply: every guard key was absent")
	}

	for _, k := range []string{"acme/system/p", "acme/system/t"} {
		v, err := inner.Get(ctx, k)
		require.NoError(t, err)
		if v == nil {
			t.Errorf("expected inner key %q to exist", k)
		}
	}
}
