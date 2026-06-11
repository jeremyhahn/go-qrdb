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

package ceremony

import (
	"errors"
	"fmt"
)

var (
	// ErrNilConfig is returned when a nil CeremonyConfig is provided.
	ErrNilConfig = errors.New("ceremony: nil config provided")

	// ErrNilBarrier is returned when a nil StorageBarrier is provided.
	ErrNilBarrier = errors.New("ceremony: nil barrier provided")

	// ErrNilLogger is returned when a nil logger is provided.
	ErrNilLogger = errors.New("ceremony: nil logger provided")

	// ErrNilStore is returned when a nil StorageBackend is provided.
	ErrNilStore = errors.New("ceremony: nil storage backend provided")

	// ErrAlreadyInitialized is returned when Initialize is called after
	// the ceremony has already completed.
	ErrAlreadyInitialized = errors.New("ceremony: ceremony already completed")

	// ErrNotInEnrollingState is returned when an operation requires
	// the enrolling state but the ceremony is in a different state.
	ErrNotInEnrollingState = errors.New("ceremony: operation requires enrolling state")

	// ErrNotInOperationalState is returned when an operation requires
	// the operational state but the ceremony is in a different state.
	ErrNotInOperationalState = errors.New("ceremony: operation requires operational state")

	// ErrInvalidSOPIN is returned when the SO PIN is empty or invalid.
	ErrInvalidSOPIN = errors.New("ceremony: SO PIN is empty or invalid")

	// ErrInvalidUserPIN is returned when the user PIN is empty or invalid.
	ErrInvalidUserPIN = errors.New("ceremony: user PIN is empty or invalid")

	// ErrInvalidThreshold is returned when the Shamir threshold is less
	// than 2 or greater than the number of officers.
	ErrInvalidThreshold = errors.New("ceremony: threshold < 2 or threshold > officers count")

	// ErrNoOfficers is returned when M-of-N is requested but no officers
	// are configured.
	ErrNoOfficers = errors.New("ceremony: M-of-N requires at least one officer")

	// ErrInvalidCSR is returned when CSR validation fails.
	ErrInvalidCSR = errors.New("ceremony: CSR validation failed")

	// ErrDuplicateUsername is returned when an officer username is
	// specified more than once.
	ErrDuplicateUsername = errors.New("ceremony: officer username already exists")

	// ErrPendingCertNotFound is returned when no pending certificate
	// exists for the given username.
	ErrPendingCertNotFound = errors.New("ceremony: no pending cert for username")

	// ErrCertAlreadyClaimed is returned when a certificate has already
	// been claimed by the officer.
	ErrCertAlreadyClaimed = errors.New("ceremony: cert already claimed")

	// ErrShareAlreadyClaimed is returned when a share has already been
	// claimed by the officer.
	ErrShareAlreadyClaimed = errors.New("ceremony: share already claimed")

	// ErrShareNotFound is returned when a share does not exist for the
	// given username.
	ErrShareNotFound = errors.New("ceremony: share not found")

	// ErrChallengeVerificationFailed is returned when the nonce signature
	// verification fails.
	ErrChallengeVerificationFailed = errors.New("ceremony: nonce signature verification failed")

	// ErrUserNotFound is returned when a user is not found during a
	// claim operation.
	ErrUserNotFound = errors.New("ceremony: user not found during claim")

	// ErrThresholdNotSupported is returned when the HSM does not support
	// native threshold operations.
	ErrThresholdNotSupported = errors.New("ceremony: HSM does not support native threshold")

	// ErrVendorNotRegistered is returned when the requested vendor is not
	// registered in the threshold registry.
	ErrVendorNotRegistered = errors.New("ceremony: vendor not registered in threshold registry")

	// ErrVendorNotAvailable is returned when the vendor hardware is not
	// present or accessible.
	ErrVendorNotAvailable = errors.New("ceremony: vendor hardware not present")

	// ErrInitFailed is returned when the ceremony initialization fails.
	ErrInitFailed = errors.New("ceremony: initialization failed")

	// ErrNotImplemented is returned when an operation is not yet implemented.
	ErrNotImplemented = errors.New("ceremony: operation not implemented")

	// ErrSOPINMismatch is returned when the provided SO PIN does not match
	// the stored SO PIN.
	ErrSOPINMismatch = errors.New("ceremony: SO PIN mismatch")

	// ErrCANotInitialized is returned when a CA operation is attempted
	// before the CA has been set via Initialize.
	ErrCANotInitialized = errors.New("ceremony: CA not initialized")

	// ErrMissingCSRPEM is returned when a CSR PEM is required but not provided.
	ErrMissingCSRPEM = errors.New("ceremony: CSR PEM is required")

	// ErrMissingUsername is returned when a username is required but not provided.
	ErrMissingUsername = errors.New("ceremony: username is required")

	// ErrMissingRole is returned when a role is required but not provided.
	ErrMissingRole = errors.New("ceremony: role is required")

	// ErrInvalidRole is returned when the provided role is not recognized.
	ErrInvalidRole = errors.New("ceremony: invalid role")

	// ErrCSRSigningFailed is returned when CSR signing fails.
	ErrCSRSigningFailed = errors.New("ceremony: CSR signing failed")

	// ErrNotInitializedOrEnrolling is returned when sign-csr requires
	// the enrolling or operational state.
	ErrNotInitializedOrEnrolling = errors.New("ceremony: sign-csr requires enrolling or operational state")

	// ErrNilCredentialService is returned when a nil credential service
	// is provided.
	ErrNilCredentialService = errors.New("ceremony: nil credential service provided")

	// ErrChallengeExpired is returned when a challenge nonce has expired.
	ErrChallengeExpired = errors.New("ceremony: challenge has expired")

	// ErrChallengeNotFound is returned when no active challenge exists
	// for the given username.
	ErrChallengeNotFound = errors.New("ceremony: no active challenge for username")

	// ErrGroupNotFound is returned when a custodian group does not exist.
	ErrGroupNotFound = errors.New("ceremony: custodian group not found")

	// ErrGroupAlreadyExists is returned when creating a group with an
	// ID that already exists.
	ErrGroupAlreadyExists = errors.New("ceremony: custodian group already exists")

	// ErrGroupFull is returned when adding a member to a group that has
	// reached its total member capacity.
	ErrGroupFull = errors.New("ceremony: custodian group is full")

	// ErrMemberNotFound is returned when a member does not exist in the
	// custodian group.
	ErrMemberNotFound = errors.New("ceremony: custodian member not found")

	// ErrMemberAlreadyExists is returned when adding a member who is
	// already in the custodian group.
	ErrMemberAlreadyExists = errors.New("ceremony: custodian member already exists")

	// ErrEmptyGroupID is returned when a group ID is empty.
	ErrEmptyGroupID = errors.New("ceremony: empty group ID")

	// ErrShareAlreadySubmitted is returned when a share has already been
	// submitted for the given group and share index.
	ErrShareAlreadySubmitted = errors.New("ceremony: share already submitted")

	// ErrEmptyShareData is returned when share data is empty.
	ErrEmptyShareData = errors.New("ceremony: empty share data")

	// ErrInsufficientMembers is returned when distributing shares to a
	// group that does not have enough members.
	ErrInsufficientMembers = errors.New("ceremony: insufficient members for share distribution")

	// ErrEmptyGroupName is returned when a group name is empty.
	ErrEmptyGroupName = errors.New("ceremony: empty group name")

	// ErrInvalidTotal is returned when the total member count is invalid.
	ErrInvalidTotal = errors.New("ceremony: invalid total member count")

	// ErrInvalidAuthenticatorType is returned when an officer's
	// AuthenticatorType is not one of the known values.
	ErrInvalidAuthenticatorType = errors.New("ceremony: invalid authenticator type")

	// ErrInvalidOfficerRecord is returned when a nil or otherwise invalid
	// officer record is supplied to the persistent registry.
	ErrInvalidOfficerRecord = errors.New("ceremony: invalid officer record")

	// ErrCertAndPubKeyBothSupplied is returned when an external officer
	// enrollment carries both CertPEM and PubKeyPEM. Exactly one must be
	// provided.
	ErrCertAndPubKeyBothSupplied = errors.New("ceremony: cannot supply both cert and pubkey")

	// ErrCertOrPubKeyRequired is returned when neither CertPEM nor
	// PubKeyPEM is provided to external officer enrollment.
	ErrCertOrPubKeyRequired = errors.New("ceremony: cert or pubkey required")

	// ErrInvalidAuthFactor is returned when an external officer enrollment
	// carries an unrecognised auth factor.
	ErrInvalidAuthFactor = errors.New("ceremony: invalid auth factor")

	// ErrCertExpired is returned when the supplied X.509 certificate is
	// not valid at the current time.
	ErrCertExpired = errors.New("ceremony: certificate expired or not yet valid")

	// ErrCertUntrusted is returned when the supplied X.509 certificate
	// does not chain to any configured CA pool.
	ErrCertUntrusted = errors.New("ceremony: certificate does not chain to a trusted CA")

	// ErrInvalidCertPEM is returned when CertPEM cannot be parsed.
	ErrInvalidCertPEM = errors.New("ceremony: invalid certificate PEM")

	// ErrInvalidPubKeyPEM is returned when PubKeyPEM cannot be parsed.
	ErrInvalidPubKeyPEM = errors.New("ceremony: invalid public key PEM")

	// ErrUnsupportedKeyAlgorithm is returned when the public key algorithm
	// is not supported for share sealing.
	ErrUnsupportedKeyAlgorithm = errors.New("ceremony: unsupported key algorithm for share sealing")

	// ErrShareSealFailed is returned when share sealing to the officer's
	// public key fails for any cryptographic reason.
	ErrShareSealFailed = errors.New("ceremony: share sealing failed")

	// ErrInvalidShareIndex is returned when ShareIndex is outside the
	// valid 1..N range.
	ErrInvalidShareIndex = errors.New("ceremony: invalid share index")

	// ErrCertThumbprintMismatch is returned when the caller's mTLS certificate
	// thumbprint does not match the officer's registered CertThumbprint.
	ErrCertThumbprintMismatch = errors.New("ceremony: certificate thumbprint mismatch")

	// ErrInsufficientOfficers is returned when fewer than threshold officers
	// are enrolled for a SealAndDistribute operation.
	ErrInsufficientOfficers = errors.New("ceremony: insufficient officers enrolled for threshold")

	// ErrOfficerCountMismatch is returned when the total share count does not
	// equal the number of enrolled officers.
	ErrOfficerCountMismatch = errors.New("ceremony: total shares must equal enrolled officer count")

	// ErrOfficerNotFound is returned when the requested officer is not enrolled
	// in the registry.
	ErrOfficerNotFound = errors.New("ceremony: officer not found")

	// ErrNoPendingShare is returned when a claim is attempted but no sealed
	// share exists for the officer (share was never distributed or was already claimed).
	ErrNoPendingShare = errors.New("ceremony: no pending share for officer")

	// ErrNilBackend is returned when a nil StorageBackend is provided to a
	// constructor that requires one.
	ErrNilBackend = errors.New("ceremony: nil storage backend provided")

	// ErrNilCA is returned when a nil CeremonyCA is provided to a constructor
	// that requires one.
	ErrNilCA = errors.New("ceremony: nil CA provided")

	// ErrEmptyTenantID is returned when an empty tenant ID is provided to
	// TenantCeremonyService or ScopeTenant.
	ErrEmptyTenantID = errors.New("ceremony: tenant ID must not be empty")
)

// OfficerRegistryError wraps an error from persistent-registry operations.
type OfficerRegistryError struct {
	Op  string
	Err error
}

// Error returns a human-readable description.
func (e *OfficerRegistryError) Error() string {
	return fmt.Sprintf("ceremony: officer registry %s failed: %v", e.Op, e.Err)
}

// Unwrap returns the underlying error.
func (e *OfficerRegistryError) Unwrap() error {
	return e.Err
}

// ShamirSplitError wraps an error from the Shamir split operation.
type ShamirSplitError struct {
	Err error
}

// Error returns a human-readable description of the Shamir split failure.
func (e *ShamirSplitError) Error() string {
	return fmt.Sprintf("ceremony: shamir split failed: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *ShamirSplitError) Unwrap() error {
	return e.Err
}

// CSRParseError wraps an error from CSR parsing.
type CSRParseError struct {
	Err error
}

// Error returns a human-readable description of the CSR parse failure.
func (e *CSRParseError) Error() string {
	return fmt.Sprintf("ceremony: CSR parse failed: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *CSRParseError) Unwrap() error {
	return e.Err
}

// CertEncodeError wraps an error from certificate encoding.
type CertEncodeError struct {
	Err error
}

// Error returns a human-readable description of the cert encode failure.
func (e *CertEncodeError) Error() string {
	return fmt.Sprintf("ceremony: cert encode failed: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *CertEncodeError) Unwrap() error {
	return e.Err
}

// CACertParseError wraps an error from CA certificate parsing.
type CACertParseError struct {
	Err error
}

// Error returns a human-readable description of the CA certificate parse failure.
func (e *CACertParseError) Error() string {
	return fmt.Sprintf("ceremony: CA certificate parse failed: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *CACertParseError) Unwrap() error {
	return e.Err
}

// CAKeyParseError wraps an error from CA private key parsing.
type CAKeyParseError struct {
	Err error
}

// Error returns a human-readable description of the CA key parse failure.
func (e *CAKeyParseError) Error() string {
	return fmt.Sprintf("ceremony: CA key parse failed: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *CAKeyParseError) Unwrap() error {
	return e.Err
}

// ShareStoreError wraps an error from share storage operations.
type ShareStoreError struct {
	Err error
}

// Error returns a human-readable description of the share store failure.
func (e *ShareStoreError) Error() string {
	return fmt.Sprintf("ceremony: share store failed: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *ShareStoreError) Unwrap() error {
	return e.Err
}

// CABundleError wraps an error from CA bundle operations.
type CABundleError struct {
	Err error
}

// Error returns a human-readable description of the CA bundle failure.
func (e *CABundleError) Error() string {
	return fmt.Sprintf("ceremony: CA bundle failed: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *CABundleError) Unwrap() error {
	return e.Err
}

// SPKIPinError wraps an error from SPKI pin computation.
type SPKIPinError struct {
	Err error
}

// Error returns a human-readable description of the SPKI pin failure.
func (e *SPKIPinError) Error() string {
	return fmt.Sprintf("ceremony: SPKI pin computation failed: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *SPKIPinError) Unwrap() error {
	return e.Err
}

// GroupStoreError wraps an error from custodian group storage operations.
type GroupStoreError struct {
	Op  string
	Err error
}

// Error returns a human-readable description of the group store failure.
func (e *GroupStoreError) Error() string {
	return fmt.Sprintf("ceremony: custodian group %s failed: %v", e.Op, e.Err)
}

// Unwrap returns the underlying error.
func (e *GroupStoreError) Unwrap() error {
	return e.Err
}

// ShareServiceError wraps an error from share service storage operations.
type ShareServiceError struct {
	Op  string
	Err error
}

// Error returns a human-readable description of the share service failure.
func (e *ShareServiceError) Error() string {
	return fmt.Sprintf("ceremony: share %s failed: %v", e.Op, e.Err)
}

// Unwrap returns the underlying error.
func (e *ShareServiceError) Unwrap() error {
	return e.Err
}
