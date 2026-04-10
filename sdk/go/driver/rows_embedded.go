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
	"reflect"
	"sync/atomic"
	"time"

	gmssql "github.com/dolthub/go-mysql-server/sql"
)

// Compile-time interface assertions.
var (
	_ driver.Rows                           = (*embeddedRows)(nil)
	_ driver.RowsColumnTypeScanType         = (*embeddedRows)(nil)
	_ driver.RowsColumnTypeDatabaseTypeName = (*embeddedRows)(nil)
)

// embeddedRows wraps a GMS RowIter and schema to implement the
// database/sql/driver.Rows interface.
type embeddedRows struct {
	ctx    *gmssql.Context
	schema gmssql.Schema
	iter   gmssql.RowIter
	closed atomic.Bool
}

// Columns returns the names of the columns in the result set.
func (r *embeddedRows) Columns() []string {
	cols := make([]string, len(r.schema))
	for i, col := range r.schema {
		cols[i] = col.Name
	}
	return cols
}

// Close closes the row iterator.
func (r *embeddedRows) Close() error {
	if !r.closed.CompareAndSwap(false, true) {
		return nil
	}
	return r.iter.Close(r.ctx)
}

// Next populates dest with the values from the next row. It returns
// io.EOF when no more rows are available.
func (r *embeddedRows) Next(dest []driver.Value) error {
	if r.closed.Load() {
		return &RowsClosedError{}
	}

	row, err := r.iter.Next(r.ctx)
	if err == io.EOF {
		return io.EOF
	}
	if err != nil {
		return err
	}

	for i, v := range row {
		dest[i] = convertValue(v)
	}
	return nil
}

// ColumnTypeScanType returns the Go type suitable for scanning into for
// the column at the given index.
func (r *embeddedRows) ColumnTypeScanType(index int) reflect.Type {
	if index < 0 || index >= len(r.schema) {
		return reflect.TypeOf("")
	}

	col := r.schema[index]
	goType := col.Type.ValueType()
	if goType == nil {
		return reflect.TypeOf("")
	}
	return goType
}

// ColumnTypeDatabaseTypeName returns the database system type name for
// the column at the given index.
func (r *embeddedRows) ColumnTypeDatabaseTypeName(index int) string {
	if index < 0 || index >= len(r.schema) {
		return ""
	}
	return r.schema[index].Type.String()
}

// convertValue normalizes a GMS row value into a type that database/sql
// can handle.
func convertValue(v interface{}) driver.Value {
	if v == nil {
		return nil
	}

	switch val := v.(type) {
	case int8:
		return int64(val)
	case int16:
		return int64(val)
	case int32:
		return int64(val)
	case int:
		return int64(val)
	case int64:
		return val
	case uint8:
		return int64(val)
	case uint16:
		return int64(val)
	case uint32:
		return int64(val)
	case uint64:
		return int64(val)
	case float32:
		return float64(val)
	case float64:
		return val
	case bool:
		return val
	case string:
		return val
	case []byte:
		return val
	case time.Time:
		return val
	default:
		return fmt.Sprintf("%v", val)
	}
}
