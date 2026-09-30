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
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
)

// fixedGroups is a featuregate.MembershipSource over fixed groups.
type fixedGroups map[uint64]featuregate.GroupMembership

func (g fixedGroups) GroupMembership(shardID uint64) (featuregate.GroupMembership, error) {
	m, ok := g[shardID]
	if !ok {
		return featuregate.GroupMembership{}, errors.New("shard not ready")
	}
	return m, nil
}

// fixedAdverts is a cluster.MembershipStore holding a fixed set of live
// advertisements.
type fixedAdverts []cluster.Member

func (fixedAdverts) PutMember(context.Context, uint64, []byte, time.Duration) error { return nil }
func (fixedAdverts) DeleteMember(context.Context, uint64) error                     { return nil }
func (fixedAdverts) WatchMembers(context.Context) (<-chan cluster.MemberEvent, error) {
	return nil, errors.New("no watch")
}
func (a fixedAdverts) ListMembers(context.Context) ([]cluster.Member, error) { return a, nil }

// noActivations is a system store with no activated feature.
type noActivations struct{}

func (noActivations) PutIfAbsent(context.Context, string, []byte) (bool, error) { return false, nil }
func (noActivations) ListAll(context.Context, string) ([]string, error)         { return nil, nil }

// gatedRaftHost is a RaftHost whose admission is the node's real feature
// gate, as qrdbd's raft host adapter's is.
type gatedRaftHost struct {
	*fakeRaftHost
	gate *featuregate.Gate
}

func (h *gatedRaftHost) AdmitReplica(shardID uint64, raftAddr string) error {
	return h.gate.AdmitReplica(shardID, raftAddr)
}

var _ ReplicaAdmitter = (*gatedRaftHost)(nil)

// Scaling up a group whose replicas all advertise guarded writes onto a node
// that does not -- a node still on the previous release -- is refused before
// anything changes: go-quicraft would replay the group's guarded writes to
// the new replica, and it would diverge on the first one.
func TestScaleForTenant_ScaleUpOfAMixedGroupIsRefused(t *testing.T) {
	t.Parallel()

	host := &gatedRaftHost{fakeRaftHost: &fakeRaftHost{leaderOK: true}}
	svc, store, f := newScaleDatabaseService(t, healthyNodes("n1", "n2", "n3"),
		&quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}, host)
	info := createScaleDB(t, svc, "db1", 2)
	p, err := store.Get(scaleCtx(), scaleTenantID, "db1")
	if err != nil {
		t.Fatalf("placement Get: %v", err)
	}

	// The group's replicas are on n1 and n2, which advertise; n3 does not.
	group := map[uint64]string{}
	for _, r := range p.Replicas {
		group[r.ReplicaID] = r.RaftAddr
	}
	gate, err := featuregate.New(featuregate.Config{
		Self:          featuregate.Self{ReplicaID: 1},
		SystemShardID: 1,
		Members: fixedGroups{
			1:            {ConfigChangeID: 1, Replicas: map[uint64]string{1: "n1:1", 2: "n2:1", 3: "n3:1"}},
			info.ShardID: {ConfigChangeID: 1, Replicas: group},
		},
	})
	if err != nil {
		t.Fatalf("featuregate.New: %v", err)
	}
	t.Cleanup(func() { _ = gate.Close() })
	if err := gate.Start(context.Background(), noActivations{}, fixedAdverts{
		{NodeID: 2, Address: "n2:1", Features: featuregate.SupportedNames()},
		{NodeID: 3, Address: "n3:1"},
	}); err != nil {
		t.Fatalf("gate.Start: %v", err)
	}
	host.gate = gate
	if !gate.Enabled(info.ShardID, featuregate.GuardedWrites) {
		t.Fatal("the group's replicas all advertise guarded writes; the gate must enable them")
	}

	_, err = svc.ScaleForTenant(scaleCtx(), "db1", 3)
	var refused *featuregate.UnsupportedReplicaError
	if !errors.As(err, &refused) {
		t.Fatalf("ScaleForTenant = %v, want *featuregate.UnsupportedReplicaError", err)
	}
	if refused.Address != "n3:1" || refused.ShardID != info.ShardID {
		t.Fatalf("refused = %+v, want shard %d on n3:1", refused, info.ShardID)
	}
	if len(host.observerCalls) != 0 {
		t.Fatalf("AddObserver called %d times for a refused replica", len(host.observerCalls))
	}
	after, err := store.Get(scaleCtx(), scaleTenantID, "db1")
	if err != nil {
		t.Fatalf("placement Get: %v", err)
	}
	if after.RF != 2 || len(after.Replicas) != 2 {
		t.Fatalf("placement RF=%d replicas=%d after a refused scale-up, want it unchanged at 2", after.RF, len(after.Replicas))
	}
	if f.incTotal-f.decTotal != 2 {
		t.Fatalf("quota net = %d after a refused scale-up, want the create's 2", f.incTotal-f.decTotal)
	}
}

// An explicit member add of a replica on a node that does not advertise
// what the group uses is refused before any config change is proposed.
func TestAdminService_AddMemberRefusesANodeTheGroupCannotUse(t *testing.T) {
	refusal := &featuregate.UnsupportedReplicaError{ShardID: 1, Address: "n3:1", Feature: featuregate.GuardedWrites}
	host := &admittingHost{err: refusal}
	host.mockHost = &mockHost{requestAddNodeFn: func(context.Context, uint64, uint64, string) (*quicraft.RequestState, error) {
		host.addCalls++
		return nil, errors.New("must not be reached")
	}}
	err := NewAdminService(host).AddMember(context.Background(), 1, 3, "n3:1", "")
	if !errors.Is(err, refusal) {
		t.Fatalf("AddMember = %v, want the admission refusal", err)
	}
	if host.addCalls != 0 {
		t.Fatalf("RequestAddNode called %d times for a refused replica", host.addCalls)
	}
	if host.asked != "n3:1" {
		t.Fatalf("admission asked about %q, want n3:1", host.asked)
	}
}

// admittingHost is a Host that answers admission and counts adds.
type admittingHost struct {
	*mockHost
	err      error
	asked    string
	addCalls int
}

func (h *admittingHost) AdmitReplica(_ uint64, raftAddr string) error {
	h.asked = raftAddr
	return h.err
}

// staticAdmission is a RaftHost that refuses one node with a fixed error.
type staticAdmission struct {
	*fakeRaftHost
	refuse string
	err    error
}

func (h *staticAdmission) AdmitReplica(_ uint64, raftAddr string) error {
	if raftAddr == h.refuse {
		return h.err
	}
	return nil
}

// A rebalance MOVE whose destination does not advertise what the group uses
// is refused before the placement changes, and the source is untouched.
func TestMoveTenantGroupShard_RefusesADestinationTheGroupCannotUse(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)
	refusal := &featuregate.UnsupportedReplicaError{ShardID: 10, Address: "node-c:9100", Feature: featuregate.GuardedWrites}
	host := &staticAdmission{fakeRaftHost: &fakeRaftHost{leaderID: 2, leaderOK: true}, refuse: "node-c:9100", err: refusal}
	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		selfNodeID:     "node-b:9100",
		raftHost:       host,
		logger:         testLogger(),
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	err := svc.moveTenantGroupShard(context.Background(), move, p)
	if !errors.Is(err, refusal) {
		t.Fatalf("move = %v, want the admission refusal", err)
	}
	if len(host.observerCalls) != 0 || len(host.removeCalls) != 0 {
		t.Fatalf("a refused move changed membership: %d adds, %d removes", len(host.observerCalls), len(host.removeCalls))
	}
	after, err := store.Get(context.Background(), "acme", "db1")
	if err != nil {
		t.Fatalf("placement Get: %v", err)
	}
	if len(after.Replicas) != len(p.Replicas) {
		t.Fatalf("placement has %d replicas after a refused move, want %d", len(after.Replicas), len(p.Replicas))
	}
}
