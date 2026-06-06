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

package user

import "strconv"

// UserNotFoundError is returned when a user does not exist. For a lookup by
// ID, ID carries the numeric identifier. For a lookup by a non-numeric key
// (username or cert thumbprint), Lookup carries a human-readable descriptor of
// the key that failed to resolve and ID is zero.
type UserNotFoundError struct {
	// ID is the numeric user id that was not found. Zero when the lookup was
	// not by id (see Lookup).
	ID uint64

	// Lookup is a descriptor of a non-id lookup that failed (e.g.
	// "tenant/username" or "cert_spki/<thumbprint>"). Empty for id lookups.
	Lookup string
}

// Error returns a human-readable description of the not-found condition.
func (e *UserNotFoundError) Error() string {
	if e.Lookup != "" {
		return "user: not found: " + e.Lookup
	}
	return "user: not found: " + strconv.FormatUint(e.ID, 10)
}

// UsernameExistsError is returned when a username is already taken
// within the tenant.
type UsernameExistsError struct {
	Username string
	TenantID string
}

// Error returns a human-readable description of the conflict.
func (e *UsernameExistsError) Error() string {
	return "user: username already exists: " + e.Username + " in tenant " + e.TenantID
}

// UserDisabledError is returned when operating on a disabled user.
type UserDisabledError struct {
	ID uint64
}

// Error returns a human-readable description of the disabled condition.
func (e *UserDisabledError) Error() string {
	return "user: disabled: " + strconv.FormatUint(e.ID, 10)
}

// SystemUserImmutableError is returned when trying to delete a system user.
type SystemUserImmutableError struct {
	ID uint64
}

// Error returns a human-readable description of the immutability constraint.
func (e *SystemUserImmutableError) Error() string {
	return "user: system user cannot be deleted: " + strconv.FormatUint(e.ID, 10)
}

// StoreError is returned for store-level operation failures.
type StoreError struct {
	Op     string
	Detail string
	Err    error
}

// Error returns a human-readable description of the store failure.
func (e *StoreError) Error() string {
	msg := "user: store " + e.Op + " failed"
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *StoreError) Unwrap() error { return e.Err }

// ValidationError is returned when request validation fails.
type ValidationError struct {
	Field  string
	Reason string
}

// Error returns a human-readable description of the validation failure.
func (e *ValidationError) Error() string {
	return "user: validation failed: " + e.Field + ": " + e.Reason
}

// CertBindingConflictError is returned when a caller attempts to bind a
// certificate thumbprint that is already bound to a different user. A
// thumbprint resolves to exactly one identity, so silently overwriting the
// existing mapping would let a caller hijack another user's mTLS identity.
type CertBindingConflictError struct {
	Thumbprint   string
	BoundUserID  uint64
	TargetUserID uint64
}

// Error returns a human-readable description of the binding conflict. The
// thumbprint is included to aid operators; it is a public-key hash, not a
// secret.
func (e *CertBindingConflictError) Error() string {
	return "user: cert thumbprint " + e.Thumbprint + " is already bound to user " + strconv.FormatUint(e.BoundUserID, 10)
}
