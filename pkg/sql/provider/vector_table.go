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

// vectorsTableName is the fixed name of the vectors table.
const vectorsTableName = "vectors"

// vectorsSchema defines the SQL schema for the vectors table.
var vectorsSchema = sqle.Schema{
	{
		Name:       "id",
		Type:       types.MustCreateString(sqltypes.VarChar, 1024, sqle.Collation_Default),
		Nullable:   false,
		PrimaryKey: true,
		Source:     vectorsTableName,
	},
	{
		Name:     "embedding",
		Type:     types.MustCreateBinary(sqltypes.Blob, 0),
		Nullable: false,
		Source:   vectorsTableName,
	},
	{
		Name:     "metadata",
		Type:     types.JSON,
		Nullable: true,
		Source:   vectorsTableName,
	},
}

// Compile-time interface assertion.
var _ sqle.Table = (*VectorsTable)(nil)

// VectorsTable implements sql.Table for vector embeddings. It exposes
// the vectors table schema for SQL queries against the QRDB vector
// storage layer.
type VectorsTable struct{}

// NewVectorsTable creates a new VectorsTable.
func NewVectorsTable() *VectorsTable {
	return &VectorsTable{}
}

// Name returns the table name.
func (t *VectorsTable) Name() string {
	return vectorsTableName
}

// String returns a human-readable description of the table.
func (t *VectorsTable) String() string {
	return vectorsTableName
}

// Schema returns the table's column schema.
func (t *VectorsTable) Schema() sqle.Schema {
	return vectorsSchema
}

// Collation returns the table's default collation.
func (t *VectorsTable) Collation() sqle.CollationID {
	return sqle.Collation_Default
}

// Partitions returns an iterator over the table's partitions. The
// vectors table uses a single partition.
func (t *VectorsTable) Partitions(_ *sqle.Context) (sqle.PartitionIter, error) {
	return sqle.PartitionsToPartitionIter(&vectorsPartition{}), nil
}

// PartitionRows returns an iterator over all rows in the given partition.
// This is a stub that returns an empty result set; actual data access will
// be wired when the Vector service is integrated.
func (t *VectorsTable) PartitionRows(_ *sqle.Context, _ sqle.Partition) (sqle.RowIter, error) {
	return sqle.RowsToRowIter(), nil
}

// vectorsPartition is a single-partition implementation for the
// vectors table.
type vectorsPartition struct{}

// Key returns the partition key.
func (p *vectorsPartition) Key() []byte {
	return []byte("vectors")
}
