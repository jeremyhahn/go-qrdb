//go:build qrdb_sql

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

package driver

import (
	"database/sql/driver"
	"io"
	"reflect"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ driver.Rows                           = (*networkRows)(nil)
	_ driver.RowsColumnTypeScanType         = (*networkRows)(nil)
	_ driver.RowsColumnTypeDatabaseTypeName = (*networkRows)(nil)
	_ driver.RowsColumnTypeNullable         = (*networkRows)(nil)
)

// networkRows adapts a remote SQL result set to database/sql/driver.Rows.
type networkRows struct {
	columns []string
	rows    []map[string]any
	pos     int
	closed  atomic.Bool
}

// newNetworkRows creates a networkRows from a transport SQL result.
func newNetworkRows(result *transport.SQLQueryResult) *networkRows {
	if result == nil {
		return &networkRows{}
	}
	return &networkRows{
		columns: result.Columns,
		rows:    result.Rows,
	}
}

// Columns returns the names of the columns in the result set.
func (r *networkRows) Columns() []string {
	return r.columns
}

// Close marks the rows as closed.
func (r *networkRows) Close() error {
	r.closed.Store(true)
	return nil
}

// Next populates dest with the values from the next row. It returns
// io.EOF when no more rows are available.
func (r *networkRows) Next(dest []driver.Value) error {
	if r.closed.Load() {
		return &RowsClosedError{}
	}
	if r.pos >= len(r.rows) {
		return io.EOF
	}

	row := r.rows[r.pos]
	r.pos++

	for i, col := range r.columns {
		if i >= len(dest) {
			break
		}
		v, ok := row[col]
		if !ok {
			dest[i] = nil
			continue
		}
		dest[i] = toDriverValue(v)
	}
	return nil
}

// ColumnTypeScanType reports the Go type a column scans into.
//
// The remote SQL API pre-formats every cell as text before serializing, so the
// only honest answer is string. Guessing a narrower type from a sampled value
// — which this driver previously did — makes the reported type depend on the
// data rather than the schema, so the same column can claim different types
// between two queries. Callers needing real column types should use an
// embedded connection or qrdb's MySQL wire protocol.
func (r *networkRows) ColumnTypeScanType(_ int) reflect.Type {
	return reflect.TypeOf("")
}

// ColumnTypeDatabaseTypeName reports the database type name for a column.
// It is TEXT for the same reason ColumnTypeScanType reports string.
func (r *networkRows) ColumnTypeDatabaseTypeName(_ int) string {
	return "TEXT"
}

// ColumnTypeNullable reports whether a column may be NULL. The remote API does
// not carry nullability, so ok is false — the contract's way of saying
// unknown, rather than asserting a value that may be wrong.
func (r *networkRows) ColumnTypeNullable(_ int) (nullable, ok bool) {
	return false, false
}

// toDriverValue converts a decoded wire value into a driver.Value.
//
// Values that are already driver.Value types pass through unchanged. Anything
// else is rejected as nil rather than being stringified: the wire format
// stringifies cells server-side, so an unexpected Go type here means the
// response did not match the contract, and inventing a rendering for it would
// hide that.
func toDriverValue(v any) driver.Value {
	switch val := v.(type) {
	case nil:
		return nil
	case string:
		return val
	case float64:
		return val
	case bool:
		return val
	case []byte:
		return val
	case int64:
		return val
	default:
		return nil
	}
}
