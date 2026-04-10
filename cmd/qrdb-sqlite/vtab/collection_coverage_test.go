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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Driver name counter for collection coverage tests
// ---------------------------------------------------------------------------

var collectionCovDriverCounter struct {
	sync.Mutex
	n int
}

func newCollectionCovDriverName() string {
	collectionCovDriverCounter.Lock()
	defer collectionCovDriverCounter.Unlock()
	collectionCovDriverCounter.n++
	return fmt.Sprintf("sqlite3_col_cov_test_%d", collectionCovDriverCounter.n)
}

// setupCollectionCovDB is like setupCollectionDB but uses a separate counter
// to avoid conflicts.
func setupCollectionCovDB(t *testing.T, meta *collectionMeta) (*sql.DB, *mockCollectionStore) {
	t.Helper()
	store := newMockCollectionStore()
	driverName := newCollectionCovDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &collectionModule{
				meta:      meta,
				store:     store,
				chunkSize: defaultChunkSize,
			}
			return conn.CreateModule(collectionModuleName, mod)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("CREATE VIRTUAL TABLE docs USING qrdb_collection")
	require.NoError(t, err)
	return db, store
}

// ---------------------------------------------------------------------------
// Connect — exercised when a second sql.Open is called on the same driver
// ---------------------------------------------------------------------------

func TestCollectionTable_Connect(t *testing.T) {
	// Connect is called the second time the same virtual table is opened on a
	// driver that has already run Create. We exercise it by opening a second
	// connection to the same in-memory database within the same sql.DB pool.
	meta := &collectionMeta{
		name:     "conntable",
		database: "testdb",
		schema:   map[string]string{"val": "VARCHAR"},
	}
	store := newMockCollectionStore()
	driverName := newCollectionCovDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &collectionModule{meta: meta, store: store, chunkSize: defaultChunkSize}
			return conn.CreateModule(collectionModuleName, mod)
		},
	})

	// Open with MaxOpenConns = 2 to force SQLite to create two connections and
	// invoke Connect on the second attach.
	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(2)

	_, err = db.Exec("CREATE VIRTUAL TABLE docs USING qrdb_collection")
	require.NoError(t, err)

	// Run a query that forces connection pool to acquire a second connection.
	rows, err := db.Query("SELECT _id FROM docs LIMIT 0")
	require.NoError(t, err)
	rows.Close()
}

// ---------------------------------------------------------------------------
// Destroy — exercised via DROP TABLE
// ---------------------------------------------------------------------------

func TestCollectionTable_Destroy(t *testing.T) {
	meta := &collectionMeta{
		name:     "destroyme",
		database: "db",
		schema:   map[string]string{"x": "VARCHAR"},
	}
	db, _ := setupCollectionCovDB(t, meta)

	_, err := db.Exec("DROP TABLE docs")
	require.NoError(t, err, "Destroy must not return error")
}

// ---------------------------------------------------------------------------
// DestroyModule — called implicitly; verified via module lifecycle
// ---------------------------------------------------------------------------

func TestCollectionTable_DestroyModule(t *testing.T) {
	// DestroyModule is called when the driver connection closes. We verify it
	// does not panic by calling it directly on a fresh module.
	meta := &collectionMeta{
		name:     "modlife",
		database: "db",
		schema:   map[string]string{"y": "BIGINT"},
	}
	store := newMockCollectionStore()
	mod := &collectionModule{meta: meta, store: store, chunkSize: defaultChunkSize}
	mod.DestroyModule() // direct call — must be a no-op
}

// ---------------------------------------------------------------------------
// resolveDocID — covers int64 registry hit, direct int64, uint64, float64,
// and unknown type branches
// ---------------------------------------------------------------------------

func TestCollectionTable_ResolveDocID_Branches(t *testing.T) {
	tbl := &collectionTable{
		meta:      &collectionMeta{name: "t", database: "db", schema: map[string]string{}},
		store:     newMockCollectionStore(),
		chunkSize: defaultChunkSize,
	}

	// Register a rowid → docID mapping.
	tbl.registerRowid(10, 42)

	tests := []struct {
		name      string
		input     interface{}
		wantID    uint64
		wantFound bool
	}{
		{"int64 registry hit", int64(10), 42, true},
		{"int64 direct positive", int64(99), 99, true},
		{"int64 zero", int64(0), 0, false},
		{"int64 negative", int64(-1), 0, false},
		{"uint64 positive", uint64(77), 77, true},
		{"uint64 zero", uint64(0), 0, false},
		{"float64 positive", float64(33), 33, true},
		{"float64 zero", float64(0), 0, false},
		{"nil", nil, 0, false},
		{"string unknown", "foo", 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, ok := tbl.resolveDocID(tc.input)
			assert.Equal(t, tc.wantFound, ok)
			assert.Equal(t, tc.wantID, id)
		})
	}
}

// ---------------------------------------------------------------------------
// writeColumnValue — covers int, int32, float32, bool, []byte, string, default
// ---------------------------------------------------------------------------

func TestWriteColumnValue_TypeCoverage(t *testing.T) {
	// We exercise writeColumnValue through the collection SELECT path. Each
	// document field uses a different Go type to exercise all branches.
	meta := &collectionMeta{
		name:     "types",
		database: "db",
		schema: map[string]string{
			"txt":   "VARCHAR",
			"num":   "BIGINT",
			"score": "DOUBLE",
			"flag":  "BOOLEAN",
			"data":  "BLOB",
		},
	}
	db, store := setupCollectionCovDB(t, meta)

	store.mu.Lock()
	store.docs[1] = map[string]any{
		"txt":   "hello",
		"num":   int64(42),
		"score": float64(3.14),
		"flag":  int64(1), // BOOLEAN stored as integer
		"data":  []byte{0xDE, 0xAD},
	}
	store.mu.Unlock()

	rows, err := db.Query("SELECT txt, num, score, flag, data FROM docs WHERE _id = ?", int64(1))
	require.NoError(t, err)
	defer rows.Close()

	require.True(t, rows.Next())
	var txt string
	var num int64
	var score float64
	var flag int64
	var data []byte
	require.NoError(t, rows.Scan(&txt, &num, &score, &flag, &data))
	assert.Equal(t, "hello", txt)
	assert.Equal(t, int64(42), num)
	assert.InDelta(t, 3.14, score, 0.001)
	assert.Equal(t, int64(1), flag)
	assert.Equal(t, []byte{0xDE, 0xAD}, data)
}

// TestWriteColumnValue_AdditionalTypes exercises int, int32, uint64, float32,
// bool, and default branches via actual collection SELECT queries.
func TestWriteColumnValue_AdditionalTypes(t *testing.T) {
	meta := &collectionMeta{
		name:     "addtypes",
		database: "db",
		schema: map[string]string{
			"a": "BIGINT",  // int
			"b": "BIGINT",  // int32
			"c": "BIGINT",  // uint64
			"d": "DOUBLE",  // float32
			"e": "BOOLEAN", // bool false
			"f": "BOOLEAN", // bool true
		},
	}
	db, store := setupCollectionCovDB(t, meta)

	store.mu.Lock()
	store.docs[1] = map[string]any{
		"a": int(10),
		"b": int32(20),
		"c": uint64(30),
		"d": float32(1.5),
		"e": false,
		"f": true,
	}
	store.mu.Unlock()

	rows, err := db.Query("SELECT a, b, c, d, e, f FROM docs WHERE _id = ?", int64(1))
	require.NoError(t, err)
	defer rows.Close()

	require.True(t, rows.Next())
	var a, b, c int64
	var d float64
	var e, f int64
	require.NoError(t, rows.Scan(&a, &b, &c, &d, &e, &f))
	assert.Equal(t, int64(10), a)
	assert.Equal(t, int64(20), b)
	assert.Equal(t, int64(30), c)
	assert.InDelta(t, 1.5, d, 0.01)
	assert.Equal(t, int64(0), e) // false → 0
	assert.Equal(t, int64(1), f) // true → 1
}

// TestWriteColumnValue_DefaultType exercises the default (toString) branch via
// a field value whose type has no explicit case in writeColumnValue.
// toString returns "" for unknown types, which go-sqlite3 stores as NULL for
// an empty ResultText; we use sql.NullString to accept that.
func TestWriteColumnValue_DefaultType(t *testing.T) {
	meta := &collectionMeta{
		name:     "deftype",
		database: "db",
		schema:   map[string]string{"obj": "VARCHAR"},
	}
	db, store := setupCollectionCovDB(t, meta)

	// A struct value falls into the default case in writeColumnValue.
	type customType struct{ X int }
	store.mu.Lock()
	store.docs[1] = map[string]any{"obj": customType{X: 42}}
	store.mu.Unlock()

	rows, err := db.Query("SELECT obj FROM docs WHERE _id = ?", int64(1))
	require.NoError(t, err)
	defer rows.Close()

	require.True(t, rows.Next())
	var s sql.NullString
	require.NoError(t, rows.Scan(&s))
	// The default branch calls toString; the scan must succeed (value may be NULL or empty).
}

// TestWriteColumnValue_NullValue verifies that nil is written as SQL NULL.
func TestWriteColumnValue_NullValue(t *testing.T) {
	meta := &collectionMeta{
		name:     "nulltest",
		database: "db",
		schema:   map[string]string{"val": "VARCHAR"},
	}
	db, store := setupCollectionCovDB(t, meta)

	store.mu.Lock()
	store.docs[1] = map[string]any{"val": nil}
	store.mu.Unlock()

	rows, err := db.Query("SELECT val FROM docs WHERE _id = ?", int64(1))
	require.NoError(t, err)
	defer rows.Close()

	require.True(t, rows.Next())
	var val sql.NullString
	require.NoError(t, rows.Scan(&val))
	assert.False(t, val.Valid)
}

// ---------------------------------------------------------------------------
// registerCurrentID — called during Next iteration
// ---------------------------------------------------------------------------

func TestCollectionTable_RegisterCurrentID_ViaIteration(t *testing.T) {
	// Iterating multiple rows forces registerCurrentID to be called on each
	// Next step — exercising the non-EOF branch.
	meta := &collectionMeta{
		name:     "iter",
		database: "db",
		schema:   map[string]string{"n": "BIGINT"},
	}
	db, store := setupCollectionCovDB(t, meta)

	store.mu.Lock()
	for i := uint64(1); i <= 5; i++ {
		store.docs[i] = map[string]any{"n": int64(i)}
	}
	store.mu.Unlock()

	rows, err := db.Query("SELECT _id FROM docs ORDER BY _id")
	require.NoError(t, err)
	defer rows.Close()

	var count int
	for rows.Next() {
		count++
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, 5, count)
}

// ---------------------------------------------------------------------------
// Next — exercises the cursor advance with multiple rows
// ---------------------------------------------------------------------------

func TestCollectionCursor_Next_MultipleDocs(t *testing.T) {
	meta := basicMeta()
	db, store := setupCollectionCovDB(t, meta)

	store.mu.Lock()
	store.docs[10] = map[string]any{"name": "Alice", "age": int64(30)}
	store.docs[20] = map[string]any{"name": "Bob", "age": int64(25)}
	store.docs[30] = map[string]any{"name": "Carol", "age": int64(35)}
	store.mu.Unlock()

	// SELECT all — forces Next to be called multiple times.
	rows, err := db.Query("SELECT _id, name FROM docs")
	require.NoError(t, err)
	defer rows.Close()

	var names []string
	for rows.Next() {
		var id int64
		var name string
		require.NoError(t, rows.Scan(&id, &name))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())
	assert.ElementsMatch(t, []string{"Alice", "Bob", "Carol"}, names)
}

// ---------------------------------------------------------------------------
// toUint64 — edge cases not covered elsewhere
// ---------------------------------------------------------------------------

func TestToUint64_Branches(t *testing.T) {
	tests := []struct {
		input interface{}
		want  uint64
	}{
		{nil, 0},
		{int64(5), 5},
		{int64(-1), 0},
		{uint64(99), 99},
		{float64(7.9), 7},
		{float64(-1.5), 0},
		{int(3), 3},
		{int(-3), 0},
		{"str", 0},
	}
	for _, tc := range tests {
		got := toUint64(tc.input)
		assert.Equal(t, tc.want, got, "input=%v", tc.input)
	}
}
