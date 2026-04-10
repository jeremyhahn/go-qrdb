//go:build qrdb_vector

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

// Package transport provides transport-layer request and response types for
// the QRDB SDK. RAG types are used by the REST and gRPC transport
// implementations to communicate with the server's RAG pipeline.
package transport

// RAGIngestRequest is the transport-layer request to ingest a document into
// the RAG pipeline.
type RAGIngestRequest struct {
	// DocumentID is the unique identifier for the document.
	DocumentID string `json:"document_id"`

	// Text is the full document content.
	Text string `json:"text"`

	// Namespace is the vector namespace for the embeddings.
	Namespace string `json:"namespace"`

	// Model is the embedding model name. Empty uses the provider default.
	Model string `json:"model,omitempty"`

	// Provider is the registered embedding provider name.
	Provider string `json:"provider,omitempty"`

	// Metadata holds optional key-value attributes for the document.
	Metadata map[string]string `json:"metadata,omitempty"`

	// ChunkStrategy selects the chunking algorithm: fixed, sentence, paragraph.
	ChunkStrategy string `json:"chunk_strategy,omitempty"`

	// ChunkSize is the target chunk size in characters.
	ChunkSize int `json:"chunk_size,omitempty"`

	// ChunkOverlap is the number of overlapping characters between chunks.
	ChunkOverlap int `json:"chunk_overlap,omitempty"`

	// ChunkMinSize is the minimum chunk size; smaller chunks are merged.
	ChunkMinSize int `json:"chunk_min_size,omitempty"`
}

// RAGIngestResponse is the transport-layer response from document ingestion.
type RAGIngestResponse struct {
	// DocumentID is the ingested document's identifier.
	DocumentID string `json:"document_id"`

	// ChunkCount is the number of chunks created from the document.
	ChunkCount int `json:"chunk_count"`

	// Namespace is the vector namespace where embeddings were stored.
	Namespace string `json:"namespace"`
}

// RAGQueryRequest is the transport-layer request for a RAG retrieval query.
type RAGQueryRequest struct {
	// Query is the natural-language question or search text.
	Query string `json:"query"`

	// Namespace is the vector namespace to search.
	Namespace string `json:"namespace"`

	// TopK is the maximum number of results to return.
	TopK int `json:"top_k,omitempty"`

	// Model is the embedding model name.
	Model string `json:"model,omitempty"`

	// Provider is the registered embedding provider name.
	Provider string `json:"provider,omitempty"`

	// Filter restricts results to vectors matching all key-value pairs.
	Filter map[string]string `json:"filter,omitempty"`
}

// RAGQueryResult is a single retrieved passage in a query response.
type RAGQueryResult struct {
	// ChunkText is the text content of the retrieved passage.
	ChunkText string `json:"chunk_text"`

	// Score is the similarity score (lower = more relevant for distance metrics).
	Score float32 `json:"score"`

	// DocumentID is the source document's identifier.
	DocumentID string `json:"document_id"`

	// ChunkIndex is the zero-based ordinal of the chunk within its document.
	ChunkIndex int `json:"chunk_index"`

	// Metadata contains key-value attributes stored with the chunk.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// RAGQueryResponse is the transport-layer response for a RAG retrieval query.
type RAGQueryResponse struct {
	// Results contains the retrieved passages ordered by relevance.
	Results []*RAGQueryResult `json:"results"`
}

// RAGDeleteDocumentRequest is the transport-layer request to remove a document
// and all its chunks from the RAG pipeline.
type RAGDeleteDocumentRequest struct {
	// DocumentID is the document to remove.
	DocumentID string `json:"document_id"`

	// Namespace is the vector namespace where the document's vectors are stored.
	Namespace string `json:"namespace"`
}

// RAGDeleteDocumentResponse is the transport-layer response after deletion.
type RAGDeleteDocumentResponse struct {
	// DocumentID is the identifier of the deleted document.
	DocumentID string `json:"document_id"`

	// ChunksRemoved is the number of chunk vectors and KV entries deleted.
	ChunksRemoved int `json:"chunks_removed"`
}

// RAGListDocumentsRequest is the transport-layer request to list ingested
// documents.
type RAGListDocumentsRequest struct {
	// Namespace filters the listing.
	Namespace string `json:"namespace"`

	// Limit caps the number of returned documents.
	Limit int `json:"limit,omitempty"`

	// Offset is the zero-based pagination offset.
	Offset int `json:"offset,omitempty"`
}

// RAGDocumentInfo is metadata about a single ingested document.
type RAGDocumentInfo struct {
	// ID is the document's unique identifier.
	ID string `json:"id"`

	// Namespace is the vector namespace for the document's embeddings.
	Namespace string `json:"namespace"`

	// ChunkCount is the number of chunks the document was split into.
	ChunkCount int `json:"chunk_count"`

	// Metadata holds key-value attributes stored with the document.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// RAGListDocumentsResponse is the transport-layer response for listing.
type RAGListDocumentsResponse struct {
	// Documents is the page of document metadata.
	Documents []*RAGDocumentInfo `json:"documents"`

	// Total is the total number of documents in the namespace.
	Total int `json:"total"`
}
