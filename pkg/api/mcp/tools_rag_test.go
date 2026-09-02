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
	text := result.Content[0].(mcp.TextContent).Text
	assert.Contains(t, text, `"DocumentID":"doc1"`)
	assert.Contains(t, text, `"Namespace":"ns"`)
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
	assert.Contains(t, result.Content[0].(mcp.TextContent).Text, `"DocumentID":"doc1"`)
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
