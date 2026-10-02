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
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
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
	claimer    atomic.Pointer[claimerRef]
	reclaimer  KeyspaceReclaimer

	// dropping holds the name of every database with a Drop in flight, so
	// two concurrent drops of one database cannot both report success. Drop
	// no longer removes the catalog entry first (see Drop), and that entry
	// was what used to decide the race: DropForTenant releases quota on a
	// successful drop, so two successes would release it twice.
	dropping sync.Map // name (string) -> struct{}

	// inFlight holds the name of every database with a Create or Open in
	// flight. See CreateWithCredentials for why two of them must not
	// interleave.
	inFlight sync.Map // name (string) -> struct{}
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
	if db, err := m.GetIgnoringFence(cfg.Name); err == nil {
		return db, nil
	}
	if _, err := os.Stat(filepath.Join(m.dbDir(cfg.Name), metaFile)); err == nil {
		return m.Open(cfg.Name)
	}
	return m.create(ctx, cfg, seal.Credentials{}, true)
}

// create is CreateWithCredentials and Provision. shared selects the empty,
// node-shared incarnation (see Provision).
func (m *Manager) create(ctx context.Context, cfg DatabaseConfig, creds seal.Credentials, shared bool) (*Database, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := m.validateDataDir(cfg.Name, cfg.DataDir); err != nil {
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

	// Check for duplicates.
	if _, loaded := m.databases.Load(cfg.Name); loaded {
		return nil, &AlreadyExistsError{Name: cfg.Name}
	}

	// One create or open of a name at a time. Two concurrent creates would
	// each mark their own incarnation live in the store and only one could
	// win the catalog, leaving the loser's marker to refuse every write to
	// the winner; Open takes the same claim, so the catalog insert below
	// can never lose to it either.
	if _, inFlight := m.inFlight.LoadOrStore(cfg.Name, struct{}{}); inFlight {
		return nil, &AlreadyExistsError{Name: cfg.Name}
	}
	defer m.inFlight.Delete(cfg.Name)
	if _, loaded := m.databases.Load(cfg.Name); loaded {
		return nil, &AlreadyExistsError{Name: cfg.Name}
	}

	// A name this node dropped left a tombstone in its store. Without a
	// reclaimer there is no way to replace it with a live incarnation, so
	// the database created here would refuse every write, forever. Refuse
	// the create instead; it succeeds once the data plane is attached.
	if m.reclaimer == nil {
		dropped, err := m.droppedHere(cfg.Name)
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

	dbDir := m.dbDir(cfg.Name)
	dataDir := filepath.Join(dbDir, dataSubDir)
	if cfg.DataDir != "" {
		dataDir = cfg.DataDir
	}

	// Everything this call creates on disk and in the barrier manager, so a
	// failure at any later step removes exactly that and nothing else: a
	// directory that existed before the call belongs to someone else.
	undo := &createUndo{m: m, name: cfg.Name}
	if err := undo.mkdirAll(dbDir); err != nil {
		return nil, &OpenError{Name: cfg.Name, Err: err}
	}
	if err := undo.mkdirAll(dataDir); err != nil {
		undo.run()
		return nil, &OpenError{Name: cfg.Name, Err: err}
	}

	// Default mode for backward compat.
	if cfg.Mode == "" {
		cfg.Mode = ModeEmbedded
	}

	// A raft database's shard ID is issued here, never chosen by the caller
	// (refused above). It is written into the config so it is persisted in
	// meta.json and survives restarts.
	if cfg.Raft != nil {
		id, err := m.issueShardID(ctx)
		if err != nil {
			undo.run()
			return nil, err
		}
		cfg.Raft.ShardID = id
	}

	// Write meta.json. A freshly created database is never already dropping.
	meta := metaFromConfig(cfg, false, incarnation)
	if err := m.writeMeta(dbDir, meta); err != nil {
		undo.run()
		return nil, err
	}

	// Open storage backend via factory.
	backend, err := m.factory.Create(cfg, dataDir)
	if err != nil {
		undo.run()
		return nil, err
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
			undo.run()
			return nil, err
		}
		undo.backends = append(undo.backends, barrierMetaBackend)
		storageAdapter := NewBackendStorageAdapter(barrierMetaBackend)
		if err := m.barrierMgr.EnsureBarrier(ctx, cfg.Name, cfg.Barrier, storageAdapter, creds); err != nil {
			undo.run()
			return nil, err
		}
		undo.barrier = true
		encryptor := m.barrierMgr.GetBarrier(cfg.Name)
		if encryptor == nil {
			undo.run()
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
	db.incarnation = incarnation

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
		if err := m.reclaimer.UnfenceDatabaseKeyspace(ctx, cfg.TenantID, cfg.Name, incarnation); err != nil {
			undo.run()
			var exists *AlreadyExistsError
			if errors.As(err, &exists) {
				return nil, exists
			}
			return nil, &KeyspaceFenceError{Name: cfg.Name, TenantID: cfg.TenantID, Err: err}
		}
		if err := m.forgetDrop(cfg.Name); err != nil {
			m.logger.Warn("failed to clear the record of an earlier drop of a re-created database",
				slog.String("name", cfg.Name), slog.Any("error", err))
		}
	}

	// Creates and opens of one name are serialized (see inFlight), so the
	// catalog cannot already hold it.
	m.databases.Store(cfg.Name, db)

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
	if _, inFlight := m.inFlight.LoadOrStore(name, struct{}{}); inFlight {
		return nil, &AlreadyExistsError{Name: name}
	}
	defer m.inFlight.Delete(name)
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

	// A node that restarted with a Drop's fence still set in meta.json
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

// loadDatabase returns the catalog entry for name, refusing with
// DroppingError while Manager.Drop is fencing it (see Database.Dropping)
// even though the catalog entry itself is not removed until the drop
// completes. It is the resolution seam every write and read path uses --
// directly via Get, or via one of the Update* methods below, which resolve
// against m.databases the same way Get does rather than calling Get itself
// -- so refusing here is what stops a caller from reaching, or mutating the
// metadata of, a database whose keyspace is already being, or about to be,
// reclaimed (go-qrdb#482).
func (m *Manager) loadDatabase(name string) (*Database, error) {
	val, ok := m.databases.Load(name)
	if !ok {
		return nil, &NotFoundError{Name: name}
	}
	db := val.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
	if db.Dropping() {
		return nil, &DroppingError{Name: name, TenantID: db.TenantID()}
	}
	return db, nil
}

// GetIgnoringFence returns an already-open database by name WITHOUT the
// drop-fence refusal Get applies. It is for the drop itself -- the one
// caller that must reach a database its own fence refuses -- and nothing
// else: every data-plane resolution goes through Get.
func (m *Manager) GetIgnoringFence(name string) (*Database, error) {
	val, ok := m.databases.Load(name)
	if !ok {
		return nil, &NotFoundError{Name: name}
	}
	return val.(*Database), nil //nolint:errcheck // type assertion on a value this container only ever holds
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
//   - the LOCAL fence (Database.Dropping, persisted in meta.json) makes the
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
// that survives a crash and a restart (Open restores Dropping from
// meta.json onto a freshly-StatusOpen database) still resolvable to a fresh
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
	val, ok := m.databases.Load(name)
	if !ok {
		return &NotFoundError{Name: name}
	}
	db := val.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds

	if _, inFlight := m.dropping.LoadOrStore(name, struct{}{}); inFlight {
		return &DropInProgressError{Name: name}
	}
	defer m.dropping.Delete(name)

	// 0. Fence: refuse every new caller before the data they would touch
	//    starts being reclaimed (go-qrdb#482). The IN-MEMORY flag is set
	//    FIRST, not the durable one: loadDatabase -- the seam every
	//    Update*/Get resolution goes through -- refuses the instant
	//    db.Dropping() reads true, and rewriteMeta always reads
	//    db.Dropping() freshly at call time (never a value cached earlier),
	//    so flipping the in-memory flag first closes the window a
	//    durable-first ordering left open: a concurrent Update* call that
	//    had already passed loadDatabase's check (observing Dropping()
	//    still false) could run rewriteMeta AFTER the durable write below
	//    committed but BEFORE the in-memory flag caught up, read the
	//    stale-at-that-instant false, and silently overwrite meta.json with
	//    the fence cleared -- reopening a mid-drop database as fully
	//    resolvable after a crash during reclamation (go-qrdb#482 round 3,
	//    finding 10). The durable write still follows immediately after, so
	//    a crash between the two still resumes fenced (Open restores
	//    Dropping from meta.json); only the commit order changed, and a
	//    durable-write failure rolls the in-memory flag back rather than
	//    leaving the two disagreeing. Idempotent: re-marking an
	//    already-fenced database (a retry after a crash or a failed
	//    reclamation) is a no-op write of the same value.
	db.SetDropping(true)
	if err := m.setDroppingFlag(name, db, true); err != nil {
		db.SetDropping(false)
		return &LocalFenceError{Name: name, Err: err}
	}

	// 1. The data the database owns in a store it shares with others. The
	//    store fence goes in first, in the same Raft group as that data, so
	//    no write the fence did not precede can land in the keyspace being
	//    reclaimed -- see the doc comment. Nothing below this line can reach
	//    the data, and everything below this line makes it unreachable.
	if m.reclaimer != nil {
		if err := m.reclaimer.FenceDatabaseKeyspace(ctx, db.TenantID(), name); err != nil {
			rolledBack := m.rollbackDropFence(ctx, name, db)
			return &KeyspaceFenceError{Name: name, TenantID: db.TenantID(), Err: err, RolledBack: rolledBack}
		}
		// The store now holds this name's tombstone; remember that here,
		// where a later create of the name with no reclaimer consults it.
		if err := m.recordDrop(name); err != nil {
			rolledBack := m.rollbackDropFence(ctx, name, db)
			return &KeyspaceFenceError{Name: name, TenantID: db.TenantID(), Err: err, RolledBack: rolledBack}
		}
		if err := m.reclaimer.ReclaimDatabaseKeyspace(ctx, db.TenantID(), name); err != nil {
			rolledBack := m.rollbackDropFence(ctx, name, db)
			return &KeyspaceReclaimError{Name: name, TenantID: db.TenantID(), Err: err, RolledBack: rolledBack}
		}
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
func (m *Manager) rollbackDropFence(ctx context.Context, name string, db *Database) bool {
	rollbackCtx, cancel := DropRollbackContext(ctx)
	defer cancel()
	if err := m.reclaimer.UnfenceDatabaseKeyspace(rollbackCtx, db.TenantID(), name, db.Incarnation()); err != nil {
		m.logger.Warn("failed to clear the store drop fence after a failed drop; the database stays fenced until the drop is retried",
			slog.String("name", name),
			slog.Any("error", err))
		return false
	}
	db.SetDropping(false)
	if err := m.setDroppingFlag(name, db, false); err != nil {
		m.logger.Warn("failed to clear the drop fence after a failed drop",
			slog.String("name", name),
			slog.Any("error", err))
	}
	return true
}

// UpdateProtocol updates the wire protocol configuration for a database.
// It updates both the in-memory config and the on-disk meta.json.
func (m *Manager) UpdateProtocol(name string, protocol ProtocolConfig) error {
	db, err := m.loadDatabase(name)
	if err != nil {
		return err
	}
	db.UpdateProtocol(protocol)

	// Rewrite meta.json with the updated protocol config, preserving whatever
	// the drop fence is currently set to (see metaFromConfig).
	if err := m.rewriteMeta(name, db); err != nil {
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
	db, err := m.loadDatabase(name)
	if err != nil {
		return err
	}
	if err := etcdEngineError(name, db.Config().Engine, etcd); err != nil {
		return err
	}
	db.UpdateEtcd(etcd)

	return m.rewriteMeta(name, db)
}

// UpdateRaft updates the Raft consensus configuration for a database.
// It updates both the in-memory config and the on-disk meta.json.
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
	if raft == nil {
		// A raft-backend database's Raft configuration carries its issued
		// shard ID, its identity: no update clears it.
		if db.BackendType() == BackendRaft {
			return &RaftConfigRequiredError{Name: name}
		}
		db.updateRaft(nil)
		return m.rewriteMeta(name, db)
	}
	issued := db.ShardID()
	if raft.ShardID != 0 && raft.ShardID != issued {
		return &CallerShardIDError{Name: name, ShardID: raft.ShardID}
	}
	kept := *raft
	kept.ShardID = issued
	keepStoredSecrets(&kept, db.Config().Raft)
	if err := kept.Validate(); err != nil {
		return err
	}
	db.updateRaft(&kept)

	return m.rewriteMeta(name, db)
}

// keepStoredSecrets fills each discovery secret an update left empty from the
// stored configuration. Secrets are write-only -- no response returns them --
// so a client that reads a database's Raft settings and writes them back
// cannot send them; an omitted secret therefore means "unchanged", not
// "cleared". Sending a secret rotates it. A discovery block the update leaves
// out is removed, secret and all: that is how a secret is removed. update's
// blocks are copied before they are filled, so the caller's request is never
// written to.
func keepStoredSecrets(update, stored *DatabaseRaftConfig) {
	if stored == nil {
		return
	}
	if update.Static != nil && update.Static.SharedSecret == "" && stored.Static != nil {
		s := *update.Static
		s.SharedSecret = stored.Static.SharedSecret
		update.Static = &s
	}
	if update.Multicast != nil && update.Multicast.SharedSecret == "" && stored.Multicast != nil {
		mc := *update.Multicast
		mc.SharedSecret = stored.Multicast.SharedSecret
		update.Multicast = &mc
	}
	if update.Token != nil && update.Token.Token == "" && stored.Token != nil {
		tk := *update.Token
		tk.Token = stored.Token.Token
		update.Token = &tk
	}
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
	db, err := m.loadDatabase(name)
	if err != nil {
		return err
	}
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
	db, err := m.loadDatabase(name)
	if err != nil {
		return err
	}

	wantEnabled := barrier != nil && barrier.Enabled
	activeNow := db.BarrierEnabled()

	if wantEnabled {
		cfg := db.Config()
		if err := barrierDataPathError(cfg); err != nil {
			return err
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
	db, err := m.loadDatabase(name)
	if err != nil {
		return err
	}
	db.updateConnection(conn)

	return m.rewriteMeta(name, db)
}

// UpdateSQL replaces the SQL settings of a database, in memory and in its
// meta.json. A nil sqlCfg restores the defaults.
func (m *Manager) UpdateSQL(name string, sqlCfg *SQLConfig) error {
	if sqlCfg != nil {
		if err := sqlCfg.Validate(); err != nil {
			return err
		}
	}
	db, err := m.loadDatabase(name)
	if err != nil {
		return err
	}
	db.updateSQL(sqlCfg)

	if err := m.rewriteMeta(name, db); err != nil {
		return err
	}
	m.logger.Info("database sql config updated",
		slog.String("name", name),
		slog.String("concurrency", string(sqlCfg.EffectiveConcurrency())))
	return nil
}

// metaFromConfig builds the DatabaseMeta persisted to meta.json from cfg.
// dropping is threaded through explicitly, rather than always false, so a
// caller that rewrites meta.json for an unrelated reason (a configuration
// update) while Drop's fence happens to be set does not silently clear it.
func metaFromConfig(cfg DatabaseConfig, dropping bool, incarnation string) DatabaseMeta {
	return DatabaseMeta{
		TenantID:     cfg.TenantID,
		Name:         cfg.Name,
		Engine:       cfg.Engine,
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

// rewriteMeta rebuilds and rewrites the meta.json for a database from its
// current in-memory configuration, preserving whatever the drop fence
// (DatabaseMeta.Dropping) is currently set to.
func (m *Manager) rewriteMeta(name string, db *Database) error {
	meta := metaFromConfig(db.Config(), db.Dropping(), db.Incarnation())
	return m.writeMeta(m.dbDir(name), meta)
}

// setDroppingFlag persists dropping to name's meta.json, which is what lets
// a restart mid-drop (see Open) tell a fenced database apart from an
// ordinary open one across a crash. It does not touch db's in-memory
// status -- Drop sets that itself, separately, so a failed reclamation can
// restore the exact prior status (open or sealed) instead of always
// falling back to StatusOpen.
func (m *Manager) setDroppingFlag(name string, db *Database, dropping bool) error {
	meta := metaFromConfig(db.Config(), dropping, db.Incarnation())
	return m.writeMeta(m.dbDir(name), meta)
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
// have a valid meta.json. It issues nothing: shard IDs come from the
// replicated claimer alone, so there is no node-local counter to restore.
func (m *Manager) OpenAll() error {
	dbsDir := filepath.Join(m.dataDir, databasesDir)
	entries, err := os.ReadDir(dbsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no databases directory yet
		}
		return &MetaReadError{Path: dbsDir, Err: err}
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if _, openErr := m.Open(name); openErr != nil {
			m.logger.Warn("failed to open database",
				slog.String("name", name),
				slog.String("error", openErr.Error()))
			// Continue opening other databases.
			continue
		}
	}
	return nil
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

// writeMeta writes the DatabaseMeta as JSON to the meta.json file via a
// write-temp-file/fsync/rename sequence, not a direct write to the final
// path (go-qrdb#482 round 3, finding 10): a crash mid-write to path itself
// can leave meta.json truncated or otherwise corrupt, and two concurrent
// writers to the same path (Drop's fence and an Update*'s rewriteMeta,
// racing) can interleave their writes with no serialization between them.
// os.Rename is atomic on the same filesystem -- guaranteed by every POSIX
// platform this ships on -- so every writer instead writes its own
// uniquely-named temp file (no interleaving possible), fsyncs it so the
// data is durable BEFORE the rename makes it visible, and only then renames
// it over path in one atomic step: a reader (readMeta) never observes a
// partial write, and two racing writers settle on "the later rename wins"
// -- the same last-writer-wins semantics this package already documents
// elsewhere for the fence -- rather than a corrupt file. The containing
// directory is also best-effort fsynced after the rename so the directory
// entry itself survives a crash immediately after; a platform where that
// second fsync is unsupported still has a fully-written, correctly-named
// file from the rename itself, so this failure is logged, not returned.
func (m *Manager) writeMeta(dbDir string, meta DatabaseMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return &MetaWriteError{Path: filepath.Join(dbDir, metaFile), Err: err}
	}
	return writeFileAtomic(dbDir, metaFile, data)
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
