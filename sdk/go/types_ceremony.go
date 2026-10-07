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

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
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

	// StateRestoreFailed indicates the server could not determine the
	// ceremony's actual state at startup (a barrier storage read timed out
	// or failed); it is a fail-closed state, not "not yet initialized" --
	// CeremonyInitialize refuses to run against it.
	StateRestoreFailed = ceremony.StateRestoreFailed
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

// ShareClaimResult holds the result of an officer Shamir share claim. It is
// produced exactly once per officer per distribution round by
// CeremonyService.CompleteClaimShare.
type ShareClaimResult = ceremony.ShareClaimResult

// CertThumbprint returns the lowercase hex SHA-256 of a DER-encoded
// certificate. It is THE canonical binding identifier for share holders: the
// ceremony records it for each holder at enrolment and matches it against the
// thumbprint a transport derives from the caller's mTLS peer certificate.
// Consumers must not invent a second encoding.
var CertThumbprint = mtls.Thumbprint

// SignCSRInitResult holds the result of a CSR signing during init.
type SignCSRInitResult = ceremony.SignCSRInitResult

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
// Constructors
// ---------------------------------------------------------------------------

// NewCeremonyService creates a new CeremonyService.
var NewCeremonyService = ceremony.NewCeremonyService

// NewThresholdRegistry creates a new ThresholdRegistry.
var NewThresholdRegistry = ceremony.NewThresholdRegistry

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
// The vocabulary serves the platform administrator. Share holders are
// enrolled from a CSR only: the ceremony CA assigns their certificate
// identity. Re-exported so downstream daemons parse `--admin` identically to
// qrdbd rather than each growing its own dialect.
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
// Security Officers and key custodians are enrolled the same way
// (docs/shamir-custody): an admin or Security Officer submits the holder's
// CSR, the ceremony CA issues a certificate whose identity it assigns, and the
// holder is bound to that certificate. Registration never returns share
// material; each holder claims its own HPKE-sealed envelope.
//
// They are re-exported so the layers above (go-xkms, and go-quicbaas through
// it) can offer the same officer flow without importing pkg/ceremony directly.
// ---------------------------------------------------------------------------

// RegisterOfficerRequest carries one officer's enrolment material.
type RegisterOfficerRequest = ceremony.RegisterOfficerRequest

// RegisterOfficerResult returns the issued certificate. It never carries
// share material.
type RegisterOfficerResult = ceremony.RegisterOfficerResult

// CeremonyOfficerStatus is a non-secret view of an enrolled holder: role,
// certificate thumbprint, share index, factors, and claim state.
type CeremonyOfficerStatus = ceremony.OfficerStatus

// FactorEnrolment enrols one additional authentication factor for a holder.
type FactorEnrolment = ceremony.FactorEnrolment

// EncryptionKeyEnrolment enrols a separate HPKE share-envelope key.
type EncryptionKeyEnrolment = ceremony.EncryptionKeyEnrolment

// QuorumChallenge is the factor challenge a holder answers to submit a share.
type QuorumChallenge = ceremony.QuorumChallenge

// ---------------------------------------------------------------------------
// Share envelopes
//
// A holder's share reaches it as a ShareEnvelope, HPKE-sealed to the holder's
// enrolled encryption key. The holder parses the envelope it claimed and opens
// it with its private key; the node keeps no copy.
// ---------------------------------------------------------------------------

// A holder claims its envelope by signing a share-claim message with its
// certificate key: ShareClaimContext, the barrier's claim label, the holder
// and the claim nonce. A holder answers an mtls quorum challenge by signing a
// message of its own (SignQuorumChallenge), so neither signature verifies as
// the other.

// ShareClaimContext is the share-claim message's domain-separation prefix.
const ShareClaimContext = ceremony.ShareClaimContext

// Claim labels of the system barrier's two domains.
const (
	SystemClaimLabel   = ceremony.SystemClaimLabel
	RecoveryClaimLabel = ceremony.RecoveryClaimLabel
)

// TenantClaimLabel returns the claim label of a tenant's barrier.
var TenantClaimLabel = ceremony.TenantClaimLabel

// DatabaseClaimLabel returns the claim label of a database's barrier.
var DatabaseClaimLabel = ceremony.DatabaseClaimLabel

// ShareClaimMessage returns the message a holder signs to claim an envelope.
var ShareClaimMessage = ceremony.ShareClaimMessage

// SignShareClaim signs a share claim with the holder's certificate key.
var SignShareClaim = ceremony.SignShareClaim

// QuorumFactorMTLS is the name of the mtls authentication factor a holder
// answers a quorum challenge with.
const QuorumFactorMTLS = mtls.Name

// ParseHolderSignerPEM parses a holder's PEM private key (EC, PKCS#1 or
// PKCS#8) into the crypto.Signer that signs share claims and quorum
// challenges.
var ParseHolderSignerPEM = mtls.ParseSignerPEM

// QuorumChallengePurposeKey is the method-data key of the purpose a quorum
// challenge is bound to ("purpose").
const QuorumChallengePurposeKey = mtls.MethodDataPurpose

// QuorumChallengeMessage returns the message an mtls quorum challenge is
// answered over: the mtls challenge context, the challenge ID, its purpose
// and its nonce, NUL-separated.
var QuorumChallengeMessage = mtls.ChallengeMessage

// SignQuorumChallenge answers an mtls quorum challenge with the holder's
// certificate key: SignQuorumChallenge(key, ch.ChallengeID,
// ch.MethodData[QuorumChallengePurposeKey], ch.Nonce).
var SignQuorumChallenge = mtls.SignChallenge

// ShareEnvelope is a holder's sealed share.
type ShareEnvelope = ceremony.ShareEnvelope

// ParseShareEnvelope decodes a claimed envelope.
var ParseShareEnvelope = ceremony.ParseShareEnvelope

// KEMSuite is an HPKE suite a holder's envelope key can use.
type KEMSuite = keydelivery.Suite

// LookupKEMSuite returns the named HPKE suite.
var LookupKEMSuite = keydelivery.LookupSuite

// KEMSuiteNames lists the supported HPKE suites.
var KEMSuiteNames = keydelivery.SuiteNames

// The supported HPKE suites.
const (
	KEMP256           = keydelivery.KEMP256
	KEMP384           = keydelivery.KEMP384
	KEMX25519         = keydelivery.KEMX25519
	KEMMLKEM768X25519 = keydelivery.KEMMLKEM768X25519
)
