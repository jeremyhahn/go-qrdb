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
	"errors"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRESTCollection_SendsTheDocumentedRequest checks each collection and
// document RPC's verb, path, query and body, and that the answer decodes
// into the result.
func TestRESTCollection_SendsTheDocumentedRequest(t *testing.T) {
	create := transport.CreateCollectionRequest{Database: "db", Name: "users"}
	doc := map[string]any{"name": "alice"}
	users := &transport.CollectionInfo{Name: "users", Database: "db"}

	assertSendsTheDocumentedRequest(t, []wireCase{
		{name: "CreateCollection", verb: http.MethodPost, path: "/v1/collections", wantBody: create, answer: users,
			callCheck: func(ctx context.Context, rt *Transport) error {
				info, err := rt.CreateCollection(ctx, create)
				if err == nil && (info.Name != "users" || info.Database != "db") {
					return errors.New("collection not decoded, want db/users")
				}
				return err
			}},
		{name: "GetCollectionInfo", verb: http.MethodGet, path: "/v1/collections/db/users", answer: users,
			callCheck: func(ctx context.Context, rt *Transport) error {
				info, err := rt.GetCollectionInfo(ctx, "db", "users")
				if err == nil && (info.Name != "users" || info.Database != "db") {
					return errors.New("collection not decoded, want db/users")
				}
				return err
			}},
		{name: "ListCollections", verb: http.MethodGet, path: "/v1/collections/list", rawQuery: "database=db",
			answer: transport.ListCollectionsResponse{Collections: []transport.CollectionInfo{{Name: "users"}}},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.ListCollections(ctx, "db")
				if err == nil && (len(resp.Collections) != 1 || resp.Collections[0].Name != "users") {
					return errors.New("collections not decoded, want users")
				}
				return err
			}},
		{name: "DropCollection", verb: http.MethodDelete, path: "/v1/collections/db/users", status: http.StatusNoContent,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return rt.DropCollection(ctx, "db", "users")
			}},
		{name: "InsertDocument", verb: http.MethodPost, path: "/v1/collections/db/users/documents",
			wantBody: map[string]any{"document": doc}, answer: transport.InsertDocumentResponse{ID: 42},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.InsertDocument(ctx, transport.InsertDocumentRequest{Database: "db", Collection: "users", Document: doc})
				if err == nil && resp.ID != 42 {
					return errors.New("document id not decoded, want 42")
				}
				return err
			}},
		{name: "GetDocument", verb: http.MethodGet, path: "/v1/collections/db/users/documents/7",
			answer: transport.DocumentResponse{ID: 7, Document: doc},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.GetDocument(ctx, "db", "users", 7)
				if err == nil && (resp.ID != 7 || resp.Document["name"] != "alice") {
					return errors.New("document not decoded, want 7 named alice")
				}
				return err
			}},
		{name: "UpdateDocument", verb: http.MethodPut, path: "/v1/collections/db/users/documents/7",
			wantBody: map[string]any{"document": doc}, status: http.StatusNoContent,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return rt.UpdateDocument(ctx, transport.UpdateDocumentRequest{Database: "db", Collection: "users", ID: 7, Document: doc})
			}},
		{name: "DeleteDocument", verb: http.MethodDelete, path: "/v1/collections/db/users/documents/7", status: http.StatusNoContent,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return rt.DeleteDocument(ctx, "db", "users", 7)
			}},
		{name: "ListDocuments/Paginated", verb: http.MethodGet, path: "/v1/collections/db/users/documents",
			rawQuery: "page_size=10&page_token=tok",
			answer:   transport.ListDocumentsResponse{TotalCount: 1, Documents: []transport.DocumentResponse{{ID: 7}}},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.ListDocuments(ctx, transport.ListDocumentsRequest{Database: "db", Collection: "users", PageSize: 10, PageToken: "tok"})
				if err == nil && (resp.TotalCount != 1 || len(resp.Documents) != 1 || resp.Documents[0].ID != 7) {
					return errors.New("documents not decoded, want one document 7")
				}
				return err
			}},
		{name: "ListDocuments/NoPagination", verb: http.MethodGet, path: "/v1/collections/db/users/documents",
			answer: transport.ListDocumentsResponse{},
			callCheck: func(ctx context.Context, rt *Transport) error {
				_, err := rt.ListDocuments(ctx, transport.ListDocumentsRequest{Database: "db", Collection: "users"})
				return err
			}},
	})
}

// TestRESTCollection_SurfacesAFailedAnswer checks that each collection and
// document RPC reports a failed answer as a RequestError naming it.
func TestRESTCollection_SurfacesAFailedAnswer(t *testing.T) {
	assertSurfacesAFailedAnswer(t, []failedAnswerCase{
		{"CreateCollection", http.MethodPost, "/v1/collections", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.CreateCollection(ctx, transport.CreateCollectionRequest{Database: "db", Name: "users"})
			return err
		}},
		{"GetCollectionInfo", http.MethodGet, "/v1/collections/db/users", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetCollectionInfo(ctx, "db", "users")
			return err
		}},
		{"ListCollections", http.MethodGet, "/v1/collections/list", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListCollections(ctx, "db")
			return err
		}},
		{"DropCollection", http.MethodDelete, "/v1/collections/db/users", false, func(ctx context.Context, rt *Transport) error {
			return rt.DropCollection(ctx, "db", "users")
		}},
		{"InsertDocument", http.MethodPost, "/v1/collections/db/users/documents", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.InsertDocument(ctx, transport.InsertDocumentRequest{Database: "db", Collection: "users"})
			return err
		}},
		{"GetDocument", http.MethodGet, "/v1/collections/db/users/documents/7", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetDocument(ctx, "db", "users", 7)
			return err
		}},
		{"UpdateDocument", http.MethodPut, "/v1/collections/db/users/documents/7", false, func(ctx context.Context, rt *Transport) error {
			return rt.UpdateDocument(ctx, transport.UpdateDocumentRequest{Database: "db", Collection: "users", ID: 7})
		}},
		{"DeleteDocument", http.MethodDelete, "/v1/collections/db/users/documents/7", false, func(ctx context.Context, rt *Transport) error {
			return rt.DeleteDocument(ctx, "db", "users", 7)
		}},
		{"ListDocuments", http.MethodGet, "/v1/collections/db/users/documents", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListDocuments(ctx, transport.ListDocumentsRequest{Database: "db", Collection: "users"})
			return err
		}},
	})
}

// TestRESTCollection_Closed checks that every collection and document RPC
// on a closed transport refuses before the wire.
func TestRESTCollection_Closed(t *testing.T) {
	assertClosedRefusesBeforeTheWire(t, map[string]func(ctx context.Context, rt *Transport) error{
		"CreateCollection": func(ctx context.Context, rt *Transport) error {
			_, err := rt.CreateCollection(ctx, transport.CreateCollectionRequest{})
			return err
		},
		"GetCollectionInfo": func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetCollectionInfo(ctx, "db", "users")
			return err
		},
		"ListCollections": func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListCollections(ctx, "db")
			return err
		},
		"DropCollection": func(ctx context.Context, rt *Transport) error {
			return rt.DropCollection(ctx, "db", "users")
		},
		"InsertDocument": func(ctx context.Context, rt *Transport) error {
			_, err := rt.InsertDocument(ctx, transport.InsertDocumentRequest{})
			return err
		},
		"GetDocument": func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetDocument(ctx, "db", "users", 7)
			return err
		},
		"UpdateDocument": func(ctx context.Context, rt *Transport) error {
			return rt.UpdateDocument(ctx, transport.UpdateDocumentRequest{})
		},
		"DeleteDocument": func(ctx context.Context, rt *Transport) error {
			return rt.DeleteDocument(ctx, "db", "users", 7)
		},
		"ListDocuments": func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListDocuments(ctx, transport.ListDocumentsRequest{})
			return err
		},
	})
}
