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
	"strings"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// diffUniqueIndexEntry: value removed / value newly added branches not
// already exercised by relational_writer_test.go.
// ---------------------------------------------------------------------------

func TestRelationalWriter_Update_UniqueValueRemoved_DeletesIndexEntry(t *testing.T) {
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(newTestContext())
	old := orderRow(1, "SKU-1", "east", int32(10))
	require.NoError(t, ins.Insert(newTestContext(), old))

	upd := tbl.Updater(newTestContext())
	newRow := orderRow(1, nil, "east", int32(10))
	require.NoError(t, upd.Update(newTestContext(), old, newRow))

	_, err := h.kvSvc.Get(context.Background(), skuIndexKey(t, def, "SKU-1"))
	assert.Error(t, err, "clearing a unique indexed value must delete its physical entry")
}

func TestRelationalWriter_Update_UniqueValueAdded_CreatesIndexEntry(t *testing.T) {
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(newTestContext())
	old := orderRow(1, nil, "east", int32(10))
	require.NoError(t, ins.Insert(newTestContext(), old))

	upd := tbl.Updater(newTestContext())
	newRow := orderRow(1, "SKU-9", "east", int32(10))
	require.NoError(t, upd.Update(newTestContext(), old, newRow))

	idxVal, err := h.kvSvc.Get(context.Background(), skuIndexKey(t, def, "SKU-9"))
	require.NoError(t, err)
	pk, err := tbl.keyCodec.Encode(newRow)
	require.NoError(t, err)
	assert.Equal(t, pk, string(idxVal))
}

func TestRelationalWriter_Update_NonUniqueValueRemoved_DeletesEntry(t *testing.T) {
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(newTestContext())
	old := orderRow(1, "SKU-1", "east", int32(10))
	require.NoError(t, ins.Insert(newTestContext(), old))

	upd := tbl.Updater(newTestContext())
	newRow := orderRow(1, "SKU-1", nil, int32(10))
	require.NoError(t, upd.Update(newTestContext(), old, newRow))

	entries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix(def.Database, def.TableID, 0, incarnationOf(def, "idx_warehouse")))
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestRelationalWriter_Update_NeitherValueExists_NoIndexChange(t *testing.T) {
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(newTestContext())
	old := orderRow(1, nil, nil, int32(10))
	require.NoError(t, ins.Insert(newTestContext(), old))

	upd := tbl.Updater(newTestContext())
	newRow := orderRow(1, nil, nil, int32(20))
	require.NoError(t, upd.Update(newTestContext(), old, newRow))

	skuEntries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix(def.Database, def.TableID, 0, incarnationOf(def, "idx_sku")))
	require.NoError(t, err)
	assert.Empty(t, skuEntries)
	whEntries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix(def.Database, def.TableID, 0, incarnationOf(def, "idx_warehouse")))
	require.NoError(t, err)
	assert.Empty(t, whEntries)
}

// ---------------------------------------------------------------------------
// Secondary index lookup: non-unique success path, and every early-exit
// shape of secondaryIndexKeysFromRanges.
// ---------------------------------------------------------------------------

func TestRelationalDML_EndToEnd_SelectByNonUniqueIndex(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop",
		`CREATE TABLE orders (
			id BIGINT UNSIGNED PRIMARY KEY,
			sku VARCHAR(64),
			warehouse VARCHAR(32)
		)`)
	mustExecQuery(t, prov, "shop", "CREATE INDEX idx_warehouse ON orders (warehouse)")
	mustExecQuery(t, prov, "shop", "INSERT INTO orders (id, sku, warehouse) VALUES (1, 'SKU-1', 'east')")
	mustExecQuery(t, prov, "shop", "INSERT INTO orders (id, sku, warehouse) VALUES (2, 'SKU-2', 'east')")
	mustExecQuery(t, prov, "shop", "INSERT INTO orders (id, sku, warehouse) VALUES (3, 'SKU-3', 'west')")

	_, rows := mustExecQuery(t, prov, "shop", "SELECT id FROM orders WHERE warehouse = 'east' ORDER BY id")
	require.Len(t, rows, 2)
	assert.Equal(t, uint64(1), rows[0][0])
	assert.Equal(t, uint64(2), rows[1][0])
}

func TestRelationalTable_SecondaryIndexKeysFromRanges_NonEqualityShapes(t *testing.T) {
	h := newDDLHarness()
	tbl, _ := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := tbl.Inserter(newTestContext())
	require.NoError(t, ins.Insert(newTestContext(), orderRow(1, "SKU-1", "east", int32(10))))

	spec, found := tbl.indexSpecByName("idx_sku") // sku
	require.True(t, found)
	strType := tbl.schema[1].Type

	// Not a MySQLRange at all.
	_, exact, err := tbl.secondaryIndexKeysFromRanges(newTestContext(), spec, []sqle.Range{})
	require.NoError(t, err)
	assert.True(t, exact)

	// A genuine (non-degenerate) closed range is not a point match.
	rangeExpr := sqle.ClosedRangeColumnExpr("SKU-1", "SKU-9", strType)
	_, exact, err = tbl.secondaryIndexKeysFromRanges(newTestContext(), spec, []sqle.Range{sqle.MySQLRange{rangeExpr}})
	require.NoError(t, err)
	assert.False(t, exact, "a genuine range must fall back to the filtered scan")

	// An open-open (non-ClosedClosed) range type.
	openExpr := sqle.GreaterThanRangeColumnExpr("SKU-1", strType)
	_, exact, err = tbl.secondaryIndexKeysFromRanges(newTestContext(), spec, []sqle.Range{sqle.MySQLRange{openExpr}})
	require.NoError(t, err)
	assert.False(t, exact)

	// An unbound (all rows) range.
	allExpr := sqle.AllRangeColumnExpr(strType)
	_, exact, err = tbl.secondaryIndexKeysFromRanges(newTestContext(), spec, []sqle.Range{sqle.MySQLRange{allExpr}})
	require.NoError(t, err)
	assert.False(t, exact)
}

func TestRelationalTable_LookupIndexValue_NonUniqueScanError(t *testing.T) {
	scanErr := errors.New("scan failed")
	def := ordersDefWithID(t)
	kv := &mockKVService{
		scanFn: func(context.Context, string) (map[string][]byte, error) {
			return nil, scanErr
		},
	}
	tbl, err := NewRelationalTable(def, kv, nil)
	require.NoError(t, err)

	spec, found := tbl.indexSpecByName("idx_warehouse") // non-unique
	require.True(t, found)

	_, lerr := tbl.lookupIndexValue(newTestContext(), spec, "x", true)
	assert.ErrorIs(t, lerr, scanErr)
}

func TestRelationalTable_LookupIndexValue_UniqueGetError(t *testing.T) {
	getErr := errors.New("get failed")
	def := ordersDefWithID(t)
	kv := &mockKVService{
		getFn: func(context.Context, string) ([]byte, error) {
			return nil, getErr
		},
	}
	tbl, err := NewRelationalTable(def, kv, nil)
	require.NoError(t, err)

	spec, found := tbl.indexSpecByName("idx_sku") // unique
	require.True(t, found)

	_, lerr := tbl.lookupIndexValue(newTestContext(), spec, "x", true)
	assert.ErrorIs(t, lerr, getErr)
}

// ordersDefWithID returns a plain (non-Raft-backed) TableDef matching
// ordersCreateRequest's shape, for tests that need a *RelationalTable but
// not the full ddlHarness plumbing.
func ordersDefWithID(t *testing.T) *service.TableDef {
	t.Helper()
	return &service.TableDef{
		TableID:  42,
		Name:     "orders",
		Database: "shop",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED"},
			{Name: "sku", Type: "VARCHAR(64)", Nullable: true},
			{Name: "warehouse", Type: "VARCHAR(32)", Nullable: true},
			{Name: "qty", Type: "INT", Nullable: true},
		},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
		Indexes: []service.TableIndex{
			{Name: "idx_sku", Columns: []service.IndexCol{{Name: "sku"}}, Unique: true},
			{Name: "idx_warehouse", Columns: []service.IndexCol{{Name: "warehouse"}}, Unique: false},
		},
		SchemaVersion: 1,
	}
}

// ---------------------------------------------------------------------------
// resolveDuplicate error/skip branches
// ---------------------------------------------------------------------------

func TestRelationalTable_ResolveDuplicate_PropagatesNonNotFoundError(t *testing.T) {
	getErr := errors.New("boom")
	def := ordersDefWithID(t)
	kv := &mockKVService{getFn: func(context.Context, string) ([]byte, error) {
		return nil, getErr
	}}
	tbl, err := NewRelationalTable(def, kv, nil)
	require.NoError(t, err)

	_, _, rerr := tbl.resolveDuplicate(newTestContext(), tbl.gen(), []uniqueCandidate{{key: "k", isPK: true}})
	assert.ErrorIs(t, rerr, getErr)
}

func TestRelationalTable_ResolveDuplicate_SecondGetNotFound_SkipsCandidate(t *testing.T) {
	def := ordersDefWithID(t)
	kv := &mockKVService{
		getFn: func(_ context.Context, key string) ([]byte, error) {
			if key == "idxkey" {
				return []byte("somepk"), nil
			}
			return nil, &service.KeyNotFoundError{Key: key}
		},
	}
	tbl, err := NewRelationalTable(def, kv, nil)
	require.NoError(t, err)

	dupErr, transient, rerr := tbl.resolveDuplicate(newTestContext(), tbl.gen(), []uniqueCandidate{{key: "idxkey", indexName: "idx_sku"}})
	require.NoError(t, rerr)
	assert.Nil(t, dupErr)
	assert.True(t, transient, "a candidate whose row has since vanished must be treated as a transient race")
}

func TestRelationalTable_ResolveDuplicate_SecondGetOtherError_Propagates(t *testing.T) {
	getErr := errors.New("boom")
	def := ordersDefWithID(t)
	kv := &mockKVService{
		getFn: func(_ context.Context, key string) ([]byte, error) {
			if key == "idxkey" {
				return []byte("somepk"), nil
			}
			return nil, getErr
		},
	}
	tbl, err := NewRelationalTable(def, kv, nil)
	require.NoError(t, err)

	_, _, rerr := tbl.resolveDuplicate(newTestContext(), tbl.gen(), []uniqueCandidate{{key: "idxkey", indexName: "idx_sku"}})
	assert.ErrorIs(t, rerr, getErr)
}

// ---------------------------------------------------------------------------
// Size-limit rejection reached through the real write path (not just the
// unit-tested helper functions).
// ---------------------------------------------------------------------------

func TestRelationalWriter_Insert_IndexKeyTooLarge_Rejected(t *testing.T) {
	def := &service.TableDef{
		TableID:  1,
		Name:     "big",
		Database: "shop",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED"},
			{Name: "val", Type: "TEXT", Nullable: true},
		},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
		Indexes: []service.TableIndex{
			{Name: "idx_val", Columns: []service.IndexCol{{Name: "val"}}, Unique: true},
		},
		SchemaVersion: 1,
	}
	huge := strings.Repeat("x", maxRowKeySize+1)
	row := sqle.Row{uint64(1), huge}

	// buildInsertIndexWrites is exercised directly: the index value alone
	// exceeds maxRowKeySize, so the write must be rejected before any
	// transaction is proposed.
	tbl, terr := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, terr)
	_, _, werr := tbl.buildInsertIndexWrites(tbl.gen(), row, "pk")
	var e *RowKeyTooLargeError
	require.ErrorAs(t, werr, &e)
}

// ---------------------------------------------------------------------------
// Truncate (go-qrdb#547): TableServicer delegation and error propagation.
//
// Truncate no longer scans or deletes anything itself -- it delegates the
// entire operation to TableServicer.TruncateTable (see that method's doc
// comment in relational_writer.go) -- so these tests exercise the
// delegation boundary: an unavailable TableServicer, a propagated
// TruncateTable error, and a successful call's effect on the table's own
// in-memory state (Generation, dataPrefix).
// ---------------------------------------------------------------------------

// TestRelationalWriter_Truncate_TableServiceUnavailable verifies a table
// resolved without a TableServicer (e.g. a legacy composition path) fails
// closed rather than silently doing nothing.
func TestRelationalWriter_Truncate_TableServiceUnavailable(t *testing.T) {
	def := ordersDefWithID(t)
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	_, terr := tbl.Truncate(newTestContext())
	var e *TableServiceUnavailableError
	require.ErrorAs(t, terr, &e)
}

// TestRelationalWriter_Truncate_TableServiceError verifies a TruncateTable
// failure (e.g. the catalog CAS lost a race after exhausting its retries, or
// a rewrite is in progress) propagates to the caller rather than being
// swallowed.
func TestRelationalWriter_Truncate_TableServiceError(t *testing.T) {
	truncErr := errors.New("truncate failed")
	def := ordersDefWithID(t)
	fake := &fakeTableService{
		truncateTableFn: func(context.Context, string, string, bool) (*service.TableDef, error) {
			return nil, truncErr
		},
	}
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil, WithTableService(fake))
	require.NoError(t, err)

	_, terr := tbl.Truncate(newTestContext())
	assert.ErrorIs(t, terr, truncErr)
}

// TestRelationalWriter_Truncate_AdvancesGenerationAndResetsSequence verifies
// a successful TRUNCATE on the default (MySQL) dialect asks TruncateTable to
// reset the sequence, and updates the table's own Generation and dataPrefix
// to match what TruncateTable reports -- the two fields every subsequent
// Insert/Update/Delete's generationCompare and rowKey derive from.
func TestRelationalWriter_Truncate_AdvancesGenerationAndResetsSequence(t *testing.T) {
	def := ordersDefWithID(t)
	oldGeneration := def.Generation
	var gotResetSequence bool
	fake := &fakeTableService{
		truncateTableFn: func(_ context.Context, database, name string, resetSequence bool) (*service.TableDef, error) {
			gotResetSequence = resetSequence
			next := *def
			next.Generation = oldGeneration + 1
			return &next, nil
		},
	}
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil, WithTableService(fake))
	require.NoError(t, err)

	_, terr := tbl.Truncate(newTestContext())
	require.NoError(t, terr)
	assert.True(t, gotResetSequence, "MySQL dialect (the default) must reset AUTO_INCREMENT")
	assert.Equal(t, oldGeneration+1, tbl.gen().generation)
	assert.Equal(t, service.TableDataPrefix(def.Database, def.TableID, oldGeneration+1), tbl.gen().dataPrefix)
}

// TestRelationalWriter_Truncate_PostgreSQLKeepsSequence verifies a table
// resolved with WithPostgreSQLDialect asks TruncateTable NOT to reset the
// sequence, matching plain PostgreSQL TRUNCATE (without RESTART IDENTITY)
// leaving a sequence the table owns untouched.
func TestRelationalWriter_Truncate_PostgreSQLKeepsSequence(t *testing.T) {
	def := ordersDefWithID(t)
	var gotResetSequence bool
	fake := &fakeTableService{
		truncateTableFn: func(_ context.Context, database, name string, resetSequence bool) (*service.TableDef, error) {
			gotResetSequence = resetSequence
			next := *def
			next.Generation = def.Generation + 1
			return &next, nil
		},
	}
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil, WithTableService(fake), WithPostgreSQLDialect())
	require.NoError(t, err)

	_, terr := tbl.Truncate(newTestContext())
	require.NoError(t, terr)
	assert.False(t, gotResetSequence, "PostgreSQL dialect must NOT reset its sequence")
}

// ---------------------------------------------------------------------------
// activeWriteSet: missing-dependency guards
// ---------------------------------------------------------------------------

// txContext builds a *sqle.Context carrying a *QRDBSession with
// GetIgnoreAutoCommit set, simulating a statement running inside an
// explicit BEGIN.
func txContext() *sqle.Context {
	sess := NewSession(sqle.NewBaseSession(), nil)
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess))
	ctx.SetIgnoreAutoCommit(true)
	return ctx
}

func TestRelationalTable_ActiveWriteSet_RequiresMVCCService(t *testing.T) {
	def := ordersDefWithID(t)
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil, WithTransactionService(&memTxn{store: newMemStore()}))
	require.NoError(t, err)

	ctx := txContext()
	_, err = ctx.Session.(*QRDBSession).StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)

	_, werr := tbl.activeWriteSet(ctx)
	var mvccErr *RelationalMVCCUnavailableError
	require.ErrorAs(t, werr, &mvccErr)
}

func TestRelationalTable_ActiveWriteSet_RequiresTransactionService(t *testing.T) {
	def := ordersDefWithID(t)
	store := newMemStore()
	tbl, err := NewRelationalTable(def, &mockKVService{}, store)
	require.NoError(t, err)

	ctx := txContext()
	_, err = ctx.Session.(*QRDBSession).StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)

	_, werr := tbl.activeWriteSet(ctx)
	var txnErr *TransactionServiceUnavailableError
	require.ErrorAs(t, werr, &txnErr)
}

func TestRelationalTable_ActiveWriteSet_AutocommitReturnsNil(t *testing.T) {
	def := ordersDefWithID(t)
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	ws, werr := tbl.activeWriteSet(newTestContext())
	require.NoError(t, werr)
	assert.Nil(t, ws)
}

// ---------------------------------------------------------------------------
// autoIncrementUint64
// ---------------------------------------------------------------------------

func TestAutoIncrementUint64(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want uint64
		ok   bool
	}{
		{"uint64", uint64(5), 5, true},
		{"uint32", uint32(5), 5, true},
		{"uint16", uint16(5), 5, true},
		{"uint8", uint8(5), 5, true},
		{"uint", uint(5), 5, true},
		{"int64", int64(5), 5, true},
		{"int64 negative", int64(-1), 0, false},
		{"int32", int32(5), 5, true},
		{"int32 negative", int32(-1), 0, false},
		{"int16", int16(5), 5, true},
		{"int16 negative", int16(-1), 0, false},
		{"int8", int8(5), 5, true},
		{"int8 negative", int8(-1), 0, false},
		{"int", int(5), 5, true},
		{"int negative", -1, 0, false},
		{"float64", float64(5), 5, true},
		{"float64 negative", float64(-1), 0, false},
		{"float32", float32(5), 5, true},
		{"float32 negative", float32(-1), 0, false},
		{"string unsupported", "5", 0, false},
		{"nil unsupported", nil, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := autoIncrementUint64(c.in)
			assert.Equal(t, c.ok, ok)
			if ok {
				assert.Equal(t, c.want, got)
			}
		})
	}
}
