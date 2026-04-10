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

	sqlite3 "github.com/mattn/go-sqlite3"
)

// metricsSchema is the DDL that declares the timeseries metrics virtual table
// schema to SQLite. name is the primary key; the remaining columns describe
// the metric's characteristics and are derived from the QRDB time-series index.
const metricsSchema = "CREATE TABLE x(name TEXT PRIMARY KEY, description TEXT, unit TEXT, type TEXT, point_count INTEGER)"

// metricsModuleName is the SQLite module name used with CREATE VIRTUAL TABLE …
// USING qrdb_ts_metrics.
const metricsModuleName = "qrdb_ts_metrics"

// Metrics column index constants.
const (
	colMName        = 0
	colMDescription = 1
	colMUnit        = 2
	colMType        = 3
	colMPointCount  = 4
)

// Metrics index strategy numbers communicated through BestIndex → Filter.
//
//	mIdxFullScan    — no usable constraint: list all metrics.
//	mIdxNameLookup  — name = ?: point lookup by metric name.
const (
	mIdxFullScan   = 0
	mIdxNameLookup = 1
)

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// MetricInfo describes a single time-series metric as returned by the metrics
// virtual table. It mirrors the TSMetricInfo transport type.
type MetricInfo struct {
	Name        string
	Description string
	Unit        string
	MetricType  string
	PointCount  int64
}

// ---------------------------------------------------------------------------
// metricsStore interface
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// metricsModule — implements sqlite3.Module
// ---------------------------------------------------------------------------

// metricsModule is the virtual table module factory for qrdb_ts_metrics.
// One module instance is shared across all connections using the same
// registered driver name.
type metricsModule struct {
	store     metricsStore
	chunkSize int
}

// Create is called when SQLite executes CREATE VIRTUAL TABLE … USING
// qrdb_ts_metrics. It declares the schema and returns a new metricsTable.
func (m *metricsModule) Create(c *sqlite3.SQLiteConn, _ []string) (sqlite3.VTab, error) {
	if err := c.DeclareVTab(metricsSchema); err != nil {
		return nil, &ModuleCreateError{Module: metricsModuleName, Err: err}
	}
	return m.newTable(), nil
}

// Connect is called when an existing virtual table is re-attached after a
// reconnect. The schema declaration must match Create exactly.
func (m *metricsModule) Connect(c *sqlite3.SQLiteConn, _ []string) (sqlite3.VTab, error) {
	if err := c.DeclareVTab(metricsSchema); err != nil {
		return nil, &ModuleCreateError{Module: metricsModuleName, Err: err}
	}
	return m.newTable(), nil
}

// DestroyModule is called when the module is unregistered.
func (m *metricsModule) DestroyModule() {}

// newTable creates a metricsTable backed by this module's store.
func (m *metricsModule) newTable() *metricsTable {
	return &metricsTable{
		store:     m.store,
		chunkSize: m.chunkSize,
	}
}

// ---------------------------------------------------------------------------
// metricsTable — implements sqlite3.VTab
// ---------------------------------------------------------------------------

// metricsTable is a read-only virtual table that enumerates available
// time-series metrics. Mutation attempts return errors because metrics are
// derived from datapoint writes and cannot be created or deleted directly.
type metricsTable struct {
	store     metricsStore
	chunkSize int
}

// BestIndex analyses WHERE-clause constraints and communicates the cheapest
// query plan to SQLite.
//
// Strategy selection:
//  1. name = ? → mIdxNameLookup, cost 1  (point lookup)
//  2. full scan → mIdxFullScan,  cost 1000
func (t *metricsTable) BestIndex(csts []sqlite3.InfoConstraint, _ []sqlite3.InfoOrderBy) (*sqlite3.IndexResult, error) {
	used := make([]bool, len(csts))

	for i, c := range csts {
		if !c.Usable || c.Column != colMName || c.Op != sqlite3.OpEQ {
			continue
		}
		used[i] = true
		return &sqlite3.IndexResult{
			Used:           used,
			IdxNum:         mIdxNameLookup,
			IdxStr:         "eq",
			AlreadyOrdered: true,
			EstimatedCost:  1,
			EstimatedRows:  1,
		}, nil
	}

	return &sqlite3.IndexResult{
		Used:           used,
		IdxNum:         mIdxFullScan,
		IdxStr:         "scan",
		AlreadyOrdered: false,
		EstimatedCost:  1000,
		EstimatedRows:  1000,
	}, nil
}

// Open creates a new cursor for iterating the virtual table.
func (t *metricsTable) Open() (sqlite3.VTabCursor, error) {
	return &metricsCursor{
		table:  t,
		cursor: newPrefetchCursor(t.chunkSize),
	}, nil
}

// Disconnect is called when the connection to this virtual table instance is
// closed. No persistent resources to release.
func (t *metricsTable) Disconnect() error { return nil }

// Destroy is called when the virtual table is dropped. Metric data in QRDB
// persists independently of the SQLite virtual table declaration.
func (t *metricsTable) Destroy() error { return nil }

// Insert is not supported. Metrics are derived from datapoint writes.
func (t *metricsTable) Insert(_ interface{}, _ []interface{}) (int64, error) {
	return 0, &MetricsListError{Err: errMetricsReadOnly}
}

// Update is not supported. Metrics are read-only.
func (t *metricsTable) Update(_ interface{}, _ []interface{}) error {
	return &MetricsListError{Err: errMetricsReadOnly}
}

// Delete is not supported. Metrics are read-only.
func (t *metricsTable) Delete(_ interface{}) error {
	return &MetricsListError{Err: errMetricsReadOnly}
}

// ---------------------------------------------------------------------------
// metricsCursor — implements sqlite3.VTabCursor
// ---------------------------------------------------------------------------

// metricsCursor iterates metric info rows returned by a metrics query.
type metricsCursor struct {
	table  *metricsTable
	cursor *prefetchCursor
	rowid  int64
}

// Filter populates the cursor based on the query plan chosen by BestIndex.
//
//	mIdxNameLookup — getMetric(name).
//	mIdxFullScan   — listMetrics().
func (c *metricsCursor) Filter(idxNum int, _ string, vals []interface{}) error {
	c.cursor.reset()
	c.rowid = 0

	ctx := context.Background()

	switch idxNum {
	case mIdxNameLookup:
		if len(vals) < 1 {
			c.cursor.load(nil)
			return nil
		}
		name := toString(vals[0])
		m, err := c.table.store.GetMetric(ctx, name)
		if err != nil {
			return &MetricsGetError{Name: name, Err: err}
		}
		if m == nil {
			c.cursor.load(nil)
			return nil
		}
		c.cursor.load([]row{metricInfoToRow(*m)})

	default: // mIdxFullScan
		metrics, err := c.table.store.ListMetrics(ctx)
		if err != nil {
			return &MetricsListError{Err: err}
		}
		rows := make([]row, len(metrics))
		for i, m := range metrics {
			rows[i] = metricInfoToRow(m)
		}
		c.cursor.load(rows)
	}

	if !c.cursor.isEOF() {
		c.rowid++
	}
	return nil
}

// Next advances the cursor to the next metric.
func (c *metricsCursor) Next() error {
	if err := c.cursor.next(); err != nil {
		if _, ok := err.(*CursorEOFError); ok {
			return nil
		}
		return err
	}
	if !c.cursor.isEOF() {
		c.rowid++
	}
	return nil
}

// EOF returns true when there are no more metrics to return.
func (c *metricsCursor) EOF() bool {
	return c.cursor.isEOF()
}

// Column writes the value for column col into ctx.
//
//	col 0 (name)        → ResultText
//	col 1 (description) → ResultText or ResultNull
//	col 2 (unit)        → ResultText or ResultNull
//	col 3 (type)        → ResultText or ResultNull
//	col 4 (point_count) → ResultInt64
func (c *metricsCursor) Column(ctx *sqlite3.SQLiteContext, col int) error {
	val, err := c.cursor.column(col)
	if err != nil {
		return err
	}
	switch col {
	case colMName, colMDescription, colMUnit, colMType:
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
	case colMPointCount:
		switch v := val.(type) {
		case int64:
			ctx.ResultInt64(v)
		default:
			ctx.ResultInt64(0)
		}
	default:
		ctx.ResultNull()
	}
	return nil
}

// Rowid returns the unique identifier for the current row.
func (c *metricsCursor) Rowid() (int64, error) {
	return c.rowid, nil
}

// Close releases cursor resources.
func (c *metricsCursor) Close() error {
	c.cursor.reset()
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// metricInfoToRow converts a MetricInfo into a cursor row whose column order
// matches metricsSchema: [name, description, unit, type, point_count].
func metricInfoToRow(m MetricInfo) row {
	return row{values: []any{m.Name, m.Description, m.Unit, m.MetricType, m.PointCount}}
}

// sentinel error for read-only mutations.
var errMetricsReadOnly = immutableError("qrdb_ts_metrics is read-only; metrics are derived from datapoint writes")
