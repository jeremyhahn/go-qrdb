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
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
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

// EmailExistsError is returned when creating or updating a user whose email
// login key is already taken within the tenant.
type EmailExistsError = user.EmailExistsError

// UserNotFoundError is returned when a user lookup (by id, username, email, or
// cert thumbprint) does not resolve.
//
// It is the ONE not-found type for users: every transport returns it, and so
// does UserServicer — pkg/user.UserNotFoundError is an alias of the same
// transport type (go-qrdb#343). A consumer classifies a missing user with a
// single errors.As against this name whether it called a servicer in-process
// or a client over REST, gRPC, QUIC, MCP, Unix or the embedded transport.
// Before go-qrdb#289 this aliased pkg/user's own type, which no remote
// transport returned; after #289 it aliased the transport's, which the
// servicer did not return. Now there is one type, and both return it.
type UserNotFoundError = transport.UserNotFoundError

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

// RoleIDExistsError is returned when creating a role with a caller-supplied
// ID that is already in use.
type RoleIDExistsError = role.RoleIDExistsError

// RoleNotFoundError is returned when a role ID does not exist.
type RoleNotFoundError = role.RoleNotFoundError

// RoleValidationError is returned when a role request fails validation
// (e.g. an empty name or malformed field).
type RoleValidationError = role.ValidationError

// RoleInvalidPermissionError is returned when a role create/update carries a
// permission string that is neither registered in the global permission
// registry nor a whole-segment wildcard (e.g. "keys:*").
type RoleInvalidPermissionError = role.InvalidPermissionError

// ReservedRoleNameError is returned when a create would take a system role's
// name without being the canonical role that name denotes — a wrong id, or a
// permission set other than the constructor's.
//
// Consumers need to tell this apart from RoleNameExistsError: that one means
// "something already holds this name", while this one refuses the name whether
// or not anything holds it. A consumer provisioning its own roles gets a
// different remedy from each — pick another name, versus stop trying to mint a
// built-in.
type ReservedRoleNameError = role.ReservedRoleNameError

// SystemRoleImmutableError is returned when an operation would alter a system
// role: deleting it, renaming it, or replacing its permission set. Op names the
// refused operation ("deleted", "renamed", "re-permissioned").
//
// Consumers could not previously receive this: every seeded built-in was
// persisted with System: false, so the guards that return it never fired
// (go-qrdb#17). It is re-exported in the change that makes it reachable,
// because a consumer that deletes roles now has an outcome to distinguish —
// "this role is protected" is not the same answer as "this role is missing"
// or "your write failed", and only the typed error separates them.
type SystemRoleImmutableError = role.SystemRoleImmutableError

// ReconcileReport is the outcome of a built-in role reconciliation for one
// tenant: one entry per built-in role that CHANGED, so an empty report means
// the tenant already agreed with the build and nothing was written.
type ReconcileReport = role.ReconcileReport

// RoleReconcileResult records what reconciliation changed for one built-in
// role — created, permissions added, permissions removed.
type RoleReconcileResult = role.RoleReconcileResult

// RetiredPermissionError reports that a stored role carries permissions the
// build's permission registry no longer defines.
//
// It is a SIGNAL rather than a failure: no read path returns it, because a
// listing that failed on drift would hide exactly the roles an operator needs
// to see in order to repair them. Role.RetiredPermissionError produces it on
// demand from a role read through any transport, so a consumer classifies the
// drift with one errors.As instead of re-deriving the comparison itself.
type RetiredPermissionError = role.RetiredPermissionError

// RetiredPermissions returns the members of perms the global permission
// registry no longer defines, sorted. Whole-segment wildcards are never
// retired — the RBAC authorizer expands them at authorization time, so there
// is no registry entry for one to lose.
var RetiredPermissions = role.RetiredPermissions

// IsSystemRoleName reports whether a role name is one of the canonical system
// role names — the six FIPS 140-3 / PCI-DSS roles or Owner.
//
// It is the same predicate the role store uses to decide the System flag, so a
// consumer can tell BEFORE issuing a create, rename, or delete whether the
// name it is about to use is reserved, instead of discovering it from a
// refusal. The names are reserved in EVERY tenant, not only the system tenant.
var IsSystemRoleName = role.IsSystemRoleName

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
//
// It derives the identifier rather than reading it, which is correct for a
// comparison against a freshly seeded cluster but NOT for writing a grant or
// for deciding whether an existing grant denotes the role. System role IDs
// moved from UUIDv5 (SHA-1) to UUIDv8 (SHA-256) for FIPS 140-3, and a cluster
// seeded before that keeps its roles — and its users' RoleIDs — under the
// legacy identifiers. Use ResolveSystemRoleID or SystemRoleIDs, which answer
// from the role store, wherever an already-provisioned cluster must keep
// resolving. Evaluating this at package-init time into a package-level var is
// the shape that gets this wrong.
func SystemRoleID(tenantID, roleName string) string {
	return role.SystemRoleID(tenantID, roleName)
}

// RoleNameResolver is the narrow role-store surface the two functions below
// need: "which role is named N in tenant T". qrdb's own role service
// satisfies it, as does any consumer adapter over its own role store.
type RoleNameResolver = role.NameResolver

// ResolveSystemRoleID returns the identifier a named system role is actually
// persisted under in the given tenant, falling back to the derived one from
// SystemRoleID when the cluster holds no role of that name yet. Bind grants
// to this, never to the derived identifier: on a cluster seeded before system
// role IDs moved off UUIDv5/SHA-1, a derived identifier names no role at all
// and the grant silently confers nothing.
var ResolveSystemRoleID = role.ResolveSystemRoleID

// SystemRoleIDs returns every identifier that denotes a named system role in
// the given tenant — the derived one plus, on a cluster seeded before the
// UUIDv5 migration, the legacy one still persisted for it. An authorization
// rule that recognises a role by identifier in order to VOID a grant must
// match against the whole set; matching only the derived identifier fails
// open on a pre-migration grant.
var SystemRoleIDs = role.SystemRoleIDs
