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
	"database/sql"
	sqld "database/sql/driver"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/dolthub/vitess/go/vt/sqlparser"
)

// TestNamedValuesToBindings_KeyNaming verifies bindings are keyed v1..vN,
// which is the naming go-mysql-server gives positional placeholders. A
// mismatch here binds nothing and the engine sees an unfilled placeholder.
func TestNamedValuesToBindings_KeyNaming(t *testing.T) {
	args := []sqld.NamedValue{
		{Ordinal: 1, Value: int64(10)},
		{Ordinal: 2, Value: "x"},
		{Ordinal: 3, Value: nil},
	}

	bindings, err := namedValuesToBindings(args, time.UTC)
	if err != nil {
		t.Fatalf("namedValuesToBindings: %v", err)
	}

	for _, want := range []string{"v1", "v2", "v3"} {
		if _, ok := bindings[want]; !ok {
			t.Errorf("missing binding %q; got keys %v", want, bindingKeys(bindings))
		}
	}
	if len(bindings) != 3 {
		t.Errorf("len(bindings) = %d, want 3", len(bindings))
	}
}

// TestNamedValuesToBindings_Empty verifies no bindings map is allocated when
// there are no arguments.
func TestNamedValuesToBindings_Empty(t *testing.T) {
	bindings, err := namedValuesToBindings(nil, time.UTC)
	if err != nil {
		t.Fatalf("namedValuesToBindings: %v", err)
	}
	if bindings != nil {
		t.Errorf("bindings = %v, want nil", bindings)
	}
}

// TestNamedValuesToBindings_RejectsNamed verifies a named parameter is
// reported rather than silently bound to the wrong position.
func TestNamedValuesToBindings_RejectsNamed(t *testing.T) {
	args := []sqld.NamedValue{{Name: "tenant", Ordinal: 1, Value: "acme"}}

	_, err := namedValuesToBindings(args, time.UTC)

	var named *NamedParameterUnsupportedError
	if !errors.As(err, &named) {
		t.Fatalf("error = %v, want *NamedParameterUnsupportedError", err)
	}
	if named.Name != "tenant" {
		t.Errorf("Name = %q, want tenant", named.Name)
	}
}

// TestBindArg_Types verifies each supported Go type binds to the expected
// vitess expression kind and text.
func TestBindArg_Types(t *testing.T) {
	tests := []struct {
		name     string
		value    sqld.Value
		wantType sqlparser.ValType
		wantVal  string
		wantNull bool
	}{
		{name: "nil", value: nil, wantNull: true},
		{name: "true", value: true, wantType: sqlparser.IntVal, wantVal: "1"},
		{name: "false", value: false, wantType: sqlparser.IntVal, wantVal: "0"},
		{name: "int64", value: int64(-42), wantType: sqlparser.IntVal, wantVal: "-42"},
		{name: "int64 max", value: int64(math.MaxInt64), wantType: sqlparser.IntVal, wantVal: "9223372036854775807"},
		{name: "float64", value: 1.5, wantType: sqlparser.FloatVal, wantVal: "1.5"},
		{name: "string", value: "hello", wantType: sqlparser.StrVal, wantVal: "hello"},
		{
			// A quote in a string argument must survive as data. Under the
			// previous interpolation this was the escaping hot spot; as a
			// bound value there is nothing to escape.
			name: "string with quote", value: "O'Brien", wantType: sqlparser.StrVal, wantVal: "O'Brien",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, err := bindArg(1, tt.value, time.UTC)
			if err != nil {
				t.Fatalf("bindArg: %v", err)
			}

			if tt.wantNull {
				if _, ok := expr.(*sqlparser.NullVal); !ok {
					t.Fatalf("expr = %T, want *sqlparser.NullVal", expr)
				}
				return
			}

			val, ok := expr.(*sqlparser.SQLVal)
			if !ok {
				t.Fatalf("expr = %T, want *sqlparser.SQLVal", expr)
			}
			if val.Type != tt.wantType {
				t.Errorf("Type = %v, want %v", val.Type, tt.wantType)
			}
			if string(val.Val) != tt.wantVal {
				t.Errorf("Val = %q, want %q", val.Val, tt.wantVal)
			}
		})
	}
}

// TestBindArg_UnsignedBeyondInt64 verifies a uint64 above math.MaxInt64 keeps
// its value. Routing it through int64 — which database/sql's default
// conversion does — would wrap it negative.
func TestBindArg_UnsignedBeyondInt64(t *testing.T) {
	expr, err := bindArg(1, uint64(math.MaxUint64), time.UTC)
	if err != nil {
		t.Fatalf("bindArg: %v", err)
	}

	val := expr.(*sqlparser.SQLVal)
	if got := string(val.Val); got != "18446744073709551615" {
		t.Errorf("Val = %q, want 18446744073709551615", got)
	}
}

// TestBindArg_BytesAreHex verifies []byte binds as a hex literal so the engine
// types it as binary. Bound as a string it would be reinterpreted under the
// connection charset and any non-UTF-8 sequence would be corrupted.
func TestBindArg_BytesAreHex(t *testing.T) {
	expr, err := bindArg(1, []byte{0x00, 0xFF, 0x41}, time.UTC)
	if err != nil {
		t.Fatalf("bindArg: %v", err)
	}

	val, ok := expr.(*sqlparser.SQLVal)
	if !ok {
		t.Fatalf("expr = %T, want *sqlparser.SQLVal", expr)
	}
	if val.Type != sqlparser.HexVal {
		t.Errorf("Type = %v, want HexVal", val.Type)
	}
	if got := strings.ToLower(string(val.Val)); got != "00ff41" {
		t.Errorf("Val = %q, want 00ff41", got)
	}
}

// TestBindArg_TimeUsesLocation verifies a time argument is rendered in the
// configured location with microsecond precision.
func TestBindArg_TimeUsesLocation(t *testing.T) {
	ts := time.Date(2026, 8, 12, 15, 4, 5, 123456000, time.UTC)

	expr, err := bindArg(1, ts, time.UTC)
	if err != nil {
		t.Fatalf("bindArg: %v", err)
	}
	val := expr.(*sqlparser.SQLVal)
	if got := string(val.Val); got != "2026-08-12 15:04:05.123456" {
		t.Errorf("Val = %q, want 2026-08-12 15:04:05.123456", got)
	}

	// A different location must shift the rendered instant.
	east := time.FixedZone("east", 3600)
	expr, err = bindArg(1, ts, east)
	if err != nil {
		t.Fatalf("bindArg: %v", err)
	}
	val = expr.(*sqlparser.SQLVal)
	if got := string(val.Val); got != "2026-08-12 16:04:05.123456" {
		t.Errorf("Val = %q, want 2026-08-12 16:04:05.123456", got)
	}
}

// TestBindArg_UnsupportedType verifies an unbindable value is reported rather
// than stringified into something that would silently be the wrong value.
func TestBindArg_UnsupportedType(t *testing.T) {
	_, err := bindArg(3, map[string]string{"a": "b"}, time.UTC)

	var unsupported *UnsupportedArgumentTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %v, want *UnsupportedArgumentTypeError", err)
	}
	if unsupported.Ordinal != 3 {
		t.Errorf("Ordinal = %d, want 3", unsupported.Ordinal)
	}
}

// TestNormalizeArgValue_Widths verifies the narrower numeric types are widened
// to the forms bindArg handles.
func TestNormalizeArgValue_Widths(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  any
	}{
		{"int", int(7), int64(7)},
		{"int8", int8(-8), int64(-8)},
		{"int16", int16(-16), int64(-16)},
		{"int32", int32(-32), int64(-32)},
		{"uint", uint(7), uint64(7)},
		{"uint8", uint8(8), uint64(8)},
		{"uint16", uint16(16), uint64(16)},
		{"uint32", uint32(32), uint64(32)},
		{"float32", float32(0.5), float64(0.5)},
		{"int64 passthrough", int64(9), int64(9)},
		{"string passthrough", "s", "s"},
		{"nil passthrough", nil, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeArgValue(1, tt.value)
			if err != nil {
				t.Fatalf("normalizeArgValue: %v", err)
			}
			if got != tt.want {
				t.Errorf("normalizeArgValue(%v) = %#v, want %#v", tt.value, got, tt.want)
			}
		})
	}
}

// TestNormalizeArgValue_UnwrapsValuer verifies driver.Valuer implementations
// are unwrapped before type analysis, so sql.NullX and similar wrappers bind
// as their underlying value.
func TestNormalizeArgValue_UnwrapsValuer(t *testing.T) {
	got, err := normalizeArgValue(1, sql.NullString{String: "hi", Valid: true})
	if err != nil {
		t.Fatalf("normalizeArgValue: %v", err)
	}
	if got != "hi" {
		t.Errorf("value = %#v, want \"hi\"", got)
	}

	got, err = normalizeArgValue(1, sql.NullInt64{Valid: false})
	if err != nil {
		t.Fatalf("normalizeArgValue: %v", err)
	}
	if got != nil {
		t.Errorf("invalid NullInt64 = %#v, want nil", got)
	}
}

// TestNormalizeArgValue_Unsupported verifies an unconvertible type is reported.
func TestNormalizeArgValue_Unsupported(t *testing.T) {
	_, err := normalizeArgValue(1, struct{ A int }{})

	var unsupported *UnsupportedArgumentTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %v, want *UnsupportedArgumentTypeError", err)
	}
}

// ---------------------------------------------------------------------------
// Defined (named) argument types
// ---------------------------------------------------------------------------

// The idiom every ORM uses for a typed column: a type defined from a type the
// driver already binds. GORM's own test models declare exactly these
// (scanner_valuer_test.go's `type Num int64`, query_test.go's `type ID uint`).
type (
	definedInt64   int64
	definedInt     int
	definedUint    uint
	definedUint64  uint64
	definedFloat32 float32
	definedString  string
	definedBool    bool
	definedBytes   []byte
	definedByte    byte
	definedByteSl  []definedByte
	definedTime    time.Time
	definedStruct  struct{ A int }
	definedMap     map[string]string
)

// TestNormalizeArgValue_DefinedTypes verifies a defined type binds as the type
// it was defined from.
//
// This is a regression guard with a specific history: classification used to be
// a concrete type switch, which no defined type can match however trivially
// convertible it is. Passing `type Num int64` as an argument was rejected with
// *UnsupportedArgumentTypeError before any SQL ran, which failed GORM's
// TestScannerValuer, TestScannerValuerWithFirstOrCreate, TestInvalidValuer and
// TestCustomizedTypePrimaryKey.
func TestNormalizeArgValue_DefinedTypes(t *testing.T) {
	ts := time.Date(2026, 8, 13, 1, 2, 3, 0, time.UTC)

	tests := []struct {
		name  string
		value any
		want  any
	}{
		{"defined int64", definedInt64(18), int64(18)},
		{"defined int", definedInt(-7), int64(-7)},
		{"defined uint", definedUint(7), uint64(7)},
		{"defined uint64 beyond int64", definedUint64(math.MaxUint64), uint64(math.MaxUint64)},
		{"defined float32", definedFloat32(0.5), float64(0.5)},
		{"defined string", definedString("s"), "s"},
		{"defined bool", definedBool(true), true},
		{"defined time.Time", definedTime(ts), ts},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeArgValue(1, tt.value)
			if err != nil {
				t.Fatalf("normalizeArgValue(%T): %v", tt.value, err)
			}
			if got != tt.want {
				t.Errorf("normalizeArgValue(%T) = %#v (%T), want %#v", tt.value, got, got, tt.want)
			}
		})
	}
}

// TestNormalizeArgValue_DefinedByteSlices verifies a defined byte slice — and a
// slice of a defined byte type — binds as []byte, keeping the hex-literal
// binding that protects non-UTF-8 payloads.
func TestNormalizeArgValue_DefinedByteSlices(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value any
	}{
		{"defined []byte", definedBytes{0x00, 0xFF, 0x41}},
		{"slice of defined byte", definedByteSl{0x00, 0xFF, 0x41}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeArgValue(1, tt.value)
			if err != nil {
				t.Fatalf("normalizeArgValue(%T): %v", tt.value, err)
			}
			b, ok := got.([]byte)
			if !ok {
				t.Fatalf("value = %#v (%T), want []byte", got, got)
			}
			if string(b) != "\x00\xffA" {
				t.Errorf("value = %q, want %q", b, "\x00\xffA")
			}
		})
	}
}

// TestNormalizeArgValue_DefinedTypesAreValidDriverValues verifies the widened
// classification still produces values database/sql itself recognizes, so
// nothing downstream of the checker has to special-case them.
func TestNormalizeArgValue_DefinedTypesAreValidDriverValues(t *testing.T) {
	for _, v := range []any{
		definedInt64(1), definedUint(2), definedFloat32(3), definedString("4"),
		definedBool(true), definedBytes("5"), definedTime(time.Now()),
	} {
		got, err := normalizeArgValue(1, v)
		if err != nil {
			t.Fatalf("normalizeArgValue(%T): %v", v, err)
		}
		// uint64 is admitted by this driver but not by driver.IsValue; every
		// other normalized form must satisfy the standard contract.
		if _, isUint := got.(uint64); isUint {
			continue
		}
		if !sqld.IsValue(got) {
			t.Errorf("normalizeArgValue(%T) = %#v, which is not a driver.Value", v, got)
		}
	}
}

// TestNormalizeArgValue_KindIsNotEnough verifies kind-based classification
// still refuses a value whose kind does not identify it. Widening the accepted
// set must not become a catch-all: an unrecognized argument is reported, never
// stringified into something that would silently be the wrong value.
func TestNormalizeArgValue_KindIsNotEnough(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{"struct that is not a time", definedStruct{A: 1}},
		{"struct embedding time.Time", struct{ time.Time }{}},
		{"slice of non-bytes", []int{1, 2, 3}},
		{"map", definedMap{"a": "b"}},
		{"channel", make(chan int)},
		{"func", func() {}},
		{"complex", complex(1, 2)},
		{"array of bytes", [3]byte{1, 2, 3}},
		{"uintptr", uintptr(1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := normalizeArgValue(4, tt.value)

			var unsupported *UnsupportedArgumentTypeError
			if !errors.As(err, &unsupported) {
				t.Fatalf("error = %v, want *UnsupportedArgumentTypeError", err)
			}
			if unsupported.Ordinal != 4 {
				t.Errorf("Ordinal = %d, want 4", unsupported.Ordinal)
			}
		})
	}
}

// TestNormalizeArgValue_ReportsOrdinal verifies the reported ordinal is the
// argument's own position. It used to be reported as 0 for every argument,
// which pointed at the wrong parameter in a multi-argument statement.
func TestNormalizeArgValue_ReportsOrdinal(t *testing.T) {
	_, err := normalizeArgValue(3, definedStruct{})

	var unsupported *UnsupportedArgumentTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %v, want *UnsupportedArgumentTypeError", err)
	}
	if unsupported.Ordinal != 3 {
		t.Errorf("Ordinal = %d, want 3", unsupported.Ordinal)
	}
	if !strings.Contains(err.Error(), "argument 3") {
		t.Errorf("Error() = %q, want it to name argument 3", err.Error())
	}
}

// TestCheckNamedValue_ReportsOrdinal verifies the ordinal survives the driver's
// real entry point, which is the only place it is known.
func TestCheckNamedValue_ReportsOrdinal(t *testing.T) {
	c := &embeddedConn{}
	nv := &sqld.NamedValue{Ordinal: 2, Value: definedStruct{}}

	err := c.CheckNamedValue(nv)

	var unsupported *UnsupportedArgumentTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %v, want *UnsupportedArgumentTypeError", err)
	}
	if unsupported.Ordinal != 2 {
		t.Errorf("Ordinal = %d, want 2", unsupported.Ordinal)
	}
}

// TestCheckNamedValue_DefinedType verifies a defined type is accepted through
// the driver's real entry point, which is where GORM's failure surfaced.
func TestCheckNamedValue_DefinedType(t *testing.T) {
	c := &embeddedConn{}
	nv := &sqld.NamedValue{Ordinal: 1, Value: definedInt64(18)}

	if err := c.CheckNamedValue(nv); err != nil {
		t.Fatalf("CheckNamedValue: %v", err)
	}
	if nv.Value != int64(18) {
		t.Errorf("Value = %#v (%T), want int64(18)", nv.Value, nv.Value)
	}
}

// TestNormalizeArgValue_ValuerReturningDefinedType verifies a driver.Valuer
// that returns a defined type is honoured. Value() is declared to return a
// driver.Value, but implementations routinely return a domain type, and the
// unwrapped result gets the same classification as a direct argument.
func TestNormalizeArgValue_ValuerReturningDefinedType(t *testing.T) {
	got, err := normalizeArgValue(1, definedTypeValuer{})
	if err != nil {
		t.Fatalf("normalizeArgValue: %v", err)
	}
	if got != int64(99) {
		t.Errorf("value = %#v (%T), want int64(99)", got, got)
	}
}

// definedTypeValuer is a driver.Valuer whose Value returns a defined type
// rather than a canonical driver.Value.
type definedTypeValuer struct{}

func (definedTypeValuer) Value() (sqld.Value, error) { return definedInt64(99), nil }

// TestNormalizeArgValue_DereferencesInterfaces verifies a pointer to an
// interface is unwrapped to the dynamic value it holds. database/sql's own
// converter unwraps interfaces, and replacing it means reproducing that.
func TestNormalizeArgValue_DereferencesInterfaces(t *testing.T) {
	var boxed any = definedInt64(5)

	got, err := normalizeArgValue(1, &boxed)
	if err != nil {
		t.Fatalf("normalizeArgValue: %v", err)
	}
	if got != int64(5) {
		t.Errorf("value = %#v (%T), want int64(5)", got, got)
	}

	var empty any
	got, err = normalizeArgValue(1, &empty)
	if err != nil {
		t.Fatalf("normalizeArgValue: %v", err)
	}
	if got != nil {
		t.Errorf("value = %#v, want nil (SQL NULL)", got)
	}
}

// TestNormalizeArgValue_DefinedTypePointers verifies a pointer to a defined
// type is dereferenced and a nil one becomes SQL NULL, which is how an ORM
// passes a nullable column of a domain type.
func TestNormalizeArgValue_DefinedTypePointers(t *testing.T) {
	n := definedInt64(42)

	got, err := normalizeArgValue(1, &n)
	if err != nil {
		t.Fatalf("normalizeArgValue: %v", err)
	}
	if got != int64(42) {
		t.Errorf("value = %#v (%T), want int64(42)", got, got)
	}

	var nilPtr *definedInt64
	got, err = normalizeArgValue(1, nilPtr)
	if err != nil {
		t.Fatalf("normalizeArgValue: %v", err)
	}
	if got != nil {
		t.Errorf("value = %#v, want nil (SQL NULL)", got)
	}
}

// TestBindArg_DefinedTypesBindLikeTheirUnderlying verifies the normalized form
// of a defined type reaches the AST as the same expression its underlying type
// does — in particular that a defined byte slice keeps the hex-literal binding
// and a defined uint64 above math.MaxInt64 keeps its decimal text.
func TestBindArg_DefinedTypesBindLikeTheirUnderlying(t *testing.T) {
	tests := []struct {
		name     string
		value    any
		wantType sqlparser.ValType
		wantVal  string
	}{
		{"defined int64", definedInt64(-42), sqlparser.IntVal, "-42"},
		{"defined uint64 beyond int64", definedUint64(math.MaxUint64), sqlparser.IntVal, "18446744073709551615"},
		{"defined string", definedString("O'Brien"), sqlparser.StrVal, "O'Brien"},
		{"defined bytes", definedBytes{0x00, 0xFF, 0x41}, sqlparser.HexVal, "00ff41"},
		{"defined bool", definedBool(true), sqlparser.IntVal, "1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized, err := normalizeArgValue(1, tt.value)
			if err != nil {
				t.Fatalf("normalizeArgValue: %v", err)
			}
			expr, err := bindArg(1, normalized, time.UTC)
			if err != nil {
				t.Fatalf("bindArg: %v", err)
			}

			val, ok := expr.(*sqlparser.SQLVal)
			if !ok {
				t.Fatalf("expr = %T, want *sqlparser.SQLVal", expr)
			}
			if val.Type != tt.wantType {
				t.Errorf("Type = %v, want %v", val.Type, tt.wantType)
			}
			got := string(val.Val)
			if val.Type == sqlparser.HexVal {
				got = strings.ToLower(got)
			}
			if got != tt.wantVal {
				t.Errorf("Val = %q, want %q", got, tt.wantVal)
			}
		})
	}
}

// bindingKeys returns the keys of a bindings map, for failure messages.
func bindingKeys(m map[string]sqlparser.Expr) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestNormalizeArgValue_DereferencesPointers verifies pointer arguments are
// dereferenced and nil pointers become SQL NULL.
//
// This is a regression guard with a specific history: implementing
// driver.NamedValueChecker replaces database/sql's DefaultParameterConverter,
// and that converter is what used to dereference pointers. Adding the checker
// silently broke *time.Time and *int arguments — which ORMs pass routinely for
// nullable columns — until this was restored. GORM's own test suite caught it.
func TestNormalizeArgValue_DereferencesPointers(t *testing.T) {
	i := 42
	s := "hello"
	ts := time.Date(2026, 8, 13, 1, 2, 3, 0, time.UTC)
	f := 1.5
	b := true

	tests := []struct {
		name  string
		value any
		want  any
	}{
		{"*int", &i, int64(42)},
		{"*string", &s, "hello"},
		{"*time.Time", &ts, ts},
		{"*float64", &f, 1.5},
		{"*bool", &b, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeArgValue(1, tt.value)
			if err != nil {
				t.Fatalf("normalizeArgValue: %v", err)
			}
			if got != tt.want {
				t.Errorf("normalizeArgValue(%s) = %#v, want %#v", tt.name, got, tt.want)
			}
		})
	}
}

// TestNormalizeArgValue_NilPointerIsNull verifies a typed nil pointer binds as
// SQL NULL rather than being rejected or dereferenced.
func TestNormalizeArgValue_NilPointerIsNull(t *testing.T) {
	var (
		nilInt  *int
		nilTime *time.Time
		nilStr  *string
	)

	for _, v := range []any{nilInt, nilTime, nilStr} {
		got, err := normalizeArgValue(1, v)
		if err != nil {
			t.Fatalf("normalizeArgValue(%T): %v", v, err)
		}
		if got != nil {
			t.Errorf("normalizeArgValue(%T) = %#v, want nil", v, got)
		}
	}
}

// TestNormalizeArgValue_PointerToPointer verifies multi-level pointers are
// fully dereferenced.
func TestNormalizeArgValue_PointerToPointer(t *testing.T) {
	i := 7
	p := &i
	pp := &p

	got, err := normalizeArgValue(1, pp)
	if err != nil {
		t.Fatalf("normalizeArgValue: %v", err)
	}
	if got != int64(7) {
		t.Errorf("normalizeArgValue(**int) = %#v, want int64(7)", got)
	}
}

// ---------------------------------------------------------------------------
// driver.Valuer with a nil pointer receiver
// ---------------------------------------------------------------------------

// valueReceiverValuer implements driver.Valuer on the VALUE type, so the
// method promoted to *valueReceiverValuer dereferences its receiver. A nil
// pointer of this type is what an ORM hands the driver for an unset nullable
// field.
type valueReceiverValuer struct {
	payload string
}

func (v valueReceiverValuer) Value() (sqld.Value, error) { return v.payload, nil }

// pointerReceiverValuer implements driver.Valuer on the POINTER type and
// handles a nil receiver itself, so the driver must NOT short-circuit it.
type pointerReceiverValuer struct {
	payload string
}

func (v *pointerReceiverValuer) Value() (sqld.Value, error) {
	if v == nil {
		return "nil-receiver-handled", nil
	}
	return v.payload, nil
}

// TestNormalizeArgValue_NilValueReceiverValuerIsNull verifies a nil pointer to
// a value-receiver Valuer becomes SQL NULL instead of panicking. Before the
// guard, this crashed the process from inside CheckNamedValue.
func TestNormalizeArgValue_NilValueReceiverValuerIsNull(t *testing.T) {
	var arg *valueReceiverValuer

	got, err := normalizeArgValue(1, arg)
	if err != nil {
		t.Fatalf("normalizeArgValue: %v", err)
	}
	if got != nil {
		t.Errorf("value = %v (%T), want nil (SQL NULL)", got, got)
	}
}

// TestNormalizeArgValue_NonNilValueReceiverValuer verifies a populated value
// still round-trips through Value().
func TestNormalizeArgValue_NonNilValueReceiverValuer(t *testing.T) {
	got, err := normalizeArgValue(1, &valueReceiverValuer{payload: "hello"})
	if err != nil {
		t.Fatalf("normalizeArgValue: %v", err)
	}
	if got != "hello" {
		t.Errorf("value = %v, want %q", got, "hello")
	}
}

// TestNormalizeArgValue_NilPointerReceiverValuerIsHonoured verifies a Valuer
// whose own pointer type declares Value is still called on a nil receiver: it
// was written to handle that and may return a non-NULL value.
func TestNormalizeArgValue_NilPointerReceiverValuerIsHonoured(t *testing.T) {
	var arg *pointerReceiverValuer

	got, err := normalizeArgValue(1, arg)
	if err != nil {
		t.Fatalf("normalizeArgValue: %v", err)
	}
	if got != "nil-receiver-handled" {
		t.Errorf("value = %v, want %q", got, "nil-receiver-handled")
	}
}

// TestCheckNamedValue_NilValueReceiverValuer verifies the guard is reached
// through the driver's real entry point, which is where the panic surfaced.
func TestCheckNamedValue_NilValueReceiverValuer(t *testing.T) {
	var arg *valueReceiverValuer
	c := &embeddedConn{}
	nv := &sqld.NamedValue{Ordinal: 1, Value: arg}

	if err := c.CheckNamedValue(nv); err != nil {
		t.Fatalf("CheckNamedValue: %v", err)
	}
	if nv.Value != nil {
		t.Errorf("Value = %v (%T), want nil (SQL NULL)", nv.Value, nv.Value)
	}
}
