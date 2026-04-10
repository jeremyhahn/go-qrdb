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

// Package rag provides the Retrieval-Augmented Generation pipeline for QRDB.
// Documents are chunked, embedded, and stored as vectors; queries are embedded
// and used to retrieve the most semantically similar passages.
package rag

import "github.com/jeremyhahn/go-qrdb/pkg/rag/chunker"

// Document represents a document to ingest.
type Document struct {
	// ID is the unique document identifier.
	ID string

	// Text is the full document content.
	Text string

	// Metadata holds optional key-value attributes for the document.
	Metadata map[string]string
}

// IngestRequest is the request to ingest a document into the RAG pipeline.
type IngestRequest struct {
	// Document is the document to ingest.
	Document *Document

	// Namespace is the vector namespace for this document's embeddings.
	Namespace string

	// Model is the embedding model to use. Empty uses the provider default.
	Model string

	// Provider is the registered embedding provider name. Empty uses the default.
	Provider string

	// ChunkConfig overrides the default chunking configuration.
	// Nil applies DefaultConfig().
	ChunkConfig *chunker.Config
}

// IngestResponse is the response from document ingestion.
type IngestResponse struct {
	// DocumentID is the ingested document's identifier.
	DocumentID string

	// ChunkCount is the number of chunks created from the document.
	ChunkCount int

	// Namespace is the vector namespace where embeddings were stored.
	Namespace string
}

// RetrieveRequest is a query for relevant passages.
type RetrieveRequest struct {
	// Query is the natural-language question or search text.
	Query string

	// Namespace is the vector namespace to search.
	Namespace string

	// TopK is the maximum number of results to return.
	TopK int

	// Model is the embedding model to use for the query.
	Model string

	// Provider is the registered embedding provider name.
	Provider string

	// Filter restricts results to vectors whose metadata matches all provided
	// key-value pairs.
	Filter map[string]string
}

// RetrieveResponse contains retrieved passages.
type RetrieveResponse struct {
	// Results is the list of retrieved passages ordered by relevance.
	Results []*RetrievalResult
}

// RetrievalResult is a single retrieved passage.
type RetrievalResult struct {
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

// DeleteDocumentRequest removes a document and all its chunks from the
// RAG pipeline.
type DeleteDocumentRequest struct {
	// DocumentID is the document to remove.
	DocumentID string

	// Namespace is the vector namespace where the document's vectors are stored.
	Namespace string
}

// DeleteDocumentResponse confirms deletion of a document.
type DeleteDocumentResponse struct {
	// DocumentID is the identifier of the deleted document.
	DocumentID string

	// ChunksRemoved is the number of chunks (and vectors) deleted.
	ChunksRemoved int
}

// ListDocumentsRequest lists metadata about ingested documents.
type ListDocumentsRequest struct {
	// Namespace filters the listing to a specific namespace.
	Namespace string

	// Limit caps the number of returned documents. Zero means no limit.
	Limit int

	// Offset is the zero-based pagination offset.
	Offset int
}

// ListDocumentsResponse contains document metadata.
type ListDocumentsResponse struct {
	// Documents is the page of document metadata.
	Documents []*DocumentInfo

	// Total is the total number of documents in the namespace.
	Total int
}

// DocumentInfo is metadata about an ingested document.
type DocumentInfo struct {
	// ID is the document's unique identifier.
	ID string

	// Namespace is the vector namespace where the document's embeddings are stored.
	Namespace string

	// ChunkCount is the number of chunks the document was split into.
	ChunkCount int

	// Metadata holds key-value attributes stored with the document.
	Metadata map[string]string
}
