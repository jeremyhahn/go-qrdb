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
type User struct {
	ID        uint64   `json:"id"`
	Username  string   `json:"username"`
	TenantID  string   `json:"tenantID"`
	RoleIDs   []string `json:"roleIDs"`
	Enabled   bool     `json:"enabled"`
	System    bool     `json:"system"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
}

// CreateUserRequest holds parameters for creating a user.
type CreateUserRequest struct {
	Username string   `json:"username"`
	TenantID string   `json:"tenantID,omitempty"`
	RoleIDs  []string `json:"roleIDs,omitempty"`
}

// UpdateUserRequest holds parameters for updating a user.
type UpdateUserRequest struct {
	ID       uint64   `json:"id"`
	Username string   `json:"username,omitempty"`
	RoleIDs  []string `json:"roleIDs,omitempty"`
}

// ListUsersRequest holds parameters for listing users.
type ListUsersRequest struct {
	TenantID string `json:"tenantID,omitempty"`
	Page     int    `json:"page,omitempty"`
	PageSize int    `json:"pageSize,omitempty"`
}

// ListUsersResponse holds the paginated user list.
type ListUsersResponse struct {
	Users   []*User `json:"users"`
	Total   int     `json:"total"`
	HasMore bool    `json:"hasMore"`
}

// ---------------------------------------------------------------------------
// Role management types
// ---------------------------------------------------------------------------

// Role represents an RBAC role entity. Roles aggregate fine-grained
// permissions and can be assigned to users.
type Role struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	TenantID    string   `json:"tenantID"`
	Permissions []string `json:"permissions"`
	System      bool     `json:"system"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

// CreateRoleRequest holds parameters for creating a role.
type CreateRoleRequest struct {
	// ID is an optional pre-assigned deterministic identifier. When empty,
	// the server generates a random UUID, preserving legacy behavior.
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	TenantID    string   `json:"tenantID,omitempty"`
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
	TenantID string `json:"tenantID,omitempty"`
	Page     int    `json:"page,omitempty"`
	PageSize int    `json:"pageSize,omitempty"`
}

// ListRolesResponse holds the paginated role list.
type ListRolesResponse struct {
	Roles   []*Role `json:"roles"`
	Total   int     `json:"total"`
	HasMore bool    `json:"hasMore"`
}

// AssignRoleRequest holds parameters for assigning a role to a user.
type AssignRoleRequest struct {
	UserID uint64 `json:"userID"`
	RoleID string `json:"roleID"`
}

// UnassignRoleRequest holds parameters for unassigning a role from a user.
type UnassignRoleRequest struct {
	UserID uint64 `json:"userID"`
	RoleID string `json:"roleID"`
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
	TenantID string `json:"tenantID,omitempty"`
	Name     string `json:"name"`
}

// ResolvePermissionsRequest holds the role IDs whose effective permissions
// should be resolved.
type ResolvePermissionsRequest struct {
	RoleIDs []string `json:"roleIDs"`
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
