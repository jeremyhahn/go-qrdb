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

package infoschema

import (
	"errors"
	"testing"

	"github.com/dolthub/go-mysql-server/memory"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/information_schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errInjected = errors.New("infoschema test: injected failure")

// failingTableNamesDatabase is a database whose table listing fails.
type failingTableNamesDatabase struct {
	sql.Database
	err error
}

func (d failingTableNamesDatabase) GetTableNames(*sql.Context) ([]string, error) {
	return nil, d.err
}

// failingIndexesTable is an index-addressable table whose index listing
// fails with err, or delegates to the wrapped table when err is nil.
type failingIndexesTable struct {
	*memory.Table
	err error
}

func (t failingIndexesTable) GetIndexes(ctx *sql.Context) ([]sql.Index, error) {
	if t.err != nil {
		return nil, t.err
	}
	return t.Table.GetIndexes(ctx)
}

// failingCatalog wraps a real catalog, failing table resolution with
// tableErr, or resolving every table to one whose index listing fails with
// indexesErr.
type failingCatalog struct {
	sql.Catalog
	tableErr   error
	indexesErr error
}

func (c failingCatalog) DatabaseTable(ctx *sql.Context, db sql.Database, tableName string) (sql.Table, sql.Database, error) {
	if c.tableErr != nil {
		return nil, nil, c.tableErr
	}
	table, resolved, err := c.Catalog.DatabaseTable(ctx, db, tableName)
	if err != nil {
		return nil, nil, err
	}
	memTable, ok := table.(*memory.Table)
	if !ok {
		return table, resolved, nil
	}
	return failingIndexesTable{Table: memTable, err: c.indexesErr}, resolved, nil
}

// stubDatabasesWithNames replaces information_schema.AllDatabasesWithNames
// for the duration of the test.
func stubDatabasesWithNames(t *testing.T, stub func(*sql.Context, sql.Catalog, bool) ([]information_schema.DbWithNames, error)) {
	t.Helper()
	original := information_schema.AllDatabasesWithNames
	information_schema.AllDatabasesWithNames = stub
	t.Cleanup(func() { information_schema.AllDatabasesWithNames = original })
}

// TestConstraintColumnUsageRowIterPropagatesErrors drives each catalog
// failure through the row iterator and asserts the iterator returns it
// unchanged rather than a partial view.
func TestConstraintColumnUsageRowIterPropagatesErrors(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, catalog sql.Catalog) sql.Catalog
	}{
		{
			name: "database listing fails",
			prepare: func(t *testing.T, catalog sql.Catalog) sql.Catalog {
				stubDatabasesWithNames(t, func(*sql.Context, sql.Catalog, bool) ([]information_schema.DbWithNames, error) {
					return nil, errInjected
				})
				return catalog
			},
		},
		{
			name: "table listing fails",
			prepare: func(t *testing.T, catalog sql.Catalog) sql.Catalog {
				original := information_schema.AllDatabasesWithNames
				stubDatabasesWithNames(t, func(ctx *sql.Context, cat sql.Catalog, privCheck bool) ([]information_schema.DbWithNames, error) {
					databases, err := original(ctx, cat, privCheck)
					if err != nil {
						return nil, err
					}
					for i := range databases {
						databases[i].Database = failingTableNamesDatabase{Database: databases[i].Database, err: errInjected}
					}
					return databases, nil
				})
				return catalog
			},
		},
		{
			name: "table resolution fails",
			prepare: func(_ *testing.T, catalog sql.Catalog) sql.Catalog {
				return failingCatalog{Catalog: catalog, tableErr: errInjected}
			},
		},
		{
			name: "index listing fails",
			prepare: func(_ *testing.T, catalog sql.Catalog) sql.Catalog {
				return failingCatalog{Catalog: catalog, indexesErr: errInjected}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine, ctx := newTestEngine(t)
			catalog := tt.prepare(t, engine.Analyzer.Catalog)

			iter, err := constraintColumnUsageRowIter(ctx, catalog)
			require.ErrorIs(t, err, errInjected)
			assert.Nil(t, iter)
		})
	}
}

// TestConstraintColumnUsageRowIterSucceedsThroughWrappedCatalog confirms the
// doubles above are faithful: with no failure injected, the wrapped catalog
// yields the same rows as the real one.
func TestConstraintColumnUsageRowIterSucceedsThroughWrappedCatalog(t *testing.T) {
	engine, ctx := newTestEngine(t)

	want, err := constraintColumnUsageRowIter(ctx, engine.Analyzer.Catalog)
	require.NoError(t, err)
	wantRows, err := sql.RowIterToRows(ctx, want)
	require.NoError(t, err)
	require.NotEmpty(t, wantRows)

	got, err := constraintColumnUsageRowIter(ctx, failingCatalog{Catalog: engine.Analyzer.Catalog, indexesErr: nil})
	require.NoError(t, err)
	gotRows, err := sql.RowIterToRows(ctx, got)
	require.NoError(t, err)
	assert.ElementsMatch(t, wantRows, gotRows)
}
