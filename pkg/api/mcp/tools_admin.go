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

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// adminTools returns the MCP tool registrations for admin control-plane
// operations.
func adminTools(adminSvc service.AdminServicer) []serverTool {
	return []serverTool{
		{
			Tool:    toolHealth(),
			Perm:    permission.AdminHealth,
			Handler: handleHealth(adminSvc),
		},
		{
			Tool:    toolStatus(),
			Perm:    permission.AdminStatus,
			Handler: handleStatus(adminSvc),
		},
		{
			Tool:    toolPeers(),
			Perm:    permission.AdminPeers,
			Handler: handlePeers(adminSvc),
		},
		{
			Tool:    toolAddMember(),
			Perm:    permission.AdminAddMember,
			Handler: handleAddMember(adminSvc),
		},
		{
			Tool:    toolRemoveMember(),
			Perm:    permission.AdminRemoveMember,
			Handler: handleRemoveMember(adminSvc),
		},
		{
			Tool:    toolSnapshot(),
			Perm:    permission.AdminSnapshot,
			Handler: handleSnapshot(adminSvc),
		},
		{
			Tool:    toolDrain(),
			Perm:    permission.AdminDrain,
			Handler: handleDrain(adminSvc),
		},
		{
			Tool:    toolCapabilities(),
			Perm:    permission.AdminCapabilities,
			Handler: handleCapabilities(adminSvc),
		},
		{
			Tool:    toolStartShard(),
			Perm:    permission.AdminStartShard,
			Handler: handleStartShard(adminSvc),
		},
		{
			Tool:    toolStopShard(),
			Perm:    permission.AdminStopShard,
			Handler: handleStopShard(adminSvc),
		},
		{
			Tool:    toolGetShardState(),
			Perm:    permission.AdminShardState,
			Handler: handleGetShardState(adminSvc),
		},
		{
			Tool:    toolGetTopology(),
			Perm:    permission.AdminTopology,
			Handler: handleGetTopology(adminSvc),
		},
	}
}

// toolHealth defines the qrdb_health tool schema.
func toolHealth() mcp.Tool {
	return mcp.NewTool("qrdb_health",
		mcp.WithDescription("Return the current health status of the QRDB host."),
	)
}

// handleHealth returns a tool handler that delegates to adminSvc.Health.
func handleHealth(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		hs, err := adminSvc.Health(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_health", Err: err}
		}

		data, jsonErr := json.Marshal(hs)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_health", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolStatus defines the qrdb_status tool schema.
func toolStatus() mcp.Tool {
	return mcp.NewTool("qrdb_status",
		mcp.WithDescription("Return detailed status information about the QRDB host and all loaded shards."),
	)
}

// handleStatus returns a tool handler that delegates to adminSvc.Status.
func handleStatus(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		si, err := adminSvc.Status(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_status", Err: err}
		}

		data, jsonErr := json.Marshal(si)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_status", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolPeers defines the qrdb_peers tool schema.
func toolPeers() mcp.Tool {
	return mcp.NewTool("qrdb_peers",
		mcp.WithDescription("Return the peer membership for all loaded shards."),
	)
}

// handlePeers returns a tool handler that delegates to adminSvc.Peers.
func handlePeers(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		pi, err := adminSvc.Peers(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_peers", Err: err}
		}

		data, jsonErr := json.Marshal(pi)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_peers", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolAddMember defines the qrdb_add_member tool schema.
func toolAddMember() mcp.Tool {
	return mcp.NewTool("qrdb_add_member",
		mcp.WithDescription("Add a new replica to the specified shard."),
		mcp.WithNumber("shard_id",
			mcp.Required(),
			mcp.Description("The shard ID to add the replica to."),
		),
		mcp.WithNumber("replica_id",
			mcp.Required(),
			mcp.Description("The replica ID of the new member."),
		),
		mcp.WithString("raft_addr",
			mcp.Required(),
			mcp.Description("The Raft network address of the new member."),
		),
		mcp.WithString("identity",
			mcp.Description("The member's Raft identity (spki:<sha256-hex>, from `qrdbd node-identity --cert`). "+
				"Omit it to use the identity the cluster already committed for the node; required for a node no committed membership names."),
		),
	)
}

// handleAddMember returns a tool handler that delegates to adminSvc.AddMember.
func handleAddMember(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		shardID := mcp.ParseUInt64(req, "shard_id", 0)
		replicaID := mcp.ParseUInt64(req, "replica_id", 0)
		raftAddr := mcp.ParseString(req, "raft_addr", "")
		identity := mcp.ParseString(req, "identity", "")

		if shardID == 0 {
			return mcp.NewToolResultError("shard_id is required"), nil
		}
		if replicaID == 0 {
			return mcp.NewToolResultError("replica_id is required"), nil
		}
		if raftAddr == "" {
			return mcp.NewToolResultError("raft_addr is required"), nil
		}

		if err := adminSvc.AddMember(ctx, shardID, replicaID, raftAddr, identity); err != nil {
			return nil, &ToolError{Tool: "qrdb_add_member", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolRemoveMember defines the qrdb_remove_member tool schema.
func toolRemoveMember() mcp.Tool {
	return mcp.NewTool("qrdb_remove_member",
		mcp.WithDescription("Remove a replica from the specified shard."),
		mcp.WithNumber("shard_id",
			mcp.Required(),
			mcp.Description("The shard ID to remove the replica from."),
		),
		mcp.WithNumber("replica_id",
			mcp.Required(),
			mcp.Description("The replica ID to remove."),
		),
	)
}

// handleRemoveMember returns a tool handler that delegates to adminSvc.RemoveMember.
func handleRemoveMember(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		shardID := mcp.ParseUInt64(req, "shard_id", 0)
		replicaID := mcp.ParseUInt64(req, "replica_id", 0)

		if shardID == 0 {
			return mcp.NewToolResultError("shard_id is required"), nil
		}
		if replicaID == 0 {
			return mcp.NewToolResultError("replica_id is required"), nil
		}

		if err := adminSvc.RemoveMember(ctx, shardID, replicaID); err != nil {
			return nil, &ToolError{Tool: "qrdb_remove_member", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolSnapshot defines the qrdb_snapshot tool schema.
func toolSnapshot() mcp.Tool {
	return mcp.NewTool("qrdb_snapshot",
		mcp.WithDescription("Trigger an asynchronous snapshot for the specified shard."),
		mcp.WithNumber("shard_id",
			mcp.Required(),
			mcp.Description("The shard ID to snapshot."),
		),
	)
}

// handleSnapshot returns a tool handler that delegates to adminSvc.RequestSnapshot.
func handleSnapshot(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		if shardID == 0 {
			return mcp.NewToolResultError("shard_id is required"), nil
		}

		if err := adminSvc.RequestSnapshot(ctx, shardID); err != nil {
			return nil, &ToolError{Tool: "qrdb_snapshot", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolDrain defines the qrdb_drain tool schema.
func toolDrain() mcp.Tool {
	return mcp.NewTool("qrdb_drain",
		mcp.WithDescription("Transition the host into drain mode for graceful shutdown."),
	)
}

// handleDrain returns a tool handler that delegates to adminSvc.Drain.
func handleDrain(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := adminSvc.Drain(ctx); err != nil {
			return nil, &ToolError{Tool: "qrdb_drain", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolCapabilities defines the qrdb_capabilities tool schema.
func toolCapabilities() mcp.Tool {
	return mcp.NewTool("qrdb_capabilities",
		mcp.WithDescription("Return the compile-time feature capabilities of the QRDB binary."),
	)
}

// handleCapabilities returns a tool handler that delegates to adminSvc.Capabilities.
func handleCapabilities(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		caps, err := adminSvc.Capabilities(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_capabilities", Err: err}
		}

		data, jsonErr := json.Marshal(caps)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_capabilities", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolStartShard defines the qrdb_start_shard tool schema.
func toolStartShard() mcp.Tool {
	return mcp.NewTool("qrdb_start_shard",
		mcp.WithDescription("Initialize and start a Raft group for the given shard."),
		mcp.WithNumber("shard_id",
			mcp.Required(),
			mcp.Description("The shard ID to start."),
		),
		mcp.WithNumber("replica_id",
			mcp.Required(),
			mcp.Description("This node's replica ID within the shard."),
		),
		mcp.WithString("members_json",
			mcp.Description("JSON-encoded map of replicaID (string) to raft address for initial members."),
		),
		mcp.WithBoolean("join",
			mcp.Description("True when joining an existing cluster."),
		),
	)
}

// handleStartShard returns a tool handler that delegates to adminSvc.StartShard.
func handleStartShard(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		shardID := mcp.ParseUInt64(req, "shard_id", 0)
		replicaID := mcp.ParseUInt64(req, "replica_id", 0)
		membersJSON := mcp.ParseString(req, "members_json", "")
		join := mcp.ParseBoolean(req, "join", false)

		if shardID == 0 {
			return mcp.NewToolResultError("shard_id is required"), nil
		}
		if replicaID == 0 {
			return mcp.NewToolResultError("replica_id is required"), nil
		}

		var members map[uint64]string
		if membersJSON != "" {
			// The JSON keys are stringified uint64 values.
			var raw map[string]string
			if jsonErr := json.Unmarshal([]byte(membersJSON), &raw); jsonErr != nil {
				return mcp.NewToolResultError("invalid members_json: " + jsonErr.Error()), nil
			}
			members = make(map[uint64]string, len(raw))
			for k, v := range raw {
				var id uint64
				if jsonErr := json.Unmarshal([]byte(k), &id); jsonErr != nil {
					return mcp.NewToolResultError("invalid replica ID in members_json: " + k), nil
				}
				members[id] = v
			}
		}

		svcReq := &service.StartShardRequest{
			ShardID:   shardID,
			ReplicaID: replicaID,
			Members:   members,
			Join:      join,
		}

		if err := adminSvc.StartShard(ctx, svcReq); err != nil {
			return nil, &ToolError{Tool: "qrdb_start_shard", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolStopShard defines the qrdb_stop_shard tool schema.
func toolStopShard() mcp.Tool {
	return mcp.NewTool("qrdb_stop_shard",
		mcp.WithDescription("Stop the Raft group for the given shard."),
		mcp.WithNumber("shard_id",
			mcp.Required(),
			mcp.Description("The shard ID to stop."),
		),
	)
}

// handleStopShard returns a tool handler that delegates to adminSvc.StopShard.
func handleStopShard(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		if shardID == 0 {
			return mcp.NewToolResultError("shard_id is required"), nil
		}

		if err := adminSvc.StopShard(ctx, shardID); err != nil {
			return nil, &ToolError{Tool: "qrdb_stop_shard", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolGetShardState defines the qrdb_shard_state tool schema.
func toolGetShardState() mcp.Tool {
	return mcp.NewTool("qrdb_shard_state",
		mcp.WithDescription("Return the Raft state for a shard including term, leader, and config change index."),
		mcp.WithNumber("shard_id",
			mcp.Required(),
			mcp.Description("The shard ID to query."),
		),
	)
}

// handleGetShardState returns a tool handler that delegates to
// adminSvc.GetShardState.
func handleGetShardState(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		if shardID == 0 {
			return mcp.NewToolResultError("shard_id is required"), nil
		}

		state, err := adminSvc.GetShardState(ctx, shardID)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_shard_state", Err: err}
		}

		data, jsonErr := json.Marshal(state)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_shard_state", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolGetTopology defines the qrdb_topology tool schema.
func toolGetTopology() mcp.Tool {
	return mcp.NewTool("qrdb_topology",
		mcp.WithDescription("Return the full cluster topology including all loaded shards and their membership."),
	)
}

// handleGetTopology returns a tool handler that delegates to
// adminSvc.GetTopology.
func handleGetTopology(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		topo, err := adminSvc.GetTopology(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_topology", Err: err}
		}

		data, jsonErr := json.Marshal(topo)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_topology", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}
