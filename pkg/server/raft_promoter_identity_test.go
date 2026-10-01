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

package server

import (
	"context"
	"errors"
	"testing"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/raftharness"
)

// TestRaftPromoter_PromoteObserverCarriesItsIdentity promotes a real
// observer on a host with a transport: the promotion must carry the identity
// the shard committed when it admitted the observer, which go-quicraft
// requires of every voter add on such a host.
func TestRaftPromoter_PromoteObserverCarriesItsIdentity(t *testing.T) {
	c := raftharness.NewCluster(t, 2)
	leader, follower := c.Hosts[0], c.Hosts[1]
	const shardID = uint64(1000)

	if err := leader.StartShard(map[uint64]string{1: c.Addrs[0]}, false, c.CreateFn(), quicraftconfig.Config{
		ShardID: shardID, ReplicaID: 1, MemberIdentities: map[uint64]string{1: c.Identities[1]},
	}); err != nil {
		t.Fatalf("StartShard(founder): %v", err)
	}
	c.WaitReadable(t, shardID, leader)

	ctx, cancel := context.WithTimeout(context.Background(), raftharness.Bound)
	defer cancel()
	if err := leader.SyncRequestAddObserverWithIdentity(ctx, shardID, 2, c.Addrs[1], c.Identities[2], 0); err != nil {
		t.Fatalf("admit observer: %v", err)
	}
	if err := follower.StartShard(nil, true, c.CreateFn(), quicraftconfig.Config{
		ShardID: shardID, ReplicaID: 2, IsObserver: true,
		// A joiner names the replica it takes its bootstrap snapshot from.
		MemberIdentities: map[uint64]string{1: c.Identities[1]},
	}); err != nil {
		t.Fatalf("StartShard(observer): %v", err)
	}

	if err := newRaftPromoter(leader).PromoteObserver(ctx, shardID, 2, c.Addrs[1]); err != nil {
		t.Fatalf("PromoteObserver: %v", err)
	}
	m, err := leader.SyncGetShardMembership(ctx, shardID)
	if err != nil {
		t.Fatalf("SyncGetShardMembership: %v", err)
	}
	if m.Nodes[2] != c.Addrs[1] {
		t.Fatalf("replica 2 is not a voter after the promotion: nodes %v, observers %v", m.Nodes, m.Observers)
	}
	if _, still := m.Observers[2]; still {
		t.Fatalf("replica 2 is still an observer: %v", m.Observers)
	}
	if m.Identities[2] != c.Identities[2] {
		t.Fatalf("promoted replica committed %q, want its host's identity %q", m.Identities[2], c.Identities[2])
	}
}

// TestRaftPromoter_PromoteObserverRefusesAnUnknownHost pins the refusal
// branch: an address no committed membership names has no identity to carry,
// so nothing is proposed and the typed refusal is returned.
func TestRaftPromoter_PromoteObserverRefusesAnUnknownHost(t *testing.T) {
	c := raftharness.NewCluster(t, 1)
	err := newRaftPromoter(c.Hosts[0]).PromoteObserver(context.Background(), 1000, 2, "127.0.0.1:9")
	var mie *service.MemberIdentityError
	var unknown *cluster.MemberIdentityUnknownError
	if !errors.As(err, &mie) || !errors.As(err, &unknown) || unknown.ReplicaID != 2 {
		t.Fatalf("PromoteObserver = %v, want a *service.MemberIdentityError naming replica 2", err)
	}
}
