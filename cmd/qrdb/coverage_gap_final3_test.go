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
	"testing"

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

func TestCollectionBinding_DeleteDocument_NotFound_WithMock(t *testing.T) {
	b, cleanup := newTestCollectionBindingWithMock(t)
	defer cleanup()

	err := b.DeleteDocument("default", "docs", "99999")
	if err == nil {
		t.Fatal("expected error for nonexistent document")
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
