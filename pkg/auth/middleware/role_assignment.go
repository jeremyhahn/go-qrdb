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

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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
// Every entry in roleIDs is validated against the resolved target tenant.
// Every RoleBinding is authorized AND validated against ITS OWN TenantID
// field, separately from the target-tenant check above: a binding may name
// a tenant other than the target user's home tenant by design
// (tenant-scoped grants, go-qrdb#192 item 4), and writing such a binding is
// itself an act of granting a role WITHIN that other tenant. A caller who
// holds roles:assign only in the target user's tenant must not be able to
// mint a grant in some OTHER tenant merely by naming it in a binding, so
// roles:assign is required in EVERY tenant a binding targets, not only the
// user's own.
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
func (m *AuthMiddleware) AuthorizeRoleAssignmentWrite(
	ctx context.Context,
	userSvc user.UserServicer,
	roleSvc role.RoleServicer,
	userID uint64,
	roleIDs []string,
	bindings []user.RoleBinding,
) error {
	if m == nil && len(roleIDs) == 0 {
		// Nothing needs the target tenant: there is no authorization to
		// run, and a binding is validated against the tenant it names.
		return validateRoleAssignment(ctx, roleSvc, "", nil, bindings)
	}
	existing, err := userSvc.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if existing == nil {
		// A nil record with a nil error is a UserServicer contract
		// violation (GetUser should return a typed not-found error
		// instead), but this gate must fail closed rather than panic on
		// it: treat it as the same not-found condition the write itself
		// would report for a missing target.
		return &user.UserNotFoundError{ID: userID}
	}
	target := existing.TenantID

	if err := m.authorizeRoleAssignment(ctx, target, bindings); err != nil {
		return err
	}
	return validateRoleAssignment(ctx, roleSvc, target, roleIDs, bindings)
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
// m == nil skips only the authorization.
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
	if err := m.authorizeRoleAssignment(ctx, target, nil); err != nil {
		return err
	}
	return validateRoleAssignment(ctx, roleSvc, target, roleIDs, nil)
}

// authorizeRoleAssignment requires roles:assign in target and, separately,
// in the tenant every binding names: writing a binding grants a role WITHIN
// that tenant, so holding roles:assign only in the user's own tenant must not
// be enough to mint a grant in another. A nil m authorizes nothing, as every
// AuthMiddleware gate does when no RBAC is configured.
func (m *AuthMiddleware) authorizeRoleAssignment(ctx context.Context, target string, bindings []user.RoleBinding) error {
	if m == nil {
		return nil
	}
	if err := m.AuthorizeTenant(ctx, target, permission.RolesAssign); err != nil {
		return err
	}
	for _, b := range bindings {
		if err := m.AuthorizeTenant(ctx, b.TenantID, permission.RolesAssign); err != nil {
			return err
		}
	}
	return nil
}

// validateRoleAssignment confirms every role reference exists and may be
// granted where it is being granted: each of roleIDs in target, and each
// binding's role in the tenant the binding names. A reference present with
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
		if err := role.ValidateAssignable(ctx, roleSvc, b.RoleID, b.TenantID); err != nil {
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
