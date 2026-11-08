// Copyright 2026 Jeremy Hahn
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
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// grantGuard is the RoleServicer decorator NewGrantGuard returns. Every
// method but CreateRole and UpdateRole is the wrapped service's own.
type grantGuard struct {
	RoleServicer
	authz auth.Authorizer
}

var _ RoleServicer = (*grantGuard)(nil)

// NewGrantGuard returns svc with the grant rule enforced on role writes: a
// grantor may grant only permissions it holds (go-qrdb#1278). CreateRole and
// UpdateRole refuse, with *GrantExceedsHolderError, a request carrying any
// permission the calling principal -- auth.IdentityFromContext -- does not
// hold in the role's tenant, as authz evaluates it (ValidateGrantable).
//
// A call with no principal in its context is not a grant by a principal and
// passes through unchanged: every transport authenticates before it reaches
// the role service, so only system work -- bootstrap, first boot, the
// tenant seeder -- arrives without one, exactly as the audit trail already
// records such writes with no actor. ReconcileBuiltInRoles is not guarded:
// it converges a tenant onto BuiltInRoles, a value no caller chooses.
//
// A nil authz is a deployment with no RBAC, where nothing is authorized, and
// svc is returned as it is.
func NewGrantGuard(svc RoleServicer, authz auth.Authorizer) RoleServicer {
	if svc == nil || authz == nil {
		return svc
	}
	return &grantGuard{RoleServicer: svc, authz: authz}
}

// CreateRole refuses a role carrying a permission the caller does not hold in
// the tenant the role is created in, then creates it.
func (g *grantGuard) CreateRole(ctx context.Context, req *CreateRoleRequest) (*Role, error) {
	if req != nil && len(req.Permissions) > 0 && auth.IdentityFromContext(ctx) != nil {
		if err := validatePermissions(req.Permissions); err != nil {
			return nil, err
		}
		target, err := tenant.EffectiveTenant(ctx, req.TenantID, "create role")
		if err != nil {
			return nil, err
		}
		if err := ValidateGrantable(ctx, g.authz, target, req.Permissions); err != nil {
			return nil, err
		}
	}
	return g.RoleServicer.CreateRole(ctx, req)
}

// UpdateRole refuses a permission set carrying a permission the caller does
// not hold in the role's own tenant, then applies the update. The whole set
// is checked, not only what it adds: the request names every permission the
// role will grant.
func (g *grantGuard) UpdateRole(ctx context.Context, req *UpdateRoleRequest) (*Role, error) {
	if req != nil && len(req.Permissions) > 0 && auth.IdentityFromContext(ctx) != nil {
		if err := validatePermissions(req.Permissions); err != nil {
			return nil, err
		}
		existing, err := g.RoleServicer.GetRole(ctx, req.ID)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			return nil, &RoleNotFoundError{ID: req.ID}
		}
		if err := ValidateGrantable(ctx, g.authz, existing.TenantID, req.Permissions); err != nil {
			return nil, err
		}
	}
	return g.RoleServicer.UpdateRole(ctx, req)
}

// ValidateGrantable reports whether the principal in ctx holds every
// permission perms grant in tenantID, as authz evaluates it. A wildcard is a
// name-matcher over the registry (permission.Expand), so it is grantable when
// every registered permission it covers is held; a caller holding a wildcard
// holds whatever it covers. The first permission refused is reported as
// *GrantExceedsHolderError; an authorizer failure that is not a refusal
// (auth.ErrUnauthorized) is returned as it is, so a transient fault is not
// reported as a denial and nothing is granted either way.
//
// A nil authz (no RBAC) or a context with no principal (system work, see
// NewGrantGuard) grants without a check.
func ValidateGrantable(ctx context.Context, authz auth.Authorizer, tenantID string, perms []permission.Permission) error {
	identity := auth.IdentityFromContext(ctx)
	if authz == nil || identity == nil || len(perms) == 0 {
		return nil
	}
	check := authz.Authorize
	if ga, ok := authz.(auth.GrantAuthorizer); ok {
		check = ga.AuthorizeGrant
	}
	targetCtx := tenant.WithTenantID(ctx, tenantID)
	for _, p := range permission.ExpandAll(perms) {
		err := check(targetCtx, identity, permission.Resource(p), permission.Action(p))
		if err == nil {
			continue
		}
		if errors.Is(err, auth.ErrUnauthorized) {
			return &GrantExceedsHolderError{Subject: identity.Subject, TenantID: tenantID, Permission: p.String()}
		}
		return err
	}
	return nil
}
