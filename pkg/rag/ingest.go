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
	"encoding/json"
	"hash/fnv"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/rag/chunker"
)

// chunkKVKey returns the KV key used to store chunk text.
// Format: rag:{namespace}:{doc_id}:{chunk_idx}
func chunkKVKey(namespace, docID string, chunkIdx int) string {
	return "rag:" + namespace + ":" + docID + ":" + strconv.Itoa(chunkIdx)
}

// docMetaKVKey returns the KV key used to store document metadata.
// Format: rag:{namespace}:{doc_id}:meta
func docMetaKVKey(namespace, docID string) string {
	return "rag:" + namespace + ":" + docID + ":meta"
}

// chunkVectorID produces a stable uint64 vector ID from namespace, docID,
// and chunk index using FNV-64a hashing.
func chunkVectorID(namespace, docID string, chunkIdx int) uint64 {
	h := fnv.New64a()
	h.Write([]byte(namespace))
	h.Write([]byte{0})
	h.Write([]byte(docID))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(chunkIdx)))
	return h.Sum64()
}

// Ingestor implements the document ingestion pipeline: chunk → embed → store.
type Ingestor struct {
	embProvider EmbedProvider
	vectorStore VectorStore
	kvStore     KVStore
	maxDocSize  int64
}

// NewIngestor creates an Ingestor backed by the supplied provider instances.
// maxDocSize is the maximum document byte length (0 means unlimited).
func NewIngestor(
	embProvider EmbedProvider,
	vectorStore VectorStore,
	kvStore KVStore,
	maxDocSize int64,
) *Ingestor {
	return &Ingestor{
		embProvider: embProvider,
		vectorStore: vectorStore,
		kvStore:     kvStore,
		maxDocSize:  maxDocSize,
	}
}

// Ingest validates the document, chunks it, embeds the chunks, stores the
// embedding vectors, and persists the chunk text and document metadata in the
// KV store. Returns IngestResponse with the number of chunks created.
func (ing *Ingestor) Ingest(ctx context.Context, req *IngestRequest) (*IngestResponse, error) {
	if req.Document == nil || req.Document.Text == "" {
		return nil, &ChunkingError{Cause: &chunker.EmptyTextError{}}
	}
	if ing.maxDocSize > 0 && int64(len(req.Document.Text)) > ing.maxDocSize {
		return nil, &DocumentTooLargeError{
			Size:    int64(len(req.Document.Text)),
			MaxSize: ing.maxDocSize,
		}
	}
	if req.Namespace == "" {
		return nil, &InvalidNamespaceError{Namespace: ""}
	}

	// Build the chunker from request config or defaults.
	cfg := req.ChunkConfig
	if cfg == nil {
		cfg = chunker.DefaultConfig()
	}
	ch, err := chunker.New(cfg)
	if err != nil {
		return nil, &ChunkingError{Cause: err}
	}

	chunks, err := ch.Chunk(req.Document.Text)
	if err != nil {
		return nil, &ChunkingError{Cause: err}
	}

	// Collect chunk texts for batch embedding.
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}

	// Embed all chunks in one batch call.
	embedResp, err := ing.embProvider.BatchEmbed(ctx, &BatchEmbedRequest{
		Texts:    texts,
		Model:    req.Model,
		Provider: req.Provider,
	})
	if err != nil {
		return nil, &EmbeddingError{Cause: err}
	}

	// Store each chunk's vector and text.
	for i, c := range chunks {
		vecID := chunkVectorID(req.Namespace, req.Document.ID, i)

		meta := map[string]interface{}{
			"doc_id":      req.Document.ID,
			"chunk_index": strconv.Itoa(i),
			"namespace":   req.Namespace,
		}
		for k, v := range req.Document.Metadata {
			meta[k] = v
		}
		for k, v := range c.Metadata {
			meta[k] = v
		}

		_, err = ing.vectorStore.UpsertVector(ctx, &UpsertVecRequest{
			Namespace: req.Namespace,
			ID:        vecID,
			Vector:    embedResp.Embeddings[i],
			Metadata:  meta,
		})
		if err != nil {
			return nil, &EmbeddingError{Cause: err}
		}

		// Persist chunk text in the KV store.
		if err = ing.kvStore.Put(ctx, chunkKVKey(req.Namespace, req.Document.ID, i), []byte(c.Text)); err != nil {
			return nil, &ChunkingError{Cause: err}
		}
	}

	// Persist document metadata.
	docInfo := &DocumentInfo{
		ID:         req.Document.ID,
		Namespace:  req.Namespace,
		ChunkCount: len(chunks),
		Metadata:   req.Document.Metadata,
	}
	docJSON, err := json.Marshal(docInfo)
	if err != nil {
		return nil, &ChunkingError{Cause: err}
	}
	if err = ing.kvStore.Put(ctx, docMetaKVKey(req.Namespace, req.Document.ID), docJSON); err != nil {
		return nil, &ChunkingError{Cause: err}
	}

	return &IngestResponse{
		DocumentID: req.Document.ID,
		ChunkCount: len(chunks),
		Namespace:  req.Namespace,
	}, nil
}
