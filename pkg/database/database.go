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
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
)

// backendHolder boxes a Backend so it can be swapped atomically via
// atomic.Pointer even though the concrete type behind the Backend interface
// changes (e.g. from a raw StandaloneBackend to a wrapping *BarrierBackend).
// atomic.Value would panic on that type change; boxing behind a stable
// struct type avoids it.
type backendHolder struct {
	backend Backend
}

// Database represents a named, isolated data store with its own engine,
// backend, and storage lifecycle. The config field is stored as an
// atomic pointer to support lock-free runtime updates (e.g., protocol config).
type Database struct {
	config atomic.Pointer[DatabaseConfig]

	// activeBackend is the Backend actually used for data operations. It
	// starts identical to rawBackend and is later replaced (never mutated
	// in place) by Manager with a *BarrierBackend wrapping rawBackend once
	// a per-database encryption barrier becomes available.
	activeBackend atomic.Pointer[backendHolder]

	// rawBackend is the original, unwrapped data-plane backend passed to
	// NewDatabase. It is immutable for the lifetime of the Database and is
	// always the base Manager wraps (or re-wraps) with barrier encryption,
	// so repeated wrapping can never nest one BarrierBackend inside another.
	rawBackend Backend

	// barrierMetaBackend holds this database's dedicated barrier bootstrap
	// key-material store (see Manager.openBarrierMetaBackend), kept
	// entirely separate from rawBackend/activeBackend so the barrier's own
	// root-key bytes never mix into the application data keyspace. nil when
	// no barrier has ever been registered for this database.
	barrierMetaBackend atomic.Pointer[backendHolder]

	status         atomic.Value // stores StatusType
	dataDir        string
	barrierEnabled atomic.Bool

	// statusMu serializes status transitions with the serving signal below,
	// so the signal is closed exactly once per stretch the database is not
	// open and replaced exactly once when it opens again. Status reads stay
	// lock-free.
	statusMu sync.Mutex

	// serving is closed when the database stops serving -- sealed, closed by
	// a drop, or failed -- and replaced by a fresh one when it opens again.
	serving atomic.Pointer[chan struct{}]

	// dropping is set by Manager.Drop (step 0) BEFORE it reclaims this
	// database's keyspace, and is what Manager.Get refuses on -- see
	// Dropping. It is deliberately NOT folded into status: status already
	// answers "is there an open backend to close" (see Close, and Drop
	// step 2, which is unaffected by a drop in progress), and Manager.
	// SetBarrierManager and Close both write status for reasons that have
	// nothing to do with a drop in flight. Overloading status with a third
	// meaning made both of those able to silently clear the fence; keeping
	// the two orthogonal is what makes the fence monotonic except through
	// Manager.Drop's own explicit rollback on a failed reclaim.
	dropping atomic.Bool

	// incarnation distinguishes this database from every other database
	// ever created under its name: the value its drop-fence marker holds
	// while it is live, and the value every write to it is guarded by. Set
	// once, by Manager.Create or Manager.Open, before the database is
	// published; empty for a database created before incarnations existed
	// or with no store to mark. See Incarnation.
	incarnation string
}

// NewDatabase creates a Database from the given config and backend.
// The database starts in StatusOpen state.
func NewDatabase(config DatabaseConfig, backend Backend, dataDir string) *Database {
	db := &Database{
		rawBackend: backend,
		dataDir:    dataDir,
	}
	db.activeBackend.Store(&backendHolder{backend: backend})
	db.config.Store(&config)
	db.status.Store(StatusOpen)
	open := make(chan struct{})
	db.serving.Store(&open)
	return db
}

// Name returns the database name.
func (d *Database) Name() string {
	return d.config.Load().Name
}

// TenantID returns the tenant ID that owns this database.
func (d *Database) TenantID() string {
	return d.config.Load().TenantID
}

// Engine returns the engine type.
func (d *Database) Engine() EngineType {
	return d.config.Load().Engine
}

// ShardID returns the Raft shard ID assigned to this database, or 0 if
// no Raft configuration has been set.
func (d *Database) ShardID() uint64 {
	cfg := d.config.Load()
	if cfg.Raft == nil {
		return 0
	}
	return cfg.Raft.ShardID
}

// BackendType returns the backend type.
func (d *Database) BackendType() BackendType {
	return d.config.Load().Backend
}

// Config returns a copy of the database configuration.
func (d *Database) Config() DatabaseConfig {
	return *d.config.Load()
}

// Backend returns the database backend actually used for data operations.
// This is the barrier-encrypting wrapper once a per-database barrier has
// been registered, or the raw backend otherwise.
func (d *Database) Backend() Backend {
	return d.activeBackend.Load().backend
}

// setActiveBackend replaces the Backend used for data operations without
// altering the immutable raw (unwrapped) backend recorded at construction.
// Manager calls this to layer barrier encryption over the raw backend once
// a barrier becomes available (at Create/Open time, or later via
// SetBarrierManager's re-registration).
func (d *Database) setActiveBackend(b Backend) {
	d.activeBackend.Store(&backendHolder{backend: b})
}

// rawDataBackend returns the original, unwrapped data-plane backend passed
// to NewDatabase. Manager always (re)wraps this -- never the possibly
// already-wrapped Backend() -- so repeated barrier (re)registration can
// never nest one BarrierBackend inside another.
func (d *Database) rawDataBackend() Backend {
	return d.rawBackend
}

// barrierMetaBackendRef returns the database's dedicated barrier bootstrap
// key-material backend, or nil if no barrier has ever been registered.
func (d *Database) barrierMetaBackendRef() Backend {
	h := d.barrierMetaBackend.Load()
	if h == nil {
		return nil
	}
	return h.backend
}

// setBarrierMetaBackend installs b as the database's barrier metadata
// backend, closing and replacing any previously-installed one. This keeps
// repeated barrier-manager (re)registration -- e.g. an in-process cluster
// migration rebuilding the registry -- from leaking the file handles of an
// orphaned prior metadata store.
func (d *Database) setBarrierMetaBackend(b Backend) {
	old := d.barrierMetaBackend.Swap(&backendHolder{backend: b})
	if old != nil && old.backend != nil {
		_ = old.backend.Close()
	}
}

// DataDir returns the resolved storage directory used by this database's
// data-plane backend.
func (d *Database) DataDir() string {
	return d.dataDir
}

// Status returns the current operational status.
func (d *Database) Status() StatusType {
	return d.status.Load().(StatusType) //nolint:errcheck // type assertion on a value this container only ever holds
}

// SetStatus sets the operational status of the database.
func (d *Database) SetStatus(status StatusType) {
	d.setStatus(status)
}

// setStatus records status and moves the serving signal with it: leaving
// StatusOpen closes the current signal, and returning to StatusOpen installs a
// fresh one.
func (d *Database) setStatus(status StatusType) {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()
	d.status.Store(status)
	ch := d.serving.Load()
	closed := isClosed(*ch)
	switch {
	case status == StatusOpen && closed:
		fresh := make(chan struct{})
		d.serving.Store(&fresh)
	case status != StatusOpen && !closed:
		close(*ch)
	}
}

// isClosed reports whether ch is closed. ch is only ever closed, never sent
// on.
func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// Unserved returns a channel that is closed once the database stops serving
// -- it is sealed, closed by a drop, or fails -- or that is already closed if
// it is not serving now. A long-lived reader (a watch or keep-alive stream)
// holds it from the moment it opens and ends when it closes, so it does not
// keep reading a database that was sealed or dropped under it. A database
// that opens again hands later callers a fresh channel.
func (d *Database) Unserved() <-chan struct{} {
	return *d.serving.Load()
}

// Incarnation returns the value that distinguishes this database from every
// other database ever created under its name. Every write to the database is
// guarded by it: its drop-fence marker holds exactly this value while the
// database is live, and a drop, or a new database created under the name,
// replaces it. Empty means the database has no incarnation of its own -- it
// predates incarnations, or it was created with no store to mark -- and is
// live while the marker is absent.
func (d *Database) Incarnation() string {
	return d.incarnation
}

// Dropping reports whether Manager.Drop has fenced this database ahead of
// reclaiming its keyspace (go-qrdb#482). It is independent of Status: a
// dropping database's underlying backend may still be open (Drop closes it
// in a later step) or, after a crash and restart, freshly reopened as
// StatusOpen or StatusSealed by Manager.Open -- Dropping stays true across
// either because it is restored from the database's own persisted metadata
// (DatabaseMeta.Dropping), not derived from Status.
func (d *Database) Dropping() bool {
	return d.dropping.Load()
}

// SetDropping sets or clears the drop fence. Manager.Drop sets it before
// reclaiming the keyspace and clears it again if the reclamation fails, so
// a failed drop is retryable rather than stuck permanently refusing.
func (d *Database) SetDropping(dropping bool) {
	d.dropping.Store(dropping)
}

// BarrierEnabled reports whether this database has barrier encryption enabled.
func (d *Database) BarrierEnabled() bool {
	return d.barrierEnabled.Load()
}

// SetBarrierEnabled sets the barrier enabled state.
func (d *Database) SetBarrierEnabled(enabled bool) {
	d.barrierEnabled.Store(enabled)
}

// ProtocolConfig returns the wire protocol configuration.
func (d *Database) ProtocolConfig() ProtocolConfig {
	return d.config.Load().Protocol
}

// UpdateProtocol atomically replaces the wire protocol configuration.
func (d *Database) UpdateProtocol(protocol ProtocolConfig) {
	for {
		old := d.config.Load()
		updated := *old
		updated.Protocol = protocol
		if d.config.CompareAndSwap(old, &updated) {
			return
		}
	}
}

// UpdateEtcd atomically replaces the etcd wire protocol configuration.
func (d *Database) UpdateEtcd(etcd *EtcdConfig) {
	for {
		old := d.config.Load()
		updated := *old
		updated.Protocol.Etcd = etcd
		if d.config.CompareAndSwap(old, &updated) {
			return
		}
	}
}

// updateRaft atomically replaces the Raft configuration.
func (d *Database) updateRaft(raft *DatabaseRaftConfig) {
	for {
		old := d.config.Load()
		updated := *old
		updated.Raft = raft
		if d.config.CompareAndSwap(old, &updated) {
			return
		}
	}
}

// updateReplicas atomically replaces the replication factor recorded on the
// database's config. It is meaningful only for a dedicated-raft database
// whose replica count changed via DatabaseService.ScaleForTenant; the
// replicated placement table (not this field) remains the authoritative
// record of which nodes host which replicas.
func (d *Database) updateReplicas(replicas int) {
	for {
		old := d.config.Load()
		updated := *old
		updated.Replicas = replicas
		if d.config.CompareAndSwap(old, &updated) {
			return
		}
	}
}

// updateBarrier atomically replaces the barrier configuration.
func (d *Database) updateBarrier(barrier *DatabaseBarrierConfig) {
	for {
		old := d.config.Load()
		updated := *old
		updated.Barrier = barrier
		if d.config.CompareAndSwap(old, &updated) {
			return
		}
	}
}

// SQLConfig returns the database's SQL settings, nil when none were ever
// set (which means the defaults).
func (d *Database) SQLConfig() *SQLConfig {
	return d.config.Load().SQL
}

// updateSQL atomically replaces the SQL settings.
func (d *Database) updateSQL(sqlCfg *SQLConfig) {
	for {
		old := d.config.Load()
		updated := *old
		updated.SQL = sqlCfg
		if d.config.CompareAndSwap(old, &updated) {
			return
		}
	}
}

// CompactionConfig returns the database's scheduled-compaction policy, nil
// when it has none (off).
func (d *Database) CompactionConfig() *compaction.Policy {
	return d.config.Load().Compaction
}

// updateCompaction atomically replaces the compaction policy.
func (d *Database) updateCompaction(policy *compaction.Policy) {
	for {
		old := d.config.Load()
		updated := *old
		updated.Compaction = policy
		if d.config.CompareAndSwap(old, &updated) {
			return
		}
	}
}

// updateConnection atomically replaces the connection configuration.
func (d *Database) updateConnection(conn *DatabaseConnectionConfig) {
	for {
		old := d.config.Load()
		updated := *old
		updated.Connection = conn
		if d.config.CompareAndSwap(old, &updated) {
			return
		}
	}
}

// reportedStatus returns StatusDropping when a drop is fencing the database
// (see Dropping), overriding whatever Status() would otherwise answer. This
// is the ONLY place StatusDropping is exposed -- the underlying status field
// is never itself set to it (see the dropping field's doc comment) -- so
// Info()/List() report a mid-drop database honestly without disturbing the
// separate bookkeeping Close() and Manager.SetBarrierManager depend on
// Status() for.
func (d *Database) reportedStatus() StatusType {
	if d.Dropping() {
		return StatusDropping
	}
	return d.Status()
}

// Info returns runtime information about this database.
func (d *Database) Info() DatabaseInfo {
	cfg := d.config.Load()
	info := DatabaseInfo{
		TenantID:     cfg.TenantID,
		Name:         cfg.Name,
		Engine:       cfg.Engine,
		Backend:      cfg.Backend,
		Status:       d.reportedStatus(),
		Protocol:     cfg.Protocol,
		DataDir:      d.dataDir,
		Mode:         cfg.Mode,
		StorageCodec: cfg.StorageCodec,
		Replicas:     cfg.Replicas,
		DedicatedWAL: cfg.DedicatedWAL,
		Raft:         cfg.Raft,
		Barrier:      cfg.Barrier,
		Connection:   cfg.Connection,
		SQL:          cfg.SQL,
		Compaction:   cfg.Compaction,
	}
	if cfg.Raft != nil {
		info.ShardID = cfg.Raft.ShardID
	}
	return info
}

// Close shuts down the database by closing its data-plane backend and (when
// present) its dedicated barrier metadata backend, then sets the status to
// StatusClosed. Closing the data-plane backend when it is barrier-wrapped
// also closes the underlying raw backend (BarrierBackend.Close forwards to
// its inner backend), so no separate close of rawDataBackend is needed.
//
// This is unaffected by Dropping: Manager.Drop's fence never touches Status,
// precisely so that Close (called from Drop step 2) keeps answering "was
// there an open backend to release" from Status alone, exactly as it did
// before the fence existed.
func (d *Database) Close() error {
	status := d.Status()
	if status != StatusOpen && status != StatusSealed {
		return &NotOpenError{Name: d.config.Load().Name}
	}
	var closeErr error
	if err := d.Backend().Close(); err != nil {
		closeErr = err
	}
	if mb := d.barrierMetaBackendRef(); mb != nil {
		if err := mb.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	if closeErr != nil {
		d.setStatus(StatusError)
		return &CloseError{Name: d.config.Load().Name, Err: closeErr}
	}
	d.setStatus(StatusClosed)
	return nil
}
