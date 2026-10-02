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

package rbac

import "github.com/jeremyhahn/go-qrdb/pkg/auth"

// PermissionDeniedError provides detailed information about a denied
// authorization check. It wraps auth.ErrUnauthorized so that callers
// using errors.Is can match the sentinel.
type PermissionDeniedError struct {
	Resource string
	Action   string
	Subject  string
	// Reason provides additional context for the denial (e.g., SO deny-list).
	// Empty when the denial is a plain permission miss.
	Reason string
}

// Error returns a human-readable description of the denied permission.
// When Reason is non-empty it is appended after the standard message.
func (e *PermissionDeniedError) Error() string {
	msg := "qrdb/auth/rbac: permission denied: " +
		e.Subject + " cannot " + e.Action + " on " + e.Resource
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}

// Unwrap returns auth.ErrUnauthorized so errors.Is works with the sentinel.
func (e *PermissionDeniedError) Unwrap() error { return auth.ErrUnauthorized }

// ResolveError is returned when the role service fails to resolve
// permissions for the given roles.
type ResolveError struct {
	Err error
}

// Error returns a human-readable description of the resolution failure.
func (e *ResolveError) Error() string {
	msg := "qrdb/auth/rbac: failed to resolve permissions"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ResolveError) Unwrap() error { return e.Err }

// InvalidRoleLookupError is returned when a role store answers a lookup by ID
// with neither a role nor an error.
//
// It is a contract violation, not an absence, and the distinction matters on
// the paths that decide whether to void a grant: reading it as "no such role"
// would clear the very grant the lookup exists to catch. The decision denies
// instead.
type InvalidRoleLookupError struct {
	// RoleID is the identifier whose lookup returned nothing at all.
	RoleID string
}

// Error returns a human-readable description of the contract violation.
func (e *InvalidRoleLookupError) Error() string {
	return "qrdb/auth/rbac: role store returned neither a role nor an error for " + e.RoleID
}

// CrossTenantFallbackError is returned when a decision would have consulted
// the StaleRead role fallback for a tenant it does not hold. The fallback is
// confined to the system tenant's identity namespace (see rolesFallback's
// doc comment), so for any other tenant it cannot answer at all — not "no
// such role", which systemRoleIDs and staleMayDenote would otherwise read as
// permission to void or strip a grant. This error is wrapped in a
// *ResolveError, but it is NOT one of resolveErrorIsTransient's allow-listed
// causes (pkg/auth/middleware/failure_class.go): it classifies as
// AuthFailureUnknown, fail closed to whichever class the caller's own
// fallback chooses (401 on an authentication gate, 403 on an authorization
// one), the same as any other contract violation this package reports. It
// is deliberately NOT retryable: reaching this path at all means a gate this
// package already believes should have refused the fallback (see
// systemRoleIDs) somehow did not, which is a defect worth surfacing
// distinctly rather than asking the caller to retry it.
//
// It is reachable only as defense in depth: systemRoleIDs already refuses to
// consult the fallback for a non-system tenant, so staleMayDenote — which
// shares the same fallback — should never be called for one either. See
// resolvePermissionSet, systemRoleIDs and staleMayDenote.
type CrossTenantFallbackError struct {
	// TenantID is the non-system tenant the fallback was about to be asked
	// about.
	TenantID string
}

// Error returns a human-readable description of the refusal.
func (e *CrossTenantFallbackError) Error() string {
	return "qrdb/auth/rbac: StaleRead role fallback is confined to the system tenant; it cannot answer for tenant " + e.TenantID
}
