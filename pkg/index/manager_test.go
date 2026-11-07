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

package index

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// mockBatch is a minimal mock batch implementation.
type mockBatch struct{}

func (m *mockBatch) Set(key, value []byte) error         { return nil }
func (m *mockBatch) Delete(key []byte) error             { return nil }
func (m *mockBatch) DeleteRange(start, end []byte) error { return nil }
func (m *mockBatch) Get(key []byte) ([]byte, error)      { return nil, nil }
func (m *mockBatch) Commit() error                       { return nil }
func (m *mockBatch) Rollback() error                     { return nil }
func (m *mockBatch) Close() error                        { return nil }
func (m *mockBatch) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return &mockIterator{}
}

// mockIterator is a minimal mock iterator implementation.
type mockIterator struct{}

func (m *mockIterator) SeekToFirst() bool       { return false }
func (m *mockIterator) SeekToLast() bool        { return false }
func (m *mockIterator) Seek(target []byte) bool { return false }
func (m *mockIterator) Next() bool              { return false }
func (m *mockIterator) Prev() bool              { return false }
func (m *mockIterator) Valid() bool             { return false }
func (m *mockIterator) Key() []byte             { return nil }
func (m *mockIterator) Value() []byte           { return nil }
func (m *mockIterator) Error() error            { return nil }
func (m *mockIterator) Close() error            { return nil }

// mockSnapshot is a minimal mock snapshot implementation.
type mockSnapshot struct{}

func (m *mockSnapshot) Get(key []byte) ([]byte, error) {
	return nil, &dberrors.MessageError{Message: "not found"}
}
func (m *mockSnapshot) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return &mockIterator{}
}
func (m *mockSnapshot) Close() error { return nil }

// mockStorage is a minimal mock storage implementation for unit tests.
type mockStorage struct {
	data map[string][]byte
}

func newMockStorage() *mockStorage {
	return &mockStorage{
		data: make(map[string][]byte),
	}
}

func (m *mockStorage) Get(key []byte) ([]byte, error) {
	val, ok := m.data[string(key)]
	if !ok {
		return nil, &dberrors.MessageError{Message: "not found"}
	}
	return val, nil
}

func (m *mockStorage) Set(key, value []byte) error {
	m.data[string(key)] = value
	return nil
}

func (m *mockStorage) Delete(key []byte) error {
	delete(m.data, string(key))
	return nil
}

func (m *mockStorage) DeleteRange(start, end []byte) error {
	if err := storage.ValidateRange(start, end); err != nil {
		return err
	}
	for k := range m.data {
		if inDeleteRange([]byte(k), start, end) {
			delete(m.data, k)
		}
	}
	return nil
}

// inDeleteRange reports whether key falls in [start, end); a nil end runs to
// the end of the keyspace, as storage.Storage.DeleteRange defines it.
func inDeleteRange(key, start, end []byte) bool {
	return bytes.Compare(key, start) >= 0 && (len(end) == 0 || bytes.Compare(key, end) < 0)
}

func (m *mockStorage) Close() error {
	return nil
}

func (m *mockStorage) Batch() storage.Batch {
	return &mockBatch{}
}

func (m *mockStorage) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return &mockIterator{}
}

func (m *mockStorage) List(prefix []byte) ([][]byte, error) {
	var keys [][]byte
	for k := range m.data {
		if len(prefix) == 0 || len(k) >= len(prefix) && k[:len(prefix)] == string(prefix) {
			keys = append(keys, []byte(k))
		}
	}
	return keys, nil
}

func (m *mockStorage) Exists(key []byte) (bool, error) {
	_, ok := m.data[string(key)]
	return ok, nil
}

func (m *mockStorage) Snapshot() (storage.Snapshot, error) {
	return &mockSnapshot{}, nil
}

// TestEntity for unit tests.
type TestEntity struct {
	ID    uint64 `index:"unique"`
	Name  string `index:"true"`
	Email string `index:"unique,case_insensitive"`
}

func setupUnitTestManager(t *testing.T) (*IndexManager, func()) {
	t.Helper()

	log, err := logger.New(logger.LogConfig{
		Level:  "error",
		Format: "json",
		Output: "stdout",
	})
	require.NoError(t, err)

	// Use memory index for fast unit tests
	mgr := &IndexManager{
		storage:  newMockStorage(),
		logger:   log.WithField("component", "index-manager-test"),
		indexes:  make(map[string]Index),
		metadata: make(map[string]*IndexMetadata),
	}

	cleanup := func() {
		_ = mgr.Close()
	}

	return mgr, cleanup
}

func TestNewIndexManager_NilStorage(t *testing.T) {
	log, err := logger.New(logger.LogConfig{
		Level:  "error",
		Format: "json",
		Output: "stdout",
	})
	require.NoError(t, err)

	mgr, err := NewIndexManager(nil, log)
	assert.Error(t, err)
	assert.Nil(t, mgr)
	assert.Contains(t, err.Error(), "storage is required")

	var qrdbErr *dberrors.QRDBError
	require.ErrorAs(t, err, &qrdbErr)
	assert.Equal(t, dberrors.ErrInvalidInput, qrdbErr.Code)
}

func TestNewIndexManager_NilLogger(t *testing.T) {
	store := newMockStorage()

	mgr, err := NewIndexManager(store, nil)
	assert.Error(t, err)
	assert.Nil(t, mgr)
	assert.Contains(t, err.Error(), "logger is required")

	var qrdbErr *dberrors.QRDBError
	require.ErrorAs(t, err, &qrdbErr)
	assert.Equal(t, dberrors.ErrInvalidInput, qrdbErr.Code)
}

func TestIndexManager_DeleteIndexes_Unit(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	entityType := reflect.TypeOf(TestEntity{})
	err := mgr.RegisterIndexes(entityType)
	require.NoError(t, err)

	entity := TestEntity{
		ID:    1,
		Name:  "test",
		Email: "test@example.com",
	}

	err = mgr.UpdateIndexes(nil, &entity, []byte("1"))
	require.NoError(t, err)

	err = mgr.DeleteIndexes(&entity, []byte("1"))
	assert.NoError(t, err)

	keys, err := mgr.QueryIndex("Name", "test")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

// TestIndexManager_DeleteIndexes_NeverIndexed verifies deleting an entity
// that was never indexed is a no-op that succeeds.
func TestIndexManager_DeleteIndexes_NeverIndexed(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	entity := &TestEntity{ID: 99, Name: "ghost", Email: "ghost@example.com"}
	assert.NoError(t, mgr.DeleteIndexes(entity, uint64ToBytes(99)))
}

func TestIndexManager_DeleteIndexes_ClosedManager(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	cleanup() // Close immediately

	entity := TestEntity{ID: 1, Name: "test"}
	err := mgr.DeleteIndexes(&entity, []byte("1"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

// TestIndexManager_DeleteIndexes_GetFieldError verifies DeleteIndexes returns
// the field-read error when the entity lacks an indexed field.
func TestIndexManager_DeleteIndexes_GetFieldError(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	mgr.indexes["Name"] = &deletefailIndex{}

	type Other struct{ X int }
	err := mgr.DeleteIndexes(Other{X: 1}, []byte("ek"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get value for field Name")
}

// TestIndexManager_UpdateIndexes_DeleteError verifies UpdateIndexes returns
// the index's Delete error when removing the old value fails.
func TestIndexManager_UpdateIndexes_DeleteError(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	type User struct {
		Name string `index:"true"`
	}
	deleteErr := &ClosedError{Name: "delete-error"}
	mgr.indexes["Name"] = &deletefailIndex{deleteErr: deleteErr}

	err := mgr.UpdateIndexes(User{Name: "alice"}, User{Name: "bob"}, []byte("ek1"))
	require.ErrorIs(t, err, deleteErr)
	assert.Contains(t, err.Error(), "failed to delete old index entry for field Name")
}

// TestIndexManager_UpdateIndexes_PutError verifies UpdateIndexes returns the
// index's Put error when adding the new value fails after a successful Delete.
func TestIndexManager_UpdateIndexes_PutError(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	type User struct {
		Name string `index:"true"`
	}
	putErr := &ClosedError{Name: "put-error"}
	mgr.indexes["Name"] = &putfailIndex{putErr: putErr}

	err := mgr.UpdateIndexes(User{Name: "alice"}, User{Name: "bob"}, []byte("ek1"))
	require.ErrorIs(t, err, putErr)
	assert.Contains(t, err.Error(), "failed to add index entry for field Name")
}

func TestIndexManager_UpdateIndexes_MultipleEntities(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	entities := []*TestEntity{
		{ID: 1, Name: "alice", Email: "alice@example.com"},
		{ID: 2, Name: "bob", Email: "bob@example.com"},
		{ID: 3, Name: "alice", Email: "alice2@example.com"},
	}
	for _, e := range entities {
		require.NoError(t, mgr.UpdateIndexes(nil, e, uint64ToBytes(e.ID)))
	}

	// "alice" maps to entities 1 and 3.
	keys, err := mgr.QueryIndex("Name", "alice")
	require.NoError(t, err)
	assert.Len(t, keys, 2)
	assert.True(t, containsKey(keys, uint64ToBytes(1)))
	assert.True(t, containsKey(keys, uint64ToBytes(3)))
}

func TestIndexManager_UpdateIndexes_ValueChanged(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	entityKey := uint64ToBytes(1)
	oldEntity := &TestEntity{ID: 1, Name: "alice", Email: "alice@example.com"}
	newEntity := &TestEntity{ID: 1, Name: "bob", Email: "alice@example.com"}
	require.NoError(t, mgr.UpdateIndexes(nil, oldEntity, entityKey))
	require.NoError(t, mgr.UpdateIndexes(oldEntity, newEntity, entityKey))

	keys, err := mgr.QueryIndex("Name", "alice")
	require.NoError(t, err)
	assert.Empty(t, keys)

	keys, err = mgr.QueryIndex("Name", "bob")
	require.NoError(t, err)
	assert.True(t, containsKey(keys, entityKey))
}

func TestIndexManager_UpdateIndexes_ValueUnchangedNoOp(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	entityKey := uint64ToBytes(1)
	entity := &TestEntity{ID: 1, Name: "alice", Email: "alice@example.com"}
	require.NoError(t, mgr.UpdateIndexes(nil, entity, entityKey))
	require.NoError(t, mgr.UpdateIndexes(entity, entity, entityKey))

	keys, err := mgr.QueryIndex("Name", "alice")
	require.NoError(t, err)
	assert.Len(t, keys, 1)
	assert.True(t, containsKey(keys, entityKey))
}

func TestIndexManager_UpdateIndexes_ClosedManager(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	cleanup()

	entity := &TestEntity{ID: 1, Name: "alice", Email: "alice@example.com"}
	err := mgr.UpdateIndexes(nil, entity, uint64ToBytes(1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

func TestIndexManager_UpdateIndexes_InvalidNewEntity(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	err := mgr.UpdateIndexes(nil, "not-a-struct", uint64ToBytes(1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected struct")
}

func TestIndexManager_UpdateIndexes_InvalidOldEntity(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	newEntity := &TestEntity{ID: 1, Name: "alice", Email: "alice@example.com"}
	err := mgr.UpdateIndexes("not-a-struct", newEntity, uint64ToBytes(1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected struct")
}

// TestIndexManager_QueryIndexWithContext_ConvertError verifies
// QueryIndexWithContext rejects a value it cannot convert to bytes on a
// context-aware index.
func TestIndexManager_QueryIndexWithContext_ConvertError(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	mgr, err := NewIndexManager(s, newTestLogger(t))
	require.NoError(t, err)

	type User struct {
		Name string `index:"true"`
	}
	require.NoError(t, mgr.RegisterIndexes(reflect.TypeOf(User{})))
	_, isCtx := mgr.indexes["Name"].(IndexWithContext)
	require.True(t, isCtx, "registered index must take the context-aware path")

	ids, err := mgr.QueryIndexWithContext(context.Background(), "Name", nil)
	require.Error(t, err)
	assert.Nil(t, ids)
	assert.Contains(t, err.Error(), "failed to convert value to bytes")
}

func TestIndexManager_QueryIndexWithContext_Unit(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	config := IndexConfig{
		Name: "Name",
		Type: IndexTypeNormal,
	}
	memIdx, err := NewExtendedMemoryIndex(&config)
	require.NoError(t, err)
	mgr.indexes["Name"] = memIdx

	err = memIdx.Put([]byte("1"), []byte("test"))
	require.NoError(t, err)

	ctx := context.Background()
	keys, err := mgr.QueryIndexWithContext(ctx, "Name", "test")
	assert.NoError(t, err)
	assert.True(t, containsKey(keys, []byte("1")))
}

func TestIndexManager_QueryIndexWithContext_NonExistentIndex(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	ctx := context.Background()
	ids, err := mgr.QueryIndexWithContext(ctx, "NonExistent", "test")
	assert.Error(t, err)
	assert.Nil(t, ids)
	assert.Contains(t, err.Error(), "not found")
}

func TestIndexManager_QueryIndexWithContext_ClosedManager(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	cleanup() // Close immediately

	ctx := context.Background()
	ids, err := mgr.QueryIndexWithContext(ctx, "Name", "test")
	assert.Error(t, err)
	assert.Nil(t, ids)
	assert.Contains(t, err.Error(), "closed")
}

func TestIndexManager_QueryIndex_NoMatch(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	keys, err := mgr.QueryIndex("Name", "nonexistent")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestIndexManager_QueryIndex_NonExistentIndex(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	keys, err := mgr.QueryIndex("DoesNotExist", "value")
	assert.Nil(t, keys)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestIndexManager_QueryIndex_ClosedManager(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	cleanup()

	keys, err := mgr.QueryIndex("Name", "alice")
	assert.Nil(t, keys)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

func TestIndexManager_QueryIndex_NilValue(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	keys, err := mgr.QueryIndex("Name", nil)
	assert.Nil(t, keys)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

func TestIndexManager_QueryIndex_UnsupportedType(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	keys, err := mgr.QueryIndex("Name", struct{}{})
	assert.Nil(t, keys)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}

func TestIndexManager_ScanIndex_WithResults(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	entities := []*TestEntity{
		{ID: 1, Name: "alice", Email: "a@example.com"},
		{ID: 2, Name: "bob", Email: "b@example.com"},
		{ID: 3, Name: "charlie", Email: "c@example.com"},
	}
	for _, e := range entities {
		require.NoError(t, mgr.UpdateIndexes(nil, e, uint64ToBytes(e.ID)))
	}

	// [alice, charlie) returns alice and bob: the upper bound is exclusive.
	results, err := mgr.ScanIndex("Name", "alice", "charlie")
	require.NoError(t, err)
	assert.Contains(t, results, "alice")
	assert.Contains(t, results, "bob")
	assert.NotContains(t, results, "charlie")
}

func TestIndexManager_ScanIndex_EmptyRange(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	entity := &TestEntity{ID: 1, Name: "alice", Email: "a@example.com"}
	require.NoError(t, mgr.UpdateIndexes(nil, entity, uint64ToBytes(1)))

	results, err := mgr.ScanIndex("Name", "zzz", "zzzz")
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestIndexManager_ScanIndex_NilBounds(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	entity := &TestEntity{ID: 1, Name: "alice", Email: "a@example.com"}
	require.NoError(t, mgr.UpdateIndexes(nil, entity, uint64ToBytes(1)))

	// Nil start and end scan every entry.
	results, err := mgr.ScanIndex("Name", nil, nil)
	require.NoError(t, err)
	assert.Contains(t, results, "alice")
}

func TestIndexManager_ScanIndex_NonExistentIndex(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	results, err := mgr.ScanIndex("DoesNotExist", "a", "z")
	assert.Nil(t, results)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestIndexManager_ScanIndex_ClosedManager(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	cleanup()

	results, err := mgr.ScanIndex("Name", "a", "z")
	assert.Nil(t, results)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

func TestIndexManager_ScanIndex_InvalidBoundType(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	tests := []struct {
		name       string
		start, end interface{}
	}{
		{"start", struct{}{}, "z"},
		{"end", "a", struct{}{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := mgr.ScanIndex("Name", tt.start, tt.end)
			assert.Nil(t, results)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unsupported")
		})
	}
}

func TestIndexManager_GetIndex_Unit(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	entityType := reflect.TypeOf(TestEntity{})
	err := mgr.RegisterIndexes(entityType)
	require.NoError(t, err)

	idx, err := mgr.GetIndex("Name")
	assert.NoError(t, err)
	assert.NotNil(t, idx)
}

func TestIndexManager_GetIndex_NonExistent(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	idx, err := mgr.GetIndex("NonExistent")
	assert.Error(t, err)
	assert.Nil(t, idx)
	assert.Contains(t, err.Error(), "not found")
}

func TestIndexManager_GetIndex_ClosedManager(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	cleanup() // Close immediately

	idx, err := mgr.GetIndex("Name")
	assert.Error(t, err)
	assert.Nil(t, idx)
	assert.Contains(t, err.Error(), "closed")
}

func TestIndexManager_GetMetadata_Unit(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	entityType := reflect.TypeOf(TestEntity{})
	err := mgr.RegisterIndexes(entityType)
	require.NoError(t, err)

	meta, err := mgr.GetMetadata("Name")
	assert.NoError(t, err)
	assert.NotNil(t, meta)
	assert.Equal(t, "Name", meta.FieldName)
}

func TestIndexManager_GetMetadata_NonExistent(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	meta, err := mgr.GetMetadata("NonExistent")
	assert.Error(t, err)
	assert.Nil(t, meta)
	assert.Contains(t, err.Error(), "not found")
}

func TestIndexManager_GetMetadata_ClosedManager(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	cleanup() // Close immediately

	meta, err := mgr.GetMetadata("Name")
	assert.Error(t, err)
	assert.Nil(t, meta)
	assert.Contains(t, err.Error(), "closed")
}

func TestIndexManager_ClearIndex_Unit(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	config := IndexConfig{
		Name: "TestIndex",
		Type: IndexTypeNormal,
	}
	memIdx, err := NewExtendedMemoryIndex(&config)
	require.NoError(t, err)

	mgr.indexes["TestIndex"] = memIdx

	err = memIdx.Put([]byte("1"), []byte("value1"))
	require.NoError(t, err)

	err = mgr.ClearIndex("TestIndex")
	assert.NoError(t, err)

	ids, err := memIdx.Get([]byte("value1"))
	assert.True(t, len(ids) == 0 || err != nil)
}

func TestIndexManager_ClearIndex_NonExistent(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	err := mgr.ClearIndex("NonExistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestIndexManager_ClearIndex_ClosedManager(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	cleanup() // Close immediately

	err := mgr.ClearIndex("TestIndex")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

func TestIndexManager_ClearAllIndexes_Unit(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	for i := 0; i < 3; i++ {
		config := IndexConfig{
			Name: fmt.Sprintf("TestIndex%d", i),
			Type: IndexTypeNormal,
		}
		memIdx, err := NewExtendedMemoryIndex(&config)
		require.NoError(t, err)
		mgr.indexes[config.Name] = memIdx

		err = memIdx.Put([]byte(fmt.Sprintf("%d", i)), []byte(fmt.Sprintf("value%d", i)))
		require.NoError(t, err)
	}

	err := mgr.ClearAllIndexes()
	assert.NoError(t, err)

	for name, idx := range mgr.indexes {
		ids, err := idx.Get([]byte("value"))
		assert.True(t, len(ids) == 0 || err != nil, "Index %s should be empty", name)
	}
}

func TestIndexManager_ClearAllIndexes_ClosedManager(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	cleanup() // Close immediately

	err := mgr.ClearAllIndexes()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

func TestIndexManager_ClearAllIndexes_WithFailingIndex(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	clearErr := &dberrors.MessageError{Message: "clear failed"}
	mgr.indexes["broken"] = &failingIndex{clearErr: clearErr}

	err := mgr.ClearAllIndexes()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clear failed")
}

func TestIndexManager_ListIndexes_EmptyManager(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	names := mgr.ListIndexes()
	assert.NotNil(t, names)
	assert.Empty(t, names)
}

func TestIndexManager_Close_DoubleClose(t *testing.T) {
	mgr, _ := setupManagerWithMemoryIndexes(t)

	assert.NoError(t, mgr.Close())
	// A second Close is a no-op.
	assert.NoError(t, mgr.Close())
}

func TestIndexManager_Close_WithFailingIndex(t *testing.T) {
	mgr, _ := setupUnitTestManager(t)

	closeErr := &dberrors.MessageError{Message: "close failed"}
	mgr.indexes["broken"] = &failingIndex{closeErr: closeErr}

	err := mgr.Close()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "close failed")
	// The manager is closed despite the index error.
	assert.True(t, mgr.closed)
}

func TestIndexManager_Close_EmptyManager(t *testing.T) {
	mgr, _ := setupUnitTestManager(t)

	assert.NoError(t, mgr.Close())
	assert.True(t, mgr.closed)
}

func TestIndexManager_RebuildIndex_Unit(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	config := IndexConfig{
		Name: "TestIndex",
		Type: IndexTypeNormal,
	}
	memIdx, err := NewExtendedMemoryIndex(&config)
	require.NoError(t, err)
	mgr.indexes["TestIndex"] = memIdx

	err = memIdx.Put([]byte("1"), []byte("old_value"))
	require.NoError(t, err)

	populateFunc := func(idx Index) error {
		return idx.Put([]byte("2"), []byte("new_value"))
	}

	err = mgr.RebuildIndex("TestIndex", populateFunc)
	assert.NoError(t, err)

	keys, _ := memIdx.Get([]byte("old_value"))
	assert.Empty(t, keys)

	keys, err = memIdx.Get([]byte("new_value"))
	assert.NoError(t, err)
	assert.True(t, containsKey(keys, []byte("2")))
}

func TestIndexManager_RebuildIndex_NonExistent(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	populateFunc := func(idx Index) error {
		return nil
	}

	err := mgr.RebuildIndex("NonExistent", populateFunc)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestIndexManager_RebuildIndex_ClosedManager(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	cleanup() // Close immediately

	populateFunc := func(idx Index) error {
		return nil
	}

	err := mgr.RebuildIndex("TestIndex", populateFunc)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

func TestIndexManager_RebuildIndex_PopulateError(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	config := IndexConfig{
		Name: "TestIndex",
		Type: IndexTypeNormal,
	}
	memIdx, err := NewExtendedMemoryIndex(&config)
	require.NoError(t, err)
	mgr.indexes["TestIndex"] = memIdx

	populateFunc := func(idx Index) error {
		return &dberrors.MessageError{Message: "populate failed"}
	}

	err = mgr.RebuildIndex("TestIndex", populateFunc)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "populate failed")
}

func TestIndexManager_Stats_Unit(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	entityType := reflect.TypeOf(TestEntity{})
	err := mgr.RegisterIndexes(entityType)
	require.NoError(t, err)

	stats := mgr.Stats()
	assert.NotNil(t, stats)
	assert.Contains(t, stats, "total_indexes")
	assert.Contains(t, stats, "closed")
	assert.Contains(t, stats, "indexes")

	totalIndexes := stats["total_indexes"].(int)
	assert.Greater(t, totalIndexes, 0)

	indexList := stats["indexes"].([]map[string]interface{})
	assert.NotEmpty(t, indexList)
}

func TestIndexManager_String_Unit(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	entityType := reflect.TypeOf(TestEntity{})
	err := mgr.RegisterIndexes(entityType)
	require.NoError(t, err)

	str := mgr.String()
	assert.NotEmpty(t, str)
	assert.Contains(t, str, "IndexManager")
	assert.Contains(t, str, "indexes:")
	assert.Contains(t, str, "closed:")
}

func TestInterfaceToBytes_AllTypes(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"string", "test", false},
		{"bytes", []byte("test"), false},
		{"uint64", uint64(123), false},
		{"uint32", uint32(123), false},
		{"uint", uint(123), false},
		{"int64", int64(-123), false},
		{"int32", int32(-123), false},
		{"int", int(-123), false},
		{"bool_true", true, false},
		{"bool_false", false, false},
		{"float64", float64(123.456), false},
		{"float32", float32(123.456), false},
		{"nil", nil, true},
		{"unsupported", struct{}{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := interfaceToBytes(tt.value)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

func TestBytesEqual(t *testing.T) {
	tests := []struct {
		name     string
		a        []byte
		b        []byte
		expected bool
	}{
		{"equal", []byte("test"), []byte("test"), true},
		{"not_equal", []byte("test"), []byte("other"), false},
		{"different_length", []byte("test"), []byte("testing"), false},
		{"both_empty", []byte{}, []byte{}, true},
		{"one_empty", []byte("test"), []byte{}, false},
		{"both_nil", nil, nil, true},
		{"nil_and_empty", nil, []byte{}, true},
		{"empty_and_nil", []byte{}, nil, true},
		{"nil_and_non_empty", nil, []byte{1}, false},
		{"non_empty_and_nil", []byte{1}, nil, false},
		{"same_length_different_content", []byte{1, 2, 3}, []byte{1, 2, 4}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := bytesEqual(tt.a, tt.b)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIndexManager_RegisterIndexes_AlreadyExists(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	defer cleanup()

	entityType := reflect.TypeOf(TestEntity{})
	err := mgr.RegisterIndexes(entityType)
	require.NoError(t, err)

	err = mgr.RegisterIndexes(entityType)
	assert.NoError(t, err)
}

func TestIndexManager_RegisterIndexes_ClosedManager(t *testing.T) {
	mgr, cleanup := setupUnitTestManager(t)
	cleanup() // Close immediately

	entityType := reflect.TypeOf(TestEntity{})
	err := mgr.RegisterIndexes(entityType)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

func TestIndexManager_RegisterIndexes_ViaConstructor(t *testing.T) {
	mgr, err := NewIndexManager(newMockStorage(), newTestLogger(t))
	require.NoError(t, err)
	defer func() { _ = mgr.Close() }()

	require.NoError(t, mgr.RegisterIndexes(reflect.TypeOf(TestEntity{})))

	names := mgr.ListIndexes()
	sort.Strings(names)
	assert.Equal(t, []string{"Email", "ID", "Name"}, names)
}

func TestIndexManager_RegisterIndexes_InvalidType(t *testing.T) {
	mgr, err := NewIndexManager(newMockStorage(), newTestLogger(t))
	require.NoError(t, err)
	defer func() { _ = mgr.Close() }()

	err = mgr.RegisterIndexes(reflect.TypeOf(42))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected struct")
}
