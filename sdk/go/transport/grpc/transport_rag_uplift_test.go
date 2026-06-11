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

package grpc

import (
	"context"
	"errors"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// RAG tests
// ---------------------------------------------------------------------------

func TestGRPCRAG_IngestDocument_Success(t *testing.T) {
	tr, cleanup := startTestServer(t, &testQRDBServer{})
	defer cleanup()

	resp, err := tr.RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{
		DocumentID: "doc-1", Text: "hello world", Namespace: "ns1",
		ChunkStrategy: "sentence", ChunkSize: 512,
	})
	if err != nil {
		t.Fatalf("RAGIngestDocument: %v", err)
	}
	if resp.DocumentID != "doc-1" {
		t.Fatalf("unexpected DocumentID: %q", resp.DocumentID)
	}
}

func TestGRPCRAG_IngestDocument_Closed(t *testing.T) {
	_, err := newUpliftClosed().RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGRPCRAG_IngestDocument_RPCError(t *testing.T) {
	svc := &testQRDBServer{
		ragIngestFn: func(_ context.Context, _ *pb.RAGIngestDocumentRequest) (*pb.RAGIngestDocumentResponse, error) {
			return nil, errors.New("ingest failed")
		},
	}
	tr, cleanup := startTestServer(t, svc)
	defer cleanup()

	_, err := tr.RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{DocumentID: "d1"})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ragIngestChunkConfig helpers
func TestGRPCRAG_IngestChunkConfig_Empty(t *testing.T) {
	got := ragIngestChunkConfig(&transport.RAGIngestRequest{})
	if got != nil {
		t.Fatal("expected nil for empty chunk config")
	}
}

func TestGRPCRAG_IngestChunkConfig_NonEmpty(t *testing.T) {
	got := ragIngestChunkConfig(&transport.RAGIngestRequest{ChunkSize: 512})
	if got == nil {
		t.Fatal("expected non-nil chunk config")
	}
	if got.ChunkSize != 512 {
		t.Fatalf("unexpected ChunkSize: %d", got.ChunkSize)
	}
}

func TestGRPCRAG_Query_Success(t *testing.T) {
	tr, cleanup := startTestServer(t, &testQRDBServer{})
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

func TestGRPCRAG_Query_Closed(t *testing.T) {
	_, err := newUpliftClosed().RAGQuery(context.Background(), &transport.RAGQueryRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGRPCRAG_Query_RPCError(t *testing.T) {
	svc := &testQRDBServer{
		ragQueryFn: func(_ context.Context, _ *pb.RAGQueryRequest) (*pb.RAGQueryResponse, error) {
			return nil, errors.New("query failed")
		},
	}
	tr, cleanup := startTestServer(t, svc)
	defer cleanup()

	_, err := tr.RAGQuery(context.Background(), &transport.RAGQueryRequest{Query: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGRPCRAG_DeleteDocument_Success(t *testing.T) {
	tr, cleanup := startTestServer(t, &testQRDBServer{})
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

func TestGRPCRAG_DeleteDocument_Closed(t *testing.T) {
	_, err := newUpliftClosed().RAGDeleteDocument(context.Background(), &transport.RAGDeleteDocumentRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGRPCRAG_DeleteDocument_RPCError(t *testing.T) {
	svc := &testQRDBServer{
		ragDeleteDocFn: func(_ context.Context, _ *pb.RAGDeleteDocumentRequest) (*pb.RAGDeleteDocumentResponse, error) {
			return nil, errors.New("delete failed")
		},
	}
	tr, cleanup := startTestServer(t, svc)
	defer cleanup()

	_, err := tr.RAGDeleteDocument(context.Background(), &transport.RAGDeleteDocumentRequest{DocumentID: "d1"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGRPCRAG_ListDocuments_Success(t *testing.T) {
	tr, cleanup := startTestServer(t, &testQRDBServer{})
	defer cleanup()

	resp, err := tr.RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{Namespace: "ns1"})
	if err != nil {
		t.Fatalf("RAGListDocuments: %v", err)
	}
	if len(resp.Documents) == 0 {
		t.Fatal("expected documents")
	}
}

func TestGRPCRAG_ListDocuments_Closed(t *testing.T) {
	_, err := newUpliftClosed().RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGRPCRAG_ListDocuments_RPCError(t *testing.T) {
	svc := &testQRDBServer{
		ragListDocsFn: func(_ context.Context, _ *pb.RAGListDocumentsRequest) (*pb.RAGListDocumentsResponse, error) {
			return nil, errors.New("list failed")
		},
	}
	tr, cleanup := startTestServer(t, svc)
	defer cleanup()

	_, err := tr.RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{Namespace: "ns1"})
	if err == nil {
		t.Fatal("expected error")
	}
}
