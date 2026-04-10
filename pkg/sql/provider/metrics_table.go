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
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/sqltypes"
)

// metricsTableName is the fixed name of the metrics table.
const metricsTableName = "metrics"

// metricsSchema defines the SQL schema for the metrics table.
var metricsSchema = sqle.Schema{
	{
		Name:       "name",
		Type:       types.MustCreateString(sqltypes.VarChar, 1024, sqle.Collation_Default),
		Nullable:   false,
		PrimaryKey: true,
		Source:     metricsTableName,
	},
	{
		Name:     "tag_count",
		Type:     types.Int32,
		Nullable: true,
		Source:   metricsTableName,
	},
	{
		Name:     "point_count",
		Type:     types.Int64,
		Nullable: true,
		Source:   metricsTableName,
	},
}

// Compile-time interface assertion.
var _ sqle.Table = (*MetricsTable)(nil)

// MetricsTable implements sql.Table for time-series metric metadata.
// It exposes the metrics table schema for SQL queries that list
// available time-series metrics and their statistics.
type MetricsTable struct{}

// NewMetricsTable creates a new MetricsTable.
func NewMetricsTable() *MetricsTable {
	return &MetricsTable{}
}

// Name returns the table name.
func (t *MetricsTable) Name() string {
	return metricsTableName
}

// String returns a human-readable description of the table.
func (t *MetricsTable) String() string {
	return metricsTableName
}

// Schema returns the table's column schema.
func (t *MetricsTable) Schema() sqle.Schema {
	return metricsSchema
}

// Collation returns the table's default collation.
func (t *MetricsTable) Collation() sqle.CollationID {
	return sqle.Collation_Default
}

// Partitions returns an iterator over the table's partitions. The
// metrics table uses a single partition.
func (t *MetricsTable) Partitions(_ *sqle.Context) (sqle.PartitionIter, error) {
	return sqle.PartitionsToPartitionIter(&metricsPartition{}), nil
}

// PartitionRows returns an iterator over all rows in the given partition.
// This is a stub that returns an empty result set; actual data access will
// be wired when the TimeSeries service is integrated.
func (t *MetricsTable) PartitionRows(_ *sqle.Context, _ sqle.Partition) (sqle.RowIter, error) {
	return sqle.RowsToRowIter(), nil
}

// metricsPartition is a single-partition implementation for the
// metrics table.
type metricsPartition struct{}

// Key returns the partition key.
func (p *metricsPartition) Key() []byte {
	return []byte("metrics")
}
