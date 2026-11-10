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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestEmbeddedCollection_CreateCollection(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{}))
	info, err := tr.CreateCollection(context.Background(), transport.CreateCollectionRequest{Database: "db", Name: "users"})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if info.Name != "users" || info.Database != "db" {
		t.Fatalf("unexpected info: %+v", info)
	}
}

func TestEmbeddedCollection_GetCollectionInfo(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{}))
	info, err := tr.GetCollectionInfo(context.Background(), "db", "users")
	if err != nil {
		t.Fatalf("GetCollectionInfo: %v", err)
	}
	if info.Name != "users" {
		t.Fatalf("unexpected name %q", info.Name)
	}
}

func TestEmbeddedCollection_ListCollections(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{}))
	resp, err := tr.ListCollections(context.Background(), "db")
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(resp.Collections) != 1 || resp.Collections[0].Name != "users" {
		t.Fatalf("unexpected: %+v", resp)
	}
}

func TestEmbeddedCollection_InsertDocument(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{}))
	resp, err := tr.InsertDocument(context.Background(), transport.InsertDocumentRequest{Database: "db", Collection: "users", Document: map[string]interface{}{"x": 1}})
	if err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}
	if resp.ID != 42 {
		t.Fatalf("ID = %d", resp.ID)
	}
}

func TestEmbeddedCollection_GetDocument(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{}))
	doc, err := tr.GetDocument(context.Background(), "db", "users", 7)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if doc.ID != 7 {
		t.Fatalf("ID = %d", doc.ID)
	}
}

func TestEmbeddedCollection_ListDocuments(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{}))
	resp, err := tr.ListDocuments(context.Background(), transport.ListDocumentsRequest{Database: "db", Collection: "users"})
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if resp.TotalCount != 1 || len(resp.Documents) != 1 {
		t.Fatalf("unexpected: %+v", resp)
	}
}

// collectionCalls drives every collection and document method of tr once.
func collectionCalls(tr *Transport) map[string]func() error {
	ctx := context.Background()
	return map[string]func() error{
		"CreateCollection": func() error {
			_, err := tr.CreateCollection(ctx, transport.CreateCollectionRequest{Database: "db", Name: "users"})
			return err
		},
		"GetCollectionInfo": func() error { _, err := tr.GetCollectionInfo(ctx, "db", "users"); return err },
		"ListCollections":   func() error { _, err := tr.ListCollections(ctx, "db"); return err },
		"DropCollection":    func() error { return tr.DropCollection(ctx, "db", "users") },
		"InsertDocument": func() error {
			_, err := tr.InsertDocument(ctx, transport.InsertDocumentRequest{Database: "db", Collection: "users"})
			return err
		},
		"GetDocument": func() error { _, err := tr.GetDocument(ctx, "db", "users", 1); return err },
		"UpdateDocument": func() error {
			return tr.UpdateDocument(ctx, transport.UpdateDocumentRequest{Database: "db", Collection: "users", ID: 1})
		},
		"DeleteDocument": func() error { return tr.DeleteDocument(ctx, "db", "users", 1) },
		"ListDocuments": func() error {
			_, err := tr.ListDocuments(ctx, transport.ListDocumentsRequest{Database: "db", Collection: "users"})
			return err
		},
	}
}

// Every collection method answers a transport built without a collection
// service with the typed wiring-defect error, never a nil dereference.
func TestEmbeddedCollection_NilServiceIsTyped(t *testing.T) {
	for name, call := range collectionCalls(newExtTransport(t)) {
		var ne *NilServiceError
		if err := call(); !errors.As(err, &ne) {
			t.Errorf("%s: err = %T (%v), want *NilServiceError", name, err, err)
		}
	}
}

// A service failure reaches the caller unchanged from every method whose
// service call can fail in the double.
func TestEmbeddedCollection_ServiceErrorPropagates(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{
		createFn: func(context.Context, *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
			return nil, errSentinel
		},
		getInfoFn: func(context.Context, string, string) (*service.CollectionInfo, error) { return nil, errSentinel },
		listFn: func(context.Context, *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
			return nil, errSentinel
		},
		insertFn: func(context.Context, *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
			return nil, errSentinel
		},
		getDocFn: func(context.Context, *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
			return nil, errSentinel
		},
		listDocsFn: func(context.Context, *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
			return nil, errSentinel
		},
	}))
	calls := collectionCalls(tr)
	for _, name := range []string{"CreateCollection", "GetCollectionInfo", "ListCollections", "InsertDocument", "GetDocument", "ListDocuments"} {
		if err := calls[name](); !errors.Is(err, errSentinel) {
			t.Errorf("%s: err = %v, want the service's error", name, err)
		}
	}
}
