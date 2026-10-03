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

package catalog

import (
	"bytes"
	"errors"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/sqltypes"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// keylessDef is a two-column table declaring no PRIMARY KEY, whose trailing
// column is BIGINT UNSIGNED -- the exact shape that used to be silently
// corrupted, because a BIGINT UNSIGNED value is type-compatible with the row
// id KeyCodec was reading out of the last row element. See
// TestKeyCodec_TrailingUint64ColumnIsNotTheRowID.
func keylessDef() *service.TableDef {
	return &service.TableDef{
		Name:     "h",
		Database: "shop",
		Columns: []service.ColumnDef{
			{Name: "name", Type: "VARCHAR(64)", Nullable: true},
			{Name: "n", Type: "BIGINT UNSIGNED", Nullable: true},
		},
	}
}

// keyedDef is keylessDef's counterpart with a declared PRIMARY KEY, used to
// assert that the row id changes nothing at all for a keyed table.
func keyedDef() *service.TableDef {
	def := keylessDef()
	def.PrimaryKey = []service.PKColumn{{Name: "n"}}
	return def
}

// ---------------------------------------------------------------------------
// The column itself
// ---------------------------------------------------------------------------

// TestRowIDColumnName_IsRecognizedAsHiddenSystem verifies the name satisfies
// the predicate go-mysql-server uses to reject a query that references the
// column (sql.IsHiddenSystemColumn, applied by the
// validateNoHiddenSystemColumns analyzer rule). Nothing else in this package
// would fail if the prefix were dropped -- the column would simply become
// user-visible and user-writable -- so it is pinned here directly.
func TestRowIDColumnName_IsRecognizedAsHiddenSystem(t *testing.T) {
	if !sqle.IsHiddenSystemColumn(RowIDColumnName) {
		t.Fatalf("sql.IsHiddenSystemColumn(%q) = false, want true", RowIDColumnName)
	}
}

// TestRowIDColumnName_IsNotAFunctionalIndexColumn verifies the name is not
// mistaken for the backing column of a functional index named "rowid", which
// is the other thing the "!hidden!" prefix is used for (see
// sql.HiddenSystemColumnName / sql.IsHiddenSystemColumnForIndex). Colliding
// with that shape would make DROP INDEX rowid delete this column.
func TestRowIDColumnName_IsNotAFunctionalIndexColumn(t *testing.T) {
	for _, indexName := range []string{"rowid", "ROWID", "primary", ""} {
		if sqle.IsHiddenSystemColumnForIndex(RowIDColumnName, indexName) {
			t.Errorf("IsHiddenSystemColumnForIndex(%q, %q) = true, want false", RowIDColumnName, indexName)
		}
	}
}

// TestHasRowID covers the three inputs the predicate distinguishes.
func TestHasRowID(t *testing.T) {
	cases := []struct {
		name string
		def  *service.TableDef
		want bool
	}{
		{"nil def", nil, false},
		{"no primary key", keylessDef(), true},
		{"declared primary key", keyedDef(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasRowID(tc.def); got != tc.want {
				t.Fatalf("HasRowID = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SchemaOf / PrimaryKeySchemaOf
// ---------------------------------------------------------------------------

// TestSchemaOf_AppendsHiddenRowIDForKeylessTable verifies the hidden column
// is appended (never inserted), and carries exactly the attributes the
// INSERT path depends on: nullable, so go-mysql-server's projection can
// leave it NULL for relationalWriter.Insert to fill, and HiddenSystem, so
// every user-facing surface excludes it.
func TestSchemaOf_AppendsHiddenRowIDForKeylessTable(t *testing.T) {
	schema, err := SchemaOf(keylessDef())
	if err != nil {
		t.Fatalf("SchemaOf: %v", err)
	}
	if len(schema) != 3 {
		t.Fatalf("len(schema) = %d, want 3 (2 declared + hidden row id)", len(schema))
	}
	if schema[0].Name != "name" || schema[1].Name != "n" {
		t.Fatalf("declared columns moved: %q, %q", schema[0].Name, schema[1].Name)
	}

	col := schema[2]
	if col.Name != RowIDColumnName {
		t.Fatalf("trailing column is %q, want %q", col.Name, RowIDColumnName)
	}
	if !col.HiddenSystem {
		t.Error("row id column is not HiddenSystem, so SELECT * and INSERT would expose it")
	}
	if !col.Nullable {
		t.Error("row id column is not Nullable, so INSERT would fail validateNullability before the writer could fill it")
	}
	if col.PrimaryKey {
		t.Error("row id column claims PrimaryKey, which SHOW CREATE TABLE would render")
	}
	if col.Type.Type() != sqltypes.Uint64 {
		t.Errorf("row id column type is %v, want an unsigned 64-bit integer", col.Type.Type())
	}
	if col.Source != "h" {
		t.Errorf("row id column Source = %q, want the table name", col.Source)
	}
	if col.Default != nil {
		t.Error("row id column declares a default, which would fill the slot before the writer sees it")
	}
}

// TestSchemaOf_KeyedTableHasNoHiddenRowID verifies a table with a declared
// primary key is completely unaffected.
func TestSchemaOf_KeyedTableHasNoHiddenRowID(t *testing.T) {
	schema, err := SchemaOf(keyedDef())
	if err != nil {
		t.Fatalf("SchemaOf: %v", err)
	}
	if len(schema) != 2 {
		t.Fatalf("len(schema) = %d, want 2", len(schema))
	}
	for _, col := range schema {
		if col.HiddenSystem {
			t.Fatalf("keyed table gained a hidden column %q", col.Name)
		}
	}
}

// TestPrimaryKeySchemaOf_KeylessReportsNoKeyOrdinals verifies the physically
// keyed-but-not-declared-keyed table still reports no primary key ordinals,
// which is what keeps SHOW CREATE TABLE and information_schema from
// rendering a PRIMARY KEY clause naming a column the user cannot see.
func TestPrimaryKeySchemaOf_KeylessReportsNoKeyOrdinals(t *testing.T) {
	pkSchema, err := PrimaryKeySchemaOf(keylessDef())
	if err != nil {
		t.Fatalf("PrimaryKeySchemaOf: %v", err)
	}
	if len(pkSchema.PkOrdinals) != 0 {
		t.Fatalf("PkOrdinals = %v, want empty", pkSchema.PkOrdinals)
	}
	if len(pkSchema.Schema) != 3 {
		t.Fatalf("len(pkSchema.Schema) = %d, want 3 (the hidden row id is still a schema column)", len(pkSchema.Schema))
	}
}

// ---------------------------------------------------------------------------
// KeyCodec
// ---------------------------------------------------------------------------

// TestKeyCodec_Synthetic covers both answers of the predicate the read and
// write paths branch on.
func TestKeyCodec_Synthetic(t *testing.T) {
	keyless, err := NewKeyCodec(keylessDef())
	if err != nil {
		t.Fatalf("NewKeyCodec(keyless): %v", err)
	}
	if !keyless.Synthetic() {
		t.Error("Synthetic() = false for a table with no declared primary key")
	}

	keyed, err := NewKeyCodec(keyedDef())
	if err != nil {
		t.Fatalf("NewKeyCodec(keyed): %v", err)
	}
	if keyed.Synthetic() {
		t.Error("Synthetic() = true for a table with a declared primary key")
	}
}

// TestKeyCodec_TrailingUint64ColumnIsNotTheRowID is the regression test for
// the silent data loss this whole subsystem exists to prevent: a keyless
// table whose LAST declared column is BIGINT UNSIGNED used to have that
// column read as the row id, because it is type-compatible with one. Two
// rows sharing that column's value then collided on the primary key and the
// second was rejected as a duplicate -- two distinct rows became one.
//
// A row that stops at the last declared column is now rejected outright, and
// a full-width row keys off the hidden slot, never off column "n".
func TestKeyCodec_TrailingUint64ColumnIsNotTheRowID(t *testing.T) {
	codec, err := NewKeyCodec(keylessDef())
	if err != nil {
		t.Fatalf("NewKeyCodec: %v", err)
	}

	if _, err := codec.Encode(sqle.Row{"a", uint64(7)}); err == nil {
		t.Fatal("Encode of a row with no row id slot succeeded, want *MissingRowIDError")
	} else {
		var missing *MissingRowIDError
		if !errors.As(err, &missing) {
			t.Fatalf("Encode error is %T (%v), want *MissingRowIDError", err, err)
		}
	}

	// Two rows with identical user columns, different row ids: distinct keys.
	k1, err := codec.Encode(sqle.Row{"a", uint64(7), uint64(1)})
	if err != nil {
		t.Fatalf("Encode row 1: %v", err)
	}
	k2, err := codec.Encode(sqle.Row{"a", uint64(7), uint64(2)})
	if err != nil {
		t.Fatalf("Encode row 2: %v", err)
	}
	if k1 == k2 {
		t.Fatal("two rows with the same user columns encoded to the same key")
	}
	if k1 >= k2 {
		t.Fatal("row id order is not key order, so a table scan would not return insertion order")
	}

	// The key depends only on the row id, never on column "n".
	kSameID, err := codec.Encode(sqle.Row{"zzz", uint64(999), uint64(1)})
	if err != nil {
		t.Fatalf("Encode row 3: %v", err)
	}
	if kSameID != k1 {
		t.Fatal("the encoded key varies with a declared column, so it is not keyed purely by the row id")
	}
}

// TestKeyCodec_DecodeRowID verifies the round trip a read path depends on:
// the row id recovered from a key is the uint64 that was encoded into it.
func TestKeyCodec_DecodeRowID(t *testing.T) {
	codec, err := NewKeyCodec(keylessDef())
	if err != nil {
		t.Fatalf("NewKeyCodec: %v", err)
	}
	for _, id := range []uint64{1, 2, 1 << 32, ^uint64(0)} {
		key, eerr := codec.EncodeValues([]any{id})
		if eerr != nil {
			t.Fatalf("EncodeValues(%d): %v", id, eerr)
		}
		vals, derr := codec.Decode(key)
		if derr != nil {
			t.Fatalf("Decode(%d): %v", id, derr)
		}
		if len(vals) != 1 || vals[0] != id {
			t.Fatalf("Decode = %v, want [%d]", vals, id)
		}
	}
}

// ---------------------------------------------------------------------------
// RowCodec
// ---------------------------------------------------------------------------

// TestRowCodec_Width covers both shapes: a keyed table's rows are exactly
// its declared columns, a keyless table's carry one more slot.
func TestRowCodec_Width(t *testing.T) {
	keyless, err := NewRowCodec(keylessDef())
	if err != nil {
		t.Fatalf("NewRowCodec(keyless): %v", err)
	}
	if got := keyless.Width(); got != 3 {
		t.Errorf("keyless Width() = %d, want 3", got)
	}

	keyed, err := NewRowCodec(keyedDef())
	if err != nil {
		t.Fatalf("NewRowCodec(keyed): %v", err)
	}
	if got := keyed.Width(); got != 2 {
		t.Errorf("keyed Width() = %d, want 2", got)
	}
}

// TestRowCodec_RowIDIsNotStored verifies the row id never reaches the
// encoded bytes: a keyless table's row encodes byte-identically to the same
// user values in a keyed table, and decodes back with a nil hidden slot for
// the caller to fill from the key.
func TestRowCodec_RowIDIsNotStored(t *testing.T) {
	keyless, err := NewRowCodec(keylessDef())
	if err != nil {
		t.Fatalf("NewRowCodec(keyless): %v", err)
	}
	keyed, err := NewRowCodec(keyedDef())
	if err != nil {
		t.Fatalf("NewRowCodec(keyed): %v", err)
	}

	withID, err := keyless.Encode(sqle.Row{"a", uint64(7), uint64(12345)})
	if err != nil {
		t.Fatalf("Encode(keyless): %v", err)
	}
	withoutID, err := keyed.Encode(sqle.Row{"a", uint64(7)})
	if err != nil {
		t.Fatalf("Encode(keyed): %v", err)
	}
	if !bytes.Equal(withID, withoutID) {
		t.Fatalf("keyless row encoded to %x, keyed row to %x; the row id leaked into the value", withID, withoutID)
	}

	// A different row id must not change the stored bytes either.
	other, err := keyless.Encode(sqle.Row{"a", uint64(7), uint64(999)})
	if err != nil {
		t.Fatalf("Encode(keyless, other id): %v", err)
	}
	if !bytes.Equal(withID, other) {
		t.Fatal("changing the row id changed the encoded row value")
	}

	row, err := keyless.Decode(withID)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(row) != 3 {
		t.Fatalf("len(decoded row) = %d, want 3", len(row))
	}
	if row[0] != "a" || row[1] != uint64(7) {
		t.Fatalf("declared columns decoded as %v, %v", row[0], row[1])
	}
	if row[2] != nil {
		t.Fatalf("hidden row id slot decoded as %v, want nil (only the key knows it)", row[2])
	}
}

// TestRowCodec_EncodeRejectsRowMissingRowIDSlot verifies the arity check
// counts the hidden slot, so a caller that hands over only the declared
// columns is told so rather than having its last column silently encoded
// into the wrong position.
func TestRowCodec_EncodeRejectsRowMissingRowIDSlot(t *testing.T) {
	codec, err := NewRowCodec(keylessDef())
	if err != nil {
		t.Fatalf("NewRowCodec: %v", err)
	}
	_, err = codec.Encode(sqle.Row{"a", uint64(7)})
	var mismatch *RowColumnCountMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Encode error is %T (%v), want *RowColumnCountMismatchError", err, err)
	}
	if mismatch.Expected != 3 || mismatch.Got != 2 {
		t.Fatalf("mismatch reports Expected=%d Got=%d, want 3 and 2", mismatch.Expected, mismatch.Got)
	}
}

// TestRowCodec_EncodedSizeIgnoresRowIDSlot verifies the size estimate agrees
// with Encode for a keyless table, which DataLength depends on.
func TestRowCodec_EncodedSizeIgnoresRowIDSlot(t *testing.T) {
	codec, err := NewRowCodec(keylessDef())
	if err != nil {
		t.Fatalf("NewRowCodec: %v", err)
	}
	row := sqle.Row{"abc", uint64(7), uint64(42)}
	encoded, err := codec.Encode(row)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if got := codec.EncodedSize(row); got != len(encoded) {
		t.Fatalf("EncodedSize = %d, Encode produced %d bytes", got, len(encoded))
	}
}
