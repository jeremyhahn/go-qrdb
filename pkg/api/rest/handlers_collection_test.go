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

package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock: CollectionServicer
// ---------------------------------------------------------------------------

// mockCollectionService implements service.CollectionServicer for REST
// handler testing.
type mockCollectionService struct {
	createCollectionFn  func(ctx context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error)
	dropCollectionFn    func(ctx context.Context, req *service.DropCollectionRequest) error
	listCollectionsFn   func(ctx context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error)
	getCollectionInfoFn func(ctx context.Context, database, name string) (*service.CollectionInfo, error)
	insertDocumentFn    func(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error)
	getDocumentFn       func(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error)
	updateDocumentFn    func(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error)
	deleteDocumentFn    func(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error)
	listDocumentsFn     func(ctx context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error)
}

// Compile-time interface check.
var _ service.CollectionServicer = (*mockCollectionService)(nil)

func newMockCollectionService() *mockCollectionService {
	return &mockCollectionService{}
}

func (m *mockCollectionService) CreateCollection(ctx context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	if m.createCollectionFn != nil {
		return m.createCollectionFn(ctx, req)
	}
	return &service.CollectionInfo{
		Name:       req.Name,
		Database:   req.Database,
		IDStrategy: req.IDStrategy,
		FieldHash:  req.FieldHash,
		Schema:     make(map[string]string),
		CreatedAt:  time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
	}, nil
}

func (m *mockCollectionService) DropCollection(ctx context.Context, req *service.DropCollectionRequest) error {
	if m.dropCollectionFn != nil {
		return m.dropCollectionFn(ctx, req)
	}
	return nil
}

func (m *mockCollectionService) ListCollections(ctx context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
	if m.listCollectionsFn != nil {
		return m.listCollectionsFn(ctx, req)
	}
	return &service.ListCollectionsResponse{
		Collections: []service.CollectionInfo{
			{
				Name:       "testcoll",
				Database:   req.Database,
				IDStrategy: service.IDStrategyUnixNano,
				Schema:     make(map[string]string),
				CreatedAt:  time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
			},
		},
	}, nil
}

func (m *mockCollectionService) GetCollectionInfo(ctx context.Context, database, name string) (*service.CollectionInfo, error) {
	if m.getCollectionInfoFn != nil {
		return m.getCollectionInfoFn(ctx, database, name)
	}
	return &service.CollectionInfo{
		Name:          name,
		Database:      database,
		IDStrategy:    service.IDStrategyUnixNano,
		Schema:        make(map[string]string),
		DocumentCount: 5,
		CreatedAt:     time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
	}, nil
}

func (m *mockCollectionService) InsertDocument(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
	if m.insertDocumentFn != nil {
		return m.insertDocumentFn(ctx, req)
	}
	return &service.InsertDocumentResponse{ID: 42}, nil
}

func (m *mockCollectionService) GetDocument(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
	if m.getDocumentFn != nil {
		return m.getDocumentFn(ctx, req)
	}
	return &service.GetDocumentResponse{
		ID:       req.ID,
		Document: map[string]interface{}{"name": "test"},
	}, nil
}

func (m *mockCollectionService) UpdateDocument(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
	if m.updateDocumentFn != nil {
		return m.updateDocumentFn(ctx, req)
	}
	return &service.UpdateDocumentResponse{ID: req.ID}, nil
}

// UpsertDocument reports an insert: no test that uses this double drives
// the guarded upsert, which is exercised against the real service.
func (m *mockCollectionService) UpsertDocument(_ context.Context, req *service.UpsertDocumentRequest) (*service.UpsertDocumentResponse, error) {
	return &service.UpsertDocumentResponse{ID: req.ID}, nil
}

func (m *mockCollectionService) DeleteDocument(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
	if m.deleteDocumentFn != nil {
		return m.deleteDocumentFn(ctx, req)
	}
	return &service.DeleteDocumentResponse{Deleted: true}, nil
}

func (m *mockCollectionService) ListDocuments(ctx context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
	if m.listDocumentsFn != nil {
		return m.listDocumentsFn(ctx, req)
	}
	return &service.ListDocumentsResponse{
		Documents: []service.GetDocumentResponse{
			{ID: 1, Document: map[string]interface{}{"name": "doc1"}},
			{ID: 2, Document: map[string]interface{}{"name": "doc2"}},
		},
		TotalCount: 2,
	}, nil
}

// ---------------------------------------------------------------------------
// handleCreateCollection tests
// ---------------------------------------------------------------------------

func TestHandleCreateCollection_Success(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	body := `{"name":"mycoll","database":"mydb","id_strategy":"unix_nano"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/collections", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)

	var resp collectionInfoResponse
	decodeJSON(t, rec, &resp)
	if resp.Name != "mycoll" {
		t.Fatalf("name = %q, want %q", resp.Name, "mycoll")
	}
	if resp.Database != "mydb" {
		t.Fatalf("database = %q, want %q", resp.Database, "mydb")
	}
	if resp.IDStrategy != "unix_nano" {
		t.Fatalf("id_strategy = %q, want %q", resp.IDStrategy, "unix_nano")
	}
}

func TestHandleCreateCollection_WithFieldHash(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	body := `{"name":"hashed","database":"mydb","id_strategy":"field_hash","field_hash":"email"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/collections", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)

	var resp collectionInfoResponse
	decodeJSON(t, rec, &resp)
	if resp.IDStrategy != "field_hash" {
		t.Fatalf("id_strategy = %q, want %q", resp.IDStrategy, "field_hash")
	}
	if resp.FieldHash != "email" {
		t.Fatalf("field_hash = %q, want %q", resp.FieldHash, "email")
	}
}

func TestHandleCreateCollection_InvalidJSON(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodPost, "/v1/collections", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCreateCollection_MissingName(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.createCollectionFn = func(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
		return nil, &service.CollectionEmptyNameError{}
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	body := `{"name":"","database":"mydb"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/collections", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCreateCollection_AlreadyExists(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.createCollectionFn = func(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
		return nil, &service.CollectionAlreadyExistsError{
			Database:   req.Database,
			Collection: req.Name,
		}
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	body := `{"name":"existing","database":"mydb"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/collections", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusConflict)
}

func TestHandleCreateCollection_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	body := `{"name":"mycoll","database":"mydb"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/collections", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// Route is not registered when collectionSvc is nil; call handler
	// directly to test the nil guard.
	srv.handleCreateCollection(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

// ---------------------------------------------------------------------------
// handleListCollections tests
// ---------------------------------------------------------------------------

func TestHandleListCollections_Success(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/list?database=mydb", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp listCollectionsResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Collections) != 1 {
		t.Fatalf("collections length = %d, want 1", len(resp.Collections))
	}
	if resp.Collections[0].Name != "testcoll" {
		t.Fatalf("collections[0].name = %q, want %q", resp.Collections[0].Name, "testcoll")
	}
	if resp.Collections[0].Database != "mydb" {
		t.Fatalf("collections[0].database = %q, want %q", resp.Collections[0].Database, "mydb")
	}
}

func TestHandleListCollections_EmptyResult(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.listCollectionsFn = func(_ context.Context, _ *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
		return &service.ListCollectionsResponse{
			Collections: []service.CollectionInfo{},
		}, nil
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/list?database=emptydb", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp listCollectionsResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Collections) != 0 {
		t.Fatalf("collections length = %d, want 0", len(resp.Collections))
	}
}

func TestHandleListCollections_MissingDatabase(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/list", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleListCollections_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/list", nil)
	rec := httptest.NewRecorder()
	srv.handleListCollections(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

// ---------------------------------------------------------------------------
// handleGetCollectionInfo tests
// ---------------------------------------------------------------------------

func TestHandleGetCollectionInfo_Success(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/mydb/mycoll", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp collectionInfoResponse
	decodeJSON(t, rec, &resp)
	if resp.Name != "mycoll" {
		t.Fatalf("name = %q, want %q", resp.Name, "mycoll")
	}
	if resp.Database != "mydb" {
		t.Fatalf("database = %q, want %q", resp.Database, "mydb")
	}
	if resp.DocumentCount != 5 {
		t.Fatalf("document_count = %d, want 5", resp.DocumentCount)
	}
}

func TestHandleGetCollectionInfo_NotFound(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.getCollectionInfoFn = func(_ context.Context, database, name string) (*service.CollectionInfo, error) {
		return nil, &service.CollectionNotFoundError{
			Database:   database,
			Collection: name,
		}
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/mydb/missing", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleGetCollectionInfo_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/mydb/mycoll", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

// ---------------------------------------------------------------------------
// handleDropCollection tests
// ---------------------------------------------------------------------------

func TestHandleDropCollection_Success(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodDelete, "/v1/collections/mydb/mycoll", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
}

func TestHandleDropCollection_NotFound(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.dropCollectionFn = func(_ context.Context, req *service.DropCollectionRequest) error {
		return &service.CollectionNotFoundError{
			Database:   req.Database,
			Collection: req.Name,
		}
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodDelete, "/v1/collections/mydb/missing", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleDropCollection_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodDelete, "/v1/collections/mydb/mycoll", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

// ---------------------------------------------------------------------------
// handleInsertDocument tests
// ---------------------------------------------------------------------------

func TestHandleInsertDocument_Success(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	body := `{"document":{"name":"Alice","age":30}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/collections/mydb/mycoll/documents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)

	var resp insertDocumentResponse
	decodeJSON(t, rec, &resp)
	if resp.ID != 42 {
		t.Fatalf("id = %d, want 42", resp.ID)
	}
}

func TestHandleInsertDocument_InvalidJSON(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodPost, "/v1/collections/mydb/mycoll/documents", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleInsertDocument_EmptyDocument(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.insertDocumentFn = func(_ context.Context, _ *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
		return nil, &service.CollectionEmptyDocumentError{}
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	body := `{"document":{}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/collections/mydb/mycoll/documents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleInsertDocument_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	body := `{"document":{"name":"Alice"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/collections/mydb/mycoll/documents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleInsertDocument_CollectionNotFound(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.insertDocumentFn = func(_ context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
		return nil, &service.CollectionNotFoundError{
			Database:   req.Database,
			Collection: req.Collection,
		}
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	body := `{"document":{"name":"Alice"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/collections/mydb/nocoll/documents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// handleGetDocument tests
// ---------------------------------------------------------------------------

func TestHandleGetDocument_Success(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/mydb/mycoll/documents/123", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp documentResponse
	decodeJSON(t, rec, &resp)
	if resp.ID != 123 {
		t.Fatalf("id = %d, want 123", resp.ID)
	}
	if resp.Document["name"] != "test" {
		t.Fatalf("document[name] = %v, want %q", resp.Document["name"], "test")
	}
}

func TestHandleGetDocument_InvalidID(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/mydb/mycoll/documents/abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleGetDocument_NotFound(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.getDocumentFn = func(_ context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
		return nil, &service.CollectionDocumentNotFoundError{
			Database:   req.Database,
			Collection: req.Collection,
			ID:         req.ID,
		}
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/mydb/mycoll/documents/999", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleGetDocument_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/mydb/mycoll/documents/123", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

// ---------------------------------------------------------------------------
// handleUpdateDocument tests
// ---------------------------------------------------------------------------

func TestHandleUpdateDocument_Success(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	body := `{"document":{"name":"Updated"}}`
	req := httptest.NewRequest(http.MethodPut, "/v1/collections/mydb/mycoll/documents/123", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp updateDocumentResponse
	decodeJSON(t, rec, &resp)
	if resp.ID != 123 {
		t.Fatalf("id = %d, want 123", resp.ID)
	}
}

func TestHandleUpdateDocument_InvalidJSON(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodPut, "/v1/collections/mydb/mycoll/documents/123", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleUpdateDocument_NotFound(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.updateDocumentFn = func(_ context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
		return nil, &service.CollectionDocumentNotFoundError{
			Database:   req.Database,
			Collection: req.Collection,
			ID:         req.ID,
		}
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	body := `{"document":{"name":"Updated"}}`
	req := httptest.NewRequest(http.MethodPut, "/v1/collections/mydb/mycoll/documents/999", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleUpdateDocument_InvalidID(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	body := `{"document":{"name":"Updated"}}`
	req := httptest.NewRequest(http.MethodPut, "/v1/collections/mydb/mycoll/documents/abc", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// handleDeleteDocument tests
// ---------------------------------------------------------------------------

func TestHandleDeleteDocument_Success(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodDelete, "/v1/collections/mydb/mycoll/documents/123", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp deleteDocumentResponse
	decodeJSON(t, rec, &resp)
	if !resp.Deleted {
		t.Fatal("deleted = false, want true")
	}
}

func TestHandleDeleteDocument_NotFound(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.deleteDocumentFn = func(_ context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
		return nil, &service.CollectionDocumentNotFoundError{
			Database:   req.Database,
			Collection: req.Collection,
			ID:         req.ID,
		}
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodDelete, "/v1/collections/mydb/mycoll/documents/999", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleDeleteDocument_InvalidID(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodDelete, "/v1/collections/mydb/mycoll/documents/abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleDeleteDocument_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodDelete, "/v1/collections/mydb/mycoll/documents/123", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

// ---------------------------------------------------------------------------
// handleListDocuments tests
// ---------------------------------------------------------------------------

func TestHandleListDocuments_Success(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/mydb/mycoll/documents", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp listDocumentsResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Documents) != 2 {
		t.Fatalf("documents length = %d, want 2", len(resp.Documents))
	}
	if resp.TotalCount != 2 {
		t.Fatalf("total_count = %d, want 2", resp.TotalCount)
	}
	if resp.Documents[0].ID != 1 {
		t.Fatalf("documents[0].id = %d, want 1", resp.Documents[0].ID)
	}
}

func TestHandleListDocuments_WithPagination(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.listDocumentsFn = func(_ context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
		if req.PageSize != 10 {
			t.Fatalf("page_size = %d, want 10", req.PageSize)
		}
		if req.PageToken != "20" {
			t.Fatalf("page_token = %q, want %q", req.PageToken, "20")
		}
		return &service.ListDocumentsResponse{
			Documents: []service.GetDocumentResponse{
				{ID: 21, Document: map[string]interface{}{"name": "doc21"}},
			},
			NextPageToken: "30",
			TotalCount:    50,
		}, nil
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/mydb/mycoll/documents?page_size=10&page_token=20", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp listDocumentsResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Documents) != 1 {
		t.Fatalf("documents length = %d, want 1", len(resp.Documents))
	}
	if resp.NextPageToken != "30" {
		t.Fatalf("next_page_token = %q, want %q", resp.NextPageToken, "30")
	}
	if resp.TotalCount != 50 {
		t.Fatalf("total_count = %d, want 50", resp.TotalCount)
	}
}

func TestHandleListDocuments_InvalidPageSize(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/mydb/mycoll/documents?page_size=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleListDocuments_ServiceError(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.listDocumentsFn = func(_ context.Context, _ *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
		return nil, &service.CollectionNotFoundError{
			Database:   "mydb",
			Collection: "nocoll",
		}
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/mydb/nocoll/documents", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleListDocuments_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/mydb/mycoll/documents", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

// ---------------------------------------------------------------------------
// handleCollectionByPath routing tests
// ---------------------------------------------------------------------------

func TestHandleCollectionByPath_MethodNotAllowed_Collection(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	// POST is not allowed on /v1/collections/{db}/{name} (only GET and DELETE).
	req := httptest.NewRequest(http.MethodPost, "/v1/collections/mydb/mycoll", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

func TestHandleCollectionByPath_MethodNotAllowed_Documents(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	// DELETE is not allowed on /v1/collections/{db}/{name}/documents
	// (only GET and POST).
	req := httptest.NewRequest(http.MethodDelete, "/v1/collections/mydb/mycoll/documents", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

func TestHandleCollectionByPath_MethodNotAllowed_DocumentByID(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	// POST is not allowed on /v1/collections/{db}/{name}/documents/{id}
	// (only GET, PUT, DELETE).
	req := httptest.NewRequest(http.MethodPost, "/v1/collections/mydb/mycoll/documents/123", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

func TestHandleCollectionByPath_InvalidSubResource(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/mydb/mycoll/unknown", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleCollectionByPath_MissingName(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	// Only database provided, no collection name.
	req := httptest.NewRequest(http.MethodGet, "/v1/collections/mydb", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCollectionByPath_EmptyRemainder(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	// Path that triggers handleCollectionByPath with empty parts.
	req := httptest.NewRequest(http.MethodGet, "/v1/collections//", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// collectionInfoToResponse tests
// ---------------------------------------------------------------------------

func TestCollectionInfoToResponse(t *testing.T) {
	ts := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
	info := &service.CollectionInfo{
		Name:          "mycoll",
		Database:      "mydb",
		IDStrategy:    service.IDStrategyIncrement,
		FieldHash:     "",
		Schema:        map[string]string{"name": "VARCHAR", "age": "DOUBLE"},
		DocumentCount: 42,
		CreatedAt:     ts,
	}

	resp := collectionInfoToResponse(info)

	if resp.Name != "mycoll" {
		t.Fatalf("name = %q, want %q", resp.Name, "mycoll")
	}
	if resp.Database != "mydb" {
		t.Fatalf("database = %q, want %q", resp.Database, "mydb")
	}
	if resp.IDStrategy != "increment" {
		t.Fatalf("id_strategy = %q, want %q", resp.IDStrategy, "increment")
	}
	if resp.DocumentCount != 42 {
		t.Fatalf("document_count = %d, want 42", resp.DocumentCount)
	}
	if resp.CreatedAt != "2026-03-08T12:00:00Z" {
		t.Fatalf("created_at = %q, want %q", resp.CreatedAt, "2026-03-08T12:00:00Z")
	}
	if resp.Schema["name"] != "VARCHAR" {
		t.Fatalf("schema[name] = %q, want %q", resp.Schema["name"], "VARCHAR")
	}
	if resp.Schema["age"] != "DOUBLE" {
		t.Fatalf("schema[age] = %q, want %q", resp.Schema["age"], "DOUBLE")
	}
}

func TestCollectionInfoToResponse_WithFieldHash(t *testing.T) {
	ts := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
	info := &service.CollectionInfo{
		Name:       "hashed",
		Database:   "mydb",
		IDStrategy: service.IDStrategyFieldHash,
		FieldHash:  "email",
		Schema:     make(map[string]string),
		CreatedAt:  ts,
	}

	resp := collectionInfoToResponse(info)

	if resp.FieldHash != "email" {
		t.Fatalf("field_hash = %q, want %q", resp.FieldHash, "email")
	}
}

// ---------------------------------------------------------------------------
// Request/response type serialization tests
// ---------------------------------------------------------------------------

func TestCreateCollectionRequest_JSON(t *testing.T) {
	input := `{"name":"testcoll","database":"testdb","id_strategy":"increment","field_hash":"email"}`
	var req createCollectionRequest
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Name != "testcoll" {
		t.Fatalf("name = %q, want %q", req.Name, "testcoll")
	}
	if req.Database != "testdb" {
		t.Fatalf("database = %q, want %q", req.Database, "testdb")
	}
	if req.IDStrategy != "increment" {
		t.Fatalf("id_strategy = %q, want %q", req.IDStrategy, "increment")
	}
	if req.FieldHash != "email" {
		t.Fatalf("field_hash = %q, want %q", req.FieldHash, "email")
	}
}

func TestInsertDocumentRequest_JSON(t *testing.T) {
	input := `{"document":{"name":"Alice","score":99.5}}`
	var req insertDocumentRequest
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Document["name"] != "Alice" {
		t.Fatalf("document[name] = %v, want %q", req.Document["name"], "Alice")
	}
}

func TestUpdateDocumentRequest_JSON(t *testing.T) {
	input := `{"document":{"name":"Bob"}}`
	var req updateDocumentRequest
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Document["name"] != "Bob" {
		t.Fatalf("document[name] = %v, want %q", req.Document["name"], "Bob")
	}
}

func TestCollectionInfoResponse_JSON(t *testing.T) {
	resp := collectionInfoResponse{
		Name:          "mycoll",
		Database:      "mydb",
		IDStrategy:    "unix_nano",
		Schema:        map[string]string{"name": "VARCHAR"},
		DocumentCount: 10,
		CreatedAt:     "2026-03-08T12:00:00Z",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded collectionInfoResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Name != "mycoll" {
		t.Fatalf("name = %q, want %q", decoded.Name, "mycoll")
	}
	if decoded.DocumentCount != 10 {
		t.Fatalf("document_count = %d, want 10", decoded.DocumentCount)
	}
}

func TestDocumentResponse_JSON(t *testing.T) {
	resp := documentResponse{
		ID:       42,
		Document: map[string]interface{}{"name": "Alice"},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded documentResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ID != 42 {
		t.Fatalf("id = %d, want 42", decoded.ID)
	}
	if decoded.Document["name"] != "Alice" {
		t.Fatalf("document[name] = %v, want %q", decoded.Document["name"], "Alice")
	}
}

func TestListDocumentsResponse_JSON(t *testing.T) {
	resp := listDocumentsResponse{
		Documents: []documentResponse{
			{ID: 1, Document: map[string]interface{}{"a": "b"}},
		},
		NextPageToken: "10",
		TotalCount:    100,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded listDocumentsResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.NextPageToken != "10" {
		t.Fatalf("next_page_token = %q, want %q", decoded.NextPageToken, "10")
	}
	if decoded.TotalCount != 100 {
		t.Fatalf("total_count = %d, want 100", decoded.TotalCount)
	}
}

func TestInsertDocumentResponse_JSON(t *testing.T) {
	resp := insertDocumentResponse{ID: 99}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded insertDocumentResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ID != 99 {
		t.Fatalf("id = %d, want 99", decoded.ID)
	}
}

func TestDeleteDocumentResponse_JSON(t *testing.T) {
	resp := deleteDocumentResponse{Deleted: true}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded deleteDocumentResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !decoded.Deleted {
		t.Fatal("deleted = false, want true")
	}
}

// ---------------------------------------------------------------------------
// WithCollectionService option test
// ---------------------------------------------------------------------------

func TestWithCollectionService_Option(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))
	if srv.collectionSvc == nil {
		t.Fatal("collectionSvc should not be nil after WithCollectionService")
	}
}

func TestWithCollectionService_NilOption(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(nil))
	if srv.collectionSvc != nil {
		t.Fatal("collectionSvc should be nil when WithCollectionService(nil)")
	}
}

// ---------------------------------------------------------------------------
// handleListCollections service error test
// ---------------------------------------------------------------------------

func TestHandleListCollections_ServiceError(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.listCollectionsFn = func(_ context.Context, _ *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
		return nil, &service.CollectionEmptyDatabaseError{}
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/collections/list?database=mydb", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// handleDropCollection service error test
// ---------------------------------------------------------------------------

func TestHandleDropCollection_ServiceError(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.dropCollectionFn = func(_ context.Context, _ *service.DropCollectionRequest) error {
		return &service.CollectionEmptyNameError{}
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	req := httptest.NewRequest(http.MethodDelete, "/v1/collections/mydb/mycoll", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// handleCreateCollection service validation errors
// ---------------------------------------------------------------------------

func TestHandleCreateCollection_EmptyDatabase(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.createCollectionFn = func(_ context.Context, _ *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
		return nil, &service.CollectionEmptyDatabaseError{}
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	body := `{"name":"mycoll","database":""}`
	req := httptest.NewRequest(http.MethodPost, "/v1/collections", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCreateCollection_InvalidIDStrategy(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.createCollectionFn = func(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
		return nil, &service.CollectionInvalidIDStrategyError{Strategy: req.IDStrategy}
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	body := `{"name":"mycoll","database":"mydb","id_strategy":"bad"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/collections", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// Documents trailing slash edge case
// ---------------------------------------------------------------------------

func TestHandleCollectionByPath_DocumentsTrailingSlash(t *testing.T) {
	collSvc := newMockCollectionService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCollectionService(collSvc))

	// /v1/collections/mydb/mycoll/documents/ with trailing slash should
	// still route to list documents (parts[3] == "").
	req := httptest.NewRequest(http.MethodGet, "/v1/collections/mydb/mycoll/documents/", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp listDocumentsResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Documents) != 2 {
		t.Fatalf("documents length = %d, want 2", len(resp.Documents))
	}
}
