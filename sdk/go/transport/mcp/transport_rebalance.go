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

package mcp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.RebalanceClient = (*Transport)(nil)

// mcpRebalanceMove mirrors the JSON for a planned move emitted by the MCP
// rebalance tools (service.RebalanceMovePlan, PascalCase, no json tags).
type mcpRebalanceMove struct {
	ShardID        uint64
	FromNode       string
	ToNode         string
	EstimatedBytes uint64
}

// mcpRebalancePlan mirrors the JSON for a rebalance plan emitted by the MCP
// rebalance tools (service.RebalancePlanResult). Durations are nanoseconds.
type mcpRebalancePlan struct {
	Moves             []mcpRebalanceMove
	EstimatedDuration time.Duration
	CurrentImbalance  float64
}

// GetShardStats returns statistics for a single shard via the
// qrdb_rebalance_shard_stats tool.
func (t *Transport) GetShardStats(ctx context.Context, shardID uint64) (*transport.ShardStatsResponse, error) {
	text, err := t.callTool(ctx, "qrdb_rebalance_shard_stats", map[string]any{"shard_id": shardID})
	if err != nil {
		return nil, &transport.RequestError{Method: "GetShardStats", Err: err}
	}
	var wire struct {
		ShardID      uint64
		KeyCount     uint64
		ByteCount    uint64
		LeaderID     uint64
		ReplicaCount int
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetShardStats", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &transport.ShardStatsResponse{
		ShardID:      wire.ShardID,
		KeyCount:     wire.KeyCount,
		ByteCount:    wire.ByteCount,
		LeaderID:     wire.LeaderID,
		ReplicaCount: wire.ReplicaCount,
	}, nil
}

// CreateRebalancePlan generates a rebalance plan via the qrdb_rebalance_plan tool.
func (t *Transport) CreateRebalancePlan(ctx context.Context, req *transport.RebalancePlanRequest) (*transport.RebalancePlan, error) {
	args := map[string]any{}
	if req.MaxMoves != 0 {
		args["max_moves"] = req.MaxMoves
	}
	if req.DryRun {
		args["dry_run"] = true
	}
	if req.TargetBalance != 0 {
		args["target_balance"] = req.TargetBalance
	}

	text, err := t.callTool(ctx, "qrdb_rebalance_plan", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "CreateRebalancePlan", Err: err}
	}
	var wire mcpRebalancePlan
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CreateRebalancePlan", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return wire.toRebalancePlan(), nil
}

// ExecuteRebalance executes a rebalance plan via the qrdb_rebalance_execute tool.
func (t *Transport) ExecuteRebalance(ctx context.Context, plan *transport.RebalancePlan) (*transport.RebalanceResult, error) {
	planJSON, err := json.Marshal(planToMCP(plan))
	if err != nil {
		return nil, &transport.RequestError{Method: "ExecuteRebalance", Err: &jsonEncodeError{Err: err}}
	}

	text, err := t.callTool(ctx, "qrdb_rebalance_execute", map[string]any{"plan": string(planJSON)})
	if err != nil {
		return nil, &transport.RequestError{Method: "ExecuteRebalance", Err: err}
	}
	var wire struct {
		MovesCompleted int
		MovesFailed    int
		Duration       time.Duration
		FinalImbalance float64
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ExecuteRebalance", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &transport.RebalanceResult{
		MovesCompleted: wire.MovesCompleted,
		MovesFailed:    wire.MovesFailed,
		Duration:       wire.Duration,
		FinalImbalance: wire.FinalImbalance,
	}, nil
}

// GetImbalanceReport returns the cluster balance report via the
// qrdb_rebalance_report tool.
func (t *Transport) GetImbalanceReport(ctx context.Context) (*transport.ImbalanceReport, error) {
	text, err := t.callTool(ctx, "qrdb_rebalance_report", map[string]any{})
	if err != nil {
		return nil, &transport.RequestError{Method: "GetImbalanceReport", Err: err}
	}
	var wire struct {
		Nodes []struct {
			NodeID      string
			Address     string
			ShardCount  int
			TotalKeys   uint64
			TotalBytes  uint64
			LeaderCount int
		}
		ImbalanceScore float64
		Hotspots       []string
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetImbalanceReport", Err: &jsonDecodeError{Err: jsonErr}}
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

// toRebalancePlan converts the MCP wire plan shape to the transport contract.
func (m *mcpRebalancePlan) toRebalancePlan() *transport.RebalancePlan {
	moves := make([]transport.RebalanceMove, len(m.Moves))
	for i := range m.Moves {
		moves[i] = transport.RebalanceMove{
			ShardID:        m.Moves[i].ShardID,
			FromNode:       m.Moves[i].FromNode,
			ToNode:         m.Moves[i].ToNode,
			EstimatedBytes: m.Moves[i].EstimatedBytes,
		}
	}
	return &transport.RebalancePlan{
		Moves:             moves,
		EstimatedDuration: m.EstimatedDuration,
		CurrentImbalance:  m.CurrentImbalance,
	}
}

// planToMCP converts a transport RebalancePlan to the MCP wire shape the
// execute tool expects (service.RebalancePlanResult JSON).
func planToMCP(plan *transport.RebalancePlan) mcpRebalancePlan {
	if plan == nil {
		return mcpRebalancePlan{}
	}
	moves := make([]mcpRebalanceMove, len(plan.Moves))
	for i := range plan.Moves {
		moves[i] = mcpRebalanceMove{
			ShardID:        plan.Moves[i].ShardID,
			FromNode:       plan.Moves[i].FromNode,
			ToNode:         plan.Moves[i].ToNode,
			EstimatedBytes: plan.Moves[i].EstimatedBytes,
		}
	}
	return mcpRebalancePlan{
		Moves:             moves,
		EstimatedDuration: plan.EstimatedDuration,
		CurrentImbalance:  plan.CurrentImbalance,
	}
}
