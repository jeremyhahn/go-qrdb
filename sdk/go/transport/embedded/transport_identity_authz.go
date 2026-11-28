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
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
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
// operation naming userID, then admits the operator access the request's
// tenant qualifier names (operatorAccess). userID plays no role in the
// decision; the routed user service confines the lookup itself (see
// tenant.OwningTenant). The returned context is the one the operation runs
// under.
func (t *Transport) authorizeUserTarget(ctx context.Context, userID uint64, perm permission.Permission) (context.Context, error) {
	if t.authMW == nil {
		return ctx, nil
	}
	if err := t.authMW.AuthorizeTenant(ctx, tenant.FromContext(ctx), perm); err != nil {
		return ctx, err
	}
	return t.operatorAccess(ctx, "", perm)
}

// authorizeRoleTarget authorizes perm against the caller's own tenant for an
// operation naming roleID, then admits the operator access the request's
// tenant qualifier names (operatorAccess). roleID plays no role in the
// decision; the routed role service confines the lookup itself (see
// tenant.OwningTenant). The returned context is the one the operation runs
// under.
func (t *Transport) authorizeRoleTarget(ctx context.Context, roleID string, perm permission.Permission) (context.Context, error) {
	if t.authMW == nil {
		return ctx, nil
	}
	if err := t.authMW.AuthorizeTenant(ctx, tenant.FromContext(ctx), perm); err != nil {
		return ctx, err
	}
	return t.operatorAccess(ctx, "", perm)
}

// authorizeIdentityTenant authorizes perm against the caller's own tenant
// for an operation naming tenantID directly (create, list,
// get-by-username/email/name), then admits the operator access the
// tenant tenantID, else the request's tenant qualifier, names
// (operatorAccess). The returned context is the one
// the operation runs under.
func (t *Transport) authorizeIdentityTenant(ctx context.Context, tenantID string, perm permission.Permission) (context.Context, error) {
	if t.authMW == nil {
		return ctx, nil
	}
	if err := t.authMW.AuthorizeTenant(ctx, tenant.FromContext(ctx), perm); err != nil {
		return ctx, err
	}
	return t.operatorAccess(ctx, tenantID, perm)
}

// operatorAccess is the embedded half of the tenant-qualified identity
// surface every network transport serves (the REST tenantID parameter, the
// gRPC and QUIC tenant_id field): a request carrying a tenant qualifier
// (transport.WithTenantQualifier) names the tenant whose users or roles it
// reaches -- explicit, the tenant the request itself names, else the
// qualifier -- and runs under the context
// AuthMiddleware.AuthorizeOperatorIdentityAccess returns for it, with the
// tenant-qualified permission standing for perm. A caller acting on its own
// tenant passes unchanged; a platform operator reaching another tenant is
// admitted, recorded and bound to that tenant, or refused, as on every
// other transport. The tenant the request itself names is a qualifier as
// much as WithTenantQualifier's is: a ListUsers{TenantID: "acme"} with no
// context qualifier is decided as the network transports decide it. A
// request naming no tenant either way is not decided here.
func (t *Transport) operatorAccess(ctx context.Context, explicit string, perm permission.Permission) (context.Context, error) {
	target := qualifiedTenant(ctx, explicit)
	if target == "" {
		return ctx, nil
	}
	if err := tenant.ValidateTenantIDShape(target); err != nil {
		return ctx, err
	}
	if t.authMW == nil {
		return ctx, nil
	}
	return t.authMW.AuthorizeOperatorIdentityAccess(ctx, target, tenantQualifiedPermission(perm))
}

// qualifiedTenant is the tenant an operation naming explicit runs in: explicit
// when set, else the request's tenant qualifier (operatorAccess admitted the
// access to it), else empty -- the caller's own.
func qualifiedTenant(ctx context.Context, explicit string) string {
	if explicit != "" {
		return explicit
	}
	return transport.TenantQualifierFromContext(ctx)
}

// tenantQualifiedPermission is the tenant-qualified permission an operator
// access to perm's records needs: users:read:tenant or users:write:tenant,
// roles:read:tenant or roles:write:tenant.
func tenantQualifiedPermission(perm permission.Permission) permission.Permission {
	read := perm == permission.UsersRead || perm == permission.UsersList || perm == permission.UserAuthFactorsList ||
		perm == permission.UsersGetByCertThumbprint ||
		perm == permission.RolesRead || perm == permission.RolesList
	switch {
	case permission.Resource(perm) == permission.Resource(permission.RolesRead) && read:
		return permission.RolesReadTenant
	case permission.Resource(perm) == permission.Resource(permission.RolesRead):
		return permission.RolesWriteTenant
	case read:
		return permission.UsersReadTenant
	default:
		return permission.UsersWriteTenant
	}
}
