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

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// Rebalance / shard stats types
// ---------------------------------------------------------------------------

// ShardStatsResponse contains statistics for a single shard.
type ShardStatsResponse struct {
	// ShardID is the shard's unique identifier.
	ShardID uint64 `json:"shard_id"`

	// KeyCount is the number of keys stored in the shard.
	KeyCount uint64 `json:"key_count"`

	// ByteCount is the total byte size of all keys and values in the shard.
	ByteCount uint64 `json:"byte_count"`

	// LeaderID is the current leader's replica ID. 0 if unknown.
	LeaderID uint64 `json:"leader_id"`

	// ReplicaCount is the number of replicas for this shard.
	ReplicaCount int `json:"replica_count"`
}

// RebalancePlanRequest describes the parameters for generating a rebalance plan.
type RebalancePlanRequest struct {
	// MaxMoves is the maximum number of shard moves to include in the plan.
	// 0 means no limit.
	MaxMoves int `json:"max_moves"`

	// DryRun when true generates the plan without executing it.
	DryRun bool `json:"dry_run"`

	// TargetBalance is the target balance percentage (0-100). A value of 0
	// means perfectly balanced. Higher values allow more imbalance tolerance.
	TargetBalance float64 `json:"target_balance"`
}

// RebalanceMove describes a single planned shard move operation.
type RebalanceMove struct {
	// ShardID is the shard to be moved.
	ShardID uint64 `json:"shard_id"`

	// FromNode is the source node address.
	FromNode string `json:"from_node"`

	// ToNode is the destination node address.
	ToNode string `json:"to_node"`

	// EstimatedBytes is the estimated data size to be transferred.
	EstimatedBytes uint64 `json:"estimated_bytes"`
}

// RebalancePlan contains the list of planned shard moves and metadata
// about the expected outcome.
type RebalancePlan struct {
	// Moves is the ordered list of shard move operations.
	Moves []RebalanceMove `json:"moves"`

	// EstimatedDuration is the estimated time to complete all moves.
	EstimatedDuration time.Duration `json:"estimated_duration"`

	// CurrentImbalance is the current imbalance score before rebalancing.
	// 0.0 means perfectly balanced; higher values indicate greater imbalance.
	CurrentImbalance float64 `json:"current_imbalance"`

	// Skipped names the moves the planner considered and refused, each
	// with its reason (an isolated tenant's serving set or home group).
	// It is informational: executing a plan ignores it.
	Skipped []RebalanceSkippedMove `json:"skipped,omitempty"`
}

// RebalanceSkippedMove is a move the planner considered and refused.
type RebalanceSkippedMove struct {
	ShardID  uint64 `json:"shard_id"`
	FromNode string `json:"from_node"`
	ToNode   string `json:"to_node"`
	// Reason names why the planner refused the move.
	Reason string `json:"reason"`
	// Error is the refusal's detail; empty when Reason says it all.
	Error string `json:"error,omitempty"`
}

// RebalanceResult contains the outcome of an executed rebalance operation.
type RebalanceResult struct {
	// MovesCompleted is the number of moves that completed successfully.
	MovesCompleted int `json:"moves_completed"`

	// MovesFailed is the number of moves that failed.
	MovesFailed int `json:"moves_failed"`

	// Failures carries one entry per failed move, in plan order, naming the
	// move and why it failed.
	Failures []RebalanceMoveFailure `json:"failures,omitempty"`

	// Duration is the actual time taken to execute all moves.
	Duration time.Duration `json:"duration"`

	// FinalImbalance is the imbalance score after rebalancing.
	FinalImbalance float64 `json:"final_imbalance"`
}

// RebalanceMoveFailure reports a single move the server could not complete.
type RebalanceMoveFailure struct {
	// ShardID is the shard the move targeted.
	ShardID uint64 `json:"shard_id"`

	// FromNode is the move's source node address.
	FromNode string `json:"from_node"`

	// ToNode is the move's destination node address.
	ToNode string `json:"to_node"`

	// Step names the phase of a dedicated-group move that failed; it is
	// empty for a generic shard move.
	Step string `json:"step,omitempty"`

	// Error is the failure's message.
	Error string `json:"error"`
}

// NodeLoad describes the load distribution on a single node.
type NodeLoad struct {
	// NodeID is the unique host identifier.
	NodeID string `json:"node_id"`

	// Address is the node's Raft network address.
	Address string `json:"address"`

	// ShardCount is the number of shards hosted by this node.
	ShardCount int `json:"shard_count"`

	// TotalKeys is the total key count across all shards on this node.
	TotalKeys uint64 `json:"total_keys"`

	// TotalBytes is the total byte count across all shards on this node.
	TotalBytes uint64 `json:"total_bytes"`

	// LeaderCount is the number of shards for which this node is leader.
	LeaderCount int `json:"leader_count"`
}

// ImbalanceReport describes the cluster's current load distribution
// and identifies hotspots.
type ImbalanceReport struct {
	// Nodes contains per-node load distribution.
	Nodes []NodeLoad `json:"nodes"`

	// ImbalanceScore is the overall cluster imbalance metric.
	// 0.0 means perfectly balanced; higher values indicate greater imbalance.
	ImbalanceScore float64 `json:"imbalance_score"`

	// Hotspots contains node IDs that are significantly overloaded
	// compared to the cluster average.
	Hotspots []string `json:"hotspots"`
}

// ---------------------------------------------------------------------------
// RebalanceClient interface
// ---------------------------------------------------------------------------

// RebalanceClient provides shard statistics and cluster rebalancing operations.
type RebalanceClient interface {
	// GetShardStats returns statistics for a single shard including
	// key count, byte count, leader, and replica count.
	GetShardStats(ctx context.Context, shardID uint64) (*ShardStatsResponse, error)

	// CreateRebalancePlan analyzes shard distribution across nodes and
	// generates an optimal move plan to balance the cluster.
	CreateRebalancePlan(ctx context.Context, req *RebalancePlanRequest) (*RebalancePlan, error)

	// ExecuteRebalance executes the given rebalance plan by moving
	// shards between nodes.
	ExecuteRebalance(ctx context.Context, plan *RebalancePlan) (*RebalanceResult, error)

	// GetImbalanceReport returns the current cluster balance report
	// including per-node load distribution and hotspot identification.
	GetImbalanceReport(ctx context.Context) (*ImbalanceReport, error)
}
