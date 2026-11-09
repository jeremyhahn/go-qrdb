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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

const (
	databasesDir = "databases"
	dataSubDir   = "data"

	// legacyMetaFile is the node-local database record a version before the
	// catalog wrote in each database's directory. Nothing reads it; OpenAll
	// refuses a node that still holds one (UnmigratedDatabasesError).
	legacyMetaFile = "meta.json"

	// tenantDatabasesDir holds every non-system tenant's database
	// directories (see dbDir). Its "." is outside a database name's
	// alphabet, so no system database's directory can be it.
	tenantDatabasesDir = "tenants.d"

	// barrierMetaSubDir is the per-database subdirectory (under dbDir) that
	// stores a database barrier's own bootstrap
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
//	    {name}/                    # a system-tenant database
//	      data/                    # actual storage files
//	    tenants.d/{sha256(tenant)}/{name}/
//
// A database's DatabaseMeta is its record in the replicated Catalog, not a
// file under its directory.
type Manager struct {
	logger     *slog.Logger
	dataDir    string
	databases  sync.Map // dbKey -> *Database
	factory    BackendFactory
	barrierMgr *DatabaseBarrierManager
	claimer    atomic.Pointer[claimerRef]
	catalogRef atomic.Pointer[catalogRef]
	reclaimer  KeyspaceReclaimer
	localNode  atomic.Pointer[string] // see SetLocalNode

	// dropping holds the name of every database with a Drop in flight, so
	// two concurrent drops of one database cannot both report success. Drop
	// no longer removes the catalog entry first (see Drop), and that entry
	// was what used to decide the race: DropForTenant releases quota on a
	// successful drop, so two successes would release it twice.
	dropping sync.Map // dbKey -> struct{}

	// inFlight holds the name of every database whose node-local resources
	// are being created, opened, released or torn down by a drop: one of
	// them at a time per name (see tryClaim). See CreateWithCredentials for
	// why two creates or opens must not interleave, and drop for why its
	// teardown must not interleave with either. The channel is closed when
	// the claim is released, for a drop waiting to take it (awaitClaim).
	inFlight sync.Map // dbKey -> chan struct{}
}

// dbKey identifies a database on this node: its owning tenant, normalized,
// and its name. It is the pair the catalog keys its records by (CatalogKey),
// so two tenants may each hold a database of one name, on one node or many.
type dbKey struct {
	tenant string
	name   string
}

// keyFor returns the key of tenantID's database name.
func keyFor(tenantID, name string) dbKey {
	return dbKey{tenant: tenantid.Normalize(tenantID), name: name}
}

// catalogTimeout bounds a catalog read or write made by a method whose
// signature carries no context.
const catalogTimeout = 30 * time.Second

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

// BarrierManager returns the barrier manager, or nil if not configured.
func (m *Manager) BarrierManager() *DatabaseBarrierManager {
	return m.barrierMgr
}

// barrierModeSupported reports whether a database barrier can be configured in
// mode: a built-in mode, or a strategy the barrier manager's registry holds.
// Without a barrier manager only the built-in modes are supported.
func (m *Manager) barrierModeSupported(mode string) bool {
	if m.barrierMgr != nil {
		return m.barrierMgr.SupportsMode(mode)
	}
	return mode == "" || mode == BarrierModePassphrase || mode == BarrierModeShamir
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
		k, _ := key.(dbKey)
		name := k.name
		db := value.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
		cfg := db.Config()
		if cfg.Barrier == nil || !cfg.Barrier.Enabled || !cfg.HasDedicatedStore() {
			return true
		}
		barrierMetaBackend, err := m.openBarrierMetaBackend(m.dbDir(k.tenant, name), name)
		if err != nil {
			m.logger.Warn("failed to open barrier metadata backend",
				slog.String("database", name),
				slog.Any("error", err))
			return true
		}
		storageAdapter := NewBackendStorageAdapter(barrierMetaBackend)
		if err := bm.RegisterBarrier(name, cfg.TenantID, cfg.Barrier, storageAdapter); err != nil {
			_ = barrierMetaBackend.Close()
			m.logger.Warn("failed to register database barrier",
				slog.String("database", name),
				slog.Any("error", err))
			return true
		}
		encryptor := bm.GetBarrier(name, cfg.TenantID)
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
	return m.create(ctx, cfg, creds, false)
}

// Provision returns the database cfg names, creating it when this node holds
// no record of it. It is how a node provisions the databases its
// configuration lists at boot -- every node of a cluster lists the same ones.
//
// A database created here gets the SHARED (empty) incarnation, not a fresh
// one: the database catalog is node-local, so every node provisions the same
// name independently, and the only incarnation they can all agree on without
// a replicated catalog is the empty one. The marker is cleared from the
// dropped tombstone to absent, which is live for the empty incarnation; a
// marker holding a live incarnation -- the name created through the API on
// some node -- refuses the provisioning with *AlreadyExistsError.
func (m *Manager) Provision(ctx context.Context, cfg DatabaseConfig) (*Database, error) {
	// A provisioned database is created on every node independently, so a
	// raft one would need a shard ID every node agrees on without a claim,
	// and nodes issuing on their own do not agree. No shipped path provisions
	// one; refuse it.
	if cfg.Raft != nil {
		return nil, &ProvisionRaftError{Name: cfg.Name}
	}
	if db, ok := m.databases.Load(keyFor(cfg.TenantID, cfg.Name)); ok {
		return db.(*Database), nil //nolint:errcheck // type assertion on a value this container only ever holds
	}
	// Another node, or an earlier run of this one, may have recorded it.
	db, err := m.Open(ctx, cfg.TenantID, cfg.Name)
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		return db, err
	}
	db, err = m.create(ctx, cfg, seal.Credentials{}, true)
	var exists *AlreadyExistsError
	if errors.As(err, &exists) {
		// Another node recorded it between the read above and this create.
		if db, openErr := m.Open(ctx, cfg.TenantID, cfg.Name); !errors.As(openErr, &notFound) {
			return db, openErr
		}
	}
	return db, err
}

// create is CreateWithCredentials and Provision. shared selects the empty,
// node-shared incarnation (see Provision).
func (m *Manager) create(ctx context.Context, cfg DatabaseConfig, creds seal.Credentials, shared bool) (*Database, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cat, err := m.catalog("create database")
	if err != nil {
		return nil, err
	}
	if err := m.validateDataDir(cfg.TenantID, cfg.Name, cfg.DataDir); err != nil {
		return nil, err
	}
	if cfg.Barrier != nil && cfg.Barrier.Enabled {
		if err := barrierDataPathError(cfg); err != nil {
			return nil, err
		}
	}
	// Shard IDs are issued by the cluster. A caller naming one could put a
	// database on another database's Raft group, or on a reserved shard such
	// as the system shard; nothing the caller supplies is checked against
	// what other nodes hold, so it is refused rather than validated.
	if cfg.Raft != nil && cfg.Raft.ShardID != 0 {
		return nil, &CallerShardIDError{Name: cfg.Name, ShardID: cfg.Raft.ShardID}
	}

	// Check for duplicates. An entry this node holds is a duplicate only
	// while the catalog still records it: a drop on another node leaves the
	// entry here, and it is released rather than refusing the name forever.
	key := keyFor(cfg.TenantID, cfg.Name)
	if value, loaded := m.databases.Load(key); loaded {
		db := value.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
		obsolete, err := m.revalidate(ctx, key, db)
		if err != nil {
			return nil, err
		}
		if !obsolete {
			return nil, &AlreadyExistsError{Name: cfg.Name}
		}
	}

	// One create or open of a name at a time. Two concurrent creates would
	// each mark their own incarnation live in the store and only one could
	// win the catalog, leaving the loser's marker to refuse every write to
	// the winner; Open takes the same claim, so the catalog insert below
	// can never lose to it either.
	unclaim, claimed := m.tryClaim(key)
	if !claimed {
		return nil, &AlreadyExistsError{Name: cfg.Name}
	}
	defer unclaim()
	if _, loaded := m.databases.Load(key); loaded {
		return nil, &AlreadyExistsError{Name: cfg.Name}
	}

	// A name this node dropped left a tombstone in its store. Without a
	// reclaimer there is no way to replace it with a live incarnation, so
	// the database created here would refuse every write, forever. Refuse
	// the create instead; it succeeds once the data plane is attached.
	if m.reclaimer == nil {
		dropped, err := m.droppedHere(key.tenant, key.name)
		if err != nil {
			return nil, err
		}
		if dropped {
			return nil, &ReclaimerRequiredError{Name: cfg.Name}
		}
	}

	// The new database's incarnation: the value its writes will be guarded
	// by. With no reclaimer there is no store to mark it in, and the
	// database is live while its marker is absent (an empty incarnation);
	// a provisioned database shares the empty incarnation on purpose.
	incarnation := ""
	if m.reclaimer != nil && !shared {
		var err error
		if incarnation, err = NewIncarnation(); err != nil {
			return nil, err
		}
	}

	dbDir := m.dbDir(cfg.TenantID, cfg.Name)
	dataDir := filepath.Join(dbDir, dataSubDir)
	if cfg.DataDir != "" {
		dataDir = cfg.DataDir
	}

	// Everything this call creates on disk, in the catalog and in the
	// barrier manager, so a failure at any later step removes exactly that
	// and nothing else: a directory that existed before the call belongs to
	// someone else.
	undo := &createUndo{m: m, tenantID: cfg.TenantID, name: cfg.Name}
	if err := undo.mkdirAll(dbDir); err != nil {
		return nil, &OpenError{Name: cfg.Name, Err: err}
	}
	if err := undo.mkdirAll(dataDir); err != nil {
		return nil, undo.run(ctx, &OpenError{Name: cfg.Name, Err: err})
	}

	// Default mode for backward compat.
	if cfg.Mode == "" {
		cfg.Mode = ModeEmbedded
	}

	// A raft database's shard ID is issued here, never chosen by the caller
	// (refused above). It is written into the config so it is persisted in
	// the catalog record and survives restarts.
	if cfg.Raft != nil {
		id, err := m.issueShardID(ctx)
		if err != nil {
			return nil, undo.run(ctx, err)
		}
		cfg.Raft.ShardID = id
	}

	// Record it in the catalog. A freshly created database is never already
	// dropping. The record is created only where none exists, so of two
	// nodes creating one name, one is refused here.
	meta := metaFromConfig(cfg, false, incarnation)
	meta.OwnerNode = m.ownerFor(cfg, shared)
	if err := cat.Create(ctx, meta); err != nil {
		// A write that may have committed is settled first: rolling back
		// around a record it left would orphan the name. A record that
		// could not be settled keeps the directories for the drop that
		// removes it.
		if createOutcomeUnknown(err) {
			if serr := undo.settleCatalog(ctx, cat, incarnation, err); serr != nil {
				return nil, serr
			}
		}
		return nil, undo.run(ctx, err)
	}
	undo.catalog, undo.incarnation = cat, incarnation

	// Open storage backend via factory.
	backend, err := m.factory.Create(cfg, dataDir)
	if err != nil {
		return nil, undo.run(ctx, err)
	}
	undo.backends = append(undo.backends, backend)

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
			return nil, undo.run(ctx, err)
		}
		undo.backends = append(undo.backends, barrierMetaBackend)
		storageAdapter := NewBackendStorageAdapter(barrierMetaBackend)
		if err := m.barrierMgr.EnsureBarrier(ctx, cfg.Name, cfg.TenantID, cfg.Barrier, storageAdapter, creds); err != nil {
			return nil, undo.run(ctx, err)
		}
		undo.barrier = true
		encryptor := m.barrierMgr.GetBarrier(cfg.Name, cfg.TenantID)
		if encryptor == nil {
			return nil, undo.run(ctx, &BarrierEncryptorUnavailableError{Name: cfg.Name})
		}
		dataBackend = NewBarrierBackend(backend, encryptor)
	}

	db := NewDatabase(cfg, dataBackend, dataDir)
	if barrierMetaBackend != nil {
		db.setBarrierMetaBackend(barrierMetaBackend)
	}

	if cfg.Barrier != nil && cfg.Barrier.Enabled && m.barrierMgr != nil {
		db.SetBarrierEnabled(true)
		// A Shamir barrier holds no key until its holders' custody is
		// initialized: the database is sealed, not open, until then.
		if isShamir(cfg.Barrier) {
			db.SetStatus(StatusSealed)
		}
	}
	db.incarnation = incarnation
	db.ownerNode = meta.OwnerNode

	// Mark the new incarnation live in the store holding the database's
	// data. This is the LAST fallible step before the database becomes
	// resolvable, and it is what makes the name writable: until it lands,
	// the store still holds whatever a previous database of this name left
	// -- its tombstone -- and every write is refused. Marking it earlier
	// would unfence the store for a create that could still fail, leaving a
	// name with no database writable to a previous incarnation's handles.
	// The mark succeeds only over an absent marker or a tombstone: a marker
	// holding another live incarnation is a database of this name created
	// on another node, and this create is refused rather than taking it
	// over.
	if m.reclaimer != nil {
		if err := m.reclaimer.UnfenceDatabaseKeyspace(ctx, cfg.TenantID, cfg.Name, incarnation, ""); err != nil {
			var exists *AlreadyExistsError
			if errors.As(err, &exists) {
				return nil, undo.run(ctx, exists)
			}
			return nil, undo.run(ctx, &KeyspaceFenceError{Name: cfg.Name, TenantID: cfg.TenantID, Err: err})
		}
		if err := m.forgetDrop(key.tenant, key.name); err != nil {
			m.logger.Warn("failed to clear the record of an earlier drop of a re-created database",
				slog.String("name", cfg.Name), slog.Any("error", err))
		}
	}

	// Creates and opens of one name are serialized (see inFlight), so the
	// map cannot already hold it.
	m.databases.Store(key, db)

	m.logger.Info("database created",
		slog.String("name", cfg.Name),
		slog.String("backend", string(cfg.Backend)))

	return db, nil
}

// Open opens tenantID's database name on this node from its catalog record,
// initializing its storage backend. It returns *NotFoundError when the
// catalog holds no such record, and the database itself when it is already
// open here.
func (m *Manager) Open(ctx context.Context, tenantID, name string) (*Database, error) {
	key := keyFor(tenantID, name)
	// Already open?
	if db, ok, err := m.loadOpen(key); ok {
		return db, err
	}
	cat, err := m.catalog("open database")
	if err != nil {
		return nil, err
	}
	meta, err := cat.Get(ctx, tenantID, name)
	if err != nil {
		return nil, err
	}
	return m.openRecord(key, meta)
}

// openRecord opens the database meta records under key, unless this node
// has it open already. Concurrent openers of one key do not both build a
// backend: the loser gets *AlreadyExistsError.
func (m *Manager) openRecord(key dbKey, meta DatabaseMeta) (*Database, error) {
	if refusal, held := m.ownerRefusal(key, meta); held {
		return nil, refusal
	}
	if db, ok, err := m.loadOpen(key); ok {
		return db, err
	}
	unclaim, claimed := m.tryClaim(key)
	if !claimed {
		return nil, &AlreadyExistsError{Name: key.name}
	}
	defer unclaim()
	if db, ok, err := m.loadOpen(key); ok {
		return db, err
	}
	return m.openMeta(key, meta)
}

// loadOpen returns the entry this node holds open for key, and whether it
// holds one. An entry reconcile found obsolete and release could not yet
// remove is refused with *StaleDatabaseError and never returned.
func (m *Manager) loadOpen(key dbKey) (*Database, bool, error) {
	val, ok := m.databases.Load(key)
	if !ok {
		return nil, false, nil
	}
	db := val.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
	if db.obsolete.Load() {
		return nil, true, &StaleDatabaseError{Name: key.name, TenantID: key.tenant}
	}
	return db, true, nil
}

// openMeta opens the database meta records on this node and adds it to the
// open-database map under key. The caller holds key's inFlight claim.
func (m *Manager) openMeta(key dbKey, meta DatabaseMeta) (*Database, error) {
	name := key.name
	dbDir := m.dbDir(key.tenant, name)

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
		SQL:          meta.SQL,
	}
	if meta.Barrier != nil && meta.Barrier.Enabled && !cfg.HasDedicatedStore() {
		return nil, &SharedStoreBarrierUnsupportedError{Name: name, Backend: cfg.Backend}
	}
	if meta.Barrier != nil && meta.Barrier.Enabled && !cfg.BarrierOnDataPath() {
		// A record written before creation refused a barrier off the data
		// path. It still opens -- sealed, like every barrier-enabled
		// database -- because the request resolver refuses every data-plane
		// request to a sealed database. Refusing to open it would drop it
		// from the catalog, and a raft-isolated tenant's database is served
		// from its placement record without a catalog entry at all, so the
		// refusal would unprotect it rather than protect it.
		//
		// A volume- or raft-isolated tenant's record is warned for by
		// service.WarnBarriersOffDataPath at boot: the tenant's isolation is
		// not visible here.
		m.logger.Warn(BarrierOffDataPathWarning,
			slog.String("name", name),
			slog.String("backend", string(cfg.Backend)))
	}
	if err := m.validateDataDir(key.tenant, name, meta.DataDir); err != nil {
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
		if err := m.barrierMgr.RegisterBarrier(name, meta.TenantID, meta.Barrier, storageAdapter); err != nil {
			_ = backend.Close()
			_ = barrierMetaBackend.Close()
			return nil, err
		}
		encryptor := m.barrierMgr.GetBarrier(name, meta.TenantID)
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

	// A node that opens a record with a Drop's fence still set
	// (see Drop step 0) reopens the database fenced, in ADDITION to
	// whichever status above applies: a crash between the fence and the
	// catalog record's removal must not silently drop the fence and let a
	// write land in a keyspace whose reclamation may be incomplete
	// (go-qrdb#482). This is independent of Status precisely so the owning
	// tenant can retry the drop to completion afterward -- Drop's step 2
	// needs Status to still say whether there is an open backend to close.
	if meta.Dropping {
		db.SetDropping(true)
	}
	db.incarnation = meta.Incarnation
	db.ownerNode = meta.OwnerNode

	if actual, loaded := m.databases.LoadOrStore(key, db); loaded {
		// Another goroutine opened it concurrently.
		_ = db.Backend().Close()
		if barrierMetaBackend != nil {
			_ = barrierMetaBackend.Close()
		}
		return actual.(*Database), nil //nolint:errcheck // type assertion on a value this container only ever holds
	}

	m.logger.Info("database opened",
		slog.String("name", name),
		slog.String("backend", string(meta.Backend)))

	return db, nil
}

// loadDatabase returns the open database named name (see lookupName),
// refusing with DroppingError while Manager.Drop is fencing it (see
// Database.Dropping) even though its catalog record is not removed until
// the drop completes. It is the resolution seam every write and read path uses --
// directly via Get, or via one of the Update* methods below, which resolve
// against m.databases the same way Get does rather than calling Get itself
// -- so refusing here is what stops a caller from reaching, or mutating the
// metadata of, a database whose keyspace is already being, or about to be,
// reclaimed (go-qrdb#482).
func (m *Manager) loadDatabase(name string) (*Database, error) {
	_, db, err := m.lookupName(name)
	if err != nil {
		return nil, err
	}
	if db.Dropping() {
		return nil, &DroppingError{Name: name, TenantID: db.TenantID()}
	}
	return db, nil
}

// lookupName returns the one database named name open on this node,
// whichever tenant owns it, and its key. A name more than one tenant holds
// here is refused with *AmbiguousNameError: a caller that has the tenant
// resolves the pair instead (GetOwned).
func (m *Manager) lookupName(name string) (dbKey, *Database, error) {
	var (
		key   dbKey
		db    *Database
		found int
	)
	m.databases.Range(func(k, value any) bool {
		dk, _ := k.(dbKey)
		if dk.name != name {
			return true
		}
		found++
		key, db = dk, value.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
		return found < 2
	})
	switch found {
	case 0:
		return dbKey{}, nil, &NotFoundError{Name: name}
	case 1:
		return key, db, nil
	default:
		return dbKey{}, nil, &AmbiguousNameError{Name: name}
	}
}

// GetIgnoringFence returns an already-open database by name (see
// lookupName) WITHOUT the
// drop-fence refusal Get applies. It is for the drop itself -- the one
// caller that must reach a database its own fence refuses -- and nothing
// else: every data-plane resolution goes through Get.
func (m *Manager) GetIgnoringFence(name string) (*Database, error) {
	_, db, err := m.lookupName(name)
	return db, err
}

// Get returns an already-open database by name. See loadDatabase for the
// drop-fence refusal.
func (m *Manager) Get(name string) (*Database, error) {
	return m.loadDatabase(name)
}

// Drop fences the database against new operations, then closes and removes
// it: the data it owns in the shared store, its barrier, its data directory
// and finally its catalog entry.
//
// Ordering: FENCE FIRST, DEPENDENTS NEXT, THE RECORD THAT MAKES THE DATABASE
// RESOLVABLE LAST. The dependents-before-record half is the ordering
// pkg/sql/provider's Database.DropTable and CollectionService.DropCollection
// already use, and reversing it is exactly the defect go-qrdb#412 reports:
// until that change the catalog entry went first, so a shared-store
// database's keys were orphaned the instant the entry was gone. The fence
// (step 0) is go-qrdb#482: reclaiming first closed the #412 window only for
// a drop that runs alone. A write that resolved the database — still open,
// still in the catalog — after the keyspace scan but before the catalog
// entry was removed survived the reclamation, orphaned under a name the
// catalog no longer named. The fence has two halves, both set BEFORE the
// reclamation that follows can run:
//
//   - the LOCAL fence (Database.Dropping, persisted in the catalog record) makes the
//     database unresolvable to Manager.Get, the seam every new handle is
//     resolved through;
//   - the STORE fence (KeyspaceReclaimer.FenceDatabaseKeyspace) writes the
//     dropped tombstone into the marker key the Raft group holding the
//     database's data keeps for it. Every write to the database carries a
//     guard that the marker still names the database's own incarnation
//     (absent, for the empty incarnation and a dedicated store), evaluated
//     by the state machine in the same command as the write, so a write
//     resolved before the drop -- a handle already in hand, a proposal
//     already in the log -- is refused at apply time if it did not apply
//     before the fence. The local fence alone cannot do that: it is checked
//     before a write is proposed, and a write can wait in the log after the
//     check.
//
// The fence (Database.Dropping) is deliberately independent of Status: this
// step never calls db.SetStatus. That is what lets step 2 keep asking
// db.Status() the exact question it always has -- was there an open backend
// to release -- undisturbed by the fence, and what makes a fenced database
// that survives a crash and a restart (Open restores Dropping from the
// catalog record onto a freshly-StatusOpen database) still resolvable to a fresh
// Drop retry rather than stuck because Status reads as if a drop already
// closed it.
//
// The worst state an interrupted Drop can leave is therefore a FENCED, EMPTY
// database — unresolvable to ordinary operations, with some or all of its
// data already reclaimed. Re-issuing the drop completes it, because every
// step is idempotent and the name stays occupied until the last one. No
// state exists in which a later CREATE silently adopts a previous
// database's data, and no write can land in one being torn down.
//
// A fencing or reclamation failure fails the whole drop rather than being
// skipped: reporting success over data that is still there is the defect
// itself. It also clears both fences, so the surviving catalog entry is
// resolvable and writable again and the retry an operator issues is not
// permanently refused.
//
// The default database cannot be dropped (CheckDroppable).
func (m *Manager) Drop(ctx context.Context, name string) error {
	if err := CheckDroppable(name); err != nil {
		return err
	}
	key, db, err := m.lookupName(name)
	if err != nil {
		return err
	}
	return m.drop(ctx, key, db)
}

// drop drops the open database db, held under key. See Drop.
func (m *Manager) drop(ctx context.Context, key dbKey, db *Database) error {
	name := key.name
	cat, err := m.catalog("drop database")
	if err != nil {
		return err
	}
	if _, inFlight := m.dropping.LoadOrStore(key, struct{}{}); inFlight {
		return &DropInProgressError{Name: name}
	}
	defer m.dropping.Delete(key)

	// 0. Fence: refuse every new caller before the data they would touch
	//    starts being reclaimed (go-qrdb#482). The IN-MEMORY flag is set
	//    FIRST, so loadDatabase -- the seam every Update*/Get resolution
	//    goes through -- refuses new callers before the durable write
	//    commits. A configuration update that already passed that check
	//    cannot clear the durable fence: rewriteMeta applies its one field
	//    to the record as the catalog holds it and refuses a record that is
	//    dropping, so it never writes a Dropping value of its own, from this
	//    node or another (go-qrdb#482 round 3, finding 10). A crash between
	//    the two writes still resumes fenced (Open restores Dropping from the
	//    catalog record), and a durable-write failure rolls the in-memory
	//    flag back rather than leaving the two disagreeing. The durable
	//    write carries this drop's claim, won in the catalog by one swap:
	//    a retry of an already-fenced database (after a crash or a failed
	//    reclamation, on any node) takes the claim over, and only the
	//    holder deletes the record in step 4 or rolls the fence back, so a
	//    drop that was taken over cannot remove the record of a database
	//    re-created under the name since (go-qrdb#893).
	claim, err := NewIncarnation()
	if err != nil {
		return &DropError{Name: name, Err: err}
	}
	db.SetDropping(true)
	if err := m.claimDrop(ctx, db, claim); err != nil {
		db.SetDropping(false)
		return &LocalFenceError{Name: name, Err: err}
	}

	// 1. The data the database owns in a store it shares with others. The
	//    store fence goes in first, in the same Raft group as that data, so
	//    no write the fence did not precede can land in the keyspace being
	//    reclaimed -- see the doc comment. Nothing below this line can reach
	//    the data, and everything below this line makes it unreachable.
	//
	//    The incarnation is the drop's fencing token where the data is
	//    deleted: the fence swaps the marker from this incarnation only, and
	//    every reclamation delete is refused by the state machine, in the
	//    same command, once the marker holds a successor's incarnation. A
	//    drop another drop took over and finished, resumed after the name was
	//    created again, gets *DropSupersededError and deletes nothing of the
	//    successor's (go-qrdb#893).
	if m.reclaimer != nil {
		if err := m.reclaimer.FenceDatabaseKeyspace(ctx, db.TenantID(), name, db.Incarnation(), claim); err != nil {
			rolledBack := m.rollbackDropFence(ctx, name, db, claim)
			return &KeyspaceFenceError{Name: name, TenantID: db.TenantID(), Err: err, RolledBack: rolledBack}
		}
		// The store now holds this name's tombstone; remember that here,
		// where a later create of the name with no reclaimer consults it.
		if err := m.recordDrop(key.tenant, key.name); err != nil {
			rolledBack := m.rollbackDropFence(ctx, name, db, claim)
			return &KeyspaceFenceError{Name: name, TenantID: db.TenantID(), Err: err, RolledBack: rolledBack}
		}
		if err := m.reclaimer.ReclaimDatabaseKeyspace(ctx, db.TenantID(), name, db.Incarnation(), claim); err != nil {
			rolledBack := m.rollbackDropFence(ctx, name, db, claim)
			return &KeyspaceReclaimError{Name: name, TenantID: db.TenantID(), Err: err, RolledBack: rolledBack}
		}
	}

	// 1b. The steps below act on this node by name, where no Raft guard
	//     reaches. They run only while this node still holds the database
	//     being dropped and the catalog still holds its incarnation: a
	//     record that is gone, or of another incarnation, is a drop another
	//     drop finished, and the name's barrier and directory may already be
	//     a successor's. They hold the name's claim, the one a create, an
	//     open and a lookup's release take, so neither check can go stale
	//     before the teardown it guards: no release of this entry and no
	//     open of a successor runs on this node until the drop is done
	//     (go-qrdb#893).
	unclaim, err := m.awaitClaim(ctx, key)
	if err != nil {
		return &DropError{Name: name, Err: err}
	}
	defer unclaim()
	if current, ok := m.databases.Load(key); !ok || current != db {
		return &DropSupersededError{Name: name, TenantID: db.TenantID()}
	}
	if err := m.requireIncarnation(ctx, cat, db); err != nil {
		return err
	}

	// 2. Close the database's own backend, so a dedicated store releases its
	//    files before the directory holding them is removed. Status is
	//    exactly what it was before step 0 -- the fence never touches it --
	//    so a database that was already closed (a retry of a drop that got
	//    this far before failing or crashing) is correctly left alone rather
	//    than closed twice.
	if db.Status() == StatusOpen || db.Status() == StatusSealed {
		if err := db.Close(); err != nil {
			return &DropError{Name: name, Err: err}
		}
	}

	// 3. Remove barrier if it was enabled.
	if db.BarrierEnabled() && m.barrierMgr != nil {
		_ = m.barrierMgr.RemoveBarrier(name, db.TenantID())
	}

	// 4. A dedicated store's data, then the durable catalog record. The
	//    record goes last: while it remains, a retry finds the database
	//    fenced and completes the drop. The delete swaps out exactly this
	//    incarnation's record under this drop's claim, so a drop another
	//    drop took over removes nothing (go-qrdb#893). A record of another
	//    incarnation is a successor created since the check in step 1b:
	//    what this drop removed above was still its own, because the
	//    successor cannot open here while the drop holds the name's claim,
	//    and its entry goes with it. A store whose data lives outside the
	//    directory (an objstore database's objects) is reclaimed first: a
	//    retry after the close above still reaches it, and a reclaim that
	//    fails fails the drop, so the record stays for that retry.
	if r, ok := db.rawDataBackend().(storeReclaimer); ok {
		if err := r.ReclaimStore(); err != nil {
			return &DropError{Name: name, Err: err}
		}
	}
	if err := os.RemoveAll(m.dbDir(key.tenant, name)); err != nil {
		return &DropError{Name: name, Err: err}
	}
	if err := cat.Delete(ctx, db.TenantID(), name, db.Incarnation(), claim); err != nil {
		var notFound *NotFoundError
		if !errors.As(err, &notFound) {
			return &DropError{Name: name, Err: err}
		}
		m.databases.CompareAndDelete(key, db)
		return &DropSupersededError{Name: name, TenantID: db.TenantID()}
	}

	// 5. The in-memory mirror of the record removed in step 4. Last, and
	//    adjacent to it, so the window in which the two disagree is as narrow
	//    as it can be made without a persisted marker (go-qrdb#425).
	m.databases.CompareAndDelete(key, db)

	m.logger.Info("database dropped", slog.String("name", name))
	return nil
}

// rollbackDropFence clears the fences a failed drop set, so the database it
// failed to drop is resolvable and writable again.
//
// The store fence is ALWAYS cleared, whether or not writing it reported
// success: a proposal can commit and still return an error (a timeout, a
// lost leader), so "the fence write failed" does not mean "there is no
// fence". Clearing is idempotent, and it runs under DropRollbackContext
// because the drop most often failed on the request's own cancellation.
//
// The local fence is cleared only once the store fence is: a database the
// store still refuses writes to must not look live. If the store cannot be
// reached the database stays fenced -- refused with DroppingError, honestly
// -- until the drop is retried. Neither failure is returned: the drop
// already failed and that is what the caller reports. The result says
// whether the drop was rolled back completely, which is what decides whether
// a caller may roll back fences of its own (DropRolledBack).
//
// Only the drop holding the record's claim rolls back: a drop whose claim
// another drop has since taken over (a retry, on any node) leaves both
// fences to it. The store enforces it: the unfence clears only a fence
// stamped with this drop's claim, so a takeover that lands between the
// claim check and the unfence -- on another node, where no lock here
// reaches -- keeps its fence, and one that lands after the unfence fences
// again before it reclaims.
func (m *Manager) rollbackDropFence(ctx context.Context, name string, db *Database, claim string) bool {
	rollbackCtx, cancel := DropRollbackContext(ctx)
	defer cancel()
	cat, err := m.catalog("roll back drop fence")
	if err != nil {
		return false
	}
	if meta, err := cat.Get(rollbackCtx, db.TenantID(), name); err != nil ||
		meta.Incarnation != db.Incarnation() || meta.DropClaim != claim {
		m.logger.Warn("not clearing the drop fence after a failed drop: another drop holds it, or the catalog could not be read",
			slog.String("name", name),
			slog.Any("error", err))
		return false
	}
	// The read above is advisory: another drop can take the claim over
	// before the unfence lands. The unfence itself is conditional on this
	// drop's claim, in the store's state machine: a takeover re-stamps the
	// fence with its own claim before it reclaims anything, so an unfence
	// ordered after it is refused, and a takeover ordered after the unfence
	// fences again itself (go-qrdb#893).
	if err := m.reclaimer.UnfenceDatabaseKeyspace(rollbackCtx, db.TenantID(), name, db.Incarnation(), claim); err != nil {
		m.logger.Warn("failed to clear the store drop fence after a failed drop; the database stays fenced until the drop is retried",
			slog.String("name", name),
			slog.Any("error", err))
		return false
	}
	if err := m.releaseDropClaim(rollbackCtx, db, claim); err != nil {
		var taken *DropInProgressError
		if errors.As(err, &taken) {
			// Another drop claimed the record after the unfence: it fences
			// the store itself before it reclaims anything.
			return false
		}
		m.logger.Warn("failed to clear the drop fence after a failed drop",
			slog.String("name", name),
			slog.Any("error", err))
	}
	db.SetDropping(false)
	return true
}

// UpdateProtocol updates the wire protocol configuration for a database.
// It updates both the in-memory config and the catalog record.
func (m *Manager) UpdateProtocol(name string, protocol ProtocolConfig) error {
	db, err := m.loadDatabase(name)
	if err != nil {
		return err
	}
	return m.updateProtocol(db, protocol)
}

// updateProtocol is UpdateProtocol on a resolved database.
func (m *Manager) updateProtocol(db *Database, protocol ProtocolConfig) error {
	db.UpdateProtocol(protocol)

	if err := m.rewriteMeta(db, func(meta *DatabaseMeta) { meta.Protocol = protocol }); err != nil {
		return err
	}

	m.logger.Info("database protocol config updated",
		slog.String("name", db.Name()),
		slog.Bool("mysql", protocol.MySQL),
		slog.Bool("postgresql", protocol.PostgreSQL))
	return nil
}

// UpdateEtcd updates the etcd wire protocol configuration for a database.
// It updates both the in-memory config and the catalog record.
func (m *Manager) UpdateEtcd(name string, etcd *EtcdConfig) error {
	db, err := m.loadDatabase(name)
	if err != nil {
		return err
	}
	return m.updateEtcd(db, etcd)
}

// updateEtcd is UpdateEtcd on a resolved database.
func (m *Manager) updateEtcd(db *Database, etcd *EtcdConfig) error {
	if etcd != nil {
		if err := etcd.Validate(); err != nil {
			return err
		}
	}
	db.UpdateEtcd(etcd)

	return m.rewriteMeta(db, func(meta *DatabaseMeta) { meta.Protocol.Etcd = etcd })
}

// UpdateRaft updates the Raft consensus configuration for a database.
// It updates both the in-memory config and the catalog record.
//
// The shard ID is not part of what an update sets: it is the database's
// identity, issued at create. An update that leaves it unset keeps the issued
// one, and an update naming a different one is refused with
// *CallerShardIDError.
func (m *Manager) UpdateRaft(name string, raft *DatabaseRaftConfig) error {
	db, err := m.loadDatabase(name)
	if err != nil {
		return err
	}
	return m.updateRaft(db, raft)
}

// updateRaft is UpdateRaft on a resolved database.
func (m *Manager) updateRaft(db *Database, raft *DatabaseRaftConfig) error {
	name := db.Name()
	if raft == nil {
		// A raft-backend database's Raft configuration carries its issued
		// shard ID, its identity: no update clears it.
		if db.BackendType() == BackendRaft {
			return &RaftConfigRequiredError{Name: name}
		}
		db.updateRaft(nil)
		return m.rewriteMeta(db, func(meta *DatabaseMeta) { meta.Raft = nil })
	}
	issued := db.ShardID()
	if raft.ShardID != 0 && raft.ShardID != issued {
		return &CallerShardIDError{Name: name, ShardID: raft.ShardID}
	}
	kept := *raft
	kept.ShardID = issued
	if err := keepStoredSecrets(&kept, db.Config().Raft); err != nil {
		return err
	}
	if err := kept.Validate(); err != nil {
		return err
	}
	db.updateRaft(&kept)

	return m.rewriteMeta(db, func(meta *DatabaseMeta) { meta.Raft = &kept })
}

// keepStoredSecrets fills each discovery secret an update left empty from the
// stored configuration. Secrets are write-only -- no response returns them --
// so a client that reads a database's Raft settings and writes them back
// cannot send them; an omitted secret therefore means "unchanged", not
// "cleared". Sending a secret rotates it. A discovery block the update leaves
// out is removed, secret and all: that is how a secret is removed. update's
// blocks are copied before they are filled, so the caller's request is never
// written to.
//
// A kept secret stays bound to what it authenticates: the static peer set
// (staticDestination), the multicast group and interface, the token endpoint.
// Destinations compare literally. An update that
// changes that destination without sending the secret is refused with
// *DiscoverySecretRebindError -- keeping it would hand a secret the caller
// never knew to a destination the caller chose.
func keepStoredSecrets(update, stored *DatabaseRaftConfig) error {
	if stored == nil {
		return nil
	}
	if update.Static != nil && update.Static.SharedSecret == "" && stored.Static != nil && stored.Static.SharedSecret != "" {
		if !sameAddresses(staticDestination(update), staticDestination(stored)) {
			return &DiscoverySecretRebindError{Method: string(DiscoveryStatic), Field: "peers"}
		}
		s := *update.Static
		s.SharedSecret = stored.Static.SharedSecret
		update.Static = &s
	}
	if update.Multicast != nil && update.Multicast.SharedSecret == "" && stored.Multicast != nil && stored.Multicast.SharedSecret != "" {
		if update.Multicast.MulticastGroup != stored.Multicast.MulticastGroup {
			return &DiscoverySecretRebindError{Method: string(DiscoveryMulticast), Field: "multicastGroup"}
		}
		if update.Multicast.Interface != stored.Multicast.Interface {
			return &DiscoverySecretRebindError{Method: string(DiscoveryMulticast), Field: "interface"}
		}
		mc := *update.Multicast
		mc.SharedSecret = stored.Multicast.SharedSecret
		update.Multicast = &mc
	}
	if update.Token != nil && update.Token.Token == "" && stored.Token != nil && stored.Token.Token != "" {
		if update.Token.Endpoint != stored.Token.Endpoint {
			return &DiscoverySecretRebindError{Method: string(DiscoveryToken), Field: "endpoint"}
		}
		tk := *update.Token
		tk.Token = stored.Token.Token
		update.Token = &tk
	}
	return nil
}

// staticDestination returns the addresses the static discovery secret is sent
// to: the static block's peers and, when discovery is static or unset (which
// Validate reads as static), the top-level peers too.
func staticDestination(r *DatabaseRaftConfig) map[string]struct{} {
	dest := make(map[string]struct{}, len(r.Static.Peers)+len(r.Peers))
	for _, p := range r.Static.Peers {
		dest[p] = struct{}{}
	}
	if r.Discovery == DiscoveryStatic || r.Discovery == "" {
		for _, p := range r.Peers {
			dest[p] = struct{}{}
		}
	}
	return dest
}

// sameAddresses reports whether two address sets are equal. Addresses compare
// literally: no name is resolved and no form is normalised, so the same host
// written two ways is two destinations.
func sameAddresses(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for p := range a {
		if _, ok := b[p]; !ok {
			return false
		}
	}
	return true
}

// UpdateReplicasForTenant updates the replication factor recorded for the
// context tenant's database name, resolved by tenant and name: two tenants
// may hold the same name, and a name alone is ambiguous. It
// updates both the in-memory config and the catalog record so
// DatabaseInfo.Replicas (reported by Get/GetForTenant/List) and the
// restart-time manager-derived fallback (managerRaftInfoSource) stay
// consistent with the replicated placement table after
// DatabaseService.ScaleForTenant adds or removes voting replicas. It does not
// itself change cluster membership or placement -- callers update those
// first and call UpdateReplicasForTenant last, once the new replica count is durable.
func (m *Manager) UpdateReplicasForTenant(ctx context.Context, name string, replicas int) error {
	db, err := m.GetOwned(name, tenant.FromContext(ctx))
	if err != nil {
		return err
	}
	db.updateReplicas(replicas)

	return m.rewriteMeta(db, func(meta *DatabaseMeta) { meta.Replicas = replicas })
}

// UpdateBarrier updates the encryption barrier configuration for a database.
// It updates both the in-memory config and the catalog record.
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
	db, err := m.loadDatabase(name)
	if err != nil {
		return err
	}
	return m.updateBarrier(db, barrier)
}

// updateBarrier is UpdateBarrier on a resolved database.
func (m *Manager) updateBarrier(db *Database, barrier *DatabaseBarrierConfig) error {
	name := db.Name()
	wantEnabled := barrier != nil && barrier.Enabled
	activeNow := db.BarrierEnabled()

	if wantEnabled {
		cfg := db.Config()
		if err := barrierDataPathError(cfg); err != nil {
			return err
		}
		if !m.barrierModeSupported(barrier.Mode) {
			return &UnsupportedBarrierStrategyError{Mode: barrier.Mode}
		}
		if !activeNow {
			return &BarrierMigrationNotSupportedError{Name: name, Direction: "enable"}
		}
		// Switching an active barrier's strategy, or a Shamir barrier's
		// M-of-N, re-keys it; that is a quorum rekey, never a configuration
		// write. Only bookkeeping that leaves both unchanged is performed.
		if barrierMode(barrier) != barrierMode(cfg.Barrier) {
			return &BarrierMigrationNotSupportedError{Name: name, Direction: "change the strategy of"}
		}
		if isShamir(barrier) && (barrier.SharesRequired != cfg.Barrier.SharesRequired || barrier.SharesTotal != cfg.Barrier.SharesTotal) {
			return &BarrierMigrationNotSupportedError{Name: name, Direction: "change the M-of-N of"}
		}
	} else if activeNow {
		return &BarrierMigrationNotSupportedError{Name: name, Direction: "disable"}
	}

	db.updateBarrier(barrier)

	return m.rewriteMeta(db, func(meta *DatabaseMeta) { meta.Barrier = barrier })
}

// barrierMode is cfg's strategy, with the empty mode meaning passphrase.
func barrierMode(cfg *DatabaseBarrierConfig) string {
	if cfg == nil || cfg.Mode == "" {
		return BarrierModePassphrase
	}
	return cfg.Mode
}

// RecordShamirScheme records the M-of-N a completed rekey left db's Shamir
// barrier at, so the database reopens with the scheme its holders hold. db is
// the database the caller resolved and authorized, never looked up again by
// name: two tenants may each hold a database of that name. Its record is
// written only while it is db's own incarnation, so a database dropped since
// gets *NotFoundError.
func (m *Manager) RecordShamirScheme(db *Database, threshold, total int) error {
	cfg := db.Config()
	if !isShamir(cfg.Barrier) {
		return &NotShamirBarrierError{Name: db.Name()}
	}
	next := *cfg.Barrier
	next.SharesRequired = threshold
	next.SharesTotal = total
	db.updateBarrier(&next)
	return m.rewriteMeta(db, func(meta *DatabaseMeta) { meta.Barrier = &next })
}

// UpdateConnection updates the remote connection configuration for a database.
// It updates both the in-memory config and the catalog record.
func (m *Manager) UpdateConnection(name string, conn *DatabaseConnectionConfig) error {
	db, err := m.loadDatabase(name)
	if err != nil {
		return err
	}
	return m.updateConnection(db, conn)
}

// updateConnection is UpdateConnection on a resolved database.
func (m *Manager) updateConnection(db *Database, conn *DatabaseConnectionConfig) error {
	if conn != nil {
		if err := conn.Validate(); err != nil {
			return err
		}
	}
	db.updateConnection(conn)

	return m.rewriteMeta(db, func(meta *DatabaseMeta) { meta.Connection = conn })
}

// UpdateSQL replaces the SQL settings of a database, in memory and in its
// catalog record. A nil sqlCfg restores the defaults.
func (m *Manager) UpdateSQL(name string, sqlCfg *SQLConfig) error {
	db, err := m.loadDatabase(name)
	if err != nil {
		return err
	}
	return m.updateSQL(db, sqlCfg)
}

// updateSQL is UpdateSQL on a resolved database.
func (m *Manager) updateSQL(db *Database, sqlCfg *SQLConfig) error {
	if sqlCfg != nil {
		if err := sqlCfg.Validate(); err != nil {
			return err
		}
	}
	db.updateSQL(sqlCfg)

	if err := m.rewriteMeta(db, func(meta *DatabaseMeta) { meta.SQL = sqlCfg }); err != nil {
		return err
	}
	m.logger.Info("database sql config updated",
		slog.String("name", db.Name()),
		slog.String("concurrency", string(sqlCfg.EffectiveConcurrency())))
	return nil
}

// metaFromConfig builds the DatabaseMeta the catalog records for cfg.
// dropping is threaded through explicitly, rather than always false, so a
// caller that rewrites the record for an unrelated reason (a configuration
// update) while Drop's fence happens to be set does not silently clear it.
func metaFromConfig(cfg DatabaseConfig, dropping bool, incarnation string) DatabaseMeta {
	return DatabaseMeta{
		TenantID:     cfg.TenantID,
		Name:         cfg.Name,
		Backend:      cfg.Backend,
		Protocol:     cfg.Protocol,
		DataDir:      cfg.DataDir,
		Mode:         cfg.Mode,
		StorageCodec: cfg.StorageCodec,
		Replicas:     cfg.Replicas,
		DedicatedWAL: cfg.DedicatedWAL,
		Raft:         cfg.Raft,
		Barrier:      cfg.Barrier,
		Connection:   cfg.Connection,
		SQL:          cfg.SQL,
		Dropping:     dropping,
		Incarnation:  incarnation,
	}
}

// rewriteMeta writes a configuration change to db's catalog record: set
// applies the change to the record as the catalog holds it, so a field set
// does not touch keeps its recorded value, whichever node wrote it, and the
// drop fence is never written from db's copy of it. A record that is
// dropping refuses the change with *DroppingError and fences db here too.
func (m *Manager) rewriteMeta(db *Database, set func(*DatabaseMeta)) error {
	ctx, cancel := context.WithTimeout(context.Background(), catalogTimeout)
	defer cancel()
	return m.updateMeta(ctx, db, func(meta *DatabaseMeta) error {
		if meta.Dropping {
			db.SetDropping(true)
			return &DroppingError{Name: db.Name(), TenantID: db.TenantID()}
		}
		set(meta)
		return nil
	})
}

// claimDrop persists the drop fence to db's catalog record with claim as its
// owner, which is what lets a restart mid-drop (see Open) tell a fenced
// database apart from an ordinary open one across a crash. The claim is won
// in the catalog: the swap takes over a fence an earlier drop left (a retry,
// on this node or another, after a crash or a failed reclamation), and from
// then on only this drop deletes the record or clears the fence. It does not touch db's in-memory
// status -- Drop sets that itself, separately, so a failed reclamation can
// restore the exact prior status (open or sealed) instead of always
// falling back to StatusOpen.
// tryClaim takes key's claim on this node's resources for the name -- its
// entry, barrier and directory -- if no create, open, release or drop
// teardown holds it, and returns the function that releases it.
func (m *Manager) tryClaim(key dbKey) (func(), bool) {
	done := make(chan struct{})
	if _, held := m.inFlight.LoadOrStore(key, done); held {
		return nil, false
	}
	return func() {
		m.inFlight.CompareAndDelete(key, done)
		close(done)
	}, true
}

// awaitClaim takes key's claim, waiting for the holder to release it. A
// drop waits rather than failing as a create or open does: the claims it
// waits on are a create's, an open's or a release's, all bounded by their
// own work, and a drop refused for a concurrent lookup would leave the
// database fenced for no reason the caller could act on.
func (m *Manager) awaitClaim(ctx context.Context, key dbKey) (func(), error) {
	for {
		if unclaim, ok := m.tryClaim(key); ok {
			return unclaim, nil
		}
		held, ok := m.inFlight.Load(key)
		if !ok {
			continue
		}
		select {
		case <-held.(chan struct{}): //nolint:errcheck // type assertion on a value this container only ever holds
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (m *Manager) claimDrop(ctx context.Context, db *Database, claim string) error {
	return m.updateMeta(ctx, db, func(meta *DatabaseMeta) error {
		meta.Dropping = true
		meta.DropClaim = claim
		return nil
	})
}

// releaseDropClaim clears the durable fence claim set, in one swap that
// fails with *DropInProgressError when another drop has taken the claim
// over since: the fence is then that drop's, and only it may clear it.
func (m *Manager) releaseDropClaim(ctx context.Context, db *Database, claim string) error {
	return m.updateMeta(ctx, db, func(meta *DatabaseMeta) error {
		if meta.DropClaim != claim {
			return &DropInProgressError{Name: db.Name()}
		}
		meta.Dropping = false
		meta.DropClaim = ""
		return nil
	})
}

// requireIncarnation returns *DropSupersededError unless the catalog record
// of db's name is still of db's incarnation. A drop that another drop took
// over keeps the record of its own incarnation while the other runs; once
// that drop has removed the record -- and the name may have been created
// again -- the database this drop holds is gone, and what the name now
// resolves to on this node is not its to remove.
func (m *Manager) requireIncarnation(ctx context.Context, cat Catalog, db *Database) error {
	meta, err := cat.Get(ctx, db.TenantID(), db.Name())
	var notFound *NotFoundError
	if errors.As(err, &notFound) || (err == nil && meta.Incarnation != db.Incarnation()) {
		return &DropSupersededError{Name: db.Name(), TenantID: db.TenantID()}
	}
	if err != nil {
		return &DropError{Name: db.Name(), Err: err}
	}
	return nil
}

// updateMeta applies mutate to db's catalog record, only while the record
// is of db's incarnation (Catalog.Update): a handle another node's drop left
// stale gets *NotFoundError, and neither resurrects the dropped database nor
// overwrites a database since created under its name.
func (m *Manager) updateMeta(ctx context.Context, db *Database, mutate func(*DatabaseMeta) error) error {
	cat, err := m.catalog("update database")
	if err != nil {
		return err
	}
	return cat.Update(ctx, db.TenantID(), db.Name(), db.Incarnation(), mutate)
}

// List returns information about all open databases, sorted by name.
func (m *Manager) List() []DatabaseInfo {
	var infos []DatabaseInfo
	m.databases.Range(func(_, value any) bool {
		db := value.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
		infos = append(infos, db.Info())
		return true
	})
	sortInfos(infos)
	return infos
}

// sortInfos orders infos by name, then by tenant: the order List and
// ListForTenant report in.
func sortInfos(infos []DatabaseInfo) {
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].Name != infos[j].Name {
			return infos[i].Name < infos[j].Name
		}
		return infos[i].TenantID < infos[j].TenantID
	})
}

// SealBarrierDatabases is the database half of a whole-node seal: it seals
// every database barrier on this node, wiping each root key and DEK
// (DatabaseBarrierManager.SealAll), then marks every open database with
// barrier encryption sealed. The mark is what refuses a database whose store
// does not refuse by itself, and it holds until the database's owner -- or,
// for a Shamir barrier, its holder quorum -- unseals it: no database barrier
// is system-managed today, so the node's own unseal reopens none. Every
// database is marked even when a barrier fails to seal; the first error is
// returned.
func (m *Manager) SealBarrierDatabases() error {
	var err error
	if bm := m.BarrierManager(); bm != nil {
		err = bm.SealAll()
	}
	m.databases.Range(func(_, value any) bool {
		db := value.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
		if db.BarrierEnabled() && db.Status() == StatusOpen {
			db.SetStatus(StatusSealed)
		}
		return true
	})
	return err
}

// Close shuts down all open databases and the backend factory. It first
// seals every database barrier its barrier manager holds, wiping each root
// key and DEK, so no close -- a shutdown, a failed boot, a data-plane
// teardown -- leaves a database key in memory: the manager's close is the one
// owner of that wipe, and every path that drops the manager closes it.
func (m *Manager) Close() error {
	var firstErr error
	if bm := m.BarrierManager(); bm != nil {
		firstErr = bm.SealAll()
	}
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

// OpenAll opens on this node every database the catalog lists in the system
// store: the system tenant's and every shared tenant's. An isolated tenant's
// databases are recorded in its home store and are opened on first access,
// once that home is reachable (GetOwnedIgnoringFence, ListForTenant). It
// issues nothing: shard IDs come from the replicated claimer alone, so there
// is no node-local counter to restore.
func (m *Manager) OpenAll(ctx context.Context) error {
	cat, err := m.catalog("open databases")
	if err != nil {
		return err
	}
	// Records written before the catalog would otherwise vanish without a
	// trace: the catalog does not hold them, so their databases would be
	// neither listed nor resolvable while their data stayed on disk.
	records, err := m.unmigratedRecords()
	if err != nil {
		return err
	}
	if len(records) > 0 {
		return &UnmigratedDatabasesError{Records: records}
	}
	metas, err := cat.List(ctx)
	if err != nil {
		return err
	}
	for _, meta := range metas {
		key := keyFor(meta.TenantID, meta.Name)
		// Another node holds it: that node opens it, and this one forwards.
		if _, held := m.ownerRefusal(key, meta); held {
			continue
		}
		if _, openErr := m.openRecord(key, meta); openErr != nil {
			m.logger.Warn("failed to open database",
				slog.String("tenant", meta.TenantID),
				slog.String("name", meta.Name),
				slog.String("error", openErr.Error()))
			// Continue opening other databases.
			continue
		}
	}
	return nil
}

// unmigratedRecords returns, sorted, the path of every legacyMetaFile in a
// database directory under this node's data directory: the system tenant's
// databases/<name>/ and every tenant's databases/tenants.d/<tenant>/<name>/.
// A data directory that holds no databases yet has none.
func (m *Manager) unmigratedRecords() ([]string, error) {
	root := filepath.Join(m.dataDir, databasesDir)
	dirs, err := subdirs(root)
	if err != nil {
		return nil, err
	}
	var candidates []string
	for _, dir := range dirs {
		if filepath.Base(dir) != tenantDatabasesDir {
			candidates = append(candidates, dir)
			continue
		}
		tenants, err := subdirs(dir)
		if err != nil {
			return nil, err
		}
		for _, tenantDir := range tenants {
			names, err := subdirs(tenantDir)
			if err != nil {
				return nil, err
			}
			candidates = append(candidates, names...)
		}
	}
	var records []string
	for _, dir := range candidates {
		path := filepath.Join(dir, legacyMetaFile)
		_, err := os.Lstat(path)
		switch {
		case err == nil:
			records = append(records, path)
		case !errors.Is(err, os.ErrNotExist):
			return nil, &OpenError{Name: filepath.Base(dir), Err: err}
		}
	}
	sort.Strings(records)
	return records, nil
}

// subdirs returns the paths of the directories directly under dir, none when
// dir does not exist.
func subdirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, &OpenError{Name: filepath.Base(dir), Err: err}
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, filepath.Join(dir, entry.Name()))
		}
	}
	return dirs, nil
}

// DataDir returns the root data directory for all databases.
func (m *Manager) DataDir() string {
	return m.dataDir
}

// barrierDataPathError returns the typed refusal for a per-database barrier
// on cfg, or nil when the barrier would wrap the store the database's data is
// served from. A database with no store of its own is refused with
// SharedStoreBarrierUnsupportedError; a database whose own store is not its
// data path (backend: raft) with BarrierOffDataPathError.
func barrierDataPathError(cfg DatabaseConfig) error {
	if !cfg.HasDedicatedStore() {
		return &SharedStoreBarrierUnsupportedError{Name: cfg.Name, Backend: cfg.Backend}
	}
	if !cfg.BarrierOnDataPath() {
		return &BarrierOffDataPathError{Name: cfg.Name, ServedBy: ServedByRaftGroup}
	}
	return nil
}

// dbDir returns the directory of tenantID's database name: a dedicated
// store's default data and the barrier metadata store. The system tenant's
// databases are directly under the databases directory; every other
// tenant's are under tenantDatabasesDir, in a directory named by the
// SHA-256 of the tenant ID, so a tenant ID is never a path element and two
// tenants' databases of one name never share a directory.
func (m *Manager) dbDir(tenantID, name string) string {
	return tenantScopedPath(filepath.Join(m.dataDir, databasesDir), tenantID, name)
}

// tenantScopedPath is where root keeps the entry for tenantID's database name:
// root/name for the system tenant, root/tenants.d/<sha256(tenant)>/name for any
// other. Two tenants' entries for one name are therefore never the same path,
// and the tenant ID, which may hold any character, never becomes a path
// segment of its own.
func tenantScopedPath(root, tenantID, name string) string {
	if tenantid.IsSystem(tenantID) {
		return filepath.Join(root, name)
	}
	sum := sha256.Sum256([]byte(tenantID))
	return filepath.Join(root, tenantDatabasesDir, hex.EncodeToString(sum[:]), name)
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
func (m *Manager) validateDataDir(tenantID, name, dataDir string) error {
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
	// would fold every other database's barrier metadata and default
	// storage inside this one
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
	// directory (the barrier metadata store): a DataDir reaching into dbDir
	// would let application data collide with barrier bootstrap key
	// material.
	ownDBDir, err := resolveDataDir(m.dbDir(tenantID, name))
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
		other, _ := key.(dbKey)
		if other == keyFor(tenantID, name) {
			return true
		}
		otherName := other.name
		db := value.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds

		if otherDBDir, derr := resolveDataDir(m.dbDir(other.tenant, otherName)); derr == nil {
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

// writeFileAtomic writes data to dir/name through a temp-file, fsync, rename
// sequence, so a crash mid-write never leaves the file truncated and two
// writers settle on "the later rename wins" rather than an interleaved file.
func writeFileAtomic(dir, name string, data []byte) error {
	path := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, "."+name+"-*.tmp")
	if err != nil {
		return &MetaWriteError{Path: path, Err: err}
	}
	tmpPath := tmp.Name()
	// Any return before the rename below must not leave the temp file
	// behind; the rename itself removes tmpPath by moving it onto path, so
	// this is a no-op on the success path (os.Remove on an already-renamed
	// path fails harmlessly and is discarded).
	defer func() { _ = os.Remove(tmpPath) }()

	// os.CreateTemp already creates tmp with mode 0o600 (before umask), the
	// permissions these files need, so there is nothing left to tighten.
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return &MetaWriteError{Path: path, Err: err}
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return &MetaWriteError{Path: path, Err: err}
	}
	if err := tmp.Close(); err != nil {
		return &MetaWriteError{Path: path, Err: err}
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return &MetaWriteError{Path: path, Err: err}
	}

	// Best-effort: the rename above already made the write durable and
	// visible under its final name, so a platform where the directory
	// entry itself cannot be fsynced (or dir cannot even be reopened) still
	// has a fully-written, correctly-named file.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
