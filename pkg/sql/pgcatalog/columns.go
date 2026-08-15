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
	"strings"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/information_schema"
	"github.com/dolthub/go-mysql-server/sql/types"
)

// Positions within a go-mysql-server information_schema.columns row that
// this package reads or rewrites. They are indices into
// information_schema's own columnsSchema and are checked against it at
// startup by mysqlColumnCount below plus TestColumnsSchemaLayout.
const (
	tableCatalogIdx     = 0
	tableSchemaIdx      = 1
	dataTypeIdx         = 7
	numericPrecisionIdx = 10
	extraIdx            = 17
)

// autoIncrementExtra is the value go-mysql-server writes into
// information_schema.columns.EXTRA for a column declared AUTO_INCREMENT.
// It is the only record that a column's values are generated, which is what
// identity_increment reports on this surface.
const autoIncrementExtra = "auto_increment"

// identityIncrement is the step reported for a generated column. MySQL's
// AUTO_INCREMENT has no configurable per-column step (auto_increment_increment
// is a session variable, not column metadata), so the step of a column
// created through this surface is always one.
const identityIncrement = "1"

// postgresColumns are the three columns PostgreSQL's
// information_schema.columns carries and MySQL's does not, appended to
// go-mysql-server's MySQL-shaped view of the same table.
//
// Appending rather than replacing is deliberate. Every existing column
// keeps its position and its meaning, so this view is a strict superset:
// invisible to a client that selects columns by name -- which every real
// client does, including gorm -- and visible only to SELECT *. That is the
// whole cost of serving one information_schema that answers both dialects'
// introspection queries.
var postgresColumns = sqle.Schema{
	{
		// The PostgreSQL catalogue name of the column's type -- int8, not
		// bigint. It is the column PostgreSQL clients read a column's type
		// from, and the one they join to pg_type.typname.
		Name:     "UDT_NAME",
		Type:     types.LongText,
		Nullable: false,
		Source:   information_schema.ColumnsTableName,
	},
	{
		// 10 for a column whose precision is a count of decimal digits, 2
		// for one whose precision is a count of bits, NULL for a
		// non-numeric column. See numericRadixByUDTName.
		Name:     "NUMERIC_PRECISION_RADIX",
		Type:     types.Uint32,
		Nullable: true,
		Source:   information_schema.ColumnsTableName,
	},
	{
		// The step between generated values for an identity column, NULL
		// for a column whose values are not generated. A PostgreSQL client
		// reads a non-NULL value here as "this column is auto-incrementing".
		Name:     "IDENTITY_INCREMENT",
		Type:     types.LongText,
		Nullable: true,
		Source:   information_schema.ColumnsTableName,
	},
}

// mysqlColumnCount is the width of go-mysql-server's own
// information_schema.columns row, captured from its own schema rather than
// written down, so this package cannot drift from it silently.
var mysqlColumnCount = len(information_schema.NewColumnsTable().TableSchema)

// InfoSchema returns the information_schema database to install on a
// PostgreSQL-dialect engine's catalog: go-mysql-server's own, with its
// columns table replaced by the PostgreSQL-shaped one this package builds.
//
// Everything else in information_schema is served unchanged, so a
// PostgreSQL client's queries against TABLES, TABLE_CONSTRAINTS,
// KEY_COLUMN_USAGE and the rest behave exactly as they do on the MySQL
// endpoint.
func InfoSchema() sqle.Database {
	return infoSchema{Database: information_schema.NewInformationSchemaDatabase()}
}

// Compile-time interface assertion.
var _ sqle.Database = (*infoSchema)(nil)

// infoSchema is go-mysql-server's information_schema with one table
// overridden.
//
// The embedded field's static type is the sqle.Database interface, which is
// the entire interface go-mysql-server's own informationSchemaDatabase
// implements, so wrapping it loses no capability -- the wrapper is exactly
// as capable as what it wraps.
type infoSchema struct {
	sqle.Database
}

// GetTableInsensitive serves the PostgreSQL-shaped columns table and
// delegates every other name to go-mysql-server.
func (d infoSchema) GetTableInsensitive(ctx *sqle.Context, name string) (sqle.Table, bool, error) {
	if strings.EqualFold(name, information_schema.ColumnsTableName) {
		return NewColumnsTable(), true, nil
	}
	if strings.EqualFold(name, ConstraintColumnUsageTableName) {
		return NewConstraintColumnUsageTable(), true, nil
	}
	return d.Database.GetTableInsensitive(ctx, name)
}

// NewColumnsTable returns information_schema.columns with the three
// PostgreSQL-only columns appended and TABLE_CATALOG corrected for
// PostgreSQL's naming of the same level (see extendRow).
//
// It builds on go-mysql-server's own table rather than reimplementing it:
// ColumnsTable exposes both its schema and its row iterator as fields
// precisely so a PostgreSQL front end can extend them, so the whole of
// go-mysql-server's column-metadata derivation -- privileges, key
// classification, column defaults, views -- is reused untouched and only
// the three values it cannot know are added.
func NewColumnsTable() *information_schema.ColumnsTable {
	tbl := information_schema.NewColumnsTable()
	mysqlRowIter := tbl.RowIter

	// Copy rather than append in place: TableSchema aliases
	// go-mysql-server's package-level columnsSchema, and appending to it
	// could write through to the slice every MySQL engine in this process
	// is also serving.
	schema := make(sqle.Schema, 0, len(tbl.TableSchema)+len(postgresColumns))
	schema = append(schema, tbl.TableSchema...)
	schema = append(schema, postgresColumns...)
	tbl.TableSchema = schema

	tbl.RowIter = func(ctx *sqle.Context, cat sqle.Catalog, allCols sqle.Schema) (sqle.RowIter, error) {
		inner, err := mysqlRowIter(ctx, cat, allCols)
		if err != nil {
			return nil, err
		}
		return &columnsIter{inner: inner}, nil
	}
	return tbl
}

// Compile-time interface assertion.
var _ sqle.RowIter = (*columnsIter)(nil)

// columnsIter extends each of go-mysql-server's information_schema.columns
// rows as it is read.
type columnsIter struct {
	inner sqle.RowIter
}

// Next returns the next extended row.
func (it *columnsIter) Next(ctx *sqle.Context) (sqle.Row, error) {
	row, err := it.inner.Next(ctx)
	if err != nil {
		return nil, err
	}
	return extendRow(row)
}

// Close releases the underlying iterator.
func (it *columnsIter) Close(ctx *sqle.Context) error {
	return it.inner.Close(ctx)
}

// extendRow rewrites TABLE_CATALOG and appends the three PostgreSQL-only
// values to one information_schema.columns row.
//
// TABLE_CATALOG is rewritten because MySQL and PostgreSQL disagree about
// what it holds, not about where it points. MySQL has no catalogue level
// above the schema and writes the constant "def"; PostgreSQL's catalogue is
// the database, and a client filters on `table_catalog = <the value
// current_database() returned>`. On this surface current_database() and
// current_schema() both resolve to the same name (pkg/sql/engine's
// translator maps both to DATABASE(), because a PostgreSQL schema and a
// go-mysql-server database are the same level of the hierarchy here), so
// reporting TABLE_SCHEMA's value is what makes that filter select the rows
// it is asking for instead of none. Leaving "def" in place would not
// produce a wrong answer -- it would produce an empty one, which a client
// reads as "this table has no columns".
func extendRow(row sqle.Row) (sqle.Row, error) {
	if len(row) != mysqlColumnCount {
		return nil, &RowWidthError{Want: mysqlColumnCount, Got: len(row)}
	}

	extended := make(sqle.Row, 0, len(row)+len(postgresColumns))
	extended = append(extended, row...)
	extended[tableCatalogIdx] = row[tableSchemaIdx]

	dataType, _ := row[dataTypeIdx].(string)
	udtName := UDTName(dataType)

	var radix interface{}
	// A radix describes how a precision is counted, so it is reported for
	// exactly those columns that report a precision -- PostgreSQL leaves
	// both NULL together.
	if r, ok := NumericRadix(udtName); ok && row[numericPrecisionIdx] != nil {
		radix = r
	}

	var increment interface{}
	if extra, ok := row[extraIdx].(string); ok && strings.Contains(strings.ToLower(extra), autoIncrementExtra) {
		increment = identityIncrement
	}

	return append(extended, udtName, radix, increment), nil
}
