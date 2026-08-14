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

//go:build qrdb_embedded && !integration

package main

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newTestCollectionBinding starts an embedded memory server and returns
// a CollectionBinding wired to it. The caller must defer esb.StopEmbedded().
func newTestCollectionBinding(t *testing.T) (*CollectionBinding, *EmbeddedServerBinding) {
	t.Helper()
	esb, conn := startEmbeddedLocal(t, "pebble")
	log := testLogger(t)
	b := NewCollectionBinding(log, conn)
	return b, esb
}

// ---------------------------------------------------------------------------
// NewCollectionBinding constructor
// ---------------------------------------------------------------------------

func TestNewCollectionBinding_Constructor(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewCollectionBinding(log, conn)
	if b == nil {
		t.Fatal("expected non-nil CollectionBinding")
	}
}

func TestNewCollectionBinding_Logger(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewCollectionBinding(log, conn)
	if b.logger != log {
		t.Error("expected logger to be set")
	}
}

// ---------------------------------------------------------------------------
// CollectionBinding.Create
// ---------------------------------------------------------------------------

func TestCollectionBinding_Create_NotConnected(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewCollectionBinding(log, conn)

	_, err := b.Create(&transport.CreateCollectionRequest{
		Database: "db",
		Name:     "col",
	})
	if err == nil {
		t.Fatal("expected error from Create when not connected")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestCollectionBinding_Create_Success(t *testing.T) {
	b, esb := newTestCollectionBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	info, err := b.Create(&transport.CreateCollectionRequest{
		Database: "default",
		Name:     "test-collection",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil CollectionInfo")
	}
	if info.Name != "test-collection" {
		t.Errorf("expected Name=%q, got %q", "test-collection", info.Name)
	}
}

func TestCollectionBinding_Create_DuplicateName(t *testing.T) {
	b, esb := newTestCollectionBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	req := &transport.CreateCollectionRequest{
		Database: "default",
		Name:     "dup-collection",
	}
	if _, err := b.Create(req); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := b.Create(req)
	if err == nil {
		t.Fatal("expected error from duplicate Create")
	}
}

// ---------------------------------------------------------------------------
// CollectionBinding.Get
// ---------------------------------------------------------------------------

func TestCollectionBinding_Get_NotConnected(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewCollectionBinding(log, conn)

	_, err := b.Get("db", "col")
	if err == nil {
		t.Fatal("expected error from Get when not connected")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestCollectionBinding_Get_Success(t *testing.T) {
	b, esb := newTestCollectionBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	if _, err := b.Create(&transport.CreateCollectionRequest{
		Database: "default",
		Name:     "get-test",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	info, err := b.Get("default", "get-test")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil CollectionInfo")
	}
	if info.Name != "get-test" {
		t.Errorf("expected Name=%q, got %q", "get-test", info.Name)
	}
}

func TestCollectionBinding_Get_NotFound(t *testing.T) {
	b, esb := newTestCollectionBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	_, err := b.Get("default", "nonexistent-collection")
	if err == nil {
		t.Fatal("expected error from Get for nonexistent collection")
	}
}

// ---------------------------------------------------------------------------
// CollectionBinding.List
// ---------------------------------------------------------------------------

func TestCollectionBinding_List_NotConnected(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewCollectionBinding(log, conn)

	_, err := b.List("db")
	if err == nil {
		t.Fatal("expected error from List when not connected")
	}
}

func TestCollectionBinding_List_Empty(t *testing.T) {
	b, esb := newTestCollectionBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	resp, err := b.List("default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil ListCollectionsResponse")
	}
	if len(resp.Collections) != 0 {
		t.Errorf("expected 0 collections, got %d", len(resp.Collections))
	}
}

func TestCollectionBinding_List_WithCollections(t *testing.T) {
	b, esb := newTestCollectionBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	for _, name := range []string{"col-a", "col-b"} {
		if _, err := b.Create(&transport.CreateCollectionRequest{
			Database: "default",
			Name:     name,
		}); err != nil {
			t.Fatalf("Create(%q): %v", name, err)
		}
	}

	resp, err := b.List("default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(resp.Collections) != 2 {
		t.Errorf("expected 2 collections, got %d", len(resp.Collections))
	}
}

// ---------------------------------------------------------------------------
// CollectionBinding.Drop
// ---------------------------------------------------------------------------

func TestCollectionBinding_Drop_NotConnected(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewCollectionBinding(log, conn)

	err := b.Drop("db", "col")
	if err == nil {
		t.Fatal("expected error from Drop when not connected")
	}
}

func TestCollectionBinding_Drop_Success(t *testing.T) {
	b, esb := newTestCollectionBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	if _, err := b.Create(&transport.CreateCollectionRequest{
		Database: "default",
		Name:     "drop-me",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := b.Drop("default", "drop-me"); err != nil {
		t.Fatalf("Drop: %v", err)
	}

	// Verify it was removed.
	resp, err := b.List("default")
	if err != nil {
		t.Fatalf("List after drop: %v", err)
	}
	for _, col := range resp.Collections {
		if col.Name == "drop-me" {
			t.Error("expected collection to be removed after Drop")
		}
	}
}

func TestCollectionBinding_Drop_NotFound(t *testing.T) {
	b, esb := newTestCollectionBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	err := b.Drop("default", "no-such-collection")
	if err == nil {
		t.Fatal("expected error from Drop for nonexistent collection")
	}
}

// ---------------------------------------------------------------------------
// CollectionBinding.InsertDocument
// ---------------------------------------------------------------------------

func TestCollectionBinding_InsertDocument_NotConnected(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewCollectionBinding(log, conn)

	_, err := b.InsertDocument(&transport.InsertDocumentRequest{})
	if err == nil {
		t.Fatal("expected error from InsertDocument when not connected")
	}
}

func TestCollectionBinding_InsertDocument_Success(t *testing.T) {
	b, esb := newTestCollectionBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	if _, err := b.Create(&transport.CreateCollectionRequest{
		Database: "default",
		Name:     "docs",
	}); err != nil {
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
	if resp == nil {
		t.Fatal("expected non-nil InsertDocumentResponse")
	}
	if resp.ID == 0 {
		t.Error("expected non-zero document ID")
	}
}

// ---------------------------------------------------------------------------
// CollectionBinding.GetDocument
// ---------------------------------------------------------------------------

func TestCollectionBinding_GetDocument_NotConnected(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewCollectionBinding(log, conn)

	_, err := b.GetDocument("db", "col", "1")
	if err == nil {
		t.Fatal("expected error from GetDocument when not connected")
	}
}

func TestCollectionBinding_GetDocument_InvalidID(t *testing.T) {
	b, esb := newTestCollectionBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	// Need to have a collection before GetDocument can fail on ID parse.
	_, err := b.GetDocument("default", "docs", "not-a-number")
	if err == nil {
		t.Fatal("expected error for non-numeric document ID")
	}
}

func TestCollectionBinding_GetDocument_Success(t *testing.T) {
	b, esb := newTestCollectionBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	if _, err := b.Create(&transport.CreateCollectionRequest{
		Database: "default",
		Name:     "getdoc",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	insertResp, err := b.InsertDocument(&transport.InsertDocumentRequest{
		Database:   "default",
		Collection: "getdoc",
		Document:   map[string]interface{}{"x": 1},
	})
	if err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}

	idStr := idToStr(insertResp.ID)
	doc, err := b.GetDocument("default", "getdoc", idStr)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if doc == nil {
		t.Fatal("expected non-nil DocumentResponse")
	}
}

// ---------------------------------------------------------------------------
// CollectionBinding.UpdateDocument
// ---------------------------------------------------------------------------

func TestCollectionBinding_UpdateDocument_NotConnected(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewCollectionBinding(log, conn)

	err := b.UpdateDocument(&transport.UpdateDocumentRequest{})
	if err == nil {
		t.Fatal("expected error from UpdateDocument when not connected")
	}
}

func TestCollectionBinding_UpdateDocument_Success(t *testing.T) {
	b, esb := newTestCollectionBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	if _, err := b.Create(&transport.CreateCollectionRequest{
		Database: "default",
		Name:     "updatedocs",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	insertResp, err := b.InsertDocument(&transport.InsertDocumentRequest{
		Database:   "default",
		Collection: "updatedocs",
		Document:   map[string]interface{}{"v": 1},
	})
	if err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}

	err = b.UpdateDocument(&transport.UpdateDocumentRequest{
		Database:   "default",
		Collection: "updatedocs",
		ID:         insertResp.ID,
		Document:   map[string]interface{}{"v": 2},
	})
	if err != nil {
		t.Fatalf("UpdateDocument: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CollectionBinding.DeleteDocument
// ---------------------------------------------------------------------------

func TestCollectionBinding_DeleteDocument_NotConnected(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewCollectionBinding(log, conn)

	err := b.DeleteDocument("db", "col", "1")
	if err == nil {
		t.Fatal("expected error from DeleteDocument when not connected")
	}
}

func TestCollectionBinding_DeleteDocument_InvalidID(t *testing.T) {
	b, esb := newTestCollectionBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	err := b.DeleteDocument("default", "docs", "bad-id")
	if err == nil {
		t.Fatal("expected error for non-numeric document ID")
	}
}

func TestCollectionBinding_DeleteDocument_Success(t *testing.T) {
	b, esb := newTestCollectionBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	if _, err := b.Create(&transport.CreateCollectionRequest{
		Database: "default",
		Name:     "deldocs",
	}); err != nil {
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

	idStr := idToStr(insertResp.ID)
	if err := b.DeleteDocument("default", "deldocs", idStr); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CollectionBinding.ListDocuments
// ---------------------------------------------------------------------------

func TestCollectionBinding_ListDocuments_NotConnected(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewCollectionBinding(log, conn)

	_, err := b.ListDocuments(&transport.ListDocumentsRequest{})
	if err == nil {
		t.Fatal("expected error from ListDocuments when not connected")
	}
}

func TestCollectionBinding_ListDocuments_Empty(t *testing.T) {
	b, esb := newTestCollectionBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	if _, err := b.Create(&transport.CreateCollectionRequest{
		Database: "default",
		Name:     "listdocs",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	resp, err := b.ListDocuments(&transport.ListDocumentsRequest{
		Database:   "default",
		Collection: "listdocs",
		PageSize:   10,
	})
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil ListDocumentsResponse")
	}
	if len(resp.Documents) != 0 {
		t.Errorf("expected 0 documents, got %d", len(resp.Documents))
	}
}

func TestCollectionBinding_ListDocuments_WithDocuments(t *testing.T) {
	b, esb := newTestCollectionBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	if _, err := b.Create(&transport.CreateCollectionRequest{
		Database: "default",
		Name:     "listdocs2",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	for i := 0; i < 3; i++ {
		if _, err := b.InsertDocument(&transport.InsertDocumentRequest{
			Database:   "default",
			Collection: "listdocs2",
			Document:   map[string]interface{}{"i": i},
		}); err != nil {
			t.Fatalf("InsertDocument[%d]: %v", i, err)
		}
	}

	resp, err := b.ListDocuments(&transport.ListDocumentsRequest{
		Database:   "default",
		Collection: "listdocs2",
		PageSize:   10,
	})
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(resp.Documents) != 3 {
		t.Errorf("expected 3 documents, got %d", len(resp.Documents))
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// idToStr converts a uint64 document ID to a decimal string.
func idToStr(id uint64) string {
	if id == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for id >= 10 {
		pos--
		buf[pos] = byte('0' + id%10)
		id /= 10
	}
	pos--
	buf[pos] = byte('0' + id)
	return string(buf[pos:])
}
