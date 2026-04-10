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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/rag/chunker"
)

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestIngestor_Success(t *testing.T) {
	embProvider := newMockEmbProvider(nil)
	vecStore := newMockVecStore(nil, nil)
	kvStore := newMockKVStore()

	ing := NewIngestor(embProvider, vecStore, kvStore, 0)
	resp, err := ing.Ingest(context.Background(), &IngestRequest{
		Document:  &Document{ID: "doc1", Text: "Hello world. This is a test document for RAG ingestion."},
		Namespace: "test-ns",
	})
	require.NoError(t, err)
	assert.Equal(t, "doc1", resp.DocumentID)
	assert.Equal(t, "test-ns", resp.Namespace)
	assert.GreaterOrEqual(t, resp.ChunkCount, 1)
}

func TestIngestor_EmptyDocument(t *testing.T) {
	ing := NewIngestor(newMockEmbProvider(nil), newMockVecStore(nil, nil), newMockKVStore(), 0)
	_, err := ing.Ingest(context.Background(), &IngestRequest{
		Document:  &Document{ID: "doc1", Text: ""},
		Namespace: "ns",
	})
	require.Error(t, err)
	assert.IsType(t, &ChunkingError{}, err)
}

func TestIngestor_NilDocument(t *testing.T) {
	ing := NewIngestor(newMockEmbProvider(nil), newMockVecStore(nil, nil), newMockKVStore(), 0)
	_, err := ing.Ingest(context.Background(), &IngestRequest{
		Document:  nil,
		Namespace: "ns",
	})
	require.Error(t, err)
	assert.IsType(t, &ChunkingError{}, err)
}

func TestIngestor_DocumentTooLarge(t *testing.T) {
	ing := NewIngestor(newMockEmbProvider(nil), newMockVecStore(nil, nil), newMockKVStore(), 10)
	_, err := ing.Ingest(context.Background(), &IngestRequest{
		Document:  &Document{ID: "doc1", Text: "This text is longer than 10 bytes."},
		Namespace: "ns",
	})
	require.Error(t, err)
	assert.IsType(t, &DocumentTooLargeError{}, err)
}

func TestIngestor_EmbeddingError(t *testing.T) {
	embProvider := newMockEmbProvider(errors.New("provider unavailable"))
	ing := NewIngestor(embProvider, newMockVecStore(nil, nil), newMockKVStore(), 0)
	_, err := ing.Ingest(context.Background(), &IngestRequest{
		Document:  &Document{ID: "doc1", Text: "Some text to embed."},
		Namespace: "ns",
	})
	require.Error(t, err)
	var embedErr *EmbeddingError
	assert.True(t, errors.As(err, &embedErr))
}

func TestIngestor_VectorUpsertError(t *testing.T) {
	vecStore := newMockVecStore(errors.New("vector store failure"), nil)
	ing := NewIngestor(newMockEmbProvider(nil), vecStore, newMockKVStore(), 0)
	_, err := ing.Ingest(context.Background(), &IngestRequest{
		Document:  &Document{ID: "doc1", Text: "Text that will fail vector upsert."},
		Namespace: "ns",
	})
	require.Error(t, err)
	assert.IsType(t, &EmbeddingError{}, err)
}

func TestIngestor_ChunkConfig(t *testing.T) {
	var capturedCount int
	embProvider := newMockEmbProviderWithFn(func(ctx context.Context, req *BatchEmbedRequest) (*BatchEmbedResponse, error) {
		capturedCount = len(req.Texts)
		embs := make([][]float32, len(req.Texts))
		for i := range embs {
			embs[i] = []float32{float32(i), 0.2}
		}
		return &BatchEmbedResponse{Embeddings: embs}, nil
	})

	ing := NewIngestor(embProvider, newMockVecStore(nil, nil), newMockKVStore(), 0)
	resp, err := ing.Ingest(context.Background(), &IngestRequest{
		Document:    &Document{ID: "doc1", Text: "AAAAABBBBBCCCCCDDDDDEEEEE"},
		Namespace:   "ns",
		ChunkConfig: &chunker.Config{Strategy: "fixed", ChunkSize: 5, Overlap: 0, MinSize: 0},
	})
	require.NoError(t, err)
	assert.Equal(t, 5, resp.ChunkCount)
	assert.Equal(t, 5, capturedCount)
}

func TestIngestor_InvalidNamespace(t *testing.T) {
	ing := NewIngestor(newMockEmbProvider(nil), newMockVecStore(nil, nil), newMockKVStore(), 0)
	_, err := ing.Ingest(context.Background(), &IngestRequest{
		Document:  &Document{ID: "doc1", Text: "text"},
		Namespace: "",
	})
	require.Error(t, err)
	assert.IsType(t, &InvalidNamespaceError{}, err)
}
