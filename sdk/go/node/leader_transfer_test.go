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

package node

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestClusteredNode_TransferLeadership drives ClusterClient.TransferLeadership
// against three in-process replicas: a transfer to a live follower returns
// once that follower leads, every replica agrees on it, and a transfer to a
// stopped replica fails with the typed timeout instead of hanging or
// reporting success.
func TestClusteredNode_TransferLeadership(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	nodes, err := bootFormationCluster(t, ca, replicas, func(self uint64) string {
		return peerIdentitySpecs(t, self, replicas)
	})
	if err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}

	from := awaitAgreedLeader(t, nodes)
	var target, other uint64
	for _, r := range replicas {
		switch {
		case r.id == from:
		case target == 0:
			target = r.id
		default:
			other = r.id
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), clusterFormationBudget)
	defer cancel()

	// A follower cannot hand off leadership it does not hold; it names the
	// replica that can.
	var nle *transport.NotLeaderError
	if err := nodes[target-1].Client().Cluster().TransferLeadership(ctx, shard.ShardKV, other); !errors.As(err, &nle) || nle.LeaderID != from {
		t.Fatalf("transfer through follower %d: got %T (%v), want *transport.NotLeaderError naming %d", target, err, err, from)
	}

	if err := nodes[from-1].Client().Cluster().TransferLeadership(ctx, shard.ShardKV, target); err != nil {
		t.Fatalf("transfer %d -> %d: %v", from, target, err)
	}
	if got := awaitAgreedLeader(t, nodes); got != target {
		t.Fatalf("replicas agree on leader %d after the transfer, want %d", got, target)
	}
	leader := nodes[target-1].Client().Cluster()

	var ure *sdk.UnknownReplicaError
	if err := leader.TransferLeadership(ctx, shard.ShardKV, 99); !errors.As(err, &ure) || ure.ReplicaID != 99 {
		t.Fatalf("transfer to replica 99: got %T (%v), want *sdk.UnknownReplicaError", err, err)
	}
	var use *sdk.UnknownShardError
	if err := leader.TransferLeadership(ctx, 1<<40, from); !errors.As(err, &use) {
		t.Fatalf("transfer on an unknown shard: got %T (%v), want *sdk.UnknownShardError", err, err)
	}

	// A stopped voter is still a member, so the transfer starts, and can
	// never finish.
	if err := nodes[other-1].Close(); err != nil {
		t.Fatalf("stop replica %d: %v", other, err)
	}
	var lte *sdk.LeaderTransferTimeoutError
	err = leader.TransferLeadership(ctx, shard.ShardKV, other)
	if !errors.As(err, &lte) {
		t.Fatalf("transfer to stopped replica %d: got %T (%v), want *sdk.LeaderTransferTimeoutError", other, err, err)
	}
	if lte.ShardID != shard.ShardKV || lte.FromReplicaID != target || lte.ToReplicaID != other {
		t.Fatalf("timeout error = %+v, want shard %d, from %d, to %d", *lte, shard.ShardKV, target, other)
	}
}
