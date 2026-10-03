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

package embedded

import (
	"context"
	"sort"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// MissingRBACAuthorizerError is returned by the embedded RBACClient when
// it is invoked without an injected authorizer. Wire one up via
// WithRBACAuthorizer to enable authorization checks.
type MissingRBACAuthorizerError struct{}

// Error returns a human-readable description.
func (e *MissingRBACAuthorizerError) Error() string {
	return "embedded: RBAC authorizer is not configured"
}

// MissingRoleServiceForRBACError is returned by RBACClient.ListPermissions
// when the role service is not configured but the call requires resolving
// the identity's role permissions.
type MissingRoleServiceForRBACError struct{}

// Error returns a human-readable description.
func (e *MissingRoleServiceForRBACError) Error() string {
	return "embedded: role service is not configured for RBAC permission resolution"
}

// rbacClient is the embedded implementation of transport.RBACClient. It
// wraps the qrdb auth.Authorizer (typically *rbac.RBACAuthorizer) and
// honours the Phase 0 system-tenant rule transitively — the wrapper
// performs no rule logic itself, all gating lives in the underlying
// authorizer.
type rbacClient struct {
	transport *Transport
}

// Compile-time interface assertion.
var _ transport.RBACClient = (*rbacClient)(nil)

// identityToDomain converts the SDK identity projection to the domain
// auth.Identity. The Roles slice is copied so the caller's slice is
// independent of the authorizer's internal state.
func identityToDomain(id *transport.Identity) *auth.Identity {
	if id == nil {
		return nil
	}
	roles := make([]string, len(id.Roles))
	copy(roles, id.Roles)
	var meta map[string]string
	if len(id.Metadata) > 0 {
		meta = make(map[string]string, len(id.Metadata))
		for k, v := range id.Metadata {
			meta[k] = v
		}
	}
	return &auth.Identity{
		Subject:  id.Subject,
		TenantID: id.TenantID,
		Roles:    roles,
		Metadata: meta,
	}
}

// validateIdentity runs the structural checks every RBAC operation
// requires. A nil identity, empty Subject, or empty TenantID is rejected
// with InvalidIdentityError.
func validateIdentity(id *transport.Identity) error {
	if id == nil {
		return &transport.InvalidIdentityError{Reason: "identity is nil"}
	}
	if id.Subject == "" {
		return &transport.InvalidIdentityError{Reason: "subject is empty"}
	}
	if id.TenantID == "" {
		return &transport.InvalidIdentityError{Reason: "tenantID is empty"}
	}
	return nil
}

// Authorize delegates to the wrapped auth.Authorizer.
func (c *rbacClient) Authorize(ctx context.Context, identity *transport.Identity, resource, action string) error {
	if err := c.transport.gate("RBACClient.Authorize"); err != nil {
		return err
	}
	if c.transport.closed.Load() {
		return &transport.ClosedError{}
	}
	if err := validateIdentity(identity); err != nil {
		return err
	}
	if c.transport.rbacAuthz == nil {
		return &MissingRBACAuthorizerError{}
	}
	return c.transport.rbacAuthz.Authorize(ctx, identityToDomain(identity), resource, action)
}

// ListPermissions resolves the identity's effective permissions through
// the role service and projects each one with its scope metadata.
//
// The wrapper does NOT apply the system-tenant rule for filtering — the
// returned set is the union of every role-granted permission, with
// Scope tags that callers can use to filter a UI ("show only what you
// can do here"). The Authorize gate is the canonical admission point;
// this method exists for descriptive UI rendering.
//
// A whole-segment wildcard grant (objstore:*, *:read, *:*) is expanded to
// the registered permissions it matches, each carrying its own declared
// scope, and the result is deduplicated and sorted by name. A wildcard is
// how the authorizer matches a name; it is not a permission and has no scope
// of its own, so it cannot be projected as one. This method used to report
// a wildcard as ScopeSelf — the fail-open default go-qrdb#504 removed — which
// was false for every member of the tenant-only broker data-plane families
// the built-in Admin role grants this way.
func (c *rbacClient) ListPermissions(ctx context.Context, identity *transport.Identity) ([]transport.PermissionInfo, error) {
	if err := c.transport.gate("RBACClient.ListPermissions"); err != nil {
		return nil, err
	}
	if c.transport.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if err := validateIdentity(identity); err != nil {
		return nil, err
	}
	if c.transport.roleSvc == nil {
		return nil, &MissingRoleServiceForRBACError{}
	}
	perms, err := c.transport.roleSvc.ResolvePermissions(ctx, identity.Roles)
	if err != nil {
		return nil, err
	}
	concrete := make(map[permission.Permission]struct{}, len(perms))
	for _, p := range perms {
		for _, member := range permission.Expand(p) {
			concrete[member] = struct{}{}
		}
	}
	names := make([]permission.Permission, 0, len(concrete))
	for p := range concrete {
		names = append(names, p)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	out := make([]transport.PermissionInfo, len(names))
	for i, p := range names {
		info, err := permissionToTransport(p)
		if err != nil {
			return nil, err
		}
		out[i] = info
	}
	return out, nil
}

// RBACClient returns the embedded RBAC client projection. Implements
// transport.RBACProvider.
func (t *Transport) RBACClient() transport.RBACClient {
	return &rbacClient{transport: t}
}

// WithRBACAuthorizer injects an auth.Authorizer into the embedded
// transport. The authorizer is consumed by RBACClient.Authorize.
func WithRBACAuthorizer(authz auth.Authorizer) Option {
	return func(t *Transport) {
		t.rbacAuthz = authz
	}
}

// WithAuthMiddleware installs the AuthMiddleware this transport's gated
// writes authorize through -- the SAME instance the node's network
// transports enforce with, so a role-assignment write (CreateUser's RoleIDs,
// UpdateUser's RoleIDs, RoleBindings and Attributes) needs roles:assign here
// exactly as it does over REST, gRPC, QUIC or MCP. A nil mw (the default)
// authorizes nothing, matching a network transport with no AuthMiddleware;
// role references are validated either way.
func WithAuthMiddleware(mw *authmw.AuthMiddleware) Option {
	return func(t *Transport) {
		t.authMW = mw
	}
}
