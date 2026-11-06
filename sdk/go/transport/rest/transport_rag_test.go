//go:build qrdb_vector

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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRESTRAG_SendsTheDocumentedRequest drives each RAG method against a
// server that records what it receives: the method must send its documented
// verb, path, query and body, and decode the server's answer.
func TestRESTRAG_SendsTheDocumentedRequest(t *testing.T) {
	ingest := &transport.RAGIngestRequest{Text: "some text", Namespace: "docs"}
	query := &transport.RAGQueryRequest{Query: "what is X"}
	del := &transport.RAGDeleteDocumentRequest{DocumentID: "doc-1"}

	cases := []struct {
		name      string
		verb      string
		path      string
		rawQuery  string
		wantBody  any // nil: no body
		answer    any
		callCheck func(ctx context.Context, rt *Transport) error
	}{
		{"RAGIngestDocument", http.MethodPost, "/v1/rag/ingest", "", ingest,
			transport.RAGIngestResponse{DocumentID: "doc-1", ChunkCount: 5},
			func(ctx context.Context, rt *Transport) error {
				resp, err := rt.RAGIngestDocument(ctx, ingest)
				if err == nil && (resp.DocumentID != "doc-1" || resp.ChunkCount != 5) {
					return errors.New("decoded " + resp.DocumentID + ", want doc-1 with 5 chunks")
				}
				return err
			}},
		{"RAGQuery", http.MethodPost, "/v1/rag/query", "", query,
			transport.RAGQueryResponse{Results: []*transport.RAGQueryResult{{DocumentID: "doc-1", Score: 0.9}}},
			func(ctx context.Context, rt *Transport) error {
				resp, err := rt.RAGQuery(ctx, query)
				if err == nil && (len(resp.Results) != 1 || resp.Results[0].DocumentID != "doc-1") {
					return errors.New("results not decoded, want doc-1")
				}
				return err
			}},
		{"RAGDeleteDocument", http.MethodPost, "/v1/rag/documents/delete", "", del,
			transport.RAGDeleteDocumentResponse{DocumentID: "doc-1", ChunksRemoved: 5},
			func(ctx context.Context, rt *Transport) error {
				resp, err := rt.RAGDeleteDocument(ctx, del)
				if err == nil && (resp.DocumentID != "doc-1" || resp.ChunksRemoved != 5) {
					return errors.New("decoded " + resp.DocumentID + ", want doc-1 with 5 chunks removed")
				}
				return err
			}},
		{"RAGListDocuments/AllParameters", http.MethodGet, "/v1/rag/documents", "limit=5&namespace=docs&offset=10", nil,
			transport.RAGListDocumentsResponse{Documents: []*transport.RAGDocumentInfo{{ID: "doc-1"}}, Total: 1},
			func(ctx context.Context, rt *Transport) error {
				resp, err := rt.RAGListDocuments(ctx, &transport.RAGListDocumentsRequest{Namespace: "docs", Limit: 5, Offset: 10})
				if err == nil && (len(resp.Documents) != 1 || resp.Documents[0].ID != "doc-1" || resp.Total != 1) {
					return errors.New("documents not decoded, want doc-1")
				}
				return err
			}},
		{"RAGListDocuments/NoParameters", http.MethodGet, "/v1/rag/documents", "", nil,
			transport.RAGListDocumentsResponse{},
			func(ctx context.Context, rt *Transport) error {
				_, err := rt.RAGListDocuments(ctx, &transport.RAGListDocumentsRequest{})
				return err
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var want []byte
			if c.wantBody != nil {
				var err error
				if want, err = json.Marshal(c.wantBody); err != nil {
					t.Fatalf("marshal want: %v", err)
				}
			}
			rt, rs, _ := newExtTransport(t)
			var gotQuery string
			var gotBody []byte
			rs.handle(exactPath(c.verb, c.path), func(w http.ResponseWriter, r *http.Request) {
				gotQuery = r.URL.RawQuery
				gotBody, _ = io.ReadAll(r.Body)
				writeJSON(w, c.answer)
			})

			if err := c.callCheck(context.Background(), rt); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if gotQuery != c.rawQuery {
				t.Fatalf("%s %s query = %q, want %q", c.verb, c.path, gotQuery, c.rawQuery)
			}
			if !bytes.Equal(bytes.TrimSpace(gotBody), want) {
				t.Fatalf("%s %s body = %s, want %s", c.verb, c.path, gotBody, want)
			}
		})
	}
}

func TestRESTRAG_Closed(t *testing.T) {
	assertClosedRefusesBeforeTheWire(t, map[string]func(context.Context, *Transport) error{
		"RAGIngestDocument": func(ctx context.Context, rt *Transport) error {
			_, err := rt.RAGIngestDocument(ctx, &transport.RAGIngestRequest{})
			return err
		},
		"RAGQuery": func(ctx context.Context, rt *Transport) error {
			_, err := rt.RAGQuery(ctx, &transport.RAGQueryRequest{})
			return err
		},
		"RAGDeleteDocument": func(ctx context.Context, rt *Transport) error {
			_, err := rt.RAGDeleteDocument(ctx, &transport.RAGDeleteDocumentRequest{})
			return err
		},
		"RAGListDocuments": func(ctx context.Context, rt *Transport) error {
			_, err := rt.RAGListDocuments(ctx, &transport.RAGListDocumentsRequest{})
			return err
		},
	})
}

// TestRESTRAG_ServerErrorIsARequestError checks that a non-2xx answer comes
// back as a *transport.RequestError naming the method and the status.
func TestRESTRAG_ServerErrorIsARequestError(t *testing.T) {
	cases := []struct {
		method string
		match  func(*http.Request) bool
		status int
		call   func(ctx context.Context, rt *Transport) error
	}{
		{"RAGIngestDocument", exactPath(http.MethodPost, "/v1/rag/ingest"), http.StatusInternalServerError,
			func(ctx context.Context, rt *Transport) error {
				_, err := rt.RAGIngestDocument(ctx, &transport.RAGIngestRequest{Text: "x"})
				return err
			}},
		{"RAGQuery", exactPath(http.MethodPost, "/v1/rag/query"), http.StatusInternalServerError,
			func(ctx context.Context, rt *Transport) error {
				_, err := rt.RAGQuery(ctx, &transport.RAGQueryRequest{Query: "q"})
				return err
			}},
		{"RAGDeleteDocument", exactPath(http.MethodPost, "/v1/rag/documents/delete"), http.StatusInternalServerError,
			func(ctx context.Context, rt *Transport) error {
				_, err := rt.RAGDeleteDocument(ctx, &transport.RAGDeleteDocumentRequest{DocumentID: "x"})
				return err
			}},
		{"RAGListDocuments", exactPath(http.MethodGet, "/v1/rag/documents"), http.StatusServiceUnavailable,
			func(ctx context.Context, rt *Transport) error {
				_, err := rt.RAGListDocuments(ctx, &transport.RAGListDocumentsRequest{})
				return err
			}},
	}
	for _, c := range cases {
		t.Run(c.method, func(t *testing.T) {
			rt, rs, _ := newExtTransport(t)
			rs.handle(c.match, func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "boom", c.status)
			})
			var reqErr *transport.RequestError
			if err := c.call(context.Background(), rt); !errors.As(err, &reqErr) || reqErr.Method != c.method || reqErr.StatusCode != c.status {
				t.Fatalf("%s = %T %v, want *transport.RequestError{Method: %s, StatusCode: %d}", c.method, err, err, c.method, c.status)
			}
		})
	}
}
