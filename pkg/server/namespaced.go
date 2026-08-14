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

// DatabaseCatalog reports the existence of databases. The restServiceRegistry
// consults it so that any database that exists for a tenant resolves to a
// namespaced view of the shared service set, without requiring a per-database
// *DatabaseServices to be pre-registered.
type DatabaseCatalog interface {
	// DatabaseExists reports whether a database with the given name exists
	// and is accessible to the given tenant.
	DatabaseExists(tenantID, name string) bool
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

// prefixBytes returns prefix+key as a new byte slice. An absent key — nil OR
// empty — is left nil so that range upper bounds that signal "single key"
// semantics are preserved. The empty-but-non-nil case matters: wire decoders
// (e.g. REST's jsonToOps) produce []byte("") for an unset field, and
// prefixing it would fabricate a RangeEnd equal to the bare namespace
// prefix, silently turning a single-key GET into an inverted (empty) range.
//
// prefixBytes must NOT be used for a range LOWER bound: a nil/empty key would be
// forwarded as nil, which pkg/mvcc.IsInRange treats as "the start of the entire
// shared store", returning every other tenant's and database's keys. Use
// prefixStartBytes for lower bounds and prefixEndBytes for upper bounds.
func prefixBytes(key []byte, prefix string) []byte {
	if len(key) == 0 {
		return nil
	}
	out := make([]byte, 0, len(prefix)+len(key))
	out = append(out, prefix...)
	out = append(out, key...)
	return out
}

// prefixKeyBytes returns the namespaced form of a single-key operation's Key
// (a Put/Delete/Get/entity-ID style key, never a range bound). An absent
// (nil or empty) key is confined to the namespace by mapping it to the bare
// prefix itself — the same confinement namespacedKVService.Put already
// applies when called with an empty string key: Put(ctx, "", v) stores under
// the literal "<prefix>" key, not at the shared store root.
//
// Without this confinement, prefixBytes leaves an empty key nil, and a nil
// single-key Op.Key is NOT confined to the namespace: pkg/mvcc.IsInRange (and
// a bare map/tree lookup) treats it as addressing the shared store root,
// where two tenants' or databases' empty-key ops collide. A non-empty key is
// prefixed normally, identically to prefixStartBytes.
//
// prefixKeyBytes intentionally shares its confinement logic with
// prefixStartBytes (a range lower bound and a single key must refuse to let
// an absent key escape the namespace in the exact same way) but is named for
// its single-key call sites so a reader is not misled into thinking a range
// bound is being computed.
//
// Do NOT use this for RangeEnd on a single-key op: RangeEnd must stay
// nil-preserving (use prefixBytes) so a GET is not misread as an inverted
// empty range.
func prefixKeyBytes(key []byte, prefix string) []byte {
	return prefixStartBytes(key, prefix)
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
// tenant.ComposeNamespace always produces a non-empty prefix ending in '/',
// so this degenerate case never arises for namespace prefixes in practice.
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
