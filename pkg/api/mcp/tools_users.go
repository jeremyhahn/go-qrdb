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
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// userTools returns the MCP tool registrations for user management operations.
func userTools(svc user.UserServicer) []serverTool {
	return []serverTool{
		{
			Tool:    toolUserCreate(),
			Perm:    permission.UsersCreate,
			Handler: handleUserCreate(svc),
		},
		{
			Tool:    toolUserGet(),
			Perm:    permission.UsersRead,
			Handler: handleUserGet(svc),
		},
		{
			Tool:    toolUserGetByUsername(),
			Perm:    permission.UsersRead,
			Handler: handleUserGetByUsername(svc),
		},
		{
			Tool:    toolUserGetByEmail(),
			Perm:    permission.UsersRead,
			Handler: handleUserGetByEmail(svc),
		},
		{
			Tool:    toolUserGetByCertThumbprint(),
			Perm:    permission.UsersGetByCertThumbprint,
			Handler: handleUserGetByCertThumbprint(svc),
		},
		{
			Tool:    toolUserAddCertBinding(),
			Perm:    permission.UsersAddCertBinding,
			Handler: handleUserAddCertBinding(svc),
		},
		{
			Tool:    toolUserRemoveCertBinding(),
			Perm:    permission.UsersRemoveCertBinding,
			Handler: handleUserRemoveCertBinding(svc),
		},
		{
			Tool:    toolUserUpdate(),
			Perm:    permission.UsersUpdate,
			Handler: handleUserUpdate(svc),
		},
		{
			Tool:    toolUserDelete(),
			Perm:    permission.UsersDelete,
			Handler: handleUserDelete(svc),
		},
		{
			Tool:    toolUserList(),
			Perm:    permission.UsersList,
			Handler: handleUserList(svc),
		},
		{
			Tool:    toolUserEnable(),
			Perm:    permission.UsersEnable,
			Handler: handleUserEnable(svc),
		},
		{
			Tool:    toolUserDisable(),
			Perm:    permission.UsersDisable,
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
		mcp.WithString("email",
			mcp.Description("The canonical login key, unique within the tenant. Empty only for system users."),
		),
		mcp.WithString("name",
			mcp.Description("Display name. Presentation only; never a lookup key."),
		),
		mcp.WithBoolean("without_email_login_key",
			mcp.Description("Create a user with no email login key (certificate-only or bootstrap identity). Required to omit email."),
		),
		mcp.WithString("role_ids",
			mcp.Description("Comma-separated list of role IDs to assign to the user."),
		),
		// A JSON array, NOT a comma-separated string — following the
		// array-argument convention this transport already uses (see
		// parseStringArray in tools_ceremony.go). A group id is an OPAQUE
		// minted token, so a CSV encoding is lossy for the values it is meant
		// to carry: an id containing a comma is split into two memberships and
		// a blank segment is silently dropped where the store refuses it,
		// writing a different authorization graph than the caller asked for
		// while reporting success (Codex peer review, PR #198).
		mcp.WithString("group_ids",
			mcp.Description(`JSON array of opaque group-membership IDs, e.g. ["platform","sre"]. qrdb stores the membership edge and owns no Group entity.`),
		),
		mcp.WithString("auth_methods",
			mcp.Description(`JSON array of permitted authentication methods, e.g. ["webauthn","piv"]. Empty states no restriction.`),
		),
		mcp.WithString("lifecycle",
			mcp.Description("Account state to create in: pending, active or disabled. Empty defaults to active, or pending when 'pending' is set."),
		),
		mcp.WithBoolean("pending",
			mcp.Description("Shorthand for lifecycle 'pending'. Setting both is refused unless they agree."),
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

		// Opaque identifier lists arrive as arrays and are NOT split on
		// anything. A malformed element is reported rather than coerced: the
		// store refuses a blank entry, and this surface must not quietly send
		// something different from what the caller wrote.
		groupIDs, gerr := parseOpaqueStringList(req, "group_ids")
		if gerr != nil {
			return mcp.NewToolResultError(gerr.Error()), nil
		}
		authMethods, aerr := parseOpaqueStringList(req, "auth_methods")
		if aerr != nil {
			return mcp.NewToolResultError(aerr.Error()), nil
		}

		u, err := svc.CreateUser(ctx, &user.CreateUserRequest{
			Username:             username,
			Email:                mcp.ParseString(req, "email", ""),
			Name:                 mcp.ParseString(req, "name", ""),
			WithoutEmailLoginKey: mcp.ParseBoolean(req, "without_email_login_key", false),
			TenantID:             tenantID,
			RoleIDs:              roleIDs,
			GroupIDs:             groupIDs,
			AuthMethods:          authMethods,
			Lifecycle:            user.Lifecycle(mcp.ParseString(req, "lifecycle", "")),
			Pending:              mcp.ParseBoolean(req, "pending", false),
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
		idStr := mcp.ParseString(req, "id", "")
		if idStr == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		id, parseErr := parseUint64(idStr)
		if parseErr != nil {
			return mcp.NewToolResultError("id must be a uint64: " + parseErr.Error()), nil
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

// toolUserGetByUsername defines the qrdb_user_get_by_username tool schema.
func toolUserGetByUsername() mcp.Tool {
	return mcp.NewTool("qrdb_user_get_by_username",
		mcp.WithDescription("Retrieve a user by tenant-scoped username."),
		mcp.WithString("tenant_id",
			mcp.Description("Tenant ID. Empty selects the default tenant."),
		),
		mcp.WithString("username",
			mcp.Required(),
			mcp.Description("The username to resolve."),
		),
	)
}

// handleUserGetByUsername returns a tool handler that delegates to
// svc.GetUserByUsername.
func handleUserGetByUsername(svc user.UserServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenantID := mcp.ParseString(req, "tenant_id", "")
		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}

		u, err := svc.GetUserByUsername(ctx, tenantID, username)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_get_by_username", Err: err}
		}

		data, jsonErr := json.Marshal(u)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_user_get_by_username", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolUserGetByEmail defines the qrdb_user_get_by_email tool schema.
func toolUserGetByEmail() mcp.Tool {
	return mcp.NewTool("qrdb_user_get_by_email",
		mcp.WithDescription("Retrieve a user by tenant-scoped email — the canonical login key."),
		mcp.WithString("tenant_id",
			mcp.Description("Tenant ID. Empty selects the default tenant."),
		),
		mcp.WithString("email",
			mcp.Required(),
			mcp.Description("The email login key to resolve."),
		),
	)
}

// handleUserGetByEmail returns a tool handler that delegates to
// svc.GetUserByEmail.
func handleUserGetByEmail(svc user.UserServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenantID := mcp.ParseString(req, "tenant_id", "")
		email := mcp.ParseString(req, "email", "")
		if email == "" {
			return mcp.NewToolResultError("email is required"), nil
		}

		u, err := svc.GetUserByEmail(ctx, tenantID, email)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_get_by_email", Err: err}
		}

		data, jsonErr := json.Marshal(u)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_user_get_by_email", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolUserGetByCertThumbprint defines the qrdb_user_get_by_cert_thumbprint
// tool schema.
func toolUserGetByCertThumbprint() mcp.Tool {
	return mcp.NewTool("qrdb_user_get_by_cert_thumbprint",
		mcp.WithDescription("Resolve a user by an mTLS certificate's SPKI thumbprint."),
		mcp.WithString("thumbprint",
			mcp.Required(),
			mcp.Description("The base64url SPKI SHA-256 thumbprint to resolve."),
		),
	)
}

// handleUserGetByCertThumbprint returns a tool handler that delegates to
// svc.UserByCertThumbprint.
func handleUserGetByCertThumbprint(svc user.UserServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		thumbprint := mcp.ParseString(req, "thumbprint", "")
		if thumbprint == "" {
			return mcp.NewToolResultError("thumbprint is required"), nil
		}

		u, err := svc.UserByCertThumbprint(ctx, thumbprint)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_get_by_cert_thumbprint", Err: err}
		}

		data, jsonErr := json.Marshal(u)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_user_get_by_cert_thumbprint", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolUserAddCertBinding defines the qrdb_user_add_cert_binding tool schema.
func toolUserAddCertBinding() mcp.Tool {
	return mcp.NewTool("qrdb_user_add_cert_binding",
		mcp.WithDescription("Bind an mTLS certificate's SPKI thumbprint to a user so it resolves via the secondary index (the enrollment seam)."),
		mcp.WithString("user_id",
			mcp.Required(),
			mcp.Description("The ID of the user to bind the certificate to."),
		),
		mcp.WithString("thumbprint",
			mcp.Required(),
			mcp.Description("The base64url SPKI SHA-256 thumbprint to bind."),
		),
		mcp.WithNumber("not_after_unix",
			mcp.Description("Optional binding expiry as a Unix timestamp. Zero means the binding never expires."),
		),
	)
}

// handleUserAddCertBinding returns a tool handler that delegates to
// svc.AddCertBinding.
func handleUserAddCertBinding(svc user.UserServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		userIDStr := mcp.ParseString(req, "user_id", "")
		if userIDStr == "" {
			return mcp.NewToolResultError("user_id is required"), nil
		}

		thumbprint := mcp.ParseString(req, "thumbprint", "")
		if thumbprint == "" {
			return mcp.NewToolResultError("thumbprint is required"), nil
		}

		notAfterUnix := mcp.ParseInt64(req, "not_after_unix", 0)

		userID, parseErr := parseUint64(userIDStr)
		if parseErr != nil {
			return mcp.NewToolResultError("user_id must be a uint64: " + parseErr.Error()), nil
		}

		u, err := svc.AddCertBinding(ctx, userID, thumbprint, notAfterUnix)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_add_cert_binding", Err: err}
		}

		data, jsonErr := json.Marshal(u)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_user_add_cert_binding", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolUserRemoveCertBinding defines the qrdb_user_remove_cert_binding tool
// schema.
func toolUserRemoveCertBinding() mcp.Tool {
	return mcp.NewTool("qrdb_user_remove_cert_binding",
		mcp.WithDescription("Withdraw an mTLS certificate's SPKI thumbprint from a user so it no longer resolves and no longer authenticates. Withdrawing the LAST binding returns the account to admission on the certificate's CN and OU alone."),
		mcp.WithString("user_id",
			mcp.Required(),
			mcp.Description("The ID of the user to withdraw the binding from."),
		),
		mcp.WithString("thumbprint",
			mcp.Required(),
			mcp.Description("The base64url SPKI SHA-256 thumbprint to withdraw."),
		),
	)
}

// handleUserRemoveCertBinding returns a tool handler that delegates to
// svc.RemoveCertBinding.
func handleUserRemoveCertBinding(svc user.UserServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		userIDStr := mcp.ParseString(req, "user_id", "")
		if userIDStr == "" {
			return mcp.NewToolResultError("user_id is required"), nil
		}

		thumbprint := mcp.ParseString(req, "thumbprint", "")
		if thumbprint == "" {
			return mcp.NewToolResultError("thumbprint is required"), nil
		}

		userID, parseErr := parseUint64(userIDStr)
		if parseErr != nil {
			return mcp.NewToolResultError("user_id must be a uint64: " + parseErr.Error()), nil
		}

		u, err := svc.RemoveCertBinding(ctx, userID, thumbprint)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_remove_cert_binding", Err: err}
		}

		data, jsonErr := json.Marshal(u)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_user_remove_cert_binding", Err: jsonErr}
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
		mcp.WithString("email",
			mcp.Description("The new canonical login key. Left unchanged when empty; uniqueness is re-validated within the tenant."),
		),
		mcp.WithString("name",
			mcp.Description("The new display name. Left unchanged when empty."),
		),
		mcp.WithString("role_ids",
			mcp.Description("Comma-separated list of role IDs to replace the current set. Left unchanged when empty."),
		),
		mcp.WithString("group_ids",
			mcp.Description(`JSON array of opaque group-membership IDs to replace the current set, e.g. ["platform","sre"]. Left unchanged when absent; use clear_group_ids to revoke every membership.`),
		),
		mcp.WithBoolean("clear_group_ids",
			mcp.Description("Revoke every group membership. An absent argument means 'leave alone', so clearing is its own flag."),
		),
		mcp.WithString("auth_methods",
			mcp.Description(`JSON array of permitted authentication methods to replace the current set, e.g. ["piv"]. Left unchanged when absent; use clear_auth_methods to remove the restriction.`),
		),
		mcp.WithBoolean("clear_auth_methods",
			mcp.Description("Remove the permitted-method restriction entirely."),
		),
		mcp.WithString("lifecycle",
			mcp.Description("Transition the account to this state: pending, active, suspended, locked, disabled or deleting. Checked against the transition table; nothing moves out of deleting."),
		),
		mcp.WithString("last_auth",
			mcp.Description("Raise the last-authentication high-water mark, RFC 3339. A value at or before the stored one writes nothing."),
		),
	)
}

// handleUserUpdate returns a tool handler that delegates to svc.UpdateUser.
func handleUserUpdate(svc user.UserServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		idStr := mcp.ParseString(req, "id", "")
		if idStr == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		id, parseErr := parseUint64(idStr)
		if parseErr != nil {
			return mcp.NewToolResultError("id must be a uint64: " + parseErr.Error()), nil
		}

		updateReq := &user.UpdateUserRequest{
			ID:        id,
			Username:  mcp.ParseString(req, "username", ""),
			Email:     mcp.ParseString(req, "email", ""),
			Name:      mcp.ParseString(req, "name", ""),
			Lifecycle: user.Lifecycle(mcp.ParseString(req, "lifecycle", "")),
		}

		roleIDsStr := mcp.ParseString(req, "role_ids", "")
		if roleIDsStr != "" {
			updateReq.RoleIDs = parseCSV(roleIDsStr)
		}

		// A flat tool argument cannot carry nil-vs-empty the way JSON null-vs-[]
		// does, so "clear" is an explicit flag. Without one, revoking every
		// membership would be unexpressible over MCP: an empty list would read
		// as "leave alone" and the revocation would silently do nothing.
		groupIDs, gerr := parseOpaqueStringList(req, "group_ids")
		if gerr != nil {
			return mcp.NewToolResultError(gerr.Error()), nil
		}
		switch {
		case groupIDs != nil:
			updateReq.GroupIDs = groupIDs
		case mcp.ParseBoolean(req, "clear_group_ids", false):
			updateReq.GroupIDs = []string{}
		}

		authMethods, aerr := parseOpaqueStringList(req, "auth_methods")
		if aerr != nil {
			return mcp.NewToolResultError(aerr.Error()), nil
		}
		switch {
		case authMethods != nil:
			updateReq.AuthMethods = authMethods
		case mcp.ParseBoolean(req, "clear_auth_methods", false):
			updateReq.AuthMethods = []string{}
		}

		if lastAuthStr := mcp.ParseString(req, "last_auth", ""); lastAuthStr != "" {
			at, perr := time.Parse(time.RFC3339, lastAuthStr)
			if perr != nil {
				return mcp.NewToolResultError("last_auth must be RFC 3339: " + perr.Error()), nil
			}
			utc := at.UTC()
			updateReq.LastAuth = &utc
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
		idStr := mcp.ParseString(req, "id", "")
		if idStr == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		id, parseErr := parseUint64(idStr)
		if parseErr != nil {
			return mcp.NewToolResultError("id must be a uint64: " + parseErr.Error()), nil
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
		idStr := mcp.ParseString(req, "id", "")
		if idStr == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		id, parseErr := parseUint64(idStr)
		if parseErr != nil {
			return mcp.NewToolResultError("id must be a uint64: " + parseErr.Error()), nil
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
		idStr := mcp.ParseString(req, "id", "")
		if idStr == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		id, parseErr := parseUint64(idStr)
		if parseErr != nil {
			return mcp.NewToolResultError("id must be a uint64: " + parseErr.Error()), nil
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

// parseOpaqueStringList reads a list-of-opaque-identifiers argument, returning
// nil ONLY when the argument is genuinely absent — which the update path reads
// as "leave alone".
//
// It does NOT split, join, trim or otherwise transform the elements. These lists
// carry OPAQUE identifiers, so any encoding that reserves a character is lossy
// for the values it is meant to hold: a comma-separated form silently splits an
// id that contains a comma into two memberships and drops blank elements the
// store would have refused, writing a different authorization graph than the
// caller asked for while reporting success (Codex peer review, PR #198).
//
// It reads req.GetArguments() rather than mcp.ParseString because ParseString
// goes through cast.ToString, which returns "" for a value it cannot convert —
// a native JSON array among them. Every malformed or unconvertible payload
// would therefore have been indistinguishable from an absent argument, leaving
// the field unchanged on update and dropped on create while reporting success:
// the same silent-corruption class this function exists to close, reached by a
// different route (second Codex round on this fix).
//
// BOTH encodings are accepted, because both are legitimate callers. The SDK MCP
// transport follows this package's convention of sending an array as a
// JSON-encoded STRING (see parseStringArray in tools_ceremony.go), while a raw
// MCP client sending the tool a native array is answering the schema honestly
// too. Anything else is an error, never a silent absence.
func parseOpaqueStringList(req mcp.CallToolRequest, key string) ([]string, error) {
	value, present := req.GetArguments()[key]
	if !present || value == nil {
		return nil, nil
	}

	malformed := func(reason string) error {
		return &ToolArgumentError{Argument: key, Reason: reason}
	}

	switch v := value.(type) {
	case string:
		// An explicitly supplied empty string is not absence — the caller sent
		// something, and it is not a list.
		if v == "" {
			return nil, malformed("must be a JSON array of strings, not an empty string")
		}
		var out []string
		if err := json.Unmarshal([]byte(v), &out); err != nil {
			return nil, malformed("must be a JSON array of strings: " + err.Error())
		}
		// A JSON `null` decodes without error but is not a list; treat it the
		// same as an absent argument so absent and null agree.
		return out, nil
	case []any:
		out := make([]string, 0, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, malformed("element " + strconv.Itoa(i) + " must be a string")
			}
			out = append(out, s)
		}
		return out, nil
	case []string:
		// make+copy, NOT append([]string(nil), v...): appending nothing to a nil
		// slice yields NIL, so an explicitly supplied empty list came back as
		// "absent" and the update left the field unchanged instead of clearing
		// it — a revocation that reported success and revoked nothing (Codex,
		// PR #198). The whole point of this function is that those two answers
		// stay distinguishable.
		out := make([]string, len(v))
		copy(out, v)
		return out, nil
	default:
		return nil, malformed("must be a JSON array of strings")
	}
}
