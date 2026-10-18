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
	"slices"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// NameResolver is the narrow name-lookup surface HoldsSystemRole needs.
// RoleServicer satisfies it, as does any downstream adapter over a role
// store that can answer "which role is named N in tenant T".
type NameResolver interface {
	// GetRoleByName retrieves a role by tenant-scoped name.
	GetRoleByName(ctx context.Context, tenantID, name string) (*Role, error)
}

// HoldsSystemRole reports whether roleIDs grant the named system role of
// tenantID: whether one of them is the identifier under which the store
// persists that role.
//
// It answers the GRANT direction. The controls that VOID a grant (the SO
// deny-list, the strict-separation Owner strip) over-match on purpose, so
// they accept both the role name and the canonical identifier. A control
// that ADMITS a caller as the holder of a role must do the opposite and
// match only what the role store itself says denotes the role:
//
//   - a role NAME never matches. identity.Roles holds identifiers (the
//     enrichment sets it from User.RoleIDs), and a name there can only have
//     come from somewhere that is not the role store, such as a token claim
//     for a subject with no user record.
//   - the canonical identifier matches only when it is the persisted one.
//   - a tenant with no role of that name grants nothing.
//
// The lookup runs under tenantID's context, because a tenant-routed store
// scopes its ownership check to the context tenant. An empty tenantID is the
// system tenant. A nil svc is refused with *ResolverRequiredError rather than
// answered, since "cannot tell" is not "no". Any other store error is
// returned, and callers must deny on it.
func HoldsSystemRole(ctx context.Context, svc NameResolver, tenantID string, roleIDs []string, roleName string) (bool, error) {
	if svc == nil {
		return false, &ResolverRequiredError{Name: roleName}
	}
	if len(roleIDs) == 0 {
		return false, nil
	}
	if tenantID == "" {
		tenantID = tenantid.System
	}
	r, err := svc.GetRoleByName(tenant.WithTenantID(ctx, tenantID), tenantID, roleName)
	if err != nil {
		var notFound *RoleNotFoundError
		if errors.As(err, &notFound) {
			return false, nil
		}
		return false, err
	}
	if r == nil {
		return false, &InvalidResolverResultError{Name: roleName, TenantID: tenantID}
	}
	return slices.Contains(roleIDs, r.ID), nil
}

// IsTenantOwner decides auth.Identity.TenantOwner: whether the role store
// resolves id's roles to the Admin system role of id.TenantID. It is the one
// place that decision is made; every step that establishes an identity calls
// it rather than comparing role names.
//
// The roles considered are id.Roles and those of id.RoleBindings granted
// within id.TenantID itself, since a binding grants its role only in its own
// tenant. The answer concerns id.TenantID alone, so a system-tenant Admin is
// the owner of the system tenant and of nothing else. A nil id is not an
// owner. Errors are HoldsSystemRole's, and the caller must not treat one as
// a "no" it can carry on with.
func IsTenantOwner(ctx context.Context, svc NameResolver, id *auth.Identity) (bool, error) {
	if id == nil {
		return false, nil
	}
	tenantID := tenantid.Normalize(id.TenantID)
	roleIDs := id.Roles
	for _, b := range id.RoleBindings {
		if tenantid.Normalize(b.TenantID) == tenantID {
			roleIDs = append(slices.Clip(roleIDs), b.RoleID)
		}
	}
	return HoldsSystemRole(ctx, svc, tenantID, roleIDs, RoleNameAdmin)
}
