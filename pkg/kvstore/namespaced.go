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
	"strings"
	"sync/atomic"
)

// Compile-time interface checks. Only the capability-carrying variants
// implement AtomicKVStore / GuardedBatchKVStore, so the type assertion a
// caller performs after NewNamespacedKVStore answers honestly over a
// non-capable inner store — the same discipline seal.NewTenantSharedKVStore
// uses.
var (
	_ KVStore                  = (*namespacedKVStore)(nil)
	_ AtomicKVStore            = (*atomicNamespacedKVStore)(nil)
	_ GuardedBatchKVStore      = (*guardedNamespacedKVStore)(nil)
	_ AtomicKVStore            = (*guardedAtomicNamespacedKVStore)(nil)
	_ GuardedBatchKVStore      = (*guardedAtomicNamespacedKVStore)(nil)
	_ MultiGuardedBatchKVStore = (*multiGuardedNamespacedKVStore)(nil)
	_ AtomicKVStore            = (*multiGuardedAtomicNamespacedKVStore)(nil)
	_ MultiGuardedBatchKVStore = (*multiGuardedAtomicNamespacedKVStore)(nil)
)

// NewNamespacedKVStore returns a KVStore that confines every operation to a
// key namespace, so callers holding different namespaces over one physical
// store cannot see each other's records.
//
// It exists because a store handed out per tenant must be isolated BY
// CONSTRUCTION rather than by every caller remembering to prefix its keys. A
// dao.GenericDAO keys entities as "{entityType}/{id}" and has no tenant
// concept at all, so without this wrapper two tenants' DAOs over one shared
// store occupy a single undifferentiated keyspace.
//
// prefix is used verbatim. Callers supply the scope they mean: the tenant
// resolver passes "{tenant}/", while the data plane's canonical
// "{tenant}/{db}/" comes from tenant.ComposeNamespace. An empty prefix is
// rejected: it would
// hand the caller the entire store root, which is the failure this type
// exists to prevent, and silently — so it is an error rather than a no-op
// wrapper.
//
// The returned store implements AtomicKVStore and/or GuardedBatchKVStore
// exactly when inner does — never more, so a type assertion after this call
// never advertises a capability inner cannot actually deliver, and never
// less, so a namespaced view over a capable store is not quietly downgraded
// to a plain one. go-qrdb#555's atomic role/user creation needs
// GuardedBatchKVStore to survive this wrapping: a boot-time composer's
// store is namespaced to the system identity namespace before role.NewStore
// or user.NewStore ever see it, so if the capability did not survive here,
// WithAtomicIdentity's construction-time check would refuse every boot-time
// composer unconditionally.
func NewNamespacedKVStore(inner KVStore, prefix string) (KVStore, error) {
	if inner == nil {
		return nil, &NilInnerStoreError{}
	}
	if prefix == "" {
		return nil, &EmptyNamespaceError{}
	}
	base := &namespacedKVStore{inner: inner, prefix: prefix}
	atomicInner, isAtomic := inner.(AtomicKVStore)
	multiInner, isMulti := inner.(MultiGuardedBatchKVStore)
	guardedInner, isGuarded := inner.(GuardedBatchKVStore)
	switch {
	case isAtomic && isMulti:
		ga := &guardedAtomicNamespacedKVStore{namespacedKVStore: base, atomicInner: atomicInner, guardedInner: multiInner}
		return &multiGuardedAtomicNamespacedKVStore{guardedAtomicNamespacedKVStore: ga, multiInner: multiInner}, nil
	case isAtomic && isGuarded:
		return &guardedAtomicNamespacedKVStore{namespacedKVStore: base, atomicInner: atomicInner, guardedInner: guardedInner}, nil
	case isMulti:
		g := &guardedNamespacedKVStore{namespacedKVStore: base, inner: multiInner}
		return &multiGuardedNamespacedKVStore{guardedNamespacedKVStore: g, multiInner: multiInner}, nil
	case isGuarded:
		return &guardedNamespacedKVStore{namespacedKVStore: base, inner: guardedInner}, nil
	case isAtomic:
		return &atomicNamespacedKVStore{namespacedKVStore: base, inner: atomicInner}, nil
	default:
		return base, nil
	}
}

// namespacedKVStore confines a KVStore to a key prefix.
type namespacedKVStore struct {
	inner  KVStore
	prefix string
	// singleUnsupported and compoundUnsupported record that the inner store
	// refused a namespace-qualified index registration, so this wrapper
	// refuses the matching QUERIES too rather than sending them to an index
	// that was never created.
	//
	// The two kinds are tracked SEPARATELY, mirroring pkg/dao: a store can
	// serve one and not the other — kvstore.MemoryKVStore maintains
	// single-field indexes and refuses compound ones — and a single flag let
	// a compound refusal poison every later single-field query.
	//
	// Atomic because the resolver shares one wrapper from a sync.Map: DAO
	// construction (which writes) can run concurrently with queries from
	// another goroutine (which read).
	singleUnsupported   atomic.Bool
	compoundUnsupported atomic.Bool
}

// route returns the physical key for a logical one. It always confines.
//
// EVERY key is confined, with no exemption. An earlier draft passed
// ReservedPrefixSystem ("__sys:") through unprefixed so IAM records kept their
// absolute locations; that was motivated by upgrade compatibility, which is a
// non-goal before public release, and it was the wrong design on its own
// merits. It created three distinct escapes, all of which vanish here:
//
//   - a tenant id like "__sys:" produced the namespace "__sys:/system/", so
//     ordinary DAO keys landed under the reserved prefix;
//   - a DAO whose entityType began with "__sys:" wrote global records;
//   - pkg/permission keys "__sys:perm_scope/{permission}" with NO tenant
//     segment, so the exemption made one tenant's permission scopes visible
//     to every other tenant.
//
// The confinement is now the SINGLE tenancy mechanism for a resolver-served
// store, which is the point: a bypass keyed on a string prefix is a second
// mechanism, and two mechanisms drift.
func (s *namespacedKVStore) route(key string) string {
	return s.prefix + key
}

// Namespace returns the prefix every key is confined to. It is exported for
// diagnostics and for callers that must reason about physical layout, such as
// a migration.
func (s *namespacedKVStore) Namespace() string { return s.prefix }

// Put stores value under the namespaced key.
func (s *namespacedKVStore) Put(ctx context.Context, key string, value []byte) error {
	return s.inner.Put(ctx, s.route(key), value)
}

// Get returns the value for the namespaced key, or (nil, nil) when absent.
func (s *namespacedKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	return s.inner.Get(ctx, s.route(key))
}

// Delete removes the namespaced key.
func (s *namespacedKVStore) Delete(ctx context.Context, key string) error {
	return s.inner.Delete(ctx, s.route(key))
}

// Scan iterates the namespace's entries under prefix, reporting keys with the
// namespace stripped so the caller sees its own flat keyspace.
func (s *namespacedKVStore) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	return s.inner.Scan(ctx, s.route(prefix), func(key string, value []byte) error {
		return fn(strings.TrimPrefix(key, s.prefix), value)
	})
}

// List returns the namespace's keys under prefix, namespace stripped.
func (s *namespacedKVStore) List(ctx context.Context, prefix string) ([]string, error) {
	keys, err := s.inner.List(ctx, s.route(prefix))
	if err != nil {
		return nil, err
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = strings.TrimPrefix(k, s.prefix)
	}
	return out, nil
}

// Exists reports whether the namespaced key is present.
func (s *namespacedKVStore) Exists(ctx context.Context, key string) (bool, error) {
	return s.inner.Exists(ctx, s.route(key))
}

// RegisterEntityIndexes registers the entity's indexes on the inner store
// under namespace-qualified names, so one namespace's index can never answer
// another's query.
func (s *namespacedKVStore) RegisterEntityIndexes(entityType string, indexes []EntityIndex) error {
	err := s.inner.RegisterEntityIndexes(entityType, s.qualify(entityType, indexes))
	if errors.Is(err, ErrIndexUnsupported) {
		// The inner store cannot maintain an index under a
		// namespace-qualified name. Remember it for the KIND that was
		// registered, so a later query of that kind is refused with the same
		// typed error instead of being sent to an index that does not exist —
		// which surfaces as an opaque engine "index not found" and breaks the
		// contract that a store either answers or refuses testably.
		//
		// Only the kind actually refused is marked. A batch is one kind:
		// pkg/dao registers single-field and compound definitions in separate
		// calls precisely because a store can serve one and not the other.
		if containsCompound(indexes) {
			s.compoundUnsupported.Store(true)
		} else {
			s.singleUnsupported.Store(true)
		}
	}
	return err
}

// containsCompound reports whether a registration batch is the compound kind.
func containsCompound(indexes []EntityIndex) bool {
	for _, ei := range indexes {
		if ei.IsCompound() {
			return true
		}
	}
	return false
}

// qualify rewrites each definition's resolved name to carry the namespace.
func (s *namespacedKVStore) qualify(entityType string, indexes []EntityIndex) []EntityIndex {
	out := make([]EntityIndex, len(indexes))
	for i, ei := range indexes {
		// Both halves must be namespaced. The NAME keeps one namespace's
		// index from answering another's query; the KEY PREFIX is what the
		// store matches writes against, and without it the store looks for
		// "{entityType}/" while the keys are "{prefix}{entityType}/" and the
		// index is created, never populated, and answers empty.
		ei.KeyPrefix = s.prefix + ei.ResolvedPrefix(entityType)
		ei.IndexName = s.prefix + ei.ResolvedName(entityType)
		out[i] = ei
	}
	return out
}

// QueryIndex performs an exact-match lookup against this namespace's index and
// returns the matching entity keys with the namespace stripped.
func (s *namespacedKVStore) QueryIndex(ctx context.Context, indexName string, value []byte) ([][]byte, error) {
	if s.singleUnsupported.Load() {
		return nil, ErrIndexUnsupported
	}
	keys, err := s.inner.QueryIndex(ctx, s.prefix+indexName, value)
	if err != nil {
		return nil, err
	}
	return s.stripKeys(keys), nil
}

// ScanIndex performs a range scan against this namespace's index.
func (s *namespacedKVStore) ScanIndex(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	if s.singleUnsupported.Load() {
		return nil, ErrIndexUnsupported
	}
	entries, err := s.inner.ScanIndex(ctx, s.prefix+indexName, start, end)
	if err != nil {
		return nil, err
	}
	return s.stripEntries(entries), nil
}

// QueryCompoundIndex performs an exact-match compound lookup in this namespace.
func (s *namespacedKVStore) QueryCompoundIndex(ctx context.Context, indexName string, values [][]byte) ([][]byte, error) {
	if s.compoundUnsupported.Load() {
		return nil, ErrIndexUnsupported
	}
	keys, err := s.inner.QueryCompoundIndex(ctx, s.prefix+indexName, values)
	if err != nil {
		return nil, err
	}
	return s.stripKeys(keys), nil
}

// ScanCompoundIndex performs a prefix + range compound scan in this namespace.
func (s *namespacedKVStore) ScanCompoundIndex(ctx context.Context, indexName string, prefixValues [][]byte, rangeStart, rangeEnd []byte) (map[string][][]byte, error) {
	if s.compoundUnsupported.Load() {
		return nil, ErrIndexUnsupported
	}
	entries, err := s.inner.ScanCompoundIndex(ctx, s.prefix+indexName, prefixValues, rangeStart, rangeEnd)
	if err != nil {
		return nil, err
	}
	return s.stripEntries(entries), nil
}

// stripKeys removes the namespace from each returned entity key. Keys outside
// the namespace are DROPPED rather than returned unchanged: a key that does
// not belong to this namespace has no business in its result, and passing it
// through stripped would hand the caller another namespace's record.
func (s *namespacedKVStore) stripKeys(keys [][]byte) [][]byte {
	out := make([][]byte, 0, len(keys))
	for _, k := range keys {
		if !strings.HasPrefix(string(k), s.prefix) {
			continue
		}
		out = append(out, []byte(strings.TrimPrefix(string(k), s.prefix)))
	}
	return out
}

// stripEntries applies stripKeys to every bucket of a scan result, dropping
// buckets left empty.
func (s *namespacedKVStore) stripEntries(entries map[string][][]byte) map[string][][]byte {
	out := make(map[string][][]byte, len(entries))
	for value, keys := range entries {
		if stripped := s.stripKeys(keys); len(stripped) > 0 {
			out[value] = stripped
		}
	}
	return out
}

// atomicNamespacedKVStore adds PutIfAbsent for an inner store that can
// actually perform it. It is a separate type so the AtomicKVStore assertion a
// caller performs at construction answers honestly.
type atomicNamespacedKVStore struct {
	*namespacedKVStore
	inner AtomicKVStore
}

// PutIfAbsent stores value under the namespaced key only when it is absent.
// The atomicity is the inner store's; this type adds only the confinement.
func (s *atomicNamespacedKVStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	return s.inner.PutIfAbsent(ctx, s.route(key), value)
}

// CompareAndDelete removes the namespaced key only when its stored value
// still equals expected. As with PutIfAbsent the atomicity is the inner
// store's; this type adds only the confinement, and route() confines the key
// exactly as it does for every other operation.
func (s *atomicNamespacedKVStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return s.inner.CompareAndDelete(ctx, s.route(key), expected)
}

// CompareAndSwap replaces the namespaced key's value only when its stored
// value still equals expected. As with the other two guarded operations the
// atomicity is the inner store's; this type only routes the key.
func (s *atomicNamespacedKVStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return s.inner.CompareAndSwap(ctx, s.route(key), expected, next)
}

// guardedNamespacedKVStore adds PutAllIfUnchanged for an inner store that
// implements GuardedBatchKVStore but NOT AtomicKVStore. It is a separate
// type from guardedAtomicNamespacedKVStore, for the same reason
// atomicNamespacedKVStore is separate from the plain namespacedKVStore: the
// GuardedBatchKVStore assertion a caller performs after NewNamespacedKVStore
// must answer honestly, which means the method exists on the type ONLY when
// the inner store can actually honour it.
//
// No production store in this repository currently implements
// GuardedBatchKVStore without ALSO implementing AtomicKVStore — every
// KVServicer-backed adapter answers PutIfAbsent unconditionally and adds
// PutAllIfUnchanged only when a transaction plane is wired — so this type is
// exercised by conformance tests rather than production wiring today. It
// exists so that ceases to be true without silently losing the guarded
// capability through this wrapper.
type guardedNamespacedKVStore struct {
	*namespacedKVStore
	inner GuardedBatchKVStore
}

// PutAllIfUnchanged applies every write, confined to the namespace, only
// while the namespaced guard key still holds guardValue. The atomicity is
// the inner store's; this type adds only the confinement, routing the guard
// key and every write's key exactly as route() does for every other
// operation.
func (s *guardedNamespacedKVStore) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []KV) (bool, error) {
	return s.inner.PutAllIfUnchanged(ctx, s.route(guardKey), guardValue, s.routeWrites(writes))
}

// guardedAtomicNamespacedKVStore composes namespacing with BOTH capability
// tiers for an inner store that implements both — true of every guarded
// adapter this codebase constructs today (see
// cmd/qrdbd/kvstore_adapter.go's guardedKVServiceAdapter and
// pkg/server/lifecycle's guarded kvStoreAdapter). It is a fourth,
// independent type rather than one wrapper embedding another: embedding
// atomicNamespacedKVStore inside guardedNamespacedKVStore (or vice versa)
// would embed *namespacedKVStore at two different depths through two
// different paths, and every KVStore method it does not itself override
// becomes an ambiguous selector — a compile error, not a subtle bug, but
// one this shape avoids needing to explain at every call site.
type guardedAtomicNamespacedKVStore struct {
	*namespacedKVStore
	atomicInner  AtomicKVStore
	guardedInner GuardedBatchKVStore
}

// PutIfAbsent stores value under the namespaced key only when it is absent.
func (s *guardedAtomicNamespacedKVStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	return s.atomicInner.PutIfAbsent(ctx, s.route(key), value)
}

// CompareAndDelete removes the namespaced key only when its stored value
// still equals expected.
func (s *guardedAtomicNamespacedKVStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return s.atomicInner.CompareAndDelete(ctx, s.route(key), expected)
}

// CompareAndSwap replaces the namespaced key's value only when its stored
// value still equals expected.
func (s *guardedAtomicNamespacedKVStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return s.atomicInner.CompareAndSwap(ctx, s.route(key), expected, next)
}

// PutAllIfUnchanged applies every write, confined to the namespace, only
// while the namespaced guard key still holds guardValue.
func (s *guardedAtomicNamespacedKVStore) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []KV) (bool, error) {
	return s.guardedInner.PutAllIfUnchanged(ctx, s.route(guardKey), guardValue, s.routeWrites(writes))
}

// routeGuards rewrites each guard's key to its namespaced form, exactly as
// route() does for every other key this package touches.
func (s *namespacedKVStore) routeGuards(guards []Guard) []Guard {
	routed := make([]Guard, len(guards))
	for i, g := range guards {
		routed[i] = Guard{Key: s.route(g.Key), Value: g.Value}
	}
	return routed
}

// routeWrites rewrites each write's key to its namespaced form.
func (s *namespacedKVStore) routeWrites(writes []KV) []KV {
	routed := make([]KV, len(writes))
	for i, w := range writes {
		routed[i] = KV{Key: s.route(w.Key), Value: w.Value}
	}
	return routed
}

// multiGuardedNamespacedKVStore adds PutAllIfAllUnchanged for an inner store
// that implements MultiGuardedBatchKVStore but NOT AtomicKVStore. It embeds
// guardedNamespacedKVStore — ONE level of embedding beyond namespacedKVStore,
// through a single path — so PutAllIfUnchanged and every plain KVStore method
// promote without the ambiguous-selector hazard guardedAtomicNamespacedKVStore's
// doc explains: that hazard comes from combining two SEPARATE wrappers that
// each embed *namespacedKVStore directly, not from wrapping one composite type
// again.
type multiGuardedNamespacedKVStore struct {
	*guardedNamespacedKVStore
	multiInner MultiGuardedBatchKVStore
}

// PutAllIfAllUnchanged applies every write, confined to the namespace, only
// while EVERY namespaced guard key still holds its expected value.
func (s *multiGuardedNamespacedKVStore) PutAllIfAllUnchanged(ctx context.Context, guards []Guard, writes []KV) (bool, error) {
	return s.multiInner.PutAllIfAllUnchanged(ctx, s.routeGuards(guards), s.routeWrites(writes))
}

// multiGuardedAtomicNamespacedKVStore composes namespacing with the atomic,
// single-guard, AND multi-guard capability tiers for an inner store that
// implements all three — true of every guarded adapter this codebase
// constructs today (cmd/qrdbd/kvstore_adapter.go's guardedKVServiceAdapter and
// pkg/server/lifecycle's guardedKVStoreAdapter). It embeds
// guardedAtomicNamespacedKVStore for the same single-path reason
// multiGuardedNamespacedKVStore embeds guardedNamespacedKVStore.
type multiGuardedAtomicNamespacedKVStore struct {
	*guardedAtomicNamespacedKVStore
	multiInner MultiGuardedBatchKVStore
}

// PutAllIfAllUnchanged applies every write, confined to the namespace, only
// while EVERY namespaced guard key still holds its expected value.
func (s *multiGuardedAtomicNamespacedKVStore) PutAllIfAllUnchanged(ctx context.Context, guards []Guard, writes []KV) (bool, error) {
	return s.multiInner.PutAllIfAllUnchanged(ctx, s.routeGuards(guards), s.routeWrites(writes))
}
