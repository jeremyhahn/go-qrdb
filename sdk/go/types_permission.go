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
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// ---------------------------------------------------------------------------
// Permission types
// ---------------------------------------------------------------------------

// Permission represents a fine-grained access control permission in
// "resource:action" format (e.g., "kv:put", "admin:health").
type Permission = permission.Permission

// Category groups related permissions under a display name for UI rendering.
type PermissionCategory = permission.Category

// ---------------------------------------------------------------------------
// Permission error types
// ---------------------------------------------------------------------------

// InvalidPermissionError is returned when a permission string does not
// conform to the required "resource:action" format.
type InvalidPermissionError = permission.InvalidPermissionError

// UnregisteredPermissionError is returned when an operation references a
// permission that has not been registered in the global registry.
type UnregisteredPermissionError = permission.UnregisteredPermissionError

// PermissionSoDViolationError is the panic value raised by
// RegisterPermissionRoleGrant when a caller attempts to grant a permission that
// performs a cryptographic operation with key material to the Security Officer
// role, violating the FIPS 140-3 AS10.03 separation-of-duties invariant.
type PermissionSoDViolationError = permission.SoDViolationError

// PermissionUndeclaredDutyError is the panic value raised by
// RegisterPermissionRoleGrant when a caller attempts to grant the Security
// Officer role a permission whose separated duty was never declared (see
// RegisterPermissionDuty). It is the fail-safe half of the AS10.03 guard:
// silence is a denial, so a contributed permission cannot reach the Security
// Officer by nobody having classified it.
type PermissionUndeclaredDutyError = permission.UndeclaredDutyError

// PermissionWildcardGrantError is the panic value raised by
// RegisterPermissionRoleGrant when a caller attempts to grant the Security
// Officer role a whole-segment wildcard (e.g. "keys:*"), which cannot be
// classified and would expand onto cryptographic operations at authorization
// time.
type PermissionWildcardGrantError = permission.WildcardGrantError

// PermissionInvalidDutyError is the panic value raised by
// RegisterPermissionDuty when the supplied duty is not one a caller may declare.
type PermissionInvalidDutyError = permission.InvalidDutyError

// PermissionDutyConflictError is the panic value raised by
// RegisterPermissionDuty when a permission that already has a declared duty is
// re-declared with a different one. Declarations are immutable.
type PermissionDutyConflictError = permission.DutyConflictError

// ---------------------------------------------------------------------------
// Permission functions
// ---------------------------------------------------------------------------

// ParsePermission validates a raw string and returns the corresponding
// Permission.
var ParsePermission = permission.Parse

// RegisterPermissions adds one or more permissions to the global registry.
var RegisterPermissions = permission.Register

// AllPermissions returns every registered permission as a sorted slice.
var AllPermissions = permission.All

// IsPermissionRegistered reports whether p exists in the global registry.
var IsPermissionRegistered = permission.IsRegistered

// PermissionsByResource returns all registered permissions whose resource
// component matches the given name.
var PermissionsByResource = permission.ByResource

// PermissionCategories returns all permission categories for UI rendering.
var PermissionCategories = permission.Categories

// PermissionResource returns the resource component of a permission string.
var PermissionResource = permission.Resource

// PermissionIdentityGated is the sentinel permission for operations whose
// authorization is deferred to the handler: the caller must be
// authenticated, but no RBAC grant is evaluated at the gate. It is
// deliberately not registered in the permission registry.
const PermissionIdentityGated = permission.IdentityGated

// PermissionAction returns the action component of a permission string.
var PermissionAction = permission.Action

// ---------------------------------------------------------------------------
// Cluster membership permissions
//
// Raft membership mutation is privileged wherever it is exposed. These are the
// canonical permissions qrdb already enforces on its own AddMember/RemoveMember
// control-plane RPCs across every transport, re-exported so a consumer that
// puts its own API in front of an embedded node (go-xkms among them) gates the
// same operation with the same permission instead of minting a parallel one.
// ---------------------------------------------------------------------------

// PermissionAdminAddMember ("admin:add_member") authorizes adding a replica to
// a shard's Raft group.
const PermissionAdminAddMember = permission.AdminAddMember

// PermissionAdminRemoveMember ("admin:remove_member") authorizes removing a
// replica from a shard's Raft group.
const PermissionAdminRemoveMember = permission.AdminRemoveMember

// PermissionAdminStatus ("admin:status") authorizes reading cluster status
// (leadership, shard identity). It is the read counterpart to the membership
// mutations above: a consumer fronting an embedded node should gate its own
// "cluster status" endpoint with this rather than a generic read permission,
// so the grant that covers qrdb's own status endpoint covers theirs too.
const PermissionAdminStatus = permission.AdminStatus

// PermissionAdminPeers ("admin:peers") authorizes listing the peers/replicas
// of a shard. Re-exported for the same reason as PermissionAdminStatus.
const PermissionAdminPeers = permission.AdminPeers

// ---------------------------------------------------------------------------
// Permission scope types and registration
//
// RegisterScope (and the Scope constants below) close a re-export gap: a
// higher layer contributing permissions to qrdb's registry (go-quicbaas,
// go-xkms) previously had no way to tag a contributed permission
// ScopeCross or ScopeTenantOnly — every consumer-registered permission was
// implicitly ScopeSelf, the only reach reachable from outside qrdb. See
// pkg/permission/scope.go for the full scope semantics.
// ---------------------------------------------------------------------------

// PermissionScope aliases the tenancy-reach classification for a
// permission: ScopeSelf, ScopeCross, or ScopeTenantOnly.
type PermissionScope = permission.Scope

const (
	// PermissionScopeSelf is the default — the permission acts on the
	// identity's own tenant, lifted across tenants for a system-tenant
	// identity by the system-tenant rule.
	PermissionScopeSelf = permission.ScopeSelf

	// PermissionScopeCross is a cross-tenant permission, exercisable only
	// by identities in the system tenant.
	PermissionScopeCross = permission.ScopeCross

	// PermissionScopeTenantOnly is a strictly tenant-local permission,
	// denied to the system tenant and never cross-tenant.
	PermissionScopeTenantOnly = permission.ScopeTenantOnly
)

// RegisterPermissionScope tags one or more built-in permissions with the
// given scope in the process-global default layer. A higher layer
// contributing a permission to qrdb's registry (see RegisterPermissions)
// calls this to declare the permission's tenancy reach; omitting the call
// leaves the permission at the default PermissionScopeSelf.
var RegisterPermissionScope = permission.RegisterScope

// ---------------------------------------------------------------------------
// Permission duty classification (FIPS 140-3 AS10.03)
//
// A permission's duty is the metadata the Security-Officer separation-of-duties
// guard decides on: governance may be held by the Security Officer,
// cryptographic use may never be. Unlike scope, the duty has no permissive
// default — an undeclared permission is not Security-Officer-grantable — so a
// higher layer contributing a permission declares its duty only when the
// Security Officer is meant to hold it (PermissionDutyGovernance) or when the
// permission performs a cryptographic primitive that must stay barred
// (PermissionDutyCryptoUse). See pkg/permission/duty.go for the full semantics.
// ---------------------------------------------------------------------------

// PermissionDuty aliases the separated-duty classification for a permission:
// PermissionDutyUndeclared, PermissionDutyGovernance, or
// PermissionDutyCryptoUse.
type PermissionDuty = permission.Duty

const (
	// PermissionDutyUndeclared is the zero value and the fail-safe state: the
	// permission's author has not stated which separated duty it belongs to,
	// and it is therefore not grantable to the Security Officer role. Most
	// permissions are legitimately undeclared.
	PermissionDutyUndeclared = permission.DutyUndeclared

	// PermissionDutyGovernance is module and security governance — the
	// Security Officer's own duties, and the only classification
	// RegisterPermissionRoleGrant accepts for a Security Officer grant.
	PermissionDutyGovernance = permission.DutyGovernance

	// PermissionDutyCryptoUse is the performance of a cryptographic primitive
	// with key material the module holds. FIPS 140-3 AS10.03 bars the Security
	// Officer from these, at registration time and again at authorization
	// time.
	PermissionDutyCryptoUse = permission.DutyCryptoUse
)

// RegisterPermissionDuty declares the separated duty of one or more
// contributed permissions. It is the third of the three declaration-time
// extension points (alongside RegisterPermissions and
// RegisterPermissionScope) and is what makes a contributed permission
// grantable to the Security Officer role.
//
// Declarations are immutable: re-declaring the same duty is a no-op, but a
// conflicting re-declaration panics with PermissionDutyConflictError so a
// cryptographic permission cannot be relabelled as governance. An
// undeclarable duty panics with PermissionInvalidDutyError.
var RegisterPermissionDuty = permission.RegisterDuty

// LookupPermissionDuty returns the declared duty for p, or
// PermissionDutyUndeclared when its author declared none.
var LookupPermissionDuty = permission.LookupDuty

// IsPermissionCryptoUse reports whether p performs a cryptographic primitive
// with key material and is therefore barred from the Security Officer role. It
// is true for a permission declared PermissionDutyCryptoUse and for any
// permission whose action names an unambiguous cryptographic primitive, in any
// resource family — including one a higher layer contributed.
var IsPermissionCryptoUse = permission.IsCryptoUse

// ---------------------------------------------------------------------------
// Built-in role default-grant registration
//
// A permission that is registered (RegisterPermissions) but never given a
// default role grant is reachable only through a hand-authored custom
// role. RegisterPermissionRoleGrant lets a higher layer place its
// contributed permissions into one of qrdb's five registry-derived
// built-in roles (Security Officer, Admin, Operator, User, Auditor) —
// see pkg/role.BuiltInRoles, which materializes exactly this registry.
// ---------------------------------------------------------------------------

// PermissionRoleSO is the Security Officer built-in role name.
// RegisterPermissionRoleGrant admits ONLY permissions declared
// PermissionDutyGovernance onto this role — FIPS 140-3 AS10.03 is enforced
// structurally at registration time, not only by the RBAC deny-list. A
// cryptographic permission, an unclassified one, and a wildcard all panic; see
// RegisterPermissionDuty.
const PermissionRoleSO = permission.RoleSO

// PermissionRoleAdmin is the Admin built-in role name.
const PermissionRoleAdmin = permission.RoleAdmin

// PermissionRoleOperator is the Operator built-in role name.
const PermissionRoleOperator = permission.RoleOperator

// PermissionRoleUser is the User built-in role name.
const PermissionRoleUser = permission.RoleUser

// PermissionRoleAuditor is the Auditor built-in role name.
const PermissionRoleAuditor = permission.RoleAuditor

// RegisterPermissionRoleGrant records that each of perms is granted by
// default to the named built-in role, so pkg/role.BuiltInRoles places it
// there without a hand-maintained list. Safe for concurrent use; call
// before role seeding (e.g. from the composition root, alongside
// RegisterPermissions) so the grant is visible to the first BuiltInRoles
// call.
//
// When role is PermissionRoleSO the FIPS 140-3 AS10.03 separation-of-duties
// guard applies and only permissions declared PermissionDutyGovernance are
// admitted. It panics with PermissionSoDViolationError for a cryptographic
// permission — every resource whose action names a cryptographic primitive, not
// just the keys:* family — with PermissionUndeclaredDutyError for a permission
// whose duty was never declared, and with PermissionWildcardGrantError for a
// whole-segment wildcard. A contributed permission therefore reaches the
// Security Officer role only after its author states, via
// RegisterPermissionDuty, that it is governance.
var RegisterPermissionRoleGrant = permission.RegisterRoleGrant

// PermissionsForRole returns every permission registered with a default
// grant for the named built-in role, sorted. It reflects the live
// registry, so a permission registered by RegisterPermissionRoleGrant
// after this package's own init() still appears once registered.
var PermissionsForRole = permission.RolePermissions

// HasPermissionRoleGrant reports whether p is registered with a default
// grant for the named built-in role.
var HasPermissionRoleGrant = permission.HasRoleGrant

// ---------------------------------------------------------------------------
// Per-tenant permission scope types
// ---------------------------------------------------------------------------

// PermissionScopeResolver resolves a permission's scope in the context of a
// specific tenant. It is the tenant-aware counterpart to the process-global
// scope registry and is the interface the RBAC authorizer depends on so that
// a caller-defined permission resolves in the caller's own tenant.
// (Aliases pkg/permission.ScopeResolver.)
type PermissionScopeResolver = permission.ScopeResolver

// TenantScopeStore persists and resolves caller-defined, per-tenant permission
// scopes. Two tenants may define the same permission name with different scopes
// without colliding, because each tenant's definitions are stored in that
// tenant's own store as selected by the TenantStoreResolver. (Aliases
// pkg/permission.TenantScopeStore.)
type TenantScopeStore = permission.TenantScopeStore

// NewTenantScopeStore constructs a TenantScopeStore over the given
// TenantStoreResolver. The resolver must be non-nil. (Aliases
// pkg/permission.NewTenantScopeStore.)
var NewTenantScopeStore = permission.NewTenantScopeStore
