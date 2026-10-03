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
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/sqltypes"
)

// namespacesTableName is the fixed name of the namespaces table.
const namespacesTableName = "namespaces"

// namespacesSchema defines the SQL schema for the namespaces table.
var namespacesSchema = sqle.Schema{
	{
		Name:       "name",
		Type:       types.MustCreateString(sqltypes.VarChar, 256, sqle.Collation_Default),
		Nullable:   false,
		PrimaryKey: true,
		Source:     namespacesTableName,
	},
	{
		Name:     "dimensions",
		Type:     types.Int32,
		Nullable: false,
		Source:   namespacesTableName,
	},
	{
		Name:     "distance",
		Type:     types.MustCreateString(sqltypes.VarChar, 32, sqle.Collation_Default),
		Nullable: true,
		Source:   namespacesTableName,
	},
	{
		Name:     "index_type",
		Type:     types.MustCreateString(sqltypes.VarChar, 32, sqle.Collation_Default),
		Nullable: true,
		Source:   namespacesTableName,
	},
}

// Compile-time interface assertion.
var _ sqle.Table = (*NamespacesTable)(nil)

// NamespacesTable implements sql.Table for vector namespace metadata.
// It exposes the namespaces table schema for SQL queries that list
// available vector namespaces and their configuration.
type NamespacesTable struct{}

// NewNamespacesTable creates a new NamespacesTable.
func NewNamespacesTable() *NamespacesTable {
	return &NamespacesTable{}
}

// Name returns the table name.
func (t *NamespacesTable) Name() string {
	return namespacesTableName
}

// String returns a human-readable description of the table.
func (t *NamespacesTable) String() string {
	return namespacesTableName
}

// Schema returns the table's column schema.
func (t *NamespacesTable) Schema(_ *sqle.Context) sqle.Schema {
	return namespacesSchema
}

// Collation returns the table's default collation.
func (t *NamespacesTable) Collation() sqle.CollationID {
	return sqle.Collation_Default
}

// Partitions returns an iterator over the table's partitions. The
// namespaces table uses a single partition.
func (t *NamespacesTable) Partitions(_ *sqle.Context) (sqle.PartitionIter, error) {
	return sqle.PartitionsToPartitionIter(&namespacesPartition{}), nil
}

// PartitionRows returns an iterator over all rows in the given partition.
// This is a stub that returns an empty result set; actual data access will
// be wired when the Vector service is integrated.
func (t *NamespacesTable) PartitionRows(_ *sqle.Context, _ sqle.Partition) (sqle.RowIter, error) {
	return sqle.RowsToRowIter(), nil
}

// namespacesPartition is a single-partition implementation for the
// namespaces table.
type namespacesPartition struct{}

// Key returns the partition key.
func (p *namespacesPartition) Key() []byte {
	return []byte("namespaces")
}
