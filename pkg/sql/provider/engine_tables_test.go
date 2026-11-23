//go:build qrdb_sql

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"context"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
)

// engineTableCases lists every engine table under its reserved name (OD-12
// B), with the capability that compiles it in.
var engineTableCases = []struct {
	name    string
	feature string
}{
	{"qrdb_ts_datapoints", "timeSeries"},
	{"qrdb_ts_metrics", "timeSeries"},
	{"qrdb_vector_namespaces", "vector"},
	{"qrdb_vector_vectors", "vector"},
}

// expectedBuiltinTableNames is the alphabetical built-in table list this
// test binary must serve: kv always, each engine's tables when the engine
// is compiled in. It is written out independently of builtinTables so the
// assertion does not restate the code under test.
func expectedBuiltinTableNames() []string {
	names := []string{"kv"}
	if capabilities.Has("timeSeries") {
		names = append(names, "qrdb_ts_datapoints", "qrdb_ts_metrics")
	}
	if capabilities.Has("vector") {
		names = append(names, "qrdb_vector_namespaces", "qrdb_vector_vectors")
	}
	return names
}

// assertEngineTable checks that a database with no registry serves name as
// an engine table of wantType exactly when feature is compiled in.
func assertEngineTable(t *testing.T, name, feature string, wantType sqle.Table) {
	t.Helper()
	db := &Database{name: "qrdb", kvSvc: &mockKVService{}}
	tbl, found, err := db.GetTableInsensitive(newTestContext(), name)
	require.NoError(t, err)
	if !capabilities.Has(feature) {
		assert.False(t, found, "%s must not be served without %s compiled in", name, feature)
		return
	}
	require.True(t, found, "%s must be served with %s compiled in", name, feature)
	assert.Equal(t, name, tbl.Name())
	assert.IsType(t, wantType, tbl)
}

func TestDatabase_EngineTables_OldNamesAreNotEngineTables(t *testing.T) {
	db := &Database{name: "qrdb", kvSvc: &mockKVService{}}
	ctx := newTestContext()
	for _, name := range []string{"metrics", "vectors", "datapoints", "namespaces"} {
		t.Run(name, func(t *testing.T) {
			assert.False(t, db.hasTable(name))
			_, found, err := db.GetTableInsensitive(ctx, name)
			require.NoError(t, err)
			assert.False(t, found, "%s is no longer an engine table", name)
		})
	}
}

func TestDatabase_EngineTables_OnlyWhenCompiledIn(t *testing.T) {
	db := &Database{name: "qrdb", kvSvc: &mockKVService{}}
	ctx := newTestContext()

	names, err := db.GetTableNames(ctx)
	require.NoError(t, err)
	assert.Equal(t, expectedBuiltinTableNames(), names)

	for _, tc := range engineTableCases {
		t.Run(tc.name, func(t *testing.T) {
			compiled := capabilities.Has(tc.feature)
			assert.Equal(t, compiled, db.hasTable(tc.name))
			assert.Equal(t, compiled, contains(names, tc.name))
			_, found, err := db.GetTableInsensitive(ctx, tc.name)
			require.NoError(t, err)
			assert.Equal(t, compiled, found)
		})
	}
}

func TestDatabase_CreateTable_ReservedEngineTableNameRefused(t *testing.T) {
	h := newDDLHarness()
	db := h.newDDLDatabase("shop")
	ctx := newTestContext()

	for _, tc := range engineTableCases {
		for _, spelling := range []string{tc.name, mixedCase(tc.name)} {
			t.Run(spelling, func(t *testing.T) {
				err := db.CreateTable(ctx, spelling, oneColumnSchema(spelling), sqle.Collation_Default, "")
				assertReservedNameSQLError(t, err, ErrCodeReservedTableName, "provider: table name is reserved: "+spelling)
			})
		}
	}

	names, err := h.tableSvc.ListTables(context.Background(), "shop")
	require.NoError(t, err)
	assert.Empty(t, names, "a refused CREATE TABLE writes nothing")
}

// assertReservedNameSQLError asserts err is the *mysql.SQLError a reserved
// name refusal reaches clients as: the code, SQLSTATE 42000 and a message
// naming the reserved name.
func assertReservedNameSQLError(t *testing.T, err error, code int, message string) {
	t.Helper()
	var sqlErr *mysql.SQLError
	require.ErrorAs(t, err, &sqlErr, "got %v", err)
	assert.Equal(t, code, sqlErr.Num)
	assert.Equal(t, "42000", sqlErr.SQLState())
	assert.Contains(t, sqlErr.Message, message)
}

func TestDatabase_RenameTable_ToReservedEngineTableNameRefused(t *testing.T) {
	h := newDDLHarness()
	db := h.newDDLDatabase("shop")
	ctx := newTestContext()
	require.NoError(t, db.CreateTable(ctx, "widgets", oneColumnSchema("widgets"), sqle.Collation_Default, ""))

	for _, tc := range engineTableCases {
		for _, spelling := range []string{tc.name, mixedCase(tc.name)} {
			t.Run(spelling, func(t *testing.T) {
				err := db.RenameTable(ctx, "widgets", spelling)
				assertReservedNameSQLError(t, err, ErrCodeReservedTableName, "provider: table name is reserved: "+spelling)
			})
		}
	}

	names, err := h.tableSvc.ListTables(context.Background(), "shop")
	require.NoError(t, err)
	assert.Equal(t, []string{"widgets"}, names, "a refused RENAME TABLE leaves the table in place")
}

func TestDatabase_CreateTable_NamedLikeOldEngineTable(t *testing.T) {
	h := newDDLHarness()
	db := h.newDDLDatabase("shop")
	ctx := newTestContext()

	for _, name := range []string{"metrics", "vectors"} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, db.CreateTable(ctx, name, oneColumnSchema(name), sqle.Collation_Default, ""))
			tbl, found, err := db.GetTableInsensitive(ctx, name)
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, name, tbl.Name())
			assert.NotContains(t, []string{"*provider.MetricsTable", "*provider.VectorsTable"},
				typeName(tbl), "the relational table, not an engine table")
			schema := tbl.Schema(ctx)
			require.Len(t, schema, 1)
			assert.Equal(t, "id", schema[0].Name)
		})
	}
}

// oneColumnSchema is a one-column primary-key schema for table name.
func oneColumnSchema(name string) sqle.PrimaryKeySchema {
	return sqle.NewPrimaryKeySchema(sqle.Schema{
		{Name: "id", Type: types.Int64, Nullable: false, Source: name, PrimaryKey: true},
	})
}

// mixedCase upper-cases every other letter of s.
func mixedCase(s string) string {
	b := []byte(s)
	for i := 0; i < len(b); i += 2 {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 'a' - 'A'
		}
	}
	return string(b)
}

// contains reports whether names holds name.
func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// typeName is the dynamic type of tbl, for messages and type checks.
func typeName(tbl sqle.Table) string {
	switch tbl.(type) {
	case *MetricsTable:
		return "*provider.MetricsTable"
	case *VectorsTable:
		return "*provider.VectorsTable"
	default:
		return "other"
	}
}
