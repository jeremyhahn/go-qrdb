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

package mcp

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.RAGClient = (*Transport)(nil)

// RAGIngestDocument ingests a document via the qrdb_rag_ingest tool.
func (t *Transport) RAGIngestDocument(ctx context.Context, req *transport.RAGIngestRequest) (*transport.RAGIngestResponse, error) {
	args := map[string]any{
		"document_id": req.DocumentID,
		"text":        req.Text,
		"namespace":   req.Namespace,
	}
	if req.Model != "" {
		args["model"] = req.Model
	}
	if req.Provider != "" {
		args["provider"] = req.Provider
	}
	if req.ChunkStrategy != "" {
		args["chunk_strategy"] = req.ChunkStrategy
	}
	if req.ChunkSize != 0 {
		args["chunk_size"] = req.ChunkSize
	}
	if req.ChunkOverlap != 0 {
		args["chunk_overlap"] = req.ChunkOverlap
	}
	if len(req.Metadata) > 0 {
		meta, err := json.Marshal(req.Metadata)
		if err != nil {
			return nil, &transport.RequestError{Method: "RAGIngestDocument", Err: &jsonEncodeError{Err: err}}
		}
		args["metadata"] = string(meta)
	}

	text, err := t.callTool(ctx, "qrdb_rag_ingest", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "RAGIngestDocument", Err: err}
	}
	var wire struct {
		DocumentID string
		ChunkCount int
		Namespace  string
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "RAGIngestDocument", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &transport.RAGIngestResponse{
		DocumentID: wire.DocumentID,
		ChunkCount: wire.ChunkCount,
		Namespace:  wire.Namespace,
	}, nil
}

// RAGQuery retrieves semantically similar passages via the qrdb_rag_query tool.
func (t *Transport) RAGQuery(ctx context.Context, req *transport.RAGQueryRequest) (*transport.RAGQueryResponse, error) {
	args := map[string]any{
		"query":     req.Query,
		"namespace": req.Namespace,
	}
	if req.TopK != 0 {
		args["top_k"] = req.TopK
	}
	if req.Model != "" {
		args["model"] = req.Model
	}
	if req.Provider != "" {
		args["provider"] = req.Provider
	}
	if len(req.Filter) > 0 {
		filter, err := json.Marshal(req.Filter)
		if err != nil {
			return nil, &transport.RequestError{Method: "RAGQuery", Err: &jsonEncodeError{Err: err}}
		}
		args["filter"] = string(filter)
	}

	text, err := t.callTool(ctx, "qrdb_rag_query", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "RAGQuery", Err: err}
	}
	var wire struct {
		Results []struct {
			ChunkText  string
			Score      float32
			DocumentID string
			ChunkIndex int
			Metadata   map[string]string
		}
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "RAGQuery", Err: &jsonDecodeError{Err: jsonErr}}
	}
	results := make([]*transport.RAGQueryResult, len(wire.Results))
	for i := range wire.Results {
		results[i] = &transport.RAGQueryResult{
			ChunkText:  wire.Results[i].ChunkText,
			Score:      wire.Results[i].Score,
			DocumentID: wire.Results[i].DocumentID,
			ChunkIndex: wire.Results[i].ChunkIndex,
			Metadata:   wire.Results[i].Metadata,
		}
	}
	return &transport.RAGQueryResponse{Results: results}, nil
}

// RAGDeleteDocument removes a document via the qrdb_rag_delete_document tool.
func (t *Transport) RAGDeleteDocument(ctx context.Context, req *transport.RAGDeleteDocumentRequest) (*transport.RAGDeleteDocumentResponse, error) {
	text, err := t.callTool(ctx, "qrdb_rag_delete_document", map[string]any{
		"document_id": req.DocumentID,
		"namespace":   req.Namespace,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "RAGDeleteDocument", Err: err}
	}
	var wire struct {
		DocumentID    string
		ChunksRemoved int
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "RAGDeleteDocument", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &transport.RAGDeleteDocumentResponse{
		DocumentID:    wire.DocumentID,
		ChunksRemoved: wire.ChunksRemoved,
	}, nil
}

// RAGListDocuments lists ingested documents via the qrdb_rag_list_documents tool.
func (t *Transport) RAGListDocuments(ctx context.Context, req *transport.RAGListDocumentsRequest) (*transport.RAGListDocumentsResponse, error) {
	args := map[string]any{}
	if req.Namespace != "" {
		args["namespace"] = req.Namespace
	}
	if req.Limit != 0 {
		args["limit"] = req.Limit
	}
	if req.Offset != 0 {
		args["offset"] = req.Offset
	}
	text, err := t.callTool(ctx, "qrdb_rag_list_documents", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "RAGListDocuments", Err: err}
	}
	var wire struct {
		Documents []struct {
			ID         string
			Namespace  string
			ChunkCount int
			Metadata   map[string]string
		}
		Total int
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "RAGListDocuments", Err: &jsonDecodeError{Err: jsonErr}}
	}
	docs := make([]*transport.RAGDocumentInfo, len(wire.Documents))
	for i := range wire.Documents {
		docs[i] = &transport.RAGDocumentInfo{
			ID:         wire.Documents[i].ID,
			Namespace:  wire.Documents[i].Namespace,
			ChunkCount: wire.Documents[i].ChunkCount,
			Metadata:   wire.Documents[i].Metadata,
		}
	}
	return &transport.RAGListDocumentsResponse{Documents: docs, Total: wire.Total}, nil
}
