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
	sqld "database/sql/driver"
	"encoding/hex"
	"reflect"
	"strconv"
	"time"

	"github.com/dolthub/vitess/go/vt/sqlparser"
)

// bindVarPrefix is the name go-mysql-server gives positional ? placeholders.
// The vitess tokenizer rewrites the Nth placeholder to the bind variable
// ":vN", 1-indexed, so bindings must be keyed "v1", "v2", ... to match.
const bindVarPrefix = "v"

// timeBindLayout is the textual form a time.Time argument is bound as. It is
// the MySQL DATETIME literal format with microsecond precision — the widest
// precision MySQL DATETIME supports, so no in-range timestamp loses fidelity
// on the way in.
const timeBindLayout = "2006-01-02 15:04:05.999999"

// namedValuesToBindings converts positional driver arguments into
// go-mysql-server bind expressions.
//
// This is the whole reason the driver no longer rewrites SQL text: arguments
// are handed to the engine as typed AST nodes and the query string is passed
// through byte for byte. Nothing the caller supplies is ever parsed as SQL, so
// the injection surface that string interpolation created is closed by
// construction rather than by escaping.
//
// Named parameters are rejected. database/sql resolves sql.Named into
// positional arguments before the driver sees them for every path GORM and the
// standard library use, so a name arriving here means a caller bypassed that
// resolution, and silently ignoring it would bind the wrong value.
func namedValuesToBindings(args []sqld.NamedValue, loc *time.Location) (map[string]sqlparser.Expr, error) {
	if len(args) == 0 {
		return nil, nil
	}
	if loc == nil {
		loc = time.UTC
	}

	bindings := make(map[string]sqlparser.Expr, len(args))
	for _, arg := range args {
		if arg.Name != "" {
			return nil, &NamedParameterUnsupportedError{Name: arg.Name}
		}

		expr, err := bindArg(arg.Ordinal, arg.Value, loc)
		if err != nil {
			return nil, err
		}
		bindings[bindVarPrefix+strconv.Itoa(arg.Ordinal)] = expr
	}
	return bindings, nil
}

// bindArg converts a single driver value to its bind expression.
//
// The set of types handled here is exactly database/sql/driver's value
// contract after NamedValueChecker has run: the six driver.Value types, plus
// the wider integer and float widths the checker admits. There is deliberately
// no default case that stringifies an unknown value — a type this driver does
// not understand is reported, because guessing is how a binding silently
// becomes the wrong value.
func bindArg(ordinal int, v sqld.Value, loc *time.Location) (sqlparser.Expr, error) {
	switch val := v.(type) {
	case nil:
		return &sqlparser.NullVal{}, nil

	case bool:
		// GMS has no boolean literal; 1/0 is what its own parser produces for
		// TRUE/FALSE and coerces correctly into BOOLEAN and TINYINT columns.
		if val {
			return sqlparser.NewIntVal([]byte("1")), nil
		}
		return sqlparser.NewIntVal([]byte("0")), nil

	case int64:
		return sqlparser.NewIntVal(strconv.AppendInt(nil, val, 10)), nil

	case uint64:
		// Formatted unsigned so values above math.MaxInt64 survive; an int64
		// round trip would wrap them negative.
		return sqlparser.NewIntVal(strconv.AppendUint(nil, val, 10)), nil

	case float64:
		// 'g' with precision -1 is the shortest representation that parses
		// back to exactly this float64.
		return sqlparser.NewFloatVal(strconv.AppendFloat(nil, val, 'g', -1, 64)), nil

	case string:
		return sqlparser.NewStrVal([]byte(val)), nil

	case []byte:
		// Bound as a hex literal rather than a string literal so the engine
		// types it as binary. A string literal would be reinterpreted under
		// the connection charset, corrupting any byte sequence that is not
		// valid UTF-8 — the exact failure the PostgreSQL path works around
		// with its bytea decoding.
		return sqlparser.NewHexVal([]byte(hex.EncodeToString(val))), nil

	case time.Time:
		return sqlparser.NewStrVal([]byte(val.In(loc).Format(timeBindLayout))), nil

	default:
		return nil, &UnsupportedArgumentTypeError{Ordinal: ordinal, Value: v}
	}
}

// normalizeArgValue converts a caller-supplied argument into one of the types
// bindArg handles.
//
// It exists because database/sql's default conversion funnels every integer
// through int64, which wraps a uint64 above math.MaxInt64 to a negative
// number. Widening the accepted set here — and normalizing in one place —
// keeps large unsigned ids intact and lets narrower numeric types through
// without each caller having to pre-convert.
//
// driver.Valuer is honoured first so that types implementing it (sql.NullX,
// custom domain types, GORM serializers) are unwrapped before type analysis.
//
// Pointers and interfaces are dereferenced, with a nil at any level becoming
// SQL NULL. That is not a convenience: implementing driver.NamedValueChecker
// replaces database/sql's DefaultParameterConverter entirely, and that
// converter is what used to dereference pointers. Without this, an ordinary
// *time.Time or *int field — which ORMs pass routinely for nullable columns —
// would be rejected as an unsupported type.
//
// The remaining value is classified by reflect.Kind rather than by concrete
// type, so a defined type keeps the binding of the type it was defined from
// (see argKindConverters). A kind with no converter is reported, never
// stringified.
func normalizeArgValue(ordinal int, v any) (sqld.Value, error) {
	if valuer, ok := v.(sqld.Valuer); ok {
		val, err := callValuerValue(valuer)
		if err != nil {
			return nil, err
		}
		v = val
	}
	if v == nil {
		return nil, nil
	}

	// Unwrap through any depth of pointer or interface. reflect.Value.Elem is
	// used rather than a re-boxed Interface() call so no step allocates.
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil, nil
		}
		rv = rv.Elem()
	}

	if convert, ok := argKindConverters[rv.Kind()]; ok {
		if val, ok := convert(rv); ok {
			return val, nil
		}
	}
	return nil, &UnsupportedArgumentTypeError{Ordinal: ordinal, Value: rv.Interface()}
}

// argKindConverters maps an argument's reflect.Kind to the conversion that
// produces the driver.Value bindArg binds.
//
// Dispatching on kind rather than on concrete type is what admits a defined
// type whose underlying type is already bindable — `type ID int64`, the idiom
// every ORM uses for a typed primary key, and the one GORM's own models use.
// Such a type matches no case in a concrete type switch, yet it has exactly
// the representation of the type it was defined from, so converting it is a
// widening of the accepted set and not a guess about its meaning.
//
// The map is deliberately partial, and a converter may still decline: a kind
// with no entry, and a value whose kind is too coarse to identify it (a struct
// that is not a time.Time, a slice that is not of bytes), is an argument this
// driver cannot bind. It is reported. There is no stringifying fallback,
// because a value bound as text the engine then reinterprets is a silently
// wrong query rather than a failed one.
var argKindConverters = map[reflect.Kind]func(reflect.Value) (sqld.Value, bool){
	reflect.Bool: func(rv reflect.Value) (sqld.Value, bool) { return rv.Bool(), true },

	reflect.Int:   convertArgInt,
	reflect.Int8:  convertArgInt,
	reflect.Int16: convertArgInt,
	reflect.Int32: convertArgInt,
	reflect.Int64: convertArgInt,

	// Unsigned widths stay unsigned rather than folding into int64, so a
	// uint64 above math.MaxInt64 reaches bindArg with its value intact.
	reflect.Uint:   convertArgUint,
	reflect.Uint8:  convertArgUint,
	reflect.Uint16: convertArgUint,
	reflect.Uint32: convertArgUint,
	reflect.Uint64: convertArgUint,

	reflect.Float32: convertArgFloat,
	reflect.Float64: convertArgFloat,

	reflect.String: func(rv reflect.Value) (sqld.Value, bool) { return rv.String(), true },
	reflect.Slice:  convertArgSlice,
	reflect.Struct: convertArgStruct,
}

// timeType is time.Time's reflect.Type, used by convertArgStruct.
var timeType = reflect.TypeFor[time.Time]()

// convertArgInt widens any signed integer width to int64.
func convertArgInt(rv reflect.Value) (sqld.Value, bool) { return rv.Int(), true }

// convertArgUint widens any unsigned integer width to uint64.
func convertArgUint(rv reflect.Value) (sqld.Value, bool) { return rv.Uint(), true }

// convertArgFloat widens any float width to float64.
func convertArgFloat(rv reflect.Value) (sqld.Value, bool) { return rv.Float(), true }

// convertArgSlice accepts a slice of bytes and declines every other slice.
//
// The element kind is what is tested, not the element type, so a defined byte
// type ([]MyByte) is accepted on the same reasoning as a defined []byte: its
// elements are bytes. The returned slice shares the caller's backing array,
// which bindArg only reads.
func convertArgSlice(rv reflect.Value) (sqld.Value, bool) {
	if rv.Type().Elem().Kind() != reflect.Uint8 {
		return nil, false
	}
	return rv.Bytes(), true
}

// convertArgStruct accepts a struct convertible to time.Time and declines
// every other struct.
//
// Convertibility is the precise test: Go permits a struct conversion only
// between identical underlying types, and time.Time's fields are unexported,
// so the only types that satisfy this are time.Time itself and types defined
// from it. A struct that merely resembles a timestamp does not qualify.
func convertArgStruct(rv reflect.Value) (sqld.Value, bool) {
	if !rv.Type().ConvertibleTo(timeType) {
		return nil, false
	}
	return rv.Convert(timeType).Interface().(time.Time), true
}

// valuerType is driver.Valuer's reflect.Type, used by callValuerValue.
var valuerType = reflect.TypeFor[sqld.Valuer]()

// callValuerValue calls vr.Value, returning SQL NULL instead of panicking when
// vr is a nil pointer whose POINTEE type is what implements driver.Valuer.
//
// A method set declared on the value type T is promoted to *T, so a nil *T
// still satisfies driver.Valuer -- and invoking the promoted method
// dereferences the nil pointer. ORMs hand the driver exactly this: a nil
// pointer field of a type with a value-receiver Value method. Calling straight
// through crashes the process, which is why database/sql's own converter has
// carried this guard since Go 1.8 (see callValuerValue in
// database/sql/convert.go); implementing driver.NamedValueChecker replaces
// that converter, so the guard has to be reproduced here.
//
// A nil pointer whose own POINTER type declares Value is left alone: that
// method was written to handle a nil receiver and may legitimately return a
// non-NULL value.
func callValuerValue(vr sqld.Valuer) (sqld.Value, error) {
	if rv := reflect.ValueOf(vr); rv.Kind() == reflect.Pointer &&
		rv.IsNil() &&
		rv.Type().Elem().Implements(valuerType) {
		return nil, nil
	}
	return vr.Value()
}
