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
	"errors"
	"io"
	"testing"

	gmssql "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
)

// sliceRowIter is a RowIter over a fixed set of rows that records whether it
// was closed, so tests can assert the iterator is always released.
type sliceRowIter struct {
	rows   []gmssql.Row
	pos    int
	closed bool
	err    error
}

func (it *sliceRowIter) Next(_ *gmssql.Context) (gmssql.Row, error) {
	if it.err != nil {
		return nil, it.err
	}
	if it.pos >= len(it.rows) {
		return nil, io.EOF
	}
	row := it.rows[it.pos]
	it.pos++
	return row, nil
}

func (it *sliceRowIter) Close(_ *gmssql.Context) error {
	it.closed = true
	return nil
}

// okResultSchema is the single-column schema go-mysql-server uses to carry an
// OkResult.
var okResultSchema = gmssql.Schema{{
	Name: types.OkResultColumnName,
	Type: types.Int64,
}}

// TestDrainOkResult_ExtractsValue verifies the affected count and insert id
// are read from the OkResult row rather than inferred from the row count.
func TestDrainOkResult_ExtractsValue(t *testing.T) {
	iter := &sliceRowIter{rows: []gmssql.Row{
		{types.OkResult{RowsAffected: 7, InsertID: 42}},
	}}

	res, ok, err := drainOkResult(gmssql.NewEmptyContext(), okResultSchema, iter)
	if err != nil {
		t.Fatalf("drainOkResult: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true for an OkResult row")
	}
	if res.RowsAffected != 7 {
		t.Errorf("RowsAffected = %d, want 7", res.RowsAffected)
	}
	if res.InsertID != 42 {
		t.Errorf("InsertID = %d, want 42", res.InsertID)
	}
	if !iter.closed {
		t.Error("iterator was not closed")
	}
}

// TestDrainOkResult_PointerForm verifies a pointer-valued OkResult is accepted.
func TestDrainOkResult_PointerForm(t *testing.T) {
	iter := &sliceRowIter{rows: []gmssql.Row{
		{&types.OkResult{RowsAffected: 3}},
	}}

	res, ok, err := drainOkResult(gmssql.NewEmptyContext(), okResultSchema, iter)
	if err != nil {
		t.Fatalf("drainOkResult: %v", err)
	}
	if !ok || res.RowsAffected != 3 {
		t.Errorf("ok=%v RowsAffected=%d, want true/3", ok, res.RowsAffected)
	}
}

// TestDrainOkResult_ResultSet verifies a real result set reports no OkResult
// and is fully drained, so a SELECT run through Exec does not report a
// fabricated affected count.
func TestDrainOkResult_ResultSet(t *testing.T) {
	schema := gmssql.Schema{
		{Name: "id", Type: types.Int64},
		{Name: "name", Type: types.Text},
	}
	iter := &sliceRowIter{rows: []gmssql.Row{
		{int64(1), "a"},
		{int64(2), "b"},
	}}

	_, ok, err := drainOkResult(gmssql.NewEmptyContext(), schema, iter)
	if err != nil {
		t.Fatalf("drainOkResult: %v", err)
	}
	if ok {
		t.Error("ok = true, want false for a result set")
	}
	if iter.pos != 2 {
		t.Errorf("drained %d rows, want 2", iter.pos)
	}
	if !iter.closed {
		t.Error("iterator was not closed")
	}
}

// TestDrainOkResult_EmptyOkSchema verifies an OkResult-shaped schema with no
// rows reports no result rather than erroring.
func TestDrainOkResult_EmptyOkSchema(t *testing.T) {
	iter := &sliceRowIter{}

	_, ok, err := drainOkResult(gmssql.NewEmptyContext(), okResultSchema, iter)
	if err != nil {
		t.Fatalf("drainOkResult: %v", err)
	}
	if ok {
		t.Error("ok = true, want false when no row is emitted")
	}
	if !iter.closed {
		t.Error("iterator was not closed")
	}
}

// TestDrainOkResult_WrongRowType verifies a single-column OkResult schema
// carrying an unexpected value reports no result instead of panicking.
func TestDrainOkResult_WrongRowType(t *testing.T) {
	iter := &sliceRowIter{rows: []gmssql.Row{{int64(5)}}}

	_, ok, err := drainOkResult(gmssql.NewEmptyContext(), okResultSchema, iter)
	if err != nil {
		t.Fatalf("drainOkResult: %v", err)
	}
	if ok {
		t.Error("ok = true, want false for a non-OkResult value")
	}
}

// TestDrainOkResult_NilPointer verifies a nil *OkResult is not dereferenced.
func TestDrainOkResult_NilPointer(t *testing.T) {
	var nilRes *types.OkResult
	iter := &sliceRowIter{rows: []gmssql.Row{{nilRes}}}

	_, ok, err := drainOkResult(gmssql.NewEmptyContext(), okResultSchema, iter)
	if err != nil {
		t.Fatalf("drainOkResult: %v", err)
	}
	if ok {
		t.Error("ok = true, want false for a nil OkResult pointer")
	}
}

// TestDrainOkResult_IteratorError verifies an iteration failure is surfaced
// and the iterator is still closed.
func TestDrainOkResult_IteratorError(t *testing.T) {
	sentinel := errors.New("boom")
	iter := &sliceRowIter{err: sentinel}

	_, ok, err := drainOkResult(gmssql.NewEmptyContext(), okResultSchema, iter)
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want %v", err, sentinel)
	}
	if ok {
		t.Error("ok = true, want false on error")
	}
	if !iter.closed {
		t.Error("iterator was not closed on the error path")
	}
}

// TestOkResultOfRow_WrongArity verifies a row of the wrong width is rejected.
func TestOkResultOfRow_WrongArity(t *testing.T) {
	if _, ok := okResultOfRow(gmssql.Row{}); ok {
		t.Error("empty row accepted")
	}
	if _, ok := okResultOfRow(gmssql.Row{types.OkResult{}, types.OkResult{}}); ok {
		t.Error("two-column row accepted")
	}
}

// TestIsOkResultSchema verifies schema shape detection, including the nil
// column entry a malformed schema could carry.
func TestIsOkResultSchema(t *testing.T) {
	if !isOkResultSchema(okResultSchema) {
		t.Error("OkResult schema not recognized")
	}
	if isOkResultSchema(gmssql.Schema{{Name: "id", Type: types.Int64}}) {
		t.Error("ordinary single-column schema recognized as OkResult")
	}
	if isOkResultSchema(gmssql.Schema{}) {
		t.Error("empty schema recognized as OkResult")
	}
	if isOkResultSchema(gmssql.Schema{nil}) {
		t.Error("nil column recognized as OkResult")
	}
}
