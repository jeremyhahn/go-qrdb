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

// Package role provides role domain types, a service interface, and a
// KVStore-backed store with secondary indexes for RBAC role management.
package role

import (
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// ---------------------------------------------------------------------------
// Canonical role name constants
// ---------------------------------------------------------------------------

// The five canonical FIPS 140-3 / PCI-DSS v4 system roles defined in
// docs/auth/roles.md. All five are undeletable system roles created
// deterministically on daemon initialization.

const (
	// RoleNameSO is the Security Officer role name. The SO manages module
	// initialization, PIN management, and CA operations. The SO is explicitly
	// denied all cryptographic operations (FIPS 140-3 AS10.03).
	RoleNameSO = "so"

	// RoleNameAdmin is the Admin role name. Admins manage key lifecycle and
	// tenant configuration. They cannot initialize the module or reset SO PINs.
	RoleNameAdmin = "admin"

	// RoleNameOperator is the Operator role name. Operators perform routine key
	// use (sign, encrypt, decrypt) and maintenance operations.
	RoleNameOperator = "operator"

	// RoleNameUser is the User role name. Users perform cryptographic operations
	// (sign, verify, encrypt, decrypt) using existing keys. No key management.
	RoleNameUser = "user"

	// RoleNameAuditor is the Auditor role name. Auditors have read-only access
	// to audit logs and system state (FIPS 140-3 AS10.10).
	RoleNameAuditor = "auditor"

	// RoleNameOwner is the Owner role name. Owner is the standard-posture
	// convenience superuser: it holds the full operational permission set
	// (permission.All()) so a single bootstrap identity can run the entire
	// database without juggling separate admin/operator/SO identities.
	//
	// Owner is NOT one of the five canonical FIPS 140-3 / PCI-DSS roles. It is
	// provisioned and assigned to the bootstrap admin ONLY under the standard
	// (non-strict) RBAC posture, and the RBAC authorizer treats any Owner
	// grant as inert whenever strict separation of duties is active (FIPS
	// 140-3 mode or auth.separation_of_duties=true). It therefore cannot
	// weaken the security model of a compliant deployment.
	RoleNameOwner = "owner"
)

// Role represents an RBAC role entity in the system. Roles aggregate
// fine-grained permissions and can be assigned to users.
type Role struct {
	// ID is the globally unique UUID for this role.
	ID string `json:"id"`

	// Name is the human-readable role name, unique within a tenant.
	Name string `json:"name"`

	// TenantID identifies the tenant this role belongs to.
	TenantID string `json:"tenantID"`

	// Permissions lists the fine-grained permissions granted by this role.
	Permissions []permission.Permission `json:"permissions"`

	// System marks built-in roles that cannot be deleted.
	System bool `json:"system"`

	// RetiredPermissions lists the members of Permissions that the build's
	// permission registry no longer defines. It is DERIVED on read from the
	// live registry and is NEVER persisted — every store write goes through
	// marshalRole, which clears it — so it reports drift between what this
	// tenant was seeded with and what this build grants, and nothing else.
	//
	// It exists so a role listing shows the drift BEFORE
	// ReconcileBuiltInRoles is run to repair it. See
	// Role.RetiredPermissionError for the typed form.
	RetiredPermissions []permission.Permission `json:"retiredPermissions,omitempty"`

	// CreatedAt records when the role was created (UTC).
	CreatedAt time.Time `json:"createdAt"`

	// UpdatedAt records the last modification time (UTC).
	UpdatedAt time.Time `json:"updatedAt"`
}

// CreateRoleRequest holds parameters for creating a role.
type CreateRoleRequest struct {
	// ID is an optional pre-assigned identifier. When non-empty, the role
	// store uses this value instead of generating a random UUID. This is
	// used by the bootstrap flow to assign deterministic IDs to built-in
	// roles so that multi-node clusters converge on identical role objects.
	ID string

	// Name is the desired role name. Must not be empty.
	Name string

	// TenantID identifies the tenant. Defaults to SystemTenantID when empty.
	TenantID string

	// Permissions are the permissions to assign to this role.
	Permissions []permission.Permission
}

// UpdateRoleRequest holds parameters for updating a role.
type UpdateRoleRequest struct {
	// ID is the role UUID to update. Required.
	ID string

	// Name replaces the current name when non-empty.
	Name string

	// Permissions replaces the current permission set when non-nil.
	Permissions []permission.Permission
}

// ListRolesRequest holds parameters for listing roles.
type ListRolesRequest struct {
	// TenantID filters roles by tenant. Defaults to SystemTenantID when empty.
	TenantID string

	// Page is the 1-based page number. Zero or negative defaults to 1.
	Page int

	// PageSize is the maximum number of roles per page. Zero or negative
	// defaults to defaultPageSize.
	PageSize int
}

// ListRolesResponse holds the paginated role list.
type ListRolesResponse struct {
	// Roles is the page of role records.
	Roles []*Role `json:"roles"`

	// Total is the total number of roles matching the query.
	Total int `json:"total"`

	// HasMore is true when additional pages exist.
	HasMore bool `json:"hasMore"`
}
