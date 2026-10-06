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

	// Roles contains the IDENTIFIERS of the roles assigned to this identity.
	// The authentication middleware sets it from the user record's RoleIDs.
	// A role NAME found here did not come from the role store, so to ask
	// whether the identity holds a named system role, resolve the name
	// through the role store (role.HoldsSystemRole) rather than comparing
	// the name here.
	Roles []string

	// RoleBindings carries the identity's TENANT-SCOPED role grants —
	// user.RoleBinding's shape, copied rather than imported: pkg/user
	// already imports pkg/auth, so pkg/auth cannot import pkg/user without
	// closing a cycle. A RoleBinding grants its RoleID ONLY within its own
	// TenantID — the RBAC authorizer (pkg/auth/rbac) resolves a binding into
	// the effective role set exactly when the request's target tenant equals
	// the binding's TenantID, never otherwise. A binding may name only the
	// identity's own tenant (user.NormalizeRoleBindings refuses any other).
	RoleBindings []RoleBinding

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

	// Metadata holds additional identity attributes derived from
	// AUTHENTICATION — the certificate, the token, the assertion that
	// established this identity. Every writer of this map is a trusted,
	// in-process authentication step (see the sealed-window sentinel this
	// package's own middleware uses it for as an example); it must never be
	// populated from a caller-controlled request field.
	Metadata map[string]string

	// Attributes holds ABAC key/value pairs sourced from the USER RECORD's
	// attribute set, which an administrator holding roles:assign writes — a
	// weaker trust level than Metadata's. It is a separate field so it can
	// never be read as Metadata: the ABAC decorator exposes it to policies
	// only in its own user_attrs namespace (see abac.Subject.UserAttributes).
	Attributes map[string]string
}

// RoleBinding grants Role within a tenant scope (tenant-scoped RBAC). It
// mirrors user.RoleBinding's two fields; see Identity.RoleBindings for why
// this package carries its own copy rather than importing that type.
type RoleBinding struct {
	RoleID   string
	TenantID string
}

// HasRole reports whether this identity carries the role identifier roleID,
// compared verbatim. Roles holds identifiers, so passing a role NAME here is
// a defect: it refuses every real holder and admits any identity whose roles
// happen to carry that string (go-qrdb#569). Use role.HoldsSystemRole for a
// named system role.
func (id *Identity) HasRole(roleID string) bool {
	for _, r := range id.Roles {
		if r == roleID {
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

// UnobservableMethodAuthenticator is an Authenticator whose credential carries
// no login method the authenticator chain can observe: a bearer token issued
// by the embedding host after its own login ceremony (WebAuthn, PIV, password,
// ...). The token says who the caller is, not how they logged in, so the chain
// cannot compare it against the account's permitted-method policy
// (user.User.AuthMethods). The issuer can, and must: the host's login
// endpoints enforce the policy when they mint the token, through
// middleware.CheckAuthMethod.
//
// The chain treats an authenticator that reports MethodUnobservable true
// exactly as it treats a capability module's session token: the identity is
// finalized with an unobservable method, so the permitted-method check is
// skipped and certificate-binding enforcement does not apply. Every other
// step -- account resolution, the disabled check, role enrichment, tenant
// binding, authorization -- runs unchanged.
//
// It is opt-in and reported per authenticator. An authenticator that does not
// implement it, or reports false, is observed under its Name() as before; the
// built-in authenticators never implement it.
type UnobservableMethodAuthenticator interface {
	Authenticator

	// MethodUnobservable reports whether the identities this authenticator
	// returns carry no login method the chain can check.
	MethodUnobservable() bool
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
