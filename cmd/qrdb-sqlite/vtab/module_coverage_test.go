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

//go:build sqlite_vtable

package vtab

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// mockBatchExecutor — exported BatchExecutor for NewTxBuffer tests
// ---------------------------------------------------------------------------

type mockBatchExecutor struct {
	mu    sync.Mutex
	calls [][]BufferedOp
	err   error
}

func (m *mockBatchExecutor) ExecuteBatch(_ context.Context, ops []BufferedOp) error {
	if m.err != nil {
		return m.err
	}
	cp := make([]BufferedOp, len(ops))
	copy(cp, ops)
	m.mu.Lock()
	m.calls = append(m.calls, cp)
	m.mu.Unlock()
	return nil
}

func (m *mockBatchExecutor) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// ---------------------------------------------------------------------------
// Unique driver names for module tests
// ---------------------------------------------------------------------------

var moduleDriverCounter struct {
	sync.Mutex
	n int
}

func newModuleDriverName() string {
	moduleDriverCounter.Lock()
	defer moduleDriverCounter.Unlock()
	moduleDriverCounter.n++
	return fmt.Sprintf("sqlite3_module_cov_test_%d", moduleDriverCounter.n)
}

// ---------------------------------------------------------------------------
// NewTxBuffer / executeBatch bridge
// ---------------------------------------------------------------------------

func TestNewTxBuffer_HappyPath(t *testing.T) {
	exec := &mockBatchExecutor{}
	buf := NewTxBuffer(exec)

	require.NoError(t, buf.begin())
	buf.put("key1", []byte("val1"))
	buf.del("key2")
	require.NoError(t, buf.commit(context.Background()))

	assert.Equal(t, 1, exec.callCount())
}

func TestNewTxBuffer_ExecutorError(t *testing.T) {
	exec := &mockBatchExecutor{err: errors.New("batch failed")}
	buf := NewTxBuffer(exec)

	require.NoError(t, buf.begin())
	buf.put("k", []byte("v"))

	err := buf.commit(context.Background())
	require.Error(t, err)

	var commitErr *TxCommitError
	require.True(t, errors.As(err, &commitErr))
	assert.Contains(t, commitErr.Error(), "batch failed")
}

// ---------------------------------------------------------------------------
// RegisterKVOnConn
// ---------------------------------------------------------------------------

func TestRegisterKVOnConn_HappyPath(t *testing.T) {
	mock := newMockKV()
	pragma := newPragmaStore("test", "localhost", "db")
	driverName := newModuleDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return RegisterKVOnConn(conn, mock, pragma, defaultChunkSize)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec("CREATE VIRTUAL TABLE kv USING qrdb_kv")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", "hello", []byte("world"))
	require.NoError(t, err)

	var key string
	var val []byte
	require.NoError(t, db.QueryRow("SELECT key, value FROM kv WHERE key = 'hello'").Scan(&key, &val))
	assert.Equal(t, "hello", key)
	assert.Equal(t, []byte("world"), val)
}

func TestRegisterKVOnConn_DropTable(t *testing.T) {
	mock := newMockKV()
	pragma := newPragmaStore("test", "localhost", "db")
	driverName := newModuleDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return RegisterKVOnConn(conn, mock, pragma, defaultChunkSize)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec("CREATE VIRTUAL TABLE kv2 USING qrdb_kv")
	require.NoError(t, err)
	// DROP TABLE exercises Destroy on the kvTable.
	_, err = db.Exec("DROP TABLE kv2")
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// RegisterCollectionOnConn
// ---------------------------------------------------------------------------

func TestRegisterCollectionOnConn_HappyPath(t *testing.T) {
	store := newMockCollectionStore()
	meta := &collectionMeta{
		name:     "orders",
		database: "shop",
		schema:   map[string]string{"amount": "DOUBLE"},
	}
	driverName := newModuleDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return RegisterCollectionOnConn(conn, meta, store, defaultChunkSize)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	modName := collectionModuleName + "_shop_orders"
	_, err = db.Exec("CREATE VIRTUAL TABLE orders USING " + modName)
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO orders (amount) VALUES (?)", 99.95)
	require.NoError(t, err)
	assert.Equal(t, 1, store.docCount())
}

func TestRegisterCollectionOnConn_DropTable(t *testing.T) {
	store := newMockCollectionStore()
	meta := &collectionMeta{
		name:     "prods",
		database: "shop2",
		schema:   map[string]string{"title": "VARCHAR"},
	}
	driverName := newModuleDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return RegisterCollectionOnConn(conn, meta, store, defaultChunkSize)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	modName := collectionModuleName + "_shop2_prods"
	_, err = db.Exec("CREATE VIRTUAL TABLE prods USING " + modName)
	require.NoError(t, err)
	_, err = db.Exec("DROP TABLE prods")
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// RegisterNamespaceOnConn
// ---------------------------------------------------------------------------

func TestRegisterNamespaceOnConn_HappyPath(t *testing.T) {
	store := newMockNamespaceStore()
	driverName := newModuleDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return RegisterNamespaceOnConn(conn, store, defaultChunkSize)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec("CREATE VIRTUAL TABLE ns USING " + namespaceModuleName)
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO ns (name, dimension, distance_metric, index_type) VALUES (?, ?, ?, ?)",
		"images", int64(128), "cosine", "hnsw")
	require.NoError(t, err)

	store.mu.RLock()
	_, ok := store.namespaces["images"]
	store.mu.RUnlock()
	assert.True(t, ok)
}

func TestRegisterNamespaceOnConn_DropTable(t *testing.T) {
	store := newMockNamespaceStore()
	driverName := newModuleDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return RegisterNamespaceOnConn(conn, store, defaultChunkSize)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec("CREATE VIRTUAL TABLE ns2 USING " + namespaceModuleName)
	require.NoError(t, err)
	_, err = db.Exec("DROP TABLE ns2")
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// RegisterVectorOnConn
// ---------------------------------------------------------------------------

func TestRegisterVectorOnConn_HappyPath(t *testing.T) {
	store := newMockVectorStore()
	driverName := newModuleDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return RegisterVectorOnConn(conn, store, defaultChunkSize)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec("CREATE VIRTUAL TABLE vecs USING " + vectorModuleName)
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO vecs (id, namespace, vector, metadata) VALUES (?, ?, ?, ?)",
		int64(1), "images", []byte{0x01, 0x02}, `{"label":"cat"}`)
	require.NoError(t, err)
}

func TestRegisterVectorOnConn_DropTable(t *testing.T) {
	store := newMockVectorStore()
	driverName := newModuleDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return RegisterVectorOnConn(conn, store, defaultChunkSize)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec("CREATE VIRTUAL TABLE vecs2 USING " + vectorModuleName)
	require.NoError(t, err)
	_, err = db.Exec("DROP TABLE vecs2")
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// RegisterEntityFromMeta / entityMetaFromTableMeta
// ---------------------------------------------------------------------------

func TestRegisterEntityFromMeta_HappyPath(t *testing.T) {
	store := newMockEntityStore()
	tableMeta := TableMeta{
		Name:     "products",
		Database: "store",
		Columns: []ColumnMeta{
			{Name: "_id", SQLiteType: "INTEGER", PrimaryKey: true},
			{Name: "title", SQLiteType: "TEXT"},
			{Name: "price", SQLiteType: "REAL"},
		},
	}
	driverName := newModuleDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return RegisterEntityFromMeta(conn, tableMeta, store, defaultChunkSize)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	modName := entityModuleName + "_products"
	_, err = db.Exec("CREATE VIRTUAL TABLE products USING " + modName)
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO products (title, price) VALUES (?, ?)", "Widget", 9.99)
	require.NoError(t, err)
}

func TestEntityMetaFromTableMeta_NoExplicitPK(t *testing.T) {
	// When no column is marked PrimaryKey, idFieldJSON defaults to "_id".
	meta := entityMetaFromTableMeta(TableMeta{
		Name:     "things",
		Database: "db",
		Columns: []ColumnMeta{
			{Name: "name", SQLiteType: "TEXT"},
			{Name: "count", SQLiteType: "INTEGER"},
		},
	})

	assert.Equal(t, "things", meta.entityType)
	assert.Equal(t, "_id", meta.idFieldJSON)
	assert.Len(t, meta.fields, 2)
}

func TestEntityMetaFromTableMeta_WithPK(t *testing.T) {
	meta := entityMetaFromTableMeta(TableMeta{
		Name:     "items",
		Database: "db",
		Columns: []ColumnMeta{
			{Name: "item_id", SQLiteType: "INTEGER", PrimaryKey: true},
			{Name: "label", SQLiteType: "TEXT"},
		},
	})

	assert.Equal(t, "item_id", meta.idFieldJSON)
	// PK column is excluded from fields slice.
	assert.Len(t, meta.fields, 1)
	assert.Equal(t, "label", meta.fields[0].jsonName)
}

// ---------------------------------------------------------------------------
// RegisterCollectionFromMeta / collectionMetaFromTableMeta
// ---------------------------------------------------------------------------

func TestRegisterCollectionFromMeta_HappyPath(t *testing.T) {
	store := newMockCollectionStore()
	tableMeta := TableMeta{
		Name:     "books",
		Database: "library",
		Columns: []ColumnMeta{
			{Name: "_id", SQLiteType: "INTEGER", PrimaryKey: true},
			{Name: "title", SQLiteType: "TEXT"},
			{Name: "pages", SQLiteType: "INTEGER"},
		},
	}
	driverName := newModuleDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return RegisterCollectionFromMeta(conn, tableMeta, store, defaultChunkSize)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	modName := collectionModuleName + "_library_books"
	_, err = db.Exec("CREATE VIRTUAL TABLE books USING " + modName)
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO books (title, pages) VALUES (?, ?)", "Go Programming", int64(500))
	require.NoError(t, err)
	assert.Equal(t, 1, store.docCount())
}

func TestCollectionMetaFromTableMeta_ExcludesPK(t *testing.T) {
	cm := collectionMetaFromTableMeta(TableMeta{
		Name:     "events",
		Database: "analytics",
		Columns: []ColumnMeta{
			{Name: "_id", SQLiteType: "INTEGER", PrimaryKey: true},
			{Name: "event_type", SQLiteType: "TEXT"},
			{Name: "ts", SQLiteType: "INTEGER"},
		},
	})

	assert.Equal(t, "events", cm.name)
	assert.Equal(t, "analytics", cm.database)
	// PK is excluded from schema.
	_, hasPK := cm.schema["_id"]
	assert.False(t, hasPK)
	assert.Equal(t, "TEXT", cm.schema["event_type"])
	assert.Equal(t, "INTEGER", cm.schema["ts"])
}

// ---------------------------------------------------------------------------
// RegisterDatapointModule
// ---------------------------------------------------------------------------

func TestRegisterDatapointModule_HappyPath(t *testing.T) {
	store := newMockDatapointStore()
	driverName := newModuleDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return RegisterDatapointModule(conn, store, defaultChunkSize)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec("CREATE VIRTUAL TABLE dp USING " + datapointModuleName)
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO dp (metric, timestamp_ns, value, tags) VALUES (?, ?, ?, ?)",
		"cpu", int64(1000), 0.85, `{"host":"web1"}`)
	require.NoError(t, err)
}

func TestRegisterDatapointModule_DropTable(t *testing.T) {
	store := newMockDatapointStore()
	driverName := newModuleDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return RegisterDatapointModule(conn, store, defaultChunkSize)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec("CREATE VIRTUAL TABLE dp2 USING " + datapointModuleName)
	require.NoError(t, err)
	_, err = db.Exec("DROP TABLE dp2")
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// RegisterMetricsModule
// ---------------------------------------------------------------------------

func TestRegisterMetricsModule_HappyPath(t *testing.T) {
	store := newMockMetricsStore()
	store.mu.Lock()
	store.metrics = append(store.metrics, MetricInfo{Name: "cpu_usage", Description: "CPU utilization", Unit: "%", MetricType: "gauge", PointCount: 42})
	store.mu.Unlock()
	driverName := newModuleDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return RegisterMetricsModule(conn, store, defaultChunkSize)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec("CREATE VIRTUAL TABLE metrics USING " + metricsModuleName)
	require.NoError(t, err)

	rows, err := db.Query("SELECT name FROM metrics")
	require.NoError(t, err)
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		names = append(names, n)
	}
	require.NoError(t, rows.Err())
	assert.Contains(t, names, "cpu_usage")
}

func TestRegisterMetricsModule_DropTable(t *testing.T) {
	store := newMockMetricsStore()
	driverName := newModuleDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return RegisterMetricsModule(conn, store, defaultChunkSize)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec("CREATE VIRTUAL TABLE metrics2 USING " + metricsModuleName)
	require.NoError(t, err)
	_, err = db.Exec("DROP TABLE metrics2")
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// DiscoverAndRegister (exported entry point)
// ---------------------------------------------------------------------------

func TestDiscoverAndRegister_HappyPath(t *testing.T) {
	// Open a fresh in-memory database with the qrdb_kv module registered.
	driverName := newModuleDriverName()
	mock := newMockKV()
	pragma := newPragmaStore("test", "", "")

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &kvModule{kv: mock, pragma: pragma, chunkSize: defaultChunkSize}
			return conn.CreateModule(kvModuleName, mod)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	provider := &mockMetadataProvider{
		tables: []TableMeta{
			{
				Name: "mystore",
				Type: TableKV,
				Columns: []ColumnMeta{
					{Name: "key", SQLiteType: "TEXT", PrimaryKey: true},
					{Name: "value", SQLiteType: "BLOB"},
				},
			},
		},
	}

	created, err := DiscoverAndRegister(context.Background(), db, provider)
	require.NoError(t, err)
	assert.Equal(t, []string{"mystore"}, created)

	// Verify the discovered table is queryable.
	rows, err := db.QueryContext(context.Background(), "SELECT key, value FROM mystore")
	require.NoError(t, err)
	defer rows.Close()
	assert.False(t, rows.Next())
}

func TestDiscoverAndRegister_ProviderError(t *testing.T) {
	driverName := newModuleDriverName()
	mock := newMockKV()
	pragma := newPragmaStore("test", "", "")

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &kvModule{kv: mock, pragma: pragma, chunkSize: defaultChunkSize}
			return conn.CreateModule(kvModuleName, mod)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	provider := &mockMetadataProvider{err: errors.New("server unreachable")}

	_, err = DiscoverAndRegister(context.Background(), db, provider)
	require.Error(t, err)

	var discoveryErr *DiscoveryError
	assert.True(t, errors.As(err, &discoveryErr))
}
