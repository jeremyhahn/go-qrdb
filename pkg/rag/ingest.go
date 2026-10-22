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
	"slices"
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/rag/chunker"
)

// chunkKVKey returns the KV key used to store chunk text.
// Format: rag:{namespace}:{doc_id}:{chunk_idx}.
func chunkKVKey(namespace, docID string, chunkIdx int) string {
	return chunkKVPrefix(namespace, docID) + strconv.Itoa(chunkIdx)
}

// chunkKVPrefix returns the part of every chunk key of the document before
// the chunk index: rag:{namespace}:{doc_id}:.
func chunkKVPrefix(namespace, docID string) string {
	return "rag:" + namespace + ":" + docID + ":"
}

// docMetaKVKey returns the KV key used to store document metadata.
// Format: rag:{namespace}:{doc_id}:meta.
func docMetaKVKey(namespace, docID string) string {
	return "rag:" + namespace + ":" + docID + ":meta"
}

// chunkVectorID produces a stable uint64 vector ID from namespace, docID,
// and chunk index using FNV-64a hashing.
func chunkVectorID(namespace, docID string, chunkIdx int) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(namespace))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(docID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.Itoa(chunkIdx)))
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

// staleChunks returns, in ascending order, the indices at or beyond keep of
// the chunks stored for the document: the chunks a re-ingest that produces
// keep chunks leaves behind. A key counts only when its suffix is exactly the
// decimal index chunkKVKey writes, so another document whose ID extends this
// one is never matched.
func (ing *Ingestor) staleChunks(ctx context.Context, namespace, docID string, keep int) ([]int, error) {
	prefix := chunkKVPrefix(namespace, docID)
	stored, err := ing.kvStore.Scan(ctx, prefix)
	if err != nil {
		return nil, &StoreError{Op: "list stored chunks", Cause: err}
	}
	var stale []int
	for key := range stored {
		suffix, ok := strings.CutPrefix(key, prefix)
		if !ok {
			continue
		}
		idx, err := strconv.Atoi(suffix)
		if err != nil || idx < keep || strconv.Itoa(idx) != suffix {
			continue
		}
		stale = append(stale, idx)
	}
	slices.Sort(stale)
	return stale, nil
}

// removeChunk deletes one chunk's vector and then its text. The vector goes
// first, so a failure part way leaves the text, and the next re-ingest or
// removal of the document finds the chunk again and retries.
func (ing *Ingestor) removeChunk(ctx context.Context, namespace, docID string, idx int) error {
	if err := ing.vectorStore.DeleteVector(ctx, namespace, chunkVectorID(namespace, docID, idx)); err != nil {
		return &StoreError{Op: "remove chunk vector", Cause: err}
	}
	if err := ing.kvStore.Delete(ctx, chunkKVKey(namespace, docID, idx)); err != nil {
		return &StoreError{Op: "remove chunk text", Cause: err}
	}
	return nil
}

// RemoveChunks removes every chunk stored for the document, each one's vector
// and then its text, and leaves the document record to the caller, who
// removes it only once RemoveChunks has succeeded. The chunk text and the
// vectors live in separate stores with no shared transaction, so the removal
// is retryable rather than atomic: a failure returns a StoreError, and the
// next call lists the chunks whose text remains and removes them again.
func (ing *Ingestor) RemoveChunks(ctx context.Context, namespace, docID string) error {
	chunks, err := ing.staleChunks(ctx, namespace, docID, 0)
	if err != nil {
		return err
	}
	for _, idx := range chunks {
		if err := ing.removeChunk(ctx, namespace, docID, idx); err != nil {
			return err
		}
	}
	return nil
}

// Ingest validates the document, chunks it, embeds the chunks, stores the
// embedding vectors, and persists the chunk text and document metadata in the
// KV store. Returns IngestResponse with the number of chunks created.
//
// Ingesting under a document ID already in the namespace replaces that
// document's whole chunk set: chunks are overwritten by index, and every
// chunk the previous version had beyond the new count is removed, vector and
// text, before the document record is rewritten. The two stores are separate
// services with no shared transaction, so the replacement is not atomic; it
// is retryable, because the document record is the last write.
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

	// Ensure the target namespace exists under the caller's tenant before
	// writing any vectors. The dimension is taken from the first embedding.
	// This is idempotent: if the namespace already exists the call succeeds.
	if len(embedResp.Embeddings) > 0 {
		dim := len(embedResp.Embeddings[0])
		if err := ing.vectorStore.EnsureNamespace(ctx, req.Namespace, dim, "cosine"); err != nil {
			return nil, &EmbeddingError{Cause: err}
		}
	}

	// Find the chunks a previous version of the document left beyond the
	// new count before anything is written, so a failed listing changes
	// nothing.
	stale, err := ing.staleChunks(ctx, req.Namespace, req.Document.ID, len(chunks))
	if err != nil {
		return nil, err
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
			return nil, &StoreError{Op: "store chunk text", Cause: err}
		}
	}

	// Remove the previous version's chunks beyond the new count. The
	// document record is written last: until it is, a retry of this
	// ingest lists the same stale chunks and removes them again.
	for _, idx := range stale {
		if err := ing.removeChunk(ctx, req.Namespace, req.Document.ID, idx); err != nil {
			return nil, err
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
		return nil, &StoreError{Op: "encode document record", Cause: err}
	}
	if err = ing.kvStore.Put(ctx, docMetaKVKey(req.Namespace, req.Document.ID), docJSON); err != nil {
		return nil, &StoreError{Op: "store document record", Cause: err}
	}

	return &IngestResponse{
		DocumentID: req.Document.ID,
		ChunkCount: len(chunks),
		Namespace:  req.Namespace,
	}, nil
}
