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
	store.advertiseRelease(t, 3, "n3:7000", "0.60.0-alpha")
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, store))

	var gap *release.GapError
	err := g.AdmitReplica(dedicatedShard, "n3:7000")
	require.ErrorAs(t, err, &gap)
	assert.Equal(t, release.GapError{Channel: "raft-membership", Local: "0.58.0-alpha", Peer: "0.60.0-alpha"}, *gap)
	// The system shard admits on features alone, but never across a gap.
	require.ErrorAs(t, g.AdmitReplica(systemShard, "n3:7000"), &gap)
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
