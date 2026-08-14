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
	"github.com/dolthub/go-mysql-server/sql/types"
)

// primaryIndexID is the identifier go-mysql-server gives a table's primary
// key. PostgreSQL has no reserved name for one, so it is reported under the
// conventional <table>_pkey spelling every PostgreSQL tool expects.
const primaryIndexID = "PRIMARY"

// indexesRows returns pg_indexes: one row per index on every table the
// engine serves, which is how a PostgreSQL client asks whether an index
// exists. gorm.io/driver/postgres's Migrator.HasIndex issues
//
//	SELECT count(*) FROM pg_indexes
//	WHERE tablename = $1 AND indexname = $2 AND schemaname = CURRENT_SCHEMA()
//
// and reads a zero count as "create it", so without this relation every
// index migration either failed outright or would have re-created an index
// that already existed.
//
// A database whose table list cannot be read is skipped rather than failing
// the scan, for the same reason classRows skips one: a single unavailable
// database must not make index introspection fail for every other.
func indexesRows(ctx *sqle.Context, cat sqle.Catalog) ([]sqle.Row, error) {
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
			indexed, ok := table.(sqle.IndexAddressable)
			if !ok {
				continue
			}
			indexes, err := indexed.GetIndexes(ctx)
			if err != nil {
				continue
			}

			for _, index := range indexes {
				rows = append(rows, sqle.Row{
					dbName,                         // schemaname
					table.Name(),                   // tablename
					indexName(index, table.Name()), // indexname
					nil,                            // tablespace
					indexDefinition(index, dbName, table.Name()), // indexdef
				})
			}
		}
	}
	return rows, nil
}

// indexName returns the name a PostgreSQL client knows an index by.
//
// go-mysql-server calls a table's primary key "PRIMARY", which is MySQL's
// spelling. PostgreSQL names it after the table it belongs to, and that is
// the name a client compares against -- gorm.io/driver/postgres derives
// exactly "<table>_pkey" when it asks whether a primary key exists.
func indexName(index sqle.Index, tableName string) string {
	if index.ID() == primaryIndexID {
		return tableName + "_pkey"
	}
	return index.ID()
}

// indexDefinition reconstructs the CREATE INDEX statement pg_indexes reports
// in its indexdef column.
//
// It is rebuilt from the index's own metadata rather than stored, because
// nothing in the engine keeps the original statement text. The result is
// therefore a faithful description of the index as it exists, which is what
// the column is for, and not necessarily the statement a client typed.
func indexDefinition(index sqle.Index, dbName, tableName string) string {
	var b strings.Builder
	b.WriteString("CREATE ")
	if index.IsUnique() {
		b.WriteString("UNIQUE ")
	}
	b.WriteString("INDEX ")
	b.WriteString(indexName(index, tableName))
	b.WriteString(" ON ")
	b.WriteString(dbName)
	b.WriteString(".")
	b.WriteString(tableName)
	b.WriteString(" USING btree (")
	b.WriteString(strings.Join(indexColumnNames(index, tableName), ", "))
	b.WriteString(")")
	return b.String()
}

// indexColumnNames returns the bare column names an index covers.
//
// sql.Index.Expressions reports each column qualified by its table
// ("users.id"). A prefix that does not match this table is left whole rather
// than truncated at an arbitrary dot, so an expression index is described by
// whatever it really is instead of having a column name invented for it.
func indexColumnNames(index sqle.Index, tableName string) []string {
	prefix := tableName + "."
	expressions := index.Expressions()
	names := make([]string, 0, len(expressions))
	for _, expression := range expressions {
		names = append(names, strings.TrimPrefix(expression, prefix))
	}
	return names
}

// indexesSchema is the shape PostgreSQL gives pg_indexes.
var indexesSchema = sqle.Schema{
	{Name: "schemaname", Type: types.LongText, Nullable: false, Source: IndexesRelation},
	{Name: "tablename", Type: types.LongText, Nullable: false, Source: IndexesRelation},
	{Name: "indexname", Type: types.LongText, Nullable: false, Source: IndexesRelation},
	{Name: "tablespace", Type: types.LongText, Nullable: true, Source: IndexesRelation},
	{Name: "indexdef", Type: types.LongText, Nullable: false, Source: IndexesRelation},
}
