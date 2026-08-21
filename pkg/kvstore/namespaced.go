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

// Compile-time interface checks. Only the atomic variant carries PutIfAbsent,
// so the AtomicKVStore assertion answers honestly over a non-atomic inner
// store — the same discipline seal.NewTenantSharedKVStore uses.
var (
	_ KVStore       = (*namespacedKVStore)(nil)
	_ AtomicKVStore = (*atomicNamespacedKVStore)(nil)
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
// The returned store implements AtomicKVStore only when inner does.
func NewNamespacedKVStore(inner KVStore, prefix string) (KVStore, error) {
	if inner == nil {
		return nil, &NilInnerStoreError{}
	}
	if prefix == "" {
		return nil, &EmptyNamespaceError{}
	}
	base := &namespacedKVStore{inner: inner, prefix: prefix}
	if atomicInner, ok := inner.(AtomicKVStore); ok {
		return &atomicNamespacedKVStore{namespacedKVStore: base, inner: atomicInner}, nil
	}
	return base, nil
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
