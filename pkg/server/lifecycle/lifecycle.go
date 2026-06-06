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

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
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
	Lock        service.LockServicer
	Collection  service.CollectionServicer
	Database    service.DatabaseServicer
	Vector      service.VectorServicer

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
	n.Admin = service.NewAdminService(infra.Host)
	n.MVCC = service.NewMVCCKVService(infra.Host, shard.ShardKV)
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

	n.Collection = service.NewCollectionServiceWithIndex(n.KV, n.Index)

	// Cluster operations. Rebalance inspects shard stats and moves shards via
	// the host. Backup snapshots/restores shards via the host's snapshot/export
	// APIs through a host-backed ShardSnapshotProvider. Both are functional only
	// on a clustered Raft host; on the single-node LocalBackend, Rebalance has
	// no peers to balance and Backup reports BackupUnsupportedError per shard.
	n.Rebalance = service.NewRebalanceService(infra.Host)
	n.Backup = service.NewBackupService(service.NewHostShardSnapshotProvider(infra.Host))

	// Auth services (user, role) backed by KV storage.
	authStore := newKVStoreAdapter(n.KV)
	userIDGen, err := dao.NewSnowflakeGenerator(resolveReplicaID(infra.ReplicaID))
	if err != nil {
		return nil, &UserServiceError{Err: err}
	}
	userSvc, err := user.NewStore(authStore, userIDGen)
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
	n.Database = service.NewDatabaseService(dbManager)

	// Vector service with PebbleDB-backed storage. Route encryption through
	// the shared Encryptor so vector storage and the MVCC state machine use
	// one code path and one instance. When Infra.Encryptor is nil, derive a
	// no-op Encryptor from SealBarrier for callers that only set that field.
	enc := infra.Encryptor
	if enc == nil {
		enc = atrest.New(infra.SealBarrier)
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

			routedUsers, err := user.NewRoutingService(resolver, userIDGen)
			if err != nil {
				return nil, &UserServiceError{Err: err}
			}
			n.User = routedUsers

			routedRoles, err := role.NewRoutingService(resolver)
			if err != nil {
				return nil, &RoleServiceError{Err: err}
			}
			n.Role = routedRoles
		}
	} else {
		// When the seal subsystem is disabled, wire a KV-backed tenant service
		// so tenant CRUD operates through Raft consensus. Barrier operations
		// on this implementation return ErrBarrierNotEnabled.
		kvTenantSvc, err := tenant.NewKVTenantService(newKVStoreAdapter(n.KV), log)
		if err != nil {
			return nil, &TenantServiceError{Err: err}
		}
		n.Tenant = kvTenantSvc
	}

	return n, nil
}

// buildSealServices constructs the security services that depend on the
// seal barrier: BarrierService, CredentialService, CeremonyService,
// TenantService, and CertProvider.
func (n *Node) buildSealServices(ctx context.Context, infra Infra, log *slog.Logger) error {
	_ = ctx // reserved for future use by barrier operations

	n.Barrier = service.NewBarrierService(infra.SealBarrier)

	if infra.CredentialCoreSvc != nil {
		n.Credential = service.NewCredentialService(infra.CredentialCoreSvc)

		ceremonyDataDir := infra.CeremonyDataDir
		if ceremonyDataDir == "" {
			ceremonyDataDir = filepath.Join(infra.DataDir, "seal")
		}
		ceremonyCfg := &ceremony.CeremonyConfig{
			DataDir: ceremonyDataDir,
		}
		ceremonyCoreSvc, err := ceremony.NewCeremonyService(
			ceremonyCfg, infra.SealBarrier, infra.CredentialCoreSvc, log)
		if err != nil {
			return &CeremonyServiceError{Err: err}
		}
		n.Ceremony = service.NewCeremonyService(ceremonyCoreSvc)
	}

	if infra.BarrierRegistry != nil {
		tenantOpts := buildTenantServiceOpts(infra)
		tenantSvcInst, err := tenant.NewTenantService(
			infra.SealBarrier, infra.BarrierRegistry, log, tenantOpts...)
		if err != nil {
			return &TenantServiceError{Err: err}
		}
		n.Tenant = tenantSvcInst

		// CertProvider uses the KV service as its underlying store and the
		// barrier for transparent encryption of certificate material.
		certStore := newKVStoreAdapter(n.KV)
		n.CertProv = cert.NewDefaultProvider(certStore, infra.SealBarrier)
	}

	return nil
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
	n.Barrier = service.NewBarrierService(infra.SealBarrier)
	n.Credential = service.NewCredentialService(infra.CredentialCoreSvc)

	ceremonyDataDir := infra.CeremonyDataDir
	if ceremonyDataDir == "" {
		ceremonyDataDir = filepath.Join(infra.DataDir, "seal")
	}
	ceremonyCfg := &ceremony.CeremonyConfig{DataDir: ceremonyDataDir}
	ceremonyCoreSvc, err := ceremony.NewCeremonyService(
		ceremonyCfg, infra.SealBarrier, infra.CredentialCoreSvc, log)
	if err != nil {
		return nil, &CeremonyServiceError{Err: err}
	}
	n.Ceremony = service.NewCeremonyService(ceremonyCoreSvc)

	if infra.BarrierRegistry != nil {
		tenantOpts := buildTenantServiceOpts(infra)
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
	if infra.StrategyRegistry == nil {
		return nil
	}
	return []tenant.TenantServiceOption{
		tenant.WithStrategyRegistry(infra.StrategyRegistry),
	}
}

// newKVStoreAdapter returns a kvstore.KVStore backed by the given KVServicer.
// Writes are proposed through the service; reads are linearizable.
func newKVStoreAdapter(svc service.KVServicer) kvstore.KVStore {
	return &kvStoreAdapter{svc: svc}
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
