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
)

// Positions within go-mysql-server's information_schema.table_constraints
// row this filter reads, checked against its schema by
// TestTableConstraintsSchemaLayout.
const (
	tcConstraintNameIdx = 2
	tcTableSchemaIdx    = 3
	tcTableNameIdx      = 4
	tcConstraintTypeIdx = 5
)

// statementIndexReporter is implemented by tables that can say whether a
// named unique index was created by a standalone CREATE INDEX statement
// (true) or declared as a constraint (false). pkg/sql/provider's
// RelationalTable implements it from the catalog's per-index record.
type statementIndexReporter interface {
	IsStatementIndex(name string) (statementIndex bool, ok bool)
}

// newTableConstraintsTable wraps go-mysql-server's own table_constraints
// with PostgreSQL's constraint semantics: a unique INDEX -- one created by
// a standalone CREATE INDEX statement -- is NOT a constraint there and
// never appears in this view, while MySQL (whose semantics the inner table
// implements) lists every unique index. gorm.io/driver/postgres drives
// real DDL off this difference: a unique index reported here reads as a
// unique CONSTRAINT, and the migrator then issues DROP CONSTRAINT for a
// name that never existed.
//
// inner must be go-mysql-server's *information_schema.InformationSchemaTable
// for table_constraints; the wrapper reuses its schema and reader wholesale
// and only filters rows.
func newTableConstraintsTable(inner *information_schema.InformationSchemaTable) *information_schema.InformationSchemaTable {
	wrapped := *inner
	innerReader := inner.Reader
	wrapped.Reader = func(ctx *sqle.Context, cat sqle.Catalog) (sqle.RowIter, error) {
		it, err := innerReader(ctx, cat)
		if err != nil {
			return nil, err
		}
		return &tableConstraintsIter{inner: it, catalog: cat}, nil
	}
	return &wrapped
}

// tableConstraintsIter filters UNIQUE rows whose backing index is a
// statement-created index rather than a declared constraint.
type tableConstraintsIter struct {
	inner   sqle.RowIter
	catalog sqle.Catalog
}

// Compile-time interface assertion.
var _ sqle.RowIter = (*tableConstraintsIter)(nil)

// Next returns the next constraint row, skipping statement-created unique
// indexes.
func (it *tableConstraintsIter) Next(ctx *sqle.Context) (sqle.Row, error) {
	for {
		row, err := it.inner.Next(ctx)
		if err != nil {
			return nil, err
		}
		if it.isStatementIndexRow(ctx, row) {
			continue
		}
		return row, nil
	}
}

// Close releases the underlying iterator.
func (it *tableConstraintsIter) Close(ctx *sqle.Context) error {
	return it.inner.Close(ctx)
}

// isStatementIndexRow reports whether row describes a UNIQUE "constraint"
// that is really a statement-created index. Any failure to resolve the
// table answers false: the row then stays visible, which is the MySQL
// answer this view served before the filter existed, rather than silently
// hiding a constraint over a lookup hiccup.
func (it *tableConstraintsIter) isStatementIndexRow(ctx *sqle.Context, row sqle.Row) bool {
	if len(row) <= tcConstraintTypeIdx {
		return false
	}
	constraintType, _ := row[tcConstraintTypeIdx].(string)
	if !strings.EqualFold(constraintType, "UNIQUE") {
		return false
	}
	schemaName, _ := row[tcTableSchemaIdx].(string)
	tableName, _ := row[tcTableNameIdx].(string)
	constraintName, _ := row[tcConstraintNameIdx].(string)
	if schemaName == "" || tableName == "" || constraintName == "" {
		return false
	}

	db, err := it.catalog.Database(ctx, schemaName)
	if err != nil {
		return false
	}
	table, found, err := db.GetTableInsensitive(ctx, tableName)
	if err != nil || !found {
		return false
	}
	reporter, ok := table.(statementIndexReporter)
	if !ok {
		return false
	}
	statementIndex, ok := reporter.IsStatementIndex(constraintName)
	return ok && statementIndex
}
