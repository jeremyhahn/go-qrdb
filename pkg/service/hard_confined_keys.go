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
// general "__sys:" discovery guard, for two reasons, not one:
//
//  1. The "ownerEnumerates" escape InternalKeyPrefixSystem is granted
//     (SkipInternalKeyFilter) lets a caller that names "__sys:" outright
//     see every system record unfiltered, because that escape exists for
//     a subsystem reading its OWN bookkeeping back (roles, users, the
//     table catalog). The cluster CA's key material is not bookkeeping,
//     and must never ride through that escape just because it happens to
//     live under the same top-level prefix -- FilterInternalKeys and
//     FilterInternalMap strip it unconditionally, even on that branch
//     (see their own doc comments).
//  2. Every (tenant, db) pair resolved through pkg/server's
//     restServiceRegistry is composite-namespaced -- tenant.ComposeNamespace
//     prepends "{tenant}/{db}/" to every key, UNCONDITIONALLY, including
//     for the system tenant's own default database -- so a literal
//     "__sys:cluster_ca/..." key named through THAT surface cannot
//     physically collide with the real record. The embedded SDK
//     transport's services (sdk/go/transport/embedded/server.go) are NOT
//     behind that registry, though: they are constructed directly over
//     the node's default shard with no namespace prefix at all, the same
//     shape pkg/cakey's own direct access has, and a data directory that
//     was ever part of a cluster carries the real "__sys:cluster_ca/..."
//     record on that same shard. A caller reaching the embedded
//     transport's KV/MVCC/Watch/Transaction services directly -- which
//     includes the desktop webview's bindings
//     (cmd/qrdb/bindings_kv.go) -- CAN address the real key by exact
//     name. hardConfinedKV/hardConfinedMVCC/hardConfinedWatch/
//     hardConfinedTransaction below are wrapped around exactly those
//     embedded services (never around pkg/cakey's own n.KV/n.Transaction
//     fields, which are not reached through this package's constructors
//     at all) to close that path: see
//     sdk/go/transport/embedded/server.go's construction of kvSvc,
//     mvccSvc, watchSvc and txSvc.
const hardConfinedPrefix = InternalKeyPrefixSystem + "cluster_ca/"

// hardConfinedPrefixEnd is the exclusive upper bound of every key under
// hardConfinedPrefix: the smallest string that sorts after all of them.
// Incrementing the prefix's last byte is correct here because that byte is
// '/' (0x2F, not 0xFF) and can never overflow.
var hardConfinedPrefixEnd = func() string {
	b := []byte(hardConfinedPrefix)
	b[len(b)-1]++
	return string(b)
}()

// isHardConfinedKey reports whether key lies in the hard-confined
// namespace.
func isHardConfinedKey(key string) bool {
	return strings.HasPrefix(key, hardConfinedPrefix)
}

// hardConfinedRangeOverlaps reports whether the half-open byte range
// [start, end) could contain a hard-confined key. An empty end is treated
// as a single-key probe at start, matching this codebase's own convention
// for a Range/Op whose end is nil or empty: "only the key at start", not
// "unbounded above" (see MVCCKVServicer.Range and lock.Op.RangeEnd).
func hardConfinedRangeOverlaps(start, end string) bool {
	if end == "" {
		return isHardConfinedKey(start)
	}
	return start < hardConfinedPrefixEnd && end > hardConfinedPrefix
}

// hardConfinedPrefixOverlaps reports whether an enumeration or watch
// rooted at prefix could reach a hard-confined key: either prefix lies
// inside the namespace (an ordinary isHardConfinedKey match) or the
// namespace lies inside prefix (a broader sweep -- "__sys:", "", or any
// other ancestor prefix -- that would reach it too).
func hardConfinedPrefixOverlaps(prefix string) bool {
	return strings.HasPrefix(hardConfinedPrefix, prefix) || strings.HasPrefix(prefix, hardConfinedPrefix)
}

// clampRangeToHardConfined returns the intersection of [start, end) with
// [hardConfinedPrefix, hardConfinedPrefixEnd), for a caller that has
// already established the two ranges overlap (hardConfinedRangeOverlaps)
// and needs a sub-range query scoped to exactly the confined portion.
func clampRangeToHardConfined(start, end []byte) (clampedStart, clampedEnd []byte) {
	clampedStart = start
	if string(start) < hardConfinedPrefix {
		clampedStart = []byte(hardConfinedPrefix)
	}
	clampedEnd = end
	if len(end) == 0 || string(end) > hardConfinedPrefixEnd {
		clampedEnd = []byte(hardConfinedPrefixEnd)
	}
	return clampedStart, clampedEnd
}

// filterHardConfinedKeys returns a new slice with hard-confined keys
// removed, for FilterInternalKeys' owner-escape branch (internal_keys.go)
// and for the embedded-transport decorators' List/Scan overrides.
func filterHardConfinedKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if !isHardConfinedKey(k) {
			out = append(out, k)
		}
	}
	return out
}

// filterHardConfinedMap removes hard-confined keys from data in place and
// returns it, for FilterInternalMap's owner-escape branch
// (internal_keys.go) and for the embedded-transport decorators' Scan
// overrides.
func filterHardConfinedMap(data map[string][]byte) map[string][]byte {
	for k := range data {
		if isHardConfinedKey(k) {
			delete(data, k)
		}
	}
	return data
}

// HardConfinedKeyError is returned by the embedded-transport decorators
// (hardConfinedKV, hardConfinedMVCC, hardConfinedWatch,
// hardConfinedTransaction) when a caller-named key, prefix, or range
// reaches into the hard-confined namespace through a point-key or
// transaction-compare verb -- one that cannot simply filter the key out
// of a result set the way an enumeration can.
//
// The refusal is unconditional and has no override: unlike
// IsInternalKey's discovery guard, there is no escape for a caller that
// names the prefix outright, because doing so is exactly the attempt this
// guards against. Transports map it to 403 Forbidden / codes.PermissionDenied,
// the same class as RestoreInternalKeyError: it reports an operation the
// caller is not permitted to perform, not a malformed one.
type HardConfinedKeyError struct {
	// Key is the offending key or prefix.
	Key string
}

// Error returns a human-readable description.
func (e *HardConfinedKeyError) Error() string {
	return "service: " + e.Key + " is not reachable through this surface"
}
