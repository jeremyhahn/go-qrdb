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
	"testing"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// connectCollectionServer connects a Transport to the real MCP server whose
// collection tools are served by the real CollectionService over the
// map-backed KV double.
func connectCollectionServer(t *testing.T) *Transport {
	t.Helper()
	return connectServerInProcess(t, mcpapi.WithCollectionService(service.NewCollectionService(newMockKVService())))
}

func TestMCP_Collection_LifecycleRoundTrip(t *testing.T) {
	tr := connectCollectionServer(t)
	ctx := context.Background()

	info, err := tr.CreateCollection(ctx, transport.CreateCollectionRequest{
		Database: "app", Name: "users", IDStrategy: "field_hash", FieldHash: "email", StorageCodec: "json",
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if info.Name != "users" || info.Database != "app" || info.IDStrategy != "field_hash" || info.FieldHash != "email" {
		t.Fatalf("CreateCollection = %+v", info)
	}

	got, err := tr.GetCollectionInfo(ctx, "app", "users")
	if err != nil {
		t.Fatalf("GetCollectionInfo: %v", err)
	}
	if got.Name != "users" || got.Database != "app" || got.IDStrategy != info.IDStrategy {
		t.Fatalf("GetCollectionInfo = %+v, created %+v", got, info)
	}

	list, err := tr.ListCollections(ctx, "app")
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(list.Collections) != 1 || list.Collections[0].Name != "users" {
		t.Fatalf("ListCollections = %+v", list)
	}

	if err := tr.DropCollection(ctx, "app", "users"); err != nil {
		t.Fatalf("DropCollection: %v", err)
	}
	if _, err := tr.GetCollectionInfo(ctx, "app", "users"); err == nil {
		t.Fatal("GetCollectionInfo succeeded after DropCollection")
	}
}

func TestMCP_Collection_DocumentRoundTrip(t *testing.T) {
	tr := connectCollectionServer(t)
	ctx := context.Background()

	// increment IDs stay below 2^53: the MCP id argument is a JSON number,
	// so a unix_nano ID does not survive the trip to the server.
	if _, err := tr.CreateCollection(ctx, transport.CreateCollectionRequest{
		Database: "app", Name: "users", IDStrategy: "increment",
	}); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	ins, err := tr.InsertDocument(ctx, transport.InsertDocumentRequest{
		Database: "app", Collection: "users", Document: map[string]interface{}{"name": "ada"},
	})
	if err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}
	if _, err := tr.InsertDocument(ctx, transport.InsertDocumentRequest{
		Database: "app", Collection: "users", Document: map[string]interface{}{"name": "bob"},
	}); err != nil {
		t.Fatalf("InsertDocument(bob): %v", err)
	}

	doc, err := tr.GetDocument(ctx, "app", "users", ins.ID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if doc.ID != ins.ID || doc.Document["name"] != "ada" {
		t.Fatalf("GetDocument = %+v", doc)
	}

	if err := tr.UpdateDocument(ctx, transport.UpdateDocumentRequest{
		Database: "app", Collection: "users", ID: ins.ID, Document: map[string]interface{}{"name": "grace"},
	}); err != nil {
		t.Fatalf("UpdateDocument: %v", err)
	}
	doc, err = tr.GetDocument(ctx, "app", "users", ins.ID)
	if err != nil {
		t.Fatalf("GetDocument after update: %v", err)
	}
	if doc.Document["name"] != "grace" {
		t.Fatalf("GetDocument after update = %+v", doc)
	}

	page, err := tr.ListDocuments(ctx, transport.ListDocumentsRequest{Database: "app", Collection: "users", PageSize: 1})
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(page.Documents) != 1 || page.NextPageToken == "" {
		t.Fatalf("ListDocuments(page 1) = %+v", page)
	}
	rest, err := tr.ListDocuments(ctx, transport.ListDocumentsRequest{
		Database: "app", Collection: "users", PageSize: 10, PageToken: page.NextPageToken,
	})
	if err != nil {
		t.Fatalf("ListDocuments(page 2): %v", err)
	}
	if len(rest.Documents) != 1 || rest.Documents[0].ID == page.Documents[0].ID {
		t.Fatalf("ListDocuments(page 2) = %+v after %+v", rest, page)
	}

	if err := tr.DeleteDocument(ctx, "app", "users", ins.ID); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
	if _, err := tr.GetDocument(ctx, "app", "users", ins.ID); err == nil {
		t.Fatal("GetDocument succeeded after DeleteDocument")
	}
}

func TestMCP_Collection_ErrorsAndClosed(t *testing.T) {
	tr := connectCollectionServer(t)
	ctx := context.Background()

	// The index definitions reach the service, which refuses them: the KV
	// double, like a database without an index store, has no secondary indexes.
	if _, err := tr.CreateCollection(ctx, transport.CreateCollectionRequest{
		Database: "app", Name: "indexed",
		Indexes: []transport.CollectionIndexDefinition{{FieldName: "email", Unique: true, Sparse: true}},
	}); err == nil {
		t.Fatal("CreateCollection with indexes succeeded on a database without secondary indexes")
	}
	if _, err := tr.InsertDocument(ctx, transport.InsertDocumentRequest{
		Database: "app", Collection: "missing", Document: map[string]interface{}{"a": 1},
	}); err == nil {
		t.Fatal("InsertDocument into a missing collection succeeded")
	}
	if _, err := tr.ListDocuments(ctx, transport.ListDocumentsRequest{Database: "app", Collection: "missing"}); err == nil {
		t.Fatal("ListDocuments of a missing collection succeeded")
	}
	if err := tr.UpdateDocument(ctx, transport.UpdateDocumentRequest{
		Database: "app", Collection: "missing", ID: 1, Document: map[string]interface{}{"a": 1},
	}); err == nil {
		t.Fatal("UpdateDocument in a missing collection succeeded")
	}
	if err := tr.DeleteDocument(ctx, "app", "missing", 1); err == nil {
		t.Fatal("DeleteDocument in a missing collection succeeded")
	}
	if err := tr.DropCollection(ctx, "app", "missing"); err == nil {
		t.Fatal("DropCollection of a missing collection succeeded")
	}

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := tr.CreateCollection(ctx, transport.CreateCollectionRequest{Database: "app", Name: "x"})
	requireClosed(t, err)
	_, err = tr.GetCollectionInfo(ctx, "app", "x")
	requireClosed(t, err)
	_, err = tr.ListCollections(ctx, "app")
	requireClosed(t, err)
	requireClosed(t, tr.DropCollection(ctx, "app", "x"))
	_, err = tr.InsertDocument(ctx, transport.InsertDocumentRequest{Database: "app", Collection: "x"})
	requireClosed(t, err)
	_, err = tr.GetDocument(ctx, "app", "x", 1)
	requireClosed(t, err)
	requireClosed(t, tr.UpdateDocument(ctx, transport.UpdateDocumentRequest{Database: "app", Collection: "x", ID: 1}))
	requireClosed(t, tr.DeleteDocument(ctx, "app", "x", 1))
	_, err = tr.ListDocuments(ctx, transport.ListDocumentsRequest{Database: "app", Collection: "x"})
	requireClosed(t, err)
}
