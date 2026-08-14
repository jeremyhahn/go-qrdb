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

//go:build qrdb_vector && !integration

// rag_coverage_test.go covers the truncate helper and the RAG CLI subcommands.
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// truncate helper
// ---------------------------------------------------------------------------

func TestTruncate_ShortString(t *testing.T) {
	got := truncate("hello", 10)
	if got != "hello" {
		t.Errorf("truncate(%q, 10) = %q, want %q", "hello", got, "hello")
	}
}

func TestTruncate_ExactLength(t *testing.T) {
	got := truncate("hello", 5)
	if got != "hello" {
		t.Errorf("truncate(%q, 5) = %q, want %q", "hello", got, "hello")
	}
}

func TestTruncate_LongString(t *testing.T) {
	got := truncate("hello world long string", 5)
	if !strings.HasPrefix(got, "hello") {
		t.Errorf("truncate(%q, 5) = %q, expected prefix 'hello'", "hello world long string", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("truncate(%q, 5) = %q, expected ellipsis", "hello world long string", got)
	}
}

func TestTruncate_MultibyteRunes(t *testing.T) {
	got := truncate("日本語テスト", 3)
	if !strings.Contains(got, "日本語") {
		t.Errorf("truncate runes: got %q", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("truncate runes: missing ellipsis, got %q", got)
	}
}

func TestTruncate_EmptyString(t *testing.T) {
	got := truncate("", 5)
	if got != "" {
		t.Errorf("truncate(%q, 5) = %q, want empty", "", got)
	}
}

// ---------------------------------------------------------------------------
// RAG command tests
// ---------------------------------------------------------------------------

// mockRAGSvc is a minimal service.RAGServicer for testing RAG CLI commands.
type mockRAGSvc struct {
	queryFn  func(ctx context.Context, req *service.RAGQueryRequest) (*service.RAGQueryResponse, error)
	deleteFn func(ctx context.Context, req *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error)
	listFn   func(ctx context.Context, req *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error)
}

func (m *mockRAGSvc) IngestDocument(_ context.Context, _ *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
	return &service.IngestDocumentResponse{}, nil
}

func (m *mockRAGSvc) Query(ctx context.Context, req *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, req)
	}
	return &service.RAGQueryResponse{}, nil
}

func (m *mockRAGSvc) DeleteDocument(ctx context.Context, req *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, req)
	}
	return &service.DeleteRAGDocumentResponse{}, nil
}

func (m *mockRAGSvc) ListDocuments(ctx context.Context, req *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
	if m.listFn != nil {
		return m.listFn(ctx, req)
	}
	return &service.ListRAGDocumentsResponse{}, nil
}

func (m *mockRAGSvc) Close() error { return nil }

var _ service.RAGServicer = (*mockRAGSvc)(nil)

// setupRAGClient creates an embedded transport with the given RAG service,
// sets the package-level client, and registers cleanup.
func setupRAGClient(t *testing.T, ragSvc service.RAGServicer) {
	t.Helper()
	tr, err := embedded.New(newMockKVService(), newMockAdminService(),
		embedded.WithRAGService(ragSvc),
	)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	if err := sdkClient.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	oldClient := client
	client = sdkClient
	t.Cleanup(func() {
		_ = sdkClient.Close()
		client = oldClient
	})
}

// ---------------------------------------------------------------------------
// rag ingest
// ---------------------------------------------------------------------------

func TestRAGIngestCmd_Success(t *testing.T) {
	svc := &mockRAGSvc{}
	setupRAGClient(t, svc)

	cmd := newRAGIngestCmd()
	out, err := executeCommand(cmd,
		"--id", "doc1",
		"--text", "Hello world",
		"--namespace", "ns1",
	)
	if err != nil {
		t.Fatalf("rag ingest: %v", err)
	}
	_ = out
}

func TestRAGIngestCmd_Error(t *testing.T) {
	svc := &mockRAGSvc{}
	// Override ingest to fail.
	var ingestCalled bool
	failSvc := &mockRAGSvcWithIngest{
		mockRAGSvc: svc,
		ingestFn: func(_ context.Context, _ *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
			ingestCalled = true
			return nil, boostRAGError("ingest failed")
		},
	}
	setupRAGClient(t, failSvc)

	cmd := newRAGIngestCmd()
	_, err := executeCommand(cmd,
		"--id", "doc1",
		"--text", "Hello world",
		"--namespace", "ns1",
	)
	if err == nil && ingestCalled {
		t.Fatal("expected error from rag ingest when service fails")
	}
}

// mockRAGSvcWithIngest overrides IngestDocument on mockRAGSvc.
type mockRAGSvcWithIngest struct {
	*mockRAGSvc
	ingestFn func(ctx context.Context, req *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error)
}

func (m *mockRAGSvcWithIngest) IngestDocument(ctx context.Context, req *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
	if m.ingestFn != nil {
		return m.ingestFn(ctx, req)
	}
	return &service.IngestDocumentResponse{}, nil
}

func (m *mockRAGSvcWithIngest) Query(ctx context.Context, req *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
	return m.mockRAGSvc.Query(ctx, req)
}

func (m *mockRAGSvcWithIngest) DeleteDocument(ctx context.Context, req *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
	return m.mockRAGSvc.DeleteDocument(ctx, req)
}

func (m *mockRAGSvcWithIngest) ListDocuments(ctx context.Context, req *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
	return m.mockRAGSvc.ListDocuments(ctx, req)
}

func (m *mockRAGSvcWithIngest) Close() error { return nil }

var _ service.RAGServicer = (*mockRAGSvcWithIngest)(nil)

// ---------------------------------------------------------------------------
// rag query
// ---------------------------------------------------------------------------

func TestRAGQueryCmd_Success(t *testing.T) {
	svc := &mockRAGSvc{
		queryFn: func(_ context.Context, _ *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
			return &service.RAGQueryResponse{
				Results: []*service.RAGQueryResult{
					{DocumentID: "doc1", ChunkIndex: 0, Score: 0.95, ChunkText: "relevant text here"},
				},
			}, nil
		},
	}
	setupRAGClient(t, svc)

	cmd := newRAGQueryCmd()
	out, err := executeCommand(cmd, "--namespace", "ns1", "my query")
	if err != nil {
		t.Fatalf("rag query: %v", err)
	}
	if !strings.Contains(out, "doc1") {
		t.Errorf("expected doc1 in output, got: %q", out)
	}
}

func TestRAGQueryCmd_Error(t *testing.T) {
	svc := &mockRAGSvc{
		queryFn: func(_ context.Context, _ *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
			return nil, boostRAGError("query failed")
		},
	}
	setupRAGClient(t, svc)

	cmd := newRAGQueryCmd()
	_, err := executeCommand(cmd, "--namespace", "ns1", "bad query")
	if err == nil {
		t.Fatal("expected error from rag query")
	}
}

// ---------------------------------------------------------------------------
// rag delete
// ---------------------------------------------------------------------------

func TestRAGDeleteCmd_Success(t *testing.T) {
	svc := &mockRAGSvc{
		deleteFn: func(_ context.Context, _ *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
			return &service.DeleteRAGDocumentResponse{ChunksRemoved: 5}, nil
		},
	}
	setupRAGClient(t, svc)

	cmd := newRAGDeleteCmd()
	out, err := executeCommand(cmd, "--id", "doc1", "--namespace", "ns1")
	if err != nil {
		t.Fatalf("rag delete: %v", err)
	}
	if !strings.Contains(out, "5") {
		t.Errorf("expected chunk count 5 in output, got: %q", out)
	}
}

func TestRAGDeleteCmd_Error(t *testing.T) {
	svc := &mockRAGSvc{
		deleteFn: func(_ context.Context, _ *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
			return nil, boostRAGError("delete failed")
		},
	}
	setupRAGClient(t, svc)

	cmd := newRAGDeleteCmd()
	_, err := executeCommand(cmd, "--id", "doc1", "--namespace", "ns1")
	if err == nil {
		t.Fatal("expected error from rag delete")
	}
}

// ---------------------------------------------------------------------------
// rag list
// ---------------------------------------------------------------------------

func TestRAGListCmd_Success(t *testing.T) {
	svc := &mockRAGSvc{
		listFn: func(_ context.Context, _ *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
			return &service.ListRAGDocumentsResponse{
				Documents: []*service.RAGDocumentInfo{
					{ID: "doc-a", Namespace: "ns1"},
					{ID: "doc-b", Namespace: "ns1"},
				},
				Total: 2,
			}, nil
		},
	}
	setupRAGClient(t, svc)

	cmd := newRAGListCmd()
	out, err := executeCommand(cmd, "--namespace", "ns1")
	if err != nil {
		t.Fatalf("rag list: %v", err)
	}
	if !strings.Contains(out, "doc-a") || !strings.Contains(out, "doc-b") {
		t.Errorf("expected doc-a and doc-b in output, got: %q", out)
	}
}

func TestRAGListCmd_Error(t *testing.T) {
	svc := &mockRAGSvc{
		listFn: func(_ context.Context, _ *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
			return nil, boostRAGError("list failed")
		},
	}
	setupRAGClient(t, svc)

	cmd := newRAGListCmd()
	_, err := executeCommand(cmd, "--namespace", "ns1")
	if err == nil {
		t.Fatal("expected error from rag list")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

type boostRAGError string

func (e boostRAGError) Error() string { return string(e) }
