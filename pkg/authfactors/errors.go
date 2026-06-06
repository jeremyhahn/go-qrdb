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

package authfactors

import (
	"errors"
)

// HardwareUnavailableError is returned when the underlying hardware device
// (HSM, smart card reader, FIDO2 authenticator) cannot be reached.
type HardwareUnavailableError struct {
	// Factor is the factor type that encountered the hardware failure.
	Factor string
	// Reason is a human-readable description of the failure.
	Reason string
}

// Error implements the error interface.
func (e *HardwareUnavailableError) Error() string {
	return "authfactors: hardware unavailable [" + e.Factor + "]: " + e.Reason
}

// Sentinel errors returned by Factor implementations. Callers should use
// errors.Is to check for these values and errors.As for the structured types.
var (
	// ErrFactorDisabled is returned when a factor implementation was compiled
	// without its build tag (e.g., pkcs11 without cgo + libp11).
	ErrFactorDisabled = errors.New("authfactors: factor disabled in this build")

	// ErrHardwareUnavailable is returned when the underlying hardware
	// (HSM, smart card reader, FIDO2 authenticator) is not reachable.
	ErrHardwareUnavailable = errors.New("authfactors: hardware device not available")
)

// EnrollmentInvalidError is returned when an Enrollment is missing required
// fields or carries malformed public key material.
type EnrollmentInvalidError struct {
	// EnrollmentID identifies the enrollment (may be empty when ID is unknown).
	EnrollmentID string
	// Reason is a human-readable description of the validation failure.
	Reason string
}

// Error implements the error interface.
func (e *EnrollmentInvalidError) Error() string {
	if e.EnrollmentID != "" {
		return "authfactors: invalid enrollment " + e.EnrollmentID + ": " + e.Reason
	}
	return "authfactors: invalid enrollment: " + e.Reason
}

// ChallengeExpiredError is returned when a Challenge has passed its ExpiresAt
// deadline and the custodian's Response can no longer be accepted.
type ChallengeExpiredError struct {
	// ChallengeID identifies the expired challenge.
	ChallengeID string
}

// Error implements the error interface.
func (e *ChallengeExpiredError) Error() string {
	return "authfactors: challenge expired: " + e.ChallengeID
}

// VerificationFailedError is returned when the Response signature does not
// verify against the enrolled public key.
type VerificationFailedError struct {
	// FactorName is the factor that attempted verification ("pkcs11", "piv", "fido2").
	FactorName string
	// Reason is a short description of the failure.
	Reason string
}

// Error implements the error interface.
func (e *VerificationFailedError) Error() string {
	return "authfactors: verification failed [" + e.FactorName + "]: " + e.Reason
}

// FactorNotFoundError is returned when the requested factor name is not
// registered with the service layer.
type FactorNotFoundError struct {
	// Name is the requested factor name.
	Name string
}

// Error implements the error interface.
func (e *FactorNotFoundError) Error() string {
	return "authfactors: factor not found: " + e.Name
}

// EnrollmentNotFoundError is returned when an enrollment ID does not exist
// in the persistent store.
type EnrollmentNotFoundError struct {
	// EnrollmentID is the requested identifier.
	EnrollmentID string
}

// Error implements the error interface.
func (e *EnrollmentNotFoundError) Error() string {
	return "authfactors: enrollment not found: " + e.EnrollmentID
}

// EnrollmentRevokedError is returned when an operation is attempted on an
// enrollment that has already been revoked.
type EnrollmentRevokedError struct {
	// EnrollmentID identifies the revoked enrollment.
	EnrollmentID string
}

// Error implements the error interface.
func (e *EnrollmentRevokedError) Error() string {
	return "authfactors: enrollment already revoked: " + e.EnrollmentID
}

// FactorRegisterError is returned by FactorRegistry.Register when a factor
// cannot be installed (nil factor, empty Name()).
type FactorRegisterError struct {
	// Reason describes why registration failed.
	Reason string
}

// Error implements the error interface.
func (e *FactorRegisterError) Error() string {
	return "authfactors: factor register failed: " + e.Reason
}

// FactorAlreadyRegisteredError is returned by FactorRegistry.Register when
// the factor's Name() is already present in the registry. Callers that want
// replacement semantics must Unregister first.
type FactorAlreadyRegisteredError struct {
	// Name is the factor name that was already registered.
	Name string
}

// Error implements the error interface.
func (e *FactorAlreadyRegisteredError) Error() string {
	return "authfactors: factor already registered: " + e.Name
}
