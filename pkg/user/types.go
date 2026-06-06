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

// Package user provides user domain types, a service interface, and a
// KVStore-backed store with secondary indexes. Authentication is
// exclusively via mTLS X.509 certificates (passwordless).
package user

import "time"

// User represents a passwordless user entity in the system.
// Authentication is exclusively via mTLS X.509 certificates.
// Certificate CN is implicitly set to Username, OU to TenantID.
type User struct {
	// ID is the globally unique Snowflake identifier for this user. It is
	// assigned by the store at creation time (server-side) and is never zero
	// for a persisted user; zero is the invalid/sentinel value.
	ID uint64 `json:"id"`

	// Username is the human-readable login name, unique within a tenant.
	Username string `json:"username"`

	// TenantID identifies the tenant this user belongs to.
	TenantID string `json:"tenantID"`

	// RoleIDs lists the roles assigned to this user.
	RoleIDs []string `json:"roleIDs"`

	// Enabled indicates whether the user can authenticate.
	Enabled bool `json:"enabled"`

	// System marks built-in users that cannot be deleted.
	System bool `json:"system"`

	// RoleBindings grants roles within a tenant scope (tenant-scoped RBAC). A
	// control-plane user may hold, e.g., a tenant-admin binding for another
	// tenant. Additive alongside RoleIDs (system-scoped roles).
	RoleBindings []RoleBinding `json:"roleBindings,omitempty"`

	// Attributes carries attribute-based access-control (ABAC) key/value pairs
	// evaluated by policy.
	Attributes map[string]string `json:"attributes,omitempty"`

	// CertBindings binds mTLS client certificates to this user by SPKI
	// thumbprint — qrdb-owned mTLS identity. See cert_binding.go.
	CertBindings []CertBinding `json:"certBindings,omitempty"`

	// Factors holds extensible auth-factor enrollments (webauthn/fido2/piv),
	// registered via pkg/authfactors. qrdb stores them opaquely; the registered
	// factor interprets Data.
	Factors []FactorEnrollment `json:"factors,omitempty"`

	// CreatedAt records when the user was created (UTC).
	CreatedAt time.Time `json:"createdAt"`

	// UpdatedAt records the last modification time (UTC).
	UpdatedAt time.Time `json:"updatedAt"`
}

// CreateUserRequest holds parameters for creating a user.
type CreateUserRequest struct {
	// Username is the desired login name. Must not be empty.
	Username string

	// TenantID identifies the tenant. Defaults to SystemTenantID when empty.
	TenantID string

	// RoleIDs are the initial roles to assign.
	RoleIDs []string
}

// UpdateUserRequest holds parameters for updating a user.
type UpdateUserRequest struct {
	// ID is the user ID to update. Required (must be non-zero).
	ID uint64

	// Username replaces the current username when non-empty.
	Username string

	// RoleIDs replaces the current role set when non-nil.
	RoleIDs []string

	// RoleBindings replaces the tenant-scoped role bindings when non-nil.
	RoleBindings []RoleBinding

	// Attributes replaces the ABAC attributes when non-nil.
	Attributes map[string]string

	// CertBindings replaces the mTLS cert bindings when non-nil; the store
	// maintains the SPKI-thumbprint secondary index accordingly.
	CertBindings []CertBinding

	// Factors replaces the auth-factor enrollments when non-nil.
	Factors []FactorEnrollment
}

// ListUsersRequest holds parameters for listing users.
type ListUsersRequest struct {
	// TenantID filters users by tenant. Defaults to SystemTenantID when empty.
	TenantID string

	// Page is the 1-based page number. Zero or negative defaults to 1.
	Page int

	// PageSize is the maximum number of users per page. Zero or negative
	// defaults to defaultPageSize.
	PageSize int
}

// ListUsersResponse holds the paginated user list.
type ListUsersResponse struct {
	// Users is the page of user records.
	Users []*User `json:"users"`

	// Total is the total number of users matching the query.
	Total int `json:"total"`

	// HasMore is true when additional pages exist.
	HasMore bool `json:"hasMore"`
}
