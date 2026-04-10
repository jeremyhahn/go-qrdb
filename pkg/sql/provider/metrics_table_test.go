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
// MetricsTable tests
// ---------------------------------------------------------------------------

func TestNewMetricsTable(t *testing.T) {
	tbl := NewMetricsTable()

	require.NotNil(t, tbl)
	assert.Equal(t, "metrics", tbl.Name())
	assert.Equal(t, "metrics", tbl.String())
}

func TestMetricsTable_Schema(t *testing.T) {
	tbl := NewMetricsTable()

	schema := tbl.Schema()

	require.Len(t, schema, 3)

	assert.Equal(t, "name", schema[0].Name)
	assert.True(t, schema[0].PrimaryKey)
	assert.False(t, schema[0].Nullable)
	assert.Equal(t, "metrics", schema[0].Source)

	assert.Equal(t, "tag_count", schema[1].Name)
	assert.False(t, schema[1].PrimaryKey)
	assert.True(t, schema[1].Nullable)
	assert.Equal(t, "metrics", schema[1].Source)

	assert.Equal(t, "point_count", schema[2].Name)
	assert.False(t, schema[2].PrimaryKey)
	assert.True(t, schema[2].Nullable)
	assert.Equal(t, "metrics", schema[2].Source)
}

func TestMetricsTable_Collation(t *testing.T) {
	tbl := NewMetricsTable()
	assert.Equal(t, sqle.Collation_Default, tbl.Collation())
}

func TestMetricsTable_Partitions(t *testing.T) {
	tbl := NewMetricsTable()
	ctx := newTestContext()

	iter, err := tbl.Partitions(ctx)
	require.NoError(t, err)

	p, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("metrics"), p.Key())

	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

func TestMetricsTable_PartitionRows_Empty(t *testing.T) {
	tbl := NewMetricsTable()
	ctx := newTestContext()

	iter, err := tbl.PartitionRows(ctx, &metricsPartition{})
	require.NoError(t, err)

	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

func TestMetricsTable_ImplementsTable(t *testing.T) {
	var _ sqle.Table = (*MetricsTable)(nil)
}

func TestMetricsPartition_Key(t *testing.T) {
	p := &metricsPartition{}
	assert.Equal(t, []byte("metrics"), p.Key())
}
