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

// Package raftadmin defines the Raft-admin half of a qrdb node host: the
// membership, snapshot, shard lifecycle, leadership and node-host
// introspection operations a cluster administrator drives, as opposed to the
// proposals and reads the data services issue through service.Host.
//
// The two halves are separate interfaces so that the data path carries no
// go-quicraft engine type in its method set. Every Raft-backed host (the
// raft.HostAdapter over *quicraft.Host, and the in-memory LocalBackend)
// implements both; a consumer that needs only one takes only that one.
package raftadmin

import (
	"context"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// Host is the Raft-admin interface of a node host: membership changes,
// snapshots, shard start and stop, leadership transfer, drain and the
// node-host introspection the admin and placement services read.
type Host interface {
	// ShardCount returns the number of shards loaded on this host.
	ShardCount() int

	// NodeHostInfo returns a snapshot of the host's state for
	// monitoring and debugging.
	NodeHostInfo(opt quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo

	// HealthCheck returns the aggregate health status of the host.
	HealthCheck() *quicraft.HealthStatus

	// Drain transitions the host into drain mode.
	Drain() error

	// IsDraining returns true when the host is in drain mode.
	IsDraining() bool

	// RequestAddNodeWithIdentity requests adding a replica to a shard
	// together with the node identity go-quicraft commits for it. A host
	// with a transport refuses an empty identity for a new member.
	RequestAddNodeWithIdentity(ctx context.Context, shardID, replicaID uint64, addr, identity string, configChangeIndex uint64) (*quicraft.RequestState, error)

	// RequestAddObserverWithIdentity requests adding a replica to a shard
	// as a non-voting observer, together with the node identity go-quicraft
	// commits for it. See service.AdminService.PromoteMember for turning
	// the observer into a voter later.
	RequestAddObserverWithIdentity(ctx context.Context, shardID, replicaID uint64, addr, identity string, configChangeIndex uint64) (*quicraft.RequestState, error)

	// SyncGetShardMembership returns a shard's committed membership,
	// including the member identities go-quicraft committed.
	SyncGetShardMembership(ctx context.Context, shardID uint64) (*quicraft.Membership, error)

	// RequestRemoveNode requests removing a replica from a shard.
	RequestRemoveNode(ctx context.Context, shardID, replicaID uint64, configChangeIndex uint64) (*quicraft.RequestState, error)

	// RequestSnapshot requests an asynchronous snapshot for a shard.
	RequestSnapshot(ctx context.Context, shardID uint64) (*quicraft.RequestState, error)

	// RequestLeaderTransfer asks this host, which must lead shardID, to hand
	// leadership to targetReplicaID, a current voter. It validates the
	// target and returns; the transfer itself completes asynchronously (see
	// service.AdminService.TransferLeadership for the bounded wait).
	RequestLeaderTransfer(ctx context.Context, shardID, targetReplicaID uint64) error

	// GetShardRaftState returns the Raft state for a shard: the config
	// change index and current term. Used for monitoring and consistency
	// checks.
	GetShardRaftState(shardID uint64) (configChangeIndex uint64, term uint64, err error)

	// StartShard initializes and starts a Raft group for the given shard.
	// members maps replicaID to address for initial cluster members. join
	// indicates whether this node is joining an existing cluster. create
	// is the state machine factory. cfg provides per-shard Raft
	// configuration including ShardID and ReplicaID.
	StartShard(members map[uint64]string, join bool, create sm.CreateFunc, cfg config.Config) error

	// StopShard stops the Raft group for the given shard.
	StopShard(shardID uint64) error
}
