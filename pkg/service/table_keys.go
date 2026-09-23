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

import (
	"context"
	"strconv"
	"time"
)

// TableGenerationPinTTL is the longest a reader may use a table generation
// it resolved (TableDef.Generation) without re-proving it is still current.
//
// A statement or transaction resolves the table once and reads the rows and
// index entries of THAT generation for as long as it runs. After a TRUNCATE
// those keys are orphaned and ReclaimSweeper deletes them -- but only
// ReclaimQuiescence after it first observes the committed reclaim job, which
// is longer than this. The SQL layer (pkg/sql/provider) checks the pin's age
// after every read: a result read under a pin younger than this was complete
// before any reclamation of its generation could begin; an older pin is
// re-proven with one generation read, which either renews it or fails the
// statement with the typed, retryable *TableGenerationStaleError. No reader
// ever sees a partially reclaimed generation as a short or empty result.
const TableGenerationPinTTL = 10 * time.Second

// TableDataPrefix returns the key prefix enumerating a relational table's
// rows at the given generation -- the same physical key layout TableService
// itself proposes writes under (see tableDataPrefixFor in table_types.go).
// It is exported for the SQL data-plane read/write path (pkg/sql/provider,
// pkg/sql/catalog), which reads and writes row bytes directly through a
// KVServicer/MVCCKVServicer already scoped to the caller's (tenant,
// database) namespace by service.Registry, and therefore needs the same
// prefix scheme without depending on unexported TableService internals.
//
// generation must be the value TableDef.Generation held when the caller
// resolved the table (see GetTable), not a freshly re-read one: recomputing
// it per row is exactly the per-row consensus read go-qrdb#547 rules out.
func TableDataPrefix(database string, tableID, generation uint64) string {
	return tableDataPrefixFor(database, tableID, generation)
}

// TableGeneration reads a table's current generation directly through kv --
// the same plain, non-CAS read TableService.GetTable itself does internally
// (readGeneration), including that same "absent key means generation 0"
// contract for a table that has never been TRUNCATEd -- for a caller that
// already has a scoped KVServicer and a TableID but not a TableServicer.
// pkg/sql/provider is the caller, from two places: RelationalTable.
// checkGenerationStale, distinguishing a TRUNCATE race from an ordinary
// duplicate-key conflict after a DML statement's compare-and-swap fails; and
// the ALTER TABLE DROP/MODIFY COLUMN background rewrite, re-checking the
// generation as it writes re-encoded rows, since TRUNCATE racing an
// in-progress rewrite must fail the rewrite's stale attempt rather than let
// it write into a generation that is about to be reclaimed (go-qrdb#547).
// It is exported for the same reason TableDataPrefix/TableIndexPrefix are:
// reading it should not require depending on unexported TableService
// internals.
func TableGeneration(ctx context.Context, kv KVServicer, database string, tableID uint64) (uint64, error) {
	data, err := kv.Get(ctx, tableGenerationKey(database, tableID))
	if err != nil {
		if isKeyNotFound(err) {
			return 0, nil
		}
		return 0, err
	}
	if data == nil {
		return 0, nil
	}
	return decodeSequenceCounter(data), nil
}

// TableGenerationKey returns the key holding a table's current generation
// counter, for a caller that needs to name it directly -- e.g. to build a
// Compare that fences a DML commit on the generation it was planned under
// (see the RelationalTable.Insert-family doc comments in pkg/sql/provider).
// It is exported for the same reason TableDataPrefix/TableGeneration are.
func TableGenerationKey(database string, tableID uint64) string {
	return tableGenerationKey(database, tableID)
}

// EncodeTableGeneration encodes generation in the same 8-byte big-endian
// format TableService itself writes to TableGenerationKey, so a caller
// building a CompareValue Compare against that key (see TableGenerationKey)
// always compares against a byte-identical encoding.
func EncodeTableGeneration(generation uint64) []byte {
	return encodeSequenceCounter(generation)
}

// DecodeTableGeneration decodes a generation value in the same 8-byte
// big-endian format EncodeTableGeneration produces, for a caller that read
// a generation key's raw bytes directly (e.g. a buffered transaction
// detecting which registered generation fence went stale at commit time --
// see WriteSet.staleResolution in pkg/sql/provider) rather than through
// TableGeneration. A value shorter than 8 bytes, including absent (nil),
// decodes as 0, matching decodeSequenceCounter/TableGeneration.
func DecodeTableGeneration(data []byte) uint64 {
	return decodeSequenceCounter(data)
}

// TableIndexPrefix returns the key prefix enumerating one secondary index's
// entries for a relational table at the given generation -- the same
// physical key layout TableService itself proposes writes under (see
// tableIndexPrefixFor in table_types.go). It is exported for the same reason
// as TableDataPrefix, and generation carries the same contract.
func TableIndexPrefix(database string, tableID, generation, incarnation uint64) string {
	return tableIndexPrefixFor(database, tableID, generation, incarnation)
}

// TableLayoutCurrent is the physical key layout every table this build
// creates is written under (TableDef.Layout): rows under
// TableDataPrefix(database, tableID, generation), index entries under
// TableIndexPrefix(database, tableID, generation, incarnation).
const TableLayoutCurrent uint32 = 1

// checkTableLayout refuses a definition written under any layout other than
// TableLayoutCurrent. Reading one would address keys that were never
// written -- an empty table -- and, since such a record carries no index
// incarnations, would make every one of its indexes share one keyspace.
func checkTableLayout(def *TableDef, database, name string) error {
	if def.Layout == TableLayoutCurrent {
		return nil
	}
	legacy := *def
	return &TableLayoutUnsupportedError{Database: database, Name: name, TableID: def.TableID, Layout: def.Layout, Definition: &legacy}
}

// LegacyTableKeyPrefixes returns the prefixes a table written under the
// layout that shipped in v0.20.0 through v0.21.0 (TableDef.Layout 0) kept
// its rows and index entries under: rows at "__tbl:<db>:<tableID>/" and
// index entries at "__idx:<db>:<tableID>:<index name>/". It exists for one
// caller, DROP TABLE, so such a table can be dropped -- its data reclaimed --
// and re-created; nothing reads through these prefixes. Neither can overlap
// another table's keys: the TableID segment is terminated by "/" or ":".
func LegacyTableKeyPrefixes(database string, tableID uint64) []string {
	id := strconv.FormatUint(tableID, 10)
	return []string{
		tableDataPrefix + database + ":" + id + "/",
		tableIndexPrefix + database + ":" + id + ":",
	}
}

// TableMetadataKey returns the key holding a table's catalog record, for a
// caller that fences a commit on the definition it resolved (see
// TableDef.MetadataRevision). It is exported for the same reason as
// TableGenerationKey.
func TableMetadataKey(database, name string) string {
	return tableMetadataKey(database, name)
}

// TableIndexGenerationPrefix returns the prefix enumerating every secondary
// index entry of a relational table at the given generation, whatever the
// index's incarnation -- see tableIndexGenerationPrefixFor. It is exported for the
// same reason as TableDataPrefix.
func TableIndexGenerationPrefix(database string, tableID, generation uint64) string {
	return tableIndexGenerationPrefixFor(database, tableID, generation)
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
