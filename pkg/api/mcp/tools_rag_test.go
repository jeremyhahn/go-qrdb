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

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/rag"
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

// TestHandleRAGIngest_ChunkArgs proves every chunk argument, chunk_min_size
// included, reaches the service, and that omitted ones leave no config.
func TestHandleRAGIngest_ChunkArgs(t *testing.T) {
	tests := []struct {
		name string
		args map[string]interface{}
		want *service.ChunkConfig
	}{
		{
			name: "all chunk fields",
			args: map[string]interface{}{
				"chunk_strategy": "sentence",
				"chunk_size":     float64(256),
				"chunk_overlap":  float64(16),
				"chunk_min_size": float64(32),
			},
			want: &service.ChunkConfig{Strategy: "sentence", ChunkSize: 256, Overlap: 16, MinSize: 32},
		},
		{
			name: "chunk_min_size alone",
			args: map[string]interface{}{"chunk_min_size": float64(64)},
			want: &service.ChunkConfig{MinSize: 64},
		},
		{
			name: "no chunk fields",
			args: map[string]interface{}{},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *service.IngestDocumentRequest
			svc := &mockRAGSvc{
				ingestFn: func(_ context.Context, req *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
					got = req
					return &service.IngestDocumentResponse{DocumentID: req.DocumentID}, nil
				},
			}
			args := map[string]interface{}{
				"document_id": "doc1",
				"text":        "The quick brown fox.",
				"namespace":   "ns",
			}
			for k, v := range tt.args {
				args[k] = v
			}
			result, err := handleRAGIngest(svc)(context.Background(), newRAGCallRequest(args))
			require.NoError(t, err)
			assert.False(t, result.IsError)
			require.NotNil(t, got)
			assert.Equal(t, tt.want, got.ChunkConfig)
		})
	}
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
	require.Error(t, err, "a service error is returned for errorCodeMiddleware to classify")
	assert.Nil(t, result)
}

// TestHandleRAGIngest_RefusalCodes pins the codes an ingest refusal reaches
// the client with: a document over max_document_size is RESOURCE_EXHAUSTED
// and a chunking failure is INVALID_ARGUMENT, as on every other transport.
func TestHandleRAGIngest_RefusalCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want wire.Code
	}{
		{"too large", &service.RAGIngestError{Cause: &rag.DocumentTooLargeError{Size: 2048, MaxSize: 1024}}, wire.CodeResourceExhausted},
		{"chunking", &service.RAGIngestError{Cause: &rag.ChunkingError{Cause: errors.New("bad chunk size")}}, wire.CodeInvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mockRAGSvc{
				ingestFn: func(context.Context, *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
					return nil, tc.err
				},
			}
			req := newRAGCallRequest(map[string]interface{}{
				"document_id": "doc1",
				"text":        "text",
				"namespace":   "ns",
			})
			coded, err := errorCodeMiddleware()(handleRAGIngest(svc))(context.Background(), req)
			require.NoError(t, err)
			require.NotNil(t, coded)
			require.True(t, coded.IsError)
			payload, ok := wire.DecodeErrorPayload(coded.StructuredContent)
			require.True(t, ok, "the refusal carries a code")
			assert.Equal(t, tc.want, payload.Code)
		})
	}
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
	require.Error(t, err, "a service error is returned for errorCodeMiddleware to classify")
	assert.Nil(t, result)
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
	require.Error(t, err, "a service error is returned for errorCodeMiddleware to classify")
	assert.Nil(t, result)
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
	require.Error(t, err, "a service error is returned for errorCodeMiddleware to classify")
	assert.Nil(t, result)
}
