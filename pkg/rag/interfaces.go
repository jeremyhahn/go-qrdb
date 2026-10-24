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

// Package rag provides the Retrieval-Augmented Generation (RAG) pipeline.
// This file defines the narrow dependency interfaces used by the package to
// avoid a direct import of pkg/service and the resulting import cycle.
package rag

import "context"

// ---------------------------------------------------------------------------
// Embedding interface and types
// ---------------------------------------------------------------------------

// BatchEmbedRequest is the input to EmbedProvider.BatchEmbed.
type BatchEmbedRequest struct {
	// Texts is the complete list of strings to embed.
	Texts []string
	// Model selects the embedding model. Empty means provider default.
	Model string
	// Provider selects the registered provider. Empty means default.
	Provider string
}

// BatchEmbedResponse contains the aggregated results of a batch embedding run.
type BatchEmbedResponse struct {
	// Embeddings contains one vector per input text, in the same order as the
	// input slice.
	Embeddings [][]float32
}

// EmbedRequest is the input to EmbedProvider.Embed.
type EmbedRequest struct {
	// Texts is the list of strings to embed. Must not be empty.
	Texts []string
	// Model selects the embedding model. Empty means provider default.
	Model string
	// Provider selects the registered provider. Empty means default.
	Provider string
}

// EmbedResponse contains the results of a single embedding call.
type EmbedResponse struct {
	// Embeddings contains one vector per input text, in the same order as the
	// input slice.
	Embeddings [][]float32
}

// EmbedProvider generates vector embeddings for text.
// pkg/service.EmbeddingServicer satisfies this interface.
type EmbedProvider interface {
	// Embed generates embeddings for the texts in req.
	Embed(ctx context.Context, req *EmbedRequest) (*EmbedResponse, error)
	// BatchEmbed splits req.Texts into sub-batches and aggregates results.
	BatchEmbed(ctx context.Context, req *BatchEmbedRequest) (*BatchEmbedResponse, error)
}

// ---------------------------------------------------------------------------
// Vector interface and types
// ---------------------------------------------------------------------------

// UpsertVecRequest is the input to VectorStore.UpsertVector.
type UpsertVecRequest struct {
	// Namespace is the target vector namespace.
	Namespace string
	// ID is the unique vector identifier.
	ID uint64
	// Vector contains the embedding values.
	Vector []float32
	// Metadata contains user-defined attributes stored alongside the vector.
	Metadata map[string]interface{}
}

// UpsertVecResponse is the result of a vector upsert.
type UpsertVecResponse struct {
	// Success is true when the upsert succeeded.
	Success bool
}

// VecSearchResult is a single result returned from a similarity search.
type VecSearchResult struct {
	// ID is the vector's unique identifier.
	ID uint64
	// Distance is the distance metric value (lower is more similar).
	Distance float32
	// Metadata contains user-defined attributes stored with the vector.
	Metadata map[string]interface{}
}

// SearchVecRequest is the input to VectorStore.SearchSimilar.
type SearchVecRequest struct {
	// Namespace is the target vector namespace.
	Namespace string
	// QueryVector is the search query embedding.
	QueryVector []float32
	// K is the number of nearest neighbours to return.
	K int
	// IncludeMetadata requests metadata in results.
	IncludeMetadata bool
	// MetadataFilter restricts results to vectors matching all key-value pairs.
	MetadataFilter map[string]interface{}
}

// SearchVecResponse contains similarity search results.
type SearchVecResponse struct {
	// Results contains the matching vectors ordered by ascending distance.
	Results []*VecSearchResult
}

// VectorStore persists and retrieves vectors.
// pkg/service.VectorService satisfies this interface.
type VectorStore interface {
	// UpsertVector inserts or updates a vector with optional metadata.
	UpsertVector(ctx context.Context, req *UpsertVecRequest) (*UpsertVecResponse, error)
	// SearchSimilar finds vectors nearest to the query vector.
	SearchSimilar(ctx context.Context, req *SearchVecRequest) (*SearchVecResponse, error)
	// EnsureNamespace creates the namespace if it does not already exist.
	// It is idempotent: if the namespace already exists the call succeeds
	// without error. dimension is the vector dimensionality; metric is the
	// distance metric (e.g. "cosine", "L2"). This must be called before the
	// first UpsertVector on a new namespace.
	EnsureNamespace(ctx context.Context, name string, dimension int, metric string) error
	// DeleteVector removes the vector id from namespace. Removing a vector
	// that is not there succeeds, so a removal can be retried.
	DeleteVector(ctx context.Context, namespace string, id uint64) error
}

// ---------------------------------------------------------------------------
// KV interface
// ---------------------------------------------------------------------------

// KVStore provides simple key-value persistence.
// pkg/service.KVServicer satisfies this interface.
type KVStore interface {
	// Put stores value at key.
	Put(ctx context.Context, key string, value []byte) error
	// Get retrieves the value stored at key.
	Get(ctx context.Context, key string) ([]byte, error)
	// Delete removes the value at key.
	Delete(ctx context.Context, key string) error
	// Scan returns all key-value pairs whose keys share the given prefix.
	Scan(ctx context.Context, prefix string) (map[string][]byte, error)
}
