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
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// volumePoolTools returns the MCP tool registrations for volume pool
// management (docs/volume-pools/api.md): list, inspect, create, update,
// delete a pool, and list a pool's assigned tenants. Every permission here
// is ScopeCross (system tenant only).
func volumePoolTools(svc volume.Service) []serverTool {
	return []serverTool{
		{Tool: toolVolumePoolList(), Perm: permission.VolumePoolList, Handler: handleVolumePoolList(svc)},
		{Tool: toolVolumePoolGet(), Perm: permission.VolumePoolRead, Handler: handleVolumePoolGet(svc)},
		{Tool: toolVolumePoolCreate(), Perm: permission.VolumePoolCreate, Handler: handleVolumePoolCreate(svc)},
		{Tool: toolVolumePoolUpdate(), Perm: permission.VolumePoolUpdate, Handler: handleVolumePoolUpdate(svc)},
		{Tool: toolVolumePoolDelete(), Perm: permission.VolumePoolDelete, Handler: handleVolumePoolDelete(svc)},
		{Tool: toolVolumePoolListTenants(), Perm: permission.VolumePoolRead, Handler: handleVolumePoolListTenants(svc)},
	}
}

// toolVolumePoolList defines the qrdb_volume_pool_list tool schema.
func toolVolumePoolList() mcp.Tool {
	return mcp.NewTool("qrdb_volume_pool_list",
		mcp.WithDescription("List every pool in the catalog with its capacity, reservation, usage, tenant count and a per-node health summary."),
	)
}

func handleVolumePoolList(svc volume.Service) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		pools, err := svc.ListPools(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_volume_pool_list", Err: err}
		}
		data, jsonErr := json.Marshal(pools)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_volume_pool_list", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolVolumePoolGet defines the qrdb_volume_pool_get tool schema.
func toolVolumePoolGet() mcp.Tool {
	return mcp.NewTool("qrdb_volume_pool_get",
		mcp.WithDescription("Inspect one pool, with one row per node: whether the node maps the pool, the provider state, size, free space, how long ago the node reported, and the provider's detail text."),
		mcp.WithString("pool", mcp.Required(), mcp.Description("The pool name.")),
	)
}

func handleVolumePoolGet(svc volume.Service) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "pool", "")
		if name == "" {
			return mcp.NewToolResultError("pool is required"), nil
		}
		detail, err := svc.GetPool(ctx, name)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_volume_pool_get", Err: err}
		}
		data, jsonErr := json.Marshal(detail)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_volume_pool_get", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolVolumePoolCreate defines the qrdb_volume_pool_create tool schema.
func toolVolumePoolCreate() mcp.Tool {
	return mcp.NewTool("qrdb_volume_pool_create",
		mcp.WithDescription("Add a pool to the catalog. The pool is not served until each node maps it in its own configuration and restarts or reloads."),
		mcp.WithString("name", mcp.Required(), mcp.Description("The pool name.")),
		mcp.WithString("provider", mcp.Description("The volume provider every node uses for this pool; must be registered on the node handling the request. Default \"dir\".")),
		mcp.WithString("description", mcp.Description("Free text.")),
		mcp.WithNumber("capacity_bytes", mcp.Description("Allocatable capacity in bytes; 0 or absent means the smallest size a healthy node reports.")),
		mcp.WithNumber("overcommit", mcp.Description("Reservations may reach capacity x overcommit; must be >= 1.0. Default 1.0.")),
	)
}

func handleVolumePoolCreate(svc volume.Service) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		pool, err := svc.CreatePool(ctx, volume.CreatePoolRequest{
			Name:          name,
			Provider:      mcp.ParseString(req, "provider", ""),
			Description:   mcp.ParseString(req, "description", ""),
			CapacityBytes: int64(mcp.ParseInt(req, "capacity_bytes", 0)),
			Overcommit:    mcp.ParseFloat64(req, "overcommit", 0),
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_volume_pool_create", Err: err}
		}
		data, jsonErr := json.Marshal(pool)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_volume_pool_create", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolVolumePoolUpdate defines the qrdb_volume_pool_update tool schema.
func toolVolumePoolUpdate() mcp.Tool {
	return mcp.NewTool("qrdb_volume_pool_update",
		mcp.WithDescription("Change a pool's description, capacity or overcommit. The provider is immutable. Absent arguments leave the corresponding property unchanged."),
		mcp.WithString("pool", mcp.Required(), mcp.Description("The pool name.")),
		mcp.WithString("description", mcp.Description("Free text; absent leaves it unchanged.")),
		mcp.WithNumber("capacity_bytes", mcp.Description("Allocatable capacity in bytes; absent leaves it unchanged, 0 returns to the derived value.")),
		mcp.WithNumber("overcommit", mcp.Description("Must be >= 1.0; absent leaves it unchanged.")),
	)
}

func handleVolumePoolUpdate(svc volume.Service) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "pool", "")
		if name == "" {
			return mcp.NewToolResultError("pool is required"), nil
		}
		args := req.GetArguments()
		var update volume.UpdatePoolRequest
		if _, present := args["description"]; present {
			v := mcp.ParseString(req, "description", "")
			update.Description = &v
		}
		if _, present := args["capacity_bytes"]; present {
			v := int64(mcp.ParseInt(req, "capacity_bytes", 0))
			update.CapacityBytes = &v
		}
		if _, present := args["overcommit"]; present {
			v := mcp.ParseFloat64(req, "overcommit", 0)
			update.Overcommit = &v
		}
		pool, err := svc.UpdatePool(ctx, name, update)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_volume_pool_update", Err: err}
		}
		data, jsonErr := json.Marshal(pool)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_volume_pool_update", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolVolumePoolDelete defines the qrdb_volume_pool_delete tool schema.
func toolVolumePoolDelete() mcp.Tool {
	return mcp.NewTool("qrdb_volume_pool_delete",
		mcp.WithDescription("Remove a pool from the catalog. Refused while any tenant is assigned to it. Changes nothing on disk."),
		mcp.WithString("pool", mcp.Required(), mcp.Description("The pool name.")),
	)
}

func handleVolumePoolDelete(svc volume.Service) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "pool", "")
		if name == "" {
			return mcp.NewToolResultError("pool is required"), nil
		}
		if err := svc.DeletePool(ctx, name); err != nil {
			return nil, &ToolError{Tool: "qrdb_volume_pool_delete", Err: err}
		}
		return mcp.NewToolResultText("{}"), nil
	}
}

// toolVolumePoolListTenants defines the qrdb_volume_pool_list_tenants tool
// schema.
func toolVolumePoolListTenants() mcp.Tool {
	return mcp.NewTool("qrdb_volume_pool_list_tenants",
		mcp.WithDescription("List the tenants assigned to a pool, with each one's reservation and measured use."),
		mcp.WithString("pool", mcp.Required(), mcp.Description("The pool name.")),
		mcp.WithNumber("page_size", mcp.Description("Rows per page. 0 or absent uses the server default.")),
		mcp.WithString("page_token", mcp.Description("Resume after a previous page's next_page_token.")),
	)
}

// volumePoolTenantRow and listVolumePoolTenantsResult give the tool's JSON
// response the same "tenants"/"next_page_token" shape every other
// transport returns, rather than volume.AssignmentPage's own
// "assignments" field name -- see sdk/go/transport's VolumePoolClient,
// which every transport's wire decoder must agree with.
type volumePoolTenantRow struct {
	TenantID      string `json:"tenantId"`
	ReservedBytes int64  `json:"reservedBytes"`
}

type listVolumePoolTenantsResult struct {
	Tenants       []volumePoolTenantRow `json:"tenants"`
	NextPageToken string                `json:"next_page_token,omitempty"`
}

func handleVolumePoolListTenants(svc volume.Service) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "pool", "")
		if name == "" {
			return mcp.NewToolResultError("pool is required"), nil
		}
		page, err := svc.ListAssignments(ctx, name, mcp.ParseString(req, "page_token", ""), mcp.ParseInt(req, "page_size", 0))
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_volume_pool_list_tenants", Err: err}
		}
		result := listVolumePoolTenantsResult{
			Tenants:       make([]volumePoolTenantRow, len(page.Assignments)),
			NextPageToken: page.NextPageToken,
		}
		for i, a := range page.Assignments {
			result.Tenants[i] = volumePoolTenantRow{TenantID: a.TenantID, ReservedBytes: a.ReservedBytes}
		}
		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_volume_pool_list_tenants", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}
