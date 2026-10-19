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
	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

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

// ClusterMigrationReport is the cluster-wide encryption migration a barrier
// status carries while one is in progress.
type ClusterMigrationReport = clustermig.Report

// ClusterMigrationNodeReport is one owner's place in a cluster migration.
type ClusterMigrationNodeReport = clustermig.NodeReport

// ShamirInitInfo holds the result of a Shamir barrier initialization.
type ShamirInitInfo = service.ShamirInitInfo

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

// BarrierShareClaimResult delivers a holder's sealed share envelope exactly
// once, upon a successful BarrierShareClaimComplete.
type BarrierShareClaimResult = service.BarrierShareClaimResult

// QuorumBeginRequest asks for a holder's quorum challenge.
type QuorumBeginRequest = service.QuorumBeginRequest

// QuorumSubmitRequest submits one authenticated holder's share.
type QuorumSubmitRequest = service.QuorumSubmitRequest

// QuorumOutcomeInfo reports a quorum round's progress after a submission.
type QuorumOutcomeInfo = service.QuorumOutcomeInfo

// QuorumRoundInfo describes an open quorum round.
type QuorumRoundInfo = service.QuorumRoundInfo

// NewBarrierService creates a new BarrierServicer backed by the given barrier.
var NewBarrierService = service.NewBarrierService

// NewBarrierServiceWithCeremony creates a new BarrierServicer backed by the
// given barrier and wired to the ceremony service, which distributes share
// envelopes to the enrolled holders and verifies their quorum submissions,
// enabling the holder verbs NewBarrierService alone leaves unconfigured.
var NewBarrierServiceWithCeremony = service.NewBarrierServiceWithCeremony

// ---------------------------------------------------------------------------
// Auth factor services (service layer)
// ---------------------------------------------------------------------------

// AuthFactorServicer is the service-layer interface for auth factor
// management.
type AuthFactorServicer = service.AuthFactorServicer

// AuthFactorTenantMismatchError is returned when EnrollFactor is called with
// an EnrollParams.TenantID that names a different tenant from the one the
// call's context resolves to (aliases service.AuthFactorTenantMismatchError).
// An empty TenantID is filled in from the context, and one that matches the
// context passes; anything else is refused with this error rather than
// forwarded unchecked or silently corrected — see docs/auth-factors/engineering.md.
//
// Exported here, in the same change that introduced the condition, per this
// seam's own error contract (docs/auth-factors/architecture.md#the-seams-error-contract):
// a condition with no SDK alias is a handler arm an out-of-repo caller can
// never construct or catch.
type AuthFactorTenantMismatchError = service.AuthFactorTenantMismatchError

// SystemBarrierInitRefusedError is returned by BarrierService's
// BarrierInitialize and BarrierInitializeShamir when the service is wired to
// a ceremony: the system barrier is initialized only through the ceremony's
// Initialize (qrdb init), never through barrier init (aliases
// service.SystemBarrierInitRefusedError). A composing service matches it with
// errors.As without importing pkg/service.
type SystemBarrierInitRefusedError = service.SystemBarrierInitRefusedError

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

// AttestationRecorder is the trusted-server path for recording what an
// enrollment ceremony PROVED about a factor -- the ONE writer of the
// server-owned attest_* metadata namespace. The production user auth-factor
// service implements it alongside UserAuthFactorServicer; the in-process host
// that ran the ceremony reaches it by asserting the servicer to this
// interface. It is deliberately absent from UserAuthFactorServicer and from
// every transport: a factor written through Add or Rotate on any wire is
// recorded as Unattested, and no handler maps to this interface (go-qrdb#342).
type AttestationRecorder = service.AttestationRecorder

// NewUserAuthFactorService creates a new UserAuthFactorServicer.
var NewUserAuthFactorService = service.NewUserAuthFactorService
