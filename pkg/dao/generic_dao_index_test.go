//go:build codec_json

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package dao

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// ---------------------------------------------------------------------------
// Indexed test entity
// ---------------------------------------------------------------------------

// indexedTestEntity is a test entity with index tags for testing index
// auto-registration and query functionality.
type indexedTestEntity struct {
	ID       uint64 `json:"id"`
	Name     string `json:"name" index:"true"`
	Email    string `json:"email" index:"unique"`
	Category string `json:"category" index:"true"`
}

func (e *indexedTestEntity) EntityID() uint64      { return e.ID }
func (e *indexedTestEntity) SetEntityID(id uint64) { e.ID = id }

// noIndexEntity is a test entity without any index tags.
type noIndexEntity struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
}

func (e *noIndexEntity) EntityID() uint64      { return e.ID }
func (e *noIndexEntity) SetEntityID(id uint64) { e.ID = id }

// jsonTagVariantEntity is used for jsonFieldName tests.
type jsonTagVariantEntity struct {
	WithTag       string `json:"custom_name"`
	WithOmitempty string `json:"omit_field,omitempty"`
	NoTag         string
	DashTag       string `json:"-"`
	EmptyTag      string `json:""`
}

// ---------------------------------------------------------------------------
// indexAwareMemoryKVStore mock
// ---------------------------------------------------------------------------

// indexAwareMemoryKVStore wraps memoryKVStore with functional index
// methods for testing DAO index query paths.
type indexAwareMemoryKVStore struct {
	*memoryKVStore

	mu            sync.RWMutex
	registrations map[string][]kvstore.EntityIndex
	// indexData maps indexName -> fieldValue -> []entityKey
	indexData map[string]map[string][][]byte
}

// newIndexAwareMemoryKVStore creates an index-aware in-memory KV store for testing.
func newIndexAwareMemoryKVStore() *indexAwareMemoryKVStore {
	return &indexAwareMemoryKVStore{
		memoryKVStore: &memoryKVStore{
			data: make(map[string][]byte),
		},
		registrations: make(map[string][]kvstore.EntityIndex),
		indexData:     make(map[string]map[string][][]byte),
	}
}

// RegisterEntityIndexes records entity index registrations for verification.
func (s *indexAwareMemoryKVStore) RegisterEntityIndexes(entityType string, indexes []kvstore.EntityIndex) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.registrations[entityType] = indexes
	return nil
}

// QueryIndex returns pre-configured entity keys matching the given value.
func (s *indexAwareMemoryKVStore) QueryIndex(_ context.Context, indexName string, value []byte) ([][]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fieldValues, ok := s.indexData[indexName]
	if !ok {
		return nil, nil
	}

	keys, ok := fieldValues[string(value)]
	if !ok {
		return nil, nil
	}

	// Return a copy to prevent mutation.
	result := make([][]byte, len(keys))
	for i, k := range keys {
		cp := make([]byte, len(k))
		copy(cp, k)
		result[i] = cp
	}
	return result, nil
}

// ScanIndex returns pre-configured entity keys for values in [start, end).
func (s *indexAwareMemoryKVStore) ScanIndex(_ context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fieldValues, ok := s.indexData[indexName]
	if !ok {
		return nil, nil
	}

	result := make(map[string][][]byte)
	for fieldVal, keys := range fieldValues {
		valBytes := []byte(fieldVal)
		if bytes.Compare(valBytes, start) >= 0 && bytes.Compare(valBytes, end) < 0 {
			keysCopy := make([][]byte, len(keys))
			for i, k := range keys {
				cp := make([]byte, len(k))
				copy(cp, k)
				keysCopy[i] = cp
			}
			result[fieldVal] = keysCopy
		}
	}
	return result, nil
}

// QueryCompoundIndex stub for compound index queries.
func (s *indexAwareMemoryKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

// ScanCompoundIndex stub for compound index scans.
func (s *indexAwareMemoryKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

// addIndexEntry adds a pre-configured index entry for testing.
func (s *indexAwareMemoryKVStore) addIndexEntry(indexName, fieldValue string, entityKey []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.indexData[indexName] == nil {
		s.indexData[indexName] = make(map[string][][]byte)
	}
	s.indexData[indexName][fieldValue] = append(
		s.indexData[indexName][fieldValue],
		entityKey,
	)
}

// registeredIndexes returns the indexes registered for the given entity type.
func (s *indexAwareMemoryKVStore) registeredIndexes(entityType string) []kvstore.EntityIndex {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.registrations[entityType]
}

var _ kvstore.KVStore = (*indexAwareMemoryKVStore)(nil)

// ---------------------------------------------------------------------------
// errorIndexAwareKVStore mock — returns errors from index operations
// ---------------------------------------------------------------------------

// errorIndexAwareKVStore is an index-aware store that returns configurable
// errors from QueryIndex and ScanIndex for testing error propagation.
type errorIndexAwareKVStore struct {
	*memoryKVStore
	registerErr error
	queryErr    error
	scanErr     error
}

func newErrorIndexAwareKVStore(queryErr, scanErr error) *errorIndexAwareKVStore {
	return &errorIndexAwareKVStore{
		memoryKVStore: &memoryKVStore{
			data: make(map[string][]byte),
		},
		queryErr: queryErr,
		scanErr:  scanErr,
	}
}

func (s *errorIndexAwareKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return s.registerErr
}

func (s *errorIndexAwareKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, s.queryErr
}

func (s *errorIndexAwareKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, s.scanErr
}

func (s *errorIndexAwareKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

func (s *errorIndexAwareKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

var _ kvstore.KVStore = (*errorIndexAwareKVStore)(nil)

// ---------------------------------------------------------------------------
// Constructor tests — index auto-registration
// ---------------------------------------------------------------------------

// TestNew_WithIndexAwareStore_AutoRegisters verifies that New() parses
// index tags and calls RegisterEntityIndexes on the store.
func TestNew_WithIndexAwareStore_AutoRegisters(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"indexed_entities",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)

	require.NoError(t, err)
	require.NotNil(t, dao)

	registered := store.registeredIndexes("indexed_entities")
	require.NotEmpty(t, registered, "expected indexes to be registered")

	// Collect registered field names for assertion.
	fieldNames := make(map[string]bool, len(registered))
	for _, idx := range registered {
		fieldNames[idx.FieldName] = true
	}

	assert.True(t, fieldNames["Name"], "expected Name field to be registered")
	assert.True(t, fieldNames["Email"], "expected Email field to be registered")
	assert.True(t, fieldNames["Category"], "expected Category field to be registered")
}

// TestNew_WithIndexAwareStore_IndexProperties verifies that the
// registered indexes carry correct metadata from struct tags.
func TestNew_WithIndexAwareStore_IndexProperties(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	_, err := New[*indexedTestEntity](
		store,
		"indexed_entities",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	registered := store.registeredIndexes("indexed_entities")
	indexMap := make(map[string]kvstore.EntityIndex, len(registered))
	for _, idx := range registered {
		indexMap[idx.FieldName] = idx
	}

	// Email has index:"unique" tag.
	emailIdx, ok := indexMap["Email"]
	require.True(t, ok)
	assert.True(t, emailIdx.Unique, "Email index should be unique")
	assert.Equal(t, "email", emailIdx.JSONFieldName)

	// Name has index:"true" tag (normal, non-unique).
	nameIdx, ok := indexMap["Name"]
	require.True(t, ok)
	assert.False(t, nameIdx.Unique, "Name index should not be unique")
	assert.Equal(t, "name", nameIdx.JSONFieldName)
}

// TestNew_WithPlainStore_NoIndexRegistration verifies that using a plain
// KVStore with no-op index methods still allows the DAO to be created.
func TestNew_WithPlainStore_NoIndexRegistration(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"indexed_entities",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)

	require.NoError(t, err)
	require.NotNil(t, dao)
	assert.Equal(t, "indexed_entities", dao.EntityType())
}

// TestNew_WithIndexAwareStore_NoIndexTags verifies that when an entity has
// no index tags, RegisterEntityIndexes is not called.
func TestNew_WithIndexAwareStore_NoIndexTags(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*noIndexEntity](
		store,
		"plain_entities",
		func() *noIndexEntity { return &noIndexEntity{} },
	)

	require.NoError(t, err)
	require.NotNil(t, dao)

	registered := store.registeredIndexes("plain_entities")
	assert.Empty(t, registered, "expected no indexes to be registered for entity without index tags")
}

// ---------------------------------------------------------------------------
// QueryByIndex tests
// ---------------------------------------------------------------------------

// TestGenericDAO_QueryByIndex_FindsEntities verifies that QueryByIndex
// returns entities matching the given field value via the index.
func TestGenericDAO_QueryByIndex_FindsEntities(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	// Save entities via DAO.
	alice := &indexedTestEntity{ID: 1, Name: "Alice", Email: "alice@example.com", Category: "admin"}
	require.NoError(t, dao.Save(ctx, alice))

	bob := &indexedTestEntity{ID: 2, Name: "Bob", Email: "bob@example.com", Category: "user"}
	require.NoError(t, dao.Save(ctx, bob))

	// Manually configure index entries (simulates engine-level indexing).
	store.addIndexEntry("users.Email", "alice@example.com", []byte("users/1"))

	results, err := dao.QueryByIndex(ctx, "Email", "alice@example.com")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, uint64(1), results[0].ID)
	assert.Equal(t, "Alice", results[0].Name)
	assert.Equal(t, "alice@example.com", results[0].Email)
}

// TestGenericDAO_QueryByIndex_EmptyResult verifies that QueryByIndex
// returns an empty slice when no entities match.
func TestGenericDAO_QueryByIndex_EmptyResult(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	results, err := dao.QueryByIndex(ctx, "Email", "nobody@example.com")
	require.NoError(t, err)
	assert.Empty(t, results)
}

// TestGenericDAO_QueryByIndex_SkipsDeletedEntities verifies that
// QueryByIndex gracefully skips entities whose keys exist in the index
// but have been deleted from the underlying store.
func TestGenericDAO_QueryByIndex_SkipsDeletedEntities(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	// Save and then delete an entity.
	entity := &indexedTestEntity{ID: 10, Name: "Ghost", Email: "ghost@example.com"}
	require.NoError(t, dao.Save(ctx, entity))
	require.NoError(t, dao.Delete(ctx, entity))

	// Index still references the deleted key.
	store.addIndexEntry("users.Email", "ghost@example.com", []byte("users/10"))

	results, err := dao.QueryByIndex(ctx, "Email", "ghost@example.com")
	require.NoError(t, err)
	assert.Empty(t, results, "deleted entities should be skipped")
}

// TestGenericDAO_QueryByIndex_MultipleMatches verifies that QueryByIndex
// returns all entities when multiple keys match the same index value.
func TestGenericDAO_QueryByIndex_MultipleMatches(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	// Save multiple entities with the same Category.
	e1 := &indexedTestEntity{ID: 1, Name: "Alice", Email: "alice@example.com", Category: "admin"}
	e2 := &indexedTestEntity{ID: 2, Name: "Bob", Email: "bob@example.com", Category: "admin"}
	e3 := &indexedTestEntity{ID: 3, Name: "Carol", Email: "carol@example.com", Category: "user"}
	require.NoError(t, dao.Save(ctx, e1))
	require.NoError(t, dao.Save(ctx, e2))
	require.NoError(t, dao.Save(ctx, e3))

	// Index entries for "admin" category.
	store.addIndexEntry("users.Category", "admin", []byte("users/1"))
	store.addIndexEntry("users.Category", "admin", []byte("users/2"))

	results, err := dao.QueryByIndex(ctx, "Category", "admin")
	require.NoError(t, err)
	require.Len(t, results, 2)

	ids := []uint64{results[0].ID, results[1].ID}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	assert.Equal(t, []uint64{1, 2}, ids)
}

// TestGenericDAO_QueryByIndex_SkipsInvalidPrefix verifies that keys
// returned by the index that do not match the entity prefix are skipped.
func TestGenericDAO_QueryByIndex_SkipsInvalidPrefix(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	entity := &indexedTestEntity{ID: 1, Name: "Alice", Email: "alice@example.com"}
	require.NoError(t, dao.Save(ctx, entity))

	// Add an index entry with a wrong prefix.
	store.addIndexEntry("users.Email", "alice@example.com", []byte("sessions/1"))
	// Add a valid entry.
	store.addIndexEntry("users.Email", "alice@example.com", []byte("users/1"))

	results, err := dao.QueryByIndex(ctx, "Email", "alice@example.com")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, uint64(1), results[0].ID)
}

// TestGenericDAO_QueryByIndex_SkipsUnparseableID verifies that keys
// with non-numeric suffixes are skipped gracefully.
func TestGenericDAO_QueryByIndex_SkipsUnparseableID(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	entity := &indexedTestEntity{ID: 1, Name: "Alice", Email: "alice@example.com"}
	require.NoError(t, dao.Save(ctx, entity))

	// Add an index entry with a non-numeric ID.
	store.addIndexEntry("users.Email", "alice@example.com", []byte("users/notanumber"))
	store.addIndexEntry("users.Email", "alice@example.com", []byte("users/1"))

	results, err := dao.QueryByIndex(ctx, "Email", "alice@example.com")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, uint64(1), results[0].ID)
}

// TestGenericDAO_QueryByIndex_PropagatesStoreError verifies that
// non-not-found errors from the index store are propagated.
func TestGenericDAO_QueryByIndex_PropagatesStoreError(t *testing.T) {
	queryErr := errors.New("index query failure")
	store := newErrorIndexAwareKVStore(queryErr, nil)
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	results, err := dao.QueryByIndex(ctx, "Email", "test@example.com")
	require.Error(t, err)
	assert.Nil(t, results)
	assert.Equal(t, queryErr, err)
}

// ---------------------------------------------------------------------------
// QueryOneByIndex tests
// ---------------------------------------------------------------------------

// TestGenericDAO_QueryOneByIndex_FindsEntity verifies that
// QueryOneByIndex returns a single matching entity.
func TestGenericDAO_QueryOneByIndex_FindsEntity(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	entity := &indexedTestEntity{ID: 42, Name: "Dave", Email: "dave@example.com"}
	require.NoError(t, dao.Save(ctx, entity))

	store.addIndexEntry("users.Name", "Dave", []byte("users/42"))

	result, err := dao.QueryOneByIndex(ctx, "Name", "Dave")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, uint64(42), result.ID)
	assert.Equal(t, "Dave", result.Name)
	assert.Equal(t, "dave@example.com", result.Email)
}

// TestGenericDAO_QueryOneByIndex_NotFound verifies that
// QueryOneByIndex returns NotFoundError when no entities match.
func TestGenericDAO_QueryOneByIndex_NotFound(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	result, err := dao.QueryOneByIndex(ctx, "Name", "nonexistent")
	require.Error(t, err)
	assert.Nil(t, result)
	assert.True(t, IsNotFound(err))
}

// TestGenericDAO_QueryOneByIndex_ReturnsFirst verifies that
// QueryOneByIndex returns the first matching entity when multiple exist.
func TestGenericDAO_QueryOneByIndex_ReturnsFirst(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	e1 := &indexedTestEntity{ID: 1, Name: "Alice", Email: "a@example.com", Category: "admin"}
	e2 := &indexedTestEntity{ID: 2, Name: "Bob", Email: "b@example.com", Category: "admin"}
	require.NoError(t, dao.Save(ctx, e1))
	require.NoError(t, dao.Save(ctx, e2))

	store.addIndexEntry("users.Category", "admin", []byte("users/1"))
	store.addIndexEntry("users.Category", "admin", []byte("users/2"))

	result, err := dao.QueryOneByIndex(ctx, "Category", "admin")
	require.NoError(t, err)
	require.NotNil(t, result)
	// Should return the first entity from the index results.
	assert.Equal(t, uint64(1), result.ID)
}

// TestGenericDAO_QueryOneByIndex_PropagatesError verifies that errors
// from QueryByIndex propagate through QueryOneByIndex.
func TestGenericDAO_QueryOneByIndex_PropagatesError(t *testing.T) {
	queryErr := errors.New("index query failure")
	store := newErrorIndexAwareKVStore(queryErr, nil)
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	result, err := dao.QueryOneByIndex(ctx, "Email", "test@example.com")
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Equal(t, queryErr, err)
}

// ---------------------------------------------------------------------------
// ScanByIndex tests
// ---------------------------------------------------------------------------

// TestGenericDAO_ScanByIndex_FindsInRange verifies that ScanByIndex
// returns entities with indexed values within the specified range.
func TestGenericDAO_ScanByIndex_FindsInRange(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	alice := &indexedTestEntity{ID: 1, Name: "Alice", Email: "alice@example.com"}
	bob := &indexedTestEntity{ID: 2, Name: "Bob", Email: "bob@example.com"}
	carol := &indexedTestEntity{ID: 3, Name: "Carol", Email: "carol@example.com"}
	zara := &indexedTestEntity{ID: 4, Name: "Zara", Email: "zara@example.com"}
	require.NoError(t, dao.Save(ctx, alice))
	require.NoError(t, dao.Save(ctx, bob))
	require.NoError(t, dao.Save(ctx, carol))
	require.NoError(t, dao.Save(ctx, zara))

	// Index entries for Name field.
	store.addIndexEntry("users.Name", "Alice", []byte("users/1"))
	store.addIndexEntry("users.Name", "Bob", []byte("users/2"))
	store.addIndexEntry("users.Name", "Carol", []byte("users/3"))
	store.addIndexEntry("users.Name", "Zara", []byte("users/4"))

	// Scan for names in [A, D) — should find Alice, Bob, Carol.
	results, err := dao.ScanByIndex(ctx, "Name", "A", "D")
	require.NoError(t, err)
	require.Len(t, results, 3)

	ids := make([]uint64, len(results))
	for i, e := range results {
		ids[i] = e.ID
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	assert.Equal(t, []uint64{1, 2, 3}, ids)
}

// TestGenericDAO_ScanByIndex_EmptyRange verifies that ScanByIndex
// returns an empty result when no entries fall within the range.
func TestGenericDAO_ScanByIndex_EmptyRange(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	entity := &indexedTestEntity{ID: 1, Name: "Alice", Email: "alice@example.com"}
	require.NoError(t, dao.Save(ctx, entity))

	store.addIndexEntry("users.Name", "Alice", []byte("users/1"))

	// Scan for names in [X, Z) — should find nothing.
	results, err := dao.ScanByIndex(ctx, "Name", "X", "Z")
	require.NoError(t, err)
	assert.Empty(t, results)
}

// TestGenericDAO_ScanByIndex_DeduplicatesKeys verifies that ScanByIndex
// deduplicates entity keys that appear in multiple scan buckets.
func TestGenericDAO_ScanByIndex_DeduplicatesKeys(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	entity := &indexedTestEntity{ID: 1, Name: "Alice", Email: "alice@example.com"}
	require.NoError(t, dao.Save(ctx, entity))

	// Same entity key under two different field values within the range.
	store.addIndexEntry("users.Name", "Alice", []byte("users/1"))
	store.addIndexEntry("users.Name", "Alicia", []byte("users/1"))

	// Scan for names in [A, B) — both "Alice" and "Alicia" match, but
	// they reference the same entity key.
	results, err := dao.ScanByIndex(ctx, "Name", "A", "B")
	require.NoError(t, err)
	require.Len(t, results, 1, "duplicated entity keys should be deduplicated")
	assert.Equal(t, uint64(1), results[0].ID)
}

// TestGenericDAO_ScanByIndex_SkipsDeletedEntities verifies that
// ScanByIndex skips entities that have been deleted from the store.
func TestGenericDAO_ScanByIndex_SkipsDeletedEntities(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	alive := &indexedTestEntity{ID: 1, Name: "Alice", Email: "alice@example.com"}
	dead := &indexedTestEntity{ID: 2, Name: "Bob", Email: "bob@example.com"}
	require.NoError(t, dao.Save(ctx, alive))
	require.NoError(t, dao.Save(ctx, dead))
	require.NoError(t, dao.Delete(ctx, dead))

	store.addIndexEntry("users.Name", "Alice", []byte("users/1"))
	store.addIndexEntry("users.Name", "Bob", []byte("users/2"))

	results, err := dao.ScanByIndex(ctx, "Name", "A", "C")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, uint64(1), results[0].ID)
}

// TestGenericDAO_ScanByIndex_SkipsInvalidKeys verifies that keys with
// wrong prefixes or unparseable IDs are skipped during scan.
func TestGenericDAO_ScanByIndex_SkipsInvalidKeys(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	entity := &indexedTestEntity{ID: 1, Name: "Alice", Email: "alice@example.com"}
	require.NoError(t, dao.Save(ctx, entity))

	store.addIndexEntry("users.Name", "Alice", []byte("users/1"))
	store.addIndexEntry("users.Name", "Bad", []byte("other/1"))           // wrong prefix
	store.addIndexEntry("users.Name", "Bad2", []byte("users/notanumber")) // unparseable ID

	results, err := dao.ScanByIndex(ctx, "Name", "A", "C")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, uint64(1), results[0].ID)
}

// TestGenericDAO_ScanByIndex_PropagatesStoreError verifies that errors
// from the scan operation are propagated.
func TestGenericDAO_ScanByIndex_PropagatesStoreError(t *testing.T) {
	scanErr := errors.New("index scan failure")
	store := newErrorIndexAwareKVStore(nil, scanErr)
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	results, err := dao.ScanByIndex(ctx, "Name", "A", "Z")
	require.Error(t, err)
	assert.Nil(t, results)
	assert.Equal(t, scanErr, err)
}

// TestGenericDAO_ScanByIndex_NoIndexData verifies that ScanByIndex
// returns nil when the index has no data for the given index name.
func TestGenericDAO_ScanByIndex_NoIndexData(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	results, err := dao.ScanByIndex(ctx, "Name", "A", "Z")
	require.NoError(t, err)
	assert.Empty(t, results)
}

// ---------------------------------------------------------------------------
// jsonFieldName tests
// ---------------------------------------------------------------------------

// TestJsonFieldName_WithJsonTag verifies that jsonFieldName returns
// the JSON tag name for a field with a json tag.
func TestJsonFieldName_WithJsonTag(t *testing.T) {
	typ := reflect.TypeOf(jsonTagVariantEntity{})
	result := jsonFieldName(typ, "WithTag")
	assert.Equal(t, "custom_name", result)
}

// TestJsonFieldName_WithOmitempty verifies that jsonFieldName strips
// the omitempty option and returns just the field name portion.
func TestJsonFieldName_WithOmitempty(t *testing.T) {
	typ := reflect.TypeOf(jsonTagVariantEntity{})
	result := jsonFieldName(typ, "WithOmitempty")
	assert.Equal(t, "omit_field", result)
}

// TestJsonFieldName_NoJsonTag verifies that jsonFieldName falls back
// to the Go field name when no json tag is present.
func TestJsonFieldName_NoJsonTag(t *testing.T) {
	typ := reflect.TypeOf(jsonTagVariantEntity{})
	result := jsonFieldName(typ, "NoTag")
	assert.Equal(t, "NoTag", result)
}

// TestJsonFieldName_DashTag verifies that jsonFieldName returns the
// Go field name when the json tag is "-".
func TestJsonFieldName_DashTag(t *testing.T) {
	typ := reflect.TypeOf(jsonTagVariantEntity{})
	result := jsonFieldName(typ, "DashTag")
	assert.Equal(t, "DashTag", result)
}

// TestJsonFieldName_EmptyTag verifies that jsonFieldName returns the
// Go field name when the json tag value is empty.
func TestJsonFieldName_EmptyTag(t *testing.T) {
	typ := reflect.TypeOf(jsonTagVariantEntity{})
	result := jsonFieldName(typ, "EmptyTag")
	assert.Equal(t, "EmptyTag", result)
}

// TestJsonFieldName_NonexistentField verifies that jsonFieldName returns
// the provided field name when the field does not exist on the struct.
func TestJsonFieldName_NonexistentField(t *testing.T) {
	typ := reflect.TypeOf(jsonTagVariantEntity{})
	result := jsonFieldName(typ, "DoesNotExist")
	assert.Equal(t, "DoesNotExist", result)
}

// TestJsonFieldName_IndexedEntity verifies jsonFieldName with the
// indexed test entity's json tags.
func TestJsonFieldName_IndexedEntity(t *testing.T) {
	typ := reflect.TypeOf(indexedTestEntity{})

	t.Run("Name", func(t *testing.T) {
		assert.Equal(t, "name", jsonFieldName(typ, "Name"))
	})

	t.Run("Email", func(t *testing.T) {
		assert.Equal(t, "email", jsonFieldName(typ, "Email"))
	})

	t.Run("Category", func(t *testing.T) {
		assert.Equal(t, "category", jsonFieldName(typ, "Category"))
	})

	t.Run("ID", func(t *testing.T) {
		assert.Equal(t, "id", jsonFieldName(typ, "ID"))
	})
}

// ---------------------------------------------------------------------------
// PageByIndex tests
// ---------------------------------------------------------------------------

// TestGenericDAO_PageByIndex_SinglePage verifies PageByIndex returns
// a single page of matching entities.
func TestGenericDAO_PageByIndex_SinglePage(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	e1 := &indexedTestEntity{ID: 1, Name: "Alice", Email: "alice@example.com", Category: "admin"}
	e2 := &indexedTestEntity{ID: 2, Name: "Bob", Email: "bob@example.com", Category: "admin"}
	require.NoError(t, dao.Save(ctx, e1))
	require.NoError(t, dao.Save(ctx, e2))

	store.addIndexEntry("users.Category", "admin", []byte("users/1"))
	store.addIndexEntry("users.Category", "admin", []byte("users/2"))

	result, err := dao.PageByIndex(ctx, "Category", "admin", PageQuery{Page: 1, PageSize: 10})
	require.NoError(t, err)
	assert.Len(t, result.Entities, 2)
	assert.Equal(t, 2, result.Total)
	assert.False(t, result.HasMore)
}

// TestGenericDAO_PageByIndex_MultiPage verifies PageByIndex pagination
// across multiple pages.
func TestGenericDAO_PageByIndex_MultiPage(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	for i := uint64(1); i <= 5; i++ {
		e := &indexedTestEntity{ID: i, Name: "User", Email: "u@x.com", Category: "admin"}
		require.NoError(t, dao.Save(ctx, e))
		store.addIndexEntry("users.Category", "admin", []byte(fmt.Sprintf("users/%d", i)))
	}

	// Page 1
	result, err := dao.PageByIndex(ctx, "Category", "admin", PageQuery{Page: 1, PageSize: 2})
	require.NoError(t, err)
	assert.Len(t, result.Entities, 2)
	assert.Equal(t, 5, result.Total)
	assert.True(t, result.HasMore)

	// Page 3 (last page)
	result, err = dao.PageByIndex(ctx, "Category", "admin", PageQuery{Page: 3, PageSize: 2})
	require.NoError(t, err)
	assert.Len(t, result.Entities, 1)
	assert.False(t, result.HasMore)
}

// TestGenericDAO_PageByIndex_EmptyResult verifies PageByIndex returns
// empty results when no entities match.
func TestGenericDAO_PageByIndex_EmptyResult(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	result, err := dao.PageByIndex(ctx, "Category", "nonexistent", PageQuery{Page: 1, PageSize: 10})
	require.NoError(t, err)
	assert.Empty(t, result.Entities)
	assert.Equal(t, 0, result.Total)
	assert.False(t, result.HasMore)
}

// TestGenericDAO_PageByIndex_WithSortField verifies that PageByIndex
// respects the SortField parameter.
func TestGenericDAO_PageByIndex_WithSortField(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	e1 := &indexedTestEntity{ID: 1, Name: "Zara", Email: "z@x.com", Category: "admin"}
	e2 := &indexedTestEntity{ID: 2, Name: "Alice", Email: "a@x.com", Category: "admin"}
	require.NoError(t, dao.Save(ctx, e1))
	require.NoError(t, dao.Save(ctx, e2))

	store.addIndexEntry("users.Category", "admin", []byte("users/1"))
	store.addIndexEntry("users.Category", "admin", []byte("users/2"))

	result, err := dao.PageByIndex(ctx, "Category", "admin", PageQuery{
		Page: 1, PageSize: 10, SortField: "Name", SortOrder: SortAscending,
	})
	require.NoError(t, err)
	require.Len(t, result.Entities, 2)
	assert.Equal(t, "Alice", result.Entities[0].Name)
	assert.Equal(t, "Zara", result.Entities[1].Name)
}

// ---------------------------------------------------------------------------
// CountByIndex tests
// ---------------------------------------------------------------------------

// TestGenericDAO_CountByIndex_NonZero verifies CountByIndex returns
// the correct count for matching entities.
func TestGenericDAO_CountByIndex_NonZero(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	store.addIndexEntry("users.Category", "admin", []byte("users/1"))
	store.addIndexEntry("users.Category", "admin", []byte("users/2"))
	store.addIndexEntry("users.Category", "admin", []byte("users/3"))

	ctx := context.Background()
	count, err := dao.CountByIndex(ctx, "Category", "admin")
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}

// TestGenericDAO_CountByIndex_Zero verifies CountByIndex returns 0
// when no entities match.
func TestGenericDAO_CountByIndex_Zero(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	count, err := dao.CountByIndex(ctx, "Category", "nonexistent")
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

// TestGenericDAO_CountByIndex_PropagatesError verifies that errors
// from the index query are propagated.
func TestGenericDAO_CountByIndex_PropagatesError(t *testing.T) {
	queryErr := errors.New("index query failure")
	store := newErrorIndexAwareKVStore(queryErr, nil)
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	count, err := dao.CountByIndex(ctx, "Category", "admin")
	require.Error(t, err)
	assert.Equal(t, 0, count)
	assert.Equal(t, queryErr, err)
}

// ---------------------------------------------------------------------------
// SortField in Page() tests
// ---------------------------------------------------------------------------

// TestGenericDAO_Page_SortField_Ascending verifies Page sorts by field
// in ascending order.
func TestGenericDAO_Page_SortField_Ascending(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, dao.Save(ctx, &indexedTestEntity{ID: 1, Name: "Zara", Email: "z@x.com"}))
	require.NoError(t, dao.Save(ctx, &indexedTestEntity{ID: 2, Name: "Alice", Email: "a@x.com"}))
	require.NoError(t, dao.Save(ctx, &indexedTestEntity{ID: 3, Name: "Mike", Email: "m@x.com"}))

	result, err := dao.Page(ctx, PageQuery{Page: 1, PageSize: 10, SortField: "Name", SortOrder: SortAscending})
	require.NoError(t, err)
	require.Len(t, result.Entities, 3)
	assert.Equal(t, "Alice", result.Entities[0].Name)
	assert.Equal(t, "Mike", result.Entities[1].Name)
	assert.Equal(t, "Zara", result.Entities[2].Name)
}

// TestGenericDAO_Page_SortField_Descending verifies Page sorts by field
// in descending order.
func TestGenericDAO_Page_SortField_Descending(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, dao.Save(ctx, &indexedTestEntity{ID: 1, Name: "Zara", Email: "z@x.com"}))
	require.NoError(t, dao.Save(ctx, &indexedTestEntity{ID: 2, Name: "Alice", Email: "a@x.com"}))
	require.NoError(t, dao.Save(ctx, &indexedTestEntity{ID: 3, Name: "Mike", Email: "m@x.com"}))

	result, err := dao.Page(ctx, PageQuery{Page: 1, PageSize: 10, SortField: "Name", SortOrder: SortDescending})
	require.NoError(t, err)
	require.Len(t, result.Entities, 3)
	assert.Equal(t, "Zara", result.Entities[0].Name)
	assert.Equal(t, "Mike", result.Entities[1].Name)
	assert.Equal(t, "Alice", result.Entities[2].Name)
}

// ---------------------------------------------------------------------------
// End-to-end integration tests
// ---------------------------------------------------------------------------

// TestGenericDAO_IndexWorkflow_SaveQueryDelete verifies the full
// lifecycle of saving an entity, querying by index, and deleting.
func TestGenericDAO_IndexWorkflow_SaveQueryDelete(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	// Save entity.
	entity := &indexedTestEntity{ID: 100, Name: "Eve", Email: "eve@example.com", Category: "user"}
	require.NoError(t, dao.Save(ctx, entity))

	// Configure index entry.
	store.addIndexEntry("users.Email", "eve@example.com", []byte("users/100"))

	// Query should find the entity.
	result, err := dao.QueryOneByIndex(ctx, "Email", "eve@example.com")
	require.NoError(t, err)
	assert.Equal(t, uint64(100), result.ID)

	// Delete entity.
	require.NoError(t, dao.Delete(ctx, entity))

	// Query should skip the deleted entity (not found).
	_, err = dao.QueryOneByIndex(ctx, "Email", "eve@example.com")
	require.Error(t, err)
	assert.True(t, IsNotFound(err))
}

// TestGenericDAO_IndexWorkflow_MixedQueries verifies that different
// index query methods work correctly on the same DAO instance.
func TestGenericDAO_IndexWorkflow_MixedQueries(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"items",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	// Save entities.
	for i := uint64(1); i <= 5; i++ {
		e := &indexedTestEntity{
			ID:       i,
			Name:     string(rune('A'-1+int(i))) + "_item",
			Email:    string(rune('a'-1+int(i))) + "@example.com",
			Category: "cat",
		}
		require.NoError(t, dao.Save(ctx, e))
		store.addIndexEntry("items.Category", "cat", []byte("items/"+string(rune('0'+i))))
		store.addIndexEntry("items.Name", e.Name, []byte("items/"+string(rune('0'+i))))
	}

	// QueryByIndex — all items with category "cat".
	allItems, err := dao.QueryByIndex(ctx, "Category", "cat")
	require.NoError(t, err)
	assert.Len(t, allItems, 5)

	// QueryOneByIndex — single item by name.
	oneItem, err := dao.QueryOneByIndex(ctx, "Name", "A_item")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), oneItem.ID)

	// ScanByIndex — items in name range.
	store.addIndexEntry("items.Name", "F_item", []byte("items/6")) // Out of store, will be skipped
	rangeItems, err := dao.ScanByIndex(ctx, "Name", "A", "D")
	require.NoError(t, err)
	// A_item, B_item, C_item match the range [A, D)
	assert.Len(t, rangeItems, 3)
}

// TestGenericDAO_QueryByIndex_IndexNameFormat verifies that the index
// name is constructed as entityType.fieldName.
func TestGenericDAO_QueryByIndex_IndexNameFormat(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"my_entities",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	entity := &indexedTestEntity{ID: 1, Name: "Test", Email: "test@example.com"}
	require.NoError(t, dao.Save(ctx, entity))

	// The DAO constructs index name as "my_entities.Email".
	store.addIndexEntry("my_entities.Email", "test@example.com", []byte("my_entities/1"))

	results, err := dao.QueryByIndex(ctx, "Email", "test@example.com")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, uint64(1), results[0].ID)
}

// TestGenericDAO_QueryByIndex_ValueConversion verifies that query values
// are converted to strings via fmt.Sprint for index lookup.
func TestGenericDAO_QueryByIndex_ValueConversion(t *testing.T) {
	store := newIndexAwareMemoryKVStore()
	dao, err := New[*indexedTestEntity](
		store,
		"users",
		func() *indexedTestEntity { return &indexedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	entity := &indexedTestEntity{ID: 1, Name: "42", Email: "test@example.com"}
	require.NoError(t, dao.Save(ctx, entity))

	// Query with integer value — fmt.Sprint(42) = "42".
	store.addIndexEntry("users.Name", "42", []byte("users/1"))

	results, err := dao.QueryByIndex(ctx, "Name", 42)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, uint64(1), results[0].ID)
}
