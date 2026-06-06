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

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// mockIndexServicer implements index.IndexServicer for unit testing.
// ---------------------------------------------------------------------------

type mockIndexServicer struct {
	listFn  func(ctx context.Context) ([]string, error)
	queryFn func(ctx context.Context, indexName string, value []byte) ([][]byte, error)
	scanFn  func(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error)
}

func (m *mockIndexServicer) CreateIndex(_ context.Context, _, _ string, _ index.IndexConfig) error {
	return nil
}

func (m *mockIndexServicer) DropIndex(_ context.Context, _ string) error {
	return nil
}

func (m *mockIndexServicer) Query(ctx context.Context, indexName string, value []byte) ([][]byte, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, indexName, value)
	}
	return nil, nil
}

func (m *mockIndexServicer) Scan(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	if m.scanFn != nil {
		return m.scanFn(ctx, indexName, start, end)
	}
	return nil, nil
}

func (m *mockIndexServicer) ListIndexes(ctx context.Context) ([]string, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// indexedPartition
// ---------------------------------------------------------------------------

func TestIndexedPartition_Key(t *testing.T) {
	p := &indexedPartition{keys: [][]byte{[]byte("k1")}}
	assert.Equal(t, []byte("indexed"), p.Key())
}

func TestIndexedPartition_EmptyKeys(t *testing.T) {
	p := &indexedPartition{}
	assert.Equal(t, []byte("indexed"), p.Key())
}

// ---------------------------------------------------------------------------
// indexedRowIter
// ---------------------------------------------------------------------------

func TestIndexedRowIter_Next(t *testing.T) {
	ctx := sqle.NewContext(context.Background())
	iter := &indexedRowIter{
		rows: []sqle.Row{
			{1, "a"},
			{2, "b"},
		},
	}

	r1, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, sqle.Row{1, "a"}, r1)

	r2, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, sqle.Row{2, "b"}, r2)

	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

func TestIndexedRowIter_Close(t *testing.T) {
	ctx := sqle.NewContext(context.Background())
	iter := &indexedRowIter{rows: []sqle.Row{{1, "x"}}}
	require.NoError(t, iter.Close(ctx))
	// After close, rows are nil and Next returns EOF immediately.
	_, err := iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

// ---------------------------------------------------------------------------
// KVTable.GetIndexes
// ---------------------------------------------------------------------------

func TestKVTable_GetIndexes_NilService(t *testing.T) {
	tbl := NewKVTable(&mockKVService{}, "system", nil)
	ctx := newTestContext()

	indexes, err := tbl.GetIndexes(ctx)
	require.NoError(t, err)
	assert.Nil(t, indexes)
}

func TestKVTable_GetIndexes_WithService(t *testing.T) {
	svc := &mockIndexServicer{
		listFn: func(_ context.Context) ([]string, error) {
			return []string{"Email", "Name"}, nil
		},
	}
	tbl := NewKVTable(&mockKVService{}, "system", svc)
	ctx := newTestContext()

	indexes, err := tbl.GetIndexes(ctx)
	require.NoError(t, err)
	require.Len(t, indexes, 2)
	// Both indexes should be qrdbIndex instances targeting the "kv" table.
	qi0 := indexes[0].(*qrdbIndex)
	qi1 := indexes[1].(*qrdbIndex)
	assert.Equal(t, tableName, qi0.table)
	assert.Equal(t, tableName, qi1.table)
	assert.Equal(t, "Email", qi0.column)
	assert.Equal(t, "Name", qi1.column)
}

func TestKVTable_GetIndexes_ListError(t *testing.T) {
	svc := &mockIndexServicer{
		listFn: func(_ context.Context) ([]string, error) {
			return nil, errors.New("storage unavailable")
		},
	}
	tbl := NewKVTable(&mockKVService{}, "system", svc)
	ctx := newTestContext()

	_, err := tbl.GetIndexes(ctx)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// KVTable.PreciseMatch
// ---------------------------------------------------------------------------

func TestKVTable_PreciseMatch(t *testing.T) {
	tbl := NewKVTable(&mockKVService{}, "system", nil)
	assert.True(t, tbl.PreciseMatch())
}

// ---------------------------------------------------------------------------
// KVTable.IndexedAccess
// ---------------------------------------------------------------------------

func TestKVTable_IndexedAccess(t *testing.T) {
	tbl := NewKVTable(&mockKVService{}, "system", nil)
	ctx := newTestContext()
	// IndexedAccess returns the table itself since KVTable implements IndexedTable.
	result := tbl.IndexedAccess(ctx, sqle.IndexLookup{})
	assert.Equal(t, tbl, result)
}

// ---------------------------------------------------------------------------
// KVTable.LookupPartitions
// ---------------------------------------------------------------------------

func TestKVTable_LookupPartitions_NilService_FallsToFullScan(t *testing.T) {
	tbl := NewKVTable(&mockKVService{}, "system", nil)
	ctx := newTestContext()

	iter, err := tbl.LookupPartitions(ctx, sqle.IndexLookup{})
	require.NoError(t, err)
	require.NotNil(t, iter)
}

func TestKVTable_LookupPartitions_EmptyLookup_FallsToFullScan(t *testing.T) {
	svc := &mockIndexServicer{}
	tbl := NewKVTable(&mockKVService{}, "system", svc)
	ctx := newTestContext()

	// Empty IndexLookup (no index set) falls back to full scan.
	iter, err := tbl.LookupPartitions(ctx, sqle.IndexLookup{})
	require.NoError(t, err)
	require.NotNil(t, iter)
}

func TestKVTable_LookupPartitions_NonQRDBIndex_FallsToFullScan(t *testing.T) {
	svc := &mockIndexServicer{}
	tbl := NewKVTable(&mockKVService{}, "system", svc)
	ctx := newTestContext()

	// An IndexLookup with a non-qrdbIndex falls back to full scan.
	lookup := sqle.IndexLookup{Index: &externalIndex{}}
	iter, err := tbl.LookupPartitions(ctx, lookup)
	require.NoError(t, err)
	require.NotNil(t, iter)
}

// ---------------------------------------------------------------------------
// KVTable indexed partition row fetch
// ---------------------------------------------------------------------------

func TestKVTable_PartitionRows_IndexedPartition(t *testing.T) {
	// PrefixKey("t1", "key1") = "t1/key1"
	kvSvc := &mockKVService{
		getFn: func(_ context.Context, key string) ([]byte, error) {
			if key == "t1/key1" {
				return []byte("value1"), nil
			}
			return nil, &service.KeyNotFoundError{Key: key}
		},
	}
	tbl := NewKVTable(kvSvc, "t1", nil)
	ctx := newTestContext()

	// indexedPartition with two keys: one found, one missing.
	part := &indexedPartition{
		keys: [][]byte{[]byte("key1"), []byte("missing")},
	}
	iter, err := tbl.PartitionRows(ctx, part)
	require.NoError(t, err)

	row, err := iter.Next(ctx)
	require.NoError(t, err)
	// StripPrefix("t1", "t1/key1") = "key1"
	assert.Equal(t, "key1", row[0])
	assert.Equal(t, []byte("value1"), row[1])

	// Missing key is skipped.
	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

// ---------------------------------------------------------------------------
// CollectionTable.GetIndexes
// ---------------------------------------------------------------------------

func TestCollectionTable_GetIndexes_NilService(t *testing.T) {
	tbl := NewCollectionTable(&mockCollectionService{}, "db", "users", nil, nil)
	ctx := newTestContext()

	indexes, err := tbl.GetIndexes(ctx)
	require.NoError(t, err)
	assert.Nil(t, indexes)
}

func TestCollectionTable_GetIndexes_WithService(t *testing.T) {
	svc := &mockIndexServicer{
		listFn: func(_ context.Context) ([]string, error) {
			return []string{"Email"}, nil
		},
	}
	tbl := NewCollectionTable(&mockCollectionService{}, "mydb", "users", nil, svc)
	ctx := newTestContext()

	indexes, err := tbl.GetIndexes(ctx)
	require.NoError(t, err)
	require.Len(t, indexes, 1)
	qi := indexes[0].(*qrdbIndex)
	assert.Equal(t, "mydb.users.Email", qi.ID())
}

func TestCollectionTable_GetIndexes_ListError(t *testing.T) {
	svc := &mockIndexServicer{
		listFn: func(_ context.Context) ([]string, error) {
			return nil, errors.New("index error")
		},
	}
	tbl := NewCollectionTable(&mockCollectionService{}, "db", "users", nil, svc)
	ctx := newTestContext()

	_, err := tbl.GetIndexes(ctx)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// CollectionTable.PreciseMatch
// ---------------------------------------------------------------------------

func TestCollectionTable_PreciseMatch(t *testing.T) {
	tbl := NewCollectionTable(&mockCollectionService{}, "db", "users", nil, nil)
	assert.True(t, tbl.PreciseMatch())
}

// ---------------------------------------------------------------------------
// CollectionTable.IndexedAccess
// ---------------------------------------------------------------------------

func TestCollectionTable_IndexedAccess(t *testing.T) {
	tbl := NewCollectionTable(&mockCollectionService{}, "db", "users", nil, nil)
	ctx := newTestContext()
	result := tbl.IndexedAccess(ctx, sqle.IndexLookup{})
	assert.Equal(t, tbl, result)
}

// ---------------------------------------------------------------------------
// CollectionTable.LookupPartitions
// ---------------------------------------------------------------------------

func TestCollectionTable_LookupPartitions_NilService(t *testing.T) {
	tbl := NewCollectionTable(&mockCollectionService{}, "db", "users", nil, nil)
	ctx := newTestContext()

	iter, err := tbl.LookupPartitions(ctx, sqle.IndexLookup{})
	require.NoError(t, err)
	require.NotNil(t, iter)
}

func TestCollectionTable_LookupPartitions_EmptyLookup(t *testing.T) {
	svc := &mockIndexServicer{}
	tbl := NewCollectionTable(&mockCollectionService{}, "db", "users", nil, svc)
	ctx := newTestContext()

	iter, err := tbl.LookupPartitions(ctx, sqle.IndexLookup{})
	require.NoError(t, err)
	require.NotNil(t, iter)
}

func TestCollectionTable_LookupPartitions_NonQRDBIndex(t *testing.T) {
	svc := &mockIndexServicer{}
	tbl := NewCollectionTable(&mockCollectionService{}, "db", "users", nil, svc)
	ctx := newTestContext()

	lookup := sqle.IndexLookup{Index: &externalIndex{}}
	iter, err := tbl.LookupPartitions(ctx, lookup)
	require.NoError(t, err)
	require.NotNil(t, iter)
}

// ---------------------------------------------------------------------------
// CollectionTable indexed partition row fetch
// ---------------------------------------------------------------------------

func TestCollectionTable_PartitionRows_IndexedPartition(t *testing.T) {
	collSvc := &mockCollectionService{
		getFn: func(_ context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
			if req.ID == 42 {
				return &service.GetDocumentResponse{
					ID:       42,
					Document: map[string]interface{}{"name": "Alice"},
				}, nil
			}
			return nil, errors.New("not found")
		},
	}
	tbl := NewCollectionTable(collSvc, "db", "users", map[string]string{"name": "VARCHAR"}, nil)
	ctx := newTestContext()

	// Keys: "42" (valid), "99" (missing), "bad" (non-parseable).
	part := &indexedPartition{
		keys: [][]byte{[]byte("42"), []byte("99"), []byte("bad")},
	}
	iter, err := tbl.PartitionRows(ctx, part)
	require.NoError(t, err)

	row, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(42), row[0])

	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

// ---------------------------------------------------------------------------
// Provider.WithIndexService functional option
// ---------------------------------------------------------------------------

func TestProvider_WithIndexService(t *testing.T) {
	svc := &mockIndexServicer{}
	prov := NewProvider("mydb", &mockKVService{})
	WithIndexService(svc)(prov)
	assert.Equal(t, svc, prov.indexSvc)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// externalIndex is a test-only sql.Index that is NOT a *qrdbIndex,
// used to verify that non-QRDB indexes fall back to full scans.
type externalIndex struct{}

func (e *externalIndex) ID() string                                         { return "external" }
func (e *externalIndex) Database() string                                   { return "db" }
func (e *externalIndex) Table() string                                      { return "t" }
func (e *externalIndex) Expressions() []string                              { return nil }
func (e *externalIndex) IsUnique() bool                                     { return false }
func (e *externalIndex) IsSpatial() bool                                    { return false }
func (e *externalIndex) IsFullText() bool                                   { return false }
func (e *externalIndex) IsVector() bool                                     { return false }
func (e *externalIndex) Comment() string                                    { return "" }
func (e *externalIndex) IndexType() string                                  { return "BTREE" }
func (e *externalIndex) IsGenerated() bool                                  { return false }
func (e *externalIndex) ColumnExpressionTypes() []sqle.ColumnExpressionType { return nil }
func (e *externalIndex) CanSupport(_ *sqle.Context, _ ...sqle.Range) bool   { return false }
func (e *externalIndex) CanSupportOrderBy(_ sqle.Expression) bool           { return false }
func (e *externalIndex) PrefixLengths() []uint16                            { return nil }
