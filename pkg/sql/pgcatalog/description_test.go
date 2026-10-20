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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vanishedTableDatabase lists a table it can no longer open: the state a
// catalog reader sees when a DROP TABLE lands between GetTableNames and the
// lookup. sql.Database permits it -- GetTableInsensitive reports "not found"
// with ok == false and no error.
type vanishedTableDatabase struct {
	name string
}

var _ sqle.Database = (*vanishedTableDatabase)(nil)

// Name implements sqle.Nameable.
func (d vanishedTableDatabase) Name() string { return d.name }

// GetTableInsensitive implements sqle.Database; the listed table is gone.
func (d vanishedTableDatabase) GetTableInsensitive(_ *sqle.Context, _ string) (sqle.Table, bool, error) {
	return nil, false, nil
}

// GetTableNames implements sqle.Database.
func (d vanishedTableDatabase) GetTableNames(_ *sqle.Context) ([]string, error) {
	return []string{"ghost"}, nil
}

func TestDescriptionRowsReportTableAndColumnComments(t *testing.T) {
	eng, ctx := newPGEngine(memory.NewDatabase("gorm"))
	ctx.SetCurrentDatabase("gorm")
	queryRows(t, eng, ctx, "CREATE TABLE people ("+
		"id INT PRIMARY KEY, "+
		"name TEXT COMMENT 'display name', "+
		"email TEXT COMMENT 'login address') COMMENT 'registered people'")

	rows, err := descriptionRows(ctx, eng.Analyzer.Catalog)
	require.NoError(t, err)

	oid := ClassOID("gorm", "people")
	assert.Equal(t, []sqle.Row{
		{oid, int32(0), "registered people"}, // objsubid 0 is the table itself
		{oid, int32(2), "display name"},      // columns are numbered from 1; id is uncommented
		{oid, int32(3), "login address"},
	}, rows)
}

func TestDescriptionRowsOmitAnUncommentedTable(t *testing.T) {
	eng, ctx := newPGEngine(memory.NewDatabase("gorm"))
	ctx.SetCurrentDatabase("gorm")
	queryRows(t, eng, ctx, "CREATE TABLE plain (id INT PRIMARY KEY, v TEXT)")

	rows, err := descriptionRows(ctx, eng.Analyzer.Catalog)
	require.NoError(t, err)
	assert.Empty(t, rows, "a table and columns without comments describe nothing")
}

func TestDescriptionRowsSkipUnreadableDatabasesAndVanishedTables(t *testing.T) {
	eng, ctx := newPGEngine(memory.NewDatabase("gorm"),
		unreadableDatabase{name: "broken"}, vanishedTableDatabase{name: "racing"})
	ctx.SetCurrentDatabase("gorm")
	queryRows(t, eng, ctx, "CREATE TABLE people (id INT PRIMARY KEY) COMMENT 'kept'")

	// pg_description is advisory metadata: a database or table the catalog
	// cannot read must not hide the comments it can.
	rows, err := descriptionRows(ctx, eng.Analyzer.Catalog)
	require.NoError(t, err)
	assert.Equal(t, []sqle.Row{{ClassOID("gorm", "people"), int32(0), "kept"}}, rows)
}

func TestConstraintColumnUsageSurfacesAnUnreadableDatabase(t *testing.T) {
	eng, ctx := newPGEngine(memory.NewDatabase("gorm"), unreadableDatabase{name: "broken"})

	// Unlike pg_description, an information_schema view cannot silently drop
	// a database's constraints, so the failure is the caller's to see.
	iter, err := constraintColumnUsageRowIter(ctx, eng.Analyzer.Catalog)
	assert.Nil(t, iter)
	var want *unreadableDatabaseError
	require.ErrorAs(t, err, &want)
	assert.Equal(t, "broken", want.Name)
}

func TestConstraintColumnUsageSurfacesAVanishedTable(t *testing.T) {
	eng, ctx := newPGEngine(vanishedTableDatabase{name: "racing"})

	iter, err := constraintColumnUsageRowIter(ctx, eng.Analyzer.Catalog)
	assert.Nil(t, iter)
	require.Error(t, err)
	assert.True(t, sqle.ErrTableNotFound.Is(err), "got %v", err)
}
