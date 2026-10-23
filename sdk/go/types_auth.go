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
	"github.com/jeremyhahn/go-qrdb/pkg/role"
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

// ChainPosition is where an authenticator registered with
// node.WithAuthenticators sits in the node's one first-success-wins chain,
// relative to the built-in mTLS authenticator. The zero value is not a
// position: every registration names one. (Aliases pkg/auth.ChainPosition.)
type ChainPosition = auth.ChainPosition

// The ChainPosition values.
const (
	// ChainBeforeMTLS runs the authenticators ahead of mTLS: they decide
	// every request they accept, including one that also presents a valid
	// client certificate.
	ChainBeforeMTLS = auth.ChainBeforeMTLS
	// ChainAfterMTLS runs them after mTLS: a request with a valid client
	// certificate is decided by mTLS.
	ChainAfterMTLS = auth.ChainAfterMTLS
	// ChainReplaceMTLS removes the mTLS authenticator and puts them in its
	// place. The transport's TLS stays: a presented client certificate is
	// still verified and its state still reaches the chain
	// (TLSStateFromContext); it just authenticates nobody by itself.
	ChainReplaceMTLS = auth.ChainReplaceMTLS
)

// InvalidChainPositionError reports a registration whose position is not one
// of the ChainPosition values. (Aliases pkg/auth.InvalidChainPositionError.)
type InvalidChainPositionError = auth.InvalidChainPositionError

// EmptyAuthenticatorChainError reports ChainReplaceMTLS with no authenticator
// anywhere in the chain to take mTLS's place.
// (Aliases pkg/auth.EmptyAuthenticatorChainError.)
type EmptyAuthenticatorChainError = auth.EmptyAuthenticatorChainError

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

// RBACAuthorizer is qrdb's role-based Authorizer: the one policy decision
// point every transport runs. A layer that authorizes its own requests (go-xkms
// key operations) constructs it with NewRBACAuthorizer and gets the server's
// exact decision, never a fork of it. (Aliases pkg/auth/rbac.RBACAuthorizer.)
type RBACAuthorizer = rbac.RBACAuthorizer

// NewRBACAuthorizer returns an RBACAuthorizer that resolves each identity's
// permissions through roles. (Aliases pkg/auth/rbac.New.)
var NewRBACAuthorizer = rbac.New

// RBACOption configures an RBACAuthorizer. (Aliases pkg/auth/rbac.Option.)
type RBACOption = rbac.Option

// WithRBACCacheTTL sets how long resolved permissions are cached; the default
// is 30 seconds. (Aliases pkg/auth/rbac.WithCacheTTL.)
var WithRBACCacheTTL = rbac.WithCacheTTL

// WithRBACStrictSeparation enables strict separation of duties, under which
// the Owner role is not honored. The daemon enables it in FIPS mode.
// (Aliases pkg/auth/rbac.WithStrictSeparation.)
var WithRBACStrictSeparation = rbac.WithStrictSeparation

// WithRBACRoleServiceFallback installs a stale-read role store consulted for
// the system tenant when the linearizable resolve fails transiently.
// (Aliases pkg/auth/rbac.WithRoleServiceFallback.)
var WithRBACRoleServiceFallback = rbac.WithRoleServiceFallback

// WithRBACScopeResolver installs a tenant-aware PermissionScopeResolver
// (typically a *TenantScopeStore), so each permission's scope resolves in the
// identity's own tenant. (Aliases pkg/auth/rbac.WithScopeResolver.)
var WithRBACScopeResolver = rbac.WithScopeResolver

// RBACResolveError is the RBAC authorizer failing to decide: the role service
// could not resolve the identity's permissions. It is not a denial; a caller
// tells it apart from PermissionDeniedError with errors.As.
// (Aliases pkg/auth/rbac.ResolveError.)
type RBACResolveError = rbac.ResolveError

// RoleServicer is the role store NewRBACAuthorizer resolves permissions
// through. (Aliases pkg/role.RoleServicer.)
type RoleServicer = role.RoleServicer

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

// NewMTLSAuthenticator returns the built-in mTLS Authenticator, which derives
// the identity from the verified client certificate the TLS layer accepted.
// It pins no SPIFFE trust domain; NewMTLSAuthenticatorWithTrustDomain, which
// does, is the one a production composer uses. (Aliases
// pkg/auth.NewMTLSAuthenticator.)
var NewMTLSAuthenticator = auth.NewMTLSAuthenticator

// NewMTLSAuthenticatorWithTrustDomain returns the built-in mTLS Authenticator
// pinned to the cluster's SPIFFE trust domain. (Aliases
// pkg/auth.NewMTLSAuthenticatorWithTrustDomain.)
var NewMTLSAuthenticatorWithTrustDomain = auth.NewMTLSAuthenticatorWithTrustDomain

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
// A tenant is a SPIFFE trust domain: a tenant's principal is
// spiffe://<tenant>/<kind>/<name>, kind user or svc.
//
//   - A certificate names a tenant only when its first Subject OU and the
//     trust domain of its workload SPIFFE ID SAN are both present and agree.
//     There is no fallback in either direction: an OU alone
//     (ErrSpiffeNoTenantID) or a SPIFFE ID alone names no tenant, and an OU
//     naming a different tenant from the trust domain, which is how a foreign
//     trust domain presents, is refused
//     (ClientCertificateTenantNameMismatchError).
//   - clusterTrustDomain is the cluster's own trust domain. A certificate
//     with no OU must be under it (ErrNoClientCertificateTenantOU) or is
//     foreign (ErrSpiffeTrustDomainMismatch), and no tenant may be it
//     (ErrClientCertificateTenantIsClusterTrustDomain).
//   - Every URI SAN is examined, never just the first: SANs under more than
//     one trust domain are refused (SpiffeAmbiguousTenantError), as is a
//     malformed one.
//   - Every refusal is a *ClientCertificateTenantError, which matches
//     ErrInvalidClientCertificate under errors.Is and carries the cause.
//
// clusterTrustDomain == "" skips the two cluster checks. That is for tests
// only: a composer MUST pass the trust domain its CA issues under (the same
// value it configures qrdb with).
//
// Bind the result exactly as the middleware does, then look the thumbprint up
// within that tenant only:
//
//	tenantID, err := sdk.TenantFromClientCert(cert, trustDomain) // never "" on success
//	if err != nil { /* refuse */ }
//	ctx = sdk.ContextWithTenant(ctx, sdk.NormalizeTenantID(tenantID))
//	u, err := client.Users().GetUserByCertThumbprint(ctx, sdk.ComputeSPKIThumbprint(cert))
//
// (Aliases pkg/auth.TenantFromClientCert.)
var TenantFromClientCert = auth.TenantFromClientCert

// ErrInvalidClientCertificate is what every TenantFromClientCert refusal
// matches under errors.Is: a nil certificate, no tenant SPIFFE ID SAN, no
// Subject OU, an OU and SPIFFE ID naming different tenants, a SPIFFE SAN
// outside the pinned trust domain, a malformed SPIFFE SAN, or SANs naming
// more than one tenant.
// (Aliases pkg/auth.ErrInvalidCertificate.)
var ErrInvalidClientCertificate = auth.ErrInvalidCertificate

// ClientCertificateTenantError is TenantFromClientCert's refusal: it matches
// ErrInvalidClientCertificate and wraps the cause -- a SPIFFE error,
// ErrNoClientCertificateTenantOU or a
// *ClientCertificateTenantNameMismatchError.
// (Aliases pkg/auth.CertificateTenantError.)
type ClientCertificateTenantError = auth.CertificateTenantError

// ErrNoClientCertificateTenantOU is the cause a *ClientCertificateTenantError
// carries when the certificate has a tenant SPIFFE ID SAN but no Subject OU:
// the OU is one of the two fields that must both name the tenant.
// (Aliases pkg/auth.ErrNoTenantOU.)
var ErrNoClientCertificateTenantOU = auth.ErrNoTenantOU

// ErrClientCertificateTenantIsClusterTrustDomain is the cause a
// *ClientCertificateTenantError carries when the certificate's tenant is the
// cluster trust domain, which names no tenant. An issuer refuses the same
// identity before it signs. (Aliases pkg/auth.ErrTenantIsClusterTrustDomain.)
var ErrClientCertificateTenantIsClusterTrustDomain = auth.ErrTenantIsClusterTrustDomain

// ClientCertificateTenantNameMismatchError is the cause a
// *ClientCertificateTenantError carries when the certificate's first Subject
// OU and its SPIFFE ID SAN name different tenants. Neither is preferred: the
// certificate names no tenant.
// (Aliases pkg/auth.TenantNameMismatchError.)
type ClientCertificateTenantNameMismatchError = auth.TenantNameMismatchError

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

// ServicePrincipalSubjectPrefix is the reserved Subject namespace of a
// node-sanctioned service principal (node.WithServicePrincipal): the principal
// named "xkms" is audited as "qrdb-service:xkms". No network transport accepts
// a subject in this namespace. (Aliases pkg/auth.ServicePrincipalSubjectPrefix.)
const ServicePrincipalSubjectPrefix = auth.ServicePrincipalSubjectPrefix

// IsServicePrincipal reports whether an identity is, or claims to be, a
// service principal. A host authenticator never has to call it: the
// authentication middleware refuses such an identity on every network
// transport. (Aliases pkg/auth.IsServicePrincipal.)
var IsServicePrincipal = auth.IsServicePrincipal

// ServicePrincipalPresentedError is the refusal every network transport gives
// a presented identity that claims a service principal, whatever credential
// produced it. It matches ErrUnauthenticated under errors.Is, so transports
// answer 401 / Unauthenticated.
type ServicePrincipalPresentedError = authmw.ServicePrincipalPresentedError

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

// HTTPAuthorizationFromContext returns the caller's raw Authorization value
// (for example "Bearer eyJ..."), or "" when the request carried none. It is
// the one credential carrier every network transport fills: REST and MCP from
// the Authorization header, gRPC and Unix from the "authorization" metadata.
// An authenticator registered with node.WithAuthenticators reads the bearer
// here and works on all four unchanged. Native QUIC frames carry no
// credential.
var HTTPAuthorizationFromContext = auth.HTTPAuthorizationFromContext

// DPoPRequest is the RFC 9449 request metadata a proof-of-possession
// authenticator verifies against: the raw DPoP header, and the HTTP method and
// target URI (without query) the proof must be bound to.
type DPoPRequest = auth.DPoPRequest

// DPoPRequestFromContext returns the request's DPoP metadata; the bool is
// false when the request carried no proof. REST and MCP fill it from the HTTP
// request, gRPC and Unix from the "dpop" metadata. Native QUIC, embedded and
// MCP stdio have no HTTP request, so DPoP does not apply there. qrdb carries
// the data only and never verifies the proof.
var DPoPRequestFromContext = auth.DPoPRequestFromContext

// ErrUnauthenticated is what an Authenticator returns when the request
// carries no credential it accepts. The chain then tries the next
// authenticator, and when none succeeds every transport answers its
// unauthenticated status (401, Unauthenticated).
var ErrUnauthenticated = auth.ErrUnauthenticated

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
