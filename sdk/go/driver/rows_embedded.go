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
	"math"
	"reflect"
	"strconv"
	"sync/atomic"
	"time"

	gmssql "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/catalog"
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
		resolved, err := r.resolve(i, v)
		if err != nil {
			return err
		}
		dest[i] = convertValue(resolved)
	}
	return nil
}

// resolve returns the externally visible value of cell i, consulting the
// column's declared type through catalog.ResolveColumnValue rather than
// inferring meaning from the cell's Go type. This is what keeps ENUM and SET
// columns -- whose cells are an ordinal and a bitmask, not a label and a
// label list -- reading the same here as they do over the MySQL wire, where
// go-mysql-server performs the identical resolution itself.
//
// Cells with no corresponding schema entry are returned unchanged: the
// schema is the only authority on a cell's meaning, and inventing one for a
// column the engine did not describe would be the very guess this function
// exists to remove.
func (r *embeddedRows) resolve(i int, v any) (any, error) {
	if i >= len(r.schema) || r.schema[i] == nil {
		return v, nil
	}
	return catalog.ResolveColumnValue(r.ctx, r.schema[i].Type, v)
}

// ColumnTypeScanType returns the Go type suitable for scanning into for
// the column at the given index.
//
// It reports the type Next actually produces (catalog.ResolvedValueType),
// not the engine's storage type: an ENUM column stores a uint16 ordinal but
// yields its label, so uint16 is a scan type no row of this result set could
// ever satisfy.
func (r *embeddedRows) ColumnTypeScanType(index int) reflect.Type {
	if index < 0 || index >= len(r.schema) {
		return reflect.TypeOf("")
	}

	goType := catalog.ResolvedValueType(r.schema[index].Type)
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
//
// Every integer width widens to int64 with one exception. driver.Value admits
// no unsigned integer, so a BIGINT UNSIGNED above math.MaxInt64 -- the upper
// half of the range the column exists to hold -- has no int64 that represents
// it: 18446744073709551615 reinterpreted as an int64 is -1, which is not an
// approximation of the stored row but a different row, delivered without an
// error. Such a value is handed over as its decimal text instead, exactly as
// go-sql-driver/mysql does for the same case on the binary protocol (see its
// uint64ToString call in packets.go), so that database/sql's own conversion
// scans it into a uint64 with its exact value, into a string as the digits
// that were stored, and into an int64 as the range error it is -- the same
// three outcomes the MySQL wire produces for the same row.
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
	case uint:
		return convertUint64(uint64(val))
	case uint64:
		return convertUint64(val)
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

// convertUint64 renders an unsigned 64-bit value as the widest driver.Value
// that holds it exactly: an int64 while one exists, and the decimal text of
// the value once none does.
func convertUint64(val uint64) driver.Value {
	if val > math.MaxInt64 {
		return []byte(strconv.FormatUint(val, 10))
	}
	return int64(val)
}
