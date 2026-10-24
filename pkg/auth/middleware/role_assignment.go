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

package middleware

import (
	"context"
	"slices"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// AuthorizeRoleAssignmentWrite authorizes and validates an UpdateUser write
// that touches RoleIDs, RoleBindings, or Attributes.
//
// Those three fields are how a caller grants or adjusts a principal's
// authority through the generic UpdateUser call rather than through a
// dedicated AssignRole/UnassignRole RPC, so they are gated behind the SAME
// permission.RolesAssign the RBAC model already defines for granting roles
// (owner ruling, go-qrdb#192/#197) — a caller who cannot grant a role
// directly must not reach the same effect by editing a user's RoleIDs,
// RoleBindings, or Attributes. All three fields share one gate rather than
// three, on the owner's explicit instruction.
//
// The target tenant is resolved by READING THE EXISTING RECORD (userSvc.
// GetUser(ctx, userID).TenantID), never trusted from the caller's ambient
// context tenant. UpdateUser itself resolves its target purely by id
// (tenant.OwningTenant against the identity index), so a caller's ambient
// tenant frequently disagrees with the tenant the write will actually land
// in -- most obviously a system-tenant administrator updating a tenant-A
// user, whose ambient tenant is "system" while the record, and every
// RoleIDs entry on it, lives in tenant A. Authorizing against the ambient
// tenant checked the wrong tenant's scope rule and could refuse or admit on
// the wrong basis entirely.
//
// Every entry in roleIDs, and every RoleBinding, is validated against the
// resolved target tenant. A RoleBinding may name only the user's own tenant
// (owner ruling, superseding the cross-tenant part of go-qrdb#192): nobody
// administers another tenant's users or roles, even through an explicit
// binding, so a binding naming any other tenant is refused with
// *user.RoleBindingTenantError -- after the roles:assign check, so a caller
// refused that learns nothing about the binding. The store applies the same
// rule (user.NormalizeRoleBindings); refusing here as well gives every
// transport the typed refusal before any role is resolved.
//
// Authorization and validation are separate steps with separate conditions.
// Authorization is m's: m == nil means the composition enforces no RBAC (auth
// disabled, matching every other AuthMiddleware-gated write) and the
// roles:assign checks are skipped. Validation is not an authorization
// decision and is never skipped: every role reference is checked for
// existence and tenant whenever one is named, with or without m, and
// roleSvc == nil with a reference present is a *role.ServiceRequiredError
// rather than an unvalidated write. An Attributes-only write names no role
// reference and needs no role service.
//
// RoleIDs and RoleBindings each replace the record's set when non-nil, so a
// write that leaves the target tenant's SO role off a holder's new set
// revokes it as surely as one naming it grants it: either is refused unless
// the caller is that tenant's SO (authorizeSORoleChange).
func (m *AuthMiddleware) AuthorizeRoleAssignmentWrite(
	ctx context.Context,
	userSvc user.UserServicer,
	roleSvc role.RoleServicer,
	userID uint64,
	roleIDs []string,
	bindings []user.RoleBinding,
) error {
	if m == nil && len(roleIDs) == 0 && len(bindings) == 0 {
		// An Attributes-only write with no RBAC: nothing to authorize and no
		// role reference to validate.
		return nil
	}
	existing, err := assignee(ctx, userSvc, userID)
	if err != nil {
		return err
	}
	target := tenantid.Normalize(existing.TenantID)

	if err := m.authorizeRolesAssign(ctx, target); err != nil {
		return err
	}
	if _, err := user.NormalizeRoleBindings(existing.ID, target, bindings); err != nil {
		return err
	}
	afterIDs, afterBindings := existing.RoleIDs, existing.RoleBindings
	if roleIDs != nil {
		afterIDs = roleIDs
	}
	if bindings != nil {
		afterBindings = bindings
	}
	if err := m.authorizeSORoleChange(ctx, target, heldRoleIDs(target, existing.RoleIDs, existing.RoleBindings), heldRoleIDs(target, afterIDs, afterBindings)); err != nil {
		return err
	}
	return validateRoleAssignment(ctx, roleSvc, target, roleIDs, bindings)
}

// AuthorizeRoleAssignment authorizes and validates AssignRole, adding roleID
// to userID's roles: AuthorizeRoleAssignmentWrite's gate under AssignRole's
// append semantics. The role joins the record's set rather than replacing it,
// so assigning another role to an SO holder revokes nothing; assigning the
// tenant's SO role to an account without it is a grant only the tenant's SO
// may make (authorizeSORoleChange).
func (m *AuthMiddleware) AuthorizeRoleAssignment(
	ctx context.Context,
	userSvc user.UserServicer,
	roleSvc role.RoleServicer,
	userID uint64,
	roleID string,
) error {
	existing, err := assignee(ctx, userSvc, userID)
	if err != nil {
		return err
	}
	target := tenantid.Normalize(existing.TenantID)
	if err := m.authorizeRolesAssign(ctx, target); err != nil {
		return err
	}
	before := heldRoleIDs(target, existing.RoleIDs, existing.RoleBindings)
	if err := m.authorizeSORoleChange(ctx, target, before, append(slices.Clip(before), roleID)); err != nil {
		return err
	}
	return validateRoleAssignment(ctx, roleSvc, target, []string{roleID}, nil)
}

// AuthorizeRoleUnassignment authorizes removing roleID from userID's roles:
// the caller needs roles:unassign in the tenant the user belongs to, read
// from the record, and removing that tenant's SO role from its holder is a
// revocation only the tenant's SO may make (authorizeSORoleChange). Removing
// a grant names no role to validate, so there is no validation step.
// AssignRole's twin is AuthorizeRoleAssignment. A nil m authorizes nothing,
// as every AuthMiddleware gate does when no RBAC is configured.
func (m *AuthMiddleware) AuthorizeRoleUnassignment(ctx context.Context, userSvc user.UserServicer, userID uint64, roleID string) error {
	if m == nil {
		return nil
	}
	existing, err := assignee(ctx, userSvc, userID)
	if err != nil {
		return err
	}
	target := tenantid.Normalize(existing.TenantID)
	if err := m.AuthorizeTenant(ctx, target, permission.RolesUnassign); err != nil {
		return err
	}
	// UnassignRole removes the role from RoleIDs alone; a binding naming it
	// still grants it afterwards.
	afterIDs := slices.DeleteFunc(slices.Clone(existing.RoleIDs), func(id string) bool { return id == roleID })
	return m.authorizeSORoleChange(ctx, target, heldRoleIDs(target, existing.RoleIDs, existing.RoleBindings), heldRoleIDs(target, afterIDs, existing.RoleBindings))
}

// AuthorizeRoleAssignmentWriteOnCreate authorizes and validates a
// CreateUser request's RoleIDs, applying AuthorizeRoleAssignmentWrite's gate
// to the create path: roles:assign in the tenant the record will be created
// in, and every role reference validated against that tenant.
//
// The target tenant is the EFFECTIVE tenant the new record will be created
// in — tenant.EffectiveTenant over the caller's identity and the request's
// requested tenant — the SAME resolution routingUserService.CreateUser
// performs internally, so this authorizes against the tenant the write will
// actually use rather than a value computed independently that could
// disagree with it.
//
// A request naming no RoleIDs is a no-op: CreateUser carries no
// RoleBindings or Attributes field, so RoleIDs is its only role-assignment
// surface. Authorization and validation follow AuthorizeRoleAssignmentWrite:
// m == nil skips only the authorization. Creating an account that holds the
// tenant's SO role grants it, so only that tenant's SO may name it here
// (authorizeSORoleChange).
func (m *AuthMiddleware) AuthorizeRoleAssignmentWriteOnCreate(
	ctx context.Context,
	roleSvc role.RoleServicer,
	requestedTenantID string,
	roleIDs []string,
) error {
	if len(roleIDs) == 0 {
		return nil
	}
	target, err := tenant.EffectiveTenant(ctx, requestedTenantID, "create user")
	if err != nil {
		return err
	}
	if err := m.authorizeRolesAssign(ctx, target); err != nil {
		return err
	}
	if err := m.authorizeSORoleChange(ctx, target, nil, roleIDs); err != nil {
		return err
	}
	return validateRoleAssignment(ctx, roleSvc, target, roleIDs, nil)
}

// SORoleGuard returns the user.UpdateGuard that binds the SO-role rule to the
// write it authorizes. AuthorizeRoleAssignmentWrite, AuthorizeRoleAssignment
// and AuthorizeRoleUnassignment judge the SO transition against the record as
// they read it, which a concurrent write can replace before the update
// commits: an Admin's role replacement checked against a record without the
// SO role would otherwise remove a grant an SO committed in between. The
// guard repeats the judgement inside the user store against the record the
// conditional commit swaps against, so the rule holds for the version written.
// Every UpdateUser that follows one of those gates carries it.
//
// The guard refuses with *SORoleRequiredError or *SORoleUnresolvedError, as
// the gates do. A nil m enforces no RBAC and returns nil.
func (m *AuthMiddleware) SORoleGuard(ctx context.Context) user.UpdateGuard {
	if m == nil {
		return nil
	}
	return func(prior, next *user.User) error {
		target := tenantid.Normalize(prior.TenantID)
		return m.authorizeSORoleChange(ctx, target,
			heldRoleIDs(target, prior.RoleIDs, prior.RoleBindings), heldRoleIDs(target, next.RoleIDs, next.RoleBindings))
	}
}

// authorizeRolesAssign requires roles:assign in target, the tenant the
// user is in or is being created in. A nil m authorizes nothing, as every
// AuthMiddleware gate does when no RBAC is configured.
func (m *AuthMiddleware) authorizeRolesAssign(ctx context.Context, target string) error {
	if m == nil {
		return nil
	}
	return m.AuthorizeTenant(ctx, target, permission.RolesAssign)
}

// authorizeSORoleChange keeps the target tenant's Security Officer role in
// that tenant's SO's hands under strict separation of duties
// (WithStrictSeparation, go-qrdb#1042); under the standard posture it admits
// every write, which roles:assign and roles:unassign then decide. A write
// whose before and after role sets differ on whether the account holds the
// SO role, a grant or a revocation, is admitted only for a caller who is
// itself target's SO. The tenant Admin holds roles:assign and
// roles:unassign and is refused here, as is any other tenant's SO, the
// system tenant's included. A write that leaves SO membership as it was is
// not this gate's to decide. The caller is judged by role.HoldsSystemRole
// over the roles it holds in target, its Roles and its bindings scoped to
// target (callerRoleIDs), as the account's own membership is counted.
//
// The refusal is *SORoleRequiredError (403 / PermissionDenied); a role
// store that cannot say who holds the SO role is *SORoleUnresolvedError
// (503), never an admission. A nil m authorizes nothing, as every
// AuthMiddleware gate does when no RBAC is configured.
func (m *AuthMiddleware) authorizeSORoleChange(ctx context.Context, target string, before, after []string) error {
	if m == nil || !m.strictSeparation || slices.Equal(before, after) {
		return nil
	}
	held, err := m.holdsSO(ctx, target, before)
	if err != nil {
		return err
	}
	holds, err := m.holdsSO(ctx, target, after)
	if err != nil {
		return err
	}
	if held == holds {
		return nil
	}
	identity := auth.IdentityFromContext(ctx)
	if identity == nil || tenantid.Normalize(identity.TenantID) != target {
		return &SORoleRequiredError{}
	}
	isSO, err := m.holdsSO(ctx, target, callerRoleIDs(identity, target))
	if err != nil {
		return err
	}
	if !isSO {
		return &SORoleRequiredError{}
	}
	return nil
}

// holdsSO reports whether roleIDs include tenantID's SO role.
func (m *AuthMiddleware) holdsSO(ctx context.Context, tenantID string, roleIDs []string) (bool, error) {
	ok, err := role.HoldsSystemRole(ctx, m.roles, tenantID, roleIDs, role.RoleNameSO)
	if err != nil {
		return false, &SORoleUnresolvedError{Err: err}
	}
	return ok, nil
}

// assignee reads the user a role-assignment write lands on. A nil record
// with a nil error is a UserServicer contract violation (GetUser returns a
// typed not-found error instead), but the gate fails closed rather than
// panic on it: it is the not-found the write itself would report.
func assignee(ctx context.Context, userSvc user.UserServicer, userID uint64) (*user.User, error) {
	existing, err := userSvc.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, &user.UserNotFoundError{ID: userID}
	}
	return existing, nil
}

// heldRoleIDs is every role an account with roleIDs and bindings holds in
// target: its roles and its bindings scoped to target. An account's own
// bindings name only its own tenant (user.NormalizeRoleBindings); a caller's
// may name others, which grant nothing in target.
func heldRoleIDs(target string, roleIDs []string, bindings []user.RoleBinding) []string {
	out := make([]string, 0, len(roleIDs)+len(bindings))
	out = append(out, roleIDs...)
	for _, b := range bindings {
		if tenantid.Normalize(b.TenantID) == target {
			out = append(out, b.RoleID)
		}
	}
	return out
}

// callerRoleIDs is every role the authenticated caller holds in target: its
// roles and, as heldRoleIDs counts them for an account, its bindings scoped
// to target. Authentication carries bindings apart from Roles
// (auth.Identity.RoleBindings), so a caller holding the SO role through a
// binding is an SO here too.
func callerRoleIDs(identity *auth.Identity, target string) []string {
	bindings := make([]user.RoleBinding, len(identity.RoleBindings))
	for i, b := range identity.RoleBindings {
		bindings[i] = user.RoleBinding{RoleID: b.RoleID, TenantID: b.TenantID}
	}
	return heldRoleIDs(target, identity.Roles, bindings)
}

// validateRoleAssignment confirms every role reference exists and may be
// granted in target: each of roleIDs, and each binding's role, which the
// caller has already confined to target. A reference present with
// no roleSvc to check it against is refused with *role.ServiceRequiredError.
func validateRoleAssignment(ctx context.Context, roleSvc role.RoleServicer, target string, roleIDs []string, bindings []user.RoleBinding) error {
	if len(roleIDs) == 0 && len(bindings) == 0 {
		return nil
	}
	if roleSvc == nil {
		return &role.ServiceRequiredError{}
	}
	for _, id := range roleIDs {
		if err := role.ValidateAssignable(ctx, roleSvc, id, target); err != nil {
			return err
		}
	}
	for _, b := range bindings {
		if err := role.ValidateAssignable(ctx, roleSvc, b.RoleID, target); err != nil {
			return err
		}
	}
	return nil
}

// TouchesRoleAssignment reports whether an UpdateUser write names any of the
// role-assignment-shaped fields (RoleIDs, RoleBindings, or Attributes),
// using the same nil-means-leave-alone convention every field on
// UpdateUserRequest already carries.
func TouchesRoleAssignment(roleIDs []string, bindings []user.RoleBinding, attributes map[string]string) bool {
	return roleIDs != nil || bindings != nil || attributes != nil
}
