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

package rest

import (
	"context"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.RebalanceClient = (*Transport)(nil)

// restShardStats is the on-wire JSON shape for GET /v1/rebalance/shards/{id}.
type restShardStats struct {
	ShardID      uint64 `json:"shard_id"`
	KeyCount     uint64 `json:"key_count"`
	ByteCount    uint64 `json:"byte_count"`
	LeaderID     uint64 `json:"leader_id"`
	ReplicaCount int    `json:"replica_count"`
}

// restRebalanceMove is the on-wire JSON shape for a single planned move.
type restRebalanceMove struct {
	ShardID        uint64 `json:"shard_id"`
	FromNode       string `json:"from_node"`
	ToNode         string `json:"to_node"`
	EstimatedBytes uint64 `json:"estimated_bytes"`
}

// restRebalancePlan is the on-wire JSON shape for a rebalance plan (both the
// plan response and the execute request body).
type restRebalancePlan struct {
	Moves               []restRebalanceMove `json:"moves"`
	EstimatedDurationMs int64               `json:"estimated_duration_ms"`
	CurrentImbalance    float64             `json:"current_imbalance"`
	// Skipped has the server's skipped-move shape, which the transport
	// type shares field for field.
	Skipped []transport.RebalanceSkippedMove `json:"skipped,omitempty"`
}

// restRebalancePlanRequest is the on-wire JSON for POST /v1/rebalance/plan.
type restRebalancePlanRequest struct {
	MaxMoves      int     `json:"max_moves,omitempty"`
	DryRun        bool    `json:"dry_run,omitempty"`
	TargetBalance float64 `json:"target_balance,omitempty"`
}

// restRebalanceExecution is the on-wire JSON for POST /v1/rebalance/execute.
type restRebalanceExecution struct {
	MovesCompleted int                              `json:"moves_completed"`
	MovesFailed    int                              `json:"moves_failed"`
	Failures       []transport.RebalanceMoveFailure `json:"failures"`
	DurationMs     int64                            `json:"duration_ms"`
	FinalImbalance float64                          `json:"final_imbalance"`
}

// restNodeLoad is the on-wire JSON shape for a single node's load.
type restNodeLoad struct {
	NodeID      string `json:"node_id"`
	Address     string `json:"address"`
	ShardCount  int    `json:"shard_count"`
	TotalKeys   uint64 `json:"total_keys"`
	TotalBytes  uint64 `json:"total_bytes"`
	LeaderCount int    `json:"leader_count"`
}

// restImbalanceReport is the on-wire JSON shape for GET /v1/rebalance/report.
type restImbalanceReport struct {
	Nodes          []restNodeLoad `json:"nodes"`
	ImbalanceScore float64        `json:"imbalance_score"`
	Hotspots       []string       `json:"hotspots,omitempty"`
}

// GetShardStats returns statistics for a single shard. Maps to
// GET /v1/rebalance/shards/{id}.
func (t *Transport) GetShardStats(ctx context.Context, shardID uint64) (*transport.ShardStatsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var wire restShardStats
	path := "/v1/rebalance/shards/" + strconv.FormatUint(shardID, 10)
	if err := t.getJSONDecode(ctx, "GetShardStats", path, &wire); err != nil {
		return nil, err
	}
	return &transport.ShardStatsResponse{
		ShardID:      wire.ShardID,
		KeyCount:     wire.KeyCount,
		ByteCount:    wire.ByteCount,
		LeaderID:     wire.LeaderID,
		ReplicaCount: wire.ReplicaCount,
	}, nil
}

// CreateRebalancePlan generates a rebalance plan. Maps to POST /v1/rebalance/plan.
func (t *Transport) CreateRebalancePlan(ctx context.Context, req *transport.RebalancePlanRequest) (*transport.RebalancePlan, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	body := restRebalancePlanRequest{
		MaxMoves:      req.MaxMoves,
		DryRun:        req.DryRun,
		TargetBalance: req.TargetBalance,
	}
	var wire restRebalancePlan
	if err := t.postJSONDecode(ctx, "CreateRebalancePlan", "/v1/rebalance/plan", body, &wire); err != nil {
		return nil, err
	}
	return wire.toRebalancePlan(), nil
}

// ExecuteRebalance executes a rebalance plan. Maps to POST /v1/rebalance/execute.
func (t *Transport) ExecuteRebalance(ctx context.Context, plan *transport.RebalancePlan) (*transport.RebalanceResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var wire restRebalanceExecution
	if err := t.postJSONDecode(ctx, "ExecuteRebalance", "/v1/rebalance/execute", planToWire(plan), &wire); err != nil {
		return nil, err
	}
	return &transport.RebalanceResult{
		MovesCompleted: wire.MovesCompleted,
		MovesFailed:    wire.MovesFailed,
		Failures:       wire.Failures,
		Duration:       time.Duration(wire.DurationMs) * time.Millisecond,
		FinalImbalance: wire.FinalImbalance,
	}, nil
}

// GetImbalanceReport returns the cluster balance report. Maps to
// GET /v1/rebalance/report.
func (t *Transport) GetImbalanceReport(ctx context.Context) (*transport.ImbalanceReport, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var wire restImbalanceReport
	if err := t.getJSONDecode(ctx, "GetImbalanceReport", "/v1/rebalance/report", &wire); err != nil {
		return nil, err
	}
	nodes := make([]transport.NodeLoad, len(wire.Nodes))
	for i := range wire.Nodes {
		nodes[i] = transport.NodeLoad{
			NodeID:      wire.Nodes[i].NodeID,
			Address:     wire.Nodes[i].Address,
			ShardCount:  wire.Nodes[i].ShardCount,
			TotalKeys:   wire.Nodes[i].TotalKeys,
			TotalBytes:  wire.Nodes[i].TotalBytes,
			LeaderCount: wire.Nodes[i].LeaderCount,
		}
	}
	return &transport.ImbalanceReport{
		Nodes:          nodes,
		ImbalanceScore: wire.ImbalanceScore,
		Hotspots:       wire.Hotspots,
	}, nil
}

// toRebalancePlan converts the on-wire plan shape to the transport contract.
func (r *restRebalancePlan) toRebalancePlan() *transport.RebalancePlan {
	moves := make([]transport.RebalanceMove, len(r.Moves))
	for i := range r.Moves {
		moves[i] = transport.RebalanceMove{
			ShardID:        r.Moves[i].ShardID,
			FromNode:       r.Moves[i].FromNode,
			ToNode:         r.Moves[i].ToNode,
			EstimatedBytes: r.Moves[i].EstimatedBytes,
		}
	}
	return &transport.RebalancePlan{
		Moves:             moves,
		EstimatedDuration: time.Duration(r.EstimatedDurationMs) * time.Millisecond,
		CurrentImbalance:  r.CurrentImbalance,
		Skipped:           r.Skipped,
	}
}

// planToWire converts a transport RebalancePlan to the on-wire shape the
// execute endpoint expects.
func planToWire(plan *transport.RebalancePlan) restRebalancePlan {
	if plan == nil {
		return restRebalancePlan{}
	}
	moves := make([]restRebalanceMove, len(plan.Moves))
	for i := range plan.Moves {
		moves[i] = restRebalanceMove{
			ShardID:        plan.Moves[i].ShardID,
			FromNode:       plan.Moves[i].FromNode,
			ToNode:         plan.Moves[i].ToNode,
			EstimatedBytes: plan.Moves[i].EstimatedBytes,
		}
	}
	return restRebalancePlan{
		Moves:               moves,
		EstimatedDurationMs: plan.EstimatedDuration.Milliseconds(),
		CurrentImbalance:    plan.CurrentImbalance,
	}
}
