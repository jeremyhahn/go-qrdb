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

package pin

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by PIN backends.
var (
	// ErrPINNotSet is returned when a PIN has not yet been configured.
	ErrPINNotSet = errors.New("pin: PIN not set")

	// ErrPINLocked is returned when the backend is currently in a
	// locked state due to too many failed attempts.
	ErrPINLocked = errors.New("pin: locked out due to too many failed attempts")

	// ErrPINInvalid is returned when the supplied PIN does not match
	// the configured PIN.
	ErrPINInvalid = errors.New("pin: invalid PIN")

	// ErrSOPINRequired is returned when an operation requires SO PIN
	// authorization but none was supplied.
	ErrSOPINRequired = errors.New("pin: SO PIN authorization required")

	// ErrPINTooShort is returned when a PIN does not meet the minimum
	// length requirement (6 characters across all backends).
	ErrPINTooShort = errors.New("pin: PIN must be at least 6 characters")

	// ErrPINAlreadySet is returned when SetSOPIN or SetUserPIN is invoked
	// on a backend that already has the PIN configured. Callers should
	// use ChangeSOPIN or ChangeUserPIN instead.
	ErrPINAlreadySet = errors.New("pin: PIN already set, use Change instead")

	// ErrInvalidCurrentPIN is returned when the current PIN verification
	// fails during a Change operation.
	ErrInvalidCurrentPIN = errors.New("pin: current PIN verification failed")

	// ErrStateCorrupted is returned when a persisted PIN record cannot
	// be decoded.
	ErrStateCorrupted = errors.New("pin: state file corrupted")

	// ErrStrategyNotSet is returned when a PIN service is invoked before
	// a backend strategy has been configured.
	ErrStrategyNotSet = errors.New("pin: no PIN strategy configured")

	// ErrHierarchyAuthMismatch is returned when a TPM hierarchy carries
	// auth from a previous session whose local state was lost and the
	// supplied PIN does not match. A factory reset of the TPM hierarchies
	// is required to recover.
	ErrHierarchyAuthMismatch = errors.New("pin: TPM hierarchy auth mismatch, factory reset required")

	// ErrUnsupportedHashAlgorithm is returned when an unknown hash
	// algorithm is specified in a HashConfig or PINRecord.
	ErrUnsupportedHashAlgorithm = errors.New("pin: unsupported hash algorithm")
)

// StoragePersistFailedError wraps a storage Put failure so callers can
// distinguish "couldn't write" from "couldn't hash".
type StoragePersistFailedError struct {
	Key   string
	Cause error
}

// Error implements the error interface.
func (e *StoragePersistFailedError) Error() string {
	return fmt.Sprintf("pin: failed to persist %q to storage: %s", e.Key, e.Cause)
}

// Unwrap supports errors.Is / errors.As against the underlying cause.
func (e *StoragePersistFailedError) Unwrap() error { return e.Cause }

// StorageLoadFailedError wraps a storage Get failure for reasons other than
// not-found (callers treat not-found as "fresh install" — no error).
type StorageLoadFailedError struct {
	Key   string
	Cause error
}

// Error implements the error interface.
func (e *StorageLoadFailedError) Error() string {
	return fmt.Sprintf("pin: failed to load %q from storage: %s", e.Key, e.Cause)
}

// Unwrap supports errors.Is / errors.As.
func (e *StorageLoadFailedError) Unwrap() error { return e.Cause }

// UnsupportedPBKDF2HashError is returned when an unsupported hash function
// is specified for PBKDF2 key derivation.
type UnsupportedPBKDF2HashError struct {
	Hash string
}

// Error implements the error interface.
func (e *UnsupportedPBKDF2HashError) Error() string {
	return fmt.Sprintf("pin: unsupported PBKDF2 hash function: %s", e.Hash)
}

// ErrTPMLocked is returned when a TPM backend is locked out due to
// dictionary-attack protection. The wrapped LockoutStatus carries the
// recovery time so callers can surface it to operators.
type ErrTPMLocked struct { //nolint:errname // exported API, rename is a breaking change
	Status *LockoutStatus
}

// Error implements the error interface.
func (e *ErrTPMLocked) Error() string {
	return fmt.Sprintf("pin: TPM locked, recovery in %d seconds", e.Status.RecoverySeconds)
}

// ErrPINInvalidWithStatus extends ErrPINInvalid with the remaining-attempts
// information so backends with lockout counters can communicate progress
// to the operator.
type ErrPINInvalidWithStatus struct { //nolint:errname // exported API, rename is a breaking change
	Status *LockoutStatus
}

// Error implements the error interface.
func (e *ErrPINInvalidWithStatus) Error() string {
	remaining := e.Status.MaxAttempts - e.Status.FailedAttempts
	return fmt.Sprintf("pin: invalid PIN, %d of %d attempts remaining", remaining, e.Status.MaxAttempts)
}
