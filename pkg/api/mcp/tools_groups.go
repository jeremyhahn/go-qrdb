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

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/group"
	"github.com/jeremyhahn/go-qrdb/pkg/group/groupauthz"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// groupTools returns the IAM group tools. Every handler goes through
// groupauthz, the one authorization path the other transports share.
func groupTools(groupSvc group.GroupServicer, userSvc user.UserServicer, roleSvc role.RoleServicer, authMW *authmw.AuthMiddleware) []serverTool {
	svc := groupauthz.New(groupSvc, userSvc, roleSvc, authMW)
	return []serverTool{
		{Tool: toolGroupCreate(), Perm: permission.GroupsCreate, Handler: handleGroupCreate(svc)},
		{Tool: toolGroupGet(), Perm: permission.GroupsRead, Handler: handleGroupGet(svc)},
		{Tool: toolGroupGetByName(), Perm: permission.GroupsRead, Handler: handleGroupGetByName(svc)},
		{Tool: toolGroupUpdate(), Perm: permission.GroupsUpdate, Handler: handleGroupUpdate(svc)},
		{Tool: toolGroupDelete(), Perm: permission.GroupsDelete, Handler: handleGroupDelete(svc)},
		{Tool: toolGroupList(), Perm: permission.GroupsList, Handler: handleGroupList(svc)},
		{Tool: toolGroupAddMember(), Perm: permission.GroupsAddMember, Handler: handleGroupMember(svc, "qrdb_group_add_member", (*groupauthz.Service).AddMember)},
		{Tool: toolGroupRemoveMember(), Perm: permission.GroupsRemoveMember, Handler: handleGroupMember(svc, "qrdb_group_remove_member", (*groupauthz.Service).RemoveMember)},
		{Tool: toolGroupListMembers(), Perm: permission.GroupsRead, Handler: handleGroupListMembers(svc)},
	}
}

func toolGroupCreate() mcp.Tool {
	return mcp.NewTool("qrdb_group_create",
		mcp.WithDescription("Create an IAM group; its members hold the group's roles."),
		mcp.WithString("name", mcp.Required(), mcp.Description("The group name. Must be unique within the tenant.")),
		mcp.WithString("tenant_id", mcp.Description("The tenant ID. Defaults to the caller's tenant when empty.")),
		mcp.WithString("description", mcp.Description("Optional description.")),
		mcp.WithString("role_ids", mcp.Description("Comma-separated role IDs of the group's tenant.")),
	)
}

func toolGroupGet() mcp.Tool {
	return mcp.NewTool("qrdb_group_get",
		mcp.WithDescription("Retrieve an IAM group by ID."),
		mcp.WithString("id", mcp.Required(), mcp.Description("The group ID.")),
	)
}

func toolGroupGetByName() mcp.Tool {
	return mcp.NewTool("qrdb_group_get_by_name",
		mcp.WithDescription("Retrieve an IAM group by its name within a tenant."),
		mcp.WithString("name", mcp.Required(), mcp.Description("The group name.")),
		mcp.WithString("tenant_id", mcp.Description("The tenant ID. Defaults to the caller's tenant when empty.")),
	)
}

func toolGroupUpdate() mcp.Tool {
	return mcp.NewTool("qrdb_group_update",
		mcp.WithDescription("Update an IAM group's name, description or role set."),
		mcp.WithString("id", mcp.Required(), mcp.Description("The group ID.")),
		mcp.WithString("name", mcp.Description("New name; empty keeps the current name.")),
		mcp.WithString("description", mcp.Description("New description; omitted keeps the current one.")),
		mcp.WithString("role_ids", mcp.Description("Comma-separated role IDs replacing the role set; omitted keeps it, empty clears it.")),
	)
}

func toolGroupDelete() mcp.Tool {
	return mcp.NewTool("qrdb_group_delete",
		mcp.WithDescription("Delete an IAM group; every member loses its roles."),
		mcp.WithString("id", mcp.Required(), mcp.Description("The group ID.")),
	)
}

func toolGroupList() mcp.Tool {
	return mcp.NewTool("qrdb_group_list",
		mcp.WithDescription("List the IAM groups of a tenant."),
		mcp.WithString("tenant_id", mcp.Description("The tenant ID. Defaults to the caller's tenant when empty.")),
		mcp.WithNumber("page", mcp.Description("Page number, starting at 1.")),
		mcp.WithNumber("page_size", mcp.Description("Groups per page.")),
	)
}

func toolGroupAddMember() mcp.Tool {
	return mcp.NewTool("qrdb_group_add_member",
		mcp.WithDescription("Add a user to an IAM group of the user's tenant."),
		mcp.WithString("user_id", mcp.Required(), mcp.Description("The user ID (uint64).")),
		mcp.WithString("group_id", mcp.Required(), mcp.Description("The group ID.")),
	)
}

func toolGroupRemoveMember() mcp.Tool {
	return mcp.NewTool("qrdb_group_remove_member",
		mcp.WithDescription("Remove a user from an IAM group."),
		mcp.WithString("user_id", mcp.Required(), mcp.Description("The user ID (uint64).")),
		mcp.WithString("group_id", mcp.Required(), mcp.Description("The group ID.")),
	)
}

func toolGroupListMembers() mcp.Tool {
	return mcp.NewTool("qrdb_group_list_members",
		mcp.WithDescription("List the members of an IAM group."),
		mcp.WithString("group_id", mcp.Required(), mcp.Description("The group ID.")),
	)
}

// groupResult marshals v as the tool's text result.
func groupResult(tool string, v any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, &ToolError{Tool: tool, Err: err}
	}
	return mcp.NewToolResultText(string(data)), nil
}

func groupNotConfigured() *mcp.CallToolResult {
	return mcp.NewToolResultError("group service not configured")
}

func handleGroupCreate(svc *groupauthz.Service) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if svc == nil {
			return groupNotConfigured(), nil
		}
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		g, err := svc.Create(ctx, &group.CreateGroupRequest{
			TenantID:    mcp.ParseString(req, "tenant_id", ""),
			Name:        name,
			Description: mcp.ParseString(req, "description", ""),
			RoleIDs:     parseCSV(mcp.ParseString(req, "role_ids", "")),
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_group_create", Err: err}
		}
		return groupResult("qrdb_group_create", g)
	}
}

func handleGroupGet(svc *groupauthz.Service) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if svc == nil {
			return groupNotConfigured(), nil
		}
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		g, err := svc.Get(ctx, id)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_group_get", Err: err}
		}
		return groupResult("qrdb_group_get", g)
	}
}

func handleGroupGetByName(svc *groupauthz.Service) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if svc == nil {
			return groupNotConfigured(), nil
		}
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		g, err := svc.GetByName(ctx, mcp.ParseString(req, "tenant_id", ""), name)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_group_get_by_name", Err: err}
		}
		return groupResult("qrdb_group_get_by_name", g)
	}
}

func handleGroupUpdate(svc *groupauthz.Service) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if svc == nil {
			return groupNotConfigured(), nil
		}
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		args := req.GetArguments()
		up := &group.UpdateGroupRequest{ID: id, Name: mcp.ParseString(req, "name", "")}
		if _, ok := args["description"]; ok {
			d := mcp.ParseString(req, "description", "")
			up.Description = &d
		}
		if _, ok := args["role_ids"]; ok {
			up.RoleIDs = append([]string{}, parseCSV(mcp.ParseString(req, "role_ids", ""))...)
		}
		g, err := svc.Update(ctx, up)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_group_update", Err: err}
		}
		return groupResult("qrdb_group_update", g)
	}
}

func handleGroupDelete(svc *groupauthz.Service) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if svc == nil {
			return groupNotConfigured(), nil
		}
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		if err := svc.Delete(ctx, id); err != nil {
			return nil, &ToolError{Tool: "qrdb_group_delete", Err: err}
		}
		return mcp.NewToolResultText("ok"), nil
	}
}

func handleGroupList(svc *groupauthz.Service) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if svc == nil {
			return groupNotConfigured(), nil
		}
		resp, err := svc.List(ctx, &group.ListGroupsRequest{
			TenantID: mcp.ParseString(req, "tenant_id", ""),
			Page:     mcp.ParseInt(req, "page", 0),
			PageSize: mcp.ParseInt(req, "page_size", 0),
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_group_list", Err: err}
		}
		return groupResult("qrdb_group_list", resp)
	}
}

func handleGroupMember(svc *groupauthz.Service, tool string, op func(*groupauthz.Service, context.Context, uint64, string) error) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if svc == nil {
			return groupNotConfigured(), nil
		}
		userIDStr := mcp.ParseString(req, "user_id", "")
		if userIDStr == "" {
			return mcp.NewToolResultError("user_id is required"), nil
		}
		userID, err := parseUint64(userIDStr)
		if err != nil {
			return mcp.NewToolResultError("user_id must be a uint64: " + err.Error()), nil
		}
		groupID := mcp.ParseString(req, "group_id", "")
		if groupID == "" {
			return mcp.NewToolResultError("group_id is required"), nil
		}
		if err := op(svc, ctx, userID, groupID); err != nil {
			return nil, &ToolError{Tool: tool, Err: err}
		}
		return mcp.NewToolResultText("ok"), nil
	}
}

func handleGroupListMembers(svc *groupauthz.Service) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if svc == nil {
			return groupNotConfigured(), nil
		}
		groupID := mcp.ParseString(req, "group_id", "")
		if groupID == "" {
			return mcp.NewToolResultError("group_id is required"), nil
		}
		members, err := svc.ListMembers(ctx, groupID)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_group_list_members", Err: err}
		}
		return groupResult("qrdb_group_list_members", map[string]any{"members": members})
	}
}
