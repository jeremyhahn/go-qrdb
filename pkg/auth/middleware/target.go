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
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// targetKind names how a request identifies the tenant it acts on.
type targetKind int

const (
	// targetSelf is the zero value: the request acts on the caller's own
	// tenant, which is what every data-plane request does.
	targetSelf targetKind = iota
	// targetTenant names the tenant directly (a path segment, a body field).
	targetTenant
	// targetUser names a user; the tenant is the one the user belongs to.
	targetUser
	// targetRole names a role; the tenant is the one the role belongs to.
	targetRole
)

// Target is the tenant a request acts on, as the request names it. Every
// transport extracts one from its own request shape — a REST path parameter,
// a protobuf field, a QUIC payload field, an MCP tool argument — and hands it
// to the SAME resolution (ResolveTarget), so the route-level permission check
// is made against the tenant the operation actually touches on every
// transport alike.
//
// It matters because a RoleBinding grants only in the tenant it names: a
// system-tenant operator whose sole grant for tenant A is a binding holds
// users:update in A and nowhere else, so authorizing an update of a tenant-A
// user against the operator's own (system) tenant would refuse it, and
// authorizing an update of a tenant-B user against A would be wrong the other
// way. The zero Target is the caller's own tenant.
type Target struct {
	kind     targetKind
	tenantID string
	userID   uint64
	roleID   string
}

// SelfTarget is the caller's own tenant — the target of every request that
// names no other.
func SelfTarget() Target { return Target{} }

// TenantTarget names tenantID directly. An empty tenantID means the caller's
// own tenant, matching how every IAM request treats an omitted tenant.
func TenantTarget(tenantID string) Target {
	if tenantID == "" {
		return Target{}
	}
	return Target{kind: targetTenant, tenantID: tenantID}
}

// UserTarget names the tenant user id belongs to.
func UserTarget(id uint64) Target {
	if id == 0 {
		return Target{}
	}
	return Target{kind: targetUser, userID: id}
}

// RoleTarget names the tenant role id belongs to.
func RoleTarget(id string) Target {
	if id == "" {
		return Target{}
	}
	return Target{kind: targetRole, roleID: id}
}

// WithRoleService lets ResolveTarget resolve a RoleTarget to the tenant the
// role belongs to. Without it a RoleTarget resolves to the caller's own
// tenant.
func WithRoleService(r role.RoleServicer) Option {
	return func(m *AuthMiddleware) {
		m.roles = r
	}
}

// ResolveTarget returns the tenant t acts on.
//
// A user or role is looked up through the same routed service the handler
// will use, with the caller's own context, so the answer is exactly the
// tenant the operation would reach. When the record is not found — it does
// not exist, or it lies in a tenant the caller cannot see, which the routed
// services report the same way — the target is the caller's own tenant, and
// the handler gives its usual not-found answer. Any other lookup failure is
// returned: it is not an answer, and authorizing against a guess would be.
func (m *AuthMiddleware) ResolveTarget(ctx context.Context, t Target) (string, error) {
	self := tenant.FromContext(ctx)
	switch t.kind {
	case targetTenant:
		return tenant.Normalize(t.tenantID), nil
	case targetUser:
		return m.userTenant(ctx, t.userID, self)
	case targetRole:
		return m.roleTenant(ctx, t.roleID, self)
	default:
		return self, nil
	}
}

// userTenant resolves the tenant user id belongs to, falling back to self
// when the caller cannot see it.
func (m *AuthMiddleware) userTenant(ctx context.Context, id uint64, self string) (string, error) {
	if m.users == nil {
		return self, nil
	}
	u, err := m.users.GetUser(ctx, id)
	if err == nil && u != nil {
		return tenant.Normalize(u.TenantID), nil
	}
	var notFound *user.UserNotFoundError
	if err == nil || errors.As(err, &notFound) {
		return self, nil
	}
	return "", &StoreUnavailableError{Err: err}
}

// roleTenant resolves the tenant role id belongs to, falling back to self
// when the caller cannot see it.
func (m *AuthMiddleware) roleTenant(ctx context.Context, id, self string) (string, error) {
	if m.roles == nil {
		return self, nil
	}
	r, err := m.roles.GetRole(ctx, id)
	if err == nil && r != nil {
		return tenant.Normalize(r.TenantID), nil
	}
	var notFound *role.RoleNotFoundError
	if err == nil || errors.As(err, &notFound) {
		return self, nil
	}
	return "", &StoreUnavailableError{Err: err}
}

// authorizeTarget resolves t and authorizes perm against it: the route-level
// check every transport makes once it knows what the request names. The
// target is bound into a context used only for this decision; the handler
// keeps the caller's own tenant in its context, so nothing downstream reads
// the target as the caller's identity.
func (m *AuthMiddleware) authorizeTarget(ctx context.Context, identity *auth.Identity, perm permission.Permission, t Target) error {
	if identity == nil {
		return auth.ErrUnauthenticated
	}
	target, err := m.ResolveTarget(ctx, t)
	if err != nil {
		return err
	}
	return m.authorize(tenant.WithTenantID(ctx, target), identity, perm)
}

// AuthorizeTarget authorizes the identity in ctx for perm against the tenant
// t names — the route-level check for a caller that is not one of the
// registry-driven transports (the embedded transport authorizes its typed
// calls through this). It returns auth.ErrUnauthenticated when ctx carries no
// identity.
func (m *AuthMiddleware) AuthorizeTarget(ctx context.Context, perm permission.Permission, t Target) error {
	return m.authorizeTarget(ctx, auth.IdentityFromContext(ctx), perm, t)
}
