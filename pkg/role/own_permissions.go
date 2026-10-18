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

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// PermissionInfo is one effective permission projected with its declared
// scope. Its JSON shape is the wire shape every JSON transport (REST, QUIC,
// MCP) serves, field for field the SDK's transport.PermissionInfo.
type PermissionInfo struct {
	// Name is the permission in "resource:action" form.
	Name string `json:"name"`

	// Resource is the portion before the colon.
	Resource string `json:"resource"`

	// Action is the portion after the colon.
	Action string `json:"action"`

	// Scope is the permission's declared tenancy reach, as
	// permission.Scope.String renders it ("self", "cross", "tenant-only",
	// "self-strict").
	Scope string `json:"scope"`
}

// EffectivePermissions resolves roleIDs through svc and returns the
// concrete permissions they grant, deduplicated and sorted by name. A
// whole-segment wildcard grant (objstore:*, *:read, *:*) is expanded to the
// registered permissions it matches: a wildcard is how the authorizer
// matches a name, not a permission, and has no scope of its own to report.
// The roles are resolved in the context tenant, as ResolvePermissions
// resolves them.
func EffectivePermissions(ctx context.Context, svc RoleServicer, roleIDs []string) ([]permission.Permission, error) {
	if svc == nil {
		return nil, &ServiceRequiredError{}
	}
	perms, err := svc.ResolvePermissions(ctx, roleIDs)
	if err != nil {
		return nil, err
	}
	return permission.ExpandAll(perms), nil
}

// ListOwnPermissions returns the authenticated caller's own effective
// permissions, each with its declared scope, sorted by name.
//
// The identity is the one the transport authenticated onto ctx
// (auth.IdentityFromContext), never a parameter, so no caller can name
// another identity. Its role bindings are resolved as EffectivePermissions
// resolves them, in the caller's OWN tenant: the context tenant is replaced
// with the identity's, so a request that targets another tenant cannot make
// the resolve read that tenant's roles. The operation needs authentication
// only, no permission: a caller with no identity is refused with
// auth.ErrUnauthenticated.
func ListOwnPermissions(ctx context.Context, svc RoleServicer) ([]PermissionInfo, error) {
	identity := auth.IdentityFromContext(ctx)
	if identity == nil || identity.Subject == "" || identity.TenantID == "" {
		return nil, auth.ErrUnauthenticated
	}
	ctx = tenant.WithTenantID(ctx, identity.TenantID)
	perms, err := EffectivePermissions(ctx, svc, identity.Roles)
	if err != nil {
		return nil, err
	}
	out := make([]PermissionInfo, len(perms))
	for i, p := range perms {
		scope, err := permission.LookupScope(p)
		if err != nil {
			return nil, err
		}
		out[i] = PermissionInfo{
			Name:     p.String(),
			Resource: permission.Resource(p),
			Action:   permission.Action(p),
			Scope:    scope.String(),
		}
	}
	return out, nil
}
