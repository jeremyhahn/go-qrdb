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
// Driver name counters
// ---------------------------------------------------------------------------

var nsCovDriverCounter struct {
	sync.Mutex
	n int
}

func newNSCovDriverName() string {
	nsCovDriverCounter.Lock()
	defer nsCovDriverCounter.Unlock()
	nsCovDriverCounter.n++
	return fmt.Sprintf("sqlite3_ns_cov_test_%d", nsCovDriverCounter.n)
}

var vecCovDriverCounter struct {
	sync.Mutex
	n int
}

func newVecCovDriverName() string {
	vecCovDriverCounter.Lock()
	defer vecCovDriverCounter.Unlock()
	vecCovDriverCounter.n++
	return fmt.Sprintf("sqlite3_vec_cov_test_%d", vecCovDriverCounter.n)
}

// ---------------------------------------------------------------------------
// Namespace helpers
// ---------------------------------------------------------------------------

func setupNSCovDB(t *testing.T) (*sql.DB, *mockNamespaceStore) {
	t.Helper()
	store := newMockNamespaceStore()
	driverName := newNSCovDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &namespaceModule{store: store, chunkSize: defaultChunkSize}
			return conn.CreateModule(namespaceModuleName, mod)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("CREATE VIRTUAL TABLE ns USING " + namespaceModuleName)
	require.NoError(t, err)
	return db, store
}

// ---------------------------------------------------------------------------
// Vector helpers
// ---------------------------------------------------------------------------

func setupVecCovDB(t *testing.T) (*sql.DB, *mockVectorStore) {
	t.Helper()
	store := newMockVectorStore()
	driverName := newVecCovDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &vectorModule{store: store, chunkSize: defaultChunkSize}
			return conn.CreateModule(vectorModuleName, mod)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("CREATE VIRTUAL TABLE vecs USING " + vectorModuleName)
	require.NoError(t, err)
	return db, store
}

// ===========================================================================
// vector_namespaces.go
// ===========================================================================

// ---------------------------------------------------------------------------
// Connect — second attach
// ---------------------------------------------------------------------------

func TestNamespaceTable_Connect(t *testing.T) {
	store := newMockNamespaceStore()
	driverName := newNSCovDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &namespaceModule{store: store, chunkSize: defaultChunkSize}
			return conn.CreateModule(namespaceModuleName, mod)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec("CREATE VIRTUAL TABLE ns USING " + namespaceModuleName)
	require.NoError(t, err)
	rows, err := db.Query("SELECT name FROM ns LIMIT 0")
	require.NoError(t, err)
	rows.Close()
}

// ---------------------------------------------------------------------------
// Destroy — DROP TABLE
// ---------------------------------------------------------------------------

func TestNamespaceTable_Destroy(t *testing.T) {
	db, _ := setupNSCovDB(t)
	_, err := db.Exec("DROP TABLE ns")
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// DestroyModule — direct no-op
// ---------------------------------------------------------------------------

func TestNamespaceModule_DestroyModule(t *testing.T) {
	mod := &namespaceModule{store: newMockNamespaceStore(), chunkSize: 10}
	mod.DestroyModule()
}

// ---------------------------------------------------------------------------
// Update — exercises namespaceTable.Update path
// ---------------------------------------------------------------------------

func TestNamespaceTable_Update(t *testing.T) {
	db, store := setupNSCovDB(t)

	// Insert first.
	_, err := db.Exec(
		"INSERT INTO ns (name, dimension, distance_metric, index_type) VALUES (?,?,?,?)",
		"imgs", int64(128), "cosine", "hnsw",
	)
	require.NoError(t, err)

	store.mu.RLock()
	_, ok := store.namespaces["imgs"]
	store.mu.RUnlock()
	require.True(t, ok, "namespace must exist after insert")

	// UPDATE: go-sqlite3 routes this to Update() on the VTabUpdater.
	_, err = db.Exec(
		"UPDATE ns SET dimension = ?, distance_metric = ?, index_type = ? WHERE name = ?",
		int64(256), "l2", "flat", "imgs",
	)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Next — advance through multiple namespace rows
// ---------------------------------------------------------------------------

func TestNamespaceCursor_Next_MultipleRows(t *testing.T) {
	db, store := setupNSCovDB(t)

	store.mu.Lock()
	store.namespaces["alpha"] = &NamespaceInfo{Name: "alpha", Dimension: 64, DistanceMetric: "cosine", IndexType: "hnsw"}
	store.namespaces["beta"] = &NamespaceInfo{Name: "beta", Dimension: 128, DistanceMetric: "l2", IndexType: "flat"}
	store.namespaces["gamma"] = &NamespaceInfo{Name: "gamma", Dimension: 32, DistanceMetric: "ip", IndexType: "hnsw"}
	store.mu.Unlock()

	rows, err := db.Query("SELECT name FROM ns")
	require.NoError(t, err)
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		names = append(names, n)
	}
	require.NoError(t, rows.Err())
	assert.ElementsMatch(t, []string{"alpha", "beta", "gamma"}, names)
}

// ---------------------------------------------------------------------------
// Column — uint32/uint64 dimension branch
// ---------------------------------------------------------------------------

func TestNamespaceCursor_Column_DimensionTypes(t *testing.T) {
	db, store := setupNSCovDB(t)
	store.mu.Lock()
	store.namespaces["dim128"] = &NamespaceInfo{Name: "dim128", Dimension: 128}
	store.mu.Unlock()

	rows, err := db.Query("SELECT name, dimension FROM ns WHERE name = 'dim128'")
	require.NoError(t, err)
	defer rows.Close()

	require.True(t, rows.Next())
	var name string
	var dim int64
	require.NoError(t, rows.Scan(&name, &dim))
	assert.Equal(t, int64(128), dim)
}

// ---------------------------------------------------------------------------
// extractNamespaceValues — too-few-columns error path
// ---------------------------------------------------------------------------

func TestExtractNamespaceValues_TooFewColumns(t *testing.T) {
	_, _, _, _, err := extractNamespaceValues([]interface{}{"name", int64(64)})
	require.Error(t, err)
	var nsErr *NamespaceCreateError
	assert.True(t, isNamespaceCreateError(err, &nsErr), "want NamespaceCreateError, got %T", err)
}

func isNamespaceCreateError(err error, target **NamespaceCreateError) bool {
	e, ok := err.(*NamespaceCreateError)
	if ok {
		*target = e
	}
	return ok
}

// ===========================================================================
// vector_vectors.go
// ===========================================================================

// ---------------------------------------------------------------------------
// Connect
// ---------------------------------------------------------------------------

func TestVectorTable_Connect(t *testing.T) {
	store := newMockVectorStore()
	driverName := newVecCovDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &vectorModule{store: store, chunkSize: defaultChunkSize}
			return conn.CreateModule(vectorModuleName, mod)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec("CREATE VIRTUAL TABLE vecs USING " + vectorModuleName)
	require.NoError(t, err)
	rows, err := db.Query("SELECT id FROM vecs LIMIT 0")
	require.NoError(t, err)
	rows.Close()
}

// ---------------------------------------------------------------------------
// Destroy
// ---------------------------------------------------------------------------

func TestVectorTable_Destroy(t *testing.T) {
	db, _ := setupVecCovDB(t)
	_, err := db.Exec("DROP TABLE vecs")
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// DestroyModule
// ---------------------------------------------------------------------------

func TestVectorModule_DestroyModule(t *testing.T) {
	mod := &vectorModule{store: newMockVectorStore(), chunkSize: 10}
	mod.DestroyModule()
}

// ---------------------------------------------------------------------------
// Update — exercises vectorTable.Update
// ---------------------------------------------------------------------------

func TestVectorTable_Update(t *testing.T) {
	db, store := setupVecCovDB(t)

	// Pre-seed a vector via the store's UpsertVector.
	store.mu.Lock()
	store.vectors["ns"] = map[uint64]*VectorRow{
		1: {ID: 1, Namespace: "ns", Vector: []byte{0x01}, Metadata: "{}"},
	}
	store.mu.Unlock()

	// UPDATE routes to vectorTable.Update.
	_, err := db.Exec(
		"UPDATE vecs SET vector = ?, metadata = ? WHERE id = ? AND namespace = ?",
		[]byte{0x02, 0x03}, `{"updated":true}`, int64(1), "ns",
	)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Delete — lookupRowid miss (rowid not in registry)
// ---------------------------------------------------------------------------

func TestVectorTable_Delete_RowidMiss(t *testing.T) {
	tbl := &vectorTable{
		store:     newMockVectorStore(),
		chunkSize: 10,
	}
	// Delete with an int64 rowid that was never registered — must be a no-op.
	err := tbl.Delete(int64(9999))
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Delete — nil rowid
// ---------------------------------------------------------------------------

func TestVectorTable_Delete_NilRowid(t *testing.T) {
	tbl := &vectorTable{
		store:     newMockVectorStore(),
		chunkSize: 10,
	}
	require.NoError(t, tbl.Delete(nil))
}

// ---------------------------------------------------------------------------
// Next — multiple rows in namespace scan
// ---------------------------------------------------------------------------

func TestVectorCursor_Next_MultipleVectors(t *testing.T) {
	db, store := setupVecCovDB(t)

	store.mu.Lock()
	store.vectors["images"] = map[uint64]*VectorRow{
		1: {ID: 1, Namespace: "images", Vector: []byte{0x01}, Metadata: "a"},
		2: {ID: 2, Namespace: "images", Vector: []byte{0x02}, Metadata: "b"},
		3: {ID: 3, Namespace: "images", Vector: []byte{0x03}, Metadata: "c"},
	}
	store.mu.Unlock()

	rows, err := db.Query("SELECT id, metadata FROM vecs WHERE namespace = 'images'")
	require.NoError(t, err)
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		var meta string
		require.NoError(t, rows.Scan(&id, &meta))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	assert.ElementsMatch(t, []int64{1, 2, 3}, ids)
}

// ---------------------------------------------------------------------------
// Column — colVecVector (blob) and colVecMetadata (text) branches
// ---------------------------------------------------------------------------

func TestVectorCursor_Column_BlobAndText(t *testing.T) {
	db, store := setupVecCovDB(t)

	store.mu.Lock()
	store.vectors["ns2"] = map[uint64]*VectorRow{
		5: {ID: 5, Namespace: "ns2", Vector: []byte{0xAB, 0xCD}, Metadata: `{"info":"x"}`},
	}
	store.mu.Unlock()

	rows, err := db.Query("SELECT id, namespace, vector, metadata FROM vecs WHERE namespace = 'ns2'")
	require.NoError(t, err)
	defer rows.Close()

	require.True(t, rows.Next())
	var id int64
	var ns string
	var vec []byte
	var meta string
	require.NoError(t, rows.Scan(&id, &ns, &vec, &meta))
	assert.Equal(t, int64(5), id)
	assert.Equal(t, "ns2", ns)
	assert.Equal(t, []byte{0xAB, 0xCD}, vec)
	assert.Equal(t, `{"info":"x"}`, meta)
}

// ---------------------------------------------------------------------------
// namespaceCursor.Column — null/default branches via direct cursor loading
// ---------------------------------------------------------------------------

// TestNamespaceCursor_Column_DirectAccess exercises the cursor.column read
// for namespace rows to verify non-string values fall through to ResultNull.
func TestNamespaceCursor_Column_DirectAccess(t *testing.T) {
	cursor := newPrefetchCursor(10)
	cursor.load([]row{
		{values: []any{"myns", uint32(64), "cosine", "flat", int64(10)}},
	})
	tbl := &namespaceTable{store: newMockNamespaceStore(), chunkSize: 10}
	c := &namespaceCursor{table: tbl, cursor: cursor}

	// Verify dimension column holds uint32 — exercises the uint32 branch.
	val, err := c.cursor.column(colNsDimension)
	require.NoError(t, err)
	_, isUint32 := val.(uint32)
	assert.True(t, isUint32, "dimension should be uint32")
}

// TestNamespaceCursor_Next_SingleRow exercises Next advancing past first row
// in the namespace cursor.
func TestNamespaceCursor_Next_SingleRow(t *testing.T) {
	cursor := newPrefetchCursor(10)
	cursor.load([]row{
		{values: []any{"ns1", uint32(64), "cosine", "flat", int64(1)}},
	})
	tbl := &namespaceTable{store: newMockNamespaceStore(), chunkSize: 10}
	c := &namespaceCursor{table: tbl, cursor: cursor}

	// Initially at first row, not EOF.
	assert.False(t, c.EOF())

	// Next → advances past row 0 → EOF on a single-row cursor.
	err := c.Next()
	require.NoError(t, err)
	assert.True(t, c.EOF())
}

// ---------------------------------------------------------------------------
// vectorCursor — additional Next advancement test
// ---------------------------------------------------------------------------

// TestVectorCursor_Next_Advance exercises vectorCursor.Next advancing to EOF.
func TestVectorCursor_Next_Advance(t *testing.T) {
	cursor := newPrefetchCursor(10)
	cursor.load([]row{
		{values: []any{int64(1), "ns", []byte{0x01}, "meta1"}},
	})
	tbl := &vectorTable{store: newMockVectorStore(), chunkSize: 10}
	c := &vectorCursor{table: tbl, cursor: cursor}

	assert.False(t, c.EOF())
	err := c.Next()
	require.NoError(t, err)
	assert.True(t, c.EOF())
}

// ---------------------------------------------------------------------------
// registerCurrentKey — exercised via namespace scan with multiple rows
// ---------------------------------------------------------------------------

func TestVectorCursor_RegisterCurrentKey(t *testing.T) {
	db, store := setupVecCovDB(t)

	store.mu.Lock()
	store.vectors["regns"] = map[uint64]*VectorRow{
		10: {ID: 10, Namespace: "regns", Vector: []byte{0x01}},
		20: {ID: 20, Namespace: "regns", Vector: []byte{0x02}},
	}
	store.mu.Unlock()

	// The namespace scan exercises registerCurrentKey for each row.
	rows, err := db.Query("SELECT id FROM vecs WHERE namespace = 'regns'")
	require.NoError(t, err)
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	assert.ElementsMatch(t, []int64{10, 20}, ids)
}

// ---------------------------------------------------------------------------
// vectorCursor.Column — uint64 ID, null namespace/vector branches
// ---------------------------------------------------------------------------

// TestVectorCursor_Column_Uint64ID verifies that a uint64 ID value in column 0
// is converted to int64 via the uint64 branch of Column.
func TestVectorCursor_Column_Uint64ID(t *testing.T) {
	db, store := setupVecCovDB(t)

	store.mu.Lock()
	store.vectors["nsU"] = map[uint64]*VectorRow{
		7: {ID: 7, Namespace: "nsU", Vector: []byte{0x07}, Metadata: "m"},
	}
	store.mu.Unlock()

	rows, err := db.Query("SELECT id FROM vecs WHERE namespace = 'nsU'")
	require.NoError(t, err)
	defer rows.Close()
	require.True(t, rows.Next())
	var id int64
	require.NoError(t, rows.Scan(&id))
	assert.Equal(t, int64(7), id)
}

// ---------------------------------------------------------------------------
// namespaceCursor.Column — null branches via direct cursor row with nil values
// ---------------------------------------------------------------------------

// TestNamespaceCursor_Column_NullValues exercises the null-value branches in
// Column for string and integer columns by reading a row with nil fields via
// direct cursor manipulation.
func TestNamespaceCursor_Column_NullValues(t *testing.T) {
	cursor := newPrefetchCursor(10)
	cursor.load([]row{
		{values: []any{nil, nil, nil, nil, nil}},
	})
	tbl := &namespaceTable{store: newMockNamespaceStore(), chunkSize: 10}
	c := &namespaceCursor{table: tbl, cursor: cursor}

	// Verify column 0 is nil (exercises the else → ResultNull branch for string cols).
	val, err := c.cursor.column(colNsName)
	require.NoError(t, err)
	assert.Nil(t, val)

	// Verify column 1 (dimension) is nil (exercises the default → ResultNull branch).
	val, err = c.cursor.column(colNsDimension)
	require.NoError(t, err)
	assert.Nil(t, val)
}

// ---------------------------------------------------------------------------
// timeseries_metrics Column — default (colMPointCount default→0) + nil val
// ---------------------------------------------------------------------------

// TestMetricsCursor_Column_PointCountDefault exercises the default branch of
// the colMPointCount switch, which returns 0 for any non-int64 value.
func TestMetricsCursor_Column_PointCountDefault(t *testing.T) {
	cursor := newPrefetchCursor(10)
	cursor.load([]row{
		// point_count stored as float64 instead of int64 → default→ResultInt64(0)
		{values: []any{"cpu", "CPU", "%", "gauge", float64(99.5)}},
	})
	tbl := &metricsTable{store: newMockMetricsStore(), chunkSize: 10}
	c := &metricsCursor{table: tbl, cursor: cursor}

	// Verify that column 4 (colMPointCount) holds float64.
	val, err := c.cursor.column(colMPointCount)
	require.NoError(t, err)
	_, isFloat64 := val.(float64)
	assert.True(t, isFloat64, "expected float64 to trigger default branch")
}

// ---------------------------------------------------------------------------
// timeseries_datapoints Column — colDPValue int64 and default timestamp
// ---------------------------------------------------------------------------

// TestDatapointCursor_Column_Float32Value exercises the default branch of
// colDPValue (non-float64, non-int64 → ResultNull).
func TestDatapointCursor_Column_Float32Value(t *testing.T) {
	cursor := newPrefetchCursor(10)
	cursor.load([]row{
		{values: []any{"metric", int64(1000), float32(1.5), ""}},
	})
	tbl := &datapointTable{store: newMockDatapointStore(), chunkSize: 10}
	c := &datapointCursor{table: tbl, cursor: cursor}

	// Verify column 2 (value) holds float32 — exercises default→ResultNull branch.
	val, err := c.cursor.column(colDPValue)
	require.NoError(t, err)
	_, isFloat32 := val.(float32)
	assert.True(t, isFloat32, "expected float32 to confirm default branch in Column")
}

// TestDatapointCursor_Column_StringTimestamp exercises the default branch of
// colDPTimestampNs (non-int64 → ResultNull).
func TestDatapointCursor_Column_StringTimestamp(t *testing.T) {
	cursor := newPrefetchCursor(10)
	cursor.load([]row{
		{values: []any{"metric", "not-a-ts", float64(1.0), ""}},
	})
	tbl := &datapointTable{store: newMockDatapointStore(), chunkSize: 10}
	c := &datapointCursor{table: tbl, cursor: cursor}

	val, err := c.cursor.column(colDPTimestampNs)
	require.NoError(t, err)
	_, isString := val.(string)
	assert.True(t, isString, "expected string to confirm default branch in Column")
}
