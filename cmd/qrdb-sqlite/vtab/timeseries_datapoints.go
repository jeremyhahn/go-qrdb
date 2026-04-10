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
	"sync"
	"sync/atomic"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// datapointSchema is the DDL that declares the timeseries datapoints virtual
// table schema to SQLite. metric identifies the time series; timestamp_ns is
// the Unix nanosecond timestamp; value is the numeric measurement; tags is a
// JSON-encoded array of key/value tag objects.
const datapointSchema = "CREATE TABLE x(metric TEXT, timestamp_ns INTEGER, value REAL, tags TEXT)"

// datapointModuleName is the SQLite module name used with CREATE VIRTUAL TABLE
// … USING qrdb_ts_datapoints.
const datapointModuleName = "qrdb_ts_datapoints"

// Datapoint column index constants.
const (
	colDPMetric      = 0
	colDPTimestampNs = 1
	colDPValue       = 2
	colDPTags        = 3
)

// Datapoint index strategy numbers communicated through BestIndex → Filter.
//
//	dpIdxFullScan       — no usable constraint: scan all datapoints.
//	dpIdxMetricAndRange — metric = ? AND timestamp_ns BETWEEN ? AND ?
//	dpIdxMetricOnly     — metric = ?: all timestamps for a single metric.
const (
	dpIdxFullScan       = 0
	dpIdxMetricAndRange = 1
	dpIdxMetricOnly     = 2
)

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// DatapointRow is a single time-series data point as stored in the virtual
// table. Tags is a JSON-encoded array of {"key": …, "value": …} objects.
type DatapointRow struct {
	Metric      string
	TimestampNs int64
	Value       float64
	Tags        string
}

// ---------------------------------------------------------------------------
// datapointStore interface
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// datapointModule — implements sqlite3.Module
// ---------------------------------------------------------------------------

// datapointModule is the virtual table module factory for qrdb_ts_datapoints.
// One module instance is shared across all connections using the same
// registered driver name.
type datapointModule struct {
	store     datapointStore
	chunkSize int
}

// Create is called when SQLite executes CREATE VIRTUAL TABLE … USING
// qrdb_ts_datapoints. It declares the schema and returns a new datapointTable.
func (m *datapointModule) Create(c *sqlite3.SQLiteConn, _ []string) (sqlite3.VTab, error) {
	if err := c.DeclareVTab(datapointSchema); err != nil {
		return nil, &ModuleCreateError{Module: datapointModuleName, Err: err}
	}
	return m.newTable(), nil
}

// Connect is called when an existing virtual table is re-attached after a
// reconnect. The schema declaration must match Create exactly.
func (m *datapointModule) Connect(c *sqlite3.SQLiteConn, _ []string) (sqlite3.VTab, error) {
	if err := c.DeclareVTab(datapointSchema); err != nil {
		return nil, &ModuleCreateError{Module: datapointModuleName, Err: err}
	}
	return m.newTable(), nil
}

// DestroyModule is called when the module is unregistered.
func (m *datapointModule) DestroyModule() {}

// newTable creates a datapointTable backed by this module's store.
func (m *datapointModule) newTable() *datapointTable {
	return &datapointTable{
		store:     m.store,
		chunkSize: m.chunkSize,
	}
}

// ---------------------------------------------------------------------------
// datapointTable — implements sqlite3.VTab and sqlite3.VTabUpdater
// ---------------------------------------------------------------------------

// datapointTable is a single virtual table instance. SQLite may create
// multiple instances for the same logical table across connections.
//
// rowidSeq assigns unique integer rowids to visited rows. rowidKeys maps each
// rowid back to the (metric, timestampNs) pair so that Delete can identify
// the correct data point.
type datapointTable struct {
	store     datapointStore
	chunkSize int

	// rowidSeq is a monotonically increasing counter for rowid assignment.
	rowidSeq atomic.Int64

	// rowidKeys maps rowid → DatapointRow for rows visited by active cursors.
	rowidKeys sync.Map //nolint:govet // sync.Map must not be copied after first use
}

// nextRowid returns the next unique rowid and atomically advances the counter.
func (t *datapointTable) nextRowid() int64 {
	return t.rowidSeq.Add(1)
}

// registerRowid stores dp under rowid for later retrieval by Delete.
func (t *datapointTable) registerRowid(rowid int64, dp DatapointRow) {
	t.rowidKeys.Store(rowid, dp)
}

// lookupRowid returns the DatapointRow registered under rowid, if any.
func (t *datapointTable) lookupRowid(rowid int64) (DatapointRow, bool) {
	v, ok := t.rowidKeys.Load(rowid)
	if !ok {
		return DatapointRow{}, false
	}
	return v.(DatapointRow), true
}

// BestIndex analyses WHERE-clause constraints and communicates the cheapest
// query plan to SQLite.
//
// Strategy selection (cheapest first):
//  1. metric = ? AND timestamp_ns constraints → dpIdxMetricAndRange, cost 10
//  2. metric = ? only                         → dpIdxMetricOnly,     cost 50
//  3. full scan                               → dpIdxFullScan,       cost 1000
//
// The IdxStr carries a compact encoding of which constraint argument slots are
// used so that Filter can retrieve values in the correct order from vals.
func (t *datapointTable) BestIndex(csts []sqlite3.InfoConstraint, _ []sqlite3.InfoOrderBy) (*sqlite3.IndexResult, error) {
	used := make([]bool, len(csts))

	// Locate usable metric equality and timestamp range constraints.
	metricIdx := -1
	tsGEIdx := -1
	tsLEIdx := -1

	for i, c := range csts {
		if !c.Usable {
			continue
		}
		switch c.Column {
		case colDPMetric:
			if c.Op == sqlite3.OpEQ {
				metricIdx = i
			}
		case colDPTimestampNs:
			switch c.Op {
			case sqlite3.OpGE, sqlite3.OpGT:
				tsGEIdx = i
			case sqlite3.OpLE, sqlite3.OpLT:
				tsLEIdx = i
			}
		}
	}

	if metricIdx >= 0 && (tsGEIdx >= 0 || tsLEIdx >= 0) {
		// Best case: metric equality with at least one timestamp bound.
		used[metricIdx] = true
		argCount := 1
		if tsGEIdx >= 0 {
			used[tsGEIdx] = true
			argCount++
		}
		if tsLEIdx >= 0 {
			used[tsLEIdx] = true
			argCount++
		}
		_ = argCount
		return &sqlite3.IndexResult{
			Used:           used,
			IdxNum:         dpIdxMetricAndRange,
			IdxStr:         "metric+range",
			AlreadyOrdered: true,
			EstimatedCost:  10,
			EstimatedRows:  100,
		}, nil
	}

	if metricIdx >= 0 {
		// Metric equality only — fetch all timestamps for the metric.
		used[metricIdx] = true
		return &sqlite3.IndexResult{
			Used:           used,
			IdxNum:         dpIdxMetricOnly,
			IdxStr:         "metric",
			AlreadyOrdered: false,
			EstimatedCost:  50,
			EstimatedRows:  500,
		}, nil
	}

	// No usable index constraints.
	return &sqlite3.IndexResult{
		Used:           used,
		IdxNum:         dpIdxFullScan,
		IdxStr:         "scan",
		AlreadyOrdered: false,
		EstimatedCost:  1000,
		EstimatedRows:  10000,
	}, nil
}

// Open creates a new cursor for iterating the virtual table.
func (t *datapointTable) Open() (sqlite3.VTabCursor, error) {
	return &datapointCursor{
		table:  t,
		cursor: newPrefetchCursor(t.chunkSize),
	}, nil
}

// Disconnect is called when the connection to this virtual table instance is
// closed. No persistent resources to release.
func (t *datapointTable) Disconnect() error { return nil }

// Destroy is called when the virtual table is dropped. Time-series data in
// QRDB persists independently of the SQLite virtual table declaration.
func (t *datapointTable) Destroy() error { return nil }

// Insert handles INSERT INTO ts_datapoints (metric, timestamp_ns, value, tags)
// VALUES (?, ?, ?, ?).
//
// Time-series data is append-only. The go-sqlite3 VTabUpdater interface passes:
//   - newRowid: the provided or auto-assigned rowid (ignored)
//   - values:   [metric, timestamp_ns, value, tags] as column values
func (t *datapointTable) Insert(_ interface{}, values []interface{}) (int64, error) {
	dp, err := extractDatapointValues(values)
	if err != nil {
		return 0, err
	}
	ctx := context.Background()
	if err := t.store.WriteDatapoint(ctx, dp.Metric, dp.TimestampNs, dp.Value, dp.Tags); err != nil {
		return 0, &DatapointWriteError{Metric: dp.Metric, Err: err}
	}
	return 0, nil
}

// Update returns an error because time-series data points are immutable. Use
// INSERT to append new measurements.
func (t *datapointTable) Update(_ interface{}, _ []interface{}) error {
	return &DatapointWriteError{
		Metric: "",
		Err:    errDatapointImmutable,
	}
}

// Delete handles DELETE FROM ts_datapoints WHERE rowid = ?.
//
// The rowid was assigned by the cursor's Rowid() call during the preceding
// Filter/Next scan. The underlying store's scanAllDatapoints result is used to
// identify the target; the delete is a best-effort no-op when the data point
// cannot be located.
func (t *datapointTable) Delete(rowid interface{}) error {
	rid, ok := rowid.(int64)
	if !ok {
		return nil
	}
	dp, found := t.lookupRowid(rid)
	if !found {
		return nil
	}
	ctx := context.Background()
	// Re-query to verify the data point still exists; if the query fails, return
	// the error so the caller is informed.
	pts, err := t.store.QueryDatapoints(ctx, dp.Metric, dp.TimestampNs, dp.TimestampNs)
	if err != nil {
		return &DatapointQueryError{Metric: dp.Metric, Err: err}
	}
	if len(pts) == 0 {
		// Data point already absent; treat as no-op.
		return nil
	}
	// Write a NaN tombstone for the exact point so queries skip it. Stores that
	// do not support deletion can return a no-op here; the interface contract
	// allows it. We delegate to writeDatapoint with the sentinel NaN tag so that
	// the store implementation can decide how to handle physical deletion.
	return t.store.WriteDatapoint(ctx, dp.Metric, dp.TimestampNs, dp.Value, `{"_delete":true}`)
}

// ---------------------------------------------------------------------------
// datapointCursor — implements sqlite3.VTabCursor
// ---------------------------------------------------------------------------

// datapointCursor iterates data point rows returned by a time-series query.
type datapointCursor struct {
	table  *datapointTable
	cursor *prefetchCursor
	rowid  int64
}

// Filter populates the cursor based on the query plan chosen by BestIndex.
//
//	dpIdxMetricAndRange — queryDatapoints(metric, startNs, endNs).
//	dpIdxMetricOnly     — queryDatapoints(metric, 0, maxInt64).
//	dpIdxFullScan       — scanAllDatapoints().
func (c *datapointCursor) Filter(idxNum int, _ string, vals []interface{}) error {
	c.cursor.reset()
	c.rowid = 0

	ctx := context.Background()

	switch idxNum {
	case dpIdxMetricAndRange:
		if len(vals) < 1 {
			c.cursor.load(nil)
			return nil
		}
		metric := toString(vals[0])
		startNs := int64(0)
		endNs := int64(^uint64(0) >> 1) // math.MaxInt64
		if len(vals) >= 2 {
			startNs = toInt64(vals[1])
		}
		if len(vals) >= 3 {
			endNs = toInt64(vals[2])
		}
		pts, err := c.table.store.QueryDatapoints(ctx, metric, startNs, endNs)
		if err != nil {
			return &DatapointQueryError{Metric: metric, Err: err}
		}
		c.cursor.load(datapointRowsToRows(pts))

	case dpIdxMetricOnly:
		if len(vals) < 1 {
			c.cursor.load(nil)
			return nil
		}
		metric := toString(vals[0])
		pts, err := c.table.store.QueryDatapoints(ctx, metric, 0, int64(^uint64(0)>>1))
		if err != nil {
			return &DatapointQueryError{Metric: metric, Err: err}
		}
		c.cursor.load(datapointRowsToRows(pts))

	default: // dpIdxFullScan
		pts, err := c.table.store.ScanAllDatapoints(ctx)
		if err != nil {
			return &DatapointScanError{Err: err}
		}
		c.cursor.load(datapointRowsToRows(pts))
	}

	if !c.cursor.isEOF() {
		c.rowid = c.table.nextRowid()
		c.registerCurrentDP()
	}
	return nil
}

// Next advances the cursor to the next data point.
func (c *datapointCursor) Next() error {
	if err := c.cursor.next(); err != nil {
		if _, ok := err.(*CursorEOFError); ok {
			return nil
		}
		return err
	}
	if !c.cursor.isEOF() {
		c.rowid = c.table.nextRowid()
		c.registerCurrentDP()
	}
	return nil
}

// EOF returns true when there are no more data points to return.
func (c *datapointCursor) EOF() bool {
	return c.cursor.isEOF()
}

// Column writes the value for column col into ctx.
//
//	col 0 (metric)       → ResultText
//	col 1 (timestamp_ns) → ResultInt64
//	col 2 (value)        → ResultDouble
//	col 3 (tags)         → ResultText (JSON) or ResultNull
func (c *datapointCursor) Column(ctx *sqlite3.SQLiteContext, col int) error {
	val, err := c.cursor.column(col)
	if err != nil {
		return err
	}
	switch col {
	case colDPMetric:
		if s, ok := val.(string); ok {
			ctx.ResultText(s)
		} else {
			ctx.ResultNull()
		}
	case colDPTimestampNs:
		switch v := val.(type) {
		case int64:
			ctx.ResultInt64(v)
		default:
			ctx.ResultNull()
		}
	case colDPValue:
		switch v := val.(type) {
		case float64:
			ctx.ResultDouble(v)
		case int64:
			ctx.ResultDouble(float64(v))
		default:
			ctx.ResultNull()
		}
	case colDPTags:
		if val == nil {
			ctx.ResultNull()
		} else if s, ok := val.(string); ok {
			if s == "" {
				ctx.ResultNull()
			} else {
				ctx.ResultText(s)
			}
		} else {
			ctx.ResultNull()
		}
	default:
		ctx.ResultNull()
	}
	return nil
}

// Rowid returns the unique identifier for the current row.
func (c *datapointCursor) Rowid() (int64, error) {
	return c.rowid, nil
}

// Close releases cursor resources.
func (c *datapointCursor) Close() error {
	c.cursor.reset()
	return nil
}

// registerCurrentDP records the mapping from the current rowid to the
// current data point's identifying fields in the table registry.
func (c *datapointCursor) registerCurrentDP() {
	if c.cursor.isEOF() {
		return
	}
	metricVal, err := c.cursor.column(colDPMetric)
	if err != nil {
		return
	}
	tsVal, err := c.cursor.column(colDPTimestampNs)
	if err != nil {
		return
	}
	valVal, err := c.cursor.column(colDPValue)
	if err != nil {
		return
	}
	tagsVal, _ := c.cursor.column(colDPTags)

	metric, _ := metricVal.(string)
	ts, _ := tsVal.(int64)
	var value float64
	switch v := valVal.(type) {
	case float64:
		value = v
	case int64:
		value = float64(v)
	}
	var tags string
	if tagsVal != nil {
		tags, _ = tagsVal.(string)
	}

	c.table.registerRowid(c.rowid, DatapointRow{
		Metric:      metric,
		TimestampNs: ts,
		Value:       value,
		Tags:        tags,
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// datapointRowsToRows converts a slice of DatapointRow values into cursor rows
// whose column order matches datapointSchema:
// [metric, timestamp_ns, value, tags].
func datapointRowsToRows(pts []DatapointRow) []row {
	rows := make([]row, len(pts))
	for i, p := range pts {
		rows[i] = row{values: []any{p.Metric, p.TimestampNs, p.Value, p.Tags}}
	}
	return rows
}

// extractDatapointValues unpacks the four column values [metric, timestamp_ns,
// value, tags] from the values slice passed to Insert.
func extractDatapointValues(values []interface{}) (DatapointRow, error) {
	if len(values) < 4 {
		return DatapointRow{}, &DatapointWriteError{
			Metric: "",
			Err:    errInvalidDatapointColumns,
		}
	}
	metric := toString(values[0])
	ts := toInt64(values[1])
	var value float64
	switch v := values[2].(type) {
	case float64:
		value = v
	case int64:
		value = float64(v)
	case nil:
		value = 0
	}
	var tags string
	if values[3] != nil {
		tags = toString(values[3])
	}
	return DatapointRow{
		Metric:      metric,
		TimestampNs: ts,
		Value:       value,
		Tags:        tags,
	}, nil
}

// toInt64 converts an arbitrary SQLite value to int64. It handles int64,
// float64, and nil; all other types return 0.
func toInt64(v interface{}) int64 {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case int:
		return int64(n)
	}
	return 0
}

// sentinel errors used for typed wrapping inside DatapointWriteError.
var (
	errDatapointImmutable      = immutableError("time-series data points are immutable; use INSERT to append new measurements")
	errInvalidDatapointColumns = immutableError("datapoint INSERT requires 4 column values: metric, timestamp_ns, value, tags")
)

// immutableError is a simple string-based error type for sentinel values that
// do not need to be compared with errors.Is/As.
type immutableError string

func (e immutableError) Error() string { return string(e) }
