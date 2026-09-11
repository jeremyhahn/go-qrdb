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
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// Internal key namespace
//
// A handful of key prefixes belong to internal subsystems rather than to the
// user keyspace: RBAC roles and users, relational table rows, secondary index
// entries and sequences, time-series points and their indexes, and retention
// policies. They are stored in the same MVCC keyspace as user data, so every
// user-facing surface that enumerates keys in bulk must exclude them.
//
// This is the ONE canonical declaration of that set, and of the two questions
// asked about it:
//
//	IsInternalKey(key)          -- is this key one of ours, not the caller's?
//	SkipInternalKeyFilter(pfx)  -- is this enumeration the owning subsystem
//	                               reading its own records back?
//
// pkg/engine and pkg/store delegate to both, so a prefix added here is
// honoured by every layer at once. Keeping the two questions derived from one
// table is what makes that safe: a prefix added to the filter but not to the
// skip rule would not leak, it would silently return nothing to the subsystem
// that owns it -- which is how adding "__tbl:" to the filter alone turns every
// relational table scan into an empty result set.
//
// What this is and is not
//
// This is a DISCOVERY guard on bulk enumeration, not an authorization
// boundary. It stops a caller from sweeping a keyspace it was given access to
// and finding another subsystem's records in the results. It does not, and
// cannot at this layer, stop a caller that already knows a key from reading it
// with Get, or from asking for the reserved prefix by name (see
// SkipInternalKeyFilter), or from issuing an MVCC Range over the same byte
// interval -- Range is the interface the relational layer itself reads rows
// through and applies no filter at all. Confidentiality between principals is
// the authorization layer's job (pkg/auth/middleware); this guard's job is to
// keep the subsystems' private keyspaces out of a user's enumeration results.
//
// Why there is no write guard
//
// The filter is deliberately read-only. Every internal subsystem writes its
// own records through the same user-facing KVServicer the guard would have to
// refuse: the relational layer Puts "__tbl:" rows during an ALTER rewrite
// (pkg/sql/provider/relational_rewrite.go) and Puts and Deletes "__idx:"
// entries when building, renaming, and dropping an index
// (relational_table_ddl.go, relational_ddl.go), while the RBAC, tenant,
// collection, credential, and auth-factor stores all Put under "__sys:". A
// refusal on Put/Delete/PutIfAbsent would break every one of them. It would
// also buy very little: relational DML does not write rows through Put at all
// -- rows and their index entries are composed into a single compare-and-swap
// through TransactionServicer.Execute, and sequences advance through the same
// service -- so a caller intent on forging a row would simply propose it
// there. A guard that breaks the owning subsystem while leaving the actual
// write path open is worse than none, so the write path is left to
// authorization, which is where a per-principal decision belongs.
// ---------------------------------------------------------------------------

// InternalKeyPrefixSystem is the prefix reserved for the internal system
// stores (roles, users, and other RBAC records). Restoring or enumerating
// keys under it through a user-facing surface is a privilege-escalation
// vector and is refused.
//
// It is defined in pkg/kvstore because both this guard and the tenant
// keyspace machinery need it, and pkg/kvstore is the only layer they can
// share without an import cycle. Note a namespaced store does NOT route
// around its confinement for this prefix: reserved keys are namespaced like
// any other (#41). This constant is the discovery guard's subject, not an
// exemption.
const InternalKeyPrefixSystem = kvstore.ReservedPrefixSystem

// internalKeyPrefix is one reserved prefix together with the single property
// that varies between them: whether the subsystem owning the namespace reads
// its own records back through the user-facing enumeration calls this guard
// filters.
type internalKeyPrefix struct {
	// prefix is the reserved key prefix.
	prefix string

	// ownerEnumerates grants the namespace the skip-filter escape: an
	// enumeration whose requested prefix lies inside it is served unfiltered,
	// because the subsystem that owns those keys reads them back through the
	// shared KVServicer and would otherwise be filtered out of its own data.
	//
	// It is a grant, so it is given only where a caller was found doing
	// exactly that, and withheld everywhere else -- a namespace whose owner
	// reaches its records by exact key (or through its own engine) stays
	// invisible to enumeration no matter which prefix is asked for.
	ownerEnumerates bool
}

// internalKeyPrefixes is the canonical set of key prefixes reserved for
// internal subsystems. Order is not significant.
//
// The relational entries reference the key-layout constants rather than
// repeating their literals, so the guard cannot drift from the layout it
// guards.
var internalKeyPrefixes = []internalKeyPrefix{
	// RBAC roles and users, tenants, collections, credentials, auth factors,
	// the relational table catalog, and every other system record. Those
	// stores List and Scan their own "__sys:" sub-prefixes.
	{prefix: InternalKeyPrefixSystem, ownerEnumerates: true},

	// Encoded relational rows. RelationalTable scans its own
	// "__tbl:{db}:{tableID}/" prefix for a full table scan and lists it for
	// TRUNCATE and DROP reclamation.
	{prefix: tableDataPrefix, ownerEnumerates: true},

	// Secondary index entries. The indexed read path, index builds, renames,
	// and drops all enumerate "__idx:{db}:{tableID}:{index}/".
	{prefix: tableIndexPrefix, ownerEnumerates: true},

	// Auto-increment and table-id sequences. SequenceService reads a counter
	// by exact key (GetRevision) and advances it by compare-and-swap; nothing
	// enumerates the namespace, so it is granted no escape.
	{prefix: tableSequencePrefix},

	// Time-series points, indexes, and retention policies. The time-series
	// subsystem reads through its own storage engine (pkg/timeseries), never
	// through KV enumeration. The constants are cited rather than repeated,
	// for the reason the relational entries above cite theirs.
	{prefix: timeseries.KeyPrefixPoints},
	{prefix: timeseries.KeyPrefixMetricIndex},
	{prefix: timeseries.KeyPrefixTagIndex},
	{prefix: timeseries.KeyPrefixRetention},
}

// InternalKeyPrefixes returns a copy of the canonical internal key prefixes.
// A copy is returned so a caller cannot mutate the guard's own backing array
// and quietly widen the user keyspace.
func InternalKeyPrefixes() []string {
	out := make([]string, len(internalKeyPrefixes))
	for i, p := range internalKeyPrefixes {
		out[i] = p.prefix
	}
	return out
}

// IsInternalKey reports whether key lies in the internal key namespace, i.e.
// whether it begins with any of the reserved internal prefixes. It is the
// single predicate every layer uses to decide that a key is not the caller's
// to see in a bulk enumeration or to move in a restore.
func IsInternalKey(key string) bool {
	for _, p := range internalKeyPrefixes {
		if strings.HasPrefix(key, p.prefix) {
			return true
		}
	}
	return false
}

// SkipInternalKeyFilter reports whether an enumeration rooted at prefix must
// be served without the internal-key filter.
//
// It is true exactly when prefix lies inside a reserved namespace whose owner
// enumerates its own records through the shared KVServicer -- the relational
// row, index, and system namespaces. Asking for "__tbl:{db}:{id}/" is a table
// scan; asking for "__sys:roles/" is the RBAC store loading its roles. Both
// must see everything under the prefix they named, so filtering is skipped
// rather than applied.
//
// A prefix that merely SPANS a reserved namespace without lying inside one --
// "_", which covers "__sys:" and "__tbl:", or "t", which covers "ts:" -- is an
// ordinary user prefix and is filtered, which is what keeps a one-character
// sweep from becoming an enumeration of the whole internal keyspace.
//
// The escape is a namespace-scoped grant, not a privilege check: this layer
// has no principal to check. A caller that names a reserved prefix outright
// receives what that namespace's owner would receive. That is the same
// posture "__sys:" has always had, and the reason the doc block above calls
// this a discovery guard rather than a confidentiality boundary.
func SkipInternalKeyFilter(prefix string) bool {
	for _, p := range internalKeyPrefixes {
		if p.ownerEnumerates && strings.HasPrefix(prefix, p.prefix) {
			return true
		}
	}
	return false
}

// FilterInternalKeys returns a new slice with internal keys removed. When the
// enumeration is the owning subsystem reading its own namespace back
// (SkipInternalKeyFilter), the keys are returned unchanged.
//
// It is exported for the same reason ValidateRestoreRequest is: a
// tenant/database-namespaced view of this service has already prepended its
// namespace by the time the inner service sees a key, so "acme/reports/__tbl:…"
// reaches the inner guard as an ordinary user key and is admitted. The wrapper
// must therefore apply the guard itself, to the keys and the requested prefix
// as the CALLER wrote them — which it can only do by calling the canonical
// filter rather than reimplementing it.
func FilterInternalKeys(keys []string, prefix string) []string {
	if SkipInternalKeyFilter(prefix) {
		return keys
	}
	filtered := make([]string, 0, len(keys))
	for _, k := range keys {
		if !IsInternalKey(k) {
			filtered = append(filtered, k)
		}
	}
	return filtered
}

// FilterInternalMap removes internal keys from data in place and returns it.
// When the enumeration is the owning subsystem reading its own namespace back
// (SkipInternalKeyFilter), data is returned unchanged. It is exported for the
// reason given on FilterInternalKeys.
func FilterInternalMap(data map[string][]byte, prefix string) map[string][]byte {
	if SkipInternalKeyFilter(prefix) {
		return data
	}
	for k := range data {
		if IsInternalKey(k) {
			delete(data, k)
		}
	}
	return data
}
