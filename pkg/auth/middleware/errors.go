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
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// UserDisabledError is returned when an authenticated user account is not in a
// lifecycle state that permits authentication. The middleware rejects the
// request even though the credentials are valid.
//
// The name is kept for compatibility with the callers that already match it,
// but the condition it reports is broader than "disabled": a pending, suspended,
// locked or deleting account fails the same gate, and an operator reading a log
// line needs to know which — a lockout is cleared by the control that set it, a
// suspension by a human, and a pending account by redeeming its invitation.
// Lifecycle carries that.
type UserDisabledError struct {
	Username string
	TenantID string

	// Lifecycle is the state the account was in when the gate refused it. It is
	// empty only for a record that carried no state at all.
	Lifecycle string
}

// Error returns a human-readable description of the refusal, naming the state
// when one is known.
func (e *UserDisabledError) Error() string {
	msg := "auth/middleware: user cannot authenticate: " + e.Username + " in tenant " + e.TenantID
	if e.Lifecycle != "" {
		msg += " (lifecycle " + e.Lifecycle + ")"
	}
	return msg
}

// AuthChainError is returned when every authenticator in the chain fails.
// It wraps the last error encountered from the authenticator chain to
// provide context about why authentication failed.
type AuthChainError struct {
	Err error
}

// Error returns a human-readable description of the chain failure.
func (e *AuthChainError) Error() string {
	if e.Err != nil {
		return "auth/middleware: all authenticators failed: " + e.Err.Error()
	}
	return "auth/middleware: all authenticators failed"
}

// Unwrap returns the last error from the authenticator chain.
func (e *AuthChainError) Unwrap() error {
	return e.Err
}

// StoreUnavailableError is returned when the user store cannot be reached
// due to a transient error (e.g. Raft ReadIndex not ready during startup).
// This maps to HTTP 503 / gRPC UNAVAILABLE to signal "try again later"
// rather than "access denied".
type StoreUnavailableError struct {
	Err error
}

// Error returns a human-readable description of the store unavailability.
func (e *StoreUnavailableError) Error() string {
	return "auth/middleware: user store unavailable: " + e.Err.Error()
}

// Unwrap returns the underlying store error.
func (e *StoreUnavailableError) Unwrap() error {
	return e.Err
}

// BarrierSealedError is returned when the user lookup met a sealed node
// barrier: go-quicraft's seal.ErrBarrierSealed on a node re-sealed at
// runtime, or qrdb's seal.ErrSealed. That is the node's sealed state, not a
// store outage, so it is reported as such rather than as
// *StoreUnavailableError. It maps to HTTP 503 / gRPC UNAVAILABLE: the request
// succeeds once the barrier is unsealed.
type BarrierSealedError struct {
	Err error
}

// Error returns a human-readable description of the sealed barrier.
func (e *BarrierSealedError) Error() string {
	return "auth/middleware: barrier is sealed: " + e.Err.Error()
}

// Unwrap returns the barrier's sealed sentinel.
func (e *BarrierSealedError) Unwrap() error {
	return e.Err
}

// The audit-failure error type and the audit-failure POSTURE both live in
// pkg/audit now, not here (go-qrdb#445).
//
// What was *middleware.AuditError is *audit.GateError, and what was
// middleware.AuditFailurePolicy is audit.FailurePolicy. The move is not
// cosmetic: pkg/auth/middleware imports pkg/auth, so the security-officer
// factor in pkg/auth could never have read a posture owned here, and that
// import edge is the whole reason the SO gate ran on a weaker posture than the
// authorization gate it sits behind. One posture, one type, one operator
// lever, both gates.
//
// *audit.GateError's Component field says WHICH gate refused ("authorize",
// "so-auth", "so-dual-factor"); ClassifyAuthError maps every one of them to
// AuthFailureAuditUnavailable, so REST answers 503 and gRPC codes.Unavailable
// whichever gate produced it -- the same statuses AuthFailureUnavailable
// carries, though the two are no longer one class (go-qrdb#460).

// InvalidPublicGRPCMethodError is returned by RegisterPublicGRPCMethods when
// a declared method name does not have the full gRPC method name shape
// ("/package.Service/Method") that grpc.UnaryServerInfo.FullMethod and
// grpc.StreamServerInfo.FullMethod use. Rejecting the malformed name at
// registration (server startup) makes a typo in a module's
// apimodule.GRPCPublicMethodLister declaration fail loudly instead of
// silently leaving the intended RPC auth-gated.
type InvalidPublicGRPCMethodError struct {
	Method string
}

// Error returns a human-readable description of the malformed method name.
func (e *InvalidPublicGRPCMethodError) Error() string {
	return "auth/middleware: invalid public gRPC method name (want \"/package.Service/Method\"): " + e.Method
}

// PublicMCPToolRefusedError is returned by MCPToolRegistry.RegisterPublic when
// the tool declared public carries a permission that reaches data or changes
// the node. Refusing it at registration (server startup) means no
// declaration, mistaken or deliberate, can serve a data verb to an
// unauthenticated caller.
type PublicMCPToolRefusedError struct {
	Tool       string
	Permission permission.Permission
}

// Error names the tool and the permission that disqualifies it.
func (e *PublicMCPToolRefusedError) Error() string {
	return "auth/middleware: mcp tool " + e.Tool + " requires " + string(e.Permission) +
		" and cannot be public: only tools that reach no data may be served without credentials"
}

// AuthMethodNotPermittedError is returned when an account authenticated
// successfully by a method its own policy does not permit.
//
// It is distinct from UserDisabledError because the account is in perfectly
// good standing: the credential is valid and the lifecycle allows a login. What
// failed is narrower — this WAY in is not one the account may use — and an
// operator reading the log needs to see which method was refused against which
// permitted set to know whether the policy or the client is wrong.
type AuthMethodNotPermittedError struct {
	Username string
	TenantID string

	// Method is the authenticator that succeeded (e.g. "mtls").
	Method string

	// Permitted is the account's policy, in the order it is stored.
	Permitted []string
}

// Error returns a human-readable description naming the refused method and the
// permitted set.
func (e *AuthMethodNotPermittedError) Error() string {
	return "auth/middleware: authentication method " + e.Method +
		" is not permitted for " + e.Username + " in tenant " + e.TenantID +
		" (permitted: " + strings.Join(e.Permitted, ", ") + ")"
}

// CertBindingNotPermittedError is returned when a client certificate names an
// account that has enrolled mTLS certificate bindings and the presented
// certificate matches none of them.
//
// It is an AUTHENTICATION failure, not an authorization one, and that is the
// difference from AuthMethodNotPermittedError above it. There the credential
// IS the account's and the WAY in is refused; here the credential was never
// the account's, so the caller has not established who it is. The
// certificate's Subject CN and OU say one thing, the account's enrolled public
// keys say another, and the enrolment wins.
//
// PresentedThumbprint is the SPKI thumbprint of the certificate that was
// refused. It is a hash of a public key the caller itself presented, so it is
// not a secret, and it is exactly the value an operator needs in order to
// enrol that certificate when the refusal turns out to be a missed enrolment
// rather than an impersonation.
type CertBindingNotPermittedError struct {
	Username string
	TenantID string

	// PresentedThumbprint is the canonical SPKI thumbprint of the refused
	// certificate, or empty when no peer certificate could be read at all.
	PresentedThumbprint string
}

// Error returns a human-readable description naming the account and the
// thumbprint that was refused.
func (e *CertBindingNotPermittedError) Error() string {
	presented := e.PresentedThumbprint
	if presented == "" {
		presented = "<no peer certificate>"
	}
	return "auth/middleware: presented certificate " + presented +
		" is not bound to " + e.Username + " in tenant " + e.TenantID
}

// CertBindingRequiredError is returned under auth.mtls_binding_policy
// "required" (go-qrdb#517) when a certificate login names an account that has
// enrolled NO certificate binding, or names no account at all.
//
// Like CertBindingNotPermittedError it is an AUTHENTICATION failure: the
// posture says possession of an enrolled key is the only way a certificate
// establishes who the caller is, and this caller has no enrolled key to have
// possessed. Enrolling PresentedThumbprint on the account — or switching the
// fleet back to "enrolled" — is what turns the refusal into an admission, and
// the error says so, because the most likely cause is a missed enrolment during
// a migration rather than an attack.
type CertBindingRequiredError struct {
	Username string
	TenantID string

	// AccountResolved is true when the account exists and has no bindings,
	// false when the certificate names no account in the user store.
	AccountResolved bool

	// PresentedThumbprint is the canonical SPKI thumbprint of the refused
	// certificate, or empty when no peer certificate could be read.
	PresentedThumbprint string
}

// Error names the account, why it was refused, and what to enrol.
func (e *CertBindingRequiredError) Error() string {
	presented := e.PresentedThumbprint
	if presented == "" {
		presented = "<no peer certificate>"
	}
	why := " names no account"
	if e.AccountResolved {
		why = " has no enrolled certificate binding"
	}
	return "auth/middleware: auth.mtls_binding_policy is \"required\" and " + e.Username +
		" in tenant " + e.TenantID + why + " (presented " + presented +
		"; enrol it with users:add_cert_binding, or set the policy to \"enrolled\")"
}

// PeerAttestationConfigError is returned by NewPeerAttestationGate for a
// policy that does not describe a gate (go-qrdb#278).
type PeerAttestationConfigError struct {
	Reason string
}

// Error names what is missing.
func (e *PeerAttestationConfigError) Error() string {
	return "auth/middleware: invalid peer attestation gate: " + e.Reason
}

// PeerAttestationRefusedError is returned by authorize when the
// attested-caller gate refuses a request (go-qrdb#278): the peer is who it
// claims to be and RBAC granted the operation, but its measured state is not
// currently acceptable -- or could not be shown to be.
//
// It is not an authentication failure and must not read as one. Reason is the
// stable refusal class; SecurityRelevant is true only when the evidence was
// present and wrong (or presented for a different peer, or dated in the
// future), never for absent or stale evidence. A verifier error classifies as
// unavailable (retryable, 503); every other refusal as forbidden (403).
type PeerAttestationRefusedError struct {
	Subject          string
	TenantID         string
	Permission       permission.Permission
	Reason           PeerAttestationReason
	SecurityRelevant bool

	// Detail is the verifier's own account of the failed checks, when it gave
	// one.
	Detail string

	// Err is the underlying failure, when there was one.
	Err error
}

// Error names the caller, the operation and the reason.
func (e *PeerAttestationRefusedError) Error() string {
	msg := "auth/middleware: peer " + e.Subject + " in tenant " + e.TenantID +
		" is not attested for " + e.Permission.String() + ": " + string(e.Reason)
	if e.Detail != "" {
		msg += " (" + e.Detail + ")"
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying failure.
func (e *PeerAttestationRefusedError) Unwrap() error {
	return e.Err
}

// RateLimitedError is returned by authorize (go-qrdb#201) when the caller has
// exceeded the Policy declared for the permission it is invoking. It carries
// enough for a transport to answer a caller that can act on the refusal: the
// permission it was throttled on and how long to wait before trying again,
// the same shape #231 established for RequestTooLargeError and
// PayloadTooLargeError -- a caller told only "denied" cannot choose to wait
// the right amount, or to check whether it is even the operation it thinks
// is failing.
type RateLimitedError struct {
	// Permission is the operation the caller was throttled on.
	Permission permission.Permission

	// Subject and TenantID identify the caller whose bucket was exhausted.
	Subject  string
	TenantID string

	// RetryAfter is the engine's own estimate of the minimum wait before the
	// next request against this permission would be admitted.
	RetryAfter time.Duration
}

// Error returns a human-readable description of the refusal.
func (e *RateLimitedError) Error() string {
	return "auth/middleware: rate limit exceeded for " + e.Subject + " in tenant " + e.TenantID +
		" on " + e.Permission.String() + "; retry after " + e.RetryAfter.String()
}

// CertSerialUnresolvableError reports that a revocation could not be
// authorized because this node had no way to establish which tenant owns the
// serial.
//
// It is a WIRING fault, not a caller fault, which is why it classifies as
// unavailable rather than forbidden: telling an entitled operator it lacks
// permission would send it to fix a grant that was never the problem. The
// request is still refused -- an unknown target is not an absent one.
type CertSerialUnresolvableError struct {
	// Serial is the certificate serial whose owning tenant could not be
	// established.
	Serial string
}

// Error returns a human-readable description of the refusal.
func (e *CertSerialUnresolvableError) Error() string {
	return "auth/middleware: cannot authorize revocation of serial " + e.Serial +
		": no certificate provider is wired to resolve its owning tenant"
}

// HolderIdentityNotPermittedError is returned when a holder certificate
// (auth.IsHolderIdentity: an officer's or custodian's, signed by the
// ceremony CA) authenticates successfully against a route that requires
// authentication but checks no specific permission (RESTRouteAuthenticated
// -- e.g. GET /metrics) or its gRPC/MCP equivalent (go-qrdb#560 review).
//
// A holder identity carries no roles by design (pkg/auth/middleware's own
// finalizeIdentity: it proves who holds a share, never a user login) and
// is admitted ONLY for the specific IdentityGated verbs its ceremony
// enrolment grants (share claim begin/complete, quorum begin/submit),
// which check the holder binding directly rather than through RBAC.
// RESTRouteAuthenticated/its equivalents have no permission to check at
// all, so without this refusal a holder certificate -- never meant to
// reach anything outside its own narrow verb set -- would pass them like
// any other authenticated peer. The credential is valid; this WAY in is
// simply not one a holder certificate may use, and retrying it will never
// work (matching AuthMethodNotPermittedError's own reasoning).
type HolderIdentityNotPermittedError struct {
	// Method and Path name the refused request, for the operator-facing
	// message; neither is a secret.
	Method string
	Path   string
}

// Error returns a human-readable description of the refusal.
func (e *HolderIdentityNotPermittedError) Error() string {
	return "auth/middleware: a holder certificate may not reach " + e.Method + " " + e.Path +
		": holder certificates are admitted only for their own ceremony verbs"
}

// ServicePrincipalPresentedError is returned when an identity authenticated
// over a network transport is, or claims to be, a node-sanctioned service
// principal (auth.IsServicePrincipal): its Subject is in the reserved
// auth.ServicePrincipalSubjectPrefix namespace, or an authenticator marked it
// with auth.ServicePrincipalMetadataKey. That principal is granted by the
// node to calls made in its own process (sdk/go/node Node.ServiceContext) and
// is never accepted from a request, whatever credential produced the claim --
// a certificate's common name, a bearer token, a forwarded on-behalf
// assertion, a user record. The single exception is a tenant-issued
// delegation the mTLS authenticator verified (auth.DelegationOf), which never
// produces this error.
//
// It matches auth.ErrUnauthenticated under errors.Is and errors.As, so every
// transport answers its unauthenticated status (401, Unauthenticated): the
// caller has not established an identity this node accepts.
type ServicePrincipalPresentedError struct {
	// Subject is the presented subject, for the operator-facing message.
	Subject string
}

// Error names the refused subject.
func (e *ServicePrincipalPresentedError) Error() string {
	return "auth/middleware: identity " + strconv.Quote(e.Subject) +
		" claims a service principal, which no network transport accepts"
}

// Unwrap exposes auth.ErrUnauthenticated.
func (e *ServicePrincipalPresentedError) Unwrap() error { return auth.ErrUnauthenticated }

// DelegationUnauditedError is the cause a delegated request is refused with
// when the middleware has no audit logger: every use of a tenant-issued
// delegation is recorded in the delegating tenant's trail, so with nowhere to
// record it the request is not served. It reaches the caller wrapped in an
// *audit.GateError.
type DelegationUnauditedError struct{}

// Error says why the request was refused.
func (e *DelegationUnauditedError) Error() string {
	return "auth/middleware: no audit logger is wired, and a delegated request is never served unaudited"
}

// UnresolvedUserSubjectError is returned when an identity names a user
// account by its immutable id -- its UserID is set, or its Subject has the
// form auth.UserSubject renders -- and no account with that id exists in the
// identity's tenant. A user-id Subject is set only by auth.Identity.BindUser
// on a resolved account, so an unresolved identity carrying one would pass
// for that account to every reader keyed on the Subject (the audit actor, the
// rate-limit bucket, an import session's owner); it is refused instead.
//
// It matches auth.ErrUnauthenticated under errors.Is and errors.As, so every
// transport answers its unauthenticated status (401, Unauthenticated).
type UnresolvedUserSubjectError struct {
	// Subject is the presented subject.
	Subject string

	// TenantID is the tenant the account was looked up in.
	TenantID string
}

// Error names the subject and tenant.
func (e *UnresolvedUserSubjectError) Error() string {
	return "auth/middleware: identity " + strconv.Quote(e.Subject) + " in tenant " + e.TenantID +
		" names a user id that resolves to no account"
}

// Unwrap exposes auth.ErrUnauthenticated.
func (e *UnresolvedUserSubjectError) Unwrap() error { return auth.ErrUnauthenticated }

// FirstInitCertificateMismatchError is returned when a tenant barrier's
// trust-on-first-init window is reached with a certificate other than the one
// the tenant's first admin was onboarded with. Only that admin may initialize
// the barrier of a tenant onboarded before it existed.
type FirstInitCertificateMismatchError struct {
	TenantID            string
	Username            string
	PresentedThumbprint string
}

// Error names the tenant and the onboarded admin.
func (e *FirstInitCertificateMismatchError) Error() string {
	presented := e.PresentedThumbprint
	if presented == "" {
		presented = "<no peer certificate>"
	}
	return "auth/middleware: tenant " + e.TenantID + "'s barrier may be initialized only with the certificate its first admin " +
		e.Username + " was onboarded with (presented " + presented + ")"
}

// FirstInitNotOnboardedError is returned under auth.mtls_binding_policy
// "required" when a tenant barrier's trust-on-first-init window is reached for
// a tenant onboarded with no first admin: no certificate is bound to it, and
// the posture admits no unbound certificate. Onboard the tenant with its first
// admin (tenant create with an admin CSR or certificate).
type FirstInitNotOnboardedError struct {
	TenantID string
}

// Error names the tenant.
func (e *FirstInitNotOnboardedError) Error() string {
	return "auth/middleware: auth.mtls_binding_policy is \"required\" and tenant " + e.TenantID +
		" was onboarded with no first admin, so no certificate may initialize its barrier"
}
