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

package featuregate

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/release"
)

// advertiseRelease registers a live advertisement for node id carrying
// every supported feature and the given release.
func (s *leaseStore) advertiseRelease(t *testing.T, id uint64, addr, rel string) {
	t.Helper()
	a, err := cluster.NewAdvertiser(s, cluster.Member{
		NodeID: id, Address: addr, Features: SupportedNames(), ReleaseVersion: rel,
	}, cluster.AdvertiseConfig{})
	require.NoError(t, err)
	require.NoError(t, a.Register(context.Background()))
}

func TestAdmitReplica_RefusesVersionGap(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.58.0-alpha"))
	store, kv := newLeaseStore(), newMemKV()
	store.advertiseRelease(t, 2, "n2:7000", "0.59.0-alpha")
	store.advertiseRelease(t, 3, "n3:7000", "0.59.0-alpha")
	store.advertiseRelease(t, 4, "n4:7000", "0.60.0-alpha")
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, store))

	var gap *release.GapError
	err := g.AdmitReplica(dedicatedShard, "n4:7000")
	require.ErrorAs(t, err, &gap)
	assert.Equal(t, release.GapError{Channel: "raft-membership", Local: "0.58.0-alpha", Peer: "0.60.0-alpha"}, *gap)
	// The system shard admits on features alone, but never across a gap.
	require.ErrorAs(t, g.AdmitReplica(systemShard, "n4:7000"), &gap)
	assert.NoError(t, g.AdmitReplica(dedicatedShard, "n2:7000"))
}

func TestAdmitReplica_AcceptsAdjacentAndUnannounced(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	store, kv := newLeaseStore(), newMemKV()
	store.advertiseRelease(t, 2, "n2:7000", "0.58.3-alpha")
	store.advertiseRelease(t, 3, "n3:7000", "") // predates announcement
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, store))

	assert.NoError(t, g.AdmitReplica(dedicatedShard, "n2:7000"))
	assert.NoError(t, g.AdmitReplica(dedicatedShard, "n3:7000"))
	assert.NoError(t, g.AdmitReplica(dedicatedShard, "n1:7000"), "this node admits itself")
}

// TestAdmitReplica_RefusesGapToAnyMember: admission through the 0.59
// member of a mixed {0.58, 0.59} cluster refuses a 0.60 candidate, which is
// adjacent to the admitting node but not to the 0.58 member, and admits a
// 0.59 candidate.
func TestAdmitReplica_RefusesGapToAnyMember(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	store, kv := newLeaseStore(), newMemKV()
	store.advertiseRelease(t, 2, "n2:7000", "0.58.0-alpha")
	store.advertiseRelease(t, 3, "n3:7000", "0.60.0-alpha")
	store.advertiseRelease(t, 4, "n4:7000", "0.59.1-alpha")
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, store))

	var gap *release.GapError
	require.ErrorAs(t, g.AdmitReplica(dedicatedShard, "n3:7000"), &gap)
	assert.Equal(t, release.GapError{Channel: "raft-membership", Local: "0.58.0-alpha", Peer: "0.60.0-alpha"}, *gap)
	require.ErrorAs(t, g.AdmitReplica(systemShard, "n3:7000"), &gap)
	assert.NoError(t, g.AdmitReplica(dedicatedShard, "n4:7000"))
}

// A committed member whose advertisement lease lapsed (it is offline) still
// holds its release: the gate compares a joiner against the release the
// member recorded in the replicated KV.
func TestAdmitReplica_OfflineMemberReleaseIsKept(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	store, kv := newLeaseStore(), newMemKV()
	store.advertiseRelease(t, 2, "n2:7000", "0.58.0-alpha")
	store.advertiseRelease(t, 3, "n3:7000", "0.59.0-alpha")
	_, err := kv.PutIfAbsent(context.Background(), ReleasePrefix+"2/0.58.0-alpha", []byte("n2:7000"))
	require.NoError(t, err)
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, store))
	store.expire(2)
	store.advertiseRelease(t, 4, "n4:7000", "0.60.0-alpha")
	require.NoError(t, g.reseed(context.Background(), 0, store))

	var gap *release.GapError
	require.ErrorAs(t, g.AdmitReplica(systemShard, "n4:7000"), &gap)
	assert.Equal(t, "0.58.0-alpha", gap.Local)

	store.advertiseRelease(t, 4, "n4:7000", "0.59.0-alpha")
	require.NoError(t, g.reseed(context.Background(), 0, store))
	assert.NoError(t, g.AdmitReplica(systemShard, "n4:7000"))
}

// With neither a live advertisement nor a recorded release for a committed
// member, the gate cannot establish adjacency and refuses.
func TestAdmitReplica_UnknownMemberReleaseRefuses(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	store, kv := newLeaseStore(), newMemKV()
	store.advertiseRelease(t, 2, "n2:7000", "0.58.0-alpha")
	store.advertiseRelease(t, 3, "n3:7000", "0.59.0-alpha")
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, store))
	store.expire(2)
	store.advertiseRelease(t, 4, "n4:7000", "0.60.0-alpha")
	require.NoError(t, g.reseed(context.Background(), 0, store))

	var unknown *UnknownMemberReleaseError
	require.ErrorAs(t, g.AdmitReplica(systemShard, "n4:7000"), &unknown)
	assert.Equal(t, UnknownMemberReleaseError{ShardID: systemShard, ReplicaID: 2, Address: "n2:7000"}, *unknown)
}

// A candidate is compared only with committed system-shard members: two
// joiners that are each adjacent to the cluster but two releases apart from
// one another do not block each other.
func TestAdmitReplica_PreMembershipCandidatesDoNotBlockEachOther(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	store, kv := newLeaseStore(), newMemKV()
	store.advertiseRelease(t, 2, "n2:7000", "0.59.0-alpha")
	store.advertiseRelease(t, 3, "n3:7000", "0.59.0-alpha")
	store.advertiseRelease(t, 4, "n4:7000", "0.58.0-alpha")
	store.advertiseRelease(t, 5, "n5:7000", "0.60.0-alpha")
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, store))

	assert.NoError(t, g.AdmitReplica(systemShard, "n4:7000"))
	assert.NoError(t, g.AdmitReplica(systemShard, "n5:7000"))
}

// The gate records its own release under ReleasePrefix and drops a stale
// one left by an earlier binary.
func TestGate_RecordsOwnRelease(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	store, kv := newLeaseStore(), newMemKV()
	_, err := kv.PutIfAbsent(context.Background(), ReleasePrefix+"1/0.58.0-alpha", []byte("n1:7000"))
	require.NoError(t, err)
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, store))
	assert.True(t, kv.has(ReleasePrefix+"1/0.59.0-alpha"))
	assert.False(t, kv.has(ReleasePrefix+"1/0.58.0-alpha"))
}

// TestStart_RefusesExistingMemberRestartAcrossAGap: committed member 1 of a
// 0.59 cluster, last run on 0.58, restarts its data directory on a 0.60
// binary -- an upgrade that skips 0.59. Admission never sees it (it is no
// membership addition), so Start refuses it with the admission's typed gap
// error naming the release it last ran, whether the other members' release
// comes from a live advertisement or, offline, from the release they
// recorded -- and records nothing for the refused binary.
func TestStart_RefusesExistingMemberRestartAcrossAGap(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.60.0-alpha"))
	for name, live := range map[string]bool{"live advertisement": true, "recorded release": false} {
		t.Run(name, func(t *testing.T) {
			store, kv := newLeaseStore(), newMemKV()
			for id, addr := range map[uint64]string{1: "n1:7000", 2: "n2:7000", 3: "n3:7000"} {
				rel := "0.59.0-alpha"
				if id == 1 {
					rel = "0.58.0-alpha"
				}
				_, err := kv.PutIfAbsent(context.Background(), ReleasePrefix+strconv.FormatUint(id, 10)+"/"+rel, []byte(addr))
				require.NoError(t, err)
				if live && id != 1 {
					store.advertiseRelease(t, id, addr, rel)
				}
			}
			g := newGate(t, threeNodes())

			var gap *release.GapError
			require.ErrorAs(t, g.Start(context.Background(), kv, store), &gap)
			assert.Equal(t, release.GapError{Channel: "raft-membership", Local: "0.60.0-alpha", Peer: "0.58.0-alpha"}, *gap)
			assert.False(t, kv.has(ReleasePrefix+"1/0.60.0-alpha"), "a refused binary records no release")
			assert.True(t, kv.has(ReleasePrefix+"1/0.58.0-alpha"))
		})
	}
}

// A member advertised from two sources with different releases is compared
// on every release it announces, not on whichever record map order yields.
func TestAdmitReplica_ConflictingAdvertisementsRefuseConservatively(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	t.Run("member", func(t *testing.T) {
		a, b, kv := newLeaseStore(), newLeaseStore(), newMemKV()
		a.advertiseRelease(t, 2, "n2:7000", "0.58.0-alpha")
		b.advertiseRelease(t, 2, "n2:7000", "0.59.0-alpha")
		a.advertiseRelease(t, 3, "n3:7000", "0.59.0-alpha")
		a.advertiseRelease(t, 4, "n4:7000", "0.60.0-alpha")
		g := newGate(t, threeNodes())
		require.NoError(t, g.Start(context.Background(), kv, a, b))
		for range 64 {
			var gap *release.GapError
			require.ErrorAs(t, g.AdmitReplica(systemShard, "n4:7000"), &gap)
			require.Equal(t, release.GapError{Channel: "raft-membership", Local: "0.58.0-alpha", Peer: "0.60.0-alpha"}, *gap)
		}
	})
	t.Run("candidate", func(t *testing.T) {
		a, b, kv := newLeaseStore(), newLeaseStore(), newMemKV()
		a.advertiseRelease(t, 2, "n2:7000", "0.59.0-alpha")
		a.advertiseRelease(t, 3, "n3:7000", "0.59.0-alpha")
		a.advertiseRelease(t, 4, "n4:7000", "0.58.0-alpha")
		b.advertiseRelease(t, 4, "n4:7000", "0.60.0-alpha")
		b.advertiseRelease(t, 5, "n5:7000", "0.60.0-alpha")
		members := threeNodes()
		members.set(systemShard, 3, map[uint64]string{1: "n1:7000", 2: "n2:7000", 3: "n3:7000", 5: "n5:7000"})
		g := newGate(t, members)
		require.NoError(t, g.Start(context.Background(), kv, a, b))
		for range 64 {
			var gap *release.GapError
			require.ErrorAs(t, g.AdmitReplica(systemShard, "n4:7000"), &gap)
			require.Equal(t, release.GapError{Channel: "raft-membership", Local: "0.60.0-alpha", Peer: "0.58.0-alpha"}, *gap)
		}
	})
}

// A restart checks every release a member announces from any source.
func TestStart_ConflictingAdvertisementsRefuseRestart(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.60.0-alpha"))
	for range 32 {
		a, b, kv := newLeaseStore(), newLeaseStore(), newMemKV()
		_, err := kv.PutIfAbsent(context.Background(), ReleasePrefix+"1/0.59.0-alpha", []byte("n1:7000"))
		require.NoError(t, err)
		a.advertiseRelease(t, 2, "n2:7000", "0.58.0-alpha")
		b.advertiseRelease(t, 2, "n2:7000", "0.59.0-alpha")
		a.advertiseRelease(t, 3, "n3:7000", "0.59.0-alpha")
		g := newGate(t, threeNodes())

		var gap *release.GapError
		require.ErrorAs(t, g.Start(context.Background(), kv, a, b), &gap)
		require.Equal(t, release.GapError{Channel: "raft-membership", Local: "0.60.0-alpha", Peer: "0.58.0-alpha"}, *gap)
	}
}

// A node whose state machine has no leases never advertises or Starts its
// gate; EnforceRelease alone still refuses its restart across a gap, keeps
// the refusal, and records an adjacent release.
func TestEnforceRelease_WithoutStartRefusesAGapAndRecordsTheRelease(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.60.0-alpha"))
	kv := newMemKV()
	_, err := kv.PutIfAbsent(context.Background(), ReleasePrefix+"1/0.58.0-alpha", []byte("n1:7000"))
	require.NoError(t, err)
	g := newGate(t, threeNodes())

	var gap *release.GapError
	require.ErrorAs(t, g.EnforceRelease(context.Background(), kv), &gap)
	require.ErrorAs(t, g.Refusal(), &gap)
	assert.False(t, kv.has(ReleasePrefix+"1/0.60.0-alpha"), "a refused binary records no release")

	kv = newMemKV()
	_, err = kv.PutIfAbsent(context.Background(), ReleasePrefix+"1/0.59.0-alpha", []byte("n1:7000"))
	require.NoError(t, err)
	g = newGate(t, threeNodes())
	require.NoError(t, g.EnforceRelease(context.Background(), kv))
	assert.True(t, kv.has(ReleasePrefix+"1/0.60.0-alpha"))
	assert.False(t, kv.has(ReleasePrefix+"1/0.59.0-alpha"))
	assert.NoError(t, g.Refusal())
}

// releaseUnreadableKV fails every read of the recorded releases, local or
// through the leader, while failing is set: a replica whose applied state is
// briefly unreadable at startup and cannot reach a leader either.
type releaseUnreadableKV struct {
	*memKV
	failing atomic.Bool
}

func (k *releaseUnreadableKV) ListStale(ctx context.Context, prefix string) ([]string, error) {
	if prefix == ReleasePrefix && k.failing.Load() {
		return nil, errors.New("applied state unreadable")
	}
	return k.memKV.ListStale(ctx, prefix)
}

func (k *releaseUnreadableKV) ListAll(ctx context.Context, prefix string) ([]string, error) {
	if prefix == ReleasePrefix && k.failing.Load() {
		return nil, errors.New("no leader")
	}
	return k.memKV.ListAll(ctx, prefix)
}

// TestStart_UnreadableReleaseHistoryRefusesRestart: committed member 1, last
// run on 0.58, restarts on a 0.60 binary while its recorded release history
// is briefly unreadable and no member advertises live. The restart check
// must not read the failure as an empty history: Start refuses with a
// *StoreError, and the 0.58 record survives for the retry to judge by.
func TestStart_UnreadableReleaseHistoryRefusesRestart(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.60.0-alpha"))
	store, mem := newLeaseStore(), newMemKV()
	for id, addr := range map[uint64]string{1: "n1:7000", 2: "n2:7000", 3: "n3:7000"} {
		rel := "0.59.0-alpha"
		if id == 1 {
			rel = "0.58.0-alpha"
		}
		_, err := mem.PutIfAbsent(context.Background(), ReleasePrefix+strconv.FormatUint(id, 10)+"/"+rel, []byte(addr))
		require.NoError(t, err)
	}
	kv := &releaseUnreadableKV{memKV: mem}
	kv.failing.Store(true)
	g := newGate(t, threeNodes())

	var storeErr *StoreError
	require.ErrorAs(t, g.Start(context.Background(), kv, store), &storeErr)
	assert.Contains(t, storeErr.Error(), "list releases")
	assert.True(t, mem.has(ReleasePrefix+"1/0.58.0-alpha"), "the release last run survives the refused start")
	assert.False(t, mem.has(ReleasePrefix+"1/0.60.0-alpha"), "a refused binary records no release")
	assert.NoError(t, g.Refusal(), "a read failure is retried, not a refusal")

	// The retry, once the history is readable, refuses the gap, and the
	// gate keeps the refusal for the node's health.
	kv.failing.Store(false)
	var gap *release.GapError
	require.ErrorAs(t, g.Start(context.Background(), kv, store), &gap)
	assert.Equal(t, "0.58.0-alpha", gap.Peer)
	require.ErrorAs(t, g.Refusal(), &gap)
	assert.Equal(t, "0.58.0-alpha", gap.Peer)
}

// TestStart_UnreadableMembershipRefusesRestart: a system-shard membership
// read failure is not "not yet a member": Start refuses rather than skipping
// the restart check.
func TestStart_UnreadableMembershipRefusesRestart(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.60.0-alpha"))
	kv := newMemKV()
	_, err := kv.PutIfAbsent(context.Background(), ReleasePrefix+"1/0.58.0-alpha", []byte("n1:7000"))
	require.NoError(t, err)
	members := threeNodes()
	members.err = errors.New("membership unavailable")
	g := newGate(t, members)
	assert.ErrorIs(t, g.Start(context.Background(), kv, newLeaseStore()), members.err)
	assert.False(t, kv.has(ReleasePrefix+"1/0.60.0-alpha"))
}

// TestStart_AcceptsExistingMemberRestartOnTheNextRelease: committed member 1
// of an all-0.58 cluster restarting on the adjacent 0.59 starts, and
// replaces its recorded release.
func TestStart_AcceptsExistingMemberRestartOnTheNextRelease(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	store, kv := newLeaseStore(), newMemKV()
	for id, addr := range map[uint64]string{1: "n1:7000", 2: "n2:7000", 3: "n3:7000"} {
		_, err := kv.PutIfAbsent(context.Background(), ReleasePrefix+strconv.FormatUint(id, 10)+"/0.58.0-alpha", []byte(addr))
		require.NoError(t, err)
	}
	store.advertiseRelease(t, 2, "n2:7000", "0.58.0-alpha")
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, store))
	assert.True(t, kv.has(ReleasePrefix+"1/0.59.0-alpha"))
	assert.False(t, kv.has(ReleasePrefix+"1/0.58.0-alpha"))
}

// TestAdmitReplica_ReleaseOnlyChangeReplacesTheView: node 3 re-advertising
// with only its release changed (0.59 -> 0.61) replaces the cached view, so
// AdmitReplica refuses it afterwards, whether the change arrives as a watch
// update or through the periodic reseed of a store without a watch.
func TestAdmitReplica_ReleaseOnlyChangeReplacesTheView(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	cases := map[string]func(*leaseStore) (cluster.MembershipStore, *Gate){
		"watch update": func(s *leaseStore) (cluster.MembershipStore, *Gate) { return s, newGate(t, threeNodes()) },
		"reseed":       func(s *leaseStore) (cluster.MembershipStore, *Gate) { return noWatchStore{s}, newFastGate(t) },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			ls, kv := newLeaseStore(), newMemKV()
			ls.advertiseRelease(t, 2, "n2:7000", "0.59.0-alpha")
			ls.advertiseRelease(t, 3, "n3:7000", "0.59.0-alpha")
			store, g := setup(ls)
			require.NoError(t, g.Start(context.Background(), kv, store))
			require.NoError(t, g.AdmitReplica(dedicatedShard, "n3:7000"))

			ls.advertiseRelease(t, 3, "n3:7000", "0.61.0-alpha")
			eventually(t, func() bool {
				var gap *release.GapError
				return errors.As(g.AdmitReplica(dedicatedShard, "n3:7000"), &gap)
			}, "a release-only change did not replace the cached view")
		})
	}
}

// releaseFaultKV fails the release record's delete, or its re-list after
// the write, so recordRelease's store errors are observable.
type releaseFaultKV struct {
	*memKV
	failDelete bool
	failRelist bool
	failPut    bool
	lists      atomic.Int32
}

func (k *releaseFaultKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if k.failPut && strings.HasPrefix(key, ReleasePrefix) {
		return false, errors.New("put refused")
	}
	return k.memKV.PutIfAbsent(ctx, key, value)
}

func (k *releaseFaultKV) Delete(ctx context.Context, key string) error {
	if k.failDelete {
		return errors.New("delete refused")
	}
	return k.memKV.Delete(ctx, key)
}

func (k *releaseFaultKV) ListStale(ctx context.Context, prefix string) ([]string, error) {
	if k.lists.Add(1) > 1 && k.failRelist {
		return nil, errors.New("list refused")
	}
	return k.memKV.ListStale(ctx, prefix)
}

func (k *releaseFaultKV) ListAll(ctx context.Context, prefix string) ([]string, error) {
	if k.lists.Load() > 1 && k.failRelist {
		return nil, errors.New("list refused")
	}
	return k.memKV.ListAll(ctx, prefix)
}

func TestRecordRelease_StoreErrors(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.60.0-alpha"))
	cases := []struct {
		name   string
		kv     func() *releaseFaultKV
		wantOp string
	}{
		{
			name: "stale own record delete fails",
			kv: func() *releaseFaultKV {
				kv := &releaseFaultKV{memKV: newMemKV(), failDelete: true}
				_, err := kv.PutIfAbsent(context.Background(), ReleasePrefix+"1/0.59.0-alpha", []byte("n1:7000"))
				require.NoError(t, err)
				return kv
			},
			wantOp: "delete release",
		},
		{
			name:   "re-list after the record fails",
			kv:     func() *releaseFaultKV { return &releaseFaultKV{memKV: newMemKV(), failRelist: true} },
			wantOp: "list releases",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newGate(t, threeNodes())
			var storeErr *StoreError
			require.ErrorAs(t, g.recordRelease(context.Background(), tc.kv()), &storeErr)
			assert.Equal(t, tc.wantOp, storeErr.Op)
		})
	}
}

// A failed release record never loses the member's history: the new
// release is written before the previous one is removed, and the
// enforcement reports the failure instead of passing.
func TestEnforceRelease_FailedRecordKeepsTheHistoryAndFails(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.60.0-alpha"))
	prev := ReleasePrefix + "1/0.59.0-alpha"
	next := ReleasePrefix + "1/0.60.0-alpha"
	cases := []struct {
		name     string
		kv       *releaseFaultKV
		wantOp   string
		wantNext bool
	}{
		{name: "the new record's write fails", kv: &releaseFaultKV{memKV: newMemKV(), failPut: true}, wantOp: "record release"},
		{name: "the old record's delete fails", kv: &releaseFaultKV{memKV: newMemKV(), failDelete: true}, wantOp: "delete release", wantNext: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.kv.memKV.PutIfAbsent(context.Background(), prev, []byte("n1:7000"))
			require.NoError(t, err)
			g := newGate(t, threeNodes())
			var storeErr *StoreError
			require.ErrorAs(t, g.EnforceRelease(context.Background(), tc.kv), &storeErr)
			assert.Equal(t, tc.wantOp, storeErr.Op)
			assert.True(t, tc.kv.has(prev), "the previous release stays on record")
			assert.Equal(t, tc.wantNext, tc.kv.has(next))
			assert.NoError(t, g.Refusal(), "a store failure is not a release gap")
		})
	}

	// After a failed write, the next binary still meets the last-run
	// release: 0.61 against the kept 0.59 is a gap.
	kv := &releaseFaultKV{memKV: newMemKV(), failPut: true}
	_, err := kv.memKV.PutIfAbsent(context.Background(), prev, []byte("n1:7000"))
	require.NoError(t, err)
	require.Error(t, newGate(t, threeNodes()).EnforceRelease(context.Background(), kv))
	t.Cleanup(release.SetForTesting("0.61.0-alpha"))
	var gap *release.GapError
	require.ErrorAs(t, newGate(t, threeNodes()).EnforceRelease(context.Background(), kv.memKV), &gap)
}

// A joiner that is not yet a member of the system shard has no release to
// record: EnforceRelease passes and leaves the record to the refresh after
// admission, while a store failure for a member stays fatal.
func TestEnforceRelease_NotAMemberIsRetriedAStoreErrorIsFatal(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.60.0-alpha"))
	next := ReleasePrefix + "1/0.60.0-alpha"

	t.Run("not a member yet", func(t *testing.T) {
		m := threeNodes()
		m.set(systemShard, 4, map[uint64]string{2: "n2:7000"})
		kv := &releaseFaultKV{memKV: newMemKV(), failPut: true}
		require.NoError(t, newGate(t, m).EnforceRelease(context.Background(), kv))
		assert.False(t, kv.has(next), "a non-member records nothing")
	})

	t.Run("a member whose record fails", func(t *testing.T) {
		kv := &releaseFaultKV{memKV: newMemKV(), failPut: true}
		var storeErr *StoreError
		require.ErrorAs(t, newGate(t, threeNodes()).EnforceRelease(context.Background(), kv), &storeErr)
		assert.Equal(t, "record release", storeErr.Op)
	})
}

func TestEnforceRelease_NilStoreIsAConfigError(t *testing.T) {
	g := newGate(t, threeNodes())
	var cfg *ConfigError
	require.ErrorAs(t, g.EnforceRelease(context.Background(), nil), &cfg)
	assert.Equal(t, "store", cfg.Field)
}

func TestParseReleases_SkipsMalformedKeys(t *testing.T) {
	got := parseReleases([]string{
		ReleasePrefix + "1/0.60.0-alpha",
		ReleasePrefix + "x/0.60.0-alpha",
		ReleasePrefix + "2",
		ReleasePrefix + "3/",
		"other/4/0.60.0-alpha",
	})
	assert.Equal(t, map[uint64][]string{1: {"0.60.0-alpha"}}, got)
}

func TestUnknownMemberReleaseError_NamesTheMember(t *testing.T) {
	err := &UnknownMemberReleaseError{ReplicaID: 2, Address: "n2:7000"}
	assert.Equal(t, "feature gate: release of system-shard member 2 at n2:7000 cannot be established", err.Error())
}
