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
	"strings"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/jackc/pgx/v5/pgtype"
)

// OIDMapping maps go-mysql-server type names to PostgreSQL OIDs.
// Used when converting result schemas from GMS format to PG wire format.
//
// A result column declared here is a promise about the Go value that will
// be written under it: psql-wire hands the value to pgx, which refuses to
// encode a value it has no plan for under the declared OID rather than
// guessing ("cannot find encode plan"). So the entries below and
// wireValueNormalizers are one decision, kept in one file and reached
// through one OID (see ColumnOID), and every type go-mysql-server can name
// for a result column must appear here or be genuinely text-shaped --
// DefaultOID is a fallback for text-shaped types, not a catch-all.
//
// Two families need a word:
//
//   - The UNSIGNED integers all map to Int8OID. PostgreSQL has no unsigned
//     integer type at all, so the choice is which signed width holds the
//     values; bigint holds every unsigned width except the top bit of a
//     uint64, which no column go-mysql-server derives here produces.
//     Narrower declarations would buy nothing and add a range cliff.
//     (information_schema.columns alone reaches this path with three
//     unsigned columns -- ORDINAL_POSITION, NUMERIC_PRECISION,
//     NUMERIC_SCALE -- so it is not a hypothetical.)
//
//   - INT8 maps to Int2OID, the narrowest integer PostgreSQL has. Whether a
//     one-byte value is an integer or a boolean is NOT decided here: it is
//     decided by ColumnOID before this table is consulted, because the
//     distinction lives in the type value and this table is keyed by the
//     base type's name, which is "INT8" for both.
//
//     MySQL spells BOOLEAN as TINYINT(1), and go-mysql-server keeps that
//     distinction faithfully -- types.Boolean is
//     MustCreateNumberTypeWithDisplayWidth(sqltypes.Int8, 1) while types.Int8
//     has no display width, and types.IsBoolean reads exactly that. It
//     answers true for a declared BOOLEAN column and for a comparison (which
//     go-mysql-server types as tinyint(1), matching PostgreSQL, where a
//     comparison yields boolean), and false for a declared TINYINT, for
//     SELECT 1, and for a CASE expression over small integer literals.
//
//     Mapping every INT8 to BoolOID therefore did not resolve an ambiguity;
//     it discarded an answer go-mysql-server had already given. A GORM
//     application issuing
//     `SELECT (CASE WHEN age = 18 THEN 1 ELSE 2 END) AS age` got that column
//     described as boolean and failed scanning "true" into a uint.
var OIDMapping = map[string]uint32{
	"TEXT":      pgtype.TextOID,
	"VARCHAR":   pgtype.TextOID,
	"BLOB":      pgtype.TextOID,
	"VARBINARY": pgtype.TextOID,
	"INT64":     pgtype.Int8OID,
	"BIGINT":    pgtype.Int8OID,
	"INT32":     pgtype.Int4OID,
	"INT":       pgtype.Int4OID,
	"INT24":     pgtype.Int4OID,
	"INT16":     pgtype.Int2OID,
	"SMALLINT":  pgtype.Int2OID,
	"INT8":      pgtype.Int2OID,
	"UINT64":    pgtype.Int8OID,
	"UINT32":    pgtype.Int8OID,
	"UINT24":    pgtype.Int8OID,
	"UINT16":    pgtype.Int8OID,
	"UINT8":     pgtype.Int8OID,
	"FLOAT64":   pgtype.Float8OID,
	"DOUBLE":    pgtype.Float8OID,
	"FLOAT32":   pgtype.Float4OID,
	"BOOLEAN":   pgtype.BoolOID,
	"TIMESTAMP": pgtype.TimestamptzOID,
	"DATETIME":  pgtype.TimestamptzOID,
	"JSON":      pgtype.JSONBOID,
}

// DefaultOID is used when no mapping exists for a GMS type.
const DefaultOID = pgtype.TextOID

// LookupOID returns the PostgreSQL OID for a given GMS type name.
// Returns DefaultOID if no mapping exists. Lookup is case-insensitive.
func LookupOID(gmsType string) uint32 {
	if oid, ok := OIDMapping[strings.ToUpper(gmsType)]; ok {
		return oid
	}
	return DefaultOID
}

// ColumnOID returns the PostgreSQL OID a result column of the given
// go-mysql-server type is declared with.
//
// It exists so that the column DESCRIPTION a client is sent and the VALUES
// written under it are derived from the same expression rather than from two
// that merely look alike: the description promises an encoding, and a value
// that does not match it is not a wrong answer but a connection-level
// failure ("cannot find encode plan"). Every caller on either side goes
// through here.
func ColumnOID(t sql.Type) uint32 {
	if t == nil {
		return DefaultOID
	}
	// Boolean before the table: MySQL spells BOOLEAN as TINYINT(1), so a
	// boolean and a one-byte integer share the base type name "INT8" that
	// OIDMapping is keyed by. types.IsBoolean reads the display width that
	// tells them apart -- true for a declared BOOLEAN column and for a
	// comparison, false for a declared TINYINT, for SELECT 1, and for a CASE
	// over small integer literals.
	if types.IsBoolean(t) {
		return pgtype.BoolOID
	}
	return LookupOID(t.Type().String())
}

// wireValueNormalizers maps a declared result-column OID to the coercion
// applied to every value written under it. An OID with no entry needs none:
// go-mysql-server already produces a Go value pgx can encode under it.
//
// Dispatching through a map rather than a switch keeps the set of OIDs that
// need a coercion a single piece of data, checkable against OIDMapping by a
// test rather than by reading two control-flow graphs.
var wireValueNormalizers = map[uint32]func(any) any{
	pgtype.BoolOID: normalizeBool,
}

// NormalizeForOID coerces a go-mysql-server cell into the Go type pgx can
// encode under oid. A nil cell is SQL NULL under every OID and is returned
// unchanged.
func NormalizeForOID(v any, oid uint32) any {
	if v == nil {
		return nil
	}
	if normalize, ok := wireValueNormalizers[oid]; ok {
		return normalize(v)
	}
	return v
}

// normalizeBool coerces a cell declared BoolOID to a Go bool.
//
// go-mysql-server hands back a bool for a comparison and an int8 for a
// stored TINYINT/BOOLEAN column, and the wider integer widths are reachable
// through an expression whose result type is still tinyint. Zero is false
// and every other value is true, which is MySQL's own truthiness and the
// only reading available once the column has been narrowed to one bit of
// information by its declaration.
//
// A value of some other type entirely is returned unchanged rather than
// guessed at: pgx then reports precisely what it could not encode, which is
// a better failure than a fabricated true.
func normalizeBool(v any) any {
	switch val := v.(type) {
	case bool:
		return val
	case int8:
		return val != 0
	case int16:
		return val != 0
	case int32:
		return val != 0
	case int64:
		return val != 0
	case int:
		return val != 0
	case uint8:
		return val != 0
	case uint16:
		return val != 0
	case uint32:
		return val != 0
	case uint64:
		return val != 0
	case uint:
		return val != 0
	default:
		return v
	}
}
