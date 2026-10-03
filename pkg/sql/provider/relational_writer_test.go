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
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/catalog"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// ordersCreateRequest returns a CreateTableRequest for a table with a
// single-column primary key, one UNIQUE secondary index (sku), and one
// non-unique secondary index (warehouse) -- enough shape to exercise both
// physical index-entry layouts (indexEntryKey) in the same table.
func ordersCreateRequest() *service.CreateTableRequest {
	return &service.CreateTableRequest{
		Database: "shop",
		Name:     "orders",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED", Nullable: false},
			{Name: "sku", Type: "VARCHAR(64)", Nullable: true},
			{Name: "warehouse", Type: "VARCHAR(32)", Nullable: true},
			{Name: "qty", Type: "INT", Nullable: true},
		},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
		Indexes: []service.TableIndex{
			{Name: "idx_sku", Columns: []service.IndexCol{{Name: "sku"}}, Unique: true},
			{Name: "idx_warehouse", Columns: []service.IndexCol{{Name: "warehouse"}}, Unique: false},
		},
	}
}

// newWriterHarnessTable creates a table through h's real TableServicer and
// returns a RelationalTable wired with every DML dependency
// (TransactionServicer, SequenceServicer), ready to drive direct
// Insert/Update/Delete/Replace calls against.
func newWriterHarnessTable(t *testing.T, h *ddlHarness, req *service.CreateTableRequest) (*RelationalTable, *service.TableDef) {
	t.Helper()
	def, err := h.tableSvc.CreateTable(context.Background(), req)
	require.NoError(t, err)
	tbl, err := NewRelationalTable(def, h.kvSvc, h.mvccSvc,
		WithTableService(h.tableSvc),
		WithTransactionService(h.txnSvc),
		WithSequenceService(h.seqSvc),
	)
	require.NoError(t, err)
	return tbl, def
}

// orderRow builds a [id, sku, warehouse, qty] row matching ordersCreateRequest.
func orderRow(id uint64, sku, warehouse any, qty any) sqle.Row {
	return sqle.Row{id, sku, warehouse, qty}
}

// skuIndexKey returns the physical UNIQUE index-entry key for sku.
func skuIndexKey(t *testing.T, def *service.TableDef, sku string) string {
	t.Helper()
	schema, err := catalog.SchemaOf(def)
	require.NoError(t, err)
	specs, err := buildIndexSpecs(def, schema)
	require.NoError(t, err)
	for _, s := range specs {
		if s.name == "idx_sku" {
			encoded, eerr := s.valueCodec().EncodeValues([]any{sku})
			require.NoError(t, eerr)
			return service.TableIndexPrefix(def.Database, def.TableID, 0, incarnationOf(def, "idx_sku")) + encoded
		}
	}
	t.Fatal("idx_sku not found")
	return ""
}

// ---------------------------------------------------------------------------
// Insert
// ---------------------------------------------------------------------------

func TestRelationalWriter_Insert_WritesRowAndUniqueIndexEntry(t *testing.T) {
	ctx, endStatement := autocommitStatement(t)
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(ctx)
	ins.StatementBegin(ctx)

	row := orderRow(1, "SKU-1", "east", int32(10))
	require.NoError(t, ins.Insert(ctx, row))
	require.NoError(t, endStatement())
	require.NoError(t, ins.Close(ctx))

	rowKey := service.TableDataPrefix(def.Database, def.TableID, 0) + encodeUserKeyGeneric(t, tbl, row)
	raw, err := h.kvSvc.Get(context.Background(), rowKey)
	require.NoError(t, err)
	decoded, err := tbl.rowCodec.Decode(raw)
	require.NoError(t, err)
	assert.Equal(t, "SKU-1", decoded[1])

	idxKey := skuIndexKey(t, def, "SKU-1")
	idxVal, err := h.kvSvc.Get(context.Background(), idxKey)
	require.NoError(t, err)
	pk, err := tbl.keyCodec.Encode(row)
	require.NoError(t, err)
	assert.Equal(t, pk, string(idxVal))
}

func TestRelationalWriter_Insert_NonUniqueIndex_KeyCarriesPrimaryKeySuffix(t *testing.T) {
	ctx, endStatement := autocommitStatement(t)
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(ctx)

	row1 := orderRow(1, "SKU-1", "east", int32(10))
	row2 := orderRow(2, "SKU-2", "east", int32(5))
	require.NoError(t, ins.Insert(ctx, row1))
	require.NoError(t, ins.Insert(ctx, row2))
	require.NoError(t, endStatement())

	entries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix(def.Database, def.TableID, 0, incarnationOf(def, "idx_warehouse")))
	require.NoError(t, err)
	assert.Len(t, entries, 2, "two rows sharing the same non-unique indexed value must get two distinct physical entries")
}

func TestRelationalWriter_Insert_NullIndexedColumn_NoIndexEntry(t *testing.T) {
	ctx, endStatement := autocommitStatement(t)
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(ctx)

	row := orderRow(1, nil, nil, int32(1))
	require.NoError(t, ins.Insert(ctx, row))
	require.NoError(t, endStatement())

	skuEntries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix(def.Database, def.TableID, 0, incarnationOf(def, "idx_sku")))
	require.NoError(t, err)
	assert.Empty(t, skuEntries, "a NULL indexed value must not contribute a physical index entry")

	whEntries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix(def.Database, def.TableID, 0, incarnationOf(def, "idx_warehouse")))
	require.NoError(t, err)
	assert.Empty(t, whEntries)
}

func TestRelationalWriter_Insert_MultiRow(t *testing.T) {
	ctx, endStatement := autocommitStatement(t)
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(ctx)

	for i := uint64(1); i <= 5; i++ {
		row := orderRow(i, "SKU-"+string(rune('0'+i)), "east", int32(i))
		require.NoError(t, ins.Insert(ctx, row))
		require.NoError(t, endStatement())
	}

	rows, err := h.kvSvc.Scan(context.Background(), service.TableDataPrefix(def.Database, def.TableID, 0))
	require.NoError(t, err)
	assert.Len(t, rows, 5)
}

func TestRelationalWriter_Insert_DuplicatePrimaryKey_Rejected(t *testing.T) {
	ctx, endStatement := autocommitStatement(t)
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(ctx)

	require.NoError(t, ins.Insert(ctx, orderRow(1, "SKU-1", "east", int32(10))))
	require.NoError(t, endStatement())
	err := ins.Insert(ctx, orderRow(1, "SKU-2", "west", int32(20)))
	require.Error(t, err)
	assert.True(t, sqle.ErrPrimaryKeyViolation.Is(err) || sqle.ErrUniqueKeyViolation.Is(err))

	// The original row must be untouched.
	rowKey := service.TableDataPrefix(def.Database, def.TableID, 0) + encodeUserKeyGeneric(t, tbl, orderRow(1, nil, nil, nil))
	raw, gerr := h.kvSvc.Get(context.Background(), rowKey)
	require.NoError(t, gerr)
	decoded, derr := tbl.rowCodec.Decode(raw)
	require.NoError(t, derr)
	assert.Equal(t, "SKU-1", decoded[1], "a rejected duplicate INSERT must not overwrite the existing row")
}

func TestRelationalWriter_Insert_UniqueIndexViolation_RejectsWholeTransaction(t *testing.T) {
	ctx, endStatement := autocommitStatement(t)
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(ctx)

	require.NoError(t, ins.Insert(ctx, orderRow(1, "SKU-1", "east", int32(10))))
	require.NoError(t, endStatement())
	err := ins.Insert(ctx, orderRow(2, "SKU-1", "west", int32(20)))
	require.Error(t, err)
	assert.True(t, sqle.ErrUniqueKeyViolation.Is(err) || sqle.ErrPrimaryKeyViolation.Is(err))

	// The colliding row must never have been written -- not the row itself,
	// and not any of its index entries (the "whole transaction" guarantee).
	rowKey := service.TableDataPrefix(def.Database, def.TableID, 0) + encodeUserKeyGeneric(t, tbl, orderRow(2, nil, nil, nil))
	_, gerr := h.kvSvc.Get(context.Background(), rowKey)
	assert.Error(t, gerr, "the rejected row must not have been written")

	whEntries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix(def.Database, def.TableID, 0, incarnationOf(def, "idx_warehouse")))
	require.NoError(t, err)
	assert.Len(t, whEntries, 1, "the rejected row's non-unique index entry must not have been written either")

	// The winning row's sku index entry must still point at row 1, not have
	// been silently overwritten by the loser.
	idxVal, err := h.kvSvc.Get(context.Background(), skuIndexKey(t, def, "SKU-1"))
	require.NoError(t, err)
	pk1, err := tbl.keyCodec.Encode(orderRow(1, nil, nil, nil))
	require.NoError(t, err)
	assert.Equal(t, pk1, string(idxVal))
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func TestRelationalWriter_Update_ChangedIndexedValue_MovesIndexEntry(t *testing.T) {
	ctx, endStatement := autocommitStatement(t)
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(ctx)
	old := orderRow(1, "SKU-1", "east", int32(10))
	require.NoError(t, ins.Insert(ctx, old))
	require.NoError(t, endStatement())

	upd := tbl.Updater(ctx)
	newRow := orderRow(1, "SKU-2", "east", int32(10))
	require.NoError(t, upd.Update(ctx, old, newRow))
	require.NoError(t, endStatement())

	_, err := h.kvSvc.Get(context.Background(), skuIndexKey(t, def, "SKU-1"))
	assert.Error(t, err, "the old value's index entry must be gone")

	idxVal, err := h.kvSvc.Get(context.Background(), skuIndexKey(t, def, "SKU-2"))
	require.NoError(t, err)
	pk, err := tbl.keyCodec.Encode(newRow)
	require.NoError(t, err)
	assert.Equal(t, pk, string(idxVal))
}

func TestRelationalWriter_Update_UnchangedIndexedValue_LeavesIndexEntryAlone(t *testing.T) {
	ctx, endStatement := autocommitStatement(t)
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(ctx)
	old := orderRow(1, "SKU-1", "east", int32(10))
	require.NoError(t, ins.Insert(ctx, old))
	require.NoError(t, endStatement())

	upd := tbl.Updater(ctx)
	newRow := orderRow(1, "SKU-1", "east", int32(99))
	require.NoError(t, upd.Update(ctx, old, newRow))
	require.NoError(t, endStatement())

	idxVal, err := h.kvSvc.Get(context.Background(), skuIndexKey(t, def, "SKU-1"))
	require.NoError(t, err)
	pk, err := tbl.keyCodec.Encode(newRow)
	require.NoError(t, err)
	assert.Equal(t, pk, string(idxVal))

	rowKey := service.TableDataPrefix(def.Database, def.TableID, 0) + pk
	raw, err := h.kvSvc.Get(context.Background(), rowKey)
	require.NoError(t, err)
	decoded, err := tbl.rowCodec.Decode(raw)
	require.NoError(t, err)
	assert.Equal(t, int32(99), decoded[3])
}

func TestRelationalWriter_Update_PrimaryKeyChange_MovesRowAndAllIndexEntries(t *testing.T) {
	ctx, endStatement := autocommitStatement(t)
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(ctx)
	old := orderRow(1, "SKU-1", "east", int32(10))
	require.NoError(t, ins.Insert(ctx, old))
	require.NoError(t, endStatement())

	upd := tbl.Updater(ctx)
	newRow := orderRow(2, "SKU-1", "east", int32(10))
	require.NoError(t, upd.Update(ctx, old, newRow))
	require.NoError(t, endStatement())

	oldRowKey := service.TableDataPrefix(def.Database, def.TableID, 0) + encodeUserKeyGeneric(t, tbl, old)
	_, err := h.kvSvc.Get(context.Background(), oldRowKey)
	assert.Error(t, err, "the row's old primary-key storage key must be gone")

	newRowKey := service.TableDataPrefix(def.Database, def.TableID, 0) + encodeUserKeyGeneric(t, tbl, newRow)
	raw, err := h.kvSvc.Get(context.Background(), newRowKey)
	require.NoError(t, err)
	decoded, err := tbl.rowCodec.Decode(raw)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), decoded[0])

	// The unique index's entry key is unchanged (same sku value), but its
	// stored primary key must now point at the row's new key.
	idxVal, err := h.kvSvc.Get(context.Background(), skuIndexKey(t, def, "SKU-1"))
	require.NoError(t, err)
	pk2, err := tbl.keyCodec.Encode(newRow)
	require.NoError(t, err)
	assert.Equal(t, pk2, string(idxVal))

	// The non-unique index's entry key IS suffixed with the primary key, so
	// it must have moved to a brand-new physical key.
	whEntries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix(def.Database, def.TableID, 0, incarnationOf(def, "idx_warehouse")))
	require.NoError(t, err)
	require.Len(t, whEntries, 1)
	for _, v := range whEntries {
		assert.Equal(t, pk2, string(v))
	}
}

func TestRelationalWriter_Update_PrimaryKeyCollision_Rejected(t *testing.T) {
	ctx, endStatement := autocommitStatement(t)
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(ctx)
	require.NoError(t, ins.Insert(ctx, orderRow(1, "SKU-1", "east", int32(10))))
	require.NoError(t, ins.Insert(ctx, orderRow(2, "SKU-2", "east", int32(20))))
	require.NoError(t, endStatement())

	upd := tbl.Updater(ctx)
	old := orderRow(1, "SKU-1", "east", int32(10))
	newRow := orderRow(2, "SKU-1", "east", int32(10)) // collides with row 2's primary key.
	err := upd.Update(ctx, old, newRow)
	require.Error(t, err)
	assert.True(t, sqle.ErrPrimaryKeyViolation.Is(err) || sqle.ErrUniqueKeyViolation.Is(err))

	// Row 1 must be untouched.
	rowKey := service.TableDataPrefix(def.Database, def.TableID, 0) + encodeUserKeyGeneric(t, tbl, old)
	raw, gerr := h.kvSvc.Get(context.Background(), rowKey)
	require.NoError(t, gerr)
	decoded, derr := tbl.rowCodec.Decode(raw)
	require.NoError(t, derr)
	assert.Equal(t, "SKU-1", decoded[1])
}

func TestRelationalWriter_Update_UniqueIndexCollision_Rejected(t *testing.T) {
	ctx, endStatement := autocommitStatement(t)
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(ctx)
	require.NoError(t, ins.Insert(ctx, orderRow(1, "SKU-1", "east", int32(10))))
	require.NoError(t, ins.Insert(ctx, orderRow(2, "SKU-2", "east", int32(20))))
	require.NoError(t, endStatement())

	upd := tbl.Updater(ctx)
	old := orderRow(2, "SKU-2", "east", int32(20))
	newRow := orderRow(2, "SKU-1", "east", int32(20)) // collides with row 1's unique sku.
	err := upd.Update(ctx, old, newRow)
	require.Error(t, err)
	assert.True(t, sqle.ErrUniqueKeyViolation.Is(err) || sqle.ErrPrimaryKeyViolation.Is(err))

	// Row 2 must be untouched -- still SKU-2.
	rowKey := service.TableDataPrefix(def.Database, def.TableID, 0) + encodeUserKeyGeneric(t, tbl, old)
	raw, gerr := h.kvSvc.Get(context.Background(), rowKey)
	require.NoError(t, gerr)
	decoded, derr := tbl.rowCodec.Decode(raw)
	require.NoError(t, derr)
	assert.Equal(t, "SKU-2", decoded[1])

	// Row 1's index entry must still point at row 1.
	idxVal, err := h.kvSvc.Get(context.Background(), skuIndexKey(t, def, "SKU-1"))
	require.NoError(t, err)
	pk1, err := tbl.keyCodec.Encode(orderRow(1, nil, nil, nil))
	require.NoError(t, err)
	assert.Equal(t, pk1, string(idxVal))
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestRelationalWriter_Delete_RemovesRowAndIndexEntries(t *testing.T) {
	ctx, endStatement := autocommitStatement(t)
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(ctx)
	row := orderRow(1, "SKU-1", "east", int32(10))
	require.NoError(t, ins.Insert(ctx, row))
	require.NoError(t, endStatement())

	del := tbl.Deleter(ctx)
	require.NoError(t, del.Delete(ctx, row))
	require.NoError(t, endStatement())

	rowKey := service.TableDataPrefix(def.Database, def.TableID, 0) + encodeUserKeyGeneric(t, tbl, row)
	_, err := h.kvSvc.Get(context.Background(), rowKey)
	assert.Error(t, err)

	_, err = h.kvSvc.Get(context.Background(), skuIndexKey(t, def, "SKU-1"))
	assert.Error(t, err)

	whEntries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix(def.Database, def.TableID, 0, incarnationOf(def, "idx_warehouse")))
	require.NoError(t, err)
	assert.Empty(t, whEntries)
}

func TestRelationalWriter_Delete_NonExistentRow_NoOp(t *testing.T) {
	ctx, endStatement := autocommitStatement(t)
	h := newDDLHarness()
	tbl, _ := newWriterHarnessTable(t, h, ordersCreateRequest())
	del := tbl.Deleter(ctx)
	err := del.Delete(ctx, orderRow(1, "SKU-1", "east", int32(10)))
	assert.NoError(t, err, "deleting an already-absent row must be a benign no-op")
	assert.NoError(t, endStatement(), "and so is committing it")
}

// ---------------------------------------------------------------------------
// Truncate
// ---------------------------------------------------------------------------

// TestRelationalWriter_Truncate_AdvancesGenerationLeavingOldRowsUntouched
// verifies go-qrdb#547's O(1) TRUNCATE: the table's rows and index entries
// are never scanned or deleted synchronously -- the OLD generation's data is
// still physically present in storage immediately after TRUNCATE returns
// (the ReclaimSweeper deletes it later, see reclaim_job.go) -- while the
// table itself immediately reads as empty because its resolved Generation
// (and therefore dataPrefix) has advanced past it.
func TestRelationalWriter_Truncate_AdvancesGenerationLeavingOldRowsUntouched(t *testing.T) {
	ctx, endStatement := autocommitStatement(t)
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(ctx)
	require.NoError(t, ins.Insert(ctx, orderRow(1, "SKU-1", "east", int32(10))))
	require.NoError(t, ins.Insert(ctx, orderRow(2, "SKU-2", "east", int32(20))))
	require.NoError(t, endStatement())

	oldGeneration := tbl.gen().generation
	n, err := tbl.Truncate(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "TRUNCATE reports no affected-row count, matching real MySQL")
	assert.Equal(t, oldGeneration+1, tbl.gen().generation)

	// The OLD generation's rows and index entries are untouched -- TRUNCATE
	// itself never deletes them; only the ReclaimSweeper does, later.
	oldRows, err := h.kvSvc.Scan(context.Background(), service.TableDataPrefix(def.Database, def.TableID, oldGeneration))
	require.NoError(t, err)
	assert.Len(t, oldRows, 2, "TRUNCATE must not touch the old generation's rows synchronously")

	// The table's own dataPrefix has moved to the new generation, which has
	// no rows: a scan resolved through the table's current state reads empty.
	newRows, err := h.kvSvc.Scan(context.Background(), tbl.gen().dataPrefix)
	require.NoError(t, err)
	assert.Empty(t, newRows, "the new generation must start empty")
}

// ---------------------------------------------------------------------------
// Availability guards
// ---------------------------------------------------------------------------

func TestRelationalWriter_NoTransactionService_Errors(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	row := sqle.Row{uint64(1), "a@example.com", int32(1)}
	var e *TransactionServiceUnavailableError

	require.ErrorAs(t, tbl.Inserter(newTestContext()).Insert(newTestContext(), row), &e)
	require.ErrorAs(t, tbl.Deleter(newTestContext()).Delete(newTestContext(), row), &e)
	require.ErrorAs(t, tbl.Updater(newTestContext()).Update(newTestContext(), row, row), &e)
}

// ---------------------------------------------------------------------------
// Size limits
// ---------------------------------------------------------------------------

func TestCheckKeySize(t *testing.T) {
	assert.NoError(t, checkKeySize("t", "short"))
	big := make([]byte, maxRowKeySize+1)
	var e *RowKeyTooLargeError
	assert.ErrorAs(t, checkKeySize("t", string(big)), &e)
}

func TestCheckValueSize(t *testing.T) {
	assert.NoError(t, checkValueSize("t", []byte("short")))
	big := make([]byte, maxRowValueSize+1)
	var e *RowValueTooLargeError
	assert.ErrorAs(t, checkValueSize("t", big), &e)
}

func TestCheckProposalSize(t *testing.T) {
	assert.NoError(t, checkProposalSize("t", nil, []service.Op{{Key: []byte("k"), Value: []byte("v")}}))

	bigOps := []service.Op{{Key: []byte("k"), Value: make([]byte, maxRowProposalBytes+1)}}
	var e *RowTransactionTooLargeError
	assert.ErrorAs(t, checkProposalSize("t", nil, bigOps), &e)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// encodeUserKeyGeneric encodes row's primary key using tbl's own KeyCodec --
// a table-agnostic counterpart to relational_table_test.go's
// encodeUserKey, usable with any RelationalTable fixture (not just
// testUsersDef).
func encodeUserKeyGeneric(t *testing.T, tbl *RelationalTable, row sqle.Row) string {
	t.Helper()
	key, err := tbl.keyCodec.Encode(row)
	require.NoError(t, err)
	return key
}
