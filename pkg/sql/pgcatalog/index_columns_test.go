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
	"testing"

	"github.com/dolthub/go-mysql-server/memory"
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/analyzer"
	"github.com/stretchr/testify/assert"
)

// TestIndexColumnsListsOnlyIndexesThatAreNotConstraints pins which indexes
// qrdb_index_columns reports for tables that can say where their indexes
// came from: non-unique indexes and statement-created unique indexes, one row
// per column; never the primary key, a declared unique constraint, a unique
// index of unknown origin, or an index backing a foreign key.
func TestIndexColumnsListsOnlyIndexesThatAreNotConstraints(t *testing.T) {
	eng, ctx := newReportingEngine(t)

	rows := scan(t, ctx, assigned(t, IndexColumnsRelation, eng.Analyzer.Catalog))
	assert.ElementsMatch(t, []sqle.Row{
		{"people", "stmt_email", true, false, "email"},
		{"people", "idx_name", false, false, "name"},
		{"orders", "idx_note", false, false, "note"},
	}, rows)
}

// TestIndexColumnsSkipsUniqueIndexesOfATableThatCannotReport covers a table
// that cannot say where its unique indexes came from: they are treated as
// constraints, while its non-unique indexes are still reported.
func TestIndexColumnsSkipsUniqueIndexesOfATableThatCannotReport(t *testing.T) {
	eng, ctx := newReportingEngine(t)
	ctx.SetCurrentDatabase(plainDBName)

	rows := scan(t, ctx, assigned(t, IndexColumnsRelation, eng.Analyzer.Catalog))
	assert.Equal(t, []sqle.Row{{"people", "plain_name", false, false, "name"}}, rows)
}

// TestIndexColumnsOfAnUnavailableDatabaseIsEmpty covers a current database
// the catalog cannot resolve or list: the relation is empty, not a failure,
// so a client's introspection query still runs.
func TestIndexColumnsOfAnUnavailableDatabaseIsEmpty(t *testing.T) {
	cases := []struct {
		name    string
		current string
	}{
		{"unknown database", "missing"},
		{"database that cannot be listed", "broken"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat := analyzer.NewCatalog(
				memory.NewDBProvider(memory.NewDatabase("gorm"), unreadableDatabase{name: "broken"}),
				sqle.EngineOverrides{})
			ctx := sqle.NewEmptyContext()
			ctx.SetCurrentDatabase(tc.current)

			assert.Empty(t, scan(t, ctx, assigned(t, IndexColumnsRelation, cat)))
		})
	}
}
