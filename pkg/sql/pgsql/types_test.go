// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build qrdb_pgsql

package pgsql

import (
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLookupOID_AllMappings(t *testing.T) {
	tests := []struct {
		name     string
		gmsType  string
		expected uint32
	}{
		{"TEXT", "TEXT", pgtype.TextOID},
		{"VARCHAR", "VARCHAR", pgtype.TextOID},
		{"BLOB", "BLOB", pgtype.ByteaOID},
		{"VARBINARY", "VARBINARY", pgtype.ByteaOID},
		{"BINARY", "BINARY", pgtype.ByteaOID},
		{"INT64", "INT64", pgtype.Int8OID},
		{"BIGINT", "BIGINT", pgtype.Int8OID},
		{"INT32", "INT32", pgtype.Int4OID},
		{"INT", "INT", pgtype.Int4OID},
		{"INT16", "INT16", pgtype.Int2OID},
		{"SMALLINT", "SMALLINT", pgtype.Int2OID},
		{"FLOAT64", "FLOAT64", pgtype.Float8OID},
		{"DOUBLE", "DOUBLE", pgtype.Float8OID},
		{"FLOAT32", "FLOAT32", pgtype.Float4OID},
		{"BOOLEAN", "BOOLEAN", pgtype.BoolOID},
		{"TIMESTAMP", "TIMESTAMP", pgtype.TimestamptzOID},
		{"DATETIME", "DATETIME", pgtype.TimestamptzOID},
		{"JSON", "JSON", pgtype.JSONBOID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oid := LookupOID(tt.gmsType)
			assert.Equal(t, tt.expected, oid)
		})
	}
}

func TestLookupOID_CaseInsensitive(t *testing.T) {
	tests := []struct {
		name     string
		gmsType  string
		expected uint32
	}{
		{"lowercase text", "text", pgtype.TextOID},
		{"mixed case Text", "Text", pgtype.TextOID},
		{"lowercase varchar", "varchar", pgtype.TextOID},
		{"lowercase int64", "int64", pgtype.Int8OID},
		{"mixed case Boolean", "Boolean", pgtype.BoolOID},
		{"lowercase json", "json", pgtype.JSONBOID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oid := LookupOID(tt.gmsType)
			assert.Equal(t, tt.expected, oid)
		})
	}
}

func TestLookupOID_UnknownType(t *testing.T) {
	tests := []struct {
		name    string
		gmsType string
	}{
		{"GEOMETRY", "GEOMETRY"},
		{"POINT", "POINT"},
		{"UNKNOWN", "UNKNOWN"},
		{"empty string", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oid := LookupOID(tt.gmsType)
			assert.Equal(t, uint32(DefaultOID), oid)
		})
	}
}

func TestDefaultOID_IsTextOID(t *testing.T) {
	assert.Equal(t, pgtype.TextOID, DefaultOID)
}

func TestOIDMapping_AllValuesNonZero(t *testing.T) {
	for typeName, oid := range OIDMapping {
		t.Run(typeName, func(t *testing.T) {
			assert.NotZero(t, oid, "OID for %s should not be zero", typeName)
		})
	}
}

func TestColumnOIDMatchesTheDeclaredType(t *testing.T) {
	cases := []struct {
		name string
		typ  sql.Type
		want uint32
	}{
		{name: "bigint", typ: types.Int64, want: pgtype.Int8OID},
		{name: "int", typ: types.Int32, want: pgtype.Int4OID},
		{name: "smallint", typ: types.Int16, want: pgtype.Int2OID},
		// MySQL spells BOOLEAN as TINYINT(1), and go-mysql-server keeps the
		// two apart by display width -- types.Boolean carries one, types.Int8
		// does not, and types.IsBoolean reads exactly that. Both share the
		// base type name "INT8", so the distinction has to be made from the
		// type value before OIDMapping (which is keyed by that name) is
		// consulted.
		{name: "tinyint is an integer", typ: types.Int8, want: pgtype.Int2OID},
		{name: "boolean is a boolean", typ: types.Boolean, want: pgtype.BoolOID},
		{name: "unsigned widths hold in bigint", typ: types.Uint32, want: pgtype.Int8OID},
		{name: "double", typ: types.Float64, want: pgtype.Float8OID},
		{name: "text", typ: types.LongText, want: pgtype.TextOID},
		{name: "datetime", typ: types.Datetime, want: pgtype.TimestamptzOID},
		{name: "an unmapped type is text-shaped", typ: types.JSON, want: pgtype.JSONBOID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ColumnOID(tc.typ))
		})
	}
}

func TestColumnOIDOfNilTypeIsTheFallback(t *testing.T) {
	// A result column with no type is still described to the client, and
	// text is what the wire layer can always encode.
	assert.Equal(t, uint32(DefaultOID), ColumnOID(nil))
}

func TestNormalizeForOIDCoercesOnlyWhatItMust(t *testing.T) {
	cases := []struct {
		name string
		in   any
		oid  uint32
		want any
	}{
		{name: "nil is NULL under every OID", in: nil, oid: pgtype.BoolOID, want: nil},
		{name: "stored tinyint zero", in: int8(0), oid: pgtype.BoolOID, want: false},
		{name: "stored tinyint one", in: int8(1), oid: pgtype.BoolOID, want: true},
		{name: "any non-zero tinyint is true", in: int8(42), oid: pgtype.BoolOID, want: true},
		{name: "comparison already produced a bool", in: true, oid: pgtype.BoolOID, want: true},
		{name: "widened integer", in: int64(0), oid: pgtype.BoolOID, want: false},
		{name: "unsigned integer", in: uint8(3), oid: pgtype.BoolOID, want: true},
		{name: "unhandled Go type is left for pgx to report", in: "yes", oid: pgtype.BoolOID, want: "yes"},
		{name: "an OID needing no coercion is untouched", in: int64(7), oid: pgtype.Int8OID, want: int64(7)},
		{name: "text is untouched", in: "hello", oid: pgtype.TextOID, want: "hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, NormalizeForOID(tc.in, tc.oid))
		})
	}
}

func TestEveryNormalizedOIDIsOneAColumnCanBeDeclaredWith(t *testing.T) {
	// A normalizer for an OID no column is ever described with would be
	// dead code that still looks like a guarantee.
	declared := make(map[uint32]bool, len(OIDMapping)+1)
	declared[DefaultOID] = true
	for _, oid := range OIDMapping {
		declared[oid] = true
	}
	for oid := range wireValueNormalizers {
		assert.Truef(t, declared[oid], "OID %d is normalized but never declared", oid)
	}
}

func TestNormalizeBoolIsTotalOverTheIntegerWidths(t *testing.T) {
	// go-mysql-server can widen a tinyint-typed expression, so every
	// integer width has to answer rather than fall through to pgx.
	for _, zero := range []any{int(0), int8(0), int16(0), int32(0), int64(0),
		uint(0), uint8(0), uint16(0), uint32(0), uint64(0)} {
		assert.Equalf(t, false, normalizeBool(zero), "zero of %T", zero)
	}
	for _, one := range []any{int(1), int8(1), int16(1), int32(1), int64(1),
		uint(1), uint8(1), uint16(1), uint32(1), uint64(1)} {
		assert.Equalf(t, true, normalizeBool(one), "one of %T", one)
	}
	assert.Equal(t, 1.5, normalizeBool(1.5), "a float is not an integer width")
}

// TestColumnOIDDistinguishesBooleanFromTinyint pins the distinction against
// the expressions that actually produce each, rather than against hand-built
// types: whether a one-byte value is a boolean is decided by go-mysql-server
// when it types the expression, and this surface's only job is not to discard
// that answer.
//
// The regression this guards is concrete. Every INT8 used to be declared
// BoolOID, so a GORM application issuing
// `SELECT (CASE WHEN age = 18 THEN 1 ELSE 2 END) AS age` had that column
// described as boolean and failed with "converting driver.Value type bool
// (\"true\") to a uint".
func TestColumnOIDDistinguishesBooleanFromTinyint(t *testing.T) {
	h, ctx := newResultColumnsHandler(t)

	_, iter, _, err := h.sqlEngine.Query(ctx,
		"CREATE TABLE flags (id BIGINT PRIMARY KEY, ok BOOLEAN, small TINYINT, n INT)")
	require.NoError(t, err)
	require.NoError(t, iter.Close(ctx))

	cases := []struct {
		name  string
		query string
		want  uint32
	}{
		{
			name:  "a declared BOOLEAN column is a boolean",
			query: "SELECT ok FROM flags",
			want:  pgtype.BoolOID,
		},
		{
			// PostgreSQL types a comparison as boolean too, so this agrees
			// with the reference implementation and not merely with MySQL.
			name:  "a comparison is a boolean",
			query: "SELECT n = 18 AS cmp FROM flags",
			want:  pgtype.BoolOID,
		},
		{
			name:  "a declared TINYINT column is an integer",
			query: "SELECT small FROM flags",
			want:  pgtype.Int2OID,
		},
		{
			name:  "a CASE over small integer literals is an integer",
			query: "SELECT (CASE WHEN n = 18 THEN 1 ELSE 2 END) AS age FROM flags",
			want:  pgtype.Int2OID,
		},
		{
			name:  "a small integer literal is an integer",
			query: "SELECT 1 AS one FROM flags",
			want:  pgtype.Int2OID,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema, iter, _, err := h.sqlEngine.Query(ctx, tc.query)
			require.NoError(t, err)
			require.NoError(t, iter.Close(ctx))
			require.Len(t, schema, 1)

			assert.Equal(t, tc.want, ColumnOID(schema[0].Type))
		})
	}
}
