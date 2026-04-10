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
	"fmt"
	"io"
	"sync/atomic"
)

// Compile-time interface assertion.
var _ driver.Rows = (*networkRows)(nil)

// networkRows wraps a JSON response from the REST API to implement the
// database/sql/driver.Rows interface.
type networkRows struct {
	columns []string
	rows    []map[string]interface{}
	pos     int
	closed  atomic.Bool
}

// newNetworkRows creates a networkRows from an SQL query response.
func newNetworkRows(resp *sqlResponse) *networkRows {
	return &networkRows{
		columns: resp.Columns,
		rows:    resp.Rows,
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
		v, ok := row[col]
		if !ok {
			dest[i] = nil
			continue
		}
		dest[i] = normalizeNetworkValue(v)
	}
	return nil
}

// normalizeNetworkValue converts JSON-decoded values to driver.Value types.
func normalizeNetworkValue(v interface{}) driver.Value {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case string:
		return val
	case float64:
		return val
	case bool:
		return val
	case []byte:
		return val
	default:
		return fmt.Sprintf("%v", val)
	}
}
