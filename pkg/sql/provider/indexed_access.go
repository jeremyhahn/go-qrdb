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

	sqle "github.com/dolthub/go-mysql-server/sql"
)

// ---------------------------------------------------------------------------
// indexedPartition
// ---------------------------------------------------------------------------

// indexedPartition is a single partition that carries pre-computed entity
// keys returned by an index lookup. It is used by IndexedTable
// implementations (KVTable, CollectionTable) when the query planner has
// chosen an index access path over a full table scan.
type indexedPartition struct {
	keys [][]byte
}

// Key returns a fixed partition key; there is always exactly one indexed
// partition per index lookup.
func (p *indexedPartition) Key() []byte {
	return []byte("indexed")
}

// ---------------------------------------------------------------------------
// indexedRowIter
// ---------------------------------------------------------------------------

// indexedRowIter is a sql.RowIter that iterates over a pre-built slice of
// SQL rows obtained via index-accelerated lookup. Rows are held in memory
// after the lookup phase and streamed one-by-one during execution.
type indexedRowIter struct {
	rows []sqle.Row
	idx  int
}

// Compile-time interface assertion.
var _ sqle.RowIter = (*indexedRowIter)(nil)

// Next returns the next row, or io.EOF when all rows have been consumed.
func (it *indexedRowIter) Next(_ *sqle.Context) (sqle.Row, error) {
	if it.idx >= len(it.rows) {
		return nil, io.EOF
	}
	row := it.rows[it.idx]
	it.idx++
	return row, nil
}

// Close releases the row slice to allow garbage collection.
func (it *indexedRowIter) Close(_ *sqle.Context) error {
	it.rows = nil
	return nil
}
