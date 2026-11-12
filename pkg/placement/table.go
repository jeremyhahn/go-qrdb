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

// Package placement implements the replicated placement table -- the one
// catalog authority for where every database's data lives, on every backend
// (go-qrdb Lane 2B, OD-4 A) -- and the load-aware placement scheduler for
// dedicated tenant Raft groups (Phase 4c-1b).
//
// A Placement records, for one database, which cluster node(s) host it: a
// dedicated Raft group's replicas, which replica is the leader, and where
// each replica keeps its on-disk state-machine data (BackendRaft); or, for
// every other Backend, the single node the database is bound to, carried in
// the same Replicas/LeaderReplicaID shape as a one-replica Raft group so a
// non-owner node's refusal and the forward to the owner reuse the Raft path
// unchanged (Placement.IsLocalBinding). Placements live in the replicated
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

// ReplicaState distinguishes a founding/established voting replica (always
// starts with join=false, preserving the no-data-loss restart invariant —
// see RaftBackendFactory.Services) from a replica added dynamically after
// the group formed. A dynamically added replica is admitted OBSERVER-FIRST
// (non-voting) so it can never transiently break the group's existing
// voting quorum while it catches up on a snapshot; only once it has caught
// up is it promoted to a full voting member by the shard's current leader.
// See the elastic-scaling design doc §B.2/§D.4 for the full observer→voter
// state machine and the quorum-break bug this corrects.
type ReplicaState uint8

const (
	// ReplicaStateActive is an established, full VOTING member: every attach
	// (initial bootstrap, or restart re-attach) uses join=false. This is the
	// zero value so every pre-existing placement entry (no "state" key) and
	// every Scheduler.Place-computed founding placement decodes to this
	// state unchanged.
	ReplicaStateActive ReplicaState = iota
	// ReplicaStateObserver is a replica added by PlaceAdditional (scale-up)
	// that has not yet received its first snapshot. It is admitted into the
	// Raft group as a NON-VOTING observer (RaftHost.AddObserver /
	// go-quicraft RequestAddObserverWithIdentity) so its addition never changes the
	// group's voting quorum size. Its FIRST attach uses join=true. The
	// reconciler running on the node that hosts this replica transitions it
	// to ReplicaStateObserverReady once go-quicraft reports the join
	// bootstrap window closed (IsShardJoining false) — every attach AFTER
	// that transition uses join=false, exactly like an established member,
	// even though it has not yet been promoted to a voter.
	ReplicaStateObserver
	// ReplicaStateObserverReady is a former ReplicaStateObserver replica
	// that has caught up (received its first snapshot / left the join
	// bootstrap window) but has not yet been promoted to a full voting
	// member. Only the shard's current Raft leader can perform that
	// promotion (RequestAddNodeWithIdentity targeting an existing observer's replica ID
	// promotes it — go-quicraft rejects the config change on any other
	// node), so the reconciler running on the LEADER node is responsible
	// for the ObserverReady → Active transition; the reconciler on the
	// replica's own host is only responsible for Observer → ObserverReady.
	ReplicaStateObserverReady
)

// Backend identifies the storage backend a placement's database is bound to.
// A dedicated Raft group (BackendRaft, the zero value) is the only kind this
// table carried before Lane 2B; the others name a single-node local binding
// -- one owning node, no Raft group -- extended into this same table so every
// backend has one catalog authority (go-qrdb Lane 2B, OD-4 A).
type Backend string

const (
	// BackendRaft is a dedicated, multi-replica Raft group. It is the zero
	// value so every placement entry persisted before Backend existed --
	// every placement this table held was a dedicated Raft group --
	// decodes unchanged.
	BackendRaft Backend = "raft"

	// BackendMemory is a single-node, in-memory local binding.
	BackendMemory Backend = "memory"

	// BackendFile is a single-node, file-backed local binding.
	BackendFile Backend = "file"

	// BackendPebble is a single-node, Pebble-backed local binding.
	BackendPebble Backend = "pebble"

	// BackendObjstore is a single-node, object-storage-backed local binding.
	BackendObjstore Backend = "objstore"
)

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
	// RequestAddNodeWithIdentity when the replica is materialized.
	RaftAddr string `json:"raft_addr"`

	// DataRoot is never written. The placement table is replicated and a path
	// names one node's disk, so every node resolves its own replica's
	// directory from the tenant's volume pool and slot. A record that carries
	// one is refused by the shard factory rather than trusted.
	DataRoot string `json:"data_root,omitempty"`

	// State distinguishes a founding/established replica from one added
	// dynamically after the group formed (scale-up). The zero value
	// (ReplicaStateActive) is backward compatible with every placement
	// entry persisted before this field existed.
	State ReplicaState `json:"state,omitempty"`
}

// Placement is the authoritative record of where a dedicated database's Raft
// group is placed across the cluster. It is JSON-serialized into the
// replicated placement table keyed by (TenantID, Database).
type Placement struct {
	// TenantID owns the database.
	TenantID string `json:"tenant_id"`

	// DatabaseID is the database's opaque, never-reused id
	// (database.DatabaseMeta.ID), allocated at create and recorded in the
	// tenant's catalog. The table is keyed by it --
	// __sys:placement/{tenant}/{dbid} -- so no database name appears in a
	// placement key.
	DatabaseID string `json:"database_id"`

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

	// Dropping is the replicated drop fence for a dedicated-raft database
	// (go-qrdb#482). DatabaseService sets it, through Store.SetDropping,
	// BEFORE the drop reclaims anything the database owns in the shared
	// system store; every node's own RaftBackendFactory.Services checks it
	// -- on both the cold-start path and a shard already cached from before
	// the drop began -- and refuses new operations for the (tenant,
	// database) pair with database.DroppingError while it is set. Because
	// the placement table is replicated, this is the one fence in the drop
	// path that is NOT node-local: it holds on every replica, survives a
	// leader change, and (like the rest of the placement entry) survives a
	// restart. It is removed together with the whole entry at the end of a
	// successful drop; a failed reclamation clears it via another
	// Store.SetDropping call, exactly as database.Manager's own local
	// fence is cleared, so a failed drop stays retryable.
	Dropping bool `json:"dropping,omitempty"`

	// DropCommitted marks that SOME node's database.Manager has already
	// completed the local, node-level half of this drop (go-qrdb#482 round
	// 3): the database's data reclaimed, its backend closed, its catalog
	// entry removed. It is set once, via Store.MarkDropCommitted, in the
	// narrow window between that local success and this placement entry's
	// own removal (finishRaftPlacementTeardown) -- the one window in which a
	// later retry landing on a DIFFERENT node needs to tell two identically
	// shaped failures apart: "manager.DropForTenant returned NotFoundError
	// because an earlier attempt already finished tearing this database
	// down" versus "manager.DropForTenant returned NotFoundError because
	// THIS node never had the local record to begin with" (a request that
	// reaches a non-authoritative replica). Only the first is safe to treat
	// as "finish the placement teardown"; treating the second the same way
	// silently reports success for a drop that never reclaimed anything or
	// released the tenant's quota. It is removed together with the whole
	// entry at the end of a successful drop, exactly like Dropping.
	DropCommitted bool `json:"drop_committed,omitempty"`

	// Backend is the storage backend the database is bound to. The zero
	// value ("") decodes as BackendRaft: every placement entry persisted
	// before this field existed was a dedicated Raft group, so an absent
	// key must not be read as "unbound".
	Backend Backend `json:"backend,omitempty"`
}

// IsLocalBinding reports whether p binds its database to a single owning
// node rather than a dedicated Raft group. A local binding's "replica set"
// is the degenerate case of one: Replicas holds exactly the owning node, RF
// is 1 and LeaderReplicaID names that one replica, so the existing
// Raft-shaped forwarding machinery (NotAReplicaError, the leader cache)
// serves it unchanged -- a non-owner node refuses exactly as it would for a
// one-replica Raft group, and the request is forwarded to the owner.
func (p *Placement) IsLocalBinding() bool {
	return p.Backend != "" && p.Backend != BackendRaft
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

// RefusalForNode returns the refusal a node not assigned a replica of p must
// answer with, and true; or a nil error and false when nodeID IS assigned
// one. It is the one place that constructs *NotAReplicaError from a
// Placement, so a dedicated Raft group and a local binding (RF 1, one
// Replica, IsLocalBinding true) are refused identically -- the existing
// forwarding machinery (the leader cache, replicaforward.Locator) resolves
// either the same way, keyed only on ShardID and Replicas.
func (p *Placement) RefusalForNode(nodeID string) (*NotAReplicaError, bool) {
	if _, assigned := p.ReplicaForNode(nodeID); assigned {
		return nil, false
	}
	return &NotAReplicaError{
		TenantID: p.TenantID, Database: p.Database, ShardID: p.ShardID,
		NodeID: nodeID, Replicas: p.Replicas, LeaderReplicaID: p.LeaderReplicaID,
	}, true
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

// MaxReplicaID returns the largest ReplicaID across all replicas in p.
// Returns 0 for an empty placement (the first replica will get ID 1).
func MaxReplicaID(p *Placement) uint64 {
	var max uint64
	for _, r := range p.Replicas {
		if r.ReplicaID > max {
			max = r.ReplicaID
		}
	}
	return max
}
