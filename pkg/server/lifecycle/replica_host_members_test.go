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
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/raftharness"
)

// unknownAdderShard is a shard ID no host of the adder cluster holds.
const unknownAdderShard uint64 = 987654321

// TestReplicaHost_ShardLeaderAndMembers proves the replica host reports the
// local leader of a group it serves and lists every member of it, the voter
// and an added observer alike, with each member's address.
func TestReplicaHost_ShardLeaderAndMembers(t *testing.T) {
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
	isLeader, err := adder.IsLocalShardLeader(dedicatedAdderShard)
	if err != nil || !isLeader {
		t.Fatalf("IsLocalShardLeader = %v, %v; want the local replica leading", isLeader, err)
	}
	if err := adder.AddObserver(ctx, dedicatedAdderShard, 2, c.Addrs[1]); err != nil {
		t.Fatalf("AddObserver: %v", err)
	}
	members, err := adder.ShardMembers(ctx, dedicatedAdderShard)
	if err != nil {
		t.Fatalf("ShardMembers: %v", err)
	}
	if _, voter := members[1]; !voter {
		t.Fatalf("ShardMembers = %v; want the leading voter 1 listed", members)
	}
	if addr := members[2]; addr != c.Addrs[1] {
		t.Fatalf("ShardMembers[2] = %q; want the observer at %q", addr, c.Addrs[1])
	}
}

// TestReplicaHost_UnknownShardLeaderAndMembers proves a group the host does
// not serve is refused by both the leader query and the member listing,
// rather than answered as a group with no leader or no members.
func TestReplicaHost_UnknownShardLeaderAndMembers(t *testing.T) {
	_, adder := newAdderCluster(t)
	ctx, cancel := context.WithTimeout(context.Background(), raftharness.Bound)
	defer cancel()
	if isLeader, err := adder.IsLocalShardLeader(unknownAdderShard); err == nil {
		t.Fatalf("IsLocalShardLeader(unknown) = %v, nil; want an error", isLeader)
	}
	if members, err := adder.ShardMembers(ctx, unknownAdderShard); err == nil {
		t.Fatalf("ShardMembers(unknown) = %v, nil; want an error", members)
	}
}
