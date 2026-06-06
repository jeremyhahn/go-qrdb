// Copyright 2026 Jeremy Hahn
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

// ---------------------------------------------------------------------------
// Phase 3 typed errors
// ---------------------------------------------------------------------------
//
// All error types declared in this file are surfaced by the new SDK
// accessors added in Phase 3 (Permissions, Factors, Login, Sessions,
// RBAC, VolumePools). Every error follows the project convention of a
// concrete struct type plus an Error() method — callers identify
// failure modes via errors.As.

// PermissionNotFoundError is returned by PermissionClient.Lookup when the
// requested permission is not in the registry.
type PermissionNotFoundError struct {
	Name string
}

// Error returns a human-readable description.
func (e *PermissionNotFoundError) Error() string {
	return "transport: permission not found: " + e.Name
}

// InvalidIdentityError is returned by RBACClient and SessionClient when
// the supplied identity is nil or otherwise structurally invalid (empty
// Subject, empty TenantID).
type InvalidIdentityError struct {
	Reason string
}

// Error returns a human-readable description.
func (e *InvalidIdentityError) Error() string {
	return "transport: invalid identity: " + e.Reason
}

// FactorTypeUnsupportedError is returned by FactorClient when the
// requested factor type is outside the canonical set (mtls, fido2, piv).
type FactorTypeUnsupportedError struct {
	Type string
}

// Error returns a human-readable description.
func (e *FactorTypeUnsupportedError) Error() string {
	return "transport: factor type unsupported: " + e.Type
}

// FactorNotFoundError is returned by FactorClient when a factor lookup
// by id yields no record.
type FactorNotFoundError struct {
	FactorID string
}

// Error returns a human-readable description.
func (e *FactorNotFoundError) Error() string {
	return "transport: factor not found: " + e.FactorID
}

// FactorAlreadyEnrolledError is returned by FactorClient.Enroll when the
// (Type, Credential) tuple is already bound to an existing factor.
type FactorAlreadyEnrolledError struct {
	Type     FactorType
	FactorID string
}

// Error returns a human-readable description.
func (e *FactorAlreadyEnrolledError) Error() string {
	return "transport: factor credential already enrolled: " + e.FactorID
}

// FactorVerificationError is returned by FactorClient.Verify when the
// hardware proof does not validate against the enrolled credential.
type FactorVerificationError struct {
	FactorID string
	Reason   string
}

// Error returns a human-readable description.
func (e *FactorVerificationError) Error() string {
	if e.Reason == "" {
		return "transport: factor verification failed: " + e.FactorID
	}
	return "transport: factor verification failed: " + e.FactorID + ": " + e.Reason
}

// InvalidBearerError is returned by LoginClient.ResolveIdentity and
// SessionClient.ResolveIdentity when the bearer is unknown, expired,
// or revoked.
type InvalidBearerError struct {
	// Reason describes the failure mode (e.g. "expired", "revoked",
	// "not found"). Empty for unknown bearers.
	Reason string
}

// Error returns a human-readable description.
func (e *InvalidBearerError) Error() string {
	if e.Reason == "" {
		return "transport: invalid bearer"
	}
	return "transport: invalid bearer: " + e.Reason
}

// LoginSessionNotFoundError is returned by LoginClient.FinishLogin when
// the supplied session id is unknown or has expired.
type LoginSessionNotFoundError struct {
	SessionID string
}

// Error returns a human-readable description.
func (e *LoginSessionNotFoundError) Error() string {
	return "transport: login session not found: " + e.SessionID
}

// LoginSessionExpiredError is returned by LoginClient.FinishLogin when
// the supplied session id has passed its ExpiresAt deadline.
type LoginSessionExpiredError struct {
	SessionID string
}

// Error returns a human-readable description.
func (e *LoginSessionExpiredError) Error() string {
	return "transport: login session expired: " + e.SessionID
}

// PoolNotConfiguredError is returned by VolumePoolClient when the named
// pool has not been registered.
type PoolNotConfiguredError struct {
	Name string
}

// Error returns a human-readable description.
func (e *PoolNotConfiguredError) Error() string {
	return "transport: volume pool not configured: " + e.Name
}

// PoolEmptyError is returned by VolumePoolClient.Pick when the named
// pool exists but has no candidates.
type PoolEmptyError struct {
	Name string
}

// Error returns a human-readable description.
func (e *PoolEmptyError) Error() string {
	return "transport: volume pool has no candidates: " + e.Name
}

// PoolBackendNotLocalError is returned by VolumePoolClient.Pick when the
// strategy returns a backend that does not satisfy the local-path
// accessor interface.
type PoolBackendNotLocalError struct {
	Name string
}

// Error returns a human-readable description.
func (e *PoolBackendNotLocalError) Error() string {
	return "transport: volume pool backend is not local: " + e.Name
}

// PoolAlreadyRegisteredError is returned by VolumePoolClient.RegisterPool
// when the pool name is already taken.
type PoolAlreadyRegisteredError struct {
	Name string
}

// Error returns a human-readable description.
func (e *PoolAlreadyRegisteredError) Error() string {
	return "transport: volume pool already registered: " + e.Name
}

// InvalidPoolStrategyError is returned by VolumePoolClient.RegisterPool
// when the requested strategy name is unknown.
type InvalidPoolStrategyError struct {
	Strategy string
}

// Error returns a human-readable description.
func (e *InvalidPoolStrategyError) Error() string {
	return "transport: invalid volume pool strategy: " + e.Strategy
}
