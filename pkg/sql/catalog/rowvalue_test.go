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

package catalog_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/cockroachdb/apd/v3"
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/catalog"
)

// enumType, setType and decimalType build the symbolic types every test below
// shares.
func enumType(t *testing.T) sqle.EnumType {
	t.Helper()
	et, err := types.CreateEnumType([]string{"pending", "active", "banned"}, sqle.Collation_Default)
	require.NoError(t, err)
	return et
}

func setType(t *testing.T) sqle.SetType {
	t.Helper()
	st, err := types.CreateSetType([]string{"read", "write", "admin"}, sqle.Collation_Default)
	require.NoError(t, err)
	return st
}

// decimalType builds the DECIMAL(20,4) column type used below.
// CreateColumnDecimalType, not CreateDecimalType, is what a real column
// carries, and the two format differently -- see resolveDecimal.
func decimalType(t *testing.T) sqle.DecimalType {
	t.Helper()
	dt, err := types.CreateColumnDecimalType(20, 4)
	require.NoError(t, err)
	return dt
}

// decimalValue parses text into the *apd.Decimal a DECIMAL cell holds.
func decimalValue(t *testing.T, text string) *apd.Decimal {
	t.Helper()
	d, _, err := apd.NewFromString(text)
	require.NoError(t, err)
	return d
}

// TestResolveColumnValue_Enum pins the defect this function exists to
// remove: go-mysql-server stores an ENUM cell as the uint16 ordinal of its
// label, so a caller that reads the cell without consulting the column type
// receives 2 where "active" was written.
func TestResolveColumnValue_Enum(t *testing.T) {
	et := enumType(t)

	tests := []struct {
		name    string
		ordinal uint16
		want    string
	}{
		{"first", 1, "pending"},
		{"middle", 2, "active"},
		{"last", 3, "banned"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := catalog.ResolveColumnValue(context.Background(), et, tc.ordinal)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestResolveColumnValue_Set covers the SET analogue: the cell is a uint64
// bitmask, and the value is the comma-joined labels of the set bits, in
// declaration order.
func TestResolveColumnValue_Set(t *testing.T) {
	st := setType(t)

	tests := []struct {
		name string
		bits uint64
		want string
	}{
		{"empty", 0b000, ""},
		{"single", 0b001, "read"},
		{"disjoint pair", 0b101, "read,admin"},
		{"all", 0b111, "read,write,admin"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := catalog.ResolveColumnValue(context.Background(), st, tc.bits)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestResolveColumnValue_NonSymbolicPassThrough asserts that every type
// whose engine representation is already the value is returned untouched, so
// that adding this resolution to a result path changes nothing but ENUM and
// SET.
func TestResolveColumnValue_NonSymbolicPassThrough(t *testing.T) {
	ts := time.Date(2020, 3, 4, 5, 6, 7, 0, time.UTC)

	tests := []struct {
		name string
		typ  sqle.Type
		val  any
	}{
		{"int32", types.Int32, int32(42)},
		{"uint64", types.Uint64, uint64(1) << 63},
		{"float64", types.Float64, 1.5},
		{"varchar", types.Text, "hello"},
		{"blob", types.Blob, []byte("bytes")},
		{"datetime", types.Datetime, ts},
		{"json", types.JSON, types.JSONDocument{Val: map[string]any{"a": 1.0}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := catalog.ResolveColumnValue(context.Background(), tc.typ, tc.val)
			require.NoError(t, err)
			assert.Equal(t, tc.val, got)
		})
	}
}

// TestResolveColumnValue_Nil covers the two inputs that carry no value to
// resolve. A NULL ENUM cell must stay NULL rather than becoming the label of
// ordinal zero.
func TestResolveColumnValue_Nil(t *testing.T) {
	t.Run("nil value with enum type", func(t *testing.T) {
		got, err := catalog.ResolveColumnValue(context.Background(), enumType(t), nil)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("nil type", func(t *testing.T) {
		got, err := catalog.ResolveColumnValue(context.Background(), nil, uint16(2))
		require.NoError(t, err)
		assert.Equal(t, uint16(2), got)
	})
}

// TestResolveColumnValue_EnumOrdinalOutOfRange asserts the resolver fails
// closed. An ordinal with no label -- a row written against a wider ENUM
// definition than the one now in effect -- must surface as a typed error
// rather than silently degrade back to the integer this function exists to
// eliminate.
func TestResolveColumnValue_EnumOrdinalOutOfRange(t *testing.T) {
	_, err := catalog.ResolveColumnValue(context.Background(), enumType(t), uint16(99))
	require.Error(t, err)

	var typed *catalog.SymbolicValueError
	assert.ErrorAs(t, err, &typed)
	assert.Contains(t, err.Error(), "99")
}

// TestResolveColumnValue_SetBitsOutOfRange is the SET analogue: a bitmask
// wider than the declared member list has no rendering and must be rejected.
func TestResolveColumnValue_SetBitsOutOfRange(t *testing.T) {
	_, err := catalog.ResolveColumnValue(context.Background(), setType(t), uint64(0b1000))
	require.Error(t, err)

	var typed *catalog.SymbolicValueError
	assert.ErrorAs(t, err, &typed)
}

// TestResolveColumnValue_EnumFromLabel covers a cell that already holds a
// label -- what a projected literal produces -- which go-mysql-server's own
// conversion accepts and which must resolve to itself, not to an error.
func TestResolveColumnValue_EnumFromLabel(t *testing.T) {
	got, err := catalog.ResolveColumnValue(context.Background(), enumType(t), "banned")
	require.NoError(t, err)
	assert.Equal(t, "banned", got)
}

// TestResolveColumnValue_EnumUnconvertible asserts a cell that is neither an
// ordinal nor a known label is rejected with a typed error.
func TestResolveColumnValue_EnumUnconvertible(t *testing.T) {
	_, err := catalog.ResolveColumnValue(context.Background(), enumType(t), "not-a-member")
	require.Error(t, err)

	var typed *catalog.SymbolicValueError
	assert.ErrorAs(t, err, &typed)
}

// TestResolveColumnValue_SetUnconvertible is the SET analogue of the ENUM
// rejection above.
func TestResolveColumnValue_SetUnconvertible(t *testing.T) {
	_, err := catalog.ResolveColumnValue(context.Background(), setType(t), "not-a-member")
	require.Error(t, err)

	var typed *catalog.SymbolicValueError
	assert.ErrorAs(t, err, &typed)
}

// baseType exists so symbolicImpostor can embed exactly sql.Type's method
// set under a field name that does not collide with sql.Type's own Type()
// method.
type baseType interface{ sqle.Type }

// symbolicImpostor claims to be an ENUM or SET through its query.Type
// discriminator -- which it forwards from a genuine symbolic type -- while
// exposing none of sql.EnumType's or sql.SetType's value-list surface, which
// is the only way the resolver can be handed a type it cannot resolve.
type symbolicImpostor struct {
	baseType
}

func (symbolicImpostor) String() string { return "impostor" }

// TestResolveColumnValue_TypeMismatch asserts that a column claiming to be
// symbolic without carrying its value list is rejected rather than panicking
// on the type assertion.
func TestResolveColumnValue_TypeMismatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  sqle.Type
	}{
		{"enum", symbolicImpostor{types.MustCreateEnumType([]string{"a"}, sqle.Collation_Default)}},
		{"set", symbolicImpostor{types.MustCreateSetType([]string{"a"}, sqle.Collation_Default)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := catalog.ResolveColumnValue(context.Background(), tc.typ, uint16(1))
			require.Error(t, err)

			var typed *catalog.SymbolicTypeMismatchError
			assert.ErrorAs(t, err, &typed)
		})
	}
}

// miscastEnumType converts to something that is not an enum ordinal, which a
// well-behaved sql.EnumType never does. It exists to prove the resolver
// reports that violation instead of panicking on the type assertion.
type miscastEnumType struct {
	sqle.EnumType
}

func (miscastEnumType) Convert(context.Context, any) (any, sqle.ConvertInRange, error) {
	return "not-an-ordinal", sqle.InRange, nil
}

// miscastSetType is the SET analogue of miscastEnumType.
type miscastSetType struct {
	sqle.SetType
}

func (miscastSetType) Convert(context.Context, any) (any, sqle.ConvertInRange, error) {
	return "not-a-bitmask", sqle.InRange, nil
}

// TestResolveColumnValue_MiscastConversion asserts a symbolic type whose
// conversion yields something other than its documented representation is
// reported, not assumed.
func TestResolveColumnValue_MiscastConversion(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  sqle.Type
	}{
		{"enum", miscastEnumType{enumType(t)}},
		{"set", miscastSetType{setType(t)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := catalog.ResolveColumnValue(context.Background(), tc.typ, uint16(1))
			require.Error(t, err)

			var typed *catalog.SymbolicValueError
			assert.ErrorAs(t, err, &typed)
			assert.Contains(t, err.Error(), "string")
		})
	}
}

// failingSetType accepts a bitmask through the embedded type's conversion
// and then refuses to render it, covering the rendering failure that a
// well-formed SetType cannot reach because its own conversion validates the
// bitmask first.
type failingSetType struct {
	sqle.SetType
}

func (failingSetType) BitsToString(uint64) (string, error) {
	return "", &catalog.EmptyColumnsError{Table: "render"}
}

// TestResolveColumnValue_SetRenderFailure asserts a rendering failure is
// reported as a typed resolution error rather than yielding an empty label
// list, which is indistinguishable from a legitimately empty SET.
func TestResolveColumnValue_SetRenderFailure(t *testing.T) {
	_, err := catalog.ResolveColumnValue(context.Background(), failingSetType{setType(t)}, uint64(0b001))
	require.Error(t, err)

	var typed *catalog.SymbolicValueError
	assert.ErrorAs(t, err, &typed)
}

// TestResolveColumnValues_Row resolves a whole row, mixing symbolic and
// plain columns plus a NULL.
func TestResolveColumnValues_Row(t *testing.T) {
	schema := sqle.Schema{
		{Name: "id", Type: types.Int32},
		{Name: "status", Type: enumType(t)},
		{Name: "perms", Type: setType(t)},
		{Name: "note", Type: enumType(t)},
	}

	got, err := catalog.ResolveColumnValues(context.Background(), schema,
		sqle.Row{int32(7), uint16(3), uint64(0b011), nil})
	require.NoError(t, err)
	assert.Equal(t, sqle.Row{int32(7), "banned", "read,write", nil}, got)
}

// TestResolveColumnValues_ShortSchema covers the row shapes that carry no
// schema entry -- notably the single-cell OkResult row DML produces -- which
// must pass through rather than be resolved against a column that does not
// describe them.
func TestResolveColumnValues_ShortSchema(t *testing.T) {
	t.Run("nil schema", func(t *testing.T) {
		got, err := catalog.ResolveColumnValues(context.Background(), nil, sqle.Row{uint16(2)})
		require.NoError(t, err)
		assert.Equal(t, sqle.Row{uint16(2)}, got)
	})

	t.Run("schema shorter than row", func(t *testing.T) {
		schema := sqle.Schema{{Name: "status", Type: enumType(t)}}
		got, err := catalog.ResolveColumnValues(context.Background(), schema, sqle.Row{uint16(2), uint16(3)})
		require.NoError(t, err)
		assert.Equal(t, sqle.Row{"active", uint16(3)}, got)
	})

	t.Run("nil column", func(t *testing.T) {
		got, err := catalog.ResolveColumnValues(context.Background(), sqle.Schema{nil}, sqle.Row{uint16(2)})
		require.NoError(t, err)
		assert.Equal(t, sqle.Row{uint16(2)}, got)
	})
}

// TestResolveColumnValues_Error propagates a per-cell failure rather than
// returning a partially-resolved row.
func TestResolveColumnValues_Error(t *testing.T) {
	schema := sqle.Schema{{Name: "status", Type: enumType(t)}}
	got, err := catalog.ResolveColumnValues(context.Background(), schema, sqle.Row{uint16(99)})
	require.Error(t, err)
	assert.Nil(t, got)
}

// TestResolvedValueType asserts the advertised scan type matches what
// ResolveColumnValue actually produces. An ENUM column's sql.Type reports
// uint16 -- the engine's storage type -- which no resolved row can satisfy.
func TestResolvedValueType(t *testing.T) {
	et := enumType(t)
	st := setType(t)

	dt := decimalType(t)

	assert.Equal(t, reflect.TypeOf(uint16(0)), et.ValueType(), "guards the premise: GMS reports the storage type")
	assert.Equal(t, reflect.TypeOf(&apd.Decimal{}), dt.ValueType(), "guards the premise: GMS reports the storage type")
	assert.Equal(t, reflect.TypeOf(""), catalog.ResolvedValueType(et))
	assert.Equal(t, reflect.TypeOf(""), catalog.ResolvedValueType(st))
	assert.Equal(t, reflect.TypeOf(""), catalog.ResolvedValueType(dt))

	assert.Equal(t, types.Int32.ValueType(), catalog.ResolvedValueType(types.Int32))
	assert.Equal(t, types.Text.ValueType(), catalog.ResolvedValueType(types.Text))
	assert.Nil(t, catalog.ResolvedValueType(nil))
}

// TestResolvedValueType_MatchesResolvedValue closes the loop between the two
// exported functions: whatever ResolveColumnValue returns must be assignable
// to the type ResolvedValueType advertises, for every symbolic type.
func TestResolvedValueType_MatchesResolvedValue(t *testing.T) {
	cases := []struct {
		name string
		typ  sqle.Type
		cell any
	}{
		{"enum", enumType(t), uint16(2)},
		{"set", setType(t), uint64(0b101)},
		{"decimal", decimalType(t), decimalValue(t, "12.3400")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := catalog.ResolveColumnValue(context.Background(), tc.typ, tc.cell)
			require.NoError(t, err)
			assert.Equal(t, catalog.ResolvedValueType(tc.typ), reflect.TypeOf(got))
		})
	}
}

// ---------------------------------------------------------------------------
// DECIMAL resolution
// ---------------------------------------------------------------------------

// TestResolveColumnValue_Decimal pins the rendering a DECIMAL cell resolves
// to. The engine holds the cell as an *apd.Decimal, which is not a
// database/sql/driver.Value and cannot be scanned into a string, so every
// transport needs the text -- and needs the same text.
func TestResolveColumnValue_Decimal(t *testing.T) {
	dt := decimalType(t)

	cases := []struct {
		name string
		cell string
		want string
	}{
		{"fractional", "1234.5678", "1234.5678"},
		{"negative", "-0.0001", "-0.0001"},
		{"zero keeps scale", "0.0000", "0.0000"},
		{"beyond float64 mantissa", "9999999999999999.9999", "9999999999999999.9999"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := catalog.ResolveColumnValue(context.Background(), dt, decimalValue(t, tc.cell))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestResolveColumnValue_DecimalAppliesColumnScale is the reason the
// rendering is delegated to the column type instead of being formatted from
// the value: a cell whose exponent does not match the column's declared scale
// is printed at the column's scale on the MySQL wire, and formatting the
// value alone would print its own scale instead.
func TestResolveColumnValue_DecimalAppliesColumnScale(t *testing.T) {
	dt, err := types.CreateColumnDecimalType(21, 5)
	require.NoError(t, err)

	cell := decimalValue(t, "1.61725000")
	require.Equal(t, int32(-8), cell.Exponent, "guards the premise: the cell carries its own scale")

	got, err := catalog.ResolveColumnValue(context.Background(), dt, cell)
	require.NoError(t, err)
	assert.Equal(t, "1.61725", got, "the column's scale wins, as it does on the wire")
	assert.NotEqual(t, cell.Text('f'), got, "formatting the value alone would diverge")
}

// TestResolveColumnValue_DecimalWrongGoType asserts a cell that is not a
// decimal is reported rather than rendered through fmt. The whole point of
// resolving against the declared type is that a cell of the wrong Go type is
// a bug to surface, not a value to guess at.
func TestResolveColumnValue_DecimalWrongGoType(t *testing.T) {
	dt := decimalType(t)

	for _, cell := range []any{"1.23", 1.23, int64(1), (*apd.Decimal)(nil)} {
		_, err := catalog.ResolveColumnValue(context.Background(), dt, cell)
		require.Error(t, err, "cell %T", cell)
		assert.Contains(t, err.Error(), "decimal")
	}
}

// TestResolveColumnValue_DecimalNil keeps NULL distinct from zero: a NULL
// DECIMAL must stay nil rather than resolving to "0.0000".
func TestResolveColumnValue_DecimalNil(t *testing.T) {
	got, err := catalog.ResolveColumnValue(context.Background(), decimalType(t), nil)
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestResolveColumnValue_DecimalForeignType covers the fallback for a DECIMAL
// column whose type is not go-mysql-server's own: there is no
// DecimalValueStringFixed to delegate to, so the value is rendered as plain
// fixed-point text -- which is what that function itself does for a type that
// describes no column. The cell is still resolved rather than passed through,
// because *apd.Decimal is not a value any caller can receive.
func TestResolveColumnValue_DecimalForeignType(t *testing.T) {
	typ := symbolicImpostor{decimalType(t)}

	got, err := catalog.ResolveColumnValue(context.Background(), typ, decimalValue(t, "1234.5678"))
	require.NoError(t, err)
	assert.Equal(t, "1234.5678", got)
	assert.Equal(t, reflect.TypeOf(""), catalog.ResolvedValueType(typ))
}
