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

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock: CollectionServicer
// ---------------------------------------------------------------------------

// mockCollectionService implements service.CollectionServicer for testing.
type mockCollectionService struct {
	createCollectionFn func(ctx context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error)
	dropCollectionFn   func(ctx context.Context, req *service.DropCollectionRequest) error
	listCollectionsFn  func(ctx context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error)
	getCollectionFn    func(ctx context.Context, database, name string) (*service.CollectionInfo, error)
	insertDocumentFn   func(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error)
	getDocumentFn      func(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error)
	updateDocumentFn   func(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error)
	deleteDocumentFn   func(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error)
	listDocumentsFn    func(ctx context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error)
}

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
		CreatedAt:  time.Now(),
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
				Name:       "users",
				Database:   req.Database,
				IDStrategy: service.IDStrategyUnixNano,
				Schema:     make(map[string]string),
				CreatedAt:  time.Now(),
			},
		},
	}, nil
}

func (m *mockCollectionService) GetCollectionInfo(ctx context.Context, database, name string) (*service.CollectionInfo, error) {
	if m.getCollectionFn != nil {
		return m.getCollectionFn(ctx, database, name)
	}
	return &service.CollectionInfo{
		Name:          name,
		Database:      database,
		IDStrategy:    service.IDStrategyUnixNano,
		Schema:        make(map[string]string),
		DocumentCount: 5,
		CreatedAt:     time.Now(),
	}, nil
}

func (m *mockCollectionService) InsertDocument(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
	if m.insertDocumentFn != nil {
		return m.insertDocumentFn(ctx, req)
	}
	return &service.InsertDocumentResponse{ID: 1001}, nil
}

func (m *mockCollectionService) GetDocument(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
	if m.getDocumentFn != nil {
		return m.getDocumentFn(ctx, req)
	}
	return &service.GetDocumentResponse{
		ID:       req.ID,
		Document: map[string]interface{}{"name": "Alice", "age": float64(30)},
	}, nil
}

func (m *mockCollectionService) UpdateDocument(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
	if m.updateDocumentFn != nil {
		return m.updateDocumentFn(ctx, req)
	}
	return &service.UpdateDocumentResponse{ID: req.ID}, nil
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
			{ID: 1, Document: map[string]interface{}{"name": "Alice"}},
			{ID: 2, Document: map[string]interface{}{"name": "Bob"}},
		},
		TotalCount: 2,
	}, nil
}

// ---------------------------------------------------------------------------
// Tool registration
// ---------------------------------------------------------------------------

func TestCollectionTools_Count(t *testing.T) {
	svc := newMockCollectionService()
	tools := collectionTools(svc)

	const expectedCount = 9
	if len(tools) != expectedCount {
		t.Fatalf("expected %d tools, got %d", expectedCount, len(tools))
	}
}

func TestCollectionTools_Names(t *testing.T) {
	svc := newMockCollectionService()
	tools := collectionTools(svc)

	expectedNames := []string{
		"qrdb_collection_create",
		"qrdb_collection_drop",
		"qrdb_collection_list",
		"qrdb_collection_info",
		"qrdb_collection_insert",
		"qrdb_collection_get",
		"qrdb_collection_update",
		"qrdb_collection_delete",
		"qrdb_collection_list_documents",
	}

	if len(tools) != len(expectedNames) {
		t.Fatalf("expected %d tools, got %d", len(expectedNames), len(tools))
	}

	for i, name := range expectedNames {
		if tools[i].Tool.Name != name {
			t.Errorf("tool[%d]: got %q, want %q", i, tools[i].Tool.Name, name)
		}
	}
}

func TestServer_WithCollectionService(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	collSvc := newMockCollectionService()

	s := NewServer(kvSvc, adminSvc, ":0", WithCollectionService(collSvc))

	if s.collectionSvc != collSvc {
		t.Error("collection service not set on server")
	}

	registeredTools := s.MCPServer().ListTools()
	collToolNames := []string{
		"qrdb_collection_create",
		"qrdb_collection_drop",
		"qrdb_collection_list",
		"qrdb_collection_info",
		"qrdb_collection_insert",
		"qrdb_collection_get",
		"qrdb_collection_update",
		"qrdb_collection_delete",
		"qrdb_collection_list_documents",
	}
	for _, name := range collToolNames {
		if _, ok := registeredTools[name]; !ok {
			t.Errorf("tool %q not registered", name)
		}
	}
}

// ---------------------------------------------------------------------------
// collection_create
// ---------------------------------------------------------------------------

func TestHandleCollectionCreate_Success(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionCreate(svc)

	req := buildCallToolRequest(t, "qrdb_collection_create", map[string]any{
		"database": "mydb",
		"name":     "users",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCollectionCreate: %v", err)
	}

	text := resultText(t, result)
	var info service.CollectionInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if info.Name != "users" {
		t.Errorf("name: got %q, want %q", info.Name, "users")
	}
	if info.Database != "mydb" {
		t.Errorf("database: got %q, want %q", info.Database, "mydb")
	}
}

func TestHandleCollectionCreate_WithIDStrategy(t *testing.T) {
	svc := newMockCollectionService()
	svc.createCollectionFn = func(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
		return &service.CollectionInfo{
			Name:       req.Name,
			Database:   req.Database,
			IDStrategy: req.IDStrategy,
			FieldHash:  req.FieldHash,
			Schema:     make(map[string]string),
			CreatedAt:  time.Now(),
		}, nil
	}
	handler := handleCollectionCreate(svc)

	req := buildCallToolRequest(t, "qrdb_collection_create", map[string]any{
		"database":    "mydb",
		"name":        "events",
		"id_strategy": "field_hash",
		"field_hash":  "event_id",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCollectionCreate: %v", err)
	}

	text := resultText(t, result)
	var info service.CollectionInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if info.IDStrategy != "field_hash" {
		t.Errorf("id_strategy: got %q, want %q", info.IDStrategy, "field_hash")
	}
	if info.FieldHash != "event_id" {
		t.Errorf("field_hash: got %q, want %q", info.FieldHash, "event_id")
	}
}

func TestHandleCollectionCreate_MissingName(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionCreate(svc)

	req := buildCallToolRequest(t, "qrdb_collection_create", map[string]any{
		"database": "mydb",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestHandleCollectionCreate_MissingDatabase(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionCreate(svc)

	req := buildCallToolRequest(t, "qrdb_collection_create", map[string]any{
		"name": "users",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing database")
	}
}

func TestHandleCollectionCreate_FieldHashMissingField(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionCreate(svc)

	req := buildCallToolRequest(t, "qrdb_collection_create", map[string]any{
		"database":    "mydb",
		"name":        "events",
		"id_strategy": "field_hash",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing field_hash with field_hash strategy")
	}
}

func TestHandleCollectionCreate_AlreadyExists(t *testing.T) {
	svc := newMockCollectionService()
	svc.createCollectionFn = func(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
		return nil, &service.CollectionAlreadyExistsError{
			Database:   req.Database,
			Collection: req.Name,
		}
	}
	handler := handleCollectionCreate(svc)

	req := buildCallToolRequest(t, "qrdb_collection_create", map[string]any{
		"database": "mydb",
		"name":     "users",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_collection_create" {
		t.Errorf("tool name: got %q, want %q", toolErr.Tool, "qrdb_collection_create")
	}

	var existsErr *service.CollectionAlreadyExistsError
	if !errors.As(err, &existsErr) {
		t.Error("expected CollectionAlreadyExistsError through ToolError chain")
	}
}

// ---------------------------------------------------------------------------
// collection_list
// ---------------------------------------------------------------------------

func TestHandleCollectionList_Success(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionList(svc)

	req := buildCallToolRequest(t, "qrdb_collection_list", map[string]any{
		"database": "mydb",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCollectionList: %v", err)
	}

	text := resultText(t, result)
	var resp service.ListCollectionsResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if len(resp.Collections) != 1 {
		t.Fatalf("expected 1 collection, got %d", len(resp.Collections))
	}
	if resp.Collections[0].Name != "users" {
		t.Errorf("name: got %q, want %q", resp.Collections[0].Name, "users")
	}
}

func TestHandleCollectionList_MissingDatabase(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionList(svc)

	req := buildCallToolRequest(t, "qrdb_collection_list", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing database")
	}
}

func TestHandleCollectionList_ServiceError(t *testing.T) {
	svc := newMockCollectionService()
	svc.listCollectionsFn = func(_ context.Context, _ *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
		return nil, &service.CollectionEmptyDatabaseError{}
	}
	handler := handleCollectionList(svc)

	req := buildCallToolRequest(t, "qrdb_collection_list", map[string]any{
		"database": "mydb",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_collection_list" {
		t.Errorf("tool name: got %q, want %q", toolErr.Tool, "qrdb_collection_list")
	}
}

// ---------------------------------------------------------------------------
// collection_info
// ---------------------------------------------------------------------------

func TestHandleCollectionInfo_Success(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionInfo(svc)

	req := buildCallToolRequest(t, "qrdb_collection_info", map[string]any{
		"database": "mydb",
		"name":     "users",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCollectionInfo: %v", err)
	}

	text := resultText(t, result)
	var info service.CollectionInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if info.Name != "users" {
		t.Errorf("name: got %q, want %q", info.Name, "users")
	}
	if info.Database != "mydb" {
		t.Errorf("database: got %q, want %q", info.Database, "mydb")
	}
	if info.DocumentCount != 5 {
		t.Errorf("document_count: got %d, want %d", info.DocumentCount, 5)
	}
}

func TestHandleCollectionInfo_MissingName(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionInfo(svc)

	req := buildCallToolRequest(t, "qrdb_collection_info", map[string]any{
		"database": "mydb",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestHandleCollectionInfo_MissingDatabase(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionInfo(svc)

	req := buildCallToolRequest(t, "qrdb_collection_info", map[string]any{
		"name": "users",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing database")
	}
}

func TestHandleCollectionInfo_NotFound(t *testing.T) {
	svc := newMockCollectionService()
	svc.getCollectionFn = func(_ context.Context, database, name string) (*service.CollectionInfo, error) {
		return nil, &service.CollectionNotFoundError{
			Database:   database,
			Collection: name,
		}
	}
	handler := handleCollectionInfo(svc)

	req := buildCallToolRequest(t, "qrdb_collection_info", map[string]any{
		"database": "mydb",
		"name":     "nonexistent",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_collection_info" {
		t.Errorf("tool name: got %q, want %q", toolErr.Tool, "qrdb_collection_info")
	}

	var notFoundErr *service.CollectionNotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Error("expected CollectionNotFoundError through ToolError chain")
	}
}

// ---------------------------------------------------------------------------
// collection_drop
// ---------------------------------------------------------------------------

func TestHandleCollectionDrop_Success(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionDrop(svc)

	req := buildCallToolRequest(t, "qrdb_collection_drop", map[string]any{
		"database": "mydb",
		"name":     "users",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCollectionDrop: %v", err)
	}

	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestHandleCollectionDrop_MissingName(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionDrop(svc)

	req := buildCallToolRequest(t, "qrdb_collection_drop", map[string]any{
		"database": "mydb",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestHandleCollectionDrop_MissingDatabase(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionDrop(svc)

	req := buildCallToolRequest(t, "qrdb_collection_drop", map[string]any{
		"name": "users",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing database")
	}
}

func TestHandleCollectionDrop_NotFound(t *testing.T) {
	svc := newMockCollectionService()
	svc.dropCollectionFn = func(_ context.Context, req *service.DropCollectionRequest) error {
		return &service.CollectionNotFoundError{
			Database:   req.Database,
			Collection: req.Name,
		}
	}
	handler := handleCollectionDrop(svc)

	req := buildCallToolRequest(t, "qrdb_collection_drop", map[string]any{
		"database": "mydb",
		"name":     "nonexistent",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_collection_drop" {
		t.Errorf("tool name: got %q, want %q", toolErr.Tool, "qrdb_collection_drop")
	}

	var notFoundErr *service.CollectionNotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Error("expected CollectionNotFoundError through ToolError chain")
	}
}

// ---------------------------------------------------------------------------
// collection_insert
// ---------------------------------------------------------------------------

func TestHandleCollectionInsertDocument_Success(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionInsert(svc)

	req := buildCallToolRequest(t, "qrdb_collection_insert", map[string]any{
		"database":   "mydb",
		"collection": "users",
		"document":   `{"name":"Alice","age":30}`,
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCollectionInsert: %v", err)
	}

	text := resultText(t, result)
	var resp service.InsertDocumentResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if resp.ID != 1001 {
		t.Errorf("id: got %d, want %d", resp.ID, 1001)
	}
}

func TestHandleCollectionInsertDocument_MissingDocument(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionInsert(svc)

	req := buildCallToolRequest(t, "qrdb_collection_insert", map[string]any{
		"database":   "mydb",
		"collection": "users",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing document")
	}
}

func TestHandleCollectionInsertDocument_MissingCollection(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionInsert(svc)

	req := buildCallToolRequest(t, "qrdb_collection_insert", map[string]any{
		"database": "mydb",
		"document": `{"name":"Alice"}`,
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing collection")
	}
}

func TestHandleCollectionInsertDocument_MissingDatabase(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionInsert(svc)

	req := buildCallToolRequest(t, "qrdb_collection_insert", map[string]any{
		"collection": "users",
		"document":   `{"name":"Alice"}`,
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing database")
	}
}

func TestHandleCollectionInsertDocument_InvalidDocumentJSON(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionInsert(svc)

	req := buildCallToolRequest(t, "qrdb_collection_insert", map[string]any{
		"database":   "mydb",
		"collection": "users",
		"document":   `{not valid json`,
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for invalid JSON document")
	}
}

func TestHandleCollectionInsertDocument_ServiceError(t *testing.T) {
	svc := newMockCollectionService()
	svc.insertDocumentFn = func(_ context.Context, _ *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
		return nil, &service.CollectionNotFoundError{
			Database:   "mydb",
			Collection: "users",
		}
	}
	handler := handleCollectionInsert(svc)

	req := buildCallToolRequest(t, "qrdb_collection_insert", map[string]any{
		"database":   "mydb",
		"collection": "users",
		"document":   `{"name":"Alice"}`,
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_collection_insert" {
		t.Errorf("tool name: got %q, want %q", toolErr.Tool, "qrdb_collection_insert")
	}
}

// ---------------------------------------------------------------------------
// collection_get
// ---------------------------------------------------------------------------

func TestHandleCollectionGetDocument_Success(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionGet(svc)

	req := buildCallToolRequest(t, "qrdb_collection_get", map[string]any{
		"database":   "mydb",
		"collection": "users",
		"id":         float64(42),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCollectionGet: %v", err)
	}

	text := resultText(t, result)
	var resp service.GetDocumentResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if resp.ID != 42 {
		t.Errorf("id: got %d, want %d", resp.ID, 42)
	}
	if resp.Document["name"] != "Alice" {
		t.Errorf("name: got %v, want %q", resp.Document["name"], "Alice")
	}
}

func TestHandleCollectionGetDocument_MissingCollection(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionGet(svc)

	req := buildCallToolRequest(t, "qrdb_collection_get", map[string]any{
		"database": "mydb",
		"id":       float64(42),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing collection")
	}
}

func TestHandleCollectionGetDocument_MissingDatabase(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionGet(svc)

	req := buildCallToolRequest(t, "qrdb_collection_get", map[string]any{
		"collection": "users",
		"id":         float64(42),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing database")
	}
}

func TestHandleCollectionGetDocument_NotFound(t *testing.T) {
	svc := newMockCollectionService()
	svc.getDocumentFn = func(_ context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
		return nil, &service.CollectionDocumentNotFoundError{
			Database:   req.Database,
			Collection: req.Collection,
			ID:         req.ID,
		}
	}
	handler := handleCollectionGet(svc)

	req := buildCallToolRequest(t, "qrdb_collection_get", map[string]any{
		"database":   "mydb",
		"collection": "users",
		"id":         float64(999),
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_collection_get" {
		t.Errorf("tool name: got %q, want %q", toolErr.Tool, "qrdb_collection_get")
	}

	var notFoundErr *service.CollectionDocumentNotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Error("expected CollectionDocumentNotFoundError through ToolError chain")
	}
}

func TestHandleCollectionGetDocument_InvalidID(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionGet(svc)

	req := buildCallToolRequest(t, "qrdb_collection_get", map[string]any{
		"database":   "mydb",
		"collection": "users",
		"id":         float64(0),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for zero ID")
	}
}

// ---------------------------------------------------------------------------
// collection_update
// ---------------------------------------------------------------------------

func TestHandleCollectionUpdateDocument_Success(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionUpdate(svc)

	req := buildCallToolRequest(t, "qrdb_collection_update", map[string]any{
		"database":   "mydb",
		"collection": "users",
		"id":         float64(42),
		"document":   `{"name":"Bob","age":25}`,
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCollectionUpdate: %v", err)
	}

	text := resultText(t, result)
	var resp service.UpdateDocumentResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if resp.ID != 42 {
		t.Errorf("id: got %d, want %d", resp.ID, 42)
	}
}

func TestHandleCollectionUpdateDocument_MissingDocument(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionUpdate(svc)

	req := buildCallToolRequest(t, "qrdb_collection_update", map[string]any{
		"database":   "mydb",
		"collection": "users",
		"id":         float64(42),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing document")
	}
}

func TestHandleCollectionUpdateDocument_MissingID(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionUpdate(svc)

	req := buildCallToolRequest(t, "qrdb_collection_update", map[string]any{
		"database":   "mydb",
		"collection": "users",
		"document":   `{"name":"Bob"}`,
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing id")
	}
}

func TestHandleCollectionUpdateDocument_MissingCollection(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionUpdate(svc)

	req := buildCallToolRequest(t, "qrdb_collection_update", map[string]any{
		"database": "mydb",
		"id":       float64(42),
		"document": `{"name":"Bob"}`,
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing collection")
	}
}

func TestHandleCollectionUpdateDocument_MissingDatabase(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionUpdate(svc)

	req := buildCallToolRequest(t, "qrdb_collection_update", map[string]any{
		"collection": "users",
		"id":         float64(42),
		"document":   `{"name":"Bob"}`,
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing database")
	}
}

func TestHandleCollectionUpdateDocument_InvalidDocumentJSON(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionUpdate(svc)

	req := buildCallToolRequest(t, "qrdb_collection_update", map[string]any{
		"database":   "mydb",
		"collection": "users",
		"id":         float64(42),
		"document":   `{broken json`,
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for invalid JSON document")
	}
}

func TestHandleCollectionUpdateDocument_NotFound(t *testing.T) {
	svc := newMockCollectionService()
	svc.updateDocumentFn = func(_ context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
		return nil, &service.CollectionDocumentNotFoundError{
			Database:   req.Database,
			Collection: req.Collection,
			ID:         req.ID,
		}
	}
	handler := handleCollectionUpdate(svc)

	req := buildCallToolRequest(t, "qrdb_collection_update", map[string]any{
		"database":   "mydb",
		"collection": "users",
		"id":         float64(999),
		"document":   `{"name":"Bob"}`,
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_collection_update" {
		t.Errorf("tool name: got %q, want %q", toolErr.Tool, "qrdb_collection_update")
	}

	var notFoundErr *service.CollectionDocumentNotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Error("expected CollectionDocumentNotFoundError through ToolError chain")
	}
}

// ---------------------------------------------------------------------------
// collection_delete
// ---------------------------------------------------------------------------

func TestHandleCollectionDeleteDocument_Success(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionDelete(svc)

	req := buildCallToolRequest(t, "qrdb_collection_delete", map[string]any{
		"database":   "mydb",
		"collection": "users",
		"id":         float64(42),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCollectionDelete: %v", err)
	}

	text := resultText(t, result)
	var resp service.DeleteDocumentResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if !resp.Deleted {
		t.Error("expected deleted to be true")
	}
}

func TestHandleCollectionDeleteDocument_MissingID(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionDelete(svc)

	req := buildCallToolRequest(t, "qrdb_collection_delete", map[string]any{
		"database":   "mydb",
		"collection": "users",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing id")
	}
}

func TestHandleCollectionDeleteDocument_MissingCollection(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionDelete(svc)

	req := buildCallToolRequest(t, "qrdb_collection_delete", map[string]any{
		"database": "mydb",
		"id":       float64(42),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing collection")
	}
}

func TestHandleCollectionDeleteDocument_MissingDatabase(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionDelete(svc)

	req := buildCallToolRequest(t, "qrdb_collection_delete", map[string]any{
		"collection": "users",
		"id":         float64(42),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing database")
	}
}

func TestHandleCollectionDeleteDocument_NotFound(t *testing.T) {
	svc := newMockCollectionService()
	svc.deleteDocumentFn = func(_ context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
		return nil, &service.CollectionDocumentNotFoundError{
			Database:   req.Database,
			Collection: req.Collection,
			ID:         req.ID,
		}
	}
	handler := handleCollectionDelete(svc)

	req := buildCallToolRequest(t, "qrdb_collection_delete", map[string]any{
		"database":   "mydb",
		"collection": "users",
		"id":         float64(999),
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_collection_delete" {
		t.Errorf("tool name: got %q, want %q", toolErr.Tool, "qrdb_collection_delete")
	}

	var notFoundErr *service.CollectionDocumentNotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Error("expected CollectionDocumentNotFoundError through ToolError chain")
	}
}

// ---------------------------------------------------------------------------
// collection_list_documents
// ---------------------------------------------------------------------------

func TestHandleCollectionListDocuments_Success(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionListDocuments(svc)

	req := buildCallToolRequest(t, "qrdb_collection_list_documents", map[string]any{
		"database":   "mydb",
		"collection": "users",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCollectionListDocuments: %v", err)
	}

	text := resultText(t, result)
	var resp service.ListDocumentsResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if len(resp.Documents) != 2 {
		t.Fatalf("expected 2 documents, got %d", len(resp.Documents))
	}
	if resp.TotalCount != 2 {
		t.Errorf("total_count: got %d, want %d", resp.TotalCount, 2)
	}
	if resp.Documents[0].ID != 1 {
		t.Errorf("documents[0].id: got %d, want %d", resp.Documents[0].ID, 1)
	}
}

func TestHandleCollectionListDocuments_MissingDatabase(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionListDocuments(svc)

	req := buildCallToolRequest(t, "qrdb_collection_list_documents", map[string]any{
		"collection": "users",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing database")
	}
}

func TestHandleCollectionListDocuments_MissingCollection(t *testing.T) {
	svc := newMockCollectionService()
	handler := handleCollectionListDocuments(svc)

	req := buildCallToolRequest(t, "qrdb_collection_list_documents", map[string]any{
		"database": "mydb",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing collection")
	}
}

func TestHandleCollectionListDocuments_WithPagination(t *testing.T) {
	svc := newMockCollectionService()
	svc.listDocumentsFn = func(_ context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
		if req.PageSize != 10 {
			return nil, &service.CollectionEmptyDatabaseError{}
		}
		if req.PageToken != "5" {
			return nil, &service.CollectionEmptyDatabaseError{}
		}
		return &service.ListDocumentsResponse{
			Documents: []service.GetDocumentResponse{
				{ID: 6, Document: map[string]interface{}{"name": "Frank"}},
			},
			NextPageToken: "15",
			TotalCount:    20,
		}, nil
	}
	handler := handleCollectionListDocuments(svc)

	req := buildCallToolRequest(t, "qrdb_collection_list_documents", map[string]any{
		"database":   "mydb",
		"collection": "users",
		"page_size":  float64(10),
		"page_token": "5",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCollectionListDocuments: %v", err)
	}

	text := resultText(t, result)
	var resp service.ListDocumentsResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if len(resp.Documents) != 1 {
		t.Fatalf("expected 1 document, got %d", len(resp.Documents))
	}
	if resp.Documents[0].ID != 6 {
		t.Errorf("documents[0].id: got %d, want %d", resp.Documents[0].ID, 6)
	}
	if resp.NextPageToken != "15" {
		t.Errorf("next_page_token: got %q, want %q", resp.NextPageToken, "15")
	}
	if resp.TotalCount != 20 {
		t.Errorf("total_count: got %d, want %d", resp.TotalCount, 20)
	}
}

func TestHandleCollectionListDocuments_ServiceError(t *testing.T) {
	svc := newMockCollectionService()
	svc.listDocumentsFn = func(_ context.Context, _ *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
		return nil, &service.CollectionNotFoundError{
			Database:   "mydb",
			Collection: "users",
		}
	}
	handler := handleCollectionListDocuments(svc)

	req := buildCallToolRequest(t, "qrdb_collection_list_documents", map[string]any{
		"database":   "mydb",
		"collection": "users",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_collection_list_documents" {
		t.Errorf("tool name: got %q, want %q", toolErr.Tool, "qrdb_collection_list_documents")
	}
}
