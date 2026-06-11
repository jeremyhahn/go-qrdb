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

// RAG coverage-uplift tests for the QUIC SDK transport. These tests require
// the qrdb_vector build tag because they reference RAG types and methods that
// are only compiled under that tag.

package quic

import (
	"context"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// RAG — success, closed, rpc-error, unmarshal-error, helper
// ---------------------------------------------------------------------------

func TestE2E_RAG_IngestDocument_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.IngestDocument", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.RAGIngestDocumentResponse{
			DocumentId: "doc-1", ChunkCount: 3, Namespace: "ns1",
		})
	})
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

func TestE2E_RAG_IngestDocument_Closed(t *testing.T) {
	_, err := newQuicClosed().RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{})
	assertQuicClosed(t, err)
}

func TestE2E_RAG_IngestDocument_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.IngestDocument", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("ingest failed")
	})
	_, err := tr.RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{DocumentID: "d1"})
	assertQuicRequestError(t, err)
}

func TestE2E_RAG_Query_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.Query", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.RAGQueryResponse{
			Results: []*pb.RAGQueryResult{
				{ChunkText: "relevant chunk", Score: 0.9, DocumentId: "doc-1"},
			},
		})
	})
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

func TestE2E_RAG_Query_Closed(t *testing.T) {
	_, err := newQuicClosed().RAGQuery(context.Background(), &transport.RAGQueryRequest{})
	assertQuicClosed(t, err)
}

func TestE2E_RAG_Query_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.Query", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("query failed")
	})
	_, err := tr.RAGQuery(context.Background(), &transport.RAGQueryRequest{Query: "x"})
	assertQuicRequestError(t, err)
}

func TestE2E_RAG_DeleteDocument_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.DeleteDocument", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.RAGDeleteDocumentResponse{
			DocumentId: "doc-1", ChunksRemoved: 3,
		})
	})
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

func TestE2E_RAG_DeleteDocument_Closed(t *testing.T) {
	_, err := newQuicClosed().RAGDeleteDocument(context.Background(), &transport.RAGDeleteDocumentRequest{})
	assertQuicClosed(t, err)
}

func TestE2E_RAG_DeleteDocument_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.DeleteDocument", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("delete failed")
	})
	_, err := tr.RAGDeleteDocument(context.Background(), &transport.RAGDeleteDocumentRequest{DocumentID: "d1"})
	assertQuicRequestError(t, err)
}

func TestE2E_RAG_ListDocuments_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.ListDocuments", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.RAGListDocumentsResponse{
			Documents: []*pb.RAGDocumentInfo{{Id: "doc-1", Namespace: "ns1", ChunkCount: 3}},
			Total:     1,
		})
	})
	resp, err := tr.RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{Namespace: "ns1"})
	if err != nil {
		t.Fatalf("RAGListDocuments: %v", err)
	}
	if len(resp.Documents) == 0 {
		t.Fatal("expected documents")
	}
}

func TestE2E_RAG_ListDocuments_Closed(t *testing.T) {
	_, err := newQuicClosed().RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{})
	assertQuicClosed(t, err)
}

func TestE2E_RAG_ListDocuments_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.ListDocuments", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("list failed")
	})
	_, err := tr.RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{Namespace: "ns1"})
	assertQuicRequestError(t, err)
}

// quicRAGChunkConfig helpers
func TestE2E_RAG_QuicRAGChunkConfig_Empty(t *testing.T) {
	if got := quicRAGChunkConfig(&transport.RAGIngestRequest{}); got != nil {
		t.Fatal("expected nil for empty chunk config")
	}
}

func TestE2E_RAG_QuicRAGChunkConfig_NonEmpty(t *testing.T) {
	got := quicRAGChunkConfig(&transport.RAGIngestRequest{ChunkSize: 512})
	if got == nil {
		t.Fatal("expected non-nil chunk config")
	}
}

// ---------------------------------------------------------------------------
// RAG unmarshal-error paths
// ---------------------------------------------------------------------------

func TestE2E_RAG_IngestDocument_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.IngestDocument", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("bad-proto")
	})
	_, err := tr.RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{DocumentID: "d1"})
	assertQuicRequestError(t, err)
}

func TestE2E_RAG_Query_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.Query", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("bad-proto")
	})
	_, err := tr.RAGQuery(context.Background(), &transport.RAGQueryRequest{Query: "x"})
	assertQuicRequestError(t, err)
}

func TestE2E_RAG_DeleteDocument_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.DeleteDocument", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("bad-proto")
	})
	_, err := tr.RAGDeleteDocument(context.Background(), &transport.RAGDeleteDocumentRequest{DocumentID: "d1"})
	assertQuicRequestError(t, err)
}

func TestE2E_RAG_ListDocuments_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.ListDocuments", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("bad-proto")
	})
	_, err := tr.RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{Namespace: "ns1"})
	assertQuicRequestError(t, err)
}

// ---------------------------------------------------------------------------
// Additional closed-path guards for RAG methods
// ---------------------------------------------------------------------------

func TestQUICExtraClosed_RAG(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{}); !errorAs(err, &ce) {
		t.Errorf("RAGIngestDocument: %v", err)
	}
	if _, err := tr.RAGQuery(context.Background(), &transport.RAGQueryRequest{}); !errorAs(err, &ce) {
		t.Errorf("RAGQuery: %v", err)
	}
	if _, err := tr.RAGDeleteDocument(context.Background(), &transport.RAGDeleteDocumentRequest{}); !errorAs(err, &ce) {
		t.Errorf("RAGDeleteDocument: %v", err)
	}
	if _, err := tr.RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{}); !errorAs(err, &ce) {
		t.Errorf("RAGListDocuments: %v", err)
	}
}
