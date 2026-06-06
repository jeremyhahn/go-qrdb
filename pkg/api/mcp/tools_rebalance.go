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

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// rebalanceTools returns the MCP tool registrations for cluster rebalance
// operations: shard statistics, plan generation, execution, and reporting.
func rebalanceTools(svc service.RebalanceServicer) []mcpserver.ServerTool {
	return []mcpserver.ServerTool{
		{Tool: toolRebalanceShardStats(), Handler: handleRebalanceShardStats(svc)},
		{Tool: toolRebalancePlan(), Handler: handleRebalancePlan(svc)},
		{Tool: toolRebalanceExecute(), Handler: handleRebalanceExecute(svc)},
		{Tool: toolRebalanceReport(), Handler: handleRebalanceReport(svc)},
	}
}

// toolRebalanceShardStats defines the qrdb_rebalance_shard_stats tool schema.
func toolRebalanceShardStats() mcp.Tool {
	return mcp.NewTool("qrdb_rebalance_shard_stats",
		mcp.WithDescription("Return statistics (key count, byte count, leader, replicas) for a single shard."),
		mcp.WithNumber("shard_id",
			mcp.Required(),
			mcp.Description("The shard ID to inspect."),
		),
	)
}

// handleRebalanceShardStats returns a tool handler that reports shard stats.
func handleRebalanceShardStats(svc service.RebalanceServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		shardID := uint64(mcp.ParseInt64(req, "shard_id", 0))
		stats, err := svc.GetShardStats(ctx, shardID)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_rebalance_shard_stats", Err: err}
		}
		return marshalToolResult("qrdb_rebalance_shard_stats", stats)
	}
}

// toolRebalancePlan defines the qrdb_rebalance_plan tool schema.
func toolRebalancePlan() mcp.Tool {
	return mcp.NewTool("qrdb_rebalance_plan",
		mcp.WithDescription("Generate an optimal shard-move plan to balance the cluster."),
		mcp.WithNumber("max_moves",
			mcp.Description("Maximum number of shard moves to include. 0 means no limit."),
		),
		mcp.WithBoolean("dry_run",
			mcp.Description("Generate the plan without executing it."),
		),
		mcp.WithNumber("target_balance",
			mcp.Description("Target balance percentage (0-100)."),
		),
	)
}

// handleRebalancePlan returns a tool handler that generates a rebalance plan.
func handleRebalancePlan(svc service.RebalanceServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		plan, err := svc.CreateRebalancePlan(ctx, &service.RebalancePlanOptions{
			MaxMoves:      mcp.ParseInt(req, "max_moves", 0),
			DryRun:        mcp.ParseBoolean(req, "dry_run", false),
			TargetBalance: mcp.ParseFloat64(req, "target_balance", 0),
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_rebalance_plan", Err: err}
		}
		return marshalToolResult("qrdb_rebalance_plan", plan)
	}
}

// toolRebalanceExecute defines the qrdb_rebalance_execute tool schema.
func toolRebalanceExecute() mcp.Tool {
	return mcp.NewTool("qrdb_rebalance_execute",
		mcp.WithDescription("Execute a rebalance plan (as returned by qrdb_rebalance_plan) by moving shards between nodes."),
		mcp.WithString("plan",
			mcp.Required(),
			mcp.Description("The rebalance plan as a JSON object (the output of qrdb_rebalance_plan)."),
		),
	)
}

// handleRebalanceExecute returns a tool handler that executes a rebalance plan.
func handleRebalanceExecute(svc service.RebalanceServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		planJSON := mcp.ParseString(req, "plan", "")
		if planJSON == "" {
			return mcp.NewToolResultError("plan is required"), nil
		}
		var plan service.RebalancePlanResult
		if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
			return mcp.NewToolResultError("plan must be a valid JSON rebalance plan: " + err.Error()), nil
		}
		result, err := svc.ExecuteRebalance(ctx, &plan)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_rebalance_execute", Err: err}
		}
		return marshalToolResult("qrdb_rebalance_execute", result)
	}
}

// toolRebalanceReport defines the qrdb_rebalance_report tool schema.
func toolRebalanceReport() mcp.Tool {
	return mcp.NewTool("qrdb_rebalance_report",
		mcp.WithDescription("Return the cluster's current load distribution, imbalance score, and hotspots."),
	)
}

// handleRebalanceReport returns a tool handler that reports cluster imbalance.
func handleRebalanceReport(svc service.RebalanceServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		report, err := svc.GetImbalanceReport(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_rebalance_report", Err: err}
		}
		return marshalToolResult("qrdb_rebalance_report", report)
	}
}
