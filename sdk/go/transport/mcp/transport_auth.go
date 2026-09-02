// Copyright 2026 Jeremy Hahn
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
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.UserClient = (*Transport)(nil)
	_ transport.RoleClient = (*Transport)(nil)
	_ transport.CertClient = (*Transport)(nil)
)

// ---------------------------------------------------------------------------
// Server-side JSON decode types for auth responses
// ---------------------------------------------------------------------------
//
// The server-side user and role types use json tags with camelCase, which
// match the SDK transport type tags. We can decode directly into SDK types
// for users. For roles, the server returns permission.Permission (a string
// type) as JSON strings, matching the SDK Role.Permissions []string.
//
// The cert package types have json tags, but CertPEM/KeyPEM are []byte on
// the server side (base64 when marshaled). The SDK uses string fields.
// The cert list handler returns []*cert.CertRecord which uses time.Time
// (RFC 3339 strings), while the SDK CertRecord uses string timestamps.

// serverUser mirrors user.User for JSON decoding. The server-side
// struct has json tags matching the SDK type. User's CreatedAt and
// UpdatedAt are time.Time (marshaled as RFC 3339 strings).
type serverUser struct {
	ID       uint64 `json:"id"`
	Username string `json:"username"`
	// Email is the canonical tenant-unique login key; Name is display only.
	// Both decode from the same JSON the user record marshals.
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	TenantID string   `json:"tenantID"`
	RoleIDs  []string `json:"roleIDs"`
	Enabled  bool     `json:"enabled"`
	System   bool     `json:"system"`
	// Lifecycle is the canonical account state (pending, active, suspended,
	// locked, disabled, deleting). Enabled is a derived projection of it.
	Lifecycle   string   `json:"lifecycle,omitempty"`
	GroupIDs    []string `json:"groupIDs,omitempty"`
	AuthMethods []string `json:"authMethods,omitempty"`
	LastAuth    string   `json:"lastAuth,omitempty"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

// serverListUsersResponse mirrors user.ListUsersResponse for JSON decoding.
type serverListUsersResponse struct {
	Users   []*serverUser `json:"users"`
	Total   int           `json:"total"`
	HasMore bool          `json:"hasMore"`
}

// serverRole mirrors role.Role for JSON decoding. The server uses
// permission.Permission (string typedef) for the Permissions field.
type serverRole struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	TenantID    string   `json:"tenantID"`
	Permissions []string `json:"permissions"`
	System      bool     `json:"system"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

// serverListRolesResponse mirrors role.ListRolesResponse for JSON decoding.
type serverListRolesResponse struct {
	Roles   []*serverRole `json:"roles"`
	Total   int           `json:"total"`
	HasMore bool          `json:"hasMore"`
}

// serverPermissionCategory mirrors permission.Category for JSON decoding.
// Category has no json tags, so Go's default PascalCase encoding is used.
type serverPermissionCategory struct {
	Name        string   `json:"Name"`
	Group       string   `json:"Group"`
	Permissions []string `json:"Permissions"`
}

// serverCertBundle mirrors cert.CertBundle for JSON decoding. The server
// uses []byte for CertPEM/KeyPEM which json.Marshal encodes as base64.
type serverCertBundle struct {
	CertPEM string `json:"CertPEM"`
	KeyPEM  string `json:"KeyPEM"`
	Serial  string `json:"Serial"`
}

// serverCertExportResponse mirrors the certExportResponse envelope used by
// the qrdb_cert_export tool handler.
type serverCertExportResponse struct {
	P12Base64 string `json:"p12_base64"`
}

// serverCertRecord mirrors cert.CertRecord for JSON decoding. The server
// uses time.Time for timestamp fields (marshaled as RFC 3339 strings).
type serverCertRecord struct {
	Serial    string `json:"serial"`
	Username  string `json:"username"`
	TenantID  string `json:"tenantID"`
	IssuedAt  string `json:"issuedAt"`
	ExpiresAt string `json:"expiresAt"`
	Revoked   bool   `json:"revoked"`
}

// ---------------------------------------------------------------------------
// Conversion helpers
// ---------------------------------------------------------------------------

// serverUserToTransport converts a server-side User to the SDK transport
// representation.
func serverUserToTransport(u *serverUser) *transport.User {
	return &transport.User{
		ID:          u.ID,
		Username:    u.Username,
		Email:       u.Email,
		Name:        u.Name,
		TenantID:    u.TenantID,
		RoleIDs:     u.RoleIDs,
		Enabled:     u.Enabled,
		System:      u.System,
		Lifecycle:   u.Lifecycle,
		GroupIDs:    u.GroupIDs,
		AuthMethods: u.AuthMethods,
		LastAuth:    u.LastAuth,
		CreatedAt:   u.CreatedAt,
		UpdatedAt:   u.UpdatedAt,
	}
}

// serverRoleToTransport converts a server-side Role to the SDK transport
// representation.
func serverRoleToTransport(r *serverRole) *transport.Role {
	return &transport.Role{
		ID:          r.ID,
		Name:        r.Name,
		TenantID:    r.TenantID,
		Permissions: r.Permissions,
		System:      r.System,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

// roleIDsToCSV converts a slice of role IDs to a comma-separated string.
// Returns an empty string when the slice is nil or empty.
func roleIDsToCSV(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return strings.Join(ids, ",")
}

// ---------------------------------------------------------------------------
// UserClient implementation
// ---------------------------------------------------------------------------

// CreateUser creates a new passwordless user. Calls the qrdb_user_create
// MCP tool.
func (t *Transport) CreateUser(ctx context.Context, req *transport.CreateUserRequest) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"username": req.Username,
	}
	if req.TenantID != "" {
		args["tenant_id"] = req.TenantID
	}
	// Email is the canonical login key: dropping it here would create a user
	// with no entry in the index logins resolve through, silently, while
	// reporting success.
	if req.Email != "" {
		args["email"] = req.Email
	}
	if req.Name != "" {
		args["name"] = req.Name
	}
	// The opt-out must travel too. Without it a caller asking for a
	// certificate-only identity sends no email AND no declaration, and the
	// server rejects it — breaking a supported create mode on this client only.
	//
	// MCP is the transport that keeps getting missed, and always for the same
	// reason: it builds an argument MAP while the others use struct literals, so
	// an edit shaped around struct fields passes over it silently and nothing
	// fails to compile. Second occurrence on this PR.
	if req.WithoutEmailLoginKey {
		args["without_email_login_key"] = true
	}
	if csv := roleIDsToCSV(req.RoleIDs); csv != "" {
		args["role_ids"] = csv
	}
	// The identity fields travel here too — the argument-map shape is exactly
	// why the comment above says this transport keeps getting missed.
	//
	// JSON, not CSV. A group id is an OPAQUE minted token, so joining on commas
	// is lossy for the values it carries: an id containing a comma arrives as
	// two memberships and a blank element is dropped where the store refuses
	// it, writing a different authorization graph than the caller asked for
	// while reporting success (Codex peer review, PR #198).
	if err := putJSONList(args, "group_ids", req.GroupIDs); err != nil {
		return nil, &transport.RequestError{Method: "CreateUser", Err: err}
	}
	if err := putJSONList(args, "auth_methods", req.AuthMethods); err != nil {
		return nil, &transport.RequestError{Method: "CreateUser", Err: err}
	}
	if req.Lifecycle != "" {
		args["lifecycle"] = req.Lifecycle
	}
	if req.Pending {
		args["pending"] = true
	}

	text, err := t.callTool(ctx, "qrdb_user_create", args)
	if err != nil {
		return nil, err
	}

	var raw serverUser
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CreateUser", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverUserToTransport(&raw), nil
}

// GetUser retrieves a user by its globally unique ID. Calls the
// qrdb_user_get MCP tool.
func (t *Transport) GetUser(ctx context.Context, id uint64) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_user_get", map[string]any{
		"id": strconv.FormatUint(id, 10),
	})
	if err != nil {
		if isNotFoundError(err) {
			return nil, &transport.KeyNotFoundError{Key: strconv.FormatUint(id, 10)}
		}
		return nil, err
	}

	var raw serverUser
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetUser", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverUserToTransport(&raw), nil
}

// GetUserByUsername retrieves a user by tenant-scoped username. Calls
// the qrdb_user_get_by_username MCP tool.
func (t *Transport) GetUserByUsername(ctx context.Context, tenantID, username string) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_user_get_by_username", map[string]any{
		"tenant_id": tenantID,
		"username":  username,
	})
	if err != nil {
		if isNotFoundError(err) {
			return nil, &transport.KeyNotFoundError{Key: username}
		}
		return nil, err
	}

	var raw serverUser
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetUserByUsername", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverUserToTransport(&raw), nil
}

// GetUserByEmail resolves the canonical login key through the MCP tool.
func (t *Transport) GetUserByEmail(ctx context.Context, tenantID, email string) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_user_get_by_email", map[string]any{
		"tenant_id": tenantID,
		"email":     email,
	})
	if err != nil {
		if isNotFoundError(err) {
			return nil, &transport.KeyNotFoundError{Key: email}
		}
		return nil, err
	}

	var raw serverUser
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetUserByEmail", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverUserToTransport(&raw), nil
}

// GetUserByCertThumbprint resolves a user by mTLS SPKI thumbprint. Calls
// the qrdb_user_get_by_cert_thumbprint MCP tool.
func (t *Transport) GetUserByCertThumbprint(ctx context.Context, thumbprint string) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_user_get_by_cert_thumbprint", map[string]any{
		"thumbprint": thumbprint,
	})
	if err != nil {
		if isNotFoundError(err) {
			return nil, &transport.KeyNotFoundError{Key: "cert_spki/" + thumbprint}
		}
		return nil, err
	}

	var raw serverUser
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetUserByCertThumbprint", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverUserToTransport(&raw), nil
}

// AddCertBinding binds an mTLS SPKI thumbprint to a user. Calls the
// qrdb_user_add_cert_binding MCP tool.
func (t *Transport) AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_user_add_cert_binding", map[string]any{
		"user_id":        strconv.FormatUint(userID, 10),
		"thumbprint":     thumbprint,
		"not_after_unix": notAfterUnix,
	})
	if err != nil {
		return nil, err
	}

	var raw serverUser
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "AddCertBinding", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverUserToTransport(&raw), nil
}

// UpdateUser applies partial updates to an existing user. Calls the
// qrdb_user_update MCP tool.
func (t *Transport) UpdateUser(ctx context.Context, req *transport.UpdateUserRequest) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"id": strconv.FormatUint(req.ID, 10),
	}
	if req.Username != "" {
		args["username"] = req.Username
	}
	if req.Email != "" {
		args["email"] = req.Email
	}
	if req.Name != "" {
		args["name"] = req.Name
	}
	if csv := roleIDsToCSV(req.RoleIDs); csv != "" {
		args["role_ids"] = csv
	}
	if req.Lifecycle != "" {
		args["lifecycle"] = req.Lifecycle
	}
	if req.LastAuth != "" {
		args["last_auth"] = req.LastAuth
	}
	// nil means "leave alone" and an empty non-nil slice means "clear". An
	// absent tool argument already says "leave alone", so an empty set becomes
	// the tool's explicit clear flag rather than an empty list the server would
	// have to disambiguate.
	//
	// The non-empty case travels as JSON for the reason given in CreateUser:
	// these are opaque identifiers and CSV is lossy for them.
	if req.GroupIDs != nil {
		if len(req.GroupIDs) == 0 {
			args["clear_group_ids"] = true
		} else if err := putJSONList(args, "group_ids", req.GroupIDs); err != nil {
			return nil, &transport.RequestError{Method: "UpdateUser", Err: err}
		}
	}
	if req.AuthMethods != nil {
		if len(req.AuthMethods) == 0 {
			args["clear_auth_methods"] = true
		} else if err := putJSONList(args, "auth_methods", req.AuthMethods); err != nil {
			return nil, &transport.RequestError{Method: "UpdateUser", Err: err}
		}
	}

	text, err := t.callTool(ctx, "qrdb_user_update", args)
	if err != nil {
		return nil, err
	}

	var raw serverUser
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "UpdateUser", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverUserToTransport(&raw), nil
}

// DeleteUser removes a non-system user by ID. Calls the qrdb_user_delete
// MCP tool.
func (t *Transport) DeleteUser(ctx context.Context, id uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_user_delete", map[string]any{
		"id": strconv.FormatUint(id, 10),
	})
	return err
}

// ListUsers returns a paginated list of users for the given tenant.
// Calls the qrdb_user_list MCP tool.
func (t *Transport) ListUsers(ctx context.Context, req *transport.ListUsersRequest) (*transport.ListUsersResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{}
	if req.TenantID != "" {
		args["tenant_id"] = req.TenantID
	}
	if req.Page > 0 {
		args["page"] = int64(req.Page)
	}
	if req.PageSize > 0 {
		args["page_size"] = int64(req.PageSize)
	}

	text, err := t.callTool(ctx, "qrdb_user_list", args)
	if err != nil {
		return nil, err
	}

	var raw serverListUsersResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ListUsers", Err: &jsonDecodeError{Err: jsonErr}}
	}

	users := make([]*transport.User, len(raw.Users))
	for i, u := range raw.Users {
		users[i] = serverUserToTransport(u)
	}

	return &transport.ListUsersResponse{
		Users:   users,
		Total:   raw.Total,
		HasMore: raw.HasMore,
	}, nil
}

// EnableUser activates a user account. Calls the qrdb_user_enable
// MCP tool.
func (t *Transport) EnableUser(ctx context.Context, id uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_user_enable", map[string]any{
		"id": strconv.FormatUint(id, 10),
	})
	return err
}

// DisableUser deactivates a user account. Calls the qrdb_user_disable
// MCP tool.
func (t *Transport) DisableUser(ctx context.Context, id uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_user_disable", map[string]any{
		"id": strconv.FormatUint(id, 10),
	})
	return err
}

// ---------------------------------------------------------------------------
// RoleClient implementation
// ---------------------------------------------------------------------------

// CreateRole creates a new RBAC role with the specified permissions.
// Calls the qrdb_role_create MCP tool. The permissions are sent as a
// comma-separated string of "resource:action" pairs.
func (t *Transport) CreateRole(ctx context.Context, req *transport.CreateRoleRequest) (*transport.Role, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"name":        req.Name,
		"permissions": strings.Join(req.Permissions, ","),
	}
	if req.TenantID != "" {
		args["tenant_id"] = req.TenantID
	}
	if req.ID != "" {
		args["id"] = req.ID
	}

	text, err := t.callTool(ctx, "qrdb_role_create", args)
	if err != nil {
		return nil, err
	}

	var raw serverRole
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CreateRole", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverRoleToTransport(&raw), nil
}

// GetRole retrieves a role by its globally unique ID. Calls the
// qrdb_role_get MCP tool.
func (t *Transport) GetRole(ctx context.Context, id string) (*transport.Role, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_role_get", map[string]any{
		"id": id,
	})
	if err != nil {
		if isNotFoundError(err) {
			return nil, &transport.KeyNotFoundError{Key: id}
		}
		return nil, err
	}

	var raw serverRole
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetRole", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverRoleToTransport(&raw), nil
}

// UpdateRole applies partial updates to an existing role. Calls the
// qrdb_role_update MCP tool.
func (t *Transport) UpdateRole(ctx context.Context, req *transport.UpdateRoleRequest) (*transport.Role, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"id": req.ID,
	}
	if req.Name != "" {
		args["name"] = req.Name
	}
	if len(req.Permissions) > 0 {
		args["permissions"] = strings.Join(req.Permissions, ",")
	}

	text, err := t.callTool(ctx, "qrdb_role_update", args)
	if err != nil {
		return nil, err
	}

	var raw serverRole
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "UpdateRole", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverRoleToTransport(&raw), nil
}

// DeleteRole removes a non-system role by ID. Calls the qrdb_role_delete
// MCP tool.
func (t *Transport) DeleteRole(ctx context.Context, id string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_role_delete", map[string]any{
		"id": id,
	})
	return err
}

// ListRoles returns a paginated list of roles for the given tenant.
// Calls the qrdb_role_list MCP tool.
func (t *Transport) ListRoles(ctx context.Context, req *transport.ListRolesRequest) (*transport.ListRolesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{}
	if req.TenantID != "" {
		args["tenant_id"] = req.TenantID
	}
	if req.Page > 0 {
		args["page"] = int64(req.Page)
	}
	if req.PageSize > 0 {
		args["page_size"] = int64(req.PageSize)
	}

	text, err := t.callTool(ctx, "qrdb_role_list", args)
	if err != nil {
		return nil, err
	}

	var raw serverListRolesResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ListRoles", Err: &jsonDecodeError{Err: jsonErr}}
	}

	roles := make([]*transport.Role, len(raw.Roles))
	for i, r := range raw.Roles {
		roles[i] = serverRoleToTransport(r)
	}

	return &transport.ListRolesResponse{
		Roles:   roles,
		Total:   raw.Total,
		HasMore: raw.HasMore,
	}, nil
}

// AssignRole assigns a role to a user. Calls the qrdb_role_assign
// MCP tool.
func (t *Transport) AssignRole(ctx context.Context, userID uint64, roleID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_role_assign", map[string]any{
		"user_id": strconv.FormatUint(userID, 10),
		"role_id": roleID,
	})
	return err
}

// UnassignRole removes a role assignment from a user. Calls the
// qrdb_role_unassign MCP tool.
func (t *Transport) UnassignRole(ctx context.Context, userID uint64, roleID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_role_unassign", map[string]any{
		"user_id": strconv.FormatUint(userID, 10),
		"role_id": roleID,
	})
	return err
}

// ListPermissions returns all available permission categories for UI
// rendering. Calls the qrdb_permissions_list MCP tool.
func (t *Transport) ListPermissions(ctx context.Context) (*transport.ListPermissionsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_permissions_list", nil)
	if err != nil {
		return nil, err
	}

	var raw []serverPermissionCategory
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ListPermissions", Err: &jsonDecodeError{Err: jsonErr}}
	}

	categories := make([]*transport.PermissionCategory, len(raw))
	for i, c := range raw {
		categories[i] = &transport.PermissionCategory{
			Name:        c.Name,
			Group:       c.Group,
			Permissions: c.Permissions,
		}
	}

	return &transport.ListPermissionsResponse{
		Categories: categories,
	}, nil
}

// ---------------------------------------------------------------------------
// CertClient implementation
// ---------------------------------------------------------------------------

// GenerateCert creates a client X.509 certificate signed by the internal
// CA for mTLS authentication. Calls the qrdb_cert_generate MCP tool.
func (t *Transport) GenerateCert(ctx context.Context, req *transport.GenerateCertRequest) (*transport.CertBundle, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"username": req.Username,
	}
	if req.TenantID != "" {
		args["tenant_id"] = req.TenantID
	}
	if req.TTL != "" {
		args["ttl"] = req.TTL
	}

	text, err := t.callTool(ctx, "qrdb_cert_generate", args)
	if err != nil {
		return nil, err
	}

	var raw serverCertBundle
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GenerateCert", Err: &jsonDecodeError{Err: jsonErr}}
	}

	certPEM, decErr := base64.StdEncoding.DecodeString(raw.CertPEM)
	if decErr != nil {
		certPEM = []byte(raw.CertPEM)
	}
	keyPEM, decErr := base64.StdEncoding.DecodeString(raw.KeyPEM)
	if decErr != nil {
		keyPEM = []byte(raw.KeyPEM)
	}

	return &transport.CertBundle{
		CertPEM: string(certPEM),
		KeyPEM:  string(keyPEM),
		Serial:  raw.Serial,
	}, nil
}

// ExportCert exports a certificate and key as a PKCS#12 archive for
// browser import. Calls the qrdb_cert_export MCP tool.
func (t *Transport) ExportCert(ctx context.Context, req *transport.ExportCertRequest) (*transport.ExportCertResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_cert_export", map[string]any{
		"cert_pem":   req.CertPEM,
		"key_pem":    req.KeyPEM,
		"passphrase": req.Passphrase,
	})
	if err != nil {
		return nil, err
	}

	var raw serverCertExportResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ExportCert", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.ExportCertResponse{
		PKCS12: raw.P12Base64,
	}, nil
}

// RevokeCert marks a certificate serial as revoked. Calls the
// qrdb_cert_revoke MCP tool.
func (t *Transport) RevokeCert(ctx context.Context, serial string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_cert_revoke", map[string]any{
		"serial": serial,
	})
	return err
}

// ListCerts lists all issued client certificates for a tenant. Calls
// the qrdb_cert_list MCP tool.
func (t *Transport) ListCerts(ctx context.Context, tenantID string) (*transport.ListCertsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{}
	if tenantID != "" {
		args["tenant_id"] = tenantID
	}

	text, err := t.callTool(ctx, "qrdb_cert_list", args)
	if err != nil {
		return nil, err
	}

	var raw []serverCertRecord
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ListCerts", Err: &jsonDecodeError{Err: jsonErr}}
	}

	certs := make([]*transport.CertRecord, len(raw))
	for i, r := range raw {
		certs[i] = &transport.CertRecord{
			Serial:    r.Serial,
			Username:  r.Username,
			TenantID:  r.TenantID,
			IssuedAt:  r.IssuedAt,
			ExpiresAt: r.ExpiresAt,
			Revoked:   r.Revoked,
		}
	}

	return &transport.ListCertsResponse{
		Certs: certs,
	}, nil
}

// putJSONList encodes a list of opaque identifiers into args as a JSON array
// string, the array-argument convention this transport uses. An empty or nil
// list writes nothing: an absent argument is what says "leave alone".
//
// JSON rather than a delimiter-joined string because the elements are OPAQUE.
// Any separator is a character the value may legitimately contain, and a
// separator-based encoding turns that into a silently different list — which,
// for an authorization list, is a success that granted or revoked something
// nobody asked for.
func putJSONList(args map[string]any, key string, values []string) error {
	if len(values) == 0 {
		return nil
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return &jsonEncodeError{Err: err}
	}
	args[key] = string(encoded)
	return nil
}
