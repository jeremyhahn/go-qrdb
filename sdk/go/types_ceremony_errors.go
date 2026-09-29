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

	// ErrCeremonyNotInEnrollingState is returned when not in enrolling state.
	ErrCeremonyNotInEnrollingState = ceremony.ErrNotInEnrollingState

	// ErrCeremonyNotInOperationalState is returned when not in operational state.
	ErrCeremonyNotInOperationalState = ceremony.ErrNotInOperationalState

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

	// ErrCeremonyPendingCertNotFound is returned when no pending cert exists.
	ErrCeremonyPendingCertNotFound = ceremony.ErrPendingCertNotFound

	// ErrCeremonyCertAlreadyClaimed is returned when a cert is already claimed.
	ErrCeremonyCertAlreadyClaimed = ceremony.ErrCertAlreadyClaimed

	// ErrCeremonyShareAlreadyClaimed is returned when a share is already claimed.
	ErrCeremonyShareAlreadyClaimed = ceremony.ErrShareAlreadyClaimed

	// ErrCeremonyShareNotFound is returned when a share does not exist.
	ErrCeremonyShareNotFound = ceremony.ErrShareNotFound

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

	// ErrCeremonySOPINMismatch is returned when the SO PIN does not match.
	ErrCeremonySOPINMismatch = ceremony.ErrSOPINMismatch

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

	// ErrCeremonyNilCredentialService is returned when credential service is nil.
	ErrCeremonyNilCredentialService = ceremony.ErrNilCredentialService

	// ErrCeremonyChallengeExpired is returned when a challenge has expired.
	ErrCeremonyChallengeExpired = ceremony.ErrChallengeExpired

	// ErrCeremonyChallengeNotFound is returned when no active challenge exists.
	ErrCeremonyChallengeNotFound = ceremony.ErrChallengeNotFound

	// ErrCeremonyGroupNotFound is returned when a custodian group is not found.
	ErrCeremonyGroupNotFound = ceremony.ErrGroupNotFound

	// ErrCeremonyGroupAlreadyExists is returned when a group already exists.
	ErrCeremonyGroupAlreadyExists = ceremony.ErrGroupAlreadyExists

	// ErrCeremonyGroupFull is returned when a group has reached capacity.
	ErrCeremonyGroupFull = ceremony.ErrGroupFull

	// ErrCeremonyMemberNotFound is returned when a member is not found.
	ErrCeremonyMemberNotFound = ceremony.ErrMemberNotFound

	// ErrCeremonyMemberAlreadyExists is returned when a member already exists.
	ErrCeremonyMemberAlreadyExists = ceremony.ErrMemberAlreadyExists

	// ErrCeremonyEmptyGroupID is returned when a group ID is empty.
	ErrCeremonyEmptyGroupID = ceremony.ErrEmptyGroupID

	// ErrCeremonyShareAlreadySubmitted is returned when a share is already submitted.
	ErrCeremonyShareAlreadySubmitted = ceremony.ErrShareAlreadySubmitted

	// ErrCeremonyEmptyShareData is returned when share data is empty.
	ErrCeremonyEmptyShareData = ceremony.ErrEmptyShareData

	// ErrCeremonyInsufficientMembers is returned when there are not enough members.
	ErrCeremonyInsufficientMembers = ceremony.ErrInsufficientMembers

	// ErrCeremonyEmptyGroupName is returned when a group name is empty.
	ErrCeremonyEmptyGroupName = ceremony.ErrEmptyGroupName

	// ErrCeremonyInvalidTotal is returned when the total member count is invalid.
	ErrCeremonyInvalidTotal = ceremony.ErrInvalidTotal
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

// CeremonyGroupStoreError wraps an error from custodian group storage.
type CeremonyGroupStoreError = ceremony.GroupStoreError
