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

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
)

// ---------------------------------------------------------------------------
// Ceremony state
// ---------------------------------------------------------------------------

// CeremonyState represents the current state of the initialization ceremony.
type CeremonyState = ceremony.CeremonyState

// CeremonyState constants.
const (
	// StateAwaitingInit indicates the barrier has not been initialized.
	StateAwaitingInit = ceremony.StateAwaitingInit

	// StateEnrolling indicates M-of-N officer enrollment is in progress.
	StateEnrolling = ceremony.StateEnrolling

	// StateOperational indicates the system is ready for service.
	StateOperational = ceremony.StateOperational
)

// ---------------------------------------------------------------------------
// Ceremony configuration types
// ---------------------------------------------------------------------------

// OfficerConfig holds the configuration for an SO officer in M-of-N mode.
type OfficerConfig = ceremony.OfficerConfig

// CeremonyConfig holds the full ceremony configuration including officers
// and Shamir parameters.
type CeremonyConfig = ceremony.CeremonyConfig

// ---------------------------------------------------------------------------
// Ceremony result types
// ---------------------------------------------------------------------------

// InitResult holds the result of a ceremony initialization.
type InitResult = ceremony.InitResult

// ChallengeResponse holds the nonce challenge for officer cert claims.
type ChallengeResponse = ceremony.ChallengeResponse

// ClaimCertResult holds the result of an officer certificate claim.
type ClaimCertResult = ceremony.ClaimCertResult

// ShareClaimResult holds the result of an officer Shamir share claim. It is
// produced exactly once per officer per distribution round by
// CeremonyService.CompleteClaimShare.
type ShareClaimResult = ceremony.ShareClaimResult

// CertThumbprint returns the lowercase hex SHA-256 of a DER-encoded
// certificate. It is THE canonical binding identifier for the officer
// share-claim flow: the ceremony records it for each officer at enrollment and
// matches it against the thumbprint a transport derives from the caller's mTLS
// peer certificate. Consumers must not invent a second encoding.
var CertThumbprint = ceremony.CertThumbprint

// SignCSRInitResult holds the result of a CSR signing during init.
type SignCSRInitResult = ceremony.SignCSRInitResult

// PendingCert holds a certificate pending officer claim.
type PendingCert = ceremony.PendingCert

// ---------------------------------------------------------------------------
// Ceremony CA interface
// ---------------------------------------------------------------------------

// CeremonyCA is the interface the ceremony uses to issue certificates.
type CeremonyCA = ceremony.CeremonyCA

// SignCSROptions configures CSR signing behavior.
type SignCSROptions = ceremony.SignCSROptions

// IssueCertRequest holds a certificate issuance request.
type IssueCertRequest = ceremony.IssueCertRequest

// IssuedCert holds a newly issued certificate.
type IssuedCert = ceremony.IssuedCert

// ---------------------------------------------------------------------------
// Ceremony services
// ---------------------------------------------------------------------------

// CeremonyService manages the initialization ceremony lifecycle.
type CeremonyService = ceremony.CeremonyService

// CredentialServicer persists sealed credentials.
type CredentialServicer = ceremony.CredentialServicer

// ---------------------------------------------------------------------------
// Threshold types
// ---------------------------------------------------------------------------

// ThresholdOptions configures HSM threshold initialization.
type ThresholdOptions = ceremony.ThresholdOptions

// ThresholdStatus reports the status of HSM threshold operations.
type ThresholdStatus = ceremony.ThresholdStatus

// ThresholdInitializer is the interface for HSM threshold implementations.
type ThresholdInitializer = ceremony.ThresholdInitializer

// NoopThresholdInitializer is a no-op threshold initializer for non-HSM modes.
type NoopThresholdInitializer = ceremony.NoopThresholdInitializer

// ThresholdFactory creates ThresholdInitializer instances.
type ThresholdFactory = ceremony.ThresholdFactory

// ThresholdRegistry manages vendor-specific threshold implementations.
type ThresholdRegistry = ceremony.ThresholdRegistry

// ---------------------------------------------------------------------------
// Custodian group types
// ---------------------------------------------------------------------------

// CustodianGroupServicer manages custodian groups for Shamir shares.
type CustodianGroupServicer = ceremony.CustodianGroupServicer

// CustodianGroupService is the concrete custodian group service.
type CustodianGroupService = ceremony.CustodianGroupService

// ---------------------------------------------------------------------------
// Share service types
// ---------------------------------------------------------------------------

// ShareServicer manages Shamir share submission and tracking.
type ShareServicer = ceremony.ShareServicer

// ShareService is the concrete share service.
type ShareService = ceremony.ShareService

// ---------------------------------------------------------------------------
// Constructors
// ---------------------------------------------------------------------------

// NewCeremonyService creates a new CeremonyService.
var NewCeremonyService = ceremony.NewCeremonyService

// NewThresholdRegistry creates a new ThresholdRegistry.
var NewThresholdRegistry = ceremony.NewThresholdRegistry

// NewCustodianGroupService creates a new CustodianGroupService.
var NewCustodianGroupService = ceremony.NewCustodianGroupService

// NewShareService creates a new ShareService.
var NewShareService = ceremony.NewShareService

// AuthenticatorType identifies the hardware or software that backs an
// officer's signing credential.
type AuthenticatorType = ceremony.AuthenticatorType

// AuthenticatorType constants.
const (
	// AuthenticatorPIV indicates a PIV smart card credential.
	AuthenticatorPIV = ceremony.AuthenticatorPIV

	// AuthenticatorFIDO2 indicates a FIDO2 / CTAP2 credential.
	AuthenticatorFIDO2 = ceremony.AuthenticatorFIDO2

	// AuthenticatorSoftware indicates a software-backed credential.
	AuthenticatorSoftware = ceremony.AuthenticatorSoftware
)

// -----------------------------------------------------------------------
// Admin login-factor enrollment
//
// The ceremony owns only the factor ORDERING and the PREFLIGHT. Enrollment
// itself goes through the canonical AuthFactorPlugin / AuthFactorRegistry
// seam, which go-xkms already implements for every factor.
// -----------------------------------------------------------------------

// AdminFactor identifies one way an administrator authenticates. It is an
// alias of AuthFactorType: enrollment and login share one vocabulary.
type AdminFactor = ceremony.AdminFactor

// Admin factor constants. Only strong, hardware-backed, standards-based
// mechanisms are admissible; the stack is passwordless by policy.
const (
	// FactorMTLS enrolls an X.509 client certificate for mTLS. It is the
	// default and the baseline.
	FactorMTLS = ceremony.FactorMTLS

	// FactorFIDO2 enrolls a WebAuthn/FIDO2 credential — a passkey — over
	// CTAP2, with no browser involved.
	FactorFIDO2 = ceremony.FactorFIDO2

	// FactorPIV enrolls a credential held in a PIV smartcard slot.
	FactorPIV = ceremony.FactorPIV
)

// EnrollableAdminFactors returns the factors an administrator may enroll at
// init. Narrower than the full login-factor set: PKCS#11 is a valid login
// factor but is enrolled after the system is running.
func EnrollableAdminFactors() []AdminFactor { return ceremony.EnrollableAdminFactors() }

// IsEnrollableAdminFactor reports whether f may be enrolled at init.
func IsEnrollableAdminFactor(f AdminFactor) bool { return ceremony.IsEnrollableAdminFactor(f) }

// ParseAdminFactors turns operator-supplied --admin-factor values into a plan.
// Every daemon in the stack parses the flag through this one function so the
// same input means the same thing everywhere; accepts repeated flags and
// comma-separated lists interchangeably.
func ParseAdminFactors(values []string) (*AdminEnrollmentPlan, error) {
	return ceremony.ParseAdminFactors(values)
}

// AdminFactorNames lists the accepted --admin-factor values for flag help.
func AdminFactorNames() []string { return ceremony.AdminFactorNames() }

// AdminFactorAvailability is the OPTIONAL capability an AuthFactorPlugin may
// implement to report whether its enrollment can proceed right now (hardware
// attached). Factors that cannot fail environmentally simply omit it.
type AdminFactorAvailability = ceremony.FactorAvailability

// AdminEnrollmentPlan is the ordered set of factors to enroll for the admin.
// Preflight it against the canonical AuthFactorRegistry — there is no separate
// enrollment registry.
type AdminEnrollmentPlan = ceremony.EnrollmentPlan

// NewAdminEnrollmentPlan validates and normalises a requested factor list. An
// empty request yields the certificate baseline.
func NewAdminEnrollmentPlan(requested []AdminFactor) (*AdminEnrollmentPlan, error) {
	return ceremony.NewEnrollmentPlan(requested)
}

// Factor enrollment errors.
var (
	// ErrNilAdminFactorRegistry indicates a plan was checked against no
	// auth-factor registry.
	ErrNilAdminFactorRegistry = ceremony.ErrNilFactorRegistry
)

// UnsupportedAdminFactorError indicates a factor this build cannot enroll.
type UnsupportedAdminFactorError = ceremony.UnsupportedFactorError

// AdminFactorUnavailableError indicates a supported factor whose hardware is
// absent.
type AdminFactorUnavailableError = ceremony.FactorUnavailableError

// AdminEnrollmentParams supplies per-factor enrollment inputs. It is how
// hardware stays out of qrdb: go-xkms drives CTAP2/PIV and returns the
// finished credential inputs for the canonical Factor.Enroll.
type AdminEnrollmentParams = ceremony.AdminEnrollmentParams

// AdminEnrollmentResult pairs a factor with the enrollment it produced.
type AdminEnrollmentResult = ceremony.AdminEnrollmentResult

// AdminFactorPersister writes an enrolled factor into the caller's factor
// store. Factor.Enroll is pure, so without a persister the credential lives on
// the operator's authenticator and nowhere on the server.
type AdminFactorPersister = ceremony.AdminFactorPersister

// AdminFactorPersisterFunc adapts a plain function into an
// AdminFactorPersister.
type AdminFactorPersisterFunc = ceremony.AdminFactorPersisterFunc

// EnrollAdminFactors runs a plan's factors in order against the canonical
// auth-factor registry and persists each result. Run
// AdminEnrollmentPlan.Preflight first, before any state-changing ceremony work.
//
// persister may be nil, which enrolls without storing anything — useful only
// for probing hardware, since a factor that is not persisted cannot be used to
// log in.
func EnrollAdminFactors(
	ctx context.Context,
	plan *AdminEnrollmentPlan,
	reg *AuthFactorRegistry,
	userID, tenantID string,
	params AdminEnrollmentParams,
	persister AdminFactorPersister,
) ([]AdminEnrollmentResult, error) {
	return ceremony.EnrollAdminFactors(ctx, plan, reg, userID, tenantID, params, persister)
}

// AdminFactorEnrollmentError names the factor whose enrollment failed.
type AdminFactorEnrollmentError = ceremony.FactorEnrollmentError

// ---------------------------------------------------------------------------
// Enrollment specs — how an identity supplies its key material
// ---------------------------------------------------------------------------

// EnrollmentKind names how an operator supplies key material for an identity:
// a CSR to be signed, a certificate they already hold, or a bare public key.
//
// The same vocabulary serves the platform administrator and every security
// officer. Re-exported so xkmsd and quicbaasd parse `--admin` and `--so`
// identically to qrdbd rather than each growing its own dialect.
type EnrollmentKind = ceremony.EnrollmentKind

const (
	// EnrollmentKindCSR supplies a CSR; the CA signs it and the private key
	// never leaves the operator.
	EnrollmentKindCSR = ceremony.EnrollmentKindCSR
	// EnrollmentKindCert supplies a certificate the operator already holds,
	// typically resident on a PIV card or security key. Nothing is issued.
	EnrollmentKindCert = ceremony.EnrollmentKindCert
	// EnrollmentKindPubKey supplies a bare public key; trust is asserted by
	// the administrator running the ceremony.
	EnrollmentKindPubKey = ceremony.EnrollmentKindPubKey
)

// EnrollmentSpec is a parsed "<username>:<kind>:<path>" argument.
type EnrollmentSpec = ceremony.EnrollmentSpec

// ParseEnrollmentSpec parses one enrollment argument. The two-field form
// "<username>:<path>" is accepted and means csr.
func ParseEnrollmentSpec(arg string) (EnrollmentSpec, error) {
	return ceremony.ParseEnrollmentSpec(arg)
}

// ParseEnrollmentSpecs parses repeated enrollment arguments and rejects a
// duplicated username.
func ParseEnrollmentSpecs(args []string) ([]EnrollmentSpec, error) {
	return ceremony.ParseEnrollmentSpecs(args)
}

// EnrollmentKindNames lists the accepted kind names for flag help.
func EnrollmentKindNames() []string { return ceremony.EnrollmentKindNames() }

// ---------------------------------------------------------------------------
// Security Officer registration and status
//
// These are the types behind step 1 of the Security Officer distribution model
// (docs/seal/shamir.md): an admin submits each officer's CSR, the ceremony CA
// signs it, and the officer is bound to the issued certificate's thumbprint.
// Registration returns the certificate and the assigned share index — never
// share bytes; splitting happens later, at SealAndDistribute.
//
// They are re-exported so the layers above (go-xkms, and go-quicbaas through
// it) can offer the same officer flow without importing pkg/ceremony directly.
// ---------------------------------------------------------------------------

// RegisterOfficerRequest carries one officer's enrolment material.
type RegisterOfficerRequest = ceremony.RegisterOfficerRequest

// RegisterOfficerResult returns the signed certificate and the officer's
// assigned Shamir share index. It never carries share material.
type RegisterOfficerResult = ceremony.RegisterOfficerResult

// CeremonyOfficerStatus is a non-secret view of an enrolled officer: username,
// certificate thumbprint, share index, auth factor, and claim state.
type CeremonyOfficerStatus = ceremony.OfficerStatus

// EnrollExternalOfficerRequest enrols an officer whose key material is held
// externally — a certificate from a corporate CA, or a bare public key —
// rather than being minted from a CSR by the ceremony CA.
type EnrollExternalOfficerRequest = ceremony.EnrollExternalOfficerRequest

// EnrollExternalOfficerResponse reports the outcome of an external enrolment.
type EnrollExternalOfficerResponse = ceremony.EnrollExternalOfficerResponse
