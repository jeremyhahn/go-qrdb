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

// Package rbac implements a role-based access control Authorizer that
// resolves permissions through the role service and caches the result
// for efficient repeated authorization checks.
package rbac

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

const (
	// defaultCacheTTL is the default time-to-live for cached permission sets.
	defaultCacheTTL = 30 * time.Second

	// superadminRole is the role name that bypasses all authorization checks.
	superadminRole = "superadmin"
)

// RBACAuthorizer implements auth.Authorizer using role-based access control.
// Permissions are resolved through the role service and cached per identity
// with a configurable TTL.
type RBACAuthorizer struct {
	roles role.RoleServicer
	cache *permissionCache
}

// Option configures an RBACAuthorizer.
type Option func(*RBACAuthorizer)

// WithCacheTTL sets the time-to-live for cached permission entries.
// The default is 30 seconds.
func WithCacheTTL(ttl time.Duration) Option {
	return func(a *RBACAuthorizer) {
		a.cache = newPermissionCache(ttl)
	}
}

// New creates a new RBACAuthorizer backed by the given role service.
func New(roles role.RoleServicer, opts ...Option) *RBACAuthorizer {
	a := &RBACAuthorizer{
		roles: roles,
		cache: newPermissionCache(defaultCacheTTL),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Authorize checks whether the given identity has permission to perform
// the specified action on the resource. It returns nil if the action is
// allowed, auth.ErrUnauthenticated if the identity is nil, or a
// PermissionDeniedError (wrapping auth.ErrUnauthorized) if denied.
func (a *RBACAuthorizer) Authorize(ctx context.Context, identity *auth.Identity, resource, action string) error {
	if identity == nil {
		return auth.ErrUnauthenticated
	}

	// Superadmin bypass: no permission resolution needed. Checks both
	// the role name ("superadmin") and the deterministic UUID that
	// BuiltInRoles generates for the identity's tenant.
	superadminID := role.SuperadminRoleID(identity.TenantID)
	for _, r := range identity.Roles {
		if r == superadminRole || r == superadminID {
			return nil
		}
	}

	// Look up cached permissions.
	permSet, ok := a.cache.get(identity.TenantID, identity.Subject)
	if !ok {
		// Cache miss: resolve through the role service.
		perms, err := a.roles.ResolvePermissions(ctx, identity.Roles)
		if err != nil {
			return &ResolveError{Err: err}
		}
		a.cache.set(identity.TenantID, identity.Subject, perms)
		permSet = make(map[permission.Permission]struct{}, len(perms))
		for _, p := range perms {
			permSet[p] = struct{}{}
		}
	}

	required := permission.Permission(resource + ":" + action)
	if _, allowed := permSet[required]; allowed {
		return nil
	}

	return &PermissionDeniedError{
		Resource: resource,
		Action:   action,
		Subject:  identity.Subject,
	}
}

// Invalidate removes the cached permissions for a specific identity,
// forcing a fresh resolution on the next Authorize call.
func (a *RBACAuthorizer) Invalidate(tenantID, subject string) {
	a.cache.invalidate(tenantID, subject)
}

// InvalidateAll clears all cached permission entries.
func (a *RBACAuthorizer) InvalidateAll() {
	a.cache.invalidateAll()
}
