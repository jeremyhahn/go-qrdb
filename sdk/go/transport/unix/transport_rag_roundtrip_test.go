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

// Round-trip tests for the RAG surface (transport.RAGClient), closing the
// unix-vs-grpc SDK transport parity gap under the qrdb_vector build tag.

package unix

import (
	"context"
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockRAGSvc
// ---------------------------------------------------------------------------

type mockRAGSvc struct {
	ingestFn func(ctx context.Context, req *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error)
	queryFn  func(ctx context.Context, req *service.RAGQueryRequest) (*service.RAGQueryResponse, error)
	deleteFn func(ctx context.Context, req *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error)
	listFn   func(ctx context.Context, req *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error)
}

var _ service.RAGServicer = (*mockRAGSvc)(nil)

func (m *mockRAGSvc) IngestDocument(ctx context.Context, req *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
	if m.ingestFn != nil {
		return m.ingestFn(ctx, req)
	}
	return &service.IngestDocumentResponse{DocumentID: req.DocumentID, ChunkCount: 3, Namespace: req.Namespace}, nil
}

func (m *mockRAGSvc) Query(ctx context.Context, req *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, req)
	}
	return &service.RAGQueryResponse{
		Results: []*service.RAGQueryResult{{ChunkText: "chunk text", Score: 0.9, DocumentID: "doc1", ChunkIndex: 0}},
	}, nil
}

func (m *mockRAGSvc) DeleteDocument(ctx context.Context, req *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, req)
	}
	return &service.DeleteRAGDocumentResponse{DocumentID: req.DocumentID, ChunksRemoved: 3}, nil
}

func (m *mockRAGSvc) ListDocuments(ctx context.Context, req *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
	if m.listFn != nil {
		return m.listFn(ctx, req)
	}
	return &service.ListRAGDocumentsResponse{
		Documents: []*service.RAGDocumentInfo{{ID: "doc1", Namespace: req.Namespace, ChunkCount: 3}},
		Total:     1,
	}, nil
}

func (m *mockRAGSvc) Close() error { return nil }

// ---------------------------------------------------------------------------
// RAGIngestDocument
// ---------------------------------------------------------------------------

func TestUnixRT_RAGIngestDocument(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithRAGService(&mockRAGSvc{}))
	defer cleanup()

	resp, err := tr.RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{DocumentID: "doc1", Text: "hello world"})
	if err != nil {
		t.Fatalf("RAGIngestDocument: %v", err)
	}
	if resp.DocumentID != "doc1" || resp.ChunkCount != 3 {
		t.Errorf("got %+v, want documentID=doc1 chunkCount=3", resp)
	}
}

func TestUnixRT_RAGIngestDocument_Error(t *testing.T) {
	svc := &mockRAGSvc{ingestFn: func(_ context.Context, _ *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithRAGService(svc))
	defer cleanup()

	if _, err := tr.RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{DocumentID: "doc1"}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// RAGQuery
// ---------------------------------------------------------------------------

func TestUnixRT_RAGQuery(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithRAGService(&mockRAGSvc{}))
	defer cleanup()

	resp, err := tr.RAGQuery(context.Background(), &transport.RAGQueryRequest{Query: "what is qrdb"})
	if err != nil {
		t.Fatalf("RAGQuery: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].DocumentID != "doc1" {
		t.Errorf("got %+v, want 1 result from doc1", resp)
	}
}

func TestUnixRT_RAGQuery_Error(t *testing.T) {
	svc := &mockRAGSvc{queryFn: func(_ context.Context, _ *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithRAGService(svc))
	defer cleanup()

	if _, err := tr.RAGQuery(context.Background(), &transport.RAGQueryRequest{Query: "x"}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// RAGDeleteDocument
// ---------------------------------------------------------------------------

func TestUnixRT_RAGDeleteDocument(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithRAGService(&mockRAGSvc{}))
	defer cleanup()

	resp, err := tr.RAGDeleteDocument(context.Background(), &transport.RAGDeleteDocumentRequest{DocumentID: "doc1"})
	if err != nil {
		t.Fatalf("RAGDeleteDocument: %v", err)
	}
	if resp.DocumentID != "doc1" || resp.ChunksRemoved != 3 {
		t.Errorf("got %+v, want documentID=doc1 chunksRemoved=3", resp)
	}
}

func TestUnixRT_RAGDeleteDocument_Error(t *testing.T) {
	svc := &mockRAGSvc{deleteFn: func(_ context.Context, _ *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithRAGService(svc))
	defer cleanup()

	if _, err := tr.RAGDeleteDocument(context.Background(), &transport.RAGDeleteDocumentRequest{DocumentID: "doc1"}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// RAGListDocuments
// ---------------------------------------------------------------------------

func TestUnixRT_RAGListDocuments(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithRAGService(&mockRAGSvc{}))
	defer cleanup()

	resp, err := tr.RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{})
	if err != nil {
		t.Fatalf("RAGListDocuments: %v", err)
	}
	if len(resp.Documents) != 1 || resp.Total != 1 {
		t.Errorf("got %+v, want 1 document total=1", resp)
	}
}

func TestUnixRT_RAGListDocuments_Error(t *testing.T) {
	svc := &mockRAGSvc{listFn: func(_ context.Context, _ *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithRAGService(svc))
	defer cleanup()

	if _, err := tr.RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// ragIngestChunkConfig
// ---------------------------------------------------------------------------

func TestUnix_RagIngestChunkConfig_EmptyReturnsNil(t *testing.T) {
	if got := ragIngestChunkConfig(&transport.RAGIngestRequest{}); got != nil {
		t.Errorf("ragIngestChunkConfig(empty) = %+v, want nil", got)
	}
}

func TestUnix_RagIngestChunkConfig_NonEmpty(t *testing.T) {
	got := ragIngestChunkConfig(&transport.RAGIngestRequest{ChunkStrategy: "fixed", ChunkSize: 512})
	if got == nil || got.Strategy != "fixed" || got.ChunkSize != 512 {
		t.Errorf("ragIngestChunkConfig(nonEmpty) = %+v, want strategy=fixed chunkSize=512", got)
	}
}
