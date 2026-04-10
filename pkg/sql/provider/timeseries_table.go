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

// datapointsTableName is the fixed name of the datapoints table.
const datapointsTableName = "datapoints"

// datapointsSchema defines the SQL schema for the datapoints table.
var datapointsSchema = sqle.Schema{
	{
		Name:       "metric",
		Type:       types.MustCreateString(sqltypes.VarChar, 1024, sqle.Collation_Default),
		Nullable:   false,
		PrimaryKey: true,
		Source:     datapointsTableName,
	},
	{
		Name:       "timestamp",
		Type:       types.Int64,
		Nullable:   false,
		PrimaryKey: true,
		Source:     datapointsTableName,
	},
	{
		Name:     "value",
		Type:     types.Float64,
		Nullable: false,
		Source:   datapointsTableName,
	},
	{
		Name:     "tags",
		Type:     types.JSON,
		Nullable: true,
		Source:   datapointsTableName,
	},
}

// Compile-time interface assertion.
var _ sqle.Table = (*DatapointsTable)(nil)

// DatapointsTable implements sql.Table for time-series data points.
// It exposes the datapoints table schema for SQL queries against the
// QRDB time-series storage layer.
type DatapointsTable struct{}

// NewDatapointsTable creates a new DatapointsTable.
func NewDatapointsTable() *DatapointsTable {
	return &DatapointsTable{}
}

// Name returns the table name.
func (t *DatapointsTable) Name() string {
	return datapointsTableName
}

// String returns a human-readable description of the table.
func (t *DatapointsTable) String() string {
	return datapointsTableName
}

// Schema returns the table's column schema.
func (t *DatapointsTable) Schema() sqle.Schema {
	return datapointsSchema
}

// Collation returns the table's default collation.
func (t *DatapointsTable) Collation() sqle.CollationID {
	return sqle.Collation_Default
}

// Partitions returns an iterator over the table's partitions. The
// datapoints table uses a single partition.
func (t *DatapointsTable) Partitions(_ *sqle.Context) (sqle.PartitionIter, error) {
	return sqle.PartitionsToPartitionIter(&datapointsPartition{}), nil
}

// PartitionRows returns an iterator over all rows in the given partition.
// This is a stub that returns an empty result set; actual data access will
// be wired when the TimeSeries service is integrated.
func (t *DatapointsTable) PartitionRows(_ *sqle.Context, _ sqle.Partition) (sqle.RowIter, error) {
	return sqle.RowsToRowIter(), nil
}

// datapointsPartition is a single-partition implementation for the
// datapoints table.
type datapointsPartition struct{}

// Key returns the partition key.
func (p *datapointsPartition) Key() []byte {
	return []byte("datapoints")
}
