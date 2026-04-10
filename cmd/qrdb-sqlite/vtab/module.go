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

package vtab

import (
	"context"
	"database/sql"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// ---------------------------------------------------------------------------
// Exported store interfaces — satisfy these in the main package.
//
// All exported interfaces use exported (uppercase) method names so that
// external packages can implement them without restriction.
// ---------------------------------------------------------------------------

// KVStore provides key-value read and write access for the qrdb_kv virtual
// table. Main-package adapters implement this interface and pass it to
// RegisterModules or RegisterKVOnConn.
type KVStore interface {
	// Get fetches the value for a single key. Returns (nil, nil) when the
	// key does not exist.
	Get(ctx context.Context, key string) ([]byte, error)

	// Scan returns all key-value pairs whose keys share the given prefix.
	// An empty prefix returns all pairs in the namespace.
	Scan(ctx context.Context, prefix string) (map[string][]byte, error)

	// Put writes a key-value pair. It is idempotent.
	Put(ctx context.Context, key string, value []byte) error

	// Delete removes the entry for key. It is a no-op when key does not exist.
	Delete(ctx context.Context, key string) error
}

// EntityStore provides CRUD and index-based access for the qrdb_entity virtual
// table. Main-package adapters implement this and pass it to RegisterEntityModule.
type EntityStore interface {
	// GetEntity fetches a single entity by its integer ID. Returns (nil, nil)
	// when the entity does not exist.
	GetEntity(ctx context.Context, entityType string, id uint64) (map[string]any, error)

	// ScanEntities returns all entities of the given type.
	ScanEntities(ctx context.Context, entityType string) ([]EntityRow, error)

	// PutEntity writes (insert or replace) an entity under the given ID.
	PutEntity(ctx context.Context, entityType string, id uint64, data map[string]any) error

	// DeleteEntity removes the entity with the given ID.
	DeleteEntity(ctx context.Context, entityType string, id uint64) error

	// QueryByIndex returns all entities where fieldName equals value exactly.
	QueryByIndex(ctx context.Context, entityType, fieldName, value string) ([]EntityRow, error)

	// ScanByIndex returns entities where fieldName falls within [start, end].
	ScanByIndex(ctx context.Context, entityType, fieldName, start, end string) ([]EntityRow, error)
}

// CollectionStore provides document collection access for the qrdb_collection
// virtual table. Main-package adapters implement this and pass it to
// RegisterCollectionOnConn.
type CollectionStore interface {
	// GetDocument returns a single document by ID. Returns (nil, nil) when
	// the document does not exist.
	GetDocument(ctx context.Context, database, collection string, id uint64) (map[string]any, error)

	// ListDocuments returns all documents in the collection.
	ListDocuments(ctx context.Context, database, collection string) ([]DocumentRow, error)

	// InsertDocument inserts a new document and returns its assigned ID.
	InsertDocument(ctx context.Context, database, collection string, doc map[string]any) (uint64, error)

	// UpdateDocument replaces an existing document in the collection.
	UpdateDocument(ctx context.Context, database, collection string, id uint64, doc map[string]any) error

	// DeleteDocument removes a document by ID.
	DeleteDocument(ctx context.Context, database, collection string, id uint64) error
}

// DatapointStore provides time-series data point ingestion and querying for
// the qrdb_ts_datapoints virtual table. Main-package adapters implement this
// and pass it to RegisterDatapointModule.
type DatapointStore interface {
	// WriteDatapoint writes a single time-series data point.
	WriteDatapoint(ctx context.Context, metric string, timestampNs int64, value float64, tags string) error

	// QueryDatapoints retrieves data points for a metric in a time range.
	QueryDatapoints(ctx context.Context, metric string, startNs, endNs int64) ([]DatapointRow, error)

	// ScanAllDatapoints returns all data points across all metrics.
	ScanAllDatapoints(ctx context.Context) ([]DatapointRow, error)
}

// MetricsStore provides time-series metric metadata access for the
// qrdb_ts_metrics virtual table. Main-package adapters implement this and
// pass it to RegisterMetricsModule.
type MetricsStore interface {
	// ListMetrics returns all known metric names.
	ListMetrics(ctx context.Context) ([]MetricInfo, error)

	// GetMetric returns the metadata for a single metric by name. Returns
	// (nil, nil) when the metric does not exist.
	GetMetric(ctx context.Context, name string) (*MetricInfo, error)
}

// NamespaceStore provides vector namespace CRUD for the
// qrdb_vector_namespaces virtual table. Main-package adapters implement this
// and pass it to RegisterNamespaceOnConn.
type NamespaceStore interface {
	// ListNamespaces returns all available vector namespaces.
	ListNamespaces(ctx context.Context) ([]NamespaceInfo, error)

	// GetNamespace returns the metadata for a single namespace by name.
	// Returns (nil, nil) when the namespace does not exist.
	GetNamespace(ctx context.Context, name string) (*NamespaceInfo, error)

	// CreateNamespace creates a new namespace with the given parameters.
	CreateNamespace(ctx context.Context, name string, dimension uint32, distanceMetric, indexType string) error

	// DeleteNamespace removes the namespace and all its vectors.
	DeleteNamespace(ctx context.Context, name string) error
}

// VectorStore provides vector CRUD for the qrdb_vectors virtual table.
// Main-package adapters implement this and pass it to RegisterVectorOnConn.
type VectorStore interface {
	// UpsertVector inserts or updates a vector.
	UpsertVector(ctx context.Context, namespace string, id uint64, vector []byte, metadata string) error

	// GetVector retrieves a single vector by ID. Returns (nil, nil) when not found.
	GetVector(ctx context.Context, namespace string, id uint64) (*VectorRow, error)

	// ListVectors returns all vectors in a namespace.
	ListVectors(ctx context.Context, namespace string) ([]VectorRow, error)

	// DeleteVector removes a vector by ID.
	DeleteVector(ctx context.Context, namespace string, id uint64) error
}

// BatchExecutor provides atomic batch execution for the transaction buffer.
// Main-package adapters implement this and pass it to NewTxBuffer.
type BatchExecutor interface {
	// ExecuteBatch applies the given operations atomically.
	ExecuteBatch(ctx context.Context, ops []BufferedOp) error
}

// ---------------------------------------------------------------------------
// Internal unexported aliases — kept so that existing internal code in this
// package that references the unexported names continues to compile. All
// module struct fields use the exported interface types directly.
// ---------------------------------------------------------------------------

// kvStore is an internal alias for KVStore kept for backward compatibility
// within the package.
type kvStore = KVStore

// kvReader is a subset of KVStore for read-only access patterns used
// internally by cursor types.
type kvReader interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Scan(ctx context.Context, prefix string) (map[string][]byte, error)
}

// entityStore is an internal alias for EntityStore.
type entityStore = EntityStore

// collectionStore is an internal alias for CollectionStore.
type collectionStore = CollectionStore

// datapointStore is an internal alias for DatapointStore.
type datapointStore = DatapointStore

// metricsStore is an internal alias for MetricsStore.
type metricsStore = MetricsStore

// namespaceStore is an internal alias for NamespaceStore.
type namespaceStore = NamespaceStore

// vectorStore is an internal alias for VectorStore.
type vectorStore = VectorStore

// batchExecutor is an internal alias for BatchExecutor. The txBuffer uses
// this via the unexported executeBatch wrapper below.
type batchExecutor interface {
	executeBatch(ctx context.Context, ops []BufferedOp) error
}

// batchExecutorBridge wraps a BatchExecutor to satisfy the unexported
// batchExecutor interface used by txBuffer.
type batchExecutorBridge struct{ s BatchExecutor }

func (b *batchExecutorBridge) executeBatch(ctx context.Context, ops []BufferedOp) error {
	return b.s.ExecuteBatch(ctx, ops)
}

// NewTxBuffer creates a txBuffer that delegates batch execution to the given
// BatchExecutor. This is the exported constructor for use by the main package.
func NewTxBuffer(executor BatchExecutor) *txBuffer {
	return newTxBuffer(&batchExecutorBridge{s: executor})
}

// kvModuleName is the SQLite module name used with CREATE VIRTUAL TABLE …
// USING qrdb_kv.
const kvModuleName = "qrdb_kv"

// RegisterModules registers all qrdb virtual table modules with a SQLite
// driver by name. The driver is registered with database/sql under
// driverName and uses kv for KV operations, pragma for session settings,
// and chunkSize for prefetch buffering.
//
// RegisterModules must be called before any sql.Open call that uses
// driverName. It is safe to call once per unique driverName.
func RegisterModules(driverName string, kv KVStore, pragma *PragmaStore, chunkSize int) error {
	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &kvModule{
				kv:        kv,
				pragma:    pragma,
				chunkSize: chunkSize,
			}
			if err := conn.CreateModule(kvModuleName, mod); err != nil {
				return &ModuleCreateError{Module: kvModuleName, Err: err}
			}
			return nil
		},
	})
	return nil
}

// RegisterKVOnConn registers the qrdb_kv virtual table module with an existing
// SQLite connection. It is designed for use inside a ConnectHook when the
// caller owns the driver registration and needs to compose multiple modules
// into a single driver.
func RegisterKVOnConn(conn *sqlite3.SQLiteConn, kv KVStore, pragma *PragmaStore, chunkSize int) error {
	mod := &kvModule{
		kv:        kv,
		pragma:    pragma,
		chunkSize: chunkSize,
	}
	if err := conn.CreateModule(kvModuleName, mod); err != nil {
		return &ModuleCreateError{Module: kvModuleName, Err: err}
	}
	return nil
}

// RegisterCollectionOnConn registers the qrdb_collection virtual table module
// for a single collection with an existing SQLite connection. It is designed
// for use inside a ConnectHook when the caller owns the driver registration.
func RegisterCollectionOnConn(conn *sqlite3.SQLiteConn, meta *collectionMeta, store CollectionStore, chunkSize int) error {
	name := collectionModuleName + "_" + meta.database + "_" + meta.name
	mod := &collectionModule{
		meta:      meta,
		store:     store,
		chunkSize: chunkSize,
	}
	if err := conn.CreateModule(name, mod); err != nil {
		return &ModuleCreateError{Module: name, Err: err}
	}
	return nil
}

// RegisterNamespaceOnConn registers the qrdb_vector_namespaces virtual table
// module with an existing SQLite connection.
func RegisterNamespaceOnConn(conn *sqlite3.SQLiteConn, store NamespaceStore, chunkSize int) error {
	mod := &namespaceModule{
		store:     store,
		chunkSize: chunkSize,
	}
	if err := conn.CreateModule(namespaceModuleName, mod); err != nil {
		return &ModuleCreateError{Module: namespaceModuleName, Err: err}
	}
	return nil
}

// RegisterVectorOnConn registers the qrdb_vectors virtual table module with an
// existing SQLite connection.
func RegisterVectorOnConn(conn *sqlite3.SQLiteConn, store VectorStore, chunkSize int) error {
	mod := &vectorModule{
		store:     store,
		chunkSize: chunkSize,
	}
	if err := conn.CreateModule(vectorModuleName, mod); err != nil {
		return &ModuleCreateError{Module: vectorModuleName, Err: err}
	}
	return nil
}

// RegisterEntityModule registers a qrdb_entity virtual table module for a
// single entity type with an existing SQLite connection.
func RegisterEntityModule(conn *sqlite3.SQLiteConn, meta *entityMeta, store EntityStore, chunkSize int) error {
	name := entityModuleName + "_" + meta.entityType
	mod := &entityModule{
		meta:      meta,
		store:     store,
		chunkSize: chunkSize,
	}
	if err := conn.CreateModule(name, mod); err != nil {
		return &ModuleCreateError{Module: name, Err: err}
	}
	return nil
}

// RegisterEntityFromMeta registers a qrdb_entity virtual table module derived
// from a TableMeta returned by auto-discovery.
func RegisterEntityFromMeta(conn *sqlite3.SQLiteConn, meta TableMeta, store EntityStore, chunkSize int) error {
	em := entityMetaFromTableMeta(meta)
	return RegisterEntityModule(conn, em, store, chunkSize)
}

// entityMetaFromTableMeta builds an *entityMeta from a TableMeta.
func entityMetaFromTableMeta(meta TableMeta) *entityMeta {
	fields := make([]fieldMeta, 0, len(meta.Columns))
	idFieldJSON := "_id"
	for _, col := range meta.Columns {
		if col.PrimaryKey {
			idFieldJSON = col.Name
			continue
		}
		fields = append(fields, fieldMeta{
			goName:     col.Name,
			jsonName:   col.Name,
			sqliteType: col.SQLiteType,
		})
	}
	return &entityMeta{
		entityType:  meta.Name,
		fields:      fields,
		idFieldJSON: idFieldJSON,
	}
}

// RegisterCollectionFromMeta registers a qrdb_collection virtual table module
// derived from a TableMeta returned by auto-discovery.
func RegisterCollectionFromMeta(conn *sqlite3.SQLiteConn, meta TableMeta, store CollectionStore, chunkSize int) error {
	cm := collectionMetaFromTableMeta(meta)
	return RegisterCollectionOnConn(conn, cm, store, chunkSize)
}

// collectionMetaFromTableMeta builds a *collectionMeta from a TableMeta.
func collectionMetaFromTableMeta(meta TableMeta) *collectionMeta {
	schema := make(map[string]string, len(meta.Columns))
	for _, col := range meta.Columns {
		if !col.PrimaryKey {
			schema[col.Name] = col.SQLiteType
		}
	}
	return &collectionMeta{
		name:     meta.Name,
		database: meta.Database,
		schema:   schema,
	}
}

// RegisterDatapointModule registers the qrdb_ts_datapoints virtual table
// module with an existing SQLite connection.
func RegisterDatapointModule(conn *sqlite3.SQLiteConn, store DatapointStore, chunkSize int) error {
	mod := &datapointModule{
		store:     store,
		chunkSize: chunkSize,
	}
	if err := conn.CreateModule(datapointModuleName, mod); err != nil {
		return &ModuleCreateError{Module: datapointModuleName, Err: err}
	}
	return nil
}

// RegisterMetricsModule registers the qrdb_ts_metrics virtual table module
// with an existing SQLite connection.
func RegisterMetricsModule(conn *sqlite3.SQLiteConn, store MetricsStore, chunkSize int) error {
	mod := &metricsModule{
		store:     store,
		chunkSize: chunkSize,
	}
	if err := conn.CreateModule(metricsModuleName, mod); err != nil {
		return &ModuleCreateError{Module: metricsModuleName, Err: err}
	}
	return nil
}

// RegisterNamespaceModule registers the qrdb_vector_namespaces virtual table
// module with a new SQLite driver identified by driverName.
func RegisterNamespaceModule(driverName string, store NamespaceStore, chunkSize int) error {
	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &namespaceModule{
				store:     store,
				chunkSize: chunkSize,
			}
			if err := conn.CreateModule(namespaceModuleName, mod); err != nil {
				return &ModuleCreateError{Module: namespaceModuleName, Err: err}
			}
			return nil
		},
	})
	return nil
}

// RegisterVectorModule registers the qrdb_vectors virtual table module with a
// new SQLite driver identified by driverName.
func RegisterVectorModule(driverName string, store VectorStore, chunkSize int) error {
	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &vectorModule{
				store:     store,
				chunkSize: chunkSize,
			}
			if err := conn.CreateModule(vectorModuleName, mod); err != nil {
				return &ModuleCreateError{Module: vectorModuleName, Err: err}
			}
			return nil
		},
	})
	return nil
}
