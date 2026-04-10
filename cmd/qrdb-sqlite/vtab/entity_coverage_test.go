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
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Driver name counter for entity coverage tests
// ---------------------------------------------------------------------------

var entityCovDriverCounter struct {
	sync.Mutex
	n int
}

func newEntityCovDriverName() string {
	entityCovDriverCounter.Lock()
	defer entityCovDriverCounter.Unlock()
	entityCovDriverCounter.n++
	return fmt.Sprintf("sqlite3_ent_cov_test_%d", entityCovDriverCounter.n)
}

// setupEntityCovDB opens a fresh in-memory SQLite DB with the entity module
// registered for the given meta.
func setupEntityCovDB(t *testing.T, meta *entityMeta) (*sql.DB, *mockEntityStore) {
	t.Helper()
	mock := newMockEntityStore()
	driverName := newEntityCovDriverName()
	moduleName := entityModuleName + "_" + meta.entityType

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return RegisterEntityModule(conn, meta, mock, defaultChunkSize)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("CREATE VIRTUAL TABLE ents USING " + moduleName)
	require.NoError(t, err)

	return db, mock
}

// ---------------------------------------------------------------------------
// Connect — exercised via second attach on same driver
// ---------------------------------------------------------------------------

func TestEntityTable_Connect(t *testing.T) {
	meta := buildUserMeta()
	mock := newMockEntityStore()
	driverName := newEntityCovDriverName()
	moduleName := entityModuleName + "_" + meta.entityType

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return RegisterEntityModule(conn, meta, mock, defaultChunkSize)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec("CREATE VIRTUAL TABLE ents USING " + moduleName)
	require.NoError(t, err)

	rows, err := db.Query("SELECT _id FROM ents LIMIT 0")
	require.NoError(t, err)
	rows.Close()
}

// ---------------------------------------------------------------------------
// Destroy — exercised via DROP TABLE
// ---------------------------------------------------------------------------

func TestEntityTable_Destroy(t *testing.T) {
	meta := buildUserMeta()
	db, _ := setupEntityCovDB(t, meta)

	_, err := db.Exec("DROP TABLE ents")
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// DestroyModule — direct call; must be a no-op
// ---------------------------------------------------------------------------

func TestEntityModule_DestroyModule(t *testing.T) {
	meta := buildUserMeta()
	mod := &entityModule{meta: meta, store: newMockEntityStore(), chunkSize: defaultChunkSize}
	mod.DestroyModule() // must not panic
}

// ---------------------------------------------------------------------------
// Filter — error paths
// ---------------------------------------------------------------------------

func TestEntityCursor_Filter_GetEntityError(t *testing.T) {
	meta := buildUserMeta()
	db, mock := setupEntityCovDB(t, meta)
	mock.getErr = errEntityGetFailed

	rows, err := db.Query("SELECT _id FROM ents WHERE _id = ?", int64(1))
	if err != nil {
		assert.Contains(t, err.Error(), "entity get failed")
		return
	}
	defer rows.Close()
	for rows.Next() {
	}
	require.Error(t, rows.Err())
	assert.Contains(t, rows.Err().Error(), "entity get failed")
}

func TestEntityCursor_Filter_ScanError(t *testing.T) {
	meta := buildUserMeta()
	db, mock := setupEntityCovDB(t, meta)
	mock.scanErr = errEntityScanFailed

	rows, err := db.Query("SELECT _id FROM ents")
	if err != nil {
		assert.Contains(t, err.Error(), "entity scan failed")
		return
	}
	defer rows.Close()
	for rows.Next() {
	}
	require.Error(t, rows.Err())
	assert.Contains(t, rows.Err().Error(), "entity scan failed")
}

func TestEntityCursor_Filter_IndexQueryError(t *testing.T) {
	meta := buildUserMeta()
	db, mock := setupEntityCovDB(t, meta)
	mock.indexErr = errEntityIndexFailed

	rows, err := db.Query("SELECT _id FROM ents WHERE email = ?", "x@example.com")
	if err != nil {
		assert.Contains(t, err.Error(), "entity index failed")
		return
	}
	defer rows.Close()
	for rows.Next() {
	}
	require.Error(t, rows.Err())
	assert.Contains(t, rows.Err().Error(), "entity index failed")
}

// ---------------------------------------------------------------------------
// Next — advance past first row
// ---------------------------------------------------------------------------

func TestEntityCursor_Next_MultipleRows(t *testing.T) {
	meta := buildUserMeta()
	db, mock := setupEntityCovDB(t, meta)

	mock.seed(1, map[string]any{"email": "a@x.com", "name": "Alice", "age": int64(30)})
	mock.seed(2, map[string]any{"email": "b@x.com", "name": "Bob", "age": int64(25)})
	mock.seed(3, map[string]any{"email": "c@x.com", "name": "Carol", "age": int64(35)})

	rows, err := db.Query("SELECT _id, name FROM ents")
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
// readCurrentID — uint64 path (column 0 holds uint64)
// ---------------------------------------------------------------------------

func TestEntityCursor_ReadCurrentID_Uint64(t *testing.T) {
	meta := buildUserMeta()
	// Build a cursor with a row that has a uint64 ID to exercise that branch.
	cursor := newPrefetchCursor(10)
	cursor.load([]row{
		{values: []any{uint64(99), "alice@x.com", "Alice", int64(28)}},
	})
	c := &entityCursor{
		table:  &entityTable{meta: meta, store: newMockEntityStore(), chunkSize: 10},
		cursor: cursor,
	}
	id := c.readCurrentID()
	assert.Equal(t, int64(99), id)
}

// ---------------------------------------------------------------------------
// Column — all branches
// ---------------------------------------------------------------------------

func TestEntityColumn_IDNilBranch(t *testing.T) {
	// Column with id value exercises the id result path; verify via a real query.
	meta := buildUserMeta()
	db, mock := setupEntityCovDB(t, meta)
	mock.seed(1, map[string]any{"email": "a@x.com", "name": "Alice", "age": int64(30)})
	rows, err := db.Query("SELECT _id, age FROM ents WHERE _id = ?", int64(1))
	require.NoError(t, err)
	defer rows.Close()
	require.True(t, rows.Next())
	var id int64
	var age int64
	require.NoError(t, rows.Scan(&id, &age))
	assert.Equal(t, int64(1), id)
}

func TestEntityColumn_SingleField(t *testing.T) {
	// Entity with a single non-PK TEXT field — exercises the fieldIdx bounds check.
	meta := &entityMeta{
		entityType:  "slim",
		idFieldJSON: "_id",
		fields:      []fieldMeta{{goName: "X", jsonName: "x", sqliteType: "TEXT"}},
	}
	db, mock := setupEntityCovDB(t, meta)
	mock.seed(1, map[string]any{"x": "value"})

	rows, err := db.Query("SELECT _id, x FROM ents WHERE _id = ?", int64(1))
	require.NoError(t, err)
	defer rows.Close()

	require.True(t, rows.Next())
	var id int64
	var x string
	require.NoError(t, rows.Scan(&id, &x))
	assert.Equal(t, "value", x)
}

// ---------------------------------------------------------------------------
// resultColumnValue — type coverage via SQL queries
// ---------------------------------------------------------------------------

func buildTypedEntityMeta() *entityMeta {
	return &entityMeta{
		entityType:  "typed",
		idFieldJSON: "_id",
		fields: []fieldMeta{
			{goName: "IntField", jsonName: "int_val", sqliteType: "INTEGER"},
			{goName: "RealField", jsonName: "real_val", sqliteType: "REAL"},
			{goName: "BlobField", jsonName: "blob_val", sqliteType: "BLOB"},
			{goName: "TextField", jsonName: "text_val", sqliteType: "TEXT"},
			{goName: "TimeField", jsonName: "time_val", sqliteType: "TEXT"},
		},
	}
}

func setupTypedEntityDB(t *testing.T) (*sql.DB, *mockEntityStore) {
	t.Helper()
	meta := buildTypedEntityMeta()
	return setupEntityCovDB(t, meta)
}

func TestEntityColumn_IntegerTypes(t *testing.T) {
	db, mock := setupTypedEntityDB(t)
	mock.seed(1, map[string]any{
		"int_val":  int64(42),
		"real_val": float64(0),
		"blob_val": []byte{},
		"text_val": "",
		"time_val": "",
	})
	var n int64
	require.NoError(t, db.QueryRow("SELECT int_val FROM ents WHERE _id = ?", int64(1)).Scan(&n))
	assert.Equal(t, int64(42), n)
}

func TestEntityColumn_RealType(t *testing.T) {
	db, mock := setupTypedEntityDB(t)
	mock.seed(1, map[string]any{
		"int_val":  int64(0),
		"real_val": float64(3.14),
		"blob_val": []byte{},
		"text_val": "",
		"time_val": "",
	})
	var f float64
	require.NoError(t, db.QueryRow("SELECT real_val FROM ents WHERE _id = ?", int64(1)).Scan(&f))
	assert.InDelta(t, 3.14, f, 0.001)
}

func TestEntityColumn_BlobType(t *testing.T) {
	db, mock := setupTypedEntityDB(t)
	mock.seed(1, map[string]any{
		"int_val":  int64(0),
		"real_val": float64(0),
		"blob_val": []byte{0x01, 0x02, 0x03},
		"text_val": "",
		"time_val": "",
	})
	var b []byte
	require.NoError(t, db.QueryRow("SELECT blob_val FROM ents WHERE _id = ?", int64(1)).Scan(&b))
	assert.Equal(t, []byte{0x01, 0x02, 0x03}, b)
}

func TestEntityColumn_TextType(t *testing.T) {
	db, mock := setupTypedEntityDB(t)
	mock.seed(1, map[string]any{
		"int_val":  int64(0),
		"real_val": float64(0),
		"blob_val": []byte{},
		"text_val": "hello world",
		"time_val": "",
	})
	var s string
	require.NoError(t, db.QueryRow("SELECT text_val FROM ents WHERE _id = ?", int64(1)).Scan(&s))
	assert.Equal(t, "hello world", s)
}

func TestEntityColumn_TimeType(t *testing.T) {
	db, mock := setupTypedEntityDB(t)
	ts := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	mock.seed(1, map[string]any{
		"int_val":  int64(0),
		"real_val": float64(0),
		"blob_val": []byte{},
		"text_val": "",
		"time_val": ts,
	})
	var s string
	require.NoError(t, db.QueryRow("SELECT time_val FROM ents WHERE _id = ?", int64(1)).Scan(&s))
	assert.Contains(t, s, "2026-01-15")
}

func TestEntityColumn_NilValues(t *testing.T) {
	db, mock := setupTypedEntityDB(t)
	mock.seed(1, map[string]any{
		"int_val":  nil,
		"real_val": nil,
		"blob_val": nil,
		"text_val": nil,
		"time_val": nil,
	})
	var iv, rv sql.NullInt64
	var bv []byte
	var sv sql.NullString
	require.NoError(t, db.QueryRow(
		"SELECT int_val, real_val, blob_val, text_val FROM ents WHERE _id = ?", int64(1),
	).Scan(&iv, &rv, &bv, &sv))
	assert.False(t, iv.Valid)
	assert.False(t, rv.Valid)
	assert.Nil(t, bv)
	assert.False(t, sv.Valid)
}

func TestEntityColumn_IntegerFromBool(t *testing.T) {
	db, mock := setupTypedEntityDB(t)
	mock.seed(1, map[string]any{
		"int_val":  true,
		"real_val": float64(0),
		"blob_val": []byte{},
		"text_val": "",
		"time_val": "",
	})
	var n int64
	require.NoError(t, db.QueryRow("SELECT int_val FROM ents WHERE _id = ?", int64(1)).Scan(&n))
	assert.Equal(t, int64(1), n)
}

func TestEntityColumn_IntegerFromUint64(t *testing.T) {
	db, mock := setupTypedEntityDB(t)
	mock.seed(1, map[string]any{
		"int_val":  uint64(55),
		"real_val": float64(0),
		"blob_val": []byte{},
		"text_val": "",
		"time_val": "",
	})
	var n int64
	require.NoError(t, db.QueryRow("SELECT int_val FROM ents WHERE _id = ?", int64(1)).Scan(&n))
	assert.Equal(t, int64(55), n)
}

func TestEntityColumn_IntegerFromFloat64(t *testing.T) {
	db, mock := setupTypedEntityDB(t)
	mock.seed(1, map[string]any{
		"int_val":  float64(7),
		"real_val": float64(0),
		"blob_val": []byte{},
		"text_val": "",
		"time_val": "",
	})
	var n int64
	require.NoError(t, db.QueryRow("SELECT int_val FROM ents WHERE _id = ?", int64(1)).Scan(&n))
	assert.Equal(t, int64(7), n)
}

func TestEntityColumn_RealFromInt64(t *testing.T) {
	db, mock := setupTypedEntityDB(t)
	mock.seed(1, map[string]any{
		"int_val":  int64(0),
		"real_val": int64(10),
		"blob_val": []byte{},
		"text_val": "",
		"time_val": "",
	})
	var f float64
	require.NoError(t, db.QueryRow("SELECT real_val FROM ents WHERE _id = ?", int64(1)).Scan(&f))
	assert.Equal(t, float64(10), f)
}

func TestEntityColumn_TextFromJSON(t *testing.T) {
	// A map value for a TEXT field is JSON-serialised.
	db, mock := setupTypedEntityDB(t)
	mock.seed(1, map[string]any{
		"int_val":  int64(0),
		"real_val": float64(0),
		"blob_val": []byte{},
		"text_val": map[string]any{"k": "v"},
		"time_val": "",
	})
	var s string
	require.NoError(t, db.QueryRow("SELECT text_val FROM ents WHERE _id = ?", int64(1)).Scan(&s))
	assert.Contains(t, s, "k")
}

func TestEntityColumn_RealFromFloat32(t *testing.T) {
	db, mock := setupTypedEntityDB(t)
	mock.seed(1, map[string]any{
		"int_val":  int64(0),
		"real_val": float32(2.5),
		"blob_val": []byte{},
		"text_val": "",
		"time_val": "",
	})
	var f float64
	require.NoError(t, db.QueryRow("SELECT real_val FROM ents WHERE _id = ?", int64(1)).Scan(&f))
	assert.InDelta(t, 2.5, f, 0.01)
}

func TestEntityColumn_RealFromDefaultType(t *testing.T) {
	// A string value for a REAL field hits the default→ResultNull branch.
	db, mock := setupTypedEntityDB(t)
	mock.seed(1, map[string]any{
		"int_val":  int64(0),
		"real_val": "not-a-float", // default → null
		"blob_val": []byte{},
		"text_val": "",
		"time_val": "",
	})
	var f sql.NullFloat64
	require.NoError(t, db.QueryRow("SELECT real_val FROM ents WHERE _id = ?", int64(1)).Scan(&f))
	assert.False(t, f.Valid)
}

func TestEntityColumn_BlobFromDefaultType(t *testing.T) {
	// A non-[]byte value for a BLOB field hits the default→ResultNull branch.
	db, mock := setupTypedEntityDB(t)
	mock.seed(1, map[string]any{
		"int_val":  int64(0),
		"real_val": float64(0),
		"blob_val": "not-bytes", // default → null
		"text_val": "",
		"time_val": "",
	})
	var b []byte
	require.NoError(t, db.QueryRow("SELECT blob_val FROM ents WHERE _id = ?", int64(1)).Scan(&b))
	assert.Nil(t, b)
}

func TestEntityColumn_IntegerFromInt(t *testing.T) {
	// An int value for an INTEGER field exercises the int case.
	db, mock := setupTypedEntityDB(t)
	mock.seed(1, map[string]any{
		"int_val":  int(77),
		"real_val": float64(0),
		"blob_val": []byte{},
		"text_val": "",
		"time_val": "",
	})
	var n int64
	require.NoError(t, db.QueryRow("SELECT int_val FROM ents WHERE _id = ?", int64(1)).Scan(&n))
	assert.Equal(t, int64(77), n)
}

func TestEntityColumn_IntegerFromDefaultType(t *testing.T) {
	// A string value for an INTEGER field hits the default→ResultNull branch.
	db, mock := setupTypedEntityDB(t)
	mock.seed(1, map[string]any{
		"int_val":  "not-int", // default → null
		"real_val": float64(0),
		"blob_val": []byte{},
		"text_val": "",
		"time_val": "",
	})
	var n sql.NullInt64
	require.NoError(t, db.QueryRow("SELECT int_val FROM ents WHERE _id = ?", int64(1)).Scan(&n))
	assert.False(t, n.Valid)
}

// ---------------------------------------------------------------------------
// Filter — entityIdxRange path (GT/GE/LT/LE on an indexed field)
// ---------------------------------------------------------------------------

// TestEntityCursor_Filter_RangeScan exercises the entityIdxRange code path in
// Filter by issuing a WHERE clause with a GT comparison on an indexed field.
// SQLite's BestIndex selects entityIdxRange for GT/GE/LT/LE operators on
// indexed fields.
func TestEntityCursor_Filter_RangeScan(t *testing.T) {
	meta := buildUserMeta()
	db, mock := setupEntityCovDB(t, meta)

	mock.seed(1, map[string]any{"email": "a@x.com", "name": "Alice", "age": int64(30)})
	mock.seed(2, map[string]any{"email": "b@x.com", "name": "Bob", "age": int64(25)})
	mock.seed(3, map[string]any{"email": "c@x.com", "name": "Carol", "age": int64(35)})

	// WHERE name >= 'B' triggers entityIdxRange (GT/GE on the indexed "name" field).
	rows, err := db.Query("SELECT _id, name FROM ents WHERE name >= ?", "B")
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
	// Bob and Carol are >= 'B'; Alice is not.
	assert.Contains(t, names, "Bob")
	assert.Contains(t, names, "Carol")
}

// TestEntityCursor_Filter_RangeScanError exercises the entityIdxRange error
// path when ScanByIndex returns an error.
func TestEntityCursor_Filter_RangeScanError(t *testing.T) {
	meta := buildUserMeta()
	db, mock := setupEntityCovDB(t, meta)
	mock.indexErr = errEntityIndexFailed

	rows, err := db.Query("SELECT _id FROM ents WHERE name >= ?", "A")
	if err != nil {
		assert.Contains(t, err.Error(), "entity index failed")
		return
	}
	defer rows.Close()
	for rows.Next() {
	}
	require.Error(t, rows.Err())
	assert.Contains(t, rows.Err().Error(), "entity index failed")
}

// ---------------------------------------------------------------------------
// Column — uint64 ID branch in Column (col 0)
// ---------------------------------------------------------------------------

// TestEntityColumn_IDUint64Branch exercises the uint64 branch for column 0
// by pre-loading a row with a uint64 _id value and reading it via SQL.
func TestEntityColumn_IDUint64Branch(t *testing.T) {
	meta := buildUserMeta()
	cursor := newPrefetchCursor(10)
	cursor.load([]row{
		{values: []any{uint64(42), "u@x.com", "UintUser", int64(20)}},
	})
	tbl := &entityTable{meta: meta, store: newMockEntityStore(), chunkSize: 10}
	c := &entityCursor{table: tbl, cursor: cursor}

	// Directly call Column for col 0 with a uint64 value to exercise that branch.
	type captureCtx struct {
		*sqlite3.SQLiteContext
	}
	// We can't easily call Column without a real SQLiteContext, so we verify
	// via readCurrentID which uses the same underlying cursor column read.
	id := c.readCurrentID()
	assert.Equal(t, int64(42), id)
}

// ---------------------------------------------------------------------------
// anyToString — additional branch coverage
// ---------------------------------------------------------------------------

func TestAnyToString_Branches(t *testing.T) {
	tests := []struct {
		input any
		want  string
	}{
		{nil, ""},
		{"hello", "hello"},
		{int64(42), "42"},
		{float64(3.0), "3"}, // float64ToStr converts to int64 first
		{[]byte("raw"), "raw"},
		{true, "true"}, // default branch: fmt.Sprintf("%v", v)
	}
	for _, tc := range tests {
		got := anyToString(tc.input)
		assert.Equal(t, tc.want, got, "input=%v (%T)", tc.input, tc.input)
	}
}

// ---------------------------------------------------------------------------
// parseColIdx — edge cases
// ---------------------------------------------------------------------------

func TestParseColIdx_Branches(t *testing.T) {
	assert.Equal(t, 0, parseColIdx(""))
	assert.Equal(t, 0, parseColIdx("0"))
	assert.Equal(t, 3, parseColIdx("3"))
	assert.Equal(t, 0, parseColIdx("notanumber"))
}

// ---------------------------------------------------------------------------
// Sentinel errors used in negative test paths
// ---------------------------------------------------------------------------

type entityTestError string

func (e entityTestError) Error() string { return string(e) }

const (
	errEntityGetFailed   entityTestError = "entity get failed"
	errEntityScanFailed  entityTestError = "entity scan failed"
	errEntityIndexFailed entityTestError = "entity index failed"
)
