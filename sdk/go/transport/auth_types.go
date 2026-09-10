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
	Name     string   `json:"name,omitempty"`
	TenantID string   `json:"tenant_id"`
	RoleIDs  []string `json:"role_ids"`
	Enabled  bool     `json:"enabled"`
	System   bool     `json:"system"`
	// Lifecycle is the canonical account state: pending, active, suspended,
	// locked, disabled or deleting. Enabled is a derived projection of it
	// (exactly lifecycle == "active") and collapses every non-active state into
	// one false, so read this when the distinction matters.
	Lifecycle string `json:"lifecycle,omitempty"`
	// GroupIDs are opaque group-membership identifiers. qrdb stores the
	// membership edge and owns no Group entity.
	GroupIDs []string `json:"group_ids,omitempty"`
	// AuthMethods are the permitted authentication methods ("webauthn",
	// "piv"). A policy statement, distinct from enrolled factors.
	AuthMethods []string `json:"auth_methods,omitempty"`
	// LastAuth is the last successful authentication, RFC 3339. Empty when the
	// account has never authenticated.
	LastAuth  string `json:"last_auth,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
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
	// GroupIDs are the initial opaque group memberships.
	GroupIDs []string `json:"group_ids,omitempty"`
	// AuthMethods are the initial permitted authentication methods. Empty
	// states no restriction.
	AuthMethods []string `json:"auth_methods,omitempty"`
	// Lifecycle is the state to create in — pending, active or disabled only.
	// Empty defaults to active, or pending when Pending is set.
	Lifecycle string `json:"lifecycle,omitempty"`
	// Pending is the shorthand for lifecycle "pending". Setting both is
	// refused unless they agree.
	Pending bool `json:"pending,omitempty"`
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
	// GroupIDs and AuthMethods replace their sets when NON-NIL. Nil leaves them
	// alone; an empty non-nil slice clears them, which is how a membership or a
	// method restriction is revoked.
	//
	// NO `omitempty` on these two, deliberately. omitempty drops an empty slice
	// from the encoded body, which would make "clear" indistinguishable from
	// "leave alone" on every JSON transport — a revocation that silently did
	// nothing. Nil encodes as `null` and decodes back to nil, so the
	// three-way distinction (absent / null / []) survives the wire intact.
	GroupIDs    []string `json:"group_ids"`
	AuthMethods []string `json:"auth_methods"`
	// Lifecycle transitions the account when non-empty. Checked against the
	// transition table; nothing moves out of "deleting".
	Lifecycle string `json:"lifecycle,omitempty"`
	// LastAuth raises the last-authentication high-water mark when non-empty.
	// RFC 3339. A value at or before the stored one writes nothing.
	LastAuth string `json:"last_auth,omitempty"`
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

	// RetiredPermissions lists the members of Permissions that the server's
	// build no longer defines. It is derived on read from the live permission
	// registry and never stored, so it reports drift between what the tenant
	// was seeded with and what the server now grants — visible on an ordinary
	// listing, BEFORE ReconcileBuiltInRoles is run to repair it.
	RetiredPermissions []string `json:"retired_permissions,omitempty"`
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

// ReconcileRolesRequest names the tenant whose built-in roles are to be
// converged onto the server build's definitions.
type ReconcileRolesRequest struct {
	// TenantID is the tenant to reconcile. Empty means the caller's own
	// effective tenant.
	TenantID string `json:"tenant_id,omitempty"`
}

// RoleReconcileResult records what reconciliation changed for one built-in
// role. Only changed roles are reported.
type RoleReconcileResult struct {
	RoleID string `json:"role_id"`
	Name   string `json:"name"`

	// Created is true when the built-in was absent from the tenant and was
	// seeded. Added then holds its whole permission set.
	Created bool `json:"created"`

	// Added lists the permissions the build grants that the stored role did
	// not carry. Sorted.
	Added []string `json:"added,omitempty"`

	// Removed lists the permissions the stored role carried that the build no
	// longer grants. Sorted.
	Removed []string `json:"removed,omitempty"`
}

// ReconcileRolesResponse is the per-tenant reconciliation report. An empty
// Roles slice means the tenant already agreed with the build and nothing was
// written.
type ReconcileRolesResponse struct {
	TenantID string                `json:"tenant_id"`
	Roles    []RoleReconcileResult `json:"roles,omitempty"`
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

// RevokeResult reports what RevokeCert did, distinguishing a revocation of
// a certificate the target node holds a record for from a deny-by-serial
// revocation of one it does not -- both are successes, so the distinction
// is carried here rather than in the error return. It mirrors
// cert.RevokeResult at the wire boundary.
type RevokeResult struct {
	Serial      string `json:"serial"`
	RecordFound bool   `json:"recordFound"`
}
