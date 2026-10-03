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
	sqle "github.com/dolthub/go-mysql-server/sql"
)

// indexColumnsRows serves the qrdb_index_columns relation: one row per
// column of every index in the CURRENT database that PostgreSQL's
// pg_index-based introspection would report -- gorm.io/driver/postgres's
// Migrator.GetIndexes runs a pg_index/pg_class/pg_attribute/pg_constraint
// join (with an `= ANY(indkey)` int2vector membership test) that this
// engine cannot execute, so pkg/sql/pgsql rewrites that exact query onto
// this relation instead.
//
// The rows mirror the upstream query's own filters:
//
//   - the primary key never appears (it is constraint-backed; upstream's
//     `con.oid IS NULL` excludes it),
//   - a unique index DECLARED as a constraint never appears for the same
//     reason (see statementIndexReporter -- in PostgreSQL such an index is
//     owned by its pg_constraint row),
//   - an index generated to back a foreign key never appears, because
//     PostgreSQL does not create indexes for foreign keys at all,
//   - everything else -- plain indexes and statement-created unique
//     indexes -- appears once per covered column.
//
// A table whose indexes cannot be read, or that is not index-addressable,
// is skipped rather than failing the scan, matching pg_indexes above.
func indexColumnsRows(ctx *sqle.Context, cat sqle.Catalog) ([]sqle.Row, error) {
	dbName := ctx.GetCurrentDatabase()
	db, err := cat.Database(ctx, dbName)
	if err != nil {
		return nil, nil
	}
	tableNames, err := db.GetTableNames(ctx)
	if err != nil {
		return nil, nil
	}

	var rows []sqle.Row
	for _, tableName := range tableNames {
		table, _, err := cat.DatabaseTable(ctx, db, tableName)
		if err != nil {
			continue
		}
		indexed, ok := table.(sqle.IndexAddressable)
		if !ok {
			continue
		}
		indexes, err := indexed.GetIndexes(ctx)
		if err != nil {
			continue
		}
		reporter, _ := table.(statementIndexReporter)
		fkReporter, _ := table.(foreignKeyIndexReporter)

		for _, index := range indexes {
			if index.ID() == primaryIndexID {
				continue
			}
			if index.IsUnique() {
				if reporter == nil {
					continue
				}
				statementIndex, known := reporter.IsStatementIndex(index.ID())
				if !known || !statementIndex {
					continue
				}
			}
			if fkReporter != nil {
				if backsFK, known := fkReporter.IsForeignKeyIndex(index.ID()); known && backsFK {
					continue
				}
			}
			for _, column := range indexColumnNames(index, table.Name()) {
				rows = append(rows, sqle.Row{
					table.Name(),     // table_name
					index.ID(),       // index_name
					index.IsUnique(), // non_unique (pg_index.indisunique: TRUE means unique)
					false,            // primary (constraint-backed rows are excluded above)
					column,           // column_name
				})
			}
		}
	}
	return rows, nil
}

// foreignKeyIndexReporter is implemented by tables that can say whether a
// named index was generated to back a foreign key constraint.
type foreignKeyIndexReporter interface {
	IsForeignKeyIndex(name string) (backsForeignKey bool, ok bool)
}
