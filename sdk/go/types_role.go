// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// User IAM request/error types
//
// Note: User and related binding types are re-exported in types_user.go.
// ---------------------------------------------------------------------------

// CreateUserRequest carries the inputs for creating a user.
type CreateUserRequest = user.CreateUserRequest

// UpdateUserRequest carries the inputs for a partial user update.
type UpdateUserRequest = user.UpdateUserRequest

// ListUsersRequest carries the inputs for a paginated user listing.
type ListUsersRequest = user.ListUsersRequest

// ListUsersResponse is the paginated user listing result.
type ListUsersResponse = user.ListUsersResponse

// UsernameExistsError is returned when creating a user whose username already
// exists.
type UsernameExistsError = user.UsernameExistsError

// UserNotFoundError is returned when a user lookup (by id, username, or cert
// thumbprint) does not resolve.
type UserNotFoundError = user.UserNotFoundError

// ---------------------------------------------------------------------------
// Role IAM types
// ---------------------------------------------------------------------------

// Role is a named collection of permissions.
type Role = role.Role

// CreateRoleRequest carries the inputs for creating a role.
type CreateRoleRequest = role.CreateRoleRequest

// RoleNameExistsError is returned when creating a role whose name already
// exists.
type RoleNameExistsError = role.RoleNameExistsError

// RoleNotFoundError is returned when a role ID does not exist.
type RoleNotFoundError = role.RoleNotFoundError

// RoleValidationError is returned when a role request fails validation
// (e.g. an empty name or malformed field).
type RoleValidationError = role.ValidationError

// RoleInvalidPermissionError is returned when a role create/update carries a
// permission string that is neither registered in the global permission
// registry nor a whole-segment wildcard (e.g. "keys:*").
type RoleInvalidPermissionError = role.InvalidPermissionError

// Canonical built-in role names. These are the role names a token's `roles`
// claim carries and that the PDP resolves to permissions. Consumers (e.g. the
// quicbaas broker) reconcile their admin identity against these instead of
// minting parallel role names.
const (
	// RoleNameSO is the Security Officer role name.
	RoleNameSO = role.RoleNameSO
	// RoleNameAdmin is the Administrator role name.
	RoleNameAdmin = role.RoleNameAdmin
	// RoleNameOperator is the Operator role name.
	RoleNameOperator = role.RoleNameOperator
	// RoleNameUser is the end-user role name.
	RoleNameUser = role.RoleNameUser
	// RoleNameAuditor is the read-only Auditor role name.
	RoleNameAuditor = role.RoleNameAuditor
	// RoleNameCustodian is the Shamir share-holder role name.
	RoleNameCustodian = role.RoleNameCustodian
	// RoleNameOwner is the standard-posture superuser role name.
	RoleNameOwner = role.RoleNameOwner
)

// BuiltInRoles returns the canonical system roles, with their canonical
// permission sets, for the given tenant. It is the seam a consumer uses to see
// exactly what a role grant confers — for example, to prove in a test that a
// surface it hosts is reachable by qrdb's "admin" role and by no lesser role.
// An empty tenantID defaults to the system tenant.
var BuiltInRoles = role.BuiltInRoles

// SystemRoleID returns the deterministic, cluster-stable ID of a named
// built-in role in the given tenant. An empty tenantID defaults to the system
// tenant. This is the seam consumers use to address qrdb's canonical roles by
// a stable identifier (e.g. binding the bootstrap admin to Admin@system).
func SystemRoleID(tenantID, roleName string) string {
	return role.SystemRoleID(tenantID, roleName)
}
