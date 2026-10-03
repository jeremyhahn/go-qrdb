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

import (
	"fmt"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
)

// FormatTypeFunction is the name of the PostgreSQL function that renders a
// type OID and modifier as the type's SQL name.
const FormatTypeFunction = "format_type"

// attributeRows returns pg_attribute: one row per column of every table the
// engine serves.
//
// It is how a PostgreSQL client reads a table's columns without going through
// information_schema. gorm.io/driver/postgres does exactly that when it
// migrates:
//
//	SELECT a.attname, format_type(a.atttypid, a.atttypmod)
//	FROM pg_attribute a JOIN pg_class b ON a.attrelid = b.oid ...
//	WHERE a.attnum > 0 AND NOT a.attisdropped AND b.relname = $1
//
// so attrelid has to agree with pg_class.oid exactly -- both come from
// ClassOID -- or the join yields nothing and every column looks absent.
//
// A database or table that cannot be read is skipped rather than failing the
// scan, for the same reason classRows skips one.
func attributeRows(ctx *sqle.Context, cat sqle.Catalog) ([]sqle.Row, error) {
	var rows []sqle.Row
	for _, db := range cat.AllDatabases(ctx) {
		dbName := db.Name()
		tableNames, err := db.GetTableNames(ctx)
		if err != nil {
			continue
		}

		for _, tableName := range tableNames {
			table, _, err := cat.DatabaseTable(ctx, db, tableName)
			if err != nil {
				continue
			}
			relOID := ClassOID(dbName, table.Name())

			for i, col := range table.Schema(ctx) {
				rows = append(rows, sqle.Row{
					relOID,                     // attrelid
					col.Name,                   // attname
					typeOIDForColumn(col.Type), // atttypid
					int32(-1),                  // atttypmod
					int16(i + 1),               // attnum, 1-based as PostgreSQL numbers them
					!col.Nullable,              // attnotnull
					false,                      // attisdropped
				})
			}
		}
	}
	return rows, nil
}

// typeOIDForColumn returns the pg_type OID a column of the given
// go-mysql-server type reports as pg_attribute.atttypid.
//
// It routes through UDTName so that pg_attribute, pg_type, and
// information_schema.columns.udt_name all describe a column identically --
// the three are joined against each other, so a disagreement between any two
// silently drops rows rather than reporting anything wrong.
func typeOIDForColumn(t sqle.Type) uint32 {
	name := DefaultUDTName
	if t != nil {
		name = UDTName(t.String())
	}
	for _, pgType := range pgTypes {
		if pgType.Name == name {
			return pgType.OID
		}
	}
	return textOID
}

// textOID is pg_type's OID for text, the fallback every unrecognised type
// reports (see DefaultUDTName).
const textOID = 25

// sqlNameByUDTName gives the name format_type renders for each type this
// catalogue can name.
//
// These are the SQL-standard spellings PostgreSQL's own format_type returns,
// which differ from the catalogue names in pg_type: a column of typname
// "int8" is reported as "bigint", and "varchar" as "character varying".
// gorm.io/driver/postgres parses this string to decide whether a column needs
// altering, so the spelling is the interface.
var sqlNameByUDTName = map[string]string{
	"bool":        "boolean",
	"bytea":       "bytea",
	"int8":        "bigint",
	"int2":        "smallint",
	"int4":        "integer",
	"text":        "text",
	"json":        "json",
	"float4":      "real",
	"float8":      "double precision",
	"bpchar":      "character",
	"varchar":     "character varying",
	"date":        "date",
	"time":        "time without time zone",
	"timestamptz": "timestamp with time zone",
	"numeric":     "numeric",
}

// FormatTypeName returns the SQL name PostgreSQL's format_type reports for a
// type OID, or a bare "???" for an OID this catalogue does not serve --
// PostgreSQL's own answer for an unknown type, rather than an error that
// would fail an introspection query wholesale.
func FormatTypeName(oid uint32) string {
	for _, pgType := range pgTypes {
		if pgType.OID != oid {
			continue
		}
		if name, ok := sqlNameByUDTName[pgType.Name]; ok {
			return name
		}
		return pgType.Name
	}
	return "???"
}

// NewFormatType returns the format_type(oid, typmod) function registration.
//
// PostgreSQL's format_type takes the modifier into account -- character
// varying(64) rather than character varying -- but a modifier is only
// meaningful for a type that has one, and QRDB's pg_attribute reports -1
// ("no modifier") for every column: go-mysql-server's schema carries a length
// on its own type rather than as a separate typmod, and inventing one would
// mean reporting a width the engine did not actually declare. A NULL or
// negative modifier is exactly the case PostgreSQL renders without a suffix,
// so the unmodified name is the truthful answer here rather than a
// simplification.
func NewFormatType() sqle.Function {
	return sqle.Function2{
		Name: FormatTypeFunction,
		Fn: func(_ *sqle.Context, oid, typmod sqle.Expression) sqle.Expression {
			return &formatType{oid: oid, typmod: typmod}
		},
	}
}

// formatType is the expression NewFormatType builds.
type formatType struct {
	oid    sqle.Expression
	typmod sqle.Expression
}

var _ sqle.Expression = (*formatType)(nil)

// Resolved reports whether both arguments are resolved.
func (f *formatType) Resolved() bool {
	return f.oid.Resolved() && f.typmod.Resolved()
}

// String renders the call as SQL.
func (f *formatType) String() string {
	return fmt.Sprintf("%s(%s, %s)", FormatTypeFunction, f.oid, f.typmod)
}

// Type returns the function's result type.
func (f *formatType) Type(_ *sqle.Context) sqle.Type { return types.LongText }

// IsNullable reports that the function can return NULL, which it does for a
// NULL OID.
func (f *formatType) IsNullable(_ *sqle.Context) bool { return true }

// Children returns the argument expressions.
func (f *formatType) Children() []sqle.Expression { return []sqle.Expression{f.oid, f.typmod} }

// WithChildren returns a copy with the given arguments.
func (f *formatType) WithChildren(_ *sqle.Context, children ...sqle.Expression) (sqle.Expression, error) {
	if len(children) != 2 {
		return nil, sqle.ErrInvalidChildrenNumber.New(f, len(children), 2)
	}
	return &formatType{oid: children[0], typmod: children[1]}, nil
}

// Eval renders the type name for the OID the first argument evaluates to.
func (f *formatType) Eval(ctx *sqle.Context, row sqle.Row) (interface{}, error) {
	value, err := f.oid.Eval(ctx, row)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}

	// The second argument is evaluated for its side effects and errors only:
	// see NewFormatType for why its value cannot change the answer here.
	if _, err := f.typmod.Eval(ctx, row); err != nil {
		return nil, err
	}

	oid, _, err := types.Uint32.Convert(ctx, value)
	if err != nil {
		return nil, err
	}
	converted, ok := oid.(uint32)
	if !ok {
		return nil, fmt.Errorf("%s: cannot read %v as a type OID", FormatTypeFunction, value)
	}
	return FormatTypeName(converted), nil
}

// attributeSchema is the subset of PostgreSQL's pg_attribute that schema
// introspection reads.
var attributeSchema = sqle.Schema{
	{Name: "attrelid", Type: oidType, Nullable: false, Source: AttributeRelation},
	{Name: "attname", Type: types.LongText, Nullable: false, Source: AttributeRelation},
	{Name: "atttypid", Type: oidType, Nullable: false, Source: AttributeRelation},
	{Name: "atttypmod", Type: types.Int32, Nullable: false, Source: AttributeRelation},
	{Name: "attnum", Type: types.Int16, Nullable: false, Source: AttributeRelation},
	{Name: "attnotnull", Type: types.Boolean, Nullable: false, Source: AttributeRelation},
	{Name: "attisdropped", Type: types.Boolean, Nullable: false, Source: AttributeRelation},
}
