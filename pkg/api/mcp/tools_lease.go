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

// leaseTools returns the MCP tool registrations for lease operations.
func leaseTools(leaseSvc service.LeaseServicer) []serverTool {
	return []serverTool{
		{
			Tool:    toolLeaseGrant(),
			Perm:    permission.LeaseGrant,
			Handler: handleLeaseGrant(leaseSvc),
		},
		{
			Tool:    toolLeaseRevoke(),
			Perm:    permission.LeaseRevoke,
			Handler: handleLeaseRevoke(leaseSvc),
		},
		{
			Tool:    toolLeaseKeepAlive(),
			Perm:    permission.LeaseKeepAlive,
			Handler: handleLeaseKeepAlive(leaseSvc),
		},
		{
			Tool:    toolLeaseTTL(),
			Perm:    permission.LeaseTTL,
			Handler: handleLeaseTTL(leaseSvc),
		},
		{
			Tool:    toolLeaseList(),
			Perm:    permission.LeaseList,
			Handler: handleLeaseList(leaseSvc),
		},
	}
}

// toolLeaseGrant defines the qrdb_lease_grant tool schema.
func toolLeaseGrant() mcp.Tool {
	return mcp.NewTool("qrdb_lease_grant",
		mcp.WithDescription("Create a new lease with the specified TTL in seconds. Keys attached to the lease are automatically deleted when it expires."),
		mcp.WithNumber("ttl",
			mcp.Required(),
			mcp.Description("Time-to-live in seconds for the lease."),
		),
	)
}

// handleLeaseGrant returns a tool handler that delegates to
// leaseSvc.Grant.
func handleLeaseGrant(leaseSvc service.LeaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		leaseSvc, rerr := resolveLease(ctx, leaseSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		ttl := mcp.ParseInt64(req, "ttl", 0)
		if ttl <= 0 {
			return mcp.NewToolResultError("ttl must be greater than 0"), nil
		}

		lease, err := leaseSvc.Grant(ctx, ttl)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_lease_grant", Err: err}
		}

		data, jsonErr := json.Marshal(lease)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_lease_grant", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolLeaseRevoke defines the qrdb_lease_revoke tool schema.
func toolLeaseRevoke() mcp.Tool {
	return mcp.NewTool("qrdb_lease_revoke",
		mcp.WithDescription("Immediately expire a lease and delete all keys attached to it."),
		mcp.WithNumber("lease_id",
			mcp.Required(),
			mcp.Description("The lease ID to revoke."),
		),
	)
}

// handleLeaseRevoke returns a tool handler that delegates to
// leaseSvc.Revoke.
func handleLeaseRevoke(leaseSvc service.LeaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		leaseSvc, rerr := resolveLease(ctx, leaseSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		leaseID := mcp.ParseInt64(req, "lease_id", 0)
		if leaseID == 0 {
			return mcp.NewToolResultError("lease_id is required"), nil
		}

		if err := leaseSvc.Revoke(ctx, leaseID); err != nil {
			return nil, &ToolError{Tool: "qrdb_lease_revoke", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolLeaseKeepAlive defines the qrdb_lease_keepalive tool schema.
func toolLeaseKeepAlive() mcp.Tool {
	return mcp.NewTool("qrdb_lease_keepalive",
		mcp.WithDescription("Reset the TTL on an existing lease, preventing it from expiring."),
		mcp.WithNumber("lease_id",
			mcp.Required(),
			mcp.Description("The lease ID to keep alive."),
		),
	)
}

// handleLeaseKeepAlive returns a tool handler that delegates to
// leaseSvc.KeepAlive.
func handleLeaseKeepAlive(leaseSvc service.LeaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		leaseSvc, rerr := resolveLease(ctx, leaseSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		leaseID := mcp.ParseInt64(req, "lease_id", 0)
		if leaseID == 0 {
			return mcp.NewToolResultError("lease_id is required"), nil
		}

		lease, err := leaseSvc.KeepAlive(ctx, leaseID)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_lease_keepalive", Err: err}
		}

		data, jsonErr := json.Marshal(lease)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_lease_keepalive", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolLeaseTTL defines the qrdb_lease_ttl tool schema.
func toolLeaseTTL() mcp.Tool {
	return mcp.NewTool("qrdb_lease_ttl",
		mcp.WithDescription("Return the current state of a lease including its remaining TTL and attached keys."),
		mcp.WithNumber("lease_id",
			mcp.Required(),
			mcp.Description("The lease ID to query."),
		),
	)
}

// handleLeaseTTL returns a tool handler that delegates to
// leaseSvc.TimeToLive.
func handleLeaseTTL(leaseSvc service.LeaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		leaseSvc, rerr := resolveLease(ctx, leaseSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		leaseID := mcp.ParseInt64(req, "lease_id", 0)
		if leaseID == 0 {
			return mcp.NewToolResultError("lease_id is required"), nil
		}

		lease, err := leaseSvc.TimeToLive(ctx, leaseID)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_lease_ttl", Err: err}
		}

		data, jsonErr := json.Marshal(lease)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_lease_ttl", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolLeaseList defines the qrdb_lease_list tool schema.
func toolLeaseList() mcp.Tool {
	return mcp.NewTool("qrdb_lease_list",
		mcp.WithDescription("Return all active leases."),
	)
}

// handleLeaseList returns a tool handler that delegates to
// leaseSvc.Leases.
func handleLeaseList(leaseSvc service.LeaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		leaseSvc, rerr := resolveLease(ctx, leaseSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		leases, err := leaseSvc.Leases(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_lease_list", Err: err}
		}

		data, jsonErr := json.Marshal(leases)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_lease_list", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}
