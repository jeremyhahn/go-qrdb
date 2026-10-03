//go:build qrdb_sql

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pgcatalog

import "strings"

// Type describes one PostgreSQL base type exactly as pg_catalog.pg_type
// reports it.
type Type struct {
	// Name is pg_type.typname, the type's catalogue name -- int8, not
	// bigint. It is also the value information_schema.columns.udt_name
	// carries for a column of this type, which is what the two are joined
	// on.
	Name string

	// OID is pg_type.oid. These are PostgreSQL's own hard-coded system
	// OIDs, fixed since the type was introduced and identical on every
	// PostgreSQL installation, so no hashing is needed to make them stable
	// across qrdb replicas the way pg_class and pg_namespace OIDs are (see
	// oid.go).
	OID uint32

	// Len is pg_type.typlen: the type's fixed width in bytes, or -1 for a
	// variable-length type. A PostgreSQL client reads a fixed-width
	// column's bit width as 8 * typlen, so int8 reporting 8 is what makes
	// a bigint column describe itself as 64 bits wide.
	Len int32
}

// pgTypes is the pg_catalog.pg_type inventory. It contains exactly the
// types udtNameByDataType can name plus its fallback, and nothing else --
// TestPGTypeCoversEveryUDTName pins that equivalence, because a udt_name
// with no pg_type row is silently dropped by the introspection join rather
// than reported (see the package comment).
//
// Ordering is by OID so pg_type scans are deterministic across replicas
// without needing a sort at read time.
var pgTypes = []Type{
	{Name: "bool", OID: 16, Len: 1},
	{Name: "bytea", OID: 17, Len: -1},
	{Name: "int8", OID: 20, Len: 8},
	{Name: "int2", OID: 21, Len: 2},
	{Name: "int4", OID: 23, Len: 4},
	{Name: "text", OID: 25, Len: -1},
	{Name: "json", OID: 114, Len: -1},
	{Name: "float4", OID: 700, Len: 4},
	{Name: "float8", OID: 701, Len: 8},
	{Name: "bpchar", OID: 1042, Len: -1},
	{Name: "varchar", OID: 1043, Len: -1},
	{Name: "date", OID: 1082, Len: 4},
	{Name: "time", OID: 1083, Len: 8},
	{Name: "timestamptz", OID: 1184, Len: 8},
	{Name: "numeric", OID: 1700, Len: -1},
}

// DefaultUDTName is the typname reported for any column whose go-mysql-server
// data type has no explicit entry in udtNameByDataType.
//
// It is deliberately a real, always-present type rather than an error or an
// empty string: information_schema.columns is joined to pg_type on this
// value, so an unrecognised type that reported something absent from
// pgTypes would make the column vanish from the client's view of the table
// entirely. text is the honest fallback -- every go-mysql-server type has a
// textual representation, and that is exactly what the wire layer sends for
// a type it has no better mapping for (see pkg/sql/pgsql.DefaultOID).
const DefaultUDTName = "text"

// udtNameByDataType maps a go-mysql-server information_schema.columns
// DATA_TYPE -- the bare type name with no width, collation, or signedness,
// lowercased -- to the PostgreSQL typname a PostgreSQL client expects to
// read back as udt_name.
//
// It is the inverse of the PostgreSQL-to-go-mysql-server DDL substitution
// in pkg/sql/engine (pgColumnTypes), read back through what go-mysql-server
// actually stores. The interesting entries and why each is the faithful one:
//
//   - tinyint maps to bool. go-mysql-server represents BOOLEAN as its Int8
//     type, indistinguishable at this level from a genuine TINYINT column --
//     but PostgreSQL has no one-byte integer, so on this surface a TINYINT
//     column can only have arrived from a PostgreSQL client writing
//     BOOLEAN. bool is therefore the only mapping that can be right.
//
//   - datetime and timestamp both map to timestamptz. The DDL substitution
//     collapses PostgreSQL's timestamp and timestamptz onto MySQL's
//     DATETIME (neither MySQL type carries a zone), so the distinction is
//     already gone by the time a column is stored and cannot be recovered
//     here. timestamptz is the right side of the collapse to land on: it is
//     what gorm.io/driver/postgres emits for every time.Time field, so a
//     column created from one compares equal to its own declared type, and
//     a client comparing against "timestamp" still matches on prefix.
//
//   - char maps to bpchar, PostgreSQL's own catalogue name for CHAR(n)
//     ("blank-padded char"). uuid is stored as CHAR(36) by the DDL
//     substitution and so reports bpchar rather than uuid; recovering
//     "uuid" would require remembering the declared PostgreSQL type, which
//     nothing on this path does.
//
//   - every unbounded string family (tinytext..longtext, enum, set) maps to
//     text and every binary family (binary..longblob) to bytea, because
//     PostgreSQL has exactly one of each and no width variants.
//
// Keys are lowercase; UDTName lowercases its argument before looking up.
var udtNameByDataType = map[string]string{
	"bigint":     "int8",
	"int":        "int4",
	"integer":    "int4",
	"mediumint":  "int4",
	"smallint":   "int2",
	"year":       "int2",
	"tinyint":    "bool",
	"decimal":    "numeric",
	"double":     "float8",
	"float":      "float4",
	"char":       "bpchar",
	"varchar":    "varchar",
	"tinytext":   "text",
	"text":       "text",
	"mediumtext": "text",
	"longtext":   "text",
	"enum":       "text",
	"set":        "text",
	"binary":     "bytea",
	"varbinary":  "bytea",
	"tinyblob":   "bytea",
	"blob":       "bytea",
	"mediumblob": "bytea",
	"longblob":   "bytea",
	"datetime":   "timestamptz",
	"timestamp":  "timestamptz",
	"date":       "date",
	"time":       "time",
	"json":       "json",
}

// UDTName returns the PostgreSQL typname reported as
// information_schema.columns.udt_name for a column whose go-mysql-server
// DATA_TYPE is dataType. Lookup is case-insensitive, and an unrecognised
// type yields DefaultUDTName rather than an error -- see DefaultUDTName for
// why silence is not an option here.
func UDTName(dataType string) string {
	name := strings.ToLower(dataType)
	// A go-mysql-server type renders its modifier into String() -- datetime(6),
	// varchar(64), decimal(9,0) -- while this map is keyed by the bare name.
	// The modifier never changes WHICH PostgreSQL type the column reports, only
	// its width/precision, so it is stripped before lookup. Without this a
	// datetime(6) column (the substitution for every unmodified PostgreSQL
	// timestamp) fell through to DefaultUDTName and introspected as text.
	if i := strings.IndexByte(name, '('); i >= 0 {
		name = strings.TrimSpace(name[:i])
	}
	if mapped, ok := udtNameByDataType[name]; ok {
		return mapped
	}
	return DefaultUDTName
}

// numericRadixByUDTName gives information_schema.columns.numeric_precision_radix
// for the numeric types, matching PostgreSQL: the binary floating-point
// types state their precision in bits and so report radix 2, while every
// exact numeric states it in decimal digits and reports radix 10. A type
// absent from this map is not numeric and reports SQL NULL, which is also
// what PostgreSQL does.
var numericRadixByUDTName = map[string]uint32{
	"int2":    10,
	"int4":    10,
	"int8":    10,
	"numeric": 10,
	"float4":  2,
	"float8":  2,
}

// NumericRadix returns the numeric_precision_radix for a column of the
// given PostgreSQL typname, and whether the column is numeric at all. A
// false second result means the column reports SQL NULL for that field.
func NumericRadix(udtName string) (uint32, bool) {
	radix, ok := numericRadixByUDTName[udtName]
	return radix, ok
}
