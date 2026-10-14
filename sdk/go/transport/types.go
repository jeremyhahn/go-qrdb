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

package transport

import "time"

// ---------------------------------------------------------------------------
// Health and status responses
// ---------------------------------------------------------------------------

// HealthResponse is returned by the health check endpoint.
type HealthResponse struct {
	Status             string `json:"status"`
	IsLeader           bool   `json:"is_leader"`
	DiagnosticsDropped uint64 `json:"diagnostics_dropped"`
}

// StatusResponse provides detailed host and shard status information.
type StatusResponse struct {
	NodeID      string `json:"node_id"`
	RaftAddress string `json:"raft_address"`
	IsLeader    bool   `json:"is_leader"`
	LeaderID    uint64 `json:"leader_id"`
	ShardCount  int    `json:"shard_count"`
	IsDraining  bool   `json:"is_draining"`
	Shards      any    `json:"shards"`
}

// PeersResponse contains the peer membership for all loaded shards.
type PeersResponse struct {
	Shards []ShardPeers `json:"shards"`
}

// ShardPeers describes the membership of a single shard.
type ShardPeers struct {
	ShardID  uint64     `json:"shard_id"`
	LeaderID uint64     `json:"leader_id"`
	Peers    []PeerInfo `json:"peers"`
}

// PeerInfo describes a single peer in a shard.
type PeerInfo struct {
	ReplicaID uint64 `json:"replica_id"`
	Address   string `json:"address"`
	IsLeader  bool   `json:"is_leader"`
}

// DrainResponse is returned by the drain endpoint.
type DrainResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// KV request/response types
// ---------------------------------------------------------------------------

// KVListResponse is the JSON payload for key listing responses.
type KVListResponse struct {
	Keys []string `json:"keys"`
}

// KVScanResponse is the JSON payload for prefix scan responses
// returning key-value pairs.
type KVScanResponse struct {
	Pairs map[string][]byte `json:"pairs"`
}

// ---------------------------------------------------------------------------
// Membership and snapshot request types
// ---------------------------------------------------------------------------

// AddMemberRequest is the JSON payload for adding a shard member.
type AddMemberRequest struct {
	ShardID   uint64 `json:"shard_id"`
	ReplicaID uint64 `json:"replica_id"`
	Address   string `json:"address"`
	// Identity is the optional operator-supplied Raft member identity; see
	// AdminClient.AddMember.
	Identity string `json:"identity,omitempty"`
}

// RemoveMemberRequest is the JSON payload for removing a shard member.
type RemoveMemberRequest struct {
	ShardID   uint64 `json:"shard_id"`
	ReplicaID uint64 `json:"replica_id"`
}

// PromoteMemberRequest is the JSON payload for promoting a committed
// observer to a voter.
type PromoteMemberRequest struct {
	ShardID   uint64 `json:"shard_id"`
	ReplicaID uint64 `json:"replica_id"`
}

// SnapshotRequest is the JSON payload for requesting a shard snapshot.
type SnapshotRequest struct {
	ShardID uint64 `json:"shard_id"`
}

// MintJoinTokenRequest is the JSON payload for minting a member-side CSR
// join token. TTLSeconds <= 0 uses the server's default TTL (see
// clusterenroll.DefaultJoinTokenTTL).
type MintJoinTokenRequest struct {
	NodeID     string `json:"node_id"`
	TTLSeconds int64  `json:"ttl_seconds,omitempty"`
}

// MintJoinTokenResponse is the JSON payload returned by a successful mint.
// Token is returned exactly once; there is no "list minted tokens" read path.
type MintJoinTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// AddObserverMemberRequest is the JSON payload for adding a shard member as
// a non-voting observer.
type AddObserverMemberRequest struct {
	ShardID   uint64 `json:"shard_id"`
	ReplicaID uint64 `json:"replica_id"`
	Address   string `json:"address"`
	Identity  string `json:"identity,omitempty"`
}

// PendingJoinRequest is the client-side view of one queued, tokenless
// member-side CSR join request awaiting a decision -- see
// pkg/service.PendingJoinRequest.
type PendingJoinRequest struct {
	ID          string    `json:"id"`
	NodeURI     string    `json:"node_uri,omitempty"`
	ReplicaID   uint64    `json:"replica_id,omitempty"`
	Features    []string  `json:"features,omitempty"`
	SubmittedAt time.Time `json:"submitted_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// ApproveJoinRequestRequest is the JSON payload for approving a queued CSR.
type ApproveJoinRequestRequest struct {
	ID     string `json:"id"`
	NodeID string `json:"node_id"`
}

// RejectJoinRequestRequest is the JSON payload for rejecting a queued CSR.
type RejectJoinRequestRequest struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// RetireJoinIdentityRequest is the JSON payload for permanently refusing
// any future admission for a node identity (see
// clusterenroll.RetireJoinIdentity). Reason is an optional, free-text
// operator note carried for audit only.
type RetireJoinIdentityRequest struct {
	NodeID string `json:"node_id"`
	Reason string `json:"reason,omitempty"`
}

// CapabilitiesResponse describes the compile-time feature set of the QRDB binary.
type CapabilitiesResponse struct {
	Mode        string `json:"mode"`
	KV          bool   `json:"kv"`
	MVCC        bool   `json:"mvcc"`
	Transaction bool   `json:"transaction"`
	Batch       bool   `json:"batch"`
	Watch       bool   `json:"watch"`
	Lease       bool   `json:"lease"`
	Lock        bool   `json:"lock"`
	Index       bool   `json:"index"`
	Cluster     bool   `json:"cluster"`
	Backup      bool   `json:"backup"`
	Rebalance   bool   `json:"rebalance"`
	TimeSeries  bool   `json:"timeSeries"`
	Vector      bool   `json:"vector"`
	SQL         bool   `json:"sql"`
	MySQL       bool   `json:"mysql"`
	PostgreSQL  bool   `json:"postgresql"`
	Etcd        bool   `json:"etcd"`
	FTS         bool   `json:"fts"`

	// UnencryptedAtRest lists, when seal is enabled, where the node keeps
	// data on disk outside the barrier's at-rest encryption. Empty otherwise.
	UnencryptedAtRest []string `json:"unencryptedAtRest,omitempty"`
}

// ---------------------------------------------------------------------------
// Shard management request/response types
// ---------------------------------------------------------------------------

// StartShardRequest is the JSON payload for starting a new Raft shard.
type StartShardRequest struct {
	// ShardID is the unique shard identifier to start.
	ShardID uint64 `json:"shard_id"`

	// ReplicaID is this node's replica identifier within the shard.
	ReplicaID uint64 `json:"replica_id"`

	// Members maps replicaID to Raft network address for initial
	// cluster members. Required for new clusters; empty for joins.
	Members map[uint64]string `json:"members,omitempty"`

	// Join indicates whether this node is joining an existing shard
	// rather than forming a new one.
	Join bool `json:"join"`
}

// StopShardRequest is the JSON payload for stopping a Raft shard.
type StopShardRequest struct {
	ShardID uint64 `json:"shard_id"`
}

// ShardStateResponse describes the Raft state of a single shard.
type ShardStateResponse struct {
	// ShardID is the shard's unique identifier.
	ShardID uint64 `json:"shard_id"`

	// ConfigChangeIndex is the current Raft configuration change index.
	ConfigChangeIndex uint64 `json:"config_change_index"`

	// Term is the current Raft term.
	Term uint64 `json:"term"`

	// LeaderID is the current leader's replica ID. 0 if unknown.
	LeaderID uint64 `json:"leader_id"`

	// HasLeader is true when a leader is known for this shard.
	HasLeader bool `json:"has_leader"`
}

// TopologyResponse describes the full cluster topology as seen from
// a single node.
type TopologyResponse struct {
	// NodeID is the unique host identifier.
	NodeID string `json:"node_id"`

	// RaftAddress is the host's Raft network address.
	RaftAddress string `json:"raft_address"`

	// ShardCount is the number of shards loaded on this host.
	ShardCount int `json:"shard_count"`

	// Shards contains per-shard topology information.
	Shards []ShardInfo `json:"shards"`
}

// ShardInfo describes a single shard's topology including its
// membership and leadership state.
type ShardInfo struct {
	// ShardID is the shard's unique identifier.
	ShardID uint64 `json:"shard_id"`

	// ReplicaID is this node's replica identifier within the shard.
	ReplicaID uint64 `json:"replica_id"`

	// IsLeader is true when this replica is the current leader.
	IsLeader bool `json:"is_leader"`

	// LeaderID is the current leader's replica ID. 0 if unknown.
	LeaderID uint64 `json:"leader_id"`

	// Nodes maps replicaID to Raft network address for all known
	// cluster members.
	Nodes map[uint64]string `json:"nodes"`
}

// ReplicaStatesResponse lists one node's replicas, ordered by shard ID.
// Witnesses hold no state machine and are never listed.
type ReplicaStatesResponse struct {
	// NodeID is the answering node's host identifier.
	NodeID string `json:"node_id"`

	// Replicas holds one entry per replica on the node.
	Replicas []ReplicaState `json:"replicas"`
}

// ReplicaState is one replica's applied index and the hash of its state
// machine taken at that index. Two replicas of a shard at the same applied
// index hold the same state exactly when their hashes are equal; at
// different indexes the hashes say nothing.
type ReplicaState struct {
	// ShardID is the shard the replica belongs to.
	ShardID uint64 `json:"shard_id"`

	// ReplicaID is the replica's identifier within the shard.
	ReplicaID uint64 `json:"replica_id"`

	// AppliedIndex is the last log index the replica's state machine applied.
	AppliedIndex uint64 `json:"applied_index"`

	// Hash is the state-machine hash taken at AppliedIndex.
	Hash uint64 `json:"hash"`
}

// OKResponse is a simple success response.
type OKResponse struct {
	Status string `json:"status"`
}

// ErrorResponse is a simple error response.
type ErrorResponse struct {
	Error string `json:"error"`
}
