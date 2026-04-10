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

// Database represents a named, isolated data store with its own engine,
// backend, and storage lifecycle. The config field is stored as an
// atomic pointer to support lock-free runtime updates (e.g., protocol config).
type Database struct {
	config         atomic.Pointer[DatabaseConfig]
	backend        Backend
	status         atomic.Value // stores StatusType
	dataDir        string
	barrierEnabled atomic.Bool
}

// NewDatabase creates a Database from the given config and backend.
// The database starts in StatusOpen state.
func NewDatabase(config DatabaseConfig, backend Backend, dataDir string) *Database {
	db := &Database{
		backend: backend,
		dataDir: dataDir,
	}
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

// Backend returns the database backend.
func (d *Database) Backend() Backend {
	return d.backend
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
		Raft:         cfg.Raft,
		Barrier:      cfg.Barrier,
		Connection:   cfg.Connection,
	}
	if cfg.Raft != nil {
		info.ShardID = cfg.Raft.ShardID
	}
	return info
}

// Close shuts down the database by closing its backend and setting
// the status to StatusClosed.
func (d *Database) Close() error {
	status := d.Status()
	if status != StatusOpen && status != StatusSealed {
		return &NotOpenError{Name: d.config.Load().Name}
	}
	if err := d.backend.Close(); err != nil {
		d.status.Store(StatusError)
		return &CloseError{Name: d.config.Load().Name, Err: err}
	}
	d.status.Store(StatusClosed)
	return nil
}
