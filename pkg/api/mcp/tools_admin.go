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
	"time"

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
			Tool:    toolPromoteMember(),
			Perm:    permission.AdminAddMember,
			Handler: handlePromoteMember(adminSvc),
		},
		{
			Tool:    toolTransferLeadership(),
			Perm:    permission.AdminRemoveMember,
			Handler: handleTransferLeadership(adminSvc),
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
		{
			Tool:    toolGetReplicaStates(),
			Perm:    permission.AdminReplicaState,
			Handler: handleGetReplicaStates(adminSvc),
		},
		{
			Tool:    toolNodeCompaction(),
			Perm:    permission.AdminNodeCompaction,
			Handler: handleNodeCompaction(adminSvc),
		},
		{
			Tool:    toolUpdateNodeCompaction(),
			Perm:    permission.AdminNodeCompaction,
			Handler: handleUpdateNodeCompaction(adminSvc),
		},
		{
			Tool:    toolStateHashes(),
			Perm:    permission.AdminStateHash,
			Handler: handleStateHashes(adminSvc),
		},
		{
			Tool:    toolMintJoinToken(),
			Perm:    permission.ClusterJoinToken,
			Handler: handleMintJoinToken(adminSvc),
		},
		{
			Tool:    toolAddObserverMember(),
			Perm:    permission.AdminAddMember,
			Handler: handleAddObserverMember(adminSvc),
		},
		{
			Tool:    toolListPendingJoinRequests(),
			Perm:    permission.ClusterJoinToken,
			Handler: handleListPendingJoinRequests(adminSvc),
		},
		{
			Tool:    toolApproveJoinRequest(),
			Perm:    permission.ClusterJoinToken,
			Handler: handleApproveJoinRequest(adminSvc),
		},
		{
			Tool:    toolRejectJoinRequest(),
			Perm:    permission.ClusterJoinToken,
			Handler: handleRejectJoinRequest(adminSvc),
		},
		{
			Tool:    toolRetireJoinIdentity(),
			Perm:    permission.ClusterJoinIdentityRetire,
			Handler: handleRetireJoinIdentity(adminSvc),
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

// toolPromoteMember defines the qrdb_promote_member tool schema.
func toolPromoteMember() mcp.Tool {
	return mcp.NewTool("qrdb_promote_member",
		mcp.WithDescription("Promote a committed observer of a shard to a voter."),
		mcp.WithNumber("shard_id",
			mcp.Required(),
			mcp.Description("The shard ID the observer belongs to."),
		),
		mcp.WithNumber("replica_id",
			mcp.Required(),
			mcp.Description("The observer's replica ID to promote."),
		),
	)
}

// handlePromoteMember returns a tool handler that delegates to adminSvc's
// MemberPromoter capability (see service.MemberPromoter).
func handlePromoteMember(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		promoter, ok := adminSvc.(service.MemberPromoter)
		if !ok {
			return nil, &ToolError{Tool: "qrdb_promote_member", Err: &service.MemberPromotionUnsupportedError{}}
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)
		replicaID := mcp.ParseUInt64(req, "replica_id", 0)

		if shardID == 0 {
			return mcp.NewToolResultError("shard_id is required"), nil
		}
		if replicaID == 0 {
			return mcp.NewToolResultError("replica_id is required"), nil
		}

		if err := promoter.PromoteMember(ctx, shardID, replicaID); err != nil {
			return nil, &ToolError{Tool: "qrdb_promote_member", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolTransferLeadership defines the qrdb_transfer_leadership tool schema.
func toolTransferLeadership() mcp.Tool {
	return mcp.NewTool("qrdb_transfer_leadership",
		mcp.WithDescription("Hand a shard's leadership to a named voter and return once it leads."),
		mcp.WithNumber("shard_id",
			mcp.Required(),
			mcp.Description("The shard ID whose leadership moves."),
		),
		mcp.WithNumber("replica_id",
			mcp.Required(),
			mcp.Description("The voter's replica ID that is to lead the shard."),
		),
	)
}

// handleTransferLeadership returns a tool handler that delegates to
// adminSvc's LeaderTransferrer capability (see service.LeaderTransferrer).
func handleTransferLeadership(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		transferrer, ok := adminSvc.(service.LeaderTransferrer)
		if !ok {
			return nil, &ToolError{Tool: "qrdb_transfer_leadership", Err: &service.LeaderTransferUnsupportedError{}}
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)
		replicaID := mcp.ParseUInt64(req, "replica_id", 0)

		if shardID == 0 {
			return mcp.NewToolResultError("shard_id is required"), nil
		}
		if replicaID == 0 {
			return mcp.NewToolResultError("replica_id is required"), nil
		}

		if err := transferrer.TransferLeadership(ctx, shardID, replicaID); err != nil {
			return nil, &ToolError{Tool: "qrdb_transfer_leadership", Err: err}
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

// toolGetReplicaStates defines the qrdb_replica_states tool schema.
func toolGetReplicaStates() mcp.Tool {
	return mcp.NewTool("qrdb_replica_states",
		mcp.WithDescription("Return each replica this node holds with its applied index and state-machine hash. Replicas of a shard at the same applied index hold the same state exactly when their hashes match. Answers for this node only."),
		mcp.WithNumber("shard_id",
			mcp.Description("The shard to report; omit or 0 for every shard."),
		),
	)
}

// handleGetReplicaStates returns a tool handler that delegates to the admin
// service's ReplicaStateReader.
func handleGetReplicaStates(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		reader, ok := adminSvc.(service.ReplicaStateReader)
		if !ok {
			return nil, &ToolError{Tool: "qrdb_replica_states", Err: &service.ReplicaStateReadUnsupportedError{}}
		}
		states, err := reader.ReplicaStates(ctx, mcp.ParseUInt64(req, "shard_id", 0))
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_replica_states", Err: err}
		}

		data, jsonErr := json.Marshal(states)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_replica_states", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolMintJoinToken defines the qrdb_mint_join_token tool schema.
func toolMintJoinToken() mcp.Tool {
	return mcp.NewTool("qrdb_mint_join_token",
		mcp.WithDescription("Mint a single-use, identity-bound member-side CSR join token for a joining node. "+
			"Requires permission cluster:join_token (ScopeCross, platform-only)."),
		mcp.WithString("node_id",
			mcp.Required(),
			mcp.Description("The joining node's identity (bound into spiffe://<trust domain>/node/<node_id>)."),
		),
		mcp.WithNumber("ttl_seconds",
			mcp.Description("Token time-to-live in seconds. Zero or omitted uses the server's default TTL."),
		),
	)
}

// parseTTLSecondsArg reads key from req's raw arguments as an integer
// number of seconds, refusing with *ToolArgumentError rather than
// silently coercing when the value is not a whole number (e.g. 3600.5) or
// is too large for mcp.ParseInt's underlying cast (github.com/spf13/cast)
// to convert safely: Go's float64->int conversion for an out-of-range or
// non-finite float is implementation-specific, not a panic, so a huge or
// non-finite JSON number would otherwise silently become some unrelated
// in-range int rather than the refusal it should be. An absent key
// returns 0 (the documented "use the server's default TTL" value); every
// OTHER type (a string, a bool, an array) is refused the same way.
func parseTTLSecondsArg(req mcp.CallToolRequest, key string) (int64, error) {
	raw, ok := req.GetArguments()[key]
	if !ok || raw == nil {
		return 0, nil
	}
	// JSON-decoded arguments (every real wire call) arrive as float64; a
	// Go-literal int (every in-process test that builds a
	// mcp.CallToolRequest by hand rather than round-tripping JSON) is
	// accepted too, rather than making this helper fragile to a
	// distinction that is an artifact of how the caller built the
	// request, not something the caller asked for.
	var f float64
	switch v := raw.(type) {
	case float64:
		f = v
	case int:
		f = float64(v)
	case int64:
		f = float64(v)
	default:
		return 0, &ToolArgumentError{Argument: key, Reason: "must be a number"}
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, &ToolArgumentError{Argument: key, Reason: "must be finite"}
	}
	if f != math.Trunc(f) {
		return 0, &ToolArgumentError{Argument: key, Reason: "must be a whole number of seconds"}
	}
	// float64 represents every integer up to 2^53 exactly; bounding to
	// service.MaxJoinTokenTTLSeconds (far smaller) keeps the eventual
	// int64 conversion exact AND already within the range
	// service.ParseJoinTokenTTLSeconds itself enforces, so this refusal
	// reports as a tool-argument problem rather than that function's own
	// overflow error for the same value.
	if f < 0 || f > float64(service.MaxJoinTokenTTLSeconds) {
		return 0, &ToolArgumentError{Argument: key, Reason: "out of range"}
	}
	return int64(f), nil
}

// handleMintJoinToken returns a tool handler that delegates to
// adminSvc.MintJoinToken when adminSvc implements
// service.ClusterJoinTokenMinter, and reports
// *service.ClusterJoinTokenNotConfiguredError otherwise (member-side CSR
// join never configured on this node) -- the same typed error the REST,
// gRPC, QUIC and embedded transports report.
func handleMintJoinToken(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		minter, ok := adminSvc.(service.ClusterJoinTokenMinter)
		if !ok {
			return nil, &ToolError{Tool: "qrdb_mint_join_token", Err: &service.ClusterJoinTokenNotConfiguredError{}}
		}

		nodeID := mcp.ParseString(req, "node_id", "")
		ttlSeconds, err := parseTTLSecondsArg(req, "ttl_seconds")
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_mint_join_token", Err: err}
		}

		ttl, err := service.ParseJoinTokenTTLSeconds(ttlSeconds)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_mint_join_token", Err: err}
		}

		token, expiresAt, err := minter.MintJoinToken(ctx, nodeID, ttl)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_mint_join_token", Err: err}
		}

		data, jsonErr := json.Marshal(struct {
			Token     string `json:"token"`
			ExpiresAt string `json:"expires_at"`
		}{Token: token, ExpiresAt: expiresAt.UTC().Format(time.RFC3339)})
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_mint_join_token", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolAddObserverMember defines the qrdb_add_observer_member tool schema.
func toolAddObserverMember() mcp.Tool {
	return mcp.NewTool("qrdb_add_observer_member",
		mcp.WithDescription("Add a new replica to the specified shard as a non-voting observer rather than a voter. "+
			"Promote it to a voter later with qrdb_promote_member."),
		mcp.WithNumber("shard_id",
			mcp.Required(),
			mcp.Description("The shard ID to add the replica to."),
		),
		mcp.WithNumber("replica_id",
			mcp.Required(),
			mcp.Description("The replica ID of the new observer."),
		),
		mcp.WithString("raft_addr",
			mcp.Required(),
			mcp.Description("The Raft network address of the new observer."),
		),
		mcp.WithString("identity",
			mcp.Description("The member's Raft identity (spki:<sha256-hex>, from `qrdbd node-identity --cert`). "+
				"Omit it to use the identity the cluster already committed for the node."),
		),
	)
}

// handleAddObserverMember returns a tool handler that delegates to
// adminSvc's ObserverMemberAdder capability.
func handleAddObserverMember(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		adder, ok := adminSvc.(service.ObserverMemberAdder)
		if !ok {
			return nil, &ToolError{Tool: "qrdb_add_observer_member", Err: &service.ClusterJoinRequestReviewUnsupportedError{}}
		}

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

		if err := adder.AddObserverMember(ctx, shardID, replicaID, raftAddr, identity); err != nil {
			return nil, &ToolError{Tool: "qrdb_add_observer_member", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolListPendingJoinRequests defines the qrdb_list_pending_join_requests
// tool schema.
func toolListPendingJoinRequests() mcp.Tool {
	return mcp.NewTool("qrdb_list_pending_join_requests",
		mcp.WithDescription("List tokenless member-side CSR join requests awaiting an operator's approval or rejection. "+
			"Requires permission cluster:join_token (ScopeCross, platform-only)."),
	)
}

// pendingJoinRequestJSON is the MCP tool result shape for one queued CSR.
type pendingJoinRequestJSON struct {
	ID          string    `json:"id"`
	NodeURI     string    `json:"node_uri,omitempty"`
	ReplicaID   uint64    `json:"replica_id,omitempty"`
	Features    []string  `json:"features,omitempty"`
	SubmittedAt time.Time `json:"submitted_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// handleListPendingJoinRequests returns a tool handler that delegates to
// adminSvc's JoinRequestReviewer capability.
func handleListPendingJoinRequests(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		reviewer, ok := adminSvc.(service.JoinRequestReviewer)
		if !ok {
			return nil, &ToolError{Tool: "qrdb_list_pending_join_requests", Err: &service.ClusterJoinTokenNotConfiguredError{}}
		}

		pending, err := reviewer.ListPendingJoinRequests(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_list_pending_join_requests", Err: err}
		}

		out := make([]pendingJoinRequestJSON, 0, len(pending))
		for _, p := range pending {
			out = append(out, pendingJoinRequestJSON{
				ID:          p.ID,
				NodeURI:     p.NodeURI,
				ReplicaID:   p.ReplicaID,
				Features:    p.Features,
				SubmittedAt: p.SubmittedAt,
				ExpiresAt:   p.ExpiresAt,
			})
		}

		data, jsonErr := json.Marshal(out)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_list_pending_join_requests", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolApproveJoinRequest defines the qrdb_approve_join_request tool schema.
func toolApproveJoinRequest() mcp.Tool {
	return mcp.NewTool("qrdb_approve_join_request",
		mcp.WithDescription("Bind a queued, tokenless CSR join request to an operator-chosen node identity. "+
			"The leaf is signed the next time the joiner's own retry reaches the join listener, not by this call."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The pending join request ID (see qrdb_list_pending_join_requests)."),
		),
		mcp.WithString("node_id",
			mcp.Required(),
			mcp.Description("The node identity to bind the eventual leaf to (spiffe://<trust domain>/node/<node_id>)."),
		),
	)
}

// handleApproveJoinRequest returns a tool handler that delegates to
// adminSvc's JoinRequestReviewer capability.
func handleApproveJoinRequest(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		reviewer, ok := adminSvc.(service.JoinRequestReviewer)
		if !ok {
			return nil, &ToolError{Tool: "qrdb_approve_join_request", Err: &service.ClusterJoinTokenNotConfiguredError{}}
		}

		id := mcp.ParseString(req, "id", "")
		nodeID := mcp.ParseString(req, "node_id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		if nodeID == "" {
			return mcp.NewToolResultError("node_id is required"), nil
		}

		if err := reviewer.ApproveJoinRequest(ctx, id, nodeID); err != nil {
			return nil, &ToolError{Tool: "qrdb_approve_join_request", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolRejectJoinRequest defines the qrdb_reject_join_request tool schema.
func toolRejectJoinRequest() mcp.Tool {
	return mcp.NewTool("qrdb_reject_join_request",
		mcp.WithDescription("Record a terminal refusal of a queued, tokenless CSR join request."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The pending join request ID (see qrdb_list_pending_join_requests)."),
		),
		mcp.WithString("reason",
			mcp.Description("An operator-supplied reason, recorded for audit."),
		),
	)
}

// toolRetireJoinIdentity defines the qrdb_retire_join_identity tool schema.
func toolRetireJoinIdentity() mcp.Tool {
	return mcp.NewTool("qrdb_retire_join_identity",
		mcp.WithDescription("Permanently refuse any future admission for a node identity -- a fresh "+
			"mint, or a renewal of one whose admission has lapsed. Requires permission "+
			"cluster:join_identity_retire (ScopeCross, platform-only)."),
		mcp.WithString("node_id",
			mcp.Required(),
			mcp.Description("The node identity to retire (bound into spiffe://<trust domain>/node/<node_id>)."),
		),
		mcp.WithString("reason",
			mcp.Description("Optional free-text operator note, carried for audit only."),
		),
	)
}

// handleRejectJoinRequest returns a tool handler that delegates to
// adminSvc's JoinRequestReviewer capability.
func handleRejectJoinRequest(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		reviewer, ok := adminSvc.(service.JoinRequestReviewer)
		if !ok {
			return nil, &ToolError{Tool: "qrdb_reject_join_request", Err: &service.ClusterJoinTokenNotConfiguredError{}}
		}

		id := mcp.ParseString(req, "id", "")
		reason := mcp.ParseString(req, "reason", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}

		if err := reviewer.RejectJoinRequest(ctx, id, reason); err != nil {
			return nil, &ToolError{Tool: "qrdb_reject_join_request", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// handleRetireJoinIdentity returns a tool handler that delegates to
// adminSvc's JoinIdentityRetirer capability (see service.
// JoinIdentityRetirer).
func handleRetireJoinIdentity(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		retirer, ok := adminSvc.(service.JoinIdentityRetirer)
		if !ok {
			return nil, &ToolError{Tool: "qrdb_retire_join_identity", Err: &service.ClusterJoinTokenNotConfiguredError{}}
		}

		nodeID := mcp.ParseString(req, "node_id", "")
		reason := mcp.ParseString(req, "reason", "")

		if err := retirer.RetireJoinIdentity(ctx, nodeID, reason); err != nil {
			return nil, &ToolError{Tool: "qrdb_retire_join_identity", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}
