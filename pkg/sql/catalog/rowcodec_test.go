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
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"strconv"
	"testing"
	"testing/quick"
	"time"

	"github.com/cockroachdb/apd/v3"
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// singleColumnCodec builds a RowCodec for a one-column table of the given
// SQL type, for use by the round-trip helpers below.
//
// The column is declared as the table's primary key so the codec's rows are
// exactly one value wide: a table with no declared primary key carries a
// trailing hidden row id slot (see rowid.go), which is a property in its own
// right and is covered separately by TestRowCodec_RowIDSlot rather than
// smeared across every per-type round trip here.
func singleColumnCodec(t *testing.T, sqlType string, nullable bool) *RowCodec {
	t.Helper()
	def := &service.TableDef{
		Name:       "t",
		Columns:    []service.ColumnDef{{Name: "v", Type: sqlType, Nullable: nullable}},
		PrimaryKey: []service.PKColumn{{Name: "v"}},
	}
	rc, err := NewRowCodec(def)
	if err != nil {
		t.Fatalf("NewRowCodec(%q): %v", sqlType, err)
	}
	return rc
}

// rowRoundTripEqual encodes and decodes a single-column row of the given SQL
// type and reports whether the decoded value equals want under equal.
func rowRoundTripEqual(t *testing.T, sqlType string, want any, equal func(a, b any) bool) bool {
	t.Helper()
	rc := singleColumnCodec(t, sqlType, false)
	data, err := rc.Encode(sqle.Row{want})
	if err != nil {
		t.Fatalf("Encode(%v): %v", want, err)
		return false
	}
	row, err := rc.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
		return false
	}
	if len(data) != rc.EncodedSize(sqle.Row{want}) {
		t.Fatalf("EncodedSize mismatch: Encode produced %d bytes, EncodedSize reported %d", len(data), rc.EncodedSize(sqle.Row{want}))
	}
	return equal(row[0], want)
}

func deepEqual(a, b any) bool { return reflect.DeepEqual(a, b) }

// ---------------------------------------------------------------------------
// testing/quick property: round trip for every supported type
// ---------------------------------------------------------------------------

func TestRowCodec_QuickRoundTrip_Int8(t *testing.T) {
	prop := func(x int8) bool { return rowRoundTripEqual(t, "TINYINT", x, deepEqual) }
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRowCodec_QuickRoundTrip_Uint8(t *testing.T) {
	prop := func(x uint8) bool { return rowRoundTripEqual(t, "TINYINT UNSIGNED", x, deepEqual) }
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRowCodec_QuickRoundTrip_Int16(t *testing.T) {
	prop := func(x int16) bool { return rowRoundTripEqual(t, "SMALLINT", x, deepEqual) }
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRowCodec_QuickRoundTrip_Uint16(t *testing.T) {
	prop := func(x uint16) bool { return rowRoundTripEqual(t, "SMALLINT UNSIGNED", x, deepEqual) }
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRowCodec_QuickRoundTrip_Int32(t *testing.T) {
	prop := func(x int32) bool { return rowRoundTripEqual(t, "INT", x, deepEqual) }
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRowCodec_QuickRoundTrip_Uint32(t *testing.T) {
	prop := func(x uint32) bool { return rowRoundTripEqual(t, "INT UNSIGNED", x, deepEqual) }
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRowCodec_QuickRoundTrip_Int64(t *testing.T) {
	prop := func(x int64) bool { return rowRoundTripEqual(t, "BIGINT", x, deepEqual) }
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRowCodec_QuickRoundTrip_Uint64(t *testing.T) {
	prop := func(x uint64) bool { return rowRoundTripEqual(t, "BIGINT UNSIGNED", x, deepEqual) }
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRowCodec_QuickRoundTrip_Float32(t *testing.T) {
	prop := func(x float32) bool { return rowRoundTripEqual(t, "FLOAT", x, deepEqual) }
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRowCodec_QuickRoundTrip_Float64(t *testing.T) {
	prop := func(x float64) bool { return rowRoundTripEqual(t, "DOUBLE", x, deepEqual) }
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRowCodec_QuickRoundTrip_String(t *testing.T) {
	prop := func(x string) bool { return rowRoundTripEqual(t, "VARCHAR(1000)", x, deepEqual) }
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRowCodec_QuickRoundTrip_Bytes(t *testing.T) {
	prop := func(x []byte) bool { return rowRoundTripEqual(t, "BLOB", x, deepEqual) }
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRowCodec_QuickRoundTrip_Bit(t *testing.T) {
	prop := func(x uint64) bool { return rowRoundTripEqual(t, "BIT(64)", x, deepEqual) }
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRowCodec_QuickRoundTrip_Enum(t *testing.T) {
	prop := func(seed uint16) bool {
		x := seed % 3
		return rowRoundTripEqual(t, "ENUM('a','b','c')", x, deepEqual)
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRowCodec_QuickRoundTrip_Set(t *testing.T) {
	prop := func(seed uint64) bool {
		x := seed % 8 // SET('a','b','c') has 2^3 = 8 valid bitmasks
		return rowRoundTripEqual(t, "SET('a','b','c')", x, deepEqual)
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRowCodec_QuickRoundTrip_Year(t *testing.T) {
	prop := func(seed uint16) bool {
		x := int16(1901 + seed%(2155-1901+1))
		return rowRoundTripEqual(t, "YEAR", x, deepEqual)
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

// quickDecimal implements quick.Generator to produce arbitrary exact
// decimal values, including magnitudes and fractional digits float64
// cannot represent exactly.
type quickDecimal struct{ d *apd.Decimal }

func (quickDecimal) Generate(r *rand.Rand, _ int) reflect.Value {
	intPart := r.Int63()
	if r.Intn(2) == 0 {
		intPart = -intPart
	}
	d, _, err := apd.NewFromString(strconv.FormatInt(intPart, 10) + "." + fmt.Sprintf("%02d", r.Intn(100)))
	if err != nil {
		panic(err)
	}
	return reflect.ValueOf(quickDecimal{d})
}

func TestRowCodec_QuickRoundTrip_Decimal(t *testing.T) {
	prop := func(x quickDecimal) bool {
		return rowRoundTripEqual(t, "DECIMAL(30,2)", x.d, func(a, b any) bool {
			return a.(*apd.Decimal).Cmp(b.(*apd.Decimal)) == 0
		})
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

// quickTime implements quick.Generator to produce arbitrary UTC instants.
type quickTime time.Time

func (quickTime) Generate(r *rand.Rand, _ int) reflect.Value {
	sec := r.Int63n(4_000_000_000) - 2_000_000_000
	return reflect.ValueOf(quickTime(time.Unix(sec, 0).UTC()))
}

func TestRowCodec_QuickRoundTrip_Datetime(t *testing.T) {
	prop := func(x quickTime) bool {
		return rowRoundTripEqual(t, "DATETIME", time.Time(x), func(a, b any) bool {
			return a.(time.Time).Equal(b.(time.Time))
		})
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

// quickTimespan implements quick.Generator to produce arbitrary TIME
// values, including negative spans (MySQL's TIME type is signed).
type quickTimespan types.Timespan

func (quickTimespan) Generate(r *rand.Rand, _ int) reflect.Value {
	const bound = 3020399000000
	us := r.Int63n(2*bound) - bound
	return reflect.ValueOf(quickTimespan(us))
}

func TestRowCodec_QuickRoundTrip_Time(t *testing.T) {
	prop := func(x quickTimespan) bool {
		return rowRoundTripEqual(t, "TIME", types.Timespan(x), deepEqual)
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// NULL vs. present-but-empty
// ---------------------------------------------------------------------------

func TestRowCodec_NullIsNeverConflatedWithEmpty(t *testing.T) {
	cases := []struct {
		name    string
		sqlType string
		empty   any
	}{
		{"varchar", "VARCHAR(10)", ""},
		{"blob", "BLOB", []byte{}},
		{"json", "JSON", types.JSONDocument{Val: nil}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rc := singleColumnCodec(t, c.sqlType, true)

			nullData, err := rc.Encode(sqle.Row{nil})
			if err != nil {
				t.Fatalf("Encode(nil): %v", err)
			}
			nullRow, err := rc.Decode(nullData)
			if err != nil {
				t.Fatalf("Decode(nullData): %v", err)
			}
			if nullRow[0] != nil {
				t.Fatalf("NULL value decoded as non-nil: %#v", nullRow[0])
			}

			emptyData, err := rc.Encode(sqle.Row{c.empty})
			if err != nil {
				t.Fatalf("Encode(empty): %v", err)
			}
			emptyRow, err := rc.Decode(emptyData)
			if err != nil {
				t.Fatalf("Decode(emptyData): %v", err)
			}
			if emptyRow[0] == nil {
				t.Fatal("present empty value decoded as nil")
			}

			if len(nullData) == len(emptyData) && reflect.DeepEqual(nullData, emptyData) {
				t.Fatal("NULL and empty encoded identically")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Decimal exactness and large-integer exactness
// ---------------------------------------------------------------------------

func TestRowCodec_DecimalExactness(t *testing.T) {
	// 0.1 has no exact float64 representation; a JSON-style float64
	// round trip would silently perturb it. Also exercise a value that
	// overflows float64's 53-bit mantissa.
	values := []string{
		"0.1",
		"79228162514264337593543950335.99",
		"-79228162514264337593543950335.99",
		"0.00",
		"-0.01",
	}
	for _, s := range values {
		t.Run(s, func(t *testing.T) {
			want, _, err := apd.NewFromString(s)
			if err != nil {
				t.Fatalf("apd.NewFromString(%q): %v", s, err)
			}
			// Comparing the rendered text as well as the numeric value is
			// what makes this an exactness test rather than an equality
			// test: 0.00 and 0 compare equal but do not carry the same
			// scale, and a codec that drops the scale still passes Cmp.
			ok := rowRoundTripEqual(t, "DECIMAL(35,2)", want, func(a, b any) bool {
				got, other := a.(*apd.Decimal), b.(*apd.Decimal)
				return got.Cmp(other) == 0 &&
					got.Text('f') == other.Text('f') &&
					got.Exponent == other.Exponent
			})
			if !ok {
				t.Fatalf("decimal %q did not round trip exactly", s)
			}
		})
	}
}

func TestRowCodec_Int64AboveFloat64Precision(t *testing.T) {
	// 2^53 is the largest integer float64 can represent exactly; anything
	// above it silently loses precision when round-tripped through
	// float64, which is exactly what the JSON-backed encoding this
	// replaces used to do.
	values := []int64{
		1<<53 + 1,
		1<<62 + 12345,
		math.MaxInt64,
		math.MinInt64,
	}
	for _, v := range values {
		if !rowRoundTripEqual(t, "BIGINT", v, deepEqual) {
			t.Fatalf("int64 %d did not round trip exactly", v)
		}
	}
}

// ---------------------------------------------------------------------------
// Schema evolution
// ---------------------------------------------------------------------------

func TestRowCodec_SchemaVersionSkew(t *testing.T) {
	v1 := &service.TableDef{
		Name:          "t",
		SchemaVersion: 1,
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED"},
			{Name: "name", Type: "VARCHAR(100)"},
		},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	}
	rcV1, err := NewRowCodec(v1)
	if err != nil {
		t.Fatalf("NewRowCodec(v1): %v", err)
	}
	encoded, err := rcV1.Encode(sqle.Row{uint64(42), "alice"})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	v2 := &service.TableDef{
		Name:          "t",
		SchemaVersion: 2,
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED"},
			{Name: "name", Type: "VARCHAR(100)"},
			{Name: "nickname", Type: "VARCHAR(100)", Nullable: true},
			{Name: "status", Type: "VARCHAR(20)", Default: "'active'"},
		},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	}
	rcV2, err := NewRowCodec(v2)
	if err != nil {
		t.Fatalf("NewRowCodec(v2): %v", err)
	}

	row, err := rcV2.Decode(encoded)
	if err != nil {
		t.Fatalf("Decode(v1 bytes with v2 codec): %v", err)
	}
	if len(row) != 4 {
		t.Fatalf("expected 4 columns, got %d", len(row))
	}
	if row[0] != uint64(42) {
		t.Fatalf("id: got %v", row[0])
	}
	if row[1] != "alice" {
		t.Fatalf("name: got %v", row[1])
	}
	if row[2] != nil {
		t.Fatalf("nickname (nullable, no default) should decode as NULL, got %v", row[2])
	}
	if row[3] != "active" {
		t.Fatalf("status should decode to its default 'active', got %v", row[3])
	}

	// The v2 codec must still be able to round trip its own, full-width
	// rows.
	full := sqle.Row{uint64(7), "bob", "bobby", "inactive"}
	encodedV2, err := rcV2.Encode(full)
	if err != nil {
		t.Fatalf("Encode(v2 row): %v", err)
	}
	decodedV2, err := rcV2.Decode(encodedV2)
	if err != nil {
		t.Fatalf("Decode(v2 row): %v", err)
	}
	if !reflect.DeepEqual(decodedV2, full) {
		t.Fatalf("v2 round trip mismatch: got %#v, want %#v", decodedV2, full)
	}
}

func TestRowCodec_SchemaVersionSkew_NotNullableNoDefaultUsesZeroValue(t *testing.T) {
	v1 := &service.TableDef{
		Name:       "t",
		Columns:    []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	}
	rcV1, err := NewRowCodec(v1)
	if err != nil {
		t.Fatalf("NewRowCodec(v1): %v", err)
	}
	encoded, err := rcV1.Encode(sqle.Row{uint64(1)})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	v2 := &service.TableDef{
		Name: "t",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED"},
			{Name: "score", Type: "INT"}, // not nullable, no default
		},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	}
	rcV2, err := NewRowCodec(v2)
	if err != nil {
		t.Fatalf("NewRowCodec(v2): %v", err)
	}
	row, err := rcV2.Decode(encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if row[1] != int32(0) {
		t.Fatalf("expected zero-value fallback int32(0), got %#v", row[1])
	}
}

// ---------------------------------------------------------------------------
// Constructor and malformed-input errors
// ---------------------------------------------------------------------------

func TestNewRowCodec_Errors(t *testing.T) {
	if _, err := NewRowCodec(nil); err == nil {
		t.Fatal("expected error for nil TableDef")
	}
	var nilErr *NilTableDefError
	if _, err := NewRowCodec(nil); !errors.As(err, &nilErr) {
		t.Fatal("expected *NilTableDefError")
	}

	empty := &service.TableDef{Name: "t"}
	if _, err := NewRowCodec(empty); err == nil {
		t.Fatal("expected error for empty columns")
	}

	bad := &service.TableDef{Name: "t", Columns: []service.ColumnDef{{Name: "g", Type: "GEOMETRY"}}}
	if _, err := NewRowCodec(bad); err == nil {
		t.Fatal("expected error to propagate for unsupported column type")
	}
}

func TestRowCodec_Encode_ColumnCountMismatch(t *testing.T) {
	rc := singleColumnCodec(t, "INT", false)
	_, err := rc.Encode(sqle.Row{int32(1), int32(2)})
	var mismatch *RowColumnCountMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected *RowColumnCountMismatchError, got %T (%v)", err, err)
	}
}

func TestRowCodec_Encode_ValueTypeMismatch(t *testing.T) {
	rc := singleColumnCodec(t, "INT", false)
	_, err := rc.Encode(sqle.Row{"not an int32"})
	var colErr *ColumnEncodeError
	if !errors.As(err, &colErr) {
		t.Fatalf("expected *ColumnEncodeError, got %T (%v)", err, err)
	}
	var mismatch *ValueTypeMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected wrapped *ValueTypeMismatchError, got %v", err)
	}
}

func TestRowCodec_Decode_MalformedInputs(t *testing.T) {
	rc := singleColumnCodec(t, "INT", false)
	valid, err := rc.Encode(sqle.Row{int32(5)})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	t.Run("too short", func(t *testing.T) {
		_, err := rc.Decode([]byte{1, 2})
		var truncated *TruncatedRowError
		if !errors.As(err, &truncated) {
			t.Fatalf("expected *TruncatedRowError, got %T (%v)", err, err)
		}
	})

	t.Run("bad magic", func(t *testing.T) {
		corrupt := append([]byte(nil), valid...)
		corrupt[0] = 'X'
		_, err := rc.Decode(corrupt)
		var magicErr *InvalidRowMagicError
		if !errors.As(err, &magicErr) {
			t.Fatalf("expected *InvalidRowMagicError, got %T (%v)", err, err)
		}
	})

	t.Run("bad version", func(t *testing.T) {
		corrupt := append([]byte(nil), valid...)
		corrupt[2] = 99
		_, err := rc.Decode(corrupt)
		var verErr *UnsupportedRowFormatError
		if !errors.As(err, &verErr) {
			t.Fatalf("expected *UnsupportedRowFormatError, got %T (%v)", err, err)
		}
	})

	t.Run("truncated value bytes", func(t *testing.T) {
		corrupt := valid[:len(valid)-1]
		_, err := rc.Decode(corrupt)
		var colErr *ColumnDecodeError
		if !errors.As(err, &colErr) {
			t.Fatalf("expected *ColumnDecodeError, got %T (%v)", err, err)
		}
	})

	t.Run("truncated bitmap", func(t *testing.T) {
		corrupt := valid[:7]
		_, err := rc.Decode(corrupt)
		var truncated *TruncatedRowError
		if !errors.As(err, &truncated) {
			t.Fatalf("expected *TruncatedRowError, got %T (%v)", err, err)
		}
	})
}

func TestRowCodec_Decode_JSONMalformed(t *testing.T) {
	rc := singleColumnCodec(t, "JSON", false)
	data, err := rc.Encode(sqle.Row{types.JSONDocument{Val: map[string]any{"a": float64(1)}}})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// Corrupt the JSON payload bytes (the last byte is inside the encoded
	// object literal for this small document).
	corrupt := append([]byte(nil), data...)
	corrupt[len(corrupt)-1] = '{'
	_, err = rc.Decode(corrupt)
	if err == nil {
		t.Fatal("expected decode error for corrupted JSON payload")
	}
}

// ---------------------------------------------------------------------------
// RowSchemaVersionAtLeast
// ---------------------------------------------------------------------------

func TestRowSchemaVersionAtLeast(t *testing.T) {
	def := &service.TableDef{
		Name:          "t",
		Columns:       []service.ColumnDef{{Name: "v", Type: "INT"}},
		PrimaryKey:    []service.PKColumn{{Name: "v"}},
		SchemaVersion: 3,
	}
	rc, err := NewRowCodec(def)
	if err != nil {
		t.Fatalf("NewRowCodec: %v", err)
	}
	data, err := rc.Encode(sqle.Row{int32(7)})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	atLeast, err := RowSchemaVersionAtLeast("t", data, 3)
	if err != nil {
		t.Fatalf("RowSchemaVersionAtLeast(3): %v", err)
	}
	if !atLeast {
		t.Fatal("expected true: row was encoded at exactly version 3")
	}

	atLeast, err = RowSchemaVersionAtLeast("t", data, 4)
	if err != nil {
		t.Fatalf("RowSchemaVersionAtLeast(4): %v", err)
	}
	if atLeast {
		t.Fatal("expected false: row was encoded below version 4")
	}

	atLeast, err = RowSchemaVersionAtLeast("t", data, 1)
	if err != nil {
		t.Fatalf("RowSchemaVersionAtLeast(1): %v", err)
	}
	if !atLeast {
		t.Fatal("expected true: row was encoded above version 1")
	}
}

func TestRowSchemaVersionAtLeast_Truncated(t *testing.T) {
	_, err := RowSchemaVersionAtLeast("t", []byte{'Q', 'R'}, 1)
	var e *TruncatedRowError
	if !errors.As(err, &e) {
		t.Fatalf("expected TruncatedRowError, got %T: %v", err, err)
	}
}

func TestRowSchemaVersionAtLeast_InvalidMagic(t *testing.T) {
	bad := make([]byte, rowHeaderLen)
	bad[0], bad[1] = 'X', 'X'
	_, err := RowSchemaVersionAtLeast("t", bad, 1)
	var e *InvalidRowMagicError
	if !errors.As(err, &e) {
		t.Fatalf("expected InvalidRowMagicError, got %T: %v", err, err)
	}
}

func TestRowSchemaVersionAtLeast_UnsupportedFormat(t *testing.T) {
	bad := make([]byte, rowHeaderLen)
	bad[0], bad[1] = rowMagic0, rowMagic1
	bad[2] = 99
	_, err := RowSchemaVersionAtLeast("t", bad, 1)
	var e *UnsupportedRowFormatError
	if !errors.As(err, &e) {
		t.Fatalf("expected UnsupportedRowFormatError, got %T: %v", err, err)
	}
}

func TestRowCodec_JSONRoundTrip(t *testing.T) {
	docs := []any{
		types.JSONDocument{Val: map[string]any{"a": float64(1), "b": "two"}},
		types.JSONDocument{Val: []any{float64(1), float64(2), float64(3)}},
		types.JSONDocument{Val: "a plain string"},
		types.JSONDocument{Val: true},
		types.JSONDocument{Val: nil},
	}
	for i, doc := range docs {
		ok := rowRoundTripEqual(t, "JSON", doc, func(a, b any) bool {
			aw := a.(types.JSONDocument)
			bw := b.(types.JSONDocument)
			av, _ := aw.ToInterface(context.Background())
			bv, _ := bw.ToInterface(context.Background())
			return reflect.DeepEqual(av, bv)
		})
		if !ok {
			t.Fatalf("doc %d did not round trip: %#v", i, doc)
		}
	}
}

// ---------------------------------------------------------------------------
// DECIMAL: the Go type contract with the engine
// ---------------------------------------------------------------------------

// TestRowCodec_DecimalAcceptsEngineType pins the codec to the Go type
// go-mysql-server actually represents a DECIMAL cell with. The two are tied
// together deliberately: DecimalType_.ValueType() is the engine's own
// statement of what a cell holds, so reading it here means an engine upgrade
// that changes the representation fails this test rather than failing every
// INSERT in production.
func TestRowCodec_DecimalAcceptsEngineType(t *testing.T) {
	col := service.ColumnDef{Name: "v", Type: "DECIMAL(20,4)"}
	engineType, err := ColumnTypeOf(col)
	if err != nil {
		t.Fatalf("ColumnTypeOf: %v", err)
	}
	if got := engineType.ValueType(); got != reflect.TypeOf(&apd.Decimal{}) {
		t.Fatalf("engine represents DECIMAL as %v; the row codec encodes *apd.Decimal", got)
	}

	// A cell the engine itself produced, converted exactly as an INSERT
	// would convert it, must encode.
	cell, _, err := engineType.Convert(context.Background(), "1234.5678")
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	rc := singleColumnCodec(t, "DECIMAL(20,4)", false)
	data, err := rc.Encode(sqle.Row{cell})
	if err != nil {
		t.Fatalf("Encode of an engine-produced DECIMAL cell failed: %v", err)
	}
	row, err := rc.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	got, ok := row[0].(*apd.Decimal)
	if !ok {
		t.Fatalf("Decode returned %T, want *apd.Decimal", row[0])
	}
	if got.Text('f') != "1234.5678" {
		t.Fatalf("Decode returned %q, want %q", got.Text('f'), "1234.5678")
	}
}

// TestRowCodec_DecimalRejectsForeignType asserts the codec refuses a decimal
// from any other library rather than coercing it. Silently accepting one
// would reintroduce exactly the "whatever the caller happened to hold" typing
// the format exists to eliminate.
func TestRowCodec_DecimalRejectsForeignType(t *testing.T) {
	rc := singleColumnCodec(t, "DECIMAL(20,4)", false)

	for _, v := range []any{"1234.5678", 1234.5678, apd.Decimal{}, (*apd.Decimal)(nil)} {
		_, err := rc.Encode(sqle.Row{v})
		var e *ValueTypeMismatchError
		if !errors.As(err, &e) {
			t.Fatalf("Encode(%T) error = %v, want ValueTypeMismatchError", v, err)
		}
		if e.Expected != "*apd.Decimal" {
			t.Fatalf("Encode(%T) expected type = %q, want %q", v, e.Expected, "*apd.Decimal")
		}
		if _, err := rc.codecs[0].size(v); err == nil {
			t.Fatalf("EncodedSize accepted %T that Encode rejected", v)
		}
	}
}

// TestRowCodec_DecimalStoredTextIsStable pins the bytes a DECIMAL occupies on
// disk. Rows outlive the process that wrote them, so the stored text is a
// compatibility surface: a value written by an earlier build must keep
// decoding, whatever Go type the encoder accepts today.
func TestRowCodec_DecimalStoredTextIsStable(t *testing.T) {
	rc := singleColumnCodec(t, "DECIMAL(35,2)", false)

	for _, text := range []string{"123.45", "-0.01", "0.00", "79228162514264337593543950335.99"} {
		want, _, err := apd.NewFromString(text)
		if err != nil {
			t.Fatalf("apd.NewFromString(%q): %v", text, err)
		}
		data, err := rc.Encode(sqle.Row{want})
		if err != nil {
			t.Fatalf("Encode(%q): %v", text, err)
		}
		// The value section begins after the header, the columnCount
		// uvarint and the one-byte null bitmap, and is itself a
		// uvarint-length-prefixed string.
		payload := string(data[rowHeaderLen+1+1+1:])
		if payload != text {
			t.Fatalf("stored text for %q = %q, want the decimal text verbatim", text, payload)
		}
	}
}

// TestRowCodec_DecimalDecodesLegacyPlainText asserts a row written before the
// encoder changed still decodes. Every previous writer emitted plain (never
// exponential) decimal text, so that is what a stored row may contain.
func TestRowCodec_DecimalDecodesLegacyPlainText(t *testing.T) {
	rc := singleColumnCodec(t, "DECIMAL(35,10)", false)

	for _, text := range []string{"0.0000000001", "-123.4500000000", "0"} {
		stored, _, err := apd.NewFromString(text)
		if err != nil {
			t.Fatalf("apd.NewFromString(%q): %v", text, err)
		}
		// Hand-assemble the row exactly as a previous build would have
		// written it: the plain text, never the canonical form.
		data := []byte{rowMagic0, rowMagic1, rowFormatVersion, 0, 0, 0, 0, 1, 0}
		data = binary.AppendUvarint(data, uint64(len(stored.Text('f'))))
		data = append(data, stored.Text('f')...)

		row, err := rc.Decode(data)
		if err != nil {
			t.Fatalf("Decode of legacy plain text %q: %v", text, err)
		}
		got := row[0].(*apd.Decimal)
		if got.Cmp(stored) != 0 {
			t.Fatalf("Decode(%q) = %v, want %v", text, got, stored)
		}
	}
}

func TestRowCodec_DecimalParseError(t *testing.T) {
	rc := singleColumnCodec(t, "DECIMAL(10,2)", false)

	data := []byte{rowMagic0, rowMagic1, rowFormatVersion, 0, 0, 0, 0, 1, 0}
	data = binary.AppendUvarint(data, uint64(len("not-a-number")))
	data = append(data, "not-a-number"...)

	_, err := rc.Decode(data)
	var e *DecimalParseError
	if !errors.As(err, &e) {
		t.Fatalf("Decode error = %v, want DecimalParseError", err)
	}
}

// ---------------------------------------------------------------------------
// BIGINT UNSIGNED: exactness across the int64 boundary
// ---------------------------------------------------------------------------

// TestRowCodec_Uint64AboveInt64Max asserts the row codec stores the upper half
// of BIGINT UNSIGNED exactly. The codec was never the narrowing point -- the
// embedded driver was -- so this pins the layer below it as correct and keeps
// a future "just use int64 everywhere" simplification from reaching storage.
func TestRowCodec_Uint64AboveInt64Max(t *testing.T) {
	values := []uint64{
		0,
		math.MaxInt64,
		math.MaxInt64 + 1,
		math.MaxUint64,
	}
	for _, want := range values {
		ok := rowRoundTripEqual(t, "BIGINT UNSIGNED", want, deepEqual)
		if !ok {
			t.Fatalf("uint64 %d did not round trip exactly", want)
		}
	}
}
