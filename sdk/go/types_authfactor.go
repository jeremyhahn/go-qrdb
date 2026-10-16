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

import "github.com/jeremyhahn/go-qrdb/pkg/auth"

// ---------------------------------------------------------------------------
// Auth factor types
// ---------------------------------------------------------------------------

// AuthFactor is a single enrolled authentication factor for a principal.
type AuthFactor = auth.AuthFactor

// AuthFactorType identifies the kind of an authentication factor.
type AuthFactorType = auth.AuthFactorType

// AuthFactorType constants.
const (
	// AuthFactorMTLS is the mutual-TLS authentication factor.
	AuthFactorMTLS = auth.AuthFactorMTLS

	// AuthFactorFIDO2 is the FIDO2/WebAuthn authentication factor.
	AuthFactorFIDO2 = auth.AuthFactorFIDO2

	// AuthFactorPIV is the PIV smartcard authentication factor.
	AuthFactorPIV = auth.AuthFactorPIV

	// AuthFactorPKCS11 is the PKCS#11 / HSM hardware key authentication factor.
	// The credential lookup key is SHA-256(SubjectPublicKeyInfo DER), 32 bytes.
	AuthFactorPKCS11 = auth.AuthFactorPKCS11
)

// Factor provenance constants.
const (
	// FactorCreatedBySelf marks a factor enrolled by the principal.
	FactorCreatedBySelf = auth.FactorCreatedBySelf

	// FactorCreatedByAdmin marks a factor enrolled by an administrator.
	FactorCreatedByAdmin = auth.FactorCreatedByAdmin
)

// CredentialKey hashes raw certificate/blob bytes into a stable lookup key
// for a stored credential factor. (Aliases pkg/auth.CredentialKey.)
var CredentialKey = auth.CredentialKey

// ---------------------------------------------------------------------------
// Attestation (the server-owned attest_* metadata namespace)
// ---------------------------------------------------------------------------

// ReservedMetadataPrefix is the AuthFactor.Metadata namespace only the server
// writes. Caller-supplied keys under it are refused on Add and Rotate over
// every transport; see ReservedMetadataKeyError.
const ReservedMetadataPrefix = auth.ReservedMetadataPrefix

// Metadata keys inside ReservedMetadataPrefix that the server writes. Read
// them with these names; never write them -- AttestationRecorder is the one
// path that records them.
const (
	// MetadataAttestLevel holds the AttestationLevel a factor proved at
	// enrollment. Every factor written since the server began stamping it
	// carries one (AttestationLevelNone when nothing was proved).
	MetadataAttestLevel = auth.MetadataAttestLevel

	// MetadataAttestAAGUID holds the attested authenticator model, present
	// only when a ceremony verified one.
	MetadataAttestAAGUID = auth.MetadataAttestAAGUID
)

// AttestationLevel is what an enrollment ceremony proved about where a
// factor's private key lives.
type AttestationLevel = auth.AttestationLevel

// AttestationLevel values.
const (
	// AttestationLevelNone records that no attestation was verified.
	AttestationLevelNone = auth.AttestationLevelNone

	// AttestationLevelSoftware records a verified but self-signed attestation.
	AttestationLevelSoftware = auth.AttestationLevelSoftware

	// AttestationLevelHardware records an attestation chained to a
	// manufacturer trust anchor the operator configured.
	AttestationLevelHardware = auth.AttestationLevelHardware
)

// VerifiedAttestation is what an enrollment ceremony proved about an
// authenticator, handed to AttestationRecorder by the in-process host that
// ran the ceremony. Nothing on any wire can produce one.
type VerifiedAttestation = auth.VerifiedAttestation

// Unattested is the attestation the server records for a factor written
// without a ceremony: AttestationLevelNone, no model.
var Unattested = auth.Unattested

// InvalidAttestationError is returned by AttestationRecorder when the
// attestation does not name a defined AttestationLevel.
type InvalidAttestationError = auth.InvalidAttestationError

// ErrInvalidAttestation is the sentinel InvalidAttestationError unwraps to.
var ErrInvalidAttestation = auth.ErrInvalidAttestation

// ReservedMetadataKeyError is returned by Add and Rotate when caller-supplied
// factor metadata carries a key inside ReservedMetadataPrefix. It names the
// key and the prefix.
type ReservedMetadataKeyError = auth.ReservedMetadataKeyError

// ErrReservedMetadata is the sentinel ReservedMetadataKeyError unwraps to.
var ErrReservedMetadata = auth.ErrReservedMetadata

// Principal is an authenticated subject resolved during login.
type Principal = auth.Principal

// ---------------------------------------------------------------------------
// Login resolution
// ---------------------------------------------------------------------------

// FactorStore is the interface for persisting and querying auth factors.
type FactorStore = auth.FactorStore

// UserLookup resolves principals by identifier during login.
type UserLookup = auth.UserLookup

// LoginResolver resolves a login attempt to a principal using enrolled
// factors.
type LoginResolver = auth.LoginResolver

// NewLoginResolver creates a new LoginResolver.
var NewLoginResolver = auth.NewLoginResolver

// ---------------------------------------------------------------------------
// Auth factor errors
// ---------------------------------------------------------------------------

// LastFactorError is returned when removing the last active factor.
type LastFactorError = auth.LastFactorError

// UserHasNoFactorsError is returned when a principal has no enrolled factors.
type UserHasNoFactorsError = auth.UserHasNoFactorsError

// FactorRevokedError is returned when verification is attempted against a
// revoked authentication factor.
type FactorRevokedError = auth.FactorRevokedError

// FactorTypeUnsupportedError is returned when a verifier receives a factor of
// an unsupported type.
type FactorTypeUnsupportedError = auth.FactorTypeUnsupportedError

// ErrFactorRevoked is returned when a factor has been revoked.
var ErrFactorRevoked = auth.ErrFactorRevoked

// ErrLastFactor is returned when attempting to remove the last active factor.
var ErrLastFactor = auth.ErrLastFactor

// ErrFactorNotFound is returned when no factor matches the given identifier
// or credential lookup key.
var ErrFactorNotFound = auth.ErrFactorNotFound

// ErrFactorAlreadyExists is returned when an identical credential is already
// enrolled for the user.
var ErrFactorAlreadyExists = auth.ErrFactorAlreadyExists

// ErrFactorConditionFailed is returned when a conditional factor add is
// refused because its condition did not hold; errors.As recovers the
// *FactorConditionFailedError carrying the factor-set state that refused it.
var ErrFactorConditionFailed = auth.ErrFactorConditionFailed

// FactorConditionFailedError reports which condition refused a conditional
// factor add and the factor-set version it was decided against.
type FactorConditionFailedError = auth.FactorConditionFailedError

// FactorAddCondition is the condition a conditional factor add commits under.
type FactorAddCondition = auth.FactorAddCondition

// FactorConditionKind names the condition a conditional factor add carried.
type FactorConditionKind = auth.FactorConditionKind

const (
	// FactorConditionNoActiveOfType holds while the user has no active factor
	// of the added factor's type.
	FactorConditionNoActiveOfType = auth.FactorConditionNoActiveOfType

	// FactorConditionExpectedVersion holds while the user's factor set is at
	// the expected version.
	FactorConditionExpectedVersion = auth.FactorConditionExpectedVersion
)

// ErrFactorTypeUnsupported is returned for an unrecognized factor type.
var ErrFactorTypeUnsupported = auth.ErrFactorTypeUnsupported

// ErrInvalidFactor is returned when a factor record fails validation.
var ErrInvalidFactor = auth.ErrInvalidFactor

// ErrUserHasNoFactors is returned when a principal has no enrolled factors.
var ErrUserHasNoFactors = auth.ErrUserHasNoFactors
