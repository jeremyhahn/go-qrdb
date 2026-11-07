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

package node

import (
	"context"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// startClusterSeal builds and starts this clustered node's cluster seal
// monitor, as cmd/qrdbd does: it reads the replicated seal records and seals
// this node when they say so, and until its first read -- and whenever it
// cannot read them for the lease -- the node refuses requests. It is active
// only once every node runs a release with cluster sealing. A node with no
// barrier has nothing to seal and starts no monitor.
//
// Before installing the monitor it reads the cluster's feature activations,
// as cmd/qrdbd does before it serves: a monitor whose gate has not read them
// refuses every request, since it cannot tell an inactive feature from one
// it has not learned of. The read is a startup read: it waits out the system
// shard settling, each attempt bounded on its own, and honours ctx; a cluster that has activated a feature this binary lacks
// fails the boot with *featuregate.UnsupportedFeatureError. A ctx already
// ended fails it with ctx's error.
func (n *Node) startClusterSeal(ctx context.Context, host *quicraft.Host, svcNode *lifecycle.Node, barriers *seal.BarrierRegistry, cfg *qrdbconfig.Config) error {
	// The last step before the node serves: a caller that gave up while the
	// boot restored its tenants gets its cancellation, and the boot's failure
	// path seals what it unsealed, rather than the node serving a boot nobody
	// awaits.
	if err := ctx.Err(); err != nil {
		return err
	}
	if n.clusterSeal == nil || host == nil || barriers == nil {
		return nil
	}
	if n.featureGate != nil {
		if err := server.WhileSystemShardSettling(ctx, host, "feature gate activation check", func(actx context.Context) error {
			return n.featureGate.CheckActivated(actx, svcNode.KV)
		}); err != nil {
			return err
		}
	}
	timing, err := cfg.Seal.ClusterSealTiming()
	if err != nil {
		return err
	}
	monitor, err := server.NewClusterSealMonitor(server.ClusterSealParts{
		KV:          svcNode.KV,
		Gate:        n.featureGate,
		Barriers:    barriers,
		Shards:      n.raftBackend,
		Hosts:       n.tenantHosts,
		Pauser:      n.shardPauser,
		Databases:   svcNode.Database,
		Membership:  host,
		SystemShard: shard.ShardKV,
		ReplicaID:   cfg.Raft.ReplicaID,
		RaftAddress: cfg.Raft.RaftAddress,
		Timing:      timing,
		Streams:     n.clusterSeal.Streams(),
		Logger:      n.logger,
	})
	if err != nil {
		return err
	}
	n.clusterSeal.Set(monitor)
	// The monitor reads the seal records through svcNode.KV and seals
	// through the barriers, so it is node work Close waits for (bgWG) before
	// stopping either: canceling its ctx alone leaves a poll in flight.
	bgCtx := n.background()
	n.bgWG.Go(func() { monitor.Run(bgCtx) })
	return nil
}
