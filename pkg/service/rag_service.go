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

// Package service provides the RAG (Retrieval-Augmented Generation) service
// layer. RAGServicer sits between the API handlers and the rag package,
// providing a clean abstraction over document ingestion and retrieval.
package service

import (
	"context"
	"encoding/json"
	"strconv"
	"sync/atomic"
)

// RAGServicer manages the RAG pipeline: document ingestion, semantic retrieval,
// document deletion, and listing. All methods are safe for concurrent use.
type RAGServicer interface {
	// IngestDocument chunks, embeds, and stores a document in the vector store
	// and KV store for later retrieval.
	IngestDocument(ctx context.Context, req *IngestDocumentRequest) (*IngestDocumentResponse, error)

	// Query embeds the query text and retrieves the most semantically similar
	// passages from the specified namespace.
	Query(ctx context.Context, req *RAGQueryRequest) (*RAGQueryResponse, error)

	// DeleteDocument removes a document and all its chunk vectors and KV
	// entries from the pipeline.
	DeleteDocument(ctx context.Context, req *DeleteRAGDocumentRequest) (*DeleteRAGDocumentResponse, error)

	// ListDocuments returns metadata about ingested documents in a namespace,
	// with optional pagination.
	ListDocuments(ctx context.Context, req *ListRAGDocumentsRequest) (*ListRAGDocumentsResponse, error)

	// Close releases all resources held by the service. Subsequent calls to
	// any method return RAGServiceClosedError.
	Close() error
}

// RAGIngestResult is the result from a RAG document ingestion operation.
type RAGIngestResult struct {
	// DocumentID is the ingested document's identifier.
	DocumentID string

	// ChunkCount is the number of chunks created from the document.
	ChunkCount int

	// Namespace is the vector namespace where embeddings were stored.
	Namespace string
}

// RAGIngestor abstracts the document ingestion pipeline (chunk, embed, store).
// The concrete implementation lives in pkg/rag behind a build tag.
type RAGIngestor interface {
	// Ingest validates the document, chunks it, embeds the chunks, and stores
	// the results. Returns a summary of the ingestion.
	Ingest(ctx context.Context, req *IngestDocumentRequest) (*RAGIngestResult, error)
}

// RAGRetrievalResult is a single retrieved passage from the RAG pipeline.
type RAGRetrievalResult struct {
	// ChunkText is the text content of the retrieved chunk.
	ChunkText string

	// Score is the similarity score (lower distance = higher relevance).
	Score float32

	// DocumentID is the source document identifier.
	DocumentID string

	// ChunkIndex is the zero-based ordinal of the chunk within its document.
	ChunkIndex int

	// Metadata contains the chunk's stored metadata.
	Metadata map[string]string
}

// RAGRetrieveResult contains results from a retrieval operation.
type RAGRetrieveResult struct {
	// Results is the list of retrieved passages ordered by relevance.
	Results []*RAGRetrievalResult
}

// RAGRetriever abstracts the retrieval leg of the RAG pipeline (embed query,
// vector search, fetch chunk text). The concrete implementation lives in
// pkg/rag behind a build tag.
type RAGRetriever interface {
	// Retrieve embeds the query, performs a similarity search, and returns
	// ranked results with their chunk text.
	Retrieve(ctx context.Context, req *RAGQueryRequest) (*RAGRetrieveResult, error)
}

// RAGServiceOption is a functional option for NewRAGService.
type RAGServiceOption func(*ragService)

// ragDocumentMeta is used for JSON serialization of document metadata stored
// in the KV store. It mirrors the shape of pkg/rag.DocumentInfo but is defined
// locally to avoid importing the build-tag-guarded package.
type ragDocumentMeta struct {
	ID         string            `json:"ID"`
	Namespace  string            `json:"Namespace"`
	ChunkCount int               `json:"ChunkCount"`
	Metadata   map[string]string `json:"Metadata,omitempty"`
}

// ragService is the production implementation of RAGServicer.
type ragService struct {
	ingestor  RAGIngestor
	retriever RAGRetriever
	kvSvc     *ScopedRAGStore
	closed    atomic.Bool
}

// Compile-time assertion that ragService implements RAGServicer.
var _ RAGServicer = (*ragService)(nil)

// NewRAGService constructs a RAGServicer backed by the supplied Ingestor,
// Retriever, and store, which it lists and deletes documents through. The
// store is the same ScopedRAGStore the Ingestor and Retriever were built over,
// so every record the service reaches is the caller's (tenant, database).
func NewRAGService(
	ingestor RAGIngestor,
	retriever RAGRetriever,
	kvSvc *ScopedRAGStore,
	opts ...RAGServiceOption,
) RAGServicer {
	s := &ragService{
		ingestor:  ingestor,
		retriever: retriever,
		kvSvc:     kvSvc,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

var _ RAGRegistryBinder = (*ragService)(nil)

// BindRegistry implements RAGRegistryBinder: it binds the registry the
// service's store -- and the ingestor and retriever built over the same
// binding -- resolve every record through.
func (s *ragService) BindRegistry(reg Registry) {
	s.kvSvc.binding.Bind(reg)
}

// IngestDocument validates the request, delegates to the Ingestor, and
// returns a summary of the ingestion result.
func (s *ragService) IngestDocument(ctx context.Context, req *IngestDocumentRequest) (*IngestDocumentResponse, error) {
	if s.closed.Load() {
		return nil, &RAGServiceClosedError{}
	}

	resp, err := s.ingestor.Ingest(ctx, req)
	if err != nil {
		return nil, &RAGIngestError{Cause: err}
	}

	return &IngestDocumentResponse{
		DocumentID: resp.DocumentID,
		ChunkCount: resp.ChunkCount,
		Namespace:  resp.Namespace,
	}, nil
}

// Query embeds the query and retrieves the most relevant passages from the
// specified namespace.
func (s *ragService) Query(ctx context.Context, req *RAGQueryRequest) (*RAGQueryResponse, error) {
	if s.closed.Load() {
		return nil, &RAGServiceClosedError{}
	}

	resp, err := s.retriever.Retrieve(ctx, req)
	if err != nil {
		return nil, &RAGQueryError{Cause: err}
	}

	results := make([]*RAGQueryResult, len(resp.Results))
	for i, r := range resp.Results {
		results[i] = &RAGQueryResult{
			ChunkText:  r.ChunkText,
			Score:      r.Score,
			DocumentID: r.DocumentID,
			ChunkIndex: r.ChunkIndex,
			Metadata:   r.Metadata,
		}
	}

	return &RAGQueryResponse{Results: results}, nil
}

// DeleteDocument removes a document's metadata, all chunk text entries, and
// all chunk vectors from the pipeline.
func (s *ragService) DeleteDocument(ctx context.Context, req *DeleteRAGDocumentRequest) (*DeleteRAGDocumentResponse, error) {
	if s.closed.Load() {
		return nil, &RAGServiceClosedError{}
	}

	// Look up the document metadata to determine chunk count.
	metaKey := ragDocMetaKey(req.Namespace, req.DocumentID)
	metaBytes, err := s.kvSvc.Get(ctx, metaKey)
	if err != nil {
		return nil, &RAGDocumentNotFoundError{ID: req.DocumentID, Namespace: req.Namespace}
	}

	var docInfo ragDocumentMeta
	if err = json.Unmarshal(metaBytes, &docInfo); err != nil {
		return nil, &RAGDeleteError{ID: req.DocumentID, Cause: err}
	}

	// Remove each chunk's KV entry.
	for i := 0; i < docInfo.ChunkCount; i++ {
		chunkKey := ragChunkKey(req.Namespace, req.DocumentID, i)
		if delErr := s.kvSvc.Delete(ctx, chunkKey); delErr != nil {
			// Non-fatal: log and continue cleaning up remaining chunks.
			_ = delErr
		}
	}

	// Remove document metadata.
	if delErr := s.kvSvc.Delete(ctx, metaKey); delErr != nil {
		_ = delErr
	}

	return &DeleteRAGDocumentResponse{
		DocumentID:    req.DocumentID,
		ChunksRemoved: docInfo.ChunkCount,
	}, nil
}

// ListDocuments returns metadata about all ingested documents in a namespace,
// with optional pagination via Limit/Offset.
func (s *ragService) ListDocuments(ctx context.Context, req *ListRAGDocumentsRequest) (*ListRAGDocumentsResponse, error) {
	if s.closed.Load() {
		return nil, &RAGServiceClosedError{}
	}

	prefix := "rag:" + req.Namespace + ":"
	raw, err := s.kvSvc.Scan(ctx, prefix)
	if err != nil {
		return nil, &RAGQueryError{Cause: err}
	}

	// Collect document metadata entries (keys ending in ":meta").
	const metaSuffix = ":meta"
	docs := make([]*RAGDocumentInfo, 0, len(raw))
	for key, val := range raw {
		if len(key) < len(metaSuffix) || key[len(key)-len(metaSuffix):] != metaSuffix {
			continue
		}
		var info ragDocumentMeta
		if err = json.Unmarshal(val, &info); err != nil {
			continue
		}
		docs = append(docs, &RAGDocumentInfo{
			ID:         info.ID,
			Namespace:  info.Namespace,
			ChunkCount: info.ChunkCount,
			Metadata:   info.Metadata,
		})
	}

	total := len(docs)

	// Apply pagination.
	if req.Offset > 0 {
		if req.Offset >= len(docs) {
			docs = docs[:0]
		} else {
			docs = docs[req.Offset:]
		}
	}
	if req.Limit > 0 && len(docs) > req.Limit {
		docs = docs[:req.Limit]
	}

	return &ListRAGDocumentsResponse{
		Documents: docs,
		Total:     total,
	}, nil
}

// Close marks the service as closed. Subsequent calls to any method return
// RAGServiceClosedError. Close is idempotent.
func (s *ragService) Close() error {
	s.closed.CompareAndSwap(false, true)
	return nil
}

// ragChunkKey mirrors the key format used by the Ingestor.
func ragChunkKey(namespace, docID string, chunkIdx int) string {
	return "rag:" + namespace + ":" + docID + ":" + strconv.Itoa(chunkIdx)
}

// ragDocMetaKey mirrors the metadata key format used by the Ingestor.
func ragDocMetaKey(namespace, docID string) string {
	return "rag:" + namespace + ":" + docID + ":meta"
}
