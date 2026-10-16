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
	"math"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// toolStateHashes defines the qrdb_state_hashes tool schema.
func toolStateHashes() mcp.Tool {
	return mcp.NewTool("qrdb_state_hashes",
		mcp.WithDescription("Return the applied index and SHA-256 state digest of every replica this node runs, isolated tenants' included, answered locally with no proposal. Two replicas of a shard at the same applied index must report the same hash. System tenant only."),
		mcp.WithArray("shard_ids",
			mcp.Description("Optional list of shard IDs to report. Empty reports every shard this node replicates."),
			mcp.WithNumberItems(),
		),
	)
}

// handleStateHashes returns a tool handler that delegates to adminSvc's
// ReplicaStateHasher capability (see service.ReplicaStateHasher).
func handleStateHashes(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		hasher, ok := adminSvc.(service.ReplicaStateHasher)
		if !ok {
			return nil, &ToolError{Tool: "qrdb_state_hashes", Err: &service.ReplicaStateHashesUnsupportedError{}}
		}
		shardIDs, err := parseShardIDList(req, "shard_ids")
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_state_hashes", Err: err}
		}
		resp, err := hasher.StateHashes(ctx, shardIDs)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_state_hashes", Err: err}
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_state_hashes", Err: err}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// parseShardIDList reads an optional array of shard IDs, refusing with
// *ToolArgumentError any element that is not a non-negative whole number in
// range rather than dropping it: a dropped element would widen the selection
// the caller asked for.
func parseShardIDList(req mcp.CallToolRequest, key string) ([]uint64, error) {
	raw, ok := req.GetArguments()[key]
	if !ok || raw == nil {
		return nil, nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil, &ToolArgumentError{Argument: key, Reason: "must be an array of shard IDs"}
	}
	ids := make([]uint64, 0, len(items))
	for _, item := range items {
		var f float64
		switch v := item.(type) {
		case float64:
			f = v
		case int:
			f = float64(v)
		default:
			return nil, &ToolArgumentError{Argument: key, Reason: "every element must be a number"}
		}
		// float64 holds every integer up to 2^53 exactly; a shard ID above
		// that cannot be named through a JSON number without rounding.
		if f < 0 || f > 1<<53 || f != math.Trunc(f) {
			return nil, &ToolArgumentError{Argument: key, Reason: "every element must be a whole number between 0 and 2^53"}
		}
		ids = append(ids, uint64(f))
	}
	return ids, nil
}
