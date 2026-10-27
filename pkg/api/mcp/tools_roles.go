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

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// roleTools returns the MCP tool registrations for role management and
// permission operations. Role assignment and unassignment require both
// the role and user services.
func roleTools(roleSvc role.RoleServicer, userSvc user.UserServicer, authMW *authmw.AuthMiddleware) []serverTool {
	return []serverTool{
		{
			Tool:    toolRoleCreate(),
			Perm:    permission.RolesCreate,
			Handler: handleRoleCreate(roleSvc, authMW),
		},
		{
			Tool:    toolRoleGet(),
			Perm:    permission.RolesRead,
			Handler: handleRoleGet(roleSvc),
		},
		{
			Tool:    toolRoleGetByName(),
			Perm:    permission.RolesRead,
			Handler: handleRoleGetByName(roleSvc),
		},
		{
			Tool:    toolRoleUpdate(),
			Perm:    permission.RolesUpdate,
			Handler: handleRoleUpdate(roleSvc),
		},
		{
			Tool:    toolRoleDelete(),
			Perm:    permission.RolesDelete,
			Handler: handleRoleDelete(roleSvc),
		},
		{
			Tool:    toolRoleList(),
			Perm:    permission.RolesList,
			Handler: handleRoleList(roleSvc),
		},
		{
			Tool:    toolRoleAssign(),
			Perm:    permission.RolesAssign,
			Handler: handleRoleAssign(roleSvc, userSvc, authMW),
		},
		{
			Tool:    toolRoleUnassign(),
			Perm:    permission.RolesUnassign,
			Handler: handleRoleUnassign(userSvc, authMW),
		},
		{
			Tool:    toolPermissionsList(),
			Perm:    permission.PermissionsList,
			Handler: handlePermissionsList(),
		},
		{
			Tool:    toolPermissionsResolve(),
			Perm:    permission.PermissionsResolve,
			Handler: handlePermissionsResolve(roleSvc),
		},
		{
			Tool: toolPermissionsOwn(),
			// Authentication only: every authenticated caller may read its
			// own effective permissions, which role.ListOwnPermissions
			// resolves in the caller's own tenant.
			Perm:    permission.IdentityGated,
			Handler: handlePermissionsOwn(roleSvc),
		},
		{
			Tool: toolPermissionsAuthorize(),
			// Authentication only for the caller's own question; another
			// identity needs permissions:resolve in its tenant, which
			// AuthMiddleware.EvaluateAuthorization enforces.
			Perm:    permission.IdentityGated,
			Handler: handlePermissionsAuthorize(authMW),
		},
		{
			Tool: toolRoleReconcile(),
			// roles:create, not a verb of its own — see the REST route
			// comment: a dedicated permission would be missing from every role
			// seeded before it existed, so nobody could hold the grant needed
			// to repair that absence.
			Perm:    permission.RolesCreate,
			Handler: handleRoleReconcile(roleSvc, authMW),
		},
	}
}

// toolRoleReconcile defines the qrdb_role_reconcile_builtin tool schema.
func toolRoleReconcile() mcp.Tool {
	return mcp.NewTool("qrdb_role_reconcile_builtin",
		mcp.WithDescription("Converge a tenant's stored built-in roles onto this build's definitions: add permissions the build now grants, remove ones it no longer grants, leave custom roles untouched. Idempotent."),
		mcp.WithString("tenant_id",
			mcp.Description("The tenant to reconcile. Defaults to the caller's own effective tenant when empty."),
		),
	)
}

// handleRoleReconcile returns a tool handler that delegates to
// roleSvc.ReconcileBuiltInRoles.
func handleRoleReconcile(roleSvc role.RoleServicer, authMW *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenantID := mcp.ParseString(req, "tenant_id", "")
		if err := authMW.AuthorizeSOExclusive(ctx, tenantID, "reconcile built-in roles"); err != nil {
			return nil, &ToolError{Tool: "qrdb_role_reconcile_builtin", Err: err}
		}
		report, err := roleSvc.ReconcileBuiltInRoles(ctx, tenantID)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_role_reconcile_builtin", Err: err}
		}

		data, jsonErr := json.Marshal(report)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_role_reconcile_builtin", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolRoleCreate defines the qrdb_role_create tool schema.
func toolRoleCreate() mcp.Tool {
	return mcp.NewTool("qrdb_role_create",
		mcp.WithDescription("Create a new RBAC role with the specified permissions."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The role name. Must be unique within the tenant."),
		),
		mcp.WithString("tenant_id",
			mcp.Description("The tenant ID. Defaults to the system default tenant when empty."),
		),
		mcp.WithString("id",
			mcp.Description("Optional pre-assigned deterministic role ID. When empty, a random UUID is generated."),
		),
		mcp.WithString("permissions",
			mcp.Required(),
			mcp.Description(`Comma-separated list of permissions in "resource:action" format (e.g., "kv:put,kv:get,admin:health").`),
		),
	)
}

// handleRoleCreate returns a tool handler that delegates to
// roleSvc.CreateRole.
func handleRoleCreate(roleSvc role.RoleServicer, authMW *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		permsStr := mcp.ParseString(req, "permissions", "")
		if permsStr == "" {
			return mcp.NewToolResultError("permissions is required"), nil
		}

		perms, err := parsePermissions(permsStr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		tenantID := mcp.ParseString(req, "tenant_id", "")
		if err := authMW.AuthorizeSOExclusive(ctx, tenantID, "create role"); err != nil {
			return nil, &ToolError{Tool: "qrdb_role_create", Err: err}
		}

		r, createErr := roleSvc.CreateRole(ctx, &role.CreateRoleRequest{
			ID:          mcp.ParseString(req, "id", ""),
			Name:        name,
			TenantID:    tenantID,
			Permissions: perms,
		})
		if createErr != nil {
			return nil, &ToolError{Tool: "qrdb_role_create", Err: createErr}
		}

		data, jsonErr := json.Marshal(r)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_role_create", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolRoleGet defines the qrdb_role_get tool schema.
func toolRoleGet() mcp.Tool {
	return mcp.NewTool("qrdb_role_get",
		mcp.WithDescription("Retrieve a role by its globally unique ID."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The role UUID."),
		),
	)
}

// handleRoleGet returns a tool handler that delegates to roleSvc.GetRole.
func handleRoleGet(roleSvc role.RoleServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}

		r, err := roleSvc.GetRole(ctx, id)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_role_get", Err: err}
		}

		data, jsonErr := json.Marshal(r)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_role_get", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolRoleGetByName defines the qrdb_role_get_by_name tool schema.
func toolRoleGetByName() mcp.Tool {
	return mcp.NewTool("qrdb_role_get_by_name",
		mcp.WithDescription("Retrieve a role by its tenant-scoped name."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The role name, unique within the tenant."),
		),
		mcp.WithString("tenant_id",
			mcp.Description("The tenant ID. Defaults to the system default tenant when empty."),
		),
	)
}

// handleRoleGetByName returns a tool handler that delegates to
// roleSvc.GetRoleByName.
func handleRoleGetByName(roleSvc role.RoleServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		r, err := roleSvc.GetRoleByName(ctx, mcp.ParseString(req, "tenant_id", ""), name)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_role_get_by_name", Err: err}
		}

		data, jsonErr := json.Marshal(r)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_role_get_by_name", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolRoleUpdate defines the qrdb_role_update tool schema.
func toolRoleUpdate() mcp.Tool {
	return mcp.NewTool("qrdb_role_update",
		mcp.WithDescription("Apply partial updates to an existing role."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The role UUID to update."),
		),
		mcp.WithString("name",
			mcp.Description("The new role name. Left unchanged when empty."),
		),
		mcp.WithString("permissions",
			mcp.Description(`Comma-separated list of permissions to replace the current set. Left unchanged when empty.`),
		),
	)
}

// handleRoleUpdate returns a tool handler that delegates to
// roleSvc.UpdateRole.
func handleRoleUpdate(roleSvc role.RoleServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}

		updateReq := &role.UpdateRoleRequest{
			ID:   id,
			Name: mcp.ParseString(req, "name", ""),
		}

		permsStr := mcp.ParseString(req, "permissions", "")
		if permsStr != "" {
			perms, err := parsePermissions(permsStr)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			updateReq.Permissions = perms
		}

		r, err := roleSvc.UpdateRole(ctx, updateReq)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_role_update", Err: err}
		}

		data, jsonErr := json.Marshal(r)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_role_update", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolRoleDelete defines the qrdb_role_delete tool schema.
func toolRoleDelete() mcp.Tool {
	return mcp.NewTool("qrdb_role_delete",
		mcp.WithDescription("Delete a non-system role by ID."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The role UUID to delete."),
		),
	)
}

// handleRoleDelete returns a tool handler that delegates to
// roleSvc.DeleteRole.
func handleRoleDelete(roleSvc role.RoleServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}

		if err := roleSvc.DeleteRole(ctx, id); err != nil {
			return nil, &ToolError{Tool: "qrdb_role_delete", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolRoleList defines the qrdb_role_list tool schema.
func toolRoleList() mcp.Tool {
	return mcp.NewTool("qrdb_role_list",
		mcp.WithDescription("Return a paginated list of roles for a tenant."),
		mcp.WithString("tenant_id",
			mcp.Description("Filter roles by tenant ID. Defaults to the system default tenant when empty."),
		),
		mcp.WithNumber("page",
			mcp.Description("1-based page number. Defaults to 1."),
		),
		mcp.WithNumber("page_size",
			mcp.Description("Maximum number of roles per page. Defaults to the server default."),
		),
	)
}

// handleRoleList returns a tool handler that delegates to roleSvc.ListRoles.
func handleRoleList(roleSvc role.RoleServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		resp, err := roleSvc.ListRoles(ctx, &role.ListRolesRequest{
			TenantID: mcp.ParseString(req, "tenant_id", ""),
			Page:     mcp.ParseInt(req, "page", 0),
			PageSize: mcp.ParseInt(req, "page_size", 0),
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_role_list", Err: err}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_role_list", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolRoleAssign defines the qrdb_role_assign tool schema.
func toolRoleAssign() mcp.Tool {
	return mcp.NewTool("qrdb_role_assign",
		mcp.WithDescription("Assign a role to a user. The role ID is appended to the user's role set if not already present."),
		mcp.WithString("user_id",
			mcp.Required(),
			mcp.Description("The user UUID to assign the role to."),
		),
		mcp.WithString("role_id",
			mcp.Required(),
			mcp.Description("The role UUID to assign."),
		),
	)
}

// handleRoleAssign returns a tool handler that fetches the user, appends the
// role ID if not already present, and updates the user.
func handleRoleAssign(roleSvc role.RoleServicer, userSvc user.UserServicer, authMW *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		userIDStr := mcp.ParseString(req, "user_id", "")
		if userIDStr == "" {
			return mcp.NewToolResultError("user_id is required"), nil
		}
		userID, parseErr := parseUint64(userIDStr)
		if parseErr != nil {
			return mcp.NewToolResultError("user_id must be a uint64: " + parseErr.Error()), nil
		}
		roleID := mcp.ParseString(req, "role_id", "")
		if roleID == "" {
			return mcp.NewToolResultError("role_id is required"), nil
		}

		// The same gate every role-assignment write passes: roles:assign in
		// the user's tenant, and the role must exist there.
		if err := authMW.AuthorizeRoleAssignment(ctx, userSvc, roleSvc, userID, roleID); err != nil {
			return nil, &ToolError{Tool: "qrdb_role_assign", Err: err}
		}

		u, err := userSvc.GetUser(ctx, userID)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_role_assign", Err: err}
		}

		// Check if role is already assigned.
		for _, rid := range u.RoleIDs {
			if rid == roleID {
				return mcp.NewToolResultText("ok"), nil
			}
		}

		roleIDs := make([]string, len(u.RoleIDs), len(u.RoleIDs)+1)
		copy(roleIDs, u.RoleIDs)
		roleIDs = append(roleIDs, roleID)

		if _, updateErr := userSvc.UpdateUser(ctx, &user.UpdateUserRequest{
			Guard:   authMW.SORoleGuard(ctx),
			ID:      userID,
			RoleIDs: roleIDs,
		}); updateErr != nil {
			return nil, &ToolError{Tool: "qrdb_role_assign", Err: updateErr}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolRoleUnassign defines the qrdb_role_unassign tool schema.
func toolRoleUnassign() mcp.Tool {
	return mcp.NewTool("qrdb_role_unassign",
		mcp.WithDescription("Remove a role from a user."),
		mcp.WithString("user_id",
			mcp.Required(),
			mcp.Description("The user UUID to remove the role from."),
		),
		mcp.WithString("role_id",
			mcp.Required(),
			mcp.Description("The role UUID to remove."),
		),
	)
}

// handleRoleUnassign returns a tool handler that fetches the user, removes the
// role ID from the user's role set, and updates the user.
func handleRoleUnassign(userSvc user.UserServicer, authMW *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		userIDStr := mcp.ParseString(req, "user_id", "")
		if userIDStr == "" {
			return mcp.NewToolResultError("user_id is required"), nil
		}
		userID, parseErr := parseUint64(userIDStr)
		if parseErr != nil {
			return mcp.NewToolResultError("user_id must be a uint64: " + parseErr.Error()), nil
		}
		roleID := mcp.ParseString(req, "role_id", "")
		if roleID == "" {
			return mcp.NewToolResultError("role_id is required"), nil
		}

		if err := authMW.AuthorizeRoleUnassignment(ctx, userSvc, userID, roleID); err != nil {
			return nil, &ToolError{Tool: "qrdb_role_unassign", Err: err}
		}

		u, err := userSvc.GetUser(ctx, userID)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_role_unassign", Err: err}
		}

		roleIDs := make([]string, 0, len(u.RoleIDs))
		for _, rid := range u.RoleIDs {
			if rid != roleID {
				roleIDs = append(roleIDs, rid)
			}
		}

		if _, updateErr := userSvc.UpdateUser(ctx, &user.UpdateUserRequest{
			Guard:   authMW.SORoleGuard(ctx),
			ID:      userID,
			RoleIDs: roleIDs,
		}); updateErr != nil {
			return nil, &ToolError{Tool: "qrdb_role_unassign", Err: updateErr}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolPermissionsList defines the qrdb_permissions_list tool schema.
func toolPermissionsList() mcp.Tool {
	return mcp.NewTool("qrdb_permissions_list",
		mcp.WithDescription("List all registered permissions grouped by category for UI rendering."),
	)
}

// handlePermissionsList returns a tool handler that returns all permission
// categories from the global registry.
func handlePermissionsList() mcpserver.ToolHandlerFunc {
	return func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		categories := permission.Categories()

		data, jsonErr := json.Marshal(categories)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_permissions_list", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolPermissionsResolve defines the qrdb_permissions_resolve tool schema.
func toolPermissionsResolve() mcp.Tool {
	return mcp.NewTool("qrdb_permissions_resolve",
		mcp.WithDescription("Resolve the union of effective permissions for a set of roles (sorted, deduplicated)."),
		mcp.WithString("role_ids",
			mcp.Required(),
			mcp.Description("Comma-separated list of role UUIDs to resolve."),
		),
	)
}

// handlePermissionsResolve returns a tool handler that delegates to
// roleSvc.ResolvePermissions.
func handlePermissionsResolve(roleSvc role.RoleServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		roleIDs := parseCSV(mcp.ParseString(req, "role_ids", ""))
		if len(roleIDs) == 0 {
			return mcp.NewToolResultError("role_ids is required"), nil
		}

		perms, err := roleSvc.ResolvePermissions(ctx, roleIDs)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_permissions_resolve", Err: err}
		}

		out := make([]string, len(perms))
		for i, p := range perms {
			out[i] = p.String()
		}

		data, jsonErr := json.Marshal(map[string][]string{"permissions": out})
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_permissions_resolve", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolPermissionsOwn defines the qrdb_permissions_own tool schema.
func toolPermissionsOwn() mcp.Tool {
	return mcp.NewTool("qrdb_permissions_own",
		mcp.WithDescription("List the authenticated caller's own effective permissions, resolved in the caller's own tenant."),
	)
}

// handlePermissionsOwn returns a tool handler that delegates to
// role.ListOwnPermissions.
func handlePermissionsOwn(roleSvc role.RoleServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		perms, err := role.ListOwnPermissions(ctx, roleSvc)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_permissions_own", Err: err}
		}
		data, err := json.Marshal(map[string][]role.PermissionInfo{"permissions": perms})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_permissions_own", Err: err}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolPermissionsAuthorize defines the qrdb_permissions_authorize tool
// schema.
func toolPermissionsAuthorize() mcp.Tool {
	return mcp.NewTool("qrdb_permissions_authorize",
		mcp.WithDescription("Decide whether an identity may perform an action on a resource, through the server's auth chain. Without subject and tenant_id the identity is the caller; another identity needs permissions:resolve in its tenant."),
		mcp.WithString("resource",
			mcp.Required(),
			mcp.Description(`The resource, e.g. "kv".`),
		),
		mcp.WithString("action",
			mcp.Required(),
			mcp.Description(`The action, e.g. "get".`),
		),
		mcp.WithString("subject",
			mcp.Description("The subject of the identity to evaluate. Defaults to the caller when subject and tenant_id are empty."),
		),
		mcp.WithString("tenant_id",
			mcp.Description("The tenant of the identity to evaluate."),
		),
		mcp.WithString("roles",
			mcp.Description("Comma-separated role IDs of the identity to evaluate."),
		),
	)
}

// handlePermissionsAuthorize returns a tool handler that answers through
// AuthMiddleware.EvaluateAuthorization: allowed, or denied with the reason.
// Asking about another identity needs permissions:resolve in that
// identity's tenant, which the evaluator enforces.
func handlePermissionsAuthorize(authMW *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var target *auth.Identity
		subject := mcp.ParseString(req, "subject", "")
		tenantID := mcp.ParseString(req, "tenant_id", "")
		if subject != "" || tenantID != "" {
			target = &auth.Identity{Subject: subject, TenantID: tenantID, Roles: parseCSV(mcp.ParseString(req, "roles", ""))}
		}
		decision, err := authMW.EvaluateAuthorization(ctx, target, mcp.ParseString(req, "resource", ""), mcp.ParseString(req, "action", ""))
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_permissions_authorize", Err: err}
		}
		resp := authorizeResult{Allowed: decision == nil}
		if decision != nil {
			resp.Reason = decision.Error()
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_permissions_authorize", Err: err}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// authorizeResult is the JSON text qrdb_permissions_authorize returns: the
// decision, with the denial's reason when it is not allowed.
type authorizeResult struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

// parsePermissions splits a comma-separated string of permissions and
// validates each one using permission.Parse. Returns the parsed
// permissions or the first validation error encountered.
func parsePermissions(s string) ([]permission.Permission, error) {
	parts := parseCSV(s)
	if len(parts) == 0 {
		return nil, nil
	}
	perms := make([]permission.Permission, 0, len(parts))
	for _, p := range parts {
		perm, err := permission.Parse(p)
		if err != nil {
			return nil, err
		}
		perms = append(perms, perm)
	}
	return perms, nil
}
