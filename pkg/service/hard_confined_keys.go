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

// hardConfinedPrefixes are "__sys:cluster_ca/" and "__sys:cert_ca/", the
// sub-prefixes of the internal key namespace (see internal_keys.go) under
// which CA key material lives: the cluster CA (Key A) and every tenant's
// login CA (Key B), both as pkg/cakey.SoftwareProvider's split
// "material"/"certificate" records.
//
// Only the "material" record under either prefix is hard-confined
// (hardConfinedMaterialSuffix, below) -- never "certificate": the
// certificate is public data by pkg/cakey's own design
// (SoftwareProvider.Seal's doc comment: "Certificate... remains cached:
// it is public data"), and pkg/cert.DefaultCertProvider.CACertPEMs
// legitimately Scans this exact prefix, through this package's own
// FilterInternalMap (below), to enumerate every tenant's Key B
// certificate for the dynamic client-auth trust pool
// (pkg/clienttrust.Pool.Refresh). Confining "certificate" too
// made that Scan filter its own enumeration's result out, under its own
// guard -- so a cert the trust pool legitimately needed to advertise
// disappeared, withdrawing Key B's TLS trust for every tenant with no
// error at all, caught by go-qrdb#591's own regression test rather than
// a targeted one, since it broke as a side effect of a lower-level fix.
//
// They are their own variable, separate from InternalKeyPrefixSystem's
// general "__sys:" discovery guard, for two reasons, not one:
//
//  1. The "ownerEnumerates" escape InternalKeyPrefixSystem is granted
//     (SkipInternalKeyFilter) lets a caller that names "__sys:" outright
//     see every system record unfiltered, because that escape exists for
//     a subsystem reading its OWN bookkeeping back (roles, users, the
//     table catalog). Neither CA's private key material is bookkeeping,
//     and must never ride through that escape just because it happens to
//     live under the same top-level prefix -- FilterInternalKeys and
//     FilterInternalMap strip it unconditionally, even on that branch
//     (see their own doc comments) -- while the certificate half, being
//     public, is left for that escape to handle normally.
//  2. Every (tenant, db) pair resolved through pkg/server's
//     restServiceRegistry is composite-namespaced -- tenant.ComposeNamespace
//     prepends "{tenant}/{db}/" to every key, UNCONDITIONALLY, including
//     for the system tenant's own default database -- so a literal
//     "__sys:cluster_ca/..." or "__sys:cert_ca/..." key named through
//     THAT surface cannot physically collide with the real record. The
//     embedded SDK transport's services
//     (sdk/go/standalone/server.go) are NOT behind that registry,
//     though: they are constructed directly over the node's default shard
//     with no namespace prefix at all, the same shape pkg/cakey's own
//     direct access has, and a data directory that ever established
//     either CA carries its real record on that same shard. A caller
//     reaching the embedded transport's KV/MVCC/Watch/Transaction
//     services directly -- which includes the desktop webview's bindings
//     (cmd/qrdb/bindings_kv.go) -- CAN address the real key by exact
//     name. hardConfinedKV/hardConfinedMVCC/hardConfinedWatch/
//     hardConfinedTransaction below are wrapped around exactly those
//     embedded services (never around pkg/cakey's own n.KV/n.Transaction
//     fields, which are not reached through this package's constructors
//     at all) to close that path: see
//     sdk/go/standalone/server.go's construction of kvSvc,
//     mvccSvc, watchSvc and txSvc.
//
// Key B was not confined here until go-qrdb#591's security review: it
// rode the same storage path and the owner's CA-at-rest ruling already
// requires its at-rest protection to match Key A's, but nothing had
// extended this embedded-transport guard to its prefix, so an embedded
// caller could read a tenant's login CA private key directly by name.
var hardConfinedPrefixes = []string{
	InternalKeyPrefixSystem + "cluster_ca/",
	InternalKeyPrefixSystem + "cert_ca/",
}

// hardConfinedWholePrefixes are internal sub-prefixes confined WHOLE on
// the embedded transport: every key under one is refused to a point
// read, write, delete, compare or transaction op, refused as an
// enumeration or watch root, and filtered out of any broader sweep, with
// no "/material" suffix test. "__sys:cert_delegation_policy/" duplicates
// pkg/cert's unexported prefixDelegationPolicy, for the same reason
// hardConfinedMaterialSuffix duplicates cakey's record name (go-qrdb#1187):
// a standing delegation policy authorizes certificate issuance, so an
// embedded caller holding only kv:put must not be able to write one
// around CertEnrollmentService's own policy-admin check. pkg/cert's own
// narrow self-scan of one tenant's policies (ListDelegationPolicies)
// stays visible through FilterInternalMap's owner escape, exactly as
// CACertPEMs' certificate scan does (isHardConfinedKeyForScan).
var hardConfinedWholePrefixes = []string{
	delegationPolicyConfinedPrefix,
}

// delegationPolicyConfinedPrefix duplicates pkg/cert's unexported
// prefixDelegationPolicy; TestDelegationPolicy_RecordsSitUnderTheConfinedPrefix
// fails if the two drift apart.
const delegationPolicyConfinedPrefix = "__sys:cert_delegation_policy/"

// hardConfinedAllPrefixes is hardConfinedPrefixes followed by
// hardConfinedWholePrefixes: every confined namespace, for the
// root, range and overlap tests below that do not look at a key's suffix.
var hardConfinedAllPrefixes = append(append([]string{}, hardConfinedPrefixes...), hardConfinedWholePrefixes...)

// hardConfinedMaterialSuffix is cakey.SoftwareProvider's literal record
// name for the private half of any CA it manages (keyMaterial in
// pkg/cakey/software.go), reproduced here rather than imported for the
// same reason sdk/go/conformance's own confinement tests name the
// wire-level string directly: a security guard should not depend on a
// constant it could silently drift apart from.
const hardConfinedMaterialSuffix = "/material"

// hardConfinedPrefixEnds are the exclusive upper bounds of every key under
// the matching entry of hardConfinedPrefixes: the smallest string that
// sorts after all of them. Incrementing the prefix's last byte is correct
// here because that byte is '/' (0x2F, not 0xFF) and can never overflow.
// Used only by hardConfinedRangeOverlaps/hardConfinedOverlapRanges to
// decide whether a RANGE could reach a confined key at all -- a coarser,
// prefix-only test than isHardConfinedKey's, deliberately: narrowing a
// range to the exact material records within it, rather than the whole
// CA prefix, would cost a second overlap test per candidate key for no
// benefit, since every candidate is re-checked by isHardConfinedKey (or
// filterHardConfinedKeys/Map) before it is ever returned to a caller.
var hardConfinedPrefixEnds = func() []string {
	ends := make([]string, len(hardConfinedAllPrefixes))
	for i, p := range hardConfinedAllPrefixes {
		b := []byte(p)
		b[len(b)-1]++
		ends[i] = string(b)
	}
	return ends
}()

// hardConfinedPrefixesLabel names every hard-confined prefix for an error
// message, when the specific offending key is not available to the
// caller (a bulk Transaction/Batch op that only reports "touches the
// confined namespace", not which key within it).
var hardConfinedPrefixesLabel = strings.Join(hardConfinedAllPrefixes, " or ")

// isHardConfinedKey reports whether key is a CA's own "material" record --
// never its "certificate" sibling, which is public (see this file's own
// doc comment above) -- or any key under hardConfinedWholePrefixes.
func isHardConfinedKey(key string) bool {
	return isHardConfinedWholeKey(key) || isHardConfinedMaterialKey(key)
}

// isHardConfinedWholeKey reports whether key lies under one of
// hardConfinedWholePrefixes, whatever its suffix.
func isHardConfinedWholeKey(key string) bool {
	return hardConfinedWholePrefix(key) != ""
}

// hardConfinedWholePrefix returns the entry of hardConfinedWholePrefixes
// key lies under, or "" when it lies under none.
func hardConfinedWholePrefix(key string) string {
	for _, p := range hardConfinedWholePrefixes {
		if strings.HasPrefix(key, p) {
			return p
		}
	}
	return ""
}

// isHardConfinedMaterialKey reports whether key is a CA's own "material"
// record under one of hardConfinedPrefixes.
func isHardConfinedMaterialKey(key string) bool {
	if !strings.HasSuffix(key, hardConfinedMaterialSuffix) {
		return false
	}
	for _, p := range hardConfinedPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// isHardConfinedRoot reports whether prefix is exactly one of
// hardConfinedPrefixes -- a confined CA's own bare root, named outright
// as a prefix/enumeration-root argument rather than a specific key. The
// embedded-transport decorators' point-key-shaped refusal checks
// (hardConfinedKV.List/Scan and friends, hardConfinedWatch's entity-type
// watch) use this ALONGSIDE isHardConfinedKey: naming the confined root
// directly as "the thing to enumerate" is the same kind of overt probe
// naming the material key directly is, refused outright rather than
// merely filtered, even though the bare root is not itself a record with
// a secret value. It is deliberately NOT part of isHardConfinedKey or
// isHardConfinedKeyForScan: neither pkg/cert.DefaultCertProvider.CACertPEMs'
// own self-scan (which queries exactly this root) nor the certificate
// visibility isHardConfinedKeyForScan grants it should be refused outright
// for asking the question isHardConfinedRoot answers about a DIFFERENT
// kind of caller.
func isHardConfinedRoot(prefix string) bool {
	for _, p := range hardConfinedAllPrefixes {
		if prefix == p {
			return true
		}
	}
	return false
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
	for i, p := range hardConfinedAllPrefixes {
		if start < hardConfinedPrefixEnds[i] && end > p {
			return true
		}
	}
	return false
}

// hardConfinedPrefixOverlaps reports whether an enumeration or watch
// rooted at prefix could reach a hard-confined key: either prefix lies
// inside a confined namespace (an ordinary isHardConfinedKey match) or a
// confined namespace lies inside prefix (a broader sweep -- "__sys:", "",
// or any other ancestor prefix -- that would reach it too).
func hardConfinedPrefixOverlaps(prefix string) bool {
	for _, p := range hardConfinedAllPrefixes {
		if strings.HasPrefix(p, prefix) || strings.HasPrefix(prefix, p) {
			return true
		}
	}
	return false
}

// hardConfinedOverlapRanges returns the intersection of [start, end) with
// every hard-confined prefix's own [prefix, prefixEnd) that it overlaps --
// usually zero or one entry, two only when the caller's own range spans
// more than one confined prefix (an unprefixed "__sys:" sweep spans both).
// Each returned sub-range is a clamp a caller has already established
// overlaps (hardConfinedRangeOverlaps) and needs scoped to exactly its own
// confined portion, e.g. for a dedicated count-only sub-query.
func hardConfinedOverlapRanges(start, end []byte) [][2][]byte {
	var out [][2][]byte
	for i, p := range hardConfinedAllPrefixes {
		pEnd := hardConfinedPrefixEnds[i]
		overlaps := false
		if len(end) == 0 {
			overlaps = strings.HasPrefix(string(start), p)
		} else {
			overlaps = string(start) < pEnd && string(end) > p
		}
		if !overlaps {
			continue
		}
		clampedStart := start
		if string(start) < p {
			clampedStart = []byte(p)
		}
		clampedEnd := end
		if len(end) == 0 || string(end) > pEnd {
			clampedEnd = []byte(pEnd)
		}
		out = append(out, [2][]byte{clampedStart, clampedEnd})
	}
	return out
}

// isHardConfinedKeyForScan reports whether key must be stripped from an
// enumeration whose own query root was scanPrefix. Every "material"
// record is always stripped (isHardConfinedKey, unconditionally -- a
// scan never has a legitimate reason to see a CA's private key). A
// "certificate" record is stripped TOO, but ONLY when scanPrefix is
// broader than (an ancestor of) the confined CA root key belongs to --
// "__sys:" itself, or any other sweep wider than the CA subsystem's own
// namespace -- never when scanPrefix is at or below that root, which is
// the CA subsystem's own narrow self-scan
// (pkg/cert.DefaultCertProvider.CACertPEMs' tenant-discovery
// enumeration) or an embedded caller naming the CA's root directly,
// whose certificate confinement never protected from a point Get either
// (it is public data; see this file's own doc comment above
// hardConfinedPrefixes). A key under hardConfinedWholePrefixes follows
// the certificate rule: stripped from a broader sweep, left in pkg/cert's
// own narrow self-scan (ListDelegationPolicies), which the embedded
// decorators never reach because they refuse such a scan root outright.
func isHardConfinedKeyForScan(key, scanPrefix string) bool {
	if isHardConfinedMaterialKey(key) {
		return true
	}
	for _, p := range hardConfinedAllPrefixes {
		if !strings.HasPrefix(key, p) {
			continue
		}
		if !strings.HasPrefix(scanPrefix, p) {
			return true
		}
	}
	return false
}

// filterHardConfinedKeys returns a new slice with every key
// isHardConfinedKeyForScan refuses for scanPrefix removed, for
// FilterInternalKeys' owner-escape branch (internal_keys.go) and for the
// embedded-transport decorators' List/Scan overrides.
func filterHardConfinedKeys(keys []string, scanPrefix string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if !isHardConfinedKeyForScan(k, scanPrefix) {
			out = append(out, k)
		}
	}
	return out
}

// filterHardConfinedMap removes every key isHardConfinedKeyForScan
// refuses for scanPrefix from data in place and returns it, for
// FilterInternalMap's owner-escape branch (internal_keys.go) and for the
// embedded-transport decorators' Scan overrides.
func filterHardConfinedMap(data map[string][]byte, scanPrefix string) map[string][]byte {
	for k := range data {
		if isHardConfinedKeyForScan(k, scanPrefix) {
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

// Error returns a human-readable description. A key under
// hardConfinedWholePrefixes is reported as its confined namespace only:
// a delegation policy's key names a tenant and a principal, which the
// refusal does not echo back.
func (e *HardConfinedKeyError) Error() string {
	if p := hardConfinedWholePrefix(e.Key); p != "" {
		return "service: " + p + " is not reachable through this surface"
	}
	return "service: " + e.Key + " is not reachable through this surface"
}
