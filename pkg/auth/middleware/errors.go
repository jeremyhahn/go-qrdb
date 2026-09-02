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

import "strings"

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

// AuditError is returned when the audit logger fails to record an entry.
// The original request is not blocked; this error is logged for
// observability.
type AuditError struct {
	Err error
}

// Error returns a human-readable description of the audit failure.
func (e *AuditError) Error() string {
	return "auth/middleware: audit log failed: " + e.Err.Error()
}

// Unwrap returns the underlying audit error.
func (e *AuditError) Unwrap() error {
	return e.Err
}

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
