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

package engine

import (
	"bytes"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// ---------------------------------------------------------------------------
// Index key scoping (go-qrdb#410)
//
// A secondary index covers a KEY SCOPE: a byte prefix every key it indexes
// must carry. Index maintenance on the Raft apply path consults the scope, so
// an index named for one database cannot index another database's keys and an
// index belonging to one tenant cannot index another tenant's keys.
//
// The scope has two parts and they compose the same way the keyspace does:
//
//	scope = IndexNamespace(name) + declared key prefix
//
// The NAMESPACE half is read back off the index name. Every transport
// registry resolves a request to a (tenant, database) pair and wraps the
// engines with the one prefix tenant.ComposeNamespace builds --
// "<tenant>/<database>/" -- applying it to every KEY and, through
// namespacedIndexService, to every index NAME. Reading it back off the name
// is therefore the exact inverse of the wrapper, not an inference: the same
// string that prefixes the keys prefixes the name.
//
// The DECLARED half is whatever the creator knew that the name does not say:
// a collection declares its document prefix "__col:{db}:{collection}/", a DAO
// entity type declares "{entityType}/". It is declared RELATIVE to the
// namespace, so a declaration made below the wrapper stays correct whether or
// not a wrapper is in play -- exactly like the key it describes.
//
// An index with NEITHER half is UNSCOPED and keeps the shard-wide behaviour
// go-qrdb#410 measured. That is not a filter failing open by accident: a bare
// name is genuinely not attributable to a keyspace, and an index the caller
// declared nothing about is the caller's whole database by intent. It is
// reachable only from a single-tenant, registry-less composition, because
// every transport namespaces both keys and index names.
// ---------------------------------------------------------------------------

// namespaceSeparator separates the tenant and database segments of the
// composite key namespace (see tenant.ComposeNamespace).
const namespaceSeparator = '/'

// IndexNamespace returns the "<tenant>/<database>/" namespace prefix carried
// by an index name, or the empty string when the name carries none.
//
// It is the inverse of the prefixing namespacedIndexService performs: that
// wrapper composes prefix+name from tenant.ComposeNamespace, which always
// emits two non-empty segments each terminated by '/'. A name with fewer than
// two such segments was not produced by a namespaced registry and yields no
// namespace.
//
// Note that anything AFTER the second separator belongs to the inner name and
// is not examined here, so an inner name that itself contains '/' neither
// widens nor narrows the namespace.
func IndexNamespace(name string) string {
	first := strings.IndexByte(name, namespaceSeparator)
	if first <= 0 {
		return ""
	}
	rest := name[first+1:]
	second := strings.IndexByte(rest, namespaceSeparator)
	if second <= 0 {
		return ""
	}
	return name[:first+1+second+1]
}

// ComposeIndexScope returns the key scope an index is maintained under: the
// namespace its name carries followed by the key prefix its creator declared.
// It returns nil for an index with neither, which is the unscoped index
// described above.
func ComposeIndexScope(name, declaredKeyPrefix string) []byte {
	ns := IndexNamespace(name)
	if ns == "" && declaredKeyPrefix == "" {
		return nil
	}
	return []byte(ns + declaredKeyPrefix)
}

// KeyInScope reports whether key falls inside scope. An empty (or nil) scope
// is the unscoped index and admits every key outside the reserved system
// namespace; no scope admits a system key (see IsSystemKey), so no index
// maintenance or backfill that asks KeyInScope ever indexes a credential
// record.
func KeyInScope(scope, key []byte) bool {
	if IsSystemKey(key) {
		return false
	}
	return len(scope) == 0 || bytes.HasPrefix(key, scope)
}

// anyIndexInScope reports whether at least one registered index covers key.
// It lets the maintenance helpers skip the JSON parse entirely for a write no
// index can possibly be interested in -- the common case on a shard that
// holds many databases and one index.
func anyIndexInScope(indexes map[string]index.Index, scopes map[string][]byte, key []byte) bool {
	if IsSystemKey(key) {
		return false
	}
	if len(scopes) == 0 {
		return true
	}
	for name := range indexes {
		if KeyInScope(scopes[name], key) {
			return true
		}
	}
	return false
}

// backfillRange returns the [start, end) key range an index-create backfill
// scans for a scope. A scoped index reads only its own keyspace; an unscoped
// one keeps the whole-keyspace range the engine has always used.
//
// prefixEndBytes returns nil for an all-0xFF prefix, which as a range end
// would mean "unbounded" rather than "just this prefix" -- the scope filter
// BackfillIndex applies still holds the result to the scope in that case.
func backfillRange(scope []byte) (start, end []byte) {
	if len(scope) == 0 {
		return []byte{0x00}, []byte{0xFF}
	}
	if e := prefixEndBytes(scope); e != nil {
		return scope, e
	}
	return scope, []byte{0xFF}
}
