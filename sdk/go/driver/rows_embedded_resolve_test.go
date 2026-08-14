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
	"context"
	sqld "database/sql/driver"
	"errors"
	"io"
	"reflect"
	"testing"

	gmssql "github.com/dolthub/go-mysql-server/sql"
	gmstypes "github.com/dolthub/go-mysql-server/sql/types"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/catalog"
)

// The end-to-end behaviour of symbolic columns is asserted through a real
// engine in relational_symbolic_e2e_test.go. These tests drive embeddedRows
// directly instead, to reach the row shapes a healthy engine never produces:
// a cell the schema does not describe, and a cell the column type cannot
// resolve.

// newSymbolicRows builds an embeddedRows over a fixed set of rows, with a
// one-column ENUM schema.
func newSymbolicRows(t *testing.T, rows ...gmssql.Row) *embeddedRows {
	t.Helper()

	enum, err := gmstypes.CreateEnumType([]string{"pending", "active", "banned"}, gmssql.Collation_Default)
	if err != nil {
		t.Fatalf("CreateEnumType: %v", err)
	}
	return &embeddedRows{
		ctx:    gmssql.NewContext(context.Background()),
		schema: gmssql.Schema{{Name: "status", Type: enum}},
		iter:   gmssql.RowsToRowIter(rows...),
	}
}

// TestEmbeddedRows_ResolvesEnumOrdinal is the unit-level statement of the
// defect: the cell holds 2, the caller must receive "active".
func TestEmbeddedRows_ResolvesEnumOrdinal(t *testing.T) {
	rows := newSymbolicRows(t, gmssql.Row{uint16(2)})
	defer func() { _ = rows.Close() }()

	dest := make([]sqld.Value, 1)
	if err := rows.Next(dest); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if dest[0] != "active" {
		t.Errorf("dest[0] = %#v (%T), want \"active\"", dest[0], dest[0])
	}

	if err := rows.Next(dest); err != io.EOF {
		t.Errorf("second Next = %v, want io.EOF", err)
	}
}

// TestEmbeddedRows_ResolveErrorFailsTheRow asserts an unresolvable cell
// surfaces as an error from Next rather than degrading to the raw ordinal,
// which is exactly the silent wrong answer this resolution replaced.
func TestEmbeddedRows_ResolveErrorFailsTheRow(t *testing.T) {
	rows := newSymbolicRows(t, gmssql.Row{uint16(99)})
	defer func() { _ = rows.Close() }()

	dest := make([]sqld.Value, 1)
	err := rows.Next(dest)
	if err == nil {
		t.Fatal("Next returned nil for an ordinal with no label")
	}

	var typed *catalog.SymbolicValueError
	if !errors.As(err, &typed) {
		t.Errorf("Next error = %T (%v), want *catalog.SymbolicValueError", err, err)
	}
	if dest[0] != nil {
		t.Errorf("dest[0] = %#v, want it left unwritten", dest[0])
	}
}

// TestEmbeddedRows_ResolveWithoutSchemaEntry covers a row wider than the
// schema describing it. The extra cells have no column to be interpreted
// against, so they must pass through the Go-type normalization unchanged
// rather than be resolved against a neighbouring column's type.
func TestEmbeddedRows_ResolveWithoutSchemaEntry(t *testing.T) {
	rows := newSymbolicRows(t, gmssql.Row{uint16(1), uint16(2)})
	defer func() { _ = rows.Close() }()

	dest := make([]sqld.Value, 2)
	if err := rows.Next(dest); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if dest[0] != "pending" {
		t.Errorf("described cell = %#v, want \"pending\"", dest[0])
	}
	if dest[1] != int64(2) {
		t.Errorf("undescribed cell = %#v (%T), want int64(2)", dest[1], dest[1])
	}
}

// TestEmbeddedRows_ResolveNilColumn covers a schema entry that is nil, which
// carries no type to resolve against.
func TestEmbeddedRows_ResolveNilColumn(t *testing.T) {
	rows := &embeddedRows{
		ctx:    gmssql.NewContext(context.Background()),
		schema: gmssql.Schema{nil},
		iter:   gmssql.RowsToRowIter(gmssql.Row{uint16(2)}),
	}
	defer func() { _ = rows.Close() }()

	dest := make([]sqld.Value, 1)
	if err := rows.Next(dest); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if dest[0] != int64(2) {
		t.Errorf("dest[0] = %#v, want int64(2)", dest[0])
	}
}

// TestEmbeddedRows_ColumnTypeScanType_Symbolic asserts the advertised scan
// type for an ENUM column is the type Next produces, not the uint16 the
// engine stores.
func TestEmbeddedRows_ColumnTypeScanType_Symbolic(t *testing.T) {
	rows := newSymbolicRows(t)
	defer func() { _ = rows.Close() }()

	if got, want := rows.ColumnTypeScanType(0), reflect.TypeOf(""); got != want {
		t.Errorf("ColumnTypeScanType(0) = %v, want %v", got, want)
	}
}

// gmsType exists so nilValueTypeColumn can embed exactly sql.Type's method
// set under a field name that does not collide with sql.Type's own Type()
// method.
type gmsType interface{ gmssql.Type }

// nilValueTypeColumn reports no Go value type at all, covering the fallback
// ColumnTypeScanType applies when a column cannot name one.
type nilValueTypeColumn struct {
	gmsType
}

func (nilValueTypeColumn) ValueType() reflect.Type { return nil }

// TestEmbeddedRows_ColumnTypeScanType_NilValueType asserts the fallback is
// string rather than a nil reflect.Type, which database/sql cannot use.
func TestEmbeddedRows_ColumnTypeScanType_NilValueType(t *testing.T) {
	rows := &embeddedRows{
		ctx:    gmssql.NewContext(context.Background()),
		schema: gmssql.Schema{{Name: "mystery", Type: nilValueTypeColumn{gmstypes.Text}}},
		iter:   gmssql.RowsToRowIter(),
	}
	defer func() { _ = rows.Close() }()

	if got, want := rows.ColumnTypeScanType(0), reflect.TypeOf(""); got != want {
		t.Errorf("ColumnTypeScanType(0) = %v, want %v", got, want)
	}
}
