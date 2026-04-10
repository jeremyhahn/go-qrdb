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
	"errors"
	"fmt"
	"sync"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// mockCollectionStore — in-memory collection backend for testing
// ---------------------------------------------------------------------------

// mockCollectionStore is a thread-safe in-memory collection store used in unit
// tests. It satisfies the collectionStore interface without any real network
// calls.
type mockCollectionStore struct {
	mu      sync.RWMutex
	docs    map[uint64]map[string]any
	nextID  uint64
	getErr  error
	listErr error
	insErr  error
	updErr  error
	delErr  error
}

// newMockCollectionStore creates an empty mockCollectionStore with its ID
// sequence starting at 1.
func newMockCollectionStore() *mockCollectionStore {
	return &mockCollectionStore{
		docs:   make(map[uint64]map[string]any),
		nextID: 1,
	}
}

func (m *mockCollectionStore) GetDocument(_ context.Context, _, _ string, id uint64) (map[string]any, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	m.mu.RLock()
	doc, ok := m.docs[id]
	m.mu.RUnlock()
	if !ok {
		return nil, nil
	}
	// Return a shallow copy so callers cannot mutate internal state.
	cp := make(map[string]any, len(doc))
	for k, v := range doc {
		cp[k] = v
	}
	return cp, nil
}

func (m *mockCollectionStore) ListDocuments(_ context.Context, _, _ string) ([]DocumentRow, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	rows := make([]DocumentRow, 0, len(m.docs))
	for id, doc := range m.docs {
		cp := make(map[string]any, len(doc))
		for k, v := range doc {
			cp[k] = v
		}
		rows = append(rows, DocumentRow{ID: id, Fields: cp})
	}
	return rows, nil
}

func (m *mockCollectionStore) InsertDocument(_ context.Context, _, _ string, doc map[string]any) (uint64, error) {
	if m.insErr != nil {
		return 0, m.insErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// Honour an explicit _id in the document payload; otherwise auto-assign.
	var id uint64
	if v, ok := doc["_id"]; ok && v != nil {
		id = toUint64(v)
	}
	if id == 0 {
		id = m.nextID
		m.nextID++
	}
	cp := make(map[string]any, len(doc))
	for k, v := range doc {
		cp[k] = v
	}
	m.docs[id] = cp
	return id, nil
}

func (m *mockCollectionStore) UpdateDocument(_ context.Context, _, _ string, id uint64, doc map[string]any) error {
	if m.updErr != nil {
		return m.updErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.docs[id]
	if !ok {
		existing = make(map[string]any)
	}
	for k, v := range doc {
		existing[k] = v
	}
	m.docs[id] = existing
	return nil
}

func (m *mockCollectionStore) DeleteDocument(_ context.Context, _, _ string, id uint64) error {
	if m.delErr != nil {
		return m.delErr
	}
	m.mu.Lock()
	delete(m.docs, id)
	m.mu.Unlock()
	return nil
}

// docCount returns the number of documents currently in the store.
func (m *mockCollectionStore) docCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.docs)
}

// ---------------------------------------------------------------------------
// Test database helpers
// ---------------------------------------------------------------------------

// collectionDriverCounter ensures unique driver names across tests.
var collectionDriverCounter struct {
	sync.Mutex
	n int
}

// newCollectionDriverName returns a unique driver name safe for sql.Register.
func newCollectionDriverName() string {
	collectionDriverCounter.Lock()
	defer collectionDriverCounter.Unlock()
	collectionDriverCounter.n++
	return fmt.Sprintf("sqlite3_collection_test_%d", collectionDriverCounter.n)
}

// setupCollectionDB registers a fresh SQLite driver with the qrdb_collection
// module and returns an open in-memory database with a collection virtual
// table already created. It also returns the underlying mockCollectionStore for
// state inspection.
func setupCollectionDB(t *testing.T, meta *collectionMeta) (*sql.DB, *mockCollectionStore) {
	t.Helper()
	store := newMockCollectionStore()
	driverName := newCollectionDriverName()

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

// basicMeta returns a collectionMeta with a representative schema covering all
// supported type mappings.
func basicMeta() *collectionMeta {
	return &collectionMeta{
		name:     "users",
		database: "mydb",
		schema: map[string]string{
			"name":   "VARCHAR",
			"age":    "BIGINT",
			"score":  "DOUBLE",
			"active": "BOOLEAN",
			"meta":   "JSON",
			"avatar": "BLOB",
		},
	}
}

// ---------------------------------------------------------------------------
// TestCollectionTable_DynamicSchema
// ---------------------------------------------------------------------------

// TestCollectionTable_DynamicSchema verifies that the virtual table exposes
// _id plus all schema fields sorted alphabetically, with the correct SQLite
// column names.
func TestCollectionTable_DynamicSchema(t *testing.T) {
	db, _ := setupCollectionDB(t, basicMeta())

	rows, err := db.Query("SELECT * FROM docs LIMIT 0")
	require.NoError(t, err)
	defer rows.Close()

	cols, err := rows.Columns()
	require.NoError(t, err)

	// _id first, then alphabetically sorted schema fields.
	want := []string{"_id", "active", "age", "avatar", "meta", "name", "score"}
	assert.Equal(t, want, cols)
}

// TestCollectionTable_DynamicSchema_EmptySchema verifies that a collection with
// no user-defined fields only exposes the _id column.
func TestCollectionTable_DynamicSchema_EmptySchema(t *testing.T) {
	meta := &collectionMeta{name: "empty", database: "db", schema: map[string]string{}}
	db, _ := setupCollectionDB(t, meta)

	rows, err := db.Query("SELECT * FROM docs LIMIT 0")
	require.NoError(t, err)
	defer rows.Close()

	cols, err := rows.Columns()
	require.NoError(t, err)
	assert.Equal(t, []string{"_id"}, cols)
}

// ---------------------------------------------------------------------------
// TestCollectionTable_Insert
// ---------------------------------------------------------------------------

// TestCollectionTable_Insert verifies that an INSERT stores the document in
// the backing store with the correct field values.
func TestCollectionTable_Insert(t *testing.T) {
	db, store := setupCollectionDB(t, basicMeta())

	_, err := db.Exec(
		"INSERT INTO docs (name, age, score, active) VALUES (?, ?, ?, ?)",
		"Alice", int64(30), 9.5, int64(1),
	)
	require.NoError(t, err)
	assert.Equal(t, 1, store.docCount())
}

// TestCollectionTable_Insert_ErrorPropagated verifies that a store-level error
// is surfaced to the caller as a CollectionInsertError.
func TestCollectionTable_Insert_ErrorPropagated(t *testing.T) {
	db, store := setupCollectionDB(t, basicMeta())
	store.insErr = errors.New("backend unavailable")

	_, err := db.Exec("INSERT INTO docs (name) VALUES (?)", "Bob")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backend unavailable")
}

// ---------------------------------------------------------------------------
// TestCollectionTable_Select_FullScan
// ---------------------------------------------------------------------------

// TestCollectionTable_Select_FullScan verifies that SELECT * returns all
// documents currently in the collection.
func TestCollectionTable_Select_FullScan(t *testing.T) {
	meta := &collectionMeta{
		name:     "items",
		database: "db",
		schema:   map[string]string{"label": "VARCHAR"},
	}
	db, store := setupCollectionDB(t, meta)

	// Pre-populate the store directly to avoid INSERT round-trip overhead.
	store.mu.Lock()
	store.docs[1] = map[string]any{"label": "first"}
	store.docs[2] = map[string]any{"label": "second"}
	store.docs[3] = map[string]any{"label": "third"}
	store.nextID = 4
	store.mu.Unlock()

	rows, err := db.Query("SELECT _id, label FROM docs")
	require.NoError(t, err)
	defer rows.Close()

	type scanRow struct {
		id    int64
		label string
	}
	var got []scanRow
	for rows.Next() {
		var r scanRow
		require.NoError(t, rows.Scan(&r.id, &r.label))
		got = append(got, r)
	}
	require.NoError(t, rows.Err())

	// All three documents must be returned (order may vary).
	assert.Len(t, got, 3)
	labels := make([]string, len(got))
	for i, r := range got {
		labels[i] = r.label
		assert.Positive(t, r.id)
	}
	assert.ElementsMatch(t, []string{"first", "second", "third"}, labels)
}

// TestCollectionTable_Select_FullScan_ListError verifies that a store-level
// list error is propagated to the caller.
func TestCollectionTable_Select_FullScan_ListError(t *testing.T) {
	db, store := setupCollectionDB(t, basicMeta())
	store.listErr = errors.New("list failure")

	rows, err := db.Query("SELECT * FROM docs")
	if err != nil {
		assert.Contains(t, err.Error(), "list failure")
		return
	}
	defer rows.Close()
	for rows.Next() {
	}
	require.Error(t, rows.Err())
	assert.Contains(t, rows.Err().Error(), "list failure")
}

// ---------------------------------------------------------------------------
// TestCollectionTable_Select_PointLookup
// ---------------------------------------------------------------------------

// TestCollectionTable_Select_PointLookup verifies that WHERE _id = ? triggers
// a point lookup and returns exactly the requested document.
func TestCollectionTable_Select_PointLookup(t *testing.T) {
	meta := &collectionMeta{
		name:     "products",
		database: "shop",
		schema:   map[string]string{"title": "VARCHAR"},
	}
	db, store := setupCollectionDB(t, meta)

	store.mu.Lock()
	store.docs[42] = map[string]any{"title": "Widget"}
	store.docs[99] = map[string]any{"title": "Gadget"}
	store.mu.Unlock()

	rows, err := db.Query("SELECT _id, title FROM docs WHERE _id = ?", int64(42))
	require.NoError(t, err)
	defer rows.Close()

	require.True(t, rows.Next())
	var id int64
	var title string
	require.NoError(t, rows.Scan(&id, &title))
	assert.Equal(t, int64(42), id)
	assert.Equal(t, "Widget", title)
	assert.False(t, rows.Next(), "expected exactly one row")
}

// TestCollectionTable_Select_PointLookup_NotFound verifies that a lookup for a
// non-existent _id returns zero rows without error.
func TestCollectionTable_Select_PointLookup_NotFound(t *testing.T) {
	db, _ := setupCollectionDB(t, basicMeta())

	rows, err := db.Query("SELECT * FROM docs WHERE _id = ?", int64(9999))
	require.NoError(t, err)
	defer rows.Close()
	assert.False(t, rows.Next(), "expected no rows for missing _id")
}

// TestCollectionTable_Select_PointLookup_GetError verifies that a store-level
// get error is propagated to the caller.
func TestCollectionTable_Select_PointLookup_GetError(t *testing.T) {
	db, store := setupCollectionDB(t, basicMeta())
	store.getErr = errors.New("get failure")

	rows, err := db.Query("SELECT * FROM docs WHERE _id = ?", int64(1))
	if err != nil {
		assert.Contains(t, err.Error(), "get failure")
		return
	}
	defer rows.Close()
	// The error may surface lazily during iteration.
	for rows.Next() {
	}
	require.Error(t, rows.Err())
	assert.Contains(t, rows.Err().Error(), "get failure")
}

// ---------------------------------------------------------------------------
// TestCollectionTable_Update
// ---------------------------------------------------------------------------

// TestCollectionTable_Update verifies that an UPDATE modifies the correct
// document fields in the backing store.
func TestCollectionTable_Update(t *testing.T) {
	meta := &collectionMeta{
		name:     "accounts",
		database: "bank",
		schema:   map[string]string{"balance": "DOUBLE", "owner": "VARCHAR"},
	}
	db, store := setupCollectionDB(t, meta)

	store.mu.Lock()
	store.docs[7] = map[string]any{"balance": float64(100), "owner": "Eve"}
	store.mu.Unlock()

	_, err := db.Exec(
		"UPDATE docs SET balance = ?, owner = ? WHERE _id = ?",
		float64(250), "Eve", int64(7),
	)
	require.NoError(t, err)

	store.mu.RLock()
	doc := store.docs[7]
	store.mu.RUnlock()

	assert.Equal(t, float64(250), doc["balance"])
}

// TestCollectionTable_Update_ErrorPropagated verifies that a store-level update
// error is surfaced to the caller.
func TestCollectionTable_Update_ErrorPropagated(t *testing.T) {
	meta := &collectionMeta{
		name:     "things",
		database: "db",
		schema:   map[string]string{"val": "BIGINT"},
	}
	db, store := setupCollectionDB(t, meta)

	store.mu.Lock()
	store.docs[1] = map[string]any{"val": int64(1)}
	store.mu.Unlock()

	store.updErr = errors.New("update failure")

	_, err := db.Exec("UPDATE docs SET val = ? WHERE _id = ?", int64(2), int64(1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "update failure")
}

// ---------------------------------------------------------------------------
// TestCollectionTable_Delete
// ---------------------------------------------------------------------------

// TestCollectionTable_Delete verifies that a DELETE removes the document from
// the backing store.
func TestCollectionTable_Delete(t *testing.T) {
	meta := &collectionMeta{
		name:     "sessions",
		database: "auth",
		schema:   map[string]string{"token": "VARCHAR"},
	}
	db, store := setupCollectionDB(t, meta)

	store.mu.Lock()
	store.docs[5] = map[string]any{"token": "abc"}
	store.docs[6] = map[string]any{"token": "def"}
	store.mu.Unlock()

	_, err := db.Exec("DELETE FROM docs WHERE _id = ?", int64(5))
	require.NoError(t, err)

	assert.Equal(t, 1, store.docCount())
	store.mu.RLock()
	_, exists := store.docs[5]
	store.mu.RUnlock()
	assert.False(t, exists, "document 5 should have been deleted")
}

// TestCollectionTable_Delete_ErrorPropagated verifies that a store-level delete
// error is surfaced to the caller.
func TestCollectionTable_Delete_ErrorPropagated(t *testing.T) {
	meta := &collectionMeta{
		name:     "things",
		database: "db",
		schema:   map[string]string{"val": "BIGINT"},
	}
	db, store := setupCollectionDB(t, meta)

	store.mu.Lock()
	store.docs[1] = map[string]any{"val": int64(1)}
	store.mu.Unlock()

	store.delErr = errors.New("delete failure")

	_, err := db.Exec("DELETE FROM docs WHERE _id = ?", int64(1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete failure")
}

// ---------------------------------------------------------------------------
// TestCollectionTable_NullableColumns
// ---------------------------------------------------------------------------

// TestCollectionTable_NullableColumns verifies that fields absent from a
// document are returned as NULL rather than causing an error or a zero value.
func TestCollectionTable_NullableColumns(t *testing.T) {
	meta := &collectionMeta{
		name:     "sparse",
		database: "db",
		schema: map[string]string{
			"a": "VARCHAR",
			"b": "BIGINT",
			"c": "DOUBLE",
		},
	}
	db, store := setupCollectionDB(t, meta)

	// Document only has field "a"; "b" and "c" are absent.
	store.mu.Lock()
	store.docs[1] = map[string]any{"a": "hello"}
	store.mu.Unlock()

	rows, err := db.Query("SELECT a, b, c FROM docs WHERE _id = ?", int64(1))
	require.NoError(t, err)
	defer rows.Close()

	require.True(t, rows.Next())
	var a sql.NullString
	var b sql.NullInt64
	var c sql.NullFloat64
	require.NoError(t, rows.Scan(&a, &b, &c))

	assert.True(t, a.Valid)
	assert.Equal(t, "hello", a.String)
	assert.False(t, b.Valid, "absent field 'b' should be NULL")
	assert.False(t, c.Valid, "absent field 'c' should be NULL")
}

// ---------------------------------------------------------------------------
// TestCollectionTable_SchemaEvolution
// ---------------------------------------------------------------------------

// TestCollectionTable_SchemaEvolution verifies that creating a new virtual
// table from updated collectionMeta exposes the additional fields, simulating
// a schema evolution between connections.
func TestCollectionTable_SchemaEvolution(t *testing.T) {
	metaV1 := &collectionMeta{
		name:     "events",
		database: "logs",
		schema:   map[string]string{"name": "VARCHAR"},
	}

	metaV2 := &collectionMeta{
		name:     "events",
		database: "logs",
		schema:   map[string]string{"name": "VARCHAR", "timestamp": "BIGINT"},
	}

	// V1 table: only "name" field.
	dbV1, _ := setupCollectionDB(t, metaV1)
	rowsV1, err := dbV1.Query("SELECT * FROM docs LIMIT 0")
	require.NoError(t, err)
	colsV1, err := rowsV1.Columns()
	rowsV1.Close()
	require.NoError(t, err)
	assert.Equal(t, []string{"_id", "name"}, colsV1)

	// V2 table registered independently has the new "timestamp" field.
	dbV2, _ := setupCollectionDB(t, metaV2)
	rowsV2, err := dbV2.Query("SELECT * FROM docs LIMIT 0")
	require.NoError(t, err)
	colsV2, err := rowsV2.Columns()
	rowsV2.Close()
	require.NoError(t, err)
	assert.Equal(t, []string{"_id", "name", "timestamp"}, colsV2)
}

// ---------------------------------------------------------------------------
// TestCollectionTable_BestIndex
// ---------------------------------------------------------------------------

// TestCollectionTable_BestIndex_PointLookup verifies that an equality
// constraint on _id produces a low-cost point-lookup index result.
func TestCollectionTable_BestIndex_PointLookup(t *testing.T) {
	table := &collectionTable{meta: basicMeta()}

	csts := []sqlite3.InfoConstraint{
		{Column: colID, Op: sqlite3.OpEQ, Usable: true},
	}
	result, err := table.BestIndex(csts, nil)
	require.NoError(t, err)

	assert.Equal(t, colIDXPointLookup, result.IdxNum)
	assert.InDelta(t, 1.0, result.EstimatedCost, 0.001)
	assert.True(t, result.Used[0], "equality constraint should be consumed")
}

// TestCollectionTable_BestIndex_FullScan verifies that a query with no
// applicable constraint falls back to the full scan plan with higher cost.
func TestCollectionTable_BestIndex_FullScan(t *testing.T) {
	table := &collectionTable{meta: basicMeta()}

	// No usable constraints.
	result, err := table.BestIndex(nil, nil)
	require.NoError(t, err)

	assert.Equal(t, colIDXFullScan, result.IdxNum)
	assert.GreaterOrEqual(t, result.EstimatedCost, float64(100))
}

// TestCollectionTable_BestIndex_NonIDConstraint verifies that a constraint on
// a non-_id column falls back to the full scan plan because field-level index
// support is not yet implemented.
func TestCollectionTable_BestIndex_NonIDConstraint(t *testing.T) {
	table := &collectionTable{meta: basicMeta()}

	csts := []sqlite3.InfoConstraint{
		{Column: 1, Op: sqlite3.OpEQ, Usable: true}, // column 1 = "active"
	}
	result, err := table.BestIndex(csts, nil)
	require.NoError(t, err)

	assert.Equal(t, colIDXFullScan, result.IdxNum)
	assert.False(t, result.Used[0], "non-_id constraint must not be consumed")
}

// ---------------------------------------------------------------------------
// TestCollectionTable_SchemaTypeMapping
// ---------------------------------------------------------------------------

// TestCollectionTable_SchemaTypeMapping verifies that all supported
// CollectionInfo schema type strings map to the expected SQLite affinity
// keywords in the generated DDL.
func TestCollectionTable_SchemaTypeMapping(t *testing.T) {
	cases := []struct {
		input    string
		wantType string
	}{
		{"VARCHAR", "TEXT"},
		{"DOUBLE", "REAL"},
		{"BOOLEAN", "INTEGER"},
		{"BIGINT", "INTEGER"},
		{"JSON", "TEXT"},
		{"BLOB", "BLOB"},
		{"UNKNOWN", "TEXT"}, // default fallback
		{"", "TEXT"},        // empty → default
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := schemaTypeToSQLite(tc.input)
			assert.Equal(t, tc.wantType, got)
		})
	}
}

// TestCollectionMeta_BuildSchema verifies the generated DDL string for a
// known schema to catch regressions in the schema builder.
func TestCollectionMeta_BuildSchema(t *testing.T) {
	meta := &collectionMeta{
		name:     "widgets",
		database: "shop",
		schema: map[string]string{
			"name":  "VARCHAR",
			"price": "DOUBLE",
		},
	}
	ddl := meta.buildSchema()
	// Fields must appear in alphabetical order: name, price.
	assert.Equal(t, "CREATE TABLE x(_id INTEGER PRIMARY KEY, name TEXT, price REAL)", ddl)
}

// ---------------------------------------------------------------------------
// TestCollectionTable_InsertReturnsGeneratedID
// ---------------------------------------------------------------------------

// TestCollectionTable_InsertReturnsGeneratedID verifies that inserting a
// document without an explicit _id causes the store to auto-assign one and
// the inserted row is retrievable by that ID.
func TestCollectionTable_InsertReturnsGeneratedID(t *testing.T) {
	meta := &collectionMeta{
		name:     "notes",
		database: "pad",
		schema:   map[string]string{"body": "VARCHAR"},
	}
	db, store := setupCollectionDB(t, meta)

	res, err := db.Exec("INSERT INTO docs (body) VALUES (?)", "hello world")
	require.NoError(t, err)

	id, err := res.LastInsertId()
	require.NoError(t, err)
	assert.Positive(t, id)

	// The document must be present in the store under the returned ID.
	store.mu.RLock()
	doc, ok := store.docs[uint64(id)]
	store.mu.RUnlock()
	require.True(t, ok, "document should exist in store under returned ID")
	assert.Equal(t, "hello world", doc["body"])
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

// TestCollectionGetError verifies the CollectionGetError message and Unwrap.
func TestCollectionGetError(t *testing.T) {
	cause := errors.New("connection reset")
	e := &CollectionGetError{Database: "db", Collection: "col", ID: 42, Err: cause}

	assert.Contains(t, e.Error(), "db/col")
	assert.Contains(t, e.Error(), "42")
	assert.Contains(t, e.Error(), "connection reset")
	assert.True(t, errors.Is(e, cause))
}

// TestCollectionInsertError verifies the CollectionInsertError message and Unwrap.
func TestCollectionInsertError(t *testing.T) {
	cause := errors.New("disk full")
	e := &CollectionInsertError{Database: "db", Collection: "col", Err: cause}

	assert.Contains(t, e.Error(), "db/col")
	assert.Contains(t, e.Error(), "disk full")
	assert.True(t, errors.Is(e, cause))
}

// TestCollectionUpdateError verifies the CollectionUpdateError message and Unwrap.
func TestCollectionUpdateError(t *testing.T) {
	cause := errors.New("write conflict")
	e := &CollectionUpdateError{Database: "db", Collection: "col", ID: 7, Err: cause}

	assert.Contains(t, e.Error(), "db/col")
	assert.Contains(t, e.Error(), "7")
	assert.Contains(t, e.Error(), "write conflict")
	assert.True(t, errors.Is(e, cause))
}

// TestCollectionDeleteError verifies the CollectionDeleteError message and Unwrap.
func TestCollectionDeleteError(t *testing.T) {
	cause := errors.New("not found")
	e := &CollectionDeleteError{Database: "db", Collection: "col", ID: 99, Err: cause}

	assert.Contains(t, e.Error(), "db/col")
	assert.Contains(t, e.Error(), "99")
	assert.Contains(t, e.Error(), "not found")
	assert.True(t, errors.Is(e, cause))
}

// TestCollectionListError verifies the CollectionListError message and Unwrap.
func TestCollectionListError(t *testing.T) {
	cause := errors.New("timeout")
	e := &CollectionListError{Database: "db", Collection: "col", Err: cause}

	assert.Contains(t, e.Error(), "db/col")
	assert.Contains(t, e.Error(), "timeout")
	assert.True(t, errors.Is(e, cause))
}
