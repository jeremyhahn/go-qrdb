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
	"io"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// NamespacesTable tests
// ---------------------------------------------------------------------------

func TestNewNamespacesTable(t *testing.T) {
	tbl := NewNamespacesTable()

	require.NotNil(t, tbl)
	assert.Equal(t, "namespaces", tbl.Name())
	assert.Equal(t, "namespaces", tbl.String())
}

func TestNamespacesTable_Schema(t *testing.T) {
	tbl := NewNamespacesTable()

	schema := tbl.Schema(newTestContext())

	require.Len(t, schema, 4)

	assert.Equal(t, "name", schema[0].Name)
	assert.True(t, schema[0].PrimaryKey)
	assert.False(t, schema[0].Nullable)
	assert.Equal(t, "namespaces", schema[0].Source)

	assert.Equal(t, "dimensions", schema[1].Name)
	assert.False(t, schema[1].PrimaryKey)
	assert.False(t, schema[1].Nullable)
	assert.Equal(t, "namespaces", schema[1].Source)

	assert.Equal(t, "distance", schema[2].Name)
	assert.False(t, schema[2].PrimaryKey)
	assert.True(t, schema[2].Nullable)
	assert.Equal(t, "namespaces", schema[2].Source)

	assert.Equal(t, "index_type", schema[3].Name)
	assert.False(t, schema[3].PrimaryKey)
	assert.True(t, schema[3].Nullable)
	assert.Equal(t, "namespaces", schema[3].Source)
}

func TestNamespacesTable_Collation(t *testing.T) {
	tbl := NewNamespacesTable()
	assert.Equal(t, sqle.Collation_Default, tbl.Collation())
}

func TestNamespacesTable_Partitions(t *testing.T) {
	tbl := NewNamespacesTable()
	ctx := newTestContext()

	iter, err := tbl.Partitions(ctx)
	require.NoError(t, err)

	p, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("namespaces"), p.Key())

	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

func TestNamespacesTable_PartitionRows_Empty(t *testing.T) {
	tbl := NewNamespacesTable()
	ctx := newTestContext()

	iter, err := tbl.PartitionRows(ctx, &namespacesPartition{})
	require.NoError(t, err)

	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

func TestNamespacesTable_ImplementsTable(t *testing.T) {
	var _ sqle.Table = (*NamespacesTable)(nil)
}

func TestNamespacesPartition_Key(t *testing.T) {
	p := &namespacesPartition{}
	assert.Equal(t, []byte("namespaces"), p.Key())
}
