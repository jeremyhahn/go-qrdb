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
	"reflect"

	"github.com/cockroachdb/apd/v3"
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/sqltypes"
	"github.com/dolthub/vitess/go/vt/proto/query"
)

// For most column types a go-mysql-server result cell already *is* the value
// a caller outside the engine should see: an INT cell is an int32, a VARCHAR
// cell is a string, a DATETIME cell is a time.Time. ENUM, SET and DECIMAL are
// the exceptions. The engine represents an ENUM cell as the uint16 ordinal of
// its label and a SET cell as the uint64 bitmask of its members, so once such
// a cell leaves the engine it is an integer whose meaning lives entirely in
// the column's sql.Type: 2 means "active" in ENUM('pending','active','banned')
// and "b" in ENUM('a','b'). A DECIMAL cell is an *apd.Decimal, an engine
// internal that no caller outside the engine can receive at all: it is not one
// of the types database/sql/driver.Value admits, and any transport that hands
// it out unrendered has to guess at a formatting the wire already decided.
// Any code that decides what a cell means by looking at the Go type of the
// cell alone -- rather than at the column type that defines it -- necessarily
// gets these wrong, and hands the caller an integer where a label was stored.
//
// ResolveColumnValue is the single place in qrdb that resolves those symbols
// back into values, so that every result path -- the embedded database/sql
// driver, the service-layer SQL API behind REST/gRPC/QUIC/MCP, and the
// PostgreSQL wire protocol -- renders a row identically, and identically to
// the MySQL wire protocol, where go-mysql-server itself does the resolution
// (see types.EnumType.SQL, types.SetType.SQL and types.DecimalType_.SQL,
// which reach the same values through the same EnumType.At /
// SetType.BitsToString / apd fixed-point rendering used here).

// symbolicResolver converts one cell of a symbolically-represented column
// into the value that cell stands for, using the column's own sql.Type.
type symbolicResolver func(ctx context.Context, t sqle.Type, v any) (any, error)

// symbolicResolvers enumerates every column type whose in-engine
// representation is a symbol standing for a value rather than the value
// itself. Keying on query.Type mirrors columnCodecs in rowcodec.go and
// columnTypeBuilders in schema.go: the discriminator is authoritative, so no
// per-column parameters are needed to select the resolver. A type absent from
// this map needs no resolution and is returned unchanged.
var symbolicResolvers = map[query.Type]symbolicResolver{
	sqltypes.Enum:    resolveEnum,
	sqltypes.Set:     resolveSet,
	sqltypes.Decimal: resolveDecimal,
}

// resolvedValueTypes gives the Go type ResolveColumnValue produces for each
// symbolic column type, which is not the sql.Type's own ValueType(): an ENUM
// column reports uint16, a SET column reports uint64 and a DECIMAL column
// reports *apd.Decimal, because those are the types the engine stores, not
// the types a caller receives. Keeping the two facts adjacent is what stops a
// driver from advertising a scan type that its own rows can never satisfy.
var resolvedValueTypes = map[query.Type]reflect.Type{
	sqltypes.Enum:    reflect.TypeOf(""),
	sqltypes.Set:     reflect.TypeOf(""),
	sqltypes.Decimal: reflect.TypeOf(""),
}

// ResolveColumnValue returns the externally visible value of one cell of a
// go-mysql-server result row, given the sql.Type of the column that produced
// it.
//
// An ENUM cell resolves to its label ("active"), a SET cell to its
// comma-joined member labels ("read,admin"), and a DECIMAL cell to its
// fixed-point text ("1234.5600"); every other cell, and every NULL cell, is
// returned unchanged, so callers keep the engine's native Go types (int32,
// float64, time.Time, ...) and remain free to apply their own
// transport-specific normalization afterwards.
//
// ctx is used only for go-mysql-server's value conversion and may be any
// context; callers holding a *sql.Context should pass it so that session
// state (notably SQL mode) is honoured exactly as it is on the MySQL wire.
func ResolveColumnValue(ctx context.Context, t sqle.Type, v any) (any, error) {
	if v == nil || t == nil {
		return v, nil
	}
	resolve, ok := symbolicResolvers[t.Type()]
	if !ok {
		return v, nil
	}
	return resolve(ctx, t, v)
}

// ResolveColumnValues resolves every cell of row against schema in place,
// returning the same slice. It is the row-at-a-time form of
// ResolveColumnValue for callers that materialize whole rows. Cells beyond
// the end of schema are left untouched.
func ResolveColumnValues(ctx context.Context, schema sqle.Schema, row sqle.Row) (sqle.Row, error) {
	for i := range row {
		if i >= len(schema) || schema[i] == nil {
			continue
		}
		v, err := ResolveColumnValue(ctx, schema[i].Type, row[i])
		if err != nil {
			return nil, err
		}
		row[i] = v
	}
	return row, nil
}

// ResolvedValueType returns the Go type ResolveColumnValue produces for a
// column of type t, for drivers reporting sql.ColumnType.ScanType. It differs
// from t.ValueType() only for the symbolic types, where the engine's storage
// type (uint16, uint64, *apd.Decimal) is not what a caller ever receives.
func ResolvedValueType(t sqle.Type) reflect.Type {
	if t == nil {
		return nil
	}
	if rt, ok := resolvedValueTypes[t.Type()]; ok {
		return rt
	}
	return t.ValueType()
}

// resolveEnum converts an ENUM cell -- the uint16 ordinal of a label -- to
// that label. It mirrors types.EnumType.SQL: convert to the canonical ordinal
// representation, then look the ordinal up in the type's value list.
func resolveEnum(ctx context.Context, t sqle.Type, v any) (any, error) {
	et, ok := t.(sqle.EnumType)
	if !ok {
		return nil, &SymbolicTypeMismatchError{Type: t.String(), Expected: "sql.EnumType"}
	}
	converted, _, err := et.Convert(ctx, v)
	if err != nil {
		return nil, &SymbolicValueError{Type: t.String(), Value: goTypeName(v), Reason: err.Error()}
	}
	ordinal, ok := converted.(uint16)
	if !ok {
		return nil, &SymbolicValueError{Type: t.String(), Value: goTypeName(converted), Reason: "converted value is not an enum ordinal"}
	}
	// At's found flag is discarded for the same reason types.EnumType.SQL
	// discards it: Convert above resolved the ordinal against this very
	// value list and fails when it has no label, so a successful conversion
	// is already proof the lookup succeeds.
	label, _ := et.At(int(ordinal))
	return label, nil
}

// resolveSet converts a SET cell -- the uint64 bitmask of its members -- to
// the comma-joined member labels. It mirrors types.SetType.SQL.
func resolveSet(ctx context.Context, t sqle.Type, v any) (any, error) {
	st, ok := t.(sqle.SetType)
	if !ok {
		return nil, &SymbolicTypeMismatchError{Type: t.String(), Expected: "sql.SetType"}
	}
	converted, _, err := st.Convert(ctx, v)
	if err != nil {
		return nil, &SymbolicValueError{Type: t.String(), Value: goTypeName(v), Reason: err.Error()}
	}
	bits, ok := converted.(uint64)
	if !ok {
		return nil, &SymbolicValueError{Type: t.String(), Value: goTypeName(converted), Reason: "converted value is not a set bitmask"}
	}
	labels, err := st.BitsToString(bits)
	if err != nil {
		return nil, &SymbolicValueError{Type: t.String(), Value: goTypeName(v), Reason: err.Error()}
	}
	return labels, nil
}

// resolveDecimal converts a DECIMAL cell -- an *apd.Decimal -- to the
// fixed-point text the MySQL wire protocol renders for it.
//
// The rendering is delegated to the column type's own
// DecimalValueStringFixed, which is the function types.DecimalType_.SQL uses
// to produce the wire's bytes, rather than reimplemented from the value
// alone. That distinction is load-bearing and not obvious: whether the
// declared scale is applied depends on whether the type describes a column,
// which is not something the cell reveals. AVG(amt) over DECIMAL(20,4)
// evaluates to 1.61725000 but is typed with scale 5, and the wire prints
// "1.61725"; formatting the cell directly yields "1.61725000" and silently
// diverges. Only the type knows.
//
// A decimal type that is not go-mysql-server's own falls back to plain
// fixed-point text, which is what that function does when its type describes
// no column.
//
// The engine's own type is never returned: *apd.Decimal is not a
// database/sql/driver.Value, cannot be scanned into a string or a
// sql.NullString, and would leave every non-MySQL transport inventing its own
// formatting for the same stored row.
func resolveDecimal(_ context.Context, t sqle.Type, v any) (any, error) {
	d, ok := v.(*apd.Decimal)
	if !ok || d == nil {
		return nil, &SymbolicValueError{Type: t.String(), Value: goTypeName(v), Reason: "value is not a decimal"}
	}
	if dt, ok := t.(types.DecimalType_); ok {
		return dt.DecimalValueStringFixed(d), nil
	}
	return d.Text('f'), nil
}
