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
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestNetworkRows_ColumnTypes verifies the column-type methods report the wire
// format honestly.
//
// The remote SQL API pre-formats every cell as text, so string/TEXT is the
// truthful answer for every column. Inferring a narrower type from a sampled
// value would make the reported type depend on the data rather than the
// schema, so the same column could claim different types between queries.
func TestNetworkRows_ColumnTypes(t *testing.T) {
	rows := newNetworkRows(&transport.SQLQueryResult{
		Columns: []string{"id", "name"},
		Rows:    []map[string]any{{"id": "1", "name": "alice"}},
	})

	for i := range rows.Columns() {
		if got := rows.ColumnTypeScanType(i); got != reflect.TypeOf("") {
			t.Errorf("ColumnTypeScanType(%d) = %v, want string", i, got)
		}
		if got := rows.ColumnTypeDatabaseTypeName(i); got != "TEXT" {
			t.Errorf("ColumnTypeDatabaseTypeName(%d) = %q, want TEXT", i, got)
		}

		// Nullability is unknown, which the contract expresses as ok=false.
		// Claiming a value here would assert something the wire cannot tell us.
		nullable, ok := rows.ColumnTypeNullable(i)
		if ok {
			t.Errorf("ColumnTypeNullable(%d) ok = true, want false (unknown)", i)
		}
		if nullable {
			t.Errorf("ColumnTypeNullable(%d) nullable = true, want false", i)
		}
	}
}

// TestNewNetworkRows_NilResult verifies a nil result yields empty rows rather
// than panicking.
func TestNewNetworkRows_NilResult(t *testing.T) {
	rows := newNetworkRows(nil)

	if len(rows.Columns()) != 0 {
		t.Errorf("Columns() = %v, want empty", rows.Columns())
	}
	if err := rows.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
