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
	"strconv"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/encfs"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/secure"
)

const serverDefaultShardID uint64 = 1

// wipeFn is secure.Wipe, indirected so a test can observe the one thing this
// package must do with its passphrase copy-out and cannot otherwise see.
//
// The copy setupBarrier makes is a LOCAL. No caller holds it, nothing
// downstream retains it, and it is unreachable the instant setupBarrier
// returns — so the wipe is the only observable event in its entire lifetime,
// and "was it wiped" is not a question any assertion on a returned value can
// answer. Swapping this is also what lets a test assert the wipe happened
// BEFORE setupBarrier returned rather than at some unspecified later point,
// which is the property that actually matters.
//
// Same device, and the same reason, as purgeFn and signalExit in pkg/secure.
// It is never swapped outside a test.
var wipeFn = secure.Wipe

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

// BarrierPassphraseError indicates that BarrierConfig.Passphrase was SUPPLIED
// but could not be read: the buffer was destroyed, or it is a zero value, or
// its borrow yielded no bytes.
//
// It exists to keep an unusable credential from being mistaken for an absent
// one (go-qrdb#429). secure.Buffer.WithBytes returns the same *NilBufferError
// for nil and for destroyed, so a single error check reads both as "no
// passphrase configured" — and on a fresh data dir that difference is silent
// and permanent: the barrier initializes under seal.StrategyNone, IsSealed()
// is false, encfs wraps the data plane with a root key sealed by nothing, and
// an embedder that asked for a passphrase barrier gets plaintext at rest with
// nothing to indicate it.
//
// A nil Passphrase is still "not configured" and still yields a none barrier.
// Only a NON-NIL buffer that cannot be read produces this error.
type BarrierPassphraseError struct {
	// Err is the borrow failure, or nil when the borrow succeeded and
	// returned no bytes.
	Err error
}

// Error returns a human-readable description.
func (e *BarrierPassphraseError) Error() string {
	const head = "embedded: Barrier.Passphrase was supplied but could not be read"
	const tail = "; refusing to fall back to an unsealed barrier"
	if e.Err == nil {
		return head + " (it holds no bytes)" + tail
	}
	return head + " (" + e.Err.Error() + ")" + tail
}

// Unwrap returns the underlying error.
func (e *BarrierPassphraseError) Unwrap() error { return e.Err }

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

	// AuditTrail, when set, is the file of the server's compliance audit
	// trail -- the same hash-chained file, ring and operational log qrdbd
	// keeps (audit.OpenTrail) -- and audit.DefaultTrailFile is qrdbd's
	// name for it. A relative path is under DataDir, and the trail is
	// confined to DataDir -- or, for an absolute path, to the file's own
	// directory (audit.OpenTrailIn): a symbolic link there that resolves
	// outside refuses the trail. Empty, the default, keeps no trail. NewServer opens it before anything else, so the
	// barrier records from its first moment, and a trail that cannot be
	// opened fails NewServer (*AuditTrailError) with nothing started. The
	// barrier records its events on it, keyed by the system tenant, and the
	// transport serves the audit query from it; Shutdown closes it.
	AuditTrail string
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
	// StrategyShamir cannot auto-unseal: a Shamir barrier opens only when
	// an authenticated quorum of its enrolled share holders submits, and
	// no configuration may carry their shares.
	AutoUnseal bool

	// Passphrase is the secret used by software / passphrase-style
	// strategies during AutoUnseal.
	//
	// NIL AND DESTROYED ARE DIFFERENT, and the difference is the
	// difference between encrypted and not:
	//
	//   nil        No passphrase was configured. With StrategyPassphrase
	//              (or the empty default) the embedded transport comes up
	//              with NO barrier — seal.StrategyNone, at-rest data in
	//              plaintext. go-quicraft announces it at initialize
	//              ("barrier performs NO encryption"). This is the
	//              long-standing behaviour of leaving the field unset.
	//   non-nil    A credential WAS configured. If it cannot be read —
	//              destroyed, or the zero value — NewServer FAILS with
	//              *BarrierPassphraseError. It does not fall back.
	//
	// THIS DOC PREVIOUSLY CLAIMED A FALLBACK TO "the deterministic
	// bootstrap passphrase keyed by deployment ID 0". That was false and
	// is corrected here (go-qrdb#429): no such derivation exists anywhere
	// in this module — the only thing resembling it is a string a test
	// builds and passes to BarrierInitialize explicitly — and the
	// resolution ten lines into setupBarrier says so in its own comment,
	// "there is no deployment-derived passphrase". On a field whose doc
	// is the only thing an embedder reads, the gap between "derives a
	// passphrase" and "comes up with no barrier" is the gap between
	// encrypted and not.
	//
	// It is a *secure.Buffer and not a []byte. An embedder fills this
	// struct from its own key source — nothing decodes it and no flag
	// parser writes it — so it is a HOLDER of key material for the whole
	// life of the process it configures, which is exactly the population
	// pkg/secure's rule 3 exists for (go-qrdb#429). Use
	// secure.NewBufferFromBytes; the SDK re-exports the type and its
	// constructors as sdk/go/secure so no consumer needs the internal
	// path. The buffer stays the CALLER'S to destroy — NewServer copies
	// out of it under a borrow and never takes ownership. Destroying it
	// and then constructing another Server with the same config is the
	// obvious consequence of that ownership rule, and it is exactly the
	// case that must fail loudly rather than quietly drop the barrier.
	Passphrase *secure.Buffer
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

	// hub and lessor are the engine's watch hub and lease table: the
	// engine publishes every change to hub and rebuilds its durable leases
	// into lessor, and the watch, lease and lock services are built over
	// them. stopExpiry ends the lease-expiry sweeper running over lessor
	// and waits for it to exit.
	hub        *watch.Hub
	lessor     *lease.Lessor
	stopExpiry func()

	// trail is the compliance audit trail ServerConfig.AuditTrail opens,
	// nil when none is kept. It closes last.
	trail audit.AuditLogger
}

// AuditTrailError is returned by NewServer when the configured compliance
// audit trail cannot be opened. The server does not start: one configured to
// keep a trail does not run without it.
type AuditTrailError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *AuditTrailError) Error() string {
	return "embedded: compliance audit trail " + strconv.Quote(e.Path) + " cannot be opened: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *AuditTrailError) Unwrap() error { return e.Err }

// openAuditTrail opens the trail config.AuditTrail names, or returns nil
// when it names none. A relative path is resolved under DataDir, which it
// then requires.
func openAuditTrail(config *ServerConfig) (audit.AuditLogger, error) {
	path := config.AuditTrail
	if path == "" {
		return nil, nil
	}
	// The trail is confined to the directory it is opened in -- DataDir for
	// a relative path, the file's own directory for an absolute one -- so a
	// symbolic link there cannot carry it anywhere else.
	dir, name := filepath.Dir(path), filepath.Base(path)
	if !filepath.IsAbs(path) {
		if config.DataDir == "" {
			return nil, &ServerConfigError{Field: "AuditTrail", Message: "a relative path needs DataDir"}
		}
		dir, name = config.DataDir, path
	}
	trail, err := audit.OpenTrailIn(dir, name)
	if err != nil {
		return nil, &AuditTrailError{Path: filepath.Join(dir, name), Err: err}
	}
	return trail, nil
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

	// The compliance trail is opened before anything else, so the barrier
	// records from its first moment, and a trail that cannot be opened stops
	// the start with nothing running. A start that fails later closes it.
	trail, err := openAuditTrail(config)
	if err != nil {
		return nil, err
	}
	srv.trail = trail
	started := false
	defer func() {
		if !started {
			srv.closeTrail()
		}
	}()

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

	// Create store engine. MVCC for pebble/memory, KV for file. The engine
	// publishes its changes to the watch hub and keeps its leases in the
	// lessor, which the watch, lease and lock services below are built over.
	srv.hub = watch.NewHub()
	srv.lessor = lease.NewLessor()
	sm, err := createServerEngine(backendType, stg, pdb, srv.hub, srv.lessor)
	if err != nil {
		srv.closeCoordination()
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
	watchSvc := service.NewWatchService(srv.hub)
	indexSvc := service.NewIndexService(host, serverDefaultShardID)
	tsSvc := service.NewTimeSeriesService(host, serverDefaultShardID)
	lockSvc, err := service.NewLockService(txSvc, leaseSvc, watchSvc, mvccSvc, logger)
	if err != nil {
		srv.closeAll()
		return nil, &ServerTransportCreateError{Err: err}
	}

	// The lease-expiry sweeper: without it a lease's keys -- and a lock
	// held by a process that died -- are never deleted. Shutdown stops it,
	// and waits for it, before closing the backend it revokes through.
	srv.stopExpiry = lifecycle.StartLocalLeaseExpiry(context.Background(), srv.lessor, leaseSvc, logger)

	opts := []Option{
		WithMVCCService(mvccSvc),
		WithTransactionService(txSvc),
		WithBatchService(batchSvc),
		WithLeaseService(leaseSvc),
		WithWatchService(watchSvc),
		WithLockService(lockSvc),
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
	if srv.trail != nil {
		opts = append(opts, WithAuditLogger(srv.trail))
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
	started = true
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

	// 3. Stop the lease-expiry sweeper before the backend it revokes
	// through, then close the local backend (state machine).
	if s.stopExpiry != nil {
		s.stopExpiry()
		s.stopExpiry = nil
	}
	if s.localBackend != nil {
		if err := s.localBackend.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.localBackend = nil
	}
	s.closeCoordination()

	// 4. Close the storage backend.
	if s.stg != nil {
		if err := s.stg.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.stg = nil
		s.pebbleDB = nil
	}

	// 5. The trail closes last: everything above may still record on it.
	if s.trail != nil {
		if err := s.trail.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.trail = nil
	}

	if firstErr != nil {
		return &ShutdownError{Err: firstErr}
	}
	return nil
}

// closeTrail closes the compliance trail, when one is kept. Used by NewServer
// error paths.
func (s *Server) closeTrail() {
	if s.trail != nil {
		_ = s.trail.Close()
		s.trail = nil
	}
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
			if config.Barrier.AutoUnseal {
				return &ServerConfigError{Field: "Barrier.AutoUnseal", Message: "a shamir barrier is unsealed only by an authenticated share-holder quorum"}
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
// backends. hub and lr are the watch hub and lessor the engine publishes to
// and keeps its leases in (see lifecycle.NewLocalEngine).
func createServerEngine(backendType database.BackendType, stg storage.Storage, pdb *pebble.DB, hub *watch.Hub, lr *lease.Lessor) (backend.SMAdapter, error) {
	eng, err := lifecycle.NewLocalEngine(backendType, stg, pdb, hub, lr)
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
		// The barrier records on the compliance trail, when one is kept.
		AuditLogger: seal.NewComplianceAuditLogger(s.trail, tenantid.System, seal.SystemBarrierAuditName),
	}

	// Resolve the effective strategy. The canonical options are none /
	// passphrase (software) / shamir. A passphrase barrier REQUIRES an explicit
	// passphrase supplied by the admin; when the software/default strategy is
	// selected with no passphrase, the embedded transport runs WITHOUT a barrier
	// (none) — there is no deployment-derived passphrase. The passphrase, when
	// present, is supplied to the deferred software strategy at init/unseal and
	// run through the KDF.
	effStrategy := strategy
	// A LOCAL, not a field. The material has to cross pageable memory for
	// the width of one statement — the copy-out happens inside the borrow,
	// and seal.NewCredentials moves it into a sealed enclave outside the
	// borrow (it allocates locked memory of its own, which rule 6 forbids
	// under a borrow) and WIPES the copy as it goes. What rule 3 asks is
	// that the long-lived DECLARATION be the wipeable type, which
	// BarrierConfig.Passphrase is; the credential the barrier sees is
	// protected memory end to end.
	var softwareSecret []byte
	var softwareCreds seal.Credentials
	if effStrategy == seal.StrategyPassphrase || effStrategy == "" {
		// NIL IS "NOT CONFIGURED". NON-NIL-BUT-UNREADABLE IS AN ERROR.
		//
		// secure.Buffer.WithBytes returns the same *NilBufferError for a nil
		// receiver, a destroyed buffer and a zero value, so one error check
		// reads all three as "no passphrase" — and the consequence is not a
		// wrong error message, it is a WRONG SECURITY POSTURE. On a fresh data
		// dir the none branch below initializes successfully, IsSealed() is
		// false, encfs wraps the pebble data plane with a root key sealed by
		// nothing, and an embedder that asked for a passphrase barrier has
		// plaintext at rest, permanently, with nothing said about it.
		//
		// The lifecycle is why this needs guarding rather than documenting.
		// When this field was a []byte the unreadable case could only be
		// reached by passing an empty slice, which is plainly a caller bug. A
		// *secure.Buffer can be DESTROYED, the field doc says the buffer stays
		// the caller's to destroy, and destroying after one NewServer and
		// constructing again lands exactly here (go-qrdb#429).
		if config.Barrier.Passphrase == nil {
			// Not configured: the documented, long-standing meaning of
			// leaving the field unset. go-quicraft warns at initialize
			// ("barrier performs NO encryption"), so this is announced
			// rather than silent, and no second warning is emitted here.
			effStrategy = seal.StrategyNone
		} else {
			if copyErr := config.Barrier.Passphrase.WithBytes(func(b []byte) error {
				softwareSecret = append([]byte(nil), b...)
				return nil
			}); copyErr != nil {
				_ = sealStore.Close()
				s.sealStore = nil
				return nil, nil, &BarrierPassphraseError{Err: copyErr}
			}
			// THIS COPY IS ORDINARY, PAGEABLE HEAP, AND IT IS OURS TO WIPE.
			//
			// Before the *secure.Buffer conversion this line was
			// `softwareSecret = config.Barrier.Passphrase` — an ALIAS of the
			// embedder's own slice. There was exactly one copy of the
			// plaintext and it belonged to the caller, so the caller's own
			// wipe removed it (go-quicbaas zeroes its sealPassphrase byte by
			// byte right after handing it over). Copying out of the enclave
			// silently broke that: the embedder's wipe now reaches a slice
			// nobody else holds, while THIS copy lives in pageable memory
			// that no caller can reach, for the life of the process, created
			// on every NewServer.
			//
			// The mlocked buffer is a real improvement and this would have
			// been a net LOSS without the wipe: +1 unrecoverable plaintext
			// copy, exposed to exactly the disclosure routes the rule exists
			// for — a core dump, a swapped page, /proc/<pid>/mem. It is also
			// the standard pkg/secure/analyzer's own scope.go writes down for
			// the codec boundary ("every handler that reads one wipes it
			// before returning"), and this site reads one (go-qrdb#429).
			//
			// The heap copy's lifetime is ONE statement: seal.NewCredentials
			// below moves it into a sealed enclave and wipes it. The deferred
			// wipe is kept as this package's own guarantee — it is what the
			// test hook observes, it runs on every path including the error
			// return NewCredentials can take, and a second pass over zeroed
			// bytes costs nothing.
			//
			// OWNERSHIP OF THE CREDENTIAL ENDS WHEN runAutoUnseal RETURNS —
			// verified, not assumed, because destroying it here would
			// otherwise pull it out from under a live reader:
			//
			//   runAutoUnseal        stores nothing on s; passes the
			//                        seal.Credentials VALUE down.
			//   StorageBarrier       Initialize/Unseal forward creds to the
			//                        EpochBarrier and retain nothing.
			//   quicraft Barrier     Initialize/Unseal hand creds to the
			//                        strategy and retain nothing.
			//   SoftwareStrategy     the deferred path opens creds.Secret for
			//                        the KDF and retains nothing; the caller
			//                        owns the credential (go-quicraft#41).
			//   NoneStrategy         never references creds at all.
			//   Shamir               initializeShamirDirect ignores creds;
			//                        the credential-mode branch opens the
			//                        secret into its own working copy.
			//
			// setupBarrier outlives runAutoUnseal, so both defers fire after
			// the last read on every path, including every error path.
			defer wipeFn(softwareSecret)
			if len(softwareSecret) == 0 {
				// Defensive. Every constructor in pkg/secure rejects a
				// zero length, so a live buffer holding nothing should not
				// exist — and if one ever does, it is still a configured
				// credential that cannot seal anything.
				_ = sealStore.Close()
				s.sealStore = nil
				return nil, nil, &BarrierPassphraseError{}
			}
			var credErr error
			if softwareCreds, credErr = seal.NewCredentials(softwareSecret); credErr != nil {
				_ = sealStore.Close()
				s.sealStore = nil
				return nil, nil, &BarrierPassphraseError{Err: credErr}
			}
			defer softwareCreds.Secret.Destroy()
			effStrategy = seal.StrategyPassphrase
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
		if err := s.runAutoUnseal(ctx, barrier, effStrategy, config.Barrier, softwareCreds); err != nil {
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
// software strategy uses softwareCreds, which the caller owns and destroys.
// A Shamir barrier is never auto-unsealed.
func (s *Server) runAutoUnseal(
	ctx context.Context,
	barrier *seal.StorageBarrier,
	strategy seal.StrategyID,
	cfg *BarrierConfig,
	softwareCreds seal.Credentials,
) error {
	switch strategy {
	case seal.StrategyShamir:
		// Unreachable through NewServer, whose validation refuses it; a
		// Shamir barrier opens only through an authenticated holder quorum.
		return &BarrierUnsealError{Err: ErrShamirAutoUnseal}

	default:
		// StrategyPassphrase (or zero value).
		creds := softwareCreds
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
	if s.stopExpiry != nil {
		s.stopExpiry()
		s.stopExpiry = nil
	}
	s.closeBackend()
	s.closeCoordination()
	s.closeStorage()
}

// closeCoordination closes the watch hub, ending every open watch, and the
// lessor.
func (s *Server) closeCoordination() {
	if s.hub != nil {
		s.hub.Close()
		s.hub = nil
	}
	if s.lessor != nil {
		s.lessor.Close()
		s.lessor = nil
	}
}

// serverKVAdapter wraps a KVServicer as a kvstore.KVStore for user/role stores.
type serverKVAdapter struct {
	svc service.KVServicer
}

func newServerKVAdapter(svc service.KVServicer) kvstore.KVStore {
	return &serverKVAdapter{svc: svc}
}

// Get reports an absent key as (nil, nil), which is the kvstore.KVStore
// contract: absence is a VALUE, not an error.
//
// It decides absence by TYPE. It used to decide it by substring, and that
// worked only by coincidence of wording: the service wraps not-found as
// (&ReadError{&KeyNotFoundError{}}), which renders as "service: read failed:
// service: key not found: <key>" and happens to contain "not found". Nothing
// pinned that phrasing, so any rewording of either error would have turned
// every absent key into a hard error for pkg/user and pkg/role, which read
// through this method and test the result with `if data == nil`.
//
// errors.As was available the whole time -- both types are exported from
// pkg/service and ReadError has Unwrap -- and it is what the sibling adapter
// over this same interface already used (pkg/server/lifecycle/kv_adapter.go).
// Two adapters over one interface should not disagree about what absence is,
// and neither should decide it by reading English.
func (a *serverKVAdapter) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := a.svc.Get(ctx, key)
	if err != nil {
		var knf *service.KeyNotFoundError
		if errors.As(err, &knf) {
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

// Exists reports an absent key as (false, nil), not as an error.
//
// It returned (false, err) for an absent key, which is the commonest answer to
// the question it is asked: a caller asking "is this key here?" was handed an
// error for "no". That is the same contract Get honours, on the same seam, and
// the two disagreed.
//
// It has no production caller today -- newServerKVAdapter has one construction
// site, and neither pkg/user nor pkg/role calls Exists; they read through Get
// and test for nil. This is fixed for the trap rather than for a live
// incident: it is an exported-interface implementation, anything later wired
// to this kvstore.KVStore inherits the behaviour, and it looked correct
// sitting next to a Get that worked.
func (a *serverKVAdapter) Exists(ctx context.Context, key string) (bool, error) {
	val, err := a.svc.Get(ctx, key)
	if err != nil {
		var knf *service.KeyNotFoundError
		if errors.As(err, &knf) {
			return false, nil
		}
		return false, err
	}
	return val != nil, nil
}

// RegisterEntityIndexes refuses with kvstore.ErrIndexUnsupported: this
// adapter wraps a bare KVServicer with no index subsystem behind it.
//
// It previously returned nil here and (nil, nil) from every query, which
// meant a DAO built over it answered index queries with an empty result and
// no error — indistinguishable from "no matches". The typed refusal lets
// pkg/dao detect the gap at construction and plan full scans, so the same
// queries now return correct results.
func (a *serverKVAdapter) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return kvstore.ErrIndexUnsupported
}

// QueryIndex refuses; see RegisterEntityIndexes.
func (a *serverKVAdapter) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

// ScanIndex refuses; see RegisterEntityIndexes.
func (a *serverKVAdapter) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

// QueryCompoundIndex refuses; see RegisterEntityIndexes.
func (a *serverKVAdapter) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

// ScanCompoundIndex refuses; see RegisterEntityIndexes.
func (a *serverKVAdapter) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}
