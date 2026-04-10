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

	// TenantID identifies the tenant. Defaults to DefaultTenantID when empty.
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
	// TenantID filters roles by tenant. Defaults to DefaultTenantID when empty.
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
