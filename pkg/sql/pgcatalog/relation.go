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
	"bytes"

	sqle "github.com/dolthub/go-mysql-server/sql"
)

// Compile-time interface assertions. The set is exhaustive on purpose:
// relation implements the read surface and the catalog handshake and
// NOTHING else, so a type assertion to sqle.InsertableTable,
// sqle.UpdatableTable, sqle.DeletableTable or sqle.ReplaceableTable fails
// structurally and a DML statement against a pg_catalog relation is
// rejected during analysis (see the package comment).
var (
	_ sqle.Table        = (*relation)(nil)
	_ sqle.CatalogTable = (*relation)(nil)
	_ sqle.Databaseable = (*relation)(nil)
)

// rowsFunc produces every row of a relation. It is handed the catalog
// rather than closing over one so that a relation value is independent of
// the engine it is served by, and is called once per scan so that a
// relation always reflects the schema as it is now -- these are catalogue
// views over live state, not snapshots.
type rowsFunc func(*sqle.Context, sqle.Catalog) ([]sqle.Row, error)

// relation is a read-only pg_catalog relation whose rows are computed on
// demand from the engine catalog.
type relation struct {
	name    string
	schema  sqle.Schema
	rows    rowsFunc
	catalog sqle.Catalog
}

// Name returns the relation name.
func (r *relation) Name() string { return r.name }

// String returns a human-readable description of the relation.
func (r *relation) String() string { return SchemaName + "." + r.name }

// Database implements sqle.Databaseable.
func (r *relation) Database() string { return SchemaName }

// Schema returns the relation's column schema.
func (r *relation) Schema(_ *sqle.Context) sqle.Schema { return r.schema }

// Collation returns the relation's collation.
func (r *relation) Collation() sqle.CollationID { return sqle.Collation_Default }

// AssignCatalog implements sqle.CatalogTable. go-mysql-server's plan
// builder calls it on every resolved table that implements the interface
// (planbuilder.buildResolvedTable), which is how a relation reaches the set
// of databases and tables it reports on.
//
// It returns a copy rather than mutating the receiver: one *relation value
// is built per lookup, but returning a copy keeps the method free of any
// assumption about that and safe to call concurrently on a shared value.
func (r *relation) AssignCatalog(cat sqle.Catalog) sqle.Table {
	assigned := *r
	assigned.catalog = cat
	return &assigned
}

// Partitions returns the relation's single partition.
func (r *relation) Partitions(_ *sqle.Context) (sqle.PartitionIter, error) {
	return sqle.PartitionsToPartitionIter(relationPartition(r.name)), nil
}

// PartitionRows computes and returns every row of the relation.
func (r *relation) PartitionRows(ctx *sqle.Context, part sqle.Partition) (sqle.RowIter, error) {
	if !bytes.Equal(part.Key(), relationPartition(r.name).Key()) {
		return nil, &UnknownPartitionError{Relation: r.name, Key: string(part.Key())}
	}
	if r.catalog == nil {
		return nil, &CatalogUnavailableError{Relation: r.name}
	}
	rows, err := r.rows(ctx, r.catalog)
	if err != nil {
		return nil, err
	}
	return sqle.RowsToRowIter(rows...), nil
}

// relationPartition is the single partition every pg_catalog relation is
// scanned through. Its key is the relation name, so a partition handed to
// the wrong relation is detected rather than silently scanned.
type relationPartition string

// Key returns the partition key.
func (p relationPartition) Key() []byte { return []byte(p) }
