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

package rag

import (
	"context"
	"fmt"
	"strconv"
)

// defaultTopK is the number of results returned when TopK is not set.
const defaultTopK = 5

// Retriever implements the retrieval leg of the RAG pipeline: embed query →
// vector search → fetch chunk text → return ranked results.
type Retriever struct {
	embProvider EmbedProvider
	vectorStore VectorStore
	kvStore     KVStore
}

// NewRetriever creates a Retriever backed by the supplied provider instances.
func NewRetriever(
	embProvider EmbedProvider,
	vectorStore VectorStore,
	kvStore KVStore,
) *Retriever {
	return &Retriever{
		embProvider: embProvider,
		vectorStore: vectorStore,
		kvStore:     kvStore,
	}
}

// Retrieve embeds the query, performs a similarity search, and hydrates
// each result with its stored chunk text. Results are ordered by ascending
// distance (most relevant first). Returns EmptyQueryError when the query
// string is empty.
func (r *Retriever) Retrieve(ctx context.Context, req *RetrieveRequest) (*RetrieveResponse, error) {
	if req.Query == "" {
		return nil, &EmptyQueryError{}
	}
	if req.Namespace == "" {
		return nil, &InvalidNamespaceError{Namespace: ""}
	}

	topK := req.TopK
	if topK <= 0 {
		topK = defaultTopK
	}

	// Embed the query text.
	embedResp, err := r.embProvider.Embed(ctx, &EmbedRequest{
		Texts:    []string{req.Query},
		Model:    req.Model,
		Provider: req.Provider,
	})
	if err != nil {
		return nil, &EmbeddingError{Cause: err}
	}
	if len(embedResp.Embeddings) == 0 {
		return nil, &EmbeddingError{Cause: &EmptyEmbeddingsError{}}
	}

	// Build optional metadata filter.
	var metadataFilter map[string]interface{}
	if len(req.Filter) > 0 {
		metadataFilter = make(map[string]interface{}, len(req.Filter))
		for k, v := range req.Filter {
			metadataFilter[k] = v
		}
	}

	// Search for similar vectors.
	searchResp, err := r.vectorStore.SearchSimilar(ctx, &SearchVecRequest{
		Namespace:       req.Namespace,
		QueryVector:     embedResp.Embeddings[0],
		K:               topK,
		IncludeMetadata: true,
		MetadataFilter:  metadataFilter,
	})
	if err != nil {
		return nil, &RetrievalError{Cause: err}
	}

	results := make([]*RetrievalResult, 0, len(searchResp.Results))
	for _, sr := range searchResp.Results {
		docID, chunkIdx, meta := extractChunkMeta(sr.Metadata)

		// Fetch chunk text from the KV store.
		chunkText, kvErr := r.kvStore.Get(ctx, chunkKVKey(req.Namespace, docID, chunkIdx))
		if kvErr != nil {
			// Skip chunks whose text has been evicted or was never stored.
			continue
		}

		results = append(results, &RetrievalResult{
			ChunkText:  string(chunkText),
			Score:      sr.Distance,
			DocumentID: docID,
			ChunkIndex: chunkIdx,
			Metadata:   meta,
		})
	}

	return &RetrieveResponse{Results: results}, nil
}

// extractChunkMeta parses the doc_id and chunk_index fields from a vector's
// metadata map. Returns empty strings / zero index on parse failure.
func extractChunkMeta(meta map[string]interface{}) (docID string, chunkIdx int, filtered map[string]string) {
	filtered = make(map[string]string, len(meta))
	for k, v := range meta {
		filtered[k] = fmt.Sprintf("%v", v)
	}

	if v, ok := meta["doc_id"]; ok {
		docID = fmt.Sprintf("%v", v)
	}
	if v, ok := meta["chunk_index"]; ok {
		s := fmt.Sprintf("%v", v)
		if n, err := strconv.Atoi(s); err == nil {
			chunkIdx = n
		}
	}
	return docID, chunkIdx, filtered
}
