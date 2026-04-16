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

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	vectorstorage "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
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

	// BarrierRegistry manages per-tenant barrier lifecycle. Required when
	// SealBarrier is non-nil.
	BarrierRegistry *seal.BarrierRegistry

	// CredentialCoreSvc is the core credential service built on top of the
	// SealBarrier. Required when SealBarrier is non-nil.
	CredentialCoreSvc *credentials.CredentialService

	// CeremonyDataDir is the data directory for the ceremony service. When
	// empty and SealBarrier is non-nil, it defaults to DataDir/seal.
	CeremonyDataDir string

	// Logger is used for service-level structured logging. When nil,
	// slog.Default() is used.
	Logger *slog.Logger
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

	// Auth services backed by KV storage.
	User user.UserServicer
	Role role.RoleServicer

	// Security services. All are nil when no SealBarrier was provided.
	Barrier    service.BarrierServicer
	Ceremony   service.CeremonyServicer
	Credential service.CredentialServicer
	Tenant     tenant.TenantServicer
	CertProv   cert.CertProvider

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

	// Auth services (user, role) backed by KV storage.
	authStore := newKVStoreAdapter(n.KV)
	userSvc, err := user.NewStore(authStore)
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

	// Vector service with PebbleDB-backed storage.
	vectorDataDir := filepath.Join(infra.DataDir, "vector")
	vectorCfg := vectorstorage.DefaultConfig(vectorDataDir)
	vectorLog := logger.NewAdapter(newMinimalLogger(log))
	vectorStorage, err := vectorstorage.NewPebbleVectorStorage(vectorCfg, vectorLog)
	if err != nil {
		return nil, &VectorStorageError{Err: err}
	}
	n.addCloser(func() error { return vectorStorage.Close() })

	vectorSvc, err := service.NewVectorService(vectorStorage, vectorLog)
	if err != nil {
		return nil, &VectorServiceError{Err: err}
	}
	n.Vector = vectorSvc

	// Seal-gated services: only constructed when a barrier is provided.
	if infra.SealBarrier != nil {
		if err := n.buildSealServices(ctx, infra, log); err != nil {
			return nil, err
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
		tenantSvcInst, err := tenant.NewTenantService(
			infra.SealBarrier, infra.BarrierRegistry, log)
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
