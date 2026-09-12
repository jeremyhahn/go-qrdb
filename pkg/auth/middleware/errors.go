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
	"strings"
	"time"

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
// AuthFailureUnavailable, so REST answers 503 and gRPC codes.Unavailable
// whichever gate produced it.

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
