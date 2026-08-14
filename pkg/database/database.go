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
	"sync/atomic"
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
	return d.status.Load().(StatusType)
}

// SetStatus sets the operational status of the database.
func (d *Database) SetStatus(status StatusType) {
	d.status.Store(status)
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

// Info returns runtime information about this database.
func (d *Database) Info() DatabaseInfo {
	cfg := d.config.Load()
	info := DatabaseInfo{
		TenantID:     cfg.TenantID,
		Name:         cfg.Name,
		Engine:       cfg.Engine,
		Backend:      cfg.Backend,
		Status:       d.Status(),
		Protocol:     cfg.Protocol,
		DataDir:      d.dataDir,
		Mode:         cfg.Mode,
		StorageCodec: cfg.StorageCodec,
		Replicas:     cfg.Replicas,
		DedicatedWAL: cfg.DedicatedWAL,
		Raft:         cfg.Raft,
		Barrier:      cfg.Barrier,
		Connection:   cfg.Connection,
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
		d.status.Store(StatusError)
		return &CloseError{Name: d.config.Load().Name, Err: closeErr}
	}
	d.status.Store(StatusClosed)
	return nil
}
