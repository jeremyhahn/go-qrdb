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

package service

// ChunkConfig configures the chunking behavior for the RAG pipeline.
// This mirrors pkg/rag/chunker.Config so the service layer does not
// import the build-tag-guarded package. A zero field is unset, as it is on
// every transport's wire, and takes the node's default from the rag
// configuration.
type ChunkConfig struct {
	// Strategy selects the chunking algorithm: "fixed", "sentence", "paragraph".
	Strategy string

	// ChunkSize is the target chunk size in characters.
	ChunkSize int

	// Overlap is the number of overlapping characters between adjacent chunks.
	Overlap int

	// MinSize is the minimum chunk size; chunks smaller than this are merged
	// with the previous chunk.
	MinSize int
}

// IngestDocumentRequest is the service-layer request to ingest a document into
// the RAG pipeline.
type IngestDocumentRequest struct {
	// DocumentID is the unique identifier for the document.
	DocumentID string

	// Text is the document content to chunk and embed.
	Text string

	// Namespace is the vector namespace where embeddings will be stored.
	Namespace string

	// Model is the embedding model to use. Empty uses the provider default.
	Model string

	// Provider is the registered embedding provider name. Empty uses the default.
	Provider string

	// ChunkConfig overrides the default chunking configuration.
	ChunkConfig *ChunkConfig

	// Metadata holds optional key-value attributes for the document.
	Metadata map[string]string
}

// IngestDocumentResponse is the service-layer response from document ingestion.
type IngestDocumentResponse struct {
	// DocumentID is the ingested document's identifier.
	DocumentID string

	// ChunkCount is the number of chunks created from the document.
	ChunkCount int

	// Namespace is the vector namespace where embeddings were stored.
	Namespace string
}

// RAGQueryRequest is the service-layer request for a RAG retrieval query.
type RAGQueryRequest struct {
	// Query is the natural-language question or search text.
	Query string

	// Namespace is the vector namespace to search.
	Namespace string

	// TopK is the maximum number of results to return. Zero uses the default.
	TopK int

	// Model is the embedding model to use for the query.
	Model string

	// Provider is the registered embedding provider name.
	Provider string

	// Filter restricts results to vectors whose metadata matches all key-value pairs.
	Filter map[string]string
}

// RAGQueryResult is a single retrieved passage in a RAG query response.
type RAGQueryResult struct {
	// ChunkText is the text content of the retrieved passage.
	ChunkText string

	// Score is the similarity score (lower = more relevant for distance metrics).
	Score float32

	// DocumentID is the source document's identifier.
	DocumentID string

	// ChunkIndex is the zero-based ordinal of the chunk within its document.
	ChunkIndex int

	// Metadata contains key-value attributes stored with the chunk.
	Metadata map[string]string
}

// RAGQueryResponse is the service-layer response for a RAG retrieval query.
type RAGQueryResponse struct {
	// Results contains the retrieved passages ordered by relevance.
	Results []*RAGQueryResult
}

// DeleteRAGDocumentRequest is the service-layer request to remove a document
// and all its chunks from the RAG pipeline.
type DeleteRAGDocumentRequest struct {
	// DocumentID is the document to remove.
	DocumentID string

	// Namespace is the vector namespace where the document's vectors are stored.
	Namespace string
}

// DeleteRAGDocumentResponse is the service-layer response after document deletion.
type DeleteRAGDocumentResponse struct {
	// DocumentID is the identifier of the deleted document.
	DocumentID string

	// ChunksRemoved is the number of chunk vectors and KV entries deleted.
	ChunksRemoved int
}

// ListRAGDocumentsRequest is the service-layer request to list ingested
// documents within a namespace.
type ListRAGDocumentsRequest struct {
	// Namespace filters the listing.
	Namespace string

	// Limit caps the number of returned documents. Zero means no limit.
	Limit int

	// Offset is the zero-based pagination offset.
	Offset int
}

// ListRAGDocumentsResponse is the service-layer response for listing documents.
type ListRAGDocumentsResponse struct {
	// Documents is the page of document metadata.
	Documents []*RAGDocumentInfo

	// Total is the total number of documents in the namespace.
	Total int
}

// RAGDocumentInfo is metadata about an ingested document.
type RAGDocumentInfo struct {
	// ID is the document's unique identifier.
	ID string

	// Namespace is the vector namespace for the document's embeddings.
	Namespace string

	// ChunkCount is the number of chunks the document was split into.
	ChunkCount int

	// Metadata holds key-value attributes stored with the document.
	Metadata map[string]string
}
