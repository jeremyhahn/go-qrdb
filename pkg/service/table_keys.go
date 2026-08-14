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

package service

import "strconv"

// TableDataPrefix returns the key prefix enumerating a relational table's
// rows -- the same physical key layout TableService itself proposes writes
// under (see tableDataPrefixFor in table_types.go). It is exported for the
// SQL data-plane read path (pkg/sql/provider, pkg/sql/catalog), which reads
// row bytes directly through a KVServicer/MVCCKVServicer already scoped to
// the caller's (tenant, database) namespace by service.Registry, and
// therefore needs the same prefix scheme without depending on unexported
// TableService internals.
func TableDataPrefix(database string, tableID uint64) string {
	return tableDataPrefixFor(database, tableID)
}

// TableIndexPrefix returns the key prefix enumerating one secondary index's
// entries for a relational table -- the same physical key layout
// TableService itself proposes writes under (see tableIndexPrefixFor in
// table_types.go). It is exported for the same reason as TableDataPrefix.
func TableIndexPrefix(database string, tableID uint64, index string) string {
	return tableIndexPrefixFor(database, tableID, index)
}

// TableAutoIncrementSequenceName returns the SequenceServicer name backing a
// relational table's AUTO_INCREMENT column. It is derived from the table's
// immutable TableID rather than its current Name, so a RENAME TABLE (a pure
// catalog compare-and-swap that never touches row/index data -- see
// TableDataPrefix/TableIndexPrefix) also never disturbs the sequence's
// accumulated state. It is exported for the same reason as
// TableDataPrefix/TableIndexPrefix: the SQL data-plane write path
// (pkg/sql/provider) allocates AUTO_INCREMENT values directly through a
// SequenceServicer already scoped to the caller's (tenant, database)
// namespace, without depending on unexported TableService internals.
func TableAutoIncrementSequenceName(tableID uint64) string {
	return "__autoinc:" + strconv.FormatUint(tableID, 10)
}

// TableRowIDSequenceName returns the SequenceServicer name backing the
// synthetic hidden row id of a relational table that declares no PRIMARY KEY
// (see pkg/sql/catalog's rowid.go). It is keyed by TableID for the same
// reason TableAutoIncrementSequenceName is, and lives under a distinct
// prefix from it because a keyless table may ALSO declare an AUTO_INCREMENT
// column: the two counters are independent and must never share storage.
func TableRowIDSequenceName(tableID uint64) string {
	return "__rowid:" + strconv.FormatUint(tableID, 10)
}
