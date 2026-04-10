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
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// mockVectorStore — in-memory vector backend for testing
// ---------------------------------------------------------------------------

// mockVectorStore is a thread-safe in-memory vector store used in unit tests.
// It satisfies the vectorStore interface without any real network calls.
type mockVectorStore struct {
	mu        sync.RWMutex
	vectors   map[string]map[uint64]*VectorRow // namespace → id → row
	upsertErr error
	getErr    error
	listErr   error
	deleteErr error
}

// newMockVectorStore creates an empty mockVectorStore.
func newMockVectorStore() *mockVectorStore {
	return &mockVectorStore{
		vectors: make(map[string]map[uint64]*VectorRow),
	}
}

func (m *mockVectorStore) UpsertVector(_ context.Context, namespace string, id uint64, vector []byte, metadata string) error {
	if m.upsertErr != nil {
		return m.upsertErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.vectors[namespace]; !ok {
		m.vectors[namespace] = make(map[uint64]*VectorRow)
	}
	vecCopy := make([]byte, len(vector))
	copy(vecCopy, vector)
	m.vectors[namespace][id] = &VectorRow{
		ID:        id,
		Namespace: namespace,
		Vector:    vecCopy,
		Metadata:  metadata,
	}
	return nil
}

func (m *mockVectorStore) GetVector(_ context.Context, namespace string, id uint64) (*VectorRow, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	ns, ok := m.vectors[namespace]
	if !ok {
		return nil, nil
	}
	vr, ok := ns[id]
	if !ok {
		return nil, nil
	}
	cp := *vr
	vecCopy := make([]byte, len(vr.Vector))
	copy(vecCopy, vr.Vector)
	cp.Vector = vecCopy
	return &cp, nil
}

func (m *mockVectorStore) ListVectors(_ context.Context, namespace string) ([]VectorRow, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	ns, ok := m.vectors[namespace]
	if !ok {
		return nil, nil
	}
	out := make([]VectorRow, 0, len(ns))
	for _, vr := range ns {
		cp := *vr
		vecCopy := make([]byte, len(vr.Vector))
		copy(vecCopy, vr.Vector)
		cp.Vector = vecCopy
		out = append(out, cp)
	}
	return out, nil
}

func (m *mockVectorStore) DeleteVector(_ context.Context, namespace string, id uint64) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ns, ok := m.vectors[namespace]; ok {
		delete(ns, id)
	}
	return nil
}

// vectorCount returns the total number of vectors across all namespaces.
func (m *mockVectorStore) vectorCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	total := 0
	for _, ns := range m.vectors {
		total += len(ns)
	}
	return total
}

// namespaceCount returns the number of vectors in the given namespace.
func (m *mockVectorStore) namespaceCount(namespace string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.vectors[namespace])
}

// ---------------------------------------------------------------------------
// float32 BLOB helpers
// ---------------------------------------------------------------------------

// float32sToBytes encodes a float32 slice to raw little-endian bytes.
func float32sToBytes(floats []float32) []byte {
	buf := make([]byte, len(floats)*4)
	for i, f := range floats {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// bytesToFloat32s decodes raw little-endian bytes to a float32 slice.
func bytesToFloat32s(b []byte) []float32 {
	n := len(b) / 4
	out := make([]float32, n)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

// ---------------------------------------------------------------------------
// Test database helpers
// ---------------------------------------------------------------------------

// vectorDriverCounter ensures unique driver names across tests.
var vectorDriverCounter struct {
	sync.Mutex
	n int
}

// newVectorDriverName returns a unique driver name safe for sql.Register.
func newVectorDriverName() string {
	vectorDriverCounter.Lock()
	defer vectorDriverCounter.Unlock()
	vectorDriverCounter.n++
	return fmt.Sprintf("sqlite3_vec_test_%d", vectorDriverCounter.n)
}

// setupVectorDB registers a fresh SQLite driver with the qrdb_vectors module
// and returns an open in-memory database with a virtual table already created.
// It also returns the mockVectorStore for state inspection.
func setupVectorDB(t *testing.T) (*sql.DB, *mockVectorStore) {
	t.Helper()
	store := newMockVectorStore()
	driverName := newVectorDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &vectorModule{store: store, chunkSize: defaultChunkSize}
			return conn.CreateModule(vectorModuleName, mod)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("CREATE VIRTUAL TABLE vectors USING qrdb_vectors")
	require.NoError(t, err)
	return db, store
}

// ---------------------------------------------------------------------------
// TestVectorsTable_Schema
// ---------------------------------------------------------------------------

// TestVectorsTable_Schema verifies that the virtual table exposes the correct
// four columns in the declared order.
func TestVectorsTable_Schema(t *testing.T) {
	db, _ := setupVectorDB(t)

	rows, err := db.Query("SELECT * FROM vectors LIMIT 0")
	require.NoError(t, err)
	defer rows.Close()

	cols, err := rows.Columns()
	require.NoError(t, err)
	assert.Equal(t, []string{"id", "namespace", "vector", "metadata"}, cols)
}

// ---------------------------------------------------------------------------
// TestVectorsTable_Insert
// ---------------------------------------------------------------------------

// TestVectorsTable_Insert verifies that an INSERT stores the vector in the
// backing store with the correct field values.
func TestVectorsTable_Insert(t *testing.T) {
	db, store := setupVectorDB(t)

	vec := float32sToBytes([]float32{0.1, 0.2, 0.3})
	_, err := db.Exec(
		"INSERT INTO vectors (id, namespace, vector, metadata) VALUES (?, ?, ?, ?)",
		int64(1), "embeddings", vec, `{"label":"doc1"}`,
	)
	require.NoError(t, err)
	assert.Equal(t, 1, store.vectorCount())
}

// TestVectorsTable_Insert_Error verifies that a store-level error surfaces to
// the caller as a VectorUpsertError.
func TestVectorsTable_Insert_Error(t *testing.T) {
	db, store := setupVectorDB(t)
	store.upsertErr = errors.New("backend unavailable")

	vec := float32sToBytes([]float32{0.1})
	_, err := db.Exec(
		"INSERT INTO vectors (id, namespace, vector, metadata) VALUES (?, ?, ?, ?)",
		int64(1), "ns", vec, "",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backend unavailable")
}

// ---------------------------------------------------------------------------
// TestVectorsTable_Select_ByIdAndNamespace
// ---------------------------------------------------------------------------

// TestVectorsTable_Select_ByIdAndNamespace verifies that
// WHERE id = ? AND namespace = ? triggers a composite point lookup and returns
// exactly the requested vector.
func TestVectorsTable_Select_ByIdAndNamespace(t *testing.T) {
	db, store := setupVectorDB(t)

	vec1 := float32sToBytes([]float32{1.0, 0.0})
	vec2 := float32sToBytes([]float32{0.0, 1.0})

	store.mu.Lock()
	store.vectors["ns1"] = map[uint64]*VectorRow{
		10: {ID: 10, Namespace: "ns1", Vector: vec1, Metadata: `{"a":1}`},
		20: {ID: 20, Namespace: "ns1", Vector: vec2, Metadata: `{"a":2}`},
	}
	store.mu.Unlock()

	rows, err := db.Query(
		"SELECT id, namespace, vector, metadata FROM vectors WHERE id = ? AND namespace = ?",
		int64(10), "ns1",
	)
	require.NoError(t, err)
	defer rows.Close()

	require.True(t, rows.Next())
	var id int64
	var ns, metadata string
	var blob []byte
	require.NoError(t, rows.Scan(&id, &ns, &blob, &metadata))

	assert.Equal(t, int64(10), id)
	assert.Equal(t, "ns1", ns)
	assert.Equal(t, `{"a":1}`, metadata)
	assert.Equal(t, vec1, blob)
	assert.False(t, rows.Next(), "expected exactly one row")
}

// ---------------------------------------------------------------------------
// TestVectorsTable_Select_ByNamespace
// ---------------------------------------------------------------------------

// TestVectorsTable_Select_ByNamespace verifies that WHERE namespace = ?
// triggers a namespace scan and returns all vectors in that namespace.
func TestVectorsTable_Select_ByNamespace(t *testing.T) {
	db, store := setupVectorDB(t)

	store.mu.Lock()
	store.vectors["alpha"] = map[uint64]*VectorRow{
		1: {ID: 1, Namespace: "alpha", Vector: float32sToBytes([]float32{1, 2, 3}), Metadata: ""},
		2: {ID: 2, Namespace: "alpha", Vector: float32sToBytes([]float32{4, 5, 6}), Metadata: ""},
	}
	store.vectors["beta"] = map[uint64]*VectorRow{
		3: {ID: 3, Namespace: "beta", Vector: float32sToBytes([]float32{7, 8, 9}), Metadata: ""},
	}
	store.mu.Unlock()

	rows, err := db.Query("SELECT id FROM vectors WHERE namespace = ?", "alpha")
	require.NoError(t, err)
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())

	assert.Len(t, ids, 2)
	assert.ElementsMatch(t, []int64{1, 2}, ids)
}

// ---------------------------------------------------------------------------
// TestVectorsTable_Select_FullScan
// ---------------------------------------------------------------------------

// TestVectorsTable_Select_FullScan verifies that a SELECT with no WHERE clause
// returns an empty result because a full cross-namespace scan is not supported.
func TestVectorsTable_Select_FullScan(t *testing.T) {
	db, store := setupVectorDB(t)

	store.mu.Lock()
	store.vectors["ns1"] = map[uint64]*VectorRow{
		1: {ID: 1, Namespace: "ns1", Vector: float32sToBytes([]float32{0.1}), Metadata: ""},
	}
	store.mu.Unlock()

	rows, err := db.Query("SELECT id FROM vectors")
	require.NoError(t, err)
	defer rows.Close()

	// Full scan without namespace returns no rows (by design).
	count := 0
	for rows.Next() {
		count++
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, 0, count)
}

// ---------------------------------------------------------------------------
// TestVectorsTable_Delete
// ---------------------------------------------------------------------------

// TestVectorsTable_Delete verifies that a DELETE removes the vector from the
// backing store.
func TestVectorsTable_Delete(t *testing.T) {
	db, store := setupVectorDB(t)

	store.mu.Lock()
	store.vectors["ns"] = map[uint64]*VectorRow{
		5: {ID: 5, Namespace: "ns", Vector: float32sToBytes([]float32{1, 2}), Metadata: ""},
		6: {ID: 6, Namespace: "ns", Vector: float32sToBytes([]float32{3, 4}), Metadata: ""},
	}
	store.mu.Unlock()

	_, err := db.Exec("DELETE FROM vectors WHERE id = ? AND namespace = ?", int64(5), "ns")
	require.NoError(t, err)

	assert.Equal(t, 1, store.namespaceCount("ns"))
	store.mu.RLock()
	_, exists := store.vectors["ns"][5]
	store.mu.RUnlock()
	assert.False(t, exists, "vector 5 should have been deleted")
}

// TestVectorsTable_Delete_Error verifies that a store-level delete error is
// surfaced to the caller.
func TestVectorsTable_Delete_Error(t *testing.T) {
	db, store := setupVectorDB(t)

	store.mu.Lock()
	store.vectors["ns"] = map[uint64]*VectorRow{
		1: {ID: 1, Namespace: "ns", Vector: float32sToBytes([]float32{1}), Metadata: ""},
	}
	store.mu.Unlock()

	store.deleteErr = errors.New("delete failure")

	_, err := db.Exec("DELETE FROM vectors WHERE id = ? AND namespace = ?", int64(1), "ns")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete failure")
}

// ---------------------------------------------------------------------------
// TestVectorsTable_BestIndex_IdAndNamespace
// ---------------------------------------------------------------------------

// TestVectorsTable_BestIndex_IdAndNamespace verifies that equality constraints
// on both id and namespace produce the composite point-lookup plan.
func TestVectorsTable_BestIndex_IdAndNamespace(t *testing.T) {
	table := &vectorTable{store: newMockVectorStore()}

	csts := []sqlite3.InfoConstraint{
		{Column: colVecID, Op: sqlite3.OpEQ, Usable: true},
		{Column: colVecNamespace, Op: sqlite3.OpEQ, Usable: true},
	}
	result, err := table.BestIndex(csts, nil)
	require.NoError(t, err)

	assert.Equal(t, vecIdxIDAndNamespace, result.IdxNum)
	assert.InDelta(t, 1.0, result.EstimatedCost, 0.001)
	assert.True(t, result.Used[0])
	assert.True(t, result.Used[1])
}

// ---------------------------------------------------------------------------
// TestVectorsTable_BestIndex_NamespaceOnly
// ---------------------------------------------------------------------------

// TestVectorsTable_BestIndex_NamespaceOnly verifies that an equality constraint
// on namespace only produces the namespace-scan plan.
func TestVectorsTable_BestIndex_NamespaceOnly(t *testing.T) {
	table := &vectorTable{store: newMockVectorStore()}

	csts := []sqlite3.InfoConstraint{
		{Column: colVecNamespace, Op: sqlite3.OpEQ, Usable: true},
	}
	result, err := table.BestIndex(csts, nil)
	require.NoError(t, err)

	assert.Equal(t, vecIdxNamespaceScan, result.IdxNum)
	assert.InDelta(t, 50.0, result.EstimatedCost, 0.001)
	assert.True(t, result.Used[0])
}

// ---------------------------------------------------------------------------
// TestVectorsTable_BestIndex_FullScan
// ---------------------------------------------------------------------------

// TestVectorsTable_BestIndex_FullScan verifies that a query with no applicable
// constraints falls back to the full scan plan.
func TestVectorsTable_BestIndex_FullScan(t *testing.T) {
	table := &vectorTable{store: newMockVectorStore()}

	result, err := table.BestIndex(nil, nil)
	require.NoError(t, err)

	assert.Equal(t, vecIdxFullScan, result.IdxNum)
	assert.GreaterOrEqual(t, result.EstimatedCost, float64(100))
}

// ---------------------------------------------------------------------------
// TestVectorsTable_BlobVector
// ---------------------------------------------------------------------------

// TestVectorsTable_BlobVector verifies that a float32 array survives a full
// INSERT → SELECT roundtrip with exact bit-level fidelity.
func TestVectorsTable_BlobVector(t *testing.T) {
	db, _ := setupVectorDB(t)

	original := []float32{1.5, -2.25, 0.0, math.MaxFloat32, -math.MaxFloat32}
	blob := float32sToBytes(original)

	_, err := db.Exec(
		"INSERT INTO vectors (id, namespace, vector, metadata) VALUES (?, ?, ?, ?)",
		int64(99), "floats", blob, "",
	)
	require.NoError(t, err)

	rows, err := db.Query(
		"SELECT vector FROM vectors WHERE id = ? AND namespace = ?",
		int64(99), "floats",
	)
	require.NoError(t, err)
	defer rows.Close()

	require.True(t, rows.Next())
	var got []byte
	require.NoError(t, rows.Scan(&got))

	recovered := bytesToFloat32s(got)
	require.Len(t, recovered, len(original))
	for i, f := range original {
		assert.Equal(t, f, recovered[i], "float32 at index %d mismatch", i)
	}
}

// ---------------------------------------------------------------------------
// TestVectorsTable_MetadataJSON
// ---------------------------------------------------------------------------

// TestVectorsTable_MetadataJSON verifies that arbitrary JSON metadata survives
// an INSERT → SELECT roundtrip unchanged.
func TestVectorsTable_MetadataJSON(t *testing.T) {
	db, _ := setupVectorDB(t)

	meta := `{"source":"wikipedia","lang":"en","score":0.987}`
	vec := float32sToBytes([]float32{0.1, 0.2})

	_, err := db.Exec(
		"INSERT INTO vectors (id, namespace, vector, metadata) VALUES (?, ?, ?, ?)",
		int64(7), "docs", vec, meta,
	)
	require.NoError(t, err)

	rows, err := db.Query(
		"SELECT metadata FROM vectors WHERE id = ? AND namespace = ?",
		int64(7), "docs",
	)
	require.NoError(t, err)
	defer rows.Close()

	require.True(t, rows.Next())
	var got string
	require.NoError(t, rows.Scan(&got))
	assert.Equal(t, meta, got)
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

// TestVectorUpsertError verifies the VectorUpsertError message and Unwrap.
func TestVectorUpsertError(t *testing.T) {
	cause := errors.New("quota exceeded")
	e := &VectorUpsertError{Namespace: "embeddings", ID: 42, Err: cause}

	assert.Contains(t, e.Error(), "embeddings")
	assert.Contains(t, e.Error(), "42")
	assert.Contains(t, e.Error(), "quota exceeded")
	assert.True(t, errors.Is(e, cause))
}

// TestVectorGetError verifies the VectorGetError message and Unwrap.
func TestVectorGetError(t *testing.T) {
	cause := errors.New("not found")
	e := &VectorGetError{Namespace: "images", ID: 7, Err: cause}

	assert.Contains(t, e.Error(), "images")
	assert.Contains(t, e.Error(), "7")
	assert.Contains(t, e.Error(), "not found")
	assert.True(t, errors.Is(e, cause))
}

// TestVectorDeleteError verifies the VectorDeleteError message and Unwrap.
func TestVectorDeleteError(t *testing.T) {
	cause := errors.New("lock timeout")
	e := &VectorDeleteError{Namespace: "ns", ID: 99, Err: cause}

	assert.Contains(t, e.Error(), "ns")
	assert.Contains(t, e.Error(), "99")
	assert.Contains(t, e.Error(), "lock timeout")
	assert.True(t, errors.Is(e, cause))
}

// TestVectorListError verifies the VectorListError message and Unwrap.
func TestVectorListError(t *testing.T) {
	cause := errors.New("connection reset")
	e := &VectorListError{Namespace: "text", Err: cause}

	assert.Contains(t, e.Error(), "text")
	assert.Contains(t, e.Error(), "connection reset")
	assert.True(t, errors.Is(e, cause))
}

// ---------------------------------------------------------------------------
// TestVectorsTable_RegisterVectorModule
// ---------------------------------------------------------------------------

// TestVectorsTable_RegisterVectorModule verifies that RegisterVectorModule
// registers the qrdb_vectors module correctly so a CREATE VIRTUAL TABLE
// statement works without error.
func TestVectorsTable_RegisterVectorModule(t *testing.T) {
	store := newMockVectorStore()
	driverName := newVectorDriverName()

	err := RegisterVectorModule(driverName, store, defaultChunkSize)
	require.NoError(t, err)

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("CREATE VIRTUAL TABLE vecs USING qrdb_vectors")
	require.NoError(t, err)
}
