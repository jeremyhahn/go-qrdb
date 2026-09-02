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

package transport

// ---------------------------------------------------------------------------
// User management types
// ---------------------------------------------------------------------------

// User represents a passwordless user entity. Authentication is exclusively
// via mTLS X.509 certificates.
//
// The ID is a uint64 Snowflake that exceeds JavaScript's
// Number.MAX_SAFE_INTEGER, so it crosses the JSON wire as a string
// (proto3 JSON convention) to avoid precision loss in JS clients. The
// json tags mirror the REST server's snake_case wire contract.
type User struct {
	ID       uint64 `json:"id,string"`
	Username string `json:"username"`
	// Email is the canonical tenant-unique login key. Every layer above qrdb
	// keys a user by this — WebAuthn credential handle, session subject, CLI
	// identifier. Empty only for system users.
	Email string `json:"email,omitempty"`
	// Name is the display name. Presentation only; never a lookup key.
	Name      string   `json:"name,omitempty"`
	TenantID  string   `json:"tenant_id"`
	RoleIDs   []string `json:"role_ids"`
	Enabled   bool     `json:"enabled"`
	System    bool     `json:"system"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

// CreateUserRequest holds parameters for creating a user.
type CreateUserRequest struct {
	Username string `json:"username"`
	// Email is the canonical tenant-unique login key. Empty is permitted only
	// for system users, which authenticate by other means.
	Email string `json:"email,omitempty"`
	// Name is the display name. Presentation only.
	Name string `json:"name,omitempty"`
	// WithoutEmailLoginKey creates a user with no email login key (a
	// certificate-only or bootstrap identity). Required to omit Email.
	WithoutEmailLoginKey bool     `json:"without_email_login_key,omitempty"`
	TenantID             string   `json:"tenant_id,omitempty"`
	RoleIDs              []string `json:"role_ids,omitempty"`
}

// UpdateUserRequest holds parameters for updating a user.
type UpdateUserRequest struct {
	ID       uint64 `json:"id,string"`
	Username string `json:"username,omitempty"`
	// Email replaces the login key when non-empty; uniqueness is re-validated
	// within the tenant and the secondary index moves with it.
	Email string `json:"email,omitempty"`
	// Name replaces the display name when non-empty.
	Name    string   `json:"name,omitempty"`
	RoleIDs []string `json:"role_ids,omitempty"`
}

// ListUsersRequest holds parameters for listing users.
type ListUsersRequest struct {
	TenantID string `json:"tenant_id,omitempty"`
	Page     int    `json:"page,omitempty"`
	PageSize int    `json:"page_size,omitempty"`
}

// ListUsersResponse holds the paginated user list.
type ListUsersResponse struct {
	Users   []*User `json:"users"`
	Total   int     `json:"total"`
	HasMore bool    `json:"has_more"`
}

// ---------------------------------------------------------------------------
// Role management types
// ---------------------------------------------------------------------------

// Role represents an RBAC role entity. Roles aggregate fine-grained
// permissions and can be assigned to users.
type Role struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	TenantID    string   `json:"tenant_id"`
	Permissions []string `json:"permissions"`
	System      bool     `json:"system"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

// CreateRoleRequest holds parameters for creating a role.
type CreateRoleRequest struct {
	// ID is an optional pre-assigned deterministic identifier. When empty,
	// the server generates a random UUID, preserving legacy behavior.
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	TenantID    string   `json:"tenant_id,omitempty"`
	Permissions []string `json:"permissions"`
}

// UpdateRoleRequest holds parameters for updating a role.
type UpdateRoleRequest struct {
	ID          string   `json:"id"`
	Name        string   `json:"name,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
}

// ListRolesRequest holds parameters for listing roles.
type ListRolesRequest struct {
	TenantID string `json:"tenant_id,omitempty"`
	Page     int    `json:"page,omitempty"`
	PageSize int    `json:"page_size,omitempty"`
}

// ListRolesResponse holds the paginated role list.
type ListRolesResponse struct {
	Roles   []*Role `json:"roles"`
	Total   int     `json:"total"`
	HasMore bool    `json:"has_more"`
}

// AssignRoleRequest holds parameters for assigning a role to a user.
//
// The user ID crosses the JSON wire as a string (proto3 JSON convention)
// because uint64 Snowflakes exceed JavaScript's Number.MAX_SAFE_INTEGER.
type AssignRoleRequest struct {
	UserID uint64 `json:"user_id,string"`
	RoleID string `json:"role_id"`
}

// UnassignRoleRequest holds parameters for unassigning a role from a user.
type UnassignRoleRequest struct {
	UserID uint64 `json:"user_id,string"`
	RoleID string `json:"role_id"`
}

// PermissionCategory groups related permissions under a display name and
// logical group for UI rendering.
type PermissionCategory struct {
	Name        string   `json:"name"`
	Group       string   `json:"group"`
	Permissions []string `json:"permissions"`
}

// ListPermissionsResponse holds all permission categories.
type ListPermissionsResponse struct {
	Categories []*PermissionCategory `json:"categories"`
}

// GetRoleByNameRequest holds parameters for retrieving a role by its
// tenant-scoped name.
type GetRoleByNameRequest struct {
	TenantID string `json:"tenant_id,omitempty"`
	Name     string `json:"name"`
}

// ResolvePermissionsRequest holds the role IDs whose effective permissions
// should be resolved.
type ResolvePermissionsRequest struct {
	RoleIDs []string `json:"role_ids"`
}

// ResolvePermissionsResponse holds the resolved union of effective
// permissions for the requested roles.
type ResolvePermissionsResponse struct {
	Permissions []string `json:"permissions"`
}

// ---------------------------------------------------------------------------
// Certificate management types
// ---------------------------------------------------------------------------

// GenerateCertRequest holds parameters for generating a client certificate.
type GenerateCertRequest struct {
	Username string `json:"username"`
	TenantID string `json:"tenantID,omitempty"`
	TTL      string `json:"ttl,omitempty"`
}

// CertBundle holds the PEM-encoded certificate and private key returned
// after generating a client certificate.
type CertBundle struct {
	CertPEM string `json:"certPEM"`
	KeyPEM  string `json:"keyPEM"`
	Serial  string `json:"serial"`
}

// ExportCertRequest holds parameters for exporting a certificate as PKCS#12.
type ExportCertRequest struct {
	CertPEM    string `json:"certPEM"`
	KeyPEM     string `json:"keyPEM"`
	Passphrase string `json:"passphrase"`
}

// ExportCertResponse holds the base64-encoded PKCS#12 archive.
type ExportCertResponse struct {
	PKCS12 string `json:"pkcs12"`
}

// CertRecord is the metadata for an issued certificate.
type CertRecord struct {
	Serial    string `json:"serial"`
	Username  string `json:"username"`
	TenantID  string `json:"tenantID"`
	IssuedAt  string `json:"issuedAt"`
	ExpiresAt string `json:"expiresAt"`
	Revoked   bool   `json:"revoked"`
}

// ListCertsResponse holds a list of issued certificate records.
type ListCertsResponse struct {
	Certs []*CertRecord `json:"certs"`
}
