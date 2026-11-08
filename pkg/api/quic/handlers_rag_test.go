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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mockRAGServiceQuic implements service.RAGServicer.
type mockRAGServiceQuic struct {
	ingestFn func(ctx context.Context, req *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error)
	queryFn  func(ctx context.Context, req *service.RAGQueryRequest) (*service.RAGQueryResponse, error)
	deleteFn func(ctx context.Context, req *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error)
	listFn   func(ctx context.Context, req *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error)
}

func (m *mockRAGServiceQuic) IngestDocument(ctx context.Context, req *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
	if m.ingestFn != nil {
		return m.ingestFn(ctx, req)
	}
	return &service.IngestDocumentResponse{DocumentID: req.DocumentID, ChunkCount: 3, Namespace: req.Namespace}, nil
}

func (m *mockRAGServiceQuic) Query(ctx context.Context, req *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, req)
	}
	return &service.RAGQueryResponse{
		Results: []*service.RAGQueryResult{
			{ChunkText: "chunk text", Score: 0.9, DocumentID: "doc1"},
		},
	}, nil
}

func (m *mockRAGServiceQuic) DeleteDocument(ctx context.Context, req *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, req)
	}
	return &service.DeleteRAGDocumentResponse{DocumentID: req.DocumentID, ChunksRemoved: 3}, nil
}

func (m *mockRAGServiceQuic) ListDocuments(ctx context.Context, req *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
	if m.listFn != nil {
		return m.listFn(ctx, req)
	}
	return &service.ListRAGDocumentsResponse{
		Documents: []*service.RAGDocumentInfo{{ID: "doc1", Namespace: req.Namespace, ChunkCount: 3}},
		Total:     1,
	}, nil
}

func (m *mockRAGServiceQuic) Close() error { return nil }

func TestHandleRAGIngestDocument_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.RAGIngestDocumentRequest{DocumentId: "d"})
	_, err := srv.handleRAGIngestDocument(context.Background(), payload)
	assertNotConfigured(t, err)
}

func TestHandleRAGIngestDocument_InvalidPayload(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(&mockRAGServiceQuic{}))
	_, err := srv.handleRAGIngestDocument(context.Background(), []byte{0xff})
	assertUnmarshalError(t, err)
}

// TestHandleRAGIngestDocument_Success also pins that an absent chunk config
// reaches the service as nil, so the service applies its defaults.
func TestHandleRAGIngestDocument_Success(t *testing.T) {
	var captured *service.IngestDocumentRequest
	svc := &mockRAGServiceQuic{
		ingestFn: func(_ context.Context, req *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
			captured = req
			return &service.IngestDocumentResponse{DocumentID: req.DocumentID, ChunkCount: 3, Namespace: req.Namespace}, nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(svc))
	payload, _ := proto.Marshal(&pb.RAGIngestDocumentRequest{
		DocumentId: "doc1",
		Text:       "hello world",
		Namespace:  "ns1",
	})
	resp, err := srv.handleRAGIngestDocument(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleRAGIngestDocument failed: %v", err)
	}
	if captured == nil || captured.DocumentID != "doc1" || captured.Text != "hello world" || captured.Namespace != "ns1" {
		t.Fatalf("service request = %+v", captured)
	}
	if captured.ChunkConfig != nil {
		t.Fatalf("chunk config = %+v, want nil", captured.ChunkConfig)
	}
	var out pb.RAGIngestDocumentResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetDocumentId() != "doc1" || out.GetChunkCount() != 3 {
		t.Fatalf("response = %+v", &out)
	}
}

func TestHandleRAGIngestDocument_WithChunkConfig(t *testing.T) {
	var captured *service.IngestDocumentRequest
	svc := &mockRAGServiceQuic{
		ingestFn: func(_ context.Context, req *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
			captured = req
			return &service.IngestDocumentResponse{DocumentID: req.DocumentID, ChunkCount: 1}, nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(svc))
	payload, _ := proto.Marshal(&pb.RAGIngestDocumentRequest{
		DocumentId: "d1",
		Text:       "text",
		ChunkConfig: &pb.ChunkConfig{
			Strategy:  "sentence",
			ChunkSize: 512,
			Overlap:   64,
			MinSize:   128,
		},
	})
	if _, err := srv.handleRAGIngestDocument(context.Background(), payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := service.ChunkConfig{Strategy: "sentence", ChunkSize: 512, Overlap: 64, MinSize: 128}
	if captured.ChunkConfig == nil || *captured.ChunkConfig != want {
		t.Fatalf("chunk config = %+v, want %+v", captured.ChunkConfig, want)
	}
}

func TestHandleRAGIngestDocument_ServiceError(t *testing.T) {
	sentinel := errors.New("ingest failed")
	svc := &mockRAGServiceQuic{
		ingestFn: func(_ context.Context, _ *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
			return nil, sentinel
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(svc))
	payload, _ := proto.Marshal(&pb.RAGIngestDocumentRequest{DocumentId: "d"})
	if _, err := srv.handleRAGIngestDocument(context.Background(), payload); !errors.Is(err, sentinel) {
		t.Fatalf("expected the service error, got %v", err)
	}
}

func TestHandleRAGQuery_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.RAGQueryRequest{Query: "q"})
	_, err := srv.handleRAGQuery(context.Background(), payload)
	assertNotConfigured(t, err)
}

func TestHandleRAGQuery_Success(t *testing.T) {
	var captured *service.RAGQueryRequest
	svc := &mockRAGServiceQuic{
		queryFn: func(_ context.Context, req *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
			captured = req
			return &service.RAGQueryResponse{
				Results: []*service.RAGQueryResult{
					{ChunkText: "chunk text", Score: 0.9, DocumentID: "doc1", ChunkIndex: 2},
				},
			}, nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(svc))
	payload, _ := proto.Marshal(&pb.RAGQueryRequest{Query: "what is qrdb", Namespace: "ns1", TopK: 5})
	resp, err := srv.handleRAGQuery(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleRAGQuery failed: %v", err)
	}
	if captured == nil || captured.Query != "what is qrdb" || captured.Namespace != "ns1" || captured.TopK != 5 {
		t.Fatalf("service request = %+v", captured)
	}
	var out pb.RAGQueryResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GetResults()) != 1 {
		t.Fatalf("expected 1 result, got %d", len(out.GetResults()))
	}
	r := out.GetResults()[0]
	if r.GetChunkText() != "chunk text" || r.GetDocumentId() != "doc1" || r.GetChunkIndex() != 2 || r.GetScore() < 0.89 {
		t.Fatalf("result = %+v", r)
	}
}

func TestHandleRAGQuery_ServiceError(t *testing.T) {
	sentinel := errors.New("query failed")
	svc := &mockRAGServiceQuic{
		queryFn: func(_ context.Context, _ *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
			return nil, sentinel
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(svc))
	payload, _ := proto.Marshal(&pb.RAGQueryRequest{Query: "q"})
	if _, err := srv.handleRAGQuery(context.Background(), payload); !errors.Is(err, sentinel) {
		t.Fatalf("expected the service error, got %v", err)
	}
}

func TestHandleRAGDeleteDocument_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.RAGDeleteDocumentRequest{DocumentId: "d"})
	_, err := srv.handleRAGDeleteDocument(context.Background(), payload)
	assertNotConfigured(t, err)
}

func TestHandleRAGDeleteDocument_Success(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(&mockRAGServiceQuic{}))
	payload, _ := proto.Marshal(&pb.RAGDeleteDocumentRequest{DocumentId: "doc1", Namespace: "ns1"})
	resp, err := srv.handleRAGDeleteDocument(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleRAGDeleteDocument failed: %v", err)
	}
	var out pb.RAGDeleteDocumentResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetDocumentId() != "doc1" || out.GetChunksRemoved() != 3 {
		t.Fatalf("response = %+v", &out)
	}
}

func TestHandleRAGDeleteDocument_ServiceError(t *testing.T) {
	sentinel := errors.New("delete failed")
	svc := &mockRAGServiceQuic{
		deleteFn: func(_ context.Context, _ *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
			return nil, sentinel
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(svc))
	payload, _ := proto.Marshal(&pb.RAGDeleteDocumentRequest{DocumentId: "d"})
	if _, err := srv.handleRAGDeleteDocument(context.Background(), payload); !errors.Is(err, sentinel) {
		t.Fatalf("expected the service error, got %v", err)
	}
}

func TestHandleRAGListDocuments_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.RAGListDocumentsRequest{Namespace: "ns"})
	_, err := srv.handleRAGListDocuments(context.Background(), payload)
	assertNotConfigured(t, err)
}

func TestHandleRAGListDocuments_Success(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(&mockRAGServiceQuic{}))
	payload, _ := proto.Marshal(&pb.RAGListDocumentsRequest{Namespace: "ns1", Limit: 10})
	resp, err := srv.handleRAGListDocuments(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleRAGListDocuments failed: %v", err)
	}
	var out pb.RAGListDocumentsResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GetDocuments()) != 1 || out.GetTotal() != 1 {
		t.Fatalf("documents = %v total = %d", out.GetDocuments(), out.GetTotal())
	}
	if d := out.GetDocuments()[0]; d.GetId() != "doc1" || d.GetNamespace() != "ns1" || d.GetChunkCount() != 3 {
		t.Fatalf("document = %+v", d)
	}
}

func TestHandleRAGListDocuments_ServiceError(t *testing.T) {
	sentinel := errors.New("list failed")
	svc := &mockRAGServiceQuic{
		listFn: func(_ context.Context, _ *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
			return nil, sentinel
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(svc))
	payload, _ := proto.Marshal(&pb.RAGListDocumentsRequest{Namespace: "ns"})
	if _, err := srv.handleRAGListDocuments(context.Background(), payload); !errors.Is(err, sentinel) {
		t.Fatalf("expected the service error, got %v", err)
	}
}
