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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// fakeMVCC is a minimal MVCCKVServicer double. It embeds fakeKV for the
// inherited KVServicer surface (already exercised by
// hard_confined_kv_test.go) and implements every MVCCKVServicer-specific
// method directly -- NOT by also embedding MVCCKVServicer, which would
// make every promoted KVServicer method ambiguous between the two
// embedded fields (fakeKV satisfies KVServicer on its own, and
// MVCCKVServicer embeds KVServicer too).
type fakeMVCC struct {
	*fakeKV
	calledRange bool
}

func newFakeMVCC() *fakeMVCC { return &fakeMVCC{fakeKV: newFakeKV()} }

// Compile-time interface check.
var _ MVCCKVServicer = (*fakeMVCC)(nil)

func (f *fakeMVCC) PutWithLease(_ context.Context, key string, value []byte, _ int64) (*KeyValue, error) {
	f.store[key] = value
	return &KeyValue{Key: []byte(key), Value: value}, nil
}

func (f *fakeMVCC) PutWithLeaseAndMode(ctx context.Context, key string, value []byte, leaseID int64, _ writemode.Mode) (*KeyValue, error) {
	return f.PutWithLease(ctx, key, value, leaseID)
}

func (f *fakeMVCC) GetRevision(_ context.Context, key string) (*KeyValue, error) {
	return &KeyValue{Key: []byte(key), Value: f.store[key]}, nil
}

func (f *fakeMVCC) GetRevisionWithConsistency(_ context.Context, key string, _ consistency.ReadOptions) (*KeyValue, error) {
	return &KeyValue{Key: []byte(key), Value: f.store[key]}, nil
}

func (f *fakeMVCC) GetAtRevision(_ context.Context, key string, _ int64) (*KeyValue, error) {
	return &KeyValue{Key: []byte(key), Value: f.store[key]}, nil
}

func (f *fakeMVCC) GetAtRevisionWithConsistency(_ context.Context, key string, _ int64, _ consistency.ReadOptions) (*KeyValue, error) {
	return &KeyValue{Key: []byte(key), Value: f.store[key]}, nil
}

// Range filters the fake's store by the half-open interval [start, end),
// matching MVCCKVServicer.Range's own point-form convention (an empty end
// means only the key at start) -- real enough range semantics for
// confinedCountInRange's own sub-range CountOnly query against this fake
// to be meaningful, not just the decorator's post-hoc filter of an
// unconditionally-full answer.
func (f *fakeMVCC) Range(_ context.Context, start, end []byte, opts ...RangeOption) ([]*KeyValue, int64, int64, error) {
	f.calledRange = true
	ro := ApplyRangeOptions(opts)

	var matched []*KeyValue
	if len(end) == 0 {
		if v, ok := f.store[string(start)]; ok {
			matched = append(matched, &KeyValue{Key: start, Value: v})
		}
	} else {
		for k, v := range f.store {
			if k >= string(start) && k < string(end) {
				matched = append(matched, &KeyValue{Key: []byte(k), Value: v})
			}
		}
	}

	total := int64(len(matched))
	if ro.CountOnly {
		return nil, 1, total, nil
	}
	return matched, 1, total, nil
}

func (f *fakeMVCC) RangeWithConsistency(ctx context.Context, start, end []byte, _ consistency.ReadOptions, opts ...RangeOption) ([]*KeyValue, int64, int64, error) {
	return f.Range(ctx, start, end, opts...)
}

func (f *fakeMVCC) Compact(_ context.Context, _ int64) error { return nil }

func (f *fakeMVCC) CurrentRevision(_ context.Context) (int64, error) { return 1, nil }

func (f *fakeMVCC) RestoreToRevision(_ context.Context, _ *RestoreRevisionRequest) (*RestoreRevisionResult, error) {
	return &RestoreRevisionResult{}, nil
}

func TestNewHardConfinedMVCC_NilServicePassesThroughNil(t *testing.T) {
	if got := NewHardConfinedMVCC(nil); got != nil {
		t.Fatalf("NewHardConfinedMVCC(nil) = %v, want nil", got)
	}
}

func TestHardConfinedMVCC_RevisionMethodsRefuseTheConfinedKey(t *testing.T) {
	ctx := context.Background()
	fake := newFakeMVCC()
	fake.store[confinedKey] = []byte("the cluster CA's private key")
	mvcc := NewHardConfinedMVCC(fake)

	assertConfined := func(t *testing.T, err error) {
		t.Helper()
		var confErr *HardConfinedKeyError
		if !errors.As(err, &confErr) {
			t.Fatalf("error = %v (%T), want *HardConfinedKeyError", err, err)
		}
	}

	if _, err := mvcc.GetRevision(ctx, confinedKey); err == nil {
		t.Fatal("GetRevision succeeded for the confined key, want a refusal")
	} else {
		assertConfined(t, err)
	}
	if _, err := mvcc.GetRevisionWithConsistency(ctx, confinedKey, consistency.ReadOptions{}); err == nil {
		t.Fatal("GetRevisionWithConsistency succeeded for the confined key, want a refusal")
	} else {
		assertConfined(t, err)
	}
	if _, err := mvcc.GetAtRevision(ctx, confinedKey, 1); err == nil {
		t.Fatal("GetAtRevision succeeded for the confined key, want a refusal")
	} else {
		assertConfined(t, err)
	}
	if _, err := mvcc.GetAtRevisionWithConsistency(ctx, confinedKey, 1, consistency.ReadOptions{}); err == nil {
		t.Fatal("GetAtRevisionWithConsistency succeeded for the confined key, want a refusal")
	} else {
		assertConfined(t, err)
	}
	if _, err := mvcc.PutWithLease(ctx, confinedKey, []byte("x"), 1); err == nil {
		t.Fatal("PutWithLease succeeded for the confined key, want a refusal")
	} else {
		assertConfined(t, err)
	}
}

// TestHardConfinedMVCC_RangeFiltersTheConfinedKeyFromAWideOpenSweep is the
// B2 regression proof: Range over this UN-namespaced servicer must not
// surface "__sys:cluster_ca/..." even for a request that would otherwise
// return everything ("from revision 1", the reviewer's own probe shape),
// and it must not refuse the whole call just because that key is present
// -- the false-positive go-qrdb#555's registry-level attempt made.
func TestHardConfinedMVCC_RangeFiltersTheConfinedKeyFromAWideOpenSweep(t *testing.T) {
	ctx := context.Background()
	fake := newFakeMVCC()
	fake.store[confinedKey] = []byte("the cluster CA's private key")
	fake.store["ordinary/a"] = []byte("1")
	mvcc := NewHardConfinedMVCC(fake)

	kvs, revision, total, err := mvcc.Range(ctx, []byte(""), []byte("\xff"), WithRevision(1))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if !fake.calledRange {
		t.Fatal("Range did not reach the underlying servicer")
	}
	if revision != 1 {
		t.Fatalf("Range revision = %d, want 1 (the underlying servicer's own answer, unfiltered)", revision)
	}
	_ = total
	for _, kv := range kvs {
		if string(kv.Key) == confinedKey {
			t.Fatal("Range returned the confined key")
		}
	}
	if len(kvs) != 1 || string(kvs[0].Key) != "ordinary/a" {
		t.Fatalf("Range = %v, want exactly the non-confined key", kvs)
	}
}

// TestHardConfinedMVCC_RangePointFormRefusesTheConfinedKey proves Range's
// point form (end empty, "only the key at start" per
// MVCCKVServicer.Range's own convention) is refused outright like Get,
// rather than filtered down to an empty success -- the same distinction
// hardConfinedKV draws between a point read and an enumeration.
func TestHardConfinedMVCC_RangePointFormRefusesTheConfinedKey(t *testing.T) {
	ctx := context.Background()
	fake := newFakeMVCC()
	fake.store[confinedKey] = []byte("the cluster CA's private key")
	mvcc := NewHardConfinedMVCC(fake)

	_, _, _, err := mvcc.Range(ctx, []byte(confinedKey), nil)
	var confErr *HardConfinedKeyError
	if !errors.As(err, &confErr) {
		t.Fatalf("Range(confinedKey, nil) = %v, want *HardConfinedKeyError", err)
	}
	if fake.calledRange {
		t.Fatal("Range(confinedKey, nil) reached the underlying servicer; the point form must refuse before delegating")
	}
}

// TestHardConfinedMVCC_RangeCountOnlyDoesNotLeakTheConfinedKeysPresence is
// the B2c regression proof: a CountOnly range returns no key-values for
// the decorator's own filter to act on, so without correcting the count
// itself, a caller could learn whether the confined key falls inside
// [start, end) purely from how the total changes -- the exact binary-search
// shape a transaction compare's refusal (not filter) already exists to
// deny on a different surface.
func TestHardConfinedMVCC_RangeCountOnlyDoesNotLeakTheConfinedKeysPresence(t *testing.T) {
	ctx := context.Background()
	fake := newFakeMVCC()
	fake.store[confinedKey] = []byte("the cluster CA's private key")
	fake.store["ordinary/a"] = []byte("1")
	fake.store["ordinary/b"] = []byte("2")
	mvcc := NewHardConfinedMVCC(fake)

	// A wide-open CountOnly range: the raw total (asked of the fake
	// directly, bypassing the decorator) is 3; the decorator's answer must
	// be 2, the count with the confined key excluded.
	_, _, rawTotal, err := fake.Range(ctx, []byte(""), []byte("\xff"), WithCountOnly())
	if err != nil || rawTotal != 3 {
		t.Fatalf("sanity check: fake.Range raw total = %d, %v; want 3, nil", rawTotal, err)
	}

	_, _, total, err := mvcc.Range(ctx, []byte(""), []byte("\xff"), WithCountOnly())
	if err != nil {
		t.Fatalf("Range(CountOnly): %v", err)
	}
	if total != 2 {
		t.Fatalf("Range(CountOnly) total = %d, want 2 (the confined key excluded, not merely absent from a result slice that was never returned)", total)
	}

	// A range that does not reach the confined namespace at all must not
	// pay for -- or alter the answer with -- the extra sub-query.
	_, _, narrowTotal, err := mvcc.Range(ctx, []byte("ordinary/"), []byte("ordinary0"), WithCountOnly())
	if err != nil {
		t.Fatalf("Range(ordinary/, CountOnly): %v", err)
	}
	if narrowTotal != 2 {
		t.Fatalf("Range(ordinary/, CountOnly) total = %d, want 2 (unaffected by a namespace it never overlaps)", narrowTotal)
	}
}

func TestHardConfinedMVCC_UnkeyedMethodsPassThrough(t *testing.T) {
	ctx := context.Background()
	mvcc := NewHardConfinedMVCC(newFakeMVCC())

	if err := mvcc.Compact(ctx, 1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if rev, err := mvcc.CurrentRevision(ctx); err != nil || rev != 1 {
		t.Fatalf("CurrentRevision = %v, %v; want 1, nil", rev, err)
	}
}

// fakeMVCCWithCompactionPolicy adds CompactionPolicyHolder and
// RevisionHistoryReporter to fakeMVCC, the two OPTIONAL capabilities
// pkg/service/compaction_state.go's registryCompactionPolicies.holder
// discovers with a type assertion against whatever value it was handed.
// NewHardConfinedMVCC's wrapper must forward both, or compaction policy
// support silently disappears for anything wrapped by it.
type fakeMVCCWithCompactionPolicy struct {
	*fakeMVCC
	policy       *compaction.Policy
	keepsHistory bool
}

func (f *fakeMVCCWithCompactionPolicy) KeepsRevisionHistory() bool { return f.keepsHistory }

func (f *fakeMVCCWithCompactionPolicy) CompactionPolicy(_ context.Context) (*compaction.Policy, error) {
	return f.policy, nil
}

func (f *fakeMVCCWithCompactionPolicy) SetCompactionPolicy(_ context.Context, policy *compaction.Policy) error {
	f.policy = policy
	return nil
}

func TestHardConfinedMVCC_ForwardsCompactionPolicyCapability(t *testing.T) {
	ctx := context.Background()
	fake := &fakeMVCCWithCompactionPolicy{fakeMVCC: newFakeMVCC(), keepsHistory: true}
	mvcc := NewHardConfinedMVCC(fake)

	holder, ok := mvcc.(CompactionPolicyHolder)
	if !ok {
		t.Fatal("NewHardConfinedMVCC's wrapper does not satisfy CompactionPolicyHolder")
	}
	reporter, ok := mvcc.(RevisionHistoryReporter)
	if !ok {
		t.Fatal("NewHardConfinedMVCC's wrapper does not satisfy RevisionHistoryReporter")
	}
	if !reporter.KeepsRevisionHistory() {
		t.Fatal("KeepsRevisionHistory did not forward to the wrapped servicer's true answer")
	}

	policy := &compaction.Policy{}
	if err := holder.SetCompactionPolicy(ctx, policy); err != nil {
		t.Fatalf("SetCompactionPolicy: %v", err)
	}
	if fake.policy != policy {
		t.Fatal("SetCompactionPolicy did not reach the wrapped servicer")
	}
	got, err := holder.CompactionPolicy(ctx)
	if err != nil || got != policy {
		t.Fatalf("CompactionPolicy = %v, %v; want the policy just set, nil", got, err)
	}
}

func TestHardConfinedMVCC_CompactionPolicyDegradesGracefullyWithoutTheCapability(t *testing.T) {
	ctx := context.Background()
	mvcc := NewHardConfinedMVCC(newFakeMVCC())

	reporter, ok := mvcc.(RevisionHistoryReporter)
	if !ok {
		t.Fatal("the wrapper itself must still satisfy RevisionHistoryReporter even when the inner servicer does not")
	}
	if !reporter.KeepsRevisionHistory() {
		t.Fatal("KeepsRevisionHistory without the capability must default to true (matches quotaMVCCService's own default)")
	}

	holder, ok := mvcc.(CompactionPolicyHolder)
	if !ok {
		t.Fatal("the wrapper itself must still satisfy CompactionPolicyHolder even when the inner servicer does not")
	}
	if policy, err := holder.CompactionPolicy(ctx); err != nil || policy != nil {
		t.Fatalf("CompactionPolicy without the capability = %v, %v; want nil, nil", policy, err)
	}
	var unavailable *CompactionPolicyUnavailableError
	if err := holder.SetCompactionPolicy(ctx, &compaction.Policy{}); !errors.As(err, &unavailable) {
		t.Fatalf("SetCompactionPolicy without the capability = %v, want *CompactionPolicyUnavailableError", err)
	}
}
