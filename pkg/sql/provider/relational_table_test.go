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
	"io"
	"testing"
	"time"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/sqltypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/catalog"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// fakeMVCCService
// ---------------------------------------------------------------------------

// fakeMVCCService implements service.MVCCKVServicer for unit testing.
// Base KVServicer methods are promoted from the embedded (nilable)
// service.KVServicer; only the MVCC-specific surface RelationalTable
// actually uses (Range) is independently configurable.
type fakeMVCCService struct {
	service.KVServicer
	rangeFn func(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error)

	// rangeConsistencyFn backs RangeWithConsistency, the read RowCount and
	// DataLength estimate a table's size with.
	rangeConsistencyFn func(ctx context.Context, start, end []byte, ro consistency.ReadOptions, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error)
}

var _ service.MVCCKVServicer = (*fakeMVCCService)(nil)

func (m *fakeMVCCService) PutWithLease(context.Context, string, []byte, int64) (*service.KeyValue, error) {
	return nil, nil
}
func (m *fakeMVCCService) PutWithLeaseAndMode(context.Context, string, []byte, int64, writemode.Mode) (*service.KeyValue, error) {
	return nil, nil
}
func (m *fakeMVCCService) GetRevision(context.Context, string) (*service.KeyValue, error) {
	return nil, nil
}
func (m *fakeMVCCService) GetRevisionWithConsistency(context.Context, string, consistency.ReadOptions) (*service.KeyValue, error) {
	return nil, nil
}
func (m *fakeMVCCService) GetAtRevision(context.Context, string, int64) (*service.KeyValue, error) {
	return nil, nil
}
func (m *fakeMVCCService) GetAtRevisionWithConsistency(context.Context, string, int64, consistency.ReadOptions) (*service.KeyValue, error) {
	return nil, nil
}
func (m *fakeMVCCService) Range(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	if m.rangeFn != nil {
		return m.rangeFn(ctx, start, end, opts...)
	}
	return nil, 0, 0, nil
}
func (m *fakeMVCCService) RangeWithConsistency(ctx context.Context, start, end []byte, ro consistency.ReadOptions, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	if m.rangeConsistencyFn != nil {
		return m.rangeConsistencyFn(ctx, start, end, ro, opts...)
	}
	return nil, 0, 0, nil
}
func (m *fakeMVCCService) Compact(context.Context, int64) error           { return nil }
func (m *fakeMVCCService) CurrentRevision(context.Context) (int64, error) { return 0, nil }
func (m *fakeMVCCService) RestoreToRevision(context.Context, *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

// testUsersDef returns a representative single-column-primary-key table
// definition used by most RelationalTable tests.
func testUsersDef() *service.TableDef {
	return &service.TableDef{
		TableID:  7,
		Name:     "users",
		Database: "shop",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED", Nullable: false},
			{Name: "email", Type: "VARCHAR(255)", Nullable: false},
			{Name: "age", Type: "INT", Nullable: true},
		},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
		Indexes: []service.TableIndex{
			{Name: "idx_email", Columns: []service.IndexCol{{Name: "email"}}, Unique: true},
		},
		SchemaVersion: 1,
		// Resolved just now, as TableService.GetTable reports it: the
		// generation pin is fresh (see confirmGenerationPin).
		GenerationReadAt: time.Now(),
	}
}

// encodeUserRow encodes a [id, email, age] row for def using catalog.RowCodec.
func encodeUserRow(t *testing.T, def *service.TableDef, id uint64, email string, age any) []byte {
	t.Helper()
	rc, err := catalog.NewRowCodec(def)
	require.NoError(t, err)
	data, err := rc.Encode(sqle.Row{id, email, age})
	require.NoError(t, err)
	return data
}

// encodeUserKey encodes the primary key for id using catalog.KeyCodec.
func encodeUserKey(t *testing.T, def *service.TableDef, id uint64) string {
	t.Helper()
	kc, err := catalog.NewKeyCodec(def)
	require.NoError(t, err)
	key, err := kc.EncodeValues([]any{id})
	require.NoError(t, err)
	return key
}

// ---------------------------------------------------------------------------
// Constructor tests
// ---------------------------------------------------------------------------

func TestNewRelationalTable_Basic(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)
	require.NotNil(t, tbl)

	assert.Equal(t, "users", tbl.Name())
	assert.Equal(t, "users", tbl.String())
	require.True(t, tbl.hasPK)
	require.Len(t, tbl.pkSpec.columns, 1)
	assert.Equal(t, 0, tbl.pkSpec.columns[0].ordinal)
	assert.Equal(t, "id", tbl.pkSpec.columns[0].name)
}

func TestNewRelationalTable_NoColumns(t *testing.T) {
	def := &service.TableDef{Name: "empty", Database: "shop", TableID: 1}
	_, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.Error(t, err)
	var emptyErr *catalog.EmptyColumnsError
	assert.ErrorAs(t, err, &emptyErr)
}

func TestNewRelationalTable_UnsupportedColumnType(t *testing.T) {
	def := &service.TableDef{
		Name: "bad", Database: "shop", TableID: 1,
		Columns: []service.ColumnDef{{Name: "c", Type: "NOT_A_TYPE"}},
	}
	_, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.Error(t, err)
}

func TestNewRelationalTable_CompositePrimaryKey(t *testing.T) {
	def := &service.TableDef{
		Name: "orders", Database: "shop", TableID: 2,
		Columns: []service.ColumnDef{
			{Name: "tenant_id", Type: "BIGINT UNSIGNED"},
			{Name: "order_id", Type: "BIGINT UNSIGNED"},
		},
		PrimaryKey: []service.PKColumn{{Name: "tenant_id"}, {Name: "order_id"}},
	}
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)
	require.True(t, tbl.hasPK, "a composite primary key is resolved like any other multi-column index")
	require.Len(t, tbl.pkSpec.columns, 2)
	assert.Equal(t, []int{0, 1}, tbl.pkSpec.ordinals())
	assert.Len(t, tbl.pkSpec.codecs, 2, "one codec per leading-column prefix")
}

func TestNewRelationalTable_PrimaryKeyColumnNotFound(t *testing.T) {
	def := &service.TableDef{
		Name: "orphan", Database: "shop", TableID: 4,
		Columns:    []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}},
		PrimaryKey: []service.PKColumn{{Name: "does_not_exist"}},
	}
	_, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.Error(t, err)
	var notFound *catalog.ColumnNotFoundError
	assert.ErrorAs(t, err, &notFound)
}

func TestNewRelationalTable_UnsupportedKeyColumnType(t *testing.T) {
	def := &service.TableDef{
		Name: "amounts", Database: "shop", TableID: 5,
		Columns:    []service.ColumnDef{{Name: "amount", Type: "DECIMAL(10,2)"}},
		PrimaryKey: []service.PKColumn{{Name: "amount"}},
	}
	_, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.Error(t, err)
	var unsupported *catalog.UnsupportedKeyColumnTypeError
	assert.ErrorAs(t, err, &unsupported)
}

func TestNewRelationalTable_NoPrimaryKey(t *testing.T) {
	def := &service.TableDef{
		Name: "log", Database: "shop", TableID: 3,
		Columns: []service.ColumnDef{{Name: "message", Type: "TEXT"}},
	}
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)
	assert.False(t, tbl.hasPK, "a table keyed by the synthetic row id offers no PRIMARY index")
}

// ---------------------------------------------------------------------------
// Schema / Collation / Partitions
// ---------------------------------------------------------------------------

func TestRelationalTable_Schema(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	schema := tbl.Schema(newTestContext())
	require.Len(t, schema, 3)
	assert.Equal(t, "id", schema[0].Name)
	assert.True(t, schema[0].PrimaryKey)
	assert.Equal(t, "email", schema[1].Name)
	assert.False(t, schema[1].Nullable)
	assert.Equal(t, "age", schema[2].Name)
	assert.True(t, schema[2].Nullable)
}

func TestRelationalTable_Collation_EmptyDefaultsToDefault(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)
	assert.Equal(t, sqle.Collation_Default, tbl.Collation())
}

func TestRelationalTable_Collation_ValidName(t *testing.T) {
	def := testUsersDef()
	def.Collation = "utf8mb4_general_ci"
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)
	assert.NotEqual(t, sqle.Collation_Unspecified, tbl.Collation())
}

func TestRelationalTable_Collation_InvalidNameFallsBackToDefault(t *testing.T) {
	def := testUsersDef()
	def.Collation = "not_a_real_collation"
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)
	assert.Equal(t, sqle.Collation_Default, tbl.Collation())
}

func TestRelationalTable_Partitions(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	ctx := newTestContext()
	iter, err := tbl.Partitions(ctx)
	require.NoError(t, err)

	p, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("default"), p.Key())

	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

// ---------------------------------------------------------------------------
// Full scan: PK order, empty table, schema-version skew, error propagation
// ---------------------------------------------------------------------------

func TestRelationalTable_FullScan_Empty(t *testing.T) {
	def := testUsersDef()
	prefix := service.TableDataPrefix(def.Database, def.TableID, 0)
	kv := &mockKVService{
		scanFn: func(_ context.Context, p string) (map[string][]byte, error) {
			assert.Equal(t, prefix, p)
			return nil, nil
		},
	}
	tbl, err := NewRelationalTable(def, kv, nil)
	require.NoError(t, err)

	ctx := newTestContext()
	iter, err := tbl.PartitionRows(ctx, &relationalPartition{})
	require.NoError(t, err)

	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

func TestRelationalTable_FullScan_ReturnsPrimaryKeyOrder(t *testing.T) {
	def := testUsersDef()
	prefix := service.TableDataPrefix(def.Database, def.TableID, 0)

	// Insert rows out of numeric order; catalog.KeyCodec's big-endian
	// numeric encoding means a lexicographic sort of the encoded keys is
	// numeric order, not insertion order.
	data := map[string][]byte{
		prefix + encodeUserKey(t, def, 30): encodeUserRow(t, def, 30, "c@example.com", int32(1)),
		prefix + encodeUserKey(t, def, 10): encodeUserRow(t, def, 10, "a@example.com", int32(2)),
		prefix + encodeUserKey(t, def, 20): encodeUserRow(t, def, 20, "b@example.com", int32(3)),
	}
	kv := &mockKVService{
		scanFn: func(context.Context, string) (map[string][]byte, error) {
			return data, nil
		},
	}
	tbl, err := NewRelationalTable(def, kv, nil)
	require.NoError(t, err)

	ctx := newTestContext()
	iter, err := tbl.PartitionRows(ctx, &relationalPartition{})
	require.NoError(t, err)

	var ids []uint64
	for {
		row, ierr := iter.Next(ctx)
		if errors.Is(ierr, io.EOF) {
			break
		}
		require.NoError(t, ierr)
		ids = append(ids, row[0].(uint64))
	}
	assert.Equal(t, []uint64{10, 20, 30}, ids)
}

func TestRelationalTable_FullScan_ScanError(t *testing.T) {
	def := testUsersDef()
	scanErr := errors.New("scan failed")
	kv := &mockKVService{
		scanFn: func(context.Context, string) (map[string][]byte, error) {
			return nil, scanErr
		},
	}
	tbl, err := NewRelationalTable(def, kv, nil)
	require.NoError(t, err)

	_, err = tbl.PartitionRows(newTestContext(), &relationalPartition{})
	assert.ErrorIs(t, err, scanErr)
}

func TestRelationalTable_FullScan_DecodeError(t *testing.T) {
	def := testUsersDef()
	prefix := service.TableDataPrefix(def.Database, def.TableID, 0)
	kv := &mockKVService{
		scanFn: func(context.Context, string) (map[string][]byte, error) {
			return map[string][]byte{prefix + "k": []byte("not a valid row")}, nil
		},
	}
	tbl, err := NewRelationalTable(def, kv, nil)
	require.NoError(t, err)

	_, err = tbl.PartitionRows(newTestContext(), &relationalPartition{})
	assert.Error(t, err)
}

// TestRelationalTable_FullScan_SchemaVersionSkew verifies that a row written
// under an older, narrower schema (fewer columns) decodes cleanly against a
// RelationalTable built from a newer definition that has since gained a
// nullable trailing column, with the new column filled as NULL.
func TestRelationalTable_FullScan_SchemaVersionSkew(t *testing.T) {
	oldDef := &service.TableDef{
		TableID: 1, Name: "widgets", Database: "shop",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED"},
			{Name: "name", Type: "VARCHAR(50)"},
		},
		PrimaryKey:    []service.PKColumn{{Name: "id"}},
		SchemaVersion: 1,
	}
	oldCodec, err := catalog.NewRowCodec(oldDef)
	require.NoError(t, err)
	oldData, err := oldCodec.Encode(sqle.Row{uint64(1), "widget-a"})
	require.NoError(t, err)

	newDef := &service.TableDef{
		TableID: 1, Name: "widgets", Database: "shop",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED"},
			{Name: "name", Type: "VARCHAR(50)"},
			{Name: "note", Type: "VARCHAR(50)", Nullable: true},
		},
		PrimaryKey:    []service.PKColumn{{Name: "id"}},
		SchemaVersion: 2,
	}

	prefix := service.TableDataPrefix(newDef.Database, newDef.TableID, 0)
	key := encodeUserKey(t, &service.TableDef{Columns: newDef.Columns, PrimaryKey: newDef.PrimaryKey, Name: newDef.Name}, 1)

	kv := &mockKVService{
		scanFn: func(_ context.Context, p string) (map[string][]byte, error) {
			assert.Equal(t, prefix, p)
			return map[string][]byte{prefix + key: oldData}, nil
		},
	}

	tbl, err := NewRelationalTable(newDef, kv, nil)
	require.NoError(t, err)

	ctx := newTestContext()
	iter, err := tbl.PartitionRows(ctx, &relationalPartition{})
	require.NoError(t, err)

	row, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), row[0])
	assert.Equal(t, "widget-a", row[1])
	assert.Nil(t, row[2], "trailing column added after this row was written should decode as NULL")
}

// ---------------------------------------------------------------------------
// Indexed partition rows (accelerated PK point lookup)
// ---------------------------------------------------------------------------

func TestRelationalTable_IndexedPartitionRows_UsesGetNotScan(t *testing.T) {
	def := testUsersDef()
	prefix := service.TableDataPrefix(def.Database, def.TableID, 0)
	key := encodeUserKey(t, def, 1)
	rowData := encodeUserRow(t, def, 1, "a@example.com", int32(30))

	kv := &mockKVService{
		getFn: func(_ context.Context, k string) ([]byte, error) {
			assert.Equal(t, prefix+key, k)
			return rowData, nil
		},
		scanFn: func(context.Context, string) (map[string][]byte, error) {
			t.Fatal("an accelerated indexed lookup must not perform a full scan")
			return nil, nil
		},
	}
	tbl, err := NewRelationalTable(def, kv, nil)
	require.NoError(t, err)

	ctx := newTestContext()
	iter, err := tbl.PartitionRows(ctx, &indexedPartition{keys: [][]byte{[]byte(key)}})
	require.NoError(t, err)

	row, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), row[0])
	assert.Equal(t, "a@example.com", row[1])
}

func TestRelationalTable_IndexedPartitionRows_SkipsMissingKey(t *testing.T) {
	def := testUsersDef()
	kv := &mockKVService{
		getFn: func(context.Context, string) ([]byte, error) {
			return nil, &service.KeyNotFoundError{Key: "gone"}
		},
	}
	tbl, err := NewRelationalTable(def, kv, nil)
	require.NoError(t, err)

	ctx := newTestContext()
	iter, err := tbl.PartitionRows(ctx, &indexedPartition{keys: [][]byte{[]byte("missing")}})
	require.NoError(t, err)

	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

func TestRelationalTable_IndexedPartitionRows_DecodeError(t *testing.T) {
	def := testUsersDef()
	kv := &mockKVService{
		getFn: func(context.Context, string) ([]byte, error) {
			return []byte("garbage"), nil
		},
	}
	tbl, err := NewRelationalTable(def, kv, nil)
	require.NoError(t, err)

	_, err = tbl.PartitionRows(newTestContext(), &indexedPartition{keys: [][]byte{[]byte("k")}})
	assert.Error(t, err)
}

func TestRelationalTable_PartitionRows_FilteredPartition(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	rows := []sqle.Row{{uint64(1), "a@example.com", int32(1)}}
	ctx := newTestContext()
	iter, err := tbl.PartitionRows(ctx, &filteredPartition{rows: rows})
	require.NoError(t, err)

	row, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, rows[0], row)
}

// ---------------------------------------------------------------------------
// PrimaryKeySchema
// ---------------------------------------------------------------------------

func TestRelationalTable_PrimaryKeySchema(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	pk := tbl.PrimaryKeySchema(newTestContext())
	require.Len(t, pk.PkOrdinals, 1)
	assert.Equal(t, 0, pk.PkOrdinals[0])
}

// ---------------------------------------------------------------------------
// RowCount / DataLength
// ---------------------------------------------------------------------------

func TestRelationalTable_RowCount_EstimateIsInexactAndLocal(t *testing.T) {
	def := testUsersDef()
	prefix := service.TableDataPrefix(def.Database, def.TableID, 0)
	var linearizable int
	mvcc := &fakeMVCCService{
		rangeFn: func(context.Context, []byte, []byte, ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
			linearizable++
			return nil, 0, 0, nil
		},
		rangeConsistencyFn: func(_ context.Context, start, end []byte, ro consistency.ReadOptions, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
			assert.Equal(t, []byte(prefix), start)
			assert.Equal(t, prefixEnd([]byte(prefix)), end)
			assert.Equal(t, consistency.Eventual, ro.Consistency, "an estimate is a local read")
			assert.True(t, ro.AllowStale)
			assert.True(t, service.ApplyRangeOptions(opts).CountOnly)
			return nil, 5, 42, nil
		},
	}
	tbl, err := NewRelationalTable(def, &mockKVService{}, mvcc)
	require.NoError(t, err)

	count, exact, err := tbl.RowCount(newTestContext())
	require.NoError(t, err)
	assert.False(t, exact, "an estimate must never be reported exact: go-mysql-server would substitute it for COUNT(*)")
	assert.Equal(t, uint64(5), count)
	assert.Zero(t, linearizable, "RowCount must not issue a linearizable Range")
}

func TestRelationalTable_RowCount_CountedOncePerGeneration(t *testing.T) {
	def := testUsersDef()
	var reads int
	mvcc := &fakeMVCCService{
		rangeConsistencyFn: func(context.Context, []byte, []byte, consistency.ReadOptions, ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
			reads++
			return nil, 9, 0, nil
		},
	}
	tbl, err := NewRelationalTable(def, &mockKVService{}, mvcc)
	require.NoError(t, err)

	ctx := newTestContext()
	for range 4 {
		n, _, err := tbl.RowCount(ctx)
		require.NoError(t, err)
		assert.Equal(t, uint64(9), n)
	}
	got, err := tbl.DataLength(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(9*tbl.averageRowSize()), got)
	assert.Equal(t, 1, reads, "planning asks for the count several times; the table is counted once")
}

func TestRelationalTable_RowCount_NegativeCountIsZero(t *testing.T) {
	def := testUsersDef()
	mvcc := &fakeMVCCService{
		rangeConsistencyFn: func(context.Context, []byte, []byte, consistency.ReadOptions, ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
			return nil, -3, 0, nil
		},
	}
	tbl, err := NewRelationalTable(def, &mockKVService{}, mvcc)
	require.NoError(t, err)

	n, exact, err := tbl.RowCount(newTestContext())
	require.NoError(t, err)
	assert.False(t, exact)
	assert.Zero(t, n)
}

func TestRelationalTable_RowCount_NilMVCC(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	_, _, err = tbl.RowCount(newTestContext())
	assert.IsType(t, &RelationalMVCCUnavailableError{}, err)
}

func TestRelationalTable_RowCount_Error(t *testing.T) {
	def := testUsersDef()
	rangeErr := errors.New("range failed")
	mvcc := &fakeMVCCService{
		rangeConsistencyFn: func(context.Context, []byte, []byte, consistency.ReadOptions, ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
			return nil, 0, 0, rangeErr
		},
	}
	tbl, err := NewRelationalTable(def, &mockKVService{}, mvcc)
	require.NoError(t, err)

	_, _, err = tbl.RowCount(newTestContext())
	assert.ErrorIs(t, err, rangeErr)
}

func TestRelationalTable_DataLength(t *testing.T) {
	def := testUsersDef()
	mvcc := &fakeMVCCService{
		rangeConsistencyFn: func(context.Context, []byte, []byte, consistency.ReadOptions, ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
			return nil, 10, 0, nil
		},
	}
	tbl, err := NewRelationalTable(def, &mockKVService{}, mvcc)
	require.NoError(t, err)

	rc, err := catalog.NewRowCodec(def)
	require.NoError(t, err)
	zeroRow := make(sqle.Row, 3)
	schema := tbl.Schema(newTestContext())
	for i, col := range schema {
		zeroRow[i] = col.Type.Zero()
	}
	wantAvg := rc.EncodedSize(zeroRow)

	got, err := tbl.DataLength(newTestContext())
	require.NoError(t, err)
	assert.Equal(t, uint64(10*wantAvg), got)
}

func TestRelationalTable_DataLength_RowCountError(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	_, err = tbl.DataLength(newTestContext())
	assert.IsType(t, &RelationalMVCCUnavailableError{}, err)
}

// ---------------------------------------------------------------------------
// GetIndexes
// ---------------------------------------------------------------------------

func TestRelationalTable_GetIndexes_PKPlusSecondary(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	indexes, err := tbl.GetIndexes(newTestContext())
	require.NoError(t, err)
	require.Len(t, indexes, 2)

	names := map[string]sqle.Index{}
	for _, idx := range indexes {
		qi := idx.(*qrdbIndex)
		names[qi.firstColumn()] = idx
	}
	require.Contains(t, names, "id")
	require.Contains(t, names, "email")
	assert.True(t, names["id"].IsUnique())
	assert.True(t, names["email"].IsUnique())
}

func TestRelationalTable_GetIndexes_NoDeclaredPrimaryKey(t *testing.T) {
	def := &service.TableDef{
		Name: "log", Database: "shop", TableID: 3,
		Columns: []service.ColumnDef{{Name: "message", Type: "TEXT"}},
		Indexes: []service.TableIndex{
			{Name: "idx_message", Columns: []service.IndexCol{{Name: "message"}}},
		},
	}
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	indexes, err := tbl.GetIndexes(newTestContext())
	require.NoError(t, err)
	require.Len(t, indexes, 1)
	assert.Equal(t, "message", indexes[0].(*qrdbIndex).firstColumn())
}

func TestRelationalTable_GetIndexes_SkipsEmptyIndexColumns(t *testing.T) {
	def := testUsersDef()
	def.Indexes = append(def.Indexes, service.TableIndex{Name: "broken"})
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	indexes, err := tbl.GetIndexes(newTestContext())
	require.NoError(t, err)
	for _, idx := range indexes {
		assert.NotEqual(t, "broken", idx.ID())
	}
}

// TestRelationalTable_GetIndexes_SkipsUnresolvableIndexColumn covers an
// index naming a column the schema no longer has: it is not reported to the
// planner at all. Reporting it would offer an index whose lookups this table
// cannot serve, since no indexSpec resolves for it either (see
// buildIndexSpecs) -- and PreciseMatch promises every reported index answers
// its predicate exactly.
func TestRelationalTable_GetIndexes_SkipsUnresolvableIndexColumn(t *testing.T) {
	def := testUsersDef()
	def.Indexes = []service.TableIndex{
		{Name: "idx_ghost", Columns: []service.IndexCol{{Name: "does_not_exist"}}},
	}
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	indexes, err := tbl.GetIndexes(newTestContext())
	require.NoError(t, err)
	require.Len(t, indexes, 1) // the primary key only
	assert.Equal(t, primaryIndexName, indexes[0].ID())
}

// tableIndexByID returns the sql.Index tbl reports under id, failing the
// test when it reports none. Every LookupPartitions test builds its lookup
// from this rather than hand-constructing a qrdbIndex, because
// LookupPartitions resolves a lookup back to the table's own primary key or
// indexSpec through exactly this ID (see RelationalTable.lookupKeys) -- so a
// hand-built index would exercise a shape the planner can never produce.
func tableIndexByID(t *testing.T, tbl *RelationalTable, id string) sqle.Index {
	t.Helper()
	indexes, err := tbl.GetIndexes(newTestContext())
	require.NoError(t, err)
	for _, idx := range indexes {
		if idx.ID() == id {
			return idx
		}
	}
	t.Fatalf("table %q reports no index with ID %q", tbl.Name(), id)
	return nil
}

// ---------------------------------------------------------------------------
// IndexedAccess / PreciseMatch
// ---------------------------------------------------------------------------

func TestRelationalTable_IndexedAccess_ReturnsSelf(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	got := tbl.IndexedAccess(newTestContext(), sqle.IndexLookup{})
	assert.Same(t, tbl, got)
}

func TestRelationalTable_PreciseMatch(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)
	assert.True(t, tbl.PreciseMatch())
}

// ---------------------------------------------------------------------------
// LookupPartitions
// ---------------------------------------------------------------------------

func TestRelationalTable_LookupPartitions_EmptyLookup(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{scanFn: func(context.Context, string) (map[string][]byte, error) {
		return nil, nil
	}}, nil)
	require.NoError(t, err)

	iter, err := tbl.LookupPartitions(newTestContext(), sqle.IndexLookup{})
	require.NoError(t, err)
	p, err := iter.Next(newTestContext())
	require.NoError(t, err)
	_, ok := p.(*relationalPartition)
	assert.True(t, ok)
}

func TestRelationalTable_LookupPartitions_NonQRDBIndex(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	other := &fakeSQLIndex{}
	expr := sqle.ClosedRangeColumnExpr(uint64(1), uint64(1), types.Uint64)
	lookup := sqle.IndexLookup{Index: other, Ranges: sqle.MySQLRangeCollection{sqle.MySQLRange{expr}}}

	iter, err := tbl.LookupPartitions(newTestContext(), lookup)
	require.NoError(t, err)
	p, err := iter.Next(newTestContext())
	require.NoError(t, err)
	_, ok := p.(*relationalPartition)
	assert.True(t, ok)
}

// TestRelationalTable_LookupPartitions_UnresolvableIndex covers a lookup
// naming an index this table does not report: with no column set to
// evaluate the pushed-down ranges against, it answers with a plain full
// scan rather than guessing.
func TestRelationalTable_LookupPartitions_UnresolvableIndex(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	idx := newQRDBIndex(def.Database, def.Name, "does_not_exist", "string", false)
	expr := sqle.ClosedRangeColumnExpr("x", "x", types.MustCreateString(sqltypes.VarChar, 255, sqle.Collation_Default))
	lookup := sqle.IndexLookup{Index: idx, Ranges: sqle.MySQLRangeCollection{sqle.MySQLRange{expr}}}

	iter, err := tbl.LookupPartitions(newTestContext(), lookup)
	require.NoError(t, err)
	p, err := iter.Next(newTestContext())
	require.NoError(t, err)
	_, ok := p.(*relationalPartition)
	assert.True(t, ok)
}

func TestRelationalTable_LookupPartitions_PKEquality_AcceleratesToKeyLookup(t *testing.T) {
	def := testUsersDef()
	key := encodeUserKey(t, def, 42)

	scanCalled := false
	kv := &mockKVService{
		scanFn: func(context.Context, string) (map[string][]byte, error) {
			scanCalled = true
			return nil, nil
		},
	}
	tbl, err := NewRelationalTable(def, kv, nil)
	require.NoError(t, err)

	idx := tableIndexByID(t, tbl, primaryIndexName)
	expr := sqle.ClosedRangeColumnExpr(uint64(42), uint64(42), types.Uint64)
	lookup := sqle.IndexLookup{Index: idx, Ranges: sqle.MySQLRangeCollection{sqle.MySQLRange{expr}}}

	iter, err := tbl.LookupPartitions(newTestContext(), lookup)
	require.NoError(t, err)
	part, err := iter.Next(newTestContext())
	require.NoError(t, err)

	ip, ok := part.(*indexedPartition)
	require.True(t, ok, "an equality primary-key predicate must use the accelerated index path")
	require.Len(t, ip.keys, 1)
	assert.Equal(t, key, string(ip.keys[0]))
	assert.False(t, scanCalled, "equality PK lookup must not perform a full scan")
}

func TestRelationalTable_LookupPartitions_PKEquality_DedupsInList(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	idx := tableIndexByID(t, tbl, primaryIndexName)
	e1 := sqle.ClosedRangeColumnExpr(uint64(1), uint64(1), types.Uint64)
	e2 := sqle.ClosedRangeColumnExpr(uint64(1), uint64(1), types.Uint64)
	e3 := sqle.ClosedRangeColumnExpr(uint64(2), uint64(2), types.Uint64)
	lookup := sqle.IndexLookup{Index: idx, Ranges: sqle.MySQLRangeCollection{
		sqle.MySQLRange{e1}, sqle.MySQLRange{e2}, sqle.MySQLRange{e3},
	}}

	iter, err := tbl.LookupPartitions(newTestContext(), lookup)
	require.NoError(t, err)
	part, err := iter.Next(newTestContext())
	require.NoError(t, err)
	ip := part.(*indexedPartition)
	assert.Len(t, ip.keys, 2, "duplicate equality values must be deduplicated")
}

func TestRelationalTable_LookupPartitions_PKRange_FallsBackToFilteredScan(t *testing.T) {
	def := testUsersDef()
	prefix := service.TableDataPrefix(def.Database, def.TableID, 0)

	data := map[string][]byte{
		prefix + encodeUserKey(t, def, 10): encodeUserRow(t, def, 10, "a@example.com", int32(20)),
		prefix + encodeUserKey(t, def, 20): encodeUserRow(t, def, 20, "b@example.com", int32(25)),
	}
	kv := &mockKVService{scanFn: func(_ context.Context, p string) (map[string][]byte, error) {
		assert.Equal(t, prefix, p)
		return data, nil
	}}
	tbl, err := NewRelationalTable(def, kv, nil)
	require.NoError(t, err)

	idx := tableIndexByID(t, tbl, primaryIndexName)
	expr := sqle.GreaterThanRangeColumnExpr(uint64(10), types.Uint64)
	lookup := sqle.IndexLookup{Index: idx, Ranges: sqle.MySQLRangeCollection{sqle.MySQLRange{expr}}}

	iter, err := tbl.LookupPartitions(newTestContext(), lookup)
	require.NoError(t, err)
	part, err := iter.Next(newTestContext())
	require.NoError(t, err)

	fp, ok := part.(*filteredPartition)
	require.True(t, ok, "a non-equality primary-key predicate must fall back to a filtered scan")
	require.Len(t, fp.rows, 1)
	assert.Equal(t, uint64(20), fp.rows[0][0])
}

// TestRelationalTable_LookupPartitions_SecondaryColumn_IndexAccelerated
// covers Phase 6's secondary-index wiring: an exact-match predicate on a
// column carrying a physical unique index is answered directly from that
// index's entries (one KV point read plus one row fetch), never by
// scanning the table's full data prefix. See
// TestRelationalTable_LookupPartitions_SecondaryColumn_NoPhysicalIndex_FilteredScan
// for the fallback this replaces when a column has no physical index (or
// the predicate is not a simple equality).
func TestRelationalTable_LookupPartitions_SecondaryColumn_IndexAccelerated(t *testing.T) {
	def := testUsersDef() // idx_email is UNIQUE.
	prefix := service.TableDataPrefix(def.Database, def.TableID, 0)
	idxPrefix := service.TableIndexPrefix(def.Database, def.TableID, 0, incarnationOf(def, "idx_email"))

	schema, err := catalog.SchemaOf(def)
	require.NoError(t, err)
	specs, err := buildIndexSpecs(def, schema)
	require.NoError(t, err)
	require.Len(t, specs, 1)
	encodedVal, err := specs[0].valueCodec().EncodeValues([]any{"b@example.com"})
	require.NoError(t, err)

	pk2 := encodeUserKey(t, def, 2)

	fullScanCalled := false
	kv := &mockKVService{
		scanFn: func(_ context.Context, p string) (map[string][]byte, error) {
			if p == prefix {
				fullScanCalled = true
			}
			return nil, nil
		},
		getFn: func(_ context.Context, key string) ([]byte, error) {
			switch key {
			case idxPrefix + encodedVal:
				return []byte(pk2), nil
			case prefix + pk2:
				return encodeUserRow(t, def, 2, "b@example.com", int32(25)), nil
			default:
				return nil, &service.KeyNotFoundError{Key: key}
			}
		},
	}
	tbl, err := NewRelationalTable(def, kv, nil)
	require.NoError(t, err)

	idx := tableIndexByID(t, tbl, "idx_email")
	strType := tbl.schema[1].Type
	expr := sqle.ClosedRangeColumnExpr("b@example.com", "b@example.com", strType)
	lookup := sqle.IndexLookup{Index: idx, Ranges: sqle.MySQLRangeCollection{sqle.MySQLRange{expr}}}

	iter, err := tbl.LookupPartitions(newTestContext(), lookup)
	require.NoError(t, err)
	part, err := iter.Next(newTestContext())
	require.NoError(t, err)

	ip, ok := part.(*indexedPartition)
	require.True(t, ok, "an exact-match unique-index predicate must be answered from the physical index, not a filtered scan")
	require.Len(t, ip.keys, 1)
	assert.Equal(t, []byte(pk2), ip.keys[0])

	rowIter, err := tbl.PartitionRows(newTestContext(), ip)
	require.NoError(t, err)
	row, err := rowIter.Next(newTestContext())
	require.NoError(t, err)
	assert.Equal(t, uint64(2), row[0])

	assert.False(t, fullScanCalled, "an index-accelerated lookup must not fall back to a full table scan")
}

// TestRelationalTable_LookupPartitions_SecondaryColumn_RangePredicate_FilteredScan
// covers a non-equality predicate on an indexed column: qrdb's physical
// index entries answer point lookups only, so a genuine range must fall back
// to the filtered full-scan path, which re-evaluates the planner's own
// bounds and is therefore still exact (see PreciseMatch).
func TestRelationalTable_LookupPartitions_SecondaryColumn_RangePredicate_FilteredScan(t *testing.T) {
	def := testUsersDef()
	prefix := service.TableDataPrefix(def.Database, def.TableID, 0)

	data := map[string][]byte{
		prefix + encodeUserKey(t, def, 1): encodeUserRow(t, def, 1, "a@example.com", int32(20)),
		prefix + encodeUserKey(t, def, 2): encodeUserRow(t, def, 2, "b@example.com", int32(25)),
	}
	kv := &mockKVService{scanFn: func(context.Context, string) (map[string][]byte, error) {
		return data, nil
	}}
	tbl, err := NewRelationalTable(def, kv, nil)
	require.NoError(t, err)

	idx := tableIndexByID(t, tbl, "idx_email")
	strType := tbl.schema[1].Type
	expr := sqle.GreaterThanRangeColumnExpr("a@example.com", strType)
	lookup := sqle.IndexLookup{Index: idx, Ranges: sqle.MySQLRangeCollection{sqle.MySQLRange{expr}}}

	iter, err := tbl.LookupPartitions(newTestContext(), lookup)
	require.NoError(t, err)
	part, err := iter.Next(newTestContext())
	require.NoError(t, err)

	fp, ok := part.(*filteredPartition)
	require.True(t, ok, "a range predicate must fall back to the filtered-scan path")
	require.Len(t, fp.rows, 1)
	assert.Equal(t, uint64(2), fp.rows[0][0])
}

func TestRelationalTable_LookupPartitions_ScanError(t *testing.T) {
	def := testUsersDef()
	scanErr := errors.New("scan failed")
	kv := &mockKVService{scanFn: func(context.Context, string) (map[string][]byte, error) {
		return nil, scanErr
	}}
	tbl, err := NewRelationalTable(def, kv, nil)
	require.NoError(t, err)

	idx := tableIndexByID(t, tbl, "idx_email")
	strType := tbl.schema[1].Type
	expr := sqle.ClosedRangeColumnExpr("a@example.com", "b@example.com", strType)
	lookup := sqle.IndexLookup{Index: idx, Ranges: sqle.MySQLRangeCollection{sqle.MySQLRange{expr}}}

	_, err = tbl.LookupPartitions(newTestContext(), lookup)
	assert.IsType(t, &IndexLookupError{}, err)
}

// ---------------------------------------------------------------------------
// primaryKeysFromRanges
// ---------------------------------------------------------------------------

func TestPrimaryKeysFromRanges_NonMySQLRange(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	_, exact, err := tbl.primaryKeysFromRanges(newTestContext(), []sqle.Range{fakeRange{}})
	require.NoError(t, err)
	assert.False(t, exact)
}

func TestPrimaryKeysFromRanges_NonEqualBounds(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	expr := sqle.ClosedRangeColumnExpr(uint64(1), uint64(2), types.Uint64)
	_, exact, err := tbl.primaryKeysFromRanges(newTestContext(), []sqle.Range{sqle.MySQLRange{expr}})
	require.NoError(t, err)
	assert.False(t, exact)
}

func TestPrimaryKeysFromRanges_NonBindingBound(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	expr := sqle.AllRangeColumnExpr(types.Uint64)
	_, exact, err := tbl.primaryKeysFromRanges(newTestContext(), []sqle.Range{sqle.MySQLRange{expr}})
	require.NoError(t, err)
	assert.False(t, exact)
}

func TestPrimaryKeysFromRanges_MultiColumnRange(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	expr := sqle.ClosedRangeColumnExpr(uint64(1), uint64(1), types.Uint64)
	_, exact, err := tbl.primaryKeysFromRanges(newTestContext(), []sqle.Range{sqle.MySQLRange{expr, expr}})
	require.NoError(t, err)
	assert.False(t, exact)
}

// ---------------------------------------------------------------------------
// rowMatchesRange / rowMatchesRanges
// ---------------------------------------------------------------------------

// lessThanOrNullExpr builds a RangeType_LessThanOrNull column expression
// ({x < upper} OR {x IS NULL}). There is no exported convenience
// constructor for this shape in go-mysql-server: LessThanRangeColumnExpr
// excludes NULL (AboveNull{} lower bound) and therefore produces
// RangeType_OpenOpen instead.
func lessThanOrNullExpr(upper any, typ sqle.Type) sqle.MySQLRangeColumnExpr {
	return sqle.MySQLRangeColumnExpr{
		LowerBound: sqle.BelowNull{},
		UpperBound: sqle.Below{Key: upper, Typ: typ},
		Typ:        typ,
	}
}

// lessOrEqualOrNullExpr builds a RangeType_LessOrEqualOrNull column
// expression ({x <= upper} OR {x IS NULL}); see lessThanOrNullExpr.
func lessOrEqualOrNullExpr(upper any, typ sqle.Type) sqle.MySQLRangeColumnExpr {
	return sqle.MySQLRangeColumnExpr{
		LowerBound: sqle.BelowNull{},
		UpperBound: sqle.Above{Key: upper, Typ: typ},
		Typ:        typ,
	}
}

func TestRowMatchesRange_AllTypes(t *testing.T) {
	ctx := newTestContext()
	typ := types.Int32

	tests := []struct {
		name string
		expr sqle.MySQLRangeColumnExpr
		val  any
		want bool
	}{
		{"empty", sqle.EmptyRangeColumnExpr(typ), int32(5), false},
		{"all", sqle.AllRangeColumnExpr(typ), int32(5), true},
		{"equalNull/null", sqle.NullRangeColumnExpr(typ), nil, true},
		{"equalNull/nonNull", sqle.NullRangeColumnExpr(typ), int32(5), false},
		{"closedClosed/inLower", sqle.ClosedRangeColumnExpr(int32(5), int32(10), typ), int32(5), true},
		{"closedClosed/inUpper", sqle.ClosedRangeColumnExpr(int32(5), int32(10), typ), int32(10), true},
		{"closedClosed/inMiddle", sqle.ClosedRangeColumnExpr(int32(5), int32(10), typ), int32(7), true},
		{"closedClosed/below", sqle.ClosedRangeColumnExpr(int32(5), int32(10), typ), int32(4), false},
		{"closedClosed/above", sqle.ClosedRangeColumnExpr(int32(5), int32(10), typ), int32(11), false},
		{"openOpen/lowerExcluded", sqle.OpenRangeColumnExpr(int32(5), int32(10), typ), int32(5), false},
		{"openOpen/upperExcluded", sqle.OpenRangeColumnExpr(int32(5), int32(10), typ), int32(10), false},
		{"openOpen/inMiddle", sqle.OpenRangeColumnExpr(int32(5), int32(10), typ), int32(7), true},
		{"openClosed/lowerExcluded", sqle.MySQLRangeColumnExpr{LowerBound: sqle.Above{Key: int32(5), Typ: typ}, UpperBound: sqle.Above{Key: int32(10), Typ: typ}, Typ: typ}, int32(5), false},
		{"openClosed/upperIncluded", sqle.MySQLRangeColumnExpr{LowerBound: sqle.Above{Key: int32(5), Typ: typ}, UpperBound: sqle.Above{Key: int32(10), Typ: typ}, Typ: typ}, int32(10), true},
		{"closedOpen/lowerIncluded", sqle.MySQLRangeColumnExpr{LowerBound: sqle.Below{Key: int32(5), Typ: typ}, UpperBound: sqle.Below{Key: int32(10), Typ: typ}, Typ: typ}, int32(5), true},
		{"closedOpen/upperExcluded", sqle.MySQLRangeColumnExpr{LowerBound: sqle.Below{Key: int32(5), Typ: typ}, UpperBound: sqle.Below{Key: int32(10), Typ: typ}, Typ: typ}, int32(10), false},
		{"greaterThan/atBound", sqle.GreaterThanRangeColumnExpr(int32(5), typ), int32(5), false},
		{"greaterThan/above", sqle.GreaterThanRangeColumnExpr(int32(5), typ), int32(6), true},
		{"greaterOrEqual/atBound", sqle.GreaterOrEqualRangeColumnExpr(int32(5), typ), int32(5), true},
		{"lessThanOrNull/atBound", lessThanOrNullExpr(int32(10), typ), int32(10), false},
		{"lessThanOrNull/below", lessThanOrNullExpr(int32(10), typ), int32(9), true},
		{"lessThanOrNull/null", lessThanOrNullExpr(int32(10), typ), nil, true},
		{"lessOrEqualOrNull/atBound", lessOrEqualOrNullExpr(int32(10), typ), int32(10), true},
		{"lessOrEqualOrNull/above", lessOrEqualOrNullExpr(int32(10), typ), int32(11), false},
		{"lessOrEqualOrNull/null", lessOrEqualOrNullExpr(int32(10), typ), nil, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rowMatchesRange(ctx, typ, tc.val, tc.expr)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRowMatchesRanges_UnionAcrossRanges(t *testing.T) {
	ctx := newTestContext()
	typ := types.Int32
	ranges := []sqle.Range{
		sqle.MySQLRange{sqle.ClosedRangeColumnExpr(int32(1), int32(1), typ)},
		sqle.MySQLRange{sqle.ClosedRangeColumnExpr(int32(5), int32(5), typ)},
	}

	match, err := rowMatchesRanges(ctx, []sqle.Type{typ}, []int{0}, sqle.Row{int32(5)}, ranges)
	require.NoError(t, err)
	assert.True(t, match)

	match, err = rowMatchesRanges(ctx, []sqle.Type{typ}, []int{0}, sqle.Row{int32(3)}, ranges)
	require.NoError(t, err)
	assert.False(t, match)
}

func TestRowMatchesRanges_SkipsNonMySQLRangeAndEmptyRange(t *testing.T) {
	ctx := newTestContext()
	typ := types.Int32
	ranges := []sqle.Range{fakeRange{}, sqle.MySQLRange{}}

	match, err := rowMatchesRanges(ctx, []sqle.Type{typ}, []int{0}, sqle.Row{int32(5)}, ranges)
	require.NoError(t, err)
	assert.False(t, match)
}

// ---------------------------------------------------------------------------
// indexFieldTypeOf
// ---------------------------------------------------------------------------

func TestIndexFieldTypeOf(t *testing.T) {
	tests := []struct {
		name string
		typ  sqle.Type
		want string
	}{
		{"uint64", types.Uint64, "uint64"},
		{"uint32", types.Uint32, "uint64"},
		{"int64", types.Int64, "int64"},
		{"int32", types.Int32, "int64"},
		{"float64", types.Float64, "float64"},
		{"boolean", types.Boolean, "boolean"},
		{"tinyint", types.Int8, "int64"},
		{"varchar", types.MustCreateString(sqltypes.VarChar, 255, sqle.Collation_Default), "string"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, indexFieldTypeOf(tc.typ))
		})
	}
}

// ---------------------------------------------------------------------------
// prefixEnd
// ---------------------------------------------------------------------------

func TestPrefixEnd(t *testing.T) {
	assert.Equal(t, []byte("ac"), prefixEnd([]byte("ab")))
	assert.Nil(t, prefixEnd(nil))
	assert.Nil(t, prefixEnd([]byte{0xFF, 0xFF}))
	assert.Equal(t, []byte{0x01, 0x01}, prefixEnd([]byte{0x01, 0x00}))
}

// ---------------------------------------------------------------------------
// columnOrdinal
// ---------------------------------------------------------------------------

func TestRelationalTable_ColumnOrdinal(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	assert.Equal(t, 0, tbl.columnOrdinal("id"))
	assert.Equal(t, 1, tbl.columnOrdinal("email"))
	assert.Equal(t, -1, tbl.columnOrdinal("does_not_exist"))
}

// ---------------------------------------------------------------------------
// Partition Key()
// ---------------------------------------------------------------------------

func TestRelationalPartition_Key(t *testing.T) {
	p := &relationalPartition{}
	assert.Equal(t, []byte("default"), p.Key())
}

func TestFilteredPartition_Key(t *testing.T) {
	p := &filteredPartition{}
	assert.Equal(t, []byte("filtered"), p.Key())
}

// ---------------------------------------------------------------------------
// Interface compliance
// ---------------------------------------------------------------------------

func TestRelationalTable_ImplementsInterfaces(t *testing.T) {
	var _ sqle.Table = (*RelationalTable)(nil)
	var _ sqle.PrimaryKeyTable = (*RelationalTable)(nil)
	var _ sqle.StatisticsTable = (*RelationalTable)(nil)
	var _ sqle.IndexAddressableTable = (*RelationalTable)(nil)
	var _ sqle.IndexedTable = (*RelationalTable)(nil)
}

// ---------------------------------------------------------------------------
// Test-only stubs
// ---------------------------------------------------------------------------

// fakeSQLIndex is a minimal sql.Index implementation, distinct from
// *qrdbIndex, used to exercise LookupPartitions' defensive type-assertion
// fallback.
type fakeSQLIndex struct{}

func (fakeSQLIndex) ID() string                                                      { return "fake" }
func (fakeSQLIndex) Database() string                                                { return "" }
func (fakeSQLIndex) Table() string                                                   { return "" }
func (fakeSQLIndex) Expressions() []string                                           { return nil }
func (fakeSQLIndex) CoversColumns(_ []string) bool                                   { return false }
func (fakeSQLIndex) IsUnique() bool                                                  { return false }
func (fakeSQLIndex) IsSpatial() bool                                                 { return false }
func (fakeSQLIndex) IsFullText() bool                                                { return false }
func (fakeSQLIndex) IsVector() bool                                                  { return false }
func (fakeSQLIndex) Comment() string                                                 { return "" }
func (fakeSQLIndex) IndexType() string                                               { return "BTREE" }
func (fakeSQLIndex) IsGenerated() bool                                               { return false }
func (fakeSQLIndex) ColumnExpressionTypes(*sqle.Context) []sqle.ColumnExpressionType { return nil }
func (fakeSQLIndex) CanSupport(*sqle.Context, ...sqle.Range) bool                    { return true }
func (fakeSQLIndex) CanSupportOrderBy(sqle.Expression) bool                          { return false }
func (fakeSQLIndex) PrefixLengths() []uint16                                         { return nil }

var _ sqle.Index = fakeSQLIndex{}

// fakeRange is a minimal sql.Range implementation that is not a
// sqle.MySQLRange, used to exercise the defensive type-assertion fallback
// in primaryKeysFromRanges and rowMatchesRanges.
type fakeRange struct{}

func (fakeRange) Equals(context.Context, sqle.Range) (bool, error) { return false, nil }
func (fakeRange) String() string                                   { return "fake" }
func (fakeRange) DebugString(*sqle.Context) string                 { return "fake" }

var _ sqle.Range = fakeRange{}

// incarnationOf returns the incarnation def assigns the index named name --
// its entries' keyspace (service.TableIndex.Incarnation). A def that does not
// list the index is a test bug: refresh it from the catalog first.
func incarnationOf(def *service.TableDef, name string) uint64 {
	for _, idx := range def.Indexes {
		if idx.Name == name {
			return idx.Incarnation
		}
	}
	panic("incarnationOf: " + def.Name + " has no index " + name)
}
