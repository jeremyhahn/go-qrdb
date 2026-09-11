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

package database

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
)

const (
	databasesDir = "databases"
	metaFile     = "meta.json"
	dataSubDir   = "data"

	// barrierMetaSubDir is the per-database subdirectory (under dbDir, a
	// sibling of meta.json) that stores a database barrier's own bootstrap
	// key-wrapping metadata (its wrapped root key and epoch bookkeeping).
	// It is always kept separate from dataSubDir -- the application data
	// plane a barrier encrypts -- so the barrier's own bootstrap material
	// never mixes into the application keyspace a List/Range enumerates,
	// and so it remains reachable (to unseal) independent of whatever
	// dedicated DataDir the caller chose for application data.
	barrierMetaSubDir = "barrier"
)

// reservedDataSubdirs lists node-internal subdirectory names, relative to
// the manager's own data root, that a per-database DataDir must never equal
// or overlap. These are conventional locations for system-level state
// (barrier key material, Raft WAL/snapshot artifacts, node metadata) that
// are never a legitimate per-database storage target. This list is a
// defense-in-depth complement to -- not a substitute for -- the exhaustive
// per-database collision check in validateDataDir, which is what actually
// prevents one database's DataDir from being pointed at another's.
var reservedDataSubdirs = []string{"seal", "wal", "snapshot", "snapshots", "system", "raft", "meta"}

// Manager manages the lifecycle of all databases. It creates isolated
// storage directories for each database and provides CRUD operations.
//
// Storage directory layout:
//
//	{dataDir}/
//	  databases/
//	    {name}/
//	      meta.json     # DatabaseMeta serialized as JSON
//	      data/         # actual storage files
type Manager struct {
	logger     *slog.Logger
	dataDir    string
	databases  sync.Map // name (string) -> *Database
	factory    BackendFactory
	barrierMgr *DatabaseBarrierManager
	allocator  *shard.Allocator
	reclaimer  KeyspaceReclaimer

	// dropping holds the name of every database with a Drop in flight, so
	// two concurrent drops of one database cannot both report success. Drop
	// no longer removes the catalog entry first (see Drop), and that entry
	// was what used to decide the race: DropForTenant releases quota on a
	// successful drop, so two successes would release it twice.
	dropping sync.Map // name (string) -> struct{}
}

// NewManager creates a new Manager rooted at the given data directory.
// Pass a nil logger to use slog.Default(). Pass a nil factory to use
// the default EmbeddedBackendFactory.
func NewManager(logger *slog.Logger, dataDir string, opts ...ManagerOption) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{
		logger:  logger,
		dataDir: dataDir,
	}
	for _, opt := range opts {
		opt(m)
	}
	if m.factory == nil {
		m.factory = NewEmbeddedBackendFactory()
	}
	return m
}

// ManagerOption configures optional Manager behavior.
type ManagerOption func(*Manager)

// WithBackendFactory sets a custom BackendFactory for the Manager.
func WithBackendFactory(f BackendFactory) ManagerOption {
	return func(m *Manager) {
		m.factory = f
	}
}

// WithBarrierManager sets the DatabaseBarrierManager for barrier operations.
func WithBarrierManager(bm *DatabaseBarrierManager) ManagerOption {
	return func(m *Manager) {
		m.barrierMgr = bm
	}
}

// WithAllocator sets a shard.Allocator for automatic shard ID assignment.
// When set, Create assigns a unique shard ID to every database whose Raft
// config has ShardID == 0. If not set, auto-allocation is disabled and the
// caller is responsible for supplying explicit shard IDs.
func WithAllocator(alloc *shard.Allocator) ManagerOption {
	return func(m *Manager) {
		m.allocator = alloc
	}
}

// BarrierManager returns the barrier manager, or nil if not configured.
func (m *Manager) BarrierManager() *DatabaseBarrierManager {
	return m.barrierMgr
}

// SetBarrierManager sets or replaces the DatabaseBarrierManager used for
// barrier operations, and re-registers the barrier for every already-open
// database whose configuration has barrier encryption enabled on a
// dedicated store.
//
// This handles two ordering problems that WithBarrierManager (a construction
// -time-only option) cannot: (1) daemon boot rehydrates database metadata via
// OpenAll before the seal subsystem's BarrierRegistry exists, since the
// Manager is constructed before the barrier is unsealed; SetBarrierManager
// lets the caller wire the manager in once the registry becomes available,
// and this re-registration catches every database OpenAll already loaded.
// (2) an in-process cluster migration rebuilds the registry against a new
// root key; calling SetBarrierManager again re-registers every barrier
// -enabled database against the new registry.
//
// Registration failures for individual databases are logged and do not abort
// the call. A shared-store database is silently skipped: it was already
// rejected by EnsureBarrier/UpdateBarrier at the time its Barrier.Enabled
// flag was set, so its presence here would indicate on-disk metadata that
// predates that guard.
func (m *Manager) SetBarrierManager(bm *DatabaseBarrierManager) {
	m.barrierMgr = bm
	if bm == nil {
		return
	}
	m.databases.Range(func(key, value any) bool {
		name, _ := key.(string)
		db := value.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
		cfg := db.Config()
		if cfg.Barrier == nil || !cfg.Barrier.Enabled || !cfg.HasDedicatedStore() {
			return true
		}
		barrierMetaBackend, err := m.openBarrierMetaBackend(m.dbDir(name), name)
		if err != nil {
			m.logger.Warn("failed to open barrier metadata backend",
				slog.String("database", name),
				slog.Any("error", err))
			return true
		}
		storageAdapter := NewBackendStorageAdapter(barrierMetaBackend)
		if err := bm.RegisterBarrier(name, cfg.Barrier, storageAdapter); err != nil {
			_ = barrierMetaBackend.Close()
			m.logger.Warn("failed to register database barrier",
				slog.String("database", name),
				slog.Any("error", err))
			return true
		}
		encryptor := bm.GetBarrier(name)
		if encryptor == nil {
			_ = barrierMetaBackend.Close()
			m.logger.Warn("barrier registered but encryptor unavailable",
				slog.String("database", name))
			return true
		}
		// Always (re)wrap the immutable raw backend, never the possibly
		// already-wrapped Backend(): this makes repeated SetBarrierManager
		// calls (e.g. a cluster migration rebuilding the registry against a
		// new root key) idempotent instead of nesting BarrierBackend
		// wrappers.
		db.setActiveBackend(NewBarrierBackend(db.rawDataBackend(), encryptor))
		db.setBarrierMetaBackend(barrierMetaBackend)
		db.SetBarrierEnabled(true)
		db.SetStatus(StatusSealed)
		return true
	})
}

// Create creates a new database with the given configuration. The database
// is opened immediately after creation.
func (m *Manager) Create(cfg DatabaseConfig) (*Database, error) {
	return m.CreateWithCredentials(context.Background(), cfg, seal.Credentials{})
}

// CreateWithCredentials creates a new database with the given configuration
// and barrier credentials. When barrier encryption is enabled, the credentials
// are used to initialize the barrier and the database is immediately usable.
func (m *Manager) CreateWithCredentials(ctx context.Context, cfg DatabaseConfig, creds seal.Credentials) (*Database, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := m.validateDataDir(cfg.Name, cfg.DataDir); err != nil {
		return nil, err
	}
	if cfg.Barrier != nil && cfg.Barrier.Enabled && !cfg.HasDedicatedStore() {
		return nil, &SharedStoreBarrierUnsupportedError{Name: cfg.Name, Backend: cfg.Backend}
	}

	// Check for duplicates.
	if _, loaded := m.databases.Load(cfg.Name); loaded {
		return nil, &AlreadyExistsError{Name: cfg.Name}
	}

	dbDir := m.dbDir(cfg.Name)
	dataDir := filepath.Join(dbDir, dataSubDir)
	if cfg.DataDir != "" {
		dataDir = cfg.DataDir
	}

	// Create directory structure for both dbDir (meta.json) and dataDir (storage).
	if err := os.MkdirAll(dbDir, 0o750); err != nil {
		return nil, &OpenError{Name: cfg.Name, Err: err}
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, &OpenError{Name: cfg.Name, Err: err}
	}

	// Default mode for backward compat.
	if cfg.Mode == "" {
		cfg.Mode = ModeEmbedded
	}

	// Auto-allocate a shard ID when an allocator is configured and the caller
	// has not provided an explicit shard ID. The allocated ID is written into
	// the config so it is persisted in meta.json and survives restarts.
	if m.allocator != nil && cfg.Raft != nil && cfg.Raft.ShardID == 0 {
		cfg.Raft.ShardID = m.allocator.Allocate()
	}

	// Write meta.json.
	meta := DatabaseMeta(cfg)
	if err := m.writeMeta(dbDir, meta); err != nil {
		return nil, err
	}

	// Open storage backend via factory.
	backend, err := m.factory.Create(cfg, dataDir)
	if err != nil {
		return nil, err
	}

	// Register a dedicated per-database barrier when enabled, and wrap the
	// data-plane backend so every value it stores is encrypted under the
	// barrier's DEK (see BarrierBackend). The barrier's own bootstrap key
	// material lives in a separate dedicated metadata backend
	// (openBarrierMetaBackend), never in the data-plane backend itself.
	dataBackend := backend
	var barrierMetaBackend Backend
	if cfg.Barrier != nil && cfg.Barrier.Enabled && m.barrierMgr != nil {
		barrierMetaBackend, err = m.openBarrierMetaBackend(dbDir, cfg.Name)
		if err != nil {
			_ = backend.Close()
			return nil, err
		}
		storageAdapter := NewBackendStorageAdapter(barrierMetaBackend)
		if err := m.barrierMgr.EnsureBarrier(ctx, cfg.Name, cfg.Barrier, storageAdapter, creds); err != nil {
			_ = backend.Close()
			_ = barrierMetaBackend.Close()
			return nil, err
		}
		encryptor := m.barrierMgr.GetBarrier(cfg.Name)
		if encryptor == nil {
			_ = backend.Close()
			_ = barrierMetaBackend.Close()
			return nil, &BarrierEncryptorUnavailableError{Name: cfg.Name}
		}
		dataBackend = NewBarrierBackend(backend, encryptor)
	}

	db := NewDatabase(cfg, dataBackend, dataDir)
	if barrierMetaBackend != nil {
		db.setBarrierMetaBackend(barrierMetaBackend)
	}

	if cfg.Barrier != nil && cfg.Barrier.Enabled && m.barrierMgr != nil {
		db.SetBarrierEnabled(true)
	}

	// Store in map (race-safe with LoadOrStore).
	if _, loaded := m.databases.LoadOrStore(cfg.Name, db); loaded {
		// Another goroutine created it concurrently.
		_ = db.Backend().Close()
		if barrierMetaBackend != nil {
			_ = barrierMetaBackend.Close()
		}
		return nil, &AlreadyExistsError{Name: cfg.Name}
	}

	m.logger.Info("database created",
		slog.String("name", cfg.Name),
		slog.String("engine", string(cfg.Engine)),
		slog.String("backend", string(cfg.Backend)))

	return db, nil
}

// Open opens an existing database by reading its meta.json and
// initializing its storage backend.
func (m *Manager) Open(name string) (*Database, error) {
	// Already open?
	if val, ok := m.databases.Load(name); ok {
		return val.(*Database), nil //nolint:errcheck // type assertion on a value this container only ever holds
	}

	dbDir := m.dbDir(name)
	meta, err := m.readMeta(dbDir)
	if err != nil {
		return nil, err
	}

	dataDir := filepath.Join(dbDir, dataSubDir)
	if meta.DataDir != "" {
		dataDir = meta.DataDir
	}

	// Default missing mode to embedded for backward compat.
	mode := meta.Mode
	if mode == "" {
		mode = ModeEmbedded
	}

	cfg := DatabaseConfig{
		TenantID:     meta.TenantID,
		Name:         meta.Name,
		Engine:       meta.Engine,
		Backend:      meta.Backend,
		Protocol:     meta.Protocol,
		DataDir:      meta.DataDir,
		Mode:         mode,
		StorageCodec: meta.StorageCodec,
		Replicas:     meta.Replicas,
		DedicatedWAL: meta.DedicatedWAL,
		Raft:         meta.Raft,
		Barrier:      meta.Barrier,
		Connection:   meta.Connection,
	}
	if meta.Barrier != nil && meta.Barrier.Enabled && !cfg.HasDedicatedStore() {
		return nil, &SharedStoreBarrierUnsupportedError{Name: name, Backend: cfg.Backend}
	}
	if err := m.validateDataDir(name, meta.DataDir); err != nil {
		return nil, err
	}

	// Open storage backend via factory.
	backend, err := m.factory.Create(cfg, dataDir)
	if err != nil {
		return nil, err
	}

	// Register the per-database barrier when enabled and wrap the
	// data-plane backend so it decrypts (once unsealed) rather than serving
	// ciphertext to callers. The barrier starts sealed -- Get/Put/etc. fail
	// closed with SealedError -- and must be explicitly unsealed via
	// UnsealDatabase. See CreateWithCredentials for why the barrier's own
	// bootstrap metadata lives in a separate dedicated backend.
	dataBackend := backend
	var barrierMetaBackend Backend
	if meta.Barrier != nil && meta.Barrier.Enabled && m.barrierMgr != nil {
		barrierMetaBackend, err = m.openBarrierMetaBackend(dbDir, name)
		if err != nil {
			_ = backend.Close()
			return nil, err
		}
		storageAdapter := NewBackendStorageAdapter(barrierMetaBackend)
		if err := m.barrierMgr.RegisterBarrier(name, meta.Barrier, storageAdapter); err != nil {
			_ = backend.Close()
			_ = barrierMetaBackend.Close()
			return nil, err
		}
		encryptor := m.barrierMgr.GetBarrier(name)
		if encryptor == nil {
			_ = backend.Close()
			_ = barrierMetaBackend.Close()
			return nil, &BarrierEncryptorUnavailableError{Name: name}
		}
		dataBackend = NewBarrierBackend(backend, encryptor)
	}

	db := NewDatabase(cfg, dataBackend, dataDir)
	if barrierMetaBackend != nil {
		db.setBarrierMetaBackend(barrierMetaBackend)
	}

	// Mark barrier-enabled databases as sealed until explicitly unsealed.
	if meta.Barrier != nil && meta.Barrier.Enabled && m.barrierMgr != nil {
		db.SetBarrierEnabled(true)
		db.SetStatus(StatusSealed)
	}

	if actual, loaded := m.databases.LoadOrStore(name, db); loaded {
		// Another goroutine opened it concurrently.
		_ = db.Backend().Close()
		if barrierMetaBackend != nil {
			_ = barrierMetaBackend.Close()
		}
		return actual.(*Database), nil //nolint:errcheck // type assertion on a value this container only ever holds
	}

	m.logger.Info("database opened",
		slog.String("name", name),
		slog.String("engine", string(meta.Engine)),
		slog.String("backend", string(meta.Backend)))

	return db, nil
}

// Get returns an already-open database by name.
func (m *Manager) Get(name string) (*Database, error) {
	val, ok := m.databases.Load(name)
	if !ok {
		return nil, &NotFoundError{Name: name}
	}
	return val.(*Database), nil //nolint:errcheck // type assertion on a value this container only ever holds
}

// Drop closes and removes a database: the data it owns in the shared store,
// its barrier, its data directory and finally its catalog entry.
//
// Ordering: DEPENDENTS FIRST, THE RECORD THAT MAKES THE DATABASE RESOLVABLE
// LAST. This is the ordering pkg/sql/provider's Database.DropTable and
// CollectionService.DropCollection already use, and reversing it is exactly
// the defect go-qrdb#412 reports. Until this change the catalog entry went
// first, so a shared-store database's keys were orphaned the instant the
// entry was gone: nothing in the system named that keyspace again, the
// residue was permanent, and a database recreated under the same name
// inherited all of it.
//
// The worst state an interrupted Drop can leave is therefore a LIVE, EMPTY
// database — still resolvable, still serving, with some or all of its data
// already reclaimed. Re-issuing the drop completes it, because every step is
// idempotent and the name stays occupied until the last one. No state exists
// in which a later CREATE silently adopts a previous database's data.
//
// A reclamation failure fails the whole drop rather than being skipped:
// reporting success over data that is still there is the defect itself, and
// the surviving catalog entry is what makes the retry possible.
func (m *Manager) Drop(ctx context.Context, name string) error {
	val, ok := m.databases.Load(name)
	if !ok {
		return &NotFoundError{Name: name}
	}
	db := val.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds

	if _, inFlight := m.dropping.LoadOrStore(name, struct{}{}); inFlight {
		return &DropInProgressError{Name: name}
	}
	defer m.dropping.Delete(name)

	// 1. The data the database owns in a store it shares with others. Nothing
	//    below this line can reach it, and everything below this line makes it
	//    unreachable.
	if m.reclaimer != nil {
		if err := m.reclaimer.ReclaimDatabaseKeyspace(ctx, db.TenantID(), name); err != nil {
			return &KeyspaceReclaimError{Name: name, TenantID: db.TenantID(), Err: err}
		}
	}

	// 2. Close the database's own backend, so a dedicated store releases its
	//    files before the directory holding them is removed.
	if db.Status() == StatusOpen || db.Status() == StatusSealed {
		if err := db.Close(); err != nil {
			return &DropError{Name: name, Err: err}
		}
	}

	// 3. Remove barrier if it was enabled.
	if db.BarrierEnabled() && m.barrierMgr != nil {
		_ = m.barrierMgr.RemoveBarrier(name)
	}

	// 4. The durable catalog record: meta.json, and a dedicated store's data.
	dbDir := m.dbDir(name)
	if err := os.RemoveAll(dbDir); err != nil {
		return &DropError{Name: name, Err: err}
	}

	// 5. The in-memory mirror of the record removed in step 4. Last, and
	//    adjacent to it, so the window in which the two disagree is as narrow
	//    as it can be made without a persisted marker (go-qrdb#425).
	m.databases.Delete(name)

	m.logger.Info("database dropped", slog.String("name", name))
	return nil
}

// UpdateProtocol updates the wire protocol configuration for a database.
// It updates both the in-memory config and the on-disk meta.json.
func (m *Manager) UpdateProtocol(name string, protocol ProtocolConfig) error {
	val, ok := m.databases.Load(name)
	if !ok {
		return &NotFoundError{Name: name}
	}
	db := val.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
	db.UpdateProtocol(protocol)

	// Rewrite meta.json with the updated protocol config.
	cfg := db.Config()
	meta := DatabaseMeta{
		TenantID:     cfg.TenantID,
		Name:         cfg.Name,
		Engine:       cfg.Engine,
		Backend:      cfg.Backend,
		Protocol:     cfg.Protocol,
		Mode:         cfg.Mode,
		StorageCodec: cfg.StorageCodec,
		Replicas:     cfg.Replicas,
		DedicatedWAL: cfg.DedicatedWAL,
		Raft:         cfg.Raft,
		Barrier:      cfg.Barrier,
		Connection:   cfg.Connection,
	}
	dbDir := m.dbDir(name)
	if err := m.writeMeta(dbDir, meta); err != nil {
		return err
	}

	m.logger.Info("database protocol config updated",
		slog.String("name", name),
		slog.Bool("mysql", protocol.MySQL),
		slog.Bool("postgresql", protocol.PostgreSQL))
	return nil
}

// UpdateEtcd updates the etcd wire protocol configuration for a database.
// It updates both the in-memory config and the on-disk meta.json.
func (m *Manager) UpdateEtcd(name string, etcd *EtcdConfig) error {
	if etcd != nil {
		if err := etcd.Validate(); err != nil {
			return err
		}
	}
	val, ok := m.databases.Load(name)
	if !ok {
		return &NotFoundError{Name: name}
	}
	db := val.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
	db.UpdateEtcd(etcd)

	return m.rewriteMeta(name, db)
}

// UpdateRaft updates the Raft consensus configuration for a database.
// It updates both the in-memory config and the on-disk meta.json.
func (m *Manager) UpdateRaft(name string, raft *DatabaseRaftConfig) error {
	if raft != nil {
		if err := raft.Validate(); err != nil {
			return err
		}
	}
	val, ok := m.databases.Load(name)
	if !ok {
		return &NotFoundError{Name: name}
	}
	db := val.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
	db.updateRaft(raft)

	return m.rewriteMeta(name, db)
}

// UpdateReplicas updates the replication factor recorded for a database. It
// updates both the in-memory config and the on-disk meta.json so
// DatabaseInfo.Replicas (reported by Get/GetForTenant/List) and the
// restart-time manager-derived fallback (managerRaftInfoSource) stay
// consistent with the replicated placement table after
// DatabaseService.ScaleForTenant adds or removes voting replicas. It does not
// itself change cluster membership or placement -- callers update those
// first and call UpdateReplicas last, once the new replica count is durable.
func (m *Manager) UpdateReplicas(name string, replicas int) error {
	val, ok := m.databases.Load(name)
	if !ok {
		return &NotFoundError{Name: name}
	}
	db := val.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
	db.updateReplicas(replicas)

	return m.rewriteMeta(name, db)
}

// UpdateBarrier updates the encryption barrier configuration for a database.
// It updates both the in-memory config and the on-disk meta.json.
//
// Enabling or disabling barrier encryption is a data migration, not a
// metadata toggle: flipping Enabled from false to true on a database that
// may already hold plaintext data would leave every existing value
// unencrypted while new writes are protected (a false sense of security);
// flipping it from true to false on an actively barrier-wrapped database
// would leave already-encrypted ciphertext on disk while reads stop
// decrypting it (corrupting, not removing, protection). This method
// performs neither re-encryption nor decryption, so both real transitions
// are rejected with *BarrierMigrationNotSupportedError: a database's
// barrier state is fixed at creation time (CreateWithCredentials). Calls
// that do not change the active state -- disabled staying disabled, or
// enabled staying enabled (e.g. to refresh non-secret bookkeeping such as
// Mode/SharesRequired/SharesTotal ahead of a future Shamir strategy) --
// are the only ones this method actually performs.
func (m *Manager) UpdateBarrier(name string, barrier *DatabaseBarrierConfig) error {
	val, ok := m.databases.Load(name)
	if !ok {
		return &NotFoundError{Name: name}
	}
	db := val.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds

	wantEnabled := barrier != nil && barrier.Enabled
	activeNow := db.BarrierEnabled()

	if wantEnabled {
		cfg := db.Config()
		if !cfg.HasDedicatedStore() {
			return &SharedStoreBarrierUnsupportedError{Name: name, Backend: cfg.Backend}
		}
		if barrier.Mode != "" && barrier.Mode != BarrierModePassphrase {
			return &UnsupportedBarrierStrategyError{Mode: barrier.Mode}
		}
		if !activeNow {
			return &BarrierMigrationNotSupportedError{Name: name, Direction: "enable"}
		}
	} else if activeNow {
		return &BarrierMigrationNotSupportedError{Name: name, Direction: "disable"}
	}

	db.updateBarrier(barrier)

	return m.rewriteMeta(name, db)
}

// UpdateConnection updates the remote connection configuration for a database.
// It updates both the in-memory config and the on-disk meta.json.
func (m *Manager) UpdateConnection(name string, conn *DatabaseConnectionConfig) error {
	if conn != nil {
		if err := conn.Validate(); err != nil {
			return err
		}
	}
	val, ok := m.databases.Load(name)
	if !ok {
		return &NotFoundError{Name: name}
	}
	db := val.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
	db.updateConnection(conn)

	return m.rewriteMeta(name, db)
}

// rewriteMeta rebuilds and rewrites the meta.json for a database from its
// current in-memory configuration.
func (m *Manager) rewriteMeta(name string, db *Database) error {
	cfg := db.Config()
	meta := DatabaseMeta{
		TenantID:     cfg.TenantID,
		Name:         cfg.Name,
		Engine:       cfg.Engine,
		Backend:      cfg.Backend,
		Protocol:     cfg.Protocol,
		Mode:         cfg.Mode,
		StorageCodec: cfg.StorageCodec,
		Replicas:     cfg.Replicas,
		DedicatedWAL: cfg.DedicatedWAL,
		Raft:         cfg.Raft,
		Barrier:      cfg.Barrier,
		Connection:   cfg.Connection,
	}
	dbDir := m.dbDir(name)
	return m.writeMeta(dbDir, meta)
}

// List returns information about all open databases, sorted by name.
func (m *Manager) List() []DatabaseInfo {
	var infos []DatabaseInfo
	m.databases.Range(func(_, value any) bool {
		db := value.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
		infos = append(infos, db.Info())
		return true
	})
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Name < infos[j].Name
	})
	return infos
}

// Close shuts down all open databases and the backend factory.
func (m *Manager) Close() error {
	var firstErr error
	m.databases.Range(func(key, value any) bool {
		db := value.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
		status := db.Status()
		if status == StatusOpen || status == StatusSealed {
			if err := db.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		m.databases.Delete(key)
		return true
	})
	if err := m.factory.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// OpenAll scans the databases directory and opens all databases that
// have a valid meta.json. When an allocator is configured it also
// advances the allocator past the highest persisted shard ID so that
// subsequent Create calls never reuse an existing shard ID.
func (m *Manager) OpenAll() error {
	dbsDir := filepath.Join(m.dataDir, databasesDir)
	entries, err := os.ReadDir(dbsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no databases directory yet
		}
		return &MetaReadError{Path: dbsDir, Err: err}
	}

	var maxShardID uint64

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		db, openErr := m.Open(name)
		if openErr != nil {
			m.logger.Warn("failed to open database",
				slog.String("name", name),
				slog.String("error", openErr.Error()))
			// Continue opening other databases.
			continue
		}
		// Track the highest shard ID across all successfully opened databases.
		if m.allocator != nil {
			if id := db.ShardID(); id > maxShardID {
				maxShardID = id
			}
		}
	}

	// Advance the allocator so the next allocation starts after every known
	// shard ID. Only advance when the discovered maximum is at or above the
	// allocator's current next value to avoid rolling the counter backwards.
	if m.allocator != nil && maxShardID > 0 {
		m.allocator.AdvanceTo(maxShardID + 1)
	}

	return nil
}

// DataDir returns the root data directory for all databases.
func (m *Manager) DataDir() string {
	return m.dataDir
}

// dbDir returns the directory path for a specific database.
func (m *Manager) dbDir(name string) string {
	return filepath.Join(m.dataDir, databasesDir, name)
}

// openBarrierMetaBackend opens (creating if necessary) the dedicated
// backend that persists a database barrier's own key-wrapping metadata --
// its wrapped root key and epoch bookkeeping -- at dbDir/barrier. This is
// entirely separate from the database's data-plane backend (dataDir):
// mixing the two would let a List/Range over application data also
// enumerate the barrier's own bootstrap key material, and would tie the
// barrier's reachability to whatever (possibly remote/tenant-owned)
// DataDir the caller chose for application data. A lightweight file-backed
// store is used regardless of the database's own configured Backend type,
// since this metadata must persist across restarts even for an in-memory
// database.
func (m *Manager) openBarrierMetaBackend(dbDir, name string) (Backend, error) {
	dir := filepath.Join(dbDir, barrierMetaSubDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, &OpenError{Name: name, Err: err}
	}
	store, err := file.New(dir)
	if err != nil {
		return nil, &StorageCreateError{Name: name, Backend: "file", Err: err}
	}
	return NewStandaloneBackend(store), nil
}

// resolveDataDir canonicalizes dataDir to a cleaned absolute path and
// resolves symlinks along its longest existing ancestor, so a pre-planted
// symlink cannot be used to defeat validateDataDir's checks even though the
// directory itself may not exist yet (Create makes it afterward via
// os.MkdirAll).
func resolveDataDir(dataDir string) (string, error) {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)

	dir := abs
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			full := resolved
			for i := len(suffix) - 1; i >= 0; i-- {
				full = filepath.Join(full, suffix[i])
			}
			return filepath.Clean(full), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the filesystem root without finding an existing
			// ancestor; nothing left to resolve.
			return abs, nil
		}
		suffix = append(suffix, filepath.Base(dir))
		dir = parent
	}
}

// pathContains reports whether candidate is equal to base or nested within
// it. Both arguments must already be cleaned, comparable paths (as returned
// by resolveDataDir).
func pathContains(base, candidate string) bool {
	if base == candidate {
		return true
	}
	rel, err := filepath.Rel(base, candidate)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// validateDataDir enforces that a caller-supplied per-database DataDir
// cannot be used to escape into another database's storage, the manager's
// own reserved subtrees, or the manager's shared catalog root. An empty
// dataDir is always valid; it selects the package-computed default path
// under the manager's own catalog directory.
//
// This is deliberately NOT restricted to paths nested under the manager's
// own data root: legitimate per-tenant DataRoots (IsolationVolume /
// IsolationRaft) are allocated from an independent volume pool and may live
// entirely outside it (see pkg/tenant.TenantInfo.DataRoot), and are
// computed server-side rather than taken from caller input. What IS always
// enforced, regardless of where the resolved path lives, is that it cannot
// collide with -- or reach into -- storage the manager or another database
// already owns.
func (m *Manager) validateDataDir(name, dataDir string) error {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	resolved, err := resolveDataDir(dataDir)
	if err != nil {
		return &InvalidDataDirError{Name: name, DataDir: dataDir, Err: err}
	}
	root, err := resolveDataDir(m.dataDir)
	if err != nil {
		return &InvalidDataDirError{Name: name, DataDir: dataDir, Err: err}
	}

	// Never the manager's own data root, and never an ancestor of it: both
	// would fold the manager's entire catalog -- every other database's
	// meta.json, barrier metadata, and default storage -- inside this one
	// database's view.
	if pathContains(resolved, root) {
		return &DataDirReservedPathError{Name: name, DataDir: dataDir, Reserved: root}
	}

	// Never the shared catalog directory itself.
	catalog := filepath.Join(root, databasesDir)
	if resolved == catalog {
		return &DataDirReservedPathError{Name: name, DataDir: dataDir, Reserved: catalog}
	}

	// Never a node-internal reserved subtree (see reservedDataSubdirs), in
	// either nesting direction.
	for _, sub := range reservedDataSubdirs {
		reserved := filepath.Join(root, sub)
		if pathContains(reserved, resolved) || pathContains(resolved, reserved) {
			return &DataDirReservedPathError{Name: name, DataDir: dataDir, Reserved: reserved}
		}
	}

	// Never inside -- or the parent of -- this database's own catalog
	// directory (meta.json plus the barrier metadata store): a DataDir
	// reaching into dbDir would let application data collide with barrier
	// bootstrap key material or the on-disk metadata record.
	ownDBDir, err := resolveDataDir(m.dbDir(name))
	if err != nil {
		return &InvalidDataDirError{Name: name, DataDir: dataDir, Err: err}
	}
	if pathContains(resolved, ownDBDir) || pathContains(ownDBDir, resolved) {
		return &DataDirReservedPathError{Name: name, DataDir: dataDir, Reserved: ownDBDir}
	}

	// Never collide -- by equality, ancestry, or descendance -- with any
	// OTHER already-registered database's own catalog directory or data
	// directory.
	var conflict error
	m.databases.Range(func(key, value any) bool {
		otherName, _ := key.(string)
		if otherName == name {
			return true
		}
		db := value.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds

		if otherDBDir, derr := resolveDataDir(m.dbDir(otherName)); derr == nil {
			if pathContains(resolved, otherDBDir) || pathContains(otherDBDir, resolved) {
				conflict = &DataDirConflictError{Name: name, DataDir: dataDir, With: otherName}
				return false
			}
		}
		if otherDataDir, derr := resolveDataDir(db.DataDir()); derr == nil {
			if pathContains(resolved, otherDataDir) || pathContains(otherDataDir, resolved) {
				conflict = &DataDirConflictError{Name: name, DataDir: dataDir, With: otherName}
				return false
			}
		}
		return true
	})
	return conflict
}

// writeMeta writes the DatabaseMeta as JSON to the meta.json file.
func (m *Manager) writeMeta(dbDir string, meta DatabaseMeta) error {
	path := filepath.Join(dbDir, metaFile)
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return &MetaWriteError{Path: path, Err: err}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return &MetaWriteError{Path: path, Err: err}
	}
	return nil
}

// readMeta reads the DatabaseMeta from the meta.json file.
func (m *Manager) readMeta(dbDir string) (*DatabaseMeta, error) {
	path := filepath.Join(dbDir, metaFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &MetaReadError{Path: path, Err: err}
	}
	var meta DatabaseMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, &MetaReadError{Path: path, Err: err}
	}
	return &meta, nil
}
