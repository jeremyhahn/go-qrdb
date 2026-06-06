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

package grpc

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// protoToChunkConfig converts a proto ChunkConfig to its service form, or nil
// when the request omits it.
func protoToChunkConfig(c *pb.ChunkConfig) *service.ChunkConfig {
	if c == nil {
		return nil
	}
	return &service.ChunkConfig{
		Strategy:  c.GetStrategy(),
		ChunkSize: int(c.GetChunkSize()),
		Overlap:   int(c.GetOverlap()),
		MinSize:   int(c.GetMinSize()),
	}
}

// RAGIngestDocument chunks, embeds, and stores a document.
func (s *qrdbServiceServer) RAGIngestDocument(ctx context.Context, req *pb.RAGIngestDocumentRequest) (*pb.RAGIngestDocumentResponse, error) {
	if s.ragSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "rag"}).Error())
	}
	resp, err := s.ragSvc.IngestDocument(ctx, &service.IngestDocumentRequest{
		DocumentID:  req.GetDocumentId(),
		Text:        req.GetText(),
		Namespace:   req.GetNamespace(),
		Model:       req.GetModel(),
		Provider:    req.GetProvider(),
		ChunkConfig: protoToChunkConfig(req.GetChunkConfig()),
		Metadata:    req.GetMetadata(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.RAGIngestDocumentResponse{
		DocumentId: resp.DocumentID,
		ChunkCount: int32(resp.ChunkCount),
		Namespace:  resp.Namespace,
	}, nil
}

// RAGQuery retrieves the most semantically similar passages from a namespace.
func (s *qrdbServiceServer) RAGQuery(ctx context.Context, req *pb.RAGQueryRequest) (*pb.RAGQueryResponse, error) {
	if s.ragSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "rag"}).Error())
	}
	resp, err := s.ragSvc.Query(ctx, &service.RAGQueryRequest{
		Query:     req.GetQuery(),
		Namespace: req.GetNamespace(),
		TopK:      int(req.GetTopK()),
		Model:     req.GetModel(),
		Provider:  req.GetProvider(),
		Filter:    req.GetFilter(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	results := make([]*pb.RAGQueryResult, len(resp.Results))
	for i, r := range resp.Results {
		results[i] = &pb.RAGQueryResult{
			ChunkText:  r.ChunkText,
			Score:      r.Score,
			DocumentId: r.DocumentID,
			ChunkIndex: int32(r.ChunkIndex),
			Metadata:   r.Metadata,
		}
	}
	return &pb.RAGQueryResponse{Results: results}, nil
}

// RAGDeleteDocument removes a document and all of its chunk vectors and KV entries.
func (s *qrdbServiceServer) RAGDeleteDocument(ctx context.Context, req *pb.RAGDeleteDocumentRequest) (*pb.RAGDeleteDocumentResponse, error) {
	if s.ragSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "rag"}).Error())
	}
	resp, err := s.ragSvc.DeleteDocument(ctx, &service.DeleteRAGDocumentRequest{
		DocumentID: req.GetDocumentId(),
		Namespace:  req.GetNamespace(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.RAGDeleteDocumentResponse{
		DocumentId:    resp.DocumentID,
		ChunksRemoved: int32(resp.ChunksRemoved),
	}, nil
}

// RAGListDocuments returns metadata about ingested documents in a namespace.
func (s *qrdbServiceServer) RAGListDocuments(ctx context.Context, req *pb.RAGListDocumentsRequest) (*pb.RAGListDocumentsResponse, error) {
	if s.ragSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "rag"}).Error())
	}
	resp, err := s.ragSvc.ListDocuments(ctx, &service.ListRAGDocumentsRequest{
		Namespace: req.GetNamespace(),
		Limit:     int(req.GetLimit()),
		Offset:    int(req.GetOffset()),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	docs := make([]*pb.RAGDocumentInfo, len(resp.Documents))
	for i, d := range resp.Documents {
		docs[i] = &pb.RAGDocumentInfo{
			Id:         d.ID,
			Namespace:  d.Namespace,
			ChunkCount: int32(d.ChunkCount),
			Metadata:   d.Metadata,
		}
	}
	return &pb.RAGListDocumentsResponse{Documents: docs, Total: int32(resp.Total)}, nil
}
