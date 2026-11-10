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

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mock_rag_service_test.go holds mockRAGSvc, the package's shared
// service.RAGServicer double.
//
// It carries NO build constraint on purpose. The double used to live in
// tools_rag_test.go, which is `!integration`, while tool_mapping_guard_test.go
// consumes it under `codec_json && raft` -- a constraint that does not imply
// `!integration`. Under BUILD_TAGS+integration the type vanished and the package
// stopped typechecking (#203, the shape #178 first hit). A test helper's
// constraint must be a SUPERSET of every caller's; the union of this one's
// callers is unconstrained, so this file is too.

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
