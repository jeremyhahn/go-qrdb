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
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// What a database owns
//
// internal_keys.go answers "is this key one of ours?". This file answers the
// question DROP DATABASE asks: "which keys belong to THIS database?" — the
// byte ranges a database's teardown must reclaim from a store it shares with
// other databases.
//
// The two questions have different answers because a reserved namespace is
// not necessarily database-keyed. "__sys:roles/" is internal and belongs to
// no database; "__sys:collections/{db}/" is internal and belongs to exactly
// one. Deriving the teardown set from the guard's set would reclaim the first
// along with the second, so the sets are declared separately and each cites
// the layout constants rather than repeating their literals.
//
// A database's keys are addressed in TWO shapes, and the split is the whole
// reason a prefix scan over the composite namespace is not sufficient.
//
//  1. NAMESPACED. Every engine resolved through service.Registry is handed a
//     view that prepends the composite "<tenant>/<db>/" namespace
//     (tenant.ComposeNamespace), so plain KV and MVCC keys, the relational
//     catalog, rows, index entries, every "__seq:" counter and the privilege
//     catalog are all physically rooted at that one prefix. Time-series keys
//     are namespaced too, but the namespace lands INSIDE the series prefixes
//     ("ts:<tenant>/<db>/<metric>…") because it is the metric NAME that the
//     namespaced view prefixes, not the key. Hence NamespacedKeyPrefixes
//     returns four ranges rather than one.
//
//  2. DATABASE-SEGMENTED, at the store root. The collection surface used to
//     be served by ONE server-wide CollectionServicer over the system shard's
//     root KV, so its keys carried the database as a key SEGMENT and no
//     tenant at all. go-qrdb#441 moved it behind the per-(tenant, database)
//     resolver, so a collection created today writes under shape 1 and this
//     shape holds only keys written before that landed.
//
//     It is kept, and is not dead code: it reclaims that residue, and it
//     costs two empty scans for a database that has none.
//     pkg/server's TestReclaim_CollectionKeysAreInsideTheCompositeNamespace
//     is what says which of the two shapes the collection surface is writing
//     today, so a change that moves it back is caught rather than assumed.
//
// A THIRD property cuts across both and is the reason a reclamation reads
// through ReclaimKV.ListAll rather than the user-facing List: some of these
// keys are INTERNAL, and List applies the internal-key discovery guard.
// Shape 1 survives it by accident -- "<tenant>/<db>/__tbl:..." does not begin
// with a reserved prefix, so the guard admits it -- but the time-series keys
// do begin with one, and their prefixes are not inside a namespace the guard
// waives. List returns an EMPTY list for them. A reclamation built on List
// therefore deleted nothing under three of these prefixes and reported the
// drop a success (go-qrdb#412).
// ---------------------------------------------------------------------------

// NamespacedKeyPrefixes returns every physical key prefix rooted at the
// composite (tenant, database) namespace ns, which must be the value
// tenant.ComposeNamespace returns for the pair — a non-empty
// "<tenant>/<db>/".
//
// Deleting every key under the returned prefixes reclaims a database's plain
// KV and MVCC keys and entities, its relational catalog, rows, secondary
// index entries, sequence counters (the per-database "__tableid", every
// per-table "__autoinc:"/"__rowid:", every user "__usq:" and every
// collection "__collection:" counter) and its privilege catalog, together
// with its time-series points, metric index and retention policies.
//
// It does NOT return timeseries.KeyPrefixTagIndex: a tag index key is
// KeyPrefixTagIndex + tagKey + ":" + tagValue and carries no metric, so it is
// shared by every metric in every database and no single database owns it.
// Nor does it return the collection prefixes, which are not namespaced —
// see CollectionKeyPrefixes and the note above.
//
// An empty ns returns nil rather than the whole store: a teardown that
// resolved no namespace must reclaim nothing, never everything.
func NamespacedKeyPrefixes(ns string) []string {
	if ns == "" {
		return nil
	}
	return []string{
		ns,
		timeseries.KeyPrefixPoints + ns,
		timeseries.KeyPrefixMetricIndex + ns,
		timeseries.KeyPrefixRetention + ns,
	}
}

// CollectionKeyPrefixes returns the ROOT-KEYED physical prefixes holding the
// named database's collection catalog and documents — the layout the
// collection surface used before go-qrdb#441 moved it behind the
// per-(tenant, database) resolver. They carry no tenant segment, because the
// surface that wrote them carried none.
//
// A collection created today lands under NamespacedKeyPrefixes instead. These
// are kept to reclaim what was written before that change; see the note
// above.
//
// An empty database returns nil, for the reason NamespacedKeyPrefixes returns
// nil for an empty namespace.
func CollectionKeyPrefixes(database string) []string {
	if database == "" {
		return nil
	}
	return []string{
		collectionMetadataPrefix + database + "/",
		collectionDataPrefix + database + ":",
	}
}

// DatabaseIndexNamePrefixes returns every secondary-index NAME prefix a
// database owns in the state machine's index catalog. The catalog is a
// namespace of its own — its entries are not KV keys — so a prefix delete
// over the key prefixes above does not touch it and a teardown must
// enumerate it separately.
//
// Index names take the same two shapes the key layout does, for the same
// reason: an index created through a registry-resolved IndexServicer is named
// under the composite namespace ns, while a collection field index written
// before go-qrdb#441 is named "{database}.{collection}.{field}" and carries no
// tenant (see collectionIndexName). Both prefixes are returned, so a catalog
// holding either generation is reclaimed.
//
// Unlike the key prefixes, neither is subject to the internal-key discovery
// guard: the index catalog is enumerated through IndexServicer.ListIndexes,
// which is not a KV read and applies no filter.
//
// An empty ns or database contributes no prefix, so a caller that resolved
// neither reclaims nothing.
func DatabaseIndexNamePrefixes(ns, database string) []string {
	prefixes := make([]string, 0, 2)
	if ns != "" {
		prefixes = append(prefixes, ns)
	}
	if database != "" {
		prefixes = append(prefixes, database+".")
	}
	if len(prefixes) == 0 {
		return nil
	}
	return prefixes
}

// TenantKeyPrefixes returns every physical key prefix the named tenant owns
// in the store serving it.
//
// It is the tenant-scoped peer of NamespacedKeyPrefixes and is built the same
// way, one segment shorter. A database's keys are rooted at
// tenant.ComposeNamespace's "<tenant>/<db>/", and a tenant's IDENTITY records
// -- users, roles, permission scopes, invitations -- are rooted at
// tenant.IdentityNamespace's "<tenant>/", which is the PARENT of every one of
// its databases' namespaces. So a single "<tenant>/" range covers both, and
// the time-series prefixes are returned separately for exactly the reason
// NamespacedKeyPrefixes returns them separately: the namespace lands INSIDE
// the series prefix, because it is the metric NAME that the namespaced view
// prefixes rather than the key.
//
// It does NOT return timeseries.KeyPrefixTagIndex, for the reason
// NamespacedKeyPrefixes does not: a tag index key carries no metric and is
// therefore shared by every tenant.
//
// It does not return the pre-go-qrdb#441 root-keyed collection prefixes
// either. Those carry a database segment and NO tenant, so no tenant owns
// them and no tenant-scoped scan can find them; they are reclaimed by the
// drop of the database that wrote them, which DeleteTenant's bottom-up
// refusal requires to have happened already.
//
// It DOES return the tenant's placement range. A dedicated-raft database's
// replica assignment lives at "__sys:placement/{tenant}/{db}", outside the
// tenant's namespace and keyed by tenant, and the per-node reconciler watches
// that range: removing an entry is what stops and purges the shard on every
// node that holds a replica. A tenant whose placement entries outlived it
// kept its shards running and its shard data dirs on disk on every replica --
// which the local node's DataRoot removal cannot reach.
//
// The SYSTEM tenant returns nil. "<tenant>/" for the system tenant is
// "system/", which is where the bootstrap database and every system identity
// record live; a reclamation that accepted it would empty the store it was
// asked to tidy. An empty tenantID returns nil for the same reason, since
// tenant.IdentityNamespace canonicalizes it to the system tenant.
func TenantKeyPrefixes(tenantID string) []string {
	if tenantID == "" || tenant.IsSystemTenant(tenantID) {
		return nil
	}
	ns := tenant.IdentityNamespace(tenantID)
	return []string{
		ns,
		timeseries.KeyPrefixPoints + ns,
		timeseries.KeyPrefixMetricIndex + ns,
		timeseries.KeyPrefixRetention + ns,
		placement.PlacementPrefix + ns,
	}
}

// TenantIndexNamePrefixes returns every secondary-index NAME prefix a tenant
// owns in the state machine's index catalog.
//
// An index created through a registry-resolved IndexServicer is named under
// the composite "<tenant>/<db>/" namespace, so the tenant segment alone
// selects every index of every database the tenant owns. The catalog is a
// namespace of its own and is not reached by deleting keys, which is why it
// is enumerated separately -- see DatabaseIndexNamePrefixes.
//
// The system tenant and an empty id return nil, for the reason
// TenantKeyPrefixes returns nil for them.
func TenantIndexNamePrefixes(tenantID string) []string {
	if tenantID == "" || tenant.IsSystemTenant(tenantID) {
		return nil
	}
	return []string{tenant.IdentityNamespace(tenantID)}
}
