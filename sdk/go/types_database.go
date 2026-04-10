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

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/database"

// ---------------------------------------------------------------------------
// Database engine and backend type enumerations
// ---------------------------------------------------------------------------

// EngineType identifies the state machine engine (kv, timeseries, vector, etc.).
type EngineType = database.EngineType

// EngineType constants.
const (
	EngineKV         = database.EngineKV
	EngineTimeSeries = database.EngineTimeSeries
	EngineVector     = database.EngineVector
)

// BackendType identifies the storage/consensus backend.
type BackendType = database.BackendType

// BackendType constants.
const (
	BackendMemory = database.BackendMemory
	BackendFile   = database.BackendFile
	BackendPebble = database.BackendPebble
	BackendRaft   = database.BackendRaft
)

// DatabaseMode identifies the operational mode (embedded, remote).
type DatabaseMode = database.DatabaseMode

// DatabaseMode constants.
const (
	ModeEmbedded = database.ModeEmbedded
	ModeRemote   = database.ModeRemote
)

// StatusType identifies the status of a database.
type StatusType = database.StatusType

// StatusType constants.
const (
	StatusOpen   = database.StatusOpen
	StatusClosed = database.StatusClosed
	StatusError  = database.StatusError
	StatusSealed = database.StatusSealed
)

// ---------------------------------------------------------------------------
// Raft cluster types
// ---------------------------------------------------------------------------

// RaftClusterMode identifies the Raft cluster mode.
type RaftClusterMode = database.RaftClusterMode

// RaftClusterMode constants.
const (
	RaftNone        = database.RaftNone
	RaftLocal       = database.RaftLocal
	RaftDistributed = database.RaftDistributed
)

// DiscoveryMethod identifies the Raft node discovery method.
type DiscoveryMethod = database.DiscoveryMethod

// DiscoveryMethod constants.
const (
	DiscoveryStatic    = database.DiscoveryStatic
	DiscoveryMulticast = database.DiscoveryMulticast
	DiscoveryDNS       = database.DiscoveryDNS
	DiscoveryToken     = database.DiscoveryToken
)

// ---------------------------------------------------------------------------
// Configuration types
// ---------------------------------------------------------------------------

// DatabaseConfig holds the full configuration for a database.
type DatabaseConfig = database.DatabaseConfig

// DatabaseRaftConfig holds Raft-specific configuration.
type DatabaseRaftConfig = database.DatabaseRaftConfig

// DatabaseBarrierConfig holds barrier-specific configuration.
type DatabaseBarrierConfig = database.DatabaseBarrierConfig

// DatabaseConnectionConfig holds connection configuration.
type DatabaseConnectionConfig = database.DatabaseConnectionConfig

// ProtocolConfig holds protocol configuration for a database.
type ProtocolConfig = database.ProtocolConfig

// EtcdConfig holds etcd-specific configuration.
type EtcdConfig = database.EtcdConfig

// StaticDiscoveryConfig holds static Raft peer addresses.
type StaticDiscoveryConfig = database.StaticDiscoveryConfig

// MulticastDiscoveryConfig holds multicast discovery settings.
type MulticastDiscoveryConfig = database.MulticastDiscoveryConfig

// DNSDiscoveryConfig holds DNS discovery settings.
type DNSDiscoveryConfig = database.DNSDiscoveryConfig

// TokenDiscoveryConfig holds token-based discovery settings.
type TokenDiscoveryConfig = database.TokenDiscoveryConfig

// ---------------------------------------------------------------------------
// Database info and metadata
// ---------------------------------------------------------------------------

// DatabaseMeta holds database metadata stored alongside data.
type DatabaseMeta = database.DatabaseMeta

// DatabaseInfo holds runtime database information.
type DatabaseInfo = database.DatabaseInfo

// Database is the runtime database instance.
type Database = database.Database

// ---------------------------------------------------------------------------
// Backend interface and implementations
// ---------------------------------------------------------------------------

// DatabaseBackend is the low-level storage backend interface.
type DatabaseBackend = database.Backend

// KeyValue is a key-value pair used in range queries and batch ops.
type KeyValue = database.KeyValue

// BatchOp describes a single operation within a batch.
type BatchOp = database.BatchOp

// BatchOpType identifies the operation type within a batch.
type BatchOpType = database.BatchOpType

// BatchOpType constants.
const (
	BatchOpPut    = database.BatchOpPut
	BatchOpDelete = database.BatchOpDelete
)

// StandaloneBackend wraps a storage engine as a standalone backend.
type StandaloneBackend = database.StandaloneBackend

// BarrierEncryptor is the interface for barrier-backed encryption.
type BarrierEncryptor = database.BarrierEncryptor

// BarrierBackend wraps a backend with barrier encryption.
type BarrierBackend = database.BarrierBackend

// BackendStorageAdapter adapts a Backend to the seal StorageBackend interface.
type BackendStorageAdapter = database.BackendStorageAdapter

// DatabaseBarrierManager manages per-database barriers.
type DatabaseBarrierManager = database.DatabaseBarrierManager

// ---------------------------------------------------------------------------
// Constructors
// ---------------------------------------------------------------------------

// NewDatabase creates a new Database instance.
var NewDatabase = database.NewDatabase

// NewStandaloneBackend creates a new standalone backend from a storage engine.
var NewStandaloneBackend = database.NewStandaloneBackend

// NewBarrierBackend creates a new barrier-encrypted backend.
var NewBarrierBackend = database.NewBarrierBackend

// NewBackendStorageAdapter creates a Backend-to-StorageBackend adapter.
var NewBackendStorageAdapter = database.NewBackendStorageAdapter

// NewDatabaseBarrierManager creates a new DatabaseBarrierManager.
var NewDatabaseBarrierManager = database.NewDatabaseBarrierManager

// ---------------------------------------------------------------------------
// Database error types
// ---------------------------------------------------------------------------

// DBInvalidNameError is returned when a database name is invalid.
type DBInvalidNameError = database.InvalidNameError

// DBInvalidEngineError is returned when an engine type is invalid.
type DBInvalidEngineError = database.InvalidEngineError

// DBInvalidBackendError is returned when a backend type is invalid.
type DBInvalidBackendError = database.InvalidBackendError

// DBAlreadyExistsError is returned when a database already exists.
type DBAlreadyExistsError = database.AlreadyExistsError

// DBNotFoundError is returned when a database is not found.
type DBNotFoundError = database.NotFoundError

// DBNotOpenError is returned when an operation requires an open database.
type DBNotOpenError = database.NotOpenError

// DBOpenError is returned when a database fails to open.
type DBOpenError = database.OpenError

// DBCloseError is returned when a database fails to close.
type DBCloseError = database.CloseError

// DBMetaReadError is returned when reading database metadata fails.
type DBMetaReadError = database.MetaReadError

// DBMetaWriteError is returned when writing database metadata fails.
type DBMetaWriteError = database.MetaWriteError

// DBDropError is returned when dropping a database fails.
type DBDropError = database.DropError

// DBStorageCreateError is returned when creating database storage fails.
type DBStorageCreateError = database.StorageCreateError

// DBInvalidModeError is returned when a database mode is invalid.
type DBInvalidModeError = database.InvalidModeError

// DBConnectionRequiredError is returned when a connection config is required.
type DBConnectionRequiredError = database.ConnectionRequiredError

// DBInvalidRaftClusterModeError is returned when a Raft cluster mode is invalid.
type DBInvalidRaftClusterModeError = database.InvalidRaftClusterModeError

// DBInvalidDiscoveryMethodError is returned when a discovery method is invalid.
type DBInvalidDiscoveryMethodError = database.InvalidDiscoveryMethodError

// DBRangeIteratorError is returned when a range iteration fails.
type DBRangeIteratorError = database.RangeIteratorError

// DBBarrierCreateError is returned when creating a database barrier fails.
type DBBarrierCreateError = database.BarrierCreateError

// DBBarrierSealError is returned when sealing a database barrier fails.
type DBBarrierSealError = database.BarrierSealError

// DBBarrierUnsealError is returned when unsealing a database barrier fails.
type DBBarrierUnsealError = database.BarrierUnsealError

// ---------------------------------------------------------------------------
// Manager types
// ---------------------------------------------------------------------------

// Manager manages the lifecycle of tenant databases.
type Manager = database.Manager

// ManagerOption configures a Manager at construction time.
type ManagerOption = database.ManagerOption

// NewManager creates a new database Manager.
var NewManager = database.NewManager

// BackendFactory creates database backends.
type BackendFactory = database.BackendFactory
