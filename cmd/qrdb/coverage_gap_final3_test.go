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

//go:build !integration

// coverage_gap_final3_test.go covers the collection and index bindings
// success paths, and other remaining low-coverage areas, using mock
// embedded transport services.
package main

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ===========================================================================
// Mock collection service
// ===========================================================================

type mockCollectionServiceForBinding struct {
	nextID      uint64
	collections map[string]*transport.CollectionInfo
	documents   map[uint64]map[string]interface{}
}

func newMockCollectionSvc() *mockCollectionServiceForBinding {
	return &mockCollectionServiceForBinding{
		collections: make(map[string]*transport.CollectionInfo),
		documents:   make(map[uint64]map[string]interface{}),
	}
}

func (m *mockCollectionServiceForBinding) CreateCollection(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	key := req.Database + ":" + req.Name
	if _, exists := m.collections[key]; exists {
		return nil, errors.New("collection already exists")
	}
	info := &service.CollectionInfo{Name: req.Name, Database: req.Database}
	m.collections[key] = &transport.CollectionInfo{Name: req.Name, Database: req.Database}
	return info, nil
}

func (m *mockCollectionServiceForBinding) DropCollection(_ context.Context, req *service.DropCollectionRequest) error {
	key := req.Database + ":" + req.Name
	if _, exists := m.collections[key]; !exists {
		return errors.New("collection not found")
	}
	delete(m.collections, key)
	return nil
}

func (m *mockCollectionServiceForBinding) ListCollections(_ context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
	var cols []service.CollectionInfo
	for _, c := range m.collections {
		if c.Database == req.Database {
			cols = append(cols, service.CollectionInfo{Name: c.Name, Database: c.Database})
		}
	}
	return &service.ListCollectionsResponse{Collections: cols}, nil
}

func (m *mockCollectionServiceForBinding) GetCollectionInfo(_ context.Context, database, name string) (*service.CollectionInfo, error) {
	key := database + ":" + name
	if _, exists := m.collections[key]; !exists {
		return nil, errors.New("collection not found")
	}
	return &service.CollectionInfo{Name: name, Database: database}, nil
}

func (m *mockCollectionServiceForBinding) InsertDocument(_ context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
	m.nextID++
	m.documents[m.nextID] = req.Document
	return &service.InsertDocumentResponse{ID: m.nextID}, nil
}

func (m *mockCollectionServiceForBinding) GetDocument(_ context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
	doc, exists := m.documents[req.ID]
	if !exists {
		return nil, errors.New("document not found")
	}
	return &service.GetDocumentResponse{ID: req.ID, Document: doc}, nil
}

func (m *mockCollectionServiceForBinding) UpdateDocument(_ context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
	if _, exists := m.documents[req.ID]; !exists {
		return nil, errors.New("document not found")
	}
	m.documents[req.ID] = req.Document
	return &service.UpdateDocumentResponse{}, nil
}

// UpsertDocument reports an insert: no test that uses this double drives
// the guarded upsert, which is exercised against the real service.
func (m *mockCollectionServiceForBinding) UpsertDocument(_ context.Context, req *service.UpsertDocumentRequest) (*service.UpsertDocumentResponse, error) {
	return &service.UpsertDocumentResponse{ID: req.ID}, nil
}

func (m *mockCollectionServiceForBinding) DeleteDocument(_ context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
	if _, exists := m.documents[req.ID]; !exists {
		return nil, errors.New("document not found")
	}
	delete(m.documents, req.ID)
	return &service.DeleteDocumentResponse{}, nil
}

func (m *mockCollectionServiceForBinding) ListDocuments(_ context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
	var docs []service.GetDocumentResponse
	for id, doc := range m.documents {
		docs = append(docs, service.GetDocumentResponse{ID: id, Document: doc})
	}
	return &service.ListDocumentsResponse{Documents: docs}, nil
}

// ===========================================================================
// Helper: newTestCollectionBindingWithMock creates a CollectionBinding
// wired against a mock collection service via the embedded transport.
// ===========================================================================

func newTestCollectionBindingWithMock(t *testing.T) (*CollectionBinding, func()) {
	t.Helper()
	mockSvc := newMockCollectionSvc()
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	tr, err := embedded.New(kvSvc, adminSvc, embedded.WithCollectionService(mockSvc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	cl, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	if err := cl.Connect(context.Background()); err != nil {
		t.Fatalf("cl.Connect: %v", err)
	}

	log := testLogger(t)
	conn := &ConnectionBinding{logger: log}
	conn.client.Store(cl)

	b := NewCollectionBinding(log, conn)
	return b, func() { _ = cl.Close() }
}

// ===========================================================================
// CollectionBinding success paths (currently 45-50%)
// ===========================================================================

func TestCollectionBinding_Create_Success_WithMock(t *testing.T) {
	b, cleanup := newTestCollectionBindingWithMock(t)
	defer cleanup()

	info, err := b.Create(&transport.CreateCollectionRequest{Database: "default", Name: "mock-col"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil CollectionInfo")
	}
	if info.Name != "mock-col" {
		t.Fatalf("expected Name=%q, got %q", "mock-col", info.Name)
	}
}

func TestCollectionBinding_Create_DuplicateWithMock(t *testing.T) {
	b, cleanup := newTestCollectionBindingWithMock(t)
	defer cleanup()

	req := &transport.CreateCollectionRequest{Database: "default", Name: "dup"}
	if _, err := b.Create(req); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := b.Create(req)
	if err == nil {
		t.Fatal("expected error for duplicate collection")
	}
}

func TestCollectionBinding_Get_Success_WithMock(t *testing.T) {
	b, cleanup := newTestCollectionBindingWithMock(t)
	defer cleanup()

	if _, err := b.Create(&transport.CreateCollectionRequest{Database: "default", Name: "get-col"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	info, err := b.Get("default", "get-col")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil CollectionInfo")
	}
}

func TestCollectionBinding_Get_NotFound_WithMock(t *testing.T) {
	b, cleanup := newTestCollectionBindingWithMock(t)
	defer cleanup()

	_, err := b.Get("default", "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent collection")
	}
}

func TestCollectionBinding_List_Success_WithMock(t *testing.T) {
	b, cleanup := newTestCollectionBindingWithMock(t)
	defer cleanup()

	for _, name := range []string{"col-a", "col-b"} {
		if _, err := b.Create(&transport.CreateCollectionRequest{Database: "default", Name: name}); err != nil {
			t.Fatalf("Create(%q): %v", name, err)
		}
	}
	resp, err := b.List("default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(resp.Collections) != 2 {
		t.Fatalf("expected 2 collections, got %d", len(resp.Collections))
	}
}

func TestCollectionBinding_Drop_Success_WithMock(t *testing.T) {
	b, cleanup := newTestCollectionBindingWithMock(t)
	defer cleanup()

	if _, err := b.Create(&transport.CreateCollectionRequest{Database: "default", Name: "drop-col"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := b.Drop("default", "drop-col"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
}

func TestCollectionBinding_Drop_NotFound_WithMock(t *testing.T) {
	b, cleanup := newTestCollectionBindingWithMock(t)
	defer cleanup()

	err := b.Drop("default", "nosuchcol")
	if err == nil {
		t.Fatal("expected error for nonexistent collection")
	}
}

func TestCollectionBinding_InsertDocument_Success_WithMock(t *testing.T) {
	b, cleanup := newTestCollectionBindingWithMock(t)
	defer cleanup()

	if _, err := b.Create(&transport.CreateCollectionRequest{Database: "default", Name: "docs"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	resp, err := b.InsertDocument(&transport.InsertDocumentRequest{
		Database:   "default",
		Collection: "docs",
		Document:   map[string]interface{}{"name": "alice"},
	})
	if err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}
	if resp.ID == 0 {
		t.Fatal("expected non-zero document ID")
	}
}

func TestCollectionBinding_GetDocument_Success_WithMock(t *testing.T) {
	b, cleanup := newTestCollectionBindingWithMock(t)
	defer cleanup()

	if _, err := b.Create(&transport.CreateCollectionRequest{Database: "default", Name: "getdocs"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	insertResp, err := b.InsertDocument(&transport.InsertDocumentRequest{
		Database:   "default",
		Collection: "getdocs",
		Document:   map[string]interface{}{"v": 1},
	})
	if err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}

	idStr := strconv.FormatUint(insertResp.ID, 10)
	doc, err := b.GetDocument("default", "getdocs", idStr)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if doc == nil {
		t.Fatal("expected non-nil DocumentResponse")
	}
}

func TestCollectionBinding_GetDocument_InvalidID_WithMock(t *testing.T) {
	b, cleanup := newTestCollectionBindingWithMock(t)
	defer cleanup()

	_, err := b.GetDocument("default", "docs", "not-a-number")
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
}

func TestCollectionBinding_UpdateDocument_Success_WithMock(t *testing.T) {
	b, cleanup := newTestCollectionBindingWithMock(t)
	defer cleanup()

	if _, err := b.Create(&transport.CreateCollectionRequest{Database: "default", Name: "upddocs"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	insertResp, err := b.InsertDocument(&transport.InsertDocumentRequest{
		Database:   "default",
		Collection: "upddocs",
		Document:   map[string]interface{}{"v": 1},
	})
	if err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}
	err = b.UpdateDocument(&transport.UpdateDocumentRequest{
		Database:   "default",
		Collection: "upddocs",
		ID:         insertResp.ID,
		Document:   map[string]interface{}{"v": 2},
	})
	if err != nil {
		t.Fatalf("UpdateDocument: %v", err)
	}
}

func TestCollectionBinding_UpdateDocument_NotFound_WithMock(t *testing.T) {
	b, cleanup := newTestCollectionBindingWithMock(t)
	defer cleanup()

	err := b.UpdateDocument(&transport.UpdateDocumentRequest{
		Database:   "default",
		Collection: "docs",
		ID:         99999,
		Document:   map[string]interface{}{"v": 1},
	})
	if err == nil {
		t.Fatal("expected error for nonexistent document")
	}
}

func TestCollectionBinding_DeleteDocument_Success_WithMock(t *testing.T) {
	b, cleanup := newTestCollectionBindingWithMock(t)
	defer cleanup()

	if _, err := b.Create(&transport.CreateCollectionRequest{Database: "default", Name: "deldocs"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	insertResp, err := b.InsertDocument(&transport.InsertDocumentRequest{
		Database:   "default",
		Collection: "deldocs",
		Document:   map[string]interface{}{"tmp": true},
	})
	if err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}
	idStr := strconv.FormatUint(insertResp.ID, 10)
	if err := b.DeleteDocument("default", "deldocs", idStr); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
}

func TestCollectionBinding_DeleteDocument_InvalidID_WithMock(t *testing.T) {
	b, cleanup := newTestCollectionBindingWithMock(t)
	defer cleanup()

	err := b.DeleteDocument("default", "docs", "bad-id")
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
}

func TestCollectionBinding_DeleteDocument_NotFound_WithMock(t *testing.T) {
	b, cleanup := newTestCollectionBindingWithMock(t)
	defer cleanup()

	err := b.DeleteDocument("default", "docs", "99999")
	if err == nil {
		t.Fatal("expected error for nonexistent document")
	}
}

func TestCollectionBinding_ListDocuments_Success_WithMock(t *testing.T) {
	b, cleanup := newTestCollectionBindingWithMock(t)
	defer cleanup()

	if _, err := b.Create(&transport.CreateCollectionRequest{Database: "default", Name: "listdocs"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := b.InsertDocument(&transport.InsertDocumentRequest{
			Database:   "default",
			Collection: "listdocs",
			Document:   map[string]interface{}{"i": i},
		}); err != nil {
			t.Fatalf("InsertDocument[%d]: %v", i, err)
		}
	}
	resp, err := b.ListDocuments(&transport.ListDocumentsRequest{
		Database:   "default",
		Collection: "listdocs",
		PageSize:   10,
	})
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(resp.Documents) != 3 {
		t.Fatalf("expected 3 documents, got %d", len(resp.Documents))
	}
}

// ===========================================================================
// IndexBinding success paths using mock index service (currently 45-50%)
// The mock is already defined in cmd_coverage_test.go as mockIndexService.
// We reuse it here.
// ===========================================================================

func newTestIndexBindingWithMock(t *testing.T, idxSvc service.IndexServicer) (*IndexBinding, func()) {
	t.Helper()
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	tr, err := embedded.New(kvSvc, adminSvc, embedded.WithIndexService(idxSvc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	cl, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	if err := cl.Connect(context.Background()); err != nil {
		t.Fatalf("cl.Connect: %v", err)
	}

	log := testLogger(t)
	conn := &ConnectionBinding{logger: log}
	conn.client.Store(cl)

	b := NewIndexBinding(log, conn)
	return b, func() { _ = cl.Close() }
}

func TestIndexBinding_Create_Success_WithMock(t *testing.T) {
	b, cleanup := newTestIndexBindingWithMock(t, &mockIndexService{})
	defer cleanup()

	if err := b.Create(IndexCreateParams{Name: "name-idx", FieldName: "name"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func TestIndexBinding_Drop_Success_WithMock(t *testing.T) {
	b, cleanup := newTestIndexBindingWithMock(t, &mockIndexService{})
	defer cleanup()

	if err := b.Drop("name-idx"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
}

func TestIndexBinding_Query_Success_WithMock(t *testing.T) {
	b, cleanup := newTestIndexBindingWithMock(t, &mockIndexService{
		keys: [][]byte{[]byte("key1"), []byte("key2")},
	})
	defer cleanup()

	resp, err := b.Query("name-idx", "alice")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestIndexBinding_Scan_Success_WithMock(t *testing.T) {
	b, cleanup := newTestIndexBindingWithMock(t, &mockIndexService{
		keys: [][]byte{[]byte("entry1")},
	})
	defer cleanup()

	resp, err := b.Scan("score-idx", "0", "100")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestIndexBinding_List_Success_WithMock(t *testing.T) {
	b, cleanup := newTestIndexBindingWithMock(t, &mockIndexService{
		names: []string{"idx1", "idx2"},
	})
	defer cleanup()

	resp, err := b.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(resp.Names) != 2 {
		t.Fatalf("expected 2 names, got %d", len(resp.Names))
	}
}

func TestIndexBinding_List_Empty_WithMock(t *testing.T) {
	b, cleanup := newTestIndexBindingWithMock(t, &mockIndexService{names: nil})
	defer cleanup()

	resp, err := b.List("")
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(resp.Names) != 0 {
		t.Fatalf("expected 0 names, got %d", len(resp.Names))
	}
}

// ===========================================================================
// bindings_connection_manager.go — startup (70.6%)
// Startup loads profiles and attempts auto-connect. We exercise it by
// calling it directly on a manager.
// ===========================================================================

func TestConnectionManager_Startup_LoadsProfiles(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	m := NewConnectionManagerBinding(testLogger(t))

	// Save a profile before startup.
	p := ConnectionProfile{
		ID:        "s-1",
		Name:      "StartupTest",
		Type:      "external",
		External:  &ConnectionConfig{Transport: "grpc", Address: "localhost:63020"},
		CreatedAt: "2026-06-01T00:00:00Z",
	}
	if err := m.SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	// Create a fresh manager and call startup; it loads profiles from disk
	// and attempts auto-connect (which will fail for external addresses but
	// exercises the profile loading path).
	m2 := NewConnectionManagerBinding(testLogger(t))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	m2.startup(ctx)

	// After startup, profiles should be loaded.
	profiles := m2.ListProfiles()
	if len(profiles) != 1 {
		t.Fatalf("expected 1 profile after startup, got %d", len(profiles))
	}
}

// ===========================================================================
// bindings_connection_manager.go — Disconnect with active connection (76%)
// ===========================================================================

func TestConnectionManager_Disconnect_WithActiveExternal(t *testing.T) {
	// Inject a managed connection directly into the sync.Map to exercise
	// the Disconnect loaded-connection path without attempting a real
	// network connection (which would panic in the test environment).
	m := newTestConnectionManager(t)
	p := ConnectionProfile{
		ID:       "disc-1",
		Name:     "Disc Test",
		Type:     "external",
		External: &ConnectionConfig{Transport: "grpc", Address: "localhost:63021"},
	}
	if err := m.SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	// Inject a fake nil-client connection (error-state).
	conn := &managedConnection{profile: p}
	conn.storeState(ConnectionState{ProfileID: "disc-1", Status: connectionStatusError})
	m.connections.Store("disc-1", conn)

	// Disconnect should remove the entry from the map.
	if err := m.Disconnect("disc-1"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	// Verify the connection was removed.
	if _, ok := m.connections.Load("disc-1"); ok {
		t.Fatal("expected connection to be removed after Disconnect")
	}
}

// ===========================================================================
// bindings.go — loadClient (83%), IsConnected (50%), Disconnect (81%)
// ===========================================================================

func TestConnectionBinding_LoadClient_Success(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService())
	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}
	if cl == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestConnectionBinding_Disconnect_WithConnectedClient(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService())
	if !conn.IsConnected() {
		t.Fatal("expected IsConnected()=true before Disconnect")
	}
	if err := conn.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if conn.IsConnected() {
		t.Fatal("expected IsConnected()=false after Disconnect")
	}
}
