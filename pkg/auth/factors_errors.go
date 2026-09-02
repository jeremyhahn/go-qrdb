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

package auth

import (
	"errors"
	"strings"
)

// Sentinel errors returned by the AuthFactor service and login resolver.
// Callers MUST use errors.Is to compare against these values; concrete
// wrapper types embed these via Unwrap.
var (
	// ErrFactorNotFound is returned when a lookup by factor ID or by
	// (type, credential lookup key) yields no record.
	ErrFactorNotFound = errors.New("auth: factor not found")

	// ErrFactorRevoked is returned when an operation is attempted against a
	// factor whose RevokedAt is non-nil.
	ErrFactorRevoked = errors.New("auth: factor revoked")

	// ErrLastFactor is returned by Remove when the targeted factor is the
	// last active factor on the user. Callers may pass force=true to bypass
	// this guard, which moves the user to inactive.
	ErrLastFactor = errors.New("auth: cannot remove last active factor")

	// ErrUserHasNoFactors is returned by Resolve and List when the user has
	// no factor records at all (active or revoked).
	ErrUserHasNoFactors = errors.New("auth: user has no auth factors")

	// ErrFactorTypeUnsupported is returned when an AuthFactorType outside
	// the canonical set (mtls, fido2, piv) is used.
	ErrFactorTypeUnsupported = errors.New("auth: factor type unsupported")

	// ErrFactorAlreadyExists is returned by Add when the (type, credential
	// lookup key) tuple is already bound to a different user.
	ErrFactorAlreadyExists = errors.New("auth: factor credential already enrolled")

	// ErrReservedMetadata is returned when caller-supplied factor metadata
	// carries a key inside a namespace the server owns. See
	// ReservedMetadataPrefix.
	ErrReservedMetadata = errors.New("auth: factor metadata key is server-owned")

	// ErrInvalidFactor is returned when a request payload is missing
	// required fields (e.g., empty Credential, missing UserID).
	ErrInvalidFactor = errors.New("auth: factor record invalid")
)

// FactorNotFoundError is returned with the offending factor ID for
// diagnostic purposes. It unwraps to ErrFactorNotFound.
type FactorNotFoundError struct {
	// FactorID is the ID that was not found.
	FactorID string
}

// Error implements error.
func (e *FactorNotFoundError) Error() string {
	return "auth: factor not found: " + e.FactorID
}

// Unwrap returns the sentinel error so errors.Is succeeds.
func (e *FactorNotFoundError) Unwrap() error { return ErrFactorNotFound }

// FactorRevokedError is returned when a factor lookup matches a record
// whose RevokedAt is set. It unwraps to ErrFactorRevoked.
type FactorRevokedError struct {
	// FactorID is the revoked factor's ID.
	FactorID string
}

// Error implements error.
func (e *FactorRevokedError) Error() string {
	return "auth: factor revoked: " + e.FactorID
}

// Unwrap returns the sentinel error so errors.Is succeeds.
func (e *FactorRevokedError) Unwrap() error { return ErrFactorRevoked }

// LastFactorError is returned by Remove when force=false and the factor is
// the user's last remaining active factor. It unwraps to ErrLastFactor.
type LastFactorError struct {
	// UserID is the affected user.
	UserID uint64
	// FactorID is the factor that could not be removed.
	FactorID string
}

// Error implements error.
func (e *LastFactorError) Error() string {
	return "auth: cannot remove last active factor: " + e.FactorID
}

// Unwrap returns the sentinel error so errors.Is succeeds.
func (e *LastFactorError) Unwrap() error { return ErrLastFactor }

// UserHasNoFactorsError is returned when a user has no factors at all
// (active or revoked). It unwraps to ErrUserHasNoFactors.
type UserHasNoFactorsError struct {
	// UserID is the affected user.
	UserID uint64
}

// Error implements error.
func (e *UserHasNoFactorsError) Error() string {
	return "auth: user has no auth factors"
}

// Unwrap returns the sentinel error so errors.Is succeeds.
func (e *UserHasNoFactorsError) Unwrap() error { return ErrUserHasNoFactors }

// FactorTypeUnsupportedError is returned when an AuthFactorType outside
// the canonical set (mtls, fido2, piv) is used. It unwraps to
// ErrFactorTypeUnsupported.
type FactorTypeUnsupportedError struct {
	// Type is the offending factor type string.
	Type string
}

// Error implements error.
func (e *FactorTypeUnsupportedError) Error() string {
	return "auth: factor type unsupported: " + e.Type
}

// Unwrap returns the sentinel error so errors.Is succeeds.
func (e *FactorTypeUnsupportedError) Unwrap() error { return ErrFactorTypeUnsupported }

// FactorAlreadyExistsError is returned by Add when the (type, credential
// lookup key) tuple is already bound to a (possibly different) user.
type FactorAlreadyExistsError struct {
	// Type is the factor type.
	Type AuthFactorType
	// FactorID is the existing factor's ID.
	FactorID string
}

// Error implements error.
func (e *FactorAlreadyExistsError) Error() string {
	return "auth: factor credential already enrolled: " + e.FactorID
}

// Unwrap returns the sentinel error so errors.Is succeeds.
func (e *FactorAlreadyExistsError) Unwrap() error { return ErrFactorAlreadyExists }

// InvalidFactorError is returned when a factor record fails validation
// (missing UserID, empty Credential, unsupported Type, etc.).
type InvalidFactorError struct {
	// Reason is a short human-readable description.
	Reason string
}

// Error implements error.
func (e *InvalidFactorError) Error() string {
	return "auth: factor record invalid: " + e.Reason
}

// Unwrap returns the sentinel error so errors.Is succeeds.
func (e *InvalidFactorError) Unwrap() error { return ErrInvalidFactor }

// ReservedMetadataPrefix names the AuthFactor.Metadata namespace that only
// the server may write. Caller-supplied metadata carrying any key with this
// prefix is REFUSED, on Add and on Rotate, by every transport.
//
// The keys under it record what an enrollment ceremony actually PROVED about
// an authenticator -- "hardware" only when a WebAuthn attestation chain or a
// PIV slot attestation verified against operator-configured roots -- and
// login policy reads that record to decide whether a credential may
// authenticate an administrator. A caller who can set such a key is not
// bypassing that control, they are supplying its input: a forged hardware
// claim wearing the field name the policy trusts, which defeats the control
// without touching it (go-qrdb#234, go-quicbaas#381).
//
// A PREFIX rather than a list of keys, deliberately. Enumerating the fields
// that exist today ("attest_level", "attest_aaguid") is a guard exactly as
// wide as today's field list, and the next attestation field is unprotected
// until someone remembers to extend it. A prefix fails closed for fields that
// do not exist yet.
const ReservedMetadataPrefix = "attest_"

// ReservedMetadataKeyError is returned when caller-supplied factor metadata
// carries a key inside the server-owned namespace. It names both the key and
// the prefix, so an operator who hits it can tell a forged attestation claim
// from an ordinary naming collision.
type ReservedMetadataKeyError struct {
	// Key is the offending metadata key exactly as the caller supplied it.
	Key string
	// Prefix is the reserved namespace the key falls inside.
	Prefix string
}

// Error implements error.
func (e *ReservedMetadataKeyError) Error() string {
	return "auth: factor metadata key " + e.Key + " is inside the server-owned " +
		e.Prefix + "* namespace and cannot be set by a caller"
}

// Unwrap returns the sentinel error so errors.Is succeeds.
func (e *ReservedMetadataKeyError) Unwrap() error { return ErrReservedMetadata }

// ValidateMetadata returns a *ReservedMetadataKeyError when md carries any key
// inside ReservedMetadataPrefix. It is the single definition of the rule; the
// service layer calls it on Add and Rotate so every transport inherits it
// rather than each handler re-implementing it.
//
// Keys are compared case-insensitively. A store that treated "Attest_Level"
// as distinct from "attest_level" would let the same forged claim through
// under a different spelling on any consumer that lower-cases before reading.
func ValidateMetadata(md map[string]string) error {
	for k := range md {
		if len(k) >= len(ReservedMetadataPrefix) &&
			strings.EqualFold(k[:len(ReservedMetadataPrefix)], ReservedMetadataPrefix) {
			return &ReservedMetadataKeyError{Key: k, Prefix: ReservedMetadataPrefix}
		}
	}
	return nil
}
