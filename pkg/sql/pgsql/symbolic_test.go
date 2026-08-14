// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build qrdb_pgsql

package pgsql

import (
	"context"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
	sqltypes "github.com/dolthub/go-mysql-server/sql/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// The PostgreSQL wire path materializes rows through collectRows, which used
// to format each cell without reference to the schema. go-mysql-server holds
// an ENUM cell as the uint16 ordinal of its label and a SET cell as the
// uint64 bitmask of its members, so a pg client received 2 and 5 where
// 'active' and 'read,admin' were stored. Passing the schema is what lets
// catalog.ResolveColumnValue turn those symbols back into values.
// ---------------------------------------------------------------------------

// symbolicSchema is a two-column schema whose cells are both symbolic.
func symbolicSchema(t *testing.T) sql.Schema {
	t.Helper()

	enum, err := sqltypes.CreateEnumType([]string{"pending", "active", "banned"}, sql.Collation_Default)
	require.NoError(t, err)
	set, err := sqltypes.CreateSetType([]string{"read", "write", "admin"}, sql.Collation_Default)
	require.NoError(t, err)

	return sql.Schema{
		{Name: "status", Type: enum},
		{Name: "perms", Type: set},
	}
}

// TestCollectRows_ResolvesEnumAndSet pins the labels a pg client receives.
func TestCollectRows_ResolvesEnumAndSet(t *testing.T) {
	sqlCtx := sql.NewContext(context.Background())
	iter := sql.RowsToRowIter(
		sql.Row{uint16(2), uint64(0b101)},
		sql.Row{uint16(1), uint64(0b111)},
	)

	result, err := collectRows(sqlCtx, symbolicSchema(t), iter)
	require.NoError(t, err)
	require.Len(t, result.rows, 2)

	assert.Equal(t, "active", result.rows[0][0])
	assert.Equal(t, "read,admin", result.rows[0][1])
	assert.Equal(t, "pending", result.rows[1][0])
	assert.Equal(t, "read,write,admin", result.rows[1][1])
}

// TestCollectRows_SymbolicNull asserts a NULL symbolic cell stays NULL
// rather than resolving to the label of ordinal zero, which does not exist.
func TestCollectRows_SymbolicNull(t *testing.T) {
	sqlCtx := sql.NewContext(context.Background())
	iter := sql.RowsToRowIter(sql.Row{nil, nil})

	result, err := collectRows(sqlCtx, symbolicSchema(t), iter)
	require.NoError(t, err)
	require.Len(t, result.rows, 1)
	assert.Nil(t, result.rows[0][0])
	assert.Nil(t, result.rows[0][1])
}

// TestCollectRows_SymbolicResolveError asserts an unresolvable cell fails
// the query rather than being written to the wire as its raw ordinal.
func TestCollectRows_SymbolicResolveError(t *testing.T) {
	sqlCtx := sql.NewContext(context.Background())
	iter := &mockRowIter{rows: []sql.Row{{uint16(99), uint64(0)}}}

	result, err := collectRows(sqlCtx, symbolicSchema(t), iter)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.True(t, iter.closed, "iterator must be closed when resolution fails")
}

// TestCollectRows_NonSymbolicUnaffected asserts adding schema-aware
// resolution left every other column type rendering exactly as before.
func TestCollectRows_NonSymbolicUnaffected(t *testing.T) {
	sqlCtx := sql.NewContext(context.Background())
	schema := sql.Schema{
		{Name: "id", Type: sqltypes.Int32},
		{Name: "name", Type: sqltypes.Text},
		{Name: "raw", Type: sqltypes.Blob},
	}
	iter := sql.RowsToRowIter(sql.Row{int32(7), "widget", []byte("bytes")})

	result, err := collectRows(sqlCtx, schema, iter)
	require.NoError(t, err)
	require.Len(t, result.rows, 1)
	assert.Equal(t, int32(7), result.rows[0][0])
	assert.Equal(t, "widget", result.rows[0][1])
	assert.Equal(t, "bytes", result.rows[0][2])
}
