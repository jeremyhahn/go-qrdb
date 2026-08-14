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
// DatapointsTable tests
// ---------------------------------------------------------------------------

func TestNewDatapointsTable(t *testing.T) {
	tbl := NewDatapointsTable()

	require.NotNil(t, tbl)
	assert.Equal(t, "datapoints", tbl.Name())
	assert.Equal(t, "datapoints", tbl.String())
}

func TestDatapointsTable_Schema(t *testing.T) {
	tbl := NewDatapointsTable()

	schema := tbl.Schema(newTestContext())

	require.Len(t, schema, 4)

	assert.Equal(t, "metric", schema[0].Name)
	assert.True(t, schema[0].PrimaryKey)
	assert.False(t, schema[0].Nullable)
	assert.Equal(t, "datapoints", schema[0].Source)

	assert.Equal(t, "timestamp", schema[1].Name)
	assert.True(t, schema[1].PrimaryKey)
	assert.False(t, schema[1].Nullable)
	assert.Equal(t, "datapoints", schema[1].Source)

	assert.Equal(t, "value", schema[2].Name)
	assert.False(t, schema[2].PrimaryKey)
	assert.False(t, schema[2].Nullable)
	assert.Equal(t, "datapoints", schema[2].Source)

	assert.Equal(t, "tags", schema[3].Name)
	assert.False(t, schema[3].PrimaryKey)
	assert.True(t, schema[3].Nullable)
	assert.Equal(t, "datapoints", schema[3].Source)
}

func TestDatapointsTable_Collation(t *testing.T) {
	tbl := NewDatapointsTable()
	assert.Equal(t, sqle.Collation_Default, tbl.Collation())
}

func TestDatapointsTable_Partitions(t *testing.T) {
	tbl := NewDatapointsTable()
	ctx := newTestContext()

	iter, err := tbl.Partitions(ctx)
	require.NoError(t, err)

	p, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("datapoints"), p.Key())

	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

func TestDatapointsTable_PartitionRows_Empty(t *testing.T) {
	tbl := NewDatapointsTable()
	ctx := newTestContext()

	iter, err := tbl.PartitionRows(ctx, &datapointsPartition{})
	require.NoError(t, err)

	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

func TestDatapointsTable_ImplementsTable(t *testing.T) {
	var _ sqle.Table = (*DatapointsTable)(nil)
}

func TestDatapointsPartition_Key(t *testing.T) {
	p := &datapointsPartition{}
	assert.Equal(t, []byte("datapoints"), p.Key())
}
