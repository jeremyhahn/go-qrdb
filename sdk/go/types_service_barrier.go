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

import "github.com/jeremyhahn/go-qrdb/pkg/service"

// ---------------------------------------------------------------------------
// Enable-encryption service sentinel errors
//
// Re-exported so a composing service (e.g. go-quicbaas, go-xkms) can map an
// enable-encryption denial to the right API status at its own boundary without
// importing pkg/service directly. ErrSharedBarrierImmutable maps to 403,
// ErrMigrationProviderUnavailable to 501, ErrTenantBarrierNotFound to 404.
// ---------------------------------------------------------------------------
var (
	// ErrSharedBarrierImmutable is returned when a tenant on the shared system
	// barrier attempts to change its encryption; it must be isolated first.
	ErrSharedBarrierImmutable = service.ErrSharedBarrierImmutable

	// ErrMigrationProviderUnavailable is returned when enable-encryption is not
	// available on this node (no BarrierMigrationProvider wired).
	ErrMigrationProviderUnavailable = service.ErrMigrationProviderUnavailable

	// ErrTenantBarrierNotFound is returned when a tenant-scoped migration names a
	// tenant that has no dedicated barrier.
	ErrTenantBarrierNotFound = service.ErrTenantBarrierNotFound
)

// ---------------------------------------------------------------------------
// Barrier service (service-layer barrier lifecycle)
// ---------------------------------------------------------------------------

// BarrierServicer is the service-layer interface for barrier lifecycle
// operations (init/unseal/seal/status/rekey/recovery/root-token).
type BarrierServicer = service.BarrierServicer

// BarrierService is the concrete BarrierServicer implementation backed by a
// StorageBarrier.
type BarrierService = service.BarrierService

// BarrierStatusInfo reports the current state of a barrier.
type BarrierStatusInfo = service.BarrierStatusInfo

// ShamirInitInfo holds the result of a Shamir barrier initialization.
type ShamirInitInfo = service.ShamirInitInfo

// UnsealShareProgress reports progress toward a Shamir unseal quorum.
type UnsealShareProgress = service.UnsealShareProgress

// RecoveryKeysInfo holds recovery keys produced during initialization.
type RecoveryKeysInfo = service.RecoveryKeysInfo

// RootTokenInfo holds a generated root token.
type RootTokenInfo = service.RootTokenInfo

// BarrierOfficerRegisterRequest carries the parameters for registering a
// Security Officer against the system or recovery barrier.
type BarrierOfficerRegisterRequest = service.BarrierOfficerRegisterRequest

// BarrierOfficerRegisterResult is the outcome of a successful officer
// registration.
type BarrierOfficerRegisterResult = service.BarrierOfficerRegisterResult

// BarrierOfficerStatus is a non-secret view of an enrolled Security Officer.
type BarrierOfficerStatus = service.BarrierOfficerStatus

// BarrierShareClaimChallenge is the challenge issued by BarrierShareClaimBegin.
type BarrierShareClaimChallenge = service.BarrierShareClaimChallenge

// BarrierShareClaimResult is returned to the Security Officer exactly once
// upon a successful BarrierShareClaimComplete.
type BarrierShareClaimResult = service.BarrierShareClaimResult

// NewBarrierService creates a new BarrierServicer backed by the given barrier.
var NewBarrierService = service.NewBarrierService

// NewBarrierServiceWithCeremony creates a new BarrierServicer backed by the
// given barrier and wired to the ceremony service for Security Officer share
// distribution. The ceremony service is installed as the barrier's
// CeremonyDistributor and RecoveryDistributor, enabling the officer / share /
// recovery verbs that NewBarrierService alone leaves unconfigured.
var NewBarrierServiceWithCeremony = service.NewBarrierServiceWithCeremony

// ---------------------------------------------------------------------------
// Ceremony service (additional service-layer types)
// ---------------------------------------------------------------------------

// ServiceCeremonyServicer is re-exported in types_service.go. The two
// additional ceremony result types below are not covered there.

// CeremonyCustodianChallenge contains the custodian challenge issued during
// the ceremony custodian-claim handshake.
type CeremonyCustodianChallenge = service.CeremonyCustodianChallenge

// CeremonyUnsealShareResult contains the result of submitting an unseal share
// during the initialization ceremony.
type CeremonyUnsealShareResult = service.CeremonyUnsealShareResult

// ---------------------------------------------------------------------------
// Auth factor services (service layer)
// ---------------------------------------------------------------------------

// AuthFactorServicer is the service-layer interface for auth factor
// management.
type AuthFactorServicer = service.AuthFactorServicer

// UserAuthFactorServicer is the service-layer interface for per-user auth
// factor management.
type UserAuthFactorServicer = service.UserAuthFactorServicer

// UserAuthFactorServiceConfig configures a UserAuthFactorServicer.
type UserAuthFactorServiceConfig = service.UserAuthFactorServiceConfig

// ActiveFactorChecker is the FAIL-CLOSED active-factor query the production
// user auth-factor service implements alongside UserAuthFactorServicer;
// deferred tenant-admin provisioning (BootstrapDependencies.AdminFactors)
// consumes it. Assert the servicer to this interface when wiring.
type ActiveFactorChecker = service.ActiveFactorChecker

// NewUserAuthFactorService creates a new UserAuthFactorServicer.
var NewUserAuthFactorService = service.NewUserAuthFactorService
