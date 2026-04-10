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
