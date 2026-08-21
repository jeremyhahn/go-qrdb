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
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// twoNamespaces returns two confined views of ONE physical store, which is
// the situation this type exists for: several tenants sharing the system
// store.
func twoNamespaces(t *testing.T) (KVStore, KVStore, *MemoryKVStore) {
	t.Helper()
	inner := NewMemoryKVStore()
	a, err := NewNamespacedKVStore(inner, "acme/system/")
	require.NoError(t, err)
	b, err := NewNamespacedKVStore(inner, "globex/system/")
	require.NoError(t, err)
	return a, b, inner
}

func TestNamespacedKVStore_RejectsAnEmptyPrefix(t *testing.T) {
	var empty *EmptyNamespaceError
	_, err := NewNamespacedKVStore(NewMemoryKVStore(), "")
	require.ErrorAs(t, err, &empty,
		"an empty prefix would hand the caller the entire store root, which is "+
			"the unconfined access this type exists to prevent")
}

func TestNamespacedKVStore_RejectsANilInnerStore(t *testing.T) {
	var nilInner *NilInnerStoreError
	_, err := NewNamespacedKVStore(nil, "acme/system/")
	require.ErrorAs(t, err, &nilInner)
}

// TestNamespacedKVStore_ReadsAndWritesAreConfined is the core property: two
// namespaces over one store cannot see each other's records, on every read
// path a caller has.
func TestNamespacedKVStore_ReadsAndWritesAreConfined(t *testing.T) {
	ctx := context.Background()
	a, b, _ := twoNamespaces(t)

	require.NoError(t, a.Put(ctx, "workitem/1", []byte(`{"state":"ready"}`)))

	got, err := b.Get(ctx, "workitem/1")
	require.NoError(t, err)
	require.Nil(t, got, "the other namespace must not see the record")

	exists, err := b.Exists(ctx, "workitem/1")
	require.NoError(t, err)
	require.False(t, exists)

	keys, err := b.List(ctx, "")
	require.NoError(t, err)
	require.Empty(t, keys, "List must not cross the namespace")

	var scanned []string
	require.NoError(t, b.Scan(ctx, "", func(k string, _ []byte) error {
		scanned = append(scanned, k)
		return nil
	}))
	require.Empty(t, scanned, "Scan must not cross the namespace")

	// And the owning namespace still sees it, under its UNPREFIXED key.
	own, err := a.Get(ctx, "workitem/1")
	require.NoError(t, err)
	require.JSONEq(t, `{"state":"ready"}`, string(own))
}

// TestNamespacedKVStore_DeleteIsConfined pins that one namespace cannot delete
// another's record — the write-side half of confinement, which a read-only
// test would miss entirely.
func TestNamespacedKVStore_DeleteIsConfined(t *testing.T) {
	ctx := context.Background()
	a, b, _ := twoNamespaces(t)
	require.NoError(t, a.Put(ctx, "workitem/1", []byte(`{"state":"ready"}`)))

	require.NoError(t, b.Delete(ctx, "workitem/1"))

	got, err := a.Get(ctx, "workitem/1")
	require.NoError(t, err)
	require.NotNil(t, got, "the other namespace's Delete must not remove this record")
}

// TestNamespacedKVStore_KeysAreStrippedOnTheWayOut keeps the abstraction
// honest: a caller writes and reads its own flat keyspace and never sees the
// prefix, so an entity key returned by a scan can be fed straight back to Get.
func TestNamespacedKVStore_KeysAreStrippedOnTheWayOut(t *testing.T) {
	ctx := context.Background()
	a, _, inner := twoNamespaces(t)
	require.NoError(t, a.Put(ctx, "workitem/1", []byte(`{"state":"ready"}`)))

	keys, err := a.List(ctx, "workitem/")
	require.NoError(t, err)
	require.Equal(t, []string{"workitem/1"}, keys)

	// Round-trip: the returned key works as an input.
	got, err := a.Get(ctx, keys[0])
	require.NoError(t, err)
	require.NotNil(t, got)

	// Physically, it really is namespaced.
	raw, err := inner.List(ctx, "")
	require.NoError(t, err)
	require.Equal(t, []string{"acme/system/workitem/1"}, raw)
}

// TestNamespacedKVStore_IsAtomicOnlyOverAnAtomicInner mirrors the discipline
// used for the tenant shared store: AtomicKVStore is discovered by assertion,
// so a wrapper that always defined PutIfAbsent would make a caller's
// fail-closed guard pass unconditionally. A guard that always passes is not a
// guard.
func TestNamespacedKVStore_IsAtomicOnlyOverAnAtomicInner(t *testing.T) {
	atomicStore, err := NewNamespacedKVStore(NewMemoryKVStore(), "acme/system/")
	require.NoError(t, err)
	_, ok := atomicStore.(AtomicKVStore)
	require.True(t, ok, "must expose the capability when the inner store has it")

	plain, err := NewNamespacedKVStore(&nonAtomicStore{KVStore: NewMemoryKVStore()}, "acme/system/")
	require.NoError(t, err)
	_, ok = plain.(AtomicKVStore)
	require.False(t, ok, "must NOT expose the capability over a non-atomic inner store")
}

// nonAtomicStore hides the inner store's PutIfAbsent by embedding the
// interface rather than the concrete type.
type nonAtomicStore struct{ KVStore }

// TestNamespacedKVStore_PutIfAbsentIsConfinedAndAtomic covers the claim path:
// two namespaces can each claim the SAME logical key, and within one namespace
// exactly one concurrent caller wins.
func TestNamespacedKVStore_PutIfAbsentIsConfinedAndAtomic(t *testing.T) {
	ctx := context.Background()
	a, b, _ := twoNamespaces(t)

	stored, err := a.(AtomicKVStore).PutIfAbsent(ctx, "claim", []byte("a"))
	require.NoError(t, err)
	require.True(t, stored)

	// The same logical key in another namespace is a DIFFERENT claim.
	stored, err = b.(AtomicKVStore).PutIfAbsent(ctx, "claim", []byte("b"))
	require.NoError(t, err)
	require.True(t, stored, "a claim in one namespace must not consume another's")

	const writers = 16
	var winners atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			<-start
			if ok, err := a.(AtomicKVStore).PutIfAbsent(ctx, "race", []byte("v")); err == nil && ok {
				winners.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	require.Equal(t, int64(1), winners.Load(), "exactly one claimant may win")
}

// TestNamespacedKVStore_IndexesAreConfined is the index half of the isolation
// property, over an inner store that CAN honour a namespace-qualified index
// name. Both tenants deliberately index the same value, so an unqualified
// index would return both and the count assertion catches it.
func TestNamespacedKVStore_IndexesAreConfined(t *testing.T) {
	ctx := context.Background()
	a, b, _ := twoNamespaces(t)
	defs := []EntityIndex{{FieldName: "State", JSONFieldName: "state", CaseSensitive: true}}

	require.NoError(t, a.RegisterEntityIndexes("workitem", defs))
	require.NoError(t, b.RegisterEntityIndexes("workitem", defs))

	require.NoError(t, a.Put(ctx, "workitem/1", []byte(`{"state":"ready"}`)))
	require.NoError(t, a.Put(ctx, "workitem/2", []byte(`{"state":"ready"}`)))
	require.NoError(t, b.Put(ctx, "workitem/9", []byte(`{"state":"ready"}`)))

	aKeys, err := a.QueryIndex(ctx, "workitem.State", []byte("ready"))
	require.NoError(t, err)
	require.Len(t, aKeys, 2, "namespace acme must see exactly its own two rows")
	for _, k := range aKeys {
		require.NotContains(t, string(k), "acme/system/",
			"returned entity keys must have the namespace stripped")
		require.NotEqual(t, "workitem/9", string(k), "acme's index returned globex's row")
	}

	bKeys, err := b.QueryIndex(ctx, "workitem.State", []byte("ready"))
	require.NoError(t, err)
	require.Len(t, bKeys, 1)
	require.Equal(t, "workitem/9", string(bKeys[0]))
}

func TestNamespacedKVStore_IndexScanIsConfined(t *testing.T) {
	ctx := context.Background()
	a, b, _ := twoNamespaces(t)
	defs := []EntityIndex{{FieldName: "State", JSONFieldName: "state", CaseSensitive: true}}
	require.NoError(t, a.RegisterEntityIndexes("workitem", defs))
	require.NoError(t, b.RegisterEntityIndexes("workitem", defs))

	require.NoError(t, a.Put(ctx, "workitem/1", []byte(`{"state":"ready"}`)))
	require.NoError(t, b.Put(ctx, "workitem/9", []byte(`{"state":"ready"}`)))

	entries, err := a.ScanIndex(ctx, "workitem.State", []byte("a"), []byte("z"))
	require.NoError(t, err)
	total := 0
	for _, keys := range entries {
		for _, k := range keys {
			total++
			require.Equal(t, "workitem/1", string(k))
		}
	}
	require.Equal(t, 1, total, "the range scan must not cross the namespace")
}

// refusingIndexInner accepts data operations but refuses index REGISTRATION,
// standing in for lifecycle.kvStoreAdapter, whose engine cannot maintain an
// index under a namespace-qualified name.
//
// Its QUERY methods deliberately SUCCEED. Left inherited from MemoryKVStore
// they already refuse, so a test asserting the query is refused would pass
// whether or not the wrapper's singleUnsupported guard existed — proving
// nothing about the thing it covers. Making the inner query succeed forces
// the refusal to come from the wrapper. (The compound fake carries the same
// treatment for the same reason.)
type refusingIndexInner struct{ *MemoryKVStore }

func (refusingIndexInner) RegisterEntityIndexes(string, []EntityIndex) error {
	return ErrIndexUnsupported
}

// QueryIndex succeeds, so a refusal can only come from the wrapper.
func (refusingIndexInner) QueryIndex(context.Context, string, []byte) ([][]byte, error) {
	return [][]byte{[]byte("acme/system/workitem/inner")}, nil
}

// ScanIndex succeeds, for the same reason.
func (refusingIndexInner) ScanIndex(context.Context, string, []byte, []byte) (map[string][][]byte, error) {
	return map[string][][]byte{"ready": {[]byte("acme/system/workitem/inner")}}, nil
}

// TestNamespacedKVStore_RefusesQueriesAfterRegistrationRefused is the contract
// that keeps the #32 guarantee intact through this wrapper.
//
// When the inner store cannot maintain a namespace-qualified index, sending a
// query down anyway surfaced a raw engine error out of a KVStore method
// ("engine: invalid query: index not found: acme/system/workitem.State") —
// untestable by callers, and not the typed refusal the contract promises. The
// wrapper records the refusal and answers subsequent queries with
// ErrIndexUnsupported, which is what lets pkg/dao plan a correct full scan.
func TestNamespacedKVStore_RefusesQueriesAfterRegistrationRefused(t *testing.T) {
	ctx := context.Background()
	store, err := NewNamespacedKVStore(refusingIndexInner{MemoryKVStore: NewMemoryKVStore()}, "acme/system/")
	require.NoError(t, err)

	err = store.RegisterEntityIndexes("workitem",
		[]EntityIndex{{FieldName: "State", JSONFieldName: "state", CaseSensitive: true}})
	require.ErrorIs(t, err, ErrIndexUnsupported)

	keys, err := store.QueryIndex(ctx, "workitem.State", []byte("ready"))
	require.ErrorIs(t, err, ErrIndexUnsupported,
		"after a refused registration the query must refuse with the SAME typed "+
			"error — and because the inner store's query SUCCEEDS, that refusal "+
			"can only be the wrapper's own guard")
	require.Empty(t, keys)

	entries, serr := store.ScanIndex(ctx, "workitem.State", []byte("a"), []byte("z"))
	require.ErrorIs(t, serr, ErrIndexUnsupported)
	require.Empty(t, entries)

	_, cerr := store.QueryCompoundIndex(ctx, "workitem.State_Title", [][]byte{[]byte("ready")})
	require.ErrorIs(t, cerr, ErrIndexUnsupported)

	_, scerr := store.ScanCompoundIndex(ctx, "workitem.State_Title", [][]byte{[]byte("ready")}, nil, nil)
	require.ErrorIs(t, scerr, ErrIndexUnsupported)
}

// TestNamespacedKVStore_DropsForeignKeysFromIndexResults guards the last line
// of defence. If an inner store ever returned a key outside this namespace,
// passing it through stripped would hand the caller another tenant's record;
// it is dropped instead.
func TestNamespacedKVStore_DropsForeignKeysFromIndexResults(t *testing.T) {
	inner := NewMemoryKVStore()
	store, err := NewNamespacedKVStore(inner, "acme/system/")
	require.NoError(t, err)
	ns, ok := store.(*atomicNamespacedKVStore)
	require.True(t, ok)

	got := ns.stripKeys([][]byte{
		[]byte("acme/system/workitem/1"),
		[]byte("globex/system/workitem/9"),
		[]byte("workitem/legacy"),
	})
	require.Len(t, got, 1, "only keys inside the namespace may survive")
	require.Equal(t, "workitem/1", string(got[0]))
}

func TestNamespacedKVStore_ContextCancellationPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a, _, _ := twoNamespaces(t)

	require.ErrorIs(t, a.Put(ctx, "workitem/1", []byte("v")), context.Canceled)
	_, err := a.Get(ctx, "workitem/1")
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, a.Delete(ctx, "workitem/1"), context.Canceled)
	_, err = a.List(ctx, "")
	require.ErrorIs(t, err, context.Canceled)
}

func TestNamespacedKVStore_ScanPropagatesCallbackError(t *testing.T) {
	ctx := context.Background()
	a, _, _ := twoNamespaces(t)
	require.NoError(t, a.Put(ctx, "workitem/1", []byte(`{"state":"ready"}`)))

	sentinel := errors.New("stop")
	require.ErrorIs(t, a.Scan(ctx, "", func(string, []byte) error { return sentinel }), sentinel)
}

// compoundOnlyRefusingInner serves single-field indexes but refuses compound
// REGISTRATION — the shape kvstore.MemoryKVStore itself has, and the shape
// that made a single indexesUnsupported flag wrong.
//
// Its compound QUERY methods deliberately SUCCEED. Without that, the fake
// would inherit MemoryKVStore's compound methods, which already refuse, and a
// test asserting a compound query is refused would pass whether or not the
// wrapper's own guard existed — proving nothing about the flag it is meant to
// cover. Making the inner query succeed is what forces the refusal to come
// from the wrapper.
type compoundOnlyRefusingInner struct{ *MemoryKVStore }

func (c compoundOnlyRefusingInner) RegisterEntityIndexes(entityType string, indexes []EntityIndex) error {
	for _, ei := range indexes {
		if ei.IsCompound() {
			return ErrIndexUnsupported
		}
	}
	return c.MemoryKVStore.RegisterEntityIndexes(entityType, indexes)
}

// QueryCompoundIndex succeeds, so a refusal can only come from the wrapper.
func (compoundOnlyRefusingInner) QueryCompoundIndex(context.Context, string, [][]byte) ([][]byte, error) {
	return [][]byte{[]byte("acme/system/workitem/inner")}, nil
}

// ScanCompoundIndex succeeds, for the same reason.
func (compoundOnlyRefusingInner) ScanCompoundIndex(context.Context, string, [][]byte, []byte, []byte) (map[string][][]byte, error) {
	return map[string][][]byte{"ready": {[]byte("acme/system/workitem/inner")}}, nil
}

// TestNamespacedKVStore_CompoundRefusalDoesNotPoisonSingleFieldQueries covers a
// review finding. The wrapper tracked ONE indexesUnsupported bit, so a compound
// registration failing marked the whole store unsupported and every later
// single-field QueryIndex refused — even though the inner store was serving
// single-field indexes perfectly well. pkg/dao already tracks the two kinds
// separately; the wrapper collapsed them.
func TestNamespacedKVStore_CompoundRefusalDoesNotPoisonSingleFieldQueries(t *testing.T) {
	ctx := context.Background()
	store, err := NewNamespacedKVStore(
		compoundOnlyRefusingInner{MemoryKVStore: NewMemoryKVStore()}, "acme/system/")
	require.NoError(t, err)

	// Single-field registration succeeds.
	require.NoError(t, store.RegisterEntityIndexes("workitem",
		[]EntityIndex{{FieldName: "State", JSONFieldName: "state", CaseSensitive: true}}))

	// Compound registration is refused — and must not affect the above.
	require.ErrorIs(t, store.RegisterEntityIndexes("workitem", []EntityIndex{{
		FieldNames: []string{"State", "Title"}, JSONFieldNames: []string{"state", "title"},
		CaseSensitive: true,
	}}), ErrIndexUnsupported)

	require.NoError(t, store.Put(ctx, "workitem/1", []byte(`{"state":"ready"}`)))

	keys, err := store.QueryIndex(ctx, "workitem.State", []byte("ready"))
	require.NoError(t, err,
		"a compound refusal must not poison single-field queries the store can serve")
	require.Len(t, keys, 1)

	// The compound path itself still refuses — and because the inner store's
	// compound query SUCCEEDS, that refusal can only be the wrapper's own
	// guard. Deleting compoundUnsupported.Store(true) or the guard makes this
	// return the inner store's result instead of an error.
	got, cerr := store.QueryCompoundIndex(ctx, "workitem.State_Title", [][]byte{[]byte("ready")})
	require.ErrorIs(t, cerr, ErrIndexUnsupported,
		"the wrapper must refuse a compound query whose registration it saw refused, "+
			"even though the inner store would happily answer")
	require.Empty(t, got)

	scanned, serr := store.ScanCompoundIndex(ctx, "workitem.State_Title",
		[][]byte{[]byte("ready")}, nil, nil)
	require.ErrorIs(t, serr, ErrIndexUnsupported)
	require.Empty(t, scanned)
}

// TestNamespacedKVStore_CapabilityFlagIsRaceFree covers the other review
// finding: the flag is written during registration and read by queries, and the
// resolver shares one wrapper from a sync.Map, so the two can run concurrently.
// Under -race a plain bool fails here.
func TestNamespacedKVStore_CapabilityFlagIsRaceFree(t *testing.T) {
	ctx := context.Background()
	store, err := NewNamespacedKVStore(
		compoundOnlyRefusingInner{MemoryKVStore: NewMemoryKVStore()}, "acme/system/")
	require.NoError(t, err)

	defs := []EntityIndex{{FieldName: "State", JSONFieldName: "state", CaseSensitive: true}}
	compound := []EntityIndex{{
		FieldNames: []string{"State", "Title"}, JSONFieldNames: []string{"state", "title"},
		CaseSensitive: true,
	}}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); _ = store.RegisterEntityIndexes("workitem", defs) }()
	go func() { defer wg.Done(); _ = store.RegisterEntityIndexes("workitem", compound) }()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_, _ = store.QueryIndex(ctx, "workitem.State", []byte("ready"))
			_, _ = store.QueryCompoundIndex(ctx, "workitem.State_Title", [][]byte{[]byte("ready")})
		}
	}()
	wg.Wait()
}

// TestNamespacedKVStore_ReservedPrefixPassesThrough is the load-bearing half
// of the #41 ruling (option A, 2026-08-19).
//

// TestNamespacedKVStore_ReservedPrefixIsSharedAcrossNamespaces states the
// registered known limitation as an executable fact rather than a comment.
//

// TestNamespacedKVStore_ReservedPrefixIsConfinedToo is the design decision
// this type settled on: there is NO exemption. Every key is confined,
// including the reserved "__sys:" prefix.
//
// An earlier draft exempted it so IAM records kept their absolute locations,
// motivated by upgrade compatibility. Pre-release that is a non-goal, and the
// exemption was the wrong design on its own merits — it was a SECOND tenancy
// mechanism alongside the namespace, keyed on a string prefix, and it opened
// three escapes that this assertion closes by construction.
func TestNamespacedKVStore_ReservedPrefixIsConfinedToo(t *testing.T) {
	ctx := context.Background()
	a, b, inner := twoNamespaces(t)

	require.NoError(t, a.Put(ctx, ReservedPrefixSystem+"users/id/1", []byte("acme-admin")))

	// Physically namespaced, like any other key.
	raw, err := inner.Get(ctx, "acme/system/"+ReservedPrefixSystem+"users/id/1")
	require.NoError(t, err)
	require.Equal(t, "acme-admin", string(raw))

	// Invisible to the other tenant — the escape the exemption left open.
	crossed, err := b.Get(ctx, ReservedPrefixSystem+"users/id/1")
	require.NoError(t, err)
	require.Nil(t, crossed,
		"a reserved-prefix key must NOT be globally visible: one tenant's IAM "+
			"and permission records are not another's")

	// Each namespace may hold its own record at the same logical key.
	require.NoError(t, b.Put(ctx, ReservedPrefixSystem+"users/id/1", []byte("globex-admin")))
	own, err := a.Get(ctx, ReservedPrefixSystem+"users/id/1")
	require.NoError(t, err)
	require.Equal(t, "acme-admin", string(own))
}

// TestNamespacedKVStore_ReservedPrefixTenantIDCannotEscape covers the escape a
// reviewer found in the exempted design: an unknown tenant id of "__sys:"
// produced the namespace "__sys:/system/", so ordinary DAO keys landed under
// the reserved prefix and became global. With no exemption the prefix is inert
// — it is just characters in a namespace like any other.
func TestNamespacedKVStore_ReservedPrefixTenantIDCannotEscape(t *testing.T) {
	ctx := context.Background()
	inner := NewMemoryKVStore()
	sneaky, err := NewNamespacedKVStore(inner, ReservedPrefixSystem+"/system/")
	require.NoError(t, err)
	victim, err := NewNamespacedKVStore(inner, "acme/system/")
	require.NoError(t, err)

	require.NoError(t, victim.Put(ctx, ReservedPrefixSystem+"users/id/1", []byte("secret")))

	got, err := sneaky.Get(ctx, ReservedPrefixSystem+"users/id/1")
	require.NoError(t, err)
	require.Nil(t, got, "a reserved-looking namespace must not reach another tenant's records")
}

// TestNamespacedKVStore_ReservedPrefixEntityTypeIsConfined covers the second
// reviewer-found escape: a DAO whose entityType began with "__sys:" wrote
// records that the exemption routed globally. Confinement makes the entity
// type irrelevant to routing.
func TestNamespacedKVStore_ReservedPrefixEntityTypeIsConfined(t *testing.T) {
	ctx := context.Background()
	a, b, _ := twoNamespaces(t)

	require.NoError(t, a.Put(ctx, ReservedPrefixSystem+"sneaky/1", []byte("acme")))

	got, err := b.Get(ctx, ReservedPrefixSystem+"sneaky/1")
	require.NoError(t, err)
	require.Nil(t, got,
		"a DAO entity type under the reserved prefix must not become global storage")
}

// TestNamespacedKVStore_ReservedPrefixScanIsConfined pins the enumeration
// path: a reserved-prefix scan sees only this namespace's records, with the
// namespace stripped like any other.
func TestNamespacedKVStore_ReservedPrefixScanIsConfined(t *testing.T) {
	ctx := context.Background()
	a, b, _ := twoNamespaces(t)
	require.NoError(t, a.Put(ctx, ReservedPrefixSystem+"users/id/1", []byte("acme")))
	require.NoError(t, b.Put(ctx, ReservedPrefixSystem+"users/id/9", []byte("globex")))

	keys, err := a.List(ctx, ReservedPrefixSystem+"users/")
	require.NoError(t, err)
	require.Equal(t, []string{ReservedPrefixSystem + "users/id/1"}, keys,
		"keys come back logical and confined, and round-trip as inputs")

	got, err := a.Get(ctx, keys[0])
	require.NoError(t, err)
	require.Equal(t, "acme", string(got))
}
