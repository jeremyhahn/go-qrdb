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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// This file is the embedded transport's half of the identity surface (users
// and roles). Every CRUD operation on a user or role record is authorized
// against the CALLER'S OWN (ambient) tenant, the same entry check every
// network transport's route middleware makes (owner ruling: a RoleBinding
// names only its holder's own tenant, so nothing is left for a per-request
// target lookup to find -- see pkg/user.NormalizeRoleBindings and
// pkg/auth/middleware/role_assignment.go).
//
// A caller naming another tenant's user or role by id, or another tenant's
// name directly, is never authorized against that OTHER tenant here. The
// routed services (pkg/user, pkg/role) are the ones that actually confine
// the request: tenant.OwningTenant resolves an id lookup to the caller's own
// tenant when the record belongs to another (so the store then answers
// not-found), and tenant.EffectiveTenant refuses a request that names
// another tenant outright for a non-system caller
// (*tenant.CrossTenantDeniedError) while trusting a system-tenant caller's
// named tenant -- the pre-existing, intentional reach documented as a known
// gap in docs/identity-rbac/architecture.md pending strict tenant isolation.
//
// Role-ASSIGNMENT writes (RoleIDs, RoleBindings, Attributes, Assign,
// Unassign) are not here -- they are AuthorizeRoleAssignmentWrite /
// AuthorizeRoleUnassignment (role_assignment.go), already wired in
// transport_auth.go, which confine every RoleBinding to the record's own
// tenant and would double-charge the rate limiter and the audit log if
// also run through the helpers below.

// authorizeUserTarget authorizes perm against the caller's own tenant for an
// operation naming userID. userID plays no role in the decision; the routed
// user service confines the lookup itself (see tenant.OwningTenant).
func (t *Transport) authorizeUserTarget(ctx context.Context, userID uint64, perm permission.Permission) error {
	if t.authMW == nil {
		return nil
	}
	return t.authMW.AuthorizeTenant(ctx, tenant.FromContext(ctx), perm)
}

// authorizeRoleTarget authorizes perm against the caller's own tenant for an
// operation naming roleID. roleID plays no role in the decision; the routed
// role service confines the lookup itself (see tenant.OwningTenant).
func (t *Transport) authorizeRoleTarget(ctx context.Context, roleID string, perm permission.Permission) error {
	if t.authMW == nil {
		return nil
	}
	return t.authMW.AuthorizeTenant(ctx, tenant.FromContext(ctx), perm)
}

// authorizeIdentityTenant authorizes perm against the caller's own tenant
// for an operation naming tenantID directly (create, list,
// get-by-username/email, reconcile-built-in). tenantID plays no role in the
// decision; the routed service confines or trusts it (see
// tenant.EffectiveTenant).
func (t *Transport) authorizeIdentityTenant(ctx context.Context, tenantID string, perm permission.Permission) error {
	if t.authMW == nil {
		return nil
	}
	return t.authMW.AuthorizeTenant(ctx, tenant.FromContext(ctx), perm)
}
