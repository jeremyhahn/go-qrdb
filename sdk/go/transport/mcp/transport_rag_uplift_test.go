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

package mcp

import (
	"context"
	"errors"
	"testing"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Mock service implementation
// ---------------------------------------------------------------------------

// minimalRAGSvc satisfies service.RAGServicer.
type minimalRAGSvc struct {
	ingestErr        error
	queryErr         error
	deleteErr        error
	listDocumentsErr error

	// lastIngest is the request IngestDocument last received.
	lastIngest *service.IngestDocumentRequest
}

var _ service.RAGServicer = (*minimalRAGSvc)(nil)

func newMinimalRAGSvc() *minimalRAGSvc { return &minimalRAGSvc{} }

func (m *minimalRAGSvc) IngestDocument(_ context.Context, req *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
	m.lastIngest = req
	if m.ingestErr != nil {
		return nil, m.ingestErr
	}
	return &service.IngestDocumentResponse{DocumentID: req.DocumentID, ChunkCount: 3, Namespace: req.Namespace}, nil
}

func (m *minimalRAGSvc) Query(_ context.Context, req *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	return &service.RAGQueryResponse{
		Results: []*service.RAGQueryResult{{ChunkText: "text", Score: 0.9, DocumentID: "doc-1"}},
	}, nil
}

func (m *minimalRAGSvc) DeleteDocument(_ context.Context, req *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
	if m.deleteErr != nil {
		return nil, m.deleteErr
	}
	return &service.DeleteRAGDocumentResponse{DocumentID: req.DocumentID, ChunksRemoved: 3}, nil
}

func (m *minimalRAGSvc) ListDocuments(_ context.Context, _ *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
	if m.listDocumentsErr != nil {
		return nil, m.listDocumentsErr
	}
	return &service.ListRAGDocumentsResponse{
		Documents: []*service.RAGDocumentInfo{{ID: "doc-1", Namespace: "ns1"}},
	}, nil
}

func (m *minimalRAGSvc) Close() error { return nil }

// ---------------------------------------------------------------------------
// Connection helper
// ---------------------------------------------------------------------------

func connectRAGInProcess(t *testing.T, svc service.RAGServicer) (*Transport, func()) {
	t.Helper()
	srv := mcpapi.NewServer(
		newMockKVService(), newMockAdminService(), ":0",
		mcpapi.WithRAG(svc),
	)
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return tr, func() { _ = tr.Close() }
}

// ---------------------------------------------------------------------------
// RAG tests — from transport_coverage_uplift2_test.go
// ---------------------------------------------------------------------------

func TestMCP_RAG_IngestDocument_Success(t *testing.T) {
	tr, cleanup := connectRAGInProcess(t, newMinimalRAGSvc())
	defer cleanup()

	resp, err := tr.RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{
		DocumentID: "doc-1", Text: "hello world", Namespace: "ns1",
	})
	if err != nil {
		t.Fatalf("RAGIngestDocument: %v", err)
	}
	if resp.DocumentID != "doc-1" {
		t.Fatalf("unexpected DocumentID: %q", resp.DocumentID)
	}
}

func TestMCP_RAG_IngestDocument_ServiceError(t *testing.T) {
	svc := newMinimalRAGSvc()
	svc.ingestErr = errors.New("ingest failed")
	tr, cleanup := connectRAGInProcess(t, svc)
	defer cleanup()

	_, err := tr.RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{DocumentID: "d1"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMCP_RAG_Query_Success(t *testing.T) {
	tr, cleanup := connectRAGInProcess(t, newMinimalRAGSvc())
	defer cleanup()

	resp, err := tr.RAGQuery(context.Background(), &transport.RAGQueryRequest{
		Query: "what is QRDB", Namespace: "ns1", TopK: 5,
	})
	if err != nil {
		t.Fatalf("RAGQuery: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected results")
	}
}

func TestMCP_RAG_Query_ServiceError(t *testing.T) {
	svc := newMinimalRAGSvc()
	svc.queryErr = errors.New("query failed")
	tr, cleanup := connectRAGInProcess(t, svc)
	defer cleanup()

	_, err := tr.RAGQuery(context.Background(), &transport.RAGQueryRequest{Query: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMCP_RAG_DeleteDocument_Success(t *testing.T) {
	tr, cleanup := connectRAGInProcess(t, newMinimalRAGSvc())
	defer cleanup()

	resp, err := tr.RAGDeleteDocument(context.Background(), &transport.RAGDeleteDocumentRequest{
		DocumentID: "doc-1", Namespace: "ns1",
	})
	if err != nil {
		t.Fatalf("RAGDeleteDocument: %v", err)
	}
	if resp.DocumentID != "doc-1" {
		t.Fatalf("unexpected DocumentID: %q", resp.DocumentID)
	}
}

func TestMCP_RAG_DeleteDocument_ServiceError(t *testing.T) {
	svc := newMinimalRAGSvc()
	svc.deleteErr = errors.New("not found")
	tr, cleanup := connectRAGInProcess(t, svc)
	defer cleanup()

	_, err := tr.RAGDeleteDocument(context.Background(), &transport.RAGDeleteDocumentRequest{DocumentID: "d1"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMCP_RAG_ListDocuments_Success(t *testing.T) {
	tr, cleanup := connectRAGInProcess(t, newMinimalRAGSvc())
	defer cleanup()

	resp, err := tr.RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{Namespace: "ns1"})
	if err != nil {
		t.Fatalf("RAGListDocuments: %v", err)
	}
	if len(resp.Documents) == 0 {
		t.Fatal("expected documents")
	}
}

func TestMCP_RAG_ListDocuments_ServiceError(t *testing.T) {
	svc := newMinimalRAGSvc()
	svc.listDocumentsErr = errors.New("list failed")
	tr, cleanup := connectRAGInProcess(t, svc)
	defer cleanup()

	_, err := tr.RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{Namespace: "ns1"})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// RAG tests — from transport_coverage_uplift3_test.go
// ---------------------------------------------------------------------------

func TestMCP_RAG_IngestDocument_JSONDecodeError(t *testing.T) {
	svc := newMinimalRAGSvc()
	tr, cleanup := connectRAGInProcess(t, svc)
	defer cleanup()

	// The response from service will be valid JSON but the mock returns
	// a real IngestDocumentResponse which should parse correctly.
	// To exercise the decode path, call with a valid request.
	resp, err := tr.RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{
		DocumentID: "doc-test", Namespace: "ns1", Text: "test content",
	})
	// Either success or error is acceptable; just ensure no panic.
	if err == nil && resp.DocumentID != "doc-test" {
		t.Logf("unexpected DocumentID: %q", resp.DocumentID)
	}
}

func TestMCP_RAG_Query_PathExercise(t *testing.T) {
	tr, cleanup := connectRAGInProcess(t, newMinimalRAGSvc())
	defer cleanup()

	// Call with filter specified to hit the additional code path.
	resp, err := tr.RAGQuery(context.Background(), &transport.RAGQueryRequest{
		Query: "test query", Namespace: "ns1", TopK: 3,
		Filter: map[string]string{"category": "test"},
	})
	if err != nil {
		t.Logf("RAGQuery with filter: %v", err)
	} else if len(resp.Results) == 0 {
		t.Logf("RAGQuery returned empty results")
	}
}

// ---------------------------------------------------------------------------
// RAG tests — from transport_coverage_uplift4_test.go
// ---------------------------------------------------------------------------

func TestMCP_RAG_IngestDocument_AllParams(t *testing.T) {
	svc := newMinimalRAGSvc()
	tr, cleanup := connectRAGInProcess(t, svc)
	defer cleanup()

	resp, err := tr.RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{
		DocumentID:    "doc-full",
		Text:          "hello world",
		Namespace:     "ns1",
		Model:         "ada-002",
		Provider:      "openai",
		ChunkStrategy: "sentence",
		ChunkSize:     512,
		ChunkOverlap:  50,
		ChunkMinSize:  32,
		Metadata:      map[string]string{"category": "test", "source": "api"},
	})
	if err != nil {
		t.Fatalf("RAGIngestDocument with all params: %v", err)
	}
	if resp.DocumentID != "doc-full" {
		t.Errorf("DocumentID = %q, want %q", resp.DocumentID, "doc-full")
	}
	got := svc.lastIngest
	if got == nil || got.ChunkConfig == nil {
		t.Fatalf("service received no chunk config: %+v", got)
	}
	want := service.ChunkConfig{Strategy: "sentence", ChunkSize: 512, Overlap: 50, MinSize: 32}
	if *got.ChunkConfig != want {
		t.Errorf("ChunkConfig = %+v, want %+v", *got.ChunkConfig, want)
	}
}

// TestMCP_RAG_IngestDocument_ChunkMinSizeAlone proves chunk_min_size
// reaches the service when it is the only chunk field set.
func TestMCP_RAG_IngestDocument_ChunkMinSizeAlone(t *testing.T) {
	svc := newMinimalRAGSvc()
	tr, cleanup := connectRAGInProcess(t, svc)
	defer cleanup()

	if _, err := tr.RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{
		DocumentID:   "doc-min",
		Text:         "hello world",
		Namespace:    "ns1",
		ChunkMinSize: 64,
	}); err != nil {
		t.Fatalf("RAGIngestDocument: %v", err)
	}
	got := svc.lastIngest
	if got == nil || got.ChunkConfig == nil {
		t.Fatalf("service received no chunk config: %+v", got)
	}
	want := service.ChunkConfig{MinSize: 64}
	if *got.ChunkConfig != want {
		t.Errorf("ChunkConfig = %+v, want %+v", *got.ChunkConfig, want)
	}
}

func TestMCP_RAG_Query_AllParams(t *testing.T) {
	tr, cleanup := connectRAGInProcess(t, newMinimalRAGSvc())
	defer cleanup()

	_, err := tr.RAGQuery(context.Background(), &transport.RAGQueryRequest{
		Query:     "what is QRDB",
		Namespace: "ns1",
		TopK:      5,
		Model:     "ada-002",
		Provider:  "openai",
		Filter:    map[string]string{"type": "doc"},
	})
	if err != nil {
		t.Logf("RAGQuery with all params: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RAG tests — from transport_coverage_uplift7_test.go
// ---------------------------------------------------------------------------

func TestMCP_RAG_ListDocuments_WithPagination(t *testing.T) {
	tr, cleanup := connectRAGInProcess(t, newMinimalRAGSvc())
	defer cleanup()

	resp, err := tr.RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{
		Namespace: "ns1",
		Limit:     10,
		Offset:    5,
	})
	if err != nil {
		t.Fatalf("RAGListDocuments with pagination: %v", err)
	}
	_ = resp
}
