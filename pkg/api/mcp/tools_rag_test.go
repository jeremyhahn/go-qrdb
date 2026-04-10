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

package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock RAGServicer
// ---------------------------------------------------------------------------

type mockRAGSvc struct {
	ingestFn func(context.Context, *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error)
	queryFn  func(context.Context, *service.RAGQueryRequest) (*service.RAGQueryResponse, error)
	deleteFn func(context.Context, *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error)
	listFn   func(context.Context, *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error)
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
		Results: []*service.RAGQueryResult{
			{ChunkText: "passage", Score: 0.1, DocumentID: "doc1", ChunkIndex: 0},
		},
	}, nil
}

func (m *mockRAGSvc) DeleteDocument(ctx context.Context, req *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, req)
	}
	return &service.DeleteRAGDocumentResponse{DocumentID: req.DocumentID, ChunksRemoved: 2}, nil
}

func (m *mockRAGSvc) ListDocuments(ctx context.Context, req *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
	if m.listFn != nil {
		return m.listFn(ctx, req)
	}
	return &service.ListRAGDocumentsResponse{
		Documents: []*service.RAGDocumentInfo{
			{ID: "doc1", Namespace: req.Namespace, ChunkCount: 3},
		},
		Total: 1,
	}, nil
}

func (m *mockRAGSvc) Close() error { return nil }

// ---------------------------------------------------------------------------
// Helper to build a CallToolRequest with string arguments.
// ---------------------------------------------------------------------------

func newRAGCallRequest(args map[string]interface{}) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: args,
		},
	}
}

// ---------------------------------------------------------------------------
// Tests: handleRAGIngest
// ---------------------------------------------------------------------------

func TestHandleRAGIngest_Success(t *testing.T) {
	svc := &mockRAGSvc{}
	handler := handleRAGIngest(svc)

	req := newRAGCallRequest(map[string]interface{}{
		"document_id": "doc1",
		"text":        "The quick brown fox.",
		"namespace":   "ns",
	})
	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "Ingested document doc1")
}

func TestHandleRAGIngest_ServiceError(t *testing.T) {
	svc := &mockRAGSvc{
		ingestFn: func(_ context.Context, _ *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
			return nil, errors.New("embed down")
		},
	}
	handler := handleRAGIngest(svc)
	req := newRAGCallRequest(map[string]interface{}{
		"document_id": "doc1",
		"text":        "text",
		"namespace":   "ns",
	})
	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

// ---------------------------------------------------------------------------
// Tests: handleRAGQuery
// ---------------------------------------------------------------------------

func TestHandleRAGQuery_Success(t *testing.T) {
	svc := &mockRAGSvc{}
	handler := handleRAGQuery(svc)

	req := newRAGCallRequest(map[string]interface{}{
		"query":     "brown fox",
		"namespace": "ns",
		"top_k":     float64(3),
	})
	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "passage")
}

func TestHandleRAGQuery_ServiceError(t *testing.T) {
	svc := &mockRAGSvc{
		queryFn: func(_ context.Context, _ *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
			return nil, errors.New("retrieval failed")
		},
	}
	handler := handleRAGQuery(svc)
	req := newRAGCallRequest(map[string]interface{}{
		"query":     "q",
		"namespace": "ns",
	})
	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

// ---------------------------------------------------------------------------
// Tests: handleRAGDeleteDocument
// ---------------------------------------------------------------------------

func TestHandleRAGDeleteDocument_Success(t *testing.T) {
	svc := &mockRAGSvc{}
	handler := handleRAGDeleteDocument(svc)

	req := newRAGCallRequest(map[string]interface{}{
		"document_id": "doc1",
		"namespace":   "ns",
	})
	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "Deleted document doc1")
}

func TestHandleRAGDeleteDocument_ServiceError(t *testing.T) {
	svc := &mockRAGSvc{
		deleteFn: func(_ context.Context, _ *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
			return nil, &service.RAGDocumentNotFoundError{ID: "doc1", Namespace: "ns"}
		},
	}
	handler := handleRAGDeleteDocument(svc)
	req := newRAGCallRequest(map[string]interface{}{
		"document_id": "doc1",
		"namespace":   "ns",
	})
	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

// ---------------------------------------------------------------------------
// Tests: handleRAGListDocuments
// ---------------------------------------------------------------------------

func TestHandleRAGListDocuments_Success(t *testing.T) {
	svc := &mockRAGSvc{}
	handler := handleRAGListDocuments(svc)

	req := newRAGCallRequest(map[string]interface{}{
		"namespace": "ns",
		"limit":     float64(10),
	})
	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "doc1")
}

func TestHandleRAGListDocuments_ServiceError(t *testing.T) {
	svc := &mockRAGSvc{
		listFn: func(_ context.Context, _ *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
			return nil, errors.New("scan error")
		},
	}
	handler := handleRAGListDocuments(svc)
	req := newRAGCallRequest(map[string]interface{}{"namespace": "ns"})
	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}
