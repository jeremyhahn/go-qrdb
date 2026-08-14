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
// (TransactionServicer, SequenceServicer, BatchServicer), ready to drive
// direct Insert/Update/Delete/Replace calls against.
func newWriterHarnessTable(t *testing.T, h *ddlHarness, req *service.CreateTableRequest) (*RelationalTable, *service.TableDef) {
	t.Helper()
	def, err := h.tableSvc.CreateTable(context.Background(), req)
	require.NoError(t, err)
	tbl, err := NewRelationalTable(def, h.kvSvc, h.mvccSvc,
		WithTableService(h.tableSvc),
		WithTransactionService(h.txnSvc),
		WithSequenceService(h.seqSvc),
		WithBatchService(h.batchSvc),
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
			return service.TableIndexPrefix(def.Database, def.TableID, "idx_sku") + encoded
		}
	}
	t.Fatal("idx_sku not found")
	return ""
}

// ---------------------------------------------------------------------------
// Insert
// ---------------------------------------------------------------------------

func TestRelationalWriter_Insert_WritesRowAndUniqueIndexEntry(t *testing.T) {
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(newTestContext())
	ins.StatementBegin(newTestContext())

	row := orderRow(1, "SKU-1", "east", int32(10))
	require.NoError(t, ins.Insert(newTestContext(), row))
	require.NoError(t, ins.Close(newTestContext()))

	rowKey := service.TableDataPrefix(def.Database, def.TableID) + encodeUserKeyGeneric(t, tbl, row)
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
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(newTestContext())

	row1 := orderRow(1, "SKU-1", "east", int32(10))
	row2 := orderRow(2, "SKU-2", "east", int32(5))
	require.NoError(t, ins.Insert(newTestContext(), row1))
	require.NoError(t, ins.Insert(newTestContext(), row2))

	entries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix(def.Database, def.TableID, "idx_warehouse"))
	require.NoError(t, err)
	assert.Len(t, entries, 2, "two rows sharing the same non-unique indexed value must get two distinct physical entries")
}

func TestRelationalWriter_Insert_NullIndexedColumn_NoIndexEntry(t *testing.T) {
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(newTestContext())

	row := orderRow(1, nil, nil, int32(1))
	require.NoError(t, ins.Insert(newTestContext(), row))

	skuEntries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix(def.Database, def.TableID, "idx_sku"))
	require.NoError(t, err)
	assert.Empty(t, skuEntries, "a NULL indexed value must not contribute a physical index entry")

	whEntries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix(def.Database, def.TableID, "idx_warehouse"))
	require.NoError(t, err)
	assert.Empty(t, whEntries)
}

func TestRelationalWriter_Insert_MultiRow(t *testing.T) {
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(newTestContext())

	for i := uint64(1); i <= 5; i++ {
		row := orderRow(i, "SKU-"+string(rune('0'+i)), "east", int32(i))
		require.NoError(t, ins.Insert(newTestContext(), row))
	}

	rows, err := h.kvSvc.Scan(context.Background(), service.TableDataPrefix(def.Database, def.TableID))
	require.NoError(t, err)
	assert.Len(t, rows, 5)
}

func TestRelationalWriter_Insert_DuplicatePrimaryKey_Rejected(t *testing.T) {
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(newTestContext())

	require.NoError(t, ins.Insert(newTestContext(), orderRow(1, "SKU-1", "east", int32(10))))
	err := ins.Insert(newTestContext(), orderRow(1, "SKU-2", "west", int32(20)))
	require.Error(t, err)
	assert.True(t, sqle.ErrPrimaryKeyViolation.Is(err) || sqle.ErrUniqueKeyViolation.Is(err))

	// The original row must be untouched.
	rowKey := service.TableDataPrefix(def.Database, def.TableID) + encodeUserKeyGeneric(t, tbl, orderRow(1, nil, nil, nil))
	raw, gerr := h.kvSvc.Get(context.Background(), rowKey)
	require.NoError(t, gerr)
	decoded, derr := tbl.rowCodec.Decode(raw)
	require.NoError(t, derr)
	assert.Equal(t, "SKU-1", decoded[1], "a rejected duplicate INSERT must not overwrite the existing row")
}

func TestRelationalWriter_Insert_UniqueIndexViolation_RejectsWholeTransaction(t *testing.T) {
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(newTestContext())

	require.NoError(t, ins.Insert(newTestContext(), orderRow(1, "SKU-1", "east", int32(10))))
	err := ins.Insert(newTestContext(), orderRow(2, "SKU-1", "west", int32(20)))
	require.Error(t, err)
	assert.True(t, sqle.ErrUniqueKeyViolation.Is(err) || sqle.ErrPrimaryKeyViolation.Is(err))

	// The colliding row must never have been written -- not the row itself,
	// and not any of its index entries (the "whole transaction" guarantee).
	rowKey := service.TableDataPrefix(def.Database, def.TableID) + encodeUserKeyGeneric(t, tbl, orderRow(2, nil, nil, nil))
	_, gerr := h.kvSvc.Get(context.Background(), rowKey)
	assert.Error(t, gerr, "the rejected row must not have been written")

	whEntries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix(def.Database, def.TableID, "idx_warehouse"))
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
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(newTestContext())
	old := orderRow(1, "SKU-1", "east", int32(10))
	require.NoError(t, ins.Insert(newTestContext(), old))

	upd := tbl.Updater(newTestContext())
	newRow := orderRow(1, "SKU-2", "east", int32(10))
	require.NoError(t, upd.Update(newTestContext(), old, newRow))

	_, err := h.kvSvc.Get(context.Background(), skuIndexKey(t, def, "SKU-1"))
	assert.Error(t, err, "the old value's index entry must be gone")

	idxVal, err := h.kvSvc.Get(context.Background(), skuIndexKey(t, def, "SKU-2"))
	require.NoError(t, err)
	pk, err := tbl.keyCodec.Encode(newRow)
	require.NoError(t, err)
	assert.Equal(t, pk, string(idxVal))
}

func TestRelationalWriter_Update_UnchangedIndexedValue_LeavesIndexEntryAlone(t *testing.T) {
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(newTestContext())
	old := orderRow(1, "SKU-1", "east", int32(10))
	require.NoError(t, ins.Insert(newTestContext(), old))

	upd := tbl.Updater(newTestContext())
	newRow := orderRow(1, "SKU-1", "east", int32(99))
	require.NoError(t, upd.Update(newTestContext(), old, newRow))

	idxVal, err := h.kvSvc.Get(context.Background(), skuIndexKey(t, def, "SKU-1"))
	require.NoError(t, err)
	pk, err := tbl.keyCodec.Encode(newRow)
	require.NoError(t, err)
	assert.Equal(t, pk, string(idxVal))

	rowKey := service.TableDataPrefix(def.Database, def.TableID) + pk
	raw, err := h.kvSvc.Get(context.Background(), rowKey)
	require.NoError(t, err)
	decoded, err := tbl.rowCodec.Decode(raw)
	require.NoError(t, err)
	assert.Equal(t, int32(99), decoded[3])
}

func TestRelationalWriter_Update_PrimaryKeyChange_MovesRowAndAllIndexEntries(t *testing.T) {
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(newTestContext())
	old := orderRow(1, "SKU-1", "east", int32(10))
	require.NoError(t, ins.Insert(newTestContext(), old))

	upd := tbl.Updater(newTestContext())
	newRow := orderRow(2, "SKU-1", "east", int32(10))
	require.NoError(t, upd.Update(newTestContext(), old, newRow))

	oldRowKey := service.TableDataPrefix(def.Database, def.TableID) + encodeUserKeyGeneric(t, tbl, old)
	_, err := h.kvSvc.Get(context.Background(), oldRowKey)
	assert.Error(t, err, "the row's old primary-key storage key must be gone")

	newRowKey := service.TableDataPrefix(def.Database, def.TableID) + encodeUserKeyGeneric(t, tbl, newRow)
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
	whEntries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix(def.Database, def.TableID, "idx_warehouse"))
	require.NoError(t, err)
	require.Len(t, whEntries, 1)
	for _, v := range whEntries {
		assert.Equal(t, pk2, string(v))
	}
}

func TestRelationalWriter_Update_PrimaryKeyCollision_Rejected(t *testing.T) {
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(newTestContext())
	require.NoError(t, ins.Insert(newTestContext(), orderRow(1, "SKU-1", "east", int32(10))))
	require.NoError(t, ins.Insert(newTestContext(), orderRow(2, "SKU-2", "east", int32(20))))

	upd := tbl.Updater(newTestContext())
	old := orderRow(1, "SKU-1", "east", int32(10))
	newRow := orderRow(2, "SKU-1", "east", int32(10)) // collides with row 2's primary key.
	err := upd.Update(newTestContext(), old, newRow)
	require.Error(t, err)
	assert.True(t, sqle.ErrPrimaryKeyViolation.Is(err) || sqle.ErrUniqueKeyViolation.Is(err))

	// Row 1 must be untouched.
	rowKey := service.TableDataPrefix(def.Database, def.TableID) + encodeUserKeyGeneric(t, tbl, old)
	raw, gerr := h.kvSvc.Get(context.Background(), rowKey)
	require.NoError(t, gerr)
	decoded, derr := tbl.rowCodec.Decode(raw)
	require.NoError(t, derr)
	assert.Equal(t, "SKU-1", decoded[1])
}

func TestRelationalWriter_Update_UniqueIndexCollision_Rejected(t *testing.T) {
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(newTestContext())
	require.NoError(t, ins.Insert(newTestContext(), orderRow(1, "SKU-1", "east", int32(10))))
	require.NoError(t, ins.Insert(newTestContext(), orderRow(2, "SKU-2", "east", int32(20))))

	upd := tbl.Updater(newTestContext())
	old := orderRow(2, "SKU-2", "east", int32(20))
	newRow := orderRow(2, "SKU-1", "east", int32(20)) // collides with row 1's unique sku.
	err := upd.Update(newTestContext(), old, newRow)
	require.Error(t, err)
	assert.True(t, sqle.ErrUniqueKeyViolation.Is(err) || sqle.ErrPrimaryKeyViolation.Is(err))

	// Row 2 must be untouched -- still SKU-2.
	rowKey := service.TableDataPrefix(def.Database, def.TableID) + encodeUserKeyGeneric(t, tbl, old)
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
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(newTestContext())
	row := orderRow(1, "SKU-1", "east", int32(10))
	require.NoError(t, ins.Insert(newTestContext(), row))

	del := tbl.Deleter(newTestContext())
	require.NoError(t, del.Delete(newTestContext(), row))

	rowKey := service.TableDataPrefix(def.Database, def.TableID) + encodeUserKeyGeneric(t, tbl, row)
	_, err := h.kvSvc.Get(context.Background(), rowKey)
	assert.Error(t, err)

	_, err = h.kvSvc.Get(context.Background(), skuIndexKey(t, def, "SKU-1"))
	assert.Error(t, err)

	whEntries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix(def.Database, def.TableID, "idx_warehouse"))
	require.NoError(t, err)
	assert.Empty(t, whEntries)
}

func TestRelationalWriter_Delete_NonExistentRow_NoOp(t *testing.T) {
	h := newDDLHarness()
	tbl, _ := newWriterHarnessTable(t, h, ordersCreateRequest())
	del := tbl.Deleter(newTestContext())
	err := del.Delete(newTestContext(), orderRow(1, "SKU-1", "east", int32(10)))
	assert.NoError(t, err, "deleting an already-absent row must be a benign no-op")
}

// ---------------------------------------------------------------------------
// Truncate
// ---------------------------------------------------------------------------

func TestRelationalWriter_Truncate_ClearsRowsAndIndexEntries(t *testing.T) {
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(newTestContext())
	require.NoError(t, ins.Insert(newTestContext(), orderRow(1, "SKU-1", "east", int32(10))))
	require.NoError(t, ins.Insert(newTestContext(), orderRow(2, "SKU-2", "east", int32(20))))

	n, err := tbl.Truncate(newTestContext())
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	rows, err := h.kvSvc.Scan(context.Background(), service.TableDataPrefix(def.Database, def.TableID))
	require.NoError(t, err)
	assert.Empty(t, rows)

	skuEntries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix(def.Database, def.TableID, "idx_sku"))
	require.NoError(t, err)
	assert.Empty(t, skuEntries)

	whEntries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix(def.Database, def.TableID, "idx_warehouse"))
	require.NoError(t, err)
	assert.Empty(t, whEntries)
}

func TestRelationalWriter_Truncate_NoBatchService(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	_, err = tbl.Truncate(newTestContext())
	var e *BatchServiceUnavailableError
	require.ErrorAs(t, err, &e)
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
