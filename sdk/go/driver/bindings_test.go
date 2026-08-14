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
			got, err := normalizeArgValue(tt.value)
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
	got, err := normalizeArgValue(sql.NullString{String: "hi", Valid: true})
	if err != nil {
		t.Fatalf("normalizeArgValue: %v", err)
	}
	if got != "hi" {
		t.Errorf("value = %#v, want \"hi\"", got)
	}

	got, err = normalizeArgValue(sql.NullInt64{Valid: false})
	if err != nil {
		t.Fatalf("normalizeArgValue: %v", err)
	}
	if got != nil {
		t.Errorf("invalid NullInt64 = %#v, want nil", got)
	}
}

// TestNormalizeArgValue_Unsupported verifies an unconvertible type is reported.
func TestNormalizeArgValue_Unsupported(t *testing.T) {
	_, err := normalizeArgValue(struct{ A int }{})

	var unsupported *UnsupportedArgumentTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %v, want *UnsupportedArgumentTypeError", err)
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
			got, err := normalizeArgValue(tt.value)
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
		got, err := normalizeArgValue(v)
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

	got, err := normalizeArgValue(pp)
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

	got, err := normalizeArgValue(arg)
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
	got, err := normalizeArgValue(&valueReceiverValuer{payload: "hello"})
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

	got, err := normalizeArgValue(arg)
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
