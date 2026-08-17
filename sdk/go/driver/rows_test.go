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
	"bytes"
	"context"
	"database/sql/driver"
	sqld "database/sql/driver"
	"errors"
	"io"
	"math"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestNetworkRows_Columns verifies Columns returns the correct column names.
func TestNetworkRows_Columns(t *testing.T) {
	resp := &transport.SQLQueryResult{
		Columns: []string{"id", "name", "email"},
		Rows:    nil,
	}
	rows := newNetworkRows(resp)
	cols := rows.Columns()
	if len(cols) != 3 {
		t.Fatalf("Columns() len = %d, want 3", len(cols))
	}
	if cols[0] != "id" || cols[1] != "name" || cols[2] != "email" {
		t.Errorf("Columns() = %v, want [id name email]", cols)
	}
}

// TestNetworkRows_ColumnsEmpty verifies Columns returns empty slice when no columns.
func TestNetworkRows_ColumnsEmpty(t *testing.T) {
	resp := &transport.SQLQueryResult{}
	rows := newNetworkRows(resp)
	cols := rows.Columns()
	if len(cols) != 0 {
		t.Errorf("Columns() = %v, want empty", cols)
	}
}

// TestNetworkRows_Next_Iteration verifies Next iterates through all rows.
func TestNetworkRows_Next_Iteration(t *testing.T) {
	resp := &transport.SQLQueryResult{
		Columns: []string{"id", "name"},
		Rows: []map[string]interface{}{
			{"id": float64(1), "name": "alice"},
			{"id": float64(2), "name": "bob"},
		},
	}
	rows := newNetworkRows(resp)
	dest := make([]driver.Value, 2)

	if err := rows.Next(dest); err != nil {
		t.Fatalf("Next (row 1): %v", err)
	}
	if dest[0] != float64(1) || dest[1] != "alice" {
		t.Errorf("row 1 = %v, want [1 alice]", dest)
	}

	if err := rows.Next(dest); err != nil {
		t.Fatalf("Next (row 2): %v", err)
	}
	if dest[0] != float64(2) || dest[1] != "bob" {
		t.Errorf("row 2 = %v, want [2 bob]", dest)
	}

	// Next after last row returns io.EOF.
	if err := rows.Next(dest); err != io.EOF {
		t.Errorf("Next at end = %v, want io.EOF", err)
	}
}

// TestNetworkRows_EOF verifies io.EOF is returned when no rows.
func TestNetworkRows_EOF(t *testing.T) {
	resp := &transport.SQLQueryResult{
		Columns: []string{"id"},
		Rows:    []map[string]interface{}{},
	}
	rows := newNetworkRows(resp)
	dest := make([]driver.Value, 1)

	err := rows.Next(dest)
	if err != io.EOF {
		t.Errorf("Next on empty rows = %v, want io.EOF", err)
	}
}

// TestNetworkRows_Close verifies Close marks rows as closed and Next returns error.
func TestNetworkRows_Close(t *testing.T) {
	resp := &transport.SQLQueryResult{
		Columns: []string{"id"},
		Rows:    []map[string]interface{}{{"id": float64(1)}},
	}
	rows := newNetworkRows(resp)

	if err := rows.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	dest := make([]driver.Value, 1)
	err := rows.Next(dest)
	if err == nil {
		t.Fatal("expected error after Close")
	}
	var rce *RowsClosedError
	if !errors.As(err, &rce) {
		t.Errorf("expected *RowsClosedError, got %T: %v", err, err)
	}
}

// TestNetworkRows_MissingColumnValue verifies nil is used for missing columns.
func TestNetworkRows_MissingColumnValue(t *testing.T) {
	resp := &transport.SQLQueryResult{
		Columns: []string{"a", "b"},
		Rows:    []map[string]interface{}{{"a": "present"}}, // "b" is missing
	}
	rows := newNetworkRows(resp)
	dest := make([]driver.Value, 2)

	if err := rows.Next(dest); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if dest[0] != "present" {
		t.Errorf("dest[0] = %v, want present", dest[0])
	}
	if dest[1] != nil {
		t.Errorf("dest[1] = %v, want nil for missing column", dest[1])
	}
}

// TestConvertValue_AllTypes verifies convertValue handles each supported type.
func TestConvertValue_AllTypes(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		input interface{}
		want  driver.Value
	}{
		{name: "nil", input: nil, want: nil},
		{name: "int8", input: int8(8), want: int64(8)},
		{name: "int16", input: int16(16), want: int64(16)},
		{name: "int32", input: int32(32), want: int64(32)},
		{name: "int", input: int(100), want: int64(100)},
		{name: "int64", input: int64(64), want: int64(64)},
		{name: "uint8", input: uint8(8), want: int64(8)},
		{name: "uint16", input: uint16(16), want: int64(16)},
		{name: "uint32", input: uint32(32), want: int64(32)},
		{name: "uint64", input: uint64(64), want: int64(64)},
		{name: "float32", input: float32(1.5), want: float64(float32(1.5))},
		{name: "float64", input: float64(3.14), want: float64(3.14)},
		{name: "bool true", input: true, want: true},
		{name: "bool false", input: false, want: false},
		{name: "string", input: "hello", want: "hello"},
		{name: "bytes", input: []byte("data"), want: []byte("data")},
		{name: "time", input: now, want: now},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertValue(tt.input)
			// Use string comparison for float32 due to precision.
			if tt.name == "float32" {
				gotF, ok1 := got.(float64)
				wantF, ok2 := tt.want.(float64)
				if !ok1 || !ok2 {
					t.Errorf("convertValue(%v) type mismatch", tt.input)
					return
				}
				// Allow small float precision difference.
				diff := gotF - wantF
				if diff > 0.001 || diff < -0.001 {
					t.Errorf("convertValue(%v) = %v, want ~%v", tt.input, got, tt.want)
				}
				return
			}
			// []byte is not comparable with ==; use bytes.Equal.
			if tt.name == "bytes" {
				gotB, ok1 := got.([]byte)
				wantB, ok2 := tt.want.([]byte)
				if !ok1 || !ok2 {
					t.Errorf("convertValue bytes: wrong type got=%T want=%T", got, tt.want)
					return
				}
				if !bytes.Equal(gotB, wantB) {
					t.Errorf("convertValue(%v) = %v, want %v", tt.input, gotB, wantB)
				}
				return
			}
			if got != tt.want {
				t.Errorf("convertValue(%v) = %v (%T), want %v (%T)", tt.input, got, got, tt.want, tt.want)
			}
		})
	}
}

// TestConvertValue_DefaultFallback verifies non-standard types are stringified.
func TestConvertValue_DefaultFallback(t *testing.T) {
	type custom struct{ x int }
	v := convertValue(custom{x: 7})
	if v == nil {
		t.Fatal("expected non-nil for custom type")
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("expected string, got %T", v)
	}
	if s == "" {
		t.Error("expected non-empty string representation")
	}
}

// TestEmbeddedRows_ColumnTypeScanType verifies ColumnTypeScanType via sql.DB.
func TestEmbeddedRows_ColumnTypeScanType(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=coltypetest")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	qc, ok := conn.(interface {
		QueryContext(ctx interface {
			Deadline() (interface{}, bool)
			Done() <-chan struct{}
			Err() error
			Value(interface{}) interface{}
		}, query string, args []interface{}) (interface{}, error)
	})
	_ = qc
	_ = ok

	// Access ColumnTypeScanType via the embeddedRows directly by querying through
	// the driver connection using the context-based query interface.
	ec, ok2 := conn.(sqld.QueryerContext)
	if !ok2 {
		t.Skip("conn does not implement QueryerContext")
	}

	rows, err := ec.QueryContext(context.Background(), "SELECT `key`, `value` FROM kv", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	defer func() { _ = rows.Close() }()

	er, ok := rows.(*embeddedRows)
	if !ok {
		t.Fatalf("expected *embeddedRows, got %T", rows)
	}

	// ColumnTypeScanType at valid index.
	scanType := er.ColumnTypeScanType(0)
	if scanType == nil {
		t.Error("ColumnTypeScanType(0) returned nil")
	}

	// ColumnTypeScanType at invalid index.
	outOfBounds := er.ColumnTypeScanType(100)
	if outOfBounds == nil {
		t.Error("ColumnTypeScanType(100) should return non-nil fallback type")
	}

	// ColumnTypeScanType at negative index.
	negative := er.ColumnTypeScanType(-1)
	if negative == nil {
		t.Error("ColumnTypeScanType(-1) should return non-nil fallback type")
	}
}

// TestEmbeddedRows_ColumnTypeDatabaseTypeName verifies ColumnTypeDatabaseTypeName.
func TestEmbeddedRows_ColumnTypeDatabaseTypeName(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=dbtypetest")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	ec, ok := conn.(sqld.QueryerContext)
	if !ok {
		t.Skip("conn does not implement QueryerContext")
	}

	rows, err := ec.QueryContext(context.Background(), "SELECT `key`, `value` FROM kv", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	defer func() { _ = rows.Close() }()

	er, ok := rows.(*embeddedRows)
	if !ok {
		t.Fatalf("expected *embeddedRows, got %T", rows)
	}

	// Valid index returns a type name string.
	typeName := er.ColumnTypeDatabaseTypeName(0)
	if typeName == "" {
		t.Error("ColumnTypeDatabaseTypeName(0) should return non-empty string")
	}

	// Out-of-bounds index returns empty string.
	empty := er.ColumnTypeDatabaseTypeName(100)
	if empty != "" {
		t.Errorf("ColumnTypeDatabaseTypeName(100) = %q, want empty", empty)
	}

	// Negative index returns empty string.
	emptyNeg := er.ColumnTypeDatabaseTypeName(-1)
	if emptyNeg != "" {
		t.Errorf("ColumnTypeDatabaseTypeName(-1) = %q, want empty", emptyNeg)
	}
}

// TestEmbeddedRows_CloseIdempotent verifies Close is idempotent.
func TestEmbeddedRows_CloseIdempotent(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=closeidtest")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	ec, ok := conn.(sqld.QueryerContext)
	if !ok {
		t.Skip("conn does not implement QueryerContext")
	}

	rows, err := ec.QueryContext(context.Background(), "SELECT `key` FROM kv", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}

	// First close.
	if err := rows.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Second close should not error.
	if err := rows.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestEmbeddedRows_NextOnClosed verifies Next returns RowsClosedError after Close.
func TestEmbeddedRows_NextOnClosed(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=nextclosedtest")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	ec, ok := conn.(sqld.QueryerContext)
	if !ok {
		t.Skip("conn does not implement QueryerContext")
	}

	rows, err := ec.QueryContext(context.Background(), "SELECT `key` FROM kv", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}

	if err := rows.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	dest := make([]sqld.Value, 1)
	err = rows.Next(dest)
	if err == nil {
		t.Fatal("expected error after Close")
	}
	var rce *RowsClosedError
	if !errors.As(err, &rce) {
		t.Errorf("expected *RowsClosedError, got %T: %v", err, err)
	}
}

// TestNormalizeNetworkValue_AllTypes verifies normalizeNetworkValue handles all JSON-decoded types.
func TestToDriverValue_AllTypes(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		check func(got driver.Value) bool
	}{
		{
			name:  "nil",
			input: nil,
			check: func(got driver.Value) bool { return got == nil },
		},
		{
			name:  "string",
			input: "hello",
			check: func(got driver.Value) bool { s, ok := got.(string); return ok && s == "hello" },
		},
		{
			name:  "float64",
			input: float64(42.5),
			check: func(got driver.Value) bool { f, ok := got.(float64); return ok && f == 42.5 },
		},
		{
			name:  "bool true",
			input: true,
			check: func(got driver.Value) bool { b, ok := got.(bool); return ok && b },
		},
		{
			name:  "bool false",
			input: false,
			check: func(got driver.Value) bool { b, ok := got.(bool); return ok && !b },
		},
		{
			name:  "bytes",
			input: []byte("raw"),
			check: func(got driver.Value) bool { b, ok := got.([]byte); return ok && string(b) == "raw" },
		},
		{
			name:  "int64",
			input: int64(7),
			check: func(got driver.Value) bool { i, ok := got.(int64); return ok && i == 7 },
		},
		{
			// A type the wire contract does not produce is rejected as nil
			// rather than being given an invented string rendering, so a
			// contract violation is visible instead of silently papered over.
			name:  "unexpected type rejected",
			input: 42,
			check: func(got driver.Value) bool { return got == nil },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toDriverValue(tt.input)
			if !tt.check(got) {
				t.Errorf("toDriverValue(%v) = %v (%T), check failed", tt.input, got, got)
			}
		})
	}
}

// TestConvertValue_Uint64AcrossInt64Boundary is the silent-corruption guard
// for BIGINT UNSIGNED. driver.Value has no unsigned integer, so the upper half
// of the column's range has no int64 that represents it -- narrowing
// 18446744073709551615 yields -1, a different row returned without an error.
// Above the boundary the value is handed over as its decimal text, which is
// what go-sql-driver/mysql does for the same case, and what lets database/sql
// scan it back into a uint64 exactly.
func TestConvertValue_Uint64AcrossInt64Boundary(t *testing.T) {
	tests := []struct {
		name string
		in   uint64
		want driver.Value
	}{
		{name: "zero", in: 0, want: int64(0)},
		{name: "small", in: 42, want: int64(42)},
		{name: "maxint64", in: math.MaxInt64, want: int64(math.MaxInt64)},
		{name: "maxint64+1", in: math.MaxInt64 + 1, want: []byte("9223372036854775808")},
		{name: "maxuint64", in: math.MaxUint64, want: []byte("18446744073709551615")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, got := range []driver.Value{convertValue(tt.in), convertValue(uint(tt.in))} {
				switch want := tt.want.(type) {
				case int64:
					if got != want {
						t.Fatalf("convertValue(%d) = %v (%T), want %d", tt.in, got, got, want)
					}
				case []byte:
					gotB, ok := got.([]byte)
					if !ok {
						t.Fatalf("convertValue(%d) = %v (%T), want []byte", tt.in, got, got)
					}
					if !bytes.Equal(gotB, want) {
						t.Fatalf("convertValue(%d) = %q, want %q", tt.in, gotB, want)
					}
				}
			}
		})
	}
}

// TestConvertValue_Uint64IsDriverValue asserts every rendering convertValue
// produces for a uint64 is a type database/sql/driver.Value admits. A uint64
// returned as itself would scan correctly today and violate the interface --
// the kind of thing that works until a caller uses a different destination.
func TestConvertValue_Uint64IsDriverValue(t *testing.T) {
	for _, in := range []uint64{0, math.MaxInt64, math.MaxInt64 + 1, math.MaxUint64} {
		got := convertValue(in)
		if !driver.IsValue(got) {
			t.Fatalf("convertValue(%d) = %v (%T), which is not a driver.Value", in, got, got)
		}
	}
}
