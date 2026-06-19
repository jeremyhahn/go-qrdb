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

// Package placement implements the replicated placement table and the
// load-aware placement scheduler for dedicated tenant Raft groups (Phase
// 4c-1b).
//
// A Placement records, for one dedicated-raft database, which cluster nodes
// host its Raft replicas, which replica is the leader, and where each replica
// keeps its on-disk state-machine data. Placements live in the replicated
// system KV under PlacementPrefix so every node converges on the same view; a
// per-node reconciler (Phase 4c-1d) watches the table and self-starts/stops the
// replicas assigned to it.
//
// The scheduler computes a placement from a snapshot of cluster topology and
// load. Its core is pure (it depends only on an injected TopologyProvider), so
// it is fully unit-testable without a live cluster; the production
// TopologyProvider is implemented separately over the Raft host and the
// RebalanceService load scoring.
package placement

// Replica is a single voting member of a dedicated database's Raft group. It
// binds a Raft replica ID to the cluster node that hosts it and the on-disk
// root under which that node keeps the replica's state-machine data.
type Replica struct {
	// ReplicaID is the replica's Raft identifier within the group, assigned
	// by the scheduler. Replica IDs are unique within a single Placement.
	ReplicaID uint64 `json:"replica_id"`

	// NodeID is the cluster-unique identifier of the node hosting this
	// replica. It matches a TopologyProvider node's NodeID and the local
	// node identity used by the reconciler to recognize its own replicas.
	NodeID string `json:"node_id"`

	// RaftAddr is the node's Raft network address, fed to StartShard /
	// RequestAddNode when the replica is materialized.
	RaftAddr string `json:"raft_addr"`

	// DataRoot is the on-disk root under which this replica keeps its
	// state-machine data on its host node.
	DataRoot string `json:"data_root"`
}

// Placement is the authoritative record of where a dedicated database's Raft
// group is placed across the cluster. It is JSON-serialized into the
// replicated placement table keyed by (TenantID, Database).
type Placement struct {
	// TenantID owns the database.
	TenantID string `json:"tenant_id"`

	// Database is the database name, unique within the tenant.
	Database string `json:"database"`

	// ShardID is the Raft shard ID allocated for the database's dedicated
	// group, shared by every replica.
	ShardID uint64 `json:"shard_id"`

	// RF is the replication factor: the number of voting replicas, equal to
	// len(Replicas).
	RF int `json:"rf"`

	// LeaderReplicaID is the replica chosen to bootstrap as the initial
	// leader (the least-loaded of the selected replicas).
	LeaderReplicaID uint64 `json:"leader_replica_id"`

	// Replicas lists the placed voting replicas, one per distinct node.
	Replicas []Replica `json:"replicas"`

	// DedicatedWAL records that the database opted into a per-tenant-KEY WAL
	// (4d Option C): every replica must write this shard's Raft log to a
	// dedicated WAL store barrier-encrypted under the owning tenant's barrier,
	// rather than the shared system-barrier WAL. It is carried in the replicated
	// placement table so follower replicas materialized by the reconciler honor
	// the same WAL routing the leader chose at create time.
	DedicatedWAL bool `json:"dedicated_wal,omitempty"`
}

// Leader returns the leader replica and true, or a zero Replica and false when
// no replica matches LeaderReplicaID.
func (p *Placement) Leader() (Replica, bool) {
	for _, r := range p.Replicas {
		if r.ReplicaID == p.LeaderReplicaID {
			return r, true
		}
	}
	return Replica{}, false
}

// ReplicaForNode returns the replica hosted by the given node and true, or a
// zero Replica and false when the node hosts no replica of this placement. The
// per-node reconciler uses it to recognize the replica it must run locally.
func (p *Placement) ReplicaForNode(nodeID string) (Replica, bool) {
	for _, r := range p.Replicas {
		if r.NodeID == nodeID {
			return r, true
		}
	}
	return Replica{}, false
}

// Members returns the replica-ID -> Raft-address map for the placement, in the
// shape StartShard expects for a multi-member bootstrap.
func (p *Placement) Members() map[uint64]string {
	members := make(map[uint64]string, len(p.Replicas))
	for _, r := range p.Replicas {
		members[r.ReplicaID] = r.RaftAddr
	}
	return members
}
