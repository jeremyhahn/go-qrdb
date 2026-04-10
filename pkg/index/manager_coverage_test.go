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
	"encoding/binary"
	"reflect"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
)

// failingIndex is an Index that returns errors for Close and Clear operations,
// used to test error aggregation paths in the manager.
type failingIndex struct {
	closeErr error
	clearErr error
}

func (f *failingIndex) Put(_ []byte, _ []byte) error                  { return nil }
func (f *failingIndex) Get(_ []byte) ([][]byte, error)                { return nil, nil }
func (f *failingIndex) Delete(_ []byte, _ []byte) error               { return nil }
func (f *failingIndex) Scan(_, _ []byte) (map[string][][]byte, error) { return nil, nil }
func (f *failingIndex) GetAll() ([][]byte, error)                     { return nil, nil }
func (f *failingIndex) Clear() error                                  { return f.clearErr }
func (f *failingIndex) Close() error                                  { return f.closeErr }

// newTestLogger creates a logger configured for testing.
func newTestLogger(t *testing.T) *logger.Logger {
	t.Helper()
	log, err := logger.New(logger.LogConfig{
		Level:  "error",
		Format: "json",
		Output: "stdout",
	})
	require.NoError(t, err)
	return log
}

// uint64ToBytes encodes a uint64 as a big-endian byte slice for use as entity keys in tests.
func uint64ToBytes(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// setupManagerWithMemoryIndexes creates an IndexManager with ExtendedMemoryIndex
// instances pre-registered for the fields of TestEntity. This avoids the
// mockStorage iterator limitations that StoragePebbleIndex would expose.
func setupManagerWithMemoryIndexes(t *testing.T) (*IndexManager, func()) {
	t.Helper()

	log := newTestLogger(t)
	store := newMockStorage()

	mgr := &IndexManager{
		storage:  store,
		logger:   log.WithField("component", "index-manager-test"),
		indexes:  make(map[string]Index),
		metadata: make(map[string]*IndexMetadata),
	}

	// Register memory-backed indexes for TestEntity fields
	fields := map[string]*IndexMetadata{
		"ID": {
			FieldName: "ID",
			FieldType: "uint64",
			Config: IndexConfig{
				Name:          "ID",
				Type:          IndexTypeUnique,
				CaseSensitive: true,
			},
			Tag: "unique",
		},
		"Name": {
			FieldName: "Name",
			FieldType: "string",
			Config: IndexConfig{
				Name:          "Name",
				Type:          IndexTypeNormal,
				CaseSensitive: true,
			},
			Tag: "true",
		},
		"Email": {
			FieldName: "Email",
			FieldType: "string",
			Config: IndexConfig{
				Name:          "Email",
				Type:          IndexTypeUnique,
				CaseSensitive: false,
			},
			Tag: "unique,case_insensitive",
		},
	}

	for name, meta := range fields {
		idx, err := NewExtendedMemoryIndex(&meta.Config)
		require.NoError(t, err)
		mgr.indexes[name] = idx
		mgr.metadata[name] = meta
	}

	cleanup := func() {
		_ = mgr.Close()
	}

	return mgr, cleanup
}

// containsKey checks whether a [][]byte slice contains a specific key.
func containsKey(keys [][]byte, target []byte) bool {
	for _, k := range keys {
		if bytes.Equal(k, target) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// NewIndexManager
// ---------------------------------------------------------------------------

func TestNewIndexManager_Success(t *testing.T) {
	log := newTestLogger(t)
	store := newMockStorage()

	mgr, err := NewIndexManager(store, log)
	require.NoError(t, err)
	require.NotNil(t, mgr)
	defer func() { _ = mgr.Close() }()

	assert.NotNil(t, mgr.storage)
	assert.NotNil(t, mgr.logger)
	assert.NotNil(t, mgr.indexes)
	assert.NotNil(t, mgr.metadata)
	assert.False(t, mgr.closed)
}

func TestNewIndexManager_NilStorageReturnsTypedError(t *testing.T) {
	log := newTestLogger(t)

	mgr, err := NewIndexManager(nil, log)
	assert.Nil(t, mgr)
	require.Error(t, err)

	var dragonErr *dberrors.DragonError
	assert.ErrorAs(t, err, &dragonErr)
	assert.Equal(t, dberrors.ErrInvalidInput, dragonErr.Code)
}

func TestNewIndexManager_NilLoggerReturnsTypedError(t *testing.T) {
	store := newMockStorage()

	mgr, err := NewIndexManager(store, nil)
	assert.Nil(t, mgr)
	require.Error(t, err)

	var dragonErr *dberrors.DragonError
	assert.ErrorAs(t, err, &dragonErr)
	assert.Equal(t, dberrors.ErrInvalidInput, dragonErr.Code)
}

// ---------------------------------------------------------------------------
// UpdateIndexes
// ---------------------------------------------------------------------------

func TestUpdateIndexes_InsertNewEntity(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	entityKey := uint64ToBytes(1)
	entity := &TestEntity{ID: 1, Name: "alice", Email: "alice@example.com"}

	err := mgr.UpdateIndexes(nil, entity, entityKey)
	require.NoError(t, err)

	keys, err := mgr.QueryIndex("Name", "alice")
	require.NoError(t, err)
	assert.True(t, containsKey(keys, entityKey))
}

func TestUpdateIndexes_ValueChanged(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	entityKey := uint64ToBytes(1)
	oldEntity := &TestEntity{ID: 1, Name: "alice", Email: "alice@example.com"}
	newEntity := &TestEntity{ID: 1, Name: "bob", Email: "alice@example.com"}

	// Insert
	err := mgr.UpdateIndexes(nil, oldEntity, entityKey)
	require.NoError(t, err)

	// Update name
	err = mgr.UpdateIndexes(oldEntity, newEntity, entityKey)
	require.NoError(t, err)

	// Old name should no longer resolve
	keys, err := mgr.QueryIndex("Name", "alice")
	require.NoError(t, err)
	assert.Empty(t, keys)

	// New name should resolve
	keys, err = mgr.QueryIndex("Name", "bob")
	require.NoError(t, err)
	assert.True(t, containsKey(keys, entityKey))
}

func TestUpdateIndexes_ValueUnchangedNoOp(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	entityKey := uint64ToBytes(1)
	entity := &TestEntity{ID: 1, Name: "alice", Email: "alice@example.com"}

	err := mgr.UpdateIndexes(nil, entity, entityKey)
	require.NoError(t, err)

	// Update with identical values should succeed without error
	err = mgr.UpdateIndexes(entity, entity, entityKey)
	require.NoError(t, err)

	// Should still have exactly one entry
	keys, err := mgr.QueryIndex("Name", "alice")
	require.NoError(t, err)
	assert.Len(t, keys, 1)
	assert.True(t, containsKey(keys, entityKey))
}

func TestUpdateIndexes_ClosedManager(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	cleanup() // Close immediately

	entityKey := uint64ToBytes(1)
	entity := &TestEntity{ID: 1, Name: "alice", Email: "alice@example.com"}
	err := mgr.UpdateIndexes(nil, entity, entityKey)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

func TestUpdateIndexes_InvalidNewEntity(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	// Pass a non-struct type as newEntity - GetFieldValue should fail
	err := mgr.UpdateIndexes(nil, "not-a-struct", uint64ToBytes(1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected struct")
}

func TestUpdateIndexes_InvalidOldEntity(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	newEntity := &TestEntity{ID: 1, Name: "alice", Email: "alice@example.com"}

	// Pass a non-struct type as oldEntity - GetFieldValue should fail
	err := mgr.UpdateIndexes("not-a-struct", newEntity, uint64ToBytes(1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected struct")
}

func TestUpdateIndexes_MultipleEntities(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	entities := []*TestEntity{
		{ID: 1, Name: "alice", Email: "alice@example.com"},
		{ID: 2, Name: "bob", Email: "bob@example.com"},
		{ID: 3, Name: "alice", Email: "alice2@example.com"},
	}

	for _, e := range entities {
		entityKey := uint64ToBytes(e.ID)
		err := mgr.UpdateIndexes(nil, e, entityKey)
		require.NoError(t, err)
	}

	// "alice" should map to entities 1 and 3
	keys, err := mgr.QueryIndex("Name", "alice")
	require.NoError(t, err)
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })
	assert.Len(t, keys, 2)
	assert.True(t, containsKey(keys, uint64ToBytes(1)))
	assert.True(t, containsKey(keys, uint64ToBytes(3)))
}

// ---------------------------------------------------------------------------
// DeleteIndexes
// ---------------------------------------------------------------------------

func TestDeleteIndexes_Success(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	entityKey := uint64ToBytes(1)
	entity := &TestEntity{ID: 1, Name: "alice", Email: "alice@example.com"}

	err := mgr.UpdateIndexes(nil, entity, entityKey)
	require.NoError(t, err)

	err = mgr.DeleteIndexes(entity, entityKey)
	require.NoError(t, err)

	keys, err := mgr.QueryIndex("Name", "alice")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestDeleteIndexes_InvalidEntity(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	// Non-struct entity should cause GetFieldValue to fail
	err := mgr.DeleteIndexes("not-a-struct", uint64ToBytes(1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected struct")
}

func TestDeleteIndexes_NonExistentEntity(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	// Delete entity that was never indexed should succeed (no-op)
	entityKey := uint64ToBytes(99)
	entity := &TestEntity{ID: 99, Name: "ghost", Email: "ghost@example.com"}
	err := mgr.DeleteIndexes(entity, entityKey)
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// QueryIndex
// ---------------------------------------------------------------------------

func TestQueryIndex_Success(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	entityKey := uint64ToBytes(1)
	entity := &TestEntity{ID: 1, Name: "alice", Email: "alice@example.com"}
	err := mgr.UpdateIndexes(nil, entity, entityKey)
	require.NoError(t, err)

	keys, err := mgr.QueryIndex("Name", "alice")
	require.NoError(t, err)
	assert.True(t, containsKey(keys, entityKey))
}

func TestQueryIndex_NotFound(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	keys, err := mgr.QueryIndex("Name", "nonexistent")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestQueryIndex_NonExistentIndex(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	keys, err := mgr.QueryIndex("DoesNotExist", "value")
	assert.Nil(t, keys)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestQueryIndex_ClosedManager(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	cleanup()

	keys, err := mgr.QueryIndex("Name", "alice")
	assert.Nil(t, keys)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

func TestQueryIndex_NilValue(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	keys, err := mgr.QueryIndex("Name", nil)
	assert.Nil(t, keys)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

func TestQueryIndex_UnsupportedType(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	keys, err := mgr.QueryIndex("Name", struct{}{})
	assert.Nil(t, keys)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}

// ---------------------------------------------------------------------------
// ScanIndex
// ---------------------------------------------------------------------------

func TestScanIndex_WithResults(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	// Insert entities with names that sort lexicographically: alice, bob, charlie
	entities := []*TestEntity{
		{ID: 1, Name: "alice", Email: "a@example.com"},
		{ID: 2, Name: "bob", Email: "b@example.com"},
		{ID: 3, Name: "charlie", Email: "c@example.com"},
	}

	for _, e := range entities {
		entityKey := uint64ToBytes(e.ID)
		err := mgr.UpdateIndexes(nil, e, entityKey)
		require.NoError(t, err)
	}

	// Scan range [alice, charlie) should return alice and bob
	results, err := mgr.ScanIndex("Name", "alice", "charlie")
	require.NoError(t, err)
	require.NotNil(t, results)

	assert.Contains(t, results, "alice")
	assert.Contains(t, results, "bob")
	assert.NotContains(t, results, "charlie") // exclusive upper bound
}

func TestScanIndex_EmptyRange(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	entityKey := uint64ToBytes(1)
	entity := &TestEntity{ID: 1, Name: "alice", Email: "a@example.com"}
	err := mgr.UpdateIndexes(nil, entity, entityKey)
	require.NoError(t, err)

	// Scan range that contains no entries
	results, err := mgr.ScanIndex("Name", "zzz", "zzzz")
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestScanIndex_NilBounds(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	entityKey := uint64ToBytes(1)
	entity := &TestEntity{ID: 1, Name: "alice", Email: "a@example.com"}
	err := mgr.UpdateIndexes(nil, entity, entityKey)
	require.NoError(t, err)

	// Both start and end nil should scan all entries
	results, err := mgr.ScanIndex("Name", nil, nil)
	require.NoError(t, err)
	assert.Contains(t, results, "alice")
}

func TestScanIndex_NonExistentIndex(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	results, err := mgr.ScanIndex("DoesNotExist", "a", "z")
	assert.Nil(t, results)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestScanIndex_ClosedManager(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	cleanup()

	results, err := mgr.ScanIndex("Name", "a", "z")
	assert.Nil(t, results)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

func TestScanIndex_InvalidStartType(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	// Use an unsupported type for start
	results, err := mgr.ScanIndex("Name", struct{}{}, "z")
	assert.Nil(t, results)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}

func TestScanIndex_InvalidEndType(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	// Use an unsupported type for end
	results, err := mgr.ScanIndex("Name", "a", struct{}{})
	assert.Nil(t, results)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}

// ---------------------------------------------------------------------------
// ListIndexes
// ---------------------------------------------------------------------------

func TestListIndexes_WithRegisteredIndexes(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	names := mgr.ListIndexes()
	sort.Strings(names)

	assert.Equal(t, []string{"Email", "ID", "Name"}, names)
}

func TestListIndexes_EmptyManager(t *testing.T) {
	log := newTestLogger(t)
	store := newMockStorage()

	mgr := &IndexManager{
		storage:  store,
		logger:   log.WithField("component", "index-manager-test"),
		indexes:  make(map[string]Index),
		metadata: make(map[string]*IndexMetadata),
	}
	defer func() { _ = mgr.Close() }()

	names := mgr.ListIndexes()
	assert.NotNil(t, names)
	assert.Empty(t, names)
}

// ---------------------------------------------------------------------------
// ClearAllIndexes
// ---------------------------------------------------------------------------

func TestClearAllIndexes_WithFailingIndex(t *testing.T) {
	log := newTestLogger(t)
	store := newMockStorage()

	mgr := &IndexManager{
		storage:  store,
		logger:   log.WithField("component", "index-manager-test"),
		indexes:  make(map[string]Index),
		metadata: make(map[string]*IndexMetadata),
	}
	defer func() { _ = mgr.Close() }()

	clearErr := &dberrors.MessageError{Message: "clear failed"}
	mgr.indexes["broken"] = &failingIndex{clearErr: clearErr}

	err := mgr.ClearAllIndexes()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clear failed")
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestClose_DoubleClose(t *testing.T) {
	mgr, _ := setupManagerWithMemoryIndexes(t)

	// First close should succeed
	err := mgr.Close()
	assert.NoError(t, err)

	// Second close should be a no-op (return nil)
	err = mgr.Close()
	assert.NoError(t, err)
}

func TestClose_WithFailingIndex(t *testing.T) {
	log := newTestLogger(t)
	store := newMockStorage()

	mgr := &IndexManager{
		storage:  store,
		logger:   log.WithField("component", "index-manager-test"),
		indexes:  make(map[string]Index),
		metadata: make(map[string]*IndexMetadata),
	}

	closeErr := &dberrors.MessageError{Message: "close failed"}
	mgr.indexes["broken"] = &failingIndex{closeErr: closeErr}

	err := mgr.Close()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "close failed")

	// Manager should still be marked as closed despite the error
	assert.True(t, mgr.closed)
}

func TestClose_EmptyManager(t *testing.T) {
	log := newTestLogger(t)
	store := newMockStorage()

	mgr := &IndexManager{
		storage:  store,
		logger:   log.WithField("component", "index-manager-test"),
		indexes:  make(map[string]Index),
		metadata: make(map[string]*IndexMetadata),
	}

	err := mgr.Close()
	assert.NoError(t, err)
	assert.True(t, mgr.closed)
}

// ---------------------------------------------------------------------------
// bytesEqual
// ---------------------------------------------------------------------------

func TestBytesEqual_BothNil(t *testing.T) {
	assert.True(t, bytesEqual(nil, nil))
}

func TestBytesEqual_OneNilOneEmpty(t *testing.T) {
	// nil and empty slice have the same length (0), so they should be equal
	assert.True(t, bytesEqual(nil, []byte{}))
	assert.True(t, bytesEqual([]byte{}, nil))
}

func TestBytesEqual_OneNilOneNonEmpty(t *testing.T) {
	assert.False(t, bytesEqual(nil, []byte{1}))
	assert.False(t, bytesEqual([]byte{1}, nil))
}

func TestBytesEqual_SameLengthDifferentContent(t *testing.T) {
	assert.False(t, bytesEqual([]byte{1, 2, 3}, []byte{1, 2, 4}))
}

func TestBytesEqual_Identical(t *testing.T) {
	a := []byte{0xff, 0x00, 0xab}
	assert.True(t, bytesEqual(a, a))
}

// ---------------------------------------------------------------------------
// RegisterIndexes via constructor (with real StoragePebbleIndex)
// ---------------------------------------------------------------------------

func TestRegisterIndexes_ViaConstructorSuccess(t *testing.T) {
	log := newTestLogger(t)
	store := newMockStorage()

	mgr, err := NewIndexManager(store, log)
	require.NoError(t, err)
	defer func() { _ = mgr.Close() }()

	entityType := reflect.TypeOf(TestEntity{})
	err = mgr.RegisterIndexes(entityType)
	require.NoError(t, err)

	// Verify all indexed fields were registered
	names := mgr.ListIndexes()
	sort.Strings(names)
	assert.Equal(t, []string{"Email", "ID", "Name"}, names)
}

func TestRegisterIndexes_InvalidType(t *testing.T) {
	log := newTestLogger(t)
	store := newMockStorage()

	mgr, err := NewIndexManager(store, log)
	require.NoError(t, err)
	defer func() { _ = mgr.Close() }()

	// Pass a non-struct type
	err = mgr.RegisterIndexes(reflect.TypeOf(42))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected struct")
}

// ---------------------------------------------------------------------------
// Full lifecycle: register, insert, query, update, delete, scan
// ---------------------------------------------------------------------------

func TestManagerFullLifecycle(t *testing.T) {
	mgr, cleanup := setupManagerWithMemoryIndexes(t)
	defer cleanup()

	entityKey1 := uint64ToBytes(1)
	entityKey2 := uint64ToBytes(2)

	// Insert
	entity1 := &TestEntity{ID: 1, Name: "alice", Email: "alice@example.com"}
	entity2 := &TestEntity{ID: 2, Name: "bob", Email: "bob@example.com"}

	err := mgr.UpdateIndexes(nil, entity1, entityKey1)
	require.NoError(t, err)

	err = mgr.UpdateIndexes(nil, entity2, entityKey2)
	require.NoError(t, err)

	// Query
	keys, err := mgr.QueryIndex("Name", "alice")
	require.NoError(t, err)
	assert.Len(t, keys, 1)
	assert.True(t, containsKey(keys, entityKey1))

	// Scan
	results, err := mgr.ScanIndex("Name", "a", "c")
	require.NoError(t, err)
	assert.Contains(t, results, "alice")
	assert.Contains(t, results, "bob")

	// Update
	updatedEntity1 := &TestEntity{ID: 1, Name: "alice-updated", Email: "alice@example.com"}
	err = mgr.UpdateIndexes(entity1, updatedEntity1, entityKey1)
	require.NoError(t, err)

	keys, err = mgr.QueryIndex("Name", "alice")
	require.NoError(t, err)
	assert.Empty(t, keys)

	keys, err = mgr.QueryIndex("Name", "alice-updated")
	require.NoError(t, err)
	assert.Len(t, keys, 1)
	assert.True(t, containsKey(keys, entityKey1))

	// Delete
	err = mgr.DeleteIndexes(updatedEntity1, entityKey1)
	require.NoError(t, err)

	keys, err = mgr.QueryIndex("Name", "alice-updated")
	require.NoError(t, err)
	assert.Empty(t, keys)

	// List
	names := mgr.ListIndexes()
	assert.Len(t, names, 3)

	// Close
	err = mgr.Close()
	assert.NoError(t, err)

	// Operations on closed manager should fail
	err = mgr.UpdateIndexes(nil, entity2, entityKey2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}
