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
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// memberRecordRetryInterval paces startMemberRecord's attempts while the
// system shard has no leader to accept the lease-backed record.
const memberRecordRetryInterval = 500 * time.Millisecond

// startMemberRecord publishes this node's lease-backed member record -- its
// replica id, Raft address, the API endpoints it serves and its features --
// and keeps the lease alive, as every raft-mode qrdbd does. Replica
// forwarding resolves its targets from these records, so a clustered
// embedded node publishes one too. It runs in the background for the reason
// startFeatureGate does: the node is built before its peers, and waiting
// here for a quorum would deadlock a cluster whose members start one after
// another.
func (n *Node) startMemberRecord(host leaderReporter, cfg *qrdbconfig.Config) {
	client := n.featureClient.Load()
	if client == nil || cfg == nil {
		return
	}
	logger := n.logger
	if logger == nil {
		logger = slog.Default()
	}
	endpoints, err := cfg.AdvertisedAPIEndpoints()
	if err != nil {
		logger.Error("member record: invalid advertised endpoints; not published", slog.Any("error", err))
		return
	}
	self := cluster.Member{
		NodeID:       cfg.Raft.ReplicaID,
		Address:      cfg.Raft.RaftAddress,
		APIEndpoints: endpoints,
		Features:     featuregate.SupportedNames(),
	}
	ctx := n.background()
	n.memberWG.Add(1)
	go func() {
		defer n.memberWG.Done()
		for ctx.Err() == nil {
			if _, ok, lerr := host.GetLeaderID(shard.ShardKV); lerr == nil && ok {
				adv, aerr := client.NewNodeAdvertiser(self, cluster.AdvertiseConfig{}, cluster.WithAdvertiseSealProbe(n))
				if aerr != nil {
					logger.Error("member record: advertiser not built", slog.Any("error", aerr))
					return
				}
				if serr := adv.Start(ctx); serr == nil {
					n.memberAdv.Store(adv)
					logger.Info("member record published", slog.Uint64("node_id", cfg.Raft.ReplicaID))
					return
				}
				_ = adv.Close()
			}
			select {
			case <-ctx.Done():
			case <-time.After(memberRecordRetryInterval):
			}
		}
	}()
}

// stopMemberRecord waits out startMemberRecord -- Close cancels its context
// first -- then withdraws the record.
func (n *Node) stopMemberRecord() error {
	n.memberWG.Wait()
	if adv := n.memberAdv.Swap(nil); adv != nil {
		return adv.Close()
	}
	return nil
}
