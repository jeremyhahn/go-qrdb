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

//go:build !integration

package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/raftharness"
)

// dedicatedAdderShard is the dedicated shard these tests found on host 0.
const dedicatedAdderShard = uint64(1000)

// newAdderCluster forms a two-host system shard and founds a one-replica
// dedicated shard on host 0, so replicas can be added to it on host 1 — a
// host whose identity only the system shard has committed.
func newAdderCluster(t *testing.T) (*raftharness.Cluster, *replicaHost) {
	t.Helper()
	c := raftharness.NewCluster(t, 2)
	if err := c.Hosts[0].StartShard(map[uint64]string{1: c.Addrs[0]}, false, c.CreateFn(), quicraftconfig.Config{
		ShardID: dedicatedAdderShard, ReplicaID: 1, MemberIdentities: map[uint64]string{1: c.Identities[1]},
	}); err != nil {
		t.Fatalf("StartShard(dedicated): %v", err)
	}
	c.WaitReadable(t, dedicatedAdderShard, c.Hosts[0])
	return c, &replicaHost{host: c.Hosts[0]}
}

// TestReplicaHost_AddsCommitTheHostIdentity pins both add paths on a
// host with a transport: the replica placed on host 1 is committed with the
// identity the system shard committed for host 1, found by its address.
func TestReplicaHost_AddsCommitTheHostIdentity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		add      func(a *replicaHost, ctx context.Context, addr string) error
		observer bool
	}{
		{name: "AddFollowerNode", add: func(a *replicaHost, ctx context.Context, addr string) error {
			return a.AddFollowerNode(ctx, dedicatedAdderShard, 2, addr)
		}},
		{name: "AddObserver", observer: true, add: func(a *replicaHost, ctx context.Context, addr string) error {
			return a.AddObserver(ctx, dedicatedAdderShard, 2, addr)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, adder := newAdderCluster(t)
			ctx, cancel := context.WithTimeout(context.Background(), raftharness.Bound)
			defer cancel()
			if err := tc.add(adder, ctx, c.Addrs[1]); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			// The leader has applied the change once the add returns; the
			// local view is read because a linearizable read would need the
			// new voter, which is never started here.
			m, err := c.Hosts[0].ShardMembership(dedicatedAdderShard)
			if err != nil {
				t.Fatalf("ShardMembership: %v", err)
			}
			roster := m.Nodes
			if tc.observer {
				roster = m.Observers
			}
			if roster[2] != c.Addrs[1] {
				t.Fatalf("replica 2 not added: nodes %v, observers %v", m.Nodes, m.Observers)
			}
			if m.Identities[2] != c.Identities[2] {
				t.Fatalf("replica 2 committed %q, want host 1's identity %q", m.Identities[2], c.Identities[2])
			}
		})
	}
}

// TestReplicaHost_RefusesAnUnknownHost pins the refusal branch of both
// add paths: an address no committed membership names has no identity the
// cluster can vouch for, so nothing is proposed.
func TestReplicaHost_RefusesAnUnknownHost(t *testing.T) {
	c, adder := newAdderCluster(t)
	ctx, cancel := context.WithTimeout(context.Background(), raftharness.Bound)
	defer cancel()
	for name, add := range map[string]func() error{
		"AddFollowerNode": func() error { return adder.AddFollowerNode(ctx, dedicatedAdderShard, 3, "127.0.0.1:9") },
		"AddObserver":     func() error { return adder.AddObserver(ctx, dedicatedAdderShard, 3, "127.0.0.1:9") },
	} {
		err := add()
		var mie *service.MemberIdentityError
		var unknown *cluster.MemberIdentityUnknownError
		if !errors.As(err, &mie) || !errors.As(err, &unknown) || unknown.ReplicaID != 3 {
			t.Fatalf("%s = %v, want a *service.MemberIdentityError naming replica 3", name, err)
		}
	}
	m, err := c.Hosts[0].ShardMembership(dedicatedAdderShard)
	if err != nil {
		t.Fatalf("ShardMembership: %v", err)
	}
	if len(m.Nodes)+len(m.Observers) != 1 {
		t.Fatalf("a refused add must propose nothing: nodes %v, observers %v", m.Nodes, m.Observers)
	}
}

// The rest of the replica host: a nil gate admits every replica, an added
// replica is removed, the leader is reported, and a leader transfer to a
// replica the group does not hold is refused by the host.
func TestReplicaHost_RemoveLeaderAndTransfer(t *testing.T) {
	c, adder := newAdderCluster(t)
	ctx, cancel := context.WithTimeout(context.Background(), raftharness.Bound)
	defer cancel()
	if err := adder.AdmitReplica(dedicatedAdderShard, c.Addrs[1]); err != nil {
		t.Fatalf("AdmitReplica with no gate = %v; want admitted", err)
	}
	leader, ok, err := adder.LeaderReplicaID(ctx, dedicatedAdderShard)
	if err != nil || !ok || leader != 1 {
		t.Fatalf("LeaderReplicaID = %d, %v, %v; want replica 1", leader, ok, err)
	}
	if err := adder.AddObserver(ctx, dedicatedAdderShard, 2, c.Addrs[1]); err != nil {
		t.Fatalf("AddObserver: %v", err)
	}
	if err := adder.RemoveFollowerNode(ctx, dedicatedAdderShard, 2); err != nil {
		t.Fatalf("RemoveFollowerNode: %v", err)
	}
	m, err := c.Hosts[0].ShardMembership(dedicatedAdderShard)
	if err != nil {
		t.Fatalf("ShardMembership: %v", err)
	}
	if _, still := m.Observers[2]; still {
		t.Fatalf("replica 2 still a member after removal: %v", m.Observers)
	}
	if err := adder.RequestLeaderTransfer(ctx, dedicatedAdderShard, 9); err == nil {
		t.Fatal("a leader transfer to a replica the group does not hold was accepted")
	}
}

// A cancelled context ends a membership change at once.
func TestReplicaHost_CancelledContext(t *testing.T) {
	_, adder := newAdderCluster(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := adder.RemoveFollowerNode(ctx, dedicatedAdderShard, 2); err == nil {
		t.Fatal("RemoveFollowerNode with a cancelled context succeeded")
	}
}

// A change to a group the host does not run is refused by the host, never
// waited on.
func TestReplicaHost_UnknownGroupRefused(t *testing.T) {
	c, adder := newAdderCluster(t)
	ctx, cancel := context.WithTimeout(context.Background(), raftharness.Bound)
	defer cancel()
	const unknownShard = 4242
	if err := adder.RemoveFollowerNode(ctx, unknownShard, 2); err == nil {
		t.Fatal("RemoveFollowerNode on a group the host does not run succeeded")
	}
	if _, _, err := adder.LeaderReplicaID(ctx, unknownShard); err == nil {
		t.Fatal("LeaderReplicaID of a group the host does not run succeeded")
	}
	if err := adder.AddObserver(ctx, unknownShard, 2, c.Addrs[1]); err == nil {
		t.Fatal("AddObserver on a group the host does not run succeeded")
	}
	if err := adder.AddFollowerNode(ctx, unknownShard, 2, c.Addrs[1]); err == nil {
		t.Fatal("AddFollowerNode on a group the host does not run succeeded")
	}
}

// gateGroups is a featuregate.MembershipSource holding fixed memberships.
type gateGroups map[uint64]featuregate.GroupMembership

func (g gateGroups) GroupMembership(shardID uint64) (featuregate.GroupMembership, error) {
	m, ok := g[shardID]
	if !ok {
		return featuregate.GroupMembership{}, errors.New("shard not ready")
	}
	return m, nil
}

// gateAdverts is a cluster.MembershipStore holding fixed live advertisements.
type gateAdverts []cluster.Member

func (gateAdverts) PutMember(context.Context, uint64, []byte, time.Duration) error { return nil }
func (gateAdverts) DeleteMember(context.Context, uint64) error                     { return nil }
func (gateAdverts) WatchMembers(context.Context) (<-chan cluster.MemberEvent, error) {
	return nil, errors.New("no watch")
}
func (a gateAdverts) ListMembers(context.Context) ([]cluster.Member, error) { return a, nil }

// gateNoActivations is a system store with no activated feature.
type gateNoActivations struct{}

func (gateNoActivations) PutIfAbsent(context.Context, string, []byte) (bool, error) {
	return false, nil
}
func (gateNoActivations) ListAll(context.Context, string) ([]string, error)   { return nil, nil }
func (gateNoActivations) ListStale(context.Context, string) ([]string, error) { return nil, nil }
func (gateNoActivations) Delete(context.Context, string) error                { return nil }

// TestReplicaHost_GateRefusesAnUnsupportingNode pins the admission branch of
// a gated host: the dedicated group's replicas advertise every feature, so a
// replica on a node that advertises none is refused by the feature gate, and
// neither add path proposes anything.
func TestReplicaHost_GateRefusesAnUnsupportingNode(t *testing.T) {
	c, adder := newAdderCluster(t)
	gate, err := featuregate.New(featuregate.Config{
		Self:          featuregate.Self{ReplicaID: 1},
		SystemShardID: 1,
		Members: gateGroups{
			1:                   {ConfigChangeID: 1, Replicas: map[uint64]string{1: "n1:1", 2: "n2:1", 3: "n3:1"}},
			dedicatedAdderShard: {ConfigChangeID: 1, Replicas: map[uint64]string{1: "n1:1", 2: "n2:1"}},
		},
	})
	if err != nil {
		t.Fatalf("featuregate.New: %v", err)
	}
	t.Cleanup(func() { _ = gate.Close() })
	if err := gate.Start(context.Background(), gateNoActivations{}, gateAdverts{
		{NodeID: 2, Address: "n2:1", Features: featuregate.SupportedNames()},
		{NodeID: 3, Address: "n3:1"},
	}); err != nil {
		t.Fatalf("gate.Start: %v", err)
	}
	adder.gate = gate
	if err := adder.AdmitReplica(dedicatedAdderShard, "n2:1"); err != nil {
		t.Fatalf("AdmitReplica(n2) = %v, want admitted: it advertises every feature", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), raftharness.Bound)
	defer cancel()
	for name, add := range map[string]func() error{
		"AdmitReplica":    func() error { return adder.AdmitReplica(dedicatedAdderShard, "n3:1") },
		"AddFollowerNode": func() error { return adder.AddFollowerNode(ctx, dedicatedAdderShard, 3, "n3:1") },
		"AddObserver":     func() error { return adder.AddObserver(ctx, dedicatedAdderShard, 3, "n3:1") },
	} {
		var refused *featuregate.UnsupportedReplicaError
		if err := add(); !errors.As(err, &refused) || refused.Address != "n3:1" || refused.ShardID != dedicatedAdderShard {
			t.Fatalf("%s = %v, want *featuregate.UnsupportedReplicaError for n3:1", name, err)
		}
	}
	m, err := c.Hosts[0].ShardMembership(dedicatedAdderShard)
	if err != nil {
		t.Fatalf("ShardMembership: %v", err)
	}
	if len(m.Nodes)+len(m.Observers) != 1 {
		t.Fatalf("a refused add must propose nothing: nodes %v, observers %v", m.Nodes, m.Observers)
	}
}

// stalledShard is a dedicated shard founded with check-quorum off.
const stalledShard = uint64(1001)

// newStalledGroup founds a one-replica shard with check-quorum off on a
// transport-less host and adds replica 2, a voter that never starts, so the
// leader keeps leading a group that can commit nothing further: every later
// config change is accepted and then waits on its context. A transport-less
// host binds no member identity, so an add reads nothing from the group and
// reaches its proposal. The harness cluster lends its state machine.
func newStalledGroup(t *testing.T) *replicaHost {
	t.Helper()
	c := raftharness.NewCluster(t, 1)
	root := t.TempDir()
	host, err := quicraft.NewHost(quicraftconfig.HostConfig{
		WALDir:        filepath.Join(root, "wal"),
		NodeHostDir:   filepath.Join(root, "nodehost"),
		RaftAddress:   "127.0.0.1:63997",
		ListenAddress: "127.0.0.1:63997",
		DeploymentID:  1,
	}, quicraft.WithoutTransport(), quicraft.WithoutEncryption())
	if err != nil {
		t.Fatalf("NewHost(transport-less): %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	if err := host.StartShard(map[uint64]string{1: "127.0.0.1:63997"}, false, c.CreateFn(), quicraftconfig.Config{
		ShardID: stalledShard, ReplicaID: 1, CheckQuorum: quicraftconfig.BoolPtr(false),
	}); err != nil {
		t.Fatalf("StartShard(stalled): %v", err)
	}
	c.WaitReadable(t, stalledShard, host)
	adder := &replicaHost{host: host}
	ctx, cancel := context.WithTimeout(context.Background(), raftharness.Bound)
	defer cancel()
	if err := adder.AddFollowerNode(ctx, stalledShard, 2, "127.0.0.1:2"); err != nil {
		t.Fatalf("AddFollowerNode(2): %v", err)
	}
	return adder
}

// TestReplicaHost_ContextEndsAnUncommittedChange pins the wait of every
// membership change: a change the group cannot commit ends with its
// context's error once the context's deadline passes.
func TestReplicaHost_ContextEndsAnUncommittedChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(a *replicaHost, ctx context.Context) error
	}{
		{name: "RemoveFollowerNode", change: func(a *replicaHost, ctx context.Context) error {
			return a.RemoveFollowerNode(ctx, stalledShard, 2)
		}},
		{name: "AddFollowerNode", change: func(a *replicaHost, ctx context.Context) error {
			return a.AddFollowerNode(ctx, stalledShard, 3, "127.0.0.1:3")
		}},
		{name: "AddObserver", change: func(a *replicaHost, ctx context.Context) error {
			return a.AddObserver(ctx, stalledShard, 3, "127.0.0.1:3")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adder := newStalledGroup(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := tc.change(adder, ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s on a group that cannot commit = %v, want %v", tc.name, err, context.DeadlineExceeded)
			}
		})
	}
}
