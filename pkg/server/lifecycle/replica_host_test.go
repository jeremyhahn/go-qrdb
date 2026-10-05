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
	"testing"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
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
			m, err := c.Hosts[0].GetShardMembership(dedicatedAdderShard)
			if err != nil {
				t.Fatalf("GetShardMembership: %v", err)
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
	m, err := c.Hosts[0].GetShardMembership(dedicatedAdderShard)
	if err != nil {
		t.Fatalf("GetShardMembership: %v", err)
	}
	if len(m.Nodes)+len(m.Observers) != 1 {
		t.Fatalf("a refused add must propose nothing: nodes %v, observers %v", m.Nodes, m.Observers)
	}
}
