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

// ---------------------------------------------------------------------------
// Health and status responses
// ---------------------------------------------------------------------------

// HealthResponse is returned by the health check endpoint.
type HealthResponse struct {
	Status   string `json:"status"`
	IsLeader bool   `json:"is_leader"`
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

// ---------------------------------------------------------------------------
// Membership and snapshot request types
// ---------------------------------------------------------------------------

// AddMemberRequest is the JSON payload for adding a shard member.
type AddMemberRequest struct {
	ShardID   uint64 `json:"shard_id"`
	ReplicaID uint64 `json:"replica_id"`
	Address   string `json:"address"`
}

// RemoveMemberRequest is the JSON payload for removing a shard member.
type RemoveMemberRequest struct {
	ShardID   uint64 `json:"shard_id"`
	ReplicaID uint64 `json:"replica_id"`
}

// SnapshotRequest is the JSON payload for requesting a shard snapshot.
type SnapshotRequest struct {
	ShardID uint64 `json:"shard_id"`
}

// OKResponse is a simple success response.
type OKResponse struct {
	Status string `json:"status"`
}

// ErrorResponse is a simple error response.
type ErrorResponse struct {
	Error string `json:"error"`
}
