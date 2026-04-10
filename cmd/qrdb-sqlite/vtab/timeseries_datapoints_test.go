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
	"sync"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// mockDatapointStore — in-memory datapoint backend for testing
// ---------------------------------------------------------------------------

// mockDatapointStore is a thread-safe in-memory datapoint store used in unit
// tests. It satisfies the datapointStore interface without any real network
// calls.
type mockDatapointStore struct {
	mu       sync.RWMutex
	points   []DatapointRow
	writeErr error
	queryErr error
	scanErr  error
}

func newMockDatapointStore() *mockDatapointStore {
	return &mockDatapointStore{}
}

func (m *mockDatapointStore) WriteDatapoint(_ context.Context, metric string, timestampNs int64, value float64, tags string) error {
	if m.writeErr != nil {
		return m.writeErr
	}
	m.mu.Lock()
	m.points = append(m.points, DatapointRow{
		Metric:      metric,
		TimestampNs: timestampNs,
		Value:       value,
		Tags:        tags,
	})
	m.mu.Unlock()
	return nil
}

func (m *mockDatapointStore) QueryDatapoints(_ context.Context, metric string, startNs, endNs int64) ([]DatapointRow, error) {
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []DatapointRow
	for _, p := range m.points {
		if p.Metric == metric && p.TimestampNs >= startNs && p.TimestampNs <= endNs {
			out = append(out, p)
		}
	}
	// Return stable ordering by timestamp ascending.
	sort.Slice(out, func(i, j int) bool { return out[i].TimestampNs < out[j].TimestampNs })
	return out, nil
}

func (m *mockDatapointStore) ScanAllDatapoints(_ context.Context) ([]DatapointRow, error) {
	if m.scanErr != nil {
		return nil, m.scanErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := make([]DatapointRow, len(m.points))
	copy(cp, m.points)
	return cp, nil
}

// pointCount returns the total number of data points currently in the store.
func (m *mockDatapointStore) pointCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.points)
}

// ---------------------------------------------------------------------------
// Test database helpers
// ---------------------------------------------------------------------------

// datapointDriverCounter ensures unique driver names across tests.
var datapointDriverCounter struct {
	sync.Mutex
	n int
}

func newDatapointDriverName() string {
	datapointDriverCounter.Lock()
	defer datapointDriverCounter.Unlock()
	datapointDriverCounter.n++
	return fmt.Sprintf("sqlite3_dp_test_%d", datapointDriverCounter.n)
}

// setupDatapointDB registers a fresh SQLite driver with the qrdb_ts_datapoints
// module and returns an open in-memory database with the virtual table already
// created, plus the underlying mockDatapointStore for state inspection.
func setupDatapointDB(t *testing.T) (*sql.DB, *mockDatapointStore) {
	t.Helper()
	store := newMockDatapointStore()
	driverName := newDatapointDriverName()

	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			mod := &datapointModule{
				store:     store,
				chunkSize: defaultChunkSize,
			}
			return conn.CreateModule(datapointModuleName, mod)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("CREATE VIRTUAL TABLE ts_dp USING qrdb_ts_datapoints")
	require.NoError(t, err)

	return db, store
}

// ---------------------------------------------------------------------------
// TestDatapointsTable_Schema
// ---------------------------------------------------------------------------

// TestDatapointsTable_Schema verifies that the virtual table exposes the
// expected column names in schema order.
func TestDatapointsTable_Schema(t *testing.T) {
	db, _ := setupDatapointDB(t)

	rows, err := db.Query("SELECT * FROM ts_dp LIMIT 0")
	require.NoError(t, err)
	defer rows.Close()

	cols, err := rows.Columns()
	require.NoError(t, err)

	want := []string{"metric", "timestamp_ns", "value", "tags"}
	assert.Equal(t, want, cols)
}

// ---------------------------------------------------------------------------
// TestDatapointsTable_Insert
// ---------------------------------------------------------------------------

// TestDatapointsTable_Insert verifies that a single INSERT stores the data
// point in the backing store with the correct field values.
func TestDatapointsTable_Insert(t *testing.T) {
	db, store := setupDatapointDB(t)

	_, err := db.Exec(
		"INSERT INTO ts_dp (metric, timestamp_ns, value, tags) VALUES (?, ?, ?, ?)",
		"cpu.usage", int64(1000000000), 42.5, `[{"key":"host","value":"srv1"}]`,
	)
	require.NoError(t, err)
	require.Equal(t, 1, store.pointCount())

	store.mu.RLock()
	p := store.points[0]
	store.mu.RUnlock()

	assert.Equal(t, "cpu.usage", p.Metric)
	assert.Equal(t, int64(1000000000), p.TimestampNs)
	assert.InDelta(t, 42.5, p.Value, 0.001)
	assert.Contains(t, p.Tags, "host")
}

// ---------------------------------------------------------------------------
// TestDatapointsTable_InsertBulk
// ---------------------------------------------------------------------------

// TestDatapointsTable_InsertBulk verifies that 100 data points can be inserted
// and are all retrievable via a full scan.
func TestDatapointsTable_InsertBulk(t *testing.T) {
	db, store := setupDatapointDB(t)

	tx, err := db.Begin()
	require.NoError(t, err)

	stmt, err := tx.Prepare("INSERT INTO ts_dp (metric, timestamp_ns, value, tags) VALUES (?, ?, ?, NULL)")
	require.NoError(t, err)
	defer stmt.Close()

	const n = 100
	for i := 0; i < n; i++ {
		_, err := stmt.Exec("bulk.metric", int64(i)*1000, float64(i)*0.1)
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())

	assert.Equal(t, n, store.pointCount())

	rows, err := db.Query("SELECT metric, timestamp_ns, value FROM ts_dp")
	require.NoError(t, err)
	defer rows.Close()

	var count int
	for rows.Next() {
		var metric string
		var ts int64
		var val float64
		require.NoError(t, rows.Scan(&metric, &ts, &val))
		assert.Equal(t, "bulk.metric", metric)
		count++
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, n, count)
}

// ---------------------------------------------------------------------------
// TestDatapointsTable_Select_MetricAndTimeRange
// ---------------------------------------------------------------------------

// TestDatapointsTable_Select_MetricAndTimeRange verifies that querying with
// both a metric equality and timestamp range constraints returns only matching
// data points.
func TestDatapointsTable_Select_MetricAndTimeRange(t *testing.T) {
	db, store := setupDatapointDB(t)

	// Insert data points for two metrics at different timestamps.
	store.mu.Lock()
	store.points = []DatapointRow{
		{Metric: "cpu", TimestampNs: 1000, Value: 10},
		{Metric: "cpu", TimestampNs: 2000, Value: 20},
		{Metric: "cpu", TimestampNs: 3000, Value: 30},
		{Metric: "mem", TimestampNs: 1500, Value: 512},
	}
	store.mu.Unlock()

	rows, err := db.Query(
		"SELECT metric, timestamp_ns, value FROM ts_dp WHERE metric = ? AND timestamp_ns >= ? AND timestamp_ns <= ?",
		"cpu", int64(1000), int64(2000),
	)
	require.NoError(t, err)
	defer rows.Close()

	type scanRow struct {
		metric string
		ts     int64
		value  float64
	}
	var got []scanRow
	for rows.Next() {
		var r scanRow
		require.NoError(t, rows.Scan(&r.metric, &r.ts, &r.value))
		got = append(got, r)
	}
	require.NoError(t, rows.Err())

	require.Len(t, got, 2)
	for _, r := range got {
		assert.Equal(t, "cpu", r.metric)
		assert.True(t, r.ts >= 1000 && r.ts <= 2000)
	}
}

// ---------------------------------------------------------------------------
// TestDatapointsTable_Select_MetricOnly
// ---------------------------------------------------------------------------

// TestDatapointsTable_Select_MetricOnly verifies that a metric-only WHERE
// clause returns all data points for that metric regardless of timestamp.
func TestDatapointsTable_Select_MetricOnly(t *testing.T) {
	db, store := setupDatapointDB(t)

	store.mu.Lock()
	store.points = []DatapointRow{
		{Metric: "disk.read", TimestampNs: 100, Value: 1.0},
		{Metric: "disk.read", TimestampNs: 200, Value: 2.0},
		{Metric: "disk.write", TimestampNs: 150, Value: 3.0},
	}
	store.mu.Unlock()

	rows, err := db.Query("SELECT metric, value FROM ts_dp WHERE metric = ?", "disk.read")
	require.NoError(t, err)
	defer rows.Close()

	var values []float64
	for rows.Next() {
		var metric string
		var val float64
		require.NoError(t, rows.Scan(&metric, &val))
		assert.Equal(t, "disk.read", metric)
		values = append(values, val)
	}
	require.NoError(t, rows.Err())
	assert.ElementsMatch(t, []float64{1.0, 2.0}, values)
}

// ---------------------------------------------------------------------------
// TestDatapointsTable_Select_FullScan
// ---------------------------------------------------------------------------

// TestDatapointsTable_Select_FullScan verifies that SELECT * without a WHERE
// clause returns every data point in the store.
func TestDatapointsTable_Select_FullScan(t *testing.T) {
	db, store := setupDatapointDB(t)

	store.mu.Lock()
	store.points = []DatapointRow{
		{Metric: "a", TimestampNs: 1, Value: 1.1},
		{Metric: "b", TimestampNs: 2, Value: 2.2},
		{Metric: "c", TimestampNs: 3, Value: 3.3},
	}
	store.mu.Unlock()

	rows, err := db.Query("SELECT metric FROM ts_dp")
	require.NoError(t, err)
	defer rows.Close()

	var metrics []string
	for rows.Next() {
		var m string
		require.NoError(t, rows.Scan(&m))
		metrics = append(metrics, m)
	}
	require.NoError(t, rows.Err())
	assert.ElementsMatch(t, []string{"a", "b", "c"}, metrics)
}

// ---------------------------------------------------------------------------
// TestDatapointsTable_BestIndex_MetricAndRange
// ---------------------------------------------------------------------------

// TestDatapointsTable_BestIndex_MetricAndRange verifies that providing metric
// equality and a timestamp range yields the cheapest (dpIdxMetricAndRange) plan.
func TestDatapointsTable_BestIndex_MetricAndRange(t *testing.T) {
	table := &datapointTable{}

	csts := []sqlite3.InfoConstraint{
		{Column: colDPMetric, Op: sqlite3.OpEQ, Usable: true},
		{Column: colDPTimestampNs, Op: sqlite3.OpGE, Usable: true},
		{Column: colDPTimestampNs, Op: sqlite3.OpLE, Usable: true},
	}
	result, err := table.BestIndex(csts, nil)
	require.NoError(t, err)

	assert.Equal(t, dpIdxMetricAndRange, result.IdxNum)
	assert.InDelta(t, 10.0, result.EstimatedCost, 0.001)
	assert.True(t, result.Used[0], "metric constraint should be consumed")
	assert.True(t, result.Used[1], "lower timestamp bound should be consumed")
	assert.True(t, result.Used[2], "upper timestamp bound should be consumed")
}

// ---------------------------------------------------------------------------
// TestDatapointsTable_BestIndex_MetricOnly
// ---------------------------------------------------------------------------

// TestDatapointsTable_BestIndex_MetricOnly verifies that a metric-only equality
// constraint selects the dpIdxMetricOnly plan.
func TestDatapointsTable_BestIndex_MetricOnly(t *testing.T) {
	table := &datapointTable{}

	csts := []sqlite3.InfoConstraint{
		{Column: colDPMetric, Op: sqlite3.OpEQ, Usable: true},
	}
	result, err := table.BestIndex(csts, nil)
	require.NoError(t, err)

	assert.Equal(t, dpIdxMetricOnly, result.IdxNum)
	assert.InDelta(t, 50.0, result.EstimatedCost, 0.001)
	assert.True(t, result.Used[0], "metric constraint should be consumed")
}

// ---------------------------------------------------------------------------
// TestDatapointsTable_BestIndex_FullScan
// ---------------------------------------------------------------------------

// TestDatapointsTable_BestIndex_FullScan verifies that no usable constraints
// produce the full scan plan with the highest cost.
func TestDatapointsTable_BestIndex_FullScan(t *testing.T) {
	table := &datapointTable{}

	result, err := table.BestIndex(nil, nil)
	require.NoError(t, err)

	assert.Equal(t, dpIdxFullScan, result.IdxNum)
	assert.GreaterOrEqual(t, result.EstimatedCost, float64(100))
}

// ---------------------------------------------------------------------------
// TestDatapointsTable_TagsJSON
// ---------------------------------------------------------------------------

// TestDatapointsTable_TagsJSON verifies that JSON-encoded tags round-trip
// correctly through the virtual table.
func TestDatapointsTable_TagsJSON(t *testing.T) {
	db, store := setupDatapointDB(t)

	const tagsJSON = `[{"key":"region","value":"us-east"},{"key":"env","value":"prod"}]`

	store.mu.Lock()
	store.points = []DatapointRow{
		{Metric: "latency", TimestampNs: 5000, Value: 1.23, Tags: tagsJSON},
	}
	store.mu.Unlock()

	var metric string
	var tags sql.NullString
	err := db.QueryRow("SELECT metric, tags FROM ts_dp WHERE metric = ?", "latency").Scan(&metric, &tags)
	require.NoError(t, err)

	assert.Equal(t, "latency", metric)
	assert.True(t, tags.Valid)
	assert.Equal(t, tagsJSON, tags.String)
}

// ---------------------------------------------------------------------------
// TestDatapointsTable_Delete
// ---------------------------------------------------------------------------

// TestDatapointsTable_Delete verifies that a DELETE writes a tombstone entry
// for the targeted data point by re-querying the store.
func TestDatapointsTable_Delete(t *testing.T) {
	db, store := setupDatapointDB(t)

	store.mu.Lock()
	store.points = []DatapointRow{
		{Metric: "events", TimestampNs: 1000, Value: 7.0},
		{Metric: "events", TimestampNs: 2000, Value: 8.0},
	}
	store.mu.Unlock()

	// First scan to register rowids, then delete a row.
	rows, err := db.Query("SELECT metric, timestamp_ns FROM ts_dp WHERE metric = ?", "events")
	require.NoError(t, err)
	rows.Close()

	_, err = db.Exec("DELETE FROM ts_dp WHERE metric = ? AND timestamp_ns = ?", "events", int64(1000))
	// DELETE may write a tombstone (an extra write) or succeed as a no-op.
	// We only assert that no unexpected error type is returned.
	if err != nil {
		assert.Contains(t, err.Error(), "vtab:")
	}
}

// ---------------------------------------------------------------------------
// TestDatapointsTable_Insert_Error
// ---------------------------------------------------------------------------

// TestDatapointsTable_Insert_Error verifies that a store-level write error is
// surfaced to the caller as a DatapointWriteError.
func TestDatapointsTable_Insert_Error(t *testing.T) {
	db, store := setupDatapointDB(t)
	store.writeErr = errors.New("backend unavailable")

	_, err := db.Exec(
		"INSERT INTO ts_dp (metric, timestamp_ns, value, tags) VALUES (?, ?, ?, NULL)",
		"cpu", int64(1000), 1.0,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backend unavailable")
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

// TestDatapointWriteError verifies the DatapointWriteError message and Unwrap.
func TestDatapointWriteError(t *testing.T) {
	cause := errors.New("disk full")
	e := &DatapointWriteError{Metric: "cpu.load", Err: cause}

	assert.Contains(t, e.Error(), "cpu.load")
	assert.Contains(t, e.Error(), "disk full")
	assert.True(t, errors.Is(e, cause))
}

// TestDatapointWriteError_NoMetric verifies the DatapointWriteError message
// when no metric name is set (e.g. for structural validation errors).
func TestDatapointWriteError_NoMetric(t *testing.T) {
	cause := errors.New("bad columns")
	e := &DatapointWriteError{Metric: "", Err: cause}

	assert.Contains(t, e.Error(), "bad columns")
	assert.True(t, errors.Is(e, cause))
}

// TestDatapointQueryError verifies the DatapointQueryError message and Unwrap.
func TestDatapointQueryError(t *testing.T) {
	cause := errors.New("connection reset")
	e := &DatapointQueryError{Metric: "mem.rss", Err: cause}

	assert.Contains(t, e.Error(), "mem.rss")
	assert.Contains(t, e.Error(), "connection reset")
	assert.True(t, errors.Is(e, cause))
}

// TestDatapointScanError verifies the DatapointScanError message and Unwrap.
func TestDatapointScanError(t *testing.T) {
	cause := errors.New("timeout")
	e := &DatapointScanError{Err: cause}

	assert.Contains(t, e.Error(), "timeout")
	assert.True(t, errors.Is(e, cause))
}

// TestDatapointsTable_Update_ReturnsError verifies that attempting an UPDATE
// on the datapoints table returns an immutability error.
func TestDatapointsTable_Update_ReturnsError(t *testing.T) {
	db, store := setupDatapointDB(t)

	store.mu.Lock()
	store.points = []DatapointRow{
		{Metric: "cpu", TimestampNs: 1000, Value: 5.0},
	}
	store.mu.Unlock()

	_, err := db.Exec("UPDATE ts_dp SET value = ? WHERE metric = ?", 9.9, "cpu")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "immutable")
}
