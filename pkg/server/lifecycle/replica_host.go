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

package lifecycle

import (
	"context"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// replicaHost adapts *quicraft.Host to the service.RaftHost interface.
// It wraps RequestAddNodeWithIdentity + rs.Result() into a single blocking call that
// returns only an error, shielding DatabaseService from the quicraft import
// and from RequestState lifecycle management.
type replicaHost struct {
	host *quicraft.Host
	gate *featuregate.Gate
}

var _ service.ReplicaAdmitter = (*replicaHost)(nil)

var _ service.HomeGroupView = (*replicaHost)(nil)

// AdmitReplica refuses to add a replica of shardID on raftAddr when the node
// does not advertise every feature the group uses (see
// featuregate.Gate.AdmitReplica). DatabaseService and RebalanceService ask it
// before they persist a placement change; AddFollowerNode and AddObserver ask
// again before the config change, so no caller can add around it.
func (a *replicaHost) AdmitReplica(shardID uint64, raftAddr string) error {
	if a.gate == nil {
		return nil
	}
	return a.gate.AdmitReplica(shardID, raftAddr)
}

// AddFollowerNode calls RequestAddNodeWithIdentity on the shared Raft host and blocks
// until the config change commits or ctx is cancelled. Used ONLY for
// founding members at create time (service.DatabaseService.CreateForTenant);
// see AddObserver for scale-up.
//
// The replica's host is an existing cluster node, so the identity committed
// with it is the one go-quicraft already committed for that host.
func (a *replicaHost) AddFollowerNode(ctx context.Context, shardID, replicaID uint64, raftAddr string) error {
	if err := a.AdmitReplica(shardID, raftAddr); err != nil {
		return err
	}
	identity, err := service.ResolveMemberIdentity(ctx, a.host, shardID, replicaID, raftAddr, "")
	if err != nil {
		return err
	}
	rs, err := a.host.RequestAddNodeWithIdentity(ctx, shardID, replicaID, raftAddr, identity, 0)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		rs.Release()
		return ctx.Err()
	case result := <-rs.ResultC():
		return result.Err
	}
}

// AddObserver calls RequestAddObserverWithIdentity on the shared Raft host and blocks
// until the config change commits or ctx is cancelled. Used exclusively by
// DatabaseService.ScaleForTenant's scale-up path (scaleUp) to admit a
// dynamically added replica as a NON-VOTING observer, so scale-up never
// transiently changes -- and therefore never breaks -- the group's existing
// voting quorum (see the elastic-scaling design doc §B.2/§D.4). The
// observer is later promoted to a full voting member asynchronously by
// server.TenantShardReconciler's Promoter (see raftPromoter), which reuses
// RequestAddNodeWithIdentity against this same, already-admitted replica ID -- go-quicraft
// treats that as an observer -> voter promotion rather than a fresh add.
func (a *replicaHost) AddObserver(ctx context.Context, shardID, replicaID uint64, raftAddr string) error {
	if err := a.AdmitReplica(shardID, raftAddr); err != nil {
		return err
	}
	identity, err := service.ResolveMemberIdentity(ctx, a.host, shardID, replicaID, raftAddr, "")
	if err != nil {
		return err
	}
	rs, err := a.host.RequestAddObserverWithIdentity(ctx, shardID, replicaID, raftAddr, identity, 0)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		rs.Release()
		return ctx.Err()
	case result := <-rs.ResultC():
		return result.Err
	}
}

// RemoveFollowerNode calls RequestRemoveNode on the shared Raft host and
// blocks until the config change commits or ctx is cancelled. Used by
// DatabaseService.ScaleForTenant's scale-down path.
func (a *replicaHost) RemoveFollowerNode(ctx context.Context, shardID, replicaID uint64) error {
	rs, err := a.host.RequestRemoveNode(ctx, shardID, replicaID, 0)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		rs.Release()
		return ctx.Err()
	case result := <-rs.ResultC():
		return result.Err
	}
}

// LeaderReplicaID returns the shard's current leader replica ID via the
// shared Raft host. Used by DatabaseService.ScaleForTenant's scale-down
// replica-selection policy so the current leader is removed last.
func (a *replicaHost) LeaderReplicaID(_ context.Context, shardID uint64) (uint64, bool, error) {
	return a.host.LeaderID(shardID)
}

// IsLocalShardLeader reports whether this host's replica leads shardID. A
// shard this host does not run is not led here.
func (a *replicaHost) IsLocalShardLeader(shardID uint64) (bool, error) {
	info, err := a.host.ShardInfo(shardID)
	if err != nil {
		return false, err
	}
	return info.IsLeader, nil
}

// ShardMembers returns shardID's committed members (voters, observers and
// witnesses) by replica id.
func (a *replicaHost) ShardMembers(ctx context.Context, shardID uint64) (map[uint64]string, error) {
	m, err := a.host.SyncGetShardMembership(ctx, shardID)
	if err != nil {
		return nil, err
	}
	out := make(map[uint64]string, len(m.Nodes)+len(m.Observers)+len(m.Witnesses))
	for _, set := range []map[uint64]string{m.Nodes, m.Observers, m.Witnesses} {
		for id, addr := range set {
			out[id] = addr
		}
	}
	return out, nil
}

// RemovedReplicas reports the replica IDs the shard's committed membership
// has removed. Raft records a removed ID permanently and refuses to admit
// it again, so a caller finishes such a replica's removal instead.
func (a *replicaHost) RemovedReplicas(ctx context.Context, shardID uint64) (map[uint64]struct{}, error) {
	m, err := a.host.SyncGetShardMembership(ctx, shardID)
	if err != nil {
		return nil, err
	}
	out := make(map[uint64]struct{}, len(m.Removed))
	for id := range m.Removed {
		out[id] = struct{}{}
	}
	return out, nil
}

// RequestLeaderTransfer calls RequestLeaderTransfer on the shared Raft host.
// Used by service.RemoveFollowerNode's caller (removeReplicaFromGroup, see
// database_service.go's transferLeadershipAwayFromRemoved) to move
// leadership away from a replica before evicting it, so the config change
// removing it is driven to full replication by a leader that survives the
// removal rather than the replica being removed.
func (a *replicaHost) RequestLeaderTransfer(ctx context.Context, shardID, targetReplicaID uint64) error {
	return a.host.RequestLeaderTransfer(ctx, shardID, targetReplicaID)
}
