//go:build !integration

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock RAGServicer
// ---------------------------------------------------------------------------

type mockRAGService struct {
	ingestFn func(context.Context, *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error)
	queryFn  func(context.Context, *service.RAGQueryRequest) (*service.RAGQueryResponse, error)
	deleteFn func(context.Context, *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error)
	listFn   func(context.Context, *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error)
}

var _ service.RAGServicer = (*mockRAGService)(nil)

func (m *mockRAGService) IngestDocument(ctx context.Context, req *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
	if m.ingestFn != nil {
		return m.ingestFn(ctx, req)
	}
	return &service.IngestDocumentResponse{DocumentID: req.DocumentID, ChunkCount: 2, Namespace: req.Namespace}, nil
}

func (m *mockRAGService) Query(ctx context.Context, req *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, req)
	}
	return &service.RAGQueryResponse{
		Results: []*service.RAGQueryResult{
			{ChunkText: "relevant passage", Score: 0.1, DocumentID: "doc1", ChunkIndex: 0},
		},
	}, nil
}

func (m *mockRAGService) DeleteDocument(ctx context.Context, req *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, req)
	}
	return &service.DeleteRAGDocumentResponse{DocumentID: req.DocumentID, ChunksRemoved: 3}, nil
}

func (m *mockRAGService) ListDocuments(ctx context.Context, req *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
	if m.listFn != nil {
		return m.listFn(ctx, req)
	}
	return &service.ListRAGDocumentsResponse{
		Documents: []*service.RAGDocumentInfo{
			{ID: "doc1", Namespace: req.Namespace, ChunkCount: 2},
		},
		Total: 1,
	}, nil
}

func (m *mockRAGService) Close() error { return nil }

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newRAGTestServer(svc service.RAGServicer) *Server {
	s := &Server{ragSvc: svc}
	s.routes = map[string]route{
		"/v1/rag/ingest":           {method: http.MethodPost, handler: s.handleRAGIngest},
		"/v1/rag/query":            {method: http.MethodPost, handler: s.handleRAGQuery},
		"/v1/rag/documents/delete": {method: http.MethodPost, handler: s.handleRAGDeleteDocument},
		"/v1/rag/documents":        {method: http.MethodGet, handler: s.handleRAGListDocuments},
	}
	return s
}

// ---------------------------------------------------------------------------
// Tests: handleRAGIngest
// ---------------------------------------------------------------------------

func TestHandleRAGIngest_Success(t *testing.T) {
	svc := &mockRAGService{}
	s := newRAGTestServer(svc)

	body, _ := json.Marshal(ragIngestRequest{
		DocumentID: "doc1",
		Text:       "The quick brown fox.",
		Namespace:  "test-ns",
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/rag/ingest", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handleRAGIngest(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	var resp ragIngestResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "doc1", resp.DocumentID)
	assert.Equal(t, 2, resp.ChunkCount)
}

func TestHandleRAGIngest_InvalidJSON(t *testing.T) {
	s := newRAGTestServer(&mockRAGService{})
	r := httptest.NewRequest(http.MethodPost, "/v1/rag/ingest", strings.NewReader("{invalid"))
	w := httptest.NewRecorder()
	s.handleRAGIngest(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleRAGIngest_ServiceError(t *testing.T) {
	svc := &mockRAGService{
		ingestFn: func(_ context.Context, _ *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
			return nil, &service.RAGIngestError{Cause: errors.New("embed fail")}
		},
	}
	s := newRAGTestServer(svc)
	body, _ := json.Marshal(ragIngestRequest{DocumentID: "doc1", Text: "text", Namespace: "ns"})
	r := httptest.NewRequest(http.MethodPost, "/v1/rag/ingest", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handleRAGIngest(w, r)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestHandleRAGIngest_NilService(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodPost, "/v1/rag/ingest", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	s.handleRAGIngest(w, r)
	assert.Equal(t, http.StatusNotImplemented, w.Code)
}

// ---------------------------------------------------------------------------
// Tests: handleRAGQuery
// ---------------------------------------------------------------------------

func TestHandleRAGQuery_Success(t *testing.T) {
	svc := &mockRAGService{}
	s := newRAGTestServer(svc)

	body, _ := json.Marshal(ragQueryRequest{Query: "brown fox", Namespace: "ns"})
	r := httptest.NewRequest(http.MethodPost, "/v1/rag/query", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handleRAGQuery(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	var resp ragQueryResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "relevant passage", resp.Results[0].ChunkText)
}

func TestHandleRAGQuery_InvalidJSON(t *testing.T) {
	s := newRAGTestServer(&mockRAGService{})
	r := httptest.NewRequest(http.MethodPost, "/v1/rag/query", strings.NewReader("{bad}"))
	w := httptest.NewRecorder()
	s.handleRAGQuery(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleRAGQuery_ServiceError(t *testing.T) {
	svc := &mockRAGService{
		queryFn: func(_ context.Context, _ *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
			return nil, &service.RAGQueryError{Cause: errors.New("index down")}
		},
	}
	s := newRAGTestServer(svc)
	body, _ := json.Marshal(ragQueryRequest{Query: "q", Namespace: "ns"})
	r := httptest.NewRequest(http.MethodPost, "/v1/rag/query", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handleRAGQuery(w, r)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestHandleRAGQuery_NilService(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodPost, "/v1/rag/query", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	s.handleRAGQuery(w, r)
	assert.Equal(t, http.StatusNotImplemented, w.Code)
}

// ---------------------------------------------------------------------------
// Tests: handleRAGDeleteDocument
// ---------------------------------------------------------------------------

func TestHandleRAGDeleteDocument_Success(t *testing.T) {
	svc := &mockRAGService{}
	s := newRAGTestServer(svc)

	r := httptest.NewRequest(http.MethodPost, "/v1/rag/documents/delete?id=doc1&namespace=ns", nil)
	w := httptest.NewRecorder()
	s.handleRAGDeleteDocument(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	var resp ragDeleteDocumentResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "doc1", resp.DocumentID)
	assert.Equal(t, 3, resp.ChunksRemoved)
}

func TestHandleRAGDeleteDocument_ServiceError(t *testing.T) {
	svc := &mockRAGService{
		deleteFn: func(_ context.Context, _ *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
			return nil, &service.RAGDocumentNotFoundError{ID: "doc1", Namespace: "ns"}
		},
	}
	s := newRAGTestServer(svc)
	r := httptest.NewRequest(http.MethodPost, "/v1/rag/documents/delete?id=doc1&namespace=ns", nil)
	w := httptest.NewRecorder()
	s.handleRAGDeleteDocument(w, r)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleRAGDeleteDocument_NilService(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodPost, "/v1/rag/documents/delete", nil)
	w := httptest.NewRecorder()
	s.handleRAGDeleteDocument(w, r)
	assert.Equal(t, http.StatusNotImplemented, w.Code)
}

// ---------------------------------------------------------------------------
// Tests: handleRAGListDocuments
// ---------------------------------------------------------------------------

func TestHandleRAGListDocuments_Success(t *testing.T) {
	svc := &mockRAGService{}
	s := newRAGTestServer(svc)

	r := httptest.NewRequest(http.MethodGet, "/v1/rag/documents?namespace=ns", nil)
	w := httptest.NewRecorder()
	s.handleRAGListDocuments(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	var resp ragListDocumentsResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, 1, resp.Total)
	require.Len(t, resp.Documents, 1)
	assert.Equal(t, "doc1", resp.Documents[0].ID)
}

func TestHandleRAGListDocuments_ServiceError(t *testing.T) {
	svc := &mockRAGService{
		listFn: func(_ context.Context, _ *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
			return nil, errors.New("scan failed")
		},
	}
	s := newRAGTestServer(svc)
	r := httptest.NewRequest(http.MethodGet, "/v1/rag/documents?namespace=ns", nil)
	w := httptest.NewRecorder()
	s.handleRAGListDocuments(w, r)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestHandleRAGListDocuments_NilService(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/v1/rag/documents", nil)
	w := httptest.NewRecorder()
	s.handleRAGListDocuments(w, r)
	assert.Equal(t, http.StatusNotImplemented, w.Code)
}
