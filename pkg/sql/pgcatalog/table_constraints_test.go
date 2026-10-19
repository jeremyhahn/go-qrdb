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
	"io"
	"testing"

	gms "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/memory"
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/information_schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTableConstraintsSchemaLayout pins the row positions this package's
// filter reads against go-mysql-server's actual table_constraints schema,
// so an upstream reordering fails here rather than silently filtering the
// wrong column.
func TestTableConstraintsSchemaLayout(t *testing.T) {
	db := information_schema.NewInformationSchemaDatabase()
	ctx := sqle.NewEmptyContext()
	tbl, found, err := db.GetTableInsensitive(ctx, information_schema.TableConstraintsTableName)
	require.NoError(t, err)
	require.True(t, found)
	schema := tbl.Schema(ctx)
	require.Greater(t, len(schema), tcConstraintTypeIdx)
	assert.Equal(t, "CONSTRAINT_NAME", schema[tcConstraintNameIdx].Name)
	assert.Equal(t, "TABLE_SCHEMA", schema[tcTableSchemaIdx].Name)
	assert.Equal(t, "TABLE_NAME", schema[tcTableNameIdx].Name)
	assert.Equal(t, "CONSTRAINT_TYPE", schema[tcConstraintTypeIdx].Name)
}

// fakeStatementIndexTable is a memory table that answers, from fixed maps,
// the two questions qrdb's own tables answer about an index: whether a
// CREATE INDEX statement made it, and whether it backs a foreign key.
type fakeStatementIndexTable struct {
	*memory.Table
	statementIndexes  map[string]bool
	foreignKeyIndexes map[string]bool
}

var (
	_ statementIndexReporter  = (*fakeStatementIndexTable)(nil)
	_ foreignKeyIndexReporter = (*fakeStatementIndexTable)(nil)
	_ sqle.IndexAddressable   = (*fakeStatementIndexTable)(nil)
)

func (f *fakeStatementIndexTable) IsStatementIndex(name string) (bool, bool) {
	v, ok := f.statementIndexes[name]
	return v, ok
}

func (f *fakeStatementIndexTable) IsForeignKeyIndex(name string) (bool, bool) {
	v, ok := f.foreignKeyIndexes[name]
	return v, ok
}

// reportingDatabase is a memory database that serves each of its tables as a
// fakeStatementIndexTable, so a catalog lookup hands this package's filters a
// table that can say where its indexes came from.
type reportingDatabase struct {
	*memory.Database
	statementIndexes  map[string]bool
	foreignKeyIndexes map[string]bool
}

func (d reportingDatabase) GetTableInsensitive(ctx *sqle.Context, name string) (sqle.Table, bool, error) {
	tbl, found, err := d.Database.GetTableInsensitive(ctx, name)
	if err != nil || !found {
		return tbl, found, err
	}
	mt, ok := tbl.(*memory.Table)
	if !ok {
		return tbl, found, nil
	}
	return &fakeStatementIndexTable{
		Table:             mt,
		statementIndexes:  d.statementIndexes,
		foreignKeyIndexes: d.foreignKeyIndexes,
	}, true, nil
}

const (
	// reportingDBName is the schema whose tables report their indexes.
	reportingDBName = "reporting"
	// plainDBName is a schema of plain memory tables, which cannot.
	plainDBName = "plain"
)

// newReportingEngine returns an engine over two schemas holding the same
// shapes of index: reportingDBName, whose tables say which unique indexes a
// statement made (stmt_email), which were declared (decl_code), which they
// do not know (unknown_nick) and which back a foreign key (fk_person), and
// plainDBName, whose tables say nothing. go-mysql-server's memory DDL needs
// the concrete *memory.Database, so the schema is built through a plain
// engine and the returned engine serves the same databases.
func newReportingEngine(t *testing.T) (*gms.Engine, *sqle.Context) {
	t.Helper()
	reportingDB := memory.NewDatabase(reportingDBName)
	plainDB := memory.NewDatabase(plainDBName)
	setup, ctx := newPGEngine(reportingDB, plainDB)
	ctx.SetCurrentDatabase(reportingDBName)

	for _, statement := range []string{
		"CREATE TABLE people (id BIGINT PRIMARY KEY, email VARCHAR(64), code VARCHAR(64), nick VARCHAR(64), name VARCHAR(64))",
		"CREATE UNIQUE INDEX stmt_email ON people (email)",
		"CREATE UNIQUE INDEX decl_code ON people (code)",
		"CREATE UNIQUE INDEX unknown_nick ON people (nick)",
		"CREATE INDEX idx_name ON people (name)",
		"CREATE TABLE orders (id BIGINT PRIMARY KEY, person_id BIGINT, note VARCHAR(64))",
		"CREATE INDEX fk_person ON orders (person_id)",
		"CREATE INDEX idx_note ON orders (note)",
		"CREATE TABLE plain.people (id BIGINT PRIMARY KEY, email VARCHAR(64), name VARCHAR(64))",
		"CREATE UNIQUE INDEX plain_email ON plain.people (email)",
		"CREATE INDEX plain_name ON plain.people (name)",
	} {
		queryRows(t, setup, ctx, statement)
	}

	eng, _ := newPGEngine(reportingDatabase{
		Database:          reportingDB,
		statementIndexes:  map[string]bool{"stmt_email": true, "decl_code": false},
		foreignKeyIndexes: map[string]bool{"fk_person": true, "idx_note": false},
	}, plainDB)
	return eng, ctx
}

// tableConstraintsRow builds a table_constraints row in go-mysql-server's
// layout, pinned by TestTableConstraintsSchemaLayout.
func tableConstraintsRow(schema, table, constraint, constraintType string) sqle.Row {
	return sqle.Row{"def", schema, constraint, schema, table, constraintType, "YES"}
}

// TestTableConstraintsIterFiltering pins the filter itself: UNIQUE rows
// whose index is statement-created disappear; declared constraints, other
// constraint types, and unresolvable rows all pass through.
func TestTableConstraintsIterFiltering(t *testing.T) {
	eng, ctx := newReportingEngine(t)
	it := &tableConstraintsIter{catalog: eng.Analyzer.Catalog}

	cases := []struct {
		name string
		row  sqle.Row
		want bool
	}{
		{"statement-created unique index is hidden", tableConstraintsRow(reportingDBName, "people", "stmt_email", "UNIQUE"), true},
		{"constraint type matches case-insensitively", tableConstraintsRow(reportingDBName, "people", "stmt_email", "unique"), true},
		{"declared unique constraint passes", tableConstraintsRow(reportingDBName, "people", "decl_code", "UNIQUE"), false},
		{"index the table does not know passes", tableConstraintsRow(reportingDBName, "people", "unknown_nick", "UNIQUE"), false},
		{"non-unique rows pass", tableConstraintsRow(reportingDBName, "people", "PRIMARY", "PRIMARY KEY"), false},
		{"short rows pass", sqle.Row{"def"}, false},
		{"row without a table name passes", tableConstraintsRow(reportingDBName, "", "stmt_email", "UNIQUE"), false},
		{"unknown schema passes", tableConstraintsRow("missing", "people", "stmt_email", "UNIQUE"), false},
		{"unknown table passes", tableConstraintsRow(reportingDBName, "missing", "stmt_email", "UNIQUE"), false},
		{"table that cannot report passes", tableConstraintsRow(plainDBName, "people", "plain_email", "UNIQUE"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, it.isStatementIndexRow(ctx, tc.row))
		})
	}
}

// TestTableConstraintsIterSkipsStatementIndexRows drives the iterator over a
// fixed inner iterator: the statement-created row is skipped, the others
// arrive in order, the inner end-of-rows is returned as is, and Close
// reaches the inner iterator.
func TestTableConstraintsIterSkipsStatementIndexRows(t *testing.T) {
	eng, ctx := newReportingEngine(t)
	primary := tableConstraintsRow(reportingDBName, "people", "PRIMARY", "PRIMARY KEY")
	declared := tableConstraintsRow(reportingDBName, "people", "decl_code", "UNIQUE")
	it := &tableConstraintsIter{
		inner: sqle.RowsToRowIter(
			primary,
			tableConstraintsRow(reportingDBName, "people", "stmt_email", "UNIQUE"),
			declared,
		),
		catalog: eng.Analyzer.Catalog,
	}

	row, err := it.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, primary, row)

	row, err = it.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, declared, row, "the statement-created row is skipped")

	_, err = it.Next(ctx)
	require.ErrorIs(t, err, io.EOF)
	require.NoError(t, it.Close(ctx))
}

// TestTableConstraintsHidesStatementIndexesThroughAQuery runs the wrapper the
// way a client reaches it, through information_schema.table_constraints.
func TestTableConstraintsHidesStatementIndexesThroughAQuery(t *testing.T) {
	eng, ctx := newReportingEngine(t)

	rows := queryRows(t, eng, ctx,
		"SELECT constraint_name, constraint_type FROM information_schema.table_constraints "+
			"WHERE table_schema = 'reporting' AND table_name = 'people'")
	assert.ElementsMatch(t, []sqle.Row{
		{"PRIMARY", "PRIMARY KEY"},
		{"decl_code", "UNIQUE"},
		{"unknown_nick", "UNIQUE"},
	}, rows)
}

// TestTableConstraintsReaderPropagatesAFailure covers the wrapper's reader
// when go-mysql-server's own reader fails: the failure is returned, not an
// iterator that reports no rows.
func TestTableConstraintsReaderPropagatesAFailure(t *testing.T) {
	want := &unreadableDatabaseError{Name: "broken"}
	wrapped := newTableConstraintsTable(&information_schema.InformationSchemaTable{
		Reader: func(*sqle.Context, sqle.Catalog) (sqle.RowIter, error) { return nil, want },
	})

	iter, err := wrapped.Reader(sqle.NewEmptyContext(), nil)
	assert.Nil(t, iter)
	require.ErrorIs(t, err, want)
}
