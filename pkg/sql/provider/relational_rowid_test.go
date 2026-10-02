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
	"sort"
	"strings"
	"sync"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/catalog"
)

// ---------------------------------------------------------------------------
// Unit coverage for the hidden row id: allocation, injection, and the
// sequence it is drawn from.
// ---------------------------------------------------------------------------

// newRowIDTable builds a RelationalTable over h for a keyless two-column
// table, going through the real TableService so the TableID (and therefore
// the sequence name) is a real allocated one.
func newRowIDTable(t *testing.T, h *ddlHarness, name string) *RelationalTable {
	t.Helper()
	def, err := h.tableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "shop",
		Name:     name,
		Columns: []service.ColumnDef{
			{Name: "name", Type: "VARCHAR(64)", Nullable: true},
			{Name: "n", Type: "BIGINT UNSIGNED", Nullable: true},
		},
	})
	require.NoError(t, err)

	tbl, err := NewRelationalTable(def, h.kvSvc, h.mvccSvc,
		WithTableService(h.tableSvc),
		WithTransactionService(h.txnSvc),
		WithSequenceService(h.seqSvc),
	)
	require.NoError(t, err)
	return tbl
}

// newKeyedTable is newRowIDTable's counterpart with a declared primary key.
func newKeyedTable(t *testing.T, h *ddlHarness, name string) *RelationalTable {
	t.Helper()
	def, err := h.tableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "shop",
		Name:     name,
		Columns: []service.ColumnDef{
			{Name: "name", Type: "VARCHAR(64)", Nullable: true},
			{Name: "n", Type: "BIGINT UNSIGNED"},
		},
		PrimaryKey: []service.PKColumn{{Name: "n"}},
	})
	require.NoError(t, err)

	tbl, err := NewRelationalTable(def, h.kvSvc, h.mvccSvc,
		WithTransactionService(h.txnSvc),
		WithSequenceService(h.seqSvc),
	)
	require.NoError(t, err)
	return tbl
}

// ---------------------------------------------------------------------------
// rowIDSequenceName
// ---------------------------------------------------------------------------

// TestRowIDSequenceName_DistinctFromAutoIncrement pins the invariant that
// makes a keyless table with an AUTO_INCREMENT column safe: the two
// generated values come from two different counters, so neither can consume
// the other's values.
func TestRowIDSequenceName_DistinctFromAutoIncrement(t *testing.T) {
	h := newDDLHarness()
	tbl := newRowIDTable(t, h, "seqnames")

	assert.NotEqual(t, tbl.autoIncSequenceName(), tbl.rowIDSequenceName())
	assert.Equal(t, service.TableRowIDSequenceName(tbl.def.TableID), tbl.rowIDSequenceName())
}

// TestRowIDSequenceName_KeyedByTableID verifies the sequence is derived from
// the immutable TableID, not the mutable name, so a RENAME TABLE cannot
// reset it and start handing out row ids that already exist.
func TestRowIDSequenceName_KeyedByTableID(t *testing.T) {
	h := newDDLHarness()
	a := newRowIDTable(t, h, "seqa")
	b := newRowIDTable(t, h, "seqb")

	assert.NotEqual(t, a.rowIDSequenceName(), b.rowIDSequenceName())
	assert.Contains(t, a.rowIDSequenceName(), "__rowid:")
}

// ---------------------------------------------------------------------------
// allocateRowID
// ---------------------------------------------------------------------------

// TestAllocateRowID_FillsEmptySlot verifies the common INSERT case: the
// hidden slot arrives nil and is filled with a fresh, monotonically
// increasing value, leaving the caller's row untouched.
func TestAllocateRowID_FillsEmptySlot(t *testing.T) {
	h := newDDLHarness()
	tbl := newRowIDTable(t, h, "alloc")
	ctx := newTestContext()

	in := sqle.Row{"a", uint64(7), nil}
	first, err := tbl.allocateRowID(ctx, in)
	require.NoError(t, err)
	require.Len(t, first, 3)
	assert.Equal(t, uint64(1), first[2], "sequences start at 1")
	assert.Nil(t, in[2], "the caller's row must not be mutated")

	second, err := tbl.allocateRowID(ctx, sqle.Row{"b", uint64(7), nil})
	require.NoError(t, err)
	assert.Equal(t, uint64(2), second[2])
}

// TestAllocateRowID_KeepsExistingRowID verifies a row that already carries a
// row id is returned untouched, which is what keeps a REPLACE retry or a
// compare-and-swap retry from burning a row id per attempt.
func TestAllocateRowID_KeepsExistingRowID(t *testing.T) {
	h := newDDLHarness()
	tbl := newRowIDTable(t, h, "keep")

	in := sqle.Row{"a", uint64(7), uint64(99)}
	out, err := tbl.allocateRowID(newTestContext(), in)
	require.NoError(t, err)
	assert.Equal(t, uint64(99), out[2])

	peek, err := h.seqSvc.Peek(context.Background(), "shop", tbl.rowIDSequenceName())
	require.NoError(t, err)
	assert.Equal(t, uint64(0), peek, "no row id should have been allocated")
}

// TestAllocateRowID_KeyedTableIsUntouched verifies a table with a declared
// primary key never consults a sequence at all.
func TestAllocateRowID_KeyedTableIsUntouched(t *testing.T) {
	h := newDDLHarness()
	tbl := newKeyedTable(t, h, "keyed")

	in := sqle.Row{"a", uint64(7)}
	out, err := tbl.allocateRowID(newTestContext(), in)
	require.NoError(t, err)
	assert.Equal(t, in, out)

	peek, err := h.seqSvc.Peek(context.Background(), "shop", tbl.rowIDSequenceName())
	require.NoError(t, err)
	assert.Equal(t, uint64(0), peek)
}

// TestAllocateRowID_WrongWidthRowIsLeftForTheKeyCodec verifies a row that is
// not the table's full schema width is passed through unchanged, so
// catalog.KeyCodec.Encode reports the real problem (*MissingRowIDError)
// rather than this function inventing a second, less specific one.
func TestAllocateRowID_WrongWidthRowIsLeftForTheKeyCodec(t *testing.T) {
	h := newDDLHarness()
	tbl := newRowIDTable(t, h, "narrow")

	narrow := sqle.Row{"a", uint64(7)}
	out, err := tbl.allocateRowID(newTestContext(), narrow)
	require.NoError(t, err)
	assert.Equal(t, narrow, out)

	_, err = tbl.keyCodec.Encode(out)
	var missing *catalog.MissingRowIDError
	assert.ErrorAs(t, err, &missing)
}

// TestAllocateRowID_RequiresSequenceService verifies a keyless table built
// without a SequenceServicer fails with the typed error naming the missing
// dependency rather than panicking on a nil interface.
func TestAllocateRowID_RequiresSequenceService(t *testing.T) {
	h := newDDLHarness()
	def, err := h.tableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "shop",
		Name:     "noseq",
		Columns:  []service.ColumnDef{{Name: "name", Type: "VARCHAR(64)", Nullable: true}},
	})
	require.NoError(t, err)

	tbl, err := NewRelationalTable(def, h.kvSvc, h.mvccSvc, WithTransactionService(h.txnSvc))
	require.NoError(t, err)

	_, err = tbl.allocateRowID(newTestContext(), sqle.Row{"a", nil})
	var unavailable *SequenceServiceUnavailableError
	assert.ErrorAs(t, err, &unavailable)
}

// ---------------------------------------------------------------------------
// decodeRow
// ---------------------------------------------------------------------------

// TestDecodeRow_InjectsRowIDFromKey verifies a stored row read back under
// its own key carries the row id that key encodes, which is what makes the
// row subsequently UPDATE-able and DELETE-able.
func TestDecodeRow_InjectsRowIDFromKey(t *testing.T) {
	h := newDDLHarness()
	tbl := newRowIDTable(t, h, "decode")

	row := sqle.Row{"a", uint64(7), uint64(42)}
	pk, err := tbl.keyCodec.Encode(row)
	require.NoError(t, err)
	encoded, err := tbl.rowCodec.Encode(row)
	require.NoError(t, err)

	got, err := tbl.decodeRow(pk, encoded)
	require.NoError(t, err)
	assert.Equal(t, row, got)
}

// TestDecodeRow_KeyedTableIsUnchanged verifies the injection is skipped
// entirely for a table with a declared primary key, whose rows have no
// hidden slot to fill.
func TestDecodeRow_KeyedTableIsUnchanged(t *testing.T) {
	h := newDDLHarness()
	tbl := newKeyedTable(t, h, "decodekeyed")

	row := sqle.Row{"a", uint64(7)}
	encoded, err := tbl.rowCodec.Encode(row)
	require.NoError(t, err)

	got, err := tbl.decodeRow("ignored-key", encoded)
	require.NoError(t, err)
	assert.Equal(t, row, got)
}

// TestDecodeRow_PropagatesDecodeErrors covers both failure paths: a corrupt
// row value and a corrupt key.
func TestDecodeRow_PropagatesDecodeErrors(t *testing.T) {
	h := newDDLHarness()
	tbl := newRowIDTable(t, h, "decodeerr")

	_, err := tbl.decodeRow("", []byte("not a row"))
	assert.Error(t, err, "a corrupt row value must surface")

	valid, err := tbl.rowCodec.Encode(sqle.Row{"a", uint64(7), uint64(1)})
	require.NoError(t, err)
	_, err = tbl.decodeRow("", valid)
	assert.Error(t, err, "a key that does not decode to a row id must surface")
}

// ---------------------------------------------------------------------------
// Cluster safety
// ---------------------------------------------------------------------------

// TestRowIDAllocation_IsClusterSafe is the proof that row id allocation is
// not process-local. Two independent RelationalTable instances resolved over
// the SAME service.SequenceService and store -- which is exactly what two
// cluster nodes serving the same table are, since SequenceService's every
// increment is a compare-and-swap over the replicated counter -- concurrently
// insert rows, and no row id is ever handed out twice.
//
// A process-local generator (each table instance counting from 1 in memory)
// passes every single-instance test and fails this one immediately: both
// instances would produce row id 1, and one row would silently overwrite the
// other.
func TestRowIDAllocation_IsClusterSafe(t *testing.T) {
	const perNode = 64

	h := newDDLHarness()
	nodeA := newRowIDTable(t, h, "cluster")

	// Resolve a SECOND, independent table instance for the same table, over
	// its OWN SequenceService object -- its own in-memory block cursor, the
	// same replicated counter underneath. That is exactly the shape of a
	// second cluster node: SequenceService holds no state of its own, only a
	// handle on the replicated store and a TransactionServicer to propose
	// compare-and-swaps through.
	def, err := h.tableSvc.GetTable(context.Background(), "shop", "cluster")
	require.NoError(t, err)
	nodeBSeq := service.NewSequenceService(h.store, &memTxn{store: h.store})
	nodeB, err := NewRelationalTable(def, h.kvSvc, h.mvccSvc,
		WithTransactionService(&memTxn{store: h.store}),
		WithSequenceService(nodeBSeq),
	)
	require.NoError(t, err)
	require.NotSame(t, nodeA.rowIDs, nodeB.rowIDs)

	insert := func(tbl *RelationalTable) {
		w := &relationalWriter{table: tbl}
		for i := 0; i < perNode; i++ {
			// Every row is byte-identical, so nothing but the row id can
			// possibly distinguish the resulting storage keys.
			assert.NoError(t, writeStatement(t, func(ctx *sqle.Context) error {
				return w.Insert(ctx, sqle.Row{"same", uint64(7), nil})
			}))
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); insert(nodeA) }()
	go func() { defer wg.Done(); insert(nodeB) }()
	wg.Wait()

	// Every insert must have produced its own storage key.
	keys, err := h.kvSvc.List(context.Background(), nodeA.gen().dataPrefix)
	require.NoError(t, err)
	assert.Len(t, keys, 2*perNode,
		"identical rows collided: some row id was handed out more than once")

	// And every row must decode back to a distinct row id.
	ids := make(map[uint64]bool, len(keys))
	for _, k := range keys {
		raw, gerr := h.kvSvc.Get(context.Background(), k)
		require.NoError(t, gerr)
		row, derr := nodeA.decodeRow(k[len(nodeA.gen().dataPrefix):], raw)
		require.NoError(t, derr)
		id, ok := row[2].(uint64)
		require.True(t, ok, "row id slot is %T, want uint64", row[2])
		assert.False(t, ids[id], "row id %d appeared twice", id)
		ids[id] = true
	}
}

// TestRowIDAllocation_UsesTheReplicatedSequence verifies allocation actually
// goes through SequenceServicer -- the Raft-committed counter -- rather than
// any local state, by observing the sequence advance as rows are inserted.
func TestRowIDAllocation_UsesTheReplicatedSequence(t *testing.T) {
	h := newDDLHarness()
	tbl := newRowIDTable(t, h, "seqadvance")
	w := &relationalWriter{table: tbl}
	ctx, endStatement := autocommitStatement(t)

	before, err := h.seqSvc.Peek(context.Background(), "shop", tbl.rowIDSequenceName())
	require.NoError(t, err)
	assert.Equal(t, uint64(0), before)

	for i := 0; i < 5; i++ {
		require.NoError(t, w.Insert(ctx, sqle.Row{"a", uint64(1), nil}))
	}
	require.NoError(t, endStatement())

	after, err := h.seqSvc.Peek(context.Background(), "shop", tbl.rowIDSequenceName())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, after, uint64(5),
		"the replicated sequence did not advance, so the row ids came from somewhere else")
}

// ---------------------------------------------------------------------------
// Insertion order, in-place UPDATE, and DELETE
// ---------------------------------------------------------------------------

// TestKeylessTable_ScanReturnsInsertionOrder verifies the row id's
// order-preserving encoding gives a keyless table InnoDB's scan order.
func TestKeylessTable_ScanReturnsInsertionOrder(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE ord (name VARCHAR(64))`)
	for _, n := range []string{"c", "a", "b", "a"} {
		mustExecQuery(t, prov, "shop", "INSERT INTO ord VALUES ('"+n+"')")
	}

	_, rows := mustExecQuery(t, prov, "shop", "SELECT name FROM ord")
	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = r[0].(string)
	}
	assert.Equal(t, []string{"c", "a", "b", "a"}, got)
}

// TestKeylessTable_UpdateRewritesInPlace verifies the property the whole
// design rests on: an UPDATE carries the hidden row id through from the old
// row to the new one, so the row is rewritten under its EXISTING storage key
// rather than moved to a new one. Asserted on the physical keys, not
// inferred from row counts, because a row-count check would pass equally
// well if every UPDATE re-keyed the row and deleted the old key.
func TestKeylessTable_UpdateRewritesInPlace(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE samekey (name VARCHAR(64), n BIGINT)`)
	mustExecQuery(t, prov, "shop", "INSERT INTO samekey VALUES ('a', 1), ('b', 2)")

	before := rowKeysOf(t, h, "samekey")
	require.Len(t, before, 2)

	mustExecQuery(t, prov, "shop", "UPDATE samekey SET n = 99 WHERE name = 'b'")

	after := rowKeysOf(t, h, "samekey")
	assert.Equal(t, before, after,
		"the UPDATE moved the row to a different storage key, so the row id did not carry through")

	_, rows := mustExecQuery(t, prov, "shop", "SELECT n FROM samekey WHERE name = 'b'")
	require.Len(t, rows, 1)
	assert.Equal(t, int64(99), rows[0][0])
}

// TestKeylessTable_IndexedDeleteAndUpdate verifies the hidden column
// survives an index-accelerated plan: a DELETE and an UPDATE driven through
// a secondary index both reach the writer with a usable row id, and leave no
// orphaned index entries behind.
//
// This is the projection-pushdown case. A planner that pruned the hidden
// column out of the table scan would leave the writer with a short row and
// this test would fail with *MissingRowIDError rather than silently.
func TestKeylessTable_IndexedDeleteAndUpdate(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE idxdel (name VARCHAR(64), n BIGINT)`)
	mustExecQuery(t, prov, "shop", "CREATE INDEX idx_n ON idxdel (n)")
	mustExecQuery(t, prov, "shop", "INSERT INTO idxdel VALUES ('a',1),('b',2),('c',2),('d',3)")

	mustExecQuery(t, prov, "shop", "UPDATE idxdel SET n = 5 WHERE n = 3")
	_, rows := mustExecQuery(t, prov, "shop", "SELECT name FROM idxdel WHERE n = 5")
	require.Len(t, rows, 1)
	assert.Equal(t, "d", rows[0][0])
	_, rows = mustExecQuery(t, prov, "shop", "SELECT name FROM idxdel WHERE n = 3")
	assert.Empty(t, rows, "the old index entry outlived the UPDATE")

	mustExecQuery(t, prov, "shop", "DELETE FROM idxdel WHERE n = 2")
	_, rows = mustExecQuery(t, prov, "shop", "SELECT name FROM idxdel WHERE n = 2")
	assert.Empty(t, rows, "the index entries outlived the DELETE")
	_, rows = mustExecQuery(t, prov, "shop", "SELECT name FROM idxdel")
	assert.Len(t, rows, 2)

	// No orphaned physical index entries.
	assert.Len(t, indexKeysOf(t, h, "idxdel", "idx_n"), 2)
}

// TestKeylessTable_IdenticalRowsAreDistinct is the end-to-end form of the
// data-loss regression: the exact statements from the bug report.
func TestKeylessTable_IdenticalRowsAreDistinct(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE h (name VARCHAR(64), n BIGINT UNSIGNED)`)
	mustExecQuery(t, prov, "shop", "INSERT INTO h VALUES ('a', 7)")
	mustExecQuery(t, prov, "shop", "INSERT INTO h VALUES ('b', 7)")
	mustExecQuery(t, prov, "shop", "INSERT INTO h VALUES ('a', 7)")

	_, rows := mustExecQuery(t, prov, "shop", "SELECT COUNT(*) FROM h")
	require.Len(t, rows, 1)
	assert.Equal(t, int64(3), rows[0][0])
}

// TestKeylessTable_WithAutoIncrementColumn verifies both generated values
// coexist: the AUTO_INCREMENT column keeps producing 1, 2, 3 while the
// hidden row id keys the rows independently.
func TestKeylessTable_WithAutoIncrementColumn(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop",
		`CREATE TABLE aik (id BIGINT AUTO_INCREMENT, name VARCHAR(8), UNIQUE KEY uq_id (id))`)
	mustExecQuery(t, prov, "shop", "INSERT INTO aik (name) VALUES ('p'),('q'),('r')")

	_, rows := mustExecQuery(t, prov, "shop", "SELECT id, name FROM aik")
	require.Len(t, rows, 3)
	for i, r := range rows {
		assert.Equal(t, int64(i+1), r[0])
	}
	assert.Len(t, rowKeysOf(t, h, "aik"), 3)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// rowKeysOf returns the sorted physical row keys of the named table.
// rowKeysOf lists the table's rows at its CURRENT generation (freshly
// re-read from the catalog, not assumed to be 0): TRUNCATE (go-qrdb#547)
// advances a table's generation without synchronously deleting the old
// one's rows, so a helper that hardcoded generation 0 would keep finding a
// truncated table's OLD rows -- still physically present until the
// ReclaimSweeper runs -- and never observe the table as empty.
func rowKeysOf(t *testing.T, h *ddlHarness, table string) []string {
	t.Helper()
	def, err := h.tableSvc.GetTable(context.Background(), "shop", table)
	require.NoError(t, err)
	keys, err := h.kvSvc.List(context.Background(), service.TableDataPrefix("shop", def.TableID, def.Generation))
	require.NoError(t, err)
	sort.Strings(keys)
	return keys
}

// indexKeysOf returns the sorted physical entry keys of one secondary index,
// at the table's CURRENT generation -- see rowKeysOf.
func indexKeysOf(t *testing.T, h *ddlHarness, table, index string) []string {
	t.Helper()
	def, err := h.tableSvc.GetTable(context.Background(), "shop", table)
	require.NoError(t, err)
	keys, err := h.kvSvc.List(context.Background(), service.TableIndexPrefix("shop", def.TableID, def.Generation, incarnationOf(def, index)))
	require.NoError(t, err)
	for _, k := range keys {
		require.True(t, strings.HasPrefix(k, service.TableIndexPrefix("shop", def.TableID, def.Generation, incarnationOf(def, index))))
	}
	sort.Strings(keys)
	return keys
}

// failingSequence is a SequenceServicer whose Allocate always fails, used to
// prove allocateRowID surfaces a sequence failure rather than writing a row
// under a zero (or stale) row id.
type failingSequence struct {
	service.SequenceServicer // nil; only Allocate is implemented
}

func (failingSequence) Allocate(context.Context, string, string, uint64) (uint64, uint64, error) {
	return 0, 0, assert.AnError
}

// TestAllocateRowID_PropagatesSequenceFailure verifies a failed allocation
// aborts the INSERT rather than falling back to any locally-derived value.
func TestAllocateRowID_PropagatesSequenceFailure(t *testing.T) {
	h := newDDLHarness()
	def, err := h.tableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "shop",
		Name:     "seqfail",
		Columns:  []service.ColumnDef{{Name: "name", Type: "VARCHAR(64)", Nullable: true}},
	})
	require.NoError(t, err)

	tbl, err := NewRelationalTable(def, h.kvSvc, h.mvccSvc,
		WithTransactionService(h.txnSvc),
		WithSequenceService(failingSequence{}),
	)
	require.NoError(t, err)

	_, err = tbl.allocateRowID(newTestContext(), sqle.Row{"a", nil})
	assert.ErrorIs(t, err, assert.AnError)

	w := &relationalWriter{table: tbl}
	assert.ErrorIs(t, w.Insert(newTestContext(), sqle.Row{"a", nil}), assert.AnError)
	assert.Empty(t, rowKeysOf(t, h, "seqfail"), "a row was written despite the failed allocation")
}

// ---------------------------------------------------------------------------
// DDL and interactive transactions over a keyless table
// ---------------------------------------------------------------------------

// TestKeylessTable_DDLRoundTrip verifies every DDL path that touches rows
// keeps working when those rows are keyed by the hidden row id: ADD COLUMN
// (metadata only), CREATE INDEX (which streams every existing row through
// BuildIndex and must key each entry off the row id), DROP and MODIFY COLUMN
// (which re-encode every row in place under its existing key), and TRUNCATE.
func TestKeylessTable_DDLRoundTrip(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE ddl (name VARCHAR(64), n BIGINT)`)
	mustExecQuery(t, prov, "shop", "INSERT INTO ddl VALUES ('a',1),('b',2)")
	keys := rowKeysOf(t, h, "ddl")
	require.Len(t, keys, 2)

	mustExecQuery(t, prov, "shop", "ALTER TABLE ddl ADD COLUMN extra VARCHAR(8) DEFAULT 'z'")
	_, rows := mustExecQuery(t, prov, "shop", "SELECT name, n, extra FROM ddl")
	require.Len(t, rows, 2)
	assert.Equal(t, "z", rows[0][2], "an existing row must decode the new column's default")

	mustExecQuery(t, prov, "shop", "CREATE INDEX idx_n ON ddl (n)")
	_, rows = mustExecQuery(t, prov, "shop", "SELECT name FROM ddl WHERE n = 2")
	require.Len(t, rows, 1)
	assert.Equal(t, "b", rows[0][0])
	assert.Len(t, indexKeysOf(t, h, "ddl", "idx_n"), 2,
		"BuildIndex did not write one entry per existing row")

	mustExecQuery(t, prov, "shop", "ALTER TABLE ddl DROP COLUMN extra")
	mustExecQuery(t, prov, "shop", "ALTER TABLE ddl MODIFY COLUMN n INT")
	_, rows = mustExecQuery(t, prov, "shop", "SELECT name, n FROM ddl")
	require.Len(t, rows, 2)
	assert.Equal(t, keys, rowKeysOf(t, h, "ddl"),
		"a rewrite re-keyed the rows; the row id must survive DROP/MODIFY COLUMN")

	mustExecQuery(t, prov, "shop", "TRUNCATE TABLE ddl")
	assert.Empty(t, rowKeysOf(t, h, "ddl"))
}

// TestKeylessTable_InteractiveTransaction verifies two identical rows
// inserted inside ONE open transaction are both committed. This is the
// buffered write path (WriteSet.PutUnique), whose duplicate detection runs
// against the transaction's own uncommitted view -- so if two identical rows
// shared a key, this is where the second would be rejected, before any
// compare-and-swap could catch it.
func TestKeylessTable_InteractiveTransaction(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE txnkeyless (name VARCHAR(64))`)

	conn := newTxConn(t, prov, "shop")
	conn.mustExec(t, "BEGIN")
	conn.mustExec(t, "INSERT INTO txnkeyless VALUES ('a')")
	conn.mustExec(t, "INSERT INTO txnkeyless VALUES ('a')")
	_, rows := conn.mustExec(t, "SELECT COUNT(*) FROM txnkeyless")
	require.Len(t, rows, 1)
	assert.Equal(t, int64(2), rows[0][0], "the transaction cannot see both of its own identical rows")
	conn.mustExec(t, "COMMIT")

	_, rows = conn.mustExec(t, "SELECT COUNT(*) FROM txnkeyless")
	require.Len(t, rows, 1)
	assert.Equal(t, int64(2), rows[0][0])

	conn.mustExec(t, "BEGIN")
	conn.mustExec(t, "INSERT INTO txnkeyless VALUES ('b')")
	conn.mustExec(t, "ROLLBACK")
	_, rows = conn.mustExec(t, "SELECT COUNT(*) FROM txnkeyless")
	require.Len(t, rows, 1)
	assert.Equal(t, int64(2), rows[0][0], "a rolled back insert survived")
}

// TestKeylessTable_DeleteJoin verifies a multi-table DELETE reaches the
// writer with the hidden row id intact: go-mysql-server slices the joined
// row by each target table's schema positions, which only lines up if the
// hidden column is part of that schema on both sides.
func TestKeylessTable_DeleteJoin(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE ja (name VARCHAR(64), n BIGINT)`)
	mustExecQuery(t, prov, "shop", `CREATE TABLE jb (name VARCHAR(64), n BIGINT)`)
	mustExecQuery(t, prov, "shop", "INSERT INTO ja VALUES ('x',1),('y',2)")
	mustExecQuery(t, prov, "shop", "INSERT INTO jb VALUES ('x',10)")

	mustExecQuery(t, prov, "shop", "DELETE ja FROM ja JOIN jb ON ja.name = jb.name")
	_, rows := mustExecQuery(t, prov, "shop", "SELECT name FROM ja")
	require.Len(t, rows, 1)
	assert.Equal(t, "y", rows[0][0])
}

// TestKeylessTable_UpdateJoinIsRejectedUpstream pins a go-mysql-server
// restriction rather than a qrdb one: its assign_update_join analyzer rule
// refuses UPDATE ... JOIN for any table whose schema reports no primary key
// column (sql.IsKeyless), which a keyless table's does not -- deliberately,
// since marking the hidden row id as a primary key column would make SHOW
// CREATE TABLE emit `PRIMARY KEY (`+"`!hidden!rowid`"+`)` and stop the
// emitted DDL from replaying at all.
//
// It is pinned so that a future go-mysql-server bump which lifts the
// restriction is noticed here rather than silently changing behaviour.
func TestKeylessTable_UpdateJoinIsRejectedUpstream(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE uja (name VARCHAR(64), n BIGINT)`)
	mustExecQuery(t, prov, "shop", `CREATE TABLE ujb (name VARCHAR(64), n BIGINT)`)
	mustExecQuery(t, prov, "shop", "INSERT INTO uja VALUES ('x',1)")
	mustExecQuery(t, prov, "shop", "INSERT INTO ujb VALUES ('x',10)")

	_, _, err := execQuery(prov, "shop", "UPDATE uja JOIN ujb ON uja.name = ujb.name SET uja.n = ujb.n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keyless tables unsupported for UPDATE JOIN")
}
