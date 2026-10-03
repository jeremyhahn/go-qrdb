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
	"context"
	"errors"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/mysql"
	"github.com/dolthub/vitess/go/sqltypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/catalog"
)

// ---------------------------------------------------------------------------
// Multi-column (composite) secondary indexes.
//
// The defect these cover: an index over more than one column was resolved,
// encoded, enforced, and reported as if it covered only its LEADING column.
// A UNIQUE index on (name, nick_name) therefore rejected ('a','y') once
// ('a','x') existed -- a legitimate row silently refused -- and
// information_schema reported COLUMN_KEY 'UNI' on name where MySQL reports
// 'MUL'.
//
// Everything below is driven through real SQL text against pkg/sql/engine
// wherever the assertion is about observable behaviour, and against the
// resolved indexSpec only where the assertion is specifically about WHICH
// physical access path was chosen (which SQL text alone cannot distinguish
// from a correct-but-unaccelerated scan).
// ---------------------------------------------------------------------------

// compositeIndexProvider creates dbName and runs each statement in ddl
// against it, returning the provider for further queries.
func compositeIndexProvider(t *testing.T, h *ddlHarness, dbName string, ddl ...string) *Provider {
	t.Helper()
	prov := h.newDDLProvider(dbName)
	for _, stmt := range ddl {
		mustExecQuery(t, prov, dbName, stmt)
	}
	return prov
}

// rowValues flattens single-column result rows into a comparable slice.
func rowValues(rows []sqle.Row) []any {
	out := make([]any, len(rows))
	for i, r := range rows {
		out[i] = r[0]
	}
	return out
}

// TestCompositeIndex_UniqueOverWholeTuple is the exact reported reproduction:
// a UNIQUE index on (name, nick_name) must admit two rows that agree on name
// and differ on nick_name, and must still reject a genuine whole-tuple
// duplicate with MySQL error 1062.
func TestCompositeIndex_UniqueOverWholeTuple(t *testing.T) {
	h := newDDLHarness()
	prov := compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, name VARCHAR(64), nick_name VARCHAR(64))",
		"CREATE UNIQUE INDEX idx_all_names ON t(name, nick_name)",
		"INSERT INTO t VALUES (1,'a','x')",
	)

	// The row the leading-column-only index used to reject.
	mustExecQuery(t, prov, "shop", "INSERT INTO t VALUES (2,'a','y')")

	_, rows := mustExecQuery(t, prov, "shop", "SELECT id FROM t ORDER BY id")
	assert.Equal(t, []any{int64(1), int64(2)}, rowValues(rows))

	// A whole-tuple duplicate is still a duplicate.
	_, _, err := execQuery(prov, "shop", "INSERT INTO t VALUES (3,'a','x')")
	require.Error(t, err)
	sqlErr := sqle.CastSQLError(err)
	require.NotNil(t, sqlErr)
	assert.EqualValues(t, mysql.ERDupEntry, sqlErr.Num)

	// ...and the rejected row must not exist.
	_, rows = mustExecQuery(t, prov, "shop", "SELECT COUNT(*) FROM t")
	assert.EqualValues(t, 2, rows[0][0])
}

// TestCompositeIndex_UniqueDifferingOnLeadingColumn is the mirror of
// TestCompositeIndex_UniqueOverWholeTuple: two rows agreeing on the TRAILING
// column and differing on the leading one must also coexist.
func TestCompositeIndex_UniqueDifferingOnLeadingColumn(t *testing.T) {
	h := newDDLHarness()
	prov := compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, name VARCHAR(64), nick_name VARCHAR(64))",
		"CREATE UNIQUE INDEX idx_all_names ON t(name, nick_name)",
		"INSERT INTO t VALUES (1,'a','x')",
		"INSERT INTO t VALUES (2,'b','x')",
	)

	_, rows := mustExecQuery(t, prov, "shop", "SELECT name FROM t WHERE nick_name = 'x' ORDER BY name")
	assert.Equal(t, []any{"a", "b"}, rowValues(rows))
}

// TestCompositeIndex_InformationSchemaStatistics covers the reporting half
// of the defect: every index column appears in information_schema.STATISTICS
// with its own SEQ_IN_INDEX, and COLUMN_KEY consequently reports MySQL's
// 'MUL' for the leading column of a composite UNIQUE index rather than 'UNI'.
func TestCompositeIndex_InformationSchemaStatistics(t *testing.T) {
	h := newDDLHarness()
	prov := compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, name VARCHAR(64), nick_name VARCHAR(64))",
		"CREATE UNIQUE INDEX idx_all_names ON t(name, nick_name)",
	)

	_, rows := mustExecQuery(t, prov, "shop",
		`SELECT SEQ_IN_INDEX, COLUMN_NAME, NON_UNIQUE FROM information_schema.statistics
		 WHERE TABLE_NAME = 't' AND INDEX_NAME = 'idx_all_names' ORDER BY SEQ_IN_INDEX`)
	require.Len(t, rows, 2, "every column of a composite index must be reported")
	assert.EqualValues(t, 1, rows[0][0])
	assert.Equal(t, "name", rows[0][1])
	assert.EqualValues(t, 0, rows[0][2], "the index is unique")
	assert.EqualValues(t, 2, rows[1][0])
	assert.Equal(t, "nick_name", rows[1][1])

	_, rows = mustExecQuery(t, prov, "shop",
		"SELECT COLUMN_NAME, COLUMN_KEY FROM information_schema.columns WHERE TABLE_NAME = 't' ORDER BY ORDINAL_POSITION")
	require.Len(t, rows, 3)
	assert.Equal(t, "PRI", rows[0][1])
	assert.Equal(t, "MUL", rows[1][1], "MySQL reports MUL, not UNI, for the leading column of a composite UNIQUE index")
	assert.Equal(t, "", rows[2][1])
}

// TestCompositeIndex_ThreeColumnUnique covers a 3-column UNIQUE index:
// coexistence when any single column differs, rejection only on a full
// three-way match.
func TestCompositeIndex_ThreeColumnUnique(t *testing.T) {
	h := newDDLHarness()
	prov := compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, a VARCHAR(16), b BIGINT, c VARCHAR(16))",
		"CREATE UNIQUE INDEX idx_abc ON t(a, b, c)",
		"INSERT INTO t VALUES (1,'x',1,'p')",
		"INSERT INTO t VALUES (2,'x',1,'q')", // differs in c
		"INSERT INTO t VALUES (3,'x',2,'p')", // differs in b
		"INSERT INTO t VALUES (4,'y',1,'p')", // differs in a
	)

	_, rows := mustExecQuery(t, prov, "shop", "SELECT COUNT(*) FROM t")
	assert.EqualValues(t, 4, rows[0][0])

	_, _, err := execQuery(prov, "shop", "INSERT INTO t VALUES (5,'x',1,'p')")
	require.Error(t, err)
	assert.EqualValues(t, mysql.ERDupEntry, sqle.CastSQLError(err).Num)
}

// TestCompositeIndex_ThreeColumnNonUnique covers a 3-column NON-unique
// index: full duplicates are permitted, and both whole-tuple and
// leading-prefix lookups return every matching row.
func TestCompositeIndex_ThreeColumnNonUnique(t *testing.T) {
	h := newDDLHarness()
	prov := compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, a VARCHAR(16) NOT NULL, b BIGINT NOT NULL, c VARCHAR(16) NOT NULL)",
		"CREATE INDEX idx_abc ON t(a, b, c)",
		"INSERT INTO t VALUES (1,'x',1,'p')",
		"INSERT INTO t VALUES (2,'x',1,'p')", // an exact duplicate tuple
		"INSERT INTO t VALUES (3,'x',2,'p')",
		"INSERT INTO t VALUES (4,'y',1,'p')",
	)

	_, rows := mustExecQuery(t, prov, "shop",
		"SELECT id FROM t WHERE a = 'x' AND b = 1 AND c = 'p' ORDER BY id")
	assert.Equal(t, []any{int64(1), int64(2)}, rowValues(rows))

	_, rows = mustExecQuery(t, prov, "shop", "SELECT id FROM t WHERE a = 'x' ORDER BY id")
	assert.Equal(t, []any{int64(1), int64(2), int64(3)}, rowValues(rows))

	_, rows = mustExecQuery(t, prov, "shop", "SELECT id FROM t WHERE a = 'x' AND b = 1 ORDER BY id")
	assert.Equal(t, []any{int64(1), int64(2)}, rowValues(rows))
}

// TestCompositeIndex_LeadingPrefixLookupIsIndexAccelerated asserts the
// ACCESS PATH, not merely the answer: a predicate binding a leading prefix
// of a composite index must be resolved from the index's physical entries
// (an indexedPartition), never by scanning the table's data prefix.
func TestCompositeIndex_LeadingPrefixLookupIsIndexAccelerated(t *testing.T) {
	h := newDDLHarness()
	compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, a VARCHAR(16) NOT NULL, b BIGINT NOT NULL)",
		"CREATE INDEX idx_ab ON t(a, b)",
		"INSERT INTO t VALUES (1,'x',1)",
		"INSERT INTO t VALUES (2,'x',2)",
		"INSERT INTO t VALUES (3,'y',1)",
	)
	tbl := compositeHarnessTable(t, h, "shop", "t")

	strType := tbl.schema[1].Type
	lookup := sqle.IndexLookup{
		Index: tableIndexByID(t, tbl, "idx_ab"),
		Ranges: sqle.MySQLRangeCollection{sqle.MySQLRange{
			sqle.ClosedRangeColumnExpr("x", "x", strType),
			sqle.AllRangeColumnExpr(types.Int64),
		}},
	}

	iter, err := tbl.LookupPartitions(newTestContext(), lookup)
	require.NoError(t, err)
	part, err := iter.Next(newTestContext())
	require.NoError(t, err)

	ip, ok := part.(*indexedPartition)
	require.True(t, ok, "a leading-prefix predicate must be answered from the index, not a table scan")
	assert.Len(t, ip.keys, 2, "both rows with a='x' must be found through the index")
}

// TestCompositeIndex_WholeTupleUniqueLookupIsPointRead asserts that a
// whole-tuple predicate on a composite UNIQUE index resolves through a
// single KV point read, never a scan of the table's data prefix.
func TestCompositeIndex_WholeTupleUniqueLookupIsPointRead(t *testing.T) {
	h := newDDLHarness()
	compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, a VARCHAR(16) NOT NULL, b BIGINT NOT NULL)",
		"CREATE UNIQUE INDEX idx_ab ON t(a, b)",
		"INSERT INTO t VALUES (1,'x',1)",
		"INSERT INTO t VALUES (2,'x',2)",
	)
	tbl := compositeHarnessTable(t, h, "shop", "t")

	lookup := sqle.IndexLookup{
		Index: tableIndexByID(t, tbl, "idx_ab"),
		Ranges: sqle.MySQLRangeCollection{sqle.MySQLRange{
			sqle.ClosedRangeColumnExpr("x", "x", tbl.schema[1].Type),
			sqle.ClosedRangeColumnExpr(int64(2), int64(2), tbl.schema[2].Type),
		}},
	}

	iter, err := tbl.LookupPartitions(newTestContext(), lookup)
	require.NoError(t, err)
	part, err := iter.Next(newTestContext())
	require.NoError(t, err)

	ip, ok := part.(*indexedPartition)
	require.True(t, ok)
	require.Len(t, ip.keys, 1)

	rowIter, err := tbl.PartitionRows(newTestContext(), ip)
	require.NoError(t, err)
	row, err := rowIter.Next(newTestContext())
	require.NoError(t, err)
	assert.Equal(t, int64(2), row[0])
}

// TestCompositeIndex_NonLeadingColumnOnlyFallsBackToFilteredScan covers a
// predicate that binds a NON-leading index column with the leading one
// unbound: no single prefix scan can answer it, so the table falls back to
// the exact filtered scan -- which must evaluate EVERY range column, not
// just the first, since PreciseMatch promises the planner no re-filtering is
// needed.
func TestCompositeIndex_NonLeadingColumnOnlyFallsBackToFilteredScan(t *testing.T) {
	h := newDDLHarness()
	compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, a VARCHAR(16) NOT NULL, b BIGINT NOT NULL)",
		"CREATE INDEX idx_ab ON t(a, b)",
		"INSERT INTO t VALUES (1,'x',1)",
		"INSERT INTO t VALUES (2,'x',2)",
		"INSERT INTO t VALUES (3,'y',2)",
	)
	tbl := compositeHarnessTable(t, h, "shop", "t")

	lookup := sqle.IndexLookup{
		Index: tableIndexByID(t, tbl, "idx_ab"),
		Ranges: sqle.MySQLRangeCollection{sqle.MySQLRange{
			sqle.AllRangeColumnExpr(tbl.schema[1].Type),
			sqle.ClosedRangeColumnExpr(int64(2), int64(2), tbl.schema[2].Type),
		}},
	}

	iter, err := tbl.LookupPartitions(newTestContext(), lookup)
	require.NoError(t, err)
	part, err := iter.Next(newTestContext())
	require.NoError(t, err)

	fp, ok := part.(*filteredPartition)
	require.True(t, ok, "an unbound leading column must fall back to the filtered scan")
	require.Len(t, fp.rows, 2, "the filtered scan must evaluate the bound trailing column")
	assert.Equal(t, int64(2), fp.rows[0][0])
	assert.Equal(t, int64(3), fp.rows[1][0])
}

// TestCompositeIndex_NullInNonLeadingColumn covers NULL in a non-leading
// index column. MySQL's UNIQUE constraint does not apply to a tuple
// containing NULL, so any number of such rows coexist; and, because a
// NULL-bearing row carries no index entry at all, a leading-prefix lookup
// that leaves the nullable column unbound must NOT be answered from the
// index -- doing so would silently omit exactly those rows.
func TestCompositeIndex_NullInNonLeadingColumn(t *testing.T) {
	h := newDDLHarness()
	prov := compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, a VARCHAR(16) NOT NULL, b VARCHAR(16) NULL)",
		"CREATE UNIQUE INDEX idx_ab ON t(a, b)",
		"INSERT INTO t VALUES (1,'x',NULL)",
		"INSERT INTO t VALUES (2,'x',NULL)", // permitted: NULL never collides
		"INSERT INTO t VALUES (3,'x','p')",
	)

	// The leading-prefix predicate must still see the NULL-bearing rows.
	_, rows := mustExecQuery(t, prov, "shop", "SELECT id FROM t WHERE a = 'x' ORDER BY id")
	assert.Equal(t, []any{int64(1), int64(2), int64(3)}, rowValues(rows))

	_, rows = mustExecQuery(t, prov, "shop", "SELECT id FROM t WHERE a = 'x' AND b IS NULL ORDER BY id")
	assert.Equal(t, []any{int64(1), int64(2)}, rowValues(rows))

	_, rows = mustExecQuery(t, prov, "shop", "SELECT id FROM t WHERE a = 'x' AND b = 'p'")
	assert.Equal(t, []any{int64(3)}, rowValues(rows))

	// A non-NULL whole-tuple duplicate is still rejected.
	_, _, err := execQuery(prov, "shop", "INSERT INTO t VALUES (4,'x','p')")
	require.Error(t, err)
	assert.EqualValues(t, mysql.ERDupEntry, sqle.CastSQLError(err).Num)
}

// TestCompositeIndex_NullableTrailingColumnDisablesPrefixAcceleration is the
// access-path half of TestCompositeIndex_NullInNonLeadingColumn: the
// leading-prefix lookup is CORRECT there only because it is deliberately not
// answered from the index.
func TestCompositeIndex_NullableTrailingColumnDisablesPrefixAcceleration(t *testing.T) {
	h := newDDLHarness()
	compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, a VARCHAR(16) NOT NULL, b VARCHAR(16) NULL)",
		"CREATE INDEX idx_ab ON t(a, b)",
		"INSERT INTO t VALUES (1,'x',NULL)",
	)
	tbl := compositeHarnessTable(t, h, "shop", "t")

	spec, found := tbl.indexSpecByName("idx_ab")
	require.True(t, found)
	require.True(t, spec.columns[1].nullable)

	r := sqle.MySQLRange{
		sqle.ClosedRangeColumnExpr("x", "x", tbl.schema[1].Type),
		sqle.AllRangeColumnExpr(tbl.schema[2].Type),
	}
	_, _, ok, err := spec.pointPrefix(newTestContext(), r)
	require.NoError(t, err)
	assert.False(t, ok, "an unbound NULLABLE trailing column must disqualify the index")

	// With the nullable column bound to a non-NULL value it is usable again.
	r[1] = sqle.ClosedRangeColumnExpr("p", "p", tbl.schema[2].Type)
	_, bound, ok, err := spec.pointPrefix(newTestContext(), r)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 2, bound)
}

// TestCompositeIndex_UpdateAndDeleteMaintainEntries covers composite index
// maintenance across the whole DML surface: an UPDATE that changes only a
// non-leading column must move the entry (freeing the old tuple for reuse
// and occupying the new one), and a DELETE must free it entirely.
func TestCompositeIndex_UpdateAndDeleteMaintainEntries(t *testing.T) {
	h := newDDLHarness()
	prov := compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, a VARCHAR(16), b VARCHAR(16))",
		"CREATE UNIQUE INDEX idx_ab ON t(a, b)",
		"INSERT INTO t VALUES (1,'x','p')",
		"UPDATE t SET b = 'q' WHERE id = 1",
	)

	_, rows := mustExecQuery(t, prov, "shop", "SELECT id FROM t WHERE a = 'x' AND b = 'q'")
	assert.Equal(t, []any{int64(1)}, rowValues(rows))
	_, rows = mustExecQuery(t, prov, "shop", "SELECT id FROM t WHERE a = 'x' AND b = 'p'")
	assert.Empty(t, rows, "the vacated tuple must no longer resolve")

	// The vacated tuple is free for another row...
	mustExecQuery(t, prov, "shop", "INSERT INTO t VALUES (2,'x','p')")
	// ...and the occupied one is not.
	_, _, err := execQuery(prov, "shop", "INSERT INTO t VALUES (3,'x','q')")
	require.Error(t, err)
	assert.EqualValues(t, mysql.ERDupEntry, sqle.CastSQLError(err).Num)

	mustExecQuery(t, prov, "shop", "DELETE FROM t WHERE id = 1")
	_, rows = mustExecQuery(t, prov, "shop", "SELECT id FROM t WHERE a = 'x' AND b = 'q'")
	assert.Empty(t, rows)
	mustExecQuery(t, prov, "shop", "INSERT INTO t VALUES (4,'x','q')")
}

// TestCompositeIndex_BuiltOverExistingRows covers CREATE INDEX on a table
// that already holds rows: the backfill (IndexBuildingTable.BuildIndex) must
// write whole-tuple entries, so the resulting index enforces and answers
// exactly as one populated by INSERT would.
func TestCompositeIndex_BuiltOverExistingRows(t *testing.T) {
	h := newDDLHarness()
	prov := compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, a VARCHAR(16) NOT NULL, b VARCHAR(16) NOT NULL)",
		"INSERT INTO t VALUES (1,'x','p')",
		"INSERT INTO t VALUES (2,'x','q')",
		"CREATE UNIQUE INDEX idx_ab ON t(a, b)",
	)

	_, rows := mustExecQuery(t, prov, "shop", "SELECT id FROM t WHERE a = 'x' AND b = 'q'")
	assert.Equal(t, []any{int64(2)}, rowValues(rows))

	_, rows = mustExecQuery(t, prov, "shop", "SELECT id FROM t WHERE a = 'x' ORDER BY id")
	assert.Equal(t, []any{int64(1), int64(2)}, rowValues(rows))

	_, _, err := execQuery(prov, "shop", "INSERT INTO t VALUES (3,'x','p')")
	require.Error(t, err)
	assert.EqualValues(t, mysql.ERDupEntry, sqle.CastSQLError(err).Num)
}

// TestCompositeIndex_MultiRowLookupOrderIsDeterministic covers the row order
// of a multi-row indexed lookup. KVServicer.Scan answers with a map, whose
// iteration order Go deliberately randomizes, so returning it directly made
// the row order of the SAME query differ between executions. The order must
// be the index's own -- indexed value, then primary key.
func TestCompositeIndex_MultiRowLookupOrderIsDeterministic(t *testing.T) {
	h := newDDLHarness()
	compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, a VARCHAR(16) NOT NULL, b BIGINT NOT NULL)",
		"CREATE INDEX idx_ab ON t(a, b)",
		"INSERT INTO t VALUES (5,'x',3)",
		"INSERT INTO t VALUES (1,'x',1)",
		"INSERT INTO t VALUES (9,'x',2)",
		"INSERT INTO t VALUES (3,'x',1)",
	)
	tbl := compositeHarnessTable(t, h, "shop", "t")

	spec, found := tbl.indexSpecByName("idx_ab")
	require.True(t, found)
	encoded, err := spec.codecs[0].EncodeValues([]any{"x"})
	require.NoError(t, err)

	// Index order is (b, then id): (1,1), (1,3), (2,9), (3,5).
	want := []int64{1, 3, 9, 5}
	for attempt := 0; attempt < 8; attempt++ {
		keys, lerr := tbl.lookupIndexValue(newTestContext(), spec, encoded, false)
		require.NoError(t, lerr)
		require.Len(t, keys, 4)

		got := make([]int64, len(keys))
		for i, k := range keys {
			vals, derr := tbl.keyCodec.Decode(string(k))
			require.NoError(t, derr)
			got[i] = vals[0].(int64)
		}
		assert.Equal(t, want, got, "a multi-row index lookup must return rows in index order, every time")
	}
}

// ---------------------------------------------------------------------------
// Composite PRIMARY KEY
// ---------------------------------------------------------------------------

// TestCompositePrimaryKey_WholeTupleAndPrefixLookups covers a composite
// PRIMARY KEY resolved through the same machinery as a composite secondary
// index: a whole-tuple predicate is a direct point read, and a
// leading-prefix predicate is a bounded prefix scan over the table's own row
// keys (a primary key needs no separate index structure -- the row's storage
// key IS the key).
func TestCompositePrimaryKey_WholeTupleAndPrefixLookups(t *testing.T) {
	h := newDDLHarness()
	prov := compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (a BIGINT NOT NULL, b VARCHAR(16) NOT NULL, note VARCHAR(32), PRIMARY KEY (a, b))",
		"INSERT INTO t VALUES (1,'x','ax')",
		"INSERT INTO t VALUES (1,'y','ay')",
		"INSERT INTO t VALUES (2,'x','bx')",
	)

	_, rows := mustExecQuery(t, prov, "shop", "SELECT note FROM t WHERE a = 1 AND b = 'y'")
	assert.Equal(t, []any{"ay"}, rowValues(rows))

	_, rows = mustExecQuery(t, prov, "shop", "SELECT note FROM t WHERE a = 1 ORDER BY b")
	assert.Equal(t, []any{"ax", "ay"}, rowValues(rows))

	tbl := compositeHarnessTable(t, h, "shop", "t")
	pkIndex := tableIndexByID(t, tbl, primaryIndexName)
	assert.Equal(t, []string{"t.a", "t.b"}, pkIndex.Expressions(),
		"a composite primary key must report every one of its columns")

	lookup := sqle.IndexLookup{
		Index: pkIndex,
		Ranges: sqle.MySQLRangeCollection{sqle.MySQLRange{
			sqle.ClosedRangeColumnExpr(int64(1), int64(1), tbl.schema[0].Type),
			sqle.AllRangeColumnExpr(tbl.schema[1].Type),
		}},
	}
	iter, err := tbl.LookupPartitions(newTestContext(), lookup)
	require.NoError(t, err)
	part, err := iter.Next(newTestContext())
	require.NoError(t, err)
	ip, ok := part.(*indexedPartition)
	require.True(t, ok, "a composite primary key prefix must resolve to row keys, not a full scan")
	assert.Len(t, ip.keys, 2)
}

// TestCompositePrimaryKey_InformationSchemaStatistics covers a composite
// PRIMARY KEY's information_schema reporting.
func TestCompositePrimaryKey_InformationSchemaStatistics(t *testing.T) {
	h := newDDLHarness()
	prov := compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (a BIGINT NOT NULL, b VARCHAR(16) NOT NULL, PRIMARY KEY (a, b))")

	_, rows := mustExecQuery(t, prov, "shop",
		`SELECT SEQ_IN_INDEX, COLUMN_NAME FROM information_schema.statistics
		 WHERE TABLE_NAME = 't' AND INDEX_NAME = 'PRIMARY' ORDER BY SEQ_IN_INDEX`)
	require.Len(t, rows, 2)
	assert.EqualValues(t, 1, rows[0][0])
	assert.Equal(t, "a", rows[0][1])
	assert.EqualValues(t, 2, rows[1][0])
	assert.Equal(t, "b", rows[1][1])
}

// ---------------------------------------------------------------------------
// Composite FOREIGN KEY
//
// go-mysql-server enforces referential integrity itself, driven entirely
// through sql.ForeignKeyTable plus the index lookups RelationalTable serves
// (see relational_foreign_key.go). A parent-existence check on a composite
// key is therefore exactly a whole-tuple lookup on a composite index -- so
// while the index was truncated to its leading column, a composite foreign
// key either could not be declared at all (no index matched the referenced
// columns) or would have accepted a child row matching only on that one
// column.
// ---------------------------------------------------------------------------

// TestCompositeForeignKey_AgainstCompositeUniqueIndex covers a composite
// foreign key whose parent key is a composite UNIQUE secondary index.
func TestCompositeForeignKey_AgainstCompositeUniqueIndex(t *testing.T) {
	h := newDDLHarness()
	prov := compositeIndexProvider(t, h, "shop",
		"CREATE TABLE p (id BIGINT PRIMARY KEY, a BIGINT NOT NULL, b VARCHAR(16) NOT NULL)",
		"CREATE UNIQUE INDEX idx_ab ON p (a, b)",
		`CREATE TABLE c (id BIGINT PRIMARY KEY, pa BIGINT NOT NULL, pb VARCHAR(16) NOT NULL,
			CONSTRAINT fk_c_p FOREIGN KEY (pa, pb) REFERENCES p (a, b))`,
		"INSERT INTO p VALUES (1,1,'x')",
		"INSERT INTO p VALUES (2,1,'y')",
		"INSERT INTO c VALUES (1,1,'y')",
	)

	// A child whose tuple matches no parent must be refused, even though its
	// LEADING column matches two parents.
	_, _, err := execQuery(prov, "shop", "INSERT INTO c VALUES (2,1,'zzz')")
	require.Error(t, err)
	assert.EqualValues(t, mysql.ErNoReferencedRow2, sqle.CastSQLError(err).Num)

	_, rows := mustExecQuery(t, prov, "shop", "SELECT COUNT(*) FROM c")
	assert.EqualValues(t, 1, rows[0][0])
}

// TestCompositeForeignKey_AgainstCompositePrimaryKey covers a composite
// foreign key referencing a composite PRIMARY KEY -- the canonical shape of
// an ORM-generated many-to-many join table.
func TestCompositeForeignKey_AgainstCompositePrimaryKey(t *testing.T) {
	h := newDDLHarness()
	prov := compositeIndexProvider(t, h, "shop",
		"CREATE TABLE p (a BIGINT NOT NULL, b VARCHAR(16) NOT NULL, PRIMARY KEY (a, b))",
		`CREATE TABLE c (id BIGINT PRIMARY KEY, pa BIGINT NOT NULL, pb VARCHAR(16) NOT NULL,
			CONSTRAINT fk_c_p FOREIGN KEY (pa, pb) REFERENCES p (a, b))`,
		"INSERT INTO p VALUES (1,'x')",
		"INSERT INTO p VALUES (1,'y')",
		"INSERT INTO c VALUES (1,1,'x')",
	)

	_, _, err := execQuery(prov, "shop", "INSERT INTO c VALUES (2,1,'zzz')")
	require.Error(t, err)
	assert.EqualValues(t, mysql.ErNoReferencedRow2, sqle.CastSQLError(err).Num)

	_, rows := mustExecQuery(t, prov, "shop", "SELECT pb FROM c ORDER BY id")
	assert.Equal(t, []any{"x"}, rowValues(rows))
}

// ---------------------------------------------------------------------------
// indexSpec unit coverage for shapes SQL text cannot reach directly
// ---------------------------------------------------------------------------

// TestIndexSpec_PrefixLengthDisablesLookups covers an index declaring a
// MySQL column prefix length: its entries carry a truncated value, so an
// entry match is not a value match and the index must never answer a lookup.
func TestIndexSpec_PrefixLengthDisablesLookups(t *testing.T) {
	def := &service.TableDef{
		TableID: 7, Name: "t", Database: "shop",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED"},
			{Name: "name", Type: "VARCHAR(255)"},
		},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
		Indexes: []service.TableIndex{
			{Name: "idx_name", Columns: []service.IndexCol{{Name: "name", PrefixLength: 4}}},
		},
	}
	tbl, err := NewRelationalTable(def, &mockKVService{scanFn: func(context.Context, string) (map[string][]byte, error) {
		return nil, nil
	}}, nil)
	require.NoError(t, err)

	spec, found := tbl.indexSpecByName("idx_name")
	require.True(t, found)
	require.True(t, spec.prefixed)

	r := sqle.MySQLRange{sqle.ClosedRangeColumnExpr("abcdefgh", "abcdefgh", tbl.schema[1].Type)}
	_, _, ok, perr := spec.pointPrefix(newTestContext(), r)
	require.NoError(t, perr)
	assert.False(t, ok, "a prefix-length index must not answer a lookup from its truncated entries")
}

// TestIndexSpec_PointPrefixRejectsMismatchedRangeWidth covers a range whose
// column count does not match the index's, which no correct prefix can be
// derived from.
func TestIndexSpec_PointPrefixRejectsMismatchedRangeWidth(t *testing.T) {
	h := newDDLHarness()
	compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, a VARCHAR(16) NOT NULL, b BIGINT NOT NULL)",
		"CREATE INDEX idx_ab ON t(a, b)")
	tbl := compositeHarnessTable(t, h, "shop", "t")

	spec, found := tbl.indexSpecByName("idx_ab")
	require.True(t, found)

	// One column expression for a two-column index.
	narrow := sqle.MySQLRange{sqle.ClosedRangeColumnExpr("x", "x", tbl.schema[1].Type)}
	_, _, ok, err := spec.pointPrefix(newTestContext(), narrow)
	require.NoError(t, err)
	assert.False(t, ok)

	// Not a MySQLRange at all.
	_, _, ok, err = spec.pointPrefix(newTestContext(), fakeRange{})
	require.NoError(t, err)
	assert.False(t, ok)

	// Every column unbound: nothing to encode a prefix from.
	all := sqle.MySQLRange{
		sqle.AllRangeColumnExpr(tbl.schema[1].Type),
		sqle.AllRangeColumnExpr(tbl.schema[2].Type),
	}
	_, _, ok, err = spec.pointPrefix(newTestContext(), all)
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestBuildIndexSpec_PrefixCodecsAreByteExactPrefixes pins the property the
// whole leading-prefix scan rests on: encoding the first k columns yields
// exactly the byte prefix the full-tuple encoding produces for the same k
// values, for every k.
func TestBuildIndexSpec_PrefixCodecsAreByteExactPrefixes(t *testing.T) {
	h := newDDLHarness()
	compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, a VARCHAR(16) NOT NULL, b BIGINT NOT NULL, c VARCHAR(16) NOT NULL)",
		"CREATE INDEX idx_abc ON t(a, b, c)")
	tbl := compositeHarnessTable(t, h, "shop", "t")

	spec, found := tbl.indexSpecByName("idx_abc")
	require.True(t, found)
	require.Len(t, spec.codecs, 3)

	vals := []any{"x", int64(7), "p"}
	full, err := spec.valueCodec().EncodeValues(vals)
	require.NoError(t, err)

	for k := 1; k <= len(vals); k++ {
		partial, perr := spec.codecs[k-1].EncodeValues(vals[:k])
		require.NoError(t, perr)
		assert.Equal(t, partial, full[:len(partial)],
			"the %d-column encoding must be a byte-exact prefix of the full one", k)
	}
}

// TestIndexSpec_EncodeSkipsNullBearingRows covers indexSpec.encode's NULL
// rule directly: a row with a NULL in any indexed column produces no entry.
func TestIndexSpec_EncodeSkipsNullBearingRows(t *testing.T) {
	h := newDDLHarness()
	compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, a VARCHAR(16) NULL, b VARCHAR(16) NULL)",
		"CREATE INDEX idx_ab ON t(a, b)")
	tbl := compositeHarnessTable(t, h, "shop", "t")

	spec, found := tbl.indexSpecByName("idx_ab")
	require.True(t, found)

	_, ok, err := spec.encode(sqle.Row{int64(1), "x", "p"})
	require.NoError(t, err)
	assert.True(t, ok)

	for _, row := range []sqle.Row{
		{int64(1), nil, "p"}, // NULL in the leading column
		{int64(1), "x", nil}, // NULL in the trailing column
		{int64(1), nil, nil}, // NULL in both
		{int64(1), "x"},      // row shorter than the index needs
	} {
		_, ok, err = spec.encode(row)
		require.NoError(t, err)
		assert.False(t, ok, "row %v must contribute no index entry", row)
	}
}

// compositeHarnessTable resolves table through h's registry exactly as the
// engine does, so the returned *RelationalTable carries the catalog
// definition every preceding DDL statement produced.
func compositeHarnessTable(t *testing.T, h *ddlHarness, database, table string) *RelationalTable {
	t.Helper()
	def, err := h.tableSvc.GetTable(context.Background(), database, table)
	require.NoError(t, err)
	tbl, err := NewRelationalTable(def, h.kvSvc, h.mvccSvc,
		WithTableService(h.tableSvc),
		WithTransactionService(h.txnSvc),
		WithSequenceService(h.seqSvc),
	)
	require.NoError(t, err)
	return tbl
}

// ---------------------------------------------------------------------------
// Error and edge paths of the shared point/prefix derivation
// ---------------------------------------------------------------------------

// TestPointValue_NullBoundIsNotAPointMatch covers a closed range whose bound
// converts to NULL: NULL contributes no index entry and never satisfies an
// equality, so it must be answered by the filtered-scan path rather than by
// encoding a key no entry can ever carry.
func TestPointValue_NullBoundIsNotAPointMatch(t *testing.T) {
	typ := types.MustCreateString(sqltypes.VarChar, 16, sqle.Collation_Default)
	_, ok, err := pointValue(newTestContext(), typ, sqle.ClosedRangeColumnExpr(nil, nil, typ))
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestPointValue_UnconvertibleBoundReportsError covers a bound the column's
// own type cannot represent: the failure is surfaced, never silently
// treated as "not a point match" (which would quietly return a full scan's
// worth of rows for a predicate the planner believes was pushed down).
func TestPointValue_UnconvertibleBoundReportsError(t *testing.T) {
	expr := sqle.ClosedRangeColumnExpr("not-a-number", "not-a-number", types.Int64)
	_, ok, err := pointValue(newTestContext(), types.Int64, expr)
	require.Error(t, err)
	assert.False(t, ok)
}

// TestSecondaryIndexKeysFromRanges_PropagatesPointValueError covers error
// propagation out of the per-range derivation.
func TestSecondaryIndexKeysFromRanges_PropagatesPointValueError(t *testing.T) {
	h := newDDLHarness()
	compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, a BIGINT NOT NULL, b BIGINT NOT NULL)",
		"CREATE INDEX idx_ab ON t(a, b)")
	tbl := compositeHarnessTable(t, h, "shop", "t")

	spec, found := tbl.indexSpecByName("idx_ab")
	require.True(t, found)

	bad := sqle.MySQLRange{
		sqle.ClosedRangeColumnExpr("not-a-number", "not-a-number", types.Int64),
		sqle.AllRangeColumnExpr(types.Int64),
	}
	_, _, err := tbl.secondaryIndexKeysFromRanges(newTestContext(), spec, []sqle.Range{bad})
	assert.Error(t, err)
}

// TestPrimaryKeysFromRanges_PrefixScanError covers the failure of the data
// prefix scan a composite primary key's leading-prefix lookup performs.
func TestPrimaryKeysFromRanges_PrefixScanError(t *testing.T) {
	scanErr := errors.New("scan failed")
	def := &service.TableDef{
		TableID: 11, Name: "t", Database: "shop",
		Columns: []service.ColumnDef{
			{Name: "a", Type: "BIGINT"},
			{Name: "b", Type: "BIGINT"},
		},
		PrimaryKey: []service.PKColumn{{Name: "a"}, {Name: "b"}},
	}
	tbl, err := NewRelationalTable(def, &mockKVService{
		scanFn: func(context.Context, string) (map[string][]byte, error) { return nil, scanErr },
	}, nil)
	require.NoError(t, err)

	r := sqle.MySQLRange{
		sqle.ClosedRangeColumnExpr(int64(1), int64(1), types.Int64),
		sqle.AllRangeColumnExpr(types.Int64),
	}
	_, _, perr := tbl.primaryKeysFromRanges(newTestContext(), []sqle.Range{r})
	assert.ErrorIs(t, perr, scanErr)
}

// TestPrimaryKeysFromRanges_DedupsOverlappingPrefixes covers the
// deduplication a set of overlapping ranges needs: a row reached through two
// different ranges must be returned once, not twice.
func TestPrimaryKeysFromRanges_DedupsOverlappingPrefixes(t *testing.T) {
	h := newDDLHarness()
	compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (a BIGINT NOT NULL, b BIGINT NOT NULL, PRIMARY KEY (a, b))",
		"INSERT INTO t VALUES (1,1)",
		"INSERT INTO t VALUES (1,2)")
	tbl := compositeHarnessTable(t, h, "shop", "t")

	prefix := sqle.MySQLRange{
		sqle.ClosedRangeColumnExpr(int64(1), int64(1), tbl.schema[0].Type),
		sqle.AllRangeColumnExpr(tbl.schema[1].Type),
	}
	whole := sqle.MySQLRange{
		sqle.ClosedRangeColumnExpr(int64(1), int64(1), tbl.schema[0].Type),
		sqle.ClosedRangeColumnExpr(int64(2), int64(2), tbl.schema[1].Type),
	}

	keys, exact, err := tbl.primaryKeysFromRanges(newTestContext(), []sqle.Range{prefix, whole})
	require.NoError(t, err)
	require.True(t, exact)
	assert.Len(t, keys, 2, "the row reachable through both ranges must appear once")
}

// TestBuildIndexSpecs_SurfacesUnencodableColumn covers the distinction
// buildIndexSpecs draws between the two failure kinds: an index that cannot
// be RESOLVED against the schema is skipped, but one whose column type has
// no order-preserving key encoding at all is surfaced, since silently
// dropping it would leave a declared index the writer never maintains.
func TestBuildIndexSpecs_SurfacesUnencodableColumn(t *testing.T) {
	def := &service.TableDef{
		TableID: 12, Name: "t", Database: "shop",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT"},
			{Name: "amount", Type: "DECIMAL(10,2)"},
		},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
		Indexes: []service.TableIndex{
			{Name: "idx_amount", Columns: []service.IndexCol{{Name: "amount"}}},
		},
	}
	_, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.Error(t, err)
	var unsupported *catalog.UnsupportedKeyColumnTypeError
	assert.ErrorAs(t, err, &unsupported)
}

// TestIndexSpec_EncodeSurfacesEncodingError covers indexSpec.encode's error
// path: a value whose Go type does not match its column's declared type.
func TestIndexSpec_EncodeSurfacesEncodingError(t *testing.T) {
	h := newDDLHarness()
	compositeIndexProvider(t, h, "shop",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, a BIGINT NOT NULL, b BIGINT NOT NULL)",
		"CREATE INDEX idx_ab ON t(a, b)")
	tbl := compositeHarnessTable(t, h, "shop", "t")

	spec, found := tbl.indexSpecByName("idx_ab")
	require.True(t, found)

	_, ok, err := spec.encode(sqle.Row{int64(1), int64(1), "not-an-int"})
	require.Error(t, err)
	assert.False(t, ok)
}

// TestRowMatchesRanges_IgnoresExtraRangeColumns covers a range carrying more
// column expressions than the resolved index has columns: the surplus has no
// row column to test and is ignored rather than indexed out of range.
func TestRowMatchesRanges_IgnoresExtraRangeColumns(t *testing.T) {
	typ := types.Int64
	ranges := []sqle.Range{sqle.MySQLRange{
		sqle.ClosedRangeColumnExpr(int64(1), int64(1), typ),
		sqle.ClosedRangeColumnExpr(int64(9), int64(9), typ),
	}}

	match, err := rowMatchesRanges(newTestContext(), []sqle.Type{typ}, []int{0}, sqle.Row{int64(1)}, ranges)
	require.NoError(t, err)
	assert.True(t, match)
}

// TestQRDBIndex_CoversColumnsIsAlwaysFalse pins the index-only-scan refusal:
// a QRDB index stores only the indexed tuple and the row's key, so no
// projection can ever be answered without fetching the row.
func TestQRDBIndex_CoversColumnsIsAlwaysFalse(t *testing.T) {
	idx := newRelationalTableIndex("shop", "t", "idx_ab",
		[]indexColumn{{name: "a", fieldType: "int64"}, {name: "b", fieldType: "string"}}, true, "note")

	assert.False(t, idx.CoversColumns(nil))
	assert.False(t, idx.CoversColumns([]string{"a"}))
	assert.False(t, idx.CoversColumns([]string{"a", "b"}))
	assert.Equal(t, "note", idx.Comment())
	assert.Equal(t, []string{"t.a", "t.b"}, idx.Expressions())

	types := idx.ColumnExpressionTypes(newTestContext())
	require.Len(t, types, 2)
	assert.Equal(t, "t.a", types[0].Expression)
	assert.Equal(t, "t.b", types[1].Expression)
}
