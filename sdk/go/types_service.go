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

// ServiceBatchServicer is the service-layer interface for atomic batch
// operations. A consumer passes a value of this type to
// embedded.WithBatchService; the BatchResponse it returns carries the single
// MVCC revision every op in the batch committed at.
type ServiceBatchServicer = service.BatchServicer

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

// NewServiceTenantService creates a new service-layer TenantServicer
// backed by a metadata store and a BarrierRegistry.
var NewServiceTenantService = tenantpkg.NewTenantService
