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

package server

import (
	"bytes"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// composeNamespace returns the composite key-namespace prefix for a
// (tenant, db) pair. A database is a key-namespace layered over the tenant's
// already-resolved store, so the physical key prefix is the concatenation of a
// tenant segment and a database segment, and is ALWAYS non-empty and uniform:
//
//	"<tenant>/<db>/"
//
// Unlike earlier revisions, neither the system tenant nor the bootstrap
// ("system") database is special-cased to an empty prefix. An empty prefix gave
// the (system, system) pair the entire shared store root as its keyspace, so a
// scan of the bootstrap database returned every OTHER database's "<db>/" keys
// and every customer tenant's "<tenant>/" keys. Prefixing every pair uniformly
// — including the bootstrap pair, which becomes "system/system/" — closes that
// cross-database/cross-tenant data-isolation leak.
//
// systemTenant and defaultDB are used only as fallbacks when tenantID or db is
// empty (e.g. an unscoped probe), so the resulting prefix is always a complete
// "<tenant>/<db>/" with no empty or doubled segment.
func composeNamespace(tenantID, db, systemTenant, defaultDB string) string {
	if tenantID == "" {
		tenantID = systemTenant
	}
	if db == "" {
		db = defaultDB
	}
	var b strings.Builder
	b.WriteString(tenantID)
	b.WriteByte('/')
	b.WriteString(db)
	b.WriteByte('/')
	return b.String()
}

// DatabaseCatalog reports the existence of databases. The restServiceRegistry
// consults it so that any database that exists for a tenant resolves to a
// namespaced view of the shared service set, without requiring a per-database
// *DatabaseServices to be pre-registered.
type DatabaseCatalog interface {
	// DatabaseExists reports whether a database with the given name exists
	// and is accessible to the given tenant.
	DatabaseExists(tenantID, name string) bool
}

// NamespacedImportUnsupportedError is returned when a bulk Import is attempted
// against a non-default (tenant, db) scope. ImportOptions carries no key-prefix
// hook and the parser derives keys internally, so the namespace wrapper cannot
// transparently prefix imported keys without duplicating the format-parsing
// logic. Importing into a named database is a documented Stage 1 follow-up; the
// Execute-based batch operations remain fully namespaced.
type NamespacedImportUnsupportedError struct {
	Prefix string
}

// Error implements the error interface.
func (e *NamespacedImportUnsupportedError) Error() string {
	return "server: bulk import into a namespaced database (" + e.Prefix + ") is not supported"
}

// isRangeOp reports whether op addresses a range of keys rather than a single
// key. A range op is either an explicit OpRange or any op carrying a non-empty
// RangeEnd (a range read encoded as a GET, or a delete-range). For such ops the
// Key is a range lower bound and must be confined with prefixStartBytes.
func isRangeOp(op service.Op) bool {
	return op.Type == service.OpRange || len(op.RangeEnd) > 0
}

// stripStringPrefix removes the namespace prefix from a single string key.
func stripStringPrefix(key, prefix string) string {
	return strings.TrimPrefix(key, prefix)
}

// stripStringSlicePrefix removes the namespace prefix from every key in a slice.
func stripStringSlicePrefix(keys []string, prefix string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = strings.TrimPrefix(k, prefix)
	}
	return out
}

// prefixBytes returns prefix+key as a new byte slice. A nil key is left nil so
// that range upper bounds that signal "single key" semantics are preserved.
//
// prefixBytes must NOT be used for a range LOWER bound: a nil/empty key would be
// forwarded as nil, which pkg/mvcc.IsInRange treats as "the start of the entire
// shared store", returning every other tenant's and database's keys. Use
// prefixStartBytes for lower bounds and prefixEndBytes for upper bounds.
func prefixBytes(key []byte, prefix string) []byte {
	if key == nil {
		return nil
	}
	out := make([]byte, 0, len(prefix)+len(key))
	out = append(out, prefix...)
	out = append(out, key...)
	return out
}

// prefixStartBytes returns the namespaced inclusive lower bound for a range. An
// absent (nil or empty) lower bound is confined to the namespace by returning
// the bare prefix, so the scan begins at the first key in the namespace rather
// than at the start of the entire shared store. A non-empty key is prefixed.
//
// This closes the cross-tenant/cross-database exposure where a nil/empty range
// start escaped the namespace: pkg/mvcc.IsInRange treats a nil start as the
// start of the whole store, so without this confinement a range with no start
// returned every namespace's keys.
func prefixStartBytes(key []byte, prefix string) []byte {
	if len(key) == 0 {
		return []byte(prefix)
	}
	out := make([]byte, 0, len(prefix)+len(key))
	out = append(out, prefix...)
	out = append(out, key...)
	return out
}

// prefixEndBytes returns the namespaced exclusive upper bound for a range. An
// absent (nil or empty) upper bound is confined to the namespace by returning
// the prefix successor (prefixSuccessor), so the scan stops at the end of the
// namespace rather than running to the end of the entire shared store. A
// non-empty key is prefixed.
//
// pkg/mvcc.IsInRange treats a nil end as the end of the whole store, so without
// this confinement a range with no end returned every later namespace's keys.
func prefixEndBytes(key []byte, prefix string) []byte {
	if len(key) == 0 {
		return prefixSuccessor(prefix)
	}
	out := make([]byte, 0, len(prefix)+len(key))
	out = append(out, prefix...)
	out = append(out, key...)
	return out
}

// prefixSuccessor returns the smallest byte slice strictly greater than every
// byte slice that begins with prefix: the prefix with its last byte that is
// below 0xFF incremented and the trailing 0xFF bytes dropped. It is the
// canonical exclusive upper bound of a prefix scan. A prefix that is empty or
// all-0xFF has no finite successor and yields nil (an unbounded upper bound);
// composeNamespace always produces a non-empty prefix ending in '/', so this
// degenerate case never arises for namespace prefixes in practice.
func prefixSuccessor(prefix string) []byte {
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 0xFF {
			out := make([]byte, i+1)
			copy(out, b[:i])
			out[i] = b[i] + 1
			return out
		}
	}
	return nil
}

// stripBytePrefix removes the namespace prefix from a single byte-slice key.
func stripBytePrefix(key []byte, prefix string) []byte {
	return bytes.TrimPrefix(key, []byte(prefix))
}

// stripKeyValue returns a shallow copy of kvp with the namespace prefix removed
// from its Key. A nil input returns nil.
func stripKeyValue(kvp *kv.KeyValue, prefix string) *kv.KeyValue {
	if kvp == nil {
		return nil
	}
	out := *kvp
	out.Key = bytes.TrimPrefix(kvp.Key, []byte(prefix))
	return &out
}

// stripKeyValues returns a new slice with the namespace prefix removed from the
// Key of every key-value in kvs.
func stripKeyValues(kvs []*kv.KeyValue, prefix string) []*kv.KeyValue {
	if kvs == nil {
		return nil
	}
	out := make([]*kv.KeyValue, len(kvs))
	for i, kvp := range kvs {
		out[i] = stripKeyValue(kvp, prefix)
	}
	return out
}
