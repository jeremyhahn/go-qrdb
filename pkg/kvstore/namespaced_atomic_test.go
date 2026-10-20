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

package kvstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// atomicOnlyStore exposes exactly AtomicKVStore's method set, hiding the
// guarded-batch capability MemoryKVStore also has, so NewNamespacedKVStore
// builds the atomic-only wrapper.
type atomicOnlyStore struct {
	AtomicKVStore
}

// nonComparableStore is a KVStore whose dynamic value is not comparable.
type nonComparableStore struct {
	KVStore
	tags []string
}

func newAtomicOnlyNamespace(t *testing.T) (KVStore, *MemoryKVStore) {
	t.Helper()
	inner := NewMemoryKVStore()
	ns, err := NewNamespacedKVStore(atomicOnlyStore{AtomicKVStore: inner}, "acme/system/")
	require.NoError(t, err)
	_, guarded := ns.(GuardedBatchKVStore)
	require.False(t, guarded, "an atomic-only inner must not yield a guarded wrapper")
	return ns, inner
}

func TestNamespacedKVStore_AtomicOnlyCompareAndSwapIsConfined(t *testing.T) {
	ns, inner := newAtomicOnlyNamespace(t)
	ctx := context.Background()
	store := ns.(AtomicKVStore)
	require.NoError(t, ns.Put(ctx, "k", []byte("v1")))

	swapped, err := store.CompareAndSwap(ctx, "k", []byte("wrong"), []byte("v2"))
	require.NoError(t, err)
	require.False(t, swapped, "a stale expectation must not swap")

	swapped, err = store.CompareAndSwap(ctx, "k", []byte("v1"), []byte("v2"))
	require.NoError(t, err)
	require.True(t, swapped)

	got, err := inner.Get(ctx, "acme/system/k")
	require.NoError(t, err)
	require.Equal(t, []byte("v2"), got, "the swap must land on the confined key")
	got, err = inner.Get(ctx, "k")
	require.NoError(t, err)
	require.Nil(t, got, "the swap must not touch the unconfined key")
}

func TestNamespacedKVStore_AtomicOnlyCompareAndDeleteIsConfined(t *testing.T) {
	ns, inner := newAtomicOnlyNamespace(t)
	ctx := context.Background()
	store := ns.(AtomicKVStore)
	require.NoError(t, inner.Put(ctx, "k", []byte("v1")))
	require.NoError(t, ns.Put(ctx, "k", []byte("v1")))

	deleted, err := store.CompareAndDelete(ctx, "k", []byte("wrong"))
	require.NoError(t, err)
	require.False(t, deleted, "a stale expectation must not delete")

	deleted, err = store.CompareAndDelete(ctx, "k", []byte("v1"))
	require.NoError(t, err)
	require.True(t, deleted)

	exists, err := inner.Exists(ctx, "acme/system/k")
	require.NoError(t, err)
	require.False(t, exists, "the confined key must be gone")
	exists, err = inner.Exists(ctx, "k")
	require.NoError(t, err)
	require.True(t, exists, "the unconfined key must survive")
}

func TestNamespacedKVStore_AtomicOnlyPropagatesCancellation(t *testing.T) {
	ns, _ := newAtomicOnlyNamespace(t)
	store := ns.(AtomicKVStore)
	require.NoError(t, ns.Put(context.Background(), "k", []byte("v1")))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	swapped, err := store.CompareAndSwap(ctx, "k", []byte("v1"), []byte("v2"))
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, swapped)

	deleted, err := store.CompareAndDelete(ctx, "k", []byte("v1"))
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, deleted)
}

func TestNamespacedKVStore_NamespaceReportsThePrefix(t *testing.T) {
	ns, err := NewNamespacedKVStore(&nonAtomicStore{KVStore: NewMemoryKVStore()}, "acme/system/")
	require.NoError(t, err)
	named, ok := ns.(interface{ Namespace() string })
	require.True(t, ok)
	require.Equal(t, "acme/system/", named.Namespace())
}

func TestUsableAsMapKey_ComparableStores(t *testing.T) {
	require.True(t, UsableAsMapKey(NewMemoryKVStore()))
	ns, err := NewNamespacedKVStore(NewMemoryKVStore(), "acme/")
	require.NoError(t, err)
	require.True(t, UsableAsMapKey(ns))
}

func TestUsableAsMapKey_RefusesNilAndNonComparable(t *testing.T) {
	require.False(t, UsableAsMapKey(nil))
	require.False(t, UsableAsMapKey(nonComparableStore{KVStore: NewMemoryKVStore(), tags: []string{"a"}}))
}
