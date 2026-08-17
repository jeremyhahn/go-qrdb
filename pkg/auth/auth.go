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

// Package auth provides pluggable authentication and authorization for go-qrdb.
// Consumers like go-xkms can inject custom implementations (JWT, adaptive, etc.)
// while go-qrdb provides a default mTLS authenticator.
package auth

import "context"

// Identity represents an authenticated entity.
type Identity struct {
	// Subject identifies the authenticated principal (e.g., CN from cert, JWT sub).
	Subject string

	// TenantID identifies the tenant namespace. Always non-empty for a
	// well-formed identity — the bootstrap flow guarantees every user is
	// created inside a real tenant (system or otherwise). Cross-tenant
	// scope is granted by the system-tenant rule (a role grant in the
	// reserved `system` tenant carries cross-tenant power); no identity
	// uses an empty TenantID as a sentinel.
	TenantID string

	// Roles contains the roles assigned to this identity.
	Roles []string

	// ScopePermissions carries permissions that were pre-resolved by a
	// TRUSTED in-process issuance path — the "resource:action" set stamped
	// into a platform token's scp claim at token issuance, projected here
	// by a capability module's authentication fallback after validating
	// the token. It exists for principals that hold a token but no user
	// record (client-credential/app tokens): store-resolved roles come up
	// empty for them, and these permissions are what the token was issued
	// FOR. The authorizer treats them as an additive grant set alongside
	// role-resolved permissions; the SO deny-list still overrides.
	//
	// SECURITY: this field must only ever be populated in-process from a
	// validated token by the middleware/fallback layer — never
	// deserialized from a wire request and never trusted from a
	// forwarded assertion (authenticateOnBehalf re-resolves identity and
	// does not carry it).
	ScopePermissions []string

	// Metadata holds additional identity attributes.
	Metadata map[string]string
}

// HasRole reports whether this identity has the specified role.
func (id *Identity) HasRole(role string) bool {
	for _, r := range id.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Authenticator validates incoming requests and extracts identity.
type Authenticator interface {
	// Authenticate extracts and validates identity from the request context.
	// Returns ErrUnauthenticated if no valid credentials are found.
	Authenticate(ctx context.Context) (*Identity, error)

	// Name returns the authenticator type (e.g., "mtls", "jwt").
	Name() string
}

// Authorizer checks whether an identity is allowed to perform an action.
type Authorizer interface {
	// Authorize checks if the identity in context may perform the action
	// on the given resource. Returns ErrUnauthorized if denied.
	Authorize(ctx context.Context, identity *Identity, resource, action string) error
}

// NoopAuthorizer permits all actions. Useful for development or single-user deployments.
type NoopAuthorizer struct{}

// Authorize always returns nil (permit all).
func (n *NoopAuthorizer) Authorize(_ context.Context, _ *Identity, _, _ string) error {
	return nil
}
