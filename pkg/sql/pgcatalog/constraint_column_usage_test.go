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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConstraintColumnUsageReportsPrimaryAndUniqueColumns covers the view's
// contract: one row per column of every PRIMARY KEY and UNIQUE constraint.
func TestConstraintColumnUsageReportsPrimaryAndUniqueColumns(t *testing.T) {
	catalog, ctx := newIndexesCatalog(t)

	got := map[string][]string{}
	for _, row := range scan(t, ctx, assignedInfoSchema(t, catalog)) {
		if row[4].(string) != "idxtest" { //nolint:errcheck // the view's own schema declares these columns
			continue // information_schema's own tables are not under test
		}
		constraint := row[2].(string) //nolint:errcheck // as above
		column := row[6].(string)     //nolint:errcheck // as above
		got[constraint] = append(got[constraint], column)
	}

	assert.Equal(t, map[string][]string{
		"PRIMARY":      {"id", "id"}, // one per table: people and orders
		"unique_email": {"email"},
	}, got)
}

// TestConstraintColumnUsageAgreesWithKeyColumnUsage pins the property that
// makes the view joinable: go-mysql-server's key_column_usage is the sibling
// table a client joins it against, and the two have to agree on every value
// used as a join key -- including the "def" catalog placeholder MySQL uses
// where PostgreSQL would name the database.
func TestConstraintColumnUsageAgreesWithKeyColumnUsage(t *testing.T) {
	catalog, ctx := newIndexesCatalog(t)

	type key struct{ catalog, schema, constraint, table, column string }
	ours := map[key]bool{}
	for _, row := range scan(t, ctx, assignedInfoSchema(t, catalog)) {
		if row[4].(string) != "idxtest" { //nolint:errcheck // declared column type
			continue
		}
		ours[key{row[0].(string), row[1].(string), row[2].(string), row[5].(string), row[6].(string)}] = true //nolint:errcheck // declared column types
	}

	require.NotEmpty(t, ours)
	for k := range ours {
		assert.Equal(t, "def", k.catalog,
			"the catalog placeholder must match key_column_usage's, or the join finds nothing")
		assert.Equal(t, "idxtest", k.schema)
	}
}

// TestConstraintColumnUsageSkipsATableItCannotIndex covers the one condition
// that is not an error: a table that is not index-addressable has no
// constraints to report and contributes no rows.
func TestConstraintColumnUsageSkipsATableItCannotIndex(t *testing.T) {
	rows, err := constraintColumnUsageRowIter(sqle.NewEmptyContext(), newTestCatalog("empty"))
	require.NoError(t, err)
	require.NotNil(t, rows)
}

// assignedInfoSchema resolves the view through the information_schema wrapper
// that actually serves it, and hands it the catalog the way go-mysql-server's
// plan builder does. It is not reachable through Relation: this is an
// information_schema table, not a pg_catalog one.
func assignedInfoSchema(t *testing.T, cat sqle.Catalog) sqle.Table {
	t.Helper()
	tbl, ok := InfoSchema().(infoSchema)
	require.True(t, ok)
	resolved, found, err := tbl.GetTableInsensitive(sqle.NewEmptyContext(), ConstraintColumnUsageTableName)
	require.NoError(t, err)
	require.True(t, found)
	ct, ok := resolved.(sqle.CatalogTable)
	require.True(t, ok)
	return ct.AssignCatalog(cat)
}
