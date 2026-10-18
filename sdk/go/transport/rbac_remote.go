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

package transport

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// AuthorizeRequest asks the server whether Identity may perform Action on
// Resource in the targeted tenant. Only the identity's subject, tenant and
// roles cross the wire; its metadata never does.
type AuthorizeRequest struct {
	Identity *Identity
	Resource string
	Action   string
}

// AuthorizeDecision is the server's answer to an AuthorizeRequest: allowed,
// or denied with the reason.
type AuthorizeDecision struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

// AuthorizationDeniedError is the denial a network RBACClient.Authorize
// returns when the server's auth chain does not permit the action. It
// matches auth.ErrUnauthorized under errors.Is, as the embedded client's
// denial does.
type AuthorizationDeniedError struct {
	Resource string
	Action   string
	Reason   string
}

// Error returns a human-readable description.
func (e *AuthorizationDeniedError) Error() string {
	msg := "transport: authorization denied for " + e.Resource + ":" + e.Action
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}

// Unwrap returns auth.ErrUnauthorized.
func (e *AuthorizationDeniedError) Unwrap() error { return auth.ErrUnauthorized }

// RBACEndpoints is the server surface a network transport exposes to build
// its RBACClient on. Every decision is the server's: the client never
// evaluates a permission itself.
type RBACEndpoints interface {
	// ResolvePermissions calls the server's role-resolution endpoint,
	// which requires permissions:resolve.
	ResolvePermissions(ctx context.Context, req *ResolvePermissionsRequest) (*ResolvePermissionsResponse, error)

	// ListOwnPermissions calls the server's own-permissions endpoint.
	ListOwnPermissions(ctx context.Context) ([]PermissionInfo, error)

	// EvaluateAuthorization calls the server's Authorize endpoint.
	EvaluateAuthorization(ctx context.Context, req *AuthorizeRequest) (*AuthorizeDecision, error)
}

// remoteRBACClient is the RBACClient every network transport shares.
type remoteRBACClient struct {
	endpoints RBACEndpoints
}

// Compile-time interface assertion.
var _ RBACClient = (*remoteRBACClient)(nil)

// NewRemoteRBACClient returns the RBACClient of a network transport, whose
// every answer comes from the server endpoints e calls.
func NewRemoteRBACClient(e RBACEndpoints) RBACClient {
	return &remoteRBACClient{endpoints: e}
}

// validateIdentity rejects a nil identity or one missing its subject or
// tenant.
func validateIdentity(id *Identity) error {
	if id == nil {
		return &InvalidIdentityError{Reason: "identity is nil"}
	}
	if id.Subject == "" {
		return &InvalidIdentityError{Reason: "subject is empty"}
	}
	if id.TenantID == "" {
		return &InvalidIdentityError{Reason: "tenantID is empty"}
	}
	return nil
}

// Authorize asks the server's Authorize endpoint. It returns nil when the
// server allows the action and an *AuthorizationDeniedError carrying the
// server's reason when it does not; a refused question (unauthenticated, or
// another identity without permissions:resolve) is the endpoint's error.
func (c *remoteRBACClient) Authorize(ctx context.Context, identity *Identity, resource, action string) error {
	if err := validateIdentity(identity); err != nil {
		return err
	}
	decision, err := c.endpoints.EvaluateAuthorization(ctx, &AuthorizeRequest{Identity: identity, Resource: resource, Action: action})
	if err != nil {
		return err
	}
	if !decision.Allowed {
		return &AuthorizationDeniedError{Resource: resource, Action: action, Reason: decision.Reason}
	}
	return nil
}

// ListPermissions resolves the identity's roles through the server's
// ResolvePermissions endpoint, behind its permissions:resolve guard, then
// expands wildcard grants into the concrete permissions they cover, sorted
// by name, each with its declared scope.
func (c *remoteRBACClient) ListPermissions(ctx context.Context, identity *Identity) ([]PermissionInfo, error) {
	if err := validateIdentity(identity); err != nil {
		return nil, err
	}
	resp, err := c.endpoints.ResolvePermissions(ctx, &ResolvePermissionsRequest{RoleIDs: identity.Roles})
	if err != nil {
		return nil, err
	}
	granted := make([]permission.Permission, len(resp.Permissions))
	for i, name := range resp.Permissions {
		granted[i] = permission.Permission(name)
	}
	names := permission.ExpandAll(granted)
	out := make([]PermissionInfo, len(names))
	for i, p := range names {
		info, err := PermissionInfoFromDomain(p)
		if err != nil {
			return nil, err
		}
		out[i] = info
	}
	return out, nil
}

// ListOwnPermissions returns the server's answer for the authenticated
// caller's own effective permissions.
func (c *remoteRBACClient) ListOwnPermissions(ctx context.Context) ([]PermissionInfo, error) {
	return c.endpoints.ListOwnPermissions(ctx)
}
