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

import "github.com/jeremyhahn/go-qrdb/pkg/ceremony"

// ---------------------------------------------------------------------------
// Ceremony sentinel errors
// ---------------------------------------------------------------------------

var (
	// ErrCeremonyNilConfig is returned when a nil CeremonyConfig is provided.
	ErrCeremonyNilConfig = ceremony.ErrNilConfig

	// ErrCeremonyNilBarrier is returned when a nil StorageBarrier is provided.
	ErrCeremonyNilBarrier = ceremony.ErrNilBarrier

	// ErrCeremonyNilLogger is returned when a nil logger is provided.
	ErrCeremonyNilLogger = ceremony.ErrNilLogger

	// ErrCeremonyNilStore is returned when a nil StorageBackend is provided.
	ErrCeremonyNilStore = ceremony.ErrNilStore

	// ErrCeremonyAlreadyInitialized is returned when the ceremony is already complete.
	ErrCeremonyAlreadyInitialized = ceremony.ErrAlreadyInitialized

	// ErrCeremonyInvalidSOPIN is returned when the SO PIN is invalid.
	ErrCeremonyInvalidSOPIN = ceremony.ErrInvalidSOPIN

	// ErrCeremonyInvalidUserPIN is returned when the user PIN is invalid.
	ErrCeremonyInvalidUserPIN = ceremony.ErrInvalidUserPIN

	// ErrCeremonyInvalidThreshold is returned when the Shamir threshold is invalid.
	ErrCeremonyInvalidThreshold = ceremony.ErrInvalidThreshold

	// ErrCeremonyNoOfficers is returned when no officers are configured.
	ErrCeremonyNoOfficers = ceremony.ErrNoOfficers

	// ErrCeremonyInvalidCSR is returned when CSR validation fails.
	ErrCeremonyInvalidCSR = ceremony.ErrInvalidCSR

	// ErrCeremonyDuplicateUsername is returned when an officer username is duplicated.
	ErrCeremonyDuplicateUsername = ceremony.ErrDuplicateUsername

	// ErrCeremonyShareAlreadyClaimed is returned when a share is already claimed.
	ErrCeremonyShareAlreadyClaimed = ceremony.ErrShareAlreadyClaimed

	// ErrCeremonyShareNotFound is returned when a share does not exist.
	ErrCeremonyShareNotFound = ceremony.ErrShareNotFound

	// ErrCeremonyShareExpired indicates a distributed share was wiped
	// unclaimed at the end of its time-to-live.
	ErrCeremonyShareExpired = ceremony.ErrShareExpired

	// ErrCeremonyCertThumbprintMismatch is returned when the caller's mTLS
	// certificate thumbprint does not match the enrolled officer record. It is
	// the refusal that keeps a named-but-unauthenticated caller from taking an
	// officer's Shamir share.
	ErrCeremonyCertThumbprintMismatch = ceremony.ErrCertThumbprintMismatch

	// ErrCeremonyOfficerNotFound is returned when no officer is enrolled under
	// the requested username.
	ErrCeremonyOfficerNotFound = ceremony.ErrOfficerNotFound

	// ErrCeremonyNoPendingShare is returned when no unclaimed share exists for
	// the officer.
	ErrCeremonyNoPendingShare = ceremony.ErrNoPendingShare

	// ErrCeremonyChallengeVerificationFailed is returned when nonce verification fails.
	ErrCeremonyChallengeVerificationFailed = ceremony.ErrChallengeVerificationFailed

	// ErrCeremonyUserNotFound is returned when a user is not found.
	ErrCeremonyUserNotFound = ceremony.ErrUserNotFound

	// ErrCeremonyThresholdNotSupported is returned when HSM threshold is unsupported.
	ErrCeremonyThresholdNotSupported = ceremony.ErrThresholdNotSupported

	// ErrCeremonyVendorNotRegistered is returned when a vendor is not registered.
	ErrCeremonyVendorNotRegistered = ceremony.ErrVendorNotRegistered

	// ErrCeremonyVendorNotAvailable is returned when vendor hardware is unavailable.
	ErrCeremonyVendorNotAvailable = ceremony.ErrVendorNotAvailable

	// ErrCeremonyInitFailed is returned when initialization fails.
	ErrCeremonyInitFailed = ceremony.ErrInitFailed

	// ErrCeremonyNotImplemented is returned when an operation is not implemented.
	ErrCeremonyNotImplemented = ceremony.ErrNotImplemented

	// ErrCeremonyCANotInitialized is returned when the CA is not initialized.
	ErrCeremonyCANotInitialized = ceremony.ErrCANotInitialized

	// ErrCeremonyMissingCSRPEM is returned when CSR PEM is required but missing.
	ErrCeremonyMissingCSRPEM = ceremony.ErrMissingCSRPEM

	// ErrCeremonyMissingUsername is returned when username is required but missing.
	ErrCeremonyMissingUsername = ceremony.ErrMissingUsername

	// ErrCeremonyMissingRole is returned when role is required but missing.
	ErrCeremonyMissingRole = ceremony.ErrMissingRole

	// ErrCeremonyInvalidRole is returned when the role is not recognized.
	ErrCeremonyInvalidRole = ceremony.ErrInvalidRole

	// ErrCeremonyCSRSigningFailed is returned when CSR signing fails.
	ErrCeremonyCSRSigningFailed = ceremony.ErrCSRSigningFailed

	// ErrCeremonyNotInitializedOrEnrolling is returned when sign-csr requires
	// enrolling or operational state.
	ErrCeremonyNotInitializedOrEnrolling = ceremony.ErrNotInitializedOrEnrolling

	// ErrCeremonyChallengeExpired is returned when a challenge has expired.
	ErrCeremonyChallengeExpired = ceremony.ErrChallengeExpired

	// ErrCeremonyChallengeNotFound is returned when no active challenge exists.
	ErrCeremonyChallengeNotFound = ceremony.ErrChallengeNotFound
)

// ---------------------------------------------------------------------------
// Ceremony error types
// ---------------------------------------------------------------------------

// CeremonyShamirSplitError wraps an error from the Shamir split operation.
type CeremonyShamirSplitError = ceremony.ShamirSplitError

// CeremonyCSRParseError wraps an error from CSR parsing.
type CeremonyCSRParseError = ceremony.CSRParseError

// CeremonyCertEncodeError wraps an error from certificate encoding.
type CeremonyCertEncodeError = ceremony.CertEncodeError

// CeremonyShareStoreError wraps an error from the share store.
type CeremonyShareStoreError = ceremony.ShareStoreError

// CeremonyCABundleError wraps an error from CA bundle operations.
type CeremonyCABundleError = ceremony.CABundleError

// CeremonySPKIPinError wraps an error from SPKI pin computation.
type CeremonySPKIPinError = ceremony.SPKIPinError
