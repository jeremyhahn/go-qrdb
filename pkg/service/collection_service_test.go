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

package service

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// In-memory KV service for collection tests
// ---------------------------------------------------------------------------

type memoryKVSvc struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func newMemoryKVSvc() *memoryKVSvc {
	return &memoryKVSvc{data: make(map[string][]byte)}
}

func (m *memoryKVSvc) Put(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	m.data[key] = cp
	return nil
}

func (m *memoryKVSvc) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[key]
	if !ok {
		return nil, &KeyNotFoundError{Key: key}
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (m *memoryKVSvc) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memoryKVSvc) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var keys []string
	for k := range m.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (m *memoryKVSvc) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string][]byte)
	for k, v := range m.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			cp := make([]byte, len(v))
			copy(cp, v)
			result[k] = cp
		}
	}
	return result, nil
}

func (m *memoryKVSvc) PutWithMode(ctx context.Context, key string, value []byte, _ writemode.Mode) error {
	return m.Put(ctx, key, value)
}

func (m *memoryKVSvc) DeleteWithMode(ctx context.Context, key string, _ writemode.Mode) error {
	return m.Delete(ctx, key)
}

func (m *memoryKVSvc) GetWithConsistency(ctx context.Context, key string, _ consistency.ReadOptions) ([]byte, error) {
	return m.Get(ctx, key)
}

func (m *memoryKVSvc) ListWithConsistency(ctx context.Context, prefix string, _ consistency.ReadOptions) ([]string, error) {
	return m.List(ctx, prefix)
}

func (m *memoryKVSvc) ScanWithConsistency(ctx context.Context, prefix string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return m.Scan(ctx, prefix)
}

// ---------------------------------------------------------------------------
// In-memory index service for collection tests
// ---------------------------------------------------------------------------

type memoryIndexSvc struct {
	mu      sync.Mutex
	indexes map[string]index.IndexConfig // keyed by index name
}

func newMemoryIndexSvc() *memoryIndexSvc {
	return &memoryIndexSvc{indexes: make(map[string]index.IndexConfig)}
}

func (m *memoryIndexSvc) CreateIndex(_ context.Context, name, _ string, config index.IndexConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.indexes[name] = config
	return nil
}

func (m *memoryIndexSvc) DropIndex(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.indexes, name)
	return nil
}

func newTestCollectionService() *CollectionService {
	return NewCollectionService(newMemoryKVSvc())
}

func newTestCollectionServiceWithIndex() (*CollectionService, *memoryIndexSvc) {
	idxSvc := newMemoryIndexSvc()
	return NewCollectionServiceWithIndex(newMemoryKVSvc(), idxSvc), idxSvc
}

// ---------------------------------------------------------------------------
// CreateCollection tests
// ---------------------------------------------------------------------------

func TestCollectionService_CreateCollection(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	info, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "users",
		Database: "mydb",
	})
	require.NoError(t, err)
	assert.Equal(t, "users", info.Name)
	assert.Equal(t, "mydb", info.Database)
	assert.Equal(t, IDStrategyUnixNano, info.IDStrategy)
	assert.NotZero(t, info.CreatedAt)
}

func TestCollectionService_CreateCollection_WithIncrement(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	info, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:       "orders",
		Database:   "mydb",
		IDStrategy: IDStrategyIncrement,
	})
	require.NoError(t, err)
	assert.Equal(t, IDStrategyIncrement, info.IDStrategy)
}

func TestCollectionService_CreateCollection_WithFieldHash(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	info, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:       "users",
		Database:   "mydb",
		IDStrategy: IDStrategyFieldHash,
		FieldHash:  "email",
	})
	require.NoError(t, err)
	assert.Equal(t, IDStrategyFieldHash, info.IDStrategy)
	assert.Equal(t, "email", info.FieldHash)
}

func TestCollectionService_CreateCollection_AlreadyExists(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb",
	})
	require.NoError(t, err)

	_, err = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb",
	})
	assert.IsType(t, &CollectionAlreadyExistsError{}, err)
}

func TestCollectionService_CreateCollection_EmptyName(t *testing.T) {
	svc := newTestCollectionService()
	_, err := svc.CreateCollection(context.Background(), &CreateCollectionRequest{
		Name: "", Database: "mydb",
	})
	assert.IsType(t, &CollectionEmptyNameError{}, err)
}

func TestCollectionService_CreateCollection_EmptyDatabase(t *testing.T) {
	svc := newTestCollectionService()
	_, err := svc.CreateCollection(context.Background(), &CreateCollectionRequest{
		Name: "users", Database: "",
	})
	assert.IsType(t, &CollectionEmptyDatabaseError{}, err)
}

func TestCollectionService_CreateCollection_InvalidStrategy(t *testing.T) {
	svc := newTestCollectionService()
	_, err := svc.CreateCollection(context.Background(), &CreateCollectionRequest{
		Name: "users", Database: "mydb", IDStrategy: "random",
	})
	assert.IsType(t, &CollectionInvalidIDStrategyError{}, err)
}

func TestCollectionService_CreateCollection_FieldHashMissingField(t *testing.T) {
	svc := newTestCollectionService()
	_, err := svc.CreateCollection(context.Background(), &CreateCollectionRequest{
		Name: "users", Database: "mydb", IDStrategy: IDStrategyFieldHash,
	})
	assert.IsType(t, &CollectionMissingFieldHashError{}, err)
}

// ---------------------------------------------------------------------------
// DropCollection tests
// ---------------------------------------------------------------------------

func TestCollectionService_DropCollection(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb",
	})
	require.NoError(t, err)

	// Insert a document first.
	_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "users", Database: "mydb",
		Document: map[string]interface{}{"name": "Alice"},
	})
	require.NoError(t, err)

	err = svc.DropCollection(ctx, &DropCollectionRequest{
		Name: "users", Database: "mydb",
	})
	require.NoError(t, err)

	// Verify collection is gone.
	_, err = svc.GetCollectionInfo(ctx, "mydb", "users")
	assert.IsType(t, &CollectionNotFoundError{}, err)
}

func TestCollectionService_DropCollection_NotFound(t *testing.T) {
	svc := newTestCollectionService()
	err := svc.DropCollection(context.Background(), &DropCollectionRequest{
		Name: "nope", Database: "mydb",
	})
	assert.IsType(t, &CollectionNotFoundError{}, err)
}

func TestCollectionService_DropCollection_EmptyName(t *testing.T) {
	svc := newTestCollectionService()
	err := svc.DropCollection(context.Background(), &DropCollectionRequest{
		Name: "", Database: "mydb",
	})
	assert.IsType(t, &CollectionEmptyNameError{}, err)
}

func TestCollectionService_DropCollection_EmptyDatabase(t *testing.T) {
	svc := newTestCollectionService()
	err := svc.DropCollection(context.Background(), &DropCollectionRequest{
		Name: "users", Database: "",
	})
	assert.IsType(t, &CollectionEmptyDatabaseError{}, err)
}

// ---------------------------------------------------------------------------
// ListCollections tests
// ---------------------------------------------------------------------------

func TestCollectionService_ListCollections(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb",
	})
	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "orders", Database: "mydb",
	})

	resp, err := svc.ListCollections(ctx, &ListCollectionsRequest{Database: "mydb"})
	require.NoError(t, err)
	assert.Len(t, resp.Collections, 2)
}

func TestCollectionService_ListCollections_Empty(t *testing.T) {
	svc := newTestCollectionService()
	resp, err := svc.ListCollections(context.Background(), &ListCollectionsRequest{
		Database: "emptydb",
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Collections)
}

func TestCollectionService_ListCollections_EmptyDatabase(t *testing.T) {
	svc := newTestCollectionService()
	_, err := svc.ListCollections(context.Background(), &ListCollectionsRequest{
		Database: "",
	})
	assert.IsType(t, &CollectionEmptyDatabaseError{}, err)
}

func TestCollectionService_ListCollections_IsolatedByDatabase(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "db1",
	})
	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "db2",
	})

	resp1, err := svc.ListCollections(ctx, &ListCollectionsRequest{Database: "db1"})
	require.NoError(t, err)
	assert.Len(t, resp1.Collections, 1)

	resp2, err := svc.ListCollections(ctx, &ListCollectionsRequest{Database: "db2"})
	require.NoError(t, err)
	assert.Len(t, resp2.Collections, 1)
}

// ---------------------------------------------------------------------------
// GetCollectionInfo tests
// ---------------------------------------------------------------------------

func TestCollectionService_GetCollectionInfo(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb",
	})

	info, err := svc.GetCollectionInfo(ctx, "mydb", "users")
	require.NoError(t, err)
	assert.Equal(t, "users", info.Name)
	assert.Equal(t, "mydb", info.Database)
	assert.Equal(t, int64(0), info.DocumentCount)
}

func TestCollectionService_GetCollectionInfo_NotFound(t *testing.T) {
	svc := newTestCollectionService()
	_, err := svc.GetCollectionInfo(context.Background(), "mydb", "nope")
	assert.IsType(t, &CollectionNotFoundError{}, err)
}

func TestCollectionService_GetCollectionInfo_EmptyName(t *testing.T) {
	svc := newTestCollectionService()
	_, err := svc.GetCollectionInfo(context.Background(), "mydb", "")
	assert.IsType(t, &CollectionEmptyNameError{}, err)
}

func TestCollectionService_GetCollectionInfo_EmptyDatabase(t *testing.T) {
	svc := newTestCollectionService()
	_, err := svc.GetCollectionInfo(context.Background(), "", "users")
	assert.IsType(t, &CollectionEmptyDatabaseError{}, err)
}

// ---------------------------------------------------------------------------
// InsertDocument tests
// ---------------------------------------------------------------------------

func TestCollectionService_InsertDocument(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb",
	})

	resp, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "users",
		Database:   "mydb",
		Document:   map[string]interface{}{"name": "Alice", "email": "alice@example.com"},
	})
	require.NoError(t, err)
	assert.NotZero(t, resp.ID)
}

func TestCollectionService_InsertDocument_FieldHash(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb",
		IDStrategy: IDStrategyFieldHash, FieldHash: "email",
	})

	resp1, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "users", Database: "mydb",
		Document: map[string]interface{}{"email": "alice@example.com"},
	})
	require.NoError(t, err)

	// Same email should produce same ID (deterministic).
	resp2, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "users", Database: "mydb",
		Document: map[string]interface{}{"email": "alice@example.com"},
	})
	require.NoError(t, err)
	assert.Equal(t, resp1.ID, resp2.ID)
}

func TestCollectionService_InsertDocument_Increment(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "orders", Database: "mydb",
		IDStrategy: IDStrategyIncrement,
	})

	resp1, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "orders", Database: "mydb",
		Document: map[string]interface{}{"item": "widget"},
	})
	require.NoError(t, err)

	resp2, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "orders", Database: "mydb",
		Document: map[string]interface{}{"item": "gadget"},
	})
	require.NoError(t, err)

	// Sequential IDs.
	assert.Equal(t, resp1.ID+1, resp2.ID)
}

func TestCollectionService_InsertDocument_EmptyDocument(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb",
	})

	_, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "users", Database: "mydb",
		Document: nil,
	})
	assert.IsType(t, &CollectionEmptyDocumentError{}, err)
}

func TestCollectionService_InsertDocument_CollectionNotFound(t *testing.T) {
	svc := newTestCollectionService()
	_, err := svc.InsertDocument(context.Background(), &InsertDocumentRequest{
		Collection: "nope", Database: "mydb",
		Document: map[string]interface{}{"x": "y"},
	})
	assert.IsType(t, &CollectionNotFoundError{}, err)
}

func TestCollectionService_InsertDocument_EmptyName(t *testing.T) {
	svc := newTestCollectionService()
	_, err := svc.InsertDocument(context.Background(), &InsertDocumentRequest{
		Collection: "", Database: "mydb",
		Document: map[string]interface{}{"x": "y"},
	})
	assert.IsType(t, &CollectionEmptyNameError{}, err)
}

// ---------------------------------------------------------------------------
// GetDocument tests
// ---------------------------------------------------------------------------

func TestCollectionService_GetDocument(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb",
	})

	insertResp, _ := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "users", Database: "mydb",
		Document: map[string]interface{}{"name": "Bob"},
	})

	getResp, err := svc.GetDocument(ctx, &GetDocumentRequest{
		Collection: "users", Database: "mydb", ID: insertResp.ID,
	})
	require.NoError(t, err)
	assert.Equal(t, insertResp.ID, getResp.ID)
	assert.Equal(t, "Bob", getResp.Document["name"])
}

func TestCollectionService_GetDocument_NotFound(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb",
	})

	_, err := svc.GetDocument(ctx, &GetDocumentRequest{
		Collection: "users", Database: "mydb", ID: 999,
	})
	assert.IsType(t, &CollectionDocumentNotFoundError{}, err)
}

func TestCollectionService_GetDocument_ZeroID(t *testing.T) {
	svc := newTestCollectionService()
	_, err := svc.GetDocument(context.Background(), &GetDocumentRequest{
		Collection: "users", Database: "mydb", ID: 0,
	})
	assert.IsType(t, &CollectionInvalidDocumentIDError{}, err)
}

func TestCollectionService_GetDocument_EmptyCollection(t *testing.T) {
	svc := newTestCollectionService()
	_, err := svc.GetDocument(context.Background(), &GetDocumentRequest{
		Collection: "", Database: "mydb", ID: 1,
	})
	assert.IsType(t, &CollectionEmptyNameError{}, err)
}

// ---------------------------------------------------------------------------
// UpdateDocument tests
// ---------------------------------------------------------------------------

func TestCollectionService_UpdateDocument(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb",
	})

	insertResp, _ := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "users", Database: "mydb",
		Document: map[string]interface{}{"name": "Alice"},
	})

	updateResp, err := svc.UpdateDocument(ctx, &UpdateDocumentRequest{
		Collection: "users", Database: "mydb", ID: insertResp.ID,
		Document: map[string]interface{}{"name": "Alice Updated", "age": float64(30)},
	})
	require.NoError(t, err)
	assert.Equal(t, insertResp.ID, updateResp.ID)

	// Verify updated content.
	getResp, _ := svc.GetDocument(ctx, &GetDocumentRequest{
		Collection: "users", Database: "mydb", ID: insertResp.ID,
	})
	assert.Equal(t, "Alice Updated", getResp.Document["name"])
	assert.Equal(t, float64(30), getResp.Document["age"])
}

func TestCollectionService_UpdateDocument_NotFound(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb",
	})

	_, err := svc.UpdateDocument(ctx, &UpdateDocumentRequest{
		Collection: "users", Database: "mydb", ID: 999,
		Document: map[string]interface{}{"x": "y"},
	})
	assert.IsType(t, &CollectionDocumentNotFoundError{}, err)
}

func TestCollectionService_UpdateDocument_ZeroID(t *testing.T) {
	svc := newTestCollectionService()
	_, err := svc.UpdateDocument(context.Background(), &UpdateDocumentRequest{
		Collection: "users", Database: "mydb", ID: 0,
		Document: map[string]interface{}{"x": "y"},
	})
	assert.IsType(t, &CollectionInvalidDocumentIDError{}, err)
}

func TestCollectionService_UpdateDocument_EmptyDocument(t *testing.T) {
	svc := newTestCollectionService()
	_, err := svc.UpdateDocument(context.Background(), &UpdateDocumentRequest{
		Collection: "users", Database: "mydb", ID: 1,
		Document: nil,
	})
	assert.IsType(t, &CollectionEmptyDocumentError{}, err)
}

// ---------------------------------------------------------------------------
// DeleteDocument tests
// ---------------------------------------------------------------------------

func TestCollectionService_DeleteDocument(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb",
	})

	insertResp, _ := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "users", Database: "mydb",
		Document: map[string]interface{}{"name": "Charlie"},
	})

	delResp, err := svc.DeleteDocument(ctx, &DeleteDocumentRequest{
		Collection: "users", Database: "mydb", ID: insertResp.ID,
	})
	require.NoError(t, err)
	assert.True(t, delResp.Deleted)

	// Verify document is gone.
	_, err = svc.GetDocument(ctx, &GetDocumentRequest{
		Collection: "users", Database: "mydb", ID: insertResp.ID,
	})
	assert.IsType(t, &CollectionDocumentNotFoundError{}, err)
}

func TestCollectionService_DeleteDocument_NotFound(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb",
	})

	_, err := svc.DeleteDocument(ctx, &DeleteDocumentRequest{
		Collection: "users", Database: "mydb", ID: 999,
	})
	assert.IsType(t, &CollectionDocumentNotFoundError{}, err)
}

func TestCollectionService_DeleteDocument_ZeroID(t *testing.T) {
	svc := newTestCollectionService()
	_, err := svc.DeleteDocument(context.Background(), &DeleteDocumentRequest{
		Collection: "users", Database: "mydb", ID: 0,
	})
	assert.IsType(t, &CollectionInvalidDocumentIDError{}, err)
}

// ---------------------------------------------------------------------------
// ListDocuments tests
// ---------------------------------------------------------------------------

func TestCollectionService_ListDocuments(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "items", Database: "mydb", IDStrategy: IDStrategyIncrement,
	})

	for i := 0; i < 5; i++ {
		_, _ = svc.InsertDocument(ctx, &InsertDocumentRequest{
			Collection: "items", Database: "mydb",
			Document: map[string]interface{}{"idx": float64(i)},
		})
	}

	resp, err := svc.ListDocuments(ctx, &ListDocumentsRequest{
		Collection: "items", Database: "mydb", PageSize: 3,
	})
	require.NoError(t, err)
	assert.Len(t, resp.Documents, 3)
	assert.Equal(t, int64(5), resp.TotalCount)
	assert.NotEmpty(t, resp.NextPageToken)
}

func TestCollectionService_ListDocuments_Pagination(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "items", Database: "mydb", IDStrategy: IDStrategyIncrement,
	})

	for i := 0; i < 5; i++ {
		_, _ = svc.InsertDocument(ctx, &InsertDocumentRequest{
			Collection: "items", Database: "mydb",
			Document: map[string]interface{}{"idx": float64(i)},
		})
	}

	// First page.
	resp1, err := svc.ListDocuments(ctx, &ListDocumentsRequest{
		Collection: "items", Database: "mydb", PageSize: 3,
	})
	require.NoError(t, err)
	assert.Len(t, resp1.Documents, 3)

	// Second page.
	resp2, err := svc.ListDocuments(ctx, &ListDocumentsRequest{
		Collection: "items", Database: "mydb",
		PageSize: 3, PageToken: resp1.NextPageToken,
	})
	require.NoError(t, err)
	assert.Len(t, resp2.Documents, 2)
	assert.Empty(t, resp2.NextPageToken)
}

func TestCollectionService_ListDocuments_Empty(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "empty", Database: "mydb",
	})

	resp, err := svc.ListDocuments(ctx, &ListDocumentsRequest{
		Collection: "empty", Database: "mydb",
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Documents)
	assert.Equal(t, int64(0), resp.TotalCount)
}

func TestCollectionService_ListDocuments_InvalidPageToken(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "items", Database: "mydb",
	})

	_, err := svc.ListDocuments(ctx, &ListDocumentsRequest{
		Collection: "items", Database: "mydb", PageToken: "notanumber",
	})
	assert.IsType(t, &InvalidPageTokenError{}, err)
}

func TestCollectionService_ListDocuments_EmptyCollection(t *testing.T) {
	svc := newTestCollectionService()
	_, err := svc.ListDocuments(context.Background(), &ListDocumentsRequest{
		Collection: "", Database: "mydb",
	})
	assert.IsType(t, &CollectionEmptyNameError{}, err)
}

// ---------------------------------------------------------------------------
// Multi-tenancy isolation tests
// ---------------------------------------------------------------------------

func TestCollectionService_MultiDatabase_Isolation(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	// Create same-named collection in two databases.
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "tenant1",
	})
	require.NoError(t, err)

	_, err = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "tenant2",
	})
	require.NoError(t, err)

	// Insert into tenant1.
	_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "users", Database: "tenant1",
		Document: map[string]interface{}{"name": "T1-User"},
	})
	require.NoError(t, err)

	// Insert into tenant2.
	_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "users", Database: "tenant2",
		Document: map[string]interface{}{"name": "T2-User"},
	})
	require.NoError(t, err)

	// Verify isolation.
	info1, _ := svc.GetCollectionInfo(ctx, "tenant1", "users")
	assert.Equal(t, int64(1), info1.DocumentCount)

	info2, _ := svc.GetCollectionInfo(ctx, "tenant2", "users")
	assert.Equal(t, int64(1), info2.DocumentCount)
}

// ---------------------------------------------------------------------------
// Schema auto-discovery tests
// ---------------------------------------------------------------------------

func TestCollectionService_SchemaDiscovery(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb",
	})

	_, _ = svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "users", Database: "mydb",
		Document: map[string]interface{}{
			"name":   "Alice",
			"age":    float64(30),
			"active": true,
		},
	})

	info, err := svc.GetCollectionInfo(ctx, "mydb", "users")
	require.NoError(t, err)
	assert.Equal(t, "VARCHAR", info.Schema["name"])
	assert.Equal(t, "DOUBLE", info.Schema["age"])
	assert.Equal(t, "BOOLEAN", info.Schema["active"])
}

func TestCollectionService_SchemaDiscovery_JSON(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "docs", Database: "mydb",
	})

	_, _ = svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "docs", Database: "mydb",
		Document: map[string]interface{}{
			"nested": map[string]interface{}{"a": "b"},
			"list":   []interface{}{1, 2, 3},
		},
	})

	info, err := svc.GetCollectionInfo(ctx, "mydb", "docs")
	require.NoError(t, err)
	assert.Equal(t, "JSON", info.Schema["nested"])
	assert.Equal(t, "JSON", info.Schema["list"])
}

// ---------------------------------------------------------------------------
// Validation helper tests
// ---------------------------------------------------------------------------

func TestValidateCollectionRequest(t *testing.T) {
	assert.IsType(t, &CollectionEmptyDatabaseError{}, validateCollectionRequest("", "name"))
	assert.IsType(t, &CollectionEmptyNameError{}, validateCollectionRequest("db", ""))
	assert.NoError(t, validateCollectionRequest("db", "name"))
}

func TestValidateIDStrategy(t *testing.T) {
	assert.NoError(t, validateIDStrategy(IDStrategyUnixNano, ""))
	assert.NoError(t, validateIDStrategy(IDStrategyIncrement, ""))
	assert.NoError(t, validateIDStrategy(IDStrategyFieldHash, "email"))
	assert.IsType(t, &CollectionMissingFieldHashError{}, validateIDStrategy(IDStrategyFieldHash, ""))
	assert.IsType(t, &CollectionInvalidIDStrategyError{}, validateIDStrategy("bad", ""))
}

func TestInferSQLType(t *testing.T) {
	assert.Equal(t, "VARCHAR", inferSQLType("hello"))
	assert.Equal(t, "DOUBLE", inferSQLType(float64(1.0)))
	assert.Equal(t, "BOOLEAN", inferSQLType(true))
	assert.Equal(t, "JSON", inferSQLType(map[string]interface{}{}))
	assert.Equal(t, "JSON", inferSQLType([]interface{}{}))
	assert.Equal(t, "VARCHAR", inferSQLType(42)) // int falls through to default
}

func TestSortStrings(t *testing.T) {
	s := []string{"c", "a", "b"}
	sortStrings(s)
	assert.Equal(t, []string{"a", "b", "c"}, s)
}

func TestSortStrings_Empty(t *testing.T) {
	var s []string
	sortStrings(s) // should not panic
}

func TestSortStrings_Single(t *testing.T) {
	s := []string{"only"}
	sortStrings(s)
	assert.Equal(t, []string{"only"}, s)
}

// ---------------------------------------------------------------------------
// WriteMode dispatch tests
// ---------------------------------------------------------------------------

func TestCollectionService_InsertDocument_WithWriteMode(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb",
	})

	resp, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "users",
		Database:   "mydb",
		Document:   map[string]interface{}{"name": "Dave"},
		WriteMode:  writemode.Mode(1),
	})
	require.NoError(t, err)
	assert.NotZero(t, resp.ID)
}

func TestCollectionService_UpdateDocument_WithWriteMode(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb",
	})
	ins, _ := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "users", Database: "mydb",
		Document: map[string]interface{}{"name": "Eve"},
	})

	upd, err := svc.UpdateDocument(ctx, &UpdateDocumentRequest{
		Collection: "users", Database: "mydb", ID: ins.ID,
		Document:  map[string]interface{}{"name": "Eve Updated"},
		WriteMode: writemode.Mode(1),
	})
	require.NoError(t, err)
	assert.Equal(t, ins.ID, upd.ID)
}

func TestCollectionService_DeleteDocument_WithWriteMode(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb",
	})
	ins, _ := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "users", Database: "mydb",
		Document: map[string]interface{}{"name": "Frank"},
	})

	del, err := svc.DeleteDocument(ctx, &DeleteDocumentRequest{
		Collection: "users", Database: "mydb", ID: ins.ID,
		WriteMode: writemode.Mode(1),
	})
	require.NoError(t, err)
	assert.True(t, del.Deleted)
}

// ---------------------------------------------------------------------------
// Consistency dispatch tests
// ---------------------------------------------------------------------------

func TestCollectionService_GetDocument_WithConsistency(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "users", Database: "mydb",
	})
	ins, _ := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "users", Database: "mydb",
		Document: map[string]interface{}{"name": "Grace"},
	})

	resp, err := svc.GetDocument(ctx, &GetDocumentRequest{
		Collection:  "users",
		Database:    "mydb",
		ID:          ins.ID,
		Consistency: consistency.Strong,
	})
	require.NoError(t, err)
	assert.Equal(t, "Grace", resp.Document["name"])
}

func TestCollectionService_ListDocuments_WithConsistency(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, _ = svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "items", Database: "mydb", IDStrategy: IDStrategyIncrement,
	})
	for i := 0; i < 3; i++ {
		_, _ = svc.InsertDocument(ctx, &InsertDocumentRequest{
			Collection: "items", Database: "mydb",
			Document: map[string]interface{}{"n": float64(i)},
		})
	}

	resp, err := svc.ListDocuments(ctx, &ListDocumentsRequest{
		Collection:  "items",
		Database:    "mydb",
		Consistency: consistency.Eventual,
	})
	require.NoError(t, err)
	assert.Len(t, resp.Documents, 3)
}

// ---------------------------------------------------------------------------
// NewCollectionServiceWithIndex tests
// ---------------------------------------------------------------------------

func TestNewCollectionServiceWithIndex(t *testing.T) {
	svc, _ := newTestCollectionServiceWithIndex()
	assert.NotNil(t, svc)
	assert.NotNil(t, svc.indexSvc)
}

func TestCollectionService_CreateCollection_WithIndexes(t *testing.T) {
	svc, idxSvc := newTestCollectionServiceWithIndex()
	ctx := context.Background()

	info, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "users",
		Database: "mydb",
		Indexes: []IndexDefinition{
			{FieldName: "email", Unique: true},
			{FieldName: "age", Sparse: true},
		},
	})
	require.NoError(t, err)
	assert.Len(t, info.Indexes, 2)

	// Verify indexes were registered with the index service.
	idxSvc.mu.Lock()
	defer idxSvc.mu.Unlock()
	emailIdx, ok := idxSvc.indexes["mydb.users.email"]
	require.True(t, ok)
	assert.True(t, emailIdx.Unique)

	ageIdx, ok := idxSvc.indexes["mydb.users.age"]
	require.True(t, ok)
	assert.True(t, ageIdx.Sparse)

	// A collection index definition has no case-sensitivity field, so both
	// must be created case-SENSITIVE, which is the behaviour they have always
	// had. Asserted because index.IndexConfig names the field for the DEFAULT:
	// its zero value folds case, and the engine honours the config now instead
	// of hardcoding CaseSensitive:true (go-qrdb#34). Leaving the field off this
	// literal would turn every collection index case-insensitive silently.
	assert.True(t, emailIdx.CaseSensitive,
		"collection index created case-INSENSITIVE")
	assert.True(t, ageIdx.CaseSensitive,
		"collection index created case-INSENSITIVE")
}

func TestCollectionService_CreateCollection_WithIndexes_NoIndexSvc(t *testing.T) {
	// When indexSvc is nil, indexes in the request are stored in CollectionInfo
	// but no index service calls are made.
	svc := newTestCollectionService()
	ctx := context.Background()

	info, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "users",
		Database: "mydb",
		Indexes:  []IndexDefinition{{FieldName: "email", Unique: true}},
	})
	require.NoError(t, err)
	assert.Len(t, info.Indexes, 1)
}

func TestCollectionService_CreateCollection_NoIndexes(t *testing.T) {
	svc, idxSvc := newTestCollectionServiceWithIndex()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "empty", Database: "mydb",
	})
	require.NoError(t, err)

	// No indexes should have been registered.
	idxSvc.mu.Lock()
	defer idxSvc.mu.Unlock()
	assert.Empty(t, idxSvc.indexes)
}

// ---------------------------------------------------------------------------
// Interface compliance
// ---------------------------------------------------------------------------

func TestCollectionService_ImplementsCollectionServicer(t *testing.T) {
	var _ CollectionServicer = (*CollectionService)(nil)
}

// ---------------------------------------------------------------------------
// Quota enforcement tests
// ---------------------------------------------------------------------------

// mockCollectionQuotaChecker is a test double for quota.Checker.
type mockCollectionQuotaChecker struct {
	checkFn func(ctx context.Context, tenantID string, resource quota.Resource) error
}

func (m *mockCollectionQuotaChecker) Check(ctx context.Context, tenantID string, resource quota.Resource) error {
	if m.checkFn != nil {
		return m.checkFn(ctx, tenantID, resource)
	}
	return nil
}

func (m *mockCollectionQuotaChecker) CheckN(ctx context.Context, tenantID string, resource quota.Resource, n int64) error {
	return nil
}

func (m *mockCollectionQuotaChecker) Usage(ctx context.Context, tenantID string, resource quota.Resource) (int64, error) {
	return 0, nil
}

func TestCollectionService_CreateCollection_QuotaExceeded(t *testing.T) {
	exceeded := &quota.ExceededError{
		TenantID: "acme",
		Resource: quota.ResourceCollections,
		Current:  10,
		Limit:    10,
	}
	checker := &mockCollectionQuotaChecker{
		checkFn: func(_ context.Context, _ string, _ quota.Resource) error {
			return exceeded
		},
	}

	svc := newTestCollectionService().WithCollectionQuotaChecker(checker)
	ctx := tenant.WithTenantID(context.Background(), "acme")

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "blocked",
		Database: "mydb",
	})
	if err == nil {
		t.Fatal("CreateCollection() returned nil, want *quota.ExceededError")
	}
	var qErr *quota.ExceededError
	if !errors.As(err, &qErr) {
		t.Fatalf("CreateCollection() error type = %T, want *quota.ExceededError", err)
	}
	if qErr.Resource != quota.ResourceCollections {
		t.Errorf("ExceededError.Resource = %v, want ResourceCollections", qErr.Resource)
	}
}

func TestCollectionService_CreateCollection_NoQuotaChecker(t *testing.T) {
	// Without a quota checker, creation must succeed normally.
	svc := newTestCollectionService()
	ctx := context.Background()

	info, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "noquota",
		Database: "mydb",
	})
	require.NoError(t, err)
	assert.Equal(t, "noquota", info.Name)
}

func TestCollectionService_CreateCollection_QuotaPasses(t *testing.T) {
	var checkedTenantID string
	var checkedResource quota.Resource
	checker := &mockCollectionQuotaChecker{
		checkFn: func(_ context.Context, tenantID string, resource quota.Resource) error {
			checkedTenantID = tenantID
			checkedResource = resource
			return nil // quota allows creation
		},
	}

	svc := newTestCollectionService().WithCollectionQuotaChecker(checker)
	ctx := tenant.WithTenantID(context.Background(), "acme")

	info, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "allowed",
		Database: "mydb",
	})
	require.NoError(t, err)
	assert.Equal(t, "allowed", info.Name)
	if checkedTenantID != "acme" {
		t.Errorf("quota.Check() tenantID = %q, want %q", checkedTenantID, "acme")
	}
	if checkedResource != quota.ResourceCollections {
		t.Errorf("quota.Check() resource = %v, want ResourceCollections", checkedResource)
	}
}
