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
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	tenantpkg "github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Service interfaces
// ---------------------------------------------------------------------------

// KVServicer is the service-layer interface for key-value operations.
type KVServicer = service.KVServicer

// AdminServicer is the service-layer interface for admin/cluster operations.
type AdminServicer = service.AdminServicer

// CompactionPolicy is the scheduled-compaction policy AdminServicer's
// NodeCompaction returns and SetNodeCompaction takes. A nil policy, or one
// whose mode is CompactionOff, compacts nothing.
type CompactionPolicy = compaction.Policy

// CompactionMode selects a CompactionPolicy: CompactionOff, CompactionRevision
// or CompactionPeriod.
type CompactionMode = compaction.Mode

// The CompactionMode values. An empty mode means CompactionOff.
const (
	CompactionOff      = compaction.Off
	CompactionRevision = compaction.Revision
	CompactionPeriod   = compaction.Period
)

// CompactionMinRetainPeriod is the shortest RetainPeriod a period policy
// accepts.
const CompactionMinRetainPeriod = compaction.MinRetainPeriod

// CompactionInvalidPolicyError is returned by CompactionPolicy.Validate, and by
// SetNodeCompaction, for a policy with an unknown mode, a field that does not
// belong to its mode, or an out-of-range retention.
type CompactionInvalidPolicyError = compaction.InvalidPolicyError

// ServiceBatchServicer is the service-layer interface for atomic batch
// operations. A consumer passes a value of this type to
// embedded.WithBatchService; the BatchResponse it returns carries the single
// MVCC revision every op in the batch committed at.
type ServiceBatchServicer = service.BatchServicer

// ServiceIndexServicer is the service-layer interface for secondary-index
// operations. A consumer passes a value of this type, built with
// NewServiceIndexService, to embedded.WithIndexService; without it every index call
// on the embedded transport fails with a NilServiceError naming
// IndexServicer.
type ServiceIndexServicer = service.IndexServicer

// Host is the interface for the backend host that services operate on.
type Host = service.Host

// ---------------------------------------------------------------------------
// Service types
// ---------------------------------------------------------------------------

// KVService is the concrete KV service implementation.
type KVService = service.KVService

// KVServiceOption configures a KVService at construction time.
type KVServiceOption = service.KVServiceOption

// AdminService is the concrete admin service implementation.
type AdminService = service.AdminService

// AdminServiceOption configures an AdminService at construction time.
type AdminServiceOption = service.AdminServiceOption

// BackendKVService is a KVServicer backed directly by a database.Backend.
type BackendKVService = service.BackendKVService

// ---------------------------------------------------------------------------
// Service info types
// ---------------------------------------------------------------------------

// HealthStatus reports the health of the cluster.
type HealthStatus = service.HealthStatus

// StatusInfo provides detailed cluster status.
type StatusInfo = service.StatusInfo

// PeerInfo describes a single cluster peer.
type PeerInfo = service.PeerInfo

// ShardPeers describes the peers of a single shard.
type ShardPeers = service.ShardPeers

// PeersInfo describes all shard peers in the cluster.
type PeersInfo = service.PeersInfo

// ---------------------------------------------------------------------------
// Constructors
// ---------------------------------------------------------------------------

// NewKVService creates a new KVService.
var NewKVService = service.NewKVService

// NewAdminService creates a new AdminService.
var NewAdminService = service.NewAdminService

// NewTransactionService creates a new TransactionServicer.
//
// Re-exported alongside NewKVService and NewAdminService because the embedded
// transport needs all three: a consumer that wires embedded.WithTransactionService
// otherwise has to import go-qrdb/pkg/service directly for this one constructor,
// which defeats the point of the SDK surface. Reported by go-xkms, whose CA key
// store reaches PutIfAbsent and CompareAndSwap through this service.
var NewTransactionService = service.NewTransactionService

// NewServiceIndexService creates a new ServiceIndexServicer over the given Host and shard.
//
// Re-exported for the same reason as NewTransactionService: a consumer that
// builds its own embedded transport wires embedded.WithIndexService with it,
// over the Host and shard it already passes to NewKVService, without
// importing go-qrdb/pkg/service.
var NewServiceIndexService = service.NewIndexService

// NewBackendKVService creates a new BackendKVService.
var NewBackendKVService = service.NewBackendKVService

// ---------------------------------------------------------------------------
// Database service types
// ---------------------------------------------------------------------------

// DatabaseServicer defines CRUD operations for database management.
type DatabaseServicer = service.DatabaseServicer

// DatabaseService implements DatabaseServicer using a database.Manager.
type DatabaseService = service.DatabaseService

// DatabaseServiceOption configures optional DatabaseService behavior.
type DatabaseServiceOption = service.DatabaseServiceOption

// NewDatabaseService creates a new DatabaseService backed by the given manager.
var NewDatabaseService = service.NewDatabaseService

// ---------------------------------------------------------------------------
// Ceremony service types (service-layer wrappers around pkg/ceremony)
// ---------------------------------------------------------------------------

// ServiceCeremonyServicer provides ceremony lifecycle operations including
// initialization, certificate claiming, share distribution, CSR signing,
// and status queries. This is the service-layer wrapper around the ceremony
// package's lower-level CeremonyService.
type ServiceCeremonyServicer = service.CeremonyServicer

// CeremonyInitResult contains the output of a successful ceremony
// initialization including certificates, keys, and enrollment state.
type CeremonyInitResult = service.CeremonyInitResult

// CeremonySignCSRResult contains the output of a successful CSR signing
// operation during the ceremony.
type CeremonySignCSRResult = service.CeremonySignCSRResult

// CeremonyStatusInfo describes the current state of the initialization
// ceremony.
type CeremonyStatusInfo = service.CeremonyStatusInfo

// NewServiceCeremonyService creates a new service-layer CeremonyServicer
// backed by the given ceremony.CeremonyService.
var NewServiceCeremonyService = service.NewCeremonyService

// ---------------------------------------------------------------------------
// Credential service types (service-layer)
// ---------------------------------------------------------------------------

// ServiceCredentialServicer provides credential management operations
// including store, retrieve, submit, delete, and strategy queries. This is
// the service-layer interface, distinct from the ceremony package's simpler
// CredentialServicer which only provides StoreCredential.
type ServiceCredentialServicer = service.CredentialServicer

// CredentialService implements ServiceCredentialServicer by delegating to a
// credential store backend.
type CredentialService = service.CredentialService

// NewCredentialService creates a new service-layer CredentialServicer.
var NewCredentialService = service.NewCredentialService

// ---------------------------------------------------------------------------
// Tenant service types (multi-tenant lifecycle including per-tenant barrier)
// ---------------------------------------------------------------------------

// ServiceTenantServicer provides multi-tenant lifecycle operations:
// CRUD on tenant metadata, quota management, and per-tenant barrier
// init/unseal/seal/status. SDK consumers use this alias instead of importing
// the internal pkg/tenant package.
type ServiceTenantServicer = tenantpkg.TenantServicer

// ServiceTenantInfo is the tenant metadata record returned by GetTenant /
// CreateTenant / UpdateTenant. Mirrors pkg/tenant.TenantInfo.
type ServiceTenantInfo = tenantpkg.TenantInfo

// ServiceTenantBarrierStatus reports the current state of a per-tenant
// barrier as returned by ServiceTenantServicer.TenantBarrierStatus.
type ServiceTenantBarrierStatus = tenantpkg.TenantBarrierStatus

// ServiceTenantBarrierInitResult reports the outcome of a per-tenant barrier
// initialization (TenantBarrierInit / TenantBarrierInitWithStrategy). It is
// re-exported so an SDK consumer that implements or wraps the
// ServiceTenantServicer can name the return type without importing pkg/tenant.
type ServiceTenantBarrierInitResult = tenantpkg.TenantBarrierInitResult

// ServiceTenantBarrierInitBeginRequest asks for the first half of a two-phase
// signed tenant barrier initialization (TenantBarrierInitBegin): the init
// statement the tenant's first admin signs away from the node, under the key
// named by its DER SubjectPublicKeyInfo.
type ServiceTenantBarrierInitBeginRequest = tenantpkg.TenantBarrierInitBeginRequest

// ServiceTenantBarrierInitBeginResult carries the statement to sign, detached,
// and the single-use reference that redeems it before it expires.
type ServiceTenantBarrierInitBeginResult = tenantpkg.TenantBarrierInitBeginResult

// ServiceTenantBarrierInitCompleteRequest redeems a begun statement with the
// detached signature over it (TenantBarrierInitComplete).
type ServiceTenantBarrierInitCompleteRequest = tenantpkg.TenantBarrierInitCompleteRequest

// ServiceTenantSignedInitError is TenantBarrierInitComplete's refusal of a
// statement reference that cannot be redeemed: never issued, issued for
// another tenant, already spent (a replay), or expired.
type ServiceTenantSignedInitError = tenantpkg.SignedInitError

// ServiceTenantSignedInitFieldRequiredError refuses a begin or complete
// request missing a required field.
type ServiceTenantSignedInitFieldRequiredError = tenantpkg.SignedInitFieldRequiredError

// ServiceTenantTooManySignedInitsError refuses a begin while the node holds
// its limit of unexpired statements.
type ServiceTenantTooManySignedInitsError = tenantpkg.TooManySignedInitsError

// ServiceTenantSignedInitUnsupportedError refuses a begin or complete on a
// node that records no init statements.
type ServiceTenantSignedInitUnsupportedError = tenantpkg.SignedInitUnsupportedError

// ServiceTenantSignedInitReason names why a statement reference was refused.
type ServiceTenantSignedInitReason = tenantpkg.SignedInitReason

// The ServiceTenantSignedInitReason values.
const (
	ServiceTenantSignedInitUnknown = tenantpkg.SignedInitUnknown
	ServiceTenantSignedInitExpired = tenantpkg.SignedInitExpired
)

// ServiceTenantSignedInitTTL is how long a begun statement's reference stays
// redeemable.
const ServiceTenantSignedInitTTL = tenantpkg.SignedInitTTL

// NewServiceTenantService creates a new service-layer TenantServicer
// backed by a metadata store and a BarrierRegistry.
var NewServiceTenantService = tenantpkg.NewTenantService
