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

// A chunked import session's staged chunks and metadata are replicated,
// lease-bound state (pkg/importstage), not anything held in the leader
// process's memory -- so a leader change between ImportBegin and
// ImportCommit must not lose a staged chunk. importSessionStore
// (batch_import_session_test.go) is a fake with no leader at all; proving
// this needs a REAL multi-node Raft group, which is what
// sdk/go/testutil/raftharness builds (go-qrdb#576's also-fix list).

import (
	"context"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/raftharness"
)

// alwaysEnabledHost wraps *raftbackend.HostAdapter and reports every
// feature enabled, overriding the embedded adapter's own FeatureEnabled.
// Production gates a host with a transport (raftharness's hosts have one)
// because its groups can hold a node running an older binary; a raftharness
// cluster is every replica running this SAME freshly-built test binary, so
// there is no older binary to gate against, and standing up the full
// featuregate.Gate advertisement/activation machinery (a replicated
// ActivationKV, a membership advertisement store) to prove that would test
// the gate, not this feature.
type alwaysEnabledHost struct {
	*raftbackend.HostAdapter
}

func (alwaysEnabledHost) FeatureEnabled(_ uint64, _ featuregate.Feature) bool { return true }

// startImportLeaderClusterShard starts a dedicated shard (distinct from the
// cluster's own system shard) on every host of c, running c.CreateFn()
// (the "memory" KVStateMachine -- see kv_import_stage.go), and waits for it
// to become readable.
func startImportLeaderClusterShard(t *testing.T, c *raftharness.Cluster, shardID uint64) {
	t.Helper()
	members := make(map[uint64]string, len(c.Hosts))
	for i := range c.Hosts {
		members[uint64(i+1)] = c.Addrs[i]
	}
	for i, host := range c.Hosts {
		if err := host.StartShard(members, false, c.CreateFn(), quicraftconfig.Config{
			ShardID: shardID, ReplicaID: uint64(i + 1), MemberIdentities: c.Identities,
		}); err != nil {
			t.Fatalf("StartShard(host %d, shard %d): %v", i, shardID, err)
		}
	}
	c.WaitReadable(t, shardID, c.Hosts...)
}

// waitForLeaderExcept polls every host of c until one reports a leader for
// shardID whose replica ID is not in exclude, returning that replica's
// own host index (0-based, i.e. replicaID-1 -- replica IDs are 1-based and
// assigned in host order by startImportLeaderClusterShard) and replica ID.
// Used both to find the cluster's initial leader (exclude empty) and to
// confirm a RequestLeaderTransfer actually landed on a DIFFERENT replica.
//
// LeaderID answers from WHATEVER host is asked, follower included -- it
// reports who the asked host believes leads, not that the asked host itself
// leads -- so the returned index is derived from the reported replica ID,
// never from which host in the loop happened to answer.
func waitForLeaderExcept(t *testing.T, hosts []*quicraft.Host, shardID uint64, exclude map[uint64]bool) (hostIdx int, replicaID uint64) {
	t.Helper()
	deadline := time.Now().Add(raftharness.Bound)
	for {
		for _, host := range hosts {
			if id, known, err := host.LeaderID(shardID); err == nil && known && id != 0 && !exclude[id] {
				return int(id - 1), id
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("waitForLeaderExcept: no qualifying leader for shard %d within %s", shardID, raftharness.Bound)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestImportSession_LeaderChangeDoesNotLoseStagedChunk proves a chunk
// ImportAppend stages through the cluster's leader survives a
// RequestLeaderTransfer to a different replica: ImportCommit, now issued
// against the NEW leader through a completely separate BatchService
// instance (modeling a client reconnecting to whichever node answers after
// a failover), still reads the staged chunk back and applies it.
func TestImportSession_LeaderChangeDoesNotLoseStagedChunk(t *testing.T) {
	const hostCount = 3
	const shardID = uint64(9001)

	c := raftharness.NewCluster(t, hostCount)
	startImportLeaderClusterShard(t, c, shardID)

	leaderIdx, leaderReplicaID := waitForLeaderExcept(t, c.Hosts, shardID, nil)
	svc := NewBatchService(alwaysEnabledHost{raftbackend.New(c.Hosts[leaderIdx])}, shardID)
	ctx := withCaller(context.Background(), "acme", "alice")

	sessionID, err := svc.ImportBegin(ctx, ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row", SkipHeader: true})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}
	if err := svc.ImportAppend(ctx, sessionID, 0, []byte("id,value\nk1,v1\n")); err != nil {
		t.Fatalf("ImportAppend: %v", err)
	}

	// Transfer leadership to any other member and confirm it actually moved.
	target := leaderReplicaID%uint64(hostCount) + 1
	if err := c.Hosts[leaderIdx].RequestLeaderTransfer(ctx, shardID, target); err != nil {
		t.Fatalf("RequestLeaderTransfer: %v", err)
	}
	newLeaderIdx, newLeaderReplicaID := waitForLeaderExcept(t, c.Hosts, shardID, map[uint64]bool{leaderReplicaID: true})
	if newLeaderReplicaID == leaderReplicaID {
		t.Fatalf("leadership did not move: still replica %d", leaderReplicaID)
	}

	// A fresh BatchService over the NEW leader -- a different process's
	// in-memory state, nothing shared with svc above -- must still see the
	// chunk svc staged through the OLD leader: it is replicated, lease-bound
	// state, not anything the old leader held locally.
	svc2 := NewBatchService(alwaysEnabledHost{raftbackend.New(c.Hosts[newLeaderIdx])}, shardID)
	result, err := svc2.ImportCommit(ctx, sessionID)
	if err != nil {
		t.Fatalf("ImportCommit against the new leader (replica %d): %v", newLeaderReplicaID, err)
	}
	if result.Total != 1 || result.Succeeded != 1 || result.Failed != 0 {
		t.Fatalf("ImportCommit result = %+v, want Total=1 Succeeded=1 Failed=0", result)
	}
}
