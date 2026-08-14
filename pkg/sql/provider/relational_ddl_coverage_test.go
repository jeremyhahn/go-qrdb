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
	"github.com/dolthub/go-mysql-server/sql/expression"
	"github.com/dolthub/go-mysql-server/sql/expression/function"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/catalog"
)

// ---------------------------------------------------------------------------
// Database.DropTable / RenameTable: additional error paths
// ---------------------------------------------------------------------------

func TestDatabase_DropTable_TableServiceResolutionError(t *testing.T) {
	reg := &fakeRelationalRegistry{
		tableSvcFn: func(string, string) (service.TableServicer, error) { return nil, assert.AnError },
	}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg}
	err := db.DropTable(newTestContext(), "users")
	assert.ErrorIs(t, err, assert.AnError)
}

func TestDatabase_DropTable_KVServiceResolutionError(t *testing.T) {
	h := newDDLHarness()
	_, err := h.tableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "shop", Name: "users", Columns: []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}},
	})
	require.NoError(t, err)

	reg := h.registry.(*fakeRelationalRegistry)
	reg.kvErr = assert.AnError
	db := h.newDDLDatabase("shop")

	err = db.DropTable(newTestContext(), "users")
	assert.ErrorIs(t, err, assert.AnError)
}

func TestDatabase_RenameTable_TableServiceResolutionError(t *testing.T) {
	reg := &fakeRelationalRegistry{
		tableSvcFn: func(string, string) (service.TableServicer, error) { return nil, assert.AnError },
	}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg}
	err := db.RenameTable(newTestContext(), "users", "accounts")
	assert.ErrorIs(t, err, assert.AnError)
}

// ---------------------------------------------------------------------------
// Collation: error paths
// ---------------------------------------------------------------------------

func TestDatabase_GetCollation_KVServiceError(t *testing.T) {
	reg := &fakeRelationalRegistry{kvErr: assert.AnError}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg}
	assert.Equal(t, sqle.Collation_Default, db.GetCollation(newTestContext()))
}

func TestDatabase_GetCollation_MalformedStoredValue(t *testing.T) {
	kv := &mockKVService{
		getFn: func(context.Context, string) ([]byte, error) { return []byte("not-a-real-collation"), nil },
	}
	reg := &fakeRelationalRegistry{kvSvc: kv}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg}
	assert.Equal(t, sqle.Collation_Default, db.GetCollation(newTestContext()))
}

func TestDatabase_SetCollation_KVServiceError(t *testing.T) {
	reg := &fakeRelationalRegistry{kvErr: assert.AnError}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg}
	err := db.SetCollation(newTestContext(), sqle.Collation_utf8mb4_bin)
	assert.ErrorIs(t, err, assert.AnError)
}

// ---------------------------------------------------------------------------
// prefixLengthOf
// ---------------------------------------------------------------------------

func TestPrefixLengthOf(t *testing.T) {
	assert.Equal(t, uint16(0), prefixLengthOf(0))
	assert.Equal(t, uint16(0), prefixLengthOf(-5))
	assert.Equal(t, uint16(10), prefixLengthOf(10))
	assert.Equal(t, uint16(65535), prefixLengthOf(65535))
	assert.Equal(t, uint16(65535), prefixLengthOf(100000))
}

// ---------------------------------------------------------------------------
// buildDDLChunks / executeDDLChunks / deleteByPrefix / deleteAllByPrefix
// ---------------------------------------------------------------------------

func TestBuildDDLChunks_Empty(t *testing.T) {
	assert.Nil(t, buildDDLChunks(nil))
}

func TestBuildDDLChunks_SplitsOnOpCount(t *testing.T) {
	ops := make([]service.Op, ddlChunkMaxOps+10)
	for i := range ops {
		ops[i] = service.Op{Type: service.OpDelete, Key: []byte("k")}
	}
	chunks := buildDDLChunks(ops)
	require.Len(t, chunks, 2)
	assert.Len(t, chunks[0], ddlChunkMaxOps)
	assert.Len(t, chunks[1], 10)
}

func TestBuildDDLChunks_SplitsOnByteSize(t *testing.T) {
	big := make([]byte, ddlChunkMaxBytes/2)
	ops := []service.Op{
		{Type: service.OpPut, Key: []byte("k1"), Value: big},
		{Type: service.OpPut, Key: []byte("k2"), Value: big},
		{Type: service.OpPut, Key: []byte("k3"), Value: big},
	}
	chunks := buildDDLChunks(ops)
	assert.GreaterOrEqual(t, len(chunks), 2, "oversized ops must be split across proposals")
	var total int
	for _, c := range chunks {
		total += len(c)
	}
	assert.Equal(t, 3, total)
}

func TestDeleteByPrefix_ListError(t *testing.T) {
	kv := &mockKVService{listFn: func(context.Context, string) ([]string, error) { return nil, assert.AnError }}
	err := deleteByPrefix(context.Background(), kv, &memBatch{store: newMemStore()}, "prefix")
	assert.ErrorIs(t, err, assert.AnError)
}

func TestDeleteByPrefix_NoKeys(t *testing.T) {
	kv := &mockKVService{listFn: func(context.Context, string) ([]string, error) { return nil, nil }}
	err := deleteByPrefix(context.Background(), kv, &memBatch{store: newMemStore()}, "prefix")
	assert.NoError(t, err)
}

func TestDeleteByPrefix_BatchExecuteError(t *testing.T) {
	kv := &mockKVService{listFn: func(context.Context, string) ([]string, error) { return []string{"a"}, nil }}
	batch := &erroringBatchServicer{err: assert.AnError}
	err := deleteByPrefix(context.Background(), kv, batch, "prefix")
	assert.ErrorIs(t, err, assert.AnError)
}

type erroringBatchServicer struct {
	service.BatchServicer
	err error
}

func (e *erroringBatchServicer) Execute(context.Context, []service.Op) ([]service.OpResponse, error) {
	return nil, e.err
}

func TestDeleteAllByPrefix_ListError(t *testing.T) {
	kv := &mockKVService{listFn: func(context.Context, string) ([]string, error) { return nil, assert.AnError }}
	err := deleteAllByPrefix(context.Background(), kv, "prefix")
	assert.ErrorIs(t, err, assert.AnError)
}

func TestDeleteAllByPrefix_DeleteError(t *testing.T) {
	kv := &mockKVService{
		listFn:   func(context.Context, string) ([]string, error) { return []string{"a"}, nil },
		deleteFn: func(context.Context, string) error { return assert.AnError },
	}
	err := deleteAllByPrefix(context.Background(), kv, "prefix")
	assert.ErrorIs(t, err, assert.AnError)
}

func TestReclaimTableData_IndexPrefixError(t *testing.T) {
	def := &service.TableDef{
		Database: "shop", TableID: 1,
		Indexes: []service.TableIndex{{Name: "idx_a", Columns: []service.IndexCol{{Name: "a"}}}},
	}
	kv := &mockKVService{listFn: func(context.Context, string) ([]string, error) { return []string{"k"}, nil }}
	batch := &erroringBatchServicer{err: assert.AnError}
	err := reclaimTableData(context.Background(), kv, batch, "shop", def)
	assert.ErrorIs(t, err, assert.AnError)
}

// ---------------------------------------------------------------------------
// tableIndexFromDef
// ---------------------------------------------------------------------------

func TestTableIndexFromDef_NoColumns(t *testing.T) {
	_, err := tableIndexFromDef(sqle.IndexDef{Name: "idx"})
	var e *IndexNoColumnsError
	require.ErrorAs(t, err, &e)
}

func TestTableIndexFromDef_Success(t *testing.T) {
	idx, err := tableIndexFromDef(sqle.IndexDef{
		Name: "idx_email", Columns: []sqle.IndexColumn{{Name: "email", Length: 20}},
		Constraint: sqle.IndexConstraint_Unique, Comment: "c",
	})
	require.NoError(t, err)
	assert.Equal(t, "idx_email", idx.Name)
	assert.True(t, idx.Unique)
	assert.Equal(t, "c", idx.Comment)
	require.Len(t, idx.Columns, 1)
	assert.Equal(t, uint16(20), idx.Columns[0].PrefixLength)
}

// ---------------------------------------------------------------------------
// columnDefFromSQL: default value translation
// ---------------------------------------------------------------------------

func TestColumnDefFromSQL_LiteralDefault(t *testing.T) {
	def, err := sqle.NewColumnDefaultValue(expression.NewLiteral(int64(42), types.Int64), types.Int64, true, false, false)
	require.NoError(t, err)
	col := &sqle.Column{Name: "n", Type: types.Int64, Default: def}
	cd, err := columnDefFromSQL(col)
	require.NoError(t, err)
	assert.Equal(t, "42", cd.Default)
}

// TestColumnDefFromSQL_ExpressionDefaultIsSelfDescribing pins the property
// that replaced the separate DefaultIsExpr discriminator: an expression
// default is persisted parenthesized, so the stored text alone says it is
// not a literal and catalog.ColumnDefaultOf needs nothing else to read it
// back.
//
// The doubled parentheses are not a mistake: expression.Arithmetic renders
// itself parenthesized, and (*sql.ColumnDefaultValue).String() wraps every
// non-literal once more. The engine's own parser treats the outer pair as
// plain grouping and drops it, so re-persisting a resolved default produces
// this same text again rather than growing by a pair each time -- which is
// what makes SHOW CREATE TABLE round-trip stable (see
// TestNode_ShowCreateTableRoundTrips).
func TestColumnDefFromSQL_ExpressionDefaultIsSelfDescribing(t *testing.T) {
	expr := expression.NewArithmetic(
		expression.NewLiteral(int64(1), types.Int64),
		expression.NewLiteral(int64(1), types.Int64),
		"+",
	)
	def, err := sqle.NewColumnDefaultValue(expr, types.Int64, false, true, false)
	require.NoError(t, err)
	col := &sqle.Column{Name: "n", Type: types.Int64, Default: def}
	cd, err := columnDefFromSQL(col)
	require.NoError(t, err)
	assert.Equal(t, "((1 + 1))", cd.Default)
	assert.NotNil(t, catalog.ColumnDefaultOf(cd),
		"a parenthesized default must read back as a default at all")
}

// TestColumnDefFromSQL_TemporalExpressionDefaultStaysBare pins the other
// half of the self-describing form: MySQL writes CURRENT_TIMESTAMP without
// parentheses, (*sql.ColumnDefaultValue).String() reproduces that, and
// catalog's own bare-temporal list is what still classifies it as an
// expression on read.
func TestColumnDefFromSQL_TemporalExpressionDefaultStaysBare(t *testing.T) {
	now, err := function.NewNow(newTestContext())
	require.NoError(t, err)
	def, err := sqle.NewColumnDefaultValue(now, types.Datetime, false, true, true)
	require.NoError(t, err)
	col := &sqle.Column{Name: "d", Type: types.Datetime, Default: def}
	cd, err := columnDefFromSQL(col)
	require.NoError(t, err)
	assert.Equal(t, "CURRENT_TIMESTAMP", cd.Default)
}

func TestColumnDefFromSQL_NoDefault(t *testing.T) {
	col := &sqle.Column{Name: "n", Type: types.Int64}
	cd, err := columnDefFromSQL(col)
	require.NoError(t, err)
	assert.Empty(t, cd.Default)
}
