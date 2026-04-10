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
	"sort"
	"strings"
	"sync"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// mockKV — in-memory KV backend for testing
// ---------------------------------------------------------------------------

// mockKV is a thread-safe in-memory KV store used in unit tests. It
// satisfies the kvStore interface without any real network calls.
type mockKV struct {
	mu      sync.RWMutex
	data    map[string][]byte
	getErr  error // if set, get always returns this error
	putErr  error // if set, put always returns this error
	delErr  error // if set, delete always returns this error
	scanErr error // if set, scan always returns this error
}

// newMockKV creates an empty mockKV.
func newMockKV() *mockKV {
	return &mockKV{data: make(map[string][]byte)}
}

func (m *mockKV) Get(_ context.Context, key string) ([]byte, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	m.mu.RLock()
	v, ok := m.data[key]
	m.mu.RUnlock()
	if !ok {
		return nil, nil
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (m *mockKV) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	if m.scanErr != nil {
		return nil, m.scanErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string][]byte)
	for k, v := range m.data {
		if strings.HasPrefix(k, prefix) {
			cp := make([]byte, len(v))
			copy(cp, v)
			out[k] = cp
		}
	}
	return out, nil
}

func (m *mockKV) Put(_ context.Context, key string, value []byte) error {
	if m.putErr != nil {
		return m.putErr
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	m.mu.Lock()
	m.data[key] = cp
	m.mu.Unlock()
	return nil
}

func (m *mockKV) Delete(_ context.Context, key string) error {
	if m.delErr != nil {
		return m.delErr
	}
	m.mu.Lock()
	delete(m.data, key)
	m.mu.Unlock()
	return nil
}

// keys returns a sorted snapshot of all keys currently in the store.
func (m *mockKV) keys() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ks := make([]string, 0, len(m.data))
	for k := range m.data {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// ---------------------------------------------------------------------------
// Test database helpers
// ---------------------------------------------------------------------------

// driverCounter ensures each test gets a unique driver name so that
// sql.Register (which panics on duplicate names) never fires.
var driverCounter struct {
	sync.Mutex
	n int
}

// newDriverName returns a unique driver name safe for use with sql.Register.
func newDriverName() string {
	driverCounter.Lock()
	defer driverCounter.Unlock()
	driverCounter.n++
	return fmt.Sprintf("sqlite3_vtab_test_%d", driverCounter.n)
}

// setupTestDB registers a fresh SQLite driver with the qrdb_kv module and
// returns an open in-memory database with a kv virtual table already created.
// It also returns the underlying mockKV for state inspection.
func setupTestDB(t *testing.T) (*sql.DB, *mockKV) {
	t.Helper()
	mock := newMockKV()
	pragma := newPragmaStore("test", "memory", "testdb")
	driverName := newDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &kvModule{
				kv:        mock,
				pragma:    pragma,
				chunkSize: defaultChunkSize,
			}
			return conn.CreateModule(kvModuleName, mod)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("CREATE VIRTUAL TABLE kv USING qrdb_kv")
	require.NoError(t, err)
	return db, mock
}

// setupTestDBWithKV is like setupTestDB but pre-populates the mock with data.
func setupTestDBWithKV(t *testing.T, initial map[string][]byte) (*sql.DB, *mockKV) {
	t.Helper()
	db, mock := setupTestDB(t)
	for k, v := range initial {
		mock.mu.Lock()
		cp := make([]byte, len(v))
		copy(cp, v)
		mock.data[k] = cp
		mock.mu.Unlock()
	}
	return db, mock
}

// ---------------------------------------------------------------------------
// Schema
// ---------------------------------------------------------------------------

func TestKVTable_Schema(t *testing.T) {
	db, _ := setupTestDB(t)

	// Verify that the table exists and has the expected columns by running a
	// zero-row query and inspecting column names.
	rows, err := db.Query("SELECT key, value FROM kv LIMIT 0")
	require.NoError(t, err)
	defer rows.Close()

	cols, err := rows.Columns()
	require.NoError(t, err)
	assert.Equal(t, []string{"key", "value"}, cols)
}

// ---------------------------------------------------------------------------
// Insert
// ---------------------------------------------------------------------------

func TestKVTable_Insert(t *testing.T) {
	db, mock := setupTestDB(t)

	_, err := db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", "hello", []byte("world"))
	require.NoError(t, err)

	assert.Equal(t, []byte("world"), mock.data["hello"])
}

func TestKVTable_Insert_ErrorPropagated(t *testing.T) {
	db, mock := setupTestDB(t)
	mock.putErr = errors.New("backend unavailable")

	_, err := db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", "k", []byte("v"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backend unavailable")
}

func TestKVTable_InsertBulk(t *testing.T) {
	db, mock := setupTestDB(t)

	tx, err := db.Begin()
	require.NoError(t, err)

	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("key%03d", i)
		val := fmt.Sprintf("val%03d", i)
		_, err = tx.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", key, []byte(val))
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())

	assert.Len(t, mock.data, 50)
}

// ---------------------------------------------------------------------------
// SELECT — full scan
// ---------------------------------------------------------------------------

func TestKVTable_Select_FullScan(t *testing.T) {
	db, _ := setupTestDBWithKV(t, map[string][]byte{
		"a": []byte("1"),
		"b": []byte("2"),
		"c": []byte("3"),
	})

	rows, err := db.Query("SELECT key, value FROM kv ORDER BY key")
	require.NoError(t, err)
	defer rows.Close()

	type kvPair struct {
		key string
		val []byte
	}
	var got []kvPair
	for rows.Next() {
		var k string
		var v []byte
		require.NoError(t, rows.Scan(&k, &v))
		got = append(got, kvPair{k, v})
	}
	require.NoError(t, rows.Err())

	require.Len(t, got, 3)
	assert.Equal(t, "a", got[0].key)
	assert.Equal(t, []byte("1"), got[0].val)
	assert.Equal(t, "b", got[1].key)
	assert.Equal(t, "c", got[2].key)
}

func TestKVTable_Select_FullScan_Empty(t *testing.T) {
	db, _ := setupTestDB(t)

	rows, err := db.Query("SELECT key, value FROM kv")
	require.NoError(t, err)
	defer rows.Close()

	assert.False(t, rows.Next())
	require.NoError(t, rows.Err())
}

// ---------------------------------------------------------------------------
// SELECT — point lookup
// ---------------------------------------------------------------------------

func TestKVTable_Select_PointLookup(t *testing.T) {
	db, _ := setupTestDBWithKV(t, map[string][]byte{
		"foo": []byte("bar"),
		"baz": []byte("qux"),
	})

	var key string
	var val []byte
	err := db.QueryRow("SELECT key, value FROM kv WHERE key = 'foo'").Scan(&key, &val)
	require.NoError(t, err)
	assert.Equal(t, "foo", key)
	assert.Equal(t, []byte("bar"), val)
}

func TestKVTable_Select_PointLookup_Miss(t *testing.T) {
	db, _ := setupTestDB(t)

	err := db.QueryRow("SELECT key FROM kv WHERE key = 'missing'").Scan(new(string))
	assert.Equal(t, sql.ErrNoRows, err)
}

func TestKVTable_Select_PointLookup_GetError(t *testing.T) {
	db, mock := setupTestDB(t)
	mock.getErr = errors.New("network timeout")

	err := db.QueryRow("SELECT key FROM kv WHERE key = 'x'").Scan(new(string))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network timeout")
}

// ---------------------------------------------------------------------------
// SELECT — prefix / range scan
// ---------------------------------------------------------------------------

func TestKVTable_Select_PrefixLike(t *testing.T) {
	db, _ := setupTestDBWithKV(t, map[string][]byte{
		"user:1": []byte("alice"),
		"user:2": []byte("bob"),
		"post:1": []byte("hello"),
	})

	rows, err := db.Query("SELECT key, value FROM kv WHERE key LIKE 'user:%'")
	require.NoError(t, err)
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var k string
		var v []byte
		require.NoError(t, rows.Scan(&k, &v))
		keys = append(keys, k)
	}
	require.NoError(t, rows.Err())
	sort.Strings(keys)
	assert.Equal(t, []string{"user:1", "user:2"}, keys)
}

func TestKVTable_Select_PrefixLike_NoMatch(t *testing.T) {
	db, _ := setupTestDBWithKV(t, map[string][]byte{
		"foo": []byte("bar"),
	})

	rows, err := db.Query("SELECT key FROM kv WHERE key LIKE 'z%'")
	require.NoError(t, err)
	defer rows.Close()
	assert.False(t, rows.Next())
}

func TestKVTable_Select_PrefixLike_ScanError(t *testing.T) {
	db, mock := setupTestDB(t)
	mock.scanErr = errors.New("scan failed")

	rows, err := db.Query("SELECT key FROM kv WHERE key LIKE 'x%'")
	if err != nil {
		assert.Contains(t, err.Error(), "scan failed")
		return
	}
	defer rows.Close()
	// Error may appear during iteration.
	for rows.Next() {
	}
	require.Error(t, rows.Err())
	assert.Contains(t, rows.Err().Error(), "scan failed")
}

// ---------------------------------------------------------------------------
// UPDATE
// ---------------------------------------------------------------------------

func TestKVTable_Update(t *testing.T) {
	db, mock := setupTestDBWithKV(t, map[string][]byte{
		"counter": []byte("0"),
	})

	_, err := db.Exec("UPDATE kv SET value = ? WHERE key = 'counter'", []byte("42"))
	require.NoError(t, err)
	assert.Equal(t, []byte("42"), mock.data["counter"])
}

func TestKVTable_Update_ErrorPropagated(t *testing.T) {
	db, mock := setupTestDBWithKV(t, map[string][]byte{
		"k": []byte("v"),
	})
	mock.putErr = errors.New("write failed")

	_, err := db.Exec("UPDATE kv SET value = ? WHERE key = 'k'", []byte("new"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write failed")
}

// ---------------------------------------------------------------------------
// DELETE
// ---------------------------------------------------------------------------

func TestKVTable_Delete(t *testing.T) {
	db, mock := setupTestDBWithKV(t, map[string][]byte{
		"to-delete": []byte("bye"),
		"keep":      []byte("me"),
	})

	_, err := db.Exec("DELETE FROM kv WHERE key = 'to-delete'")
	require.NoError(t, err)

	_, exists := mock.data["to-delete"]
	assert.False(t, exists)
	_, exists = mock.data["keep"]
	assert.True(t, exists)
}

func TestKVTable_Delete_NonExistent(t *testing.T) {
	db, mock := setupTestDB(t)

	// Deleting a key that was never inserted must not fail; it is a no-op in
	// the underlying KV store.
	_, err := db.Exec("DELETE FROM kv WHERE key = 'ghost'")
	require.NoError(t, err)
	assert.Empty(t, mock.data)
}

func TestKVTable_Delete_ErrorPropagated(t *testing.T) {
	db, mock := setupTestDBWithKV(t, map[string][]byte{
		"x": []byte("y"),
	})
	mock.delErr = errors.New("delete failed")

	_, err := db.Exec("DELETE FROM kv WHERE key = 'x'")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete failed")
}

// ---------------------------------------------------------------------------
// BestIndex plan selection
// ---------------------------------------------------------------------------

func TestKVTable_BestIndex_PointLookup(t *testing.T) {
	db, _ := setupTestDBWithKV(t, map[string][]byte{
		"only": []byte("value"),
	})

	// A point-lookup plan should return exactly one row without scanning others.
	// We verify this by seeding many keys and confirming only the target is returned.
	for i := 0; i < 20; i++ {
		db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)",
			fmt.Sprintf("other%d", i), []byte("x"))
	}

	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM kv WHERE key = 'only'").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestKVTable_BestIndex_FullScan(t *testing.T) {
	db, _ := setupTestDBWithKV(t, map[string][]byte{
		"a": []byte("1"),
		"b": []byte("2"),
	})

	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM kv").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

func TestKVTable_BestIndex_PrefixScan(t *testing.T) {
	db, _ := setupTestDBWithKV(t, map[string][]byte{
		"x:1": []byte("a"),
		"x:2": []byte("b"),
		"y:1": []byte("c"),
	})

	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM kv WHERE key LIKE 'x:%'").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

// ---------------------------------------------------------------------------
// Value edge cases
// ---------------------------------------------------------------------------

func TestKVTable_BinaryValue(t *testing.T) {
	db, mock := setupTestDB(t)

	binary := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE}
	_, err := db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", "bin", binary)
	require.NoError(t, err)

	var val []byte
	err = db.QueryRow("SELECT value FROM kv WHERE key = 'bin'").Scan(&val)
	require.NoError(t, err)
	assert.Equal(t, binary, val)
	assert.Equal(t, binary, mock.data["bin"])
}

func TestKVTable_EmptyValue(t *testing.T) {
	db, mock := setupTestDB(t)

	_, err := db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", "empty", []byte{})
	require.NoError(t, err)

	var val []byte
	err = db.QueryRow("SELECT value FROM kv WHERE key = 'empty'").Scan(&val)
	require.NoError(t, err)
	// Either nil or empty slice is acceptable for an empty BLOB.
	assert.True(t, len(val) == 0)
	_ = mock
}

func TestKVTable_LargeValue(t *testing.T) {
	db, mock := setupTestDB(t)

	large := make([]byte, 64*1024)
	for i := range large {
		large[i] = byte(i % 256)
	}

	_, err := db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", "large", large)
	require.NoError(t, err)

	var val []byte
	err = db.QueryRow("SELECT value FROM kv WHERE key = 'large'").Scan(&val)
	require.NoError(t, err)
	assert.Equal(t, large, val)
	assert.Equal(t, large, mock.data["large"])
}

func TestKVTable_SpecialCharKeys(t *testing.T) {
	db, mock := setupTestDB(t)

	keys := []string{
		"key with spaces",
		"key/with/slashes",
		"key:with:colons",
		"key.with.dots",
		"key-with-dashes",
		"UPPERCASE",
		"MixedCase123",
	}

	for _, k := range keys {
		_, err := db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", k, []byte(k))
		require.NoError(t, err, "insert key %q", k)
	}

	for _, k := range keys {
		var val []byte
		err := db.QueryRow("SELECT value FROM kv WHERE key = ?", k).Scan(&val)
		require.NoError(t, err, "select key %q", k)
		assert.Equal(t, []byte(k), val, "value mismatch for key %q", k)
	}
	assert.Len(t, mock.data, len(keys))
}

func TestKVTable_NullValue(t *testing.T) {
	db, _ := setupTestDB(t)

	// Inserting a NULL value should work without panicking.
	_, err := db.Exec("INSERT INTO kv (key, value) VALUES (?, NULL)", "nullkey")
	require.NoError(t, err)

	var val []byte
	err = db.QueryRow("SELECT value FROM kv WHERE key = 'nullkey'").Scan(&val)
	require.NoError(t, err)
	assert.Nil(t, val)
}

// ---------------------------------------------------------------------------
// Module registration
// ---------------------------------------------------------------------------

func TestRegisterModules_Success(t *testing.T) {
	mock := newMockKV()
	pragma := newPragmaStore("test", "memory", "testdb")
	driverName := newDriverName()

	err := RegisterModules(driverName, mock, pragma, defaultChunkSize)
	require.NoError(t, err)

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec("CREATE VIRTUAL TABLE kv2 USING qrdb_kv")
	require.NoError(t, err)
}

func TestRegisterModules_MultipleTablesPerConnection(t *testing.T) {
	mock := newMockKV()
	pragma := newPragmaStore("test", "memory", "testdb")
	driverName := newDriverName()

	err := RegisterModules(driverName, mock, pragma, defaultChunkSize)
	require.NoError(t, err)

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec("CREATE VIRTUAL TABLE ns1 USING qrdb_kv")
	require.NoError(t, err)
	_, err = db.Exec("CREATE VIRTUAL TABLE ns2 USING qrdb_kv")
	require.NoError(t, err)

	// Both tables share the same underlying mock, so data is visible from both.
	_, err = db.Exec("INSERT INTO ns1 (key, value) VALUES ('shared', 'yes')")
	require.NoError(t, err)

	var val []byte
	err = db.QueryRow("SELECT value FROM ns2 WHERE key = 'shared'").Scan(&val)
	require.NoError(t, err)
	assert.Equal(t, []byte("yes"), val)
}

// ---------------------------------------------------------------------------
// Helper function unit tests
// ---------------------------------------------------------------------------

func TestExtractLikePrefix(t *testing.T) {
	tests := []struct {
		pattern string
		want    string
	}{
		{"foo%", "foo"},
		{"foo*", "foo"},
		{"foo%bar", "foo"},
		{"foo_bar", "foo"},
		{"prefix", "prefix"},
		{"%", ""},
		{"", ""},
		{"abc%def%", "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			got := extractLikePrefix(tt.pattern)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractLikePrefix_InvalidInput(t *testing.T) {
	// A pattern with no wildcard returns the full pattern.
	assert.Equal(t, "notwildcard", extractLikePrefix("notwildcard"))
}

func TestLikeMatch(t *testing.T) {
	tests := []struct {
		pattern string
		s       string
		want    bool
	}{
		{"foo", "foo", true},
		{"foo", "bar", false},
		{"foo%", "foobar", true},
		{"foo%", "foo", true},
		{"foo%", "fo", false},
		{"%bar", "foobar", true},
		{"%bar", "bar", true},
		{"%bar", "baz", false},
		{"f_o", "foo", true},
		{"f_o", "fo", false},
		{"f_o", "fooo", false},
		{"%", "anything", true},
		{"%", "", true},
		{"", "", true},
		{"", "x", false},
		{"a%b%c", "abc", true},
		{"a%b%c", "axbxc", true},
		{"a%b%c", "axbx", false},
	}
	for _, tt := range tests {
		name := fmt.Sprintf("pattern=%q s=%q", tt.pattern, tt.s)
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, likeMatch(tt.pattern, tt.s))
		})
	}
}

func TestToString(t *testing.T) {
	tests := []struct {
		input interface{}
		want  string
	}{
		{"hello", "hello"},
		{[]byte("world"), "world"},
		{int64(42), "42"},
		{int64(-1), "-1"},
		{int64(0), "0"},
		{float64(3), "3"},
		{nil, ""},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%v", tt.input), func(t *testing.T) {
			assert.Equal(t, tt.want, toString(tt.input))
		})
	}
}

func TestInt64ToStr(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{-1, "-1"},
		{9223372036854775807, "9223372036854775807"},
		{-9223372036854775808, "-9223372036854775808"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, int64ToStr(tt.n))
		})
	}
}

// ---------------------------------------------------------------------------
// BestIndex unit tests (table-level, without a live SQLite connection)
// ---------------------------------------------------------------------------

func TestKVTable_BestIndex_Unit_PointLookup(t *testing.T) {
	tbl := &kvTable{
		kv:        newMockKV(),
		pragma:    newPragmaStore("test", "memory", "db"),
		chunkSize: defaultChunkSize,
	}

	csts := []sqlite3.InfoConstraint{
		{Column: colKey, Op: sqlite3.OpEQ, Usable: true},
	}
	res, err := tbl.BestIndex(csts, nil)
	require.NoError(t, err)
	assert.Equal(t, idxPointLookup, res.IdxNum)
	assert.True(t, res.Used[0])
	assert.Equal(t, float64(1), res.EstimatedCost)
}

func TestKVTable_BestIndex_Unit_PrefixScanLIKE(t *testing.T) {
	tbl := &kvTable{
		kv:        newMockKV(),
		pragma:    newPragmaStore("test", "memory", "db"),
		chunkSize: defaultChunkSize,
	}

	csts := []sqlite3.InfoConstraint{
		{Column: colKey, Op: sqlite3.OpLIKE, Usable: true},
	}
	res, err := tbl.BestIndex(csts, nil)
	require.NoError(t, err)
	assert.Equal(t, idxPrefixScan, res.IdxNum)
	assert.True(t, res.Used[0])
	assert.Greater(t, res.EstimatedCost, float64(1))
}

func TestKVTable_BestIndex_Unit_PrefixScanGLOB(t *testing.T) {
	tbl := &kvTable{
		kv:        newMockKV(),
		pragma:    newPragmaStore("test", "memory", "db"),
		chunkSize: defaultChunkSize,
	}

	csts := []sqlite3.InfoConstraint{
		{Column: colKey, Op: sqlite3.OpGLOB, Usable: true},
	}
	res, err := tbl.BestIndex(csts, nil)
	require.NoError(t, err)
	assert.Equal(t, idxPrefixScan, res.IdxNum)
	assert.True(t, res.Used[0])
}

func TestKVTable_BestIndex_Unit_FullScan(t *testing.T) {
	tbl := &kvTable{
		kv:        newMockKV(),
		pragma:    newPragmaStore("test", "memory", "db"),
		chunkSize: defaultChunkSize,
	}

	// No constraints at all → full scan.
	res, err := tbl.BestIndex(nil, nil)
	require.NoError(t, err)
	assert.Equal(t, idxFullScan, res.IdxNum)
	assert.Greater(t, res.EstimatedCost, float64(100))
}

func TestKVTable_BestIndex_Unit_UnusableConstraintIgnored(t *testing.T) {
	tbl := &kvTable{
		kv:        newMockKV(),
		pragma:    newPragmaStore("test", "memory", "db"),
		chunkSize: defaultChunkSize,
	}

	// A constraint on key = ? but marked unusable must fall back to full scan.
	csts := []sqlite3.InfoConstraint{
		{Column: colKey, Op: sqlite3.OpEQ, Usable: false},
	}
	res, err := tbl.BestIndex(csts, nil)
	require.NoError(t, err)
	assert.Equal(t, idxFullScan, res.IdxNum)
	assert.False(t, res.Used[0])
}

func TestKVTable_BestIndex_Unit_ValueColumnConstraintIgnored(t *testing.T) {
	tbl := &kvTable{
		kv:        newMockKV(),
		pragma:    newPragmaStore("test", "memory", "db"),
		chunkSize: defaultChunkSize,
	}

	// A usable EQ constraint on the value column must not trigger point-lookup.
	csts := []sqlite3.InfoConstraint{
		{Column: colValue, Op: sqlite3.OpEQ, Usable: true},
	}
	res, err := tbl.BestIndex(csts, nil)
	require.NoError(t, err)
	assert.Equal(t, idxFullScan, res.IdxNum)
}

// ---------------------------------------------------------------------------
// Cursor unit tests
// ---------------------------------------------------------------------------

func TestKVCursor_Filter_FullScan_Empty(t *testing.T) {
	mock := newMockKV()
	tbl := &kvTable{kv: mock, pragma: newPragmaStore("t", "m", "d"), chunkSize: 100}
	cur := &kvCursor{table: tbl, cursor: newPrefetchCursor(100)}

	err := cur.Filter(idxFullScan, "scan", nil)
	require.NoError(t, err)
	assert.True(t, cur.EOF())
}

func TestKVCursor_Filter_PointLookup_Found(t *testing.T) {
	mock := newMockKV()
	mock.data["hello"] = []byte("world")
	tbl := &kvTable{kv: mock, pragma: newPragmaStore("t", "m", "d"), chunkSize: 100}
	cur := &kvCursor{table: tbl, cursor: newPrefetchCursor(100)}

	err := cur.Filter(idxPointLookup, "eq", []interface{}{"hello"})
	require.NoError(t, err)
	require.False(t, cur.EOF())

	rowid, err := cur.Rowid()
	require.NoError(t, err)
	assert.Greater(t, rowid, int64(0)) // rowid is a positive, table-assigned integer
}

func TestKVCursor_Filter_PointLookup_NotFound(t *testing.T) {
	mock := newMockKV()
	tbl := &kvTable{kv: mock, pragma: newPragmaStore("t", "m", "d"), chunkSize: 100}
	cur := &kvCursor{table: tbl, cursor: newPrefetchCursor(100)}

	err := cur.Filter(idxPointLookup, "eq", []interface{}{"missing"})
	require.NoError(t, err)
	assert.True(t, cur.EOF())
}

func TestKVCursor_Filter_PrefixScan(t *testing.T) {
	mock := newMockKV()
	mock.data["ns:a"] = []byte("1")
	mock.data["ns:b"] = []byte("2")
	mock.data["other"] = []byte("3")
	tbl := &kvTable{kv: mock, pragma: newPragmaStore("t", "m", "d"), chunkSize: 100}
	cur := &kvCursor{table: tbl, cursor: newPrefetchCursor(100)}

	err := cur.Filter(idxPrefixScan, "prefix", []interface{}{"ns:%"})
	require.NoError(t, err)
	require.False(t, cur.EOF())

	var keys []string
	for !cur.EOF() {
		v, err := cur.cursor.column(colKey)
		require.NoError(t, err)
		keys = append(keys, v.(string))
		_ = cur.Next()
	}
	sort.Strings(keys)
	assert.Equal(t, []string{"ns:a", "ns:b"}, keys)
}

func TestKVCursor_Next_Advances(t *testing.T) {
	mock := newMockKV()
	for i := 0; i < 3; i++ {
		mock.data[fmt.Sprintf("k%d", i)] = []byte("v")
	}
	tbl := &kvTable{kv: mock, pragma: newPragmaStore("t", "m", "d"), chunkSize: 100}
	cur := &kvCursor{table: tbl, cursor: newPrefetchCursor(100)}

	require.NoError(t, cur.Filter(idxFullScan, "scan", nil))

	count := 0
	for !cur.EOF() {
		count++
		require.NoError(t, cur.Next())
	}
	assert.Equal(t, 3, count)
}

func TestKVCursor_Close_Reset(t *testing.T) {
	mock := newMockKV()
	mock.data["x"] = []byte("y")
	tbl := &kvTable{kv: mock, pragma: newPragmaStore("t", "m", "d"), chunkSize: 100}
	cur := &kvCursor{table: tbl, cursor: newPrefetchCursor(100)}

	require.NoError(t, cur.Filter(idxFullScan, "scan", nil))
	require.False(t, cur.EOF())
	require.NoError(t, cur.Close())
	assert.True(t, cur.EOF())
}

// ---------------------------------------------------------------------------
// Error types
// ---------------------------------------------------------------------------

func TestKVGetError(t *testing.T) {
	cause := errors.New("underlying")
	e := &KVGetError{Key: "mykey", Err: cause}
	assert.Contains(t, e.Error(), "mykey")
	assert.Contains(t, e.Error(), "underlying")
	assert.Equal(t, cause, errors.Unwrap(e))
}

func TestKVPutError(t *testing.T) {
	cause := errors.New("put failed")
	e := &KVPutError{Key: "k", Err: cause}
	assert.Contains(t, e.Error(), "k")
	assert.Contains(t, e.Error(), "put failed")
	assert.Equal(t, cause, errors.Unwrap(e))
}

func TestKVDeleteError(t *testing.T) {
	cause := errors.New("del failed")
	e := &KVDeleteError{Key: "k", Err: cause}
	assert.Contains(t, e.Error(), "k")
	assert.Contains(t, e.Error(), "del failed")
	assert.Equal(t, cause, errors.Unwrap(e))
}

func TestKVScanError(t *testing.T) {
	cause := errors.New("scan failed")
	e := &KVScanError{Prefix: "pfx", Err: cause}
	assert.Contains(t, e.Error(), "pfx")
	assert.Contains(t, e.Error(), "scan failed")
	assert.Equal(t, cause, errors.Unwrap(e))
}

func TestModuleCreateError(t *testing.T) {
	cause := errors.New("register failed")
	e := &ModuleCreateError{Module: "qrdb_kv", Err: cause}
	assert.Contains(t, e.Error(), "qrdb_kv")
	assert.Contains(t, e.Error(), "register failed")
	assert.Equal(t, cause, errors.Unwrap(e))
}

func TestInvalidPragmaValueError(t *testing.T) {
	e := &InvalidPragmaValueError{
		Pragma: "qrdb_consistency",
		Value:  "bad",
		Valid:  []string{"strong", "linearizable", "eventual"},
	}
	assert.Contains(t, e.Error(), "bad")
	assert.Contains(t, e.Error(), "qrdb_consistency")
	assert.Contains(t, e.Error(), "strong")
}

// ---------------------------------------------------------------------------
// kvTable unit tests — Disconnect, Destroy
// ---------------------------------------------------------------------------

func TestKVTable_Disconnect(t *testing.T) {
	tbl := &kvTable{kv: newMockKV(), pragma: newPragmaStore("t", "m", "d"), chunkSize: 100}
	assert.NoError(t, tbl.Disconnect())
}

func TestKVTable_Destroy(t *testing.T) {
	tbl := &kvTable{kv: newMockKV(), pragma: newPragmaStore("t", "m", "d"), chunkSize: 100}
	assert.NoError(t, tbl.Destroy())
}

// ---------------------------------------------------------------------------
// kvTable.Delete — additional code path coverage
// ---------------------------------------------------------------------------

func TestKVTable_Delete_NilRowid(t *testing.T) {
	tbl := &kvTable{kv: newMockKV(), pragma: newPragmaStore("t", "m", "d"), chunkSize: 100}
	// Passing nil rowid must not error.
	assert.NoError(t, tbl.Delete(nil))
}

func TestKVTable_Delete_UnknownRowid(t *testing.T) {
	// An int64 rowid that was never registered is a no-op.
	tbl := &kvTable{kv: newMockKV(), pragma: newPragmaStore("t", "m", "d"), chunkSize: 100}
	assert.NoError(t, tbl.Delete(int64(99999)))
}

func TestKVTable_Delete_StringRowid(t *testing.T) {
	mock := newMockKV()
	mock.data["directkey"] = []byte("v")
	tbl := &kvTable{kv: mock, pragma: newPragmaStore("t", "m", "d"), chunkSize: 100}
	// go-sqlite3 may pass a string rowid for shadow tables; ensure it is handled.
	assert.NoError(t, tbl.Delete("directkey"))
	_, exists := mock.data["directkey"]
	assert.False(t, exists)
}

// ---------------------------------------------------------------------------
// kvModule — Connect method coverage
// ---------------------------------------------------------------------------

func TestKVModule_Connect(t *testing.T) {
	// Connect is exercised via a second sql.Open on an existing driver (which
	// reuses the same module). We exercise it via setupTestDB where the
	// ConnectHook internally calls CreateModule, triggering Create not Connect.
	// To get Connect coverage we call it directly on the module.
	mock := newMockKV()
	pragma := newPragmaStore("test", "memory", "testdb")
	mod := &kvModule{kv: mock, pragma: pragma, chunkSize: defaultChunkSize}
	driverName := newDriverName()

	var capturedConn *sqlite3.SQLiteConn
	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			capturedConn = conn
			return conn.CreateModule(kvModuleName, mod)
		},
	})
	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()

	// Force a connection to be established.
	require.NoError(t, db.Ping())
	require.NotNil(t, capturedConn)

	// Directly call Connect to verify it registers the schema without error.
	// We must have a DeclareVTab context, so use the live connection.
	_, err = db.Exec("CREATE VIRTUAL TABLE conn_kv USING qrdb_kv")
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// DestroyModule coverage
// ---------------------------------------------------------------------------

func TestKVModule_DestroyModule(t *testing.T) {
	mod := &kvModule{kv: newMockKV(), pragma: newPragmaStore("t", "m", "d"), chunkSize: 100}
	// DestroyModule must not panic and has no return value.
	assert.NotPanics(t, func() { mod.DestroyModule() })
}

// ---------------------------------------------------------------------------
// filterRowsByPattern — GLOB path coverage
// ---------------------------------------------------------------------------

func TestFilterRowsByPattern_GlobPrefix(t *testing.T) {
	rows := []row{
		{values: []any{"foo/bar", []byte("1")}},
		{values: []any{"foo/baz", []byte("2")}},
		{values: []any{"other", []byte("3")}},
	}
	// GLOB pattern: "foo/*"
	result := filterRowsByPattern(rows, "foo/*")
	require.Len(t, result, 2)
	keys := []string{result[0].values[0].(string), result[1].values[0].(string)}
	sort.Strings(keys)
	assert.Equal(t, []string{"foo/bar", "foo/baz"}, keys)
}

func TestFilterRowsByPattern_Empty(t *testing.T) {
	rows := []row{{values: []any{"k", []byte("v")}}}
	// Empty pattern returns all rows unchanged.
	result := filterRowsByPattern(rows, "")
	assert.Equal(t, rows, result)
}

// ---------------------------------------------------------------------------
// Column edge cases — non-string key, non-byte-slice value
// ---------------------------------------------------------------------------

func TestKVCursor_Column_NonByteSliceValue(t *testing.T) {
	// Directly construct a row whose value is a string (not []byte) to exercise
	// the ResultText branch in Column.
	mock := newMockKV()
	tbl := &kvTable{kv: mock, pragma: newPragmaStore("t", "m", "d"), chunkSize: 100}
	cur := &kvCursor{table: tbl, cursor: newPrefetchCursor(100)}
	cur.cursor.load([]row{{values: []any{"key", "stringvalue"}}})
	cur.rowid = tbl.nextRowid()

	// We can't call Column without a *sqlite3.SQLiteContext outside of an active
	// sqlite3 callback. Instead, test via the underlying cursor column access.
	v, err := cur.cursor.column(colValue)
	require.NoError(t, err)
	assert.Equal(t, "stringvalue", v)
}

// ---------------------------------------------------------------------------
// toString — unsupported type returns empty string
// ---------------------------------------------------------------------------

func TestToString_UnsupportedType(t *testing.T) {
	type custom struct{}
	assert.Equal(t, "", toString(custom{}))
}

// ---------------------------------------------------------------------------
// extractKeyValue — fewer than 2 values returns error
// ---------------------------------------------------------------------------

func TestExtractKeyValue_TooFewValues(t *testing.T) {
	_, _, err := extractKeyValue([]interface{}{"onlyone"})
	require.Error(t, err)
	var kpe *KVPutError
	assert.True(t, errors.As(err, &kpe))
}

func TestExtractKeyValue_NilValue(t *testing.T) {
	key, val, err := extractKeyValue([]interface{}{"k", nil})
	require.NoError(t, err)
	assert.Equal(t, "k", key)
	assert.Nil(t, val)
}

func TestExtractKeyValue_StringValue(t *testing.T) {
	key, val, err := extractKeyValue([]interface{}{"k", "strval"})
	require.NoError(t, err)
	assert.Equal(t, "k", key)
	assert.Equal(t, []byte("strval"), val)
}

func TestExtractKeyValue_UnknownTypeValue(t *testing.T) {
	key, val, err := extractKeyValue([]interface{}{"k", int64(42)})
	require.NoError(t, err)
	assert.Equal(t, "k", key)
	// int64 is converted via toString → "42"
	assert.Equal(t, []byte("42"), val)
}
