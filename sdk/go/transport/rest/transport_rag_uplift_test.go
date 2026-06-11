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
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// RAG: RAGIngestDocument / RAGQuery / RAGDeleteDocument / RAGListDocuments
// ===========================================================================

func TestRESTRAGIngestDocument_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/rag/ingest"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, transport.RAGIngestResponse{DocumentID: "doc-1", ChunkCount: 5})
	})
	resp, err := rt.RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{
		Text:      "some text",
		Namespace: "docs",
	})
	if err != nil {
		t.Fatalf("RAGIngestDocument: %v", err)
	}
	if resp.DocumentID != "doc-1" {
		t.Errorf("expected doc-1, got %q", resp.DocumentID)
	}
}

func TestRESTRAGIngestDocument_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTRAGIngestDocument_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/rag/ingest"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{Text: "x"})
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
}

func TestRESTRAGQuery_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/rag/query"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, transport.RAGQueryResponse{
			Results: []*transport.RAGQueryResult{{DocumentID: "doc-1", Score: 0.9}},
		})
	})
	resp, err := rt.RAGQuery(context.Background(), &transport.RAGQueryRequest{Query: "what is X"})
	if err != nil {
		t.Fatalf("RAGQuery: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
}

func TestRESTRAGQuery_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.RAGQuery(context.Background(), &transport.RAGQueryRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTRAGQuery_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/rag/query"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "failed", http.StatusInternalServerError)
	})
	_, err := rt.RAGQuery(context.Background(), &transport.RAGQueryRequest{Query: "q"})
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
}

func TestRESTRAGDeleteDocument_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/rag/documents/delete"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, transport.RAGDeleteDocumentResponse{DocumentID: "doc-1", ChunksRemoved: 5})
	})
	resp, err := rt.RAGDeleteDocument(context.Background(), &transport.RAGDeleteDocumentRequest{DocumentID: "doc-1"})
	if err != nil {
		t.Fatalf("RAGDeleteDocument: %v", err)
	}
	if resp.DocumentID != "doc-1" {
		t.Errorf("expected DocumentID doc-1, got %q", resp.DocumentID)
	}
}

func TestRESTRAGDeleteDocument_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.RAGDeleteDocument(context.Background(), &transport.RAGDeleteDocumentRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTRAGDeleteDocument_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/rag/documents/delete"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.RAGDeleteDocument(context.Background(), &transport.RAGDeleteDocumentRequest{DocumentID: "x"})
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
}

func TestRESTRAGListDocuments_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/v1/rag/documents"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, transport.RAGListDocumentsResponse{
			Documents: []*transport.RAGDocumentInfo{{ID: "doc-1"}},
			Total:     1,
		})
	})
	resp, err := rt.RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{
		Namespace: "docs",
		Limit:     10,
		Offset:    0,
	})
	if err != nil {
		t.Fatalf("RAGListDocuments: %v", err)
	}
	if len(resp.Documents) != 1 {
		t.Fatalf("expected 1 document, got %d", len(resp.Documents))
	}
}

func TestRESTRAGListDocuments_WithOffsetLimit(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var capturedPath string
	rs.handle(pathPrefix(http.MethodGet, "/v1/rag/documents"), func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.RawQuery
		writeJSON(w, transport.RAGListDocumentsResponse{})
	})
	_, err := rt.RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{
		Limit:  5,
		Offset: 10,
	})
	if err != nil {
		t.Fatalf("RAGListDocuments: %v", err)
	}
	if capturedPath == "" {
		t.Error("expected query params to be set")
	}
}

func TestRESTRAGListDocuments_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTRAGListDocuments_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/v1/rag/documents"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "failed", http.StatusServiceUnavailable)
	})
	_, err := rt.RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{})
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
}
