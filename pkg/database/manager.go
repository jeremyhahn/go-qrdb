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
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

const (
	databasesDir = "databases"
	metaFile     = "meta.json"
	dataSubDir   = "data"
)

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

	// Wire barrier encryption when enabled and barrier manager is available.
	activeBackend := Backend(backend)
	if cfg.Barrier != nil && cfg.Barrier.Enabled && m.barrierMgr != nil {
		storageAdapter := NewBackendStorageAdapter(backend)
		if err := m.barrierMgr.EnsureBarrier(ctx, cfg.Name, cfg.Barrier, storageAdapter, creds); err != nil {
			_ = backend.Close()
			return nil, err
		}
		barrier := m.barrierMgr.GetBarrier(cfg.Name)
		if barrier != nil {
			activeBackend = NewBarrierBackend(backend, barrier)
		}
	}

	db := NewDatabase(cfg, activeBackend, dataDir)

	if cfg.Barrier != nil && cfg.Barrier.Enabled && m.barrierMgr != nil {
		db.SetBarrierEnabled(true)
	}

	// Store in map (race-safe with LoadOrStore).
	if _, loaded := m.databases.LoadOrStore(cfg.Name, db); loaded {
		// Another goroutine created it concurrently.
		_ = activeBackend.Close()
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
		return val.(*Database), nil
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
		Raft:         meta.Raft,
		Barrier:      meta.Barrier,
		Connection:   meta.Connection,
	}

	// Open storage backend via factory.
	backend, err := m.factory.Create(cfg, dataDir)
	if err != nil {
		return nil, err
	}

	// Wire barrier encryption when enabled. The barrier starts sealed
	// and must be explicitly unsealed via UnsealDatabase.
	activeBackend := Backend(backend)
	if meta.Barrier != nil && meta.Barrier.Enabled && m.barrierMgr != nil {
		storageAdapter := NewBackendStorageAdapter(backend)
		if err := m.barrierMgr.RegisterBarrier(name, meta.Barrier, storageAdapter); err != nil {
			_ = backend.Close()
			return nil, err
		}
		barrier := m.barrierMgr.GetBarrier(name)
		if barrier != nil {
			activeBackend = NewBarrierBackend(backend, barrier)
		}
	}

	db := NewDatabase(cfg, activeBackend, dataDir)

	// Mark barrier-enabled databases as sealed until explicitly unsealed.
	if meta.Barrier != nil && meta.Barrier.Enabled && m.barrierMgr != nil {
		db.SetBarrierEnabled(true)
		db.SetStatus(StatusSealed)
	}

	if actual, loaded := m.databases.LoadOrStore(name, db); loaded {
		// Another goroutine opened it concurrently.
		_ = activeBackend.Close()
		return actual.(*Database), nil
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
	return val.(*Database), nil
}

// Drop closes and removes a database, deleting its data directory.
func (m *Manager) Drop(name string) error {
	val, loaded := m.databases.LoadAndDelete(name)
	if !loaded {
		return &NotFoundError{Name: name}
	}

	db := val.(*Database)
	if db.Status() == StatusOpen || db.Status() == StatusSealed {
		if err := db.Close(); err != nil {
			return &DropError{Name: name, Err: err}
		}
	}

	// Remove barrier if it was enabled.
	if db.BarrierEnabled() && m.barrierMgr != nil {
		_ = m.barrierMgr.RemoveBarrier(name)
	}

	dbDir := m.dbDir(name)
	if err := os.RemoveAll(dbDir); err != nil {
		return &DropError{Name: name, Err: err}
	}

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
	db := val.(*Database)
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
	db := val.(*Database)
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
	db := val.(*Database)
	db.updateRaft(raft)

	return m.rewriteMeta(name, db)
}

// UpdateBarrier updates the encryption barrier configuration for a database.
// It updates both the in-memory config and the on-disk meta.json.
func (m *Manager) UpdateBarrier(name string, barrier *DatabaseBarrierConfig) error {
	val, ok := m.databases.Load(name)
	if !ok {
		return &NotFoundError{Name: name}
	}
	db := val.(*Database)
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
	db := val.(*Database)
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
		db := value.(*Database)
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
		db := value.(*Database)
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
