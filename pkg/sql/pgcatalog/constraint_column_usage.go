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
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/information_schema"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/sqltypes"
)

// constraint_column_usage is the SQL-standard view listing the columns a
// constraint covers. PostgreSQL implements it; go-mysql-server does not,
// offering the overlapping key_column_usage instead.
//
// It is served here rather than registered into go-mysql-server's
// information_schema globally, because that database is shared by every
// engine in the process and the MySQL endpoint's information_schema must stay
// byte-for-byte what go-mysql-server ships (see infoSchemaByDialect in
// pkg/sql/engine). The wrapper in columns.go is the dialect-scoped seam, and
// this view goes through it for the same reason the PostgreSQL-shaped columns
// table does.
//
// gorm.io/driver/postgres's Migrator joins this view against
// table_constraints and columns to resolve a table's primary-key and unique
// constraints; without it every such migration failed with
// "table not found: constraint_column_usage".

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

// NewConstraintColumnUsageTable returns the view as a table the
// information_schema wrapper serves.
//
// It is built per call for the same reason Relation is: go-mysql-server
// assigns the engine catalog onto the table it resolves, and two sessions on
// two engines must not share one.
func NewConstraintColumnUsageTable() sqle.Table {
	return &information_schema.InformationSchemaTable{
		TableName:   ConstraintColumnUsageTableName,
		TableSchema: constraintColumnUsageSchema,
		Reader:      constraintColumnUsageRowIter,
	}
}
