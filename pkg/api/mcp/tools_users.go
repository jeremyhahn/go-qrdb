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

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// userRecord is the JSON wire representation of a user entity returned by
// every qrdb_user_* tool. It exists so this transport projects the SAME
// fields REST, gRPC and QUIC do — in particular, so FactorEnrollment.Data
// never crosses this transport either. Marshaling the domain *user.User
// directly (this transport's historical behavior) leaked it, since Data is
// json.RawMessage and survives an ordinary json.Marshal untouched
// (go-qrdb#192/#197).
//
// Nested types use LOCAL camelCase wire structs (mcpCertBinding,
// mcpRoleBinding, mcpFactorMetadata) rather than the SDK's snake_case
// transport.CertBinding/RoleBinding/FactorMetadata directly, so every field
// in the payload this tool returns shares one casing convention, matching
// this transport's pre-existing top-level fields (certBindings, roleIDs,
// tenantID, ...).
type userRecord struct {
	ID       uint64   `json:"id"`
	Username string   `json:"username"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	TenantID string   `json:"tenantID"`
	RoleIDs  []string `json:"roleIDs"`
	System   bool     `json:"system"`
	// Lifecycle is the canonical and only account state.
	Lifecycle    string              `json:"lifecycle,omitempty"`
	AuthMethods  []string            `json:"authMethods,omitempty"`
	LastAuth     string              `json:"lastAuth,omitempty"`
	CertBindings []mcpCertBinding    `json:"certBindings,omitempty"`
	RoleBindings []mcpRoleBinding    `json:"roleBindings,omitempty"`
	Attributes   map[string]string   `json:"attributes,omitempty"`
	Factors      []mcpFactorMetadata `json:"factors,omitempty"`
	// MFAEnabled/MFAMethods/MFAEnrolledAt are DERIVED from Factors, never
	// stored.
	MFAEnabled    bool     `json:"mfaEnabled"`
	MFAMethods    []string `json:"mfaMethods,omitempty"`
	MFAEnrolledAt string   `json:"mfaEnrolledAt,omitempty"`
	CreatedAt     string   `json:"createdAt"`
	UpdatedAt     string   `json:"updatedAt"`
}

// mcpCertBinding is this transport's camelCase wire form of one enrolled
// mTLS key.
type mcpCertBinding struct {
	Thumbprint string `json:"thumbprint"`
	Kind       string `json:"kind"`
	NotAfter   string `json:"notAfter,omitempty"`
}

// mcpRoleBinding is this transport's camelCase wire form of one tenant-scoped
// role binding.
type mcpRoleBinding struct {
	RoleID   string `json:"roleID"`
	TenantID string `json:"tenantID"`
}

// mcpFactorMetadata is this transport's camelCase wire form of one enrolled
// auth factor, Data omitted.
type mcpFactorMetadata struct {
	ID         string `json:"id"`
	FactorName string `json:"factorName"`
	CreatedAt  string `json:"createdAt,omitempty"`
}

// certBindingsToRecord renders the account's bindings through the shared
// projection, so this transport lists exactly what REST, gRPC and QUIC list.
func certBindingsToRecord(bindings []user.CertBinding) []mcpCertBinding {
	wire := user.WireCertBindings(bindings)
	if wire == nil {
		return nil
	}
	out := make([]mcpCertBinding, len(wire))
	for i, b := range wire {
		out[i] = mcpCertBinding{Thumbprint: b.Thumbprint, Kind: b.Kind, NotAfter: b.NotAfter}
	}
	return out
}

// roleBindingsToRecord renders the account's tenant-scoped role bindings
// through the shared projection.
func roleBindingsToRecord(bindings []user.RoleBinding) []mcpRoleBinding {
	wire := user.WireRoleBindings(bindings)
	if wire == nil {
		return nil
	}
	out := make([]mcpRoleBinding, len(wire))
	for i, b := range wire {
		out[i] = mcpRoleBinding{RoleID: b.RoleID, TenantID: b.TenantID}
	}
	return out
}

// factorsToRecord renders factor METADATA (see user.FactorView); the opaque
// data blob never crosses this transport (go-qrdb#192/#197).
func factorsToRecord(factors []transport.FactorMetadata) []mcpFactorMetadata {
	if len(factors) == 0 {
		return nil
	}
	out := make([]mcpFactorMetadata, len(factors))
	for i, f := range factors {
		out[i] = mcpFactorMetadata{ID: f.ID, FactorName: f.FactorName, CreatedAt: f.CreatedAt}
	}
	return out
}

// listUsersRecord is the JSON wire payload for qrdb_user_list, mirroring
// user.ListUsersResponse with each entry projected through userToRecord.
type listUsersRecord struct {
	Users   []*userRecord `json:"users"`
	Total   int           `json:"total"`
	HasMore bool          `json:"hasMore"`
}

// userToRecord converts a user.User to its MCP wire representation. Factors
// metadata and the derived MFA fields are computed at read time from the
// real enrollment records via lister (service.UserAuthFactorServicer), read
// once -- never from a stored field on u. lister may be nil (the auth-factor
// subsystem is not configured for this deployment); a lister that fails
// returns the *user.FactorLookupError rather than a record claiming no
// factors.
func userToRecord(ctx context.Context, lister user.FactorLister, u *user.User) (*userRecord, error) {
	if u == nil {
		return nil, nil
	}
	roleIDs := u.RoleIDs
	if roleIDs == nil {
		roleIDs = []string{}
	}
	factors, err := user.ResolveFactorView(ctx, lister, u.ID)
	if err != nil {
		return nil, err
	}
	var lastAuth string
	if u.LastAuth != nil {
		lastAuth = u.LastAuth.UTC().Format(time.RFC3339)
	}
	return &userRecord{
		ID:            u.ID,
		Username:      u.Username,
		Email:         u.Email,
		Name:          u.Name,
		TenantID:      u.TenantID,
		RoleIDs:       roleIDs,
		System:        u.System,
		Lifecycle:     string(u.Lifecycle),
		AuthMethods:   u.AuthMethods,
		LastAuth:      lastAuth,
		CertBindings:  certBindingsToRecord(u.CertBindings),
		RoleBindings:  roleBindingsToRecord(u.RoleBindings),
		Attributes:    u.Attributes,
		Factors:       factorsToRecord(factors.Factors),
		MFAEnabled:    factors.MFAEnabled,
		MFAMethods:    factors.MFAMethods,
		MFAEnrolledAt: factors.MFAEnrolledAt,
		CreatedAt:     u.CreatedAt.Format(time.RFC3339),
		UpdatedAt:     u.UpdatedAt.Format(time.RFC3339),
	}, nil
}

// marshalUserRecord projects u (see userToRecord) and encodes it. Either
// failure -- a factor lookup or the encoding -- is returned for the caller's
// ToolError, so the tool middleware classifies it like any service error.
func marshalUserRecord(ctx context.Context, lister user.FactorLister, u *user.User) ([]byte, error) {
	rec, err := userToRecord(ctx, lister, u)
	if err != nil {
		return nil, err
	}
	return json.Marshal(rec)
}

// userTools returns the MCP tool registrations for user management operations.
//
// authMW and roleSvc are used by handleUserCreate and handleUserUpdate, to
// authorize and validate a write that touches RoleIDs, RoleBindings, or
// Attributes (go-qrdb#192 item 3); every other handler ignores them.
func userTools(svc user.UserServicer, factorSvc user.FactorLister, authMW *authmw.AuthMiddleware, roleSvc role.RoleServicer) []serverTool {
	return []serverTool{
		{
			Tool:    toolUserCreate(),
			Perm:    permission.UsersCreate,
			Handler: handleUserCreate(svc, factorSvc, authMW, roleSvc),
		},
		{
			Tool:    toolUserGet(),
			Perm:    permission.UsersRead,
			Handler: handleUserGet(svc, factorSvc),
		},
		{
			Tool:    toolUserGetByUsername(),
			Perm:    permission.UsersRead,
			Handler: handleUserGetByUsername(svc, factorSvc),
		},
		{
			Tool:    toolUserGetByEmail(),
			Perm:    permission.UsersRead,
			Handler: handleUserGetByEmail(svc, factorSvc),
		},
		{
			Tool:    toolUserGetByCertThumbprint(),
			Perm:    permission.UsersGetByCertThumbprint,
			Handler: handleUserGetByCertThumbprint(svc, factorSvc),
		},
		{
			Tool:    toolUserAddCertBinding(),
			Perm:    permission.UsersAddCertBinding,
			Handler: handleUserAddCertBinding(svc, factorSvc, authMW),
		},
		{
			Tool:    toolUserRemoveCertBinding(),
			Perm:    permission.UsersRemoveCertBinding,
			Handler: handleUserRemoveCertBinding(svc, factorSvc),
		},
		{
			Tool:    toolUserUpdate(),
			Perm:    permission.UsersUpdate,
			Handler: handleUserUpdate(svc, factorSvc, authMW, roleSvc),
		},
		{
			Tool:    toolUserDelete(),
			Perm:    permission.UsersDelete,
			Handler: handleUserDelete(svc),
		},
		{
			Tool:    toolUserList(),
			Perm:    permission.UsersList,
			Handler: handleUserList(svc, factorSvc),
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
		// wire.DecodeArgs, which the ceremony tools use); see parseOpaqueStringList.
		mcp.WithString("auth_methods",
			mcp.Description(`JSON array of permitted authentication methods, e.g. ["webauthn","piv"]: the built-in "mtls", "piv", "webauthn", or the name of an authenticator this deployment runs. Empty states no restriction.`),
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
func handleUserCreate(svc user.UserServicer, factorSvc user.FactorLister, authMW *authmw.AuthMiddleware, roleSvc role.RoleServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}

		tenantID := mcp.ParseString(req, "tenant_id", "")
		roleIDs := parseCSV(mcp.ParseString(req, "role_ids", ""))

		// Opaque lists arrive as arrays and are NOT split on anything. A
		// malformed element is reported rather than coerced: the store refuses
		// a blank entry, and this surface must not quietly send something
		// different from what the caller wrote.
		authMethods, aerr := parseOpaqueStringList(req, "auth_methods")
		if aerr != nil {
			return mcp.NewToolResultError(aerr.Error()), nil
		}

		if len(roleIDs) > 0 {
			if err := authMW.AuthorizeRoleAssignmentWriteOnCreate(ctx, roleSvc, tenantID, roleIDs); err != nil {
				return nil, &ToolError{Tool: "qrdb_user_create", Err: err}
			}
		}

		u, err := svc.CreateUser(ctx, &user.CreateUserRequest{
			Username:             username,
			Email:                mcp.ParseString(req, "email", ""),
			Name:                 mcp.ParseString(req, "name", ""),
			WithoutEmailLoginKey: mcp.ParseBoolean(req, "without_email_login_key", false),
			TenantID:             tenantID,
			RoleIDs:              roleIDs,
			AuthMethods:          authMethods,
			Lifecycle:            user.Lifecycle(mcp.ParseString(req, "lifecycle", "")),
			Pending:              mcp.ParseBoolean(req, "pending", false),
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_create", Err: err}
		}

		data, err := marshalUserRecord(ctx, factorSvc, u)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_create", Err: err}
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
func handleUserGet(svc user.UserServicer, factorSvc user.FactorLister) mcpserver.ToolHandlerFunc {
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

		data, err := marshalUserRecord(ctx, factorSvc, u)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_get", Err: err}
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
func handleUserGetByUsername(svc user.UserServicer, factorSvc user.FactorLister) mcpserver.ToolHandlerFunc {
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

		data, err := marshalUserRecord(ctx, factorSvc, u)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_get_by_username", Err: err}
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
func handleUserGetByEmail(svc user.UserServicer, factorSvc user.FactorLister) mcpserver.ToolHandlerFunc {
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

		data, err := marshalUserRecord(ctx, factorSvc, u)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_get_by_email", Err: err}
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
func handleUserGetByCertThumbprint(svc user.UserServicer, factorSvc user.FactorLister) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		thumbprint := mcp.ParseString(req, "thumbprint", "")
		if thumbprint == "" {
			return mcp.NewToolResultError("thumbprint is required"), nil
		}

		u, err := svc.UserByCertThumbprint(ctx, thumbprint)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_get_by_cert_thumbprint", Err: err}
		}

		data, err := marshalUserRecord(ctx, factorSvc, u)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_get_by_cert_thumbprint", Err: err}
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
func handleUserAddCertBinding(svc user.UserServicer, factorSvc user.FactorLister, authMW *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
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

		// Binding a thumbprint binds a key: the certificate target rule applies.
		if err := authMW.AuthorizeCertBindingTarget(ctx, svc, userID); err != nil {
			return nil, &ToolError{Tool: "qrdb_user_add_cert_binding", Err: err}
		}
		u, err := svc.AddCertBinding(ctx, userID, thumbprint, notAfterUnix)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_add_cert_binding", Err: err}
		}

		data, err := marshalUserRecord(ctx, factorSvc, u)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_add_cert_binding", Err: err}
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
func handleUserRemoveCertBinding(svc user.UserServicer, factorSvc user.FactorLister) mcpserver.ToolHandlerFunc {
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

		data, err := marshalUserRecord(ctx, factorSvc, u)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_remove_cert_binding", Err: err}
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
		mcp.WithString("role_bindings",
			mcp.Description(`JSON array of tenant-scoped role bindings to replace the current set, e.g. [{"role_id":"...","tenant_id":"..."}]. Left unchanged when absent; use clear_role_bindings to remove all of them.`),
		),
		mcp.WithBoolean("clear_role_bindings",
			mcp.Description("Remove every tenant-scoped role binding."),
		),
		mcp.WithString("attributes",
			mcp.Description(`JSON object of ABAC key/value pairs to replace the current set, e.g. {"department":"eng"}. Left unchanged when absent; use clear_attributes to remove all of them.`),
		),
		mcp.WithBoolean("clear_attributes",
			mcp.Description("Remove every ABAC attribute."),
		),
	)
}

// handleUserUpdate returns a tool handler that delegates to svc.UpdateUser.
func handleUserUpdate(svc user.UserServicer, factorSvc user.FactorLister, authMW *authmw.AuthMiddleware, roleSvc role.RoleServicer) mcpserver.ToolHandlerFunc {
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
		// does, so "clear" is an explicit flag. Without one, lifting the
		// restriction would be unexpressible over MCP: an empty list would read
		// as "leave alone" and the clear would silently do nothing.
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

		roleBindings, rerr := parseRoleBindingsArg(req, "role_bindings")
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		switch {
		case roleBindings != nil:
			updateReq.RoleBindings = roleBindings
		case mcp.ParseBoolean(req, "clear_role_bindings", false):
			updateReq.RoleBindings = []user.RoleBinding{}
		}

		attributes, atterr := parseAttributesArg(req, "attributes")
		if atterr != nil {
			return mcp.NewToolResultError(atterr.Error()), nil
		}
		switch {
		case attributes != nil:
			updateReq.Attributes = attributes
		case mcp.ParseBoolean(req, "clear_attributes", false):
			updateReq.Attributes = map[string]string{}
		}

		if authmw.TouchesRoleAssignment(updateReq.RoleIDs, updateReq.RoleBindings, updateReq.Attributes) {
			if err := authMW.AuthorizeRoleAssignmentWrite(ctx, svc, roleSvc, id, updateReq.RoleIDs, updateReq.RoleBindings); err != nil {
				return nil, &ToolError{Tool: "qrdb_user_update", Err: err}
			}
		}

		u, err := svc.UpdateUser(ctx, updateReq)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_update", Err: err}
		}

		data, err := marshalUserRecord(ctx, factorSvc, u)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_update", Err: err}
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
func handleUserList(svc user.UserServicer, factorSvc user.FactorLister) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		resp, err := svc.ListUsers(ctx, &user.ListUsersRequest{
			TenantID: mcp.ParseString(req, "tenant_id", ""),
			Page:     mcp.ParseInt(req, "page", 0),
			PageSize: mcp.ParseInt(req, "page_size", 0),
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_list", Err: err}
		}

		records := make([]*userRecord, len(resp.Users))
		for i, u := range resp.Users {
			if records[i], err = userToRecord(ctx, factorSvc, u); err != nil {
				return nil, &ToolError{Tool: "qrdb_user_list", Err: err}
			}
		}
		data, jsonErr := json.Marshal(listUsersRecord{
			Users:   records,
			Total:   resp.Total,
			HasMore: resp.HasMore,
		})
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
// for the values it is meant to hold: a comma-separated form silently splits a
// value that contains a comma into two and drops blank elements the store would
// have refused, writing something different from what the caller asked for
// while reporting success (Codex peer review, PR #198).
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
// JSON-encoded STRING (see wire.DecodeArgs), while a raw
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

// mcpRoleBindingArg is the JSON shape one element of the "role_bindings" tool
// argument takes.
type mcpRoleBindingArg struct {
	RoleID   string `json:"role_id"`
	TenantID string `json:"tenant_id"`
}

// parseRoleBindingsArg reads the "role_bindings" argument, returning nil ONLY
// when it is genuinely absent — which the update path reads as "leave alone"
// (see clear_role_bindings for the explicit clear). Follows
// parseOpaqueStringList's convention: both a JSON-encoded string and a native
// array are accepted, since both the SDK MCP transport and a raw MCP client
// are legitimate callers.
func parseRoleBindingsArg(req mcp.CallToolRequest, key string) ([]user.RoleBinding, error) {
	value, present := req.GetArguments()[key]
	if !present || value == nil {
		return nil, nil
	}

	malformed := func(reason string) error {
		return &ToolArgumentError{Argument: key, Reason: reason}
	}

	var raw []mcpRoleBindingArg
	switch v := value.(type) {
	case string:
		if v == "" {
			return nil, malformed(`must be a JSON array of {"role_id","tenant_id"} objects, not an empty string`)
		}
		if err := json.Unmarshal([]byte(v), &raw); err != nil {
			return nil, malformed(`must be a JSON array of {"role_id","tenant_id"} objects: ` + err.Error())
		}
	case []any:
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil, malformed(`must be a JSON array of {"role_id","tenant_id"} objects`)
		}
		if err := json.Unmarshal(encoded, &raw); err != nil {
			return nil, malformed(`must be a JSON array of {"role_id","tenant_id"} objects: ` + err.Error())
		}
	default:
		return nil, malformed(`must be a JSON array of {"role_id","tenant_id"} objects`)
	}

	out := make([]user.RoleBinding, len(raw))
	for i, r := range raw {
		out[i] = user.RoleBinding{RoleID: r.RoleID, TenantID: r.TenantID}
	}
	return out, nil
}

// parseAttributesArg reads the "attributes" argument, returning nil ONLY when
// it is genuinely absent — which the update path reads as "leave alone" (see
// clear_attributes for the explicit clear). Both a JSON-encoded string and a
// native object are accepted, matching parseRoleBindingsArg.
func parseAttributesArg(req mcp.CallToolRequest, key string) (map[string]string, error) {
	value, present := req.GetArguments()[key]
	if !present || value == nil {
		return nil, nil
	}

	malformed := func(reason string) error {
		return &ToolArgumentError{Argument: key, Reason: reason}
	}

	toStringMap := func(raw map[string]any) (map[string]string, error) {
		out := make(map[string]string, len(raw))
		for k, v := range raw {
			s, ok := v.(string)
			if !ok {
				return nil, malformed("value for key " + k + " must be a string")
			}
			out[k] = s
		}
		return out, nil
	}

	switch v := value.(type) {
	case string:
		if v == "" {
			return nil, malformed("must be a JSON object of string key/value pairs, not an empty string")
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(v), &raw); err != nil {
			return nil, malformed("must be a JSON object of string key/value pairs: " + err.Error())
		}
		return toStringMap(raw)
	case map[string]any:
		return toStringMap(v)
	default:
		return nil, malformed("must be a JSON object of string key/value pairs")
	}
}
