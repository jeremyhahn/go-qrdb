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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.RAGClient = (*Transport)(nil)

// RAGIngestDocument chunks, embeds, and stores a document via the injected service.
func (t *Transport) RAGIngestDocument(ctx context.Context, req *transport.RAGIngestRequest) (*transport.RAGIngestResponse, error) {
	if t.ragSvc == nil {
		return nil, &NilServiceError{Service: "RAGServicer"}
	}
	resp, err := t.ragSvc.IngestDocument(ctx, &service.IngestDocumentRequest{
		DocumentID:  req.DocumentID,
		Text:        req.Text,
		Namespace:   req.Namespace,
		Model:       req.Model,
		Provider:    req.Provider,
		Metadata:    req.Metadata,
		ChunkConfig: ragChunkConfig(req),
	})
	if err != nil {
		return nil, err
	}
	return &transport.RAGIngestResponse{
		DocumentID: resp.DocumentID,
		ChunkCount: resp.ChunkCount,
		Namespace:  resp.Namespace,
	}, nil
}

// Query retrieves semantically similar passages via the injected service.
func (t *Transport) RAGQuery(ctx context.Context, req *transport.RAGQueryRequest) (*transport.RAGQueryResponse, error) {
	if t.ragSvc == nil {
		return nil, &NilServiceError{Service: "RAGServicer"}
	}
	resp, err := t.ragSvc.Query(ctx, &service.RAGQueryRequest{
		Query:     req.Query,
		Namespace: req.Namespace,
		TopK:      req.TopK,
		Model:     req.Model,
		Provider:  req.Provider,
		Filter:    req.Filter,
	})
	if err != nil {
		return nil, err
	}
	results := make([]*transport.RAGQueryResult, len(resp.Results))
	for i, r := range resp.Results {
		results[i] = &transport.RAGQueryResult{
			ChunkText:  r.ChunkText,
			Score:      r.Score,
			DocumentID: r.DocumentID,
			ChunkIndex: r.ChunkIndex,
			Metadata:   r.Metadata,
		}
	}
	return &transport.RAGQueryResponse{Results: results}, nil
}

// DeleteDocument removes a document via the injected service.
func (t *Transport) RAGDeleteDocument(ctx context.Context, req *transport.RAGDeleteDocumentRequest) (*transport.RAGDeleteDocumentResponse, error) {
	if t.ragSvc == nil {
		return nil, &NilServiceError{Service: "RAGServicer"}
	}
	resp, err := t.ragSvc.DeleteDocument(ctx, &service.DeleteRAGDocumentRequest{
		DocumentID: req.DocumentID,
		Namespace:  req.Namespace,
	})
	if err != nil {
		return nil, err
	}
	return &transport.RAGDeleteDocumentResponse{
		DocumentID:    resp.DocumentID,
		ChunksRemoved: resp.ChunksRemoved,
	}, nil
}

// ListDocuments returns ingested-document metadata via the injected service.
func (t *Transport) RAGListDocuments(ctx context.Context, req *transport.RAGListDocumentsRequest) (*transport.RAGListDocumentsResponse, error) {
	if t.ragSvc == nil {
		return nil, &NilServiceError{Service: "RAGServicer"}
	}
	resp, err := t.ragSvc.ListDocuments(ctx, &service.ListRAGDocumentsRequest{
		Namespace: req.Namespace,
		Limit:     req.Limit,
		Offset:    req.Offset,
	})
	if err != nil {
		return nil, err
	}
	docs := make([]*transport.RAGDocumentInfo, len(resp.Documents))
	for i, d := range resp.Documents {
		docs[i] = &transport.RAGDocumentInfo{
			ID:         d.ID,
			Namespace:  d.Namespace,
			ChunkCount: d.ChunkCount,
			Metadata:   d.Metadata,
		}
	}
	return &transport.RAGListDocumentsResponse{Documents: docs, Total: resp.Total}, nil
}

// ragChunkConfig builds a service.ChunkConfig from the flat request fields,
// returning nil when no chunk overrides are supplied.
func ragChunkConfig(req *transport.RAGIngestRequest) *service.ChunkConfig {
	if req.ChunkStrategy == "" && req.ChunkSize == 0 && req.ChunkOverlap == 0 && req.ChunkMinSize == 0 {
		return nil
	}
	return &service.ChunkConfig{
		Strategy:  req.ChunkStrategy,
		ChunkSize: req.ChunkSize,
		Overlap:   req.ChunkOverlap,
		MinSize:   req.ChunkMinSize,
	}
}
