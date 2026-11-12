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
	"context"
	sqld "database/sql/driver"
	"errors"
	"testing"

	gmssql "github.com/dolthub/go-mysql-server/sql"
	gmstypes "github.com/dolthub/go-mysql-server/sql/types"
)

// newWidthRows returns embedded rows whose schema declares one column and
// whose iterator yields the given rows, whatever their width.
func newWidthRows(rows ...gmssql.Row) *embeddedRows {
	return &embeddedRows{
		ctx:    gmssql.NewContext(context.Background()),
		schema: gmssql.Schema{{Name: "v", Type: gmstypes.Int64}},
		iter:   gmssql.RowsToRowIter(rows...),
	}
}

// TestEmbeddedRows_Next_RowWiderThanDest is the defect gosec's G602 found
// once #408 lowered the severity floor: Next wrote dest[i] for every cell the
// engine returned, so a row wider than the destination database/sql sized
// from Columns() panicked inside the caller's rows.Next instead of returning
// an error.
func TestEmbeddedRows_Next_RowWiderThanDest(t *testing.T) {
	rows := newWidthRows(gmssql.Row{int64(1), int64(2)})
	defer func() { _ = rows.Close() }()

	dest := make([]sqld.Value, 1)
	err := rows.Next(dest)

	var width *RowWidthError
	if !errors.As(err, &width) {
		t.Fatalf("Next = %v, want *RowWidthError", err)
	}
	if width.Columns != 1 || width.Values != 2 {
		t.Errorf("RowWidthError = %+v, want Columns=1 Values=2", width)
	}
}

// TestEmbeddedRows_Next_RowNarrowerThanDest pins the other direction: a row
// that fills fewer slots than dest holds would leave stale values from the
// previous row in the rest, which database/sql would hand to Scan as if they
// belonged to this one.
func TestEmbeddedRows_Next_RowNarrowerThanDest(t *testing.T) {
	rows := newWidthRows(gmssql.Row{int64(1)})
	defer func() { _ = rows.Close() }()

	dest := make([]sqld.Value, 2)
	err := rows.Next(dest)

	var width *RowWidthError
	if !errors.As(err, &width) {
		t.Fatalf("Next = %v, want *RowWidthError", err)
	}
	if width.Columns != 2 || width.Values != 1 {
		t.Errorf("RowWidthError = %+v, want Columns=2 Values=1", width)
	}
}

// TestRowWidthError_Message pins the message format.
func TestRowWidthError_Message(t *testing.T) {
	err := &RowWidthError{Columns: 3, Values: 4}
	if got, want := err.Error(), "driver: row has 4 values for 3 columns"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
