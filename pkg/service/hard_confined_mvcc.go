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

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// hardConfinedMVCC wraps an MVCCKVServicer, extending hardConfinedKV's
// point-key and enumeration refusals (promoted through embedding, since
// MVCCKVServicer embeds KVServicer) with the same treatment over this
// interface's own revision-aware reads and ranges: a point-shaped read
// (GetRevision and friends) is refused outright, and Range strips the
// hard-confined key out of its result rather than refusing the whole
// call, matching hardConfinedKV's List/Scan.
//
// RestoreToRevision needs no override: it already refuses the entire
// internal key namespace unconditionally (see RestoreInternalKeyError and
// mvcc_restore.go), which is a superset of the hard-confined namespace.
// Compact and CurrentRevision take no key and need none either.
//
// KeepsRevisionHistory, CompactionPolicy and SetCompactionPolicy are
// forwarded to the wrapped servicer below, mirroring pkg/server's
// quotaMVCCService, which solves the identical problem for quota
// accounting: RevisionHistoryReporter and CompactionPolicyHolder are
// OPTIONAL capabilities other code discovers with a type assertion
// against whatever value it was handed, and wrapping unconditionally
// without forwarding them would silently break compaction-policy support
// for anything wrapped by this type.
type hardConfinedMVCC struct {
	hardConfinedKV
	mvcc MVCCKVServicer
}

// Compile-time interface checks.
var (
	_ CompactionPolicyHolder  = (*hardConfinedMVCC)(nil)
	_ RevisionHistoryReporter = (*hardConfinedMVCC)(nil)
)

// NewHardConfinedMVCC wraps svc, or returns nil unchanged so a resolver
// that found no service configured for a scope keeps reporting that
// rather than a wrapped nil satisfying the interface.
func NewHardConfinedMVCC(svc MVCCKVServicer) MVCCKVServicer {
	if svc == nil {
		return nil
	}
	return &hardConfinedMVCC{hardConfinedKV: hardConfinedKV{KVServicer: svc}, mvcc: svc}
}

// KeepsRevisionHistory forwards to the wrapped servicer; one that cannot
// report is assumed to keep history, matching quotaMVCCService's default.
func (h *hardConfinedMVCC) KeepsRevisionHistory() bool {
	r, ok := h.mvcc.(RevisionHistoryReporter)
	return !ok || r.KeepsRevisionHistory()
}

// CompactionPolicy forwards to the wrapped servicer: a policy is no
// storage, so there is nothing of the cluster CA's for this wrapper to
// confine here.
func (h *hardConfinedMVCC) CompactionPolicy(ctx context.Context) (*compaction.Policy, error) {
	p, ok := h.mvcc.(CompactionPolicyHolder)
	if !ok {
		return nil, nil
	}
	return p.CompactionPolicy(ctx)
}

// SetCompactionPolicy forwards to the wrapped servicer.
func (h *hardConfinedMVCC) SetCompactionPolicy(ctx context.Context, policy *compaction.Policy) error {
	p, ok := h.mvcc.(CompactionPolicyHolder)
	if !ok {
		return &CompactionPolicyUnavailableError{}
	}
	return p.SetCompactionPolicy(ctx, policy)
}

func (h *hardConfinedMVCC) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64) (*KeyValue, error) {
	if isHardConfinedKey(key) {
		return nil, &HardConfinedKeyError{Key: key}
	}
	return h.mvcc.PutWithLease(ctx, key, value, leaseID)
}

func (h *hardConfinedMVCC) PutWithLeaseAndMode(ctx context.Context, key string, value []byte, leaseID int64, mode writemode.Mode) (*KeyValue, error) {
	if isHardConfinedKey(key) {
		return nil, &HardConfinedKeyError{Key: key}
	}
	return h.mvcc.PutWithLeaseAndMode(ctx, key, value, leaseID, mode)
}

func (h *hardConfinedMVCC) GetRevision(ctx context.Context, key string) (*KeyValue, error) {
	if isHardConfinedKey(key) {
		return nil, &HardConfinedKeyError{Key: key}
	}
	return h.mvcc.GetRevision(ctx, key)
}

func (h *hardConfinedMVCC) GetRevisionWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) (*KeyValue, error) {
	if isHardConfinedKey(key) {
		return nil, &HardConfinedKeyError{Key: key}
	}
	return h.mvcc.GetRevisionWithConsistency(ctx, key, opts)
}

func (h *hardConfinedMVCC) GetAtRevision(ctx context.Context, key string, revision int64) (*KeyValue, error) {
	if isHardConfinedKey(key) {
		return nil, &HardConfinedKeyError{Key: key}
	}
	return h.mvcc.GetAtRevision(ctx, key, revision)
}

func (h *hardConfinedMVCC) GetAtRevisionWithConsistency(ctx context.Context, key string, revision int64, opts consistency.ReadOptions) (*KeyValue, error) {
	if isHardConfinedKey(key) {
		return nil, &HardConfinedKeyError{Key: key}
	}
	return h.mvcc.GetAtRevisionWithConsistency(ctx, key, revision, opts)
}

// Range filters the hard-confined key out of its result, mirroring
// hardConfinedKV's List/Scan: a wide-open "give me everything" range --
// which, over this UN-namespaced servicer, genuinely reaches
// "__sys:cluster_ca/..." in the underlying store, unlike a namespaced
// tenant view -- must not refuse outright just because that key happens
// to lie within it. The one exception is the point form (end empty, per
// MVCCKVServicer.Range's own "only the key at start" convention): that is
// a single-key read like Get, with nothing to filter, so it is refused
// outright instead.
//
// MVCCKVServicer.Range returns (kvs, count, revision, err) -- count
// BEFORE revision, per its own doc comment and MVCCKVService.Range's
// literal `return rr.KeyValues, rr.Count, rr.Revision, nil`. Binding them
// in the other order compiles (both are int64) and passed the review
// delta's own tests, because the fake test double encoded the identical
// swap; a live cell caught it by reporting the wrong revision.
//
// Count is corrected, not merely left at the underlying answer: a
// CountOnly caller (WithCountOnly, which returns no key-values at all)
// would otherwise learn whether the confined key falls within [start, end)
// purely from how the count changes across ranges, without ever seeing a
// key-value to filter. confinedCountInRange issues a second, bounded
// CountOnly query restricted to the overlap with the confined namespace --
// skipped entirely when [start, end) doesn't reach that namespace at all,
// which is every ordinary application range -- carrying the caller's own
// WithRevision through to it (a historical range must subtract only the
// confined records that existed AT that revision, not however many exist
// now), and that sub-query's count, not the filtered-slice length, is
// what gets subtracted, so it is correct whether or not this call itself
// was CountOnly.
func (h *hardConfinedMVCC) Range(ctx context.Context, start, end []byte, opts ...RangeOption) ([]*KeyValue, int64, int64, error) {
	if len(end) == 0 && isHardConfinedKey(string(start)) {
		return nil, 0, 0, &HardConfinedKeyError{Key: string(start)}
	}
	kvs, count, revision, err := h.mvcc.Range(ctx, start, end, opts...)
	if err != nil {
		return nil, 0, 0, err
	}
	confined, err := h.confinedCountInRange(ctx, start, end, opts, func(ctx context.Context, s, e []byte, ro ...RangeOption) ([]*KeyValue, int64, int64, error) {
		return h.mvcc.Range(ctx, s, e, ro...)
	})
	if err != nil {
		return nil, 0, 0, err
	}
	return filterHardConfinedKeyValues(kvs), count - confined, revision, nil
}

func (h *hardConfinedMVCC) RangeWithConsistency(ctx context.Context, start, end []byte, opts consistency.ReadOptions, rangeOpts ...RangeOption) ([]*KeyValue, int64, int64, error) {
	if len(end) == 0 && isHardConfinedKey(string(start)) {
		return nil, 0, 0, &HardConfinedKeyError{Key: string(start)}
	}
	kvs, count, revision, err := h.mvcc.RangeWithConsistency(ctx, start, end, opts, rangeOpts...)
	if err != nil {
		return nil, 0, 0, err
	}
	confined, err := h.confinedCountInRange(ctx, start, end, rangeOpts, func(ctx context.Context, s, e []byte, ro ...RangeOption) ([]*KeyValue, int64, int64, error) {
		return h.mvcc.RangeWithConsistency(ctx, s, e, opts, ro...)
	})
	if err != nil {
		return nil, 0, 0, err
	}
	return filterHardConfinedKeyValues(kvs), count - confined, revision, nil
}

// confinedCountInRange returns how many hard-confined keys fall within
// [start, end), via a dedicated WithCountOnly sub-query restricted to the
// overlap of [start, end) with the confined namespace -- issued only when
// there is an overlap at all, so an ordinary application range that never
// touches "__sys:cluster_ca/" costs nothing beyond the overlap check.
// callerOpts' own WithRevision (if any) is carried through to the
// sub-query unchanged, so a historical range (GetAtRevision-shaped)
// subtracts only the confined records that existed at THAT revision, not
// however many exist at the current one.
func (h *hardConfinedMVCC) confinedCountInRange(ctx context.Context, start, end []byte, callerOpts []RangeOption, rangeFn func(ctx context.Context, start, end []byte, opts ...RangeOption) ([]*KeyValue, int64, int64, error)) (int64, error) {
	if !hardConfinedRangeOverlaps(string(start), string(end)) {
		return 0, nil
	}
	clampedStart, clampedEnd := clampRangeToHardConfined(start, end)
	subOpts := []RangeOption{WithCountOnly()}
	if resolved := ApplyRangeOptions(callerOpts); resolved.Revision != 0 {
		subOpts = append(subOpts, WithRevision(resolved.Revision))
	}
	_, confined, _, err := rangeFn(ctx, clampedStart, clampedEnd, subOpts...)
	if err != nil {
		return 0, err
	}
	return confined, nil
}

// filterHardConfinedKeyValues returns a new slice with any *KeyValue whose
// Key lies in the hard-confined namespace removed.
func filterHardConfinedKeyValues(kvs []*KeyValue) []*KeyValue {
	out := make([]*KeyValue, 0, len(kvs))
	for _, kv := range kvs {
		if kv == nil || isHardConfinedKey(string(kv.Key)) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func (h *hardConfinedMVCC) Compact(ctx context.Context, revision int64) error {
	return h.mvcc.Compact(ctx, revision)
}

func (h *hardConfinedMVCC) CurrentRevision(ctx context.Context) (int64, error) {
	return h.mvcc.CurrentRevision(ctx)
}

func (h *hardConfinedMVCC) RestoreToRevision(ctx context.Context, req *RestoreRevisionRequest) (*RestoreRevisionResult, error) {
	return h.mvcc.RestoreToRevision(ctx, req)
}
