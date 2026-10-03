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

// Package infoschema supplies the information_schema views QRDB serves that
// go-mysql-server does not.
//
// information_schema is go-mysql-server's own database -- the analyzer's
// catalog resolves the name to its built-in instance and never consults the
// DatabaseProvider -- so a view cannot be contributed the way pkg/sql/pgcatalog
// contributes the pg_catalog schema. go-mysql-server exposes
// information_schema.NewInformationSchemaTablesToAdd for exactly this purpose,
// documented as the hook Doltgres injects PostgreSQL-specific tables and views
// through, and that is the seam used here.
package infoschema

import (
	"strings"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/information_schema"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/sqltypes"
)

// ConstraintColumnUsageTableName is the name of the view this package adds.
const ConstraintColumnUsageTableName = "constraint_column_usage"

// nameColumnType matches the type go-mysql-server declares for every
// identifier-valued column in information_schema. The view is joined against
// those tables by name, so its columns have to compare against theirs without
// a coercion in between.
var nameColumnType = types.MustCreateString(sqltypes.VarChar, 64, sqle.Collation_Information_Schema_Default)

// constraintColumnUsageSchema is the SQL-standard shape of the view: one row
// per (constraint, column) pair.
var constraintColumnUsageSchema = sqle.Schema{
	{Name: "CONSTRAINT_CATALOG", Type: nameColumnType, Nullable: true, Source: ConstraintColumnUsageTableName},
	{Name: "CONSTRAINT_SCHEMA", Type: nameColumnType, Nullable: true, Source: ConstraintColumnUsageTableName},
	{Name: "CONSTRAINT_NAME", Type: nameColumnType, Nullable: true, Source: ConstraintColumnUsageTableName},
	{Name: "TABLE_CATALOG", Type: nameColumnType, Nullable: true, Source: ConstraintColumnUsageTableName},
	{Name: "TABLE_SCHEMA", Type: nameColumnType, Nullable: true, Source: ConstraintColumnUsageTableName},
	{Name: "TABLE_NAME", Type: nameColumnType, Nullable: true, Source: ConstraintColumnUsageTableName},
	{Name: "COLUMN_NAME", Type: nameColumnType, Nullable: true, Source: ConstraintColumnUsageTableName},
}

// Register adds constraint_column_usage to the set of tables every
// information_schema database built afterwards will serve. It is idempotent,
// and must run before the engine whose catalog should carry the view is
// constructed: NewInformationSchemaDatabase copies this map once, at
// construction.
//
// # Why the view is needed
//
// It is how a PostgreSQL client asks which columns a constraint covers.
// gorm.io/driver/postgres's Migrator resolves a table's primary-key and unique
// constraints by joining information_schema.table_constraints against this view
// and information_schema.columns, so without it every such migration failed
// with "table not found: constraint_column_usage" -- 29 tests of the upstream
// GORM suite, the second-largest cause of failure on the PostgreSQL wire.
//
// # Why it belongs in information_schema rather than pg_catalog
//
// constraint_column_usage is SQL-standard, not a PostgreSQL extension.
// PostgreSQL implements it; MySQL does not, offering the overlapping
// key_column_usage instead. Adding it is therefore filling in the standard
// namespace rather than teaching MySQL's namespace a foreign concept, which is
// also why registering it process-wide is harmless: a MySQL client that finds
// it finds a standard view holding a projection of data key_column_usage
// already exposes.
func Register() {
	information_schema.NewInformationSchemaTablesToAdd[ConstraintColumnUsageTableName] = &information_schema.InformationSchemaTable{
		TableName:   ConstraintColumnUsageTableName,
		TableSchema: constraintColumnUsageSchema,
		Reader:      constraintColumnUsageRowIter,
	}
}

// constraintColumnUsageRowIter computes the view's rows from the catalog.
//
// Only PRIMARY KEY and UNIQUE constraints are reported. That is not a
// simplification: PostgreSQL's view lists, for a FOREIGN KEY, the columns of
// the constraint it REFERENCES rather than the referencing columns, and the
// referenced side is identified there by a unique constraint this loop cannot
// name from an sql.ForeignKeyConstraint alone. Emitting the referencing
// columns instead would be a plausible-looking wrong answer; omitting foreign
// keys is a true partial one, and no caller of this view asks for them --
// GORM's Migrator filters to PRIMARY KEY and UNIQUE in the query itself.
//
// The catalog and schema names come from
// information_schema.AllDatabasesWithNames, the same source go-mysql-server's
// own key_column_usage uses, so the two agree on every value a client joins
// them by -- including the "def" catalog placeholder MySQL uses where
// PostgreSQL would name the database.
func constraintColumnUsageRowIter(ctx *sqle.Context, catalog sqle.Catalog) (sqle.RowIter, error) {
	databases, err := information_schema.AllDatabasesWithNames(ctx, catalog, false)
	if err != nil {
		return nil, err
	}

	var rows []sqle.Row
	for _, db := range databases {
		tableNames, err := db.Database.GetTableNames(ctx)
		if err != nil {
			return nil, err
		}

		for _, tableName := range tableNames {
			table, _, err := catalog.DatabaseTable(ctx, db.Database, tableName)
			if err != nil {
				return nil, err
			}

			indexed, ok := table.(sqle.IndexAddressable)
			if !ok {
				continue
			}
			indexes, err := indexed.GetIndexes(ctx)
			if err != nil {
				return nil, err
			}

			for _, index := range indexes {
				if index.ID() != "PRIMARY" && !index.IsUnique() {
					continue
				}
				for _, column := range indexColumnNames(index, table.Name()) {
					rows = append(rows, sqle.Row{
						db.CatalogName, // constraint_catalog
						db.SchemaName,  // constraint_schema
						index.ID(),     // constraint_name
						db.CatalogName, // table_catalog
						db.SchemaName,  // table_schema
						table.Name(),   // table_name
						column,         // column_name
					})
				}
			}
		}
	}

	return sqle.RowsToRowIter(rows...), nil
}

// indexColumnNames returns the bare column names an index covers.
//
// sql.Index.Expressions reports each column qualified by its table
// ("users.id") and, for an expression index, something that is not a column
// reference at all. Only the qualified-column form is meaningful here, so a
// prefix that does not match this table is left whole rather than truncated at
// an arbitrary dot -- a column name is never invented.
func indexColumnNames(index sqle.Index, tableName string) []string {
	prefix := tableName + "."
	expressions := index.Expressions()
	names := make([]string, 0, len(expressions))
	for _, expression := range expressions {
		names = append(names, strings.TrimPrefix(expression, prefix))
	}
	return names
}
