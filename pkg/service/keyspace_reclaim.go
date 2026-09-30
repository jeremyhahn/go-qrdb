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

package service

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Reclaiming a dropped database's keyspace (go-qrdb#412)
//
// Until this existed, DROP DATABASE reclaimed the database's directory and
// nothing else. That is the whole teardown for a database with a store of its
// own, and none of it for a database on the shared state machine -- the
// default, and the only shape docs/databases/architecture.md allows without a
// barrier. Its data lives in the shared store under the prefixes
// database_keys.go names, and the directory does not hold it. Dropping such a
// database orphaned every key it owned, permanently: the catalog record went
// first, so nothing in the system ever named that keyspace again, and a
// database recreated under the same name inherited all of it.
//
// This lives in the service layer rather than in pkg/server because it must
// exist in EVERY composition that can drop a database -- the daemon, the SDK's
// in-process node, an embedding -- and those do not all build a server. It is
// wired where the database manager and the data-plane services first meet
// (pkg/server/lifecycle.BuildServices), which is the one place common to all
// of them.
// ---------------------------------------------------------------------------

// The four verbs a reclamation needs. They are declared here, narrow, rather
// than taken as KVServicer/BatchServicer/IndexServicer/VectorServicer,
// because a teardown that accepted those interfaces would be asking for
// read-your-writes consistency, leases, entity encoding and vector search in
// order to delete some keys. Every production servicer satisfies these
// structurally at the wiring site, and a test double is four methods rather
// than forty.

// ReclaimKV enumerates and deletes keys.
//
// The enumeration is ListAll, NOT List, and the distinction is the whole
// reason this interface names a method rather than reusing the user-facing
// one. `List` applies the internal-key discovery guard, which returns an
// EMPTY list for the time-series prefixes a database owns -- their keys are
// internal and their prefixes are not inside a namespace the guard waives.
// A reclamation built on List therefore deleted nothing under them and
// reported success, which is the orphaning go-qrdb#412 exists to fix. A
// filtered implementation can no longer satisfy this interface by accident,
// because it does not have the method.
type ReclaimKV interface {
	// ListAll returns every key under prefix, including keys in the internal
	// namespace. See KVServicer.ListAll.
	ListAll(ctx context.Context, prefix string) ([]string, error)

	// Delete removes one key. Deleting an absent key is success.
	Delete(ctx context.Context, key string) error

	// Put writes one key. The teardown writes exactly one: the drop fence
	// (DropFenceKey), before it deletes anything.
	Put(ctx context.Context, key string, value []byte) error

	// Get reads one key; an absent key is *KeyNotFoundError.
	Get(ctx context.Context, key string) ([]byte, error)

	// PutIfAbsent, CompareAndSwap and CompareAndDelete are the guarded verbs
	// a create uses to claim the fence marker: from absent or from the
	// dropped tombstone, and never over another live incarnation.
	PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error)
	CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error)
	CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error)
}

// Compile-time check: the production KV engines are what the teardown is
// wired from, and a drift in either direction must fail here rather than at a
// wiring site in another package.
var (
	_ ReclaimKV = (*KVService)(nil)
	_ ReclaimKV = (*MVCCKVService)(nil)
	_ ReclaimKV = (*BackendKVService)(nil)
)

// ReclaimIndex enumerates and drops secondary index catalog entries.
type ReclaimIndex interface {
	// ListIndexes returns every index name in the catalog.
	ListIndexes(ctx context.Context) ([]string, error)

	// DropIndex removes one index by name.
	DropIndex(ctx context.Context, name string) error
}

// ReclaimVector enumerates and deletes vector namespaces.
type ReclaimVector interface {
	// ListNamespaces returns every vector namespace.
	ListNamespaces(ctx context.Context, req *ListNamespacesRequest) (*ListNamespacesResponse, error)

	// DeleteNamespace removes one namespace and its vectors.
	DeleteNamespace(ctx context.Context, req *DeleteNamespaceRequest) (*DeleteNamespaceResponse, error)
}

// ReclaimTarget is the set of engines a reclamation touches in one store.
//
// It is a struct rather than one interface because the three are independent
// surfaces of one store and a reclamation needs all of them: keys live in KV,
// secondary indexes live in the state machine's own index catalog, and
// vectors live in a per-node store of their own. A nil member is skipped, so
// a composition that does not serve vectors does not have to fake one.
type ReclaimTarget struct {
	// KV enumerates and deletes the store's keys.
	KV ReclaimKV

	// Index drops entries from the secondary index catalog, which is a
	// namespace of its own and is NOT reached by deleting keys.
	Index ReclaimIndex

	// Vector deletes vector namespaces, which live in their own per-node
	// store and are likewise not reached by deleting keys.
	Vector ReclaimVector
}

// StoreResolver answers which store holds a tenant's composite-namespaced
// keys, for the one isolation mode where that is not the system store: a
// volume-isolated tenant's databases share a dedicated per-tenant Host.
//
// It is a seam because the answer is a pkg/server concern (TenantHostRegistry,
// tenant isolation records) that does not exist yet when
// lifecycle.BuildServices wires the reclaim service. A composition that
// serves no volume-isolated tenants never sets one.
type StoreResolver interface {
	// ReclaimTarget returns the store holding tenantID's namespaced keys.
	// Returning false means "the system store", which is the answer for
	// prefix and barrier isolation, for the system tenant, and for a
	// database with a store of its own (whose composite prefix is unused
	// wherever it is scanned, making the scan a no-op).
	ReclaimTarget(ctx context.Context, tenantID string) (target ReclaimTarget, dedicated bool, err error)

	// FenceTarget returns the store holding the (tenantID, db) database's
	// data, for its drop fence. It is database-scoped where ReclaimTarget is
	// tenant-scoped because one more isolation mode matters here: a
	// dedicated-raft database's data is in its OWN shard, so that is where
	// the fence its writes are guarded by must be written.
	//
	// dedicated=false means "the system store". dedicated=true with a nil
	// store.KV means the database's own store does not exist (yet, or any
	// more): there is nothing in it to fence, and nothing to unfence. An
	// error is never answered with the system store: a fence written where
	// the database's writes are not guarded against it fences nothing.
	FenceTarget(ctx context.Context, tenantID, db string) (store FenceStore, dedicated bool, err error)
}

// FenceStore is the store a database's drop fence lives in.
type FenceStore struct {
	// KV is the store. nil when the database's own store does not exist.
	KV ReclaimKV

	// PerDatabase reports that the store belongs to this database alone --
	// a dedicated-raft database's shard. A database created again under the
	// same name gets a new store, so the store itself is the incarnation and
	// the fence marker carries none: it is absent while the database is live
	// (see DropFenceGuard).
	PerDatabase bool
}

// KeyspaceReclaimService reclaims the data a dropped database owns in a store
// it shares with other databases. It is the KeyspaceReclaimer
// database.Manager.Drop calls before it removes the database's catalog
// record.
type KeyspaceReclaimService struct {
	system   ReclaimTarget
	resolver StoreResolver
}

// Compile-time interface checks: the two teardown seams are the only
// consumers, and a drift in either signature must fail here rather than at a
// wiring site in another package.
//
// ONE type satisfies both on purpose. A database's keyspace and a tenant's
// keyspace are the same SHAPES of key -- namespaced ranges, time-series
// ranges, index-catalog names, vector namespaces -- over different scopes, so
// a second implementation of that walk would be a duplicate of this one that
// drifts the first time a fifth shape is added.
var (
	_ database.KeyspaceReclaimer = (*KeyspaceReclaimService)(nil)
	_ tenant.KeyspaceReclaimer   = (*KeyspaceReclaimService)(nil)
)

// NewKeyspaceReclaimService returns a reclaimer over the system store --
// the store that holds every shared-store database's namespaced keys and, for
// EVERY database regardless of isolation, its collection catalog and
// documents.
func NewKeyspaceReclaimService(system ReclaimTarget) *KeyspaceReclaimService {
	return &KeyspaceReclaimService{system: system}
}

// SetStoreResolver installs the resolver that routes a volume-isolated
// tenant's namespaced keys to its dedicated per-tenant Host.
//
// It is set after construction for the same ordering reason
// database.Manager.SetBarrierManager exists: the reclaim service is wired
// where the manager and the data plane meet, which is before the server that
// owns the tenant Host registry exists. Without one, a volume-isolated
// tenant's databases are scanned in the system store, where they have no
// keys, and their data is left behind.
//
// Thread-safety: call it before serving drop requests.
func (s *KeyspaceReclaimService) SetStoreResolver(r StoreResolver) {
	s.resolver = r
}

// ReclaimDatabaseKeyspace deletes everything the (tenantID, database) pair
// owns in the store serving it.
//
// The order within a single reclamation does not matter -- every step removes
// data nothing else names -- but the order RELATIVE to the catalog record
// does, and that is database.Manager.Drop's business: this runs first, and
// the record that makes the database resolvable is removed last.
//
// # Everything it deletes is scoped to ONE tenant
//
// Every prefix and every index name below is rooted at the composite
// "<tenant>/<db>/" namespace, so a drop cannot reach across tenants. That is a
// property to keep rather than an accident: this call is reachable by any
// tenant that owns a database, and the store it runs against is shared.
//
// The PRE-tenant-namespace collection layout is therefore NOT reclaimed here.
// Those keys are "__col:{db}:…" and "__sys:collections/{db}/…" -- database in
// the key, tenant nowhere -- so a reclamation of them scoped by database alone
// deletes every tenant's residue for a database of that name, and there is no
// scoping available that would not. go-qrdb#441 settled what happens to them:
// the server cannot attribute data it cannot attribute, so they are left in
// place, inert, and an operator upgrading a deployment that has them exports
// them with the previous binary and re-imports them per tenant. Deleting them
// here destroyed the only migration path the project documents, for tenants
// that had not asked for anything (go-qrdb#481). See
// LegacyCollectionKeyPrefixes and docs/collections/engineering.md.
//
// Collections created since that change are namespaced like everything else
// and ARE reclaimed, by the very first prefix below.
//
// It is idempotent. A keyspace that is already empty is the success case, not
// a not-found error, which is what lets a re-issued drop finish an
// interrupted one.
func (s *KeyspaceReclaimService) ReclaimDatabaseKeyspace(ctx context.Context, tenantID, db string) error {
	if db == "" {
		return &ReclaimScopeError{TenantID: tenantID, Database: db}
	}

	// The database's own composite keyspace, in the store that serves it.
	// The namespace is ALWAYS a non-empty "<tenant>/<db>/"
	// (tenant.ComposeNamespace), so a bug that lost the scope deletes nothing
	// rather than everything.
	target, err := s.namespacedTarget(ctx, tenantID)
	if err != nil {
		return err
	}
	ns := tenant.ComposeNamespace(tenantID, db)
	return s.reclaim(ctx, target, NamespacedKeyPrefixes(ns), DatabaseIndexNamePrefixes(ns), ns)
}

// FenceDatabaseKeyspace fences the (tenantID, db) database in the store
// holding its data: its fence marker (DropFenceKey) is set to the dropped
// tombstone, which no incarnation's guard accepts. See the drop-fence
// section of drop_fence.go: once it returns, every write to the database
// that had not applied is refused by the state machine, in the same command
// as the write.
//
// A store that cannot be resolved FAILS the fence rather than falling back
// to the system store: a fence written where the database's writes are not
// guarded against it fences nothing.
func (s *KeyspaceReclaimService) FenceDatabaseKeyspace(ctx context.Context, tenantID, db string) error {
	if db == "" {
		return &ReclaimScopeError{TenantID: tenantID, Database: db}
	}
	store, err := s.fenceTarget(ctx, tenantID, db)
	if err != nil {
		return err
	}
	if store.KV == nil {
		return &DropFenceError{TenantID: tenantID, Database: db, Op: "write", Err: &FenceStoreMissingError{TenantID: tenantID, Database: db}}
	}
	if err := store.KV.Put(ctx, DropFenceKey(tenantID, db), dropFenceTombstone); err != nil {
		return &DropFenceError{TenantID: tenantID, Database: db, Op: "write", Err: err}
	}
	return nil
}

// UnfenceDatabaseKeyspace makes incarnation the live incarnation of the
// (tenantID, db) database in the store holding its data: the one whose
// writes the store accepts. Create calls it with a new incarnation, before
// the new database is resolvable -- which is what refuses every handle of
// an earlier incarnation of the name -- and a failed drop calls it with the
// database's own incarnation to roll the fence back.
//
// An empty incarnation -- a database created before incarnations existed,
// or with no store to mark -- and a store that belongs to the database alone
// both mean "live is marker-absent", so the marker is removed. A database
// whose own store does not exist has nothing to mark, which is the case a
// brand-new dedicated-raft database is in when it is created.
//
// The marker is only ever claimed from ABSENT or from the dropped
// TOMBSTONE, with compare-and-swap, never overwritten. A marker holding
// another live incarnation is a database of this name created on another
// node -- the catalog is node-local, the marker is replicated -- and taking
// it over would refuse every write to that database. That is refused with
// *database.AlreadyExistsError (Elsewhere set). Marking the incarnation the
// marker already holds is success, so a retried create or rollback is
// idempotent.
func (s *KeyspaceReclaimService) UnfenceDatabaseKeyspace(ctx context.Context, tenantID, db, incarnation string) error {
	if db == "" {
		return &ReclaimScopeError{TenantID: tenantID, Database: db}
	}
	store, err := s.fenceTarget(ctx, tenantID, db)
	if err != nil || store.KV == nil {
		return err
	}
	key := DropFenceKey(tenantID, db)
	if incarnation == "" || store.PerDatabase {
		return clearMarker(ctx, store.KV, tenantID, db, key)
	}
	return claimMarker(ctx, store.KV, tenantID, db, key, []byte(incarnation))
}

// markerClaimAttempts bounds the claim loop. Each attempt either decides or
// observes a marker that changed between two of its reads; a marker cannot
// keep changing without another create or drop of the same name racing it.
const markerClaimAttempts = 3

// claimMarker sets key to live, from absent or from the tombstone only.
func claimMarker(ctx context.Context, kv ReclaimKV, tenantID, db, key string, live []byte) error {
	for attempt := 0; attempt < markerClaimAttempts; attempt++ {
		stored, err := kv.PutIfAbsent(ctx, key, live)
		if err != nil {
			return &DropFenceError{TenantID: tenantID, Database: db, Op: "mark live", Err: err}
		}
		if stored {
			return nil
		}
		swapped, err := kv.CompareAndSwap(ctx, key, dropFenceTombstone, live)
		if err != nil {
			return &DropFenceError{TenantID: tenantID, Database: db, Op: "mark live", Err: err}
		}
		if swapped {
			return nil
		}
		current, present, err := readMarker(ctx, kv, key)
		if err != nil {
			return &DropFenceError{TenantID: tenantID, Database: db, Op: "mark live", Err: err}
		}
		if !present {
			continue // removed between our reads: claim it again
		}
		if bytes.Equal(current, live) {
			return nil
		}
		if !bytes.Equal(current, dropFenceTombstone) {
			return &database.AlreadyExistsError{Name: db, Elsewhere: true}
		}
	}
	return &DropFenceError{TenantID: tenantID, Database: db, Op: "mark live", Err: &MarkerContendedError{Key: key}}
}

// clearMarker removes key when it holds the tombstone; an absent marker is
// already clear. A marker holding a live incarnation is another database of
// this name and is left alone.
func clearMarker(ctx context.Context, kv ReclaimKV, tenantID, db, key string) error {
	for attempt := 0; attempt < markerClaimAttempts; attempt++ {
		deleted, err := kv.CompareAndDelete(ctx, key, dropFenceTombstone)
		if err != nil {
			return &DropFenceError{TenantID: tenantID, Database: db, Op: "remove", Err: err}
		}
		if deleted {
			return nil
		}
		current, present, err := readMarker(ctx, kv, key)
		if err != nil {
			return &DropFenceError{TenantID: tenantID, Database: db, Op: "remove", Err: err}
		}
		if !present {
			return nil
		}
		if !bytes.Equal(current, dropFenceTombstone) {
			return &database.AlreadyExistsError{Name: db, Elsewhere: true}
		}
	}
	return &DropFenceError{TenantID: tenantID, Database: db, Op: "remove", Err: &MarkerContendedError{Key: key}}
}

// readMarker reads key, reporting an absent key as present=false.
func readMarker(ctx context.Context, kv ReclaimKV, key string) (value []byte, present bool, err error) {
	v, err := kv.Get(ctx, key)
	if err != nil {
		var notFound *KeyNotFoundError
		if errors.As(err, &notFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return v, true, nil
}

// fenceTarget returns the store holding (tenantID, db)'s data: the system
// store unless the resolver names a dedicated one.
func (s *KeyspaceReclaimService) fenceTarget(ctx context.Context, tenantID, db string) (FenceStore, error) {
	if s.resolver == nil {
		return FenceStore{KV: s.system.KV}, nil
	}
	store, dedicated, err := s.resolver.FenceTarget(ctx, tenantID, db)
	if err != nil {
		return FenceStore{}, &ReclaimStoreUnavailableError{TenantID: tenantID, Err: err}
	}
	if !dedicated {
		return FenceStore{KV: s.system.KV}, nil
	}
	return store, nil
}

// ReclaimTenantKeyspace deletes everything tenantID owns in the system store:
// every key under its "<tenant>/" namespace, the three time-series ranges
// that carry the namespace inside the series prefix, its placement range,
// every secondary-index catalog entry named under it, and every vector
// namespace beneath it.
//
// The scope is the TENANT segment, which is the parent of every
// "<tenant>/<db>/" namespace its databases use and also the root of its
// identity records, so one range covers both planes. In practice a tenant
// reaching here has no databases left -- DeleteTenant refuses while it does
// -- so what this removes is the identity residue. The wider scope is kept
// anyway because the database catalog is node-local while the keyspace is
// replicated: a node whose catalog under-reports would otherwise leave a
// tenant's data behind with nothing left to name it.
//
// It runs against the SYSTEM store, with no StoreResolver hop, which is the
// one place this differs from ReclaimDatabaseKeyspace. A prefix or barrier
// tenant keeps everything there. A volume or raft tenant keeps its IAM there
// too -- encrypted under its own DEK, because identity must be resolvable on
// every replica (see seal.NewTenantSharedKVStore) -- while its data-plane
// keys are on a dedicated store that DeleteTenant has already destroyed
// wholesale, key material and directory together. Routing there would be
// asking a store that no longer exists to delete keys that are already gone.
//
// The SYSTEM tenant is refused. Its namespace is "system/", which holds the
// bootstrap database and every system identity record, so a reclamation that
// accepted it would empty the store.
//
// It is idempotent. A keyspace that is already empty is the success case, not
// a not-found error, which is what lets a re-issued delete finish an
// interrupted one.
func (s *KeyspaceReclaimService) ReclaimTenantKeyspace(ctx context.Context, tenantID string) error {
	prefixes := TenantKeyPrefixes(tenantID)
	if len(prefixes) == 0 {
		return &ReclaimScopeError{TenantID: tenantID}
	}
	return s.reclaim(ctx, s.system, prefixes,
		TenantIndexNamePrefixes(tenantID), tenant.IdentityNamespace(tenantID))
}

// namespacedTarget returns the store holding tenantID's composite-namespaced
// keys.
//
// With no resolver wired, or a resolver that reports the tenant is not on a
// store of its own, that is the system store. A resolver error FAILS the
// reclamation rather than falling back: scanning the system store for a
// tenant whose keys are on a dedicated Host would report a successful drop
// over data that is still there, which is the defect itself.
func (s *KeyspaceReclaimService) namespacedTarget(ctx context.Context, tenantID string) (ReclaimTarget, error) {
	if s.resolver == nil {
		return s.system, nil
	}
	target, dedicated, err := s.resolver.ReclaimTarget(ctx, tenantID)
	if err != nil {
		return ReclaimTarget{}, &ReclaimStoreUnavailableError{TenantID: tenantID, Err: err}
	}
	if !dedicated {
		return s.system, nil
	}
	return target, nil
}

// reclaim deletes every key under keyPrefixes, drops every secondary index
// named under indexPrefixes, and -- when vectorNS is non-empty -- deletes
// every vector namespace under it, all from one store.
func (s *KeyspaceReclaimService) reclaim(ctx context.Context, t ReclaimTarget, keyPrefixes, indexPrefixes []string, vectorNS string) error {
	if err := reclaimKeys(ctx, t, keyPrefixes); err != nil {
		return err
	}
	if err := reclaimIndexes(ctx, t.Index, indexPrefixes); err != nil {
		return err
	}
	return reclaimVectorNamespaces(ctx, t.Vector, vectorNS)
}

// reclaimKeys deletes every key under each prefix, one key at a time.
//
// ONE AT A TIME IS DELIBERATE, and it is the honest cost of this fix rather
// than an oversight. Two faster shapes were tried and neither is available:
//
//   - A range delete would make each prefix a single proposal. No engine in
//     this repository has one; adding it is the right eventual fix and is
//     filed separately.
//   - BatchServicer.Execute would make it one proposal per chunk, and it
//     works -- on an MVCC shard. It encodes its deletes as protocol
//     OpMVCCDelete, which the KV engine behind a standalone node rejects with
//     "unsupported operation", and nothing in the service set says which
//     engine is underneath. A teardown that succeeded on one deployment and
//     failed on another would be a worse defect than a slow one, and guessing
//     then retrying on failure would be guessing.
//
// So the cost is O(keys) proposals, which a very large database pays as
// latency on the drop call. That is a stated limitation, not a hidden one.
//
// A prefix that matches nothing is success, which is what makes a re-issued
// drop able to finish an interrupted one.
func reclaimKeys(ctx context.Context, t ReclaimTarget, prefixes []string) error {
	if t.KV == nil || len(prefixes) == 0 {
		return nil
	}
	for _, prefix := range prefixes {
		if prefix == "" {
			// Unreachable through the constructors in database_keys.go, which
			// refuse an empty scope. Asserted here so a future caller that
			// builds its own list cannot turn a teardown into a store wipe.
			return &ReclaimScopeError{}
		}
		keys, err := t.KV.ListAll(ctx, prefix)
		if err != nil {
			return &ReclaimPrefixError{Prefix: prefix, Op: "list", Err: err}
		}
		for _, k := range keys {
			if err := t.KV.Delete(ctx, k); err != nil {
				return &ReclaimPrefixError{Prefix: prefix, Op: "delete", Err: err}
			}
		}
	}
	return nil
}

// reclaimIndexes drops every secondary index whose name begins with one of
// the given prefixes.
//
// The index catalog is a namespace of its own inside the state machine, not a
// range of KV keys, so the prefix deletes above do not reach it. An index
// left behind points at documents that no longer exist and collides with an
// index of the same name in a database later created under the same name --
// the defect go-qrdb#399 fixed one level down, at collection drop.
func reclaimIndexes(ctx context.Context, idx ReclaimIndex, prefixes []string) error {
	if idx == nil || len(prefixes) == 0 {
		return nil
	}
	names, err := idx.ListIndexes(ctx)
	if err != nil {
		return &ReclaimIndexError{Op: "list", Err: err}
	}
	for _, name := range names {
		if !hasAnyPrefix(name, prefixes) {
			continue
		}
		if err := idx.DropIndex(ctx, name); err != nil {
			return &ReclaimIndexError{Name: name, Op: "drop", Err: err}
		}
	}
	return nil
}

// reclaimVectorNamespaces deletes every vector namespace under ns. Vectors
// live in their own per-node store rather than the state machine's keyspace,
// so like the index catalog they survive a key prefix delete.
func reclaimVectorNamespaces(ctx context.Context, vec ReclaimVector, ns string) error {
	if vec == nil || ns == "" {
		return nil
	}
	resp, err := vec.ListNamespaces(ctx, &ListNamespacesRequest{})
	if err != nil {
		return &ReclaimVectorError{Op: "list", Err: err}
	}
	if resp == nil {
		return nil
	}
	for _, info := range resp.Namespaces {
		if !strings.HasPrefix(info.Namespace, ns) {
			continue
		}
		if _, err := vec.DeleteNamespace(ctx, &DeleteNamespaceRequest{Namespace: info.Namespace}); err != nil {
			return &ReclaimVectorError{Namespace: info.Namespace, Op: "delete", Err: err}
		}
	}
	return nil
}

// hasAnyPrefix reports whether s begins with any of the given prefixes.
func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
