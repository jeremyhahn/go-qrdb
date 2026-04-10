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
// mockNamespaceStore — in-memory namespace backend for testing
// ---------------------------------------------------------------------------

// mockNamespaceStore is a thread-safe in-memory namespace store used in unit
// tests. It satisfies the namespaceStore interface without any real network
// calls.
type mockNamespaceStore struct {
	mu         sync.RWMutex
	namespaces map[string]*NamespaceInfo
	listErr    error
	getErr     error
	createErr  error
	deleteErr  error
}

// newMockNamespaceStore creates an empty mockNamespaceStore.
func newMockNamespaceStore() *mockNamespaceStore {
	return &mockNamespaceStore{
		namespaces: make(map[string]*NamespaceInfo),
	}
}

func (m *mockNamespaceStore) ListNamespaces(_ context.Context) ([]NamespaceInfo, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]NamespaceInfo, 0, len(m.namespaces))
	for _, ns := range m.namespaces {
		cp := *ns
		out = append(out, cp)
	}
	return out, nil
}

func (m *mockNamespaceStore) GetNamespace(_ context.Context, name string) (*NamespaceInfo, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	m.mu.RLock()
	ns, ok := m.namespaces[name]
	m.mu.RUnlock()
	if !ok {
		return nil, nil
	}
	cp := *ns
	return &cp, nil
}

func (m *mockNamespaceStore) CreateNamespace(_ context.Context, name string, dimension uint32, distanceMetric, indexType string) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.mu.Lock()
	m.namespaces[name] = &NamespaceInfo{
		Name:           name,
		Dimension:      dimension,
		DistanceMetric: distanceMetric,
		IndexType:      indexType,
		VectorCount:    0,
	}
	m.mu.Unlock()
	return nil
}

func (m *mockNamespaceStore) DeleteNamespace(_ context.Context, name string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.mu.Lock()
	delete(m.namespaces, name)
	m.mu.Unlock()
	return nil
}

// count returns the number of namespaces currently in the store.
func (m *mockNamespaceStore) count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.namespaces)
}

// ---------------------------------------------------------------------------
// Test database helpers
// ---------------------------------------------------------------------------

// namespaceDriverCounter ensures unique driver names across tests.
var namespaceDriverCounter struct {
	sync.Mutex
	n int
}

// newNamespaceDriverName returns a unique driver name safe for sql.Register.
func newNamespaceDriverName() string {
	namespaceDriverCounter.Lock()
	defer namespaceDriverCounter.Unlock()
	namespaceDriverCounter.n++
	return fmt.Sprintf("sqlite3_ns_test_%d", namespaceDriverCounter.n)
}

// setupNamespaceDB registers a fresh SQLite driver with the
// qrdb_vector_namespaces module and returns an open in-memory database with a
// virtual table already created. It also returns the mockNamespaceStore for
// state inspection.
func setupNamespaceDB(t *testing.T) (*sql.DB, *mockNamespaceStore) {
	t.Helper()
	store := newMockNamespaceStore()
	driverName := newNamespaceDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &namespaceModule{store: store, chunkSize: defaultChunkSize}
			return conn.CreateModule(namespaceModuleName, mod)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("CREATE VIRTUAL TABLE namespaces USING qrdb_vector_namespaces")
	require.NoError(t, err)
	return db, store
}

// ---------------------------------------------------------------------------
// TestNamespacesTable_Schema
// ---------------------------------------------------------------------------

// TestNamespacesTable_Schema verifies that the virtual table exposes the
// correct five columns in the declared order.
func TestNamespacesTable_Schema(t *testing.T) {
	db, _ := setupNamespaceDB(t)

	rows, err := db.Query("SELECT * FROM namespaces LIMIT 0")
	require.NoError(t, err)
	defer rows.Close()

	cols, err := rows.Columns()
	require.NoError(t, err)
	assert.Equal(t, []string{"name", "dimension", "distance_metric", "index_type", "vector_count"}, cols)
}

// ---------------------------------------------------------------------------
// TestNamespacesTable_Insert
// ---------------------------------------------------------------------------

// TestNamespacesTable_Insert verifies that an INSERT creates a namespace in
// the backing store with the correct parameters.
func TestNamespacesTable_Insert(t *testing.T) {
	db, store := setupNamespaceDB(t)

	_, err := db.Exec(
		"INSERT INTO namespaces (name, dimension, distance_metric, index_type) VALUES (?, ?, ?, ?)",
		"embeddings", int64(768), "cosine", "hnsw",
	)
	require.NoError(t, err)
	assert.Equal(t, 1, store.count())

	store.mu.RLock()
	ns := store.namespaces["embeddings"]
	store.mu.RUnlock()

	require.NotNil(t, ns)
	assert.Equal(t, uint32(768), ns.Dimension)
	assert.Equal(t, "cosine", ns.DistanceMetric)
	assert.Equal(t, "hnsw", ns.IndexType)
}

// TestNamespacesTable_Insert_Error verifies that a store-level error surfaces
// to the caller as a NamespaceCreateError.
func TestNamespacesTable_Insert_Error(t *testing.T) {
	db, store := setupNamespaceDB(t)
	store.createErr = errors.New("backend unavailable")

	_, err := db.Exec(
		"INSERT INTO namespaces (name, dimension, distance_metric, index_type) VALUES (?, ?, ?, ?)",
		"fail", int64(128), "l2", "flat",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backend unavailable")
}

// ---------------------------------------------------------------------------
// TestNamespacesTable_Select_ByName
// ---------------------------------------------------------------------------

// TestNamespacesTable_Select_ByName verifies that WHERE name = ? triggers a
// point lookup and returns exactly the requested namespace.
func TestNamespacesTable_Select_ByName(t *testing.T) {
	db, store := setupNamespaceDB(t)

	store.mu.Lock()
	store.namespaces["images"] = &NamespaceInfo{
		Name: "images", Dimension: 512, DistanceMetric: "dot", IndexType: "ivf", VectorCount: 100,
	}
	store.namespaces["text"] = &NamespaceInfo{
		Name: "text", Dimension: 1536, DistanceMetric: "cosine", IndexType: "hnsw", VectorCount: 50,
	}
	store.mu.Unlock()

	rows, err := db.Query(
		"SELECT name, dimension, distance_metric, index_type, vector_count FROM namespaces WHERE name = ?",
		"images",
	)
	require.NoError(t, err)
	defer rows.Close()

	require.True(t, rows.Next())
	var name, distMetric, idxType string
	var dimension, vectorCount int64
	require.NoError(t, rows.Scan(&name, &dimension, &distMetric, &idxType, &vectorCount))

	assert.Equal(t, "images", name)
	assert.Equal(t, int64(512), dimension)
	assert.Equal(t, "dot", distMetric)
	assert.Equal(t, "ivf", idxType)
	assert.Equal(t, int64(100), vectorCount)
	assert.False(t, rows.Next(), "expected exactly one row")
}

// ---------------------------------------------------------------------------
// TestNamespacesTable_Select_FullScan
// ---------------------------------------------------------------------------

// TestNamespacesTable_Select_FullScan verifies that SELECT * returns all
// namespaces currently in the store.
func TestNamespacesTable_Select_FullScan(t *testing.T) {
	db, store := setupNamespaceDB(t)

	store.mu.Lock()
	store.namespaces["alpha"] = &NamespaceInfo{Name: "alpha", Dimension: 64}
	store.namespaces["beta"] = &NamespaceInfo{Name: "beta", Dimension: 128}
	store.namespaces["gamma"] = &NamespaceInfo{Name: "gamma", Dimension: 256}
	store.mu.Unlock()

	rows, err := db.Query("SELECT name FROM namespaces")
	require.NoError(t, err)
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())

	assert.Len(t, names, 3)
	assert.ElementsMatch(t, []string{"alpha", "beta", "gamma"}, names)
}

// ---------------------------------------------------------------------------
// TestNamespacesTable_Delete
// ---------------------------------------------------------------------------

// TestNamespacesTable_Delete verifies that a DELETE removes the namespace from
// the backing store.
func TestNamespacesTable_Delete(t *testing.T) {
	db, store := setupNamespaceDB(t)

	store.mu.Lock()
	store.namespaces["to-delete"] = &NamespaceInfo{Name: "to-delete", Dimension: 32}
	store.namespaces["keep"] = &NamespaceInfo{Name: "keep", Dimension: 32}
	store.mu.Unlock()

	_, err := db.Exec("DELETE FROM namespaces WHERE name = ?", "to-delete")
	require.NoError(t, err)

	assert.Equal(t, 1, store.count())
	store.mu.RLock()
	_, exists := store.namespaces["to-delete"]
	store.mu.RUnlock()
	assert.False(t, exists, "namespace 'to-delete' should have been removed")
}

// TestNamespacesTable_Delete_Error verifies that a store-level delete error is
// surfaced to the caller.
func TestNamespacesTable_Delete_Error(t *testing.T) {
	db, store := setupNamespaceDB(t)

	store.mu.Lock()
	store.namespaces["ns"] = &NamespaceInfo{Name: "ns", Dimension: 32}
	store.mu.Unlock()

	store.deleteErr = errors.New("delete failure")

	_, err := db.Exec("DELETE FROM namespaces WHERE name = ?", "ns")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete failure")
}

// ---------------------------------------------------------------------------
// TestNamespacesTable_ListError
// ---------------------------------------------------------------------------

// TestNamespacesTable_ListError verifies that a store-level list error is
// propagated to the caller during a full scan.
func TestNamespacesTable_ListError(t *testing.T) {
	db, store := setupNamespaceDB(t)
	store.listErr = errors.New("list failure")

	rows, err := db.Query("SELECT * FROM namespaces")
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
// TestNamespacesTable_BestIndex_NameLookup
// ---------------------------------------------------------------------------

// TestNamespacesTable_BestIndex_NameLookup verifies that an equality constraint
// on name produces a low-cost point-lookup index result.
func TestNamespacesTable_BestIndex_NameLookup(t *testing.T) {
	table := &namespaceTable{store: newMockNamespaceStore()}

	csts := []sqlite3.InfoConstraint{
		{Column: colNsName, Op: sqlite3.OpEQ, Usable: true},
	}
	result, err := table.BestIndex(csts, nil)
	require.NoError(t, err)

	assert.Equal(t, nsIdxNameLookup, result.IdxNum)
	assert.InDelta(t, 1.0, result.EstimatedCost, 0.001)
	assert.InDelta(t, 1, result.EstimatedRows, 0.001)
	assert.True(t, result.Used[0], "equality constraint should be consumed")
	assert.True(t, result.AlreadyOrdered)
}

// ---------------------------------------------------------------------------
// TestNamespacesTable_BestIndex_FullScan
// ---------------------------------------------------------------------------

// TestNamespacesTable_BestIndex_FullScan verifies that a query with no
// applicable constraint falls back to the full scan plan with higher cost.
func TestNamespacesTable_BestIndex_FullScan(t *testing.T) {
	table := &namespaceTable{store: newMockNamespaceStore()}

	result, err := table.BestIndex(nil, nil)
	require.NoError(t, err)

	assert.Equal(t, nsIdxFullScan, result.IdxNum)
	assert.GreaterOrEqual(t, result.EstimatedCost, float64(100))
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

// TestNamespaceCreateError verifies the NamespaceCreateError message and Unwrap.
func TestNamespaceCreateError(t *testing.T) {
	cause := errors.New("quota exceeded")
	e := &NamespaceCreateError{Name: "embeddings", Err: cause}

	assert.Contains(t, e.Error(), "embeddings")
	assert.Contains(t, e.Error(), "quota exceeded")
	assert.True(t, errors.Is(e, cause))
}

// TestNamespaceDeleteError verifies the NamespaceDeleteError message and Unwrap.
func TestNamespaceDeleteError(t *testing.T) {
	cause := errors.New("not found")
	e := &NamespaceDeleteError{Name: "old-ns", Err: cause}

	assert.Contains(t, e.Error(), "old-ns")
	assert.Contains(t, e.Error(), "not found")
	assert.True(t, errors.Is(e, cause))
}

// TestNamespaceListError verifies the NamespaceListError message and Unwrap.
func TestNamespaceListError(t *testing.T) {
	cause := errors.New("timeout")
	e := &NamespaceListError{Err: cause}

	assert.Contains(t, e.Error(), "timeout")
	assert.True(t, errors.Is(e, cause))
}

// TestNamespaceGetError verifies the NamespaceGetError message and Unwrap.
func TestNamespaceGetError(t *testing.T) {
	cause := errors.New("network error")
	e := &NamespaceGetError{Name: "my-ns", Err: cause}

	assert.Contains(t, e.Error(), "my-ns")
	assert.Contains(t, e.Error(), "network error")
	assert.True(t, errors.Is(e, cause))
}

// ---------------------------------------------------------------------------
// TestNamespacesTable_RegisterNamespaceModule
// ---------------------------------------------------------------------------

// TestNamespacesTable_RegisterNamespaceModule verifies that
// RegisterNamespaceModule registers the qrdb_vector_namespaces module
// correctly so that a CREATE VIRTUAL TABLE statement works without error.
func TestNamespacesTable_RegisterNamespaceModule(t *testing.T) {
	store := newMockNamespaceStore()
	driverName := newNamespaceDriverName()

	err := RegisterNamespaceModule(driverName, store, defaultChunkSize)
	require.NoError(t, err)

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("CREATE VIRTUAL TABLE ns USING qrdb_vector_namespaces")
	require.NoError(t, err)
}
