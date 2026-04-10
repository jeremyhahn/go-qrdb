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

// UserDisabledError is returned when an authenticated user account is
// disabled. The middleware rejects the request even though the credentials
// are valid.
type UserDisabledError struct {
	Username string
	TenantID string
}

// Error returns a human-readable description of the disabled condition.
func (e *UserDisabledError) Error() string {
	return "auth/middleware: user disabled: " + e.Username + " in tenant " + e.TenantID
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
