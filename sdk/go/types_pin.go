// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/pin"

// ---------------------------------------------------------------------------
// PIN backend
// ---------------------------------------------------------------------------

// PINBackend is the interface for PIN/passphrase verification backends.
type PINBackend = pin.PINBackend

// SoftwareBackend is the software (in-process) PINBackend implementation.
type SoftwareBackend = pin.SoftwareBackend

// PINStorage is the persistence interface backing a SoftwareBackend. (Aliases
// pkg/pin.Storage; named PINStorage to avoid colliding with the storage
// package's Storage alias in the flat SDK.)
type PINStorage = pin.Storage

// PINHashConfig configures the password-hashing algorithm used by a
// SoftwareBackend. (Aliases pkg/pin.HashConfig.)
type PINHashConfig = pin.HashConfig

// NewSoftwareBackend creates a new software PINBackend over the given storage
// and hash configuration.
var NewSoftwareBackend = pin.NewSoftwareBackend

// AutoDetectHashConfig returns the hash configuration appropriate for the
// current FIPS mode (PBKDF2 in FIPS mode, Argon2id otherwise).
var AutoDetectHashConfig = pin.AutoDetectHashConfig

// DefaultHashConfig returns the default password-hashing configuration.
var DefaultHashConfig = pin.DefaultHashConfig

// ---------------------------------------------------------------------------
// PIN service
// ---------------------------------------------------------------------------

// PINService coordinates PIN/passphrase operations over a PINBackend.
// (Aliases pkg/pin.Service; named PINService to avoid colliding with other
// Service symbols in the flat SDK.)
type PINService = pin.Service

// NewPINService creates a new PINService over the given backend and logger.
var NewPINService = pin.NewService

// PINStrategyID identifies a PIN verification strategy. (Aliases
// pkg/pin.StrategyID; named PINStrategyID to avoid colliding with the seal
// package's StrategyID alias in the flat SDK.)
type PINStrategyID = pin.StrategyID

// PIN strategy constants.
const (
	// PINStrategySoftware uses in-process software PIN verification.
	PINStrategySoftware = pin.StrategySoftware

	// PINStrategyTPM2 uses TPM 2.0 platform-key auth for PIN verification.
	PINStrategyTPM2 = pin.StrategyTPM2

	// PINStrategyPKCS11 uses a PKCS#11 token for PIN verification.
	PINStrategyPKCS11 = pin.StrategyPKCS11
)

// PINLockoutStatus reports the lockout state of a PIN backend. (Aliases
// pkg/pin.LockoutStatus.)
type PINLockoutStatus = pin.LockoutStatus

// FIDO2HashVerifier is implemented by backends that can verify a raw FIDO2
// PIN hash. (Aliases pkg/pin.FIDO2HashVerifier.)
type FIDO2HashVerifier = pin.FIDO2HashVerifier

// FIDO2PINHashSize is the size in bytes of a FIDO2 PIN hash.
const FIDO2PINHashSize = pin.FIDO2PINHashSize

// ComputeFIDO2PINHash derives the FIDO2 PIN hash for the given PIN string.
var ComputeFIDO2PINHash = pin.ComputeFIDO2PINHash

// ---------------------------------------------------------------------------
// PIN error types and sentinels
// ---------------------------------------------------------------------------

// PINTPMLockedError is returned when the TPM is locked out. (Aliases
// pkg/pin.ErrTPMLocked, a struct error type.)
type PINTPMLockedError = pin.ErrTPMLocked

// PINInvalidWithStatusError is returned when a PIN is invalid and carries the
// current lockout status. (Aliases pkg/pin.ErrPINInvalidWithStatus.)
type PINInvalidWithStatusError = pin.ErrPINInvalidWithStatus

// PIN error sentinels (aliases of the pkg/pin Err* sentinel values).
var (
	// ErrPINNotSet is returned when no PIN has been set.
	ErrPINNotSet = pin.ErrPINNotSet

	// ErrPINLocked is returned when the PIN is locked out.
	ErrPINLocked = pin.ErrPINLocked

	// ErrPINInvalid is returned when the supplied PIN is invalid.
	ErrPINInvalid = pin.ErrPINInvalid

	// ErrSOPINRequired is returned when SO PIN authorization is required.
	ErrSOPINRequired = pin.ErrSOPINRequired

	// ErrPINTooShort is returned when a PIN is shorter than the minimum length.
	ErrPINTooShort = pin.ErrPINTooShort

	// ErrPINAlreadySet is returned when setting a PIN that is already set.
	ErrPINAlreadySet = pin.ErrPINAlreadySet

	// ErrInvalidCurrentPIN is returned when current PIN verification fails.
	ErrInvalidCurrentPIN = pin.ErrInvalidCurrentPIN

	// ErrPINStateCorrupted is returned when a persisted PIN record is corrupt.
	ErrPINStateCorrupted = pin.ErrStateCorrupted

	// ErrPINStrategyNotSet is returned when no PIN strategy is configured.
	ErrPINStrategyNotSet = pin.ErrStrategyNotSet

	// ErrPINStorageNotFound is returned by PIN storage when a key is not found.
	// (Aliases pkg/pin.ErrNotFound; named ErrPINStorageNotFound to avoid
	// colliding with the dao/errors ErrNotFound alias in the flat SDK.)
	ErrPINStorageNotFound = pin.ErrNotFound
)
