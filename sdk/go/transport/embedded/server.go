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
	"errors"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/encfs"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
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

// BarrierSealedError indicates that the encrypted-at-rest data plane
// could not be opened because the seal barrier was not unsealed before
// the underlying PebbleDB was constructed. It always wraps
// seal.ErrSealed so callers can match via errors.Is.
type BarrierSealedError struct {
	Err error
}

// Error returns a human-readable description.
func (e *BarrierSealedError) Error() string {
	if e.Err == nil {
		return "embedded: data-plane barrier is sealed"
	}
	return "embedded: data-plane barrier is sealed: " + e.Err.Error()
}

// Unwrap returns the underlying seal error so errors.Is(err, seal.ErrSealed)
// matches.
func (e *BarrierSealedError) Unwrap() error {
	if e.Err == nil {
		return seal.ErrSealed
	}
	return e.Err
}

// BarrierUnsealError indicates that the seal barrier failed to
// initialize or unseal during embedded server construction.
type BarrierUnsealError struct {
	Err error
}

// Error returns a human-readable description.
func (e *BarrierUnsealError) Error() string {
	return "embedded: failed to unseal barrier: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *BarrierUnsealError) Unwrap() error { return e.Err }

// SealSentinelError indicates that the format-version sentinel could
// not be read or written on the seal sub-store. This typically means
// the data dir was created by a pre-encfs build.
type SealSentinelError struct {
	Err error
}

// Error returns a human-readable description.
func (e *SealSentinelError) Error() string {
	return "embedded: seal sentinel: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *SealSentinelError) Unwrap() error { return e.Err }

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
	// Strategy selects the sealing strategy. Defaults to StrategyPassphrase
	// if the zero value is provided.
	Strategy seal.StrategyID

	// Shamir configures M-of-N secret sharing. Required when Strategy is
	// seal.StrategyShamir.
	Shamir *ShamirConfig

	// AutoUnseal, when true, instructs NewServer to initialize (on a
	// fresh data dir) or unseal (on an existing one) the barrier
	// during construction so the data-plane PebbleDB can be opened
	// behind the encrypting FS. When the disk-backed Pebble backend
	// is selected with a Barrier, AutoUnseal must be true: encfs
	// requires an unsealed barrier before the database can be
	// opened. The memory backend ignores this field.
	//
	// For StrategyPassphrase the credential is the passphrase below.
	// For StrategyShamir the credential is the M-of-N share set in
	// ShamirShares; the embedded server reconstructs the secret and
	// unseals via the Shamir strategy.
	AutoUnseal bool

	// Passphrase is the secret used by software / passphrase-style
	// strategies during AutoUnseal. When AutoUnseal is true, Strategy
	// is StrategyPassphrase (or empty), and Passphrase is empty,
	// NewServer falls back to the deterministic bootstrap passphrase
	// keyed by deployment ID 0 (matching the behavior of the daemon's
	// bootstrap path).
	Passphrase []byte

	// ShamirShares supplies the unseal shares when AutoUnseal is true
	// and Strategy is StrategyShamir.
	ShamirShares []string
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

	// Sequence the boot so the seal barrier is constructed and unsealed
	// before the data-plane PebbleDB is opened. This is required because
	// the encrypting FS in pkg/seal/encfs uses the barrier as a KEK to
	// wrap each file's per-file DEK; opening the database against a
	// sealed barrier would surface seal.ErrSealed during sstable
	// loading. The memory and file backends are unaffected because they
	// do not interact with vfs.FS at all.
	var (
		barrierSvc      service.BarrierServicer
		dataPlaneFS     vfs.FS
		barrierConfigOK = config.Barrier != nil
	)
	if barrierConfigOK {
		svc, fs, err := srv.setupBarrier(ctx, config, backendType, logger)
		if err != nil {
			return nil, err
		}
		barrierSvc = svc
		dataPlaneFS = fs
	}

	// Create storage backend. dataPlaneFS is non-nil only for the
	// pebble backend with an unsealed barrier; in every other case the
	// pebble open path falls back to vfs.Default.
	stg, pdb, err := createServerStorage(backendType, config.DataDir, dataPlaneFS)
	if err != nil {
		srv.closeSeal()
		return nil, err
	}
	srv.stg = stg
	srv.pebbleDB = pdb

	// Create store engine. MVCC for pebble/memory, KV for file.
	sm, err := createServerEngine(backendType, stg, pdb)
	if err != nil {
		srv.closeStorage()
		srv.closeSeal()
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

	// Wire auth services (user, role) backed by KV storage. The embedded
	// server is single-node, so user IDs are Snowflake-assigned from node 1.
	authStore := newServerKVAdapter(kvSvc)
	userIDGen, idGenErr := dao.NewSnowflakeGenerator(1)
	if idGenErr != nil {
		return nil, idGenErr
	}
	if userSvc, err := user.NewStore(authStore, userIDGen); err == nil {
		opts = append(opts, WithUserService(userSvc))
	}
	if roleSvc, err := role.NewStore(authStore); err == nil {
		opts = append(opts, WithRoleService(roleSvc))
	}

	if barrierSvc != nil {
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
			if config.Barrier.AutoUnseal && len(config.Barrier.ShamirShares) < config.Barrier.Shamir.Threshold {
				return &ServerConfigError{Field: "Barrier.ShamirShares", Message: "auto-unseal needs at least Threshold shares"}
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
// for pebble backends). When dataPlaneFS is non-nil it is wired into the
// pebble backend's vfs.FS so all on-disk I/O is proxied through it. The
// memory and file backends ignore dataPlaneFS — encfs is a no-op for an
// in-memory store, and the file backend does not run through Pebble.
func createServerStorage(backendType database.BackendType, dataDir string, dataPlaneFS vfs.FS) (storage.Storage, *pebble.DB, error) {
	stg, pdb, err := lifecycle.NewLocalStorage(backendType, dataDir, dataPlaneFS, qrdbconfig.ObjstoreConfig{})
	if err != nil {
		var unsupported *lifecycle.UnsupportedLocalBackendError
		if errors.As(err, &unsupported) {
			return nil, nil, &ServerConfigError{Field: "Backend", Message: "unsupported backend: " + string(backendType)}
		}
		return nil, nil, &StorageCreateError{Backend: backendType, Err: err}
	}
	return stg, pdb, nil
}

// createServerEngine creates the appropriate store engine for the given
// backend type. MVCC is used for pebble and memory backends; KV for file
// backends.
func createServerEngine(backendType database.BackendType, stg storage.Storage, pdb *pebble.DB) (backend.SMAdapter, error) {
	eng, err := lifecycle.NewLocalEngine(backendType, stg, pdb, nil)
	if err != nil {
		return nil, &EngineCreateError{Engine: "mvcc", Err: err}
	}
	return eng, nil
}

// setupBarrier opens the seal sub-store, builds the seal barrier with
// the configured strategy chain, writes / validates the format-version
// sentinel, and (when AutoUnseal is requested) initializes or unseals
// the barrier so the data-plane PebbleDB can be opened behind the
// encrypting FS. The returned vfs.FS is non-nil only when the disk
// backend is selected and the barrier ended in the unsealed state.
//
// On error the seal sub-store is closed before returning so the caller
// does not need to invoke closeSeal.
func (s *Server) setupBarrier(
	ctx context.Context,
	config *ServerConfig,
	backendType database.BackendType,
	logger *slog.Logger,
) (service.BarrierServicer, vfs.FS, error) {
	sealDir := filepath.Join(config.DataDir, "seal")
	// SyncWrites is forced on: this store is the sole persisted copy of the
	// root-key blob and epoch/rotation state, not a Raft-replicated store,
	// so an unsynced write can be lost on crash even though it appeared to
	// succeed — orphaning every file the lost root key had encrypted.
	sealCfg := pebbleStorage.DefaultConfig(sealDir)
	sealCfg.SyncWrites = true
	sealStore, err := pebbleStorage.New(sealCfg)
	if err != nil {
		return nil, nil, &BarrierCreateError{Err: err}
	}
	s.sealStore = sealStore

	sealBackend := seal.NewStorageAdapter(sealStore)

	strategy := config.Barrier.Strategy
	if strategy == "" {
		strategy = seal.StrategyPassphrase
	}

	barrierCfg := seal.BarrierConfig{
		RootKeyPath: "root-key",
	}

	// Resolve the effective strategy. The canonical options are none /
	// passphrase (software) / shamir. A passphrase barrier REQUIRES an explicit
	// passphrase supplied by the admin; when the software/default strategy is
	// selected with no passphrase, the embedded transport runs WITHOUT a barrier
	// (none) — there is no deployment-derived passphrase. The passphrase, when
	// present, is supplied to the deferred software strategy at init/unseal and
	// run through the KDF.
	effStrategy := strategy
	var softwareSecret []byte
	if effStrategy == seal.StrategyPassphrase || effStrategy == "" {
		if len(config.Barrier.Passphrase) > 0 {
			effStrategy = seal.StrategyPassphrase
			softwareSecret = config.Barrier.Passphrase
		} else {
			effStrategy = seal.StrategyNone
		}
	}

	var strategies []seal.SealingStrategy

	switch effStrategy {
	case seal.StrategyShamir:
		barrierCfg.Shamir = &seal.ShamirConfig{
			Threshold:   config.Barrier.Shamir.Threshold,
			TotalShares: config.Barrier.Shamir.Total,
		}

		shamirStrat, err := seal.NewShamirStrategy(
			config.Barrier.Shamir.Threshold,
			config.Barrier.Shamir.Total,
		)
		if err != nil {
			_ = sealStore.Close()
			s.sealStore = nil
			return nil, nil, &BarrierCreateError{Err: err}
		}
		strategies = append(strategies, shamirStrat)

	case seal.StrategyNone:
		strategies = append(strategies, seal.NewNoneStrategy())

	default:
		// Passphrase strategy (deferred software): the passphrase is supplied to
		// the barrier at init/unseal time and run through the KDF.
		strategies = append(strategies, seal.NewDeferredSoftwareStrategy())
	}

	barrier, err := seal.NewStorageBarrier(logger, sealBackend, barrierCfg, strategies...)
	if err != nil {
		_ = sealStore.Close()
		s.sealStore = nil
		return nil, nil, &BarrierCreateError{Err: err}
	}
	s.barrier = barrier

	// Format-version sentinel guards against running this build against
	// a data dir produced by a pre-encfs version that wrote plaintext
	// sstables. Writes the current version on a genuinely empty seal
	// sub-store, validates it on subsequent boots, and rejects
	// non-empty stores that lack the sentinel.
	sentinelKeys, listErr := sealBackend.List(ctx, "")
	if listErr != nil {
		_ = sealStore.Close()
		s.sealStore = nil
		s.barrier = nil
		return nil, nil, &SealSentinelError{Err: listErr}
	}
	if sentinelErr := seal.EnsureFormatSentinel(ctx, sealBackend, len(sentinelKeys) == 0); sentinelErr != nil {
		_ = sealStore.Close()
		s.sealStore = nil
		s.barrier = nil
		return nil, nil, &SealSentinelError{Err: sentinelErr}
	}

	// Auto-unseal so the data-plane PebbleDB can be opened with the
	// encrypting FS. Skipping this step is only valid when the data
	// backend does not depend on the barrier being unsealed (memory,
	// file). For the disk-backed pebble backend with a barrier, an
	// unsealed state is required and a sealed barrier surfaces
	// BarrierSealedError to the caller.
	if config.Barrier.AutoUnseal {
		if err := s.runAutoUnseal(ctx, barrier, effStrategy, config.Barrier, softwareSecret); err != nil {
			_ = sealStore.Close()
			s.sealStore = nil
			s.barrier = nil
			return nil, nil, err
		}
	}

	// Build the data-plane FS only when the pebble backend is in use
	// and the barrier reached the unsealed state. For memory / file
	// backends the wrap is a no-op; we return nil so the storage
	// constructor takes the default path.
	var dataPlaneFS vfs.FS
	if backendType == database.BackendPebble {
		if barrier.IsSealed() {
			_ = sealStore.Close()
			s.sealStore = nil
			s.barrier = nil
			return nil, nil, &BarrierSealedError{Err: seal.ErrSealed}
		}
		dataPlaneFS = encfs.New(vfs.Default, barrier)
		if dataPlaneFS == nil {
			// encfs.New returns nil when the barrier is nil, which can
			// only happen here on a programming error; surface it as
			// a typed barrier-create failure rather than panicking on
			// a downstream nil FS.
			_ = sealStore.Close()
			s.sealStore = nil
			s.barrier = nil
			return nil, nil, &BarrierCreateError{Err: errors.New("encfs.New returned nil")}
		}
	}

	return service.NewBarrierService(barrier), dataPlaneFS, nil
}

// runAutoUnseal initializes (on a fresh seal sub-store) or unseals (on
// an existing one) the barrier using the configured credentials. The
// software strategy uses the shared softwareSecret; the Shamir strategy
// reconstructs from the configured share set.
func (s *Server) runAutoUnseal(
	ctx context.Context,
	barrier *seal.StorageBarrier,
	strategy seal.StrategyID,
	cfg *BarrierConfig,
	softwareSecret []byte,
) error {
	switch strategy {
	case seal.StrategyShamir:
		if len(cfg.ShamirShares) == 0 {
			return &BarrierUnsealError{Err: errors.New("auto-unseal requires Barrier.ShamirShares for shamir strategy")}
		}
		// Shamir initialization requires a seed secret on a fresh
		// barrier. Try Unseal first; on ErrNotInitialized fall back
		// to InitializeShamir + UnsealWithShares.
		err := barrier.Unseal(ctx, seal.Credentials{})
		if err == nil {
			// The barrier was already unsealed; nothing more to do.
			return nil
		}
		if errors.Is(err, seal.ErrAlreadyUnsealed) {
			return nil
		}
		if !errors.Is(err, seal.ErrNotInitialized) {
			// Either ErrInvalidCredentials, missing strategy, or a
			// real error — let it bubble up.
			return &BarrierUnsealError{Err: err}
		}
		// Fresh barrier: initialize Shamir, then unseal with the
		// supplied shares. The seed credential is the bootstrap
		// passphrase to keep parity with the embedded software path.
		if _, initErr := barrier.InitializeShamir(ctx, seal.Credentials{Secret: softwareSecret}); initErr != nil {
			return &BarrierUnsealError{Err: initErr}
		}
		if unsealErr := barrier.UnsealWithShares(ctx, cfg.ShamirShares); unsealErr != nil {
			return &BarrierUnsealError{Err: unsealErr}
		}
		return nil

	default:
		// StrategyPassphrase (or zero value).
		creds := seal.Credentials{Secret: softwareSecret}
		// Initialize on the very first boot; subsequent boots Unseal.
		err := barrier.Initialize(ctx, creds)
		if err == nil {
			return nil
		}
		if !errors.Is(err, seal.ErrAlreadyInitialized) {
			return &BarrierUnsealError{Err: err}
		}
		if unsealErr := barrier.Unseal(ctx, creds); unsealErr != nil {
			if errors.Is(unsealErr, seal.ErrAlreadyUnsealed) {
				return nil
			}
			return &BarrierUnsealError{Err: unsealErr}
		}
		return nil
	}
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

// closeSeal releases the seal sub-store and clears the barrier
// reference. Used by NewServer error paths after the barrier has been
// constructed but before the data-plane storage is up.
func (s *Server) closeSeal() {
	if s.barrier != nil {
		s.barrier = nil
	}
	if s.sealStore != nil {
		_ = s.sealStore.Close()
		s.sealStore = nil
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
