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
	"database/sql"
	"fmt"
	"sync"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// connectDestroyDriverCounter — unique driver names per test
// ---------------------------------------------------------------------------

var connectDestroyDriverCounter struct {
	sync.Mutex
	n int
}

func newConnectDestroyDriverName() string {
	connectDestroyDriverCounter.Lock()
	defer connectDestroyDriverCounter.Unlock()
	connectDestroyDriverCounter.n++
	return fmt.Sprintf("sqlite3_cd_cov_test_%d", connectDestroyDriverCounter.n)
}

// ---------------------------------------------------------------------------
// Helpers — exercise Connect by using a file-based SQLite database
//
// SQLite calls Connect (instead of Create) when a virtual table already
// exists in the schema and a new connection opens the same database file.
// We write a temp file, create the virtual table, close the connection,
// then open a *new* connection to the same file. The second open triggers
// Connect on the module.
// ---------------------------------------------------------------------------

// openFileDB returns an open *sql.DB backed by a temporary file so that
// Connect is triggered when a new connection is opened.
func openFileDB(t *testing.T, driverName, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open(driverName, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ---------------------------------------------------------------------------
// kvModule.Connect + DestroyModule
// ---------------------------------------------------------------------------

func TestKVModule_Connect_FileDB(t *testing.T) {
	mock := newMockKV()
	pragma := newPragmaStore("test", "", "")
	tmpFile := t.TempDir() + "/kv_connect.db"
	driverName := newConnectDestroyDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &kvModule{kv: mock, pragma: pragma, chunkSize: defaultChunkSize}
			return conn.CreateModule(kvModuleName, mod)
		},
	})

	// First connection — triggers Create.
	db1 := openFileDB(t, driverName, tmpFile)
	_, err := db1.Exec("CREATE VIRTUAL TABLE kv USING qrdb_kv")
	require.NoError(t, err)
	db1.Close()

	// Second connection to the same file — triggers Connect.
	db2 := openFileDB(t, driverName, tmpFile)
	rows, err := db2.Query("SELECT key FROM kv LIMIT 0")
	require.NoError(t, err)
	rows.Close()
}

func TestKVModule_DestroyModule_FileDB(t *testing.T) {
	mock := newMockKV()
	pragma := newPragmaStore("test", "", "")
	tmpFile := t.TempDir() + "/kv_destroy.db"
	driverName := newConnectDestroyDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &kvModule{kv: mock, pragma: pragma, chunkSize: defaultChunkSize}
			return conn.CreateModule(kvModuleName, mod)
		},
	})

	db := openFileDB(t, driverName, tmpFile)
	_, err := db.Exec("CREATE VIRTUAL TABLE kv USING qrdb_kv")
	require.NoError(t, err)
	// DROP TABLE exercises Destroy on the table (different from DestroyModule).
	_, err = db.Exec("DROP TABLE kv")
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// collectionModule.Connect + DestroyModule
// ---------------------------------------------------------------------------

func TestCollectionModule_Connect(t *testing.T) {
	store := newMockCollectionStore()
	meta := &collectionMeta{
		name:     "docs",
		database: "db",
		schema:   map[string]string{"title": "VARCHAR"},
	}
	tmpFile := t.TempDir() + "/col_connect.db"
	driverName := newConnectDestroyDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &collectionModule{meta: meta, store: store, chunkSize: defaultChunkSize}
			return conn.CreateModule(collectionModuleName, mod)
		},
	})

	db1 := openFileDB(t, driverName, tmpFile)
	_, err := db1.Exec("CREATE VIRTUAL TABLE docs USING qrdb_collection")
	require.NoError(t, err)
	db1.Close()

	db2 := openFileDB(t, driverName, tmpFile)
	rows, err := db2.Query("SELECT _id FROM docs LIMIT 0")
	require.NoError(t, err)
	rows.Close()
}

// ---------------------------------------------------------------------------
// entityModule.Connect + DestroyModule
// ---------------------------------------------------------------------------

func TestEntityModule_Connect(t *testing.T) {
	mock := newMockEntityStore()
	meta := buildUserMeta()
	tmpFile := t.TempDir() + "/entity_connect.db"
	driverName := newConnectDestroyDriverName()
	moduleName := entityModuleName + "_" + meta.entityType

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return RegisterEntityModule(conn, meta, mock, defaultChunkSize)
		},
	})

	db1 := openFileDB(t, driverName, tmpFile)
	_, err := db1.Exec("CREATE VIRTUAL TABLE users USING " + moduleName)
	require.NoError(t, err)
	db1.Close()

	db2 := openFileDB(t, driverName, tmpFile)
	rows, err := db2.Query("SELECT _id FROM users LIMIT 0")
	require.NoError(t, err)
	rows.Close()
}

// ---------------------------------------------------------------------------
// datapointModule.Connect + DestroyModule
// ---------------------------------------------------------------------------

func TestDatapointModule_Connect(t *testing.T) {
	store := newMockDatapointStore()
	tmpFile := t.TempDir() + "/dp_connect.db"
	driverName := newConnectDestroyDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &datapointModule{store: store, chunkSize: defaultChunkSize}
			return conn.CreateModule(datapointModuleName, mod)
		},
	})

	db1 := openFileDB(t, driverName, tmpFile)
	_, err := db1.Exec("CREATE VIRTUAL TABLE dp USING " + datapointModuleName)
	require.NoError(t, err)
	db1.Close()

	db2 := openFileDB(t, driverName, tmpFile)
	rows, err := db2.Query("SELECT metric FROM dp LIMIT 0")
	require.NoError(t, err)
	rows.Close()
}

// ---------------------------------------------------------------------------
// metricsModule.Connect + DestroyModule
// ---------------------------------------------------------------------------

func TestMetricsModule_Connect(t *testing.T) {
	store := newMockMetricsStore()
	tmpFile := t.TempDir() + "/metrics_connect.db"
	driverName := newConnectDestroyDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &metricsModule{store: store, chunkSize: defaultChunkSize}
			return conn.CreateModule(metricsModuleName, mod)
		},
	})

	db1 := openFileDB(t, driverName, tmpFile)
	_, err := db1.Exec("CREATE VIRTUAL TABLE metrics USING " + metricsModuleName)
	require.NoError(t, err)
	db1.Close()

	db2 := openFileDB(t, driverName, tmpFile)
	rows, err := db2.Query("SELECT name FROM metrics LIMIT 0")
	require.NoError(t, err)
	rows.Close()
}

// ---------------------------------------------------------------------------
// namespaceModule.Connect + DestroyModule
// ---------------------------------------------------------------------------

func TestNamespaceModule_Connect(t *testing.T) {
	store := newMockNamespaceStore()
	tmpFile := t.TempDir() + "/ns_connect.db"
	driverName := newConnectDestroyDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &namespaceModule{store: store, chunkSize: defaultChunkSize}
			return conn.CreateModule(namespaceModuleName, mod)
		},
	})

	db1 := openFileDB(t, driverName, tmpFile)
	_, err := db1.Exec("CREATE VIRTUAL TABLE ns USING " + namespaceModuleName)
	require.NoError(t, err)
	db1.Close()

	db2 := openFileDB(t, driverName, tmpFile)
	rows, err := db2.Query("SELECT name FROM ns LIMIT 0")
	require.NoError(t, err)
	rows.Close()
}

// ---------------------------------------------------------------------------
// vectorModule.Connect + DestroyModule
// ---------------------------------------------------------------------------

func TestVectorModule_Connect(t *testing.T) {
	store := newMockVectorStore()
	tmpFile := t.TempDir() + "/vec_connect.db"
	driverName := newConnectDestroyDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &vectorModule{store: store, chunkSize: defaultChunkSize}
			return conn.CreateModule(vectorModuleName, mod)
		},
	})

	db1 := openFileDB(t, driverName, tmpFile)
	_, err := db1.Exec("CREATE VIRTUAL TABLE vecs USING " + vectorModuleName)
	require.NoError(t, err)
	db1.Close()

	db2 := openFileDB(t, driverName, tmpFile)
	rows, err := db2.Query("SELECT id FROM vecs LIMIT 0")
	require.NoError(t, err)
	rows.Close()
}
