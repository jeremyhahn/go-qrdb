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
// to lie within it. Revision and TotalCount reflect the underlying
// servicer's real answer, not the filtered count: a caller that needs an
// exact count re-derives it from the filtered keys if it must.
func (h *hardConfinedMVCC) Range(ctx context.Context, start, end []byte, opts ...RangeOption) ([]*KeyValue, int64, int64, error) {
	kvs, revision, total, err := h.mvcc.Range(ctx, start, end, opts...)
	if err != nil {
		return nil, 0, 0, err
	}
	return filterHardConfinedKeyValues(kvs), revision, total, nil
}

func (h *hardConfinedMVCC) RangeWithConsistency(ctx context.Context, start, end []byte, opts consistency.ReadOptions, rangeOpts ...RangeOption) ([]*KeyValue, int64, int64, error) {
	kvs, revision, total, err := h.mvcc.RangeWithConsistency(ctx, start, end, opts, rangeOpts...)
	if err != nil {
		return nil, 0, 0, err
	}
	return filterHardConfinedKeyValues(kvs), revision, total, nil
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
