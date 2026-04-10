// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// userTools returns the MCP tool registrations for user management operations.
func userTools(svc user.UserServicer) []mcpserver.ServerTool {
	return []mcpserver.ServerTool{
		{
			Tool:    toolUserCreate(),
			Handler: handleUserCreate(svc),
		},
		{
			Tool:    toolUserGet(),
			Handler: handleUserGet(svc),
		},
		{
			Tool:    toolUserUpdate(),
			Handler: handleUserUpdate(svc),
		},
		{
			Tool:    toolUserDelete(),
			Handler: handleUserDelete(svc),
		},
		{
			Tool:    toolUserList(),
			Handler: handleUserList(svc),
		},
		{
			Tool:    toolUserEnable(),
			Handler: handleUserEnable(svc),
		},
		{
			Tool:    toolUserDisable(),
			Handler: handleUserDisable(svc),
		},
	}
}

// toolUserCreate defines the qrdb_user_create tool schema.
func toolUserCreate() mcp.Tool {
	return mcp.NewTool("qrdb_user_create",
		mcp.WithDescription("Create a new passwordless user. Authentication is exclusively via mTLS X.509 certificates."),
		mcp.WithString("username",
			mcp.Required(),
			mcp.Description("The username for the new user. Must be unique within the tenant."),
		),
		mcp.WithString("tenant_id",
			mcp.Description("The tenant ID. Defaults to the system default tenant when empty."),
		),
		mcp.WithString("role_ids",
			mcp.Description("Comma-separated list of role IDs to assign to the user."),
		),
	)
}

// handleUserCreate returns a tool handler that delegates to svc.CreateUser.
func handleUserCreate(svc user.UserServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}

		tenantID := mcp.ParseString(req, "tenant_id", "")
		roleIDs := parseCSV(mcp.ParseString(req, "role_ids", ""))

		u, err := svc.CreateUser(ctx, &user.CreateUserRequest{
			Username: username,
			TenantID: tenantID,
			RoleIDs:  roleIDs,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_create", Err: err}
		}

		data, jsonErr := json.Marshal(u)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_user_create", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolUserGet defines the qrdb_user_get tool schema.
func toolUserGet() mcp.Tool {
	return mcp.NewTool("qrdb_user_get",
		mcp.WithDescription("Retrieve a user by its globally unique ID."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The user UUID."),
		),
	)
}

// handleUserGet returns a tool handler that delegates to svc.GetUser.
func handleUserGet(svc user.UserServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}

		u, err := svc.GetUser(ctx, id)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_get", Err: err}
		}

		data, jsonErr := json.Marshal(u)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_user_get", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolUserUpdate defines the qrdb_user_update tool schema.
func toolUserUpdate() mcp.Tool {
	return mcp.NewTool("qrdb_user_update",
		mcp.WithDescription("Apply partial updates to an existing user."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The user UUID to update."),
		),
		mcp.WithString("username",
			mcp.Description("The new username. Left unchanged when empty."),
		),
		mcp.WithString("role_ids",
			mcp.Description("Comma-separated list of role IDs to replace the current set. Left unchanged when empty."),
		),
	)
}

// handleUserUpdate returns a tool handler that delegates to svc.UpdateUser.
func handleUserUpdate(svc user.UserServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}

		updateReq := &user.UpdateUserRequest{
			ID:       id,
			Username: mcp.ParseString(req, "username", ""),
		}

		roleIDsStr := mcp.ParseString(req, "role_ids", "")
		if roleIDsStr != "" {
			updateReq.RoleIDs = parseCSV(roleIDsStr)
		}

		u, err := svc.UpdateUser(ctx, updateReq)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_update", Err: err}
		}

		data, jsonErr := json.Marshal(u)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_user_update", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolUserDelete defines the qrdb_user_delete tool schema.
func toolUserDelete() mcp.Tool {
	return mcp.NewTool("qrdb_user_delete",
		mcp.WithDescription("Delete a non-system user by ID."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The user UUID to delete."),
		),
	)
}

// handleUserDelete returns a tool handler that delegates to svc.DeleteUser.
func handleUserDelete(svc user.UserServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}

		if err := svc.DeleteUser(ctx, id); err != nil {
			return nil, &ToolError{Tool: "qrdb_user_delete", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolUserList defines the qrdb_user_list tool schema.
func toolUserList() mcp.Tool {
	return mcp.NewTool("qrdb_user_list",
		mcp.WithDescription("Return a paginated list of users for a tenant."),
		mcp.WithString("tenant_id",
			mcp.Description("Filter users by tenant ID. Defaults to the system default tenant when empty."),
		),
		mcp.WithNumber("page",
			mcp.Description("1-based page number. Defaults to 1."),
		),
		mcp.WithNumber("page_size",
			mcp.Description("Maximum number of users per page. Defaults to the server default."),
		),
	)
}

// handleUserList returns a tool handler that delegates to svc.ListUsers.
func handleUserList(svc user.UserServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		resp, err := svc.ListUsers(ctx, &user.ListUsersRequest{
			TenantID: mcp.ParseString(req, "tenant_id", ""),
			Page:     mcp.ParseInt(req, "page", 0),
			PageSize: mcp.ParseInt(req, "page_size", 0),
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_list", Err: err}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_user_list", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolUserEnable defines the qrdb_user_enable tool schema.
func toolUserEnable() mcp.Tool {
	return mcp.NewTool("qrdb_user_enable",
		mcp.WithDescription("Activate a user account, allowing authentication."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The user UUID to enable."),
		),
	)
}

// handleUserEnable returns a tool handler that delegates to svc.EnableUser.
func handleUserEnable(svc user.UserServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}

		if err := svc.EnableUser(ctx, id); err != nil {
			return nil, &ToolError{Tool: "qrdb_user_enable", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolUserDisable defines the qrdb_user_disable tool schema.
func toolUserDisable() mcp.Tool {
	return mcp.NewTool("qrdb_user_disable",
		mcp.WithDescription("Deactivate a user account, preventing authentication."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The user UUID to disable."),
		),
	)
}

// handleUserDisable returns a tool handler that delegates to svc.DisableUser.
func handleUserDisable(svc user.UserServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}

		if err := svc.DisableUser(ctx, id); err != nil {
			return nil, &ToolError{Tool: "qrdb_user_disable", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// parseCSV splits a comma-separated string into a trimmed slice of strings.
// Returns nil when the input is empty.
func parseCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
