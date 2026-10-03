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

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// mockKVService
// ---------------------------------------------------------------------------

// mockKVService implements service.KVServicer for unit testing.
type mockKVService struct {
	putFn    func(ctx context.Context, key string, value []byte) error
	getFn    func(ctx context.Context, key string) ([]byte, error)
	deleteFn func(ctx context.Context, key string) error
	listFn   func(ctx context.Context, prefix string) ([]string, error)
	scanFn   func(ctx context.Context, prefix string) (map[string][]byte, error)
}

func (m *mockKVService) Put(ctx context.Context, key string, value []byte) error {
	if m.putFn != nil {
		return m.putFn(ctx, key, value)
	}
	return nil
}

// PutIfAbsent reports a successful create by default. This mock delegates to
// injected functions rather than holding state, so there is nothing here for
// the condition to consult.
func (m *mockKVService) PutIfAbsent(_ context.Context, _ string, _ []byte) (bool, error) {
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (m *mockKVService) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	return false, nil
}

func (m *mockKVService) Get(ctx context.Context, key string) ([]byte, error) {
	if m.getFn != nil {
		return m.getFn(ctx, key)
	}
	return nil, &service.KeyNotFoundError{Key: key}
}

func (m *mockKVService) Delete(ctx context.Context, key string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, key)
	}
	return nil
}

func (m *mockKVService) List(ctx context.Context, prefix string) ([]string, error) {
	if m.listFn != nil {
		return m.listFn(ctx, prefix)
	}
	return nil, nil
}

func (m *mockKVService) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	if m.scanFn != nil {
		return m.scanFn(ctx, prefix)
	}
	return nil, nil
}

func (m *mockKVService) GetStale(context.Context, string) ([]byte, error) {
	return nil, nil
}

func (m *mockKVService) ListStale(context.Context, string) ([]string, error) {
	return nil, nil
}

func (m *mockKVService) ScanStale(context.Context, string) (map[string][]byte, error) {
	return nil, nil
}

func (m *mockKVService) PutEntity(context.Context, *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return nil, nil
}

func (m *mockKVService) GetEntity(context.Context, *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return nil, nil
}

func (m *mockKVService) DeleteEntity(context.Context, *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	return nil, nil
}

func (m *mockKVService) ListEntities(context.Context, *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return nil, nil
}

func (m *mockKVService) PutWithMode(_ context.Context, _ string, _ []byte, _ writemode.Mode) error {
	return nil
}

func (m *mockKVService) DeleteWithMode(_ context.Context, _ string, _ writemode.Mode) error {
	return nil
}

func (m *mockKVService) GetWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]byte, error) {
	return nil, nil
}

func (m *mockKVService) ListWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]string, error) {
	return nil, nil
}

func (m *mockKVService) ScanWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return nil, nil
}

func (m *mockKVService) ExistsWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (bool, error) {
	return false, nil
}

// newTestContext creates a sql.Context for testing with a default session.
func newTestContext() *sqle.Context {
	return sqle.NewContext(context.Background())
}

// ---------------------------------------------------------------------------
// KVTable tests
// ---------------------------------------------------------------------------

func TestNewKVTable(t *testing.T) {
	svc := &mockKVService{}
	tbl := NewKVTable(svc, tenant.ComposeNamespace("tenant1", ""), nil)

	require.NotNil(t, tbl)
	assert.Equal(t, "kv", tbl.Name())
	assert.Equal(t, "kv", tbl.String())
}

func TestKVTable_Schema(t *testing.T) {
	tbl := NewKVTable(&mockKVService{}, tenant.ComposeNamespace("system", ""), nil)

	schema := tbl.Schema(newTestContext())

	require.Len(t, schema, 2)
	assert.Equal(t, "key", schema[0].Name)
	assert.True(t, schema[0].PrimaryKey)
	assert.False(t, schema[0].Nullable)
	assert.Equal(t, "kv", schema[0].Source)
	assert.Equal(t, "value", schema[1].Name)
	assert.False(t, schema[1].Nullable)
	assert.Equal(t, "kv", schema[1].Source)
}

func TestKVTable_Collation(t *testing.T) {
	tbl := NewKVTable(&mockKVService{}, tenant.ComposeNamespace("system", ""), nil)
	assert.Equal(t, sqle.Collation_Default, tbl.Collation())
}

func TestKVTable_Partitions(t *testing.T) {
	tbl := NewKVTable(&mockKVService{}, tenant.ComposeNamespace("system", ""), nil)
	ctx := newTestContext()

	iter, err := tbl.Partitions(ctx)
	require.NoError(t, err)

	p, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("default"), p.Key())

	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

func TestKVTable_PartitionRows_Empty(t *testing.T) {
	svc := &mockKVService{
		scanFn: func(_ context.Context, prefix string) (map[string][]byte, error) {
			assert.Equal(t, "tenant1/system/", prefix)
			return nil, nil
		},
	}
	tbl := NewKVTable(svc, tenant.ComposeNamespace("tenant1", ""), nil)
	ctx := newTestContext()

	iter, err := tbl.PartitionRows(ctx, &kvPartition{})
	require.NoError(t, err)

	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

func TestKVTable_PartitionRows_WithData(t *testing.T) {
	svc := &mockKVService{
		scanFn: func(_ context.Context, prefix string) (map[string][]byte, error) {
			return map[string][]byte{
				"tenant1/system/alpha": []byte("val-a"),
				"tenant1/system/beta":  []byte("val-b"),
				"tenant1/system/gamma": []byte("val-g"),
			}, nil
		},
	}
	tbl := NewKVTable(svc, tenant.ComposeNamespace("tenant1", ""), nil)
	ctx := newTestContext()

	iter, err := tbl.PartitionRows(ctx, &kvPartition{})
	require.NoError(t, err)

	// Rows should be sorted by key and have tenant prefix stripped.
	row1, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, sqle.Row{"alpha", []byte("val-a")}, row1)

	row2, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, sqle.Row{"beta", []byte("val-b")}, row2)

	row3, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, sqle.Row{"gamma", []byte("val-g")}, row3)

	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

func TestKVTable_PartitionRows_ScanError(t *testing.T) {
	scanErr := errors.New("scan failed")
	svc := &mockKVService{
		scanFn: func(_ context.Context, _ string) (map[string][]byte, error) {
			return nil, scanErr
		},
	}
	tbl := NewKVTable(svc, tenant.ComposeNamespace("system", ""), nil)
	ctx := newTestContext()

	_, err := tbl.PartitionRows(ctx, &kvPartition{})
	assert.ErrorIs(t, err, scanErr)
}

// ---------------------------------------------------------------------------
// Inserter tests
// ---------------------------------------------------------------------------

func TestKVTable_Insert(t *testing.T) {
	var putKey string
	var putValue []byte
	svc := &mockKVService{
		putFn: func(_ context.Context, key string, value []byte) error {
			putKey = key
			putValue = value
			return nil
		},
	}
	tbl := NewKVTable(svc, tenant.ComposeNamespace("tenant1", ""), nil)
	ctx := newTestContext()

	inserter := tbl.Inserter(ctx)
	require.NotNil(t, inserter)

	inserter.StatementBegin(ctx)
	err := inserter.Insert(ctx, sqle.Row{"mykey", []byte("myvalue")})
	require.NoError(t, err)
	require.NoError(t, inserter.StatementComplete(ctx))
	require.NoError(t, inserter.Close(ctx))

	assert.Equal(t, "tenant1/system/mykey", putKey)
	assert.Equal(t, []byte("myvalue"), putValue)
}

func TestKVTable_Insert_StringValue(t *testing.T) {
	var putValue []byte
	svc := &mockKVService{
		putFn: func(_ context.Context, _ string, value []byte) error {
			putValue = value
			return nil
		},
	}
	tbl := NewKVTable(svc, tenant.ComposeNamespace("system", ""), nil)
	ctx := newTestContext()

	inserter := tbl.Inserter(ctx)
	err := inserter.Insert(ctx, sqle.Row{"key1", "string-value"})
	require.NoError(t, err)
	assert.Equal(t, []byte("string-value"), putValue)
}

func TestKVTable_Insert_EmptyKey(t *testing.T) {
	tbl := NewKVTable(&mockKVService{}, tenant.ComposeNamespace("system", ""), nil)
	ctx := newTestContext()

	inserter := tbl.Inserter(ctx)
	err := inserter.Insert(ctx, sqle.Row{"", []byte("value")})
	assert.IsType(t, &InvalidKeyError{}, err)
}

func TestKVTable_Insert_NilValue(t *testing.T) {
	tbl := NewKVTable(&mockKVService{}, tenant.ComposeNamespace("system", ""), nil)
	ctx := newTestContext()

	inserter := tbl.Inserter(ctx)
	err := inserter.Insert(ctx, sqle.Row{"key1", nil})
	assert.IsType(t, &InvalidValueError{}, err)
}

func TestKVTable_Insert_MissingValueColumn(t *testing.T) {
	tbl := NewKVTable(&mockKVService{}, tenant.ComposeNamespace("system", ""), nil)
	ctx := newTestContext()

	inserter := tbl.Inserter(ctx)
	err := inserter.Insert(ctx, sqle.Row{"key1"})
	assert.IsType(t, &InvalidValueError{}, err)
}

func TestKVTable_Insert_EmptyRow(t *testing.T) {
	tbl := NewKVTable(&mockKVService{}, tenant.ComposeNamespace("system", ""), nil)
	ctx := newTestContext()

	inserter := tbl.Inserter(ctx)
	err := inserter.Insert(ctx, sqle.Row{})
	assert.IsType(t, &InvalidKeyError{}, err)
}

func TestKVTable_Insert_NonStringKey(t *testing.T) {
	tbl := NewKVTable(&mockKVService{}, tenant.ComposeNamespace("system", ""), nil)
	ctx := newTestContext()

	inserter := tbl.Inserter(ctx)
	err := inserter.Insert(ctx, sqle.Row{42, []byte("value")})
	assert.IsType(t, &InvalidKeyError{}, err)
}

func TestKVTable_Insert_PutError(t *testing.T) {
	putErr := errors.New("put failed")
	svc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return putErr
		},
	}
	tbl := NewKVTable(svc, tenant.ComposeNamespace("system", ""), nil)
	ctx := newTestContext()

	inserter := tbl.Inserter(ctx)
	err := inserter.Insert(ctx, sqle.Row{"key1", []byte("value")})
	assert.ErrorIs(t, err, putErr)
}

// ---------------------------------------------------------------------------
// Deleter tests
// ---------------------------------------------------------------------------

func TestKVTable_Delete(t *testing.T) {
	var deletedKey string
	svc := &mockKVService{
		deleteFn: func(_ context.Context, key string) error {
			deletedKey = key
			return nil
		},
	}
	tbl := NewKVTable(svc, tenant.ComposeNamespace("tenant2", ""), nil)
	ctx := newTestContext()

	deleter := tbl.Deleter(ctx)
	require.NotNil(t, deleter)

	err := deleter.Delete(ctx, sqle.Row{"thekey", []byte("theval")})
	require.NoError(t, err)
	require.NoError(t, deleter.Close(ctx))

	assert.Equal(t, "tenant2/system/thekey", deletedKey)
}

func TestKVTable_Delete_EmptyKey(t *testing.T) {
	tbl := NewKVTable(&mockKVService{}, tenant.ComposeNamespace("system", ""), nil)
	ctx := newTestContext()

	deleter := tbl.Deleter(ctx)
	err := deleter.Delete(ctx, sqle.Row{"", []byte("val")})
	assert.IsType(t, &InvalidKeyError{}, err)
}

func TestKVTable_Delete_DeleteError(t *testing.T) {
	deleteErr := errors.New("delete failed")
	svc := &mockKVService{
		deleteFn: func(_ context.Context, _ string) error {
			return deleteErr
		},
	}
	tbl := NewKVTable(svc, tenant.ComposeNamespace("system", ""), nil)
	ctx := newTestContext()

	deleter := tbl.Deleter(ctx)
	err := deleter.Delete(ctx, sqle.Row{"key1", []byte("val")})
	assert.ErrorIs(t, err, deleteErr)
}

// ---------------------------------------------------------------------------
// Updater tests
// ---------------------------------------------------------------------------

func TestKVTable_Update_SameKey(t *testing.T) {
	var putKey string
	var putValue []byte
	deleteCalled := false
	svc := &mockKVService{
		putFn: func(_ context.Context, key string, value []byte) error {
			putKey = key
			putValue = value
			return nil
		},
		deleteFn: func(_ context.Context, _ string) error {
			deleteCalled = true
			return nil
		},
	}
	tbl := NewKVTable(svc, tenant.ComposeNamespace("t1", ""), nil)
	ctx := newTestContext()

	updater := tbl.Updater(ctx)
	err := updater.Update(ctx,
		sqle.Row{"mykey", []byte("old-val")},
		sqle.Row{"mykey", []byte("new-val")},
	)
	require.NoError(t, err)

	assert.Equal(t, "t1/system/mykey", putKey)
	assert.Equal(t, []byte("new-val"), putValue)
	assert.False(t, deleteCalled, "delete should not be called when key unchanged")
}

func TestKVTable_Update_KeyChanged(t *testing.T) {
	var putKey string
	var deletedKey string
	svc := &mockKVService{
		putFn: func(_ context.Context, key string, _ []byte) error {
			putKey = key
			return nil
		},
		deleteFn: func(_ context.Context, key string) error {
			deletedKey = key
			return nil
		},
	}
	tbl := NewKVTable(svc, tenant.ComposeNamespace("t1", ""), nil)
	ctx := newTestContext()

	updater := tbl.Updater(ctx)
	err := updater.Update(ctx,
		sqle.Row{"old-key", []byte("val")},
		sqle.Row{"new-key", []byte("val")},
	)
	require.NoError(t, err)

	assert.Equal(t, "t1/system/new-key", putKey)
	assert.Equal(t, "t1/system/old-key", deletedKey)
}

func TestKVTable_Update_PutError(t *testing.T) {
	putErr := errors.New("put failed")
	svc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return putErr
		},
	}
	tbl := NewKVTable(svc, tenant.ComposeNamespace("system", ""), nil)
	ctx := newTestContext()

	updater := tbl.Updater(ctx)
	err := updater.Update(ctx,
		sqle.Row{"key1", []byte("old")},
		sqle.Row{"key1", []byte("new")},
	)
	assert.ErrorIs(t, err, putErr)
}

func TestKVTable_Update_DeleteError(t *testing.T) {
	deleteErr := errors.New("delete failed")
	svc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return nil
		},
		deleteFn: func(_ context.Context, _ string) error {
			return deleteErr
		},
	}
	tbl := NewKVTable(svc, tenant.ComposeNamespace("system", ""), nil)
	ctx := newTestContext()

	updater := tbl.Updater(ctx)
	err := updater.Update(ctx,
		sqle.Row{"old-key", []byte("val")},
		sqle.Row{"new-key", []byte("val")},
	)
	assert.ErrorIs(t, err, deleteErr)
}

func TestKVTable_Update_InvalidOldKey(t *testing.T) {
	tbl := NewKVTable(&mockKVService{}, tenant.ComposeNamespace("system", ""), nil)
	ctx := newTestContext()

	updater := tbl.Updater(ctx)
	err := updater.Update(ctx,
		sqle.Row{"", []byte("val")},
		sqle.Row{"new-key", []byte("val")},
	)
	assert.IsType(t, &InvalidKeyError{}, err)
}

func TestKVTable_Update_InvalidNewKey(t *testing.T) {
	tbl := NewKVTable(&mockKVService{}, tenant.ComposeNamespace("system", ""), nil)
	ctx := newTestContext()

	updater := tbl.Updater(ctx)
	err := updater.Update(ctx,
		sqle.Row{"old-key", []byte("val")},
		sqle.Row{"", []byte("val")},
	)
	assert.IsType(t, &InvalidKeyError{}, err)
}

// ---------------------------------------------------------------------------
// Replacer tests
// ---------------------------------------------------------------------------

func TestKVTable_Replace(t *testing.T) {
	var putKey string
	var putValue []byte
	svc := &mockKVService{
		putFn: func(_ context.Context, key string, value []byte) error {
			putKey = key
			putValue = value
			return nil
		},
	}
	tbl := NewKVTable(svc, tenant.ComposeNamespace("tenant1", ""), nil)
	ctx := newTestContext()

	replacer := tbl.Replacer(ctx)
	require.NotNil(t, replacer)

	err := replacer.Insert(ctx, sqle.Row{"rkey", []byte("rval")})
	require.NoError(t, err)

	assert.Equal(t, "tenant1/system/rkey", putKey)
	assert.Equal(t, []byte("rval"), putValue)
}

func TestKVTable_Replace_InvalidRow(t *testing.T) {
	tbl := NewKVTable(&mockKVService{}, tenant.ComposeNamespace("system", ""), nil)
	ctx := newTestContext()

	replacer := tbl.Replacer(ctx)
	err := replacer.Insert(ctx, sqle.Row{})
	assert.IsType(t, &InvalidKeyError{}, err)
}

// ---------------------------------------------------------------------------
// kvWriter lifecycle tests
// ---------------------------------------------------------------------------

func TestKVWriter_StatementBegin_NoError(t *testing.T) {
	w := &kvWriter{kvSvc: &mockKVService{}, prefix: tenant.ComposeNamespace("system", "")}
	ctx := newTestContext()

	// Should not panic.
	w.StatementBegin(ctx)
}

func TestKVWriter_DiscardChanges_NoError(t *testing.T) {
	w := &kvWriter{kvSvc: &mockKVService{}, prefix: tenant.ComposeNamespace("system", "")}
	ctx := newTestContext()

	err := w.DiscardChanges(ctx, errors.New("some error"))
	assert.NoError(t, err)
}

func TestKVWriter_StatementComplete_NoError(t *testing.T) {
	w := &kvWriter{kvSvc: &mockKVService{}, prefix: tenant.ComposeNamespace("system", "")}
	ctx := newTestContext()

	err := w.StatementComplete(ctx)
	assert.NoError(t, err)
}

func TestKVWriter_Close_NoError(t *testing.T) {
	w := &kvWriter{kvSvc: &mockKVService{}, prefix: tenant.ComposeNamespace("system", "")}
	ctx := newTestContext()

	err := w.Close(ctx)
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// coerceValue tests
// ---------------------------------------------------------------------------

func TestCoerceValue_ByteSlice(t *testing.T) {
	val, err := coerceValue([]byte("hello"))
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), val)
}

func TestCoerceValue_String(t *testing.T) {
	val, err := coerceValue("hello")
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), val)
}

func TestCoerceValue_Nil(t *testing.T) {
	_, err := coerceValue(nil)
	assert.IsType(t, &InvalidValueError{}, err)
}

func TestCoerceValue_UnsupportedType(t *testing.T) {
	_, err := coerceValue(42)
	assert.IsType(t, &InvalidValueError{}, err)
}

// ---------------------------------------------------------------------------
// extractKey tests
// ---------------------------------------------------------------------------

func TestExtractKey_Valid(t *testing.T) {
	key, err := extractKey(sqle.Row{"mykey", []byte("val")})
	require.NoError(t, err)
	assert.Equal(t, "mykey", key)
}

func TestExtractKey_EmptyRow(t *testing.T) {
	_, err := extractKey(sqle.Row{})
	assert.IsType(t, &InvalidKeyError{}, err)
}

func TestExtractKey_NonStringKey(t *testing.T) {
	_, err := extractKey(sqle.Row{123})
	assert.IsType(t, &InvalidKeyError{}, err)
}

func TestExtractKey_EmptyString(t *testing.T) {
	_, err := extractKey(sqle.Row{""})
	assert.IsType(t, &InvalidKeyError{}, err)
}

// ---------------------------------------------------------------------------
// extractKeyValue tests
// ---------------------------------------------------------------------------

func TestExtractKeyValue_Valid(t *testing.T) {
	key, val, err := extractKeyValue(sqle.Row{"k", []byte("v")})
	require.NoError(t, err)
	assert.Equal(t, "k", key)
	assert.Equal(t, []byte("v"), val)
}

func TestExtractKeyValue_MissingValue(t *testing.T) {
	_, _, err := extractKeyValue(sqle.Row{"k"})
	assert.IsType(t, &InvalidValueError{}, err)
}

func TestExtractKeyValue_InvalidKey(t *testing.T) {
	_, _, err := extractKeyValue(sqle.Row{})
	assert.IsType(t, &InvalidKeyError{}, err)
}

// ---------------------------------------------------------------------------
// kvPartition tests
// ---------------------------------------------------------------------------

func TestKVPartition_Key(t *testing.T) {
	p := &kvPartition{}
	assert.Equal(t, []byte("default"), p.Key())
}

// ---------------------------------------------------------------------------
// kvRowIter tests
// ---------------------------------------------------------------------------

func TestKVRowIter_EmptyRows(t *testing.T) {
	iter := &kvRowIter{rows: nil}
	ctx := newTestContext()

	_, err := iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

func TestKVRowIter_IteratesAll(t *testing.T) {
	rows := []sqle.Row{
		{"a", []byte("1")},
		{"b", []byte("2")},
	}
	iter := &kvRowIter{rows: rows}
	ctx := newTestContext()

	r1, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, rows[0], r1)

	r2, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, rows[1], r2)

	_, err = iter.Next(ctx)
	assert.ErrorIs(t, err, io.EOF)
}

func TestKVRowIter_Close(t *testing.T) {
	iter := &kvRowIter{rows: []sqle.Row{{"a", []byte("1")}}}
	ctx := newTestContext()

	err := iter.Close(ctx)
	require.NoError(t, err)
	assert.Nil(t, iter.rows)
}

// ---------------------------------------------------------------------------
// Interface compliance tests
// ---------------------------------------------------------------------------

func TestKVTable_ImplementsInsertableTable(t *testing.T) {
	var _ sqle.InsertableTable = (*KVTable)(nil)
}

func TestKVTable_ImplementsDeletableTable(t *testing.T) {
	var _ sqle.DeletableTable = (*KVTable)(nil)
}

func TestKVTable_ImplementsUpdatableTable(t *testing.T) {
	var _ sqle.UpdatableTable = (*KVTable)(nil)
}

func TestKVTable_ImplementsReplaceableTable(t *testing.T) {
	var _ sqle.ReplaceableTable = (*KVTable)(nil)
}

func TestKVWriter_ImplementsRowReplacer(t *testing.T) {
	var _ sqle.RowReplacer = (*kvWriter)(nil)
}

// ---------------------------------------------------------------------------
// Insert with unsupported value type
// ---------------------------------------------------------------------------

func TestKVTable_Insert_UnsupportedValueType(t *testing.T) {
	tbl := NewKVTable(&mockKVService{}, tenant.ComposeNamespace("system", ""), nil)
	ctx := newTestContext()

	inserter := tbl.Inserter(ctx)
	err := inserter.Insert(ctx, sqle.Row{"key1", 12345})
	assert.IsType(t, &InvalidValueError{}, err)
}

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
func (*mockKVService) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}

// ListAll mirrors List on this double: it applies no internal-key guard,
// so the filtered and unfiltered enumerations are the same call.
func (m *mockKVService) ListAll(ctx context.Context, prefix string) ([]string, error) {
	return m.List(ctx, prefix)
}
