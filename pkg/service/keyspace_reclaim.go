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
	"context"
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
}

// KeyspaceReclaimService reclaims the data a dropped database owns in a store
// it shares with other databases. It is the KeyspaceReclaimer
// database.Manager.Drop calls before it removes the database's catalog
// record.
type KeyspaceReclaimService struct {
	system   ReclaimTarget
	resolver StoreResolver
}

// Compile-time interface check: the Manager's seam is the only consumer, and
// a drift in its signature must fail here rather than at a wiring site.
var _ database.KeyspaceReclaimer = (*KeyspaceReclaimService)(nil)

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
// It is idempotent. A keyspace that is already empty is the success case, not
// a not-found error, which is what lets a re-issued drop finish an
// interrupted one.
func (s *KeyspaceReclaimService) ReclaimDatabaseKeyspace(ctx context.Context, tenantID, db string) error {
	if db == "" {
		return &ReclaimScopeError{TenantID: tenantID, Database: db}
	}

	// 1. Collections, from the SYSTEM store, unconditionally. They are served
	//    by ONE server-wide CollectionServicer over the system shard's root KV
	//    rather than by a per-(tenant, database) view, so their keys carry the
	//    database as a key segment, carry no tenant, and sit in the system
	//    store even for a database whose own data-plane keys live elsewhere.
	//    A teardown that reclaimed only the composite namespace would leave
	//    every document behind. See database_keys.go.
	if err := s.reclaim(ctx, s.system, CollectionKeyPrefixes(db), DatabaseIndexNamePrefixes("", db), ""); err != nil {
		return err
	}

	// 2. The database's own composite keyspace, in the store that serves it.
	//    The namespace is ALWAYS a non-empty "<tenant>/<db>/"
	//    (tenant.ComposeNamespace), so a bug that lost the scope deletes
	//    nothing rather than everything.
	target, err := s.namespacedTarget(ctx, tenantID)
	if err != nil {
		return err
	}
	ns := tenant.ComposeNamespace(tenantID, db)
	return s.reclaim(ctx, target, NamespacedKeyPrefixes(ns), DatabaseIndexNamePrefixes(ns, ""), ns)
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
