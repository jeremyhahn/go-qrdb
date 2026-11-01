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

package role

import (
	"context"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ValidateAssignable resolves roleID and confirms it may be granted within
// tenantID: the role must belong to tenantID itself. That holds for the
// built-in roles too -- every tenant's ReconcileBuiltInRoles seeds its own
// copy under its own identifier, and the role store resolves an id only in the
// tenant that owns it, so another tenant's role (the system tenant's built-ins
// included) is one the authorizer cannot resolve for a principal of tenantID.
// Granting it would leave every later decision for that principal failing as
// unavailable, not merely deny the one grant.
//
// A role that does not exist is a *ReferenceError wrapping the lookup's
// *RoleNotFoundError; any other lookup failure is returned verbatim. A role
// whose deletion has begun is a *RoleDeletingError. A role that exists in
// another tenant is a *NotAssignableError.
//
// Last, the grant rule (go-qrdb#1278): a grantor may grant only what it
// holds, whether it writes the permission into a role or hands over a role
// that already carries it. The assigner -- the principal in ctx -- must hold
// every permission a custom role grants, in tenantID, as authz evaluates it
// (ValidateGrantable). A built-in role (IsBuiltIn) keeps the duty split the
// built-ins encode instead: the tenant Admin assigns the built-in User role,
// whose crypto-use duty it does not hold, to the principals who perform that
// duty. Binding a role to the assigner itself is ValidateSelfAssignable. A
// refusal is a *GrantExceedsHolderError naming the role. It runs after every
// check above, so each of those keeps its own error. A nil authz (no RBAC) or
// a context with no principal skips it.
func ValidateAssignable(ctx context.Context, svc RoleServicer, authz auth.Authorizer, roleID, tenantID string) error {
	return validateAssignable(ctx, svc, authz, roleID, tenantID, false)
}

// ValidateSelfAssignable is ValidateAssignable for a binding whose holder is
// the assigner itself, and applies the grant rule to every role, built-in or
// custom: no principal binds to itself a role carrying a permission it does
// not hold. That closes the self-grant of go-qrdb#1278 through the built-in
// roles, the one path ValidateAssignable leaves to the duty split.
func ValidateSelfAssignable(ctx context.Context, svc RoleServicer, authz auth.Authorizer, roleID, tenantID string) error {
	return validateAssignable(ctx, svc, authz, roleID, tenantID, true)
}

// IsBuiltIn reports whether r is one of its tenant's built-in roles, the
// record ReconcileBuiltInRoles seeds: a system role under the deterministic
// identifier SystemRoleID gives its tenant and name. A role a caller created
// under a reserved name is immutable (IsSystemRoleName) but carries an
// identifier of its own, so it is not built in.
func IsBuiltIn(r *Role) bool {
	return r != nil && r.System && r.ID == SystemRoleID(r.TenantID, r.Name)
}

func validateAssignable(ctx context.Context, svc RoleServicer, authz auth.Authorizer, roleID, tenantID string, self bool) error {
	r, err := svc.GetRole(ctx, roleID)
	var notFound *RoleNotFoundError
	if errors.As(err, &notFound) {
		return &ReferenceError{RoleID: roleID, Err: err}
	}
	if err != nil {
		return err
	}
	if r == nil {
		// A nil role with a nil error breaks the RoleServicer contract; the
		// reference is unresolved, so it is refused rather than dereferenced.
		return &ReferenceError{RoleID: roleID, Err: &RoleNotFoundError{ID: roleID}}
	}
	if r.Deleting {
		return &RoleDeletingError{ID: roleID, Op: "assigned"}
	}
	if tenantid.Normalize(r.TenantID) != tenantid.Normalize(tenantID) {
		return &NotAssignableError{RoleID: roleID, TenantID: tenantID}
	}
	if !self && IsBuiltIn(r) {
		return nil
	}
	err = ValidateGrantable(ctx, authz, tenantID, r.Permissions)
	var exceeds *GrantExceedsHolderError
	if errors.As(err, &exceeds) {
		exceeds.RoleID = roleID
	}
	return err
}
