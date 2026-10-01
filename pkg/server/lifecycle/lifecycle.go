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

// Package lifecycle provides shared service construction for qrdbd daemon
// and embedded GUI modes. BuildServices accepts already-initialised
// infrastructure (Host, WatchHub, optional seal barrier) and constructs the
// complete service graph, returning a Node that aggregates all services and
// their closers.
//
// This is Step 1 of the lifecycle refactor: service construction only. TLS
// bootstrap, storage open, and barrier init remain with callers for now and
// will be moved in subsequent steps.
package lifecycle

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	vectorstorage "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// Infra holds already-initialised infrastructure that BuildServices requires.
// Callers are responsible for constructing each field before calling
// BuildServices: storage backends, Raft host, watch hub, and (when seal is
// enabled) barrier and registry.
type Infra struct {
	// Host is the service.Host implementation (Raft adapter or LocalBackend).
	// Required.
	Host service.Host

	// NoRevisionHistory declares that the system shard's state machine keeps
	// no MVCC revision history (see StateMachineKeepsRevisionHistory). The
	// MVCC service then reports a lease-attached put without reading it back
	// (service.WithoutRevisionHistory), and revision-answering surfaces
	// refuse to serve over it.
	NoRevisionHistory bool

	// WatchHub delivers change events between the state machine and watchers.
	// Required.
	WatchHub *watch.Hub

	// DataDir is the base data directory used for vector storage, database
	// metadata, and (when SealBarrier is non-nil) the certificate store.
	// Required.
	DataDir string

	// DatabaseManager is an already-open database manager. When non-nil it
	// is used directly; when nil BuildServices creates a new manager rooted
	// at DataDir. Callers that create the manager themselves (e.g. to run
	// migrations before service construction) should pass it here.
	DatabaseManager *database.Manager

	// KVOptions are optional configuration options applied when constructing
	// the KVService. Used for codec injection in daemon mode.
	KVOptions []service.KVServiceOption

	// SealBarrier is the initialised storage barrier. When non-nil, the
	// barrier, credential, ceremony, cert, and tenant services are wired.
	// When nil, those services are omitted from the Node and will be nil.
	SealBarrier *seal.StorageBarrier

	// SealEnabled records the caller's own configuration decision (its
	// seal.enabled equivalent) independent of whether SealBarrier ended up
	// non-nil. A composer sets this from the config value it read BEFORE
	// attempting to construct a barrier, never by inspecting SealBarrier
	// afterward: a barrier that was requested but failed to construct
	// (a bad passphrase, a corrupt seal store, a strategy error) leaves
	// SealBarrier nil too, and SealEnabled is what keeps that construction
	// failure from being reported as an operator's decision to run without
	// encryption. Defaults to false (matching seal.enabled's own default)
	// for composers that never intended a barrier at all; a composer for
	// which "no barrier" is not a state it ever chooses (for example, one
	// that always builds at least a none-strategy barrier) should set this
	// true unconditionally so the plaintext-at-rest warning below never
	// misattributes one of ITS failures either.
	SealEnabled bool

	// Encryptor is the single at-rest encryption authority for all Pebble
	// stores opened by BuildServices (vector storage, etc.). When nil,
	// BuildServices derives a no-op Encryptor from SealBarrier so that
	// callers that only set SealBarrier continue to work unchanged.
	// Prefer passing an Encryptor built by the daemon once so all stores
	// share the same instance.
	Encryptor *atrest.Encryptor

	// BarrierRegistry manages per-tenant barrier lifecycle. Required when
	// SealBarrier is non-nil.
	BarrierRegistry *seal.BarrierRegistry

	// StrategyRegistry is the canonical name→factory lookup for sealing
	// strategies. When non-nil it is injected into the TenantService so
	// operators can select hardware/cloud seal strategies (tpm2, pkcs11,
	// aws-kms, etc.) for per-tenant barriers. When nil the TenantService
	// falls back to the built-in software-only strategies (passphrase, none).
	StrategyRegistry seal.StrategyRegistry

	// CredentialCoreSvc is the core credential service built on top of the
	// SealBarrier. Required when SealBarrier is non-nil.
	CredentialCoreSvc *credentials.CredentialService

	// CeremonyDataDir is the data directory for the ceremony service. When
	// empty and SealBarrier is non-nil, it defaults to DataDir/seal.
	CeremonyDataDir string

	// CeremonyCA is an optional certificate authority used to sign Security
	// Officer CSRs outside the full initialization ceremony. When non-nil it
	// is wired into the ceremony service via SetCA, which never overwrites a
	// CA already established by Initialize. Daemon deployments pass the
	// node's cluster CA here so officer certificates chain to the same CA
	// the server trusts for mTLS client authentication.
	CeremonyCA ceremony.CeremonyCA

	// CeremonyCoreSvc is an already-constructed core ceremony service to
	// reuse instead of building a new one. The sealed-window boot phase
	// (BuildSealServicesOnly) accumulates in-memory state — enclave-stored
	// shares and cache-only officer registrations — that must survive into
	// the post-unseal service graph; threading the instance here preserves
	// it. When nil a fresh ceremony service is constructed.
	CeremonyCoreSvc *ceremony.CeremonyService

	// SPIFFETrustDomain is the operator-configured SPIFFE trust domain
	// (auth.spiffe_trust_domain). When non-empty, the certificate provider
	// mints every client certificate with a tenant-scoped SPIFFE URI SAN under
	// it (cert.WithTrustDomain), so the mTLS authenticator's pinned
	// derivation resolves the certificates this node's CA issues. It must be
	// the value the authenticator pins. Empty issues certificates exactly as
	// before, with no SAN.
	SPIFFETrustDomain string

	// MTLSBindingPolicy is the operator's auth.mtls_binding_policy, as
	// configured. Under "required" the user service refuses every change that
	// would leave the bootstrap admin unable to log in by certificate
	// (user.WithBootstrapAdminBindingRequired), so no transport can lock the
	// admin out. An unrecognised value fails BuildServices. Empty means
	// "enrolled".
	MTLSBindingPolicy string
	// PINBackend holds the node's SO and User PINs. Composers wire either
	// an injected (hardware) backend or NewSealStorePINBackend over the
	// node-local seal store. When nil every PIN-gated operation fails
	// closed with pin.ErrBackendNotConfigured: there is no fallback.
	PINBackend pin.PINBackend

	// HolderTrustDomain is the SPIFFE trust domain share-holder
	// certificates are issued in. Holder registration fails with
	// ceremony.ErrTrustDomainNotConfigured while it is empty.
	HolderTrustDomain string

	// FactorRegistry resolves the authentication factors share holders
	// enrol and answer quorum challenges with. The built-in mtls factor is
	// always available; hardware factors (FIDO2, PIV, ...) are registered
	// by the composer.
	FactorRegistry *authfactors.FactorRegistry

	// Logger is used for service-level structured logging. When nil,
	// slog.Default() is used.
	Logger *slog.Logger

	// EmbeddingConfig configures the embedding provider registry. When
	// Providers is empty no embedding service is constructed and Node.Embedding
	// remains nil.
	EmbeddingConfig qrdbconfig.EmbeddingConfig

	// RAGConfig configures the Retrieval-Augmented Generation pipeline.
	// Node.RAG is only non-nil when EmbeddingConfig.Providers is non-empty and
	// a vector service is available.
	RAGConfig qrdbconfig.RAGConfig

	// ReplicaID is this node's Raft replica identifier (config.Raft.ReplicaID).
	// It seeds the coordination-free Snowflake generator that assigns uint64
	// user IDs, keeping IDs globally unique across cluster members. Defaults
	// to 1 (standalone) when zero. Must be <= 1023 (the Snowflake node field).
	ReplicaID uint64

	// StrictTenantIsolation enables strict tenant isolation for vector
	// namespace operations. When true, CreateNamespace rejects requests that
	// carry no explicit tenant ID (i.e. the system tenant is used as the
	// implicit owner). This should be set to true in multi-tenant production
	// environments and is automatically engaged under FIPS-140.
	StrictTenantIsolation bool

	// DefaultTenantQuotas are the operator-configured default resource quotas
	// applied to tenants whose own quotas are unset. When nil, nil-quota
	// tenants fall back to the zero-value Quotas (all limits at system default).
	DefaultTenantQuotas *quota.Quotas

	// DefaultTenantEntitlements are the operator-configured default feature
	// entitlements applied to tenants whose own entitlements are unset. When
	// nil, nil-entitlement tenants fall back to the conservative built-in
	// defaults (prefix-only isolation; dedicated raft/volume denied).
	DefaultTenantEntitlements *quota.Entitlements

	// Profile, when non-nil, is the effective capability profile for this
	// node (build tags x run mode x config, config.EffectiveCapabilities).
	// It is pinned onto the AdminService so the node reports its own truth
	// even when several nodes share one process (the SDK). When nil, the
	// AdminService falls back to the process-global capabilities view.
	Profile *capabilities.Capabilities

	// BackupSink, when non-nil, is wired into the Backup service so backup
	// artifacts (shard payloads + metadata) are persisted durably instead of
	// held only in the daemon process's memory. Without it, backups do not
	// survive a process restart, which makes them unusable for disaster
	// recovery. The caller constructs and owns the sink's lifecycle (e.g. the
	// storage.Storage it wraps); BuildServices only reads from it.
	BackupSink service.BackupSink

	// AuthMethodVocabulary, when non-nil, is the permitted-method vocabulary of
	// THIS node's auth chain (usually middleware.AuthMethodVocabulary's
	// IsAuthMethod, bound once the node's middleware is built). Every user
	// store BuildServices creates validates written policies against it
	// (user.WithAuthMethodVocabulary). Nil means the built-in tokens only.
	AuthMethodVocabulary func(method string) bool
}

// Node aggregates all services built by BuildServices and provides a
// coordinated Stop method that closes resources in reverse order.
type Node struct {
	// Core data-plane services.
	KV          service.KVServicer
	Admin       service.AdminServicer
	MVCC        service.MVCCKVServicer
	Batch       service.BatchServicer
	Transaction service.TransactionServicer
	Lease       service.LeaseServicer
	Index       service.IndexServicer
	TimeSeries  service.TimeSeriesServicer
	Watch       service.WatchServicer

	// Lock is the node's lock servicer over the node's RAW, pre-namespace
	// engines. It is the registry-LESS composition's servicer -- the embedded
	// transport, and single-database servers -- where the whole store and the
	// caller's own keyspace are the same set. A multi-tenant server must NOT
	// serve locks from it: a lock key carries no tenant segment, so every
	// tenant would take the same lock (go-qrdb#455). Those servers resolve
	// through service.Registry.LockService instead, which builds a lock
	// service over the (tenant, database)-namespaced engines, and the
	// transports prefer the registry whenever one is configured.
	Lock service.LockServicer

	Collection service.CollectionServicer
	Database   service.DatabaseServicer
	Vector     service.VectorServicer

	// Cluster operations services. Rebalance inspects/moves shards via the
	// host; Backup snapshots/restores shards via the host's snapshot/export
	// APIs (see NewHostShardSnapshotProvider).
	Rebalance service.RebalanceServicer
	Backup    service.BackupServicer

	// AI/ML services. Both are nil when no embedding providers are configured
	// (EmbeddingConfig.Providers is empty) or when the qrdb_vector build tag
	// is absent.
	Embedding service.EmbeddingServicer
	RAG       service.RAGServicer

	// Auth services backed by KV storage.
	User user.UserServicer
	Role role.RoleServicer

	// Security services. All are nil when no SealBarrier was provided.
	Barrier    service.BarrierServicer
	Ceremony   service.CeremonyServicer
	Credential service.CredentialServicer
	Tenant     tenant.TenantServicer
	CertProv   cert.CertProvider

	// StoreResolver maps a tenant ID to the KVStore backing its IAM data.
	// It is non-nil only when a BarrierRegistry was supplied: barrier-mode
	// tenants route their user/role records through their own TenantBarrier
	// while prefix/system tenants use the shared system store. It is a
	// forward seam for the SDK to expose; there is no SDK accessor yet.
	StoreResolver tenant.StoreResolver

	// UserAuthFactor is the M14 user-side auth-factor service, TENANT-ROUTED
	// through StoreResolver. It is non-nil exactly when StoreResolver is: the
	// resolver is the tenant boundary of factor resolution (go-qrdb#393), so a
	// composition that cannot resolve a tenant gets no factor service rather
	// than one serving every tenant out of one keyspace.
	UserAuthFactor service.UserAuthFactorServicer

	// DEK is the per-tenant data encryption keyring service. It is non-nil
	// exactly when StoreResolver is: the keyring is a record in each
	// tenant's resolved store, so it inherits that store's replication,
	// barrier encryption, sealed-tenant refusal and teardown.
	DEK service.DEKServicer

	// KeyspaceReclaim reclaims a dropped database's data from the store it
	// shares with other databases. It is always non-nil and is already wired
	// into the database manager; it is exposed so a composer that serves
	// volume-isolated tenants can attach the StoreResolver that routes their
	// keys to their dedicated per-tenant Host (SetStoreResolver), which does
	// not exist yet at the point BuildServices runs.
	KeyspaceReclaim *service.KeyspaceReclaimService

	// ReclaimSweeper drains pending reclaim jobs (go-qrdb#547) -- durable
	// records of keyspace an operation orphaned instead of deleting inline:
	// TRUNCATE TABLE's superseded generation, RENAME INDEX's old name (see
	// service.ReclaimJob). It is always non-nil, built here -- the one
	// composition point every composer shares -- so that starting its Run
	// loop is never a construction a composer can forget (go-qrdb#174). Every
	// composer that serves relational DDL (the daemon, the SDK node) starts
	// Run on every node; the server installs its resolver.
	ReclaimSweeper *service.ReclaimSweeper

	// PlacementStore is the replicated placement table for dedicated tenant
	// Raft groups (Phase 4c). It is always non-nil: the DatabaseService writes
	// a database's computed placement here on create, and the daemon threads it
	// into the RaftBackendFactory so the table is the source of truth for
	// resolving a dedicated-raft database's shard.
	PlacementStore *placement.Store

	// ClusterMigStore is the replicated coordination plane for cluster-wide
	// enable-encryption migrations (Phase D). It lives in the same system KV as
	// the placement table; the daemon threads it into the migration coordinator +
	// per-node reconciler. Non-nil whenever the system KV is available.
	ClusterMigStore *clustermig.Store

	// closers are called in reverse order on Stop.
	closers []func() error
}

// addCloser registers a closer to be called during Stop.
func (n *Node) addCloser(fn func() error) {
	n.closers = append(n.closers, fn)
}

// Stop calls all registered closers in LIFO order. All errors are collected;
// the first non-nil error is returned.
func (n *Node) Stop(_ context.Context) error {
	var firstErr error
	for i := len(n.closers) - 1; i >= 0; i-- {
		if err := n.closers[i](); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// BuildServices constructs all service-layer objects from the provided
// already-initialised infrastructure. It does not open storage, create
// listeners, or initialize the seal barrier — those responsibilities remain
// with the caller.
//
// Services that depend on a seal barrier (Barrier, Ceremony, Credential,
// Tenant, CertProv) are constructed only when infra.SealBarrier is non-nil.
// The returned Node always has KV, Admin, MVCC, Batch, Transaction, Lease,
// Index, TimeSeries, Watch, Lock, Collection, Database, and Vector populated
// (or non-nil errors returned for construction failures).
func BuildServices(ctx context.Context, infra Infra) (*Node, error) {
	if infra.Host == nil {
		return nil, &NilHostError{}
	}
	if infra.WatchHub == nil {
		return nil, &NilWatchHubError{}
	}

	log := infra.Logger
	if log == nil {
		log = slog.Default()
	}

	n := &Node{}

	// Build core data-plane services.
	n.KV = service.NewKVService(infra.Host, shard.ShardKV, infra.KVOptions...)
	var mvccOpts []service.MVCCKVServiceOption
	if infra.NoRevisionHistory {
		mvccOpts = append(mvccOpts, service.WithoutRevisionHistory())
	}
	n.MVCC = service.NewMVCCKVService(infra.Host, shard.ShardKV, mvccOpts...)
	var adminOpts []service.AdminServiceOption
	if infra.Profile != nil {
		adminOpts = append(adminOpts, service.WithCapabilityProfile(*infra.Profile))
	}
	// The platform's node-keyspace compaction policy is a record in the
	// shared store's compaction table; a shared store with no revision
	// history has none to hold.
	if table, ok := n.MVCC.(service.CompactionStateStore); ok && !infra.NoRevisionHistory {
		adminOpts = append(adminOpts, service.WithNodeCompaction(table))
	}
	n.Admin = service.NewAdminService(infra.Host, adminOpts...)
	n.Batch = service.NewBatchService(infra.Host, shard.ShardKV)
	n.Transaction = service.NewTransactionService(infra.Host, shard.ShardKV)
	n.Lease = service.NewLeaseService(infra.Host, shard.ShardKV)
	n.Index = service.NewIndexService(infra.Host, shard.ShardKV)
	n.TimeSeries = service.NewTimeSeriesService(infra.Host, shard.ShardKV)
	n.Watch = service.NewWatchService(infra.WatchHub)

	lockSvc, err := service.NewLockService(n.Transaction, n.Lease, n.Watch, n.MVCC, log)
	if err != nil {
		return nil, &LockServiceError{Err: err}
	}
	n.Lock = lockSvc

	// SequenceServicer backs Collection's IDStrategyIncrement document IDs
	// with a Raft-replicated compare-and-swap counter (see
	// service.CollectionService.WithSequenceService) instead of the
	// process-local counter that would otherwise restart at 0 on every
	// process restart and diverge across replicas.
	n.Collection = service.NewCollectionServiceWithIndex(n.KV, n.Index).
		WithSequenceService(service.NewSequenceService(n.MVCC, n.Transaction))

	// Cluster operations. Rebalance inspects shard stats and moves shards via
	// the host. Backup snapshots/restores shards via the host's snapshot/export
	// APIs through a host-backed ShardSnapshotProvider. Both are functional only
	// on a clustered Raft host; on the single-node LocalBackend, Rebalance has
	// no peers to balance and Backup reports BackupUnsupportedError per shard.
	n.Rebalance = service.NewRebalanceService(infra.Host)
	var backupOpts []service.BackupServiceOption
	if infra.BackupSink != nil {
		backupOpts = append(backupOpts, service.WithBackupSink(infra.BackupSink))
	}
	n.Backup = service.NewBackupService(service.NewHostShardSnapshotProvider(infra.Host), backupOpts...)

	// Auth services (user, role) backed by KV storage.
	//
	// GUARDED: n.Transaction is already built above, unconditionally, so
	// authStore gets kvstore.MultiGuardedBatchKVStore capability at no extra
	// cost — the same shared implementation (ExecuteGuardedBatch) cmd/qrdbd's
	// role/user stores use (go-qrdb#555 owner ruling: every composer that
	// builds these stores gets the guarded adapter). The plain n.User/n.Role
	// built directly over it just below stay sequential; where a
	// StoreResolver is wired further down, the routed services that replace
	// them (role/user.NewRoutingService over authStore) are what use it,
	// committing a
	// system-tenant create atomically once kvstore.ProbeGuardedBatch confirms
	// the state machine can evaluate the batch.
	authStore, err := NewGuardedKVStoreAdapter(n.KV, n.Index, n.Transaction)
	if err != nil {
		return nil, &UserServiceError{Err: err}
	}
	userIDGen, err := dao.NewSnowflakeGenerator(resolveReplicaID(infra.ReplicaID))
	if err != nil {
		return nil, &UserServiceError{Err: err}
	}
	userOpts, err := UserStoreOptions(infra.MTLSBindingPolicy, log)
	if err != nil {
		return nil, &UserServiceError{Err: err}
	}
	// Every user store below validates written permitted-method policies
	// against THIS node's auth-chain vocabulary.
	userOpts = append(userOpts, user.WithAuthMethodVocabulary(infra.AuthMethodVocabulary))
	userSvc, err := user.NewStore(authStore, userIDGen, userOpts...)
	if err != nil {
		return nil, &UserServiceError{Err: err}
	}
	n.User = userSvc

	roleSvc, err := role.NewStore(authStore)
	if err != nil {
		return nil, &RoleServiceError{Err: err}
	}
	n.Role = roleSvc

	// Database service. Use the caller-supplied manager when available
	// (callers may have run migrations or created databases before calling
	// BuildServices). Otherwise create a fresh manager.
	dbManager := infra.DatabaseManager
	if dbManager == nil {
		dbManager = database.NewManager(log, infra.DataDir)
		n.addCloser(func() error { return dbManager.Close() })
	}

	// Wire the per-database encryption barrier manager now that the seal
	// subsystem's BarrierRegistry is available (BuildServices runs after the
	// barrier is unsealed). dbManager may already have been open for a while
	// -- callers construct it, and OpenAll it, before the registry exists --
	// so SetBarrierManager also re-registers the barrier for every
	// already-open database that has one enabled on a dedicated store. When
	// infra.BarrierRegistry is nil (seal disabled) no manager is set, so
	// per-database barrier operations correctly fail closed with
	// BarrierManagerNotConfiguredError. Guarded on BarrierManager() == nil so
	// a caller that already wired one explicitly is not clobbered.
	//
	// The manager keeps its barriers in a registry of its own over the system
	// barrier, never in infra.BarrierRegistry's tenant namespace: a database
	// named like a tenant would otherwise resolve to that tenant's barrier.
	if infra.BarrierRegistry != nil && dbManager.BarrierManager() == nil {
		bm, err := database.NewDatabaseBarrierManager(log, infra.BarrierRegistry.SystemBarrier())
		if err != nil {
			return nil, &DatabaseBarrierManagerError{Err: err}
		}
		dbManager.SetBarrierManager(bm)
	}

	// Replicated placement table (Phase 4c) over the shared system KV, plus the
	// load-aware scheduler over the live cluster topology (host roster +
	// RebalanceService load scoring). These are wired into the DatabaseService
	// so a dedicated-raft database's replica placement is computed and persisted
	// at create time; the store is also exposed on the Node so the daemon can
	// make it the RaftBackendFactory's source of truth.
	// Cluster-migration coordination plane over the same replicated system KV.
	n.ClusterMigStore = clustermig.NewStore(authStore)

	var placementOpts []service.DatabaseServiceOption
	if store, perr := placement.NewStore(authStore); perr == nil {
		n.PlacementStore = store
		var schedOpts []placement.SchedulerOption
		// A host whose groups gate features never has a group placed on a
		// node that cannot apply what the group uses.
		if admitter, ok := infra.Host.(service.ReplicaAdmitter); ok {
			schedOpts = append(schedOpts, placement.WithReplicaFilter(admitter.AdmitReplica))
		}
		if scheduler, serr := placement.NewScheduler(service.NewHostTopologyProvider(infra.Host, n.Rebalance), schedOpts...); serr == nil {
			placementOpts = []service.DatabaseServiceOption{
				service.WithPlacementStore(store),
				service.WithPlacementScheduler(scheduler),
				service.WithDatabaseLogger(log),
			}
		}
	}
	// Cluster-wide shard IDs: every raft database create claims its ID from
	// one replicated high-water mark in the system store, once the cluster
	// has activated cluster-shard-ids (see newShardClaimer).
	claimer, err := newShardClaimer(authStore, infra.Host)
	if err != nil {
		return nil, err
	}
	dbManager.SetShardIDClaimer(claimer)

	// Thread the same DatabaseBarrierManager instance dbManager was just
	// wired with into DatabaseService, so SealDatabase/UnsealDatabase/
	// DatabaseBarrierStatus (which read DatabaseService.barrierMgr directly)
	// and Create/Open (which read Manager.barrierMgr) operate against the
	// same seal.BarrierRegistry. Appended to placementOpts so it survives the
	// tenant-quota-opts rebuild below.
	if bm := dbManager.BarrierManager(); bm != nil {
		placementOpts = append(placementOpts, service.WithBarrierManager(bm))
	}

	n.Database = service.NewDatabaseService(dbManager, placementOpts...)

	// Vector service with PebbleDB-backed storage. Route encryption through
	// the shared Encryptor so vector storage and the MVCC state machine use
	// one code path and one instance. When Infra.Encryptor is nil, derive a
	// no-op Encryptor from SealBarrier for callers that only set that field.
	enc := infra.Encryptor
	if enc == nil {
		enc = atrest.New(infra.SealBarrier)
	}

	if infra.DataDir == "" {
		return nil, &EmptyVectorDataDirError{}
	}
	vectorDataDir := filepath.Join(infra.DataDir, "vector")
	vectorCfg := vectorstorage.DefaultConfig(vectorDataDir)
	vectorLog := logger.NewAdapter(newMinimalLogger(log))

	vectorStorageOpts := []vectorstorage.StorageOption{
		vectorstorage.WithEncryptor(enc),
	}
	vectorStorage, err := vectorstorage.NewPebbleVectorStorage(vectorCfg, vectorLog, vectorStorageOpts...)
	if err != nil {
		return nil, &VectorStorageError{Err: err}
	}
	n.addCloser(func() error { return vectorStorage.Close() })

	vectorSvc, err := service.NewVectorService(
		vectorStorage,
		vectorLog,
		service.WithStrictTenantIsolation(infra.StrictTenantIsolation),
	)
	if err != nil {
		return nil, &VectorServiceError{Err: err}
	}
	n.Vector = vectorSvc

	// DROP DATABASE reclamation (go-qrdb#412). The manager's own teardown
	// reclaims what a database holds on this node's filesystem; for a
	// shared-store database -- the default shape -- none of its data is
	// there, and before this seam existed the drop orphaned every key it
	// owned. This is the one place the database manager and the data-plane
	// engines meet in EVERY composition that can drop a database (the
	// daemon, the SDK's in-process node, an embedding), which is why it is
	// wired here rather than at a server.
	//
	// It is wired after n.Vector because a vector namespace is part of what a
	// database owns and is not reached by deleting keys.
	systemCompaction, _ := n.MVCC.(service.CompactionStateStore)
	n.KeyspaceReclaim = service.NewKeyspaceReclaimService(service.ReclaimTarget{
		KV:         n.KV,
		Index:      n.Index,
		Vector:     n.Vector,
		Compaction: systemCompaction,
	})
	dbManager.SetKeyspaceReclaimer(n.KeyspaceReclaim)

	// go-qrdb#547's reclaim-job sweeper, over the un-namespaced system store
	// where every pending job's index entry lives. Each job's own store is
	// resolved per entry once the server installs its resolver
	// (server.WithReclaimSweeper). Every composer starts this ONE instance's
	// Run loop (see the ReclaimSweeper field doc comment); none constructs
	// its own.
	n.ReclaimSweeper = service.NewReclaimSweeper(n.MVCC)

	// Embedding and RAG services: constructed only when providers are
	// configured. buildEmbeddingAndRAG is a no-op stub in non-vector builds.
	embSvc, ragSvc, err := buildEmbeddingAndRAG(
		infra.EmbeddingConfig,
		infra.RAGConfig,
		n.Vector,
		n.KV,
		log,
	)
	if err != nil {
		return nil, err
	}
	n.Embedding = embSvc
	n.RAG = ragSvc

	// Seal-gated services: only constructed when a barrier is provided.
	if infra.SealBarrier != nil {
		if err := n.buildSealServices(ctx, infra, log); err != nil {
			return nil, err
		}
		// Tenant storage isolation: when a barrier registry and a barrier-mode
		// tenant service are present, route IAM (user/role) reads and writes
		// through a StoreResolver. Barrier-isolated tenants persist their IAM
		// data via their own TenantBarrier (own DEK + "{tenantID}/" namespace);
		// prefix/system tenants continue to use the shared system store.
		if infra.BarrierRegistry != nil && n.Tenant != nil {
			resolver, err := tenant.NewStoreResolver(authStore, infra.BarrierRegistry, n.Tenant)
			if err != nil {
				return nil, &UserServiceError{Err: err}
			}
			n.StoreResolver = resolver

			routedUsers, err := user.NewRoutingService(resolver, userIDGen, userOpts...)
			if err != nil {
				return nil, &UserServiceError{Err: err}
			}
			n.User = routedUsers

			routedRoles, err := role.NewRoutingService(resolver)
			if err != nil {
				return nil, &RoleServiceError{Err: err}
			}
			n.Role = routedRoles

			if err := n.buildUserAuthFactorService(ctx, resolver, authStore, log); err != nil {
				return nil, err
			}
		}
	} else {
		// When the seal subsystem is disabled, wire a KV-backed tenant service
		// so tenant CRUD operates through Raft consensus. Barrier operations
		// on this implementation return ErrBarrierNotEnabled.
		// The fenced adapter carries the transaction plane so CreateTenant can
		// commit a tenant only while its tombstone is absent, atomically.
		kvTenantSvc, err := tenant.NewKVTenantService(&fencedKVStoreAdapter{kvStoreAdapter: &kvStoreAdapter{svc: n.KV, idx: n.Index}, tx: n.Transaction}, log)
		if err != nil {
			return nil, &TenantServiceError{Err: err}
		}
		n.Tenant = kvTenantSvc

		// Route IAM here TOO. Keyspace isolation is not a property of
		// encryption: a node with the seal subsystem off still has tenants,
		// and they are still entitled to separate keyspaces. Leaving n.User
		// and n.Role on the raw authStore in this branch meant every prefix
		// tenant on a seal-disabled node shared one IAM keyspace — the exact
		// exposure #41 closes everywhere else.
		//
		// The resolver takes no barrier registry here. Prefix and system
		// tenants get their confined "{tenant}/" view of the shared store as
		// usual; a dedicated-isolation tenant fails closed with
		// ErrTenantSealed, because without a registry there is no barrier to
		// route it through and serving it from the shared store would hand a
		// tenant that asked for cryptographic isolation an unencrypted
		// keyspace.
		resolver, rerr := tenant.NewStoreResolver(authStore, nil, kvTenantSvc)
		if rerr != nil {
			return nil, &UserServiceError{Err: rerr}
		}
		n.StoreResolver = resolver

		routedUsers, uerr := user.NewRoutingService(resolver, userIDGen, userOpts...)
		if uerr != nil {
			return nil, &UserServiceError{Err: uerr}
		}
		n.User = routedUsers

		routedRoles, roerr := role.NewRoutingService(resolver)
		if roerr != nil {
			return nil, &RoleServiceError{Err: roerr}
		}
		n.Role = routedRoles

		if uafErr := n.buildUserAuthFactorService(ctx, resolver, authStore, log); uafErr != nil {
			return nil, uafErr
		}
	}

	// DELETE TENANT teardown (go-qrdb#413). The tenant service is built above
	// in both branches -- barrier-backed when the seal subsystem is on,
	// KV-backed when it is off -- and both need the same three seams, because
	// keyspace isolation is not a property of encryption and a prefix tenant
	// on a seal-disabled node is exactly the case whose data stayed READABLE
	// after a delete.
	//
	// The reclaimer is the SAME service DROP DATABASE uses. A tenant's
	// keyspace and a database's are the same shapes of key over different
	// scopes, so there is one walk, not two.
	if w, ok := n.Tenant.(tenant.TeardownWiring); ok {
		w.SetDatabaseLister(dbManager)
		w.SetKeyspaceReclaimer(n.KeyspaceReclaim)
	}

	// The tenant DEK keyring rides the StoreResolver both branches above
	// build, so it is served whether or not the seal subsystem is enabled.
	if n.StoreResolver != nil {
		dekSvc, err := buildDEKService(n.StoreResolver)
		if err != nil {
			return nil, err
		}
		n.DEK = dekSvc
	}

	// Wire tenant-driven quota and entitlement enforcement into the database
	// service now that the tenant service exists. The tenant service is both
	// the quota.QuotaProvider and the quota.EntitlementChecker; CreateForTenant
	// consults them (the entitlement plane first, then the numeric quota plane)
	// before provisioning a tenant-scoped database. System-tenant creates
	// short-circuit ahead of both checks.
	if n.Tenant != nil {
		dbOpts := buildDatabaseQuotaOpts(n.Tenant, log)
		if len(dbOpts) > 0 {
			// Rebuild with both the quota/entitlement options and the placement
			// options so neither set is dropped when the tenant service exists.
			dbOpts = append(dbOpts, placementOpts...)
			n.Database = service.NewDatabaseService(dbManager, dbOpts...)
		}
		// dbManager.Open warned for the barrier records it can classify
		// alone; a volume- or raft-isolated tenant's needs the tenant
		// service, which exists only now.
		service.WarnBarriersOffDataPath(ctx, dbManager.List(), n.Tenant, log)
	}

	// infra.SealEnabled is the composer's own seal.enabled config decision,
	// read BEFORE it attempted to build a barrier -- NOT an inference from
	// SealBarrier's nilness, which a failed passphrase/Shamir/strategy
	// construction also produces and which is a distinct failure, not an
	// operator's choice to run without encryption (that failure is the
	// composer's own responsibility to report). Requiring SealBarrier to
	// ALSO be nil is a deliberate second belt: a composer that supplies a
	// real barrier without yet knowing about SealEnabled (an older call
	// site, a test fixture) must not be accused of running one it doesn't
	// have -- a false positive here wrongly tells an operator their data is
	// unencrypted, which is worse than the false negative of an omitted
	// warning from a call site whose barrier construction failed AND whose
	// composer forgot to report it. This is the single place both the
	// daemon (cmd/qrdbd) and the embedded SDK node (sdk/go/node) funnel
	// through on every successful boot path, sealed or not, so logging here
	// -- rather than in each caller, and only once THIS function's own
	// construction has fully succeeded -- guarantees the warning fires
	// exactly once per node start regardless of composer, and never for a
	// node whose service graph failed to build here. It does not cover a
	// later failure elsewhere in the boot sequence (server.New, transport
	// listen, etc.), which BuildServices cannot see. Wording mirrors
	// go-quicraft's own "strategy none" WARN so an operator sees the same
	// fact whether the barrier exists-but-does-nothing or does not exist at
	// all.
	//
	// There is no live command, and no safe in-place flag flip, that adds
	// encryption to a deployment that ran this way: enable-encryption
	// re-encodes files that are already in qrdb's encrypted-file format, and
	// this deployment's files were never written in that format at all.
	// Simply setting seal.enabled: true and restarting does NOT encrypt
	// existing data -- the encrypting filesystem passes pre-existing
	// plaintext files through unmodified on read and can fail outright
	// (encfs: invalid header) the first time a file is reopened for
	// writing, which can wedge the boot. The hint therefore does not name a
	// procedure to run in place; the supported path is a new node/tenant
	// provisioned with seal.enabled: true, with data moved into it through
	// backup/restore or an application-level re-seed (see
	// docs/barrier-seal/user.md).
	if !infra.SealEnabled && infra.SealBarrier == nil {
		log.Warn("qrdb/seal: seal disabled; at-rest data is stored in plaintext",
			"hint", "encryption cannot be added in place; provision a new node/tenant with seal.enabled: true and migrate data via backup/restore")
	}

	return n, nil
}

// buildDEKService builds the tenant DEK keyring service over resolver.
func buildDEKService(resolver tenant.StoreResolver) (service.DEKServicer, error) {
	keyring, err := dek.NewKeyring(resolver)
	if err != nil {
		return nil, &DEKServiceError{Err: err}
	}
	svc, err := service.NewDEKService(keyring)
	if err != nil {
		return nil, &DEKServiceError{Err: err}
	}
	return svc, nil
}

// buildDatabaseQuotaOpts derives the DatabaseService options that wire the
// tenant service's quota and entitlement enforcement. Returns nil when the
// tenant service is not a QuotaProvider (no enforcement possible).
func buildDatabaseQuotaOpts(tenantSvc tenant.TenantServicer, log *slog.Logger) []service.DatabaseServiceOption {
	provider, ok := tenantSvc.(quota.QuotaProvider)
	if !ok {
		return nil
	}
	quotaSvc, err := quota.NewService(provider, log)
	if err != nil {
		return nil
	}
	// Current-usage counters read the persisted per-tenant usage record.
	quotaSvc.RegisterCounter(quota.ResourceDatabases, func(ctx context.Context, tenantID string) (int64, error) {
		return tenantSvc.GetQuotaUsage(ctx, tenantID, quota.ResourceDatabases)
	})
	quotaSvc.RegisterCounter(quota.ResourceRaftGroups, func(ctx context.Context, tenantID string) (int64, error) {
		return tenantSvc.GetQuotaUsage(ctx, tenantID, quota.ResourceRaftGroups)
	})
	quotaSvc.RegisterCounter(quota.ResourceTotalReplicas, func(ctx context.Context, tenantID string) (int64, error) {
		return tenantSvc.GetQuotaUsage(ctx, tenantID, quota.ResourceTotalReplicas)
	})
	// ResourceReplicasPerGroup is a per-group cap evaluated against a zero
	// current usage; the counter always reports zero so a CheckN reduces to
	// "requested replicas <= MaxReplicasPerGroup".
	quotaSvc.RegisterCounter(quota.ResourceReplicasPerGroup, func(ctx context.Context, tenantID string) (int64, error) {
		return 0, nil
	})
	opts := []service.DatabaseServiceOption{service.WithQuotaChecker(quotaSvc)}
	if ec, ok := tenantSvc.(quota.EntitlementChecker); ok {
		opts = append(opts, service.WithEntitlementChecker(ec))
	}
	// Route IsolationVolume database storage under the tenant's DataRoot.
	if lookup, ok := tenantSvc.(service.TenantInfoLookup); ok {
		opts = append(opts, service.WithTenantInfoLookup(lookup))
	}
	// Record per-database raft-group usage so MaxRaftGroups is enforced for each
	// dedicated-raft (IsolationRaft) database.
	if rec, ok := tenantSvc.(service.QuotaUsageRecorder); ok {
		opts = append(opts, service.WithQuotaUsageRecorder(rec))
	}
	return opts
}

// buildSealServices constructs the security services that depend on the
// seal barrier: BarrierService, CredentialService, CeremonyService,
// TenantService, and CertProvider.
func (n *Node) buildSealServices(ctx context.Context, infra Infra, log *slog.Logger) error {
	if infra.CredentialCoreSvc != nil {
		n.Credential = service.NewCredentialService(infra.CredentialCoreSvc)

		ceremonyCoreSvc, err := ceremonyService(infra, log)
		if err != nil {
			return err
		}
		n.Ceremony = service.NewCeremonyService(ceremonyCoreSvc)
		n.Barrier = service.NewBarrierServiceWithCeremony(infra.SealBarrier, ceremonyCoreSvc)
	} else {
		n.Barrier = service.NewBarrierService(infra.SealBarrier)
	}

	if infra.BarrierRegistry != nil {
		tenantOpts := buildTenantServiceOpts(infra)
		if n.Ceremony != nil {
			if coreSvc, ok := service.CeremonyServiceCore(n.Ceremony); ok {
				tenantOpts = append(tenantOpts, tenant.WithTenantHolderDeps(coreSvc.HolderDeps()))
			}
		}
		// Tenant metadata (TenantInfo) is replicated cluster state: back it with
		// the REPLICATED system shard via the KV service — exactly like the
		// user, role, and placement stores — so a tenant created on one node is
		// visible on every node. The per-tenant barrier lifecycle still runs
		// through infra.BarrierRegistry; only TenantInfo persistence moves onto
		// Raft consensus. (Previously this used infra.SealBarrier, the
		// node-local seal store, which left a created tenant invisible to
		// followers and broke tenant resolution after a leader change/restart.)
		tenantMetaStore := kvSealBackend{KVStore: newKVStoreAdapter(n.KV, n.Index), tx: n.Transaction}
		tenantSvcInst, err := tenant.NewTenantService(
			tenantMetaStore, infra.BarrierRegistry, log, tenantOpts...)
		if err != nil {
			return &TenantServiceError{Err: err}
		}
		n.Tenant = tenantSvcInst

		// One-time, non-fatal compatibility migration: older builds persisted
		// TenantInfo to the node-local seal store. Copy any such records that are
		// not yet in the replicated store so existing tenants survive the upgrade.
		// On a fresh cluster the source is empty and this is a no-op.
		if migrated, mErr := tenant.MigrateMetadata(ctx, infra.SealBarrier, tenantMetaStore, log); mErr != nil {
			log.Warn("tenant metadata migration failed; continuing",
				slog.String("error", mErr.Error()))
		} else if migrated > 0 {
			log.Info("migrated tenant metadata from local seal store to replicated store",
				slog.Int("count", migrated))
		}

		// CertProvider uses the KV service as its underlying store and the
		// barrier for transparent encryption of certificate material.
		certStore := newKVStoreAdapter(n.KV, n.Index)
		n.CertProv = cert.NewDefaultProvider(certStore, infra.SealBarrier,
			cert.WithTrustDomain(infra.SPIFFETrustDomain))
	}

	return nil
}

// ceremonyService returns the node's core ceremony service — infra's when
// one is threaded through from the sealed-window phase or across an
// in-process data-plane restart, a new one otherwise — configured with the
// CA, the holder trust domain, the factor registry and the PIN backend.
// Configuration is applied to a threaded instance too, so the post-unseal
// graph cannot run with dependencies the sealed window had, and a threaded
// instance is rebound to this graph's barrier: a restart builds a new barrier
// instance, and the ceremony service carries the holders and the unclaimed
// envelopes across to it.
func ceremonyService(infra Infra, log *slog.Logger) (*ceremony.CeremonyService, error) {
	svc := infra.CeremonyCoreSvc
	if svc != nil {
		svc.Rebind(infra.SealBarrier)
	}
	if svc == nil {
		dataDir := infra.CeremonyDataDir
		if dataDir == "" {
			dataDir = filepath.Join(infra.DataDir, "seal")
		}
		created, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{DataDir: dataDir}, infra.SealBarrier, log)
		if err != nil {
			return nil, &CeremonyServiceError{Err: err}
		}
		svc = created
	}
	svc.SetCA(infra.CeremonyCA)
	svc.SetTrustDomain(infra.HolderTrustDomain)
	svc.SetFactorRegistry(infra.FactorRegistry)
	svc.SetPINBackend(infra.PINBackend)
	return svc, nil
}

// BuildSealServicesOnly constructs the subset of seal-gated services that
// do NOT require a Raft host: Barrier, Credential, Ceremony, and (when a
// BarrierRegistry is supplied) Tenant. It is intended for the pre-host
// "sealed-window" boot phase where the data-plane Pebble instance cannot
// yet be opened because the storage barrier is sealed.
//
// The returned Node has nil core data-plane services (KV, MVCC, Watch,
// etc.) and nil CertProvider. The caller is expected to bring the host
// up after the operator drives the unseal API and to then call
// BuildServices for the full service graph; the seal service instances
// returned here can be threaded into BuildServices' Infra (via
// SealBarrier, BarrierRegistry, CredentialCoreSvc) so the post-unseal
// graph reuses them rather than re-instantiating.
//
// infra.SealBarrier and infra.CredentialCoreSvc are required. infra.Host
// and infra.WatchHub are NOT consulted and may be nil. infra.DataDir is
// used only as the default for the ceremony data dir when
// infra.CeremonyDataDir is empty.
func BuildSealServicesOnly(ctx context.Context, infra Infra) (*Node, error) {
	_ = ctx // reserved for future use by barrier operations
	if infra.SealBarrier == nil {
		return nil, &NilSealBarrierError{}
	}
	if infra.CredentialCoreSvc == nil {
		return nil, &NilCredentialCoreError{}
	}

	log := infra.Logger
	if log == nil {
		log = slog.Default()
	}

	n := &Node{}
	n.Credential = service.NewCredentialService(infra.CredentialCoreSvc)

	ceremonyCoreSvc, err := ceremonyService(infra, log)
	if err != nil {
		return nil, err
	}
	n.Ceremony = service.NewCeremonyService(ceremonyCoreSvc)
	n.Barrier = service.NewBarrierServiceWithCeremony(infra.SealBarrier, ceremonyCoreSvc)

	if infra.BarrierRegistry != nil {
		tenantOpts := buildTenantServiceOpts(infra)
		tenantOpts = append(tenantOpts, tenant.WithTenantHolderDeps(ceremonyCoreSvc.HolderDeps()))
		// The sealed-window service graph has no Raft host (the data plane
		// cannot be opened while the storage barrier is sealed), so this tenant
		// service is necessarily backed by the node-local seal store rather than
		// the replicated system shard. That is acceptable because the
		// sealed-window listener exposes ONLY the barrier, ceremony, and
		// credential services — tenant CRUD is never invoked during the sealed
		// window. The replicated, cluster-visible tenant service is built
		// post-host in BuildServices (buildSealServices); per-tenant barrier
		// restore at boot uses the barrier registry's own persisted
		// __registry:tenant_root/ records, not TenantInfo, so this seal-backed
		// instance is never consulted for tenant resolution.
		tenantSvcInst, err := tenant.NewTenantService(
			infra.SealBarrier, infra.BarrierRegistry, log, tenantOpts...)
		if err != nil {
			return nil, &TenantServiceError{Err: err}
		}
		n.Tenant = tenantSvcInst
	}

	return n, nil
}

// resolveReplicaID maps a configured Raft ReplicaID onto the Snowflake node
// field. A zero ReplicaID (standalone default not yet materialised) maps to
// node 1, matching the qrdb standalone convention. Values above the 10-bit
// Snowflake node range are rejected by dao.NewSnowflakeGenerator.
func resolveReplicaID(id uint64) uint64 {
	if id == 0 {
		return 1
	}
	return id
}

// buildTenantServiceOpts returns the TenantServiceOptions derived from the
// Infra. When Infra.StrategyRegistry is non-nil, WithStrategyRegistry is
// included so the TenantService can resolve hardware/cloud seal strategies.
func buildTenantServiceOpts(infra Infra) []tenant.TenantServiceOption {
	var opts []tenant.TenantServiceOption
	// Reserve the node's own data directory so an operator-supplied dedicated
	// DataRoot (WithDataRoot) can never be placed equal to, above, or below the
	// system data dir — which would nest a tenant's dedicated disk inside the
	// daemon's own storage and cause data-confusion. Pool-base and existing-
	// tenant overlap are checked unconditionally in CreateTenant; this activates
	// the system-data-dir arm of that check in production.
	if infra.DataDir != "" {
		opts = append(opts, tenant.WithReservedDataRoots(infra.DataDir))
	}
	if infra.StrategyRegistry != nil {
		opts = append(opts, tenant.WithStrategyRegistry(infra.StrategyRegistry))
	}
	if infra.DefaultTenantQuotas != nil {
		opts = append(opts, tenant.WithDefaultQuotas(infra.DefaultTenantQuotas))
	}
	if infra.DefaultTenantEntitlements != nil {
		opts = append(opts, tenant.WithDefaultEntitlements(infra.DefaultTenantEntitlements))
	}
	return opts
}

// newKVStoreAdapter returns a kvstore.KVStore backed by the given KVServicer.
// Writes are proposed through the service; reads are linearizable.
//
// idx is the IndexServicer for the SAME shard, which is what makes the
// returned store's secondary indexes real. Pass nil only where no index
// subsystem exists; the store then refuses index operations with
// kvstore.ErrIndexUnsupported rather than answering them empty.
func newKVStoreAdapter(svc service.KVServicer, idx service.IndexServicer) kvstore.KVStore {
	return &kvStoreAdapter{svc: svc, idx: idx}
}

// newMinimalLogger wraps a *slog.Logger as a *logger.Logger for packages
// that require the project's structured logger type. A warn-level JSON
// logger is created from the slog.Logger's handler level.
func newMinimalLogger(log *slog.Logger) *logger.Logger {
	_ = log // slog.Logger attrs not currently extractable into LogConfig
	l, err := logger.New(logger.LogConfig{
		Level:  "warn",
		Format: "json",
		Output: "stderr",
	})
	if err != nil {
		panic("lifecycle: failed to create minimal logger: " + err.Error())
	}
	return l
}

// buildUserAuthFactorService migrates any legacy untenanted auth-factor
// records into their owning tenants and assigns the tenant-routed M14 service
// to n.UserAuthFactor.
//
// It is called from BOTH IAM-routing branches — seal-enabled and seal-disabled
// — for the same reason routing itself is: keyspace isolation is not a
// property of encryption, and a node with the seal subsystem off still has
// tenants whose credentials must not resolve each other's logins
// (go-qrdb#393).
//
// legacy is the UNCONFINED shared store, which is exactly what the resolver
// wraps; the migration reads the flat "__sys:auth/..." keys out of it and
// writes each record into "{tenant}/__sys:auth/...". It runs inside the
// service, before the service answers anything — NOT here. This function is
// called while the shard is still electing, and a linearizable read at that
// moment fails with "not leader, leader unknown" on a node that is otherwise
// healthy, which would turn a migration into a failed boot.
func (n *Node) buildUserAuthFactorService(_ context.Context, resolver tenant.StoreResolver, legacy kvstore.KVStore, log *slog.Logger) error {
	svc, err := service.NewRoutingUserAuthFactorService(service.RoutingUserAuthFactorServiceConfig{
		Resolver:    resolver,
		LegacyStore: legacy,
		Logger:      log,
	})
	if err != nil {
		return &UserAuthFactorServiceError{Err: err}
	}
	n.UserAuthFactor = svc
	return nil
}

// UserStoreOptions maps the operator's auth.mtls_binding_policy onto the user
// store options that enforce it at the data layer, with logger as the store's
// logger. Under "required" the store refuses every change that would leave the
// bootstrap admin unable to log in by certificate -- deleting, renaming,
// disabling or suspending it, dropping mtls from its methods, or withdrawing
// its last live binding (user.WithBootstrapAdminBindingRequired) -- and warns
// through logger as the admin's last key nears expiry. An unrecognised value is
// an *auth.InvalidMTLSBindingPolicyError. BuildServices applies it; a composer
// that builds its own user stores -- the daemon does -- applies the same
// options through this one function.
func UserStoreOptions(mtlsBindingPolicy string, logger *slog.Logger) ([]user.Option, error) {
	policy, err := auth.ParseMTLSBindingPolicy(mtlsBindingPolicy)
	if err != nil {
		return nil, err
	}
	opts := []user.Option{user.WithLogger(logger)}
	if policy == auth.MTLSBindingRequired {
		opts = append(opts, user.WithBootstrapAdminBindingRequired())
	}
	return opts, nil
}

// StateMachineKeepsRevisionHistory reports whether the registered state
// machine type smType keeps MVCC revision history: its descriptor declares
// statemachine.CapabilityMVCC. An unregistered type is reported as keeping
// none.
func StateMachineKeepsRevisionHistory(smType string) bool {
	d, err := statemachine.Describe(smType)
	if err != nil {
		return false
	}
	return slices.Contains(d.Capabilities, statemachine.CapabilityMVCC)
}
