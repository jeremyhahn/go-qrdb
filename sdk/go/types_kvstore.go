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

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/kvstore"

// ---------------------------------------------------------------------------
// KVStore types (core interface is already in dao.go as KVStore)
// ---------------------------------------------------------------------------

// AtomicKVStore is a KVStore that additionally offers the three guarded
// writes — PutIfAbsent, CompareAndSwap and CompareAndDelete — whose outcomes
// are defined under concurrency.
//
// They are a set, not three conveniences. A guarded create and a guarded
// delete with no guarded EDIT between them leaves the transition in the middle
// decided nowhere, and composing a swap from the other two leaves the key
// briefly ABSENT — so readers see "not found" for a live record, and a stalled
// contender can take it as a fresh create (go-qrdb#41, go-qrdb#56).
//
// It is discovered by type assertion, so a consumer that needs exactly-once
// semantics asserts for it at construction and fails closed:
//
//	atomic, ok := store.(sdk.AtomicKVStore)
//	if !ok {
//	    return sdk.ErrKVAtomicWriteUnsupported
//	}
//
// Without this alias that assertion could only be written against
// pkg/kvstore, which consumers may not import.
type AtomicKVStore = kvstore.AtomicKVStore

// FencedCreator is the optional capability of creating a key only while it
// AND a set of other keys -- its fences -- are all absent, decided as one
// atomic step. It exists for a create that another operation retires through
// a DIFFERENT key (see kvstore.FencedCreator for the tombstone example a
// plain PutIfAbsent cannot see).
//
// Like AtomicKVStore, it is discovered by type assertion against whatever
// kvstore.KVStore the embedder's own composition produced -- an engine's
// store, a barrier-backed store -- rather than something kvStoreAdapter
// forwards: no sdk/go transport.KVClient wire contract carries a
// fenced-create verb today, so a store reached through Client.KVStore()
// never satisfies this. An embedder holding a store directly (go-xkms's
// seal.StorageBackend composition, for one) asserts for it the same way:
//
//	fenced, ok := store.(sdk.FencedCreator)
type FencedCreator = kvstore.FencedCreator

// CompareAndSwapWithCurrent calls store.CompareAndSwap and, on a mismatch,
// reads the key back so the caller learns what it should have compared
// against without a second round trip -- useful for a contended counter (a
// PIN-lockout attempt count) that retries on every failed compare. See
// kvstore.CompareAndSwapWithCurrent for the window this does and does not
// close.
var CompareAndSwapWithCurrent = kvstore.CompareAndSwapWithCurrent

// NewMemoryKVStore returns an in-process KVStore that supports atomic
// conditional writes and maintains registered secondary indexes for real.
//
// It is the supported way to unit-test DAO code without booting a node.
// Consumers previously had to hand-write a twelve-method index-aware fake
// per repository, and a fake with subtly wrong index semantics produces
// tests that prove nothing. Single-process only: its atomicity is a mutex,
// not consensus.
var NewMemoryKVStore = kvstore.NewMemoryKVStore

// MemoryKVStore is the concrete type NewMemoryKVStore returns.
type MemoryKVStore = kvstore.MemoryKVStore

// UsableAsMapKey reports whether a KVStore value may be used as a map or
// sync.Map key.
//
// Ask it before caching anything keyed by a resolved store. Store identity is
// the natural cache key for per-tenant derived state — one DAO set per tenant's
// backing store — but KVStore is an interface and nothing requires its dynamic
// type to be comparable. A named slice or map type with value receivers
// satisfies the interface perfectly well, and using such a value as a key
// panics with "hash of unhashable type" on the FIRST operation, before any
// tenant work runs.
//
// A store that cannot be a key is not an error and must not be refused: the
// caller's choice of representation is its own. The cache is an optimization,
// so a non-comparable store skips it and gets freshly built state:
//
//	if !sdk.UsableAsMapKey(store) {
//	    return newDAOSet(store) // uncacheable, still correct
//	}
//
// Re-exported because consumers building tenant-routed stores on
// sdk.TenantStoreResolver need the identical judgement and may not import
// pkg/kvstore (docs/00 L3). Without it the choice is a local reimplementation
// of a predicate that must have exactly one definition, or shipping the cache
// unguarded — which is the panic (go-qrdb#55, and #47 for the two in-repo
// routing layers that did not make this check).
//
// A FUNC, not a `var f = kvstore.UsableAsMapKey` alias like the constructors
// above it. A package-level var is assignable by any importer, and this one is
// a safety predicate: reassigning it to return true process-wide hands the next
// consumer a map-backed store to use as a sync.Map key, which is the panic this
// exists to prevent — reintroduced from another package, and racily if it is
// reassigned while another goroutine reads it. "One canonical judgement" has to
// mean one nobody can replace.
func UsableAsMapKey(store KVStore) bool {
	return kvstore.UsableAsMapKey(store)
}

// ErrKVIndexUnsupported is returned by a store that cannot maintain or serve
// secondary indexes, from RegisterEntityIndexes and from every index query
// method. Test it with errors.Is.
//
// Such a store returns this INSTEAD of an empty result, because an empty
// result with a nil error is indistinguishable from a maintained index with
// no matches. A DAO built over such a store still answers correctly: pkg/dao
// detects the refusal at construction and plans full scans.
var ErrKVIndexUnsupported = kvstore.ErrIndexUnsupported

// ErrKVAtomicWriteUnsupported is returned by PutIfAbsent on a store that
// cannot perform a write whose outcome is defined under concurrency. A store
// reports this rather than degrading to Exists-then-Put, which would deliver
// the exact race the capability exists to eliminate while reporting success.
//
// It is the one marker for the condition across qrdb: the DAO's typed refusal
// and ErrInviteAtomicWriteUnsupported match it too. It also matches
// ErrKVNotApplied, since a capability refusal writes nothing.
var ErrKVAtomicWriteUnsupported = kvstore.ErrAtomicWriteUnsupported

// ErrKVNotApplied marks a conditional-write error that PROVES nothing was
// written — a refused capability, no leader to propose it. A caller holding it
// need not read back to reconcile; an error without it has an unknown outcome.
var ErrKVNotApplied = kvstore.ErrNotApplied

// EntityIndex defines a secondary index on a DAO entity field.
type EntityIndex = kvstore.EntityIndex

// EntityRegisterer allows entity types to register their secondary indexes.
type EntityRegisterer = kvstore.EntityRegisterer

// LookupFunc is a callback used for custom index lookups.
type LookupFunc = kvstore.LookupFunc

// ---------------------------------------------------------------------------
// KVStore error types
// ---------------------------------------------------------------------------

// KVSealedError is returned when an operation is attempted on a sealed KVStore.
type KVSealedError = kvstore.SealedError

// KVPutError is returned when a KVStore Put operation fails.
type KVPutError = kvstore.PutError

// KVGetError is returned when a KVStore Get operation fails.
type KVGetError = kvstore.GetError

// KVDeleteError is returned when a KVStore Delete operation fails.
type KVDeleteError = kvstore.DeleteError

// KVScanError is returned when a KVStore Scan operation fails.
type KVScanError = kvstore.ScanError

// KVListError is returned when a KVStore List operation fails.
type KVListError = kvstore.ListError

// KVExistsError is returned when a KVStore Exists operation fails.
type KVExistsError = kvstore.ExistsError

// KVIndexQueryError is returned when a KVStore index query fails.
type KVIndexQueryError = kvstore.IndexQueryError

// KVIndexScanError is returned when a KVStore index scan fails.
type KVIndexScanError = kvstore.IndexScanError

// KVIndexRegistrationError is returned when KVStore index registration fails.
type KVIndexRegistrationError = kvstore.IndexRegistrationError
