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

package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
)

// ---------------------------------------------------------------------------
// Auth types
// ---------------------------------------------------------------------------

// Identity represents an authenticated entity carrying its subject, tenant,
// roles, and arbitrary metadata.
type Identity = auth.Identity

// Authenticator validates incoming requests and extracts identity.
type Authenticator = auth.Authenticator

// UnobservableMethodAuthenticator is an Authenticator whose tokens carry no
// login method the chain can check against a user's permitted methods — a
// bearer the host mints after its own login ceremony, which enforces the
// policy at issue (CheckAuthMethod). Register it with node.WithAuthenticators;
// reporting MethodUnobservable true skips only the permitted-method check.
type UnobservableMethodAuthenticator = auth.UnobservableMethodAuthenticator

// Authorizer checks whether an identity is allowed to perform an action on a
// resource.
type Authorizer = auth.Authorizer

// NoopAuthorizer permits all actions. Useful for development or single-user
// deployments.
type NoopAuthorizer = auth.NoopAuthorizer

// PermissionDeniedError is the RBAC authorizer's denial: the identity holds no
// grant for the requested resource and action, or holds one it may not
// exercise (a cross-scope permission outside the system tenant, a
// cryptographic operation on the Security Officer role). Reason is set when
// the denial is more than a plain permission miss.
//
// It is the only error the RBAC authorizer returns that means "denied".
// Anything else — a role-resolution failure, a cancelled context — is the
// authorizer failing to decide, so a consumer tells the two apart with
// errors.As rather than treating every error as a denial. (An authorizer
// wrapped by DecorateAuthorizer adds the ABAC policy's own denial.)
type PermissionDeniedError = rbac.PermissionDeniedError

// MethodMTLS is the name the built-in mTLS authenticator reports, and
// therefore the exact token a user's permitted-method policy
// (CreateUserRequest.AuthMethods / UpdateUserRequest.AuthMethods) must spell
// to allow an mTLS login. It is re-exported so a consumer writing that policy
// never has to guess the spelling the server compares against.
//
// It is also the method the certificate-binding gate applies to: an account
// that has enrolled cert bindings (see AddCertBinding and
// ComputeSPKIThumbprint) authenticates over mTLS only with a certificate whose
// SPKI thumbprint matches one of them.
const MethodMTLS = auth.MethodMTLS

// MTLSBindingPolicy is the fleet-wide mTLS enrolment posture
// (auth.mtls_binding_policy, go-qrdb#517). The zero value is
// MTLSBindingEnrolled.
type MTLSBindingPolicy = auth.MTLSBindingPolicy

// The MTLSBindingPolicy values.
const (
	// MTLSBindingEnrolled enforces a binding only on accounts that have one.
	MTLSBindingEnrolled = auth.MTLSBindingEnrolled
	// MTLSBindingRequired requires every certificate login to present a
	// certificate enrolled on the account it names.
	MTLSBindingRequired = auth.MTLSBindingRequired
)

// ParseMTLSBindingPolicy resolves "enrolled", "required" or empty; anything
// else is an *InvalidMTLSBindingPolicyError.
var ParseMTLSBindingPolicy = auth.ParseMTLSBindingPolicy

// InvalidMTLSBindingPolicyError reports an unrecognised policy spelling.
type InvalidMTLSBindingPolicyError = auth.InvalidMTLSBindingPolicyError

// TenantFromClientCert is qrdb's certificate-to-tenant rule -- the one the
// mTLS authenticator applies to every login -- for a composing host that
// resolves a verified client certificate's user before any tenant is bound
// (a public route, where the middleware authenticates nothing).
//
//   - The SPIFFE tenant SAN wins. Every URI SAN is examined, never just the
//     first: SANs naming more than one distinct tenant are refused
//     (SpiffeAmbiguousTenantError), and under a pin ANY SPIFFE SAN from
//     another trust domain is refused (ErrSpiffeTrustDomainMismatch), as is a
//     malformed one. A refusal is never overridden by the OU.
//   - With no SPIFFE tenant SAN, the first Subject OU is the tenant; with
//     neither, the tenant is "".
//   - Every refusal is a *ClientCertificateTenantError, which matches
//     ErrInvalidClientCertificate under errors.Is and carries the cause.
//
// pinnedTrustDomain == "" ACCEPTS A SPIFFE SAN FROM ANY TRUST DOMAIN. That is
// for tests only. A composer MUST pass the trust domain its CA issues under
// (the same value it configures qrdb with); an unpinned call lets a
// certificate from any SPIFFE issuer the CA pool trusts choose its tenant.
//
// Bind the result exactly as the middleware does, then look the thumbprint up
// within that tenant only:
//
//	tenantID, _, err := sdk.TenantFromClientCert(cert, trustDomain) // never ""
//	if err != nil { /* refuse */ }
//	ctx = sdk.ContextWithTenant(ctx, sdk.NormalizeTenantID(tenantID))
//	u, err := client.Users().GetUserByCertThumbprint(ctx, sdk.ComputeSPKIThumbprint(cert))
//
// (Aliases pkg/auth.TenantFromClientCert.)
var TenantFromClientCert = auth.TenantFromClientCert

// ErrInvalidClientCertificate is what every TenantFromClientCert refusal
// matches under errors.Is: a nil certificate, a SPIFFE SAN outside the pinned
// trust domain, a malformed SPIFFE SAN, or SANs naming more than one tenant.
// (Aliases pkg/auth.ErrInvalidCertificate.)
var ErrInvalidClientCertificate = auth.ErrInvalidCertificate

// ClientCertificateTenantError is TenantFromClientCert's refusal: it matches
// ErrInvalidClientCertificate and wraps the SPIFFE cause.
// (Aliases pkg/auth.CertificateTenantError.)
type ClientCertificateTenantError = auth.CertificateTenantError

// MethodPIV is the permitted-method token for a PIV smart-card login. A layer
// that runs the PIV ceremony enforces the account's policy with
// CheckAuthMethod(u, MethodPIV). It is distinct from MethodMTLS, so a policy
// can permit one without the other.
const MethodPIV = auth.MethodPIV

// MethodWebAuthn is the permitted-method token for a WebAuthn (FIDO2
// authenticator) login. There is no separate FIDO2 token: a FIDO2
// authenticator is presented through the WebAuthn ceremony.
const MethodWebAuthn = auth.MethodWebAuthn

// IsBuiltinAuthMethod reports whether a canonical name is one of qrdb's own
// method tokens (MethodMTLS, MethodPIV, MethodWebAuthn). A node's full
// vocabulary also holds the Name() of every authenticator in its chain; a
// login method a host wants policies to name must therefore be an
// authenticator in that chain (node WithAuthenticators).
var IsBuiltinAuthMethod = auth.IsBuiltinAuthMethod

// BuiltinAuthMethods returns qrdb's own method tokens, sorted.
var BuiltinAuthMethods = auth.BuiltinAuthMethods

// CanonicalAuthMethod is the form method names are stored and compared in —
// trimmed and lowercased, NOT validated — so a stored policy entry predating
// the vocabulary still matches the method it names.
var CanonicalAuthMethod = auth.CanonicalAuthMethod

// CheckAuthMethod enforces a user's permitted-authentication-method policy
// (User.AuthMethods) against the method that authenticated the request. It is
// the same function the qrdb authentication middleware runs, exported so a
// layer that runs its own login ceremony applies the identical rule: an empty
// policy (or a nil user) states no restriction; otherwise the method and each
// stored entry are compared in CanonicalAuthMethod form (trimmed, lowercased,
// NOT checked against the vocabulary, so a legacy entry still matches), and a
// method matching no entry — including a blank one — fails closed with
// *AuthMethodNotPermittedError.
var CheckAuthMethod = authmw.CheckAuthMethod

// AuthMethodNotPermittedError is returned by CheckAuthMethod, and by the
// authentication middleware on every transport, when an account authenticated
// successfully by a method its own policy does not permit. It names the
// refused method and the permitted set. Transports render it as 403 /
// PermissionDenied: the credential is valid, the way in is not.
type AuthMethodNotPermittedError = authmw.AuthMethodNotPermittedError

// ---------------------------------------------------------------------------
// Auth context helpers
// ---------------------------------------------------------------------------

// WithIdentity stores an Identity in the context.
var WithIdentity = auth.WithIdentity

// WithTLSState stores the mTLS connection state on the context. It is the ONE
// carrier the stack uses to hand a verified peer certificate from a transport
// down to the code that binds an operation to the caller's certificate — most
// notably the Security Officer share claim. Transports that terminate TLS
// themselves (raw-socket MCP, unix) must install it; HTTP-shaped transports
// read *http.Request.TLS directly.
var WithTLSState = auth.WithTLSState

// TLSStateFromContext retrieves the mTLS connection state stored by
// WithTLSState. It returns nil when the context carries none, so callers fail
// closed.
var TLSStateFromContext = auth.TLSStateFromContext

// IdentityFromContext retrieves the Identity from context.
// Returns nil if no identity is stored.
var IdentityFromContext = auth.IdentityFromContext

// IdentityIsSystemTenant reports whether identity is scoped to the
// structurally privileged system tenant. A nil identity is treated as
// system-scoped (no auth context implies an internal, unauthenticated
// pipeline such as platform boot). A non-nil identity delegates to
// IsSystemTenant(identity.TenantID) — the single source of truth for the
// string-level comparison. (Aliases rbac.IsSystemTenant.)
var IdentityIsSystemTenant = rbac.IsSystemTenant
