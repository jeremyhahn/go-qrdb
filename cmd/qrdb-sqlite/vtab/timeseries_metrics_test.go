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
// mockMetricsStore — in-memory metrics backend for testing
// ---------------------------------------------------------------------------

// mockMetricsStore is a thread-safe in-memory metrics store used in unit
// tests. It satisfies the metricsStore interface without any real network
// calls.
type mockMetricsStore struct {
	mu      sync.RWMutex
	metrics []MetricInfo
	listErr error
	getErr  error
}

func newMockMetricsStore() *mockMetricsStore {
	return &mockMetricsStore{}
}

func (m *mockMetricsStore) ListMetrics(_ context.Context) ([]MetricInfo, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := make([]MetricInfo, len(m.metrics))
	copy(cp, m.metrics)
	return cp, nil
}

func (m *mockMetricsStore) GetMetric(_ context.Context, name string) (*MetricInfo, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, mi := range m.metrics {
		if mi.Name == name {
			cp := mi
			return &cp, nil
		}
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// Test database helpers
// ---------------------------------------------------------------------------

// metricsDriverCounter ensures unique driver names across tests.
var metricsDriverCounter struct {
	sync.Mutex
	n int
}

func newMetricsDriverName() string {
	metricsDriverCounter.Lock()
	defer metricsDriverCounter.Unlock()
	metricsDriverCounter.n++
	return fmt.Sprintf("sqlite3_metrics_test_%d", metricsDriverCounter.n)
}

// setupMetricsDB registers a fresh SQLite driver with the qrdb_ts_metrics
// module and returns an open in-memory database with the virtual table already
// created, plus the underlying mockMetricsStore for state inspection.
func setupMetricsDB(t *testing.T) (*sql.DB, *mockMetricsStore) {
	t.Helper()
	store := newMockMetricsStore()
	driverName := newMetricsDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &metricsModule{
				store:     store,
				chunkSize: defaultChunkSize,
			}
			return conn.CreateModule(metricsModuleName, mod)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("CREATE VIRTUAL TABLE ts_metrics USING qrdb_ts_metrics")
	require.NoError(t, err)

	return db, store
}

// ---------------------------------------------------------------------------
// TestMetricsTable_Schema
// ---------------------------------------------------------------------------

// TestMetricsTable_Schema verifies that the virtual table exposes the expected
// column names in schema order.
func TestMetricsTable_Schema(t *testing.T) {
	db, _ := setupMetricsDB(t)

	rows, err := db.Query("SELECT * FROM ts_metrics LIMIT 0")
	require.NoError(t, err)
	defer rows.Close()

	cols, err := rows.Columns()
	require.NoError(t, err)

	want := []string{"name", "description", "unit", "type", "point_count"}
	assert.Equal(t, want, cols)
}

// ---------------------------------------------------------------------------
// TestMetricsTable_Select_ByName
// ---------------------------------------------------------------------------

// TestMetricsTable_Select_ByName verifies that WHERE name = ? triggers a point
// lookup and returns exactly the requested metric.
func TestMetricsTable_Select_ByName(t *testing.T) {
	db, store := setupMetricsDB(t)

	store.mu.Lock()
	store.metrics = []MetricInfo{
		{Name: "cpu.usage", Description: "CPU usage percent", Unit: "%", MetricType: "gauge", PointCount: 42},
		{Name: "mem.rss", Description: "RSS memory", Unit: "bytes", MetricType: "gauge", PointCount: 100},
	}
	store.mu.Unlock()

	var name, desc, unit, mtype string
	var pointCount int64
	err := db.QueryRow(
		"SELECT name, description, unit, type, point_count FROM ts_metrics WHERE name = ?",
		"cpu.usage",
	).Scan(&name, &desc, &unit, &mtype, &pointCount)
	require.NoError(t, err)

	assert.Equal(t, "cpu.usage", name)
	assert.Equal(t, "CPU usage percent", desc)
	assert.Equal(t, "%", unit)
	assert.Equal(t, "gauge", mtype)
	assert.Equal(t, int64(42), pointCount)
}

// TestMetricsTable_Select_ByName_NotFound verifies that a lookup for a
// non-existent metric name returns zero rows without error.
func TestMetricsTable_Select_ByName_NotFound(t *testing.T) {
	db, _ := setupMetricsDB(t)

	rows, err := db.Query("SELECT * FROM ts_metrics WHERE name = ?", "nonexistent")
	require.NoError(t, err)
	defer rows.Close()
	assert.False(t, rows.Next(), "expected no rows for unknown metric")
}

// ---------------------------------------------------------------------------
// TestMetricsTable_Select_FullScan
// ---------------------------------------------------------------------------

// TestMetricsTable_Select_FullScan verifies that SELECT * returns all metrics
// currently in the store.
func TestMetricsTable_Select_FullScan(t *testing.T) {
	db, store := setupMetricsDB(t)

	store.mu.Lock()
	store.metrics = []MetricInfo{
		{Name: "disk.read", PointCount: 10},
		{Name: "disk.write", PointCount: 20},
		{Name: "net.rx", PointCount: 30},
	}
	store.mu.Unlock()

	rows, err := db.Query("SELECT name, point_count FROM ts_metrics")
	require.NoError(t, err)
	defer rows.Close()

	type scanRow struct {
		name       string
		pointCount int64
	}
	var got []scanRow
	for rows.Next() {
		var r scanRow
		require.NoError(t, rows.Scan(&r.name, &r.pointCount))
		got = append(got, r)
	}
	require.NoError(t, rows.Err())

	assert.Len(t, got, 3)
	names := make([]string, len(got))
	for i, r := range got {
		names[i] = r.name
	}
	assert.ElementsMatch(t, []string{"disk.read", "disk.write", "net.rx"}, names)
}

// ---------------------------------------------------------------------------
// TestMetricsTable_BestIndex_NameLookup
// ---------------------------------------------------------------------------

// TestMetricsTable_BestIndex_NameLookup verifies that an equality constraint
// on name produces a low-cost point-lookup index result.
func TestMetricsTable_BestIndex_NameLookup(t *testing.T) {
	table := &metricsTable{}

	csts := []sqlite3.InfoConstraint{
		{Column: colMName, Op: sqlite3.OpEQ, Usable: true},
	}
	result, err := table.BestIndex(csts, nil)
	require.NoError(t, err)

	assert.Equal(t, mIdxNameLookup, result.IdxNum)
	assert.InDelta(t, 1.0, result.EstimatedCost, 0.001)
	assert.True(t, result.Used[0], "name equality constraint should be consumed")
}

// ---------------------------------------------------------------------------
// TestMetricsTable_BestIndex_FullScan
// ---------------------------------------------------------------------------

// TestMetricsTable_BestIndex_FullScan verifies that no usable constraints
// produce the full scan plan with the highest cost.
func TestMetricsTable_BestIndex_FullScan(t *testing.T) {
	table := &metricsTable{}

	result, err := table.BestIndex(nil, nil)
	require.NoError(t, err)

	assert.Equal(t, mIdxFullScan, result.IdxNum)
	assert.GreaterOrEqual(t, result.EstimatedCost, float64(100))
}

// ---------------------------------------------------------------------------
// TestMetricsTable_ListError
// ---------------------------------------------------------------------------

// TestMetricsTable_ListError verifies that a store-level list error is
// propagated to the caller.
func TestMetricsTable_ListError(t *testing.T) {
	db, store := setupMetricsDB(t)
	store.listErr = errors.New("list failure")

	rows, err := db.Query("SELECT * FROM ts_metrics")
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

// TestMetricsTable_GetError verifies that a store-level get error is
// propagated when querying by name.
func TestMetricsTable_GetError(t *testing.T) {
	db, store := setupMetricsDB(t)
	store.getErr = errors.New("get failure")

	rows, err := db.Query("SELECT * FROM ts_metrics WHERE name = ?", "cpu")
	if err != nil {
		assert.Contains(t, err.Error(), "get failure")
		return
	}
	defer rows.Close()
	for rows.Next() {
	}
	require.Error(t, rows.Err())
	assert.Contains(t, rows.Err().Error(), "get failure")
}

// ---------------------------------------------------------------------------
// TestMetricsTable_NullableOptionalFields
// ---------------------------------------------------------------------------

// TestMetricsTable_NullableOptionalFields verifies that optional metric fields
// (description, unit, type) are returned as NULL when empty.
func TestMetricsTable_NullableOptionalFields(t *testing.T) {
	db, store := setupMetricsDB(t)

	store.mu.Lock()
	store.metrics = []MetricInfo{
		{Name: "raw.counter", PointCount: 5},
	}
	store.mu.Unlock()

	var name string
	var desc, unit, mtype sql.NullString
	var pc int64
	err := db.QueryRow("SELECT name, description, unit, type, point_count FROM ts_metrics WHERE name = ?", "raw.counter").
		Scan(&name, &desc, &unit, &mtype, &pc)
	require.NoError(t, err)

	assert.Equal(t, "raw.counter", name)
	assert.False(t, desc.Valid, "empty description should be NULL")
	assert.False(t, unit.Valid, "empty unit should be NULL")
	assert.False(t, mtype.Valid, "empty type should be NULL")
	assert.Equal(t, int64(5), pc)
}

// ---------------------------------------------------------------------------
// TestMetricsTable_ReadOnly
// ---------------------------------------------------------------------------

// TestMetricsTable_ReadOnly verifies that INSERT, UPDATE and DELETE operations
// on the metrics table return errors.
func TestMetricsTable_ReadOnly(t *testing.T) {
	db, store := setupMetricsDB(t)

	// INSERT must fail on read-only table.
	_, err := db.Exec("INSERT INTO ts_metrics (name) VALUES (?)", "new.metric")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-only")

	// Seed a row so UPDATE actually triggers xUpdate.
	store.mu.Lock()
	store.metrics = append(store.metrics, MetricInfo{Name: "cpu", PointCount: 42})
	store.mu.Unlock()

	_, err = db.Exec("UPDATE ts_metrics SET point_count = ? WHERE name = ?", int64(0), "cpu")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-only")
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

// TestMetricsListError verifies the MetricsListError message and Unwrap.
func TestMetricsListError(t *testing.T) {
	cause := errors.New("network timeout")
	e := &MetricsListError{Err: cause}

	assert.Contains(t, e.Error(), "network timeout")
	assert.True(t, errors.Is(e, cause))
}

// TestMetricsGetError verifies the MetricsGetError message and Unwrap.
func TestMetricsGetError(t *testing.T) {
	cause := errors.New("not found")
	e := &MetricsGetError{Name: "cpu.usage", Err: cause}

	assert.Contains(t, e.Error(), "cpu.usage")
	assert.Contains(t, e.Error(), "not found")
	assert.True(t, errors.Is(e, cause))
}

// TestMetricsTable_Select_FullScan_Empty verifies that scanning an empty
// metrics store returns zero rows without error.
func TestMetricsTable_Select_FullScan_Empty(t *testing.T) {
	db, _ := setupMetricsDB(t)

	rows, err := db.Query("SELECT * FROM ts_metrics")
	require.NoError(t, err)
	defer rows.Close()
	assert.False(t, rows.Next(), "expected no rows from empty store")
	require.NoError(t, rows.Err())
}
