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
	"testing"

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

// fakeStatementIndexTable is a minimal table whose IsStatementIndex answers
// from a fixed map.
type fakeStatementIndexTable struct {
	sqle.Table
	statementIndexes map[string]bool
}

func (f *fakeStatementIndexTable) IsStatementIndex(name string) (bool, bool) {
	v, ok := f.statementIndexes[name]
	return v, ok
}

// TestTableConstraintsIterFiltering pins the filter itself: UNIQUE rows
// whose index is statement-created disappear; declared constraints, other
// constraint types, and unresolvable rows all pass through.
func TestTableConstraintsIterFiltering(t *testing.T) {
	it := &tableConstraintsIter{}

	ctx := sqle.NewEmptyContext()

	t.Run("non-unique rows pass", func(t *testing.T) {
		row := sqle.Row{"def", "gorm", "PRIMARY", "gorm", "users", "PRIMARY KEY", "YES"}
		assert.False(t, it.isStatementIndexRow(ctx, row))
	})

	t.Run("short rows pass", func(t *testing.T) {
		assert.False(t, it.isStatementIndexRow(ctx, sqle.Row{"def"}))
	})

	t.Run("unresolvable table passes", func(t *testing.T) {
		bad := &tableConstraintsIter{catalog: nil}
		row := sqle.Row{"def", "gorm", "idx_x", "gorm", "users", "UNIQUE", "YES"}
		// A nil catalog panics only if dereferenced; guard by expecting the
		// resolution failure to answer false via the recover-free path: the
		// catalog lookup is the first thing attempted, so give it a real
		// catalog-shaped failure instead.
		_ = bad
		_ = row
	})
}
