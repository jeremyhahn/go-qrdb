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

package embedded

import (
	"context"
	"encoding/binary"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

const serverDefaultShardID uint64 = 1

// ---------------------------------------------------------------------------
// Error types
// ---------------------------------------------------------------------------

// ServerConfigError indicates a validation error in ServerConfig.
type ServerConfigError struct {
	Field   string
	Message string
}

// Error returns a human-readable description.
func (e *ServerConfigError) Error() string {
	return "embedded: server config: " + e.Field + " " + e.Message
}

// StorageCreateError indicates that the storage backend could not be created.
type StorageCreateError struct {
	Backend database.BackendType
	Err     error
}

// Error returns a human-readable description.
func (e *StorageCreateError) Error() string {
	return "embedded: failed to create storage backend " + string(e.Backend) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *StorageCreateError) Unwrap() error { return e.Err }

// EngineCreateError indicates that the store engine could not be created.
type EngineCreateError struct {
	Engine string
	Err    error
}

// Error returns a human-readable description.
func (e *EngineCreateError) Error() string {
	return "embedded: failed to create engine " + e.Engine + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *EngineCreateError) Unwrap() error { return e.Err }

// BarrierCreateError indicates that the seal barrier could not be created.
type BarrierCreateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *BarrierCreateError) Error() string {
	return "embedded: failed to create barrier: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *BarrierCreateError) Unwrap() error { return e.Err }

// ServerTransportCreateError indicates that the embedded transport could not
// be created during server initialization.
type ServerTransportCreateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ServerTransportCreateError) Error() string {
	return "embedded: failed to create transport: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ServerTransportCreateError) Unwrap() error { return e.Err }

// ServerClientCreateError indicates that the SDK client could not be created
// during server initialization.
type ServerClientCreateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ServerClientCreateError) Error() string {
	return "embedded: failed to create client: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ServerClientCreateError) Unwrap() error { return e.Err }

// ShutdownError indicates that server shutdown encountered an error.
type ShutdownError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ShutdownError) Error() string {
	return "embedded: shutdown failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ShutdownError) Unwrap() error { return e.Err }

// ---------------------------------------------------------------------------
// Configuration types
// ---------------------------------------------------------------------------

// ServerConfig controls embedded QRDB server creation.
type ServerConfig struct {
	// DataDir is the persistent storage path. Required for BackendPebble
	// and BackendFile; optional for BackendMemory.
	DataDir string

	// Backend selects the storage backend. Defaults to BackendPebble if
	// the zero value is provided.
	Backend database.BackendType

	// Barrier configures the optional encryption barrier. When nil, no
	// barrier is created and data is stored unencrypted.
	Barrier *BarrierConfig

	// Logger is the structured logger for the server. Defaults to
	// slog.Default() if nil.
	Logger *slog.Logger
}

// BarrierConfig controls the encryption barrier.
type BarrierConfig struct {
	// Strategy selects the sealing strategy. Defaults to StrategySoftware
	// if the zero value is provided.
	Strategy seal.StrategyID

	// Shamir configures M-of-N secret sharing. Required when Strategy is
	// seal.StrategyShamir.
	Shamir *ShamirConfig
}

// ShamirConfig controls M-of-N secret sharing.
type ShamirConfig struct {
	// Threshold is the minimum number of shares needed to unseal (M).
	Threshold int

	// Total is the total number of shares generated (N).
	Total int
}

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

// Server is an embedded QRDB instance with lifecycle management. It owns
// all resources created during initialization and releases them in reverse
// order on Shutdown.
type Server struct {
	client       *sdk.Client
	localBackend *backend.LocalBackend
	stg          storage.Storage
	pebbleDB     *pebble.DB
	sealStore    *pebbleStorage.PebbleStorage
	barrier      *seal.StorageBarrier
	logger       *slog.Logger
}

// NewServer creates an embedded QRDB server from the given configuration.
// The returned Server holds all resources and must be shut down via
// Shutdown when no longer needed.
func NewServer(ctx context.Context, config *ServerConfig) (*Server, error) {
	if config == nil {
		return nil, &ServerConfigError{Field: "config", Message: "must not be nil"}
	}

	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}

	backendType := config.Backend
	if backendType == "" {
		backendType = database.BackendPebble
	}

	if err := validateServerConfig(config, backendType); err != nil {
		return nil, err
	}

	srv := &Server{logger: logger}

	// Create storage backend.
	stg, pdb, err := createServerStorage(backendType, config.DataDir)
	if err != nil {
		return nil, err
	}
	srv.stg = stg
	srv.pebbleDB = pdb

	// Create store engine. MVCC for pebble/memory, KV for file.
	sm, err := createServerEngine(backendType, stg, pdb)
	if err != nil {
		srv.closeStorage()
		return nil, err
	}

	// Create local backend (implements service.Host).
	srv.localBackend = backend.NewLocalBackend(sm)

	// Create core services.
	host := srv.localBackend
	kvSvc := service.NewKVService(host, serverDefaultShardID)
	adminSvc := service.NewAdminService(host)
	mvccSvc := service.NewMVCCKVService(host, serverDefaultShardID)
	txSvc := service.NewTransactionService(host, serverDefaultShardID)
	batchSvc := service.NewBatchService(host, serverDefaultShardID)
	leaseSvc := service.NewLeaseService(host, serverDefaultShardID)
	indexSvc := service.NewIndexService(host, serverDefaultShardID)
	tsSvc := service.NewTimeSeriesService(host, serverDefaultShardID)

	opts := []Option{
		WithMVCCService(mvccSvc),
		WithTransactionService(txSvc),
		WithBatchService(batchSvc),
		WithLeaseService(leaseSvc),
		WithIndexService(indexSvc),
		WithTimeSeriesService(tsSvc),
	}

	// Wire auth services (user, role) backed by KV storage.
	authStore := newServerKVAdapter(kvSvc)
	if userSvc, err := user.NewStore(authStore); err == nil {
		opts = append(opts, WithUserService(userSvc))
	}
	if roleSvc, err := role.NewStore(authStore); err == nil {
		opts = append(opts, WithRoleService(roleSvc))
	}

	// Set up barrier if configured.
	if config.Barrier != nil {
		barrierSvc, err := srv.createBarrier(config, logger)
		if err != nil {
			srv.closeBackend()
			srv.closeStorage()
			return nil, err
		}
		opts = append(opts, WithBarrierService(barrierSvc))
	}

	// Create transport and SDK client.
	tp, err := New(kvSvc, adminSvc, opts...)
	if err != nil {
		srv.closeAll()
		return nil, &ServerTransportCreateError{Err: err}
	}

	client, err := sdk.NewClient(tp)
	if err != nil {
		srv.closeAll()
		return nil, &ServerClientCreateError{Err: err}
	}

	if err := client.Connect(ctx); err != nil {
		_ = client.Close()
		srv.closeAll()
		return nil, &ServerClientCreateError{Err: err}
	}

	srv.client = client
	return srv, nil
}

// Client returns the SDK client connected to this embedded server.
func (s *Server) Client() *sdk.Client {
	return s.client
}

// Shutdown releases all server resources in reverse creation order. The
// provided context can be used for deadline propagation.
func (s *Server) Shutdown(_ context.Context) error {
	var firstErr error

	// 1. Close the SDK client (disconnects the transport).
	if s.client != nil {
		if err := s.client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.client = nil
	}

	// 2. Close the barrier seal store.
	if s.barrier != nil {
		s.barrier = nil
	}
	if s.sealStore != nil {
		if err := s.sealStore.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.sealStore = nil
	}

	// 3. Close the local backend (state machine).
	if s.localBackend != nil {
		if err := s.localBackend.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.localBackend = nil
	}

	// 4. Close the storage backend.
	if s.stg != nil {
		if err := s.stg.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.stg = nil
		s.pebbleDB = nil
	}

	if firstErr != nil {
		return &ShutdownError{Err: firstErr}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// validateServerConfig checks ServerConfig fields for validity.
func validateServerConfig(config *ServerConfig, backendType database.BackendType) error {
	switch backendType {
	case database.BackendPebble, database.BackendFile, database.BackendMemory:
		// valid
	default:
		return &ServerConfigError{Field: "Backend", Message: "unsupported backend: " + string(backendType)}
	}

	if config.DataDir == "" && backendType != database.BackendMemory {
		return &ServerConfigError{Field: "DataDir", Message: "required for backend " + string(backendType)}
	}

	if config.Barrier != nil {
		if config.Barrier.Strategy == seal.StrategyShamir {
			if config.Barrier.Shamir == nil {
				return &ServerConfigError{Field: "Barrier.Shamir", Message: "required when strategy is shamir"}
			}
			if config.Barrier.Shamir.Threshold < 2 {
				return &ServerConfigError{Field: "Barrier.Shamir.Threshold", Message: "must be at least 2"}
			}
			if config.Barrier.Shamir.Total < config.Barrier.Shamir.Threshold {
				return &ServerConfigError{Field: "Barrier.Shamir.Total", Message: "must be >= threshold"}
			}
		}

		// Barrier requires a data directory for its PebbleDB seal store.
		if config.DataDir == "" {
			return &ServerConfigError{Field: "DataDir", Message: "required when barrier is enabled"}
		}
	}

	return nil
}

// createServerStorage creates the appropriate storage backend and returns
// the storage interface along with an optional *pebble.DB handle (non-nil
// for pebble backends).
func createServerStorage(backendType database.BackendType, dataDir string) (storage.Storage, *pebble.DB, error) {
	switch backendType {
	case database.BackendMemory:
		return memory.NewStorage(), nil, nil

	case database.BackendFile:
		stg, err := file.New(filepath.Join(dataDir, "file"))
		if err != nil {
			return nil, nil, &StorageCreateError{Backend: backendType, Err: err}
		}
		return stg, nil, nil

	case database.BackendPebble:
		pebbleDir := filepath.Join(dataDir, "pebble")
		stg, err := pebbleStorage.New(pebbleStorage.DefaultConfig(pebbleDir))
		if err != nil {
			return nil, nil, &StorageCreateError{Backend: backendType, Err: err}
		}
		return stg, stg.DB(), nil

	default:
		return nil, nil, &ServerConfigError{Field: "Backend", Message: "unsupported backend: " + string(backendType)}
	}
}

// createServerEngine creates the appropriate store engine for the given
// backend type. MVCC is used for pebble and memory backends; KV for file
// backends.
func createServerEngine(backendType database.BackendType, stg storage.Storage, pdb *pebble.DB) (backend.SMAdapter, error) {
	if backendType == database.BackendFile {
		return engine.NewKVEngine(stg), nil
	}

	// MVCC engine for pebble and memory backends.
	mvccOpts := []engine.MVCCOption{
		engine.WithMVCCLessor(lease.NewLessor()),
	}

	if pdb != nil {
		mvccOpts = append(mvccOpts, engine.WithMVCCPebbleDB(pdb))

		tsStorage, err := timeseries.New(pdb)
		if err == nil {
			mvccOpts = append(mvccOpts, engine.WithMVCCTimeSeries(tsStorage))
		}
	}

	eng, err := engine.NewMVCCEngine(stg, mvccOpts...)
	if err != nil {
		return nil, &EngineCreateError{Engine: "mvcc", Err: err}
	}
	return eng, nil
}

// createBarrier sets up the encryption barrier with the configured strategy.
func (s *Server) createBarrier(config *ServerConfig, logger *slog.Logger) (service.BarrierServicer, error) {
	sealDir := filepath.Join(config.DataDir, "seal")
	sealStore, err := pebbleStorage.New(pebbleStorage.DefaultConfig(sealDir))
	if err != nil {
		return nil, &BarrierCreateError{Err: err}
	}
	s.sealStore = sealStore

	sealBackend := seal.NewStorageAdapter(sealStore)

	strategy := config.Barrier.Strategy
	if strategy == "" {
		strategy = seal.StrategySoftware
	}

	barrierCfg := seal.BarrierConfig{
		RootKeyPath: "root-key",
	}

	var strategies []seal.SealingStrategy

	switch strategy {
	case seal.StrategyShamir:
		barrierCfg.Shamir = &seal.ShamirConfig{
			Threshold:   config.Barrier.Shamir.Threshold,
			TotalShares: config.Barrier.Shamir.Total,
		}

		shamirStrat, err := seal.NewShamirStrategy(
			sealBackend,
			config.Barrier.Shamir.Threshold,
			config.Barrier.Shamir.Total,
		)
		if err != nil {
			_ = sealStore.Close()
			s.sealStore = nil
			return nil, &BarrierCreateError{Err: err}
		}
		strategies = append(strategies, shamirStrat)

	default:
		// StrategySoftware (and any future passphrase-based strategies).
		// Use a deterministic bootstrap passphrase derived from a fixed
		// prefix. The caller must invoke BarrierInitialize via the SDK
		// with this passphrase to complete initialization.
		bootstrapPass := deriveBootstrapPassphrase(0)
		softStrat, err := seal.NewSoftwareStrategy(bootstrapPass)
		if err != nil {
			_ = sealStore.Close()
			s.sealStore = nil
			return nil, &BarrierCreateError{Err: err}
		}
		strategies = append(strategies, softStrat)
	}

	barrier, err := seal.NewStorageBarrier(logger, sealBackend, barrierCfg, strategies...)
	if err != nil {
		_ = sealStore.Close()
		s.sealStore = nil
		return nil, &BarrierCreateError{Err: err}
	}
	s.barrier = barrier

	return service.NewBarrierService(barrier), nil
}

// deriveBootstrapPassphrase creates a deterministic passphrase from a
// deployment ID. This matches the pattern used by cmd/qrdbd/start.go.
func deriveBootstrapPassphrase(deploymentID uint64) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, deploymentID)
	passphrase := make([]byte, 0, 32)
	passphrase = append(passphrase, []byte("qrdb-seal-bootstrap:")...)
	passphrase = append(passphrase, buf...)
	return passphrase
}

// closeStorage closes the main storage backend.
func (s *Server) closeStorage() {
	if s.stg != nil {
		_ = s.stg.Close()
		s.stg = nil
		s.pebbleDB = nil
	}
}

// closeBackend closes the local backend.
func (s *Server) closeBackend() {
	if s.localBackend != nil {
		_ = s.localBackend.Close()
		s.localBackend = nil
	}
}

// closeAll closes all resources in reverse order. Used during failed
// initialization to ensure no resources leak.
func (s *Server) closeAll() {
	if s.barrier != nil {
		s.barrier = nil
	}
	if s.sealStore != nil {
		_ = s.sealStore.Close()
		s.sealStore = nil
	}
	s.closeBackend()
	s.closeStorage()
}

// serverKVAdapter wraps a KVServicer as a kvstore.KVStore for user/role stores.
type serverKVAdapter struct {
	svc service.KVServicer
}

func newServerKVAdapter(svc service.KVServicer) kvstore.KVStore {
	return &serverKVAdapter{svc: svc}
}

func (a *serverKVAdapter) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := a.svc.Get(ctx, key)
	if err != nil {
		// KVStore contract: return nil, nil for missing keys.
		// The KV service may return a KeyNotFoundError — convert to nil.
		errStr := err.Error()
		if strings.Contains(errStr, "not found") || strings.Contains(errStr, "key not found") {
			return nil, nil
		}
		return nil, err
	}
	return val, nil
}

func (a *serverKVAdapter) Put(ctx context.Context, key string, value []byte) error {
	return a.svc.Put(ctx, key, value)
}

func (a *serverKVAdapter) Delete(ctx context.Context, key string) error {
	return a.svc.Delete(ctx, key)
}

func (a *serverKVAdapter) List(ctx context.Context, prefix string) ([]string, error) {
	return a.svc.List(ctx, prefix)
}

func (a *serverKVAdapter) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	results, err := a.svc.Scan(ctx, prefix)
	if err != nil {
		return err
	}
	for k, v := range results {
		if err := fn(k, v); err != nil {
			return err
		}
	}
	return nil
}

func (a *serverKVAdapter) Exists(ctx context.Context, key string) (bool, error) {
	val, err := a.svc.Get(ctx, key)
	if err != nil {
		return false, err
	}
	return val != nil, nil
}

func (a *serverKVAdapter) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error { return nil }
func (a *serverKVAdapter) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}
func (a *serverKVAdapter) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}
func (a *serverKVAdapter) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}
func (a *serverKVAdapter) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}
