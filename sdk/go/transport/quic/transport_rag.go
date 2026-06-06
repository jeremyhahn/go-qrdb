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

package quic

import (
	"context"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.RAGClient = (*Transport)(nil)

// quicRAGChunkConfig builds a proto ChunkConfig from the flat request fields,
// returning nil when no chunk overrides are supplied.
func quicRAGChunkConfig(req *transport.RAGIngestRequest) *pb.ChunkConfig {
	if req.ChunkStrategy == "" && req.ChunkSize == 0 && req.ChunkOverlap == 0 && req.ChunkMinSize == 0 {
		return nil
	}
	return &pb.ChunkConfig{
		Strategy:  req.ChunkStrategy,
		ChunkSize: int32(req.ChunkSize),
		Overlap:   int32(req.ChunkOverlap),
		MinSize:   int32(req.ChunkMinSize),
	}
}

// RAGIngestDocument ingests a document via the QUIC rag.IngestDocument handler.
func (t *Transport) RAGIngestDocument(ctx context.Context, req *transport.RAGIngestRequest) (*transport.RAGIngestResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.RAGIngestDocumentRequest{
		DocumentId:  req.DocumentID,
		Text:        req.Text,
		Namespace:   req.Namespace,
		Model:       req.Model,
		Provider:    req.Provider,
		ChunkConfig: quicRAGChunkConfig(req),
		Metadata:    req.Metadata,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "RAGIngestDocument", Err: err}
	}
	respBytes, err := t.call(ctx, "rag.IngestDocument", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "RAGIngestDocument", Err: err}
	}
	var resp pb.RAGIngestDocumentResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "RAGIngestDocument", Err: err}
	}
	return &transport.RAGIngestResponse{
		DocumentID: resp.GetDocumentId(),
		ChunkCount: int(resp.GetChunkCount()),
		Namespace:  resp.GetNamespace(),
	}, nil
}

// RAGQuery retrieves semantically similar passages via the QUIC rag.Query handler.
func (t *Transport) RAGQuery(ctx context.Context, req *transport.RAGQueryRequest) (*transport.RAGQueryResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.RAGQueryRequest{
		Query:     req.Query,
		Namespace: req.Namespace,
		TopK:      int32(req.TopK),
		Model:     req.Model,
		Provider:  req.Provider,
		Filter:    req.Filter,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "RAGQuery", Err: err}
	}
	respBytes, err := t.call(ctx, "rag.Query", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "RAGQuery", Err: err}
	}
	var resp pb.RAGQueryResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "RAGQuery", Err: err}
	}
	results := make([]*transport.RAGQueryResult, len(resp.GetResults()))
	for i, r := range resp.GetResults() {
		results[i] = &transport.RAGQueryResult{
			ChunkText:  r.GetChunkText(),
			Score:      r.GetScore(),
			DocumentID: r.GetDocumentId(),
			ChunkIndex: int(r.GetChunkIndex()),
			Metadata:   r.GetMetadata(),
		}
	}
	return &transport.RAGQueryResponse{Results: results}, nil
}

// RAGDeleteDocument removes a document via the QUIC rag.DeleteDocument handler.
func (t *Transport) RAGDeleteDocument(ctx context.Context, req *transport.RAGDeleteDocumentRequest) (*transport.RAGDeleteDocumentResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.RAGDeleteDocumentRequest{
		DocumentId: req.DocumentID,
		Namespace:  req.Namespace,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "RAGDeleteDocument", Err: err}
	}
	respBytes, err := t.call(ctx, "rag.DeleteDocument", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "RAGDeleteDocument", Err: err}
	}
	var resp pb.RAGDeleteDocumentResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "RAGDeleteDocument", Err: err}
	}
	return &transport.RAGDeleteDocumentResponse{
		DocumentID:    resp.GetDocumentId(),
		ChunksRemoved: int(resp.GetChunksRemoved()),
	}, nil
}

// RAGListDocuments lists ingested documents via the QUIC rag.ListDocuments handler.
func (t *Transport) RAGListDocuments(ctx context.Context, req *transport.RAGListDocumentsRequest) (*transport.RAGListDocumentsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.RAGListDocumentsRequest{
		Namespace: req.Namespace,
		Limit:     int32(req.Limit),
		Offset:    int32(req.Offset),
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "RAGListDocuments", Err: err}
	}
	respBytes, err := t.call(ctx, "rag.ListDocuments", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "RAGListDocuments", Err: err}
	}
	var resp pb.RAGListDocumentsResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "RAGListDocuments", Err: err}
	}
	docs := make([]*transport.RAGDocumentInfo, len(resp.GetDocuments()))
	for i, d := range resp.GetDocuments() {
		docs[i] = &transport.RAGDocumentInfo{
			ID:         d.GetId(),
			Namespace:  d.GetNamespace(),
			ChunkCount: int(d.GetChunkCount()),
			Metadata:   d.GetMetadata(),
		}
	}
	return &transport.RAGListDocumentsResponse{Documents: docs, Total: int(resp.GetTotal())}, nil
}
