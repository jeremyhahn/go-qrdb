//go:build qrdb_embedded

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

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"
	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

const embeddedConfigFile = "embedded.json"

func init() {
	// Register "pebble" and "mvcc" state machine types in the centralized
	// registry. These live in the store package which cannot be imported
	// from statemachine (circular dependency), so they are registered here
	// at program startup.
	statemachine.RegisterWithDescriptor("pebble", func() sm.CreateFunc {
		return sm.NewDiskCreateFunc(func(_, _ uint64) sm.DiskStateMachine {
			return store.NewPebbleStateMachine(nil)
		})
	}, statemachine.Descriptor{
		Description: "PebbleDB-backed on-disk state machine",
		Persistence: statemachine.PersistenceDisk,
		Kind:        statemachine.SMKindDisk,
		Capabilities: []statemachine.Capability{
			statemachine.CapabilityKV,
		},
	})
	statemachine.RegisterWithDescriptor("mvcc", func() sm.CreateFunc {
		return store.NewMVCCCreateFunc()
	}, statemachine.Descriptor{
		Description: "MVCC state machine with transactions, leases, indexes, and time-series",
		Persistence: statemachine.PersistenceDisk,
		Kind:        statemachine.SMKindDisk,
		Capabilities: []statemachine.Capability{
			statemachine.CapabilityKV,
			statemachine.CapabilityMVCC,
			statemachine.CapabilityTransaction,
			statemachine.CapabilityBatch,
			statemachine.CapabilityLease,
			statemachine.CapabilityIndex,
			statemachine.CapabilityTimeSeries,
			statemachine.CapabilityWatch,
		},
	})
}

const (
	defaultShardID   uint64 = 1
	defaultReplicaID uint64 = 1
	defaultRaftAddr         = "127.0.0.1:0"

	// leaderElectionTimeout is the maximum duration to wait for Raft
	// leader election to complete in single-node embedded mode.
	leaderElectionTimeout = 5 * time.Second

	// leaderPollInterval is the interval between leader election polls.
	leaderPollInterval = 10 * time.Millisecond
)

// EmbeddedServerBinding manages the lifecycle of an in-process qrdbd
// server. On startup it creates either a LocalBackend (for memory, file,
// or direct PebbleDB modes) or a Raft host (for distributed consensus),
// starts the service layer, and injects an SDK client backed by the
// embedded transport into the ConnectionBinding.
type EmbeddedServerBinding struct {
	logger       *logger.Logger
	conn         *ConnectionBinding
	sqlBinding   *SQLBinding
	mu           sync.Mutex
	running      atomic.Bool
	config       *EmbeddedConfig
	ctx          context.Context
	cancelFn     context.CancelFunc
	host         *quicraft.Host
	localBackend *backend.LocalBackend
	storage      storage.Storage // underlying storage (pebble/file/memory) that must be closed on stop
	watchHub     *watch.Hub
	svcNode      *lifecycle.Node      // owns all service lifecycle (vector, db, etc.)
	sealBarrier  *seal.StorageBarrier // owns the persistent seal store; closed on stop to release its file lock
	registry     *DatabaseInstanceRegistry
}

// NewEmbeddedServerBinding creates a new EmbeddedServerBinding backed
// by the given connection binding.
func NewEmbeddedServerBinding(log *logger.Logger, conn *ConnectionBinding, sqlBinding *SQLBinding) *EmbeddedServerBinding {
	return &EmbeddedServerBinding{
		logger:     log,
		conn:       conn,
		sqlBinding: sqlBinding,
		registry:   NewDatabaseInstanceRegistry(log),
	}
}

// startup is called by Wails on application startup. It stores the
// Wails context for lifecycle management.
func (b *EmbeddedServerBinding) startup(ctx context.Context) {
	b.logger.Debug("EmbeddedServerBinding startup")
	b.ctx = ctx
}

// shutdown is called by Wails on application shutdown. It stops the
// embedded server if it is running.
func (b *EmbeddedServerBinding) shutdown(_ context.Context) {
	b.logger.Debug("EmbeddedServerBinding shutdown")
	if b.IsEmbedded() {
		_ = b.StopEmbedded()
	}
}

// IsEmbedded returns true when the embedded server is running.
func (b *EmbeddedServerBinding) IsEmbedded() bool {
	return b.running.Load()
}

// Registry returns the per-database instance registry.
func (b *EmbeddedServerBinding) Registry() *DatabaseInstanceRegistry {
	return b.registry
}

// StartEmbedded starts an in-process qrdbd node with the given
// configuration, then injects an SDK client backed by the embedded
// transport into the ConnectionBinding. The server runs until
// StopEmbedded is called.
func (b *EmbeddedServerBinding) StartEmbedded(cfg *EmbeddedConfig) error {
	b.logger.Debug("StartEmbedded called", "storageBackend", cfg.StorageBackend, "raftEnabled", cfg.RaftEnabled)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running.Load() {
		b.logger.Error("StartEmbedded failed", "error", "already running")
		return &EmbeddedServerError{Op: "start", Err: &AlreadyRunningError{}}
	}

	// Create the watch hub early so it can be shared between the state
	// machine (to emit change notifications) and the watch service (to
	// deliver them to subscribers).
	hub := watch.NewHub()

	var svcHost service.Host

	if cfg.RaftEnabled {
		host, err := b.startRaftNode(cfg)
		if err != nil {
			hub.Close()
			return &EmbeddedServerError{Op: "start", Err: err}
		}
		svcHost = &hostAdapter{Host: host}
		b.host = host
	} else {
		lb, err := b.startLocalNode(cfg, hub)
		if err != nil {
			hub.Close()
			return &EmbeddedServerError{Op: "start", Err: err}
		}
		svcHost = lb
		b.localBackend = lb
	}

	// Resolve the data directory once; it is used for service construction
	// (vector, database) and optional embedded seal barrier init.
	dataDir, dirErr := b.resolveDataDir(cfg)
	if dirErr != nil {
		hub.Close()
		return &EmbeddedServerError{Op: "start", Err: dirErr}
	}

	// Initialize the in-process seal barrier so that all seal-gated
	// services (tenant, ceremony, credential, cert) are available in
	// embedded mode. The barrier is backed by a persistent on-disk pebble
	// seal store and selects its sealing strategy from cfg.SealConfig
	// (defaulting to None / plaintext-at-rest when unset). It is
	// initialized on a fresh data dir and unsealed on restart, so the
	// persistent data-plane store decrypts cleanly across restarts.
	embeddedBarrier, embeddedRegistry, embeddedCred, embeddedCeremonyDir, barrierErr :=
		initEmbeddedBarrier(dataDir, cfg.SealConfig)
	if barrierErr != nil {
		b.logger.Warn("embedded: seal barrier unavailable, tenant/ceremony services disabled",
			"error", barrierErr)
	} else {
		// Track the barrier so StopEmbedded can close its persistent seal
		// store and release the on-disk file lock for the next start.
		b.sealBarrier = embeddedBarrier
	}

	// Build all services via the shared lifecycle package.
	svcNode, svcErr := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:              svcHost,
		WatchHub:          hub,
		DataDir:           dataDir,
		SealBarrier:       embeddedBarrier,
		BarrierRegistry:   embeddedRegistry,
		CredentialCoreSvc: embeddedCred,
		CeremonyDataDir:   embeddedCeremonyDir,
		Logger:            slog.Default(),
	})
	if svcErr != nil {
		hub.Close()
		if b.sealBarrier != nil {
			_ = b.sealBarrier.Close()
			b.sealBarrier = nil
		}
		return &EmbeddedServerError{Op: "start", Err: svcErr}
	}

	kvSvc := svcNode.KV
	adminSvc := svcNode.Admin

	// Collect embedded transport options for all services.
	opts := []embedded.Option{
		embedded.WithMVCCService(svcNode.MVCC),
		embedded.WithTransactionService(svcNode.Transaction),
		embedded.WithBatchService(svcNode.Batch),
		embedded.WithWatchService(svcNode.Watch),
		embedded.WithLeaseService(svcNode.Lease),
		embedded.WithIndexService(svcNode.Index),
		embedded.WithTimeSeriesService(svcNode.TimeSeries),
	}

	if svcNode.User != nil {
		opts = append(opts, embedded.WithUserService(svcNode.User))
	}
	if svcNode.Role != nil {
		opts = append(opts, embedded.WithRoleService(svcNode.Role))
	}
	if svcNode.Tenant != nil {
		opts = append(opts, embedded.WithTenantService(svcNode.Tenant))
	}
	if svcNode.Barrier != nil {
		opts = append(opts, embedded.WithBarrierService(svcNode.Barrier))
	}
	if svcNode.Ceremony != nil {
		opts = append(opts, embedded.WithCeremonyService(svcNode.Ceremony))
	}
	if svcNode.Credential != nil {
		opts = append(opts, embedded.WithCredentialService(svcNode.Credential))
	}
	if svcNode.CertProv != nil {
		opts = append(opts, embedded.WithCertProvider(svcNode.CertProv))
	}
	if svcNode.Vector != nil {
		opts = append(opts, embedded.WithVectorService(svcNode.Vector))
	}
	if svcNode.Database != nil {
		opts = append(opts, embedded.WithDatabaseService(svcNode.Database))
	}

	// Collection service uses KV + index from the node.
	opts = append(opts, embedded.WithCollectionService(svcNode.Collection))

	// Wire SQL services so the SQL browser can execute queries.
	if b.sqlBinding != nil {
		b.sqlBinding.SetServices(kvSvc, svcNode.Database)
	}

	// Hold a reference to the lifecycle node so StopEmbedded can call Stop.
	b.svcNode = svcNode

	// Store the watch hub for cleanup on shutdown.
	b.watchHub = hub

	client, err := b.buildClient(kvSvc, adminSvc, opts...)
	if err != nil {
		if b.watchHub != nil {
			b.watchHub.Close()
			b.watchHub = nil
		}
		if b.svcNode != nil {
			_ = b.svcNode.Stop(context.Background())
			b.svcNode = nil
		}
		if b.host != nil {
			_ = b.host.Close()
			b.host = nil
		}
		if b.localBackend != nil {
			_ = b.localBackend.Close()
			b.localBackend = nil
		}
		if b.storage != nil {
			_ = b.storage.Close()
			b.storage = nil
		}
		if b.sealBarrier != nil {
			_ = b.sealBarrier.Close()
			b.sealBarrier = nil
		}
		return &EmbeddedServerError{Op: "start", Err: err}
	}

	ctx, cancel := context.WithCancel(context.Background())
	b.ctx = ctx
	b.cancelFn = cancel
	b.config = cfg
	b.running.Store(true)
	b.conn.SetClient(client)

	// Persist the config so the server can be auto-restored on next launch.
	if err := b.saveConfig(cfg); err != nil {
		b.logger.Warn("embedded: failed to save config", "error", err)
	}

	b.logger.Debug("StartEmbedded completed")
	return nil
}

// StopEmbedded shuts down the embedded host or local backend, cancels
// the server context, clears stored configuration, and disconnects the
// SDK client.
func (b *EmbeddedServerBinding) StopEmbedded() error {
	b.logger.Debug("StopEmbedded called")
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.running.Load() {
		b.logger.Error("StopEmbedded failed", "error", "not running")
		return &EmbeddedServerError{Op: "stop", Err: &NotRunningError{}}
	}
	if b.watchHub != nil {
		b.watchHub.Close()
		b.watchHub = nil
	}
	if b.svcNode != nil {
		_ = b.svcNode.Stop(context.Background())
		b.svcNode = nil
	}
	if b.host != nil {
		_ = b.host.Close()
		b.host = nil
	}
	if b.localBackend != nil {
		_ = b.localBackend.Close()
		b.localBackend = nil
	}
	// Close underlying storage (pebble/file) to release file locks so the
	// server can be restarted without "lock held by current process".
	if b.storage != nil {
		if err := b.storage.Close(); err != nil {
			b.logger.Warn("embedded: storage close failed", "error", err)
		}
		b.storage = nil
	}
	// Close the seal barrier to release its persistent seal-store file lock.
	if b.sealBarrier != nil {
		if err := b.sealBarrier.Close(); err != nil {
			b.logger.Warn("embedded: seal barrier close failed", "error", err)
		}
		b.sealBarrier = nil
	}
	if b.cancelFn != nil {
		b.cancelFn()
		b.cancelFn = nil
	}
	if b.sqlBinding != nil {
		b.sqlBinding.ClearServices()
	}
	// Stop all per-database instances.
	if b.registry != nil {
		_ = b.registry.StopAll()
	}
	b.running.Store(false)
	b.config = nil
	b.conn.SetClient(nil)
	b.logger.Debug("StopEmbedded completed")
	return nil
}

// HasSavedConfig returns true when a saved embedded configuration exists
// on disk. The frontend calls this on startup to determine whether to
// auto-start the embedded server or show the database wizard.
func (b *EmbeddedServerBinding) HasSavedConfig() bool {
	path, err := b.configPath()
	if err != nil {
		return false
	}
	_, statErr := os.Stat(path)
	return statErr == nil
}

// RestoreEmbedded loads a saved embedded configuration from disk and
// starts the embedded server. Returns NoSavedConfigError when no config
// file exists.
func (b *EmbeddedServerBinding) RestoreEmbedded() error {
	b.logger.Debug("RestoreEmbedded called")
	cfg, err := b.loadConfig()
	if err != nil {
		return err
	}
	return b.StartEmbedded(cfg)
}

// ClearSavedConfig removes the saved embedded configuration from disk.
func (b *EmbeddedServerBinding) ClearSavedConfig() error {
	path, err := b.configPath()
	if err != nil {
		return err
	}
	if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
		return &ConfigSaveError{Path: path, Err: removeErr}
	}
	return nil
}

// configPath returns the file path for the persisted embedded config.
func (b *EmbeddedServerBinding) configPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", &DataDirCreateError{Path: "~/.config", Err: err}
	}
	dir := filepath.Join(configDir, "qrdb")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", &DataDirCreateError{Path: dir, Err: err}
	}
	return filepath.Join(dir, embeddedConfigFile), nil
}

// saveConfig persists the embedded config as JSON to disk so the server
// can be auto-restored on the next application launch.
func (b *EmbeddedServerBinding) saveConfig(cfg *EmbeddedConfig) error {
	path, err := b.configPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return &ConfigSaveError{Path: path, Err: err}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return &ConfigSaveError{Path: path, Err: err}
	}
	b.logger.Debug("embedded config saved", "path", path)
	return nil
}

// loadConfig reads the persisted embedded config from disk.
func (b *EmbeddedServerBinding) loadConfig() (*EmbeddedConfig, error) {
	path, err := b.configPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &NoSavedConfigError{}
		}
		return nil, &ConfigLoadError{Path: path, Err: err}
	}
	var cfg EmbeddedConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, &ConfigLoadError{Path: path, Err: err}
	}
	b.logger.Debug("embedded config loaded", "path", path, "backend", cfg.StorageBackend)
	return &cfg, nil
}

// startLocalNode creates a LocalBackend for non-Raft storage modes.
// It creates a storage backend and wraps it in the appropriate store engine
// based on the engine choice. The provided watch hub is attached for write
// notifications.
func (b *EmbeddedServerBinding) startLocalNode(cfg *EmbeddedConfig, hub *watch.Hub) (*backend.LocalBackend, error) {
	dataDir, err := b.resolveDataDir(cfg)
	if err != nil {
		return nil, err
	}

	// Create storage backend.
	stg, pebbleDB, err := b.createStorage(cfg, dataDir)
	if err != nil {
		return nil, err
	}
	// Retain a reference so StopEmbedded can close it and release file locks.
	b.storage = stg

	// Create store engine. Default to "mvcc" for backends that support
	// binary key encoding (pebble, memory). File backends default to "kv"
	// because MVCC key encoding uses binary prefixes that are invalid as
	// filenames.
	engineType := cfg.StoreEngine
	if engineType == "" {
		if cfg.StorageBackend == "file" {
			engineType = "kv"
		} else {
			engineType = "mvcc"
		}
	}

	var sm backend.SMAdapter

	switch engineType {
	case "kv":
		opts := []engine.KVOption{
			engine.WithKVWatchHub(hub),
		}
		if pebbleDB != nil {
			opts = append(opts, engine.WithKVPebbleDB(pebbleDB))
		}
		sm = engine.NewKVEngine(stg, opts...)

	case "mvcc":
		mvccOpts := []engine.MVCCOption{
			engine.WithMVCCLessor(lease.NewLessor()),
			engine.WithMVCCWatchHub(hub),
		}
		if pebbleDB != nil {
			mvccOpts = append(mvccOpts, engine.WithMVCCPebbleDB(pebbleDB))

			// Create time-series storage for pebble backends.
			tsStorage, tsErr := timeseries.New(pebbleDB)
			if tsErr != nil {
				b.logger.Warn("embedded: time-series storage unavailable", "error", tsErr)
			} else {
				mvccOpts = append(mvccOpts, engine.WithMVCCTimeSeries(tsStorage))
			}
		}
		mvccEng, mvccErr := engine.NewMVCCEngine(stg, mvccOpts...)
		if mvccErr != nil {
			return nil, &SMOpenError{Backend: engineType, Err: mvccErr}
		}
		sm = mvccEng

	default:
		return nil, &UnknownEngineError{Engine: engineType}
	}

	return backend.NewLocalBackend(sm), nil
}

// createStorage creates the appropriate storage.Storage backend based on
// the configured backend name. Returns the storage, an optional *pebble.DB
// (non-nil for pebble backends), and any error.
func (b *EmbeddedServerBinding) createStorage(cfg *EmbeddedConfig, dataDir string) (storage.Storage, *pebble.DB, error) {
	backendName := cfg.StorageBackend
	if backendName == "" {
		backendName = "pebble"
	}

	switch backendName {
	case "memory":
		return memory.NewStorage(), nil, nil

	case "file":
		stg, err := file.New(filepath.Join(dataDir, "file"))
		if err != nil {
			return nil, nil, &DataDirCreateError{Path: dataDir, Err: err}
		}
		return stg, nil, nil

	case "pebble", "pebbledb":
		pebbleDir := filepath.Join(dataDir, "pebble")
		stg, err := pebbleStorage.New(pebbleStorage.DefaultConfig(pebbleDir))
		if err != nil {
			if isPebbleLockError(err) {
				return nil, nil, &DataDirLockedError{Path: pebbleDir, Err: err}
			}
			return nil, nil, &SMOpenError{Backend: "pebble", Err: err}
		}
		db := stg.DB()
		return stg, db, nil

	default:
		return nil, nil, &UnknownBackendError{Backend: backendName}
	}
}

// startRaftNode creates a single-node Raft host, starts a shard, and
// waits for leader election to complete.
func (b *EmbeddedServerBinding) startRaftNode(cfg *EmbeddedConfig) (*quicraft.Host, error) {
	// Resolve state machine type from the storage backend name.
	smType := resolveStorageSM(cfg.StorageBackend)
	createFn, err := statemachine.Resolve(smType)
	if err != nil {
		return nil, &SMResolveError{Name: smType, Err: err}
	}

	dataDir, err := b.resolveDataDir(cfg)
	if err != nil {
		return nil, err
	}

	// Determine shard and replica IDs.
	shardID := defaultShardID
	replicaID := defaultReplicaID
	raftAddr := defaultRaftAddr

	if cfg.RaftConfig != nil {
		if cfg.RaftConfig.ShardID != 0 {
			shardID = cfg.RaftConfig.ShardID
		}
		if cfg.RaftConfig.ReplicaID != 0 {
			replicaID = cfg.RaftConfig.ReplicaID
		}
		if cfg.RaftConfig.RaftAddress != "" {
			raftAddr = cfg.RaftConfig.RaftAddress
		}
	}

	hostCfg, shardCfg := buildEmbeddedRaftConfigs(dataDir, raftAddr, shardID, replicaID)

	// Create the host. Single-node embedded mode disables the network
	// transport since all communication is in-process.
	host, err := quicraft.NewHost(hostCfg, quicraft.WithoutTransport())
	if err != nil {
		return nil, &HostCreateError{Err: err}
	}

	// Start the shard with a single-node initial member map.
	members := map[uint64]string{replicaID: raftAddr}
	if err := host.StartShard(members, false, createFn, shardCfg); err != nil {
		_ = host.Close()
		return nil, &ShardStartError{ShardID: shardID, Err: err}
	}

	// Wait for leader election before returning so that the first
	// SyncPropose/SyncRead call succeeds without the "not leader" error.
	if err := waitForLeader(context.Background(), host, shardID, leaderElectionTimeout); err != nil {
		_ = host.Close()
		return nil, err
	}

	return host, nil
}

// resolveDataDir determines and creates the data directory.
func (b *EmbeddedServerBinding) resolveDataDir(cfg *EmbeddedConfig) (string, error) {
	dataDir := cfg.DataDir
	if dataDir == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return "", &DataDirCreateError{Path: "~/.config", Err: err}
		}
		dataDir = filepath.Join(configDir, "qrdb", "data")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", &DataDirCreateError{Path: dataDir, Err: err}
	}
	return dataDir, nil
}

// waitForLeader polls the host until a leader is elected for the given
// shard. Returns LeaderElectionTimeoutError if the timeout expires.
func waitForLeader(ctx context.Context, host *quicraft.Host, shardID uint64, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return &LeaderElectionTimeoutError{ShardID: shardID, Timeout: timeout}
		}
		leaderID, ok, err := host.GetLeaderID(shardID)
		if err == nil && ok && leaderID != 0 {
			return nil
		}
		time.Sleep(leaderPollInterval)
	}
	return &LeaderElectionTimeoutError{ShardID: shardID, Timeout: timeout}
}

// buildClient creates the embedded transport and SDK client from the
// service layer interfaces.
func (b *EmbeddedServerBinding) buildClient(kvSvc service.KVServicer, adminSvc service.AdminServicer, opts ...embedded.Option) (*sdk.Client, error) {
	transport, err := embedded.New(kvSvc, adminSvc, opts...)
	if err != nil {
		return nil, &TransportCreateError{Err: err}
	}

	client, err := sdk.NewClient(transport)
	if err != nil {
		return nil, &ClientCreateError{Err: err}
	}

	if err := client.Connect(context.Background()); err != nil {
		_ = client.Close()
		return nil, &ClientCreateError{Err: err}
	}

	return client, nil
}

// isPebbleLockError reports whether err originates from Pebble failing to
// acquire the directory lock file. Pebble returns a plain error whose message
// contains "lock held by current process" when the same process attempts to
// open the same database directory twice, which happens when two embedded
// server profiles share the same dataDir.
func isPebbleLockError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "lock held by current process")
}

// resolveStorageSM maps a user-facing storage backend name to the
// state machine type registered in the statemachine registry. Used
// only in Raft mode. Defaults to "pebble" for unknown backends.
func resolveStorageSM(bknd string) string {
	smTypes := map[string]string{
		"pebble":   "pebble",
		"pebbledb": "pebble",
		"mvcc":     "mvcc",
	}
	if sm, ok := smTypes[bknd]; ok {
		return sm
	}
	return "pebble"
}

// buildEmbeddedRaftConfigs builds the Raft host and shard configuration for
// embedded mode, which is unconditionally single-node and transport-less.
//
// ListenAddress mirrors RaftAddress because no real network listener is ever
// created (WithoutTransport). AllowZeroDeploymentID permits the default zero
// value since embedded mode has no cross-cluster risk.
//
// Both transport-less Raft tunings apply here without qualification: the host
// has no network, so its WAN RTT inheritance is simply wrong for it, and its
// single shard can never acquire a peer and so is permanently its own quorum.
// See cluster.TransportlessRTTMillisecond and cluster.SingleNodeElectionRTT
// (go-qrdb#250).
func buildEmbeddedRaftConfigs(dataDir, raftAddr string, shardID, replicaID uint64) (quicraftconfig.HostConfig, quicraftconfig.Config) {
	hostCfg := quicraftconfig.HostConfig{
		WALDir:                filepath.Join(dataDir, "wal"),
		NodeHostDir:           filepath.Join(dataDir, "nodehost"),
		RaftAddress:           raftAddr,
		ListenAddress:         raftAddr,
		AllowZeroDeploymentID: true,
	}
	cluster.ApplyTransportlessHostTuning(&hostCfg)

	shardCfg := quicraftconfig.Config{
		ShardID:   shardID,
		ReplicaID: replicaID,
	}
	cluster.ApplyTransportlessShardTuning(&shardCfg)

	return hostCfg, shardCfg
}
