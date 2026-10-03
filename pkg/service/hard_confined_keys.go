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

import "strings"

// hardConfinedPrefix is "__sys:cluster_ca/", the sub-prefix of the internal
// key namespace (see internal_keys.go) that holds the cluster CA's private
// key and certificate (pkg/cakey.SoftwareProvider's two split records).
//
// It exists as its own constant, separate from InternalKeyPrefixSystem's
// general "__sys:" discovery guard, for exactly one reason: the
// "ownerEnumerates" escape InternalKeyPrefixSystem is granted
// (SkipInternalKeyFilter) lets a caller that names "__sys:" outright see
// every system record unfiltered, because that escape exists for a
// subsystem reading its OWN bookkeeping back (roles, users, the table
// catalog). The cluster CA's key material is not bookkeeping, and must
// never ride through that escape just because it happens to live under the
// same top-level prefix. FilterInternalKeys and FilterInternalMap strip it
// unconditionally, even when the owner escape applies to everything else
// in the request (see their own doc comments).
//
// Every (tenant, db) pair a KVServicer/MVCCKVServicer resolves through
// this package's Registry is composite-namespaced -- tenant.ComposeNamespace
// prepends "{tenant}/{db}/" to every key before it reaches the shared
// store, UNCONDITIONALLY, including for the system tenant's own default
// database (see pkg/server/rest_registry.go's resolveShared) -- so a
// literal "__sys:cluster_ca/..." key named through that surface can never
// physically collide with the real record; it becomes an ordinary,
// harmless key under the caller's own namespace instead. The real record
// is reached only by pkg/cakey's own in-process plumbing, which
// pkg/server/lifecycle wires directly to a node's raw, unnamespaced
// service.KVServicer and service.TransactionServicer fields, never through
// this package's Registry. The exposure this guards against is therefore
// enumeration, not an exact-key read: a caller enumerating "__sys:" broadly
// enough to use the owner escape must not see this sub-prefix's keys
// mixed into bookkeeping it legitimately reads back that way.
const hardConfinedPrefix = InternalKeyPrefixSystem + "cluster_ca/"

// IsHardConfinedKey reports whether key lies in the hard-confined
// namespace.
func IsHardConfinedKey(key string) bool {
	return strings.HasPrefix(key, hardConfinedPrefix)
}

// filterHardConfinedKeys returns a new slice with hard-confined keys
// removed, for FilterInternalKeys' owner-escape branch (internal_keys.go).
func filterHardConfinedKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if !IsHardConfinedKey(k) {
			out = append(out, k)
		}
	}
	return out
}

// filterHardConfinedMap removes hard-confined keys from data in place and
// returns it, for FilterInternalMap's owner-escape branch
// (internal_keys.go).
func filterHardConfinedMap(data map[string][]byte) map[string][]byte {
	for k := range data {
		if IsHardConfinedKey(k) {
			delete(data, k)
		}
	}
	return data
}
